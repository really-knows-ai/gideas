package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	"github.com/go-git/go-git/v5/plumbing"
)

// TestSyncWorker_PushRequestLandedDuringPushIsNotLost pins the SPEC R10 WithAck
// contract against the push-flag clear race: a SetPushNeeded (CommitTransaction,
// MergeCompleted, WipeGraph) that lands while a push is in flight must not be
// lost by the clear after that push succeeds. The buggy unconditional clear
// acknowledged request 1's commit as delivered while dropping request 2, with
// no subsequent cycle delivering it. The fixed clear stores the pre-push
// generation snapshot as the delivered watermark, so request 2's generation
// stays ahead of the watermark and the derived push-needed state survives.
func TestSyncWorker_PushRequestLandedDuringPushIsNotLost(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{
		GitStore:    gs,
		pushEntered: make(chan struct{}),
		pushRelease: make(chan struct{}),
	}
	base, err := ladybug.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	sw := NewSyncWorker("https://example.com/repo.git", syncGit, base, RealClock{})
	sw.backoffFn = func(int) time.Duration { return 0 }
	t.Cleanup(syncGit.releasePush)

	// Request 1 begins a cycle whose push blocks on the gate.
	sw.SetPushNeeded()
	cycleDone := make(chan struct{})
	go func() {
		sw.runSyncCycle()
		close(cycleDone)
	}()

	// Wait for the push to enter the gate, then flag a second request while the
	// push is in flight — the window in which an unconditional clear would lose
	// it (SetPushNeeded between push success and clear is a subset of this).
	select {
	case <-syncGit.pushEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("push never entered the gate")
	}
	sw.SetPushNeeded()
	syncGit.releasePush()

	select {
	case <-cycleDone:
	case <-time.After(5 * time.Second):
		t.Fatal("sync cycle did not complete")
	}
	if !sw.pushNeeded() {
		t.Fatal("a SetPushNeeded landing during a push was lost: push flag cleared " +
			"without the second request being delivered")
	}

	// A follow-up cycle delivers the second request. Disable the gate first —
	// the mock closes pushEntered exactly once, and a second gated push would
	// double-close it.
	syncGit.mu.Lock()
	syncGit.pushEntered = nil
	syncGit.pushRelease = nil
	syncGit.mu.Unlock()
	sw.runSyncCycle()
	syncGit.mu.Lock()
	pushCalls := syncGit.pushCalls
	syncGit.mu.Unlock()
	if pushCalls != 2 {
		t.Fatalf("expected the follow-up cycle to deliver the second push request, got %d pushes", pushCalls)
	}
	if sw.pushNeeded() {
		t.Fatal("push flag not cleared after the follow-up cycle delivered the second request")
	}
}

// clearBoundaryGitStore parks the worker at the push-clear boundary: after
// PushRemote succeeds but before the cycle's clear runs, so a test can issue
// SetPushNeeded at the closest deterministically-injectable point to the
// check-then-act window the old clear exposed (between the generation re-read
// and the flag store).
type clearBoundaryGitStore struct {
	gitstore.GitStore
	mu          sync.Mutex
	pushCalls   int
	pushEntered chan struct{} // closed when a gated push begins
	pushRelease chan struct{} // closing it unblocks the gated push
	afterPush   chan struct{} // when set, the push parks here after returning, before the clear
	returned    chan struct{} // closed when the first gated push has returned (worker parked at the boundary)
}

func (s *clearBoundaryGitStore) WithGitLock(fn func() error) error { return fn() }

func (s *clearBoundaryGitStore) FetchAndMerge(ctx context.Context, remote, branch string) (plumbing.Hash, error) {
	return plumbing.ZeroHash, nil
}

func (s *clearBoundaryGitStore) PushRemote(ctx context.Context) error {
	s.mu.Lock()
	s.pushCalls++
	entered, release, returned, afterPush := s.pushEntered, s.pushRelease, s.returned, s.afterPush
	s.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if release != nil {
		<-release
	}
	s.mu.Lock()
	if returned != nil {
		close(returned)
		s.returned = nil
	}
	s.mu.Unlock()
	// Park the worker between the push and the clear. The test closes afterPush
	// after issuing SetPushNeeded at this boundary.
	if afterPush != nil {
		<-afterPush
	}
	return nil
}

// TestSyncWorker_PushRequestConcurrentWithClearIsNotLost pins the SPEC R10
// WithAck clear atomicity against the exact race the old check-then-act clear
// exposed: a SetPushNeeded (CommitTransaction, MergeCompleted, WipeGraph)
// landing between the push's success and the flag clear was wiped, so the
// follow-up cycle saw no push needed and the WithAck caller received success
// for a commit never delivered to the remote. The window between the
// generation re-read and the flag store is nanoseconds and not directly
// injectable, so the test parks the worker at the closest deterministic
// boundary — after the push returns, before the clear — and issues
// SetPushNeeded there. The fixed clear stores the pre-push generation snapshot
// as a delivered watermark (a single atomic store, no check-then-act), so the
// request is never wiped: the derived push-needed state survives, the follow-up
// cycle pushes, and a WithAck wait (WakeAndWait) resolves with success only
// after that push has been delivered.
func TestSyncWorker_PushRequestConcurrentWithClearIsNotLost(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	boundary := &clearBoundaryGitStore{
		GitStore:    gs,
		pushEntered: make(chan struct{}),
		pushRelease: make(chan struct{}),
		afterPush:   make(chan struct{}),
		returned:    make(chan struct{}),
	}
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	sw := NewSyncWorker("https://example.com/repo.git", boundary, base, RealClock{})
	sw.backoffFn = func(int) time.Duration { return 0 }

	// Request 1 begins a cycle whose push succeeds but whose clear is parked at
	// the boundary: after the push returns, before the clear runs.
	sw.SetPushNeeded()
	cycleDone := make(chan struct{})
	go func() {
		sw.runSyncCycle()
		close(cycleDone)
	}()

	select {
	case <-boundary.pushEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("push never entered the gate")
	}
	close(boundary.pushRelease)
	// Wait for the push to return: the worker is now parked before the clear.
	select {
	case <-boundary.returned:
	case <-time.After(5 * time.Second):
		t.Fatal("push never returned to the clear boundary")
	}

	// Request 2 lands at the closest deterministically-injectable point to the
	// old clear window (between the generation re-read and the flag store):
	// after the push succeeded, before the clear ran.
	sw.SetPushNeeded()
	close(boundary.afterPush)

	select {
	case <-cycleDone:
	case <-time.After(5 * time.Second):
		t.Fatal("sync cycle did not complete")
	}

	// The clear must not have lost request 2: the delivered watermark covers
	// only the generation actually pushed (request 1), so the derived
	// push-needed state still holds. A watermark store that re-read the
	// generation (or an unconditional clear) would mark request 2 delivered
	// without pushing it — the follow-up cycle would see no push needed and
	// the WithAck caller would receive success for an undelivered commit.
	if got := sw.lastPushedGeneration.Load(); got != 1 {
		t.Fatalf("delivered watermark = %d after the raced clear, want 1 (only request 1 pushed)", got)
	}
	if !sw.pushNeeded() {
		t.Fatal("a SetPushNeeded landing concurrent with the clear was lost: push-needed state " +
			"cleared without the second request being delivered")
	}

	// The follow-up cycle (the one a WithAck caller's wake triggers, or the
	// next timer tick) must still push request 2. Disable the boundary gates
	// first so the delivery push runs freely.
	boundary.mu.Lock()
	boundary.pushEntered = nil
	boundary.pushRelease = nil
	boundary.afterPush = nil
	boundary.mu.Unlock()
	go sw.Run()
	t.Cleanup(sw.Stop)

	// A WithAck wait for the pending request must not resolve with success
	// until a cycle has actually pushed it: the worker loop's startup catch-up
	// cycle is that delivery cycle.
	if err := sw.WakeAndWait(ctxWithTimeout(t)); err != nil {
		t.Fatalf("WakeAndWait for the second request: %v", err)
	}
	boundary.mu.Lock()
	pushCalls := boundary.pushCalls
	boundary.mu.Unlock()
	if pushCalls != 2 {
		t.Fatalf("expected the follow-up cycle to deliver the second push request, got %d pushes", pushCalls)
	}
	if got := sw.lastPushedGeneration.Load(); got != 2 {
		t.Fatalf("delivered watermark = %d after the follow-up push, want 2", got)
	}
	if sw.pushNeeded() {
		t.Fatal("push-needed state not cleared after the follow-up cycle delivered the second request")
	}
}
