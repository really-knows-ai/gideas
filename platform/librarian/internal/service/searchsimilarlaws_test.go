package service

import (
	"context"
	"fmt"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// SearchSimilarLaws
// ---------------------------------------------------------------------------

func TestSearchSimilarLaws_ReturnsMatchingLaw(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	// Create a law via RecordFinding (active + embedding stored).
	_, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "All tests must pass",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "tests"}},
	})
	if err != nil {
		t.Fatalf("RecordFinding: %v", err)
	}

	// Search with a query that produces a similar embedding.
	// The stubEmbedder uses text length, so same-length text = identical vector.
	resp, err := srv.SearchSimilarLaws(ctx, &flowv1.SearchSimilarLawsRequest{
		QueryText: "All tests must pass", // same length -> identical embedding -> high similarity
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("SearchSimilarLaws: %v", err)
	}
	if len(resp.GetResults()) == 0 {
		t.Fatal("expected at least one result")
	}
	if resp.GetResults()[0].GetLaw().GetGoal() != "All tests must pass" {
		t.Fatalf("expected matching law goal, got %q", resp.GetResults()[0].GetLaw().GetGoal())
	}
	if resp.GetResults()[0].GetSimilarityScore() <= 0 {
		t.Fatalf("expected positive similarity score, got %f", resp.GetResults()[0].GetSimilarityScore())
	}
}

func TestSearchSimilarLaws_ScopeFilter(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	// Create two laws in different groups.
	_, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "Security rule alpha", Tier: flowv1.LawTier_LAW_TIER_RULING,
			Group:           "security",
			Representations: []*flowv1.Representation{{Type: "text/plain", Content: "s"}},
		},
	})
	if err != nil {
		t.Fatalf("WriteLaw security: %v", err)
	}
	_, err = srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "Architecture rule bet", Tier: flowv1.LawTier_LAW_TIER_RULING,
			Group:           "architecture",
			Representations: []*flowv1.Representation{{Type: "text/plain", Content: "a"}},
		},
	})
	if err != nil {
		t.Fatalf("WriteLaw architecture: %v", err)
	}

	// Search with scope_filter = "security" -> only security law.
	resp, err := srv.SearchSimilarLaws(ctx, &flowv1.SearchSimilarLawsRequest{
		QueryText:   "Security rule alpha", // same length as security law goal
		ScopeFilter: "security",
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("SearchSimilarLaws: %v", err)
	}
	for _, r := range resp.GetResults() {
		if r.GetLaw().GetGroup() != testGroupSecurity {
			t.Fatalf("expected only security group, got %q", r.GetLaw().GetGroup())
		}
	}
}

func TestSearchSimilarLaws_LimitEnforced(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	// Create 3 laws.
	for i := range 3 {
		_, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
			Goal:            fmt.Sprintf("Law number %d padded out", i),
			Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
		})
		if err != nil {
			t.Fatalf("RecordFinding %d: %v", i, err)
		}
	}

	// Search with limit=2 -> at most 2 results.
	resp, err := srv.SearchSimilarLaws(ctx, &flowv1.SearchSimilarLawsRequest{
		QueryText: "Law number 0 padded out",
		Limit:     2,
	})
	if err != nil {
		t.Fatalf("SearchSimilarLaws: %v", err)
	}
	if len(resp.GetResults()) > 2 {
		t.Fatalf("expected at most 2 results, got %d", len(resp.GetResults()))
	}
}

func TestSearchSimilarLaws_NoMatches_ReturnsEmpty(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	// Empty library -> no results.
	resp, err := srv.SearchSimilarLaws(ctx, &flowv1.SearchSimilarLawsRequest{
		QueryText: "anything",
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("SearchSimilarLaws: %v", err)
	}
	if len(resp.GetResults()) != 0 {
		t.Fatalf("expected 0 results for empty library, got %d", len(resp.GetResults()))
	}
}

func TestSearchSimilarLaws_ResultsOrderedBySimilarity(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	// Create laws with varying goal lengths to produce different embeddings.
	// stubEmbedder: v[i] = float32(len(text)+i) / 100.0
	// "Short" (len=5) and "A much longer goal text here" (len=29) will have
	// different similarity to query "Short" (len=5).
	_, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Short",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})
	if err != nil {
		t.Fatalf("RecordFinding Short: %v", err)
	}
	_, err = srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "A much longer goal text here!!",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})
	if err != nil {
		t.Fatalf("RecordFinding Long: %v", err)
	}

	resp, err := srv.SearchSimilarLaws(ctx, &flowv1.SearchSimilarLawsRequest{
		QueryText: "Short",
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("SearchSimilarLaws: %v", err)
	}
	if len(resp.GetResults()) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(resp.GetResults()))
	}

	// Results should be ordered by similarity descending.
	for i := 1; i < len(resp.GetResults()); i++ {
		if resp.GetResults()[i].GetSimilarityScore() > resp.GetResults()[i-1].GetSimilarityScore() {
			t.Fatalf("results not ordered by similarity descending: [%d]=%f > [%d]=%f",
				i, resp.GetResults()[i].GetSimilarityScore(),
				i-1, resp.GetResults()[i-1].GetSimilarityScore())
		}
	}
}

func TestSearchSimilarLaws_EmptyQueryText(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	_, err := srv.SearchSimilarLaws(ctx, &flowv1.SearchSimilarLawsRequest{
		Limit: 10,
	})
	if err == nil {
		t.Fatal("expected InvalidArgument for empty query_text")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestSearchSimilarLaws_NoEmbedder(t *testing.T) {
	srv := newTestServer(t) // no embedder
	ctx := context.Background()

	_, err := srv.SearchSimilarLaws(ctx, &flowv1.SearchSimilarLawsRequest{
		QueryText: "anything",
		Limit:     10,
	})
	if err == nil {
		t.Fatal("expected FailedPrecondition when embedder is nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
}
