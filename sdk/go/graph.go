package flow

import (
	"context"
	"fmt"
	"math"
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
// Distance is the raw cosine distance to the query embedding — LOWER is more
// similar, and results are ordered ascending by distance. It is a distance,
// not a similarity score: consumers must not sort by Distance descending
// expecting higher-is-better.
type SearchResult struct {
	ID         string
	Type       string
	Properties map[string]string
	Distance   float64
}

// EntityPage is a page of entities returned by ListEntities.
type EntityPage struct {
	Entities      []Entity
	NextPageToken string
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

// store records the ID→type mapping, stamping the entry with the current
// time for TTL tracking. An empty id or entityType is rejected: an entry
// with an empty type would resolve as ("", true) and annotate
// entity_type="" capability metadata instead of the wildcard (see
// resolveOrWildcard), which fails resolution.
func (m *idTypeMap) store(id, entityType string) {
	if id == "" || entityType == "" {
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

// ---------------------------------------------------------------------------
// Functional options
// ---------------------------------------------------------------------------

// ListEntitiesOption configures a ListEntities call.
type ListEntitiesOption func(*listEntitiesConfig)

type listEntitiesConfig struct {
	pageSize  int32
	pageToken string
}

// WithPageSize sets the page size for a ListEntities call (SPEC R4). A
// pageSize of 0 is omitted and defaults to the server's page size (1000);
// negative values and values above the maximum are rejected server-side
// (SPEC error-table row "Invalid pageSize in ListEntities").
func WithPageSize(size int) ListEntitiesOption {
	return func(c *listEntitiesConfig) {
		c.pageSize = int32(size)
	}
}

// WithPageToken sets the page token for the next ListEntities page (SPEC R4).
func WithPageToken(token string) ListEntitiesOption {
	return func(c *listEntitiesConfig) {
		c.pageToken = token
	}
}

// BeginTransactionOption configures a BeginTransaction call.
type BeginTransactionOption func(*beginTransactionConfig)

type beginTransactionConfig struct {
	timeout time.Duration
}

// WithTimeout overrides the default transaction timeout for a
// graph.BeginTransaction call (SPEC R9). The duration is passed to the wire
// verbatim — no silent capping — and a duration exceeding the 7-day hard
// maximum is rejected by the Cartographer with INVALID_ARGUMENT, which the
// SDK surfaces to the caller.
func WithTimeout(d time.Duration) BeginTransactionOption {
	return func(c *beginTransactionConfig) {
		c.timeout = d
	}
}

// ---------------------------------------------------------------------------
// Graph domain object
// ---------------------------------------------------------------------------

// Graph is the SDK's entry surface for the Cartographer knowledge graph
// (SPEC R4). It exposes read operations (ExecuteCypher, SearchNeighbors,
// FullTextSearch, ListEntities) and administrative RPCs (Sync, ExportGraph)
// plus transaction creation (BeginTransaction); it exposes no mutation
// methods — all mutations require a Transaction handle.
//
// The Graph shares the SDK's bounded local ID-to-type cache with the
// transactions it creates (SPEC R3): read results populate the cache and
// transaction write-path capability annotations resolve against it.
type Graph struct {
	session   *session
	idTypeMap *idTypeMap
}

// ExecuteCypher executes a read-only Cypher query against main (SPEC R2).
// params is an optional JSON object bound to the query parameters.
func (g *Graph) ExecuteCypher(cypher string, params map[string]any) ([][]string, error) {
	return executeCypherQuery(g.session, cypher, params, "")
}

// SearchNeighbors performs a vector similarity search against indexed entity
// types (SPEC R2).
func (g *Graph) SearchNeighbors(embedding []float32, entityType string, topK int) ([]SearchResult, error) {
	return searchNeighborsResults(g.session, g.idTypeMap, embedding, entityType, topK, "")
}

// FullTextSearch performs a full-text search across string properties
// (SPEC R2).
func (g *Graph) FullTextSearch(query, entityType string) ([]Entity, error) {
	return fullTextSearchResults(g.session, g.idTypeMap, query, entityType, "")
}

// ListEntities lists entities of the given type from main (SPEC R2).
func (g *Graph) ListEntities(entityType string, opts ...ListEntitiesOption) (*EntityPage, error) {
	return listEntitiesPage(g.session, g.idTypeMap, entityType, opts, "")
}

// BeginTransaction creates a new transaction and returns its handle (SPEC
// R4). All mutations require a Transaction handle obtained this way.
func (g *Graph) BeginTransaction(opts ...BeginTransactionOption) (*Transaction, error) {
	cfg := &beginTransactionConfig{}
	for _, o := range opts {
		o(cfg)
	}

	req := &flowv1.BeginTransactionRequest{}
	if cfg.timeout > 0 {
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

	return &Transaction{
		session:   g.session,
		id:        resp.GetTransactionId(),
		idTypeMap: g.idTypeMap,
	}, nil
}

// Sync wakes the background sync worker and waits for one full cycle (fetch →
// merge → re-hydrate → push). Returns FAILED_PRECONDITION when no remote is
// configured (SPEC R2 error-table row "Remote not configured").
func (g *Graph) Sync() error {
	return g.session.call(g.session.ctx, func(ctx context.Context) error {
		_, callErr := g.session.Cartographer.Sync(ctx, &flowv1.SyncRequest{})
		return callErr
	}, "")
}

// GraphExportStream is the server-streaming handle returned by
// Graph.ExportGraph (SPEC R2). Recv returns the next chunk of the exported
// graph; the stream ends with io.EOF.
type GraphExportStream struct {
	cancel context.CancelFunc
	stream grpc.ServerStreamingClient[flowv1.ExportGraphResponse]
}

// Recv returns the next exported chunk. It returns io.EOF at the end of the
// stream.
func (s *GraphExportStream) Recv() (*flowv1.ExportGraphResponse, error) {
	return s.stream.Recv()
}

// Stop cancels the export stream. Subsequent Recv calls return a
// context-cancelled error.
func (s *GraphExportStream) Stop() {
	s.cancel()
}

// ExportGraph exports the full graph in the requested format as a
// server-streaming byte-chunk stream (SPEC R2). The session per-call timeout
// bounds the whole stream lifetime: grpc-go pins the context passed to a
// streaming RPC to the stream for its whole lifetime, so the deadline
// applied here covers the lazy dial and every Recv.
func (g *Graph) ExportGraph(format string) (*GraphExportStream, error) {
	ctx, cancel := context.WithCancel(g.session.ctx)
	if g.session.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, g.session.timeout)
	}
	stream, err := g.session.Cartographer.ExportGraph(ctx, &flowv1.ExportGraphRequest{Format: format})
	if err != nil {
		cancel()
		return nil, err
	}
	return &GraphExportStream{cancel: cancel, stream: stream}, nil
}

// ---------------------------------------------------------------------------
// Shared read helpers (Graph and Transaction surfaces)
// ---------------------------------------------------------------------------

// listEntitiesPage lists entities of the given type. txID is injected when
// non-empty (transaction-scoped read, SPEC R2); an empty txID reads from
// main. Results populate the SDK's local ID-to-type cache (SPEC R3).
func listEntitiesPage(
	sess *session, cache *idTypeMap, entityType string, opts []ListEntitiesOption, txID string,
) (*EntityPage, error) {
	cfg := &listEntitiesConfig{}
	for _, o := range opts {
		o(cfg)
	}

	req := &flowv1.ListEntitiesRequest{
		EntityType:    entityType,
		PageSize:      cfg.pageSize,
		PageToken:     cfg.pageToken,
		TransactionId: txID,
	}

	var resp *flowv1.ListEntitiesResponse
	err := sess.call(sess.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = sess.Cartographer.ListEntities(ctx, req)
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
		cache.store(e.GetEntityId(), e.GetEntityType())
	}

	return &EntityPage{
		Entities:      entities,
		NextPageToken: resp.GetNextPageToken(),
	}, nil
}

// executeCypherQuery runs a read-only Cypher query. txID is injected when
// non-empty; an empty txID reads from main (SPEC R2). No entity-type
// capability metadata is attached — the Cartographer derives the referenced
// types from its own server-side parse of the statement (SPEC R3).
func executeCypherQuery(sess *session, cypher string, params map[string]any, txID string) ([][]string, error) {
	req := &flowv1.ExecuteCypherRequest{
		Cypher:        cypher,
		TransactionId: txID,
	}
	p, err := executeCypherParams(params)
	if err != nil {
		return nil, err
	}
	req.Params = p

	var resp *flowv1.ExecuteCypherResponse
	err = sess.call(sess.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = sess.Cartographer.ExecuteCypher(ctx, req)
		return callErr
	}, "")
	if err != nil {
		return nil, err
	}

	rows := make([][]string, 0, len(resp.GetRows()))
	for _, r := range resp.GetRows() {
		rows = append(rows, r.GetValues())
	}
	return rows, nil
}

// searchNeighborsResults runs a vector similarity search. txID is injected
// when non-empty; an empty txID reads from main (SPEC R2). Results populate
// the SDK's local ID-to-type cache (SPEC R3).
func searchNeighborsResults(
	sess *session, cache *idTypeMap, embedding []float32, entityType string, topK int, txID string,
) ([]SearchResult, error) {
	req := &flowv1.SearchNeighborsRequest{
		Embedding:     embedding,
		EntityType:    entityType,
		TopK:          int32(topK),
		TransactionId: txID,
	}

	var resp *flowv1.SearchNeighborsResponse
	err := sess.call(sess.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = sess.Cartographer.SearchNeighbors(ctx, req)
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
			Distance:   r.GetDistance(),
		})
		cache.store(r.GetEntityId(), r.GetEntityType())
	}
	return results, nil
}

// fullTextSearchResults runs a full-text search. txID is injected when
// non-empty; an empty txID reads from main (SPEC R2). Results populate the
// SDK's local ID-to-type cache (SPEC R3).
func fullTextSearchResults(
	sess *session, cache *idTypeMap, query, entityType, txID string,
) ([]Entity, error) {
	req := &flowv1.FullTextSearchRequest{
		Query:         query,
		EntityType:    entityType,
		TransactionId: txID,
	}

	var resp *flowv1.FullTextSearchResponse
	err := sess.call(sess.ctx, func(ctx context.Context) error {
		var callErr error
		resp, callErr = sess.Cartographer.FullTextSearch(ctx, req)
		return callErr
	}, "")
	if err != nil {
		return nil, err
	}

	entities := make([]Entity, 0, len(resp.GetResults()))
	for _, e := range resp.GetResults() {
		entities = append(entities, Entity{
			ID:         e.GetEntityId(),
			Type:       e.GetEntityType(),
			Properties: e.GetProperties(),
			Embedding:  e.GetEmbedding(),
		})
		cache.store(e.GetEntityId(), e.GetEntityType())
	}
	return entities, nil
}

// executeCypherParams converts a JSON-object params map into the wire params
// field. The wire field is google.protobuf.Struct (SPEC error-table row
// "ExecuteCypher params not a JSON object"), and the helper returns the
// Struct directly. A nil or empty map yields no params field (the optional
// parameter is absent).
func executeCypherParams(params map[string]any) (*structpb.Struct, error) {
	if len(params) == 0 {
		return nil, nil
	}
	s, err := structpb.NewStruct(params)
	if err != nil {
		return nil, fmt.Errorf("flow sdk: ExecuteCypher params must be a JSON object: %w", err)
	}
	return s, nil
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
