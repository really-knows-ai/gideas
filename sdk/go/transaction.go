package flow

import (
	"context"
	"fmt"
	"regexp"
	"sync"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowmeta "github.com/foundry/flow/pkg/metadata"
)

// ErrTransactionRolledBack is returned when a method is called on a
// transaction that has already been rolled back.
var ErrTransactionRolledBack = fmt.Errorf("flow sdk: transaction has been rolled back")

// ErrTransactionCommitted is returned when a method is called on a
// transaction that has already been committed.
var ErrTransactionCommitted = fmt.Errorf("flow sdk: transaction has been committed")

// Transaction wraps a Cartographer transaction with an isolated graph context.
// The handle is safe to share across goroutines: the terminal/lifecycle
// fields (rolledBack, committed) are guarded by mu.
type Transaction struct {
	session    *session
	id         string
	mu         sync.Mutex // guards rolledBack, committed
	idTypeMap  *idTypeMap
	rolledBack bool
	committed  bool
}

// checkTerminal returns an error if the transaction has been rolled back or
// committed. Once terminal, the handle rejects every further operation
// locally: a committed transaction ID sent to the wire would otherwise fail
// server-side with NOT_FOUND (SPEC error-table row "Transaction not found":
// "already committed/rolled back"), and the R4 example's deferred
// `tx.Rollback()` after a successful `tx.Commit()` would surface a spurious
// discarded error.
func (tx *Transaction) checkTerminal() error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.rolledBack {
		return ErrTransactionRolledBack
	}
	if tx.committed {
		return ErrTransactionCommitted
	}
	return nil
}

// ---------------------------------------------------------------------------
// Read path methods (with transactionId injection)
// ---------------------------------------------------------------------------

// ListEntities lists entities within the transaction.
func (tx *Transaction) ListEntities(entityType string, opts ...ListEntitiesOption) (*EntityPage, error) {
	if err := tx.checkTerminal(); err != nil {
		return nil, err
	}

	cfg := &listEntitiesConfig{}
	for _, o := range opts {
		o(cfg)
	}

	req := &flowv1.ListEntitiesRequest{
		EntityType:    entityType,
		PageSize:      cfg.pageSize,
		PageToken:     cfg.pageToken,
		TransactionId: tx.id,
	}

	var resp *flowv1.ListEntitiesResponse
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = tx.session.Cartographer.ListEntities(ctx, req)
		return callErr
	}, "")
	if err != nil {
		return nil, err
	}

	entities := make([]Entity, 0, len(resp.GetEntities()))
	for _, e := range resp.GetEntities() {
		entities = append(entities, Entity{
			ID:         e.GetEntityId(),
			Type:       e.GetEntityType(),
			Properties: e.GetProperties(),
			Embedding:  e.GetEmbedding(),
		})
		tx.idTypeMap.store(e.GetEntityId(), e.GetEntityType())
	}

	return &EntityPage{
		Entities:      entities,
		NextPageToken: resp.GetNextPageToken(),
	}, nil
}

// ---------------------------------------------------------------------------
// Write path methods (with transactionId injection)
// ---------------------------------------------------------------------------

// CreateEntity creates an entity within the transaction.
func (tx *Transaction) CreateEntity(
	entityType string, id *string, properties map[string]string, embedding []float32,
) (*Entity, error) {
	if err := tx.checkTerminal(); err != nil {
		return nil, err
	}
	if err := validateEmbedding(embedding); err != nil {
		return nil, err
	}

	req := &flowv1.CreateEntityRequest{
		EntityType:    entityType,
		Properties:    properties,
		Embedding:     embedding,
		TransactionId: tx.id,
	}
	if id != nil {
		if err := validateUUIDOrEmpty(*id); err != nil {
			return nil, err
		}
		req.Id = *id
	}

	var resp *flowv1.CreateEntityResponse
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = tx.session.Cartographer.CreateEntity(ctx, req)
		return callErr
	}, "")
	if err != nil {
		return nil, err
	}

	entity := &Entity{
		ID:         resp.GetEntityId(),
		Type:       resp.GetEntityType(),
		Properties: resp.GetProperties(),
		Embedding:  resp.GetEmbedding(),
	}
	tx.idTypeMap.store(entity.ID, entity.Type)
	return entity, nil
}

// UpdateEntity partially updates an entity within the transaction.
func (tx *Transaction) UpdateEntity(id string, properties map[string]string, embedding []float32) (*Entity, error) {
	if err := tx.checkTerminal(); err != nil {
		return nil, err
	}
	if err := validateUUIDOrEmpty(id); err != nil {
		return nil, err
	}
	if err := validateEmbedding(embedding); err != nil {
		return nil, err
	}

	req := &flowv1.UpdateEntityRequest{
		Id:            id,
		Properties:    properties,
		Embedding:     embedding,
		TransactionId: tx.id,
	}

	entityType := tx.idTypeMap.resolveOrWildcard(id)

	var resp *flowv1.UpdateEntityResponse
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = tx.session.Cartographer.UpdateEntity(ctx, req)
		return callErr
	}, flowmeta.MetadataKeyEntityType, entityType)
	if err != nil {
		return nil, err
	}

	entity := &Entity{
		ID:         resp.GetEntityId(),
		Type:       resp.GetEntityType(),
		Properties: resp.GetProperties(),
		Embedding:  resp.GetEmbedding(),
	}
	if entity.Type != "" {
		tx.idTypeMap.store(entity.ID, entity.Type)
	}
	return entity, nil
}

// DeleteEntity deletes an entity within the transaction.
func (tx *Transaction) DeleteEntity(id string) (*Entity, error) {
	if err := tx.checkTerminal(); err != nil {
		return nil, err
	}
	if err := validateUUIDOrEmpty(id); err != nil {
		return nil, err
	}

	req := &flowv1.DeleteEntityRequest{
		Id:            id,
		TransactionId: tx.id,
	}

	entityType := tx.idTypeMap.resolveOrWildcard(id)

	var resp *flowv1.DeleteEntityResponse
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = tx.session.Cartographer.DeleteEntity(ctx, req)
		return callErr
	}, flowmeta.MetadataKeyEntityType, entityType)
	if err != nil {
		return nil, err
	}

	tx.idTypeMap.remove(id)
	return &Entity{
		ID:         resp.GetEntityId(),
		Type:       resp.GetEntityType(),
		Properties: resp.GetProperties(),
		Embedding:  resp.GetEmbedding(),
	}, nil
}

// CreateEdge creates a directed edge within the transaction.
func (tx *Transaction) CreateEdge(
	edgeType, fromEntityID, toEntityID string, properties map[string]string,
) (*Edge, error) {
	if err := tx.checkTerminal(); err != nil {
		return nil, err
	}
	if err := validateUUIDOrEmpty(fromEntityID); err != nil {
		return nil, err
	}
	if err := validateUUIDOrEmpty(toEntityID); err != nil {
		return nil, err
	}

	req := &flowv1.CreateEdgeRequest{
		EdgeType:      edgeType,
		FromEntityId:  fromEntityID,
		ToEntityId:    toEntityID,
		Properties:    properties,
		TransactionId: tx.id,
	}

	sourceType := tx.idTypeMap.resolveOrWildcard(fromEntityID)

	var resp *flowv1.CreateEdgeResponse
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = tx.session.Cartographer.CreateEdge(ctx, req)
		return callErr
	}, flowmeta.MetadataKeyEntityType, sourceType)
	if err != nil {
		return nil, err
	}

	return &Edge{
		ID:           resp.GetEdgeId(),
		Type:         resp.GetEdgeType(),
		FromEntityID: resp.GetFromEntityId(),
		ToEntityID:   resp.GetToEntityId(),
		Properties:   resp.GetProperties(),
	}, nil
}

// DeleteEdge deletes an edge within the transaction.
func (tx *Transaction) DeleteEdge(id string) (*Edge, error) {
	if err := tx.checkTerminal(); err != nil {
		return nil, err
	}
	if err := validateUUIDOrEmpty(id); err != nil {
		return nil, err
	}

	req := &flowv1.DeleteEdgeRequest{
		Id:            id,
		TransactionId: tx.id,
	}

	var resp *flowv1.DeleteEdgeResponse
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = tx.session.Cartographer.DeleteEdge(ctx, req)
		return callErr
	}, flowmeta.MetadataKeyEntityType, "*")
	if err != nil {
		return nil, err
	}

	return &Edge{
		ID:           resp.GetEdgeId(),
		Type:         resp.GetEdgeType(),
		FromEntityID: resp.GetFromEntityId(),
		ToEntityID:   resp.GetToEntityId(),
		Properties:   resp.GetProperties(),
	}, nil
}

// ---------------------------------------------------------------------------
// Client-side input validation helpers
// ---------------------------------------------------------------------------

// canonicalUUIDV4Re matches the canonical RFC4122 §3 UUID v4 string form:
// the 8-4-4-4-12 lowercase dashed form produced by the standard library's
// String() (SPEC:162). The Cartographer persists IDs verbatim as <id>.json
// file names, so a non-canonical spelling of an already valid UUID (uppercase
// hex, 32-char no-hyphen, braced {...}, urn:uuid:) would create a second
// entity for the same UUID and bypass the CreateEntity ALREADY_EXISTS check.
var canonicalUUIDV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// validateUUIDOrEmpty reports whether id is the canonical RFC4122 §3 UUID v4
// string form, mirroring the Cartographer store's uuidutil.Validate at the
// SDK's write-path boundary (SPEC:162; error-table row "Invalid entity or
// edge ID format"). An empty id passes (optional parameters); every other
// spelling — including non-canonical ones that parse as UUIDs — is rejected
// before it reaches the wire, matching the validateEmbedding client-side
// guard precedent.
func validateUUIDOrEmpty(id string) error {
	if id == "" {
		return nil
	}
	// The regex pins the canonical lowercase dashed form. The version nibble
	// (third group) must be 4 and the variant nibble (fourth group) must be
	// 8, 9, a, or b (RFC 4122) — mirroring uuidutil.Validate exactly.
	if !canonicalUUIDV4Re.MatchString(id) ||
		id[14] != '4' ||
		(id[19] != '8' && id[19] != '9' && id[19] != 'a' && id[19] != 'b') {
		return fmt.Errorf(
			"flow sdk: entity ID must be canonical lowercase dashed UUID v4 form " +
				"(e.g. 550e8400-e29b-41d4-a716-446655440000)")
	}
	return nil
}
