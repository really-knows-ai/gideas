package flow

import (
	"context"

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
