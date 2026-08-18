package flow

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

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
