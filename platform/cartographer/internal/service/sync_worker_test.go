package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	"github.com/go-git/go-git/v5/plumbing"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// intervalRecordingClock records the duration handed to NewTicker so tests can
// assert the SyncWorker's periodic interval without waiting on real time.
type intervalRecordingClock struct {
	mu      sync.Mutex
	dur     time.Duration
	tickers int
}

func (c *intervalRecordingClock) Now() time.Time { return time.Now() }

func (c *intervalRecordingClock) NewTicker(d time.Duration) Ticker {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dur = d
	c.tickers++
	return &noopTicker{ch: make(chan time.Time, 1)}
}

type noopTicker struct{ ch chan time.Time }

func (t *noopTicker) C() <-chan time.Time { return t.ch }
func (t *noopTicker) Stop()               {}

// created reports whether the worker created its ticker (which happens right
// after the startup cycle) and, if so, the interval it used.
func (c *intervalRecordingClock) created() (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dur, c.tickers > 0
}

// TestSyncWorkerInterval pins the SPEC R10 "(configurable)" contract: the
// periodic sync interval defaults to DefaultSyncInterval (one minute) and is
// overridable via SyncWorkerWithSyncInterval.
func TestSyncWorkerInterval(t *testing.T) {
	tests := []struct {
		name string
		opts []SyncWorkerOption
		want time.Duration
	}{
		{"defaults to one minute", nil, DefaultSyncInterval},
		{"option overrides the interval",
			[]SyncWorkerOption{SyncWorkerWithSyncInterval(30 * time.Second)}, 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk := &intervalRecordingClock{}
			w := NewSyncWorker("", nil, nil, clk, tt.opts...)
			go w.Run()
			t.Cleanup(w.Stop)
			// The worker creates its ticker immediately after the startup
			// catch-up cycle; the empty remoteURL keeps that cycle a no-op.
			waitFor(t, func() bool {
				_, ok := clk.created()
				return ok
			}, "worker to create its ticker")
			got, _ := clk.created()
			if got != tt.want {
				t.Fatalf("sync interval = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSyncWorkerDefaultGitOperationTimeout pins the SPEC R10 per-operation git
// deadline default: SPEC R10 and the "Git operation deadline exceeded" error
// row define the default as five minutes ("a configurable deadline the worker
// derives per operation (default: 5 minutes)"). The override path is exercised
// by TestSyncWorkerGitOpDeadline_HungPushAbortsWithDeadlineExceeded; this test
// pins the default constant itself.
func TestSyncWorkerDefaultGitOperationTimeout(t *testing.T) {
	if DefaultGitOperationTimeout != 5*time.Minute {
		t.Fatalf("DefaultGitOperationTimeout = %v, want 5 * time.Minute (SPEC R10)", DefaultGitOperationTimeout)
	}
}

// sequencedPushGitStore drives the SyncWorker deterministically for the
// push-retry classification test: FetchAndMerge always reports up-to-date, and
// PushRemote returns a scripted error per attempt (the last entry repeats).
type sequencedPushGitStore struct {
	gitstore.GitStore
	mu        sync.Mutex
	pushErrs  []error
	pushCalls int
}

func (s *sequencedPushGitStore) WithGitLock(fn func() error) error { return fn() }

func (s *sequencedPushGitStore) FetchAndMerge(ctx context.Context, remote, branch string) (plumbing.Hash, error) {
	return plumbing.ZeroHash, nil
}

func (s *sequencedPushGitStore) PushRemote(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pushCalls++
	idx := s.pushCalls - 1
	if idx >= len(s.pushErrs) {
		idx = len(s.pushErrs) - 1
	}
	return s.pushErrs[idx]
}

// TestSyncWorkerPushRetry_NonRecoverableOnRetryStopsLoop pins SPEC R10's
// per-attempt classification ("No retry this cycle") for the push path: a
// retry attempt that surfaces a non-recoverable error (auth revoked) stops the
// retry loop immediately — 2 attempts, not 3 — and classifies the cycle
// non-recoverable so the Sync handler propagates the worker's last error
// (SPEC:628).
func TestSyncWorkerPushRetry_NonRecoverableOnRetryStopsLoop(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	seq := &sequencedPushGitStore{
		GitStore: gs,
		pushErrs: []error{gitstore.ErrRemoteUnreachable, gitstore.ErrAuthFailed},
	}
	base, err := ladybug.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	fc := newFakeClock(time.Now())
	sw := NewSyncWorker("https://example.com/repo.git", seq, base, fc)
	sw.backoffFn = func(int) time.Duration { return 0 }
	go sw.Run()
	t.Cleanup(sw.Stop)
	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	sw.SetPushNeeded()
	completed, class, cycleErr := sw.WakeAndWaitClassified(ctxWithTimeout(t))
	if !completed {
		t.Fatalf("cycle did not complete: %v", cycleErr)
	}
	if class != syncNonRecoverable {
		t.Fatalf("cycle classified %v, want syncNonRecoverable", class)
	}
	if !errors.Is(cycleErr, gitstore.ErrAuthFailed) {
		t.Fatalf("expected the non-recoverable ErrAuthFailed as the worker's last error, got %v", cycleErr)
	}
	seq.mu.Lock()
	pushCalls := seq.pushCalls
	seq.mu.Unlock()
	if pushCalls != 2 {
		t.Fatalf("expected 2 push attempts (recoverable retried once, then non-recoverable "+
			"stops the loop), got %d", pushCalls)
	}
	if !sw.pushNeeded.Load() {
		t.Fatal("push flag cleared despite non-recoverable failure")
	}
	// The Sync handler propagates a non-recoverable cycle via mapGitError
	// (SPEC:628): pin the code a Sync caller receives.
	if got := status.Code(mapGitError(cycleErr)); got != codes.Unauthenticated {
		t.Fatalf("Sync would surface %v, want Unauthenticated for ErrAuthFailed", got)
	}
}

// hungPushGitStore simulates a remote that hangs: PushRemote blocks until the
// operation's context expires (go-git's PushContext aborts with the context
// error when the gitOp deadline fires).
type hungPushGitStore struct {
	gitstore.GitStore
	mu        sync.Mutex
	pushCalls int
}

func (s *hungPushGitStore) WithGitLock(fn func() error) error { return fn() }

func (s *hungPushGitStore) FetchAndMerge(ctx context.Context, remote, branch string) (plumbing.Hash, error) {
	return plumbing.ZeroHash, nil
}

func (s *hungPushGitStore) PushRemote(ctx context.Context) error {
	s.mu.Lock()
	s.pushCalls++
	s.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

// TestSyncWorkerGitOpDeadline_HungPushAbortsWithDeadlineExceeded pins SPEC
// R10 / SPEC:978: a hung git operation aborts with DEADLINE_EXCEEDED under the
// configured per-operation deadline instead of wedging the worker. The
// deadline outcome is classified recoverable (a hung remote is a network
// timeout, SPEC:610) so the cycle retries up to 3 attempts, each under a fresh
// deadline, and the push flag stays set for the next cycle.
func TestSyncWorkerGitOpDeadline_HungPushAbortsWithDeadlineExceeded(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	hung := &hungPushGitStore{GitStore: gs}
	base, err := ladybug.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	sw := NewSyncWorker("https://example.com/repo.git", hung, base, RealClock{},
		SyncWorkerWithGitOperationTimeout(50*time.Millisecond))
	sw.backoffFn = func(int) time.Duration { return 0 }
	sw.SetPushNeeded()

	// The cycle must complete despite the hung push: without the gitOp
	// deadline the old code blocked forever in PushRemote, wedging the worker
	// (and hanging this test), so bound the run and fail loudly on a wedge.
	cycleDone := make(chan struct{})
	go func() {
		sw.runSyncCycle()
		close(cycleDone)
	}()
	select {
	case <-cycleDone:
	case <-time.After(5 * time.Second):
		t.Fatal("sync cycle wedged on a hung git operation: the operation deadline did not abort it")
	}

	sw.cycleMu.Lock()
	cycleErr := sw.cycleErr
	sw.cycleMu.Unlock()
	if cycleErr == nil {
		t.Fatal("expected the deadline-aborted push to fail the cycle")
	}
	// The caller that surfaces the worker's last error (WithAck, SPEC:617-618)
	// must see the SPEC:978 code, not INTERNAL.
	if got := status.Code(mapGitError(cycleErr)); got != codes.DeadlineExceeded {
		t.Fatalf("worker's last error maps to %v, want DeadlineExceeded (SPEC:978)", got)
	}
	hung.mu.Lock()
	pushCalls := hung.pushCalls
	hung.mu.Unlock()
	if pushCalls != 3 {
		t.Fatalf("expected 3 push attempts (deadline = recoverable timeout, retried up to 3 per SPEC:610), got %d", pushCalls)
	}
	if !sw.pushNeeded.Load() {
		t.Fatal("push flag cleared despite the deadline-aborted push")
	}
}

// TestSyncWorkerStop_ConcurrentCalls pins that Stop() is safe under concurrent
// callers: the stop signal must be closed exactly once (sync.Once), so racing
// Stop() calls cannot double-close stopCh and panic with "close of closed
// channel" (the select/default close-once idiom is a data race). All callers
// must still block until the worker loop has actually exited.
func TestSyncWorkerStop_ConcurrentCalls(t *testing.T) {
	clk := &intervalRecordingClock{}
	w := NewSyncWorker("", nil, nil, clk)
	go w.Run()
	// Cleanup is a second Stop() after the test's own — exercising the
	// already-stopped path and guarding against a leaked worker if the test
	// fails early.
	t.Cleanup(w.Stop)
	// Wait for the startup no-op cycle to finish and the loop to park in its
	// select, so the concurrent Stop() calls race against a live loop.
	waitFor(t, func() bool {
		_, ok := clk.created()
		return ok
	}, "worker to create its ticker")

	const callers = 16
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			w.Stop()
		}()
	}
	wg.Wait()

	// The stop signal is closed exactly once — a double close would have
	// panicked during the race above — and the loop exited.
	select {
	case <-w.stopCh:
	default:
		t.Fatal("stopCh not closed after Stop()")
	}
	select {
	case <-w.doneCh:
	default:
		t.Fatal("worker loop did not exit after Stop()")
	}
}
