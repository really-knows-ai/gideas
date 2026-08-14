package flow

import (
	"context"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
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
// The Graph domain object no longer exists (its methods were deleted as dead
// production surface), so the write-path tests construct a Transaction handle
// directly via newMockTx.

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
// an entry older than the TTL resolves as unknown and is excluded from
// snapshots, while re-storing an ID refreshes its TTL.
func TestIDTypeMap_TTLExpiry(t *testing.T) {
	m := newIDTypeMap()
	m.ttl = 5 * time.Millisecond
	m.store("id-1", "Component")
	m.store("id-2", "Service")
	time.Sleep(20 * time.Millisecond)
	if _, ok := m.resolve("id-1"); ok {
		t.Error("expected id-1 to expire after TTL")
	}
	if snap := m.snapshot(); len(snap) != 0 {
		t.Errorf("expected empty snapshot after TTL expiry, got %v", snap)
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
