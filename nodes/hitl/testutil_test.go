package main

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flow "github.com/foundry/flow/sdk/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
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

// configWithChoices returns a hitlConfig restricting the presented choices to
// the given output/label pairs (in order).
func configWithChoices(choices ...choiceEntry) *hitlConfig {
	return &hitlConfig{Choices: choices}
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
	cfg *hitlConfig,
	wctx *flowv1.WorkitemContext,
) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		wi, wiErr := client.GetWorkitem(wctx.GetWorkitemId())
		if wiErr != nil {
			errCh <- fmt.Errorf("runHandler: get workitem: %w", wiErr)
			return
		}
		errCh <- handleHITL(ctx, client, wi, qm, cfg, wctx)
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

// newFlowFromTopology creates a *flow.Flow from the spy's Topology response
// by creating a client that reads from the spy's GetFlowTopology implementation.
func newFlowFromTopology(t *testing.T, spy *hitlSpy) *flow.Flow {
	t.Helper()
	client := newSpyClient(t, spy)
	f, err := client.GetFlow()
	if err != nil {
		t.Fatalf("GetFlow() failed: %v", err)
	}
	return f
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

// newThreeOutputSpy returns a spy with three routing outputs and no exit
// contract. Used to test the choices restriction against a config subset.
func newThreeOutputSpy() *hitlSpy {
	return &hitlSpy{
		Topology: &flowv1.GetFlowTopologyResponse{
			Self: &flowv1.FlowNode{
				Name:         "hitl-three-output",
				Capabilities: []string{"READ:flow"},
				Outputs: []*flowv1.FlowOutput{
					{Name: "a", Target: "node-a"},
					{Name: "b", Target: "node-b"},
					{Name: "c", Target: "node-c"},
				},
			},
		},
	}
}

// newApprovalSpy returns a spy configured as a hitl:latest approval CRD
// instance: output "approve" → sort, READ:artefact/haiku + petition,
// STAMP:artefact/haiku/approval, exit-bound.
func newApprovalSpy() *hitlSpy {
	return &hitlSpy{
		ArtefactContents: map[string]string{
			"haiku":    "test-haiku-content",
			"petition": "test-petition-content",
		},
		Topology: &flowv1.GetFlowTopologyResponse{
			Self: &flowv1.FlowNode{
				Name: "hitl-approval",
				Capabilities: []string{
					"READ:flow",
					"READ:artefact/haiku",
					"READ:artefact/petition",
					"STAMP:artefact/haiku/approval",
				},
				Outputs: []*flowv1.FlowOutput{
					{Name: "approve", Target: "sort"},
				},
			},
			ExitContract: map[string]*flowv1.StampRequirements{
				"haiku": {Stamps: []string{"approval"}},
			},
		},
	}
}
