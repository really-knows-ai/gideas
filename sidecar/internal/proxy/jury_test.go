package proxy

import (
	"context"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// captureJuryServer captures Deliberate requests for assertions.
type captureJuryServer struct {
	flowv1.UnimplementedJuryServiceServer
	lastReq    *flowv1.DeliberateRequest
	capturedMD metadata.MD
}

func (s *captureJuryServer) Deliberate(
	ctx context.Context, req *flowv1.DeliberateRequest,
) (*flowv1.DeliberateResponse, error) {
	s.lastReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.DeliberateResponse{
		Outcome:    "favour_refiner",
		RoundsUsed: 2,
		Hung:       false,
		Justifications: []*flowv1.JurorJustification{
			{JurorId: "textualist-0", Outcome: "favour_refiner", Reasoning: "rules are clear"},
		},
	}, nil
}

func setupJuryProxy(t *testing.T) (*JuryProxy, *captureJuryServer) {
	t.Helper()

	capture := &captureJuryServer{}
	conn := dialBufconn(t, func(srv *grpc.Server) {
		flowv1.RegisterJuryServiceServer(srv, capture)
	})

	proxy := &JuryProxy{
		client: flowv1.NewJuryServiceClient(conn),
		conn:   conn,
	}

	return proxy, capture
}

func TestJuryProxy_Deliberate_Passthrough(t *testing.T) {
	proxy, capture := setupJuryProxy(t)

	resp, err := proxy.Deliberate(context.Background(), &flowv1.DeliberateRequest{
		Question:          "Should the refiner's refusal stand?",
		Evidence:          "## Evidence\nFeedback history...",
		AllowedOutcomes:   []string{"favour_refiner", "favour_reviewer"},
		ConsensusStrategy: flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY,
		MaxRounds:         3,
		JurySize:          5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify request was forwarded with all fields.
	if capture.lastReq.GetQuestion() != "Should the refiner's refusal stand?" {
		t.Fatalf("expected question forwarded, got %q", capture.lastReq.GetQuestion())
	}
	if capture.lastReq.GetEvidence() != "## Evidence\nFeedback history..." {
		t.Fatalf("expected evidence forwarded, got %q", capture.lastReq.GetEvidence())
	}
	if len(capture.lastReq.GetAllowedOutcomes()) != 2 {
		t.Fatalf("expected 2 allowed_outcomes, got %d", len(capture.lastReq.GetAllowedOutcomes()))
	}
	if capture.lastReq.GetConsensusStrategy() != flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY {
		t.Fatalf("expected SIMPLE_MAJORITY, got %v", capture.lastReq.GetConsensusStrategy())
	}
	if capture.lastReq.GetMaxRounds() != 3 {
		t.Fatalf("expected max_rounds=3, got %d", capture.lastReq.GetMaxRounds())
	}
	if capture.lastReq.GetJurySize() != 5 {
		t.Fatalf("expected jury_size=5, got %d", capture.lastReq.GetJurySize())
	}

	// Verify response passthrough.
	if resp.GetOutcome() != "favour_refiner" {
		t.Fatalf("expected outcome=favour_refiner, got %q", resp.GetOutcome())
	}
	if resp.GetRoundsUsed() != 2 {
		t.Fatalf("expected rounds_used=2, got %d", resp.GetRoundsUsed())
	}
	if resp.GetHung() {
		t.Fatal("expected hung=false")
	}
	if len(resp.GetJustifications()) != 1 {
		t.Fatalf("expected 1 justification, got %d", len(resp.GetJustifications()))
	}
}

func TestJuryProxy_Deliberate_PropagatesMetadata(t *testing.T) {
	proxy, capture := setupJuryProxy(t)

	md := metadata.Pairs("x-flow-workitem-id", "wi-jury-test")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.Deliberate(ctx, &flowv1.DeliberateRequest{
		Question:        "test question",
		AllowedOutcomes: []string{"yes", "no"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vals := capture.capturedMD.Get("x-flow-workitem-id")
	if len(vals) != 1 || vals[0] != "wi-jury-test" {
		t.Fatalf("expected metadata propagation, got %v", vals)
	}
}
