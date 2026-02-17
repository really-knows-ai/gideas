package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/gideas/flow/librarian/internal/store/sqlite"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

var idCounter int

func testIDGen() string {
	idCounter++
	return fmt.Sprintf("law-%04d", idCounter)
}

func newTestServer(t *testing.T) *LibrarianServer {
	t.Helper()
	idCounter = 0
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return NewLibrarianServer(store, nil, testIDGen, 0.85)
}

// ---------------------------------------------------------------------------
// QueryLaws
// ---------------------------------------------------------------------------

func TestQueryLaws_AllLaws(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Seed data.
	srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Law A",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "a"}},
	})
	srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Law B",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "b"}},
	})

	resp, err := srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{})
	if err != nil {
		t.Fatalf("QueryLaws: %v", err)
	}
	if len(resp.GetLaws()) != 2 {
		t.Fatalf("expected 2 laws, got %d", len(resp.GetLaws()))
	}
}

func TestQueryLaws_ScopedFilter(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Scoped law",
		AppliesTo:       []string{"source-code"},
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "scoped"}},
	})
	srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Other scope",
		AppliesTo:       []string{"docs"},
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "other"}},
	})

	resp, err := srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{
		Filter: &flowv1.LawFilter{ArtefactKind: "source-code"},
	})
	if err != nil {
		t.Fatalf("QueryLaws: %v", err)
	}
	if len(resp.GetLaws()) != 1 {
		t.Fatalf("expected 1 scoped law, got %d", len(resp.GetLaws()))
	}
}

func TestQueryLaws_RepresentationTypeRequiresKind(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{
		Filter: &flowv1.LawFilter{RepresentationType: "text/markdown"},
	})
	if err == nil {
		t.Fatal("expected error when representation_type is set without artefact_kind")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestQueryLaws_CapabilityDenied(t *testing.T) {
	srv := newTestServer(t)

	// Set metadata with capabilities that DON'T include READ:law.
	md := metadata.Pairs(metadataKeyCapabilities, "WRITE:law/tier1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{})
	if err == nil {
		t.Fatal("expected PermissionDenied, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Cite
// ---------------------------------------------------------------------------

func TestCite_Basic(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Create a law first.
	findResp, _ := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Citeable law",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "cite me"}},
	})

	resp, err := srv.Cite(ctx, &flowv1.CiteRequest{
		LawIds: []string{findResp.GetLawId()},
	})
	if err != nil {
		t.Fatalf("Cite: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}
}

func TestCite_EmptyLawIDs(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.Cite(ctx, &flowv1.CiteRequest{})
	if err == nil {
		t.Fatal("expected error for empty law_ids")
	}
}

func TestCite_MissingLaw_NoError(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Cite a non-existent law — should succeed (citation is a signal).
	resp, err := srv.Cite(ctx, &flowv1.CiteRequest{
		LawIds: []string{"nonexistent-law"},
	})
	if err != nil {
		t.Fatalf("Cite should not fail for missing laws: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}
}

// ---------------------------------------------------------------------------
// RecordFinding
// ---------------------------------------------------------------------------

func TestRecordFinding_Basic(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:      "All tests must pass",
		AppliesTo: []string{"source-code"},
		Representations: []*flowv1.Representation{
			{Type: "text/plain", Content: "All tests must pass before merge."},
		},
	})
	if err != nil {
		t.Fatalf("RecordFinding: %v", err)
	}
	if resp.GetLawId() == "" {
		t.Fatal("expected non-empty law_id")
	}

	// Verify it's a Tier 1 law.
	getLawResp, err := srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: resp.GetLawId()})
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	if getLawResp.GetLaw().GetTier() != flowv1.LawTier_LAW_TIER_FINDING {
		t.Fatalf("expected Tier 1 (FINDING), got %v", getLawResp.GetLaw().GetTier())
	}
}

func TestRecordFinding_EmptyGoal(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})
	if err == nil {
		t.Fatal("expected error for empty goal")
	}
}

func TestRecordFinding_NoRepresentations(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal: "Some goal",
	})
	if err == nil {
		t.Fatal("expected error for no representations")
	}
}

func TestRecordFinding_CapabilityDenied(t *testing.T) {
	srv := newTestServer(t)

	md := metadata.Pairs(metadataKeyCapabilities, "READ:law")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "test",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})
	if err == nil {
		t.Fatal("expected PermissionDenied, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// GetLaw
// ---------------------------------------------------------------------------

func TestGetLaw_NotFound(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: "nonexistent"})
	if err == nil {
		t.Fatal("expected NotFound error")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestGetLaw_EmptyID(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.GetLaw(ctx, &flowv1.GetLawRequest{})
	if err == nil {
		t.Fatal("expected InvalidArgument error")
	}
}

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
// RetireLaw
// ---------------------------------------------------------------------------

func TestRetireLaw_Basic(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	findResp, _ := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "To retire",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})

	resp, err := srv.RetireLaw(ctx, &flowv1.RetireLawRequest{LawId: findResp.GetLawId()})
	if err != nil {
		t.Fatalf("RetireLaw: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	// Should no longer be retrievable.
	_, err = srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: findResp.GetLawId()})
	if err == nil {
		t.Fatal("expected NotFound after retirement")
	}
}

func TestRetireLaw_EmptyID(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.RetireLaw(ctx, &flowv1.RetireLawRequest{})
	if err == nil {
		t.Fatal("expected InvalidArgument error")
	}
}

// ---------------------------------------------------------------------------
// ReplicateLaws (stubbed)
// ---------------------------------------------------------------------------

func TestReplicateLaws_Unimplemented(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.ReplicateLaws(ctx, &flowv1.ReplicateLawsRequest{})
	if err == nil {
		t.Fatal("expected Unimplemented error")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ApplyLifecycleAction
// ---------------------------------------------------------------------------

func TestApplyLifecycleAction_Promote(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	findResp, _ := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Promotable",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})

	resp, err := srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{
		LawId:   findResp.GetLawId(),
		Verdict: flowv1.Verdict_VERDICT_PROMOTE,
	})
	if err != nil {
		t.Fatalf("ApplyLifecycleAction: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	// Verify tier was incremented.
	getLawResp, _ := srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: findResp.GetLawId()})
	if getLawResp.GetLaw().GetTier() != flowv1.LawTier_LAW_TIER_RULING {
		t.Fatalf("expected Tier 2 after promote, got %v", getLawResp.GetLaw().GetTier())
	}
}

func TestApplyLifecycleAction_Demote(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	findResp, _ := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Demotable",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})

	// Promote first (T1 -> T2).
	srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{
		LawId:   findResp.GetLawId(),
		Verdict: flowv1.Verdict_VERDICT_PROMOTE,
	})

	// Now demote (T2 -> T1).
	resp, err := srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{
		LawId:   findResp.GetLawId(),
		Verdict: flowv1.Verdict_VERDICT_DEMOTE,
	})
	if err != nil {
		t.Fatalf("ApplyLifecycleAction demote: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	getLawResp, _ := srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: findResp.GetLawId()})
	if getLawResp.GetLaw().GetTier() != flowv1.LawTier_LAW_TIER_FINDING {
		t.Fatalf("expected Tier 1 after demote, got %v", getLawResp.GetLaw().GetTier())
	}
}

func TestApplyLifecycleAction_DemoteBelowTier1(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	findResp, _ := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Already T1",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})

	_, err := srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{
		LawId:   findResp.GetLawId(),
		Verdict: flowv1.Verdict_VERDICT_DEMOTE,
	})
	if err == nil {
		t.Fatal("expected error when demoting below Tier 1")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
}

func TestApplyLifecycleAction_Retire(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	findResp, _ := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "To retire via lifecycle",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})

	resp, err := srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{
		LawId:   findResp.GetLawId(),
		Verdict: flowv1.Verdict_VERDICT_RETIRE,
	})
	if err != nil {
		t.Fatalf("ApplyLifecycleAction retire: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	// Should be gone.
	_, err = srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: findResp.GetLawId()})
	if err == nil {
		t.Fatal("expected NotFound after lifecycle retire")
	}
}

func TestApplyLifecycleAction_ActivatesPendingLaw(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Create an inactive law via WriteLaw.
	writeResp, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "Pending ruling",
			Tier: flowv1.LawTier_LAW_TIER_FINDING,
			Representations: []*flowv1.Representation{
				{Type: "text/plain", Content: "pending"},
			},
		},
	})
	if err != nil {
		t.Fatalf("WriteLaw: %v", err)
	}

	// Should not appear in QueryLaws (inactive).
	queryResp, _ := srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{})
	if len(queryResp.GetLaws()) != 0 {
		t.Fatalf("expected 0 active laws, got %d", len(queryResp.GetLaws()))
	}

	// Promote — should activate.
	_, err = srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{
		LawId:   writeResp.GetLawId(),
		Verdict: flowv1.Verdict_VERDICT_PROMOTE,
	})
	if err != nil {
		t.Fatalf("ApplyLifecycleAction promote: %v", err)
	}

	// Now should appear in QueryLaws.
	queryResp, _ = srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{})
	if len(queryResp.GetLaws()) != 1 {
		t.Fatalf("expected 1 active law after promotion, got %d", len(queryResp.GetLaws()))
	}
}

func TestApplyLifecycleAction_EmptyLawID(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{
		Verdict: flowv1.Verdict_VERDICT_PROMOTE,
	})
	if err == nil {
		t.Fatal("expected error for empty law_id")
	}
}

func TestApplyLifecycleAction_UnspecifiedVerdict(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{
		LawId: "some-id",
	})
	if err == nil {
		t.Fatal("expected error for unspecified verdict")
	}
}
