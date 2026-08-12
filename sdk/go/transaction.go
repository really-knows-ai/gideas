package flow

import (
	"context"
	"fmt"
	"regexp"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
)

// ErrTransactionRolledBack is returned when a method is called on a
// transaction that has already been rolled back.
var ErrTransactionRolledBack = fmt.Errorf("flow sdk: transaction has been rolled back")

// Transaction wraps a Cartographer transaction with an isolated graph context.
type Transaction struct {
	session    *session
	id         string
	timeout    time.Duration
	idTypeMap  *idTypeMap
	rolledBack bool
}

// checkRolled returns an error if the transaction has been rolled back.
func (tx *Transaction) checkRolled() error {
	if tx.rolledBack {
		return ErrTransactionRolledBack
	}
	return nil
}

// ID returns the transaction ID.
func (tx *Transaction) ID() string { return tx.id }

// Timeout returns the server-applied transaction timeout — the value the
// Cartographer granted in the applied_timeout of the BeginTransaction or
// ExtendTimeout response (SPEC R9/R2; no silent capping).
func (tx *Transaction) Timeout() time.Duration { return tx.timeout }

// ---------------------------------------------------------------------------
// Read path methods (with transactionId injection)
// ---------------------------------------------------------------------------

// ExecuteCypher executes a read-only Cypher query within the transaction.
func (tx *Transaction) ExecuteCypher(cypher string, params map[string]any) ([]map[string]any, error) {
	if err := tx.checkRolled(); err != nil {
		return nil, err
	}

	var paramsProto *structpb.Value
	if len(params) > 0 {
		s, err := structpb.NewStruct(params)
		if err != nil {
			return nil, fmt.Errorf("flow sdk: invalid params: %w", err)
		}
		paramsProto = structpb.NewStructValue(s)
	}

	req := &flowv1.ExecuteCypherRequest{
		Cypher:        cypher,
		Params:        paramsProto,
		TransactionId: tx.id,
	}

	// No entity-type capability metadata is attached (SPEC R3): the SDK
	// performs no Cypher parsing and cannot influence the authorization
	// decision. The Cartographer parses the statement server-side and checks
	// the caller's capabilities against each referenced type.
	var resp *flowv1.ExecuteCypherResponse
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = tx.session.Cartographer.ExecuteCypher(ctx, req)
		return callErr
	}, "")
	if err != nil {
		return nil, err
	}

	// Convert proto rows to []map[string]any. Each Row is one flat tuple of
	// string values in the order LadybugDB returned them; expose them
	// positionally as col_<N> (SPEC R2).
	rows := make([]map[string]any, 0, len(resp.GetRows()))
	for _, row := range resp.GetRows() {
		m := make(map[string]any)
		for i, v := range row.GetValues() {
			m[fmt.Sprintf("col_%d", i)] = v
		}
		rows = append(rows, m)
	}
	return rows, nil
}

// SearchNeighbors performs a vector similarity search within the transaction.
func (tx *Transaction) SearchNeighbors(embedding []float32, entityType string, topK int) ([]SearchResult, error) {
	if err := tx.checkRolled(); err != nil {
		return nil, err
	}
	if err := validateEmbedding(embedding); err != nil {
		return nil, err
	}

	req := &flowv1.SearchNeighborsRequest{
		Embedding:     embedding,
		EntityType:    entityType,
		TopK:          int32(topK),
		TransactionId: tx.id,
	}

	var resp *flowv1.SearchNeighborsResponse
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = tx.session.Cartographer.SearchNeighbors(ctx, req)
		return callErr
	}, "")
	if err != nil {
		return nil, err
	}

	results := make([]SearchResult, 0, len(resp.GetResults()))
	for _, r := range resp.GetResults() {
		results = append(results, SearchResult{
			ID:         r.GetEntityId(),
			Type:       r.GetEntityType(),
			Properties: r.GetProperties(),
			Similarity: r.GetScore(),
		})
		tx.idTypeMap.store(r.GetEntityId(), r.GetEntityType())
	}
	return results, nil
}

// FullTextSearch performs a full-text search within the transaction.
func (tx *Transaction) FullTextSearch(query, entityType string) ([]Entity, error) {
	if err := tx.checkRolled(); err != nil {
		return nil, err
	}

	req := &flowv1.FullTextSearchRequest{
		Query:         query,
		EntityType:    entityType,
		TransactionId: tx.id,
	}

	var resp *flowv1.FullTextSearchResponse
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = tx.session.Cartographer.FullTextSearch(ctx, req)
		return callErr
	}, "")
	if err != nil {
		return nil, err
	}

	results := make([]Entity, 0, len(resp.GetResults()))
	for _, e := range resp.GetResults() {
		results = append(results, Entity{
			ID:         e.GetEntityId(),
			Type:       e.GetEntityType(),
			Properties: e.GetProperties(),
			Embedding:  e.GetEmbedding(),
		})
		tx.idTypeMap.store(e.GetEntityId(), e.GetEntityType())
	}
	return results, nil
}

// ListEntities lists entities within the transaction.
func (tx *Transaction) ListEntities(entityType string, opts ...ListEntitiesOption) (*EntityPage, error) {
	if err := tx.checkRolled(); err != nil {
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
	if err := tx.checkRolled(); err != nil {
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
	if err := tx.checkRolled(); err != nil {
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
	}, "entity_type", entityType)
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
	if err := tx.checkRolled(); err != nil {
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
	}, "entity_type", entityType)
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
	if err := tx.checkRolled(); err != nil {
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
	}, "entity_type", sourceType)
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
	if err := tx.checkRolled(); err != nil {
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
	}, "entity_type", "*")
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
// Transaction lifecycle methods
// ---------------------------------------------------------------------------

// Diff returns the transaction's structured diff.
func (tx *Transaction) Diff() (*TransactionDiff, error) {
	if err := tx.checkRolled(); err != nil {
		return nil, err
	}

	var resp *flowv1.GetTransactionDiffResponse
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = tx.session.Cartographer.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{
			TransactionId: tx.id,
		})
		return callErr
	}, "")
	if err != nil {
		return nil, err
	}

	diff := &TransactionDiff{}

	for _, e := range resp.GetAddedEntities() {
		diff.AddedEntities = append(diff.AddedEntities, diffEntryToEntity(e))
	}
	for _, e := range resp.GetModifiedEntities() {
		diff.ModifiedEntities = append(diff.ModifiedEntities, diffEntryToEntity(e))
	}
	for _, e := range resp.GetDeletedEntities() {
		diff.DeletedEntities = append(diff.DeletedEntities, diffEntryToEntity(e))
	}
	for _, e := range resp.GetAddedEdges() {
		diff.AddedEdges = append(diff.AddedEdges, diffEntryToEdge(e))
	}
	for _, e := range resp.GetModifiedEdges() {
		diff.ModifiedEdges = append(diff.ModifiedEdges, diffEntryToEdge(e))
	}
	for _, e := range resp.GetDeletedEdges() {
		diff.DeletedEdges = append(diff.DeletedEdges, diffEntryToEdge(e))
	}

	return diff, nil
}

// Refresh re-hydrates the transaction from latest main.
func (tx *Transaction) Refresh() error {
	if err := tx.checkRolled(); err != nil {
		return err
	}
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		_, callErr := tx.session.Cartographer.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
			TransactionId: tx.id,
		})
		return callErr
	}, "")
	return err
}

// CommitOption configures a CommitTransaction call.
type CommitOption func(*commitConfig)

type commitConfig struct {
	ack bool
}

// WithAck requests synchronous push delivery (SPEC R10): the commit signals
// the sync worker to wake immediately and blocks until the sync cycle
// completes. A caller that hits the context deadline receives DEADLINE_EXCEEDED
// and the push flag stays set for the next cycle. A plain Commit returns
// immediately; the worker picks the push up on its next timer cycle.
func WithAck() CommitOption {
	return func(c *commitConfig) {
		c.ack = true
	}
}

// Commit commits the transaction back to main.
func (tx *Transaction) Commit(opts ...CommitOption) error {
	if err := tx.checkRolled(); err != nil {
		return err
	}

	cfg := &commitConfig{}
	for _, o := range opts {
		o(cfg)
	}

	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		_, callErr := tx.session.Cartographer.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
			TransactionId: tx.id,
			Ack:           cfg.ack,
		})
		return callErr
	}, "")
	return err
}

// Rollback discards the transaction branch.
func (tx *Transaction) Rollback() error {
	if tx.rolledBack {
		return nil // idempotent
	}
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		_, callErr := tx.session.Cartographer.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
			TransactionId: tx.id,
		})
		return callErr
	}, "")
	if err != nil {
		return err
	}
	tx.rolledBack = true
	return nil
}

// ExtendTimeout extends the transaction timeout. The duration must be positive;
// total lifetime (beginTime + all extensions) cannot exceed 7 days. The server
// rejects an over-limit extension (INVALID_ARGUMENT) rather than capping, and
// otherwise applies the requested duration verbatim; this returns the applied
// timeout the server granted (the requested duration, as no silent caps exist).
func (tx *Transaction) ExtendTimeout(d time.Duration) (time.Duration, error) {
	if err := tx.checkRolled(); err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("flow sdk: extend timeout duration must be positive")
	}

	var applied = d
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		resp, callErr := tx.session.Cartographer.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{
			TransactionId: tx.id,
			Duration:      durationpb.New(d),
		})
		if callErr != nil {
			return callErr
		}
		if resp.GetAppliedTimeout() != nil {
			applied = resp.GetAppliedTimeout().AsDuration()
		}
		return nil
	}, "")
	if err != nil {
		return 0, err
	}

	tx.timeout = applied
	return applied, nil
}

// ---------------------------------------------------------------------------
// Conversion helpers
// ---------------------------------------------------------------------------

func diffEntryToEntity(e *flowv1.DiffEntry) Entity {
	return Entity{
		ID:         e.GetId(),
		Type:       e.GetType(),
		Properties: e.GetProperties(),
		Embedding:  e.GetEmbedding(),
		Suspected:  e.GetSuspected(),
	}
}

func diffEntryToEdge(e *flowv1.DiffEntry) Edge {
	return Edge{
		ID:           e.GetId(),
		Type:         e.GetType(),
		FromEntityID: e.GetFromEntityId(),
		ToEntityID:   e.GetToEntityId(),
		Properties:   e.GetProperties(),
		Suspected:    e.GetSuspected(),
	}
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
