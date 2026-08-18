package ladybug

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
)

// SPEC R9 recovery point 4 rolls back a transaction whose branch .lbug is
// absent; this pins the sibling crash window: a crash between CreateBranchDB
// and ReplicateSchemaToBranch leaves a present-but-empty branch .lbug with no
// schema metadata (ReplicateSchemaToBranch writes branches/<txID>.schema.json
// only after its DDL loop). In that window the branch has no tables and no
// data — the transaction never made a durable change — so the reopen must
// classify it exactly like the absent-.lbug case (ErrBranchNotFound, which
// RecoverOpenTransactions turns into a rollback via cleanupTransaction/
// DropBranchDB) instead of surfacing a hard error that bricks startup. The
// non-empty-catalog sibling — a crash mid-DDL in ReplicateSchemaToBranch
// (TestPersistence_MissingBranchSchemaMetadataRollsBackOnReopen) — is
// classified identically: the client never received the txID, so the partial
// branch is just as harmless.
func TestBranch_EmptyBranchNoMetadataRollsBackOnReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	applyTestSchema(t, s)

	const branch = "tx-crash-window"
	if err := s.CreateBranchDB(ctx, branch); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	// Simulate the crash: ReplicateSchemaToBranch never runs, so the branch
	// schema metadata file is never written and the branch catalog stays empty.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Confirm the mid-sequence crash state on disk: present-but-empty .lbug,
	// absent .schema.json.
	if _, err := os.Stat(filepath.Join(dir, "branches", branch+".lbug")); err != nil {
		t.Fatalf("expected persisted empty branch .lbug: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "branches", branch+".schema.json")); !os.IsNotExist(err) {
		t.Fatalf("expected absent branch schema metadata: %v", err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen main: %v", err)
	}
	defer closeStore(t, reopened)

	if _, err := reopened.DumpAllEntities(ctx, branch); !errors.Is(err, store.ErrBranchNotFound) {
		t.Fatalf("DumpAllEntities = %v, want ErrBranchNotFound (rollback classification)", err)
	}
	// The rollback path (RecoverOpenTransactions' cleanup) drops the branch via
	// DropBranchDB; it must succeed and remove the persisted .lbug.
	if err := reopened.DropBranchDB(ctx, branch); err != nil {
		t.Fatalf("DropBranchDB: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "branches", branch+".lbug")); !os.IsNotExist(err) {
		t.Fatalf("branch .lbug was not removed by rollback: %v", err)
	}
	// After the rollback the branch is fully absent: classification stays
	// ErrBranchNotFound, never a resurrected empty branch.
	if _, err := reopened.DumpAllEntities(ctx, branch); !errors.Is(err, store.ErrBranchNotFound) {
		t.Fatalf("DumpAllEntities after rollback = %v, want ErrBranchNotFound", err)
	}
}

// SPEC R9 recovery point 4 rolls back a transaction whose branch .lbug is
// absent (e.g. PVC corruption); a present-but-corrupt branch .lbug is the same
// loss mechanism and must be classified identically — ErrBranchNotFound, which
// RecoverOpenTransactions turns into a rollback via cleanupTransaction/
// DropBranchDB — instead of surfacing a hard error that wedges startup until a
// human deletes the file (the pre-fix behavior). The classification mirrors
// main's R8 corruption heuristic (corruptionCandidates): only a present,
// OS-readable file the engine cannot open is treated as corruption; an
// unreadable file is an operational failure and must stay a hard error so the
// rollback path never deletes a branch DB that was never corrupt.
func TestBranch_CorruptBranchLbugRollsBackOnReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	t.Run("readable corrupt .lbug rolls back", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		ctx := context.Background()
		applyTestSchema(t, s)

		const branch = "tx-corrupt-lbug"
		if err := s.CreateBranchDB(ctx, branch); err != nil {
			t.Fatalf("CreateBranchDB: %v", err)
		}
		if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
			t.Fatalf("ReplicateSchemaToBranch: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		// Corrupt the persisted branch .lbug in place (PVC corruption).
		path := filepath.Join(dir, "branches", branch+".lbug")
		if err := os.WriteFile(path, []byte("not a ladybug database"), 0600); err != nil {
			t.Fatalf("corrupt branch .lbug: %v", err)
		}
		if !corruptionCandidates(path) {
			t.Fatal("expected corrupt branch .lbug to be classified as a corruption candidate")
		}

		reopened, err := Open(dir)
		if err != nil {
			t.Fatalf("reopen main: %v", err)
		}
		defer closeStore(t, reopened)

		// A corrupt branch .lbug must be classified as ErrBranchNotFound — the
		// recovery path (RecoverOpenTransactions) rolls the transaction back.
		if _, err := reopened.DumpAllEntities(ctx, branch); !errors.Is(err, store.ErrBranchNotFound) {
			t.Fatalf("DumpAllEntities = %v, want ErrBranchNotFound (rollback classification)", err)
		}
		// The rollback path drops the branch via DropBranchDB; it must succeed
		// and remove the corrupt persisted .lbug.
		if err := reopened.DropBranchDB(ctx, branch); err != nil {
			t.Fatalf("DropBranchDB: %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("corrupt branch .lbug was not removed by rollback: %v", err)
		}
		// After the rollback the branch is fully absent: classification stays
		// ErrBranchNotFound, never a resurrected corrupt branch.
		if _, err := reopened.DumpAllEntities(ctx, branch); !errors.Is(err, store.ErrBranchNotFound) {
			t.Fatalf("DumpAllEntities after rollback = %v, want ErrBranchNotFound", err)
		}
	})

	t.Run("unreadable .lbug stays a hard error", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		ctx := context.Background()
		applyTestSchema(t, s)

		const branch = "tx-unreadable-lbug"
		if err := s.CreateBranchDB(ctx, branch); err != nil {
			t.Fatalf("CreateBranchDB: %v", err)
		}
		if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
			t.Fatalf("ReplicateSchemaToBranch: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		path := filepath.Join(dir, "branches", branch+".lbug")
		if err := os.Chmod(path, 0000); err != nil {
			t.Fatalf("chmod 000: %v", err)
		}
		// Not a corruption candidate: the open must fail loudly and the file
		// must be preserved, mirroring main's R8 operational-failure handling.
		if corruptionCandidates(path) {
			t.Fatal("unreadable file must not be a corruption candidate")
		}

		reopened, err := Open(dir)
		if err != nil {
			t.Fatalf("reopen main: %v", err)
		}
		defer closeStore(t, reopened)
		if _, err := reopened.DumpAllEntities(ctx, branch); errors.Is(err, store.ErrBranchNotFound) {
			t.Fatalf("DumpAllEntities = %v, want a hard open failure (not ErrBranchNotFound)", err)
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("unreadable branch .lbug was removed: %v", statErr)
		}
	})
}

// TestCloseBranchDB_CheckpointsDataWithoutRemovingFiles pins the store
// primitive RefreshTransaction's branch-DB swap relies on (SPEC R9): closing
// a file-backed branch must checkpoint its write-ahead log into the .lbug
// file — so the file alone is complete and a crash between the swap's rename
// and the old close cannot lose rows — must not remove the persisted branch
// files (unlike DropBranchDB), must evict the in-memory handle so the next
// read goes through the reopen path, and is idempotent (closing an
// unregistered branch is a no-op). The recovery path is pinned directly:
// after the close, moving the .lbug onto a new name without its WAL companion
// and reading through the branch must still yield every row from the file
// alone, via the same lazy branchLocked reopen a restarted process uses.
func TestCloseBranchDB_CheckpointsDataWithoutRemovingFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	ctx := context.Background()
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer closeStore(t, st)
	applyTestSchema(t, st)

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
	db := st.(*ladybugDB)

	if err := st.CloseBranchDB(ctx, txID); err != nil {
		t.Fatalf("CloseBranchDB: %v", err)
	}
	// The close checkpoints the WAL into the .lbug but must not remove the
	// persisted branch files, and must evict the in-memory handle so every
	// subsequent read reopens from the file (the recovery path).
	if _, err := os.Stat(filepath.Join(dir, "branches", txID+".lbug")); err != nil {
		t.Fatalf("CloseBranchDB deleted the branch file: %v", err)
	}
	if _, ok := db.branches[txID]; ok {
		t.Fatal("CloseBranchDB left the branch registered in memory")
	}
	// Idempotent: closing the now-unregistered branch is a no-op.
	if err := st.CloseBranchDB(ctx, txID); err != nil {
		t.Fatalf("second CloseBranchDB must be a no-op, got: %v", err)
	}

	// Simulate the refresh swap's crash window: rename the branch's files onto
	// a new name WITHOUT the .lbug.wal WAL companion — the engine's path-based
	// WAL recovery cannot find the orphaned <old>.wal, so only data checkpointed
	// into the .lbug before the rename survives. The read below lazily reopens
	// the branch from the file alone (branchLocked), the same reopen a
	// restarted process performs.
	const moved = "22222222-2222-4222-8222-222222222222"
	for _, suffix := range []string{".lbug", ".schema.json", ".state.json"} {
		src := filepath.Join(dir, "branches", txID+suffix)
		if _, err := os.Stat(src); err != nil {
			continue // no such file (e.g. no state record saved yet)
		}
		if err := os.Rename(src, filepath.Join(dir, "branches", moved+suffix)); err != nil {
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

	// Contrast with DropBranchDB, which DOES remove the persisted files: the
	// closed branch stays recoverable from disk while a dropped branch is gone.
	if err := st.DropBranchDB(ctx, moved); err != nil {
		t.Fatalf("DropBranchDB: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "branches", moved+".lbug")); !os.IsNotExist(err) {
		t.Fatalf("DropBranchDB did not remove the branch file: %v", err)
	}
}
