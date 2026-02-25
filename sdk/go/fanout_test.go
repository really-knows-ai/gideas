package flow

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// fanoutSpy — configurable spy for fan-out / await / collect tests
// ---------------------------------------------------------------------------

type fanoutSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer
	flowv1.UnimplementedJuryServiceServer
	flowv1.UnimplementedClerkServiceServer

	mu sync.Mutex

	// CreateChildWorkitem state.
	childCounter int
	createErr    error // if set, CreateChildWorkitem returns this error

	// RouteChild state.
	routeChildErr  error
	routedChildren []string

	// StoreArtefact state.
	storeErr        error
	storedArtefacts []storedArt

	// GetChildren state — returns getChildrenResp on each call.
	// If getChildrenFunc is set, it takes priority.
	getChildrenResp *flowv1.GetChildrenResponse
	getChildrenFunc func() (*flowv1.GetChildrenResponse, error)

	// GetArtefact state.
	artefactData map[string][]byte // key: "childID/artefactID" → content
	getArtErr    error

	// PauseTimer / ResumeTimer tracking.
	pauseCalls  atomic.Int32
	resumeCalls atomic.Int32
	pauseErr    error
	resumeErr   error
}

type storedArt struct {
	workitemID       string
	artefactID       string
	governedArtefact string
	content          []byte
}

func (s *fanoutSpy) CreateChildWorkitem(
	_ context.Context, _ *flowv1.CreateChildWorkitemRequest,
) (*flowv1.CreateChildWorkitemResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return nil, s.createErr
	}
	s.childCounter++
	return &flowv1.CreateChildWorkitemResponse{
		ChildWorkitemId: fmt.Sprintf("child-%03d", s.childCounter),
	}, nil
}

func (s *fanoutSpy) RouteChild(
	_ context.Context, req *flowv1.RouteChildRequest,
) (*flowv1.RouteChildResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.routeChildErr != nil {
		return nil, s.routeChildErr
	}
	s.routedChildren = append(s.routedChildren, req.GetChildWorkitemId())
	return &flowv1.RouteChildResponse{Accepted: true}, nil
}

func (s *fanoutSpy) StoreArtefact(
	_ context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.storeErr != nil {
		return nil, s.storeErr
	}
	s.storedArtefacts = append(s.storedArtefacts, storedArt{
		workitemID:       req.GetWorkitemId(),
		artefactID:       req.GetArtefactId(),
		governedArtefact: req.GetGovernedArtefact(),
		content:          req.GetContent(),
	})
	return &flowv1.StoreArtefactResponse{VersionHash: "hash-ok", IsNewVersion: true}, nil
}

func (s *fanoutSpy) GetChildren(
	_ context.Context, _ *flowv1.GetChildrenRequest,
) (*flowv1.GetChildrenResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getChildrenFunc != nil {
		return s.getChildrenFunc()
	}
	if s.getChildrenResp != nil {
		return s.getChildrenResp, nil
	}
	return &flowv1.GetChildrenResponse{}, nil
}

func (s *fanoutSpy) GetArtefact(
	_ context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	if s.getArtErr != nil {
		return nil, s.getArtErr
	}
	key := req.GetTargetWorkitemId() + "/" + req.GetArtefactId()
	s.mu.Lock()
	content, ok := s.artefactData[key]
	s.mu.Unlock()
	if !ok {
		return nil, status.Errorf(codes.NotFound, "artefact not found: %s", key)
	}
	return &flowv1.GetArtefactResponse{Content: content}, nil
}

func (s *fanoutSpy) PauseTimer(
	_ context.Context, _ *flowv1.PauseTimerRequest,
) (*flowv1.PauseTimerResponse, error) {
	s.pauseCalls.Add(1)
	if s.pauseErr != nil {
		return nil, s.pauseErr
	}
	return &flowv1.PauseTimerResponse{Acknowledged: true}, nil
}

func (s *fanoutSpy) ResumeTimer(
	_ context.Context, _ *flowv1.ResumeTimerRequest,
) (*flowv1.ResumeTimerResponse, error) {
	s.resumeCalls.Add(1)
	if s.resumeErr != nil {
		return nil, s.resumeErr
	}
	return &flowv1.ResumeTimerResponse{Acknowledged: true}, nil
}

func (s *fanoutSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

// setGetChildrenResp updates the response returned by GetChildren.
func (s *fanoutSpy) setGetChildrenResp(resp *flowv1.GetChildrenResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getChildrenResp = resp
}

// setupFanoutEnv creates a test Client wired to a fanoutSpy via bufconn.
func setupFanoutEnv(t *testing.T, spy *fanoutSpy) *Client {
	t.Helper()
	client, _ := setupGRPCTestEnv(t, "workitem-fanout-parent", func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
		flowv1.RegisterJuryServiceServer(s, spy)
		flowv1.RegisterClerkServiceServer(s, spy)
	})
	return client
}

// ---------------------------------------------------------------------------
// Tests — FanOut
// ---------------------------------------------------------------------------

func TestFanOut_CreatesAndRoutes(t *testing.T) {
	spy := &fanoutSpy{}
	client := setupFanoutEnv(t, spy)

	tasks := []FanOutTask{
		{
			TargetNode: "codify-smt",
			Artefacts: []ChildArtefact{
				{ID: "input", GovernedArtefact: "codification-input", Content: []byte("goal-1")},
			},
		},
		{
			TargetNode: "codify-rego",
			Artefacts: []ChildArtefact{
				{ID: "input", GovernedArtefact: "codification-input", Content: []byte("goal-2")},
			},
		},
		{
			TargetNode: "codify-prolog",
			Artefacts: []ChildArtefact{
				{ID: "input", GovernedArtefact: "codification-input", Content: []byte("goal-3")},
				{ID: "context", GovernedArtefact: "law-context", Content: []byte("ctx-3")},
			},
		},
	}

	children, err := client.FanOut(context.Background(), tasks)
	if err != nil {
		t.Fatalf("FanOut() error: %v", err)
	}
	if len(children) != 3 {
		t.Fatalf("expected 3 children, got %d", len(children))
	}

	// Verify unique IDs.
	ids := make(map[string]bool)
	for _, ch := range children {
		if ids[ch.ID()] {
			t.Fatalf("duplicate child ID: %s", ch.ID())
		}
		ids[ch.ID()] = true
	}

	// Verify artefacts were stored.
	spy.mu.Lock()
	storedCount := len(spy.storedArtefacts)
	spy.mu.Unlock()
	if storedCount != 4 { // 1 + 1 + 2
		t.Fatalf("expected 4 stored artefacts, got %d", storedCount)
	}

	// Verify all children were routed.
	spy.mu.Lock()
	routedCount := len(spy.routedChildren)
	spy.mu.Unlock()
	if routedCount != 3 {
		t.Fatalf("expected 3 routed children, got %d", routedCount)
	}
}

func TestFanOut_FailFast_OnCreateError(t *testing.T) {
	spy := &fanoutSpy{
		createErr: status.Errorf(codes.Internal, "operator unavailable"),
	}
	client := setupFanoutEnv(t, spy)

	tasks := []FanOutTask{
		{TargetNode: "node-a", Artefacts: []ChildArtefact{
			{ID: "in", GovernedArtefact: "ga", Content: []byte("data")},
		}},
		{TargetNode: "node-b", Artefacts: []ChildArtefact{
			{ID: "in", GovernedArtefact: "ga", Content: []byte("data")},
		}},
	}

	children, err := client.FanOut(context.Background(), tasks)
	if err == nil {
		t.Fatal("expected error from FanOut, got nil")
	}
	// No children should have been created since the very first create fails.
	if len(children) != 0 {
		t.Fatalf("expected 0 children on first-create failure, got %d", len(children))
	}
}

func TestFanOut_FailFast_OnRouteError(t *testing.T) {
	spy := &fanoutSpy{
		routeChildErr: status.Errorf(codes.FailedPrecondition, "target node not found"),
	}
	client := setupFanoutEnv(t, spy)

	tasks := []FanOutTask{
		{TargetNode: "nonexistent", Artefacts: []ChildArtefact{
			{ID: "in", GovernedArtefact: "ga", Content: []byte("data")},
		}},
		{TargetNode: "also-missing"},
	}

	children, err := client.FanOut(context.Background(), tasks)
	if err == nil {
		t.Fatal("expected error from FanOut on route failure, got nil")
	}
	// First child was created but route failed.
	if len(children) != 1 {
		t.Fatalf("expected 1 child (created before route failure), got %d", len(children))
	}
}

func TestFanOut_FailFast_OnStoreError(t *testing.T) {
	spy := &fanoutSpy{
		storeErr: status.Errorf(codes.Internal, "archivist down"),
	}
	client := setupFanoutEnv(t, spy)

	tasks := []FanOutTask{
		{TargetNode: "node-a", Artefacts: []ChildArtefact{
			{ID: "in", GovernedArtefact: "ga", Content: []byte("data")},
		}},
	}

	children, err := client.FanOut(context.Background(), tasks)
	if err == nil {
		t.Fatal("expected error from FanOut on store failure, got nil")
	}
	// Child was created before store failed.
	if len(children) != 1 {
		t.Fatalf("expected 1 child (created before store failure), got %d", len(children))
	}
}

func TestFanOut_EmptyTasks(t *testing.T) {
	spy := &fanoutSpy{}
	client := setupFanoutEnv(t, spy)

	children, err := client.FanOut(context.Background(), nil)
	if err != nil {
		t.Fatalf("FanOut(nil) error: %v", err)
	}
	if len(children) != 0 {
		t.Fatalf("expected 0 children for nil tasks, got %d", len(children))
	}
}

// ---------------------------------------------------------------------------
// Tests — AwaitChildren (polling fallback — no Event Bus)
// ---------------------------------------------------------------------------

func TestAwaitChildren_Polling_AllCompleted(t *testing.T) {
	spy := &fanoutSpy{
		getChildrenResp: &flowv1.GetChildrenResponse{
			Children: []*flowv1.ChildWorkitemStatus{
				{WorkitemId: "child-001", Phase: "Completed"},
				{WorkitemId: "child-002", Phase: "Completed"},
			},
		},
	}
	client := setupFanoutEnv(t, spy)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	children, err := client.AwaitChildren(ctx, WithPollingInterval(50*time.Millisecond))
	if err != nil {
		t.Fatalf("AwaitChildren() error: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}
	for _, ch := range children {
		if ch.Phase != "Completed" {
			t.Fatalf("expected Completed, got %q", ch.Phase)
		}
	}

	// Verify timer was paused and resumed.
	if spy.pauseCalls.Load() != 1 {
		t.Fatalf("expected 1 PauseTimer call, got %d", spy.pauseCalls.Load())
	}
	if spy.resumeCalls.Load() != 1 {
		t.Fatalf("expected 1 ResumeTimer call, got %d", spy.resumeCalls.Load())
	}
}

func TestAwaitChildren_Polling_WaitsForTerminal(t *testing.T) {
	spy := &fanoutSpy{}
	client := setupFanoutEnv(t, spy)

	// First poll: one Running. Second poll: all Completed.
	pollCount := atomic.Int32{}
	spy.mu.Lock()
	spy.getChildrenFunc = func() (*flowv1.GetChildrenResponse, error) {
		n := pollCount.Add(1)
		if n <= 1 {
			return &flowv1.GetChildrenResponse{
				Children: []*flowv1.ChildWorkitemStatus{
					{WorkitemId: "child-001", Phase: "Running"},
					{WorkitemId: "child-002", Phase: "Completed"},
				},
			}, nil
		}
		return &flowv1.GetChildrenResponse{
			Children: []*flowv1.ChildWorkitemStatus{
				{WorkitemId: "child-001", Phase: "Completed"},
				{WorkitemId: "child-002", Phase: "Completed"},
			},
		}, nil
	}
	spy.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	children, err := client.AwaitChildren(ctx, WithPollingInterval(50*time.Millisecond))
	if err != nil {
		t.Fatalf("AwaitChildren() error: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}
	if pollCount.Load() < 2 {
		t.Fatalf("expected at least 2 polls, got %d", pollCount.Load())
	}
}

func TestAwaitChildren_Polling_MixedTerminal(t *testing.T) {
	spy := &fanoutSpy{
		getChildrenResp: &flowv1.GetChildrenResponse{
			Children: []*flowv1.ChildWorkitemStatus{
				{WorkitemId: "child-001", Phase: "Completed"},
				{WorkitemId: "child-002", Phase: "Failed"},
			},
		},
	}
	client := setupFanoutEnv(t, spy)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	children, err := client.AwaitChildren(ctx, WithPollingInterval(50*time.Millisecond))
	if err != nil {
		t.Fatalf("AwaitChildren() error: %v", err)
	}
	// Both Completed and Failed are terminal.
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}
}

func TestAwaitChildren_ResumesTimerOnContextCancel(t *testing.T) {
	spy := &fanoutSpy{
		getChildrenResp: &flowv1.GetChildrenResponse{
			Children: []*flowv1.ChildWorkitemStatus{
				{WorkitemId: "child-001", Phase: "Running"},
			},
		},
	}
	client := setupFanoutEnv(t, spy)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := client.AwaitChildren(ctx, WithPollingInterval(50*time.Millisecond))
	if err == nil {
		t.Fatal("expected error on context cancellation")
	}

	// Timer should still be resumed even though context was cancelled.
	// Give a brief moment for the deferred resume to fire.
	time.Sleep(50 * time.Millisecond)
	if spy.resumeCalls.Load() < 1 {
		t.Fatalf("expected ResumeTimer to be called on cancellation, got %d calls", spy.resumeCalls.Load())
	}
}

func TestAwaitChildren_PauseTimerFailure(t *testing.T) {
	spy := &fanoutSpy{
		pauseErr: status.Errorf(codes.FailedPrecondition, "already paused"),
	}
	client := setupFanoutEnv(t, spy)

	_, err := client.AwaitChildren(context.Background())
	if err == nil {
		t.Fatal("expected error when PauseTimer fails")
	}
}

// ---------------------------------------------------------------------------
// Tests — AwaitChildren (streaming via Event Bus)
// ---------------------------------------------------------------------------

func TestAwaitChildren_Streaming(t *testing.T) {
	spy := &fanoutSpy{}
	// Initial snapshot: both children running.
	spy.mu.Lock()
	spy.getChildrenResp = &flowv1.GetChildrenResponse{
		Children: []*flowv1.ChildWorkitemStatus{
			{WorkitemId: "child-001", Phase: "Running"},
			{WorkitemId: "child-002", Phase: "Running"},
		},
	}
	spy.mu.Unlock()

	ebSpy := &spyEventBusServer{
		events: []*flowv1.FlowEvent{
			{
				WorkitemId: "child-001",
				EventType:  "workitem.phase_changed",
				Labels: []*flowv1.Label{
					{Key: "parent_workitem_id", Value: "workitem-fanout-parent"},
					{Key: "phase", Value: "Completed"},
					{Key: "node_id", Value: "codify-smt"},
				},
			},
			{
				WorkitemId: "child-002",
				EventType:  "workitem.phase_changed",
				Labels: []*flowv1.Label{
					{Key: "parent_workitem_id", Value: "workitem-fanout-parent"},
					{Key: "phase", Value: "Completed"},
					{Key: "node_id", Value: "codify-rego"},
				},
			},
		},
	}

	client, _, _ := setupGRPCTestEnvWithEventBus(t, "workitem-fanout-parent",
		func(s *grpc.Server) {
			flowv1.RegisterSidecarServiceServer(s, spy)
			flowv1.RegisterOperatorServiceServer(s, spy)
			flowv1.RegisterArchivistServiceServer(s, spy)
			flowv1.RegisterLibrarianServiceServer(s, spy)
			flowv1.RegisterFrictionLedgerServiceServer(s, spy)
			flowv1.RegisterJuryServiceServer(s, spy)
			flowv1.RegisterClerkServiceServer(s, spy)
		},
		func(s *grpc.Server) {
			flowv1.RegisterFlowEventBusServiceServer(s, ebSpy)
		},
	)

	// After streaming sees both Completed, it does a final GetChildren poll.
	// Update the response to match the terminal state.
	spy.setGetChildrenResp(&flowv1.GetChildrenResponse{
		Children: []*flowv1.ChildWorkitemStatus{
			{WorkitemId: "child-001", Phase: "Completed"},
			{WorkitemId: "child-002", Phase: "Completed"},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	children, err := client.AwaitChildren(ctx, WithPollingInterval(50*time.Millisecond))
	if err != nil {
		t.Fatalf("AwaitChildren() streaming error: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}
	for _, ch := range children {
		if ch.Phase != "Completed" {
			t.Fatalf("expected Completed, got %q for %s", ch.Phase, ch.WorkitemID)
		}
	}

	// Verify PauseTimer and ResumeTimer were called.
	if spy.pauseCalls.Load() != 1 {
		t.Fatalf("expected 1 PauseTimer call, got %d", spy.pauseCalls.Load())
	}
	if spy.resumeCalls.Load() != 1 {
		t.Fatalf("expected 1 ResumeTimer call, got %d", spy.resumeCalls.Load())
	}
}

// ---------------------------------------------------------------------------
// Tests — CollectArtefacts
// ---------------------------------------------------------------------------

func TestCollectArtefacts_HappyPath(t *testing.T) {
	spy := &fanoutSpy{
		artefactData: map[string][]byte{
			"child-001/output": []byte("smt-lib-code"),
			"child-002/output": []byte("rego-policy"),
		},
	}
	client := setupFanoutEnv(t, spy)

	children := []ChildWorkitemStatus{
		{WorkitemID: "child-001", Phase: "Completed"},
		{WorkitemID: "child-002", Phase: "Completed"},
	}

	results, err := client.CollectArtefacts(context.Background(), children, "output")
	if err != nil {
		t.Fatalf("CollectArtefacts() error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	if string(results[0].Artefacts["output"]) != "smt-lib-code" {
		t.Fatalf("expected smt-lib-code, got %q", results[0].Artefacts["output"])
	}
	if string(results[1].Artefacts["output"]) != "rego-policy" {
		t.Fatalf("expected rego-policy, got %q", results[1].Artefacts["output"])
	}
}

func TestCollectArtefacts_FailedChild_ReturnsError(t *testing.T) {
	spy := &fanoutSpy{}
	client := setupFanoutEnv(t, spy)

	children := []ChildWorkitemStatus{
		{WorkitemID: "child-001", Phase: "Completed"},
		{WorkitemID: "child-002", Phase: "Failed"},
	}

	_, err := client.CollectArtefacts(context.Background(), children, "output")
	if err == nil {
		t.Fatal("expected error when a child is Failed")
	}
}

func TestCollectArtefacts_MissingArtefact_NilInMap(t *testing.T) {
	spy := &fanoutSpy{
		artefactData: map[string][]byte{
			"child-001/output": []byte("smt-lib-code"),
			// child-002 has no "output" artefact.
		},
	}
	client := setupFanoutEnv(t, spy)

	children := []ChildWorkitemStatus{
		{WorkitemID: "child-001", Phase: "Completed"},
		{WorkitemID: "child-002", Phase: "Completed"},
	}

	results, err := client.CollectArtefacts(context.Background(), children, "output")
	if err != nil {
		t.Fatalf("CollectArtefacts() error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// First child has the artefact.
	if string(results[0].Artefacts["output"]) != "smt-lib-code" {
		t.Fatalf("expected smt-lib-code, got %q", results[0].Artefacts["output"])
	}

	// Second child's artefact is nil (absent, not an error).
	if results[1].Artefacts["output"] != nil {
		t.Fatalf("expected nil for missing artefact, got %v", results[1].Artefacts["output"])
	}
}

func TestCollectArtefacts_MultipleArtefactIDs(t *testing.T) {
	spy := &fanoutSpy{
		artefactData: map[string][]byte{
			"child-001/output": []byte("smt-code"),
			"child-001/report": []byte("assessment"),
			"child-002/output": []byte("rego-code"),
			// child-002 has no "report" artefact.
		},
	}
	client := setupFanoutEnv(t, spy)

	children := []ChildWorkitemStatus{
		{WorkitemID: "child-001", Phase: "Completed"},
		{WorkitemID: "child-002", Phase: "Completed"},
	}

	results, err := client.CollectArtefacts(context.Background(), children, "output", "report")
	if err != nil {
		t.Fatalf("CollectArtefacts() error: %v", err)
	}

	if string(results[0].Artefacts["output"]) != "smt-code" {
		t.Fatalf("child-001 output: expected smt-code, got %q", results[0].Artefacts["output"])
	}
	if string(results[0].Artefacts["report"]) != "assessment" {
		t.Fatalf("child-001 report: expected assessment, got %q", results[0].Artefacts["report"])
	}
	if string(results[1].Artefacts["output"]) != "rego-code" {
		t.Fatalf("child-002 output: expected rego-code, got %q", results[1].Artefacts["output"])
	}
	if results[1].Artefacts["report"] != nil {
		t.Fatalf("child-002 report: expected nil, got %v", results[1].Artefacts["report"])
	}
}

func TestCollectArtefacts_EmptyChildren(t *testing.T) {
	spy := &fanoutSpy{}
	client := setupFanoutEnv(t, spy)

	results, err := client.CollectArtefacts(context.Background(), nil, "output")
	if err != nil {
		t.Fatalf("CollectArtefacts(nil) error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results for nil children, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// Tests — Helper functions
// ---------------------------------------------------------------------------

func TestIsTerminalPhase(t *testing.T) {
	cases := []struct {
		phase    string
		terminal bool
	}{
		{"Completed", true},
		{"Failed", true},
		{"Running", false},
		{"Pending", false},
		{"Routing", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isTerminalPhase(tc.phase); got != tc.terminal {
			t.Errorf("isTerminalPhase(%q) = %v, want %v", tc.phase, got, tc.terminal)
		}
	}
}

func TestAllTerminal(t *testing.T) {
	cases := []struct {
		name     string
		children []ChildWorkitemStatus
		want     bool
	}{
		{"empty", nil, false},
		{"all completed", []ChildWorkitemStatus{
			{Phase: "Completed"}, {Phase: "Completed"},
		}, true},
		{"mixed terminal", []ChildWorkitemStatus{
			{Phase: "Completed"}, {Phase: "Failed"},
		}, true},
		{"one running", []ChildWorkitemStatus{
			{Phase: "Completed"}, {Phase: "Running"},
		}, false},
		{"all running", []ChildWorkitemStatus{
			{Phase: "Running"}, {Phase: "Running"},
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := allTerminal(tc.children); got != tc.want {
				t.Errorf("allTerminal() = %v, want %v", got, tc.want)
			}
		})
	}
}
