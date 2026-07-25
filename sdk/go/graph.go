package flow

import (
	"context"
	"fmt"
	"maps"
	"math"
	"runtime"
	"sync"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
)

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// Entity represents a graph entity with its ID, type, properties, and embedding.
type Entity struct {
	ID         string
	Type       string
	Properties map[string]string
	Embedding  []float32
}

// Edge represents a directed edge between two entities.
type Edge struct {
	ID           string
	Type         string
	FromEntityID string
	ToEntityID   string
	Properties   map[string]string
}

// SearchResult is a single result from a vector similarity search.
type SearchResult struct {
	Entity     map[string]any
	Similarity float64
}

// EntityPage is a page of entities returned by ListEntities.
type EntityPage struct {
	Entities      []map[string]any
	NextPageToken string
}

// TransactionDiff is a structured diff of changes within a transaction.
type TransactionDiff struct {
	AddedEntities    []Entity
	ModifiedEntities []Entity
	DeletedEntities  []string
	AddedEdges       []Edge
	ModifiedEdges    []Edge
	DeletedEdges     []string
}

// GraphChunk is a segment of serialised graph data yielded by ExportGraph.
// Individual chunks MUST be concatenated in order to form the complete output.
type GraphChunk struct {
	Data []byte
}

// ExportStream wraps the server-streaming ExportGraph response.
// Recv returns the next chunk of serialised graph data as a pure Go type.
// Stop cancels the stream. A finalizer provides best-effort cancellation if
// the caller forgets to call Stop().
type ExportStream struct {
	ctx    context.Context
	cancel context.CancelFunc
	stream grpc.ServerStreamingClient[flowv1.ExportGraphResponse]
}

// newExportStream creates an ExportStream with a finalizer that cancels the
// stream context on GC.
func newExportStream(ctx context.Context, cancel context.CancelFunc, stream grpc.ServerStreamingClient[flowv1.ExportGraphResponse]) *ExportStream {
	s := &ExportStream{ctx: ctx, cancel: cancel, stream: stream}
	runtime.SetFinalizer(s, func(s *ExportStream) { s.cancel() })
	return s
}

// Recv returns the next chunk from the export stream.
func (s *ExportStream) Recv() (*GraphChunk, error) {
	resp, err := s.stream.Recv()
	if err != nil {
		return nil, err
	}
	return &GraphChunk{Data: resp.GetChunk()}, nil
}

// Stop cancels the stream and clears the finalizer.
func (s *ExportStream) Stop() {
	s.cancel()
	runtime.SetFinalizer(s, nil)
}

// ---------------------------------------------------------------------------
// ID-to-type mapping
// ---------------------------------------------------------------------------

// idTypeMap is a thread-safe map of entity ID to entity type.
type idTypeMap struct {
	mu    sync.RWMutex
	types map[string]string
}

func newIDTypeMap() *idTypeMap {
	return &idTypeMap{types: make(map[string]string)}
}

func (m *idTypeMap) store(id, entityType string) {
	if id == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.types[id] = entityType
}

func (m *idTypeMap) resolve(id string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.types[id]
	return t, ok
}

func (m *idTypeMap) remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.types, id)
}

// snapshot returns a shallow copy of the underlying map, safe for reading
// without holding the lock. The caller is responsible for its own
// synchronisation.
func (m *idTypeMap) snapshot() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	snap := make(map[string]string, len(m.types))
	maps.Copy(snap, m.types)
	return snap
}

// ---------------------------------------------------------------------------
// Functional options
// ---------------------------------------------------------------------------

// ListEntitiesOption configures a ListEntities call.
type ListEntitiesOption func(*listEntitiesConfig)

type listEntitiesConfig struct {
	pageSize  int32
	pageToken string
}

// WithPageSize sets the page size for paginated listing.
func WithPageSize(n int) ListEntitiesOption {
	return func(c *listEntitiesConfig) {
		c.pageSize = int32(n)
	}
}

// WithPageToken sets the page token for paginated listing.
func WithPageToken(token string) ListEntitiesOption {
	return func(c *listEntitiesConfig) {
		c.pageToken = token
	}
}

// BeginTxOption configures a BeginTransaction call.
type BeginTxOption func(*beginTxConfig)

type beginTxConfig struct {
	timeout time.Duration
}

// WithTxTimeout sets the initial transaction timeout, silently capped
// at the hard maximum of 7 days.
func WithTxTimeout(d time.Duration) BeginTxOption {
	return func(c *beginTxConfig) {
		c.timeout = d
	}
}

// ---------------------------------------------------------------------------
// Graph
// ---------------------------------------------------------------------------

// Graph wraps the session's Cartographer service client.
type Graph struct {
	session   *session
	idTypeMap *idTypeMap
}

// ExecuteCypher executes a read-only Cypher query and returns the result rows.
func (g *Graph) ExecuteCypher(cypher string, params map[string]any) ([]map[string]any, error) {
	if g.session == nil {
		return nil, fmt.Errorf("flow sdk: graph not initialised")
	}

	// Parse params into proto structpb.Value.
	var paramsProto *structpb.Value
	if len(params) > 0 {
		s, err := structpb.NewStruct(params)
		if err != nil {
			return nil, fmt.Errorf("flow sdk: invalid params: %w", err)
		}
		paramsProto = structpb.NewStructValue(s)
	}

	req := &flowv1.ExecuteCypherRequest{
		Cypher: cypher,
		Params: paramsProto,
	}

	labels := extractEntityTypes(cypher)

	var resp *flowv1.ExecuteCypherResponse
	err := g.session.call(g.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = g.session.Cartographer.ExecuteCypher(ctx, req)
		return callErr
	}, "x-flow-entity-types", labels...)
	if err != nil {
		return nil, err
	}

	// Convert proto rows to []map[string]any.
	rows := make([]map[string]any, 0, len(resp.GetRows()))
	for _, row := range resp.GetRows() {
		m := make(map[string]any)
		vals := row.GetValues()
		for i, v := range vals {
			key := fmt.Sprintf("col_%d", i)
			m[key] = decodeProtoValue(v)
		}
		rows = append(rows, m)
	}
	return rows, nil
}

// SearchNeighbors performs a vector similarity search.
func (g *Graph) SearchNeighbors(embedding []float32, entityType string, topK int) ([]SearchResult, error) {
	if g.session == nil {
		return nil, fmt.Errorf("flow sdk: graph not initialised")
	}
	if err := validateEmbedding(embedding); err != nil {
		return nil, err
	}

	req := &flowv1.SearchNeighborsRequest{
		Embedding:  embedding,
		EntityType: entityType,
		TopK:       int32(topK),
	}

	var resp *flowv1.SearchNeighborsResponse
	err := g.session.call(g.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = g.session.Cartographer.SearchNeighbors(ctx, req)
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
		// Populate ID-to-type map.
		g.idTypeMap.store(r.GetEntityId(), r.GetEntityType())
	}
	return results, nil
}

// FullTextSearch performs a full-text search across all string properties.
func (g *Graph) FullTextSearch(query, entityType string) ([]map[string]any, error) {
	if g.session == nil {
		return nil, fmt.Errorf("flow sdk: graph not initialised")
	}

	req := &flowv1.FullTextSearchRequest{
		Query:      query,
		EntityType: entityType,
	}

	var resp *flowv1.FullTextSearchResponse
	err := g.session.call(g.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = g.session.Cartographer.FullTextSearch(ctx, req)
		return callErr
	}, "", entityType)
	if err != nil {
		return nil, err
	}

	// Convert proto Entity list to []map[string]any.
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
		// Populate ID-to-type map.
		g.idTypeMap.store(e.GetEntityId(), e.GetEntityType())
	}
	return results, nil
}

// ListEntities lists entities of a given type with optional pagination.
func (g *Graph) ListEntities(entityType string, opts ...ListEntitiesOption) (*EntityPage, error) {
	if g.session == nil {
		return nil, fmt.Errorf("flow sdk: graph not initialised")
	}

	cfg := &listEntitiesConfig{}
	for _, o := range opts {
		o(cfg)
	}

	req := &flowv1.ListEntitiesRequest{
		EntityType: entityType,
		PageSize:   cfg.pageSize,
		PageToken:  cfg.pageToken,
	}

	var resp *flowv1.ListEntitiesResponse
	err := g.session.call(g.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = g.session.Cartographer.ListEntities(ctx, req)
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
		// Populate ID-to-type map.
		g.idTypeMap.store(e.GetEntityId(), e.GetEntityType())
	}

	return &EntityPage{
		Entities:      entities,
		NextPageToken: resp.GetNextPageToken(),
	}, nil
}

// CreateEntity creates a new entity. If id is nil, the server auto-generates a UUID v4.
//
// ponytail: Client-side UUID v4 format validation is intentionally omitted.
// When id is non-nil, it is passed as-is to the Cartographer, which validates
// the format and returns INVALID_ARGUMENT on malformed UUIDs. This is
// consistent with the SPEC's server-side validation model — the server is
// authoritative for all structural validation including UUID format, entity
// type existence, property schema, and embedding constraints.
func (g *Graph) CreateEntity(entityType string, id *string, properties map[string]string, embedding []float32) (*Entity, error) {
	if g.session == nil {
		return nil, fmt.Errorf("flow sdk: graph not initialised")
	}
	if err := validateEmbedding(embedding); err != nil {
		return nil, err
	}

	req := &flowv1.CreateEntityRequest{
		EntityType: entityType,
		Properties: properties,
		Embedding:  embedding,
	}
	if id != nil {
		req.Id = *id
	}

	var resp *flowv1.CreateEntityResponse
	err := g.session.call(g.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = g.session.Cartographer.CreateEntity(ctx, req)
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
	g.idTypeMap.store(entity.ID, entity.Type)
	return entity, nil
}

// UpdateEntity partially updates an entity.
func (g *Graph) UpdateEntity(id string, properties map[string]string, embedding []float32) (*Entity, error) {
	if g.session == nil {
		return nil, fmt.Errorf("flow sdk: graph not initialised")
	}
	if err := validateEmbedding(embedding); err != nil {
		return nil, err
	}

	req := &flowv1.UpdateEntityRequest{
		Id:         id,
		Properties: properties,
		Embedding:  embedding,
	}

	// Resolve entity type from local map for capability annotation.
	entityType, _ := g.idTypeMap.resolve(id)

	var resp *flowv1.UpdateEntityResponse
	err := g.session.call(g.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = g.session.Cartographer.UpdateEntity(ctx, req)
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
	// Update local map if the response contains type info.
	if entity.Type != "" {
		g.idTypeMap.store(entity.ID, entity.Type)
	}
	return entity, nil
}

// DeleteEntity deletes an entity by ID.
func (g *Graph) DeleteEntity(id string) (*Entity, error) {
	if g.session == nil {
		return nil, fmt.Errorf("flow sdk: graph not initialised")
	}

	req := &flowv1.DeleteEntityRequest{
		Id: id,
	}

	// Resolve entity type from local map for capability annotation.
	entityType, _ := g.idTypeMap.resolve(id)

	var resp *flowv1.DeleteEntityResponse
	err := g.session.call(g.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = g.session.Cartographer.DeleteEntity(ctx, req)
		return callErr
	}, "x-flow-entity-type", entityType)
	if err != nil {
		return nil, err
	}

	g.idTypeMap.remove(id)
	return &Entity{
		ID:         resp.GetEntityId(),
		Type:       resp.GetEntityType(),
		Properties: resp.GetProperties(),
	}, nil
}

// CreateEdge creates a directed edge between two entities.
func (g *Graph) CreateEdge(edgeType, fromEntityID, toEntityID string, properties map[string]string) (*Edge, error) {
	if g.session == nil {
		return nil, fmt.Errorf("flow sdk: graph not initialised")
	}

	req := &flowv1.CreateEdgeRequest{
		EdgeType:     edgeType,
		FromEntityId: fromEntityID,
		ToEntityId:   toEntityID,
		Properties:   properties,
	}

	// Resolve source entity type for capability annotation.
	sourceType, _ := g.idTypeMap.resolve(fromEntityID)

	var resp *flowv1.CreateEdgeResponse
	err := g.session.call(g.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = g.session.Cartographer.CreateEdge(ctx, req)
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

// DeleteEdge deletes an edge by ID.
func (g *Graph) DeleteEdge(id string) (*Edge, error) {
	if g.session == nil {
		return nil, fmt.Errorf("flow sdk: graph not initialised")
	}

	req := &flowv1.DeleteEdgeRequest{Id: id}

	var resp *flowv1.DeleteEdgeResponse
	// Always annotate WRITE:graph/entity/* (Cartographer is authoritative for source type).
	// ponytail: DeleteEdge always uses the wildcard "*" as the entity type
	// because the edge's source entity type is resolved authoritatively by the
	// Cartographer (R3). The metadata key "x-flow-entity-type" is used here
	// for consistency with other write-path wildcard annotations.
	err := g.session.call(g.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = g.session.Cartographer.DeleteEdge(ctx, req)
		return callErr
	}, "x-flow-entity-type", "*")
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

// BeginTransaction starts a new transaction.
func (g *Graph) BeginTransaction(opts ...BeginTxOption) (*Transaction, error) {
	if g.session == nil {
		return nil, fmt.Errorf("flow sdk: graph not initialised")
	}

	cfg := &beginTxConfig{}
	for _, o := range opts {
		o(cfg)
	}

	req := &flowv1.BeginTransactionRequest{}
	if cfg.timeout > 0 {
		req.Timeout = durationpb.New(cfg.timeout)
	}

	resp, err := g.session.Cartographer.BeginTransaction(g.session.ctx, req)
	if err != nil {
		return nil, err
	}

	appliedTimeout := cfg.timeout
	if resp.GetAppliedTimeout() != nil {
		appliedTimeout = resp.GetAppliedTimeout().AsDuration()
	}

	// Snapshot the current ID-to-type map into the transaction.
	txTypeMap := newIDTypeMap()
	txTypeMap.mu.Lock()
	maps.Copy(txTypeMap.types, g.idTypeMap.snapshot())
	txTypeMap.mu.Unlock()

	return &Transaction{
		session:   g.session,
		id:        resp.GetTransactionId(),
		timeout:   appliedTimeout,
		idTypeMap: txTypeMap,
	}, nil
}

// PullFromRemote pulls latest data from the configured remote repository.
func (g *Graph) PullFromRemote() error {
	if g.session == nil {
		return fmt.Errorf("flow sdk: graph not initialised")
	}
	_, err := g.session.Cartographer.PullFromRemote(g.session.ctx, &flowv1.PullFromRemoteRequest{})
	return err
}

// ExportGraph starts a server-streaming export of the full graph.
func (g *Graph) ExportGraph(format string) (*ExportStream, error) {
	if g.session == nil {
		return nil, fmt.Errorf("flow sdk: graph not initialised")
	}
	ctx, cancel := context.WithCancel(g.session.ctx)
	req := &flowv1.ExportGraphRequest{Format: format}
	stream, err := g.session.Cartographer.ExportGraph(ctx, req)
	if err != nil {
		cancel()
		return nil, err
	}
	return newExportStream(ctx, cancel, stream), nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// validateEmbedding checks for NaN or infinity values.
func validateEmbedding(embedding []float32) error {
	for _, v := range embedding {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return fmt.Errorf("flow sdk: embedding contains NaN or infinity")
		}
	}
	return nil
}

// decodeProtoValue converts a protobuf Value to a Go value.
func decodeProtoValue(v *structpb.Value) any {
	if v == nil {
		return nil
	}
	switch k := v.GetKind().(type) {
	case *structpb.Value_NullValue:
		return nil
	case *structpb.Value_NumberValue:
		return k.NumberValue
	case *structpb.Value_StringValue:
		return k.StringValue
	case *structpb.Value_BoolValue:
		return k.BoolValue
	case *structpb.Value_StructValue:
		m := make(map[string]any)
		for k2, v2 := range k.StructValue.GetFields() {
			m[k2] = decodeProtoValue(v2)
		}
		return m
	case *structpb.Value_ListValue:
		items := make([]any, 0, len(k.ListValue.GetValues()))
		for _, item := range k.ListValue.GetValues() {
			items = append(items, decodeProtoValue(item))
		}
		return items
	default:
		return nil
	}
}
