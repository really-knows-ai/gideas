package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/uuid"
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
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
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
	if !sw.pushNeeded() {
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
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	// The git-op deadline is fixed at the SPEC default (DefaultGitOperationTimeout)
	// in production (the option seam was deleted); the test shortens the deadline
	// directly to keep the hung-push assertion fast.
	sw := NewSyncWorker("https://example.com/repo.git", hung, base, RealClock{})
	sw.gitOpTimeout = 50 * time.Millisecond
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
	if !sw.pushNeeded() {
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

// TestSyncWorker_RehydrateFailureKeepsMainConsistent pins the SPEC error-table
// row "Sync re-hydration failed" atomicity from the worker's perspective: a
// post-fetch re-hydration that fails on a corrupt source (e.g. a corrupt merged
// JSON) must leave main serving its pre-fetch graph — the DETACH DELETE must
// not run before the file tree is proven loadable. Without this, the cycle
// returns with main serving a silently-wiped graph and the R8 "automatic
// recovery on next startup" escape hatch has no consistent graph to recover.
func TestSyncWorker_RehydrateFailureKeepsMainConsistent(t *testing.T) {
	ctx := context.Background()
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	// A non-empty repo with a corrupt tracked file: commit a valid entity, then
	// a corrupt JSON file under the same type directory (tracked so the
	// worker's CleanUntracked cannot remove it before re-hydration).
	now := time.Now().UTC().Round(time.Millisecond)
	if err := gs.WithGitLock(func() error {
		if err := gs.WriteEntityFiles(ctx, "Component", []gitstore.Entity{
			{ID: "11111111-1111-4111-8111-111111111111", Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx, "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx, "transaction:seed"); err != nil {
			return err
		}
		entitiesDir, _ := gs.HydrationDirs()
		compDir := filepath.Join(entitiesDir, "Component")
		if err := os.WriteFile(filepath.Join(compDir, "corrupt.json"), []byte("not json"), 0644); err != nil {
			return err
		}
		if err := gs.AddAll(ctx, "."); err != nil {
			return err
		}
		return gs.Commit(ctx, "corrupt-merge")
	}); err != nil {
		t.Fatalf("seed git tree: %v", err)
	}

	base, err := ladybug.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	if err := base.ApplySchema(ctx, &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{{
			Name:       "Component",
			Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
		}},
	}); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	// A main-only entity: present in main.lbug but absent from the git tree, so
	// a wipe-then-fail would destroy it and only the validation-first order
	// keeps it.
	mainOnlyID := uuid.NewString()
	if _, err := base.CreateEntity(ctx, "Component", mainOnlyID,
		map[string]string{"name": "main-only"}, nil, "main"); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	preHead, err := gs.BranchHEAD(ctx, "main")
	if err != nil {
		t.Fatalf("BranchHEAD: %v", err)
	}
	fetchHash := "1" + preHead[1:]
	if fetchHash == preHead {
		fetchHash = "2" + preHead[1:]
	}
	syncGit := &syncMockGitStore{GitStore: gs, fetchHash: plumbing.NewHash(fetchHash)}
	sw := NewSyncWorker("https://example.com/repo.git", syncGit, base, RealClock{})
	sw.runSyncCycle()

	sw.cycleMu.Lock()
	cycleErr := sw.cycleErr
	sw.cycleMu.Unlock()
	if cycleErr == nil {
		t.Fatal("expected the re-hydration failure to surface from the cycle")
	}
	got, err := base.GetEntity(ctx, mainOnlyID, "main")
	if err != nil {
		t.Fatalf("failed re-hydration wiped main.lbug: %v", err)
	}
	if got.Properties["name"] != "main-only" {
		t.Fatalf("pre-fetch entity mutated by failed re-hydration: %v", got.Properties)
	}
}

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
