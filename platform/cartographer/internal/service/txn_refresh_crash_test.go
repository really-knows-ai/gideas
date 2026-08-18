package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestRefreshTransaction_CrashSafeRebuildPreservesBranchDBOnFailure pins the
// RefreshTransaction branch-DB rebuild's crash-safety property: when the
// refresh's re-hydration fails, the existing branch DB — the only durable
// record of the transaction's mutations (SPEC R9 change-log recovery) — must
// survive, and no temporary files may leak.
func TestRefreshTransaction_CrashSafeRebuildPreservesBranchDBOnFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	ctx := testCtx()
	dataPath := t.TempDir()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	// Fail the refresh's branch re-hydration (the 2nd HydrateBranchFromFiles
	// call — the 1st is BeginTransaction's).
	release := make(chan struct{})
	close(release)
	blocking := &hydrationBlockingStore{
		Store: st, blocked: make(chan struct{}), release: release, fail: true,
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		blocking, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, st)
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
	commitGitEntity(ctx, t, gs, testMutationEntityID, "main")

	if _, err := srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err == nil {
		t.Fatal("expected refresh hydration failure")
	}
	// The existing branch DB — the only durable record of the transaction's
	// mutations — must survive the failed rebuild.
	if _, err := os.Stat(filepath.Join(dataPath, "branches", begin.TransactionId+".lbug")); err != nil {
		t.Fatalf("transaction branch DB was destroyed by the failed refresh: %v", err)
	}
	ent, err := st.GetEntity(ctx, created.EntityId, begin.TransactionId)
	if err != nil {
		t.Fatalf("transaction mutation lost after failed refresh: %v", err)
	}
	if ent.Properties["name"] != "tx" {
		t.Fatalf("transaction entity content = %+v, want name=tx", ent.Properties)
	}
	// No leftover temporary branch files from the aborted rebuild. The
	// engine's `<key>.lbug.wal` write-ahead-log artifact is deliberately
	// ignored: the store's DropBranchDB removes only the `.lbug`/`.schema.json`/
	// `.state.json` files (a pre-existing store behavior applying to every
	// branch drop), so the assertion pins that no temporary *branch DB*
	// resources leak.
	entries, err := os.ReadDir(filepath.Join(dataPath, "branches"))
	if err != nil {
		t.Fatalf("read branches dir: %v", err)
	}
	expected := map[string]bool{
		begin.TransactionId + ".lbug":        true,
		begin.TransactionId + ".schema.json": true,
		begin.TransactionId + ".state.json":  true,
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".wal") {
			continue
		}
		if !expected[e.Name()] {
			t.Fatalf("leftover branch file from aborted refresh: %q", e.Name())
		}
	}
}

// TestRefreshTransaction_FileBackedCrashSafeSwap exercises the crash-safe
// rebuild+swap on a file-backed store end to end: the refreshed branch reflects
// main's new state plus the transaction's changes, and the subsequent commit
// converges main.lbug with git main.
func TestRefreshTransaction_FileBackedCrashSafeSwap(t *testing.T) {
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
	t.Cleanup(func() { _ = st.Close() })
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
	txEntity, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	// Main advances while the transaction is open.
	interim, err := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "interim"}, nil, "main")
	if err != nil {
		t.Fatalf("create interim entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, interim.Id, "interim")

	if _, err := srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RefreshTransaction: %v", err)
	}
	// The refreshed branch reflects main's new state plus the transaction's
	// change (all reads go through the swapped-in branch DB).
	for _, probe := range []struct{ id, want string }{
		{mainEntity.Id, "main"}, {interim.Id, "interim"}, {txEntity.EntityId, "tx"},
	} {
		ent, err := st.GetEntity(ctx, probe.id, begin.TransactionId)
		if err != nil {
			t.Fatalf("entity %s missing from refreshed branch: %v", probe.id, err)
		}
		if ent.Properties["name"] != probe.want {
			t.Fatalf("entity %s on branch has name %q, want %q", probe.id, ent.Properties["name"], probe.want)
		}
	}
	// The commit succeeds and main.lbug converges with git main.
	if _, err := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CommitTransaction after file-backed refresh: %v", err)
	}
	for _, probe := range []struct{ id, want string }{
		{mainEntity.Id, "main"}, {interim.Id, "interim"}, {txEntity.EntityId, "tx"},
	} {
		ent, err := st.GetEntity(ctx, probe.id, "main")
		if err != nil {
			t.Fatalf("entity %s missing from main after commit: %v", probe.id, err)
		}
		if ent.Properties["name"] != probe.want {
			t.Fatalf("entity %s on main has name %q, want %q", probe.id, ent.Properties["name"], probe.want)
		}
	}
}

// TestRefreshTransaction_SwapClosesTempBranchBeforeRename pins the
// RefreshTransaction branch-DB swap's crash-window fix (SPEC R9 refresh): the
// old branch's in-memory handle must be evicted and the replacement branch must
// be closed — its write-ahead log checkpointed into its .lbug file — before
// its files are renamed onto the transaction's canonical names. The old swap
// dropped the old branch (removing its files before the rename) and renamed
// <temp>.lbug → <txID>.lbug while the temp connection was still open; a crash
// in those windows left the durable branch DB absent or missing rows still held
// in the orphaned <temp>.lbug.wal, and RecoverOpenTransactions rolled the
// transaction back or classified the absent entities as suspected deletions
// that the recovered commit re-applied to main's committed data. The swap must
// therefore close the old branch (evicting the handle while keeping its files
// until the atomic rename overwrites them), close the temp replacement, then
// release the temp key after the rename.
func TestRefreshTransaction_SwapClosesTempBranchBeforeRename(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	ctx := testCtx()
	dataPath := t.TempDir()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	recording := &swapRecordingStore{Store: st}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		recording, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, st)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	// Main advances while the transaction is open so the refresh re-hydrates.
	interim, err := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "interim"}, nil, "main")
	if err != nil {
		t.Fatalf("create interim entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, interim.Id, "interim")

	if _, err := srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RefreshTransaction: %v", err)
	}

	recording.mu.Lock()
	ops := append([]string(nil), recording.ops...)
	recording.mu.Unlock()
	// The swap must be: close the old tx branch handle, close the temp
	// replacement (checkpointing its WAL), then release the temp key after the
	// rename. A swap that dropped the old branch (deleting its files) before
	// the rename, or that released the temp key without a close, would leave a
	// crash window in which the durable branch DB is absent or the renamed file
	// is missing un-checkpointed rows.
	if len(ops) != 3 {
		t.Fatalf("expected swap ops [close:%s close:<temp> drop:<temp>], got %v", begin.TransactionId, ops)
	}
	if ops[0] != "close:"+begin.TransactionId {
		t.Fatalf("swap must close (evict) the old branch handle first, got %q", ops[0])
	}
	if !strings.HasPrefix(ops[1], "close:") {
		t.Fatalf("swap must close the temp branch before the rename, got %q", ops[1])
	}
	if ops[2] != "drop:"+strings.TrimPrefix(ops[1], "close:") {
		t.Fatalf("swap must release the temp key after the close, got %q (close %q)", ops[2], ops[1])
	}
}

// TestRefreshTransaction_MidRefreshCrashPreservesMutations pins the SPEC R9
// change-log-recovery guarantee across the RefreshTransaction branch-DB swap
// (the swap-to-reapply crash window): the swap re-applies the transaction's
// changes onto the replacement branch before the swap, and evicts the old
// branch handle without deleting its files until the atomic rename installs
// the fully re-applied replacement. A crash at any point in the refresh — here
// simulated by failing the final state persist after the swap — leaves the
// durable branch DB carrying the complete change set and the BranchRefreshInProgress
// marker set, so RecoverOpenTransactions reconstructs the FULL change log
// instead of misclassifying the transaction as already committed and deleting
// it (or, in the partial-reapply sub-window, reconstructing a truncated log).
func TestRefreshTransaction_MidRefreshCrashPreservesMutations(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()

	base, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	failing := &refreshTailPersistFailingStore{Store: base}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	srv := NewCartographerServer(
		failing, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	// BeginTransaction already wrote the durable record (the first txID save);
	// the next two txID saves are the refresh's pre-swap in-progress marker and
	// its final persist. Account for the first so the failure fires on the
	// final persist.
	failing.txID = begin.TransactionId
	failing.txSaves = 1
	first, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx-one"}, TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity tx-one: %v", err)
	}
	second, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx-two"}, TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity tx-two: %v", err)
	}
	// Main advances while the transaction is open, forcing a real refresh.
	mainEntity, err := base.CreateEntity(ctx, "Component", "", map[string]string{"name": "main"}, nil, "main")
	if err != nil {
		t.Fatalf("create main entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, mainEntity.Id, "main")

	// The refresh completes its branch-DB swap (re-applying both transaction
	// changes onto the replacement) and then "crashes" at the final state
	// persist: the durable record keeps the in-progress marker and the
	// swapped-in branch DB carries the full change set.
	if _, err := srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err == nil || !strings.Contains(err.Error(), "simulated crash at refresh tail") {
		t.Fatalf("expected refresh tail persist failure, got %v", err)
	}
	// The durable marker distinguishes this mid-refresh crash from a
	// post-merge crash.
	durable, err := base.LoadBranchTransactionState(ctx, begin.TransactionId)
	if err != nil {
		t.Fatalf("load durable state after refresh crash: %v", err)
	}
	if !durable.BranchRefreshInProgress {
		t.Fatal("mid-refresh crash left no BranchRefreshInProgress marker")
	}
	if err := base.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Restart: the transaction's uncommitted mutations must be recovered, not
	// misclassified as already committed and deleted.
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
		t.Fatalf("mid-refresh-crash transaction was not recovered (deleted?): %v", err)
	}
	if !state.BranchRefreshInProgress {
		t.Fatal("recovered mid-refresh transaction lost its in-progress marker")
	}
	diff, err := restarted.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("get recovered diff: %v", err)
	}
	if len(diff.AddedEntities) != 2 {
		t.Fatalf("expected both transaction mutations recovered, got %+v", diff.AddedEntities)
	}
	got := map[string]bool{}
	for _, e := range diff.AddedEntities {
		got[e.Id] = true
	}
	if !got[first.EntityId] || !got[second.EntityId] {
		t.Fatalf("recovered diff missing transaction mutations: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dataPath, "branches", begin.TransactionId+".lbug")); err != nil {
		t.Fatalf("recovered transaction branch DB missing: %v", err)
	}
}

// TestRefreshTransaction_RehydrationFailureIsInternal pins SPEC error-table
// row "Commit serialisation or re-hydration failed" (INTERNAL, SPEC:987) for
// the RefreshTransaction branch re-hydration path: a refresh whose
// HydrateBranchFromFiles fails with the store's ErrInvalidEntityDir sentinel
// must surface INTERNAL, never the INVALID_ARGUMENT the old mapStoreError
// mapping produced. TestRefreshTransaction_HydrationFailureDoesNotAdvanceSyncHead
// covers the plain-error hydration failure; this test covers the sentinel that
// previously hit the removed ErrInvalidEntityDir/ErrInvalidEdgeDir →
// INVALID_ARGUMENT mappings (errors.go).
func TestRefreshTransaction_RehydrationFailureIsInternal(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()
	base, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	dirErr := &hydrationDirErrorStore{Store: base}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	srv := NewCartographerServer(
		dirErr, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	// Main advances while the transaction is open so the refresh re-hydrates.
	interim, err := base.CreateEntity(ctx, "Component", "", map[string]string{"name": "interim"}, nil, "main")
	if err != nil {
		t.Fatalf("create interim entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, interim.Id, "interim")
	// Arm the failure for the refresh's HydrateBranchFromFiles call (the
	// BeginTransaction call has already completed).
	dirErr.fail = true

	_, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{TransactionId: begin.TransactionId})
	if err == nil {
		t.Fatal("expected branch re-hydration failure during refresh")
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal for branch re-hydration failure, got %v (%v)", status.Code(err), err)
	}
}

// TestRefreshTransaction_MissingTxCapability pins SPEC R3 (SPEC:244): a
// caller without WRITE:graph/tx is denied RefreshTransaction with
// PERMISSION_DENIED before any transaction lookup or validation.
func TestRefreshTransaction_MissingTxCapability(t *testing.T) {
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

	_, err := srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: testMutationEntityID,
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing WRITE:graph/tx capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

// TestStoreCloseBranchDB_CheckpointsDataBeforeFileRename pins the store
// primitive RefreshTransaction's swap relies on: closing a file-backed branch
// must checkpoint its write-ahead log into the .lbug file (so a renamed .lbug
// is complete) and must not delete the persisted branch files. After the
// close, moving the .lbug to a new name without its WAL companion — exactly
// what a crash between the refresh swap's rename and the (old) close did —
// must still yield every row from the file alone.
func TestStoreCloseBranchDB_CheckpointsDataBeforeFileRename(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	dataPath := t.TempDir()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	applyTestSchema(ctx, t, st)
	const txID = "11111111-1111-4111-8111-111111111111"
	if err := st.CreateBranchDB(ctx, txID); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := st.ReplicateSchemaToBranch(ctx, txID); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}
	ent, err := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "kept"}, nil, txID)
	if err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}
	// Close the branch: this must checkpoint the WAL into the .lbug and must
	// not remove the persisted files.
	if err := st.CloseBranchDB(ctx, txID); err != nil {
		t.Fatalf("CloseBranchDB: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataPath, "branches", txID+".lbug")); err != nil {
		t.Fatalf("CloseBranchDB deleted the branch file: %v", err)
	}
	// Simulate the refresh swap's crash: rename the branch's files onto a new
	// name WITHOUT the .lbug.wal WAL companion — the engine's path-based WAL
	// recovery cannot find the orphaned <old>.wal, so only data checkpointed
	// into the .lbug before the rename survives.
	const moved = "22222222-2222-4222-8222-222222222222"
	for _, suffix := range []string{".lbug", ".schema.json", ".state.json"} {
		src := filepath.Join(dataPath, "branches", txID+suffix)
		if _, err := os.Stat(src); err != nil {
			continue // no such file (e.g. no state record saved yet)
		}
		if err := os.Rename(src, filepath.Join(dataPath, "branches", moved+suffix)); err != nil {
			t.Fatalf("rename branch file %s: %v", suffix, err)
		}
	}
	got, err := st.GetEntity(ctx, ent.Id, moved)
	if err != nil {
		t.Fatalf("entity lost after close+rename (WAL not checkpointed): %v", err)
	}
	if got.Properties["name"] != "kept" {
		t.Fatalf("entity content = %+v, want name=kept", got.Properties)
	}
}
