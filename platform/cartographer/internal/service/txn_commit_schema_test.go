package service

import (
	"context"
	"errors"
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

func TestCommitTransaction_Divergence(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	// Begin a transaction.
	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	// Add a change so we're not zero-mutation.
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "test"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	// Corrupt MainHeadAtLastSync to simulate main having advanced.
	state, _ := srv.txManager.Lookup(txID)
	state.MainHeadAtLastSync = "0000000000000000000000000000000000000000"

	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: txID})
	if err == nil {
		t.Fatal("expected error for divergence, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", status.Code(err))
	}
}

// TestCommitTransaction_EmptyBaselineStaleMainFailsPrecondition pins the
// commit divergence check's fail-closed behavior when no baseline is recorded
// (state.MainHeadAtLastSync == ""): a stale-branch commit must surface step
// 5's FAILED_PRECONDITION ("Commit not up-to-date with main", SPEC:980) — not
// the step-10 INTERNAL merge failure — so the empty-baseline corner cannot
// silently skip the serialisation guard.
func TestCommitTransaction_EmptyBaselineStaleMainFailsPrecondition(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	// Begin a transaction.
	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	// Add a change so we're not zero-mutation.
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "test"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	// Drop the recorded baseline to exercise the no-baseline corner, then
	// advance main so the commit is genuinely stale.
	state, _ := srv.txManager.Lookup(txID)
	state.MainHeadAtLastSync = ""
	commitGitEntity(ctx, t, srv.gitstore, testMutationEntityID, "main")

	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: txID})
	if err == nil {
		t.Fatal("expected error for stale-branch commit, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition for empty-baseline stale commit, got %v (%v)", status.Code(err), err)
	}
}

// TestCommitTransaction_AdditiveSchemaPushDoesNotBlockCommit pins the SPEC R9
// commit flow step 1 semantics: a schema push that is additive (new types, new
// properties, rule modifications — SPEC R2/R6 non-destructive) does not make the
// branch DB state incompatible with the current schema, so an in-flight
// transaction commits normally. The previous full-schema-hash equality check
// rejected any schema change — and, because RefreshTransaction never refreshed
// the begin-time hash, permanently wedged the transaction in
// FAILED_PRECONDITION, forcing a rollback.
func TestCommitTransaction_AdditiveSchemaPushDoesNotBlockCommit(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	// Begin a transaction.
	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	// Add a change so we're not zero-mutation.
	created, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "test"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	// Push an additive schema between begin and commit: a new property on an
	// existing type, a new entity type, and rule/edge declarations.
	alteredSchema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string", Required: true},
					{Name: "version", Type: "string"},
				},
			},
			{
				Name: "Service",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string", Required: true},
				},
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}},
				},
			},
			{Name: "NewType", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "weight", Type: "string"}}},
		},
	}
	if err := srv.store.ApplySchema(ctx, alteredSchema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: txID}); err != nil {
		t.Fatalf("commit after additive schema push should succeed: %v", err)
	}
	if _, err := st.GetEntity(ctx, created.EntityId, "main"); err != nil {
		t.Fatalf("committed entity missing from main after additive schema push: %v", err)
	}
}

func TestCommitTransaction_IncompatibleSchemaBlocksCommit(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "test"},
		TransactionId: txID,
	}); err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	// From here on the store reports an incompatible branch schema, exercising
	// the service's mapping of ErrDestructiveSchemaChange to the SPEC
	// FAILED_PRECONDITION row.
	srv.store = &fakeStore{Store: srv.store, onCheckBranchSchemaCompatibility: func(context.Context, string) error {
		return fmt.Errorf("%w: simulated incompatible schema", store.ErrDestructiveSchemaChange)
	}}

	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: txID})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "schema changed incompatibly") {
		t.Fatalf("expected FailedPrecondition schema-incompatible error, got %v", err)
	}
}

// TestCommitTransaction_MissingTxCapability pins SPEC R3 (SPEC:244): a caller
// without WRITE:graph/tx is denied CommitTransaction with PERMISSION_DENIED
// before any transaction lookup or validation.
func TestCommitTransaction_MissingTxCapability(t *testing.T) {
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

	_, err := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: testMutationEntityID,
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing WRITE:graph/tx capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

// TestCommitTransaction_RecoveredPartialCommitRehydratesAfterStartupRebuild
// pins the CommitTransaction cross-restart retry (SPEC serialisation flow
// retry contract): a crash between main's re-hydration and the fast-forward
// merge, followed by the unconditional startup rebuild
// (rehydrateMainAfterRecovery, cmd/main.go) which re-hydrates main.lbug from
// git main's pre-transaction tree, must not leave the retried commit skipping
// re-hydration. Recovery must clear the durable CommitHydrated flag so the
// retried commit re-hydrates from the transaction branch and main.lbug
// converges with git main.
func TestCommitTransaction_RecoveredPartialCommitRehydratesAfterStartupRebuild(t *testing.T) {
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
	// The first commit's fast-forward merge fails, simulating the crash
	// window between main's re-hydration and the merge.
	failingGit := failOnceMerge(gs)
	srv := NewCartographerServer(
		st, failingGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, st)
	// Git main is non-empty, so the startup rebuild runs on restart.
	mainEntity, err := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "main"}, nil, "main")
	if err != nil {
		t.Fatalf("create main entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, mainEntity.Id, "main")

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
	// First commit: the re-hydration completes (CommitHydrated=true persists)
	// but the merge fails — the crash window.
	if _, err := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err == nil {
		t.Fatal("expected merge failure")
	}

	// Simulate restart.
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
	// Simulate the unconditional startup rebuild (rehydrateMainAfterRecovery):
	// restore the working tree to main, then re-hydrate main.lbug from git
	// main — which does NOT contain the un-merged transaction commit.
	if err := reopenedGit.WithGitLock(func() error {
		if err := reopenedGit.RestoreMain(ctx); err != nil {
			return err
		}
		return reopenedGit.CleanUntracked(ctx)
	}); err != nil {
		t.Fatalf("restore main before rebuild: %v", err)
	}
	entitiesDir, edgesDir := reopenedGit.HydrationDirs()
	if err := reopened.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		t.Fatalf("simulate startup rebuild: %v", err)
	}
	// The rebuild left main.lbug pre-transaction.
	if _, err := reopened.GetEntity(ctx, created.EntityId, "main"); !errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("main.lbug should serve pre-transaction data after the rebuild, got err=%v", err)
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
	if err != nil || state.CommitHydrated {
		t.Fatalf("recovered partial commit must not carry CommitHydrated: state=%+v err=%v", state, err)
	}
	// Retry the commit: it must re-hydrate main from the transaction branch's
	// files (CommitHydrated was cleared) so main.lbug converges with git main.
	if _, err := restarted.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("retry CommitTransaction: %v", err)
	}
	if _, err := reopened.GetEntity(ctx, created.EntityId, "main"); err != nil {
		t.Fatalf("committed transaction entity missing from main.lbug after restart-retry: %v", err)
	}
}

// TestCommitTransaction_MergeCompletedAckWaitsForPush pins the MergeCompleted
// retry path's WithAck contract (SPEC R10, SPEC:630-634): an acked commit
// retried after the merge landed must wake the sync worker and block until the
// push is delivered — it must not return success while the push flag is still
// set.
func TestCommitTransaction_MergeCompletedAckWaitsForPush(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git sync worker")
	}
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{
		GitStore:    gs,
		pushEntered: make(chan struct{}),
		pushRelease: make(chan struct{}),
	}
	t.Cleanup(syncGit.releasePush)
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	// Fail only the "persist completed merge" state write so the first commit
	// leaves MergeCompleted=true in memory with the git merge already landed.
	failingStore := newTxStateFailingStore(base, func(state store.BranchTransactionState) bool {
		return state.MergeCompleted
	})
	fc := newFakeClock(time.Now())
	sw := NewSyncWorker("https://example.com/repo.git", syncGit, failingStore, fc)
	go sw.Run()
	t.Cleanup(sw.Stop)
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		failingStore, syncGit, opPub, initTestKey(), nil, "https://example.com/repo.git",
		30*time.Second, "test-ns", 30*time.Minute, 100000, WithLadybugPath(ladybugPath), WithSyncWorker(sw),
	)
	srv.MarkDBReady()
	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "merged"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	// First attempt: the fast-forward merge lands on main, but persisting
	// MergeCompleted fails, so CommitTransaction returns before the normal-path
	// push wiring.
	if _, err := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err == nil || !strings.Contains(err.Error(), "state write failure") {
		t.Fatalf("CommitTransaction error=%v", err)
	}
	state, err := srv.txManager.Lookup(begin.TransactionId)
	if err != nil || !state.MergeCompleted {
		t.Fatalf("MergeCompleted not retained: state=%+v err=%v", state, err)
	}
	// Retry with Ack: the MergeCompleted path must wake the worker and block
	// until the sync cycle delivers the push.
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
		t.Fatal("acked MergeCompleted commit never reached the sync worker's push")
	}
	select {
	case err := <-commitDone:
		t.Fatalf("MergeCompleted commit returned before the push was delivered: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	syncGit.releasePush()
	if err := <-commitDone; err != nil {
		t.Fatalf("MergeCompleted acked commit returned error after the push was delivered: %v", err)
	}
	syncGit.mu.Lock()
	pushCalls := syncGit.pushCalls
	syncGit.mu.Unlock()
	if pushCalls != 1 {
		t.Fatalf("expected exactly 1 push for the acked MergeCompleted commit, got %d", pushCalls)
	}
	if srv.syncWorker.pushNeeded() {
		t.Fatal("push flag not cleared after the acked push")
	}
}
