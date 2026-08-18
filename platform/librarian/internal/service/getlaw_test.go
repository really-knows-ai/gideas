package service

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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

func TestStoreLawToProto_IncludesGroup(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// RecordFinding doesn't set group (always empty for findings).
	findResp, _ := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Finding",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "f"}},
	})

	getLawResp, err := srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: findResp.GetLawId()})
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	// Group should be empty for a RecordFinding (no group in request).
	if getLawResp.GetLaw().GetGroup() != "" {
		t.Fatalf("expected empty group for finding, got %q", getLawResp.GetLaw().GetGroup())
	}
}
