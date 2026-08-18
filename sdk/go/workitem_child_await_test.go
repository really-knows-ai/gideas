package flow

import (
	"sync/atomic"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
)

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
