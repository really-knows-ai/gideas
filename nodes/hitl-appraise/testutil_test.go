package main

import (
	"context"
	"net"
	"sync"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flow "github.com/foundry/flow/sdk/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// newSpyGRPCServer creates a gRPC server with the hitlAppraiseSpy registered
// for all five Foundry Flow service interfaces.
func newSpyGRPCServer(spy *hitlAppraiseSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	return srv
}

// hitlAppraiseSpy captures calls to service operations for test assertions.
type hitlAppraiseSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	mu sync.Mutex

	// Operation records.
	StampedArtefacts []stampRecord // stamps applied
	RoutedOutputs    []string      // output names routed to
	PauseTimerCalls  int           // PauseTimer calls
	ResumeTimerCalls int           // ResumeTimer calls
	ReadArtefacts    []string      // artefact IDs read via GetArtefact
	TopologyCalls    int           // GetFlowTopology calls

	// Configurable responses.
	ArtefactContents map[string]string               // artefact ID -> content
	Topology         *flowv1.GetFlowTopologyResponse // returned by GetFlowTopology
}

type stampRecord struct {
	ArtefactID string
	StampName  string
}

func newHITLAppraiseSpy() *hitlAppraiseSpy {
	return &hitlAppraiseSpy{
		ArtefactContents: map[string]string{
			"petition": "test-petition",
			"haiku":    "test-haiku-content",
		},
		Topology: &flowv1.GetFlowTopologyResponse{
			Self: &flowv1.FlowNode{
				Name: "hitl-appraise",
				Capabilities: []string{
					"READ:flow",
					"READ:artefact",
					"WRITE:feedback/new",
					"WRITE:feedback/resolved",
					"STAMP:artefact/haiku/review",
				},
				Outputs: []*flowv1.FlowOutput{
					{Name: "default", Target: "sort"},
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *hitlAppraiseSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *hitlAppraiseSpy) PauseTimer(
	_ context.Context, _ *flowv1.PauseTimerRequest,
) (*flowv1.PauseTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PauseTimerCalls++
	return &flowv1.PauseTimerResponse{Acknowledged: true}, nil
}

func (s *hitlAppraiseSpy) ResumeTimer(
	_ context.Context, _ *flowv1.ResumeTimerRequest,
) (*flowv1.ResumeTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ResumeTimerCalls++
	return &flowv1.ResumeTimerResponse{Acknowledged: true}, nil
}

func (s *hitlAppraiseSpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch a := req.GetAction().(type) {
	case *flowv1.SubmitResultRequest_Route:
		if a.Route != nil {
			s.RoutedOutputs = append(s.RoutedOutputs, a.Route.GetTarget())
		}
	default:
		// Complete / Suspend / nil — nothing to record.
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

// ---------------------------------------------------------------------------
// Operator methods
// ---------------------------------------------------------------------------

func (s *hitlAppraiseSpy) GetFlowTopology(
	_ context.Context, _ *flowv1.GetFlowTopologyRequest,
) (*flowv1.GetFlowTopologyResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TopologyCalls++
	return s.Topology, nil
}

// ---------------------------------------------------------------------------
// Archivist methods
// ---------------------------------------------------------------------------

func (s *hitlAppraiseSpy) GetArtefact(
	_ context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ReadArtefacts = append(s.ReadArtefacts, req.GetArtefactId())

	content := "test-content"
	if s.ArtefactContents != nil {
		if c, ok := s.ArtefactContents[req.GetArtefactId()]; ok {
			content = c
		}
	}
	return &flowv1.GetArtefactResponse{
		Content:     []byte(content),
		VersionHash: "test-hash",
	}, nil
}

func (s *hitlAppraiseSpy) StampArtefact(
	_ context.Context, req *flowv1.StampArtefactRequest,
) (*flowv1.StampArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.StampedArtefacts = append(s.StampedArtefacts, stampRecord{
		ArtefactID: req.GetArtefactId(),
		StampName:  req.GetStampName(),
	})
	return &flowv1.StampArtefactResponse{Stamp: &flowv1.Stamp{Name: req.GetStampName()}}, nil
}

// ---------------------------------------------------------------------------
// FrictionLedger methods
// ---------------------------------------------------------------------------

func (s *hitlAppraiseSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newSpyClient creates a flow.Client backed by a local gRPC server with
// the hitlAppraiseSpy registered for all five service interfaces.
func newSpyClient(t *testing.T, spy *hitlAppraiseSpy) *flow.Client {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	srv := newSpyGRPCServer(spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	client, err := flow.NewClient(flow.WithSidecarAddress(lis.Addr().String()))
	if err != nil {
		t.Fatalf("NewClient() failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}

// ---------------------------------------------------------------------------
// Fake queue-service (flowv1.QueueGatewayServiceServer)
// ---------------------------------------------------------------------------

// fakeQueueService is an in-memory, in-process implementation of the
// queue-service's SDK-facing surface (flowv1.QueueGatewayServiceServer). It is
// a true unit-test fake: no real I/O, no database, no EventBus. Items are
// stored in a mutex-guarded map keyed by workitem_id, and the RPCs return the
// PHASE_01 gRPC status codes the thin client's reverse-mapping expects
// (NotFound/AlreadyExists/FailedPrecondition). Decisions are delivered
// client-locally by the thin client's WaitForDecision, so Decide only needs to
// return success.
type fakeQueueService struct {
	flowv1.UnimplementedQueueGatewayServiceServer

	mu    sync.Mutex
	items map[string]*flowv1.QueueItem
}

const (
	queueStatusWaiting = "waiting"
	queueStatusClaimed = "claimed"
)

func newFakeQueueService() *fakeQueueService {
	return &fakeQueueService{items: map[string]*flowv1.QueueItem{}}
}

func (f *fakeQueueService) Enqueue(_ context.Context, req *flowv1.EnqueueRequest) (*flowv1.EnqueueResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[req.GetWorkitemId()] = &flowv1.QueueItem{
		WorkitemId: req.GetWorkitemId(),
		QueueName:  req.GetQueueName(),
		Status:     queueStatusWaiting,
		Choices:    append([]string(nil), req.GetChoices()...),
	}
	return &flowv1.EnqueueResponse{Acknowledged: true}, nil
}

func (f *fakeQueueService) GetGlobalQueue(
	_ context.Context, _ *flowv1.GetGlobalQueueRequest,
) (*flowv1.GetGlobalQueueResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*flowv1.QueueItem, 0, len(f.items))
	for _, it := range f.items {
		cp := proto.Clone(it).(*flowv1.QueueItem)
		out = append(out, cp)
	}
	return &flowv1.GetGlobalQueueResponse{Items: out}, nil
}

func (f *fakeQueueService) GetItem(_ context.Context, req *flowv1.GetItemRequest) (*flowv1.GetItemResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[req.GetWorkitemId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "queue item not found")
	}
	cp := proto.Clone(it).(*flowv1.QueueItem)
	return &flowv1.GetItemResponse{Item: cp}, nil
}

func (f *fakeQueueService) Claim(_ context.Context, req *flowv1.ClaimRequest) (*flowv1.ClaimResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[req.GetWorkitemId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "queue item not found")
	}
	if it.GetStatus() == queueStatusClaimed {
		return nil, status.Error(codes.AlreadyExists, "queue item already claimed")
	}
	it.Status = queueStatusClaimed
	cp := proto.Clone(it).(*flowv1.QueueItem)
	return &flowv1.ClaimResponse{Item: cp}, nil
}

func (f *fakeQueueService) Release(_ context.Context, req *flowv1.ReleaseRequest) (*flowv1.ReleaseResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[req.GetWorkitemId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "queue item not found")
	}
	if it.GetStatus() != queueStatusClaimed {
		return nil, status.Error(codes.FailedPrecondition, "queue item invalid state transition")
	}
	it.Status = queueStatusWaiting
	cp := proto.Clone(it).(*flowv1.QueueItem)
	return &flowv1.ReleaseResponse{Item: cp}, nil
}

func (f *fakeQueueService) Decide(_ context.Context, req *flowv1.DecideRequest) (*flowv1.DecideResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[req.GetWorkitemId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "queue item not found")
	}
	if it.GetStatus() != queueStatusClaimed {
		return nil, status.Error(codes.FailedPrecondition, "queue item invalid state transition")
	}
	delete(f.items, req.GetWorkitemId())
	return &flowv1.DecideResponse{Acknowledged: true}, nil
}

// newTestQueueManager starts a fake queue-service on a loopback TCP listener,
// points FLOW_QUEUE_SERVICE_ADDR at it, and returns a flow.QueueManager thin
// client that reaches it via the default dialer. The fake server is stopped via
// t.Cleanup. The thin client has no Start/Stop; a Decide on the returned
// manager delivers the choice client-locally, unblocking WaitForDecision.
func newTestQueueManager(t *testing.T) flow.QueueManager {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	srv := grpc.NewServer()
	flowv1.RegisterQueueGatewayServiceServer(srv, newFakeQueueService())
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	t.Setenv("FLOW_QUEUE_SERVICE_ADDR", lis.Addr().String())

	qm, err := flow.NewQueueManager()
	if err != nil {
		t.Fatalf("NewQueueManager failed: %v", err)
	}
	return qm
}

// newTestWorkitem creates a Workitem from a client by setting the workitem ID env var.
func newTestWorkitem(t *testing.T, client *flow.Client, workitemID string) *flow.Workitem {
	t.Helper()
	wi, err := client.GetWorkitem(workitemID)
	if err != nil {
		t.Fatalf("GetWorkitem() failed: %v", err)
	}
	return wi
}
