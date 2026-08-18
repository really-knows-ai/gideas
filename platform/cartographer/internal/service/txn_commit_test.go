package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestCommitTransaction_MergeDivergedIsInternal asserts the SPEC R2 error-table
// row "Commit merge failed (post-re-hydration) → INTERNAL". When Commit's
// FastForwardMerge surfaces gitstore.ErrMergeDiverged, the handler must map it
// to INTERNAL — not the distinct "Refresh conflict → ABORTED" code.
func TestCommitTransaction_MergeDivergedIsInternal(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git + recovery")
	}
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	diverged := false
	divergingGit := &fakeGitStore{GitStore: gs, onFastForwardMerge: func(ctx context.Context, branch, into string) error {
		if !diverged {
			diverged = true
			return gitstore.ErrMergeDiverged
		}
		return gs.FastForwardMerge(ctx, branch, into)
	}}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, divergingGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "item"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected merge-diverged commit to map to INTERNAL, got %v (%v)", status.Code(err), err)
	}
	if !strings.Contains(err.Error(), "merge") {
		t.Fatalf("expected commit-merge error message, got %q", err.Error())
	}
}

// TestCommitTransaction_WithSyncWorker_AckWaitsForPush pins the service-layer
// CommitTransaction sync-worker branch (SPEC R10 commit/WithAck contract,
// SPEC:615-619): with a SyncWorker wired via WithSyncWorker, an acked commit
// sets the push-needed flag and blocks until the sync cycle delivers the push
// (the ack wait resolves only after the push completes), then returns success
// with the flag cleared. The TestSyncWorker_WithAck_* tests drive
// sw.WakeAndWait directly; this test pins the handler wiring
// (SetPushNeeded → req.GetAck() → WakeAndWait) end to end.
func TestCommitTransaction_WithSyncWorker_AckWaitsForPush(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git sync worker")
	}
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

	ctx := testCtx()
	applyTestSchema(ctx, t, srv.store)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "acked"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	// The acked commit must block until the sync cycle delivers the push: the
	// woken cycle parks in the push gate, and CommitTransaction stays pending.
	commitDone := make(chan error, 1)
	go func() {
		_, err := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
			TransactionId: begin.TransactionId, Ack: true,
		})
		commitDone <- err
	}()
	select {
	case <-syncGit.pushEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("acked commit never reached the sync worker's push")
	}
	select {
	case err := <-commitDone:
		t.Fatalf("CommitTransaction returned before the push was delivered: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	syncGit.releasePush()
	if err := <-commitDone; err != nil {
		t.Fatalf("CommitTransaction with ack returned error after the push was delivered: %v", err)
	}

	// The commit→push-flag contract fired and the ack cycle delivered exactly
	// one push (SetPushNeeded was observed by the worker).
	syncGit.mu.Lock()
	pushCalls := syncGit.pushCalls
	syncGit.mu.Unlock()
	if pushCalls != 1 {
		t.Fatalf("expected exactly 1 push for the acked commit, got %d", pushCalls)
	}
	if srv.syncWorker.pushNeeded() {
		t.Fatal("push flag not cleared after the acked push")
	}
}

// TestCommitTransaction_WithSyncWorker_AckPushFailureSurfacesMappedError pins
// the ack-error mapping branch of the CommitTransaction sync-worker wiring
// (SPEC R10, SPEC:620-621: "If the cycle ends with the flag still set
// (permanent failure, or retries exhausted), the call returns an error with
// the worker's last push error"): a non-recoverable push rejection surfaces
// through mapGitError as FAILED_PRECONDITION ("push rejected
// (non-fast-forward)"), not a raw INTERNAL error, and the push flag stays set
// for the next cycle.
func TestCommitTransaction_WithSyncWorker_AckPushFailureSurfacesMappedError(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git sync worker")
	}
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs, pushErr: gitstore.ErrPushRejected}
	srv, fc := newSyncServer(t, syncGit)
	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	ctx := testCtx()
	applyTestSchema(ctx, t, srv.store)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "rejected"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId, Ack: true,
	})
	if err == nil {
		t.Fatal("expected the acked commit to surface the rejected push")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition for a rejected push, got %v (%v)", status.Code(err), err)
	}
	if !srv.syncWorker.pushNeeded() {
		t.Fatal("push flag cleared despite the rejected push")
	}
}

// TestCommitTransaction_WithSyncWorker_AckPushRetriesExhaustedSurfacesUnavailable
// pins the SPEC error-table row "Remote unreachable" (UNAVAILABLE) through the
// ack-error mapping branch of the CommitTransaction sync-worker wiring (SPEC
// R10, SPEC:620-621: "If the cycle ends with the flag still set (permanent
// failure, or retries exhausted), the call returns an error with the worker's
// last push error"): a push whose retries exhaust against an unreachable remote
// (DNS failure, connection refused, or transport timeout — ErrRemoteUnreachable
// is classified recoverable and retried within the cycle) surfaces through
// mapGitError as UNAVAILABLE, not a raw INTERNAL error, and the push flag stays
// set for the next cycle.
func TestCommitTransaction_WithSyncWorker_AckPushRetriesExhaustedSurfacesUnavailable(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git sync worker")
	}
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs, pushErr: gitstore.ErrRemoteUnreachable}
	srv, fc := newSyncServer(t, syncGit)
	srv.syncWorker.backoffFn = func(int) time.Duration { return 0 }
	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	ctx := testCtx()
	applyTestSchema(ctx, t, srv.store)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "unreachable"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId, Ack: true,
	})
	if err == nil {
		t.Fatal("expected the acked commit to surface the exhausted-push error")
	}
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable for an unreachable remote, got %v (%v)", status.Code(err), err)
	}
	if !srv.syncWorker.pushNeeded() {
		t.Fatal("push flag cleared despite the unreachable remote")
	}
}

// TestCommitTransaction_WithSyncWorker_AckCallerDeadlineSurfacesDeadlineExceeded
// pins the caller-deadline branch of the CommitTransaction sync-worker wiring
// (SPEC R10, SPEC:621-622: "A caller that hits the context deadline receives
// DEADLINE_EXCEEDED and the flag stays set"): a caller deadline expires while
// the acked commit waits on the sync cycle, and the
// commit surfaces DEADLINE_EXCEEDED (mapGitError's context-error mapping, not
// a raw INTERNAL), with the push flag left set for the next cycle.
func TestCommitTransaction_WithSyncWorker_AckCallerDeadlineSurfacesDeadlineExceeded(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git sync worker")
	}
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

	ctx := testCtx()
	applyTestSchema(ctx, t, srv.store)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "slow"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	// The commit's local git work is fast; the caller deadline is long enough
	// for it to finish but expires while the acked commit is blocked in the
	// sync-cycle wait (the push gate keeps the cycle from completing). Derived
	// from ctx so the capability metadata set by testCtx is preserved.
	ackCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	start := time.Now()
	_, err = srv.CommitTransaction(ackCtx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId, Ack: true,
	})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded for an expired ack wait, got %v (%v)", status.Code(err), err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("acked commit took %v to surface the deadline", elapsed)
	}
	if !srv.syncWorker.pushNeeded() {
		t.Fatal("push flag cleared despite the timed-out ack wait")
	}
}

// TestCommitTransaction_WithSyncWorker_NoAckReturnsWithoutBlocking pins the
// SPEC R10 commit branch "commit() returns immediately and sets the
// push-needed flag" (SPEC:614-615 — the non-WithAck path): with a SyncWorker
// wired via WithSyncWorker, a commit whose Ack is unset must set the push flag
// and return without blocking for the sync cycle. Every other
// CommitTransaction-with-sync-worker test uses Ack: true (the acked tests park
// the woken cycle's push in the gate and assert the handler stays blocked); a
// regression that made the handler wake-and-wait unconditionally would trip
// this test's timeout while the cycle parks in the gate below.
func TestCommitTransaction_WithSyncWorker_NoAckReturnsWithoutBlocking(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git sync worker")
	}
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

	ctx := testCtx()
	applyTestSchema(ctx, t, srv.store)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "no-ack"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	// The non-acked commit must return immediately — SetPushNeeded fires but no
	// WakeAndWait blocks for the cycle.
	commitDone := make(chan error, 1)
	go func() {
		_, err := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
			TransactionId: begin.TransactionId,
		})
		commitDone <- err
	}()
	select {
	case err := <-commitDone:
		if err != nil {
			t.Fatalf("CommitTransaction (no ack): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("non-acked CommitTransaction blocked on the sync worker")
	}

	// No sync cycle ran during the commit, and the push flag is set for the
	// worker to pick up on its next cycle.
	syncGit.mu.Lock()
	pushCalls := syncGit.pushCalls
	syncGit.mu.Unlock()
	if pushCalls != 0 {
		t.Fatalf("non-acked commit ran the sync cycle (%d push attempts)", pushCalls)
	}
	if !srv.syncWorker.pushNeeded() {
		t.Fatal("push flag not set after the non-acked commit")
	}

	// Deliver the flag on the next timer-driven cycle: the push completes and
	// the flag clears, proving the worker attached to the commit is live.
	syncGit.releasePush()
	fc.FireTicker()
	waitFor(t, func() bool {
		syncGit.mu.Lock()
		defer syncGit.mu.Unlock()
		return syncGit.pushCalls >= 1
	}, "push on the next cycle after the non-acked commit")
	if srv.syncWorker.pushNeeded() {
		t.Fatal("push flag not cleared after the next cycle delivered the push")
	}
}

func TestCommitTransaction_FileRehydrationUsesExactlyOnePath(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	counting := &hydrationCountingStore{Store: base}
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		counting, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(t.TempDir()),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "hydrate"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("CommitTransaction: %v", err)
	}
	if counting.fromFiles != 1 || counting.fromBranch != 0 {
		t.Fatalf(
			"expected one file hydration and no branch hydration, got files=%d branch=%d",
			counting.fromFiles, counting.fromBranch,
		)
	}
}

func TestCommitTransaction_RetryAfterCommitCreatedDoesNotDuplicateCommit(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	countingGit := &commitCountingGitStore{GitStore: gs}
	rehydrateFailed := false
	failingStore := &fakeStore{Store: base, onRehydrateMainFromFiles: func(
		ctx context.Context, entitiesDir, edgesDir string,
	) error {
		if !rehydrateFailed {
			rehydrateFailed = true
			return fmt.Errorf("simulated rehydration failure")
		}
		return base.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
	}}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		failingStore, countingGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "retry"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	// SPEC error table (SPEC:975): "Commit serialisation or re-hydration failed"
	// maps to INTERNAL — the re-hydration failure surfaces via mapGitError's
	// default branch, so the first commit attempt must fail with codes.Internal.
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected commit re-hydration failure to map to INTERNAL, got %v (%v)", status.Code(err), err)
	}
	state, lookupErr := srv.txManager.Lookup(begin.TransactionId)
	if lookupErr != nil || !state.CommitCreated || state.MergeCompleted {
		t.Fatalf("unexpected retry state: state=%+v error=%v", state, lookupErr)
	}
	// A mutation against a transaction whose commit has started is rejected
	// with NOT_FOUND (SPEC error-table row "Transaction not found": "was
	// already committed/rolled back" — the commit-in-progress handle no
	// longer references a usable active transaction from the write surface).
	// FAILED_PRECONDITION would be an un-justified code. RefreshTransaction,
	// by contrast, remains available for a commit-started transaction whose
	// commit has not merged (the SPEC "Commit not up-to-date with main" row
	// prescribes "Call Refresh() before Commit()") — see
	// TestRefreshTransaction_CommitMergeFailedThenMainAdvancedUnwedges.
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "late"}, TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.NotFound {
		t.Fatalf("expected mutation rejection after commit creation, got %v", err)
	}
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("retry CommitTransaction: %v", err)
	}
	if countingGit.commits != 1 {
		t.Fatalf("expected one transaction commit, got %d", countingGit.commits)
	}
}
