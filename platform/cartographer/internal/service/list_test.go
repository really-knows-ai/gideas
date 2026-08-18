package service

import (
	"fmt"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestListEntities_UnknownType(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	_, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{EntityType: "NonExistent"})
	if err == nil {
		t.Fatal("expected error for unknown type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestListEntities_Valid(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	_, _ = srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "a"}, nil, "")
	_, _ = srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "b"}, nil, "")

	resp, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
		EntityType: "Component",
		PageSize:   10,
	})
	if err != nil {
		t.Fatalf("ListEntities failed: %v", err)
	}
	if len(resp.Entities) != 2 {
		t.Fatalf("expected 2 entities, got %d", len(resp.Entities))
	}
}

// TestListEntities_ReturnsEmbedding pins the Entity wire contract
// (proto/flow/v1/cartographer.proto: Entity.embedding): the ListEntities
// handler must populate the embedding field from the store result instead of
// silently dropping it, so SDK callers reading GetEmbedding() receive the
// stored vector.
func TestListEntities_ReturnsEmbedding(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VectorType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	_, _ = srv.store.CreateEntity(ctx, "VectorType", "", map[string]string{"name": "a"}, []float32{0.4, 0.5}, "")

	resp, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{EntityType: "VectorType", PageSize: 10})
	if err != nil {
		t.Fatalf("ListEntities failed: %v", err)
	}
	if len(resp.Entities) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(resp.Entities))
	}
	if len(resp.Entities[0].Embedding) != 2 ||
		resp.Entities[0].Embedding[0] != 0.4 || resp.Entities[0].Embedding[1] != 0.5 {
		t.Fatalf("expected embedding [0.4 0.5] on entity, got %v", resp.Entities[0].Embedding)
	}
}

// TestListEntities_SingleTypeSpecificCapabilityPasses pins SPEC R3
// (SPEC:240): a caller holding only READ:graph/entity/<type> is authorised
// for a ListEntities scoped to that type (the per-type branch, not the
// wildcard fallback).
func TestListEntities_SingleTypeSpecificCapabilityPasses(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("READ:graph/entity/Component")

	applyTestSchema(ctx, t, srv.store)
	_, _ = srv.store.CreateEntity(testCtx(), "Component", "", map[string]string{"name": "a"}, nil, "")
	_, _ = srv.store.CreateEntity(testCtx(), "Component", "", map[string]string{"name": "b"}, nil, "")

	resp, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
		EntityType: "Component",
		PageSize:   10,
	})
	if err != nil {
		t.Fatalf("ListEntities with per-type capability failed: %v", err)
	}
	if len(resp.Entities) != 2 {
		t.Fatalf("expected 2 entities, got %d", len(resp.Entities))
	}
}

// TestReadWildcardOnly_TypeOmittedSearchesSucceed covers the SPEC R3 success
// path for the type-omitted read-search branch: a caller holding ONLY
// READ:graph/entity/* must be able to run a type-omitted (empty entityType)
// FullTextSearch and SearchNeighbors. The empty entityType request takes the
// checkWildcardEntityCap branch (not the per-type branch), so an exclusive
// wildcard holder must not be denied.
func TestReadWildcardOnly_TypeOmittedSearchesSucceed(t *testing.T) {
	srv, st := newTestServer(t)
	// Caller holds ONLY READ:graph/entity/* (no WRITE, no per-type caps).
	ctx := narrowCtx("READ:graph/entity/*")

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VectorType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	// Seed data directly via the store (the caller holds no write capability).
	_, _ = st.CreateEntity(ctx, "VectorType", "", map[string]string{"name": "alpha"}, []float32{1.0, 0, 0}, "")
	_, _ = st.CreateEntity(ctx, "VectorType", "", map[string]string{"name": "beta"}, []float32{0.0, 1.0, 0.0}, "")

	// FullTextSearch with EntityType omitted (wildcard branch).
	fts, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{Query: "alpha"})
	if err != nil {
		t.Fatalf("FullTextSearch (type-omitted) denied for wildcard-only holder: %v", err)
	}
	if len(fts.Results) == 0 {
		t.Fatal("expected at least one FullTextSearch result")
	}

	// SearchNeighbors with EntityType omitted (wildcard branch).
	sn, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding: []float32{1.0, 0.0, 0.0},
		TopK:      5,
	})
	if err != nil {
		t.Fatalf("SearchNeighbors (type-omitted) with wildcard-only holder: %v", err)
	}
	if len(sn.Results) == 0 {
		t.Fatal("expected at least one neighbor result")
	}
}

// TestReadTypeSpecificOnly_TypeOmittedSearchesDenied pins SPEC R3 (SPEC:262):
// "a per-type capability cannot authorise an all-types search" — a caller
// holding ONLY READ:graph/entity/<type> is denied a type-omitted (empty
// entityType) FullTextSearch and SearchNeighbors with PERMISSION_DENIED,
// because the type-omitted branch requires READ:graph/entity/*.
func TestReadTypeSpecificOnly_TypeOmittedSearchesDenied(t *testing.T) {
	srv, st := newTestServer(t)
	// Caller holds ONLY the per-type read capability (no wildcard).
	ctx := narrowCtx("READ:graph/entity/Component")

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VectorType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
			{
				Name:       "Component",
				Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	_, _ = st.CreateEntity(ctx, "VectorType", "", map[string]string{"name": "alpha"}, []float32{1.0, 0, 0}, "")
	_, _ = st.CreateEntity(ctx, "Component", "", map[string]string{"name": "beta"}, nil, "")

	// FullTextSearch with EntityType omitted (wildcard branch) — denied.
	_, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{Query: "alpha"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied for per-type-only holder on type-omitted FullTextSearch, got %v", err)
	}

	// SearchNeighbors with EntityType omitted (wildcard branch) — denied.
	_, err = srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding: []float32{1.0, 0.0, 0.0},
		TopK:      5,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied for per-type-only holder on type-omitted SearchNeighbors, got %v", err)
	}
}

// TestReadMethods_BeforeFirstApplySchema pins the SPEC R5 (SPEC:345-346) read
// boundary: before the first ApplySchema, non-type-referencing read methods
// (ExecuteCypher without type labels, type-omitted FullTextSearch and
// SearchNeighbors) succeed on an empty graph, while methods referencing a
// specific type return INVALID_ARGUMENT specifically because no schema has been
// applied.
func TestReadMethods_BeforeFirstApplySchema(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	t.Run("non-type-referencing methods succeed on an empty graph", func(t *testing.T) {
		// ExecuteCypher with no type reference.
		resp, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: "MATCH (n) RETURN n"})
		if err != nil {
			t.Fatalf("ExecuteCypher without a type reference should succeed on an empty graph, got %v", err)
		}
		if len(resp.Rows) != 0 {
			t.Fatalf("expected no rows on an empty graph, got %d", len(resp.Rows))
		}

		// FullTextSearch with entityType omitted (wildcard branch).
		fts, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{Query: "anything"})
		if err != nil {
			t.Fatalf("type-omitted FullTextSearch should succeed on an empty graph, got %v", err)
		}
		if len(fts.Results) != 0 {
			t.Fatalf("expected no FullTextSearch results on an empty graph, got %d", len(fts.Results))
		}

		// SearchNeighbors with entityType omitted (wildcard branch).
		sn, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
			Embedding: []float32{1.0, 2.0, 3.0},
			TopK:      5,
		})
		if err != nil {
			t.Fatalf("type-omitted SearchNeighbors should succeed on an empty graph, got %v", err)
		}
		if len(sn.Results) != 0 {
			t.Fatalf("expected no neighbor results on an empty graph, got %d", len(sn.Results))
		}
	})

	t.Run("type-referencing methods return INVALID_ARGUMENT", func(t *testing.T) {
		// ExecuteCypher referencing a specific type label.
		_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: "MATCH (n:Component) RETURN n"})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument for ExecuteCypher referencing an unapplied type, got %v", err)
		}

		// FullTextSearch / SearchNeighbors / ListEntities with an explicit type.
		_, err = srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{Query: "x", EntityType: "Component"})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument for type-referencing FullTextSearch, got %v", err)
		}
		_, err = srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
			Embedding: []float32{1.0, 2.0, 3.0}, EntityType: "Component", TopK: 5,
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument for type-referencing SearchNeighbors, got %v", err)
		}
		_, err = srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{EntityType: "Component", PageSize: 10})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument for type-referencing ListEntities, got %v", err)
		}
	})
}

func TestListEntities_InvalidPageSize(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	// Negative page size.
	_, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
		EntityType: "Component",
		PageSize:   -1,
	})
	if err == nil {
		t.Fatal("expected error for negative page size, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}

	// Page size exceeding maximum.
	_, err = srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
		EntityType: "Component",
		PageSize:   1001,
	})
	if err == nil {
		t.Fatal("expected error for page size > 1000, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// TestListEntities_PageSizeZeroDefaultsTo1000 pins the SPEC error-table
// boundary "pageSize of 0 is treated as omitted and defaults to 1000" (row
// "Invalid pageSize in ListEntities"): a zero pageSize is accepted and behaves
// like the default, not an error. Verified by listing a graph larger than the
// default page size and asserting every entity is returned.
func TestListEntities_PageSizeZeroDefaultsTo1000(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	// 1005 entities exceeds the 1000 default page size.
	for i := range 1005 {
		_, err := srv.store.CreateEntity(ctx, "Component", "",
			map[string]string{"name": fmt.Sprintf("entity-%d", i)}, nil, "")
		if err != nil {
			t.Fatalf("CreateEntity %d: %v", i, err)
		}
	}

	resp, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{EntityType: "Component"})
	if err != nil {
		t.Fatalf("ListEntities with zero pageSize failed: %v", err)
	}
	if len(resp.Entities) != 1000 {
		t.Fatalf("expected 1000 entities with zero pageSize (default), got %d", len(resp.Entities))
	}
	if resp.NextPageToken == "" {
		t.Fatal("expected a next-page token when the graph exceeds the default page size")
	}
}

func TestListEntities_InvalidPageToken(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
		EntityType: "Component",
		PageSize:   10,
		PageToken:  "invalid-token",
	})
	if err == nil {
		t.Fatal("expected error for invalid page token, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestListEntities_Pagination(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	// Create exactly 10 entities (enough for 2 pages of 5).
	const total = 10
	for i := range total {
		_, err := srv.store.CreateEntity(ctx, "Component", "",
			map[string]string{"name": fmt.Sprintf("entity-%d", i)}, nil, "")
		if err != nil {
			t.Fatalf("CreateEntity %d: %v", i, err)
		}
	}

	// First page: PageSize=5.
	resp, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
		EntityType: "Component",
		PageSize:   5,
	})
	if err != nil {
		t.Fatalf("ListEntities page 1 failed: %v", err)
	}
	if len(resp.Entities) != 5 {
		t.Fatalf("page 1 expected 5 entities, got %d", len(resp.Entities))
	}
	if resp.NextPageToken == "" {
		t.Fatal("page 1 expected non-empty NextPageToken")
	}

	// Collect first page IDs for dedup check.
	page1IDs := make(map[string]bool)
	for _, e := range resp.Entities {
		page1IDs[e.EntityId] = true
	}

	// Second page: use the token from the first page.
	resp2, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
		EntityType: "Component",
		PageSize:   5,
		PageToken:  resp.NextPageToken,
	})
	if err != nil {
		t.Fatalf("ListEntities page 2 failed: %v", err)
	}
	if len(resp2.Entities) != 5 {
		t.Fatalf("page 2 expected 5 entities, got %d", len(resp2.Entities))
	}
	if resp2.NextPageToken != "" {
		t.Fatal("page 2 expected empty NextPageToken (no more entities)")
	}

	// Verify no overlap between pages.
	for _, e := range resp2.Entities {
		if page1IDs[e.EntityId] {
			t.Fatalf("entity %q appears in both pages", e.EntityId)
		}
	}
}
