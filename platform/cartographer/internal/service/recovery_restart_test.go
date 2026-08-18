package service

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// TestRecoverOpenTransactionsMissingStateRollsBack pins the BeginTransaction
// crash-window recovery (SPEC R9 change-log recovery): a git branch whose
// branch DB exists but whose state record is missing (crash between
// HydrateBranchFromFiles and persistTransactionState) must be rolled back
// instead of hard-failing startup.
func TestRecoverOpenTransactionsMissingStateRollsBack(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	ctx := testCtx()
	dataPath := t.TempDir()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	applyTestSchema(ctx, t, st)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	branchDBPath := filepath.Join(dataPath, "branches", begin.TransactionId+".lbug")
	if _, err := os.Stat(branchDBPath); err != nil {
		t.Fatalf("stat persisted branch DB: %v", err)
	}
	// Simulate the BeginTransaction crash window: the branch DB and git branch
	// persist but the state record was never written.
	if err := os.Remove(filepath.Join(dataPath, "branches", begin.TransactionId+".state.json")); err != nil {
		t.Fatalf("remove state record: %v", err)
	}
	if err := st.Close(); err != nil {
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
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	// Recovery must roll the harmless transaction back, not wedge startup.
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions with missing state record: %v", err)
	}
	if _, err := os.Stat(branchDBPath); !os.IsNotExist(err) {
		t.Fatalf("missing-state transaction branch DB was not rolled back: %v", err)
	}
	if err := reopenedGit.WithGitLock(func() error {
		exists, err := reopenedGit.BranchExists(ctx, begin.TransactionId)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("missing-state transaction git branch still exists")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Startup continues: a fresh transaction begins normally.
	if _, err := restarted.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{}); err != nil {
		t.Fatalf("BeginTransaction after missing-state recovery: %v", err)
	}
}

// TestRecoverOpenTransactionsMidRefreshEmptyDiffRollsBack pins the
// BranchRefreshInProgress guard in RecoverOpenTransactions' empty-diff branch
// (SPEC R9 change-log recovery step 5): a transaction whose durable record
// carries the refresh-in-progress marker AND whose branch DB diff is empty is a
// mid-refresh crash — the branch DB was swapped to a clean copy of main and the
// transaction's changes existed only in the in-memory change log — NOT an
// already-committed transaction. Recovery must roll the branch back loudly
// instead of silently reporting the transaction as committed.
func TestRecoverOpenTransactionsMidRefreshEmptyDiffRollsBack(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()

	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, st)
	mainEntity, err := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "main"}, nil, "main")
	if err != nil {
		t.Fatalf("create main entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, mainEntity.Id, "main")
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "lost"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	// Construct the old swap-to-reapply crash state: the branch DB has been
	// replaced by a clean copy of main (the transaction's change existed only
	// in the in-memory change log, lost at the crash) and the durable record
	// carries the refresh-in-progress marker.
	state, err := st.LoadBranchTransactionState(ctx, begin.TransactionId)
	if err != nil {
		t.Fatalf("load branch state: %v", err)
	}
	state.BranchRefreshInProgress = true
	if err := st.SaveBranchTransactionState(ctx, begin.TransactionId, state); err != nil {
		t.Fatalf("persist marker: %v", err)
	}
	if err := st.CloseBranchDB(ctx, begin.TransactionId); err != nil {
		t.Fatalf("close branch handle: %v", err)
	}
	branchesDir := filepath.Join(dataPath, "branches")
	for _, f := range []string{begin.TransactionId + ".lbug", begin.TransactionId + ".schema.json"} {
		if err := os.Remove(filepath.Join(branchesDir, f)); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove branch file %s: %v", f, err)
		}
	}
	if err := st.CreateBranchDB(ctx, begin.TransactionId); err != nil {
		t.Fatalf("recreate branch DB: %v", err)
	}
	if err := st.ReplicateSchemaToBranch(ctx, begin.TransactionId); err != nil {
		t.Fatalf("replicate schema: %v", err)
	}
	entitiesDir, edgesDir := gs.HydrationDirs()
	if err := st.HydrateBranchFromFiles(ctx, begin.TransactionId, entitiesDir, edgesDir); err != nil {
		t.Fatalf("hydrate clean branch: %v", err)
	}

	oldLogger := slog.Default()
	defer slog.SetDefault(oldLogger)
	captured := &captureLogHandler{}
	slog.SetDefault(slog.New(captured))

	if err := st.Close(); err != nil {
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
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	restarted.MarkDBReady()
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions: %v", err)
	}
	// The transaction must not be re-registered as active (there is nothing to
	// recover — its changes were never durable) and its branch must be rolled
	// back.
	if _, err := restarted.txManager.Lookup(begin.TransactionId); err == nil {
		t.Fatal("mid-refresh-crash transaction was recovered as active despite an empty branch diff")
	}
	if _, err := os.Stat(filepath.Join(branchesDir, begin.TransactionId+".lbug")); !os.IsNotExist(err) {
		t.Fatalf("mid-refresh-crash branch DB was not rolled back: %v", err)
	}
	// The empty-diff classification must be the loud mid-refresh rollback, not
	// the silent "already committed" claim.
	if !slices.Contains(captured.messages,
		"RecoverOpenTransactions: rolled back transaction interrupted by a mid-refresh crash (never committed)") {
		t.Fatalf("expected loud mid-refresh-crash rollback log, got %v", captured.messages)
	}
}

// TestRecoverOpenTransactionsStaleBranchNoFalseDeletions pins SPEC R9
// change-log recovery computing the reconstruction diff against the
// transaction's true baseline (MainHeadAtLastSync) rather than current main: a
// transaction whose branch predates a main advancement (stale branch) must not
// report entities added to main after the branch began as "suspected deletions"
// — it never deleted them, and doing so wedges the transaction into an
// unresolvable ABORTED refresh. Recovery (recoverEntityChanges) only flags an
// entity as a suspected deletion when it was present in main at the
// transaction's begin head and is absent from the branch DB.
func TestRecoverOpenTransactionsStaleBranchNoFalseDeletions(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()

	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, st)
	// Entity A is present at the transaction's begin head.
	mainA, err := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "main"}, nil, "main")
	if err != nil {
		t.Fatalf("create main entity A: %v", err)
	}
	commitGitEntity(ctx, t, gs, mainA.Id, "main")
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	// The transaction legitimately modifies A.
	if _, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: mainA.Id, Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("UpdateEntity in transaction: %v", err)
	}
	// Main advances past the transaction's begin head: a NEW entity B is
	// committed to main after the branch began (stale branch).
	commitGitEntity(ctx, t, gs, "99999999-9999-4999-8999-999999999999", "added-later")

	if err := st.Close(); err != nil {
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
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	restarted.MarkDBReady()
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions: %v", err)
	}
	state, err := restarted.txManager.Lookup(begin.TransactionId)
	if err != nil {
		t.Fatalf("lookup recovered transaction: %v", err)
	}
	diff, err := restarted.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("GetTransactionDiff: %v", err)
	}
	// A is a genuine modification (present at the begin head, content changed).
	if len(diff.ModifiedEntities) != 1 || diff.ModifiedEntities[0].Id != mainA.Id {
		t.Fatalf("expected the transaction's own modification of A, got %+v", diff.ModifiedEntities)
	}
	// B was added to main after the branch began and must NOT be reported as a
	// suspected deletion of this transaction.
	if len(diff.DeletedEntities) != 0 {
		t.Fatalf("stale branch produced false suspected deletions: %+v", diff.DeletedEntities)
	}
	if state.MainHeadAtLastSync == "" {
		t.Fatal("recovered stale transaction has no begin-head baseline")
	}
	// The transaction is not falsely wedged: main advanced (B) while the tx only
	// modified A, so a Refresh re-baselines cleanly (no conflicting UUID) instead
	// of ABORTING on a false suspected deletion.
	if _, err := restarted.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("recovered stale transaction failed to refresh (wedged): %v", err)
	}
}
