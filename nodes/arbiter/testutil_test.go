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

// newSpyGRPCServer creates a gRPC server with the arbiterSpy registered
// for all seven Foundry Flow service interfaces.
func newSpyGRPCServer(spy *arbiterSpy) *grpc.Server {
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

// arbiterSpy captures calls to service operations for test assertions.
type arbiterSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFlowMonitorServiceServer
	flowv1.UnimplementedJuryServiceServer
	flowv1.UnimplementedClerkServiceServer

	mu sync.Mutex

	// Configurable topology response.
	TopologyResponse *flowv1.GetFlowTopologyResponse

	// Configurable feedback items returned by GetFeedback.
	FeedbackItems []*flowv1.FeedbackItem

	// Configurable artefact content returned by GetArtefact.
	ArtefactContent []byte

	// Configurable laws returned by QueryLaws.
	Laws []*flowv1.Law

	// Configurable friction aggregates returned by QueryFriction.
	FrictionAggregates []*flowv1.FrictionAggregate

	// Configurable deliberation response.
	DeliberateResponse *flowv1.DeliberateResponse

	// Configurable DraftLaw response.
	DraftLawResponse *flowv1.DraftLawResponse

	// Configurable error returns.
	GetFlowTopologyErr error
	GetFeedbackErr     error
	GetArtefactErr     error
	QueryLawsErr       error
	QueryFrictionErr   error
	DeliberateErr      error
	DraftLawErr        error
	LinkRulingErr      error
	RouteToOutputErr   error

	// Recorded operations for assertions.
	RoutedOutputs      []string
	LinkedRulings      []linkRulingRecord
	DeliberateCalls    []deliberateRecord
	DraftLawCalls      []draftLawRecord
	StoreArtefactCalls []storeArtefactRecord
	Completed          bool
}

type storeArtefactRecord struct {
	ArtefactID string
	Content    []byte
}

type linkRulingRecord struct {
	FeedbackID  string
	LawID       string
	TargetState flowv1.FeedbackState
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

func newArbiterSpy() *arbiterSpy {
	return &arbiterSpy{
		TopologyResponse: defaultArbiterTopology(),
		ArtefactContent:  []byte("sample artefact content"),
		DeliberateResponse: &flowv1.DeliberateResponse{
			Outcome: "favour_reviewer",
			Justifications: []*flowv1.JurorJustification{
				{JurorId: "juror-1", Outcome: "favour_reviewer", Reasoning: "Strong evidence"},
			},
			RoundsUsed: 1,
			Hung:       false,
		},
		DraftLawResponse: &flowv1.DraftLawResponse{
			LawId:       "law-ruling-001",
			VersionHash: "abc123",
		},
	}
}

// defaultArbiterTopology returns a topology appropriate for arbiter tests.
func defaultArbiterTopology() *flowv1.GetFlowTopologyResponse {
	return &flowv1.GetFlowTopologyResponse{
		Self: &flowv1.FlowNode{
			Name: "arbiter",
			Outputs: []*flowv1.FlowOutput{
				{Name: "sort", Target: "sort"},
				{Name: "advocate", Target: "advocate"},
			},
		},
		Nodes: map[string]*flowv1.FlowNode{
			"arbiter": {Name: "arbiter"},
			"sort":    {Name: "sort"},
		},
		ExitContract: map[string]*flowv1.StampRequirements{
			"haiku": {Stamps: []string{"linter", "review", "approval"}},
		},
	}
}

// setupArbiterTest creates a flow.Client backed by the spy.
func setupArbiterTest(t *testing.T, spy *arbiterSpy) *flow.Client {
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

func (s *arbiterSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *arbiterSpy) SubmitResult(
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
// Operator methods
// ---------------------------------------------------------------------------

func (s *arbiterSpy) GetFlowTopology(
	_ context.Context, _ *flowv1.GetFlowTopologyRequest,
) (*flowv1.GetFlowTopologyResponse, error) {
	if s.GetFlowTopologyErr != nil {
		return nil, s.GetFlowTopologyErr
	}
	return s.TopologyResponse, nil
}

// ---------------------------------------------------------------------------
// Archivist methods
// ---------------------------------------------------------------------------

func (s *arbiterSpy) GetArtefact(
	_ context.Context, _ *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	if s.GetArtefactErr != nil {
		return nil, s.GetArtefactErr
	}
	return &flowv1.GetArtefactResponse{
		Content: s.ArtefactContent,
	}, nil
}

func (s *arbiterSpy) StoreArtefact(
	_ context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.StoreArtefactCalls = append(s.StoreArtefactCalls, storeArtefactRecord{
		ArtefactID: req.GetArtefactId(),
		Content:    req.GetContent(),
	})
	return &flowv1.StoreArtefactResponse{
		VersionHash:  "mock-hash",
		IsNewVersion: true,
	}, nil
}

func (s *arbiterSpy) GetFeedback(
	_ context.Context, _ *flowv1.GetFeedbackRequest,
) (*flowv1.GetFeedbackResponse, error) {
	if s.GetFeedbackErr != nil {
		return nil, s.GetFeedbackErr
	}
	return &flowv1.GetFeedbackResponse{
		FeedbackItems: s.FeedbackItems,
	}, nil
}

func (s *arbiterSpy) LinkRuling(
	_ context.Context, req *flowv1.LinkRulingRequest,
) (*flowv1.LinkRulingResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.LinkRulingErr != nil {
		return nil, s.LinkRulingErr
	}
	s.LinkedRulings = append(s.LinkedRulings, linkRulingRecord{
		FeedbackID:  req.GetFeedbackId(),
		LawID:       req.GetLawId(),
		TargetState: req.GetTargetState(),
	})
	return &flowv1.LinkRulingResponse{
		UpdatedItem: &flowv1.FeedbackItem{
			Id:           req.GetFeedbackId(),
			State:        req.GetTargetState(),
			LinkedRuling: req.GetLawId(),
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Librarian methods
// ---------------------------------------------------------------------------

func (s *arbiterSpy) QueryLaws(
	_ context.Context, _ *flowv1.QueryLawsRequest,
) (*flowv1.QueryLawsResponse, error) {
	if s.QueryLawsErr != nil {
		return nil, s.QueryLawsErr
	}
	return &flowv1.QueryLawsResponse{Laws: s.Laws}, nil
}

// ---------------------------------------------------------------------------
// Monitor methods
// ---------------------------------------------------------------------------

func (s *arbiterSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

func (s *arbiterSpy) QueryFriction(
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

func (s *arbiterSpy) Deliberate(
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

func (s *arbiterSpy) DraftLaw(
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
