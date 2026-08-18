package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/go-git/go-git/v5/plumbing"
)

// TestSyncWorkerFetchAndPushCycle exercises the background sync worker's
// fetch→push cycle (the SPEC R10 commit-push surface under the sync-worker
// model): with a remote configured and the push flag set by CommitTransaction,
// one cycle performs FetchAndMerge then PushRemote, clears the flag, and emits
// no push_failed telemetry.
func TestSyncWorkerFetchAndPushCycle(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	pushGit := &pushTrackingGitStore{GitStore: gs}
	mockPub := &mockTelemetryPublisher{}
	sw := NewSyncWorker("https://example.com/repo.git", pushGit, base, RealClock{}, SyncWorkerWithAuditPublisher(mockPub))
	sw.SetPushNeeded()
	sw.runSyncCycle()
	if sw.pushNeeded() {
		t.Fatal("push flag not cleared after successful push")
	}
	pushGit.mu.Lock()
	fetchCalls, pushCalls := pushGit.fetchCalls, pushGit.pushCalls
	pushGit.mu.Unlock()
	if fetchCalls != 1 {
		t.Fatalf("expected 1 FetchAndMerge call, got %d", fetchCalls)
	}
	if pushCalls != 1 {
		t.Fatalf("expected 1 PushRemote call, got %d", pushCalls)
	}
	for _, e := range mockPub.Events() {
		if e.Event != nil && e.Event.EventType == syncFailureEventType {
			t.Fatal("push_failed telemetry emitted on successful push")
		}
	}
}

// TestSyncWorker_RehydrateOnlyWhenNewDataPulled pins the SPEC R10 re-hydration
// condition ("if new data was pulled re-hydrates main.lbug"):
// an up-to-date fetch (unchanged HEAD) must not re-hydrate, while a fetch that
// advances HEAD must.
func TestSyncWorker_RehydrateOnlyWhenNewDataPulled(t *testing.T) {
	t.Run("unchanged HEAD does not re-hydrate", func(t *testing.T) {
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		base, err := openTestStore(t)
		if err != nil {
			t.Fatalf("openTestStore: %v", err)
		}
		t.Cleanup(func() { _ = base.Close() })
		syncGit := &syncMockGitStore{GitStore: gs} // ZeroHash = remote up-to-date
		rt := &rehydrateTrackingStore{Store: base}
		sw := NewSyncWorker("https://example.com/repo.git", syncGit, rt, RealClock{})
		sw.SetPushNeeded()
		sw.runSyncCycle()
		if calls := rt.hydrateCalls(); calls != 0 {
			t.Fatalf("expected no re-hydration on an up-to-date fetch, got %d", calls)
		}
		if syncGit.fetchCalls != 1 {
			t.Fatalf("expected the fetch to run, got %d calls", syncGit.fetchCalls)
		}
	})

	t.Run("changed HEAD re-hydrates", func(t *testing.T) {
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		base, err := openTestStore(t)
		if err != nil {
			t.Fatalf("openTestStore: %v", err)
		}
		t.Cleanup(func() { _ = base.Close() })
		preHead, err := gs.BranchHEAD(context.Background(), "main")
		if err != nil {
			t.Fatalf("BranchHEAD: %v", err)
		}
		// A different valid hash: the new-data signal FetchAndMerge returns
		// when the remote advanced main.
		fetchHash := "1" + preHead[1:]
		if fetchHash == preHead {
			fetchHash = "2" + preHead[1:]
		}
		syncGit := &syncMockGitStore{GitStore: gs, fetchHash: plumbing.NewHash(fetchHash)}
		rt := &rehydrateTrackingStore{Store: base}
		sw := NewSyncWorker("https://example.com/repo.git", syncGit, rt, RealClock{})
		sw.runSyncCycle()
		if calls := rt.hydrateCalls(); calls != 1 {
			t.Fatalf("expected exactly one re-hydration after new data, got %d", calls)
		}
	})
}

// TestSyncWorker_RehydrateRetriesOnNextCycle pins the next-cycle re-hydration
// retry contract: "The next sync cycle retries the re-hydration — the git files
// are already merged, re-hydration is a read from the working tree". A re-hydration
// that fails after a successful fetch (e.g. disk full) is retried by the next
// cycle even though HEAD no longer advances.
func TestSyncWorker_RehydrateRetriesOnNextCycle(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	preHead, err := gs.BranchHEAD(context.Background(), "main")
	if err != nil {
		t.Fatalf("BranchHEAD: %v", err)
	}
	fetchHash := "1" + preHead[1:]
	if fetchHash == preHead {
		fetchHash = "2" + preHead[1:]
	}
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	syncGit := &syncMockGitStore{GitStore: gs, fetchHash: plumbing.NewHash(fetchHash)}
	flaky := &flakyRehydrateStore{Store: base, failAt: 1}
	sw := NewSyncWorker("https://example.com/repo.git", syncGit, flaky, RealClock{})

	// Cycle 1: fetch advances HEAD, re-hydration fails (disk full).
	sw.runSyncCycle()
	if calls := flaky.rehydrateCalls(); calls != 1 {
		t.Fatalf("expected 1 re-hydration attempt in the first cycle, got %d", calls)
	}
	sw.cycleMu.Lock()
	cycleErr := sw.cycleErr
	sw.cycleMu.Unlock()
	if cycleErr == nil {
		t.Fatal("expected the first cycle to surface the re-hydration failure")
	}

	// Cycle 2: the remote is now up-to-date (fetch returns the unchanged HEAD),
	// but the failed re-hydration must still be retried — and succeed once the
	// underlying cause clears.
	syncGit.fetchHash = plumbing.NewHash(preHead) // unchanged local HEAD: new-data signal absent
	sw.runSyncCycle()
	if calls := flaky.rehydrateCalls(); calls != 2 {
		t.Fatalf("expected the next cycle to retry re-hydration, got %d calls", calls)
	}
	sw.cycleMu.Lock()
	cycleErr = sw.cycleErr
	sw.cycleMu.Unlock()
	if cycleErr != nil {
		t.Fatalf("expected the retried re-hydration to succeed, got %v", cycleErr)
	}
}

// TestSyncWorker_RehydrateRestoresMainBeforeReadingTree pins the
// transaction-isolation invariant (SPEC R10 re-hydration: the working tree is
// restored to main before files are read): with
// the working tree checked out on a transaction branch carrying an uncommitted
// entity file, a new-data cycle must restore main (and clean the tree) before
// RehydrateMainFromFiles so the uncommitted file can never be published into
// main.lbug.
func TestSyncWorker_RehydrateRestoresMainBeforeReadingTree(t *testing.T) {
	ctx := context.Background()
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })

	const leakedID = "22222222-2222-4222-8222-222222222222"
	// Simulate an open transaction whose commit is in flight: the working tree
	// is checked out on the transaction branch and WriteEntityFiles has dropped
	// an (uncommitted, unstaged) file there (BeginTransaction →
	// HardResetToBranch; CommitTransaction → Checkout(tx) → WriteEntityFiles).
	if err := gs.WithGitLock(func() error {
		if err := gs.CreateBranch(ctx, testMutationEntityID); err != nil {
			return err
		}
		if err := gs.HardResetToBranch(ctx, testMutationEntityID); err != nil {
			return err
		}
		return gs.WriteEntityFiles(ctx, "Component", []gitstore.Entity{{
			ID: leakedID, Type: "Component", Properties: map[string]string{"name": "uncommitted"},
		}})
	}); err != nil {
		t.Fatalf("set up transaction-branch working tree: %v", err)
	}

	// Sanity: the uncommitted file is present in the working tree on the
	// transaction branch — the leak scenario is real.
	files, err := gs.ReadAllEntityFiles(ctx, "Component")
	if err != nil {
		t.Fatalf("ReadAllEntityFiles: %v", err)
	}
	if len(files) != 1 || files[0].ID != leakedID {
		t.Fatalf("expected the uncommitted entity file on the transaction branch, got %+v", files)
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

	if _, err := base.GetEntity(ctx, leakedID, ""); err == nil {
		t.Fatal("uncommitted transaction data leaked into main.lbug")
	}
}

// TestSyncWorkerPushFailureLeavesFlagSet exercises the sync worker's push
// failure semantics: when the fetch or the push fails, the push flag stays set
// so the next cycle (or a later commit/Sync/BeginTransaction wake) retries the
// delivery. The failure is logged; the commit itself is not rolled back.
func TestSyncWorkerPushFailureLeavesFlagSet(t *testing.T) {
	cases := []struct {
		name      string
		fetchErr  error
		pushErr   error
		wantFetch int
		wantPush  int
	}{
		{name: "fetch fails", fetchErr: errors.New("simulated fetch failure"), wantFetch: 3, wantPush: 0},
		{name: "push fails", pushErr: errors.New("simulated push rejection"), wantFetch: 1, wantPush: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, err := openTestStore(t)
			if err != nil {
				t.Fatalf("openTestStore: %v", err)
			}
			t.Cleanup(func() { _ = base.Close() })
			gs, err := gitstore.New(t.TempDir())
			if err != nil {
				t.Fatalf("gitstore.New: %v", err)
			}
			pushGit := &pushTrackingGitStore{
				GitStore: gs, fetchErr: tc.fetchErr, pushErr: tc.pushErr,
			}
			sw := NewSyncWorker("https://example.com/repo.git", pushGit, base, RealClock{})
			sw.backoffFn = func(int) time.Duration { return 0 }
			sw.SetPushNeeded()
			sw.runSyncCycle()
			if !sw.pushNeeded() {
				t.Fatal("push flag cleared despite push failure")
			}
			pushGit.mu.Lock()
			fetchCalls, pushCalls := pushGit.fetchCalls, pushGit.pushCalls
			pushGit.mu.Unlock()
			if fetchCalls != tc.wantFetch {
				t.Fatalf("expected %d FetchAndMerge calls, got %d", tc.wantFetch, fetchCalls)
			}
			if pushCalls != tc.wantPush {
				t.Fatalf("expected %d PushRemote calls, got %d", tc.wantPush, pushCalls)
			}
		})
	}
}

// TestSyncWorker_FailureEmitsTelemetry pins the SPEC R10 telemetry
// contract ("log loudly + telemetry"): every permanent sync failure — a
// non-recoverable error or retries exhausted, for fetch or push — emits
// exactly one "cartographer.push_failed" Event Bus event.
func TestSyncWorker_FailureEmitsTelemetry(t *testing.T) {
	cases := []struct {
		name      string
		fetchErr  error
		pushErr   error
		operation string
	}{
		{name: "fetch non-recoverable", fetchErr: gitstore.ErrAuthFailed, operation: "fetch"},
		{name: "fetch retries exhausted", fetchErr: gitstore.ErrRemoteUnreachable, operation: "fetch"},
		{name: "push non-recoverable", pushErr: gitstore.ErrAuthFailed, operation: "push"},
		{name: "push retries exhausted", pushErr: gitstore.ErrRemoteUnreachable, operation: "push"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gs, err := gitstore.New(t.TempDir())
			if err != nil {
				t.Fatalf("gitstore.New: %v", err)
			}
			base, err := openTestStore(t)
			if err != nil {
				t.Fatalf("openTestStore: %v", err)
			}
			t.Cleanup(func() { _ = base.Close() })
			syncGit := &syncMockGitStore{GitStore: gs, fetchErr: tc.fetchErr, pushErr: tc.pushErr}
			mockPub := &mockTelemetryPublisher{}
			sw := NewSyncWorker("https://example.com/repo.git", syncGit, base, RealClock{},
				SyncWorkerWithAuditPublisher(mockPub), SyncWorkerWithPodNamespace("test-ns"))
			sw.backoffFn = func(int) time.Duration { return 0 }
			sw.SetPushNeeded()
			sw.runSyncCycle()

			events := mockPub.Events()
			if len(events) != 1 {
				t.Fatalf("expected exactly 1 telemetry event, got %d", len(events))
			}
			if events[0].Event == nil || events[0].Event.EventType != syncFailureEventType {
				t.Fatalf("expected a %s event, got %+v", syncFailureEventType, events[0])
			}
			if events[0].Event.FlowNamespace != "test-ns" {
				t.Fatalf("expected FlowNamespace %q, got %q", "test-ns", events[0].Event.FlowNamespace)
			}
			if got := events[0].Event.Attributes["operation"]; got != tc.operation {
				t.Fatalf("expected operation %q, got %q", tc.operation, got)
			}
			if events[0].Event.Attributes["error"] == "" {
				t.Fatal("expected the failure error in the telemetry attributes")
			}
		})
	}
}

// TestSyncWorker_NoRemote_ClassifiedNonRecoverable pins the SPEC error-table
// row "Remote not configured" (FAILED_PRECONDITION) at the worker layer: when
// the gitstore has no remote — FetchAndMerge returns ErrNoRemote, the
// production misconfiguration of a non-empty REMOTE_URL whose SetRemote was
// rejected non-fatally at startup with pullOnInit=false (cmd/main.go) — a
// woken or timer cycle must not silently succeed. It returns ErrNoRemote
// classified non-recoverable and emits a telemetry event, so Sync() surfaces
// FAILED_PRECONDITION instead of reporting success without ever running a
// fetch (SPEC R10 "one full cycle" promise, SPEC:992).
func TestSyncWorker_NoRemote_ClassifiedNonRecoverable(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrNoRemote}
	mockPub := &mockTelemetryPublisher{}
	sw := NewSyncWorker("https://example.com/repo.git", syncGit, base, RealClock{},
		SyncWorkerWithAuditPublisher(mockPub), SyncWorkerWithPodNamespace("test-ns"))
	sw.backoffFn = func(int) time.Duration { return 0 }
	sw.SetPushNeeded()

	res := sw.doSyncCycle()
	if res.err == nil {
		t.Fatal("expected the no-remote cycle to fail, not silently succeed")
	}
	if !errors.Is(res.err, gitstore.ErrNoRemote) {
		t.Fatalf("expected ErrNoRemote as the cycle error, got %v", res.err)
	}
	if res.classification != syncNonRecoverable {
		t.Fatalf("expected the no-remote cycle classified non-recoverable, got %v", res.classification)
	}
	events := mockPub.Events()
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 telemetry event, got %d", len(events))
	}
	if events[0].Event == nil || events[0].Event.EventType != syncFailureEventType {
		t.Fatalf("expected a %s event, got %+v", syncFailureEventType, events[0])
	}
	if got := events[0].Event.Attributes["operation"]; got != "fetch" {
		t.Fatalf("expected operation %q, got %q", "fetch", got)
	}
}
