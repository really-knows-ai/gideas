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

// metadataEntityTypesKey is the plural read-path capability annotation key
// (SPEC R3): ExecuteCypher annotates the entity types extracted from the
// Cypher statement against this plural key, falling back to the
// READ:graph/entity/* wildcard when extraction yields no labels.
const metadataEntityTypesKey = "x-flow-entity-types"

const (
	clobberedIDProp   = "clobbered-id"
	clobberedTypeProp = "ClobberedType"
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
	pullFromRemote func(ctx context.Context, req *flowv1.PullFromRemoteRequest) (*flowv1.PullFromRemoteResponse, error)
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
	g := c.GetGraph()
	if g == nil {
		t.Fatal("GetGraph() returned nil")
	}
}

func TestGetGraph_NilSession(t *testing.T) {
	c := &Client{}
	g := c.GetGraph()
	if g == nil {
		t.Fatal("GetGraph() returned nil")
	}
	// The Graph handle is always returned; nil-session errors surface on
	// graph operations.
	_, err := g.ExecuteCypher("MATCH (c:"+componentType+") RETURN c", nil)
	if err == nil {
		t.Fatal("expected error for graph operation on nil session")
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

// TestExecuteCypher_AnnotatesPluralEntityTypes verifies SPEC R3 on the Graph
// read path: the SDK parses the Cypher, extracts the entity-type labels, and
// annotates the plural "x-flow-entity-types" gRPC metadata key with the
// comma-separated labels (joined by session.call via strings.Join).
func TestExecuteCypher_AnnotatesPluralEntityTypes(t *testing.T) {
	mock := &mockCartographerClient{
		executeCypher: func(ctx context.Context, req *flowv1.ExecuteCypherRequest) (*flowv1.ExecuteCypherResponse, error) {
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatal("no outgoing metadata")
			}
			vals := md.Get(metadataEntityTypesKey)
			if len(vals) == 0 {
				t.Fatal("no x-flow-entity-types metadata")
			}
			if vals[0] != componentType+",Service" {
				t.Errorf("expected metadata %s=%q, got %q",
					metadataEntityTypesKey, componentType+",Service", vals[0])
			}
			return &flowv1.ExecuteCypherResponse{}, nil
		},
	}
	g := newMockGraph(mock)
	_, err := g.ExecuteCypher("MATCH (c:"+componentType+")-[:DEPENDS_ON]->(s:Service) RETURN c, s", nil)
	if err != nil {
		t.Fatalf("ExecuteCypher returned error: %v", err)
	}
}

// TestExecuteCypher_FallsBackToWildcard verifies R3's wildcard fallback on
// the Graph read path: when label extraction yields no entity types (e.g. a
// query with no MATCH clause), the annotation must carry the
// READ:graph/entity/* wildcard instead of no annotation.
func TestExecuteCypher_FallsBackToWildcard(t *testing.T) {
	mock := &mockCartographerClient{
		executeCypher: func(ctx context.Context, req *flowv1.ExecuteCypherRequest) (*flowv1.ExecuteCypherResponse, error) {
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatal("no outgoing metadata")
			}
			vals := md.Get(metadataEntityTypesKey)
			if len(vals) == 0 {
				t.Fatal("no x-flow-entity-types metadata")
			}
			if vals[0] != "*" {
				t.Errorf("expected wildcard * in %s, got %q", metadataEntityTypesKey, vals[0])
			}
			return &flowv1.ExecuteCypherResponse{}, nil
		},
	}
	g := newMockGraph(mock)
	_, err := g.ExecuteCypher("RETURN 1", nil)
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

func TestSearchNeighbors_LosslessIdentityLikeProperties(t *testing.T) {
	mock := &mockCartographerClient{
		searchNeighbors: func(ctx context.Context,
			req *flowv1.SearchNeighborsRequest,
		) (*flowv1.SearchNeighborsResponse, error) {
			return &flowv1.SearchNeighborsResponse{
				Results: []*flowv1.SearchNeighborResult{
					{
						EntityId:   "n1",
						EntityType: componentType,
						Properties: map[string]string{
							"entity_id":   clobberedIDProp,
							"entity_type": clobberedTypeProp,
							"score":       "0.9",
						},
						Score: 0.9,
					},
				},
			}, nil
		},
	}
	g := newMockGraph(mock)
	results, err := g.SearchNeighbors([]float32{0.1, 0.2}, componentType, 10)
	if err != nil {
		t.Fatalf("SearchNeighbors returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.ID != "n1" {
		t.Errorf("expected identity ID n1, got %q", r.ID)
	}
	if r.Type != componentType {
		t.Errorf("expected identity type Component, got %q", r.Type)
	}
	if r.Properties["entity_id"] != clobberedIDProp {
		t.Errorf("expected identity-like property entity_id preserved, got %q", r.Properties["entity_id"])
	}
	if r.Properties["entity_type"] != clobberedTypeProp {
		t.Errorf("expected identity-like property entity_type preserved, got %q", r.Properties["entity_type"])
	}
	if r.Similarity != 0.9 {
		t.Errorf("expected similarity 0.9, got %v", r.Similarity)
	}
}

func TestSearchNeighbors_NaNRejection(t *testing.T) {
	g := newMockGraph(&mockCartographerClient{})
	_, err := g.SearchNeighbors([]float32{float32(math.NaN())}, componentType, 10)
	if err == nil {
		t.Fatal("expected error for NaN embedding")
	}
}

// TestSearchNeighbors_InfinityRejection verifies validateEmbedding rejects
// +Inf and -Inf (SPEC error table "Embedding contains NaN or infinity").
func TestSearchNeighbors_InfinityRejection(t *testing.T) {
	for _, tc := range []struct {
		name string
		emb  []float32
	}{
		{"positive", []float32{float32(math.Inf(1)), 1.0}},
		{"negative", []float32{1.0, float32(math.Inf(-1))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newMockGraph(&mockCartographerClient{})
			_, err := g.SearchNeighbors(tc.emb, componentType, 10)
			if err == nil {
				t.Fatal("expected error for infinity embedding")
			}
		})
	}
}

// The SPEC error table ("Embedding contains NaN or infinity") applies the
// NaN/infinity check to CreateEntity and UpdateEntity as well as
// SearchNeighbors. These tests pin the SDK-side validation boundary on the
// write paths, which call the same validateEmbedding guard.
func TestCreateEntity_NaNInfinityRejection(t *testing.T) {
	for _, tc := range []struct {
		name string
		emb  []float32
	}{
		{"nan", []float32{float32(math.NaN())}},
		{"positive-infinity", []float32{float32(math.Inf(1))}},
		{"negative-infinity", []float32{float32(math.Inf(-1))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newMockGraph(&mockCartographerClient{})
			_, err := g.CreateEntity(componentType, nil, nil, tc.emb)
			if err == nil {
				t.Fatal("expected error for NaN/infinity embedding on CreateEntity")
			}
		})
	}
}

func TestUpdateEntity_NaNInfinityRejection(t *testing.T) {
	for _, tc := range []struct {
		name string
		emb  []float32
	}{
		{"nan", []float32{float32(math.NaN())}},
		{"positive-infinity", []float32{float32(math.Inf(1))}},
		{"negative-infinity", []float32{float32(math.Inf(-1))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newMockGraph(&mockCartographerClient{})
			_, err := g.UpdateEntity("entity-1", nil, tc.emb)
			if err == nil {
				t.Fatal("expected error for NaN/finite embedding on UpdateEntity")
			}
		})
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

func TestFullTextSearch_LosslessIdentityLikeProperties(t *testing.T) {
	mock := &mockCartographerClient{
		fullTextSearch: func(ctx context.Context, req *flowv1.FullTextSearchRequest) (*flowv1.FullTextSearchResponse, error) {
			return &flowv1.FullTextSearchResponse{
				Results: []*flowv1.Entity{
					{
						EntityId:   "f1",
						EntityType: componentType,
						Properties: map[string]string{
							"entity_id":   "flattened-id",
							"entity_type": "FlattenedType",
							"name":        "search-hit",
						},
					},
				},
			}, nil
		},
	}
	g := newMockGraph(mock)
	results, err := g.FullTextSearch("search hit", componentType)
	if err != nil {
		t.Fatalf("FullTextSearch returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	e := results[0]
	if e.ID != "f1" {
		t.Errorf("expected identity ID f1, got %q", e.ID)
	}
	if e.Type != componentType {
		t.Errorf("expected identity type Component, got %q", e.Type)
	}
	if e.Properties["entity_id"] != "flattened-id" {
		t.Errorf("expected identity-like property entity_id preserved, got %q", e.Properties["entity_id"])
	}
	if e.Properties["entity_type"] != "FlattenedType" {
		t.Errorf("expected identity-like property entity_type preserved, got %q", e.Properties["entity_type"])
	}
	if e.Properties["name"] != "search-hit" {
		t.Errorf("expected property name search-hit, got %q", e.Properties["name"])
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

// The type-omitted read-search tests below exercise the SPEC type-omitted
// wildcard branch (SPEC line 247: the Sidecar validates request entityType,
// or against READ:graph/entity/* "when omitted") at the SDK metadata-wiring
// level, on a fresh/non-bootstrapped graph (no entities, still succeeds).
// The SDK wire path is identical for a concrete type and the omitted type
// (both skip capability annotation), so each asserts the request carries the
// empty entityType the Sidecar interprets as the wildcard, and that an empty
// graph resolves a successful empty result.

func TestSearchNeighbors_WildcardOmittedOnEmptyGraph(t *testing.T) {
	var capturedType string
	mock := &mockCartographerClient{
		searchNeighbors: func(ctx context.Context,
			req *flowv1.SearchNeighborsRequest,
		) (*flowv1.SearchNeighborsResponse, error) {
			capturedType = req.GetEntityType()
			return &flowv1.SearchNeighborsResponse{}, nil
		},
	}
	g := newMockGraph(mock)
	results, err := g.SearchNeighbors([]float32{0.1, 0.2}, "", 10)
	if err != nil {
		t.Fatalf("SearchNeighbors returned error: %v", err)
	}
	if capturedType != "" {
		t.Errorf("expected omitted entityType (wildcard branch) on request, got %q", capturedType)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results on fresh graph, got %d", len(results))
	}
}

func TestFullTextSearch_WildcardOmittedOnEmptyGraph(t *testing.T) {
	var capturedType string
	mock := &mockCartographerClient{
		fullTextSearch: func(ctx context.Context, req *flowv1.FullTextSearchRequest) (*flowv1.FullTextSearchResponse, error) {
			capturedType = req.GetEntityType()
			return &flowv1.FullTextSearchResponse{}, nil
		},
	}
	g := newMockGraph(mock)
	results, err := g.FullTextSearch("query", "")
	if err != nil {
		t.Fatalf("FullTextSearch returned error: %v", err)
	}
	if capturedType != "" {
		t.Errorf("expected omitted entityType (wildcard branch) on request, got %q", capturedType)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results on fresh graph, got %d", len(results))
	}
}

func TestListEntities_WildcardOmittedOnEmptyGraph(t *testing.T) {
	var capturedType string
	mock := &mockCartographerClient{
		listEntities: func(ctx context.Context, req *flowv1.ListEntitiesRequest) (*flowv1.ListEntitiesResponse, error) {
			capturedType = req.GetEntityType()
			return &flowv1.ListEntitiesResponse{}, nil
		},
	}
	g := newMockGraph(mock)
	page, err := g.ListEntities("")
	if err != nil {
		t.Fatalf("ListEntities returned error: %v", err)
	}
	if capturedType != "" {
		t.Errorf("expected omitted entityType (wildcard branch) on request, got %q", capturedType)
	}
	if len(page.Entities) != 0 {
		t.Errorf("expected empty entities on fresh graph, got %d", len(page.Entities))
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
	g := newMockGraph(mock)
	entity, err := g.DeleteEntity("entity-1")
	if err != nil {
		t.Fatalf("DeleteEntity returned error: %v", err)
	}
	if len(entity.Embedding) != 3 || entity.Embedding[0] != 0.1 || entity.Embedding[2] != 0.3 {
		t.Errorf("expected embedding [0.1 0.2 0.3], got %v", entity.Embedding)
	}
}

func TestListEntities_LosslessIdentityLikeProperties(t *testing.T) {
	mock := &mockCartographerClient{
		listEntities: func(ctx context.Context, req *flowv1.ListEntitiesRequest) (*flowv1.ListEntitiesResponse, error) {
			return &flowv1.ListEntitiesResponse{
				Entities: []*flowv1.Entity{
					{
						EntityId:   "e1",
						EntityType: componentType,
						Properties: map[string]string{
							"entity_id":   clobberedIDProp,
							"entity_type": clobberedTypeProp,
							"name":        "real",
						},
					},
				},
			}, nil
		},
	}
	g := newMockGraph(mock)
	page, err := g.ListEntities(componentType)
	if err != nil {
		t.Fatalf("ListEntities returned error: %v", err)
	}
	if len(page.Entities) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(page.Entities))
	}
	e := page.Entities[0]
	if e.ID != "e1" {
		t.Errorf("expected identity ID e1, got %q", e.ID)
	}
	if e.Type != componentType {
		t.Errorf("expected identity type Component, got %q", e.Type)
	}
	if e.Properties["entity_id"] != clobberedIDProp {
		t.Errorf("expected identity-like property entity_id preserved, got %q", e.Properties["entity_id"])
	}
	if e.Properties["entity_type"] != clobberedTypeProp {
		t.Errorf("expected identity-like property entity_type preserved, got %q", e.Properties["entity_type"])
	}
	if e.Properties["name"] != "real" {
		t.Errorf("expected property name real, got %q", e.Properties["name"])
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
				TransactionId:  "tx-1",
				AppliedTimeout: durationpb.New(30 * time.Minute),
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
	// SPEC R2: the applied timeout returned by BeginTransaction must be
	// propagated to the Transaction handle's timeout.
	if tx.timeout != 30*time.Minute {
		t.Errorf("expected tx.timeout to equal server applied timeout 30m, got %v", tx.timeout)
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

// TestExportGraph_AppliesPerCallDeadlineToEstablishment verifies that the
// per-call deadline configured via session timeout bounds the stream
// ESTABLISHMENT call, mirroring how session.call bounds unary RPCs. A mock
// that parks until its context deadline fires proves a blackholed upstream
// is cut during establishment rather than hanging on the deadline-less
// session ctx.
func TestExportGraph_AppliesPerCallDeadlineToEstablishment(t *testing.T) {
	mock := &mockCartographerClient{
		exportGraph: func(ctx context.Context, _ *flowv1.ExportGraphRequest,
		) (grpc.ServerStreamingClient[flowv1.ExportGraphResponse], error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(2 * time.Second):
				return nil, errors.New("establishment deadline was not applied; call was not cut")
			}
		},
	}
	g := newMockGraph(mock)
	g.session.timeout = 100 * time.Millisecond

	_, err := g.ExportGraph("json")
	if err == nil {
		t.Fatal("expected ExportGraph establishment to be cut by the per-call deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded to cut the call, got %v", err)
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

// TestGraphUpdateEntity_ResolvedTypeAnnotation proves the capability
// annotation carries the resolved concrete <type> (e.g. Component), not the
// wildcard, when the entity ID IS present in the local ID-to-type map. This
// is SPEC R3's mode-1 resolution: the Sidecar can then block on a specific
// <type> mismatch instead of falling back to a wildcard best-effort check.
func TestGraphUpdateEntity_ResolvedTypeAnnotation(t *testing.T) {
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
			return &flowv1.UpdateEntityResponse{EntityId: req.GetId(), EntityType: componentType}, nil
		},
	}
	g := newMockGraph(mock)
	// entity-1 IS in the map -> annotation must carry the resolved Component.
	g.idTypeMap.store("entity-1", componentType)
	_, err := g.UpdateEntity("entity-1", nil, nil)
	if err != nil {
		t.Fatalf("UpdateEntity returned error: %v", err)
	}
	if capturedKey != metadataEntityTypeKey {
		t.Errorf("expected metadata key x-flow-entity-type, got %q", capturedKey)
	}
	if capturedValue != componentType {
		t.Errorf("expected resolved type %q in annotation, got %q", componentType, capturedValue)
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

func TestWithTimeout(t *testing.T) {
	opt := WithTimeout(48 * time.Hour)
	cfg := &beginTxConfig{}
	opt(cfg)
	if cfg.timeout != 48*time.Hour {
		t.Errorf("expected 48h timeout, got %v", cfg.timeout)
	}
}

func TestBeginTransaction_WithTimeout(t *testing.T) {
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
	tx, err := g.BeginTransaction(WithTimeout(10 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("BeginTransaction returned error: %v", err)
	}
	if tx == nil {
		t.Fatal("expected non-nil transaction")
	}
	if captured == nil {
		t.Fatal("expected timeout to be set on request")
	}
	if captured.AsDuration() != 10*24*time.Hour {
		t.Errorf("expected requested timeout 10d, got %v", captured.AsDuration())
	}
	if tx.ID() != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("expected tx ID %q, got %q", "11111111-1111-4111-8111-111111111111", tx.ID())
	}
	// SPEC R2: the applied timeout may be shorter than the requested. The
	// Transaction must reflect the server's applied_timeout.
	if tx.timeout != 48*time.Hour {
		t.Errorf("expected tx.timeout to equal server applied timeout 48h, got %v", tx.timeout)
	}
}
