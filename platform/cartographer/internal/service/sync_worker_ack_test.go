package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
)

// TestSyncWorker_PushSucceedsOnNextCycle exercises the timer-driven cycle: a
// push flagged after the startup cycle is delivered on the next ticker tick,
// and the flag is cleared once the push succeeds.
func TestSyncWorker_PushSucceedsOnNextCycle(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs}
	fc := newFakeClock(time.Now())
	sw := newSyncWorker(t, syncGit, fc)
	go sw.Run()
	t.Cleanup(sw.Stop)

	// Wait for the startup cycle so the ticker exists and the worker is parked
	// in select.
	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	sw.SetPushNeeded()
	if !sw.pushNeeded() {
		t.Fatal("push flag not set after SetPushNeeded")
	}

	// Fire the ticker -> the next cycle performs fetch + push and clears the
	// flag.
	fc.FireTicker()
	waitFor(t, func() bool {
		syncGit.mu.Lock()
		defer syncGit.mu.Unlock()
		return syncGit.pushCalls >= 1
	}, "push on ticker cycle")

	if sw.pushNeeded() {
		t.Fatal("push flag not cleared after successful push")
	}
	syncGit.mu.Lock()
	fetchCalls, pushCalls := syncGit.fetchCalls, syncGit.pushCalls
	syncGit.mu.Unlock()
	if fetchCalls != 2 {
		t.Fatalf("expected 2 fetch cycles (startup + ticker), got %d", fetchCalls)
	}
	if pushCalls != 1 {
		t.Fatalf("expected 1 push, got %d", pushCalls)
	}
}

// TestSyncWorker_WithAck_BlocksUntilPush exercises the WithAck contract:
// WakeAndWait blocks until the woken cycle completes (a push parked in the
// gitstore gate), returns without error once the push succeeds, and the push
// flag is cleared.
func TestSyncWorker_WithAck_BlocksUntilPush(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{
		GitStore:    gs,
		pushEntered: make(chan struct{}),
		pushRelease: make(chan struct{}),
	}
	fc := newFakeClock(time.Now())
	sw := newSyncWorker(t, syncGit, fc)
	go sw.Run()
	t.Cleanup(sw.Stop)
	// Release any pending push before Stop's final cycle can run.
	t.Cleanup(syncGit.releasePush)

	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	sw.SetPushNeeded()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wakeDone := make(chan error, 1)
	go func() { wakeDone <- sw.WakeAndWait(ctx) }()

	// The woken cycle parks in the push gate: WakeAndWait must still be
	// blocked until the push completes.
	select {
	case <-syncGit.pushEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("push never started")
	}
	select {
	case err := <-wakeDone:
		t.Fatalf("WakeAndWait returned before the push completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	syncGit.releasePush()
	if err := <-wakeDone; err != nil {
		t.Fatalf("WakeAndWait returned error: %v", err)
	}
	if sw.pushNeeded() {
		t.Fatal("push flag not cleared after successful push")
	}
}

// TestSyncWorker_WithAck_ConcurrentWaitersBothComplete pins per-waiter
// completion-channel ownership (SPEC R10 WithAck): two concurrent
// WakeAndWait callers each own their channel — the second must not overwrite
// the first. The first waiter is satisfied by the cycle that delivers the push;
// the second, registered while that cycle is in flight, is satisfied by the
// follow-up cycle the buffered wakeCh triggers.
func TestSyncWorker_WithAck_ConcurrentWaitersBothComplete(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{
		GitStore:    gs,
		pushEntered: make(chan struct{}),
		pushRelease: make(chan struct{}),
	}
	fc := newFakeClock(time.Now())
	sw := newSyncWorker(t, syncGit, fc)
	go sw.Run()
	t.Cleanup(sw.Stop)
	t.Cleanup(syncGit.releasePush)

	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	sw.SetPushNeeded()
	wakeDone := make([]chan error, 2)
	for i := range wakeDone {
		wakeDone[i] = make(chan error, 1)
	}
	// First waiter wakes the worker; the cycle parks in the push gate.
	go func() { wakeDone[0] <- sw.WakeAndWait(ctxWithTimeout(t)) }()
	select {
	case <-syncGit.pushEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("push never started")
	}
	// Second waiter registers while the first cycle is in flight.
	go func() { wakeDone[1] <- sw.WakeAndWait(ctxWithTimeout(t)) }()
	for i := range wakeDone {
		select {
		case err := <-wakeDone[i]:
			t.Fatalf("waiter %d returned before the push completed: %v", i, err)
		case <-time.After(100 * time.Millisecond):
		}
	}

	syncGit.releasePush()
	for i := range wakeDone {
		if err := <-wakeDone[i]; err != nil {
			t.Fatalf("waiter %d returned error: %v", i, err)
		}
	}
	if sw.pushNeeded() {
		t.Fatal("push flag not cleared after successful push")
	}
}

// TestSyncWorker_WithAck_TimerCycleInFlightDoesNotSatisfyFreshWaiter pins the
// WithAck/timer-race edge case: a WithAck waiter registered while a
// timer-driven cycle is in flight must not be satisfied by that cycle (whose
// waiter snapshot predates the registration). The waiter stays blocked until a
// push is actually delivered; the follow-up cycle then unblocks it.
func TestSyncWorker_WithAck_TimerCycleInFlightDoesNotSatisfyFreshWaiter(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{
		GitStore:    gs,
		pushEntered: make(chan struct{}),
		pushRelease: make(chan struct{}),
	}
	fc := newFakeClock(time.Now())
	sw := newSyncWorker(t, syncGit, fc)
	go sw.Run()
	t.Cleanup(sw.Stop)
	t.Cleanup(syncGit.releasePush)

	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	// Flag the push and fire the timer: the timer-driven cycle parks in the
	// push gate.
	sw.SetPushNeeded()
	fc.FireTicker()
	select {
	case <-syncGit.pushEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("timer cycle never reached the push")
	}

	// A WithAck waiter registered while the timer cycle is in flight.
	wakeDone := make(chan error, 1)
	go func() { wakeDone <- sw.WakeAndWait(ctxWithTimeout(t)) }()

	// The waiter must not be satisfied by the in-flight cycle: it stays
	// blocked while that cycle is still parked at the push gate.
	select {
	case err := <-wakeDone:
		t.Fatalf("WakeAndWait returned while the in-flight cycle was still parked: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	// Deliver the push: the in-flight cycle completes it, and the follow-up
	// cycle (woken by the buffered wakeCh) satisfies the waiter.
	syncGit.releasePush()
	if err := <-wakeDone; err != nil {
		t.Fatalf("WakeAndWait returned error: %v", err)
	}
	if sw.pushNeeded() {
		t.Fatal("push flag not cleared after the push was delivered")
	}
}

// TestSyncWorker_RecoverableError_RetriesAndGivesUp exercises the recoverable
// fetch path: an ErrRemoteUnreachable is retried within the cycle (3 total
// attempts with backoff), and when all attempts fail the error is recorded
// for the next WakeAndWait caller while the push flag stays set.
//
//nolint:dupl // Recoverable/non-recoverable worker-failure tests share structure; the error classes under test differ.
func TestSyncWorker_RecoverableError_RetriesAndGivesUp(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrRemoteUnreachable}
	sw := newSyncWorker(t, syncGit, RealClock{})
	sw.backoffFn = func(int) time.Duration { return 0 }
	sw.SetPushNeeded()

	sw.runSyncCycle()

	syncGit.mu.Lock()
	fetchCalls, pushCalls := syncGit.fetchCalls, syncGit.pushCalls
	syncGit.mu.Unlock()
	if fetchCalls != 3 {
		t.Fatalf("expected 3 fetch attempts (1 + 2 retries), got %d", fetchCalls)
	}
	if pushCalls != 0 {
		t.Fatalf("expected no push attempts after fetch failure, got %d", pushCalls)
	}
	if !sw.pushNeeded() {
		t.Fatal("push flag cleared despite recoverable fetch failure")
	}
	sw.cycleMu.Lock()
	cycleErr := sw.cycleErr
	sw.cycleMu.Unlock()
	if !errors.Is(cycleErr, gitstore.ErrRemoteUnreachable) {
		t.Fatalf("expected propagated ErrRemoteUnreachable, got %v", cycleErr)
	}
}

// TestSyncWorker_NonRecoverableError_LogsAndLeavesFlag exercises the
// non-recoverable path: an ErrAuthFailed fails the cycle immediately (no
// retries), the error is recorded for waiting callers, and the push flag
// stays set for the next cycle.
//
//nolint:dupl // Recoverable/non-recoverable worker-failure tests share structure; the error classes under test differ.
func TestSyncWorker_NonRecoverableError_LogsAndLeavesFlag(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrAuthFailed}
	sw := newSyncWorker(t, syncGit, RealClock{})
	sw.SetPushNeeded()

	sw.runSyncCycle()

	syncGit.mu.Lock()
	fetchCalls, pushCalls := syncGit.fetchCalls, syncGit.pushCalls
	syncGit.mu.Unlock()
	if fetchCalls != 1 {
		t.Fatalf("expected a single fetch attempt (no retries for non-recoverable), got %d", fetchCalls)
	}
	if pushCalls != 0 {
		t.Fatalf("expected no push attempts after fetch failure, got %d", pushCalls)
	}
	if !sw.pushNeeded() {
		t.Fatal("push flag cleared despite non-recoverable failure")
	}
	sw.cycleMu.Lock()
	cycleErr := sw.cycleErr
	sw.cycleMu.Unlock()
	if !errors.Is(cycleErr, gitstore.ErrAuthFailed) {
		t.Fatalf("expected propagated ErrAuthFailed, got %v", cycleErr)
	}
}

// TestSyncWorker_StartupCatchUpPush exercises the startup catch-up contract:
// a push flag left set from a prior run (committed-but-unpushed data) is
// delivered by the worker's initial cycle, without waiting for the first
// ticker tick.
func TestSyncWorker_StartupCatchUpPush(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs}
	sw := newSyncWorker(t, syncGit, newFakeClock(time.Now()))
	sw.SetPushNeeded() // simulate uncommitted push from a prior pod lifetime

	go sw.Run()
	t.Cleanup(sw.Stop)

	waitFor(t, func() bool {
		syncGit.mu.Lock()
		defer syncGit.mu.Unlock()
		return syncGit.pushCalls >= 1
	}, "startup catch-up push")

	if sw.pushNeeded() {
		t.Fatal("push flag not cleared after startup catch-up push")
	}
	syncGit.mu.Lock()
	fetchCalls := syncGit.fetchCalls
	syncGit.mu.Unlock()
	if fetchCalls != 1 {
		t.Fatalf("expected a single startup fetch cycle, got %d", fetchCalls)
	}
}
