package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
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
