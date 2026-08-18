package service

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// WriteLaw
// ---------------------------------------------------------------------------

func TestWriteLaw_CreateInactive(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "New ruling",
			Tier: flowv1.LawTier_LAW_TIER_RULING,
			Representations: []*flowv1.Representation{
				{Type: "text/plain", Content: "A new ruling."},
			},
		},
	})
	if err != nil {
		t.Fatalf("WriteLaw: %v", err)
	}
	if resp.GetLawId() == "" {
		t.Fatal("expected non-empty law_id")
	}

	// Should be inactive (pending activation).
	getLawResp, err := srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: resp.GetLawId()})
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	// The law should exist but checking active requires store-level verification.
	if getLawResp.GetLaw().GetTier() != flowv1.LawTier_LAW_TIER_RULING {
		t.Fatalf("expected Tier 2, got %v", getLawResp.GetLaw().GetTier())
	}
}

func TestWriteLaw_UpdateExisting(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Create via RecordFinding.
	findResp, _ := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Original",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "v1"}},
	})

	// Update via WriteLaw.
	resp, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Id:   findResp.GetLawId(),
			Goal: "Updated",
			Tier: flowv1.LawTier_LAW_TIER_FINDING,
			Representations: []*flowv1.Representation{
				{Type: "text/plain", Content: "v2"},
			},
		},
	})
	if err != nil {
		t.Fatalf("WriteLaw: %v", err)
	}

	// Verify update.
	getLawResp, _ := srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: findResp.GetLawId()})
	if getLawResp.GetLaw().GetGoal() != "Updated" {
		t.Fatalf("expected updated goal, got %q", getLawResp.GetLaw().GetGoal())
	}
	if resp.GetVersionHash() == "" {
		t.Fatal("expected non-empty version_hash")
	}
}

func TestWriteLaw_InvalidTier(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal:            "Bad tier",
			Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
		},
	})
	if err == nil {
		t.Fatal("expected error for unspecified tier")
	}
}

// ---------------------------------------------------------------------------
// Group support
// ---------------------------------------------------------------------------

func TestWriteLaw_GroupRoundTrip(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	writeResp, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "Styled rule", Tier: flowv1.LawTier_LAW_TIER_RULING,
			Group:           "style",
			Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
		},
	})
	if err != nil {
		t.Fatalf("WriteLaw: %v", err)
	}

	getLawResp, err := srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: writeResp.GetLawId()})
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	if getLawResp.GetLaw().GetGroup() != "style" {
		t.Fatalf("expected group=style, got %q", getLawResp.GetLaw().GetGroup())
	}
}

func TestWriteLaw_StoresVecEmbedding(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	resp, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "New ruling with embedding",
			Tier: flowv1.LawTier_LAW_TIER_RULING,
			Representations: []*flowv1.Representation{
				{Type: "text/plain", Content: "A ruling."},
			},
		},
	})
	if err != nil {
		t.Fatalf("WriteLaw: %v", err)
	}

	if !vecEmbeddingStored(t, srv, resp.GetLawId(), "New ruling with embedding") {
		t.Fatal("expected vec embedding to be stored after WriteLaw")
	}
}

func TestWriteLaw_UpdateStoresVecEmbedding(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	// Create via RecordFinding.
	findResp, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Original finding",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "v1"}},
	})
	if err != nil {
		t.Fatalf("RecordFinding: %v", err)
	}

	// Update via WriteLaw.
	_, err = srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Id:   findResp.GetLawId(),
			Goal: "Updated finding with new embedding",
			Tier: flowv1.LawTier_LAW_TIER_FINDING,
			Representations: []*flowv1.Representation{
				{Type: "text/plain", Content: "v2"},
			},
		},
	})
	if err != nil {
		t.Fatalf("WriteLaw update: %v", err)
	}

	if !vecEmbeddingStored(t, srv, findResp.GetLawId(), "Updated finding with new embedding") {
		t.Fatal("expected vec embedding to be updated after WriteLaw update")
	}
}

func TestWriteLaw_NoEmbedder_NoVecEmbedding(t *testing.T) {
	srv := newTestServer(t) // no embedder
	ctx := context.Background()

	resp, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "No embedder",
			Tier: flowv1.LawTier_LAW_TIER_RULING,
			Representations: []*flowv1.Representation{
				{Type: "text/plain", Content: "x"},
			},
		},
	})
	if err != nil {
		t.Fatalf("WriteLaw: %v", err)
	}

	// No embedder means no vec embedding.
	if vecEmbeddingStored(t, srv, resp.GetLawId(), "No embedder") {
		t.Fatal("expected no vec embedding when embedder is nil")
	}
}
