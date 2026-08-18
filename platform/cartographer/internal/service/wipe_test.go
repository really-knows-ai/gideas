package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestWipeGraph_WithOpenTx(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	// Create an active transaction.
	srv.dbReady.Store(true)
	applyTestSchema(ctx, t, srv.store)
	err := srv.store.CreateBranchDB(ctx, "test-tx")
	if err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	_, _ = srv.txManager.Create("test-tx", 5*time.Minute, "head")

	_, err = srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{})
	if err == nil {
		t.Fatal("expected error for WipeGraph with open transactions, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", status.Code(err))
	}
}

// TestWipeGraph_ExpiredTxDoesNotBlock verifies that a transaction whose
// deadline has passed (but which has not yet been garbage-collected) is not
// considered active, so WipeGraph does NOT return FAILED_PRECONDITION
// (SPEC R2 WipeGraph: only transactions that are still open block a wipe).
func TestWipeGraph_ExpiredTxDoesNotBlock(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	srv.dbReady.Store(true)
	applyTestSchema(ctx, t, srv.store)
	err := srv.store.CreateBranchDB(ctx, "test-tx")
	if err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}

	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(7*24*time.Hour, 100000)
	srv.txManager.clock = fc
	_, _ = srv.txManager.Create("test-tx", 1*time.Minute, "head")

	// Advance past the transaction deadline without GC: the transaction is
	// still registered but is no longer active.
	fc.Advance(2 * time.Minute)
	if srv.txManager.HasActive() {
		t.Fatal("expired transaction must not be reported as active")
	}

	_, err = srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{})
	if err != nil {
		t.Fatalf("WipeGraph with only expired transaction should succeed, got %v", err)
	}
}

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
	failingGit := &mergeFailingGitStore{GitStore: gs}
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
	failingGit.failMerge = true
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

func TestWipeGraph_Clean(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	_, err := srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{})
	if err != nil {
		t.Fatalf("WipeGraph on empty graph failed: %v", err)
	}
}

// TestWipeGraph_CommitsDeletionWithMessageWipe pins the SPEC R2 WipeGraph
// contract "commits the deletion with message \"wipe\"" (SPEC:207): the git
// wipe commit must carry the exact "wipe" message. A regression that changed
// or dropped the wipe commit message (or skipped the commit entirely) would
// fail this test.
func TestWipeGraph_CommitsDeletionWithMessageWipe(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()
	ctx := context.Background()
	applyTestSchema(ctx, t, st)
	// Establish a non-empty git main so the wipe has content to delete.
	commitGitEntity(ctx, t, gs, testMutationEntityID, "pre-wipe")

	if _, err := srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{}); err != nil {
		t.Fatalf("WipeGraph: %v", err)
	}
	logs, err := gs.GitLogOneline(ctx, "wipe")
	if err != nil {
		t.Fatalf("GitLogOneline: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected exactly one wipe commit, got %d: %v", len(logs), logs)
	}
	if !strings.Contains(logs[0], "wipe") {
		t.Fatalf("wipe commit does not carry the SPEC \"wipe\" message: %q", logs[0])
	}
}

// TestWipeGraph_SetsPushNeeded pins the SPEC R10 push contract for WipeGraph's
// wipe commit: the wipe is a mutation-making commit on main ("backing up every
// committed change", SPEC R10), so it must set the sync worker's push-needed
// flag. Without the flag the remote backup retains the pre-wipe graph
// indefinitely, and a manual reprovision from the remote (R10 Init clone)
// would resurrect exactly the data the destructive change deleted.
func TestWipeGraph_SetsPushNeeded(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs}
	srv, fc := newSyncServer(t, syncGit)
	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	ctx := context.Background()
	applyTestSchema(ctx, t, srv.store)
	if _, err := srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{}); err != nil {
		t.Fatalf("WipeGraph: %v", err)
	}
	if !srv.syncWorker.pushNeeded() {
		t.Fatal("push flag not set after WipeGraph's wipe commit")
	}
	// The next timer cycle must deliver the push and clear the flag.
	fc.FireTicker()
	waitFor(t, func() bool { return !srv.syncWorker.pushNeeded() }, "push flag cleared after cycle")
	syncGit.mu.Lock()
	pushCalls := syncGit.pushCalls
	syncGit.mu.Unlock()
	if pushCalls != 1 {
		t.Fatalf("expected exactly 1 push after the wipe, got %d", pushCalls)
	}
}

func TestWipeGraph_WaitsForBeginSetupAndSeesRegisteredTransaction(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	blocking := &beginSetupBlockingStore{Store: base, entered: make(chan struct{}), release: make(chan struct{})}
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		blocking, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
	)
	srv.MarkDBReady()
	ctx := testCtx()
	beginDone := make(chan *flowv1.BeginTransactionResponse, 1)
	beginErr := make(chan error, 1)
	go func() {
		response, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
		beginDone <- response
		beginErr <- err
	}()
	<-blocking.entered
	if srv.txAdmission.TryLock() {
		srv.txAdmission.Unlock()
		t.Fatal("WipeGraph admission unexpectedly succeeded during BeginTransaction setup")
	}
	wipeDone := make(chan error, 1)
	wipeStarted := make(chan struct{})
	go func() {
		close(wipeStarted)
		_, err := srv.WipeGraph(context.Background(), &flowv1.WipeGraphRequest{})
		wipeDone <- err
	}()
	<-wipeStarted
	close(blocking.release)
	begin := <-beginDone
	if err := <-beginErr; err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if err := <-wipeDone; status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected WipeGraph to observe registered transaction, got %v", err)
	}
	if _, err := srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RollbackTransaction: %v", err)
	}
}

func TestWipeGraph_MidWipeFailure(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()
	ctx := context.Background()

	// Replace the store with one that fails on WipeAll.
	srv.store = &wipeFailingStore{Store: st}

	// The git operations (withGitLock) will succeed, but WipeAll will fail,
	// triggering the mid-wipe error.
	_, err := srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{})
	if err == nil {
		t.Fatal("expected error for mid-wipe failure, got nil")
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", status.Code(err))
	}
}

// TestWipeGraph_GitSideMidWipeFailure pins SPEC error-table row 940 ("WipeGraph
// mid-wipe failure → INTERNAL") for the git-side wipe steps: git rm entities,
// the "wipe" commit, and clean untracked. These failures were previously
// returned as raw plain errors, which grpc-go converted to codes.Unknown; only
// the store-side failure produced INTERNAL.
func TestWipeGraph_GitSideMidWipeFailure(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	for _, tc := range []struct {
		name      string
		configure func(*wipeFailingGitStore)
	}{
		{"git rm entities", func(g *wipeFailingGitStore) { g.failGitRm = true }},
		{"wipe commit", func(g *wipeFailingGitStore) { g.failCommit = true }},
		{"clean untracked", func(g *wipeFailingGitStore) { g.failClean = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := openTestStore(t)
			t.Cleanup(func() { _ = st.Close() })
			gs, _ := gitstore.New(t.TempDir())
			failingGit := &wipeFailingGitStore{GitStore: gs}
			tc.configure(failingGit)
			srv := NewCartographerServer(st, failingGit, opPub, scPub, nil, "",
				30*time.Second, "test-ns", 30*time.Minute, 100000)
			srv.MarkDBReady()

			_, err := srv.WipeGraph(context.Background(), &flowv1.WipeGraphRequest{})
			if err == nil {
				t.Fatal("expected error for git-side mid-wipe failure, got nil")
			}
			if status.Code(err) != codes.Internal {
				t.Fatalf("expected Internal, got %v", status.Code(err))
			}
		})
	}
}

// TestWipeGraph_StoreSideFailureConvergesLiveStoreToWipedGit pins the SPEC R2
// WipeGraph recovery contract for the window between the git wipe and the
// store-side wipe: the git main is already wiped (wipe commit) before
// WipeSchema runs under the write lock, so if WipeSchema fails (INTERNAL) the
// live main.lbug must not keep serving the pre-wipe graph while git main is
// wiped. WipeGraph converges main.lbug to the (wiped) git state within the RPC
// by re-hydrating from the now-absent git entities/edges dirs — the pre-wipe
// entity is no longer readable from the live store — while still returning
// INTERNAL per SPEC R2 "graph state may be partially cleaned… INTERNAL" (R8
// restart re-hydration remains the fallback if the in-RPC re-hydration also
// fails).
func TestWipeGraph_StoreSideFailureConvergesLiveStoreToWipedGit(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	// The store-side wipe fails on demand; the real store's data is cleared by
	// the in-RPC re-hydration.
	srv := NewCartographerServer(&wipeFailingStore{Store: st}, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()
	ctx := context.Background()

	applyTestSchema(ctx, t, st)
	// Establish a pre-wipe graph in BOTH git main and the live main.lbug.
	commitGitEntity(ctx, t, gs, testMutationEntityID, "pre-wipe")
	if _, err := st.CreateEntity(ctx, "Component", testMutationEntityID,
		map[string]string{"name": "pre-wipe"}, nil, "main"); err != nil {
		t.Fatalf("seed pre-wipe entity into main.lbug: %v", err)
	}
	if _, err := st.GetEntity(ctx, testMutationEntityID, "main"); err != nil {
		t.Fatalf("pre-wipe entity should be present in main.lbug before wipe: %v", err)
	}

	// WipeSchema fails after the git wipe already committed → INTERNAL.
	if _, err := srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{}); err == nil {
		t.Fatal("expected error for store-side mid-wipe failure, got nil")
	} else if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", status.Code(err))
	}

	// The live store must no longer serve the pre-wipe graph: the in-RPC
	// re-hydration from the wiped git cleared it, so the store and git main
	// have converged.
	if _, err := st.GetEntity(ctx, testMutationEntityID, "main"); !errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("pre-wipe entity must be cleared from main.lbug after store-side wipe failure, got err=%v", err)
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
