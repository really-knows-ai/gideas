package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/nodes/internal/nodeutil"
	flow "github.com/foundry/flow/sdk/go"
	"google.golang.org/grpc"
)

// ---------------------------------------------------------------------------
// Spy infrastructure
// ---------------------------------------------------------------------------

// linkRulingRecord captures a single LinkRuling call for test assertions.
type linkRulingRecord struct {
	FeedbackID  string
	LawID       string
	TargetState flowv1.FeedbackState
}

type stampRecord struct {
	ArtefactID string
	StampName  string
}

// humanArbiterSpy captures calls to all Foundry Flow service operations
// for test assertions on the human-arbiter handler.
type humanArbiterSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	mu sync.Mutex

	// Operation records.
	ReadArtefacts    []string                  // artefact IDs read via GetArtefact
	StampedArtefacts []stampRecord             // stamps applied
	RoutedOutputs    []string                  // output names routed to
	CompletedReasons []flowv1.CompletionReason // reasons from Complete actions
	PauseTimerCalls  int                       // PauseTimer calls
	ResumeTimerCalls int                       // ResumeTimer calls
	LinkRulingCalls  []linkRulingRecord        // LinkRuling calls

	// Configurable responses.
	ArtefactContents map[string]string               // artefact ID -> content
	FeedbackItems    []*flowv1.FeedbackItem          // returned by GetFeedback
	Topology         *flowv1.GetFlowTopologyResponse // returned by GetFlowTopology
}

// ---------------------------------------------------------------------------
// Spy factories
// ---------------------------------------------------------------------------

// newHumanArbiterSpy returns a spy with one DEADLOCKED feedback item,
// both haiku and petition artefacts, and a topology matching the
// human-arbiter FoundryNode.
func newHumanArbiterSpy() *humanArbiterSpy {
	return &humanArbiterSpy{
		ArtefactContents: map[string]string{
			"haiku":    "test-haiku-content",
			"petition": "test-petition-content",
		},
		FeedbackItems: []*flowv1.FeedbackItem{
			{
				Id:    "fb-deadlocked-1",
				State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
			},
		},
		Topology: &flowv1.GetFlowTopologyResponse{
			Self: &flowv1.FlowNode{
				Name: "human-arbiter",
				Capabilities: []string{
					"READ:flow",
					"READ:artefact/haiku",
					"READ:artefact/petition",
					"STAMP:artefact/haiku/arbitrated",
				},
				Outputs: []*flowv1.FlowOutput{
					{Name: "accept", Target: "sort"},
					{Name: "reject", Target: "sort"},
				},
			},
		},
	}
}

// newHumanArbiterSpyNoDeadlocked returns a spy with a RESOLVED feedback
// item (no DEADLOCKED items), to test graceful degradation.
func newHumanArbiterSpyNoDeadlocked() *humanArbiterSpy {
	s := newHumanArbiterSpy()
	s.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:    "fb-resolved-1",
			State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED,
		},
	}
	return s
}

// newHumanArbiterSpyMultipleDeadlocked returns a spy with two DEADLOCKED
// feedback items, to test multi-item ruling.
func newHumanArbiterSpyMultipleDeadlocked() *humanArbiterSpy {
	s := newHumanArbiterSpy()
	s.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:    "fb-deadlocked-1",
			State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
		},
		{
			Id:    "fb-deadlocked-2",
			State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
		},
	}
	return s
}

// ---------------------------------------------------------------------------
// Spy gRPC server
// ---------------------------------------------------------------------------

// newSpyGRPCServer creates a gRPC server with the humanArbiterSpy registered
// for all five Foundry Flow service interfaces.
func newSpyGRPCServer(spy *humanArbiterSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	return srv
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *humanArbiterSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *humanArbiterSpy) PauseTimer(
	_ context.Context, _ *flowv1.PauseTimerRequest,
) (*flowv1.PauseTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PauseTimerCalls++
	return &flowv1.PauseTimerResponse{Acknowledged: true}, nil
}

func (s *humanArbiterSpy) ResumeTimer(
	_ context.Context, _ *flowv1.ResumeTimerRequest,
) (*flowv1.ResumeTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ResumeTimerCalls++
	return &flowv1.ResumeTimerResponse{Acknowledged: true}, nil
}

func (s *humanArbiterSpy) SubmitResult(
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

func (s *humanArbiterSpy) GetFlowTopology(
	_ context.Context, _ *flowv1.GetFlowTopologyRequest,
) (*flowv1.GetFlowTopologyResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Topology, nil
}

// ---------------------------------------------------------------------------
// Archivist methods
// ---------------------------------------------------------------------------

func (s *humanArbiterSpy) GetArtefact(
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

func (s *humanArbiterSpy) StampArtefact(
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

func (s *humanArbiterSpy) GetFeedback(
	_ context.Context, _ *flowv1.GetFeedbackRequest,
) (*flowv1.GetFeedbackResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.FeedbackItems
	if items == nil {
		items = []*flowv1.FeedbackItem{}
	}
	return &flowv1.GetFeedbackResponse{
		FeedbackItems: items,
	}, nil
}

func (s *humanArbiterSpy) LinkRuling(
	_ context.Context, req *flowv1.LinkRulingRequest,
) (*flowv1.LinkRulingResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LinkRulingCalls = append(s.LinkRulingCalls, linkRulingRecord{
		FeedbackID:  req.GetFeedbackId(),
		LawID:       req.GetLawId(),
		TargetState: req.GetTargetState(),
	})
	return &flowv1.LinkRulingResponse{
		UpdatedItem: &flowv1.FeedbackItem{
			Id:    req.GetFeedbackId(),
			State: req.GetTargetState(),
		},
	}, nil
}

// ---------------------------------------------------------------------------
// FrictionLedger methods
// ---------------------------------------------------------------------------

func (s *humanArbiterSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newSpyClient creates a flow.Client backed by a local gRPC server with
// the humanArbiterSpy registered for all five service interfaces.
func newSpyClient(t *testing.T, spy *humanArbiterSpy) *flow.Client {
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
// both the interface and a stop function.
func newTestQueueManagerWithStop(t *testing.T) (flow.QueueManager, func() error) {
	t.Helper()
	// The storage-path and API-port knobs are configured via environment
	// variables (the SDK option constructors were deleted as test-only).
	t.Setenv("FLOW_STORAGE_PATH", ":memory:")
	t.Setenv("FLOW_HITL_PORT", "0")
	qm, err := flow.NewQueueManager()
	if err != nil {
		t.Fatalf("NewQueueManager failed: %v", err)
	}
	if err := qm.Start(context.Background()); err != nil {
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

// newWorkitemContext creates a WorkitemContext for testing.
func newWorkitemContext(workitemID string) *flowv1.WorkitemContext {
	return &flowv1.WorkitemContext{
		WorkitemId:    workitemID,
		FlowNamespace: "test-flow",
		NodeId:        "human-arbiter",
	}
}

// runHandler starts handleArbiter in a goroutine and returns an error channel.
func runHandler(
	ctx context.Context,
	client *flow.Client,
	qm flow.QueueManager,
	wctx *flowv1.WorkitemContext,
) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		wi, wiErr := client.GetWorkitem(wctx.GetWorkitemId())
		if wiErr != nil {
			errCh <- wiErr
			return
		}
		errCh <- handleArbiter(ctx, wi, qm, wctx)
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
// Test 1: Accept decision → LinkRuling WONT_FIX
// ---------------------------------------------------------------------------

func TestHumanArbiter_Accept_Decision(t *testing.T) {
	spy := newHumanArbiterSpy()
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)
	ctx := context.Background()
	wctx := newWorkitemContext("wi-arb-1")

	errCh := runHandler(ctx, client, qm, wctx)
	simulateDecision(t, ctx, qm, "wi-arb-1", choiceAccept)

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Both artefacts were read (haiku and petition).
	if len(spy.ReadArtefacts) != 2 {
		t.Fatalf("expected 2 artefact reads, got %d: %v", len(spy.ReadArtefacts), spy.ReadArtefacts)
	}
	readSet := make(map[string]bool)
	for _, a := range spy.ReadArtefacts {
		readSet[a] = true
	}
	if !readSet[artefactHaiku] {
		t.Errorf("expected haiku to be read, got %v", spy.ReadArtefacts)
	}
	if !readSet[artefactPetition] {
		t.Errorf("expected petition to be read, got %v", spy.ReadArtefacts)
	}

	// Timer paused and resumed.
	if spy.PauseTimerCalls != 1 {
		t.Errorf("expected 1 PauseTimer call, got %d", spy.PauseTimerCalls)
	}
	if spy.ResumeTimerCalls != 1 {
		t.Errorf("expected 1 ResumeTimer call, got %d", spy.ResumeTimerCalls)
	}

	// LinkRuling called once with WONT_FIX.
	if len(spy.LinkRulingCalls) != 1 {
		t.Fatalf("expected 1 LinkRuling call, got %d", len(spy.LinkRulingCalls))
	}
	lc := spy.LinkRulingCalls[0]
	if lc.FeedbackID != "fb-deadlocked-1" {
		t.Errorf("expected FeedbackID=fb-deadlocked-1, got %s", lc.FeedbackID)
	}
	if lc.LawID != sourceLawID {
		t.Errorf("expected LawID=%s, got %s", sourceLawID, lc.LawID)
	}
	if lc.TargetState != flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX {
		t.Errorf("expected TargetState=WONT_FIX, got %v", lc.TargetState)
	}

	// Stamp applied on haiku.
	if len(spy.StampedArtefacts) != 1 {
		t.Fatalf("expected 1 stamp, got %d", len(spy.StampedArtefacts))
	}
	st := spy.StampedArtefacts[0]
	if st.ArtefactID != artefactHaiku {
		t.Errorf("expected stamp on haiku, got %s", st.ArtefactID)
	}
	if st.StampName != stampArbitrated {
		t.Errorf("expected stamp name=%s, got %s", stampArbitrated, st.StampName)
	}

	// Routed to "accept".
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != choiceAccept {
		t.Errorf("expected route to %q, got %v", choiceAccept, spy.RoutedOutputs)
	}

	// No completions.
	if len(spy.CompletedReasons) != 0 {
		t.Errorf("expected no completions, got %v", spy.CompletedReasons)
	}
}

// ---------------------------------------------------------------------------
// Test 2: Reject decision → LinkRuling REJECTED
// ---------------------------------------------------------------------------

func TestHumanArbiter_Reject_Decision(t *testing.T) {
	spy := newHumanArbiterSpy()
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)
	ctx := context.Background()
	wctx := newWorkitemContext("wi-arb-2")

	errCh := runHandler(ctx, client, qm, wctx)
	simulateDecision(t, ctx, qm, "wi-arb-2", choiceReject)

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// LinkRuling called once with REJECTED.
	if len(spy.LinkRulingCalls) != 1 {
		t.Fatalf("expected 1 LinkRuling call, got %d", len(spy.LinkRulingCalls))
	}
	lc := spy.LinkRulingCalls[0]
	if lc.TargetState != flowv1.FeedbackState_FEEDBACK_STATE_REJECTED {
		t.Errorf("expected TargetState=REJECTED, got %v", lc.TargetState)
	}
	if lc.LawID != sourceLawID {
		t.Errorf("expected LawID=%s, got %s", sourceLawID, lc.LawID)
	}

	// Stamp applied.
	if len(spy.StampedArtefacts) != 1 {
		t.Fatalf("expected 1 stamp, got %d", len(spy.StampedArtefacts))
	}
	if spy.StampedArtefacts[0].StampName != stampArbitrated {
		t.Errorf("expected stamp name=%s, got %s", stampArbitrated, spy.StampedArtefacts[0].StampName)
	}

	// Routed to "reject".
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != choiceReject {
		t.Errorf("expected route to %q, got %v", choiceReject, spy.RoutedOutputs)
	}

	// No completions.
	if len(spy.CompletedReasons) != 0 {
		t.Errorf("expected no completions, got %v", spy.CompletedReasons)
	}
}

// ---------------------------------------------------------------------------
// Test 3: Cancel decision → Complete(CANCELLED)
// ---------------------------------------------------------------------------

func TestHumanArbiter_Cancel_Decision(t *testing.T) {
	spy := newHumanArbiterSpy()
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)
	ctx := context.Background()
	wctx := newWorkitemContext("wi-arb-3")

	errCh := runHandler(ctx, client, qm, wctx)
	simulateDecision(t, ctx, qm, "wi-arb-3", choiceCancel)

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// No rulings on cancel.
	if len(spy.LinkRulingCalls) != 0 {
		t.Errorf("expected no LinkRuling calls, got %d", len(spy.LinkRulingCalls))
	}

	// No stamps on cancel.
	if len(spy.StampedArtefacts) != 0 {
		t.Errorf("expected no stamps, got %v", spy.StampedArtefacts)
	}

	// No routes on cancel.
	if len(spy.RoutedOutputs) != 0 {
		t.Errorf("expected no routes, got %v", spy.RoutedOutputs)
	}

	// Completed with CANCELLED.
	if len(spy.CompletedReasons) != 1 {
		t.Fatalf("expected 1 completion, got %d", len(spy.CompletedReasons))
	}
	if spy.CompletedReasons[0] != flowv1.CompletionReason_COMPLETION_REASON_CANCELLED {
		t.Errorf("expected COMPLETION_REASON_CANCELLED, got %v", spy.CompletedReasons[0])
	}

	// Timer lifecycle: paused then resumed.
	if spy.PauseTimerCalls != 1 {
		t.Errorf("expected 1 PauseTimer, got %d", spy.PauseTimerCalls)
	}
	if spy.ResumeTimerCalls != 1 {
		t.Errorf("expected 1 ResumeTimer, got %d", spy.ResumeTimerCalls)
	}
}

// ---------------------------------------------------------------------------
// Test 4: Invalid choice → error
// ---------------------------------------------------------------------------

func TestHumanArbiter_InvalidChoice(t *testing.T) {
	spy := newHumanArbiterSpy()
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)
	ctx := context.Background()
	wctx := newWorkitemContext("wi-arb-4")

	errCh := runHandler(ctx, client, qm, wctx)
	simulateDecision(t, ctx, qm, "wi-arb-4", "bogus")

	err := <-errCh
	if err == nil {
		t.Fatal("expected error for invalid choice, got nil")
	}
	if !strings.Contains(err.Error(), "invalid choice") {
		t.Errorf("expected 'invalid choice' in error, got: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// No side effects.
	if len(spy.LinkRulingCalls) != 0 {
		t.Errorf("expected no LinkRuling calls, got %d", len(spy.LinkRulingCalls))
	}
	if len(spy.StampedArtefacts) != 0 {
		t.Errorf("expected no stamps, got %v", spy.StampedArtefacts)
	}
	if len(spy.RoutedOutputs) != 0 {
		t.Errorf("expected no routes, got %v", spy.RoutedOutputs)
	}
	if len(spy.CompletedReasons) != 0 {
		t.Errorf("expected no completions, got %v", spy.CompletedReasons)
	}

	// ResumeTimer should NOT be called on invalid choice.
	if spy.ResumeTimerCalls != 0 {
		t.Errorf("expected 0 ResumeTimer calls on invalid choice, got %d", spy.ResumeTimerCalls)
	}
}

// ---------------------------------------------------------------------------
// Test 5: No deadlocked feedback → graceful degradation
// ---------------------------------------------------------------------------

func TestHumanArbiter_NoDeadlockedFeedback(t *testing.T) {
	spy := newHumanArbiterSpyNoDeadlocked()
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)
	ctx := context.Background()
	wctx := newWorkitemContext("wi-arb-5")

	errCh := runHandler(ctx, client, qm, wctx)
	err := <-errCh // handler completes without enqueueing

	if err != nil {
		t.Fatalf("handler returned error (expected graceful degradation): %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// No LinkRuling calls (no deadlocked items).
	if len(spy.LinkRulingCalls) != 0 {
		t.Errorf("expected no LinkRuling calls, got %d", len(spy.LinkRulingCalls))
	}

	// Stamp applied on haiku (arbitrated).
	if len(spy.StampedArtefacts) != 1 {
		t.Fatalf("expected 1 stamp, got %d", len(spy.StampedArtefacts))
	}
	if spy.StampedArtefacts[0].StampName != stampArbitrated {
		t.Errorf("expected stamp name=%s, got %s", stampArbitrated, spy.StampedArtefacts[0].StampName)
	}

	// Routed to "accept" (default graceful degradation).
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != choiceAccept {
		t.Errorf("expected route to %q, got %v", choiceAccept, spy.RoutedOutputs)
	}

	// No completions.
	if len(spy.CompletedReasons) != 0 {
		t.Errorf("expected no completions, got %v", spy.CompletedReasons)
	}
}

// ---------------------------------------------------------------------------
// Test 6: Context cancelled during WaitForDecision → error
// ---------------------------------------------------------------------------

func TestHumanArbiter_ContextCancelled(t *testing.T) {
	spy := newHumanArbiterSpy()
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	wctx := newWorkitemContext("wi-arb-6")

	errCh := runHandler(ctx, client, qm, wctx)

	// Wait for the workitem to appear in the queue, then cancel.
	waitForEnqueue(t, qm, "wi-arb-6")
	cancel()

	err := <-errCh
	if err == nil {
		t.Fatal("expected error on context cancellation, got nil")
	}
	if !strings.Contains(err.Error(), "context canceled") &&
		!strings.Contains(err.Error(), "wait for decision") {
		t.Errorf("expected context/wait error, got: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// No side effects.
	if len(spy.LinkRulingCalls) != 0 {
		t.Errorf("expected no LinkRuling calls, got %d", len(spy.LinkRulingCalls))
	}
	if len(spy.StampedArtefacts) != 0 {
		t.Errorf("expected no stamps, got %v", spy.StampedArtefacts)
	}
	if len(spy.RoutedOutputs) != 0 {
		t.Errorf("expected no routes, got %v", spy.RoutedOutputs)
	}
	if len(spy.CompletedReasons) != 0 {
		t.Errorf("expected no completions, got %v", spy.CompletedReasons)
	}
}

// ---------------------------------------------------------------------------
// Test 7: GET /choices returns hardcoded JSON
// ---------------------------------------------------------------------------

func TestHumanArbiter_ChoicesEndpoint(t *testing.T) {
	req := httptest.NewRequest("GET", "/choices", nil)
	rec := httptest.NewRecorder()

	handleChoices(rec, req)

	if rec.Code != 200 {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var resp nodeutil.ChoicesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response body: %v", err)
	}

	if !resp.HasFeedback {
		t.Error("expected HasFeedback=true")
	}
	if !resp.HasCancel {
		t.Error("expected HasCancel=true")
	}

	if len(resp.Choices) != 3 {
		t.Fatalf("expected 3 choices, got %d", len(resp.Choices))
	}

	// Choice 0: accept.
	c0 := resp.Choices[0]
	if c0.Value != choiceAccept {
		t.Errorf("choice[0].Value=%q, want %q", c0.Value, choiceAccept)
	}
	if c0.Label != "Accept — Mark as WONT_FIX" {
		t.Errorf("choice[0].Label=%q, want 'Accept — Mark as WONT_FIX'", c0.Label)
	}
	if c0.Type != "route" {
		t.Errorf("choice[0].Type=%q, want 'route'", c0.Type)
	}

	// Choice 1: reject.
	c1 := resp.Choices[1]
	if c1.Value != choiceReject {
		t.Errorf("choice[1].Value=%q, want %q", c1.Value, choiceReject)
	}
	if c1.Label != "Demand Fix — Mark as REJECTED" {
		t.Errorf("choice[1].Label=%q, want 'Demand Fix — Mark as REJECTED'", c1.Label)
	}
	if c1.Type != "route" {
		t.Errorf("choice[1].Type=%q, want 'route'", c1.Type)
	}

	// Choice 2: cancel.
	c2 := resp.Choices[2]
	if c2.Value != choiceCancel {
		t.Errorf("choice[2].Value=%q, want %q", c2.Value, choiceCancel)
	}
	if c2.Label != "Reject Workitem" {
		t.Errorf("choice[2].Label=%q, want 'Reject Workitem'", c2.Label)
	}
	if c2.Type != "cancel" {
		t.Errorf("choice[2].Type=%q, want 'cancel'", c2.Type)
	}
}

// ---------------------------------------------------------------------------
// Test 8: Multiple deadlocked items → LinkRuling called for each
// ---------------------------------------------------------------------------

func TestHumanArbiter_MultipleDeadlocked(t *testing.T) {
	spy := newHumanArbiterSpyMultipleDeadlocked()
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)
	ctx := context.Background()
	wctx := newWorkitemContext("wi-arb-8")

	errCh := runHandler(ctx, client, qm, wctx)
	simulateDecision(t, ctx, qm, "wi-arb-8", choiceAccept)

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Two LinkRuling calls.
	if len(spy.LinkRulingCalls) != 2 {
		t.Fatalf("expected 2 LinkRuling calls, got %d", len(spy.LinkRulingCalls))
	}

	// Both entries have the correct lawID and targetState.
	for i, lc := range spy.LinkRulingCalls {
		if lc.LawID != sourceLawID {
			t.Errorf("call[%d] LawID=%q, want %q", i, lc.LawID, sourceLawID)
		}
		if lc.TargetState != flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX {
			t.Errorf("call[%d] TargetState=%v, want WONT_FIX", i, lc.TargetState)
		}
	}

	// First call is fb-deadlocked-1, second is fb-deadlocked-2.
	if spy.LinkRulingCalls[0].FeedbackID != "fb-deadlocked-1" {
		t.Errorf("call[0].FeedbackID=%q, want fb-deadlocked-1", spy.LinkRulingCalls[0].FeedbackID)
	}
	if spy.LinkRulingCalls[1].FeedbackID != "fb-deadlocked-2" {
		t.Errorf("call[1].FeedbackID=%q, want fb-deadlocked-2", spy.LinkRulingCalls[1].FeedbackID)
	}

	// One stamp for the haiku.
	if len(spy.StampedArtefacts) != 1 {
		t.Fatalf("expected 1 stamp, got %d", len(spy.StampedArtefacts))
	}
	if spy.StampedArtefacts[0].StampName != stampArbitrated {
		t.Errorf("expected stamp=%s, got %s", stampArbitrated, spy.StampedArtefacts[0].StampName)
	}

	// Routed to "accept".
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != choiceAccept {
		t.Errorf("expected route to %q, got %v", choiceAccept, spy.RoutedOutputs)
	}
}
