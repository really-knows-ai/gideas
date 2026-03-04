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

// newSpyGRPCServer creates a gRPC server with the ruleRouterSpy registered
// for all five Foundry Flow service interfaces.
func newSpyGRPCServer(spy *ruleRouterSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	return srv
}

// ---------------------------------------------------------------------------
// ruleRouterSpy — configurable inputs, recorded outputs
// ---------------------------------------------------------------------------

// telemetryEvent captures a single RecordTelemetry call.
type telemetryEvent struct {
	EventType string
	Payload   []byte
}

// ruleRouterSpy captures calls made by handleRuleRouter for test assertions.
// It embeds all unimplemented servers and overrides only the methods the
// rule-router handler invokes.
type ruleRouterSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	mu sync.Mutex

	// --- Configurable inputs ---

	// ArtefactRefs returned by ListArtefacts.
	ArtefactRefs []*flowv1.ArtefactRef

	// FeedbackByArtefact maps artefact ID → feedback items returned by
	// GetFeedback for that artefact. If a key is missing, an empty list
	// is returned.
	FeedbackByArtefact map[string][]*flowv1.FeedbackItem

	// ArtefactStates returned by QueryArtefactState.
	ArtefactStates []*flowv1.ArtefactState

	// ChildStatuses returned by GetChildren (raw proto to include
	// CompletionReason).
	ChildStatuses []*flowv1.ChildWorkitemStatus

	// --- Error injection ---

	ListArtefactsErr      error
	GetFeedbackErr        error
	QueryArtefactStateErr error
	GetChildrenErr        error
	RouteToOutputErr      error
	CompleteErr           error
	RecordTelemetryErr    error

	// --- Recorded outputs ---

	// RoutedOutputs records the target of each RouteToOutput call.
	RoutedOutputs []string

	// Completed is true if a CompleteAction was received.
	Completed bool

	// HeartbeatCount records the number of Heartbeat calls.
	HeartbeatCount int

	// TelemetryEvents records every RecordTelemetry call.
	TelemetryEvents []telemetryEvent

	// --- Lazy-load tracking ---

	// These booleans record whether each lazy-load RPC was actually called,
	// allowing tests to verify that unneeded data is not fetched.
	ListArtefactsCalled      bool
	GetFeedbackCalled        bool
	QueryArtefactStateCalled bool
	GetChildrenCalled        bool
}

func newRuleRouterSpy() *ruleRouterSpy {
	return &ruleRouterSpy{
		FeedbackByArtefact: make(map[string][]*flowv1.FeedbackItem),
	}
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *ruleRouterSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.HeartbeatCount++
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *ruleRouterSpy) RecordTelemetry(
	_ context.Context, req *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.RecordTelemetryErr != nil {
		return nil, s.RecordTelemetryErr
	}
	s.TelemetryEvents = append(s.TelemetryEvents, telemetryEvent{
		EventType: req.GetEventType(),
		Payload:   req.GetPayload(),
	})
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

func (s *ruleRouterSpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch a := req.GetAction().(type) {
	case *flowv1.SubmitResultRequest_Complete:
		if s.CompleteErr != nil {
			return nil, s.CompleteErr
		}
		s.Completed = true
	case *flowv1.SubmitResultRequest_Route:
		if s.RouteToOutputErr != nil {
			return nil, s.RouteToOutputErr
		}
		if a.Route != nil {
			s.RoutedOutputs = append(s.RoutedOutputs, a.Route.GetTarget())
		}
	case nil:
		// No action set — treat as no-op.
	default:
		// Suspend — no-op for rule-router spy.
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

// ---------------------------------------------------------------------------
// Operator methods
// ---------------------------------------------------------------------------

func (s *ruleRouterSpy) GetChildren(
	_ context.Context, _ *flowv1.GetChildrenRequest,
) (*flowv1.GetChildrenResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.GetChildrenCalled = true
	if s.GetChildrenErr != nil {
		return nil, s.GetChildrenErr
	}
	return &flowv1.GetChildrenResponse{
		Children: s.ChildStatuses,
	}, nil
}

// ---------------------------------------------------------------------------
// Archivist methods
// ---------------------------------------------------------------------------

func (s *ruleRouterSpy) ListArtefacts(
	_ context.Context, _ *flowv1.ListArtefactsRequest,
) (*flowv1.ListArtefactsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ListArtefactsCalled = true
	if s.ListArtefactsErr != nil {
		return nil, s.ListArtefactsErr
	}
	return &flowv1.ListArtefactsResponse{
		ArtefactRefs: s.ArtefactRefs,
	}, nil
}

func (s *ruleRouterSpy) GetFeedback(
	_ context.Context, req *flowv1.GetFeedbackRequest,
) (*flowv1.GetFeedbackResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.GetFeedbackCalled = true
	if s.GetFeedbackErr != nil {
		return nil, s.GetFeedbackErr
	}
	items := s.FeedbackByArtefact[req.GetArtefactId()]
	return &flowv1.GetFeedbackResponse{
		FeedbackItems: items,
	}, nil
}

func (s *ruleRouterSpy) QueryArtefactState(
	_ context.Context, _ *flowv1.QueryArtefactStateRequest,
) (*flowv1.QueryArtefactStateResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.QueryArtefactStateCalled = true
	if s.QueryArtefactStateErr != nil {
		return nil, s.QueryArtefactStateErr
	}
	return &flowv1.QueryArtefactStateResponse{
		ArtefactStates: s.ArtefactStates,
	}, nil
}
