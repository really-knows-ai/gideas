package flow

import (
	"sync/atomic"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// setupTestWorkitem creates a Workitem wired to a fanoutSpy via bufconn.
func setupTestWorkitem(t *testing.T, workitemID string, registerServices func(*grpc.Server)) *Workitem {
	t.Helper()
	client, _ := setupGRPCTestEnv(t, workitemID, registerServices)
	wi, err := client.GetWorkitem(workitemID)
	if err != nil {
		t.Fatalf("GetWorkitem() error: %v", err)
	}
	return wi
}

// ---------------------------------------------------------------------------
// Tests — CreateChild
// ---------------------------------------------------------------------------

func TestWorkitem_CreateChild_ReturnsHandle(t *testing.T) {
	spy := &fanoutSpy{}
	wi := setupTestWorkitem(t, "wi-create-child", func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

	child, err := wi.CreateChild()
	if err != nil {
		t.Fatalf("CreateChild() error: %v", err)
	}
	if child.ID() == "" {
		t.Fatal("expected non-empty child ID")
	}
	if child.session != wi.session {
		t.Fatal("child.session not wired to workitem session")
	}
}

// ---------------------------------------------------------------------------
// Tests — GetChildren
// ---------------------------------------------------------------------------

func TestWorkitem_GetChildren_ReturnsStatuses(t *testing.T) {
	spy := &fanoutSpy{}
	wi := setupTestWorkitem(t, "wi-get-children", func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

	spy.setGetChildrenResp(&flowv1.GetChildrenResponse{
		Children: []*flowv1.ChildWorkitemStatus{
			{WorkitemId: "child-001", Phase: PhaseRunning, CurrentAssignee: "node-a"},
			{WorkitemId: "child-002", Phase: PhaseCompleted},
		},
	})

	children, err := wi.GetChildren()
	if err != nil {
		t.Fatalf("GetChildren() error: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}
	if children[0].WorkitemID != "child-001" {
		t.Fatalf("expected child-001, got %q", children[0].WorkitemID)
	}
	if children[0].Phase != PhaseRunning {
		t.Fatalf("expected Running, got %q", children[0].Phase)
	}
}

// ---------------------------------------------------------------------------
// Tests — FanOut
// ---------------------------------------------------------------------------

func TestWorkitem_FanOut_CreatesAndRoutes(t *testing.T) {
	spy := &fanoutSpy{}
	wi := setupTestWorkitem(t, "wi-fanout", func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

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

	children, err := wi.FanOut(tasks)
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
		// Verify each child has a wired session.
		if ch.session != wi.session {
			t.Fatal("child session not wired")
		}
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

func TestWorkitem_FanOut_EmptyTasks(t *testing.T) {
	spy := &fanoutSpy{}
	wi := setupTestWorkitem(t, "wi-fanout-empty", func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

	children, err := wi.FanOut(nil)
	if err != nil {
		t.Fatalf("FanOut(nil) error: %v", err)
	}
	if len(children) != 0 {
		t.Fatalf("expected 0 children, got %d", len(children))
	}
}

func TestWorkitem_FanOut_FailFast_OnCreateError(t *testing.T) {
	spy := &fanoutSpy{
		createErr: status.Errorf(codes.Internal, "operator unavailable"),
	}
	wi := setupTestWorkitem(t, "wi-fanout-createerr", func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

	tasks := []FanOutTask{
		{TargetNode: "node-a", Artefacts: []ChildArtefact{
			{ID: "in", GovernedArtefact: "ga", Content: []byte("data")},
		}},
		{TargetNode: "node-b", Artefacts: []ChildArtefact{
			{ID: "in", GovernedArtefact: "ga", Content: []byte("data")},
		}},
	}

	children, err := wi.FanOut(tasks)
	if err == nil {
		t.Fatal("expected error from FanOut, got nil")
	}
	if len(children) != 0 {
		t.Fatalf("expected 0 children on first-create failure, got %d", len(children))
	}
}

func TestWorkitem_FanOut_FailFast_OnStoreError(t *testing.T) {
	spy := &fanoutSpy{
		storeErr: status.Errorf(codes.Internal, "archivist down"),
	}
	wi := setupTestWorkitem(t, "wi-fanout-storeerr", func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

	tasks := []FanOutTask{
		{TargetNode: "node-a", Artefacts: []ChildArtefact{
			{ID: "in", GovernedArtefact: "ga", Content: []byte("data")},
		}},
	}

	children, err := wi.FanOut(tasks)
	if err == nil {
		t.Fatal("expected error from FanOut on store failure, got nil")
	}
	if len(children) != 1 {
		t.Fatalf("expected 1 child (created before store failure), got %d", len(children))
	}
}

func TestWorkitem_FanOut_FailFast_OnRouteError(t *testing.T) {
	spy := &fanoutSpy{
		routeChildErr: status.Errorf(codes.FailedPrecondition, "target node not found"),
	}
	wi := setupTestWorkitem(t, "wi-fanout-routeerr", func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

	tasks := []FanOutTask{
		{TargetNode: "nonexistent", Artefacts: []ChildArtefact{
			{ID: "in", GovernedArtefact: "ga", Content: []byte("data")},
		}},
		{TargetNode: "also-missing"},
	}

	children, err := wi.FanOut(tasks)
	if err == nil {
		t.Fatal("expected error from FanOut on route failure, got nil")
	}
	// First child was created but route failed.
	if len(children) != 1 {
		t.Fatalf("expected 1 child (created before route failure), got %d", len(children))
	}
}

// ---------------------------------------------------------------------------
// Tests — AwaitAll (polling fallback — no Event Bus)
// ---------------------------------------------------------------------------

func TestWorkitem_AwaitAll_Polling_AllCompleted(t *testing.T) {
	spy := &fanoutSpy{
		getChildrenResp: &flowv1.GetChildrenResponse{
			Children: []*flowv1.ChildWorkitemStatus{
				{WorkitemId: "child-001", Phase: "Completed"},
				{WorkitemId: "child-002", Phase: "Completed"},
			},
		},
	}
	wi := setupTestWorkitem(t, "wi-await-poll-all", func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

	children, err := wi.AwaitAll()
	if err != nil {
		t.Fatalf("AwaitAll() error: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}
	for _, ch := range children {
		if ch.Phase != PhaseCompleted {
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

func TestWorkitem_AwaitAll_Polling_WaitsForTerminal(t *testing.T) {
	spy := &fanoutSpy{}
	wi := setupTestWorkitem(t, "wi-await-poll-wait", func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

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

	// The default poll interval is 5s, which is too long for tests.
	// Override by making the test work with the 5s wait... Actually,
	// we need to accept the 5s default. Let me set a short check.
	// We'll use a context with timeout on the AwaitAll call indirectly
	// by setting a very short GetChildren response cycle and relying
	// on the fact that AwaitAll doesn't check context.
	//
	// Since the default poll interval is 5s, this test would be slow.
	// Mark it as requiring patience and run with a short deadline.
	// ponytail: AwaitAll hardcodes 5s poll interval. Tests with
	// polling verification will be slow. Upgrade path: add a
	// test-only setter for the poll interval or make AwaitAll
	// accept an optional configuration.

	children, err := wi.AwaitAll()
	if err != nil {
		t.Fatalf("AwaitAll() error: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}
	if pollCount.Load() < 2 {
		t.Fatalf("expected at least 2 polls, got %d", pollCount.Load())
	}
}

func TestWorkitem_AwaitAll_Polling_MixedTerminal(t *testing.T) {
	spy := &fanoutSpy{
		getChildrenResp: &flowv1.GetChildrenResponse{
			Children: []*flowv1.ChildWorkitemStatus{
				{WorkitemId: "child-001", Phase: "Completed"},
				{WorkitemId: "child-002", Phase: "Failed"},
			},
		},
	}
	wi := setupTestWorkitem(t, "wi-await-mixed", func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

	children, err := wi.AwaitAll()
	if err != nil {
		t.Fatalf("AwaitAll() error: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}
}

func TestWorkitem_AwaitAll_Streaming(t *testing.T) {
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
					{Key: "parent_workitem_id", Value: "wi-await-stream"},
					{Key: "phase", Value: "Completed"},
					{Key: "node_id", Value: "codify-smt"},
				},
			},
			{
				WorkitemId: "child-002",
				EventType:  "workitem.phase_changed",
				Labels: []*flowv1.Label{
					{Key: "parent_workitem_id", Value: "wi-await-stream"},
					{Key: "phase", Value: "Completed"},
					{Key: "node_id", Value: "codify-rego"},
				},
			},
		},
	}

	client := setupGRPCTestEnvWithEventBus(t, "wi-await-stream",
		func(s *grpc.Server) {
			flowv1.RegisterSidecarServiceServer(s, spy)
			flowv1.RegisterOperatorServiceServer(s, spy)
			flowv1.RegisterArchivistServiceServer(s, spy)
			flowv1.RegisterLibrarianServiceServer(s, spy)
			flowv1.RegisterFrictionLedgerServiceServer(s, spy)
		},
		func(s *grpc.Server) {
			flowv1.RegisterFlowEventBusServiceServer(s, ebSpy)
		},
	)

	// After streaming sees both Completed, it does a final GetChildren poll.
	spy.setGetChildrenResp(&flowv1.GetChildrenResponse{
		Children: []*flowv1.ChildWorkitemStatus{
			{WorkitemId: "child-001", Phase: "Completed"},
			{WorkitemId: "child-002", Phase: "Completed"},
		},
	})

	wi, err := client.GetWorkitem("wi-await-stream")
	if err != nil {
		t.Fatalf("GetWorkitem() error: %v", err)
	}

	children, err := wi.AwaitAll()
	if err != nil {
		t.Fatalf("AwaitAll() streaming error: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}
	for _, ch := range children {
		if ch.Phase != PhaseCompleted {
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

func TestWorkitem_AwaitAll_Streaming_FallsBackToPolling(t *testing.T) {
	spy := &fanoutSpy{
		getChildrenResp: &flowv1.GetChildrenResponse{
			Children: []*flowv1.ChildWorkitemStatus{
				{WorkitemId: "child-001", Phase: "Completed"},
				{WorkitemId: "child-002", Phase: "Completed"},
			},
		},
	}

	ebSpy := &spyEventBusServer{
		// Subscribe immediately returns an error, so streaming path fails
		// and AwaitAll falls back to polling.
	}

	client := setupGRPCTestEnvWithEventBus(t, "wi-await-fallback",
		func(s *grpc.Server) {
			flowv1.RegisterSidecarServiceServer(s, spy)
			flowv1.RegisterOperatorServiceServer(s, spy)
			flowv1.RegisterArchivistServiceServer(s, spy)
			flowv1.RegisterLibrarianServiceServer(s, spy)
			flowv1.RegisterFrictionLedgerServiceServer(s, spy)
		},
		func(s *grpc.Server) {
			flowv1.RegisterFlowEventBusServiceServer(s, ebSpy)
		},
	)

	wi, err := client.GetWorkitem("wi-await-fallback")
	if err != nil {
		t.Fatalf("GetWorkitem() error: %v", err)
	}

	// The EventBus is wired but Subscribe receives events. With empty events,
	// Subscribe returns nil immediately (stream ends). WatchChildren succeeds,
	// but Recv returns io.EOF immediately, so awaitStreaming falls back to
	// GetChildren which returns Completed. This should work.
	children, err := wi.AwaitAll()
	if err != nil {
		t.Fatalf("AwaitAll() error: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}
}

// ---------------------------------------------------------------------------
// Tests — WatchChildren
// ---------------------------------------------------------------------------

func TestWorkitem_WatchChildren_ReturnsWatcher(t *testing.T) {
	parentID := "wi-watch-watcher"
	ebSpy := &spyEventBusServer{
		events: []*flowv1.FlowEvent{
			{
				WorkitemId: "child-001",
				EventType:  "workitem.phase_changed",
				Labels: []*flowv1.Label{
					{Key: "parent_workitem_id", Value: parentID},
					{Key: "phase", Value: "Running"},
					{Key: "node_id", Value: "codify-smt"},
				},
			},
		},
	}

	client := setupGRPCTestEnvWithEventBus(t, parentID,
		func(s *grpc.Server) {
			flowv1.RegisterSidecarServiceServer(s, &spyServer{})
			flowv1.RegisterOperatorServiceServer(s, &spyServer{})
			flowv1.RegisterArchivistServiceServer(s, &spyServer{})
			flowv1.RegisterLibrarianServiceServer(s, &spyServer{})
			flowv1.RegisterFrictionLedgerServiceServer(s, &spyServer{})
		},
		func(s *grpc.Server) {
			flowv1.RegisterFlowEventBusServiceServer(s, ebSpy)
		},
	)

	wi, err := client.GetWorkitem(parentID)
	if err != nil {
		t.Fatalf("GetWorkitem() error: %v", err)
	}

	watcher, err := wi.WatchChildren()
	if err != nil {
		t.Fatalf("WatchChildren() error: %v", err)
	}
	defer watcher.Stop()

	if watcher == nil {
		t.Fatal("expected non-nil ChildWatcher")
	}

	// Verify Recv works.
	evt, err := watcher.Recv()
	if err != nil {
		t.Fatalf("Recv() error: %v", err)
	}
	if evt.WorkitemID != "child-001" {
		t.Fatalf("expected child-001, got %q", evt.WorkitemID)
	}
	if evt.Phase != PhaseRunning {
		t.Fatalf("expected Running, got %q", evt.Phase)
	}
}

func TestWorkitem_AwaitAll_Streaming_PollsWhenNoEventsArrive(t *testing.T) {
	spy := &fanoutSpy{
		getChildrenResp: &flowv1.GetChildrenResponse{
			Children: []*flowv1.ChildWorkitemStatus{
				{WorkitemId: "child-001", Phase: "Running"},
				{WorkitemId: "child-002", Phase: "Running"},
			},
		},
	}

	// Stream stays open forever — no events, no close.  Without the fix,
	// awaitStreaming blocks on Recv() indefinitely.
	ebSpy := &controllableEventBusSpy{
		events: make(chan *flowv1.FlowEvent),
		done:   make(chan struct{}),
	}

	client := setupGRPCTestEnvWithEventBus(t, "wi-await-silent-stream",
		func(s *grpc.Server) {
			flowv1.RegisterSidecarServiceServer(s, spy)
			flowv1.RegisterOperatorServiceServer(s, spy)
			flowv1.RegisterArchivistServiceServer(s, spy)
			flowv1.RegisterLibrarianServiceServer(s, spy)
			flowv1.RegisterFrictionLedgerServiceServer(s, spy)
		},
		func(s *grpc.Server) {
			flowv1.RegisterFlowEventBusServiceServer(s, ebSpy)
		},
	)

	wi, err := client.GetWorkitem("wi-await-silent-stream")
	if err != nil {
		t.Fatalf("GetWorkitem() error: %v", err)
	}

	done := make(chan []ChildWorkitemStatus, 1)
	go func() {
		children, awErr := wi.AwaitAll()
		if awErr != nil {
			t.Errorf("AwaitAll() error: %v", awErr)
		}
		done <- children
	}()

	// Give the streaming path time to enter the Recv() loop.
	time.Sleep(1 * time.Second)

	// Now simulate the children completing.
	spy.setGetChildrenResp(&flowv1.GetChildrenResponse{
		Children: []*flowv1.ChildWorkitemStatus{
			{WorkitemId: "child-001", Phase: "Completed"},
			{WorkitemId: "child-002", Phase: "Completed"},
		},
	})

	select {
	case children := <-done:
		if len(children) != 2 {
			t.Fatalf("expected 2 children, got %d", len(children))
		}
		for _, ch := range children {
			if ch.Phase != PhaseCompleted {
				t.Fatalf("expected Completed, got %q for %s", ch.Phase, ch.WorkitemID)
			}
		}
	case <-time.After(15 * time.Second):
		t.Fatal("AwaitAll() timed out — streaming loop never polls, hangs on Recv()")
	}
}

func TestWorkitem_WatchChildren_NoEventBus_ReturnsError(t *testing.T) {
	spy := &fanoutSpy{}
	wi := setupTestWorkitem(t, "wi-watch-noeb", func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

	_, err := wi.WatchChildren()
	if err == nil {
		t.Fatal("expected error when EventBus is nil")
	}
}
