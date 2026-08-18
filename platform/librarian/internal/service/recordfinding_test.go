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

	md := metadata.Pairs(flowmeta.MetadataKeyCapabilities, "READ:law", flowmeta.MetadataKeyNodeID, "node-1")
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
	md := metadata.Pairs(flowmeta.MetadataKeyNodeID, "node-1")
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

func TestRecordFinding_StoresVecEmbedding(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	resp, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Finding with embedding",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "finding"}},
	})
	if err != nil {
		t.Fatalf("RecordFinding: %v", err)
	}

	if !vecEmbeddingStored(t, srv, resp.GetLawId(), "Finding with embedding") {
		t.Fatal("expected vec embedding to be stored after RecordFinding")
	}
}
