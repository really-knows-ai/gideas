package queue

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Tests — HITL QueueManager: WaitForDecision behavior
// ---------------------------------------------------------------------------

func TestQueueManager_WaitForDecision_UnblocksOnDecide(t *testing.T) {
	qm := newTestManager(t)
	ctx := context.Background()

	if err := qm.Enqueue(ctx, "wi-wait"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	if _, err := qm.Claim(ctx, "wi-wait"); err != nil {
		t.Fatalf("Claim failed: %v", err)
	}

	// WaitForDecision in a goroutine.
	done := make(chan error, 1)
	go func() {
		_, err := qm.WaitForDecision(ctx, "wi-wait")
		done <- err
	}()

	// Give WaitForDecision time to enter the select.
	time.Sleep(50 * time.Millisecond)

	if err := qm.Decide(ctx, "wi-wait", ""); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	if err := <-done; err != nil {
		t.Fatalf("WaitForDecision returned error: %v", err)
	}
}

func TestQueueManager_DecisionSignal_NoConsumer_DoesNotHang(t *testing.T) {
	t.Setenv("FLOW_STORAGE_PATH", ":memory:")
	t.Setenv("FLOW_HITL_PORT", "0")

	qm, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	if err := qm.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { _ = qm.Stop() })

	ctx := context.Background()
	if err := qm.Enqueue(ctx, "wi-noconsumer"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	if _, err := qm.Claim(ctx, "wi-noconsumer"); err != nil {
		t.Fatalf("Claim failed: %v", err)
	}

	// Fill the decision channel's capacity-1 buffer with a first signal while
	// no WaitForDecision consumer is waiting (the slow/absent-consumer case).
	qm.peer.onDecide("wi-noconsumer", "first")

	// A second decision signal must not block even though the channel is full
	// and no consumer is receiving. Run the signal path used by the HTTP
	// /decide handler under a timeout.
	signaled := make(chan error, 1)
	go func() {
		signaled <- qm.Decide(ctx, "wi-noconsumer", "second")
	}()

	select {
	case err := <-signaled:
		if err != nil {
			t.Fatalf("Decide failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Decide hung — blocking send on full decision channel with no consumer")
	}

	// The buffered first signal must still be deliverable to a waiting caller.
	choice, err := qm.WaitForDecision(ctx, "wi-noconsumer")
	if err != nil {
		t.Fatalf("WaitForDecision failed: %v", err)
	}
	if choice != "first" {
		t.Fatalf("expected first signal to be delivered, got %q", choice)
	}
}

func TestQueueManager_WaitForDecision_ContextCancelled(t *testing.T) {
	qm := newTestManager(t)
	ctx := context.Background()

	if err := qm.Enqueue(ctx, "wi-cancel"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := qm.WaitForDecision(cancelCtx, "wi-cancel")
		done <- err
	}()

	cancel()

	err := <-done
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestQueueManager_WaitForDecision_LateDecisionReachesSubsequentWaiter(t *testing.T) {
	qm := newTestManager(t)
	ctx := context.Background()

	if err := qm.Enqueue(ctx, "wi-late"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	if _, err := qm.Claim(ctx, "wi-late"); err != nil {
		t.Fatalf("Claim failed: %v", err)
	}

	// A first waiter abandons the wait via context cancellation while the
	// item is still pending in the store.
	cancelCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := qm.WaitForDecision(cancelCtx, "wi-late")
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	// A late Decide after ctx.Done must still be captured and delivered to a
	// subsequent waiter, not dropped because the first waiter gave up.
	if err := qm.Decide(ctx, "wi-late", "late"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	choice, err := qm.WaitForDecision(ctx, "wi-late")
	if err != nil {
		t.Fatalf("subsequent WaitForDecision failed: %v", err)
	}
	if choice != "late" {
		t.Fatalf("expected late decision delivered to subsequent waiter, got %q", choice)
	}
}

func TestQueueManager_WaitForDecision_UnknownWorkitem(t *testing.T) {
	qm := newTestManager(t)
	ctx := context.Background()

	_, err := qm.WaitForDecision(ctx, "nonexistent")
	if !errors.Is(err, ErrQueueItemNotFound) {
		t.Fatalf("expected ErrQueueItemNotFound, got %v", err)
	}
}

func TestQueueManager_WaitForDecision_UnblocksOnStop(t *testing.T) {
	qm := newTestManager(t)
	ctx := context.Background()

	if err := qm.Enqueue(ctx, "wi-stop"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Start WaitForDecision in a goroutine.
	waitErr := make(chan error, 1)
	go func() {
		_, err := qm.WaitForDecision(ctx, "wi-stop")
		waitErr <- err
	}()

	// Give WaitForDecision time to enter the select.
	time.Sleep(50 * time.Millisecond)

	// Stop the manager — should close all decision channels.
	if err := qm.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	select {
	case err := <-waitErr:
		if err != nil {
			t.Fatalf("WaitForDecision returned error after Stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForDecision did not unblock within 5s after Stop")
	}
}

func TestQueueManager_WaitForDecision_ReturnsChoice(t *testing.T) {
	qm := newTestManager(t)
	ctx := context.Background()

	if err := qm.Enqueue(ctx, "wi-choice"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	if _, err := qm.Claim(ctx, "wi-choice"); err != nil {
		t.Fatalf("Claim failed: %v", err)
	}

	type result struct {
		choice string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		choice, err := qm.WaitForDecision(ctx, "wi-choice")
		done <- result{choice: choice, err: err}
	}()

	time.Sleep(50 * time.Millisecond)

	if err := qm.Decide(ctx, "wi-choice", "approve"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	r := <-done
	if r.err != nil {
		t.Fatalf("WaitForDecision returned error: %v", r.err)
	}
	if r.choice != "approve" {
		t.Fatalf("expected choice=approve, got %q", r.choice)
	}
}

func TestQueueManager_WaitForDecision_EmptyChoiceOnStop(t *testing.T) {
	qm := newTestManager(t)
	ctx := context.Background()

	if err := qm.Enqueue(ctx, "wi-stop-choice"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	type result struct {
		choice string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		choice, err := qm.WaitForDecision(ctx, "wi-stop-choice")
		done <- result{choice: choice, err: err}
	}()

	time.Sleep(50 * time.Millisecond)

	if err := qm.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("expected nil error on Stop, got: %v", r.err)
		}
		if r.choice != "" {
			t.Fatalf("expected empty choice on Stop, got %q", r.choice)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForDecision did not unblock within 5s after Stop")
	}
}
