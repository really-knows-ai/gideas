package flow

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowmeta "github.com/foundry/flow/pkg/metadata"
	"google.golang.org/protobuf/types/known/durationpb"
)

// ErrTransactionRolledBack is returned when a method is called on a
// transaction that has already been rolled back.
var ErrTransactionRolledBack = fmt.Errorf("flow sdk: transaction has been rolled back")

// ErrTransactionCommitted is returned when a method is called on a
// transaction that has already been committed.
var ErrTransactionCommitted = fmt.Errorf("flow sdk: transaction has been committed")

// Transaction wraps a Cartographer transaction with an isolated graph context.
// The handle is safe to share across goroutines: the terminal/lifecycle
// fields (rolledBack, committed) and the server-granted appliedTimeout are
// guarded by mu.
type Transaction struct {
	session        *session
	id             string
	mu             sync.Mutex // guards rolledBack, committed, appliedTimeout
	idTypeMap      *idTypeMap
	rolledBack     bool
	committed      bool
	appliedTimeout time.Duration
}

// AppliedTimeout returns the timeout the server most recently granted for the
// transaction (SPEC R2/R9): the applied_timeout BeginTransaction or the last
// successful ExtendTimeout surfaced on the wire, not the value the client
// requested. Zero when the server response carried no applied_timeout.
func (tx *Transaction) AppliedTimeout() time.Duration {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	return tx.appliedTimeout
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

// TransactionDiff is the structured change set of a transaction (SPEC R9):
// lists of added, modified, and deleted entities and edges.
type TransactionDiff struct {
	AddedEntities    []Entity
	ModifiedEntities []Entity
	DeletedEntities  []Entity
	AddedEdges       []Edge
	ModifiedEdges    []Edge
	DeletedEdges     []Edge
}

// diffEntriesToEntities converts DiffEntry messages (which carry the entity
// identity fields) into the domain Entity type.
func diffEntriesToEntities(entries []*flowv1.DiffEntry) []Entity {
	out := make([]Entity, 0, len(entries))
	for _, e := range entries {
		out = append(out, Entity{
			ID:         e.GetId(),
			Type:       e.GetType(),
			Properties: e.GetProperties(),
			Suspected:  e.GetSuspected(),
			Embedding:  e.GetEmbedding(),
		})
	}
	return out
}

// diffEntriesToEdges converts DiffEntry messages (which carry the edge
// identity fields) into the domain Edge type.
func diffEntriesToEdges(entries []*flowv1.DiffEntry) []Edge {
	out := make([]Edge, 0, len(entries))
	for _, e := range entries {
		out = append(out, Edge{
			ID:           e.GetId(),
			Type:         e.GetType(),
			FromEntityID: e.GetFromEntityId(),
			ToEntityID:   e.GetToEntityId(),
			Properties:   e.GetProperties(),
			Suspected:    e.GetSuspected(),
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Read path methods (with transactionId injection)
// ---------------------------------------------------------------------------

// ListEntities lists entities within the transaction.
func (tx *Transaction) ListEntities(entityType string, opts ...ListEntitiesOption) (*EntityPage, error) {
	if err := tx.checkTerminal(); err != nil {
		return nil, err
	}
	return listEntitiesPage(tx.session, tx.idTypeMap, entityType, opts, tx.id)
}

// ExecuteCypher executes a read-only Cypher query within the transaction
// (SPEC R2). params is an optional JSON object bound to the query parameters.
func (tx *Transaction) ExecuteCypher(cypher string, params map[string]any) ([][]string, error) {
	if err := tx.checkTerminal(); err != nil {
		return nil, err
	}
	return executeCypherQuery(tx.session, cypher, params, tx.id)
}

// SearchNeighbors performs a vector similarity search within the transaction
// (SPEC R2).
func (tx *Transaction) SearchNeighbors(embedding []float32, entityType string, topK int) ([]SearchResult, error) {
	if err := tx.checkTerminal(); err != nil {
		return nil, err
	}
	return searchNeighborsResults(tx.session, tx.idTypeMap, embedding, entityType, topK, tx.id)
}

// FullTextSearch performs a full-text search within the transaction
// (SPEC R2).
func (tx *Transaction) FullTextSearch(query, entityType string) ([]Entity, error) {
	if err := tx.checkTerminal(); err != nil {
		return nil, err
	}
	return fullTextSearchResults(tx.session, tx.idTypeMap, query, entityType, tx.id)
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
// Lifecycle methods (SPEC R4 SDK-method-to-RPC mapping table)
// ---------------------------------------------------------------------------

// Diff returns the structured diff of the transaction's changes: lists of
// added, modified, and deleted entities and edges (SPEC R9).
func (tx *Transaction) Diff() (*TransactionDiff, error) {
	if err := tx.checkTerminal(); err != nil {
		return nil, err
	}

	req := &flowv1.GetTransactionDiffRequest{TransactionId: tx.id}

	var resp *flowv1.GetTransactionDiffResponse
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = tx.session.Cartographer.GetTransactionDiff(ctx, req)
		return callErr
	}, "")
	if err != nil {
		return nil, err
	}

	return &TransactionDiff{
		AddedEntities:    diffEntriesToEntities(resp.GetAddedEntities()),
		ModifiedEntities: diffEntriesToEntities(resp.GetModifiedEntities()),
		DeletedEntities:  diffEntriesToEntities(resp.GetDeletedEntities()),
		AddedEdges:       diffEntriesToEdges(resp.GetAddedEdges()),
		ModifiedEdges:    diffEntriesToEdges(resp.GetModifiedEdges()),
		DeletedEdges:     diffEntriesToEdges(resp.GetDeletedEdges()),
	}, nil
}

// Refresh re-hydrates the transaction's branch from the latest main and
// re-applies the transaction's changes (SPEC R9). It fails with ABORTED when
// an overlapping entity/edge was modified on main. Refresh does not reset the
// transaction timeout — nodes that need to keep a long-running transaction
// alive must call ExtendTimeout separately (SPEC R9).
func (tx *Transaction) Refresh() error {
	if err := tx.checkTerminal(); err != nil {
		return err
	}

	req := &flowv1.RefreshTransactionRequest{TransactionId: tx.id}
	return tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		_, callErr := tx.session.Cartographer.RefreshTransaction(ctx, req)
		return callErr
	}, "")
}

// CommitOption configures a Transaction.Commit call.
type CommitOption func(*commitConfig)

type commitConfig struct {
	ack bool
}

// WithAck makes Commit block until the background sync worker has pushed the
// merged commit to the remote (SPEC R10 commit(WithAck())): the wire carries
// ack=true and the Cartographer wakes the worker and waits for one full sync
// cycle before returning. Without WithAck, Commit returns immediately and the
// worker picks the push up on its next timer cycle.
func WithAck() CommitOption {
	return func(c *commitConfig) {
		c.ack = true
	}
}

// Commit commits the transaction's changes back to main (SPEC R9). On success
// the handle is marked committed and rejects every further operation locally,
// so the R4 example's deferred `tx.Rollback()` after a successful Commit
// surfaces the terminal error the caller ignores. Commit(WithAck()) blocks
// until the merged commit is pushed to the configured remote (SPEC R10).
func (tx *Transaction) Commit(opts ...CommitOption) error {
	if err := tx.checkTerminal(); err != nil {
		return err
	}

	cfg := &commitConfig{}
	for _, o := range opts {
		o(cfg)
	}

	req := &flowv1.CommitTransactionRequest{
		TransactionId: tx.id,
		Ack:           cfg.ack,
	}
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		_, callErr := tx.session.Cartographer.CommitTransaction(ctx, req)
		return callErr
	}, "")
	if err != nil {
		return err
	}

	tx.mu.Lock()
	tx.committed = true
	tx.mu.Unlock()
	return nil
}

// Rollback discards the transaction's branch and returns the handle to its
// terminal rolled-back state (SPEC R9). On success the handle rejects every
// further operation locally.
func (tx *Transaction) Rollback() error {
	if err := tx.checkTerminal(); err != nil {
		return err
	}

	req := &flowv1.RollbackTransactionRequest{TransactionId: tx.id}
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		_, callErr := tx.session.Cartographer.RollbackTransaction(ctx, req)
		return callErr
	}, "")
	if err != nil {
		return err
	}

	tx.mu.Lock()
	tx.rolledBack = true
	tx.mu.Unlock()
	return nil
}

// ExtendTimeout resets the transaction's expiry timer (SPEC R9). The duration
// must be positive and the total lifetime must not exceed the 7-day hard
// maximum; the Cartographer rejects violations with INVALID_ARGUMENT (no
// silent capping), which the SDK surfaces. On success the applied timeout the
// server granted is surfaced on the handle via AppliedTimeout (SPEC R9: the
// response exists "so the SDK surfaces what the server granted rather than
// assuming it").
func (tx *Transaction) ExtendTimeout(duration time.Duration) error {
	if err := tx.checkTerminal(); err != nil {
		return err
	}

	req := &flowv1.ExtendTimeoutRequest{
		TransactionId: tx.id,
		Duration:      durationpb.New(duration),
	}
	var resp *flowv1.ExtendTimeoutResponse
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = tx.session.Cartographer.ExtendTimeout(ctx, req)
		return callErr
	}, "")
	if err != nil {
		return err
	}

	tx.mu.Lock()
	tx.appliedTimeout = resp.GetAppliedTimeout().AsDuration()
	tx.mu.Unlock()
	return nil
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
