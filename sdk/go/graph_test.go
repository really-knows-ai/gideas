package flow

import (
	"context"
	"errors"
	"io"
	"math"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/durationpb"
)

const metadataEntityTypeKey = "x-flow-entity-type"

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
	pullFromRemote  func(ctx context.Context, req *flowv1.PullFromRemoteRequest) (*flowv1.PullFromRemoteResponse, error)
	pushToRemote    func(ctx context.Context, req *flowv1.PushToRemoteRequest) (*flowv1.PushToRemoteResponse, error)
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
	return nil, errors.New("ExportGraph mock function not set")
}
func (m *mockCartographerClient) PullFromRemote(
	ctx context.Context, req *flowv1.PullFromRemoteRequest, opts ...grpc.CallOption,
) (*flowv1.PullFromRemoteResponse, error) {
	if m.pullFromRemote != nil {
		return m.pullFromRemote(ctx, req)
	}
	return &flowv1.PullFromRemoteResponse{}, nil
}
func (m *mockCartographerClient) PushToRemote(
	ctx context.Context, req *flowv1.PushToRemoteRequest, opts ...grpc.CallOption,
) (*flowv1.PushToRemoteResponse, error) {
	if m.pushToRemote != nil {
		return m.pushToRemote(ctx, req)
	}
	return &flowv1.PushToRemoteResponse{}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newMockGraph(mock *mockCartographerClient) *Graph {
	ctx, cancel := context.WithCancel(context.Background())
	sess := &session{
		Cartographer: mock,
		ctx:          ctx,
		cancel:       cancel,
	}
	return &Graph{session: sess, idTypeMap: newIDTypeMap()}
}

func TestGetGraphReturnsHandle(t *testing.T) {
	c := &Client{session: &session{}}
	g, err := c.GetGraph()
	if err != nil {
		t.Fatalf("GetGraph() returned error: %v", err)
	}
	if g == nil {
		t.Fatal("GetGraph() returned nil")
	}
}

func TestGetGraph_NilSession(t *testing.T) {
	c := &Client{}
	_, err := c.GetGraph()
	if err == nil {
		t.Fatal("expected error for nil session")
	}
}

func TestExecuteCypher(t *testing.T) {
	mock := &mockCartographerClient{
		executeCypher: func(ctx context.Context, req *flowv1.ExecuteCypherRequest) (*flowv1.ExecuteCypherResponse, error) {
			if req.GetCypher() == "" {
				t.Error("expected non-empty cypher")
			}
			return &flowv1.ExecuteCypherResponse{}, nil
		},
	}
	g := newMockGraph(mock)
	_, err := g.ExecuteCypher("MATCH (c:"+componentType+") RETURN c", nil)
	if err != nil {
		t.Fatalf("ExecuteCypher returned error: %v", err)
	}
}

func TestSearchNeighbors(t *testing.T) {
	mock := &mockCartographerClient{
		searchNeighbors: func(ctx context.Context,
			req *flowv1.SearchNeighborsRequest,
		) (*flowv1.SearchNeighborsResponse, error) {
			return &flowv1.SearchNeighborsResponse{}, nil
		},
	}
	g := newMockGraph(mock)
	_, err := g.SearchNeighbors([]float32{0.1, 0.2}, componentType, 10)
	if err != nil {
		t.Fatalf("SearchNeighbors returned error: %v", err)
	}
}

func TestSearchNeighbors_NaNRejection(t *testing.T) {
	g := newMockGraph(&mockCartographerClient{})
	_, err := g.SearchNeighbors([]float32{float32(math.NaN())}, componentType, 10)
	if err == nil {
		t.Fatal("expected error for NaN embedding")
	}
}

func TestFullTextSearch(t *testing.T) {
	mock := &mockCartographerClient{
		fullTextSearch: func(ctx context.Context, req *flowv1.FullTextSearchRequest) (*flowv1.FullTextSearchResponse, error) {
			if req.GetQuery() == "" {
				t.Error("expected non-empty query")
			}
			return &flowv1.FullTextSearchResponse{}, nil
		},
	}
	g := newMockGraph(mock)
	_, err := g.FullTextSearch("test query", componentType)
	if err != nil {
		t.Fatalf("FullTextSearch returned error: %v", err)
	}
}

func TestListEntities(t *testing.T) {
	mock := &mockCartographerClient{
		listEntities: func(ctx context.Context, req *flowv1.ListEntitiesRequest) (*flowv1.ListEntitiesResponse, error) {
			if req.GetEntityType() != componentType {
				t.Errorf("expected entity type Component, got %s", req.GetEntityType())
			}
			return &flowv1.ListEntitiesResponse{}, nil
		},
	}
	g := newMockGraph(mock)
	_, err := g.ListEntities("Component")
	if err != nil {
		t.Fatalf("ListEntities returned error: %v", err)
	}
}

func TestListEntities_Pagination(t *testing.T) {
	var capturedPageSize int32
	var capturedPageToken string
	mock := &mockCartographerClient{
		listEntities: func(ctx context.Context, req *flowv1.ListEntitiesRequest) (*flowv1.ListEntitiesResponse, error) {
			capturedPageSize = req.GetPageSize()
			capturedPageToken = req.GetPageToken()
			return &flowv1.ListEntitiesResponse{
				Entities:      []*flowv1.Entity{},
				NextPageToken: "next-token",
			}, nil
		},
	}
	g := newMockGraph(mock)

	// First page
	page, err := g.ListEntities(componentType, WithPageSize(50))
	if err != nil {
		t.Fatalf("ListEntities returned error: %v", err)
	}
	if capturedPageSize != 50 {
		t.Errorf("expected page size 50, got %d", capturedPageSize)
	}
	if page.NextPageToken != "next-token" {
		t.Errorf("expected next page token, got %q", page.NextPageToken)
	}

	// Second page
	_, err = g.ListEntities(componentType, WithPageSize(50), WithPageToken("next-token"))
	if err != nil {
		t.Fatalf("ListEntities returned error: %v", err)
	}
	if capturedPageToken != "next-token" {
		t.Errorf("expected page token 'next-token', got %q", capturedPageToken)
	}
}

func TestListEntities_EmptyResult(t *testing.T) {
	mock := &mockCartographerClient{
		listEntities: func(ctx context.Context, req *flowv1.ListEntitiesRequest) (*flowv1.ListEntitiesResponse, error) {
			return &flowv1.ListEntitiesResponse{}, nil
		},
	}
	g := newMockGraph(mock)
	page, err := g.ListEntities("NonExistent")
	if err != nil {
		t.Fatalf("ListEntities returned error: %v", err)
	}
	if len(page.Entities) != 0 {
		t.Errorf("expected empty entities, got %d", len(page.Entities))
	}
	if page.NextPageToken != "" {
		t.Errorf("expected empty next page token, got %q", page.NextPageToken)
	}
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
	g := newMockGraph(mock)
	props := map[string]string{"name": "test"}
	entity, err := g.CreateEntity(componentType, nil, props, nil)
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
	g := newMockGraph(mock)
	entity, err := g.CreateEntity("Component", nil, nil, nil)
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

func TestCreateEntity_PopulatesMap(t *testing.T) {
	mock := &mockCartographerClient{
		createEntity: func(ctx context.Context, req *flowv1.CreateEntityRequest) (*flowv1.CreateEntityResponse, error) {
			return &flowv1.CreateEntityResponse{
				EntityId:   "entity-1",
				EntityType: "Component",
			}, nil
		},
	}
	g := newMockGraph(mock)
	_, err := g.CreateEntity("Component", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateEntity returned error: %v", err)
	}
	typ, ok := g.idTypeMap.resolve("entity-1")
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
	g := newMockGraph(mock)
	g.idTypeMap.store("entity-1", "Component")
	entity, err := g.UpdateEntity("entity-1", map[string]string{"name": "updated"}, nil)
	if err != nil {
		t.Fatalf("UpdateEntity returned error: %v", err)
	}
	if entity.ID != "entity-1" {
		t.Errorf("expected entity ID entity-1, got %s", entity.ID)
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
	g := newMockGraph(mock)
	g.idTypeMap.store("entity-1", "Component")
	entity, err := g.DeleteEntity("entity-1")
	if err != nil {
		t.Fatalf("DeleteEntity returned error: %v", err)
	}
	if entity.ID != "entity-1" {
		t.Errorf("expected entity ID entity-1, got %s", entity.ID)
	}
	// Verify removed from map
	_, ok := g.idTypeMap.resolve("entity-1")
	if ok {
		t.Error("expected entity-1 to be removed from map after delete")
	}
}

func TestCreateEdge(t *testing.T) {
	mock := &mockCartographerClient{
		createEdge: func(ctx context.Context, req *flowv1.CreateEdgeRequest) (*flowv1.CreateEdgeResponse, error) {
			return &flowv1.CreateEdgeResponse{
				EdgeId:       "edge-1",
				EdgeType:     "DEPENDS_ON",
				FromEntityId: req.GetFromEntityId(),
				ToEntityId:   req.GetToEntityId(),
			}, nil
		},
	}
	g := newMockGraph(mock)
	g.idTypeMap.store("from-1", "Component")
	edge, err := g.CreateEdge("DEPENDS_ON", "from-1", "to-1", nil)
	if err != nil {
		t.Fatalf("CreateEdge returned error: %v", err)
	}
	if edge.ID != "edge-1" {
		t.Errorf("expected edge ID edge-1, got %s", edge.ID)
	}
	if edge.FromEntityID != "from-1" {
		t.Errorf("expected from entity from-1, got %s", edge.FromEntityID)
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
	g := newMockGraph(mock)
	edge, err := g.DeleteEdge("edge-1")
	if err != nil {
		t.Fatalf("DeleteEdge returned error: %v", err)
	}
	if edge.ID != "edge-1" {
		t.Errorf("expected edge ID edge-1, got %s", edge.ID)
	}
}

func TestBeginTransaction(t *testing.T) {
	mock := &mockCartographerClient{
		beginTx: func(ctx context.Context, req *flowv1.BeginTransactionRequest) (*flowv1.BeginTransactionResponse, error) {
			return &flowv1.BeginTransactionResponse{
				TransactionId: "tx-1",
			}, nil
		},
	}
	g := newMockGraph(mock)
	tx, err := g.BeginTransaction()
	if err != nil {
		t.Fatalf("BeginTransaction returned error: %v", err)
	}
	if tx.ID() != "tx-1" {
		t.Errorf("expected tx ID tx-1, got %s", tx.ID())
	}
}

func TestPullFromRemote(t *testing.T) {
	called := false
	mock := &mockCartographerClient{
		pullFromRemote: func(ctx context.Context, req *flowv1.PullFromRemoteRequest) (*flowv1.PullFromRemoteResponse, error) {
			called = true
			return &flowv1.PullFromRemoteResponse{}, nil
		},
	}
	g := newMockGraph(mock)
	err := g.PullFromRemote()
	if err != nil {
		t.Fatalf("PullFromRemote returned error: %v", err)
	}
	if !called {
		t.Error("expected PullFromRemote to be called")
	}
}

func TestPushToRemote(t *testing.T) {
	called := false
	mock := &mockCartographerClient{
		pushToRemote: func(ctx context.Context, req *flowv1.PushToRemoteRequest) (*flowv1.PushToRemoteResponse, error) {
			called = true
			return &flowv1.PushToRemoteResponse{}, nil
		},
	}
	g := newMockGraph(mock)
	err := g.PushToRemote()
	if err != nil {
		t.Fatalf("PushToRemote returned error: %v", err)
	}
	if !called {
		t.Error("expected PushToRemote to be called")
	}
}

func TestExportGraph_NilSession(t *testing.T) {
	g := &Graph{}
	_, err := g.ExportGraph("json")
	if err == nil {
		t.Fatal("expected error for nil session")
	}
}

func TestExportGraph_Success(t *testing.T) {
	mock := &mockCartographerClient{
		exportGraph: func(ctx context.Context, req *flowv1.ExportGraphRequest,
		) (grpc.ServerStreamingClient[flowv1.ExportGraphResponse], error) {
			if req.GetFormat() != "json" {
				t.Errorf("expected format json, got %q", req.GetFormat())
			}
			return &mockStream{
				chunks: []*flowv1.ExportGraphResponse{
					{Chunk: []byte(`{"nodes":`)},
					{Chunk: []byte(`[{"id":"1"}]`)},
					{Chunk: []byte(`}`)},
				},
			}, nil
		},
	}
	g := newMockGraph(mock)
	stream, err := g.ExportGraph("json")
	if err != nil {
		t.Fatalf("ExportGraph returned error: %v", err)
	}
	defer stream.Stop()

	var got []byte
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv returned error: %v", err)
		}
		got = append(got, chunk.Data...)
	}
	expected := `{"nodes":[{"id":"1"}]}`
	if string(got) != expected {
		t.Errorf("expected %q, got %q", expected, string(got))
	}
}

func TestGraphMethodsWithNilSession(t *testing.T) {
	g := &Graph{}
	tests := []struct {
		name string
		fn   func() error
	}{
		{"ExecuteCypher", func() error { _, err := g.ExecuteCypher("", nil); return err }},
		{"SearchNeighbors", func() error { _, err := g.SearchNeighbors(nil, "", 0); return err }},
		{"FullTextSearch", func() error { _, err := g.FullTextSearch("", ""); return err }},
		{"ListEntities", func() error { _, err := g.ListEntities(""); return err }},
		{"CreateEntity", func() error { _, err := g.CreateEntity("", nil, nil, nil); return err }},
		{"UpdateEntity", func() error { _, err := g.UpdateEntity("", nil, nil); return err }},
		{"DeleteEntity", func() error { _, err := g.DeleteEntity(""); return err }},
		{"CreateEdge", func() error { _, err := g.CreateEdge("", "", "", nil); return err }},
		{"DeleteEdge", func() error { _, err := g.DeleteEdge(""); return err }},
		{"BeginTransaction", func() error { _, err := g.BeginTransaction(); return err }},
		{"PullFromRemote", func() error { return g.PullFromRemote() }},
		{"PushToRemote", func() error { return g.PushToRemote() }},
		{"ExportGraph", func() error { _, err := g.ExportGraph("json"); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.fn(); err == nil {
				t.Error("expected error for nil session")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ID-to-type map tests
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

func TestIDTypeMap_ResolveUnknown(t *testing.T) {
	m := newIDTypeMap()
	_, ok := m.resolve("unknown")
	if ok {
		t.Error("expected unknown id to not be found")
	}
}

func TestListEntities_PopulatesMap(t *testing.T) {
	mock := &mockCartographerClient{
		listEntities: func(ctx context.Context, req *flowv1.ListEntitiesRequest) (*flowv1.ListEntitiesResponse, error) {
			return &flowv1.ListEntitiesResponse{
				Entities: []*flowv1.Entity{
					{EntityId: "e1", EntityType: "Component"},
					{EntityId: "e2", EntityType: "Service"},
				},
			}, nil
		},
	}
	g := newMockGraph(mock)
	_, err := g.ListEntities(componentType)
	if err != nil {
		t.Fatalf("ListEntities returned error: %v", err)
	}
	typ, ok := g.idTypeMap.resolve("e1")
	if !ok || typ != componentType {
		t.Errorf("expected Component for e1, got %q (ok=%v)", typ, ok)
	}
	typ, ok = g.idTypeMap.resolve("e2")
	if !ok || typ != "Service" {
		t.Errorf("expected Service for e2, got %q (ok=%v)", typ, ok)
	}
}

func TestDeleteEntity_RemovesFromMap(t *testing.T) {
	mock := &mockCartographerClient{
		deleteEntity: func(ctx context.Context, req *flowv1.DeleteEntityRequest) (*flowv1.DeleteEntityResponse, error) {
			return &flowv1.DeleteEntityResponse{EntityId: req.GetId()}, nil
		},
	}
	g := newMockGraph(mock)
	g.idTypeMap.store("e1", "Component")
	_, err := g.DeleteEntity("e1")
	if err != nil {
		t.Fatalf("DeleteEntity returned error: %v", err)
	}
	_, ok := g.idTypeMap.resolve("e1")
	if ok {
		t.Error("expected e1 to be removed from map")
	}
}

// ---------------------------------------------------------------------------
// resolveOrWildcard tests
// ---------------------------------------------------------------------------

func TestIDTypeMap_ResolveOrWildcard_Found(t *testing.T) {
	m := newIDTypeMap()
	m.store("id-1", "Component")
	typ := m.resolveOrWildcard("id-1")
	if typ != "Component" {
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

// ---------------------------------------------------------------------------
// Capability annotation tests (Graph write methods with unknown entity IDs)
// ---------------------------------------------------------------------------

//nolint:dupl // Graph and Transaction wildcard metadata tests share structure.
func TestGraphUpdateEntity_UnknownIDSendsWildcard(t *testing.T) {
	var capturedKey, capturedValue string
	mock := &mockCartographerClient{
		updateEntity: func(ctx context.Context, req *flowv1.UpdateEntityRequest) (*flowv1.UpdateEntityResponse, error) {
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatal("no outgoing metadata")
			}
			vals := md.Get(metadataEntityTypeKey)
			if len(vals) == 0 {
				t.Fatal("no x-flow-entity-type metadata")
			}
			capturedKey = metadataEntityTypeKey
			capturedValue = vals[0]
			return &flowv1.UpdateEntityResponse{EntityId: req.GetId(), EntityType: "Component"}, nil
		},
	}
	g := newMockGraph(mock)
	// entity-1 is NOT in the map -> should produce wildcard
	_, err := g.UpdateEntity("entity-1", nil, nil)
	if err != nil {
		t.Fatalf("UpdateEntity returned error: %v", err)
	}
	if capturedKey != metadataEntityTypeKey {
		t.Errorf("expected metadata key x-flow-entity-type, got %q", capturedKey)
	}
	if capturedValue != "*" {
		t.Errorf("expected wildcard *, got %q", capturedValue)
	}
}

func TestGraphDeleteEntity_UnknownIDSendsWildcard(t *testing.T) {
	var capturedKey, capturedValue string
	mock := &mockCartographerClient{
		deleteEntity: func(ctx context.Context, req *flowv1.DeleteEntityRequest) (*flowv1.DeleteEntityResponse, error) {
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatal("no outgoing metadata")
			}
			vals := md.Get(metadataEntityTypeKey)
			if len(vals) == 0 {
				t.Fatal("no x-flow-entity-type metadata")
			}
			capturedKey = metadataEntityTypeKey
			capturedValue = vals[0]
			return &flowv1.DeleteEntityResponse{EntityId: req.GetId()}, nil
		},
	}
	g := newMockGraph(mock)
	// entity-1 is NOT in the map -> should produce wildcard
	_, err := g.DeleteEntity("entity-1")
	if err != nil {
		t.Fatalf("DeleteEntity returned error: %v", err)
	}
	if capturedKey != metadataEntityTypeKey {
		t.Errorf("expected metadata key x-flow-entity-type, got %q", capturedKey)
	}
	if capturedValue != "*" {
		t.Errorf("expected wildcard *, got %q", capturedValue)
	}
}

func TestGraphCreateEdge_UnknownFromIDSendsWildcard(t *testing.T) {
	var capturedKey, capturedValue string
	mock := &mockCartographerClient{
		createEdge: func(ctx context.Context, req *flowv1.CreateEdgeRequest) (*flowv1.CreateEdgeResponse, error) {
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatal("no outgoing metadata")
			}
			vals := md.Get(metadataEntityTypeKey)
			if len(vals) == 0 {
				t.Fatal("no x-flow-entity-type metadata")
			}
			capturedKey = metadataEntityTypeKey
			capturedValue = vals[0]
			return &flowv1.CreateEdgeResponse{
				EdgeId: "edge-1", FromEntityId: req.GetFromEntityId(), ToEntityId: req.GetToEntityId(),
			}, nil
		},
	}
	g := newMockGraph(mock)
	// from-1 is NOT in the map -> should produce wildcard
	_, err := g.CreateEdge("DEPENDS_ON", "from-1", "to-1", nil)
	if err != nil {
		t.Fatalf("CreateEdge returned error: %v", err)
	}
	if capturedKey != metadataEntityTypeKey {
		t.Errorf("expected metadata key x-flow-entity-type, got %q", capturedKey)
	}
	if capturedValue != "*" {
		t.Errorf("expected wildcard *, got %q", capturedValue)
	}
}

func TestGraphDeleteEdge_SendsWildcardAndKey(t *testing.T) {
	var capturedKey, capturedValue string
	mock := &mockCartographerClient{
		deleteEdge: func(ctx context.Context, req *flowv1.DeleteEdgeRequest) (*flowv1.DeleteEdgeResponse, error) {
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatal("no outgoing metadata")
			}
			vals := md.Get(metadataEntityTypeKey)
			if len(vals) == 0 {
				t.Fatal("no x-flow-entity-type metadata")
			}
			capturedKey = metadataEntityTypeKey
			capturedValue = vals[0]
			return &flowv1.DeleteEdgeResponse{
				EdgeId: req.GetId(), EdgeType: "DEPENDS_ON",
			}, nil
		},
	}
	g := newMockGraph(mock)
	_, err := g.DeleteEdge("edge-1")
	if err != nil {
		t.Fatalf("DeleteEdge returned error: %v", err)
	}
	if capturedKey != metadataEntityTypeKey {
		t.Errorf("expected metadata key x-flow-entity-type, got %q", capturedKey)
	}
	if capturedValue != "*" {
		t.Errorf("expected wildcard *, got %q", capturedValue)
	}
}

// ---------------------------------------------------------------------------
// ExportStream tests
// ---------------------------------------------------------------------------

// mockStream implements grpc.ServerStreamingClient for testing.
type mockStream struct {
	grpc.ClientStream
	chunks []*flowv1.ExportGraphResponse
	pos    int
	err    error
}

func (s *mockStream) Recv() (*flowv1.ExportGraphResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.pos >= len(s.chunks) {
		return nil, io.EOF // stream end
	}
	chunk := s.chunks[s.pos]
	s.pos++
	return chunk, nil
}

func TestExportStream_Recv(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := &mockStream{
		chunks: []*flowv1.ExportGraphResponse{
			{Chunk: []byte("chunk1")},
			{Chunk: []byte("chunk2")},
		},
	}
	es := newExportStream(ctx, cancel, stream)

	chunk, err := es.Recv()
	if err != nil {
		t.Fatalf("Recv returned error: %v", err)
	}
	if string(chunk.Data) != "chunk1" {
		t.Errorf("expected chunk1, got %s", string(chunk.Data))
	}

	chunk, err = es.Recv()
	if err != nil {
		t.Fatalf("Recv returned error: %v", err)
	}
	if string(chunk.Data) != "chunk2" {
		t.Errorf("expected chunk2, got %s", string(chunk.Data))
	}

	// Stop the stream
	es.Stop()
}

func TestExportStream_Stop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stream := &mockStream{chunks: []*flowv1.ExportGraphResponse{{Chunk: []byte("data")}}}
	es := newExportStream(ctx, cancel, stream)

	es.Stop()
	// After stop, the context is cancelled
	if ctx.Err() == nil {
		t.Error("expected context to be cancelled after Stop")
	}
}

// ---------------------------------------------------------------------------
// WithTimeout / ListEntitiesOption tests
// ---------------------------------------------------------------------------

func TestWithPageSize(t *testing.T) {
	opt := WithPageSize(50)
	cfg := &listEntitiesConfig{}
	opt(cfg)
	if cfg.pageSize != 50 {
		t.Errorf("expected page size 50, got %d", cfg.pageSize)
	}
}

func TestWithPageToken(t *testing.T) {
	opt := WithPageToken("token-1")
	cfg := &listEntitiesConfig{}
	opt(cfg)
	if cfg.pageToken != "token-1" {
		t.Errorf("expected token token-1, got %q", cfg.pageToken)
	}
}

func TestWithTxTimeout(t *testing.T) {
	opt := WithTxTimeout(48 * time.Hour)
	cfg := &beginTxConfig{}
	opt(cfg)
	if cfg.timeout != 48*time.Hour {
		t.Errorf("expected 48h timeout, got %v", cfg.timeout)
	}
}

func TestBeginTransaction_WithTxTimeout(t *testing.T) {
	var captured *durationpb.Duration
	mock := &mockCartographerClient{
		beginTx: func(ctx context.Context, req *flowv1.BeginTransactionRequest) (*flowv1.BeginTransactionResponse, error) {
			captured = req.GetTimeout()
			return &flowv1.BeginTransactionResponse{
				TransactionId:  "11111111-1111-4111-8111-111111111111",
				AppliedTimeout: durationpb.New(48 * time.Hour),
			}, nil
		},
	}
	g := newMockGraph(mock)
	tx, err := g.BeginTransaction(WithTxTimeout(48 * time.Hour))
	if err != nil {
		t.Fatalf("BeginTransaction returned error: %v", err)
	}
	if tx == nil {
		t.Fatal("expected non-nil transaction")
	}
	if captured == nil {
		t.Fatal("expected timeout to be set on request")
	}
	if captured.AsDuration() != 48*time.Hour {
		t.Errorf("expected timeout 48h, got %v", captured.AsDuration())
	}
	if tx.ID() != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("expected tx ID %q, got %q", "11111111-1111-4111-8111-111111111111", tx.ID())
	}
}
