package flow

import (
	"context"
	"fmt"
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
	Suspected  bool
}

// Edge represents a directed edge between two entities.
type Edge struct {
	ID           string
	Type         string
	FromEntityID string
	ToEntityID   string
	Properties   map[string]string
	Suspected    bool
}

// SearchResult is a single result from a vector similarity search.
// Identity is exposed as typed fields; properties are carried losslessly.
type SearchResult struct {
	ID         string
	Type       string
	Properties map[string]string
	Similarity float64
}

// EntityPage is a page of entities returned by ListEntities.
type EntityPage struct {
	Entities      []Entity
	NextPageToken string
}

// TransactionDiff is a structured diff of changes within a transaction.
type TransactionDiff struct {
	AddedEntities    []Entity
	ModifiedEntities []Entity
	DeletedEntities  []Entity
	AddedEdges       []Edge
	ModifiedEdges    []Edge
	DeletedEdges     []Edge
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
func newExportStream(
	ctx context.Context, cancel context.CancelFunc, stream grpc.ServerStreamingClient[flowv1.ExportGraphResponse],
) *ExportStream {
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

// idTypeMapMaxSize and idTypeMapTTL bound the SDK's local ID-to-type cache
// (SPEC R3: "bounded local cache (TTL-bounded)"). The SPEC prescribes the
// bounds but not their values; these pick a generous ceiling for a
// best-effort capability cache.
const (
	idTypeMapMaxSize = 1000
	idTypeMapTTL     = 10 * time.Minute
)

// idTypeMap is a thread-safe, size- and TTL-bounded cache of entity ID to
// entity type (SPEC R3: "bounded local cache (TTL-bounded)"). Entries are
// treated as unknown once they are older than idTypeMapTTL, and once the map
// holds idTypeMapMaxSize IDs a new ID evicts the oldest entry.
//
// ponytail: Eviction is a linear scan for the oldest entry on an
// over-capacity store, and expired entries are only physically removed when
// such an eviction scan runs (they are excluded from resolve/snapshot
// earlier). Both are O(n) in the map size, which is bounded by
// idTypeMapMaxSize (1000), so the cost is acceptable for a best-effort cache
// populated from the SDK's own traffic. Upgrade path: a container/heap keyed
// by insertion time if the cache grows.
type idTypeMap struct {
	mu      sync.RWMutex
	entries map[string]idTypeEntry
	ttl     time.Duration
	maxSize int
}

// idTypeEntry is a single cached mapping with its insertion time for TTL
// eviction.
type idTypeEntry struct {
	entityType string
	insertedAt time.Time
}

func newIDTypeMap() *idTypeMap {
	return &idTypeMap{
		entries: make(map[string]idTypeEntry),
		ttl:     idTypeMapTTL,
		maxSize: idTypeMapMaxSize,
	}
}

func (m *idTypeMap) store(id, entityType string) {
	if id == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[id]; !ok && len(m.entries) >= m.maxSize {
		m.evictOldestLocked()
	}
	// Re-storing an existing ID refreshes its TTL: the entity was seen again
	// in the SDK's own traffic.
	m.entries[id] = idTypeEntry{entityType: entityType, insertedAt: time.Now()}
}

// evictOldestLocked removes the entry with the earliest insertion time.
// Caller must hold m.mu for writing.
func (m *idTypeMap) evictOldestLocked() {
	var oldestID string
	var oldestAt time.Time
	first := true
	for id, e := range m.entries {
		if first || e.insertedAt.Before(oldestAt) {
			oldestID, oldestAt = id, e.insertedAt
			first = false
		}
	}
	delete(m.entries, oldestID)
}

func (m *idTypeMap) resolve(id string) (string, bool) {
	m.mu.RLock()
	e, ok := m.entries[id]
	m.mu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Since(e.insertedAt) > m.ttl {
		return "", false // TTL-expired: treat as unknown (lazy eviction).
	}
	return e.entityType, true
}

// resolveOrWildcard returns the entity type for the given ID if present in the
// map, or "*" if not. This ensures capability annotation falls back to the
// wildcard rather than annotating with an empty type (which fails resolution).
func (m *idTypeMap) resolveOrWildcard(id string) string {
	t, ok := m.resolve(id)
	if !ok {
		return "*"
	}
	return t
}

func (m *idTypeMap) remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, id)
}

// snapshot returns a shallow copy of the live (non-expired) entries, safe for
// reading without holding the lock. The caller is responsible for its own
// synchronisation.
func (m *idTypeMap) snapshot() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	snap := make(map[string]string, len(m.entries))
	for id, e := range m.entries {
		if time.Since(e.insertedAt) <= m.ttl {
			snap[id] = e.entityType
		}
	}
	return snap
}

// merge copies a snapshot (ID → type) into the cache, stamping each entry
// with the current time so it gets a fresh TTL. Used by BeginTransaction to
// seed a transaction's cache from the graph's current snapshot.
func (m *idTypeMap) merge(snap map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, t := range snap {
		m.entries[id] = idTypeEntry{entityType: t, insertedAt: time.Now()}
	}
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
	set     bool
}

// WithTimeout sets the initial transaction timeout requested on
// BeginTransaction. The 7-day hard maximum (SPEC R9/R2) is enforced by the
// Cartographer: a requested timeout exceeding the cap is rejected with
// INVALID_ARGUMENT (matching ExtendTimeout — no silent capping; SPEC error
// table row "Invalid transaction timeout duration"). A non-positive duration
// is rejected locally by BeginTransaction (matching ExtendTimeout's local
// rejection — no silent capping). Valid requests receive
// the granted value in the response's applied_timeout, which the SDK
// surfaces on the resulting Transaction handle. The client sends the
// requested value verbatim and relies on applied_timeout, never a local cap.
//
// This is the SDK-surface BeginTxOption prescribed by SPEC R4/R9 as
// `graph.BeginTransaction(graph.WithTimeout(48 * time.Hour))`. It is distinct
// from the Client-level per-request timeout option WithRequestTimeout.
func WithTimeout(d time.Duration) BeginTxOption {
	return func(c *beginTxConfig) {
		c.timeout = d
		c.set = true
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
	if len(labels) == 0 {
		// Parser failed to extract entity types — fall back to the
		// READ:graph/entity/* wildcard capability (R3).
		labels = []string{"*"}
	}

	var resp *flowv1.ExecuteCypherResponse
	err := g.session.call(g.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = g.session.Cartographer.ExecuteCypher(ctx, req)
		return callErr
	}, "entity_types", labels...)
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
		results = append(results, SearchResult{
			ID:         r.GetEntityId(),
			Type:       r.GetEntityType(),
			Properties: r.GetProperties(),
			Similarity: r.GetScore(),
		})
		// Populate ID-to-type map.
		g.idTypeMap.store(r.GetEntityId(), r.GetEntityType())
	}
	return results, nil
}

// FullTextSearch performs a full-text search across all string properties.
func (g *Graph) FullTextSearch(query, entityType string) ([]Entity, error) {
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

	// Convert proto Entity list to []Entity.
	results := make([]Entity, 0, len(resp.GetResults()))
	for _, e := range resp.GetResults() {
		results = append(results, Entity{
			ID:         e.GetEntityId(),
			Type:       e.GetEntityType(),
			Properties: e.GetProperties(),
			Embedding:  e.GetEmbedding(),
		})
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

	entities := make([]Entity, 0, len(resp.GetEntities()))
	for _, e := range resp.GetEntities() {
		entities = append(entities, Entity{
			ID:         e.GetEntityId(),
			Type:       e.GetEntityType(),
			Properties: e.GetProperties(),
			Embedding:  e.GetEmbedding(),
		})
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
func (g *Graph) CreateEntity(
	entityType string, id *string, properties map[string]string, embedding []float32,
) (*Entity, error) {
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
	entityType := g.idTypeMap.resolveOrWildcard(id)

	var resp *flowv1.UpdateEntityResponse
	err := g.session.call(g.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = g.session.Cartographer.UpdateEntity(ctx, req)
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
	entityType := g.idTypeMap.resolveOrWildcard(id)

	var resp *flowv1.DeleteEntityResponse
	err := g.session.call(g.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = g.session.Cartographer.DeleteEntity(ctx, req)
		return callErr
	}, "entity_type", entityType)
	if err != nil {
		return nil, err
	}

	g.idTypeMap.remove(id)
	return &Entity{
		ID:         resp.GetEntityId(),
		Type:       resp.GetEntityType(),
		Properties: resp.GetProperties(),
		Embedding:  resp.GetEmbedding(),
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
	sourceType := g.idTypeMap.resolveOrWildcard(fromEntityID)

	var resp *flowv1.CreateEdgeResponse
	err := g.session.call(g.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = g.session.Cartographer.CreateEdge(ctx, req)
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
	// Cartographer (R3). The metadata key "entity_type" is used here
	// for consistency with other write-path wildcard annotations.
	err := g.session.call(g.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = g.session.Cartographer.DeleteEdge(ctx, req)
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

// BeginTransaction starts a new transaction.
func (g *Graph) BeginTransaction(opts ...BeginTxOption) (*Transaction, error) {
	if g.session == nil {
		return nil, fmt.Errorf("flow sdk: graph not initialised")
	}

	cfg := &beginTxConfig{}
	for _, o := range opts {
		o(cfg)
	}

	// Reject a non-positive requested timeout locally (SPEC error table row
	// "Invalid transaction timeout duration": "Timeout duration is non-positive
	// ... Applies to: ExtendTimeout, BeginTransaction"), mirroring
	// ExtendTimeout's local rejection — no silent capping. An absent option
	// (cfg.set == false) leaves the timeout unset and the server default
	// applies.
	if cfg.set && cfg.timeout <= 0 {
		return nil, fmt.Errorf("flow sdk: begin transaction timeout must be positive")
	}

	req := &flowv1.BeginTransactionRequest{}
	if cfg.set {
		req.Timeout = durationpb.New(cfg.timeout)
	}

	var resp *flowv1.BeginTransactionResponse
	err := g.session.call(g.session.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = g.session.Cartographer.BeginTransaction(ctx, req)
		return callErr
	}, "")
	if err != nil {
		return nil, err
	}

	appliedTimeout := cfg.timeout
	if resp.GetAppliedTimeout() != nil {
		appliedTimeout = resp.GetAppliedTimeout().AsDuration()
	}

	// Snapshot the current ID-to-type map into the transaction.
	txTypeMap := newIDTypeMap()
	txTypeMap.merge(g.idTypeMap.snapshot())

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
	return g.session.call(g.session.ctx, func(ctx context.Context) error {
		_, err := g.session.Cartographer.PullFromRemote(ctx, &flowv1.PullFromRemoteRequest{})
		return err
	}, "")
}

// ExportGraph starts a server-streaming export of the full graph.
func (g *Graph) ExportGraph(format string) (*ExportStream, error) {
	if g.session == nil {
		return nil, fmt.Errorf("flow sdk: graph not initialised")
	}
	req := &flowv1.ExportGraphRequest{Format: format}

	// Bound stream establishment (connect + RPC start) with a per-call
	// deadline, mirroring how session.call bounds unary RPCs, so a
	// blackholed upstream fails fast instead of hanging on the deadline-less
	// session ctx. The deadline is applied ONLY to the ExportGraph call that
	// establishes the stream.
	// ponytail: gRPC pins the context passed to a streaming RPC to the stream
	// for its whole lifetime, so this establishment deadline also bounds the
	// streaming phase when session.timeout is configured. There is no gRPC
	// mechanism to detach the context after establishment. The requested
	// "bound only connect+establishment, then release" separation is not
	// achievable for a single gRPC server-streaming channel; the intent that
	// matters here — fail-fast on a blackholed upstream instead of hanging on
	// the unbounded session ctx — is met, and the client always retains
	// Stream.Stop() plus a finalizer to release the stream early.
	// The stream is pinned to a child of ctx (timeout-bounded when
	// configured); Stop()/the finalizer cancel the context the stream is
	// actually pinned to — establishCtx itself when a deadline applies,
	// else the cancellable ctx — so an established stream is released early.
	ctx, cancel := context.WithCancel(g.session.ctx)
	establishCtx := ctx
	streamCancel := cancel
	if g.session.timeout > 0 {
		var establishCancel context.CancelFunc
		establishCtx, establishCancel = context.WithTimeout(ctx, g.session.timeout)
		streamCancel = establishCancel
	}
	stream, err := g.session.Cartographer.ExportGraph(establishCtx, req)
	if err != nil {
		cancel()
		return nil, err
	}
	return newExportStream(establishCtx, streamCancel, stream), nil
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
