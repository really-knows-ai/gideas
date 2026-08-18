package service

import (
	"context"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/go-git/go-git/v5/plumbing"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestSync_WakesWorkerAndBlocks verifies the Sync RPC contract: it wakes the
// worker and blocks until the cycle completes, and propagates the cycle's
// non-recoverable errors to the caller.
//
//nolint:gocyclo // One subtest per SPEC Sync error-table row; each is a t.Run branch.
func TestSync_WakesWorkerAndBlocks(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git sync worker")
	}
	t.Run("blocks until cycle completes", func(t *testing.T) {
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		syncGit := &syncMockGitStore{
			GitStore:    gs,
			pushEntered: make(chan struct{}),
			pushRelease: make(chan struct{}),
		}
		srv, fc := newSyncServer(t, syncGit)
		t.Cleanup(syncGit.releasePush)

		waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")
		srv.syncWorker.SetPushNeeded()

		syncDone := make(chan error, 1)
		go func() { _, err := srv.Sync(testCtx(), &flowv1.SyncRequest{}); syncDone <- err }()

		// The woken cycle parks in the push gate: Sync must still be blocked.
		select {
		case <-syncGit.pushEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("sync cycle never reached the push")
		}
		select {
		case err := <-syncDone:
			t.Fatalf("Sync returned before the cycle completed: %v", err)
		case <-time.After(100 * time.Millisecond):
		}

		syncGit.releasePush()
		if err := <-syncDone; err != nil {
			t.Fatalf("Sync returned error: %v", err)
		}
		if srv.syncWorker.pushNeeded() {
			t.Fatal("push flag not cleared after successful sync")
		}
	})

	t.Run("propagates non-recoverable errors", func(t *testing.T) {
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrAuthFailed}
		srv, fc := newSyncServer(t, syncGit)
		waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

		_, err = srv.Sync(testCtx(), &flowv1.SyncRequest{})
		if err == nil {
			t.Fatal("expected Sync to propagate the worker error")
		}
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("expected Unauthenticated for auth failure, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("propagates divergence as FailedPrecondition", func(t *testing.T) {
		// SPEC error-table row "Sync diverged" (SPEC:967): FetchAndMerge
		// detecting divergence surfaces FAILED_PRECONDITION through Sync().
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrPullDiverged}
		srv, fc := newSyncServer(t, syncGit)
		waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

		_, err = srv.Sync(testCtx(), &flowv1.SyncRequest{})
		if err == nil {
			t.Fatal("expected Sync to propagate the divergence error")
		}
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("expected FailedPrecondition for divergence, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("propagates auth-config-missing as FailedPrecondition", func(t *testing.T) {
		// SPEC error-table row "Remote auth config missing (Sync)" (SPEC:975):
		// gitstore.ErrAuthConfigMissing — the pre-flight guard when the remote
		// demands credentials but no auth provider is configured — is classified
		// non-recoverable by the worker and surfaces FAILED_PRECONDITION through
		// Sync() (classifySyncError → mapGitError, errors.go:168).
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrAuthConfigMissing}
		srv, fc := newSyncServer(t, syncGit)
		waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

		_, err = srv.Sync(testCtx(), &flowv1.SyncRequest{})
		if err == nil {
			t.Fatal("expected Sync to propagate the auth-config-missing error")
		}
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("expected FailedPrecondition for missing remote auth config, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("propagates no-remote as FailedPrecondition", func(t *testing.T) {
		// SPEC error-table row "Remote not configured" (SPEC:972) beyond the
		// server gate: a server with remoteURL set but a gitstore with no
		// remote — the production misconfiguration of a REMOTE_URL whose
		// SetRemote was rejected non-fatally at startup (pullOnInit=false,
		// cmd/main.go) — must surface FAILED_PRECONDITION through Sync(), not
		// a silent success. ErrNoRemote is classified non-recoverable by the
		// worker (classifySyncError → mapGitError, errors.go:164), so Sync
		// reports the row instead of claiming a full cycle ran (SPEC R10).
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrNoRemote}
		srv, fc := newSyncServer(t, syncGit)
		waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

		_, err = srv.Sync(testCtx(), &flowv1.SyncRequest{})
		if err == nil {
			t.Fatal("expected Sync to propagate the no-remote error")
		}
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("expected FailedPrecondition for a missing gitstore remote, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("propagates unsupported-URL-scheme as InvalidArgument", func(t *testing.T) {
		// SPEC error-table row "Unsupported remote URL scheme" (SPEC:984): a
		// scheme that is not https:// or ssh:// is a permanent pre-flight
		// config error — "the git operation cannot be attempted at all" — so
		// it is classified non-recoverable and surfaces INVALID_ARGUMENT
		// through Sync() (classifySyncError → mapGitError, errors.go:170).
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrUnsupportedURLScheme}
		srv, fc := newSyncServer(t, syncGit)
		waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

		_, err = srv.Sync(testCtx(), &flowv1.SyncRequest{})
		if err == nil {
			t.Fatal("expected Sync to propagate the unsupported-URL-scheme error")
		}
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument for unsupported remote URL scheme, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("propagates push rejection as FailedPrecondition", func(t *testing.T) {
		// SPEC R10 error classification ("non-fast-forward push rejection" is
		// non-recoverable, SPEC:610): a rejected push is classified
		// non-recoverable by the worker and surfaces FAILED_PRECONDITION
		// through Sync() (classifySyncError → mapGitError, errors.go:174).
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		syncGit := &syncMockGitStore{GitStore: gs, pushErr: gitstore.ErrPushRejected}
		srv, fc := newSyncServer(t, syncGit)
		waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")
		srv.syncWorker.SetPushNeeded()

		_, err = srv.Sync(testCtx(), &flowv1.SyncRequest{})
		if err == nil {
			t.Fatal("expected Sync to propagate the push-rejection error")
		}
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("expected FailedPrecondition for push rejection, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("propagates re-hydration failure as Internal", func(t *testing.T) {
		// SPEC error-table row "Sync re-hydration failed" (SPEC:973): a fetch
		// that advances main whose RehydrateMainFromFiles then fails (e.g. disk
		// full) is a non-recoverable hydrateError in the worker and surfaces
		// INTERNAL through Sync() (sync_worker.go fetchAttempt → mapGitError
		// default branch).
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
		flaky := &flakyRehydrateStore{Store: base, failAt: 100000}
		syncGit := &syncMockGitStore{GitStore: gs, fetchHash: plumbing.NewHash(fetchHash)}
		fc := newFakeClock(time.Now())
		sw := NewSyncWorker("https://example.com/repo.git", syncGit, flaky, fc)
		go sw.Run()
		t.Cleanup(sw.Stop)
		opPub, _ := generateTestKey()
		srv := NewCartographerServer(flaky, syncGit, opPub, initTestKey(), nil, "https://example.com/repo.git",
			30*time.Second, "test-ns", 30*time.Minute, 100000, WithSyncWorker(sw))
		srv.MarkDBReady()
		waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

		_, err = srv.Sync(testCtx(), &flowv1.SyncRequest{})
		if err == nil {
			t.Fatal("expected Sync to propagate the re-hydration failure")
		}
		if status.Code(err) != codes.Internal {
			t.Fatalf("expected Internal for re-hydration failure, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("recoverable-exhausted does not surface", func(t *testing.T) {
		// SPEC R10 Sync: "If the cycle encounters a non-recoverable error,
		// returns the worker's last error" — a recoverable-exhausted cycle
		// (all retries failed) is logged + telemetry in the worker and must
		// not surface as an RPC error.
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrRemoteUnreachable}
		base, err := openTestStore(t)
		if err != nil {
			t.Fatalf("openTestStore: %v", err)
		}
		t.Cleanup(func() { _ = base.Close() })
		fc := newFakeClock(time.Now())
		sw := NewSyncWorker("https://example.com/repo.git", syncGit, base, fc)
		sw.backoffFn = func(int) time.Duration { return 0 }
		go sw.Run()
		t.Cleanup(sw.Stop)
		opPub, _ := generateTestKey()
		srv := NewCartographerServer(base, syncGit, opPub, initTestKey(), nil, "https://example.com/repo.git",
			30*time.Second, "test-ns", 30*time.Minute, 100000, WithSyncWorker(sw))
		srv.MarkDBReady()
		waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

		if _, err := srv.Sync(testCtx(), &flowv1.SyncRequest{}); err != nil {
			t.Fatalf("Sync must not surface a recoverable-exhausted cycle error: %v", err)
		}
	})
}

// TestSync_WakesWorkerAndReturnsSuccess verifies the up-to-date Sync path:
// when the remote is up-to-date (fetch succeeds, no push needed), Sync wakes
// the worker, waits for the cycle, and returns nil.
func TestSync_WakesWorkerAndReturnsSuccess(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs}
	srv, fc := newSyncServer(t, syncGit)
	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	_, err = srv.Sync(testCtx(), &flowv1.SyncRequest{})
	if err != nil {
		t.Fatalf("Sync on an up-to-date remote should succeed: %v", err)
	}
	syncGit.mu.Lock()
	fetchCalls, pushCalls := syncGit.fetchCalls, syncGit.pushCalls
	syncGit.mu.Unlock()
	if fetchCalls != 2 {
		t.Fatalf("expected startup cycle + sync cycle fetches, got %d", fetchCalls)
	}
	if pushCalls != 0 {
		t.Fatalf("expected no push without a push flag, got %d", pushCalls)
	}
}

// TestSync_RemoteNotConfigured covers the SPEC error-table row "Remote not
// configured" (SPEC:972): Sync() on a server with no remote configured must
// return FAILED_PRECONDITION. Every other Sync test uses newSyncServer, which
// always configures a remote, so this branch is otherwise uncovered.
func TestSync_RemoteNotConfigured(t *testing.T) {
	srv, _ := newTestServer(t) // remoteURL == ""

	_, err := srv.Sync(testCtx(), &flowv1.SyncRequest{})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v (%v)", status.Code(err), err)
	}
}

// TestSync_MissingWriteCapability covers SPEC R10's Sync() capability
// requirement (WRITE:graph/entity/*): a context lacking it must return
// PERMISSION_DENIED before the sync worker is consulted.
func TestSync_MissingWriteCapability(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.remoteURL = "https://example.com/repo.git"
	ctx := narrowCtx("READ:graph/entity/*", "READ:graph/tx") // no WRITE:graph/entity/*

	_, err := srv.Sync(ctx, &flowv1.SyncRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v (%v)", status.Code(err), err)
	}
}

// TestSync_PerTypeWriteCapabilityDenied pins the SPEC R3 negative branch
// (SPEC:243: only WRITE:graph/entity/* "Authorises ... plus Sync()"): a caller
// holding only a per-type WRITE grant (e.g. WRITE:graph/entity/Component) must
// be denied Sync. TestSync_MissingWriteCapability uses a no-WRITE-at-all
// holder; if the wildcard gate regressed to accept per-type grants, only this
// test fails.
func TestSync_PerTypeWriteCapabilityDenied(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.remoteURL = "https://example.com/repo.git"
	ctx := narrowCtx("WRITE:graph/entity/Component") // per-type only, no wildcard

	_, err := srv.Sync(ctx, &flowv1.SyncRequest{})
	if status.Code(err) != codes.PermissionDenied ||
		status.Convert(err).Message() != "capability denied: WRITE:graph/entity/*" {
		t.Fatalf("expected per-type-only PermissionDenied for Sync, got %v", err)
	}
}

// TestSync_MissingCapabilityBeforeRemoteProbe pins the SPEC check-order table
// (SPEC:1023: Sync is "general rule only" — the capability gate) combined with
// SPEC R10's WRITE:graph/entity/* requirement (SPEC:243): the capability gate
// runs before the remote-configuration probe, so a caller holding no WRITE
// capability receives PERMISSION_DENIED regardless of whether a remote is
// configured. Before the fix the remoteURL=="" probe ran first, disclosing
// remote-configuration state to unprivileged callers (FAILED_PRECONDITION when
// no remote is configured). TestSync_MissingWriteCapability pins the
// configured-remote half; this test pins the no-remote half that detects a
// regression moving the capability check after the probe.
func TestSync_MissingCapabilityBeforeRemoteProbe(t *testing.T) {
	srv, _ := newTestServer(t) // remoteURL == "" AND caller lacks WRITE:graph/entity/*
	ctx := narrowCtx("READ:graph/entity/*", "READ:graph/tx")

	_, err := srv.Sync(ctx, &flowv1.SyncRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied (capability gate before remote probe), got %v (%v)", status.Code(err), err)
	}
}
