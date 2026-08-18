package service

import (
	"context"
	"fmt"
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

func TestCommitTransaction_CommitFailureWithoutCommitAllowsRefreshAndRetry(t *testing.T) {
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
	commits := 0
	failBefore := true
	failingGit := &fakeGitStore{GitStore: gs, onCommit: func(ctx context.Context, message string) error {
		commits++
		if failBefore {
			failBefore = false
			return fmt.Errorf("simulated commit failure")
		}
		return gs.Commit(ctx, message)
	}}
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
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "retry"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.Internal {
		// SPEC error table (SPEC:975): "Commit serialisation or re-hydration
		// failed" maps to INTERNAL — the git Commit failure surfaces via
		// mapGitError's default branch as codes.Internal.
		t.Fatalf("expected commit serialisation failure to map to INTERNAL, got %v (%v)", status.Code(err), err)
	}
	state, lookupErr := srv.txManager.Lookup(begin.TransactionId)
	if lookupErr != nil || state.CommitStarted || state.CommitCreated {
		t.Fatalf("commit failure remained irreversible: state=%+v error=%v", state, lookupErr)
	}
	commitGitEntity(ctx, t, gs, "22222222-2222-4222-8222-222222222222", "concurrent")
	if _, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RefreshTransaction after failed commit: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("retry CommitTransaction: %v", err)
	}
	if commits != 2 {
		t.Fatalf("expected two transaction commit attempts, got %d", commits)
	}
}

func TestCommitTransaction_ErrorAfterCommitRetainsResumableState(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	ladybugPath := t.TempDir()
	base, err := ladybug.Open(ladybugPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	commits := 0
	failAfter := true
	failingGit := &fakeGitStore{GitStore: gs, onCommit: func(ctx context.Context, message string) error {
		commits++
		if err := gs.Commit(ctx, message); err != nil {
			return err
		}
		if failAfter {
			failAfter = false
			return fmt.Errorf("simulated error after commit")
		}
		return nil
	}}
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
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "resume"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err == nil {
		t.Fatal("expected error after commit creation")
	}
	if err = base.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := ladybug.Open(ladybugPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	countingGit := &commitCountingGitStore{GitStore: reopenedGit}
	restarted := NewCartographerServer(
		reopened, countingGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	restarted.MarkDBReady()
	if err = restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions: %v", err)
	}
	state, lookupErr := restarted.txManager.Lookup(begin.TransactionId)
	if lookupErr != nil || !state.CommitStarted || !state.CommitCreated {
		t.Fatalf("created commit was not retained: state=%+v error=%v", state, lookupErr)
	}
	// A mutation against the recovered mid-commit transaction is rejected
	// with NOT_FOUND (SPEC error-table row "Transaction not found": the
	// commit-in-progress handle no longer references a usable active
	// transaction from the write surface). RefreshTransaction, by contrast,
	// remains available for a commit-started transaction whose commit has not
	// merged (the SPEC "Commit not up-to-date with main" row prescribes
	// "Call Refresh() before Commit()") — see
	// TestRefreshTransaction_CommitMergeFailedThenMainAdvancedUnwedges.
	if _, err = restarted.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "late"},
		TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.NotFound {
		t.Fatalf("expected mutation rejection after commit creation, got %v", err)
	}
	if _, err = restarted.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("retry CommitTransaction: %v", err)
	}
	if commits != 1 || countingGit.commits != 0 {
		t.Fatalf("expected no duplicate transaction commit, before restart=%d after restart=%d",
			commits, countingGit.commits)
	}
	if err = reopenedGit.WithGitLock(func() error {
		if err := reopenedGit.RestoreMain(ctx); err != nil {
			return err
		}
		logs, logErr := reopenedGit.GitLogOneline(ctx, "transaction:"+begin.TransactionId)
		if logErr != nil {
			return logErr
		}
		if len(logs) != 1 {
			return fmt.Errorf("expected one transaction commit, got %d", len(logs))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCommitTransaction_StateWriteFailureRemainsDiscoverableAndRetryable(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git refresh/recovery")
	}
	for _, tc := range []struct {
		name          string
		fail          func(store.BranchTransactionState) bool
		commitCreated bool
	}{
		{
			name: "before commit",
			fail: func(state store.BranchTransactionState) bool {
				return state.CommitStarted && !state.CommitCreated
			},
		},
		{
			name: "after commit",
			fail: func(state store.BranchTransactionState) bool {
				return state.CommitCreated
			},
			commitCreated: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, err := openTestStore(t)
			if err != nil {
				t.Fatalf("openTestStore: %v", err)
			}
			t.Cleanup(func() { _ = base.Close() })
			failingStore := newTxStateFailingStore(base, tc.fail)
			ladybugPath := t.TempDir()
			gs, err := gitstore.New(ladybugPath)
			if err != nil {
				t.Fatalf("gitstore.New: %v", err)
			}
			countingGit := &commitCountingGitStore{GitStore: gs}
			opPub, _ := generateTestKey()
			srv := NewCartographerServer(
				failingStore, countingGit, opPub, initTestKey(), nil, "", 30*time.Second,
				"test-ns", 30*time.Minute, 100000, WithLadybugPath(ladybugPath),
			)
			srv.MarkDBReady()
			ctx := testCtx()
			applyTestSchema(ctx, t, base)
			begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
			if err != nil {
				t.Fatalf("BeginTransaction: %v", err)
			}
			if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
				EntityType: "Component", Properties: map[string]string{"name": "retry"},
				TransactionId: begin.TransactionId,
			}); err != nil {
				t.Fatalf("CreateEntity: %v", err)
			}
			if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
				TransactionId: begin.TransactionId,
			}); err == nil || !strings.Contains(err.Error(), "state write failure") {
				t.Fatalf("CommitTransaction error=%v", err)
			}
			state, err := srv.txManager.Lookup(begin.TransactionId)
			if err != nil || state.CommitCreated != tc.commitCreated {
				t.Fatalf("reconciled state=%+v err=%v", state, err)
			}
			_, refreshErr := srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
				TransactionId: begin.TransactionId,
			})
			// A refresh remains available after both failure points — the SPEC
			// error-table row "Commit not up-to-date with main" prescribes
			// "Call Refresh() before Commit()", and a commit whose state write
			// failed never merged (the SPEC "Commit merge failed
			// (post-re-hydration)" row leaves the merge retryable). The
			// refresh re-opens the transaction, so the retried commit re-enters
			// the serialisation flow: a fresh commit is created for the
			// after-commit case, whose orphaned commit the refresh discarded.
			if refreshErr != nil {
				t.Fatalf("refresh after %s failure: %v", tc.name, refreshErr)
			}
			if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
				TransactionId: begin.TransactionId,
			}); err != nil {
				t.Fatalf("retry CommitTransaction: %v", err)
			}
			expectedCommits := 1
			if tc.commitCreated {
				expectedCommits = 2
			}
			if countingGit.commits != expectedCommits {
				t.Fatalf("expected %d Git commits, got %d", expectedCommits, countingGit.commits)
			}
		})
	}
}

func TestCommitTransaction_RetryAfterMergeCompletedOnlyCleansUp(t *testing.T) {
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
	commits, merges := 0, 0
	failRestore := false
	failingGit := &fakeGitStore{GitStore: gs,
		onCommit: func(ctx context.Context, message string) error {
			commits++
			return gs.Commit(ctx, message)
		},
		onFastForwardMerge: func(ctx context.Context, branch, into string) error {
			merges++
			return gs.FastForwardMerge(ctx, branch, into)
		},
		onRestoreMain: func(ctx context.Context) error {
			if failRestore {
				failRestore = false
				return fmt.Errorf("simulated post-merge restore failure")
			}
			return gs.RestoreMain(ctx)
		},
	}
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
		EntityType: "Component", Properties: map[string]string{"name": "merged"}, TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	failRestore = true
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	if err == nil {
		t.Fatal("expected post-merge cleanup failure")
	}
	state, lookupErr := srv.txManager.Lookup(begin.TransactionId)
	if lookupErr != nil || !state.MergeCompleted {
		t.Fatalf("merge completion was not retained: state=%+v error=%v", state, lookupErr)
	}
	// A rollback against an already-committed transaction (the merge landed on
	// main) is rejected with NOT_FOUND (SPEC error-table row "Transaction not
	// found": "was already committed/rolled back"), not FAILED_PRECONDITION.
	_, err = srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{TransactionId: begin.TransactionId})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected rollback rejection after merge, got %v", err)
	}
	if _, err = base.GetEntity(ctx, created.EntityId, "main"); err != nil {
		t.Fatalf("committed entity was removed by rejected rollback: %v", err)
	}
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
	if commits != 1 || merges != 1 {
		t.Fatalf("retry repeated irreversible work: commits=%d merges=%d", commits, merges)
	}
	if err := gs.WithGitLock(func() error {
		exists, err := gs.BranchExists(ctx, begin.TransactionId)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("transaction Git branch still exists after cleanup")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestCommitTransaction_MergePersistFailureSetsPushNeededOnRetry pins the SPEC
// R10 "commit() returns immediately and sets the push-needed flag" contract
// across the merge-then-persist-failure retry: when the fast-forward merge
// lands on main but the MergeCompleted state write fails, the first attempt
// returns an error without flagging the push; the retry (MergeCompleted path)
// must flag the locally-merged commit for push even though it only finishes the
// cleanup. Without the flag the locally-merged commit stays un-pushed until an
// unrelated later commit sets it.
func TestCommitTransaction_MergePersistFailureSetsPushNeededOnRetry(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	// Fail the one MergeCompleted state write (the "persist completed merge"
	// step); the earlier commit-created / re-hydration writes pass through.
	failingStore := newTxStateFailingStore(base, func(state store.BranchTransactionState) bool {
		return state.MergeCompleted
	})
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		failingStore, gs, opPub, initTestKey(), nil, "", 30*time.Second,
		"test-ns", 30*time.Minute, 100000, WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	// Wire a (non-running) SyncWorker so the push flag is observable. No
	// remote URL is configured, so BeginTransaction's implicit sync is skipped.
	sw := NewSyncWorker("", gs, base, RealClock{})
	srv.syncWorker = sw
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	created, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "merged"},
		TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	// First attempt: the git fast-forward merge lands on main, but persisting
	// MergeCompleted fails, so CommitTransaction returns an error before the
	// normal-path SetPushNeeded call.
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err == nil || !strings.Contains(err.Error(), "state write failure") {
		t.Fatalf("CommitTransaction error=%v", err)
	}
	if sw.pushNeeded() {
		t.Fatal("push flag set on the failed first attempt before any retry")
	}
	// Retry: the MergeCompleted path finishes the cleanup and must flag the
	// locally-merged commit for push.
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("retry CommitTransaction: %v", err)
	}
	if !sw.pushNeeded() {
		t.Fatal("locally-merged commit never flagged for push after the retry")
	}
	if _, err = base.GetEntity(ctx, created.EntityId, "main"); err != nil {
		t.Fatalf("merged entity missing from main: %v", err)
	}
}
