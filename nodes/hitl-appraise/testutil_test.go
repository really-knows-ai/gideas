package main

import (
	"context"
	"sync"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeutil"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
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

	lis, err := nodeutil.NewLocalListener()
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
