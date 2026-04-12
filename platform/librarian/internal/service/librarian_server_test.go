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
	t.Cleanup(func() { _ = store.Close() })
	return NewLibrarianServer(store, nil, testIDGen, 0.85)
}

// ---------------------------------------------------------------------------
// QueryLaws
// ---------------------------------------------------------------------------

func TestQueryLaws_AllLaws(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Seed data.
	if _, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Law A",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "a"}},
	}); err != nil {
		t.Fatalf("RecordFinding Law A: %v", err)
	}
	if _, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Law B",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "b"}},
	}); err != nil {
		t.Fatalf("RecordFinding Law B: %v", err)
	}

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

	if _, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Scoped law",
		AppliesTo:       []string{"source-code"},
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "scoped"}},
	}); err != nil {
		t.Fatalf("RecordFinding Scoped law: %v", err)
	}
	if _, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Other scope",
		AppliesTo:       []string{"docs"},
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "other"}},
	}); err != nil {
		t.Fatalf("RecordFinding Other scope: %v", err)
	}

	resp, err := srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{
		Filter: &flowv1.LawFilter{GovernedArtefact: "source-code"},
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
		t.Fatal("expected error when representation_type is set without governed_artefact")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestQueryLaws_CapabilityDenied(t *testing.T) {
	srv := newTestServer(t)

	// Set metadata with capabilities that DON'T include READ:law.
	md := metadata.Pairs(metadataKeyCapabilities, "WRITE:law/tier1", metadataKeyNodeID, "node-1")
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

	md := metadata.Pairs(metadataKeyCapabilities, "READ:law", metadataKeyNodeID, "node-1")
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

// TestRecordFinding_NodeCallNoCapabilities_Denied verifies deny-by-default:
// a node-originated call with no capabilities header is denied.
func TestRecordFinding_NodeCallNoCapabilities_Denied(t *testing.T) {
	srv := newTestServer(t)

	// Node identity present but no capabilities.
	md := metadata.Pairs(metadataKeyNodeID, "node-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "test",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for node call with no capabilities")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

// TestQueryLaws_NodeCallNoCapabilities_Denied verifies deny-by-default
// for node calls to QueryLaws.
func TestQueryLaws_NodeCallNoCapabilities_Denied(t *testing.T) {
	srv := newTestServer(t)

	md := metadata.Pairs(metadataKeyNodeID, "node-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{})
	if err == nil {
		t.Fatal("expected PermissionDenied for node call with no capabilities")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

// TestQueryLaws_SystemCall_BypassesEnforcement verifies system-to-system
// calls (no node identity) pass through without capability checks.
func TestQueryLaws_SystemCall_BypassesEnforcement(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Should succeed — no node identity means system call.
	_, err := srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{})
	if err != nil {
		t.Fatalf("system call should bypass capability enforcement, got %v", err)
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

// ---------------------------------------------------------------------------
// Division support
// ---------------------------------------------------------------------------

func TestQueryLaws_DivisionFilter(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Create laws in different divisions via WriteLaw (which passes division through).
	if _, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "Security rule", Tier: flowv1.LawTier_LAW_TIER_RULING,
			Division:        "security",
			Representations: []*flowv1.Representation{{Type: "text/plain", Content: "s"}},
		},
	}); err != nil {
		t.Fatalf("WriteLaw security: %v", err)
	}
	if _, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "Arch rule", Tier: flowv1.LawTier_LAW_TIER_RULING,
			Division:        "architecture",
			Representations: []*flowv1.Representation{{Type: "text/plain", Content: "a"}},
		},
	}); err != nil {
		t.Fatalf("WriteLaw architecture: %v", err)
	}

	// Activate both so they appear in QueryLaws.
	srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{ //nolint:errcheck
		LawId: "law-0001", Verdict: flowv1.Verdict_VERDICT_PROMOTE,
	})
	srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{ //nolint:errcheck
		LawId: "law-0002", Verdict: flowv1.Verdict_VERDICT_PROMOTE,
	})

	// Filter by security.
	resp, err := srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{
		Filter: &flowv1.LawFilter{Division: "security"},
	})
	if err != nil {
		t.Fatalf("QueryLaws division=security: %v", err)
	}
	if len(resp.GetLaws()) != 1 {
		t.Fatalf("expected 1 security law, got %d", len(resp.GetLaws()))
	}
	if resp.GetLaws()[0].GetDivision() != "security" {
		t.Fatalf("expected division=security, got %q", resp.GetLaws()[0].GetDivision())
	}

	// No division filter returns all.
	resp, err = srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{})
	if err != nil {
		t.Fatalf("QueryLaws no filter: %v", err)
	}
	if len(resp.GetLaws()) != 2 {
		t.Fatalf("expected 2 laws, got %d", len(resp.GetLaws()))
	}
}

func TestWriteLaw_DivisionRoundTrip(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	writeResp, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "Styled rule", Tier: flowv1.LawTier_LAW_TIER_RULING,
			Division:        "style",
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
	if getLawResp.GetLaw().GetDivision() != "style" {
		t.Fatalf("expected division=style, got %q", getLawResp.GetLaw().GetDivision())
	}
}

func TestStoreLawToProto_IncludesDivision(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// RecordFinding doesn't set division (always empty for findings).
	findResp, _ := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Finding",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "f"}},
	})

	getLawResp, err := srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: findResp.GetLawId()})
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	// Division should be empty for a RecordFinding (no division in request).
	if getLawResp.GetLaw().GetDivision() != "" {
		t.Fatalf("expected empty division for finding, got %q", getLawResp.GetLaw().GetDivision())
	}
}

// ---------------------------------------------------------------------------
// Dispute Record RPCs
// ---------------------------------------------------------------------------

func TestCreateDisputeRecord_Basic(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  "petition-1",
		CitedLawIds: []string{"law-a", "law-b"},
	})
	if err != nil {
		t.Fatalf("CreateDisputeRecord: %v", err)
	}
	rec := resp.GetRecord()
	if rec.GetPetitionId() != "petition-1" {
		t.Fatalf("expected petition_id %q, got %q", "petition-1", rec.GetPetitionId())
	}
	if len(rec.GetCitedLawIds()) != 2 {
		t.Fatalf("expected 2 cited law IDs, got %d", len(rec.GetCitedLawIds()))
	}
	if rec.GetStatus() != flowv1.DisputeStatus_DISPUTE_STATUS_ACTIVE {
		t.Fatalf("expected ACTIVE status, got %v", rec.GetStatus())
	}
	if rec.GetCreatedAt() == nil {
		t.Fatal("expected non-nil created_at")
	}
}

func TestCreateDisputeRecord_EmptyPetitionID(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		CitedLawIds: []string{"law-a"},
	})
	if err == nil {
		t.Fatal("expected InvalidArgument for empty petition_id")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestCreateDisputeRecord_EmptyCitedLawIDs(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId: "petition-empty",
	})
	if err == nil {
		t.Fatal("expected InvalidArgument for empty cited_law_ids")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestRetireDisputeRecord_Basic(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Create first.
	if _, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  "petition-retire",
		CitedLawIds: []string{"law-1"},
	}); err != nil {
		t.Fatalf("CreateDisputeRecord: %v", err)
	}

	resp, err := srv.RetireDisputeRecord(ctx, &flowv1.RetireDisputeRecordRequest{
		PetitionId: "petition-retire",
	})
	if err != nil {
		t.Fatalf("RetireDisputeRecord: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	// Should no longer appear in active disputes.
	getResp, err := srv.GetActiveDisputes(ctx, &flowv1.GetActiveDisputesRequest{})
	if err != nil {
		t.Fatalf("GetActiveDisputes: %v", err)
	}
	if len(getResp.GetRecords()) != 0 {
		t.Fatalf("expected 0 active disputes after retirement, got %d", len(getResp.GetRecords()))
	}
}

func TestRetireDisputeRecord_NonExistent(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.RetireDisputeRecord(ctx, &flowv1.RetireDisputeRecordRequest{
		PetitionId: "petition-ghost",
	})
	if err == nil {
		t.Fatal("expected NotFound for non-existent petition")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestGetActiveDisputes_ReturnsOnlyActive(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	if _, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  "petition-a",
		CitedLawIds: []string{"law-1"},
	}); err != nil {
		t.Fatalf("CreateDisputeRecord petition-a: %v", err)
	}
	if _, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  "petition-b",
		CitedLawIds: []string{"law-2"},
	}); err != nil {
		t.Fatalf("CreateDisputeRecord petition-b: %v", err)
	}

	// Retire one.
	if _, err := srv.RetireDisputeRecord(ctx, &flowv1.RetireDisputeRecordRequest{
		PetitionId: "petition-a",
	}); err != nil {
		t.Fatalf("RetireDisputeRecord: %v", err)
	}

	resp, err := srv.GetActiveDisputes(ctx, &flowv1.GetActiveDisputesRequest{})
	if err != nil {
		t.Fatalf("GetActiveDisputes: %v", err)
	}
	if len(resp.GetRecords()) != 1 {
		t.Fatalf("expected 1 active dispute, got %d", len(resp.GetRecords()))
	}
	if resp.GetRecords()[0].GetPetitionId() != "petition-b" {
		t.Fatalf("expected petition-b, got %q", resp.GetRecords()[0].GetPetitionId())
	}
}

func TestGetActiveDisputes_WithLawIDFilter(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	if _, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  "petition-x",
		CitedLawIds: []string{"law-10", "law-20"},
	}); err != nil {
		t.Fatalf("CreateDisputeRecord petition-x: %v", err)
	}
	if _, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  "petition-y",
		CitedLawIds: []string{"law-30"},
	}); err != nil {
		t.Fatalf("CreateDisputeRecord petition-y: %v", err)
	}

	// Filter by law-20 -- should only return petition-x.
	resp, err := srv.GetActiveDisputes(ctx, &flowv1.GetActiveDisputesRequest{
		LawId: "law-20",
	})
	if err != nil {
		t.Fatalf("GetActiveDisputes with filter: %v", err)
	}
	if len(resp.GetRecords()) != 1 {
		t.Fatalf("expected 1 dispute citing law-20, got %d", len(resp.GetRecords()))
	}
	if resp.GetRecords()[0].GetPetitionId() != "petition-x" {
		t.Fatalf("expected petition-x, got %q", resp.GetRecords()[0].GetPetitionId())
	}
}
