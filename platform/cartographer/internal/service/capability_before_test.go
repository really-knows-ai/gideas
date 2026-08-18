package service

import (
	"math"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestSearchNeighbors_MissingCapBeforeTypeCheck pins the SPEC check-order row
// "SearchNeighbors / FullTextSearch: capability → structural" (SPEC:1019) at
// the service layer for SearchNeighbors: a caller lacking READ capability who
// also supplies an unknown entity type must receive PERMISSION_DENIED (the
// capability gate fires first), never the structural INVALID_ARGUMENT from
// errUnknownEntityType. TestSearchNeighbors_NaNBeforeTypeCheck pins the
// NaN-before-type ordering with full capabilities; only this test detects a
// gate reorder that moved capability after the type check.
func TestSearchNeighbors_MissingCapBeforeTypeCheck(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := noReadCtx() // WRITE-only, no READ

	_, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{1.0, 2.0, 3.0},
		EntityType: "NonExistentType",
		TopK:       5,
	})
	if err == nil {
		t.Fatal("expected error for missing READ capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied (capability gate first), got %v", status.Code(err))
	}
}

// TestFullTextSearch_MissingCapBeforeTypeCheck pins the same capability →
// structural ordering for FullTextSearch (SPEC:1019).
func TestFullTextSearch_MissingCapBeforeTypeCheck(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := noReadCtx() // WRITE-only, no READ

	_, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{
		Query:      "apple",
		EntityType: "NonExistentType",
	})
	if err == nil {
		t.Fatal("expected error for missing READ capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied (capability gate first), got %v", status.Code(err))
	}
}

// TestFullTextSearch_MissingCapBeforeEmptyQuery pins the SPEC check-order row
// "SearchNeighbors / FullTextSearch: capability → structural" (SPEC:1019) for
// the empty-query gate (crud.go: capability 164-172, empty query 173-175): a
// caller lacking READ capability sending an EMPTY query must receive the
// capability gate's PERMISSION_DENIED — never the empty-query gate's
// INVALID_ARGUMENT. TestFullTextSearch_EmptyQuery pins the capability-held
// half; only this combined fault detects a reorder that hoisted the
// empty-query gate ahead of the capability gate.
func TestFullTextSearch_MissingCapBeforeEmptyQuery(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := noReadCtx() // WRITE-only, no READ

	_, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{Query: ""})
	if err == nil {
		t.Fatal("expected error for empty query from a no-READ caller, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied (capability gate first), got %v", status.Code(err))
	}

	// Capability held: the same empty query surfaces the empty-query gate's
	// INVALID_ARGUMENT (SPEC error-table row "Empty FullTextSearch query").
	_, err = srv.FullTextSearch(testCtx(), &flowv1.FullTextSearchRequest{Query: ""})
	if err == nil {
		t.Fatal("expected error for empty query from a READ-capable caller, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument (empty-query gate), got %v", status.Code(err))
	}
}

// TestSearchNeighbors_MissingCapBeforeTopK pins the SPEC check-order row
// "SearchNeighbors / FullTextSearch: capability → structural" (SPEC:1019) for
// the negative-topK gate (crud.go: capability 107-116, topK 117-123): a caller
// lacking READ capability sending a request with a negative topK must receive
// the capability gate's PERMISSION_DENIED — never the topK gate's
// INVALID_ARGUMENT. TestSearchNeighbors_InvalidTopK pins the capability-held
// half; only this combined fault detects a reorder that hoisted the topK gate
// ahead of the capability gate.
func TestSearchNeighbors_MissingCapBeforeTopK(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := noReadCtx() // WRITE-only, no READ

	_, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{1.0, 2.0, 3.0},
		EntityType: "Component",
		TopK:       -1,
	})
	if err == nil {
		t.Fatal("expected error for negative topK from a no-READ caller, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied (capability gate first), got %v", status.Code(err))
	}

	// Capability held: the same request surfaces the topK gate's
	// INVALID_ARGUMENT (SPEC error-table row "Invalid topK in
	// SearchNeighbors").
	_, err = srv.SearchNeighbors(testCtx(), &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{1.0, 2.0, 3.0},
		EntityType: "Component",
		TopK:       -1,
	})
	if err == nil {
		t.Fatal("expected error for negative topK from a READ-capable caller, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument (topK gate), got %v", status.Code(err))
	}
}

// TestSearchNeighbors_MissingCapBeforeEmbeddingCheck pins the SPEC check-order
// row "SearchNeighbors / FullTextSearch: capability → structural" (SPEC:1019)
// for the embedding gates (crud.go: capability 107-116, empty/NaN embedding
// 129-136): a caller lacking READ capability sending a request with an empty
// or NaN embedding must receive the capability gate's PERMISSION_DENIED —
// never the embedding gates' INVALID_ARGUMENT. TestSearchNeighbors_EmptyEmbedding
// and TestSearchNeighbors_NaNEmbedding pin the capability-held half; only this
// combined fault detects a reorder that hoisted the embedding checks ahead of
// the capability gate.
func TestSearchNeighbors_MissingCapBeforeEmbeddingCheck(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := noReadCtx() // WRITE-only, no READ

	for name, embedding := range map[string][]float32{
		"empty": nil,
		"NaN":   {float32(math.NaN()), 0.0, 0.0},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
				Embedding:  embedding,
				EntityType: "Component",
				TopK:       5,
			})
			if err == nil {
				t.Fatal("expected error for invalid embedding from a no-READ caller, got nil")
			}
			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("expected PermissionDenied (capability gate first), got %v", status.Code(err))
			}
		})
	}

	// Capability held: the same requests surface the embedding gates'
	// INVALID_ARGUMENT (SPEC error-table rows "Empty embedding in
	// SearchNeighbors" and "Embedding contains NaN or infinity").
	_, err := srv.SearchNeighbors(testCtx(), &flowv1.SearchNeighborsRequest{
		Embedding:  nil,
		EntityType: "Component",
		TopK:       5,
	})
	if err == nil {
		t.Fatal("expected error for empty embedding from a READ-capable caller, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument (empty-embedding gate), got %v", status.Code(err))
	}
	_, err = srv.SearchNeighbors(testCtx(), &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{float32(math.NaN()), 0.0, 0.0},
		EntityType: "Component",
		TopK:       5,
	})
	if err == nil {
		t.Fatal("expected error for NaN embedding from a READ-capable caller, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument (NaN-embedding gate), got %v", status.Code(err))
	}
}

// TestListEntities_MissingCapBeforePagination pins the SPEC check-order row
// "ListEntities: capability → structural (unknown entity type → pageSize →
// pageToken)" (SPEC:1020) for the pagination gates (crud.go: capability
// 201-203, pageSize 213-221, pageToken validated by the store): a caller
// lacking READ capability sending a request with an invalid pageSize or
// pageToken must receive the capability gate's PERMISSION_DENIED — never the
// pagination gates' INVALID_ARGUMENT. TestListEntities_InvalidPageSize and
// TestListEntities_InvalidPageToken pin the capability-held half; only this
// combined fault detects a reorder that hoisted the pagination gates ahead of
// the capability gate.
func TestListEntities_MissingCapBeforePagination(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := noReadCtx() // WRITE-only, no READ

	// Invalid pageSize from a no-READ caller.
	_, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
		EntityType: "Component",
		PageSize:   -1,
	})
	if err == nil {
		t.Fatal("expected error for negative pageSize from a no-READ caller, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied (capability gate first), got %v", status.Code(err))
	}

	// Invalid pageToken from a no-READ caller.
	_, err = srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
		EntityType: "Component",
		PageSize:   10,
		PageToken:  "invalid-token",
	})
	if err == nil {
		t.Fatal("expected error for invalid pageToken from a no-READ caller, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied (capability gate first), got %v", status.Code(err))
	}

	// Capability held: the same requests surface the pagination gates'
	// INVALID_ARGUMENT (SPEC error-table rows "Invalid pageSize in ListEntities"
	// and "Invalid pageToken in ListEntities"). The schema must be applied so
	// the requests reach the pagination gates rather than the unknown-type gate.
	applyTestSchema(testCtx(), t, srv.store)
	_, err = srv.ListEntities(testCtx(), &flowv1.ListEntitiesRequest{
		EntityType: "Component",
		PageSize:   -1,
	})
	if err == nil {
		t.Fatal("expected error for negative pageSize from a READ-capable caller, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument (pageSize gate), got %v", status.Code(err))
	}
	_, err = srv.ListEntities(testCtx(), &flowv1.ListEntitiesRequest{
		EntityType: "Component",
		PageSize:   10,
		PageToken:  "invalid-token",
	})
	if err == nil {
		t.Fatal("expected error for invalid pageToken from a READ-capable caller, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument (pageToken gate), got %v", status.Code(err))
	}
}
