package service

import (
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestRefreshTransaction_NoConflicts(t *testing.T) {
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
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}

	_, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: beginResp.TransactionId,
	})
	if err != nil {
		t.Fatalf("RefreshTransaction failed: %v", err)
	}
}

// TestRefreshTransaction_DoesNotResetTimeoutTimer pins SPEC R9 step 4
// ("Refresh() does not reset the transaction timeout timer — the timeout is an
// absolute lifetime from BeginTransaction, not an idle timeout"): after a
// successful RefreshTransaction, advancing the fake clock past the original
// ExpiresAt must surface DEADLINE_EXCEEDED on the next transaction operation.
// The handler never touches ExpiresAt, but a regression that re-armed the timer
// inside Refresh (ExpiresAt = now + timeout) would keep the transaction alive
// past its original absolute lifetime — this test fails if that happens.
func TestRefreshTransaction_DoesNotResetTimeoutTimer(t *testing.T) {
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
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	// Replace the tx manager with a fake clock so the absolute lifetime can be
	// advanced deterministically without running the GC loop.
	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(7*24*time.Hour, 100000)
	srv.txManager.clock = fc
	ctx := testCtx()

	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(1 * time.Minute),
	})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}

	// Refresh while the transaction is still within its absolute lifetime.
	if _, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RefreshTransaction: %v", err)
	}

	// Advance past the original ExpiresAt (t0 + 1m). The refresh must not have
	// re-armed the timer, so the next operation reports DEADLINE_EXCEEDED.
	fc.Advance(2 * time.Minute)
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "late"},
		TransactionId: begin.TransactionId,
	})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded after refresh and expiry, got %v (%v)", status.Code(err), err)
	}
}

// TestRefreshTransaction_EmptyRefreshThenMutateAndCommit pins the SPEC R9
// refresh flow for a zero-mutation refresh: the branch must be reset and
// re-hydrated from latest main even when the change log is empty, so a
// subsequent mutate+commit produces the clean refresh-then-commit outcome. The
// previous empty-refresh short-circuit only advanced MainHeadAtLastSync: the
// branch DB stayed on its stale begin-time snapshot, so a mutate after the
// refresh committed against the stale branch, re-hydrated main from files
// missing the interim entity, and the fast-forward merge failed with INTERNAL,
// leaving main LadybugDB and git main divergent.
func TestRefreshTransaction_EmptyRefreshThenMutateAndCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git refresh/recovery")
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
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)

	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}

	// Main advances while the (empty) transaction is open.
	mainEntityID := testMutationEntityID
	commitGitEntity(ctx, t, gs, mainEntityID, "main")

	// Empty refresh: must reset-and-re-hydrate the branch from the new main.
	if _, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RefreshTransaction (empty): %v", err)
	}

	// Mutate after the empty refresh, then commit.
	created, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity after empty refresh: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CommitTransaction after empty refresh: %v", err)
	}

	// Both the interim main entity and the transaction's own entity must be
	// present on main, with the git fast-forward merge having succeeded.
	if _, err := base.GetEntity(ctx, mainEntityID, "main"); err != nil {
		t.Fatalf("interim main entity missing after refresh-then-commit: %v", err)
	}
	if _, err := base.GetEntity(ctx, created.EntityId, "main"); err != nil {
		t.Fatalf("transaction entity missing from main after commit: %v", err)
	}
}

func TestRefreshTransaction_ConcurrentCommitCannotUseStaleHead(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git refresh/recovery")
	}
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	blocking := &hydrationBlockingStore{Store: base, blocked: make(chan struct{}), release: make(chan struct{})}
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	attemptingGit := &gitAttemptStore{GitStore: gs}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		blocking, attemptingGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	created, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	mainEntityID := testMutationEntityID
	commitGitEntity(ctx, t, gs, mainEntityID, "main")

	refreshDone := make(chan error, 1)
	go func() {
		_, refreshErr := srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{TransactionId: begin.TransactionId})
		refreshDone <- refreshErr
	}()
	<-blocking.blocked
	gitAttempted := make(chan struct{})
	attemptingGit.setAttempted(gitAttempted)
	unrelatedDone := make(chan error, 1)
	go func() { unrelatedDone <- srv.withGitLock(func() error { return nil }) }()
	<-gitAttempted
	commitAtLifecycleLock := make(chan struct{})
	srv.txManager.beforeLifecycleLock = func(string) { close(commitAtLifecycleLock) }
	commitDone := make(chan error, 1)
	go func() {
		_, commitErr := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
		commitDone <- commitErr
	}()
	<-commitAtLifecycleLock
	srv.txManager.beforeLifecycleLock = nil
	close(blocking.release)
	if err := <-refreshDone; err != nil {
		t.Fatalf("RefreshTransaction: %v", err)
	}
	if err := <-unrelatedDone; err != nil {
		t.Fatalf("unrelated Git checkout: %v", err)
	}
	if err := <-commitDone; err != nil {
		t.Fatalf("CommitTransaction: %v", err)
	}
	if _, err := base.GetEntity(ctx, created.EntityId, "main"); err != nil {
		t.Fatalf("committed entity missing from main: %v", err)
	}
	if _, err := base.GetEntity(ctx, mainEntityID, "main"); err != nil {
		t.Fatalf("advanced main entity missing after refresh/commit: %v", err)
	}
}

func TestRefreshTransaction_HydrationFailureDoesNotAdvanceSyncHead(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git refresh/recovery")
	}
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	release := make(chan struct{})
	close(release)
	blocking := &hydrationBlockingStore{
		Store: base, blocked: make(chan struct{}), release: release, fail: true,
	}
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		blocking, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
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
		EntityType: "Component", Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	state, err := srv.txManager.Lookup(begin.TransactionId)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	oldHead := state.MainHeadAtLastSync
	commitGitEntity(ctx, t, gs, "11111111-1111-4111-8111-111111111111", "main")

	_, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{TransactionId: begin.TransactionId})
	if err == nil {
		t.Fatal("expected hydration failure")
	}
	if state.MainHeadAtLastSync != oldHead {
		t.Fatalf("sync head advanced after failed hydration: got %q want %q", state.MainHeadAtLastSync, oldHead)
	}
}
