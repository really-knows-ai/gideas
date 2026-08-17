package flow

import (
	"context"
	"io"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
)

const metadataEntityTypeKey = "entity_type"

// componentType is the entity-type name used across the Graph and Transaction
// test suites.
const componentType = "Component"

const (
	clobberedIDProp   = "clobbered-id"
	clobberedTypeProp = "ClobberedType"
)

// Canonical RFC4122 §3 UUID v4 IDs used by write-path tests. The SDK rejects
// non-canonical ID spellings client-side (SPEC:162), so every write-path call
// must supply the canonical 8-4-4-4-12 lowercase dashed form.
const (
	testUUIDEntity = "550e8400-e29b-41d4-a716-446655440000"
	testUUIDFrom   = "8c3a6d5e-9f1b-4e2a-8c4d-1f2e3a4b5c6d"
	testUUIDTo     = "1f2e3a4b-5c6d-4e7f-8a9b-0c1d2e3f4a5b"
	testUUIDEdge   = "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"
	testUUIDTx     = "9c4a3e2f-1b2d-4a6b-8c9d-0e1f2a3b4c5e"
)

// ---------------------------------------------------------------------------
// Mock CartographerServiceClient
// ---------------------------------------------------------------------------

// mockCartographerClient implements flowv1.CartographerServiceClient for testing.
type mockCartographerClient struct {
	executeCypher   func(ctx context.Context, req *flowv1.ExecuteCypherRequest) (*flowv1.ExecuteCypherResponse, error)
	searchNeighbors func(ctx context.Context, req *flowv1.SearchNeighborsRequest) (*flowv1.SearchNeighborsResponse, error)
	fullTextSearch  func(ctx context.Context, req *flowv1.FullTextSearchRequest) (*flowv1.FullTextSearchResponse, error)
	listEntities    func(ctx context.Context, req *flowv1.ListEntitiesRequest) (*flowv1.ListEntitiesResponse, error)
	createEntity    func(ctx context.Context, req *flowv1.CreateEntityRequest) (*flowv1.CreateEntityResponse, error)
	updateEntity    func(ctx context.Context, req *flowv1.UpdateEntityRequest) (*flowv1.UpdateEntityResponse, error)
	deleteEntity    func(ctx context.Context, req *flowv1.DeleteEntityRequest) (*flowv1.DeleteEntityResponse, error)
	createEdge      func(ctx context.Context, req *flowv1.CreateEdgeRequest) (*flowv1.CreateEdgeResponse, error)
	deleteEdge      func(ctx context.Context, req *flowv1.DeleteEdgeRequest) (*flowv1.DeleteEdgeResponse, error)
	beginTx         func(ctx context.Context,
		req *flowv1.BeginTransactionRequest,
	) (*flowv1.BeginTransactionResponse, error)
	commitTx func(ctx context.Context,
		req *flowv1.CommitTransactionRequest,
	) (*flowv1.CommitTransactionResponse, error)
	rollbackTx func(ctx context.Context,
		req *flowv1.RollbackTransactionRequest,
	) (*flowv1.RollbackTransactionResponse, error)
	refreshTx func(ctx context.Context,
		req *flowv1.RefreshTransactionRequest,
	) (*flowv1.RefreshTransactionResponse, error)
	getTxDiff func(ctx context.Context,
		req *flowv1.GetTransactionDiffRequest,
	) (*flowv1.GetTransactionDiffResponse, error)
	extendTimeout func(ctx context.Context, req *flowv1.ExtendTimeoutRequest) (*flowv1.ExtendTimeoutResponse, error)
	exportGraph   func(ctx context.Context, req *flowv1.ExportGraphRequest,
	) (grpc.ServerStreamingClient[flowv1.ExportGraphResponse], error)
	sync func(ctx context.Context, req *flowv1.SyncRequest) (*flowv1.SyncResponse, error)
}

func (m *mockCartographerClient) ExecuteCypher(
	ctx context.Context, req *flowv1.ExecuteCypherRequest, opts ...grpc.CallOption,
) (*flowv1.ExecuteCypherResponse, error) {
	if m.executeCypher != nil {
		return m.executeCypher(ctx, req)
	}
	return &flowv1.ExecuteCypherResponse{}, nil
}
func (m *mockCartographerClient) SearchNeighbors(
	ctx context.Context, req *flowv1.SearchNeighborsRequest, opts ...grpc.CallOption,
) (*flowv1.SearchNeighborsResponse, error) {
	if m.searchNeighbors != nil {
		return m.searchNeighbors(ctx, req)
	}
	return &flowv1.SearchNeighborsResponse{}, nil
}
func (m *mockCartographerClient) FullTextSearch(
	ctx context.Context, req *flowv1.FullTextSearchRequest, opts ...grpc.CallOption,
) (*flowv1.FullTextSearchResponse, error) {
	if m.fullTextSearch != nil {
		return m.fullTextSearch(ctx, req)
	}
	return &flowv1.FullTextSearchResponse{}, nil
}
func (m *mockCartographerClient) ListEntities(
	ctx context.Context, req *flowv1.ListEntitiesRequest, opts ...grpc.CallOption,
) (*flowv1.ListEntitiesResponse, error) {
	if m.listEntities != nil {
		return m.listEntities(ctx, req)
	}
	return &flowv1.ListEntitiesResponse{}, nil
}
func (m *mockCartographerClient) CreateEntity(
	ctx context.Context, req *flowv1.CreateEntityRequest, opts ...grpc.CallOption,
) (*flowv1.CreateEntityResponse, error) {
	if m.createEntity != nil {
		return m.createEntity(ctx, req)
	}
	return &flowv1.CreateEntityResponse{}, nil
}
func (m *mockCartographerClient) UpdateEntity(
	ctx context.Context, req *flowv1.UpdateEntityRequest, opts ...grpc.CallOption,
) (*flowv1.UpdateEntityResponse, error) {
	if m.updateEntity != nil {
		return m.updateEntity(ctx, req)
	}
	return &flowv1.UpdateEntityResponse{}, nil
}
func (m *mockCartographerClient) DeleteEntity(
	ctx context.Context, req *flowv1.DeleteEntityRequest, opts ...grpc.CallOption,
) (*flowv1.DeleteEntityResponse, error) {
	if m.deleteEntity != nil {
		return m.deleteEntity(ctx, req)
	}
	return &flowv1.DeleteEntityResponse{}, nil
}
func (m *mockCartographerClient) CreateEdge(
	ctx context.Context, req *flowv1.CreateEdgeRequest, opts ...grpc.CallOption,
) (*flowv1.CreateEdgeResponse, error) {
	if m.createEdge != nil {
		return m.createEdge(ctx, req)
	}
	return &flowv1.CreateEdgeResponse{}, nil
}
func (m *mockCartographerClient) DeleteEdge(
	ctx context.Context, req *flowv1.DeleteEdgeRequest, opts ...grpc.CallOption,
) (*flowv1.DeleteEdgeResponse, error) {
	if m.deleteEdge != nil {
		return m.deleteEdge(ctx, req)
	}
	return &flowv1.DeleteEdgeResponse{}, nil
}
func (m *mockCartographerClient) BeginTransaction(
	ctx context.Context, req *flowv1.BeginTransactionRequest, opts ...grpc.CallOption,
) (*flowv1.BeginTransactionResponse, error) {
	if m.beginTx != nil {
		return m.beginTx(ctx, req)
	}
	return &flowv1.BeginTransactionResponse{}, nil
}
func (m *mockCartographerClient) CommitTransaction(
	ctx context.Context, req *flowv1.CommitTransactionRequest, opts ...grpc.CallOption,
) (*flowv1.CommitTransactionResponse, error) {
	if m.commitTx != nil {
		return m.commitTx(ctx, req)
	}
	return &flowv1.CommitTransactionResponse{}, nil
}
func (m *mockCartographerClient) RollbackTransaction(
	ctx context.Context, req *flowv1.RollbackTransactionRequest, opts ...grpc.CallOption,
) (*flowv1.RollbackTransactionResponse, error) {
	if m.rollbackTx != nil {
		return m.rollbackTx(ctx, req)
	}
	return &flowv1.RollbackTransactionResponse{}, nil
}
func (m *mockCartographerClient) RefreshTransaction(
	ctx context.Context, req *flowv1.RefreshTransactionRequest, opts ...grpc.CallOption,
) (*flowv1.RefreshTransactionResponse, error) {
	if m.refreshTx != nil {
		return m.refreshTx(ctx, req)
	}
	return &flowv1.RefreshTransactionResponse{}, nil
}
func (m *mockCartographerClient) GetTransactionDiff(
	ctx context.Context, req *flowv1.GetTransactionDiffRequest, opts ...grpc.CallOption,
) (*flowv1.GetTransactionDiffResponse, error) {
	if m.getTxDiff != nil {
		return m.getTxDiff(ctx, req)
	}
	return &flowv1.GetTransactionDiffResponse{}, nil
}
func (m *mockCartographerClient) ExtendTimeout(
	ctx context.Context, req *flowv1.ExtendTimeoutRequest, opts ...grpc.CallOption,
) (*flowv1.ExtendTimeoutResponse, error) {
	if m.extendTimeout != nil {
		return m.extendTimeout(ctx, req)
	}
	return &flowv1.ExtendTimeoutResponse{}, nil
}
func (m *mockCartographerClient) ApplySchema(
	ctx context.Context, req *flowv1.ApplySchemaRequest, opts ...grpc.CallOption,
) (*flowv1.ApplySchemaResponse, error) {
	return &flowv1.ApplySchemaResponse{}, nil
}
func (m *mockCartographerClient) WipeGraph(
	ctx context.Context, req *flowv1.WipeGraphRequest, opts ...grpc.CallOption,
) (*flowv1.WipeGraphResponse, error) {
	return &flowv1.WipeGraphResponse{}, nil
}
func (m *mockCartographerClient) HealthCheck(
	ctx context.Context, req *flowv1.HealthCheckRequest, opts ...grpc.CallOption,
) (*flowv1.HealthCheckResponse, error) {
	return &flowv1.HealthCheckResponse{}, nil
}
func (m *mockCartographerClient) ExportGraph(
	ctx context.Context, req *flowv1.ExportGraphRequest, opts ...grpc.CallOption,
) (grpc.ServerStreamingClient[flowv1.ExportGraphResponse], error) {
	if m.exportGraph != nil {
		return m.exportGraph(ctx, req)
	}
	return nil, nil
}
func (m *mockCartographerClient) Sync(
	ctx context.Context, req *flowv1.SyncRequest, opts ...grpc.CallOption,
) (*flowv1.SyncResponse, error) {
	if m.sync != nil {
		return m.sync(ctx, req)
	}
	return &flowv1.SyncResponse{}, nil
}

// ---------------------------------------------------------------------------
// Transaction write-method tests
// ---------------------------------------------------------------------------
//
// The write-path tests construct a Transaction handle directly via newMockTx:
// mutation methods exist only on the Transaction surface (SPEC R4 — the Graph
// exposes read and administrative methods only), so each test exercises the
// Transaction handle in isolation from the Graph that produced it.

// newMockGraph returns a Graph bound to the mock Cartographer client, for
// testing the Graph read/admin surface in isolation.
func newMockGraph(mock *mockCartographerClient) *Graph {
	ctx, cancel := context.WithCancel(context.Background())
	sess := &session{
		Cartographer: mock,
		ctx:          ctx,
		cancel:       cancel,
	}
	return &Graph{session: sess, idTypeMap: newIDTypeMap()}
}

func TestCreateEntity(t *testing.T) {
	mock := &mockCartographerClient{
		createEntity: func(ctx context.Context, req *flowv1.CreateEntityRequest) (*flowv1.CreateEntityResponse, error) {
			return &flowv1.CreateEntityResponse{
				EntityId:   "test-id",
				EntityType: componentType,
				Properties: req.GetProperties(),
			}, nil
		},
	}
	tx := newMockTx(mock)
	props := map[string]string{"name": "test"}
	entity, err := tx.CreateEntity(componentType, nil, props, nil)
	if err != nil {
		t.Fatalf("CreateEntity returned error: %v", err)
	}
	if entity.ID != "test-id" {
		t.Errorf("expected entity ID test-id, got %s", entity.ID)
	}
	if entity.Type != componentType {
		t.Errorf("expected entity type Component, got %s", entity.Type)
	}
	if entity.Properties["name"] != "test" {
		t.Errorf("expected name=test, got %v", entity.Properties)
	}
}

func TestCreateEntity_NilIDSendsEmpty(t *testing.T) {
	var capturedID string
	mock := &mockCartographerClient{
		createEntity: func(ctx context.Context, req *flowv1.CreateEntityRequest) (*flowv1.CreateEntityResponse, error) {
			capturedID = req.GetId()
			return &flowv1.CreateEntityResponse{
				EntityId:   "generated-id",
				EntityType: "Component",
			}, nil
		},
	}
	tx := newMockTx(mock)
	entity, err := tx.CreateEntity("Component", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateEntity returned error: %v", err)
	}
	if capturedID != "" {
		t.Errorf("expected empty id in request, got %q", capturedID)
	}
	if entity.ID != "generated-id" {
		t.Errorf("expected generated ID, got %s", entity.ID)
	}
}

// TestCreateEntity_PopulatesMap pins SPEC R3's ID-to-type cache population
// from creation responses on the Transaction layer: the created entity's
// response type is recorded keyed by entity ID.
func TestCreateEntity_PopulatesMap(t *testing.T) {
	mock := &mockCartographerClient{
		createEntity: func(ctx context.Context, req *flowv1.CreateEntityRequest) (*flowv1.CreateEntityResponse, error) {
			return &flowv1.CreateEntityResponse{
				EntityId:   "entity-1",
				EntityType: "Component",
			}, nil
		},
	}
	tx := newMockTx(mock)
	_, err := tx.CreateEntity("Component", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateEntity returned error: %v", err)
	}
	typ, ok := tx.idTypeMap.resolve("entity-1")
	if !ok || typ != componentType {
		t.Errorf("expected Component type for entity-1, got %q (ok=%v)", typ, ok)
	}
}

func TestUpdateEntity(t *testing.T) {
	mock := &mockCartographerClient{
		updateEntity: func(ctx context.Context, req *flowv1.UpdateEntityRequest) (*flowv1.UpdateEntityResponse, error) {
			return &flowv1.UpdateEntityResponse{
				EntityId:   req.GetId(),
				EntityType: "Component",
				Properties: req.GetProperties(),
			}, nil
		},
	}
	tx := newMockTx(mock)
	tx.idTypeMap.store(testUUIDEntity, "Component")
	entity, err := tx.UpdateEntity(testUUIDEntity, map[string]string{"name": "updated"}, nil)
	if err != nil {
		t.Fatalf("UpdateEntity returned error: %v", err)
	}
	if entity.ID != testUUIDEntity {
		t.Errorf("expected entity ID %s, got %s", testUUIDEntity, entity.ID)
	}
}

func TestDeleteEntity(t *testing.T) {
	mock := &mockCartographerClient{
		deleteEntity: func(ctx context.Context, req *flowv1.DeleteEntityRequest) (*flowv1.DeleteEntityResponse, error) {
			return &flowv1.DeleteEntityResponse{
				EntityId:   req.GetId(),
				EntityType: "Component",
			}, nil
		},
	}
	tx := newMockTx(mock)
	tx.idTypeMap.store(testUUIDEntity, "Component")
	entity, err := tx.DeleteEntity(testUUIDEntity)
	if err != nil {
		t.Fatalf("DeleteEntity returned error: %v", err)
	}
	if entity.ID != testUUIDEntity {
		t.Errorf("expected entity ID %s, got %s", testUUIDEntity, entity.ID)
	}
	// Verify removed from map
	_, ok := tx.idTypeMap.resolve(testUUIDEntity)
	if ok {
		t.Error("expected entity to be removed from map after delete")
	}
}

func TestDeleteEntity_ReturnsEmbedding(t *testing.T) {
	mock := &mockCartographerClient{
		deleteEntity: func(ctx context.Context, req *flowv1.DeleteEntityRequest) (*flowv1.DeleteEntityResponse, error) {
			return &flowv1.DeleteEntityResponse{
				EntityId:   req.GetId(),
				EntityType: "Component",
				Embedding:  []float32{0.1, 0.2, 0.3},
			}, nil
		},
	}
	tx := newMockTx(mock)
	entity, err := tx.DeleteEntity(testUUIDEntity)
	if err != nil {
		t.Fatalf("DeleteEntity returned error: %v", err)
	}
	if len(entity.Embedding) != 3 || entity.Embedding[0] != 0.1 || entity.Embedding[2] != 0.3 {
		t.Errorf("expected embedding [0.1 0.2 0.3], got %v", entity.Embedding)
	}
}

func TestCreateEdge(t *testing.T) {
	mock := &mockCartographerClient{
		createEdge: func(ctx context.Context, req *flowv1.CreateEdgeRequest) (*flowv1.CreateEdgeResponse, error) {
			return &flowv1.CreateEdgeResponse{
				EdgeId:       testUUIDEdge,
				EdgeType:     "DEPENDS_ON",
				FromEntityId: req.GetFromEntityId(),
				ToEntityId:   req.GetToEntityId(),
			}, nil
		},
	}
	tx := newMockTx(mock)
	tx.idTypeMap.store(testUUIDFrom, "Component")
	edge, err := tx.CreateEdge("DEPENDS_ON", testUUIDFrom, testUUIDTo, nil)
	if err != nil {
		t.Fatalf("CreateEdge returned error: %v", err)
	}
	if edge.ID != testUUIDEdge {
		t.Errorf("expected edge ID %s, got %s", testUUIDEdge, edge.ID)
	}
	if edge.FromEntityID != testUUIDFrom {
		t.Errorf("expected from entity %s, got %s", testUUIDFrom, edge.FromEntityID)
	}
}

func TestDeleteEdge(t *testing.T) {
	mock := &mockCartographerClient{
		deleteEdge: func(ctx context.Context, req *flowv1.DeleteEdgeRequest) (*flowv1.DeleteEdgeResponse, error) {
			return &flowv1.DeleteEdgeResponse{
				EdgeId:       req.GetId(),
				EdgeType:     "DEPENDS_ON",
				FromEntityId: "from-1",
				ToEntityId:   "to-1",
			}, nil
		},
	}
	tx := newMockTx(mock)
	edge, err := tx.DeleteEdge(testUUIDEdge)
	if err != nil {
		t.Fatalf("DeleteEdge returned error: %v", err)
	}
	if edge.ID != testUUIDEdge {
		t.Errorf("expected edge ID %s, got %s", testUUIDEdge, edge.ID)
	}
}

// ---------------------------------------------------------------------------
// ID-to-type map unit tests
// ---------------------------------------------------------------------------

func TestIDTypeMap_StoreAndResolve(t *testing.T) {
	m := newIDTypeMap()
	m.store("id-1", "Component")
	typ, ok := m.resolve("id-1")
	if !ok || typ != componentType {
		t.Errorf("expected Component, got %q (ok=%v)", typ, ok)
	}
}

func TestIDTypeMap_Remove(t *testing.T) {
	m := newIDTypeMap()
	m.store("id-1", "Component")
	m.remove("id-1")
	_, ok := m.resolve("id-1")
	if ok {
		t.Error("expected id-1 to be removed")
	}
}

// TestIDTypeMap_EvictsOldestAtCapacity verifies the size bound (SPEC R3:
// "bounded local cache"): once the map holds maxSize IDs, storing a new ID
// evicts the oldest entry.
func TestIDTypeMap_EvictsOldestAtCapacity(t *testing.T) {
	m := newIDTypeMap()
	m.maxSize = 3
	m.store("id-1", "Component")
	m.store("id-2", "Service")
	m.store("id-3", "Component")
	m.store("id-4", "Service")
	// id-1 was inserted first and must have been evicted at capacity.
	if _, ok := m.resolve("id-1"); ok {
		t.Error("expected id-1 (oldest) to be evicted at capacity")
	}
	for _, id := range []string{"id-2", "id-3", "id-4"} {
		if _, ok := m.resolve(id); !ok {
			t.Errorf("expected %s to remain in the map", id)
		}
	}
}

// TestIDTypeMap_TTLExpiry verifies the TTL bound (SPEC R3: "TTL-bounded"):
// an entry older than the TTL no longer resolves (lazy expiry via resolve),
// while re-storing an ID refreshes its TTL.
func TestIDTypeMap_TTLExpiry(t *testing.T) {
	m := newIDTypeMap()
	m.ttl = 5 * time.Millisecond
	m.store("id-1", "Component")
	m.store("id-2", "Service")
	time.Sleep(20 * time.Millisecond)
	for _, id := range []string{"id-1", "id-2"} {
		if _, ok := m.resolve(id); ok {
			t.Errorf("expected %s to expire after TTL", id)
		}
	}
	// Re-storing refreshes the TTL.
	m.store("id-1", componentType)
	typ, ok := m.resolve("id-1")
	if !ok || typ != componentType {
		t.Errorf("expected id-1 to resolve again after re-store, got %q (ok=%v)", typ, ok)
	}
}

func TestIDTypeMap_ResolveUnknown(t *testing.T) {
	m := newIDTypeMap()
	_, ok := m.resolve("unknown")
	if ok {
		t.Error("expected unknown id to not be found")
	}
}

// TestDeleteEntity_RemovesFromMap pins tx.DeleteEntity evicting the deleted
// ID from the local ID-to-type map: a deleted entity must not keep resolving
// to a concrete type on a later capability annotation.
func TestDeleteEntity_RemovesFromMap(t *testing.T) {
	mock := &mockCartographerClient{
		deleteEntity: func(ctx context.Context, req *flowv1.DeleteEntityRequest) (*flowv1.DeleteEntityResponse, error) {
			return &flowv1.DeleteEntityResponse{EntityId: req.GetId()}, nil
		},
	}
	tx := newMockTx(mock)
	tx.idTypeMap.store(testUUIDEntity, "Component")
	_, err := tx.DeleteEntity(testUUIDEntity)
	if err != nil {
		t.Fatalf("DeleteEntity returned error: %v", err)
	}
	_, ok := tx.idTypeMap.resolve(testUUIDEntity)
	if ok {
		t.Error("expected e1 to be removed from map")
	}
}

// ---------------------------------------------------------------------------
// resolveOrWildcard tests
// ---------------------------------------------------------------------------

func TestIDTypeMap_ResolveOrWildcard_Found(t *testing.T) {
	m := newIDTypeMap()
	m.store("id-1", componentType)
	typ := m.resolveOrWildcard("id-1")
	if typ != componentType {
		t.Errorf("expected Component, got %q", typ)
	}
}

func TestIDTypeMap_ResolveOrWildcard_NotFound(t *testing.T) {
	m := newIDTypeMap()
	typ := m.resolveOrWildcard("unknown")
	if typ != "*" {
		t.Errorf("expected wildcard *, got %q", typ)
	}
}

// TestIDTypeMap_EmptyTypeNotStored pins resolveOrWildcard's documented
// guarantee: capability annotation falls back to the wildcard rather than
// annotating with an empty type (which fails resolution). An empty
// entity_type from a server response must not be stored, so a stored ID with
// an empty type resolves as unknown and produces "*" — never entity_type="".
func TestIDTypeMap_EmptyTypeNotStored(t *testing.T) {
	m := newIDTypeMap()
	m.store("id-1", "")
	if typ := m.resolveOrWildcard("id-1"); typ != "*" {
		t.Errorf("expected wildcard * for empty stored type, got %q", typ)
	}
	if _, ok := m.resolve("id-1"); ok {
		t.Error("expected empty-type entry not to be stored")
	}
}

// ---------------------------------------------------------------------------
// Graph read/admin method tests (SPEC R4)
// ---------------------------------------------------------------------------

// TestGraph_ExecuteCypher pins the Graph's read-only Cypher surface: the
// statement reaches the wire, nil params omit the params field, and rows are
// surfaced as flat string tuples in wire order (SPEC R2).
func TestGraph_ExecuteCypher(t *testing.T) {
	var capturedCypher string
	var capturedParams *structpb.Struct
	mock := &mockCartographerClient{
		executeCypher: func(ctx context.Context, req *flowv1.ExecuteCypherRequest) (*flowv1.ExecuteCypherResponse, error) {
			capturedCypher = req.GetCypher()
			capturedParams = req.GetParams()
			return &flowv1.ExecuteCypherResponse{
				Rows: []*flowv1.Row{
					{Values: []string{"c1", "Component"}},
					{Values: []string{"c2", "Service"}},
				},
			}, nil
		},
	}
	g := newMockGraph(mock)

	rows, err := g.ExecuteCypher("MATCH (c:Component) RETURN c", nil)
	if err != nil {
		t.Fatalf("ExecuteCypher returned error: %v", err)
	}
	if capturedCypher != "MATCH (c:Component) RETURN c" {
		t.Errorf("expected cypher statement on the wire, got %q", capturedCypher)
	}
	if capturedParams != nil {
		t.Error("expected no params field for a nil params map")
	}
	if len(rows) != 2 || rows[0][0] != "c1" || rows[0][1] != "Component" || rows[1][0] != "c2" {
		t.Errorf("expected 2 rows with flat string values in wire order, got %v", rows)
	}
}

// TestGraph_ExecuteCypher_JSONObjectParams pins the params wire shape: a
// non-nil params map must arrive as a JSON object (struct value) — the SPEC
// error-table row "ExecuteCypher params not a JSON object" names
// google.protobuf.Struct, so the SDK must never send a scalar or list.
func TestGraph_ExecuteCypher_JSONObjectParams(t *testing.T) {
	var capturedParams *structpb.Struct
	mock := &mockCartographerClient{
		executeCypher: func(ctx context.Context, req *flowv1.ExecuteCypherRequest) (*flowv1.ExecuteCypherResponse, error) {
			capturedParams = req.GetParams()
			return &flowv1.ExecuteCypherResponse{}, nil
		},
	}
	g := newMockGraph(mock)

	_, err := g.ExecuteCypher("MATCH (c:Component {name:$name}) RETURN c", map[string]any{"name": "auth"})
	if err != nil {
		t.Fatalf("ExecuteCypher returned error: %v", err)
	}
	if capturedParams == nil {
		t.Fatal("expected params field to be set for a non-nil params map")
	}
	if got := capturedParams.Fields["name"].GetStringValue(); got != "auth" {
		t.Errorf("expected params.name=auth, got %q", got)
	}
}

// TestGraph_SearchNeighbors pins the Graph's vector-search surface: the
// embedding, entityType, and topK reach the wire, no transactionId is
// attached (main read), and results surface as SearchResult with the raw
// distance and populate the ID-to-type cache (SPEC R2/R3).
func TestGraph_SearchNeighbors(t *testing.T) {
	var capturedReq *flowv1.SearchNeighborsRequest
	mock := &mockCartographerClient{
		searchNeighbors: func(
			ctx context.Context, req *flowv1.SearchNeighborsRequest,
		) (*flowv1.SearchNeighborsResponse, error) {
			capturedReq = req
			return &flowv1.SearchNeighborsResponse{
				Results: []*flowv1.SearchNeighborResult{
					{EntityId: "e1", EntityType: componentType, Properties: map[string]string{"name": "auth"}, Distance: 0.25},
				},
			}, nil
		},
	}
	g := newMockGraph(mock)

	results, err := g.SearchNeighbors([]float32{0.1, 0.2}, "Component", 5)
	if err != nil {
		t.Fatalf("SearchNeighbors returned error: %v", err)
	}
	if capturedReq.GetTransactionId() != "" {
		t.Errorf("expected no transactionId on a main-graph read, got %q", capturedReq.GetTransactionId())
	}
	if capturedReq.GetEntityType() != componentType {
		t.Errorf("expected entityType %s on the wire, got %q", componentType, capturedReq.GetEntityType())
	}
	if capturedReq.GetTopK() != 5 {
		t.Errorf("expected topK 5 on the wire, got %d", capturedReq.GetTopK())
	}
	if len(capturedReq.GetEmbedding()) != 2 || capturedReq.GetEmbedding()[0] != 0.1 {
		t.Errorf("expected embedding [0.1 0.2] on the wire, got %v", capturedReq.GetEmbedding())
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "e1" || results[0].Type != componentType || results[0].Distance != 0.25 {
		t.Errorf("unexpected SearchResult: %+v", results[0])
	}
	if results[0].Properties["name"] != "auth" {
		t.Errorf("expected properties preserved on SearchResult, got %v", results[0].Properties)
	}
	if typ, ok := g.idTypeMap.resolve("e1"); !ok || typ != componentType {
		t.Errorf("expected search result to seed the ID-to-type cache with %s, got %q (ok=%v)", componentType, typ, ok)
	}
}

// TestGraph_FullTextSearch pins the Graph's full-text surface: query and
// entityType reach the wire, and results surface as domain Entities and seed
// the ID-to-type cache (SPEC R2/R3).
func TestGraph_FullTextSearch(t *testing.T) {
	var capturedReq *flowv1.FullTextSearchRequest
	mock := &mockCartographerClient{
		fullTextSearch: func(ctx context.Context, req *flowv1.FullTextSearchRequest) (*flowv1.FullTextSearchResponse, error) {
			capturedReq = req
			return &flowv1.FullTextSearchResponse{
				Results: []*flowv1.Entity{
					{EntityId: "e1", EntityType: componentType, Properties: map[string]string{"name": "auth service"}},
				},
			}, nil
		},
	}
	g := newMockGraph(mock)

	entities, err := g.FullTextSearch("auth service", "Component")
	if err != nil {
		t.Fatalf("FullTextSearch returned error: %v", err)
	}
	if capturedReq.GetQuery() != "auth service" {
		t.Errorf("expected query on the wire, got %q", capturedReq.GetQuery())
	}
	if capturedReq.GetEntityType() != componentType {
		t.Errorf("expected entityType %s on the wire, got %q", componentType, capturedReq.GetEntityType())
	}
	if len(entities) != 1 || entities[0].ID != "e1" || entities[0].Type != componentType {
		t.Errorf("unexpected FullTextSearch results: %+v", entities)
	}
	if entities[0].Properties["name"] != "auth service" {
		t.Errorf("expected properties preserved, got %v", entities[0].Properties)
	}
	if typ, ok := g.idTypeMap.resolve("e1"); !ok || typ != componentType {
		t.Errorf("expected search result to seed the ID-to-type cache with %s, got %q (ok=%v)", componentType, typ, ok)
	}
}

// TestGraph_ListEntities pins the Graph's listing surface: entityType reaches
// the wire, no transactionId is attached (main read), and the page carries the
// next page token and seeds the ID-to-type cache (SPEC R2/R3).
func TestGraph_ListEntities(t *testing.T) {
	var capturedReq *flowv1.ListEntitiesRequest
	mock := &mockCartographerClient{
		listEntities: func(ctx context.Context, req *flowv1.ListEntitiesRequest) (*flowv1.ListEntitiesResponse, error) {
			capturedReq = req
			return &flowv1.ListEntitiesResponse{
				Entities:      []*flowv1.Entity{{EntityId: "e1", EntityType: componentType}},
				NextPageToken: "tok-2",
			}, nil
		},
	}
	g := newMockGraph(mock)

	page, err := g.ListEntities("Component")
	if err != nil {
		t.Fatalf("ListEntities returned error: %v", err)
	}
	if capturedReq.GetEntityType() != componentType {
		t.Errorf("expected entityType %s on the wire, got %q", componentType, capturedReq.GetEntityType())
	}
	if capturedReq.GetTransactionId() != "" {
		t.Errorf("expected no transactionId on a main-graph read, got %q", capturedReq.GetTransactionId())
	}
	if page.NextPageToken != "tok-2" {
		t.Errorf("expected next page token tok-2, got %q", page.NextPageToken)
	}
	if len(page.Entities) != 1 || page.Entities[0].ID != "e1" {
		t.Errorf("unexpected entity page: %+v", page.Entities)
	}
	if typ, ok := g.idTypeMap.resolve("e1"); !ok || typ != componentType {
		t.Errorf("expected list result to seed the ID-to-type cache with %s, got %q (ok=%v)", componentType, typ, ok)
	}
}

// TestGraph_ListEntities_PaginationOptions pins the R4 functional-option
// branches: WithPageSize and WithPageToken populate the request's page_size
// and page_token (SPEC R4 example: ListEntities with pageSize + pageToken).
func TestGraph_ListEntities_PaginationOptions(t *testing.T) {
	var capturedReq *flowv1.ListEntitiesRequest
	mock := &mockCartographerClient{
		listEntities: func(ctx context.Context, req *flowv1.ListEntitiesRequest) (*flowv1.ListEntitiesResponse, error) {
			capturedReq = req
			return &flowv1.ListEntitiesResponse{NextPageToken: "tok-3"}, nil
		},
	}
	g := newMockGraph(mock)

	page, err := g.ListEntities("Component", WithPageSize(50), WithPageToken("tok-2"))
	if err != nil {
		t.Fatalf("ListEntities returned error: %v", err)
	}
	if capturedReq.GetPageSize() != 50 {
		t.Errorf("expected page_size 50 on the wire, got %d", capturedReq.GetPageSize())
	}
	if capturedReq.GetPageToken() != "tok-2" {
		t.Errorf("expected page_token tok-2 on the wire, got %q", capturedReq.GetPageToken())
	}
	// The R4 example chains the next page using page.NextPageToken.
	if page.NextPageToken != "tok-3" {
		t.Errorf("expected next page token tok-3, got %q", page.NextPageToken)
	}
}

// TestGraph_ListEntities_DefaultOmitted pins the SPEC R4 default pageSize
// branch: with no options the request carries pageSize 0 (treated as omitted
// by the server, which defaults to 1000) and an empty page token.
func TestGraph_ListEntities_DefaultOmitted(t *testing.T) {
	var capturedReq *flowv1.ListEntitiesRequest
	mock := &mockCartographerClient{
		listEntities: func(ctx context.Context, req *flowv1.ListEntitiesRequest) (*flowv1.ListEntitiesResponse, error) {
			capturedReq = req
			return &flowv1.ListEntitiesResponse{}, nil
		},
	}
	g := newMockGraph(mock)

	if _, err := g.ListEntities("Component"); err != nil {
		t.Fatalf("ListEntities returned error: %v", err)
	}
	if capturedReq.GetPageSize() != 0 {
		t.Errorf("expected omitted pageSize (0) on the wire, got %d", capturedReq.GetPageSize())
	}
	if capturedReq.GetPageToken() != "" {
		t.Errorf("expected empty page token on the wire, got %q", capturedReq.GetPageToken())
	}
}

// TestGraph_Sync pins graph.Sync's wire mapping: a SyncRequest is sent and
// the RPC result is surfaced (SPEC R2 administrative path).
func TestGraph_Sync(t *testing.T) {
	var called bool
	mock := &mockCartographerClient{
		sync: func(ctx context.Context, req *flowv1.SyncRequest) (*flowv1.SyncResponse, error) {
			called = true
			return &flowv1.SyncResponse{}, nil
		},
	}
	g := newMockGraph(mock)

	if err := g.Sync(); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}
	if !called {
		t.Error("expected the Sync RPC to be invoked")
	}
}

// TestGraph_Sync_PropagatesRejection pins the SDK surfacing the server's
// rejection verbatim (SPEC R2 error-table row "Remote not configured" →
// FAILED_PRECONDITION).
func TestGraph_Sync_PropagatesRejection(t *testing.T) {
	mock := &mockCartographerClient{
		sync: func(ctx context.Context, req *flowv1.SyncRequest) (*flowv1.SyncResponse, error) {
			return nil, status.Error(codes.FailedPrecondition, "no remote configured")
		},
	}
	g := newMockGraph(mock)

	err := g.Sync()
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition surfaced from Sync, got %v (%v)", status.Code(err), err)
	}
}

// fakeExportStream is a minimal grpc.ServerStreamingClient[ExportGraphResponse]
// that serves pre-set byte chunks then io.EOF.
type fakeExportStream struct {
	ctx    context.Context
	chunks [][]byte
}

func (s *fakeExportStream) Recv() (*flowv1.ExportGraphResponse, error) {
	if len(s.chunks) == 0 {
		return nil, io.EOF
	}
	chunk := s.chunks[0]
	s.chunks = s.chunks[1:]
	return &flowv1.ExportGraphResponse{Chunk: chunk}, nil
}
func (s *fakeExportStream) Header() (metadata.MD, error) { return nil, nil }
func (s *fakeExportStream) Trailer() metadata.MD         { return nil }
func (s *fakeExportStream) CloseSend() error             { return nil }
func (s *fakeExportStream) Context() context.Context     { return s.ctx }
func (s *fakeExportStream) SendMsg(any) error            { return nil }
func (s *fakeExportStream) RecvMsg(any) error            { return nil }

// TestGraph_ExportGraph pins graph.ExportGraph's wire mapping and stream
// semantics (SPEC R2): the format reaches the request, the session per-call
// timeout bounds the whole stream lifetime, chunks arrive in order, and the
// stream ends with io.EOF.
func TestGraph_ExportGraph(t *testing.T) {
	var capturedFormat string
	var capturedStreamCtx context.Context
	mock := &mockCartographerClient{
		exportGraph: func(
			ctx context.Context, req *flowv1.ExportGraphRequest,
		) (grpc.ServerStreamingClient[flowv1.ExportGraphResponse], error) {
			capturedFormat = req.GetFormat()
			capturedStreamCtx = ctx
			return &fakeExportStream{ctx: ctx, chunks: [][]byte{[]byte(`{"nodes":[`), []byte(`]}`)}}, nil
		},
	}
	g := newMockGraph(mock)
	g.session.timeout = 5 * time.Second

	stream, err := g.ExportGraph("json")
	if err != nil {
		t.Fatalf("ExportGraph returned error: %v", err)
	}
	if capturedFormat != "json" {
		t.Errorf("expected format json on the request, got %q", capturedFormat)
	}
	// The session timeout must bound the stream: grpc-go pins the deadline
	// context passed to the streaming RPC for its whole lifetime.
	dl, ok := capturedStreamCtx.Deadline()
	if !ok {
		t.Fatal("expected the stream context to carry the session timeout deadline")
	}
	if remaining := time.Until(dl); remaining <= 0 || remaining > 5*time.Second {
		t.Errorf("expected the stream deadline ~5s out, got %v remaining", remaining)
	}

	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("first Recv() returned error: %v", err)
	}
	if string(first.GetChunk()) != `{"nodes":[` {
		t.Errorf("unexpected first chunk %q", first.GetChunk())
	}
	second, err := stream.Recv()
	if err != nil {
		t.Fatalf("second Recv() returned error: %v", err)
	}
	if string(second.GetChunk()) != `]}` {
		t.Errorf("unexpected second chunk %q", second.GetChunk())
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("expected io.EOF at the end of the stream, got %v", err)
	}
}

// TestGraph_BeginTransaction pins graph.BeginTransaction (SPEC R4): the
// requested WithTimeout reaches the wire, and the returned handle is wired to
// the session and the Graph's shared ID-to-type cache so transaction writes
// inject the transaction ID and resolve capability types against the same
// cache the graph reads populate.
func TestGraph_BeginTransaction(t *testing.T) {
	var capturedTimeout time.Duration
	var hadTimeout bool
	mock := &mockCartographerClient{
		beginTx: func(ctx context.Context, req *flowv1.BeginTransactionRequest) (*flowv1.BeginTransactionResponse, error) {
			if req.GetTimeout() != nil {
				hadTimeout = true
				capturedTimeout = req.GetTimeout().AsDuration()
			}
			return &flowv1.BeginTransactionResponse{
				TransactionId:  testUUIDTx,
				AppliedTimeout: durationpb.New(48 * time.Hour),
			}, nil
		},
	}
	g := newMockGraph(mock)

	tx, err := g.BeginTransaction(WithTimeout(48 * time.Hour))
	if err != nil {
		t.Fatalf("BeginTransaction returned error: %v", err)
	}
	if !hadTimeout || capturedTimeout != 48*time.Hour {
		t.Errorf("expected the requested 48h timeout on the wire, got %v (present=%v)", capturedTimeout, hadTimeout)
	}
	if tx.id != testUUIDTx {
		t.Errorf("expected tx handle ID %s, got %q", testUUIDTx, tx.id)
	}
	if tx.session != g.session {
		t.Error("expected the tx handle to share the graph's session")
	}
	if tx.idTypeMap != g.idTypeMap {
		t.Error("expected the tx handle to share the graph's ID-to-type cache")
	}
	// The response's applied_timeout ("the value actually granted", SPEC R2)
	// must be surfaced on the handle rather than dropped.
	if got := tx.AppliedTimeout(); got != 48*time.Hour {
		t.Errorf("expected the granted 48h applied timeout on the handle, got %v", got)
	}
}

// TestGraph_BeginTransaction_NoTimeoutOmitted pins the omitted-timeout
// branch: without WithTimeout the request carries no timeout field and the
// server applies its default transaction timeout.
func TestGraph_BeginTransaction_NoTimeoutOmitted(t *testing.T) {
	var hadTimeout bool
	mock := &mockCartographerClient{
		beginTx: func(ctx context.Context, req *flowv1.BeginTransactionRequest) (*flowv1.BeginTransactionResponse, error) {
			hadTimeout = req.GetTimeout() != nil
			return &flowv1.BeginTransactionResponse{TransactionId: testUUIDTx}, nil
		},
	}
	g := newMockGraph(mock)

	if _, err := g.BeginTransaction(); err != nil {
		t.Fatalf("BeginTransaction returned error: %v", err)
	}
	if hadTimeout {
		t.Error("expected no timeout field on the wire when WithTimeout is omitted (server default)")
	}
}

// TestGraph_BeginTransaction_TimeoutPassedVerbatim pins the R9 WithTimeout
// branch end to end: the requested duration is passed to the wire verbatim
// (no silent capping), and a request exceeding the 7-day hard maximum is
// rejected by the Cartographer with INVALID_ARGUMENT which the SDK surfaces.
func TestGraph_BeginTransaction_TimeoutPassedVerbatim(t *testing.T) {
	var captured time.Duration
	mock := &mockCartographerClient{
		beginTx: func(ctx context.Context, req *flowv1.BeginTransactionRequest) (*flowv1.BeginTransactionResponse, error) {
			captured = req.GetTimeout().AsDuration()
			return nil, status.Error(codes.InvalidArgument, "timeout exceeds the 7-day maximum")
		},
	}
	g := newMockGraph(mock)

	_, err := g.BeginTransaction(WithTimeout(10 * 24 * time.Hour))
	if captured != 10*24*time.Hour {
		t.Fatalf("expected the requested 10d timeout on the wire verbatim (no capping), got %v", captured)
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument surfaced for an oversized timeout, got %v (%v)", status.Code(err), err)
	}
}

// TestGraph_BeginTransaction_NonPositiveTimeoutPassed pins the sibling half of
// the R2/R9 WithTimeout contract on the Begin path: a zero or negative
// WithTimeout is passed to the wire verbatim (no silent default-substitution),
// so the SPEC error-table row "Invalid transaction timeout duration" →
// INVALID_ARGUMENT is reachable on BeginTransaction exactly as on
// ExtendTimeout (TestTx_ExtendTimeout / TestTx_ExtendTimeout_RejectsOversized
// pin the sibling path).
func TestGraph_BeginTransaction_NonPositiveTimeoutPassed(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Hour} {
		t.Run(d.String(), func(t *testing.T) {
			var captured time.Duration
			var hadTimeout bool
			mock := &mockCartographerClient{
				beginTx: func(ctx context.Context, req *flowv1.BeginTransactionRequest) (*flowv1.BeginTransactionResponse, error) {
					hadTimeout = req.GetTimeout() != nil
					if hadTimeout {
						captured = req.GetTimeout().AsDuration()
					}
					return nil, status.Error(codes.InvalidArgument, "duration must be positive")
				},
			}
			g := newMockGraph(mock)

			_, err := g.BeginTransaction(WithTimeout(d))
			if !hadTimeout || captured != d {
				t.Fatalf("expected the requested %v timeout on the wire verbatim, got %v (present=%v)", d, captured, hadTimeout)
			}
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument surfaced for a non-positive timeout, got %v (%v)", status.Code(err), err)
			}
		})
	}
}
