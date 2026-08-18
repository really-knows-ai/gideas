package service

import (
	"fmt"
	"math"
	"strings"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSearchNeighbors_NonIndexed(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{1.0, 2.0, 3.0},
		EntityType: "Component",
		TopK:       5,
	})
	if err == nil {
		t.Fatal("expected error for non-indexed type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestSearchNeighbors_Valid(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

	// Apply a schema with a vector-indexed type.
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

	// Bootstrap with first entity (establishes dimension).
	_, _ = srv.store.CreateEntity(ctx, "VectorType", "", map[string]string{"name": "a"}, []float32{1.0, 0.0, 0.0}, "")
	_, _ = srv.store.CreateEntity(ctx, "VectorType", "", map[string]string{"name": "b"}, []float32{0.0, 1.0, 0.0}, "")

	resp, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{1.0, 0.0, 0.0},
		EntityType: "VectorType",
		TopK:       5,
	})
	if err != nil {
		t.Fatalf("SearchNeighbors failed: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected at least one neighbor result")
	}
}

// TestSearchNeighbors_SingleTypeSpecificCapabilityPasses pins SPEC R3
// (SPEC:240): a caller holding only READ:graph/entity/<type> is authorised
// for a SearchNeighbors scoped to that type (the per-type branch, not the
// wildcard fallback).
func TestSearchNeighbors_SingleTypeSpecificCapabilityPasses(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := narrowCtx("READ:graph/entity/VectorType")

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
	_, _ = srv.store.CreateEntity(testCtx(), "VectorType", "",
		map[string]string{"name": "a"}, []float32{1.0, 0.0, 0.0}, "")
	_, _ = srv.store.CreateEntity(testCtx(), "VectorType", "",
		map[string]string{"name": "b"}, []float32{0.0, 1.0, 0.0}, "")

	resp, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{1.0, 0.0, 0.0},
		EntityType: "VectorType",
		TopK:       5,
	})
	if err != nil {
		t.Fatalf("SearchNeighbors with per-type capability failed: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected at least one neighbor result")
	}
}

func TestSearchNeighbors_UnknownEntityType(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{1.0, 2.0, 3.0},
		EntityType: "NonExistent",
		TopK:       5,
	})
	if err == nil {
		t.Fatal("expected error for unknown entity type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestSearchNeighbors_InvalidTopK(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	_, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{1.0, 2.0, 3.0},
		EntityType: "Component",
		TopK:       -1,
	})
	if err == nil {
		t.Fatal("expected error for negative topK, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// TestSearchNeighbors_TopKZeroDefaultsTo10 pins the SPEC error-table boundary
// "topK is negative (zero is treated as omitted and defaults to 10)" (row
// "Invalid topK in SearchNeighbors"): a zero topK is accepted and behaves like
// the default of 10, not an error. Verified against a graph with more indexed
// entities than the default: all 10 nearest neighbors are returned and no
// more.
func TestSearchNeighbors_TopKZeroDefaultsTo10(t *testing.T) {
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
	// 12 indexed entities so the result set exceeds the default topK of 10.
	// Each embedding is distinct; the query embedding matches the first, whose
	// distance 0 is the nearest.
	for i := range 12 {
		vec := make([]float32, 12)
		vec[i] = 1.0
		if _, err := srv.store.CreateEntity(ctx, "VectorType", "",
			map[string]string{"name": fmt.Sprintf("entity-%d", i)}, vec, ""); err != nil {
			t.Fatalf("CreateEntity %d: %v", i, err)
		}
	}
	query := make([]float32, 12)
	query[0] = 1.0

	resp, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  query,
		EntityType: "VectorType",
		TopK:       0,
	})
	if err != nil {
		t.Fatalf("SearchNeighbors with zero topK failed: %v", err)
	}
	if len(resp.Results) != 10 {
		t.Fatalf("expected 10 results with zero topK (default), got %d", len(resp.Results))
	}
}

func TestSearchNeighbors_NaNEmbedding(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	// Bootstrap with a valid entity.
	_, _ = srv.store.CreateEntity(ctx, "VecType", "", map[string]string{"name": "a"}, []float32{1.0, 0.0, 0.0}, "")

	_, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{float32(math.NaN()), 0.0, 0.0},
		EntityType: "VecType",
		TopK:       5,
	})
	if err == nil {
		t.Fatal("expected error for NaN embedding, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// TestSearchNeighbors_NaNEmbeddingNonIndexed pins SPEC R7: the NaN/Inf
// embedding rejection applies "regardless of indexing status" — the service
// layer rejects a NaN/Inf embedding for a non-indexed entity type before the
// store's non-indexed-type rejection (also INVALID_ARGUMENT) could surface.
func TestSearchNeighbors_NaNEmbeddingNonIndexed(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	// Component is a non-indexed type (enableVectorIndex not set).
	applyTestSchema(ctx, t, srv.store)

	_, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{float32(math.NaN()), 0.0, 0.0},
		EntityType: "Component",
		TopK:       5,
	})
	if err == nil {
		t.Fatal("expected error for NaN embedding on non-indexed type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
	}
	if !strings.Contains(err.Error(), "NaN") {
		t.Fatalf("expected the NaN-embedding rejection, got %v", err)
	}
}

func TestSearchNeighbors_EmbeddingDimensionMismatch(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	// Bootstrap with a 3-dim vector.
	_, _ = srv.store.CreateEntity(ctx, "VecType", "", map[string]string{"name": "a"}, []float32{1.0, 0.0, 0.0}, "")

	_, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{1.0, 0.0}, // only 2 dims
		EntityType: "VecType",
		TopK:       5,
	})
	if err == nil {
		t.Fatal("expected error for dimension mismatch, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestSearchNeighbors_EmptyEmbedding(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  nil,
		EntityType: "Component",
		TopK:       5,
	})
	if err == nil {
		t.Fatal("expected error for empty embedding, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
	if msg := status.Convert(err).Message(); msg != "embedding is required" {
		t.Fatalf("expected missing-embedding error, got %q", msg)
	}
}
