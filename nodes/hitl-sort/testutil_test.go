package main

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
)

// newLocalListener creates a TCP listener on an ephemeral localhost port.
func newLocalListener() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

// newSpyGRPCServer creates a gRPC server with the hitlSortSpy registered
// for all five Foundry Flow service interfaces.
func newSpyGRPCServer(spy *hitlSortSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	return srv
}

// hitlSortSpy captures calls to service operations for test assertions.
type hitlSortSpy struct {
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
	TopologyCalls    int           // GetFlowTopology calls

	// Configurable responses.
	Topology *flowv1.GetFlowTopologyResponse // returned by GetFlowTopology
}

type stampRecord struct {
	ArtefactID string
	StampName  string
}

// newHITLSortSpy returns a spy with a default multi-output topology that
// includes a STAMP capability. Suitable for most tests.
func newHITLSortSpy() *hitlSortSpy {
	return &hitlSortSpy{
		Topology: &flowv1.GetFlowTopologyResponse{
			Self: &flowv1.FlowNode{
				Name: "hitl-sort",
				Capabilities: []string{
					"READ:flow",
					"STAMP:artefact/haiku/review",
				},
				Outputs: []*flowv1.FlowOutput{
					{Name: "approve", Target: "publish"},
					{Name: "reject", Target: "refine"},
					{Name: "escalate", Target: "arbiter"},
				},
			},
		},
	}
}

// newSpyNoStamp returns a spy with topology that has no STAMP capability.
func newSpyNoStamp() *hitlSortSpy {
	return &hitlSortSpy{
		Topology: &flowv1.GetFlowTopologyResponse{
			Self: &flowv1.FlowNode{
				Name:         "hitl-sort",
				Capabilities: []string{"READ:flow"},
				Outputs: []*flowv1.FlowOutput{
					{Name: "approve", Target: "publish"},
					{Name: "reject", Target: "refine"},
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *hitlSortSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *hitlSortSpy) PauseTimer(
	_ context.Context, _ *flowv1.PauseTimerRequest,
) (*flowv1.PauseTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PauseTimerCalls++
	return &flowv1.PauseTimerResponse{Acknowledged: true}, nil
}

func (s *hitlSortSpy) ResumeTimer(
	_ context.Context, _ *flowv1.ResumeTimerRequest,
) (*flowv1.ResumeTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ResumeTimerCalls++
	return &flowv1.ResumeTimerResponse{Acknowledged: true}, nil
}

func (s *hitlSortSpy) SubmitResult(
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

func (s *hitlSortSpy) GetFlowTopology(
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

func (s *hitlSortSpy) StampArtefact(
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

func (s *hitlSortSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newSpyClient creates a flow.Client backed by a local gRPC server with
// the hitlSortSpy registered for all five service interfaces.
func newSpyClient(t *testing.T, spy *hitlSortSpy) *flow.Client {
	t.Helper()

	lis, err := newLocalListener()
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

// newTestQueueManager creates an in-memory QueueManager for tests.
func newTestQueueManager(t *testing.T) flow.QueueManager {
	t.Helper()
	qm, err := flow.NewQueueManager(
		flow.WithShardID("test-shard"),
		flow.WithPeerResolver(&staticResolver{}),
	)
	if err != nil {
		t.Fatalf("NewQueueManager failed: %v", err)
	}
	if err := qm.Start(context.Background(), flow.WithStoragePath(":memory:"), flow.WithAPIPort("0")); err != nil {
		t.Fatalf("QueueManager.Start failed: %v", err)
	}
	t.Cleanup(func() { _ = qm.Stop() })
	return qm
}

// waitForEnqueue polls until the given workitem appears in the queue.
func waitForEnqueue(t *testing.T, qm flow.QueueManager, workitemID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := qm.GetItem(context.Background(), workitemID); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s to appear in queue", workitemID)
}

// staticResolver returns no peers (standalone mode for testing).
type staticResolver struct{}

func (r *staticResolver) Resolve(_ context.Context) ([]string, error) {
	return nil, nil
}
