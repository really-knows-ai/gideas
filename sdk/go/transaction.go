package flow

import (
	"context"
	"fmt"
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

	labels := extractEntityTypes(cypher)

	var resp *flowv1.ExecuteCypherResponse
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = tx.session.Cartographer.ExecuteCypher(ctx, req)
		return callErr
	}, "x-flow-entity-types", labels...)
	if err != nil {
		return nil, err
	}

	rows := make([]map[string]any, 0, len(resp.GetRows()))
	for _, row := range resp.GetRows() {
		m := make(map[string]any)
		for i, v := range row.GetValues() {
			m[fmt.Sprintf("col_%d", i)] = decodeProtoValue(v)
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
	}, "", entityType)
	if err != nil {
		return nil, err
	}

	results := make([]SearchResult, 0, len(resp.GetResults()))
	for _, r := range resp.GetResults() {
		entity := map[string]any{
			"entity_id":   r.GetEntityId(),
			"entity_type": r.GetEntityType(),
		}
		for k, v := range r.GetProperties() {
			entity[k] = v
		}
		results = append(results, SearchResult{
			Entity:     entity,
			Similarity: r.GetScore(),
		})
		tx.idTypeMap.store(r.GetEntityId(), r.GetEntityType())
	}
	return results, nil
}

// FullTextSearch performs a full-text search within the transaction.
func (tx *Transaction) FullTextSearch(query, entityType string) ([]map[string]any, error) {
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
	}, "", entityType)
	if err != nil {
		return nil, err
	}

	results := make([]map[string]any, 0, len(resp.GetResults()))
	for _, e := range resp.GetResults() {
		m := map[string]any{
			"entity_id":   e.GetEntityId(),
			"entity_type": e.GetEntityType(),
		}
		for k, v := range e.GetProperties() {
			m[k] = v
		}
		results = append(results, m)
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
	}, "", entityType)
	if err != nil {
		return nil, err
	}

	entities := make([]map[string]any, 0, len(resp.GetEntities()))
	for _, e := range resp.GetEntities() {
		m := map[string]any{
			"entity_id":   e.GetEntityId(),
			"entity_type": e.GetEntityType(),
		}
		for k, v := range e.GetProperties() {
			m[k] = v
		}
		entities = append(entities, m)
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
		req.Id = *id
	}

	var resp *flowv1.CreateEntityResponse
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = tx.session.Cartographer.CreateEntity(ctx, req)
		return callErr
	}, "", entityType)
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
	if err := validateEmbedding(embedding); err != nil {
		return nil, err
	}

	req := &flowv1.UpdateEntityRequest{
		Id:            id,
		Properties:    properties,
		Embedding:     embedding,
		TransactionId: tx.id,
	}

	entityType, _ := tx.idTypeMap.resolve(id)

	var resp *flowv1.UpdateEntityResponse
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = tx.session.Cartographer.UpdateEntity(ctx, req)
		return callErr
	}, "x-flow-entity-type", entityType)
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

	req := &flowv1.DeleteEntityRequest{
		Id:            id,
		TransactionId: tx.id,
	}

	entityType, _ := tx.idTypeMap.resolve(id)

	var resp *flowv1.DeleteEntityResponse
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = tx.session.Cartographer.DeleteEntity(ctx, req)
		return callErr
	}, "x-flow-entity-type", entityType)
	if err != nil {
		return nil, err
	}

	tx.idTypeMap.remove(id)
	return &Entity{
		ID:         resp.GetEntityId(),
		Type:       resp.GetEntityType(),
		Properties: resp.GetProperties(),
	}, nil
}

// CreateEdge creates a directed edge within the transaction.
func (tx *Transaction) CreateEdge(
	edgeType, fromEntityID, toEntityID string, properties map[string]string,
) (*Edge, error) {
	if err := tx.checkRolled(); err != nil {
		return nil, err
	}

	req := &flowv1.CreateEdgeRequest{
		EdgeType:      edgeType,
		FromEntityId:  fromEntityID,
		ToEntityId:    toEntityID,
		Properties:    properties,
		TransactionId: tx.id,
	}

	sourceType, _ := tx.idTypeMap.resolve(fromEntityID)

	var resp *flowv1.CreateEdgeResponse
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = tx.session.Cartographer.CreateEdge(ctx, req)
		return callErr
	}, "x-flow-entity-type", sourceType)
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

	req := &flowv1.DeleteEdgeRequest{
		Id:            id,
		TransactionId: tx.id,
	}

	var resp *flowv1.DeleteEdgeResponse
	err := tx.session.call(tx.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = tx.session.Cartographer.DeleteEdge(ctx, req)
		return callErr
	}, "", "*")
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

	resp, err := tx.session.Cartographer.GetTransactionDiff(tx.session.ctx, &flowv1.GetTransactionDiffRequest{
		TransactionId: tx.id,
	})
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
		diff.DeletedEntities = append(diff.DeletedEntities, e.GetId())
	}
	for _, e := range resp.GetAddedEdges() {
		diff.AddedEdges = append(diff.AddedEdges, diffEntryToEdge(e))
	}
	for _, e := range resp.GetModifiedEdges() {
		diff.ModifiedEdges = append(diff.ModifiedEdges, diffEntryToEdge(e))
	}
	for _, e := range resp.GetDeletedEdges() {
		diff.DeletedEdges = append(diff.DeletedEdges, e.GetId())
	}

	return diff, nil
}

// Refresh re-hydrates the transaction from latest main.
func (tx *Transaction) Refresh() error {
	if err := tx.checkRolled(); err != nil {
		return err
	}
	_, err := tx.session.Cartographer.RefreshTransaction(tx.session.ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: tx.id,
	})
	return err
}

// Commit commits the transaction back to main.
func (tx *Transaction) Commit() error {
	if err := tx.checkRolled(); err != nil {
		return err
	}
	_, err := tx.session.Cartographer.CommitTransaction(tx.session.ctx, &flowv1.CommitTransactionRequest{
		TransactionId: tx.id,
	})
	return err
}

// Rollback discards the transaction branch.
func (tx *Transaction) Rollback() error {
	if tx.rolledBack {
		return nil // idempotent
	}
	_, err := tx.session.Cartographer.RollbackTransaction(tx.session.ctx, &flowv1.RollbackTransactionRequest{
		TransactionId: tx.id,
	})
	if err != nil {
		return err
	}
	tx.rolledBack = true
	return nil
}

// ExtendTimeout extends the transaction timeout. The duration must be positive;
// total lifetime (beginTime + all extensions) cannot exceed 7 days. Returns
// the applied timeout (after capping).
func (tx *Transaction) ExtendTimeout(d time.Duration) (time.Duration, error) {
	if err := tx.checkRolled(); err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("flow sdk: extend timeout duration must be positive")
	}

	_, err := tx.session.Cartographer.ExtendTimeout(tx.session.ctx, &flowv1.ExtendTimeoutRequest{
		TransactionId: tx.id,
		Duration:      durationpb.New(d),
	})
	if err != nil {
		return 0, err
	}

	// ponytail: ExtendTimeoutResponse does not return AppliedTimeout.
	// The server caps silently; return the requested duration as the
	// best-effort approximation. Callers relying on the return value for
	// timeout tracking will see the requested (possibly uncapped) value,
	// not the server-enforced cap. A future proto update should add
	// AppliedTimeout to ExtendTimeoutResponse.
	tx.timeout = d
	return d, nil
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
	}
}

func diffEntryToEdge(e *flowv1.DiffEntry) Edge {
	return Edge{
		ID:           e.GetId(),
		Type:         e.GetType(),
		FromEntityID: e.GetFromEntityId(),
		ToEntityID:   e.GetToEntityId(),
		Properties:   e.GetProperties(),
	}
}
