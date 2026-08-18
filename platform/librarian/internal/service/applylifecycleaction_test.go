package service

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
	if _, err := srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{
		LawId:   findResp.GetLawId(),
		Verdict: flowv1.Verdict_VERDICT_PROMOTE,
	}); err != nil {
		t.Fatalf("ApplyLifecycleAction promote: %v", err)
	}

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
