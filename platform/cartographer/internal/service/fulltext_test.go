package service

import (
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestFullTextSearch_EmptyQuery(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	_, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{Query: ""})
	if err == nil {
		t.Fatal("expected error for empty query, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestFullTextSearch_Valid(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	_, _ = srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "apple", "version": "1"}, nil, "")

	resp, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{Query: "apple"})
	if err != nil {
		t.Fatalf("FullTextSearch failed: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected at least one result")
	}
}

// TestFullTextSearch_ReturnsEmbedding pins the Entity wire contract
// (proto/flow/v1/cartographer.proto: Entity.embedding): the FullTextSearch
// handler must populate the embedding field from the store result (the store
// returns embeddings via entityFromNode for vector-indexed types) instead of
// silently dropping it, so SDK callers reading GetEmbedding() receive the
// stored vector.
func TestFullTextSearch_ReturnsEmbedding(t *testing.T) {
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
	_, _ = srv.store.CreateEntity(ctx, "VectorType", "", map[string]string{"name": "apple"}, []float32{0.1, 0.2, 0.3}, "")

	resp, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{Query: "apple"})
	if err != nil {
		t.Fatalf("FullTextSearch failed: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected at least one result")
	}
	if len(resp.Results[0].Embedding) != 3 ||
		resp.Results[0].Embedding[0] != 0.1 || resp.Results[0].Embedding[2] != 0.3 {
		t.Fatalf("expected embedding [0.1 0.2 0.3] on result, got %v", resp.Results[0].Embedding)
	}
}

// TestFullTextSearch_SingleTypeSpecificCapabilityPasses pins SPEC R3
// (SPEC:240): a caller holding only READ:graph/entity/<type> is authorised
// for a FullTextSearch scoped to that type (the per-type branch, not the
// wildcard fallback).
func TestFullTextSearch_SingleTypeSpecificCapabilityPasses(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("READ:graph/entity/Component")

	applyTestSchema(ctx, t, srv.store)
	_, _ = srv.store.CreateEntity(testCtx(), "Component", "", map[string]string{"name": "apple", "version": "1"}, nil, "")

	resp, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{
		Query:      "apple",
		EntityType: "Component",
	})
	if err != nil {
		t.Fatalf("FullTextSearch with per-type capability failed: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected at least one result")
	}
}

func TestFullTextSearch_UnknownEntityType(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{
		Query:      "anything",
		EntityType: "NonExistent",
	})
	if err == nil {
		t.Fatal("expected error for unknown entity type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}
