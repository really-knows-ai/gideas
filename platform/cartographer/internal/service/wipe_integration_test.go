package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// TestWipeGraph_TreeOnTxBranchWipeLandsOnMain pins the wipe-on-main invariant
// when the git working tree is on a transaction branch: a failed commit leaves
// the tree checked out on the transaction branch
// (reconcileFailedCommitGitLocked), and an expired-but-not-yet-garbage-collected
// transaction is not considered active by HasActive (SPEC R2 error-table row
// "WipeGraph called while open transactions exist": "A timed-out transaction
// (deadline passed) is not considered open for this guard"). A wipe issued in
// that grace window must restore main before the git rm/commit so the deletion
// lands on main's history — otherwise the next sync-cycle RestoreMain brings
// the pre-wipe files back and stale data silently survives the wipe.
func TestWipeGraph_TreeOnTxBranchWipeLandsOnMain(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: wipe lands on main (real git)")
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
	failMerge := false
	failingGit := &fakeGitStore{GitStore: gs, onFastForwardMerge: func(ctx context.Context, branch, into string) error {
		if failMerge {
			failMerge = false
			return fmt.Errorf("simulated merge failure")
		}
		return gs.FastForwardMerge(ctx, branch, into)
	}}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, failingGit, opPub, initTestKey(), nil, "", 30*time.Second,
		"test-ns", 1*time.Minute, 100000, WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	// Replace the tx manager with a fake clock so the transaction can be
	// expired deterministically without running the GC loop.
	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(7*24*time.Hour, 100000)
	srv.txManager.clock = fc
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	// Establish a non-empty main: a committed entity whose pre-wipe file must
	// not survive a wipe that lands on the transaction branch.
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "pre-wipe"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("first CommitTransaction: %v", err)
	}
	// A second transaction whose commit fails at the fast-forward merge leaves
	// the working tree checked out on the transaction branch with its commit
	// recorded (reconcileFailedCommitGitLocked).
	failMerge = true
	begin2, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "stale"},
		TransactionId: begin2.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin2.TransactionId,
	}); err == nil {
		t.Fatal("expected the simulated merge failure")
	}
	// Expire the transaction without GC: it is still registered (the tree is
	// still on its branch) but no longer blocks the wipe guard.
	fc.Advance(2 * time.Minute)
	if srv.txManager.HasActive() {
		t.Fatal("expired transaction must not be reported as active")
	}
	if _, err = srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{}); err != nil {
		t.Fatalf("WipeGraph: %v", err)
	}
	// The wipe must have landed on main: restore main (as the next sync cycle
	// does) and assert the pre-wipe files do not survive.
	if err := gs.WithGitLock(func() error {
		if err := gs.RestoreMain(ctx); err != nil {
			return err
		}
		if err := gs.CleanUntracked(ctx); err != nil {
			return err
		}
		types, err := gs.ListEntityTypes(ctx)
		if err != nil {
			return err
		}
		if len(types) != 0 {
			return fmt.Errorf("entity files survived the wipe on main: %v", types)
		}
		edgeTypes, err := gs.ListEdgeTypes(ctx)
		if err != nil {
			return err
		}
		if len(edgeTypes) != 0 {
			return fmt.Errorf("edge files survived the wipe on main: %v", edgeTypes)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestWipeGraph_RemovesUntrackedResidualFiles pins the SPEC R2 WipeGraph
// sentence "performs a git clean -fd on the working tree to remove any
// untracked residual files" (SPEC:207): a wipe must remove a file planted in
// the git working tree so a subsequent re-hydration on restart does not
// encounter stale files from removed types. The gitstore primitive is pinned
// by TestCleanUntracked (gitstore_test.go) and the failure branch by
// TestWipeGraph_GitSideMidWipeFailure; this test seeds an untracked residual
// file and asserts the WipeGraph-level wipe removes it.
func TestWipeGraph_RemovesUntrackedResidualFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + wipe lands on main")
	}
	ctx := context.Background()
	dataPath := t.TempDir()
	st, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()

	// Plant an untracked residual file inside the tracked entities directory of
	// the git working tree (graph-repo/entities/.gitkeep is tracked, so a new
	// file there is untracked and must be removed by git clean -fd).
	residual := filepath.Join(dataPath, "graph-repo", "entities", "stale-residual.json")
	if err := os.MkdirAll(filepath.Dir(residual), 0o755); err != nil {
		t.Fatalf("mkdir residual dir: %v", err)
	}
	if err := os.WriteFile(residual, []byte(`{"stale":true}`), 0o600); err != nil {
		t.Fatalf("plant residual file: %v", err)
	}

	if _, err := srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{}); err != nil {
		t.Fatalf("WipeGraph: %v", err)
	}
	if _, err := os.Stat(residual); !os.IsNotExist(err) {
		t.Fatalf("untracked residual file survived the wipe: %v", err)
	}
}
