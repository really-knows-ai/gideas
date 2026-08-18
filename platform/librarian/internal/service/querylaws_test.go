package service

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowmeta "github.com/foundry/flow/pkg/metadata"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

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
	md := metadata.Pairs(flowmeta.MetadataKeyCapabilities, "WRITE:law/tier1", flowmeta.MetadataKeyNodeID, "node-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{})
	if err == nil {
		t.Fatal("expected PermissionDenied, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

// TestQueryLaws_NodeCallNoCapabilities_Denied verifies deny-by-default
// for node calls to QueryLaws.
func TestQueryLaws_NodeCallNoCapabilities_Denied(t *testing.T) {
	srv := newTestServer(t)

	md := metadata.Pairs(flowmeta.MetadataKeyNodeID, "node-1")
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
// Group support
// ---------------------------------------------------------------------------

func TestQueryLaws_GroupFilter(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Create laws in different groups via WriteLaw (which passes group through).
	if _, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "Security rule", Tier: flowv1.LawTier_LAW_TIER_RULING,
			Group:           "security",
			Representations: []*flowv1.Representation{{Type: "text/plain", Content: "s"}},
		},
	}); err != nil {
		t.Fatalf("WriteLaw security: %v", err)
	}
	if _, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "Arch rule", Tier: flowv1.LawTier_LAW_TIER_RULING,
			Group:           "architecture",
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
		Filter: &flowv1.LawFilter{Group: testGroupSecurity},
	})
	if err != nil {
		t.Fatalf("QueryLaws group=security: %v", err)
	}
	if len(resp.GetLaws()) != 1 {
		t.Fatalf("expected 1 security law, got %d", len(resp.GetLaws()))
	}
	if resp.GetLaws()[0].GetGroup() != testGroupSecurity {
		t.Fatalf("expected group=%s, got %q", testGroupSecurity, resp.GetLaws()[0].GetGroup())
	}

	// No group filter returns all.
	resp, err = srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{})
	if err != nil {
		t.Fatalf("QueryLaws no filter: %v", err)
	}
	if len(resp.GetLaws()) != 2 {
		t.Fatalf("expected 2 laws, got %d", len(resp.GetLaws()))
	}
}

// TestQueryLaws_GroupFilter is tested at the store level (TestQueryLaws_GroupFilter,
// TestQueryLaws_GroupAndArtefactFilter, TestQueryLaws_GroupFilter).
// The server handler simply passes the Group field through to the store's QueryFilter.
// The handler wiring for group filter is verified by TestQueryLaws_AllLaws (no filter)
// and at the store level.
