package main

import (
	"context"
	"net"
	"sync"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
)

// newLocalListener creates a TCP listener on an ephemeral localhost port.
func newLocalListener() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

// newSpyGRPCServer creates a gRPC server with the sortSpy registered
// for all five Foundry Flow service interfaces.
func newSpyGRPCServer(spy *sortSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFlowMonitorServiceServer(srv, spy)
	return srv
}

// sortSpy captures calls to stamp, feedback, routing, topology, and completion
// operations for test assertions. It embeds all unimplemented servers
// and overrides the methods the sort handler calls.
type sortSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFlowMonitorServiceServer

	mu sync.Mutex

	// Configurable topology response returned by GetFlowTopology.
	TopologyResponse *flowv1.GetFlowTopologyResponse

	// Configurable stamp state: stamp name → present.
	StampState map[string]bool

	// Configurable feedback items returned by GetFeedback.
	FeedbackItems []*flowv1.FeedbackItem

	// Configurable feedback depths: feedback ID → depth.
	FeedbackDepths map[string]int32

	// Configurable error returns for specific operations.
	GetFlowTopologyErr  error
	HasStampErr         error
	GetFeedbackErr      error
	GetFeedbackDepthErr error
	DeadlockFeedbackErr error
	RouteToOutputErr    error
	StampArtefactErr    error
	CompleteErr         error

	// Recorded operations for assertions.
	DeadlockedIDs []string // feedback IDs that were deadlocked
	StampedNames  []string // stamp names applied
	RoutedOutputs []string // output names routed to
	Completed     bool     // whether Complete was called
}

func newSortSpy() *sortSpy {
	return &sortSpy{
		StampState:     make(map[string]bool),
		FeedbackDepths: make(map[string]int32),
	}
}

// defaultTopology returns a standard haiku-flow topology response
// for tests that mirrors the reference arrangement.
func defaultTopology() *flowv1.GetFlowTopologyResponse {
	return &flowv1.GetFlowTopologyResponse{
		Self: &flowv1.FlowNode{
			Name: "sort",
			Capabilities: []string{
				"READ:flow",
				"READ:artefact",
				"READ:feedback",
				"WRITE:feedback/deadlocked",
				"STAMP:artefact/haiku/approval",
			},
			Outputs: []*flowv1.FlowOutput{
				{Name: "quench", Target: "quench"},
				{Name: "appraise", Target: "appraise"},
				{Name: "refine", Target: "refine"},
				{Name: "arbiter", Target: "arbiter"},
			},
		},
		Nodes: map[string]*flowv1.FlowNode{
			"sort": {
				Name: "sort",
				Capabilities: []string{
					"READ:flow",
					"STAMP:artefact/haiku/approval",
				},
			},
			"quench": {
				Name:         "quench",
				Capabilities: []string{"STAMP:artefact/haiku/linter"},
			},
			"appraise": {
				Name:         "appraise",
				Capabilities: []string{"STAMP:artefact/haiku/review"},
			},
			"refine": {
				Name: "refine",
			},
			"arbiter": {
				Name: "arbiter",
			},
		},
		ExitContract: map[string]*flowv1.StampRequirements{
			"haiku": {Stamps: []string{"linter", "review", "approval"}},
		},
	}
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *sortSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *sortSpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ri := req.GetRoutingInstruction()
	if ri == nil {
		return &flowv1.SubmitResultResponse{Accepted: true}, nil
	}

	isComplete := ri.GetType() == flowv1.RoutingType_ROUTING_TYPE_COMPLETE

	if s.CompleteErr != nil && isComplete {
		return nil, s.CompleteErr
	}
	if s.RouteToOutputErr != nil && !isComplete {
		return nil, s.RouteToOutputErr
	}

	if isComplete {
		s.Completed = true
	} else {
		s.RoutedOutputs = append(s.RoutedOutputs, ri.GetTarget())
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

// ---------------------------------------------------------------------------
// Operator methods
// ---------------------------------------------------------------------------

func (s *sortSpy) GetFlowTopology(
	_ context.Context, _ *flowv1.GetFlowTopologyRequest,
) (*flowv1.GetFlowTopologyResponse, error) {
	if s.GetFlowTopologyErr != nil {
		return nil, s.GetFlowTopologyErr
	}
	if s.TopologyResponse != nil {
		return s.TopologyResponse, nil
	}
	return defaultTopology(), nil
}

// ---------------------------------------------------------------------------
// Archivist methods
// ---------------------------------------------------------------------------

func (s *sortSpy) HasStamp(
	_ context.Context, req *flowv1.HasStampRequest,
) (*flowv1.HasStampResponse, error) {
	if s.HasStampErr != nil {
		return nil, s.HasStampErr
	}
	return &flowv1.HasStampResponse{
		Exists: s.StampState[req.GetStampName()],
	}, nil
}

func (s *sortSpy) GetFeedback(
	_ context.Context, _ *flowv1.GetFeedbackRequest,
) (*flowv1.GetFeedbackResponse, error) {
	if s.GetFeedbackErr != nil {
		return nil, s.GetFeedbackErr
	}
	return &flowv1.GetFeedbackResponse{
		FeedbackItems: s.FeedbackItems,
	}, nil
}

func (s *sortSpy) GetFeedbackDepth(
	_ context.Context, req *flowv1.GetFeedbackDepthRequest,
) (*flowv1.GetFeedbackDepthResponse, error) {
	if s.GetFeedbackDepthErr != nil {
		return nil, s.GetFeedbackDepthErr
	}
	depth := s.FeedbackDepths[req.GetFeedbackId()]
	return &flowv1.GetFeedbackDepthResponse{Depth: depth}, nil
}

func (s *sortSpy) DeadlockFeedback(
	_ context.Context, req *flowv1.DeadlockFeedbackRequest,
) (*flowv1.DeadlockFeedbackResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.DeadlockFeedbackErr != nil {
		return nil, s.DeadlockFeedbackErr
	}
	s.DeadlockedIDs = append(s.DeadlockedIDs, req.GetFeedbackId())
	return &flowv1.DeadlockFeedbackResponse{
		UpdatedItem: &flowv1.FeedbackItem{
			Id:    req.GetFeedbackId(),
			State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
		},
	}, nil
}

func (s *sortSpy) StampArtefact(
	_ context.Context, req *flowv1.StampArtefactRequest,
) (*flowv1.StampArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.StampArtefactErr != nil {
		return nil, s.StampArtefactErr
	}
	s.StampedNames = append(s.StampedNames, req.GetStampName())
	return &flowv1.StampArtefactResponse{
		Stamp: &flowv1.Stamp{Name: req.GetStampName()},
	}, nil
}

// ---------------------------------------------------------------------------
// Monitor methods
// ---------------------------------------------------------------------------

func (s *sortSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}
