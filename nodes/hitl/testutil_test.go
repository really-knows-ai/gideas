package main

import (
	"context"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeutil"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
)

// newSpyGRPCServer creates a gRPC server with the hitlSpy registered
// for all five Foundry Flow service interfaces.
func newSpyGRPCServer(spy *hitlSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	return srv
}

// hitlSpy captures calls to service operations for test assertions.
type hitlSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	mu sync.Mutex

	// Operation records.
	TopologyCalls    int                       // GetFlowTopology calls
	ReadArtefacts    []string                  // artefact IDs read via GetArtefact
	StampedArtefacts []stampRecord             // stamps applied
	RoutedOutputs    []string                  // output names routed to
	CompletedReasons []flowv1.CompletionReason // reasons from Complete actions
	PauseTimerCalls  int                       // PauseTimer calls
	ResumeTimerCalls int                       // ResumeTimer calls

	// Configurable responses.
	ArtefactContents map[string]string               // artefact ID -> content
	Topology         *flowv1.GetFlowTopologyResponse // returned by GetFlowTopology
}

type stampRecord struct {
	ArtefactID string
	StampName  string
}

// ---------------------------------------------------------------------------
// Factory functions — one per CRD instance pattern
// ---------------------------------------------------------------------------

// newHITLAppraiseSpy returns a spy configured like the hitl-appraise CRD
// instance: single "approved" output, WRITE:feedback, READ:artefact/petition,
// STAMP:artefact/petition/reviewed, exit-bound.
func newHITLAppraiseSpy() *hitlSpy {
	return &hitlSpy{
		ArtefactContents: map[string]string{
			"petition": "test-petition-content",
		},
		Topology: &flowv1.GetFlowTopologyResponse{
			Self: &flowv1.FlowNode{
				Name: "hitl-appraise",
				Capabilities: []string{
					"READ:flow",
					"WRITE:feedback/new",
					"READ:artefact/petition",
					"STAMP:artefact/petition/reviewed",
				},
				Outputs: []*flowv1.FlowOutput{
					{Name: "approved", Target: "clerk-done-router"},
				},
			},
			ExitContract: map[string]*flowv1.StampRequirements{
				"petition": {Stamps: []string{"reviewed"}},
			},
		},
	}
}

// newArbiterHITLResolveSpy returns a spy configured like the
// arbiter-hitl-resolve CRD instance: single "resolution" output,
// READ:artefact/evidence-bundle, no feedback, exit-bound.
func newArbiterHITLResolveSpy() *hitlSpy {
	return &hitlSpy{
		ArtefactContents: map[string]string{
			"evidence-bundle": "test-evidence-bundle",
		},
		Topology: &flowv1.GetFlowTopologyResponse{
			Self: &flowv1.FlowNode{
				Name: "arbiter-hitl-resolve",
				Capabilities: []string{
					"READ:flow",
					"READ:artefact/evidence-bundle",
				},
				Outputs: []*flowv1.FlowOutput{
					{Name: "resolution", Target: "arbiter"},
				},
			},
			ExitContract: map[string]*flowv1.StampRequirements{
				"evidence-bundle": {Stamps: []string{}},
			},
		},
	}
}

// newTribunalHITLResolveSpy returns a spy configured like the
// tribunal-hitl-resolve CRD instance: single "resolution" output,
// READ:artefact/evidence-bundle, no feedback, exit-bound.
func newTribunalHITLResolveSpy() *hitlSpy {
	return &hitlSpy{
		ArtefactContents: map[string]string{
			"evidence-bundle": "test-evidence-bundle",
		},
		Topology: &flowv1.GetFlowTopologyResponse{
			Self: &flowv1.FlowNode{
				Name: "tribunal-hitl-resolve",
				Capabilities: []string{
					"READ:flow",
					"READ:artefact/evidence-bundle",
				},
				Outputs: []*flowv1.FlowOutput{
					{Name: "resolution", Target: "tribunal"},
				},
			},
			ExitContract: map[string]*flowv1.StampRequirements{
				"evidence-bundle": {Stamps: []string{}},
			},
		},
	}
}

// newMinimalSpy returns a spy with a single output, no stamps, no feedback,
// and no exit contract. Useful for testing the simplest HITL behaviour.
func newMinimalSpy() *hitlSpy {
	return &hitlSpy{
		Topology: &flowv1.GetFlowTopologyResponse{
			Self: &flowv1.FlowNode{
				Name:         "hitl-minimal",
				Capabilities: []string{"READ:flow"},
				Outputs: []*flowv1.FlowOutput{
					{Name: "default", Target: "next-node"},
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *hitlSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *hitlSpy) PauseTimer(
	_ context.Context, _ *flowv1.PauseTimerRequest,
) (*flowv1.PauseTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PauseTimerCalls++
	return &flowv1.PauseTimerResponse{Acknowledged: true}, nil
}

func (s *hitlSpy) ResumeTimer(
	_ context.Context, _ *flowv1.ResumeTimerRequest,
) (*flowv1.ResumeTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ResumeTimerCalls++
	return &flowv1.ResumeTimerResponse{Acknowledged: true}, nil
}

func (s *hitlSpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch a := req.GetAction().(type) {
	case *flowv1.SubmitResultRequest_Route:
		if a.Route != nil {
			s.RoutedOutputs = append(s.RoutedOutputs, a.Route.GetTarget())
		}
	case *flowv1.SubmitResultRequest_Complete:
		reason := flowv1.CompletionReason_COMPLETION_REASON_UNSPECIFIED
		if a.Complete != nil {
			reason = a.Complete.GetReason()
		}
		s.CompletedReasons = append(s.CompletedReasons, reason)
	default:
		// Suspend / nil — nothing to record.
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

// ---------------------------------------------------------------------------
// Operator methods
// ---------------------------------------------------------------------------

func (s *hitlSpy) GetFlowTopology(
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

func (s *hitlSpy) GetArtefact(
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

func (s *hitlSpy) StampArtefact(
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

func (s *hitlSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newSpyClient creates a flow.Client backed by a local gRPC server with
// the hitlSpy registered for all five service interfaces.
func newSpyClient(t *testing.T, spy *hitlSpy) *flow.Client {
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
	qm, _ := newTestQueueManagerWithStop(t)
	return qm
}

// newTestQueueManagerWithStop creates an in-memory QueueManager and returns
// both the interface and a stop function. Use this when a test needs to
// explicitly stop the QueueManager (e.g., to test WaitForDecision unblocking).
func newTestQueueManagerWithStop(t *testing.T) (flow.QueueManager, func() error) {
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
	return qm, qm.Stop
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

// defaultConfig returns a hitlConfig with no labels (output names used as-is).
func defaultConfig() *hitlConfig {
	return &hitlConfig{}
}

// configWithLabels returns a hitlConfig with the given label map.
func configWithLabels(labels map[string]string) *hitlConfig {
	return &hitlConfig{ChoiceLabels: labels}
}

// newWorkitemContext creates a WorkitemContext for testing.
func newWorkitemContext(workitemID string) *flowv1.WorkitemContext {
	return &flowv1.WorkitemContext{
		WorkitemId:    workitemID,
		FlowNamespace: "test-flow",
		NodeId:        "hitl",
	}
}

// runHandler starts handleHITL in a goroutine and returns an error channel.
// The caller should simulate the human decision then read from errCh.
func runHandler(
	ctx context.Context,
	client *flow.Client,
	qm flow.QueueManager,
	_ *hitlConfig,
	wctx *flowv1.WorkitemContext,
) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- handleHITL(ctx, client, qm, wctx)
	}()
	return errCh
}

// simulateDecision waits for the workitem to appear, claims it, and decides.
func simulateDecision(t *testing.T, ctx context.Context, qm flow.QueueManager, workitemID, choice string) {
	t.Helper()
	waitForEnqueue(t, qm, workitemID)

	if _, err := qm.Claim(ctx, workitemID); err != nil {
		t.Fatalf("Claim failed: %v", err)
	}
	if err := qm.Decide(ctx, workitemID, choice); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Additional spy factories for edge case tests
// ---------------------------------------------------------------------------

// newMultiStampSpy returns a spy with two STAMP capabilities on the same
// artefact (petition/reviewed and petition/approved). Used to verify all
// stamps are applied before routing.
func newMultiStampSpy() *hitlSpy {
	return &hitlSpy{
		ArtefactContents: map[string]string{
			"petition": "test-petition-content",
		},
		Topology: &flowv1.GetFlowTopologyResponse{
			Self: &flowv1.FlowNode{
				Name: "hitl-multi-stamp",
				Capabilities: []string{
					"READ:flow",
					"READ:artefact/petition",
					"STAMP:artefact/petition/reviewed",
					"STAMP:artefact/petition/approved",
				},
				Outputs: []*flowv1.FlowOutput{
					{Name: "done", Target: "next-node"},
				},
			},
		},
	}
}
