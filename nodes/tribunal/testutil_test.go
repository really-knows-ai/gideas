package main

import (
	"context"
	"net"
	"sync"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
)

// newLocalListener creates a TCP listener on an ephemeral localhost port.
func newLocalListener() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

// newSpyGRPCServer creates a gRPC server with the tribunalSpy registered
// for all seven Foundry Flow service interfaces.
func newSpyGRPCServer(spy *tribunalSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFlowMonitorServiceServer(srv, spy)
	flowv1.RegisterJuryServiceServer(srv, spy)
	flowv1.RegisterClerkServiceServer(srv, spy)
	return srv
}

// tribunalSpy captures calls to service operations for test assertions.
type tribunalSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFlowMonitorServiceServer
	flowv1.UnimplementedJuryServiceServer
	flowv1.UnimplementedClerkServiceServer

	mu sync.Mutex

	// Configurable responses.
	LawReferenceContent []byte                      // content of "law-reference" artefact
	Law                 *flowv1.Law                 // returned by GetLaw
	FrictionAggregates  []*flowv1.FrictionAggregate // returned by QueryFriction
	RelatedLaws         []*flowv1.Law               // returned by QueryLaws
	DeliberateResponse  *flowv1.DeliberateResponse  // returned by Deliberate
	DraftLawResponse    *flowv1.DraftLawResponse    // returned by DraftLaw

	// Configurable errors.
	GetArtefactErr   error
	GetLawErr        error
	QueryFrictionErr error
	QueryLawsErr     error
	DeliberateErr    error
	DraftLawErr      error
	RouteToOutputErr error
	CompleteErr      error

	// Recorded operations for assertions.
	RoutedOutputs   []string
	Completed       bool
	DeliberateCalls []deliberateRecord
	DraftLawCalls   []draftLawRecord
}

type deliberateRecord struct {
	Question        string
	Evidence        string
	AllowedOutcomes []string
	Strategy        flowv1.ConsensusStrategy
	MaxRounds       int32
	JurySize        int32
}

type draftLawRecord struct {
	Goal      string
	Tier      int32
	AppliesTo []string
	Outcome   string
}

func newTribunalSpy(tier flowv1.LawTier) *tribunalSpy {
	return &tribunalSpy{
		LawReferenceContent: []byte("law-under-review-001"),
		Law: &flowv1.Law{
			Id:        "law-under-review-001",
			Goal:      "Haiku must contain a seasonal reference",
			Tier:      tier,
			AppliesTo: []string{"haiku"},
			Representations: []*flowv1.Representation{
				{Type: "text/markdown", Content: "All haiku must include a kigo (seasonal word)."},
			},
		},
		DeliberateResponse: &flowv1.DeliberateResponse{
			Outcome:    "promote",
			RoundsUsed: 1,
		},
		DraftLawResponse: &flowv1.DraftLawResponse{
			LawId:       "law-promoted-001",
			VersionHash: "abc123",
		},
	}
}

// setupTribunalTest creates a flow.Client backed by the spy.
func setupTribunalTest(t *testing.T, spy *tribunalSpy) *flow.Client {
	t.Helper()

	lis, err := newLocalListener()
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	srv := newSpyGRPCServer(spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	t.Setenv(flow.EnvWorkitemID, "test-workitem")
	client, err := flow.NewClient(
		flow.WithSidecarAddress(lis.Addr().String()),
	)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *tribunalSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *tribunalSpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ri := req.GetRoutingInstruction()
	if ri == nil {
		return &flowv1.SubmitResultResponse{Accepted: true}, nil
	}

	isComplete := ri.GetType() == flowv1.RoutingType_ROUTING_TYPE_COMPLETE

	if isComplete {
		if s.CompleteErr != nil {
			return nil, s.CompleteErr
		}
		s.Completed = true
	} else {
		if s.RouteToOutputErr != nil {
			return nil, s.RouteToOutputErr
		}
		s.RoutedOutputs = append(s.RoutedOutputs, ri.GetTarget())
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

// ---------------------------------------------------------------------------
// Archivist methods
// ---------------------------------------------------------------------------

func (s *tribunalSpy) GetArtefact(
	_ context.Context, _ *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	if s.GetArtefactErr != nil {
		return nil, s.GetArtefactErr
	}
	return &flowv1.GetArtefactResponse{
		Content: s.LawReferenceContent,
	}, nil
}

// ---------------------------------------------------------------------------
// Librarian methods
// ---------------------------------------------------------------------------

func (s *tribunalSpy) GetLaw(
	_ context.Context, _ *flowv1.GetLawRequest,
) (*flowv1.GetLawResponse, error) {
	if s.GetLawErr != nil {
		return nil, s.GetLawErr
	}
	return &flowv1.GetLawResponse{Law: s.Law}, nil
}

func (s *tribunalSpy) QueryLaws(
	_ context.Context, _ *flowv1.QueryLawsRequest,
) (*flowv1.QueryLawsResponse, error) {
	if s.QueryLawsErr != nil {
		return nil, s.QueryLawsErr
	}
	return &flowv1.QueryLawsResponse{Laws: s.RelatedLaws}, nil
}

// ---------------------------------------------------------------------------
// Monitor methods
// ---------------------------------------------------------------------------

func (s *tribunalSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

func (s *tribunalSpy) QueryFriction(
	_ context.Context, _ *flowv1.QueryFrictionRequest,
) (*flowv1.QueryFrictionResponse, error) {
	if s.QueryFrictionErr != nil {
		return nil, s.QueryFrictionErr
	}
	return &flowv1.QueryFrictionResponse{
		FrictionAggregates: s.FrictionAggregates,
	}, nil
}

// ---------------------------------------------------------------------------
// Jury methods
// ---------------------------------------------------------------------------

func (s *tribunalSpy) Deliberate(
	_ context.Context, req *flowv1.DeliberateRequest,
) (*flowv1.DeliberateResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.DeliberateErr != nil {
		return nil, s.DeliberateErr
	}
	s.DeliberateCalls = append(s.DeliberateCalls, deliberateRecord{
		Question:        req.GetQuestion(),
		Evidence:        req.GetEvidence(),
		AllowedOutcomes: req.GetAllowedOutcomes(),
		Strategy:        req.GetConsensusStrategy(),
		MaxRounds:       req.GetMaxRounds(),
		JurySize:        req.GetJurySize(),
	})
	return s.DeliberateResponse, nil
}

// ---------------------------------------------------------------------------
// Clerk methods
// ---------------------------------------------------------------------------

func (s *tribunalSpy) DraftLaw(
	_ context.Context, req *flowv1.DraftLawRequest,
) (*flowv1.DraftLawResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.DraftLawErr != nil {
		return nil, s.DraftLawErr
	}
	s.DraftLawCalls = append(s.DraftLawCalls, draftLawRecord{
		Goal:      req.GetGoal(),
		Tier:      req.GetTier(),
		AppliesTo: req.GetAppliesTo(),
		Outcome:   req.GetVerdict().GetOutcome(),
	})
	return s.DraftLawResponse, nil
}
