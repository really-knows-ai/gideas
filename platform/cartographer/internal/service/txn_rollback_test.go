package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRollbackTransaction_RestoresMainAfterFailedMerge(t *testing.T) {
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
	failingGit := &mergeFailingGitStore{GitStore: gs, failMerge: true}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, failingGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
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
		EntityType: "Component", Properties: map[string]string{"name": "partial"}, TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	if err == nil {
		t.Fatal("expected merge failure")
	} else if !strings.Contains(err.Error(), "simulated merge failure") {
		t.Fatalf("commit failed before merge: %v", err)
	}
	if _, err = base.GetEntity(ctx, created.EntityId, "main"); err != nil {
		t.Fatalf("partial commit did not rehydrate main before merge failure: %v", err)
	}
	state, err := srv.txManager.Lookup(begin.TransactionId)
	if err != nil || !state.MainRehydrated {
		t.Fatalf("partial commit state not recorded: state=%+v error=%v", state, err)
	}
	_, err = srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("RollbackTransaction: %v", err)
	}
	if _, err = base.GetEntity(ctx, created.EntityId, "main"); !errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("transaction entity remained visible after rollback: %v", err)
	}
}

// TestRollbackTransaction_PartialCommitWithoutLadybugPathIsExplicit pins the
// SPEC error-table row "Commit serialisation or re-hydration failed"
// (INTERNAL) for a rollback that must restore the main store after a partial
// commit in a process whose LADYBUG_DB_PATH is unset: the restoration is
// re-hydration work, so the failure surfaces INTERNAL (never a FAILED_PRECONDITION
// no error-table row assigns to this condition), and the failed rollback leaves
// the transaction registered for retry.
func TestRollbackTransaction_PartialCommitWithoutLadybugPathIsExplicit(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.gitstore = &mergeFailingGitStore{GitStore: srv.gitstore, failMerge: true}
	ctx := testCtx()
	applyTestSchema(ctx, t, srv.store)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "partial"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	if err == nil {
		t.Fatal("expected merge failure")
	}
	_, err = srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{TransactionId: begin.TransactionId})
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal without LADYBUG_DB_PATH, got %v", err)
	}
	if _, unlock, err := srv.txManager.LockActive(begin.TransactionId); err != nil {
		t.Fatalf("transaction was deregistered after explicit restoration failure: %v", err)
	} else {
		unlock()
	}
}

func TestRollbackTransaction_WaitsForUnrelatedGitActivity(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	attempting := &gitAttemptStore{GitStore: srv.gitstore}
	srv.gitstore = attempting
	gitHeld := make(chan struct{})
	releaseGit := make(chan struct{})
	unrelatedDone := make(chan error, 1)
	go func() {
		unrelatedDone <- srv.withGitLock(func() error {
			close(gitHeld)
			<-releaseGit
			return nil
		})
	}()
	<-gitHeld
	attempted := make(chan struct{})
	attempting.setAttempted(attempted)
	rollbackDone := make(chan error, 1)
	go func() {
		_, rollbackErr := srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
			TransactionId: begin.TransactionId,
		})
		rollbackDone <- rollbackErr
	}()
	<-attempted
	close(releaseGit)
	if err := <-unrelatedDone; err != nil {
		t.Fatalf("unrelated Git activity: %v", err)
	}
	if err := <-rollbackDone; err != nil {
		t.Fatalf("RollbackTransaction: %v", err)
	}
}

func TestRollbackTransaction_AfterReconciledCommitError(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git refresh/recovery")
	}
	for _, tc := range []struct {
		name       string
		failBefore bool
		failAfter  bool
	}{
		{name: "no commit created", failBefore: true},
		{name: "commit created", failAfter: true},
	} {
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
			failingGit := &commitErrorGitStore{
				GitStore: gs, failBefore: tc.failBefore, failAfter: tc.failAfter,
			}
			opPub, _ := generateTestKey()
			srv := NewCartographerServer(
				base, failingGit, opPub, initTestKey(), nil, "", 30*time.Second,
				"test-ns", 30*time.Minute, 100000,
			)
			srv.MarkDBReady()
			ctx := testCtx()
			applyTestSchema(ctx, t, base)
			begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
			if err != nil {
				t.Fatalf("BeginTransaction: %v", err)
			}
			if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
				EntityType: "Component", Properties: map[string]string{"name": "rollback"},
				TransactionId: begin.TransactionId,
			}); err != nil {
				t.Fatalf("CreateEntity: %v", err)
			}
			if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
				TransactionId: begin.TransactionId,
			}); err == nil {
				t.Fatal("expected commit error")
			}
			if _, err = srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
				TransactionId: begin.TransactionId,
			}); err != nil {
				t.Fatalf("RollbackTransaction: %v", err)
			}
			if _, err = srv.txManager.Lookup(begin.TransactionId); err == nil {
				t.Fatal("rolled-back transaction remains registered")
			}
		})
	}
}

func TestRollbackTransaction_AfterRestartDuringMainRehydrationRestoresMain(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()
	base, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	failingGit := &mergeFailingGitStore{GitStore: gs, failMerge: true}
	srv := NewCartographerServer(
		base, failingGit, opPub, initTestKey(), nil, "", 30*time.Second,
		"test-ns", 30*time.Minute, 100000, WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	created, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "unmerged"},
		TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err == nil {
		t.Fatal("expected merge failure")
	}
	if err = base.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second,
		"test-ns", 30*time.Minute, 100000, WithLadybugPath(dataPath),
	)
	restarted.MarkDBReady()
	if err = restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions: %v", err)
	}
	state, err := restarted.txManager.Lookup(begin.TransactionId)
	if err != nil || !state.MainRehydrated || !state.CommitCreated {
		t.Fatalf("recovered partial commit state=%+v err=%v", state, err)
	}
	if _, err = restarted.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RollbackTransaction: %v", err)
	}
	if _, err = reopened.GetEntity(ctx, created.EntityId, "main"); !errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("unmerged transaction entity survived rollback: %v", err)
	}
}

func TestRollbackTransaction_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	_, err := srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
		TransactionId: "11111111-1111-4111-8111-111111111111",
	})
	if err == nil {
		t.Fatal("expected error for not-found tx, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

// TestRollbackTransaction_MissingTxCapability pins SPEC R3 (SPEC:244): a
// caller without WRITE:graph/tx is denied RollbackTransaction with
// PERMISSION_DENIED before any transaction lookup or validation.
func TestRollbackTransaction_MissingTxCapability(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Only entity capabilities, no WRITE:graph/tx.
	ctx := capabilityContext("READ:graph/entity/*,WRITE:graph/entity/*", scPriv, "sidecar")

	_, err := srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
		TransactionId: testMutationEntityID,
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing WRITE:graph/tx capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}
