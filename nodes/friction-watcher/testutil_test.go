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

// ---------------------------------------------------------------------------
// Spy: Operator (captures CreateWorkitem calls for entry function tests)
// ---------------------------------------------------------------------------

type spyOperator struct {
	flowv1.UnimplementedOperatorServiceServer

	mu        sync.Mutex
	calls     []*flowv1.CreateWorkitemRequest
	returnID  string
	returnErr error
}

func (s *spyOperator) CreateWorkitem(
	_ context.Context, req *flowv1.CreateWorkitemRequest,
) (*flowv1.CreateWorkitemResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, req)
	if s.returnErr != nil {
		return nil, s.returnErr
	}
	return &flowv1.CreateWorkitemResponse{WorkitemId: s.returnID}, nil
}

func (s *spyOperator) getCalls() []*flowv1.CreateWorkitemRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]*flowv1.CreateWorkitemRequest, len(s.calls))
	copy(cp, s.calls)
	return cp
}

// ---------------------------------------------------------------------------
// Spy: Event Bus (sends pre-configured events on Subscribe)
// ---------------------------------------------------------------------------

type spyEventBus struct {
	flowv1.UnimplementedFlowEventBusServiceServer

	events    []*flowv1.FlowEvent
	returnErr error
}

func (s *spyEventBus) Subscribe(
	_ *flowv1.SubscribeRequest, stream grpc.ServerStreamingServer[flowv1.FlowEvent],
) error {
	if s.returnErr != nil {
		return s.returnErr
	}
	for _, evt := range s.events {
		if err := stream.Send(evt); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Spy: full Sidecar (all five services for handler tests via flow.Client)
// ---------------------------------------------------------------------------

// handlerSpy captures calls made by handleHearing through the SDK client.
type handlerSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	mu              sync.Mutex
	storedArtefacts []*flowv1.StoreArtefactRequest
	routedOutputs   []string
	heartbeatCount  int
}

func (s *handlerSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeatCount++
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *handlerSpy) StoreArtefact(
	_ context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storedArtefacts = append(s.storedArtefacts, req)
	return &flowv1.StoreArtefactResponse{
		VersionHash:  "hash-test",
		IsNewVersion: true,
	}, nil
}

func (s *handlerSpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch a := req.GetAction().(type) {
	case *flowv1.SubmitResultRequest_Route:
		if a.Route != nil {
			s.routedOutputs = append(s.routedOutputs, a.Route.GetTarget())
		}
	default:
		// Complete / Suspend / nil — nothing to record.
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

func (s *handlerSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestListener creates a TCP listener on an ephemeral localhost port.
func newTestListener(t *testing.T) net.Listener {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	return lis
}

// newHandlerTestClient creates a flow.Client backed by a local gRPC server
// with the handlerSpy providing all five service interfaces.
func newHandlerTestClient(t *testing.T, spy *handlerSpy) *flow.Client {
	t.Helper()

	lis := newTestListener(t)
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	client, err := flow.NewClient(flow.WithSidecarAddress(lis.Addr().String()))
	if err != nil {
		t.Fatalf("NewClient() failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}

// makeThresholdEvent creates a FlowEvent with the given event_id and law_id label.
func makeThresholdEvent(eventID, lawID string) *flowv1.FlowEvent {
	return &flowv1.FlowEvent{
		EventId:   eventID,
		EventType: eventType,
		Channel:   channel,
		Labels: []*flowv1.Label{
			{Key: "law_id", Value: lawID},
		},
	}
}

// makeEventNoLawID creates a FlowEvent without a law_id label.
func makeEventNoLawID(eventID string) *flowv1.FlowEvent {
	return &flowv1.FlowEvent{
		EventId:   eventID,
		EventType: eventType,
		Channel:   channel,
	}
}
