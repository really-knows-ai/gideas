package proxy

import (
	"context"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// captureClerkServer captures DraftLaw requests for assertions.
type captureClerkServer struct {
	flowv1.UnimplementedClerkServiceServer
	lastReq    *flowv1.DraftLawRequest
	capturedMD metadata.MD
}

func (s *captureClerkServer) DraftLaw(
	ctx context.Context, req *flowv1.DraftLawRequest,
) (*flowv1.DraftLawResponse, error) {
	s.lastReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.DraftLawResponse{
		LawId:       "law-drafted-001",
		VersionHash: "abc123",
		Representations: []*flowv1.Representation{
			{Type: "text/markdown", Content: "# Ruling\nThe refiner prevails."},
		},
	}, nil
}

func setupClerkProxy(t *testing.T) (*ClerkProxy, *captureClerkServer) {
	t.Helper()

	capture := &captureClerkServer{}
	conn := dialBufconn(t, func(srv *grpc.Server) {
		flowv1.RegisterClerkServiceServer(srv, capture)
	})

	proxy := &ClerkProxy{
		client: flowv1.NewClerkServiceClient(conn),
		conn:   conn,
	}

	return proxy, capture
}

func TestClerkProxy_DraftLaw_Passthrough(t *testing.T) {
	proxy, capture := setupClerkProxy(t)

	resp, err := proxy.DraftLaw(context.Background(), &flowv1.DraftLawRequest{
		Verdict: &flowv1.DeliberateResponse{
			Outcome:    "favour_refiner",
			RoundsUsed: 1,
			Hung:       false,
			Justifications: []*flowv1.JurorJustification{
				{JurorId: "textualist-0", Outcome: "favour_refiner", Reasoning: "clear precedent"},
			},
		},
		Goal:      "Enforce consistent formatting in markdown artefacts",
		Tier:      2,
		AppliesTo: []string{"md", "txt"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify request was forwarded with all fields.
	if capture.lastReq.GetGoal() != "Enforce consistent formatting in markdown artefacts" {
		t.Fatalf("expected goal forwarded, got %q", capture.lastReq.GetGoal())
	}
	if capture.lastReq.GetTier() != 2 {
		t.Fatalf("expected tier=2, got %d", capture.lastReq.GetTier())
	}
	if len(capture.lastReq.GetAppliesTo()) != 2 {
		t.Fatalf("expected 2 applies_to, got %d", len(capture.lastReq.GetAppliesTo()))
	}
	if capture.lastReq.GetVerdict().GetOutcome() != "favour_refiner" {
		t.Fatalf("expected verdict outcome forwarded, got %q", capture.lastReq.GetVerdict().GetOutcome())
	}

	// Verify response passthrough.
	if resp.GetLawId() != "law-drafted-001" {
		t.Fatalf("expected law_id=law-drafted-001, got %q", resp.GetLawId())
	}
	if resp.GetVersionHash() != "abc123" {
		t.Fatalf("expected version_hash=abc123, got %q", resp.GetVersionHash())
	}
	if len(resp.GetRepresentations()) != 1 {
		t.Fatalf("expected 1 representation, got %d", len(resp.GetRepresentations()))
	}
	if resp.GetRepresentations()[0].GetType() != "text/markdown" {
		t.Fatalf("expected type=text/markdown, got %q", resp.GetRepresentations()[0].GetType())
	}
}

func TestClerkProxy_DraftLaw_PropagatesMetadata(t *testing.T) {
	proxy, capture := setupClerkProxy(t)

	md := metadata.Pairs("x-flow-workitem-id", "wi-clerk-test")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.DraftLaw(ctx, &flowv1.DraftLawRequest{
		Verdict: &flowv1.DeliberateResponse{Outcome: "favour_reviewer"},
		Goal:    "test goal",
		Tier:    1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vals := capture.capturedMD.Get("x-flow-workitem-id")
	if len(vals) != 1 || vals[0] != "wi-clerk-test" {
		t.Fatalf("expected metadata propagation, got %v", vals)
	}
}
