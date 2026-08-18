package ladybug

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/store"
)

func TestBranchTransactionState_InMemoryLifecycle(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	if err := s.CreateBranchDB(context.Background(), "tx-state"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if _, err := s.LoadBranchTransactionState(context.Background(), "tx-state"); err != nil &&
		!errors.Is(err, store.ErrBranchStateMissing) {
		t.Fatalf("unregistered branch state: expected ErrBranchStateMissing, got %v", err)
	}
	want := store.BranchTransactionState{
		MainHeadAtLastSync: "head", SchemaHash: "schema", AppliedTimeout: 5 * time.Minute,
		RollbackOnly: true,
	}
	if err := s.SaveBranchTransactionState(context.Background(), "tx-state", want); err != nil {
		t.Fatalf("SaveBranchTransactionState: %v", err)
	}
	got, err := s.LoadBranchTransactionState(context.Background(), "tx-state")
	if err != nil || got != want {
		t.Fatalf("loaded branch state: got=%+v want=%+v err=%v", got, want, err)
	}
	if err := s.DropBranchDB(context.Background(), "tx-state"); err != nil {
		t.Fatalf("DropBranchDB: %v", err)
	}
	if _, err = s.LoadBranchTransactionState(context.Background(), "tx-state"); err != nil &&
		!errors.Is(err, store.ErrBranchStateMissing) {
		t.Fatalf("dropped branch state: expected ErrBranchStateMissing, got %v", err)
	}
}

func TestBranchTransactionState_MissingRecordFailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	path := t.TempDir()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBranchDB(context.Background(), "tx-state"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.SaveBranchTransactionState(context.Background(), "tx-state", store.BranchTransactionState{
		MainHeadAtLastSync: "head", SchemaHash: "schema", AppliedTimeout: 5 * time.Minute,
	}); err != nil {
		t.Fatalf("SaveBranchTransactionState: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := os.Remove(filepath.Join(path, "branches", "tx-state.state.json")); err != nil {
		t.Fatalf("remove branch marker: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer closeStore(t, reopened)
	if _, err := reopened.LoadBranchTransactionState(context.Background(), "tx-state"); err != nil &&
		!errors.Is(err, store.ErrBranchStateMissing) {
		t.Fatalf("missing branch state marker: expected ErrBranchStateMissing, got %v", err)
	}
}

func TestBranchTransactionState_PersistsAndRejectsCorruption(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	path := t.TempDir()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBranchDB(context.Background(), "tx-state"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	want := store.BranchTransactionState{
		MainHeadAtLastSync: "original-head", SchemaHash: "original-schema",
		AppliedTimeout: 5 * time.Minute,
		CommitStarted:  true, CommitCreated: true, CommitHydrated: true,
		MainRehydrated: true, RollbackOnly: true,
	}
	if err := s.SaveBranchTransactionState(context.Background(), "tx-state", want); err != nil {
		t.Fatalf("SaveBranchTransactionState: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open after marker: %v", err)
	}
	got, err := reopened.LoadBranchTransactionState(context.Background(), "tx-state")
	if err != nil || got != want {
		t.Fatalf("persisted state: got=%+v want=%+v err=%v", got, want, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close reopened: %v", err)
	}
	markerPath := filepath.Join(path, "branches", "tx-state.state.json")
	if err := os.WriteFile(markerPath, []byte("not-json"), 0600); err != nil {
		t.Fatalf("corrupt marker: %v", err)
	}
	corrupt, err := Open(path)
	if err != nil {
		t.Fatalf("Open corrupt marker store: %v", err)
	}
	defer closeStore(t, corrupt)
	if _, err := corrupt.LoadBranchTransactionState(context.Background(), "tx-state"); err == nil {
		t.Fatal("corrupt rollback-only marker was accepted")
	}
}

// branchLocked and LoadBranchTransactionState build filesystem paths from txID;
// a non-UUID branch string containing path separators would escape branches/ on
// a file-backed store (path traversal on read). Every other branch-path builder
// (CreateBranchDB, DropBranchDB, SaveBranchTransactionState) enforces
// filepath.Base(txID) == txID; these two read paths must too — defense in depth,
// since a future caller could skip the service-layer UUID-v4 gate.
func TestBranchReadPaths_RejectPathTraversalTxID(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	db := s.(*ladybugDB)
	ctx := context.Background()

	// Plant files at the escaped paths the traversal would touch:
	// filepath.Join(dir, "branches", "../escaped.lbug") resolves to
	// dir/escaped.lbug, and the .state.json variant to dir/escaped.state.json.
	// They must never be opened/read.
	if err := os.WriteFile(filepath.Join(dir, "escaped.lbug"), []byte("not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "escaped.state.json"), []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}

	for _, txID := range []string{"../escaped", ".", ".."} {
		db.mu.Lock()
		_, err = db.branchLocked(txID)
		db.mu.Unlock()
		if err == nil || !strings.Contains(err.Error(), "invalid branch ID") {
			t.Fatalf("branchLocked(%q): expected invalid-branch-ID rejection, got %v", txID, err)
		}

		_, err = s.LoadBranchTransactionState(ctx, txID)
		if err == nil || !strings.Contains(err.Error(), "invalid branch ID") {
			t.Fatalf("LoadBranchTransactionState(%q): expected invalid-branch-ID rejection, got %v", txID, err)
		}
	}
}

// InvalidateBranchState removes filesystem paths built from txID via os.Remove;
// a non-UUID branch string containing path separators would escape branches/ on
// a file-backed store (path traversal on delete). Every sibling path builder
// (CreateBranchDB, DropBranchDB, SaveBranchTransactionState,
// LoadBranchTransactionState, branchLocked) enforces filepath.Base(txID) == txID;
// the destructive remove path must too — defense in depth, since a future caller
// could skip the service-layer UUID-v4 gate.
func TestInvalidateBranchState_RejectPathTraversalTxID(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	// Plant a file at the escaped path the traversal would delete:
	// filepath.Join(dir, "branches", "../escaped.state.json") resolves to
	// dir/escaped.state.json. It must never be removed.
	escapedPath := filepath.Join(dir, "escaped.state.json")
	if err := os.WriteFile(escapedPath, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}

	for _, txID := range []string{"../escaped", ".", ".."} {
		err := s.InvalidateBranchState(ctx, txID)
		if err == nil || !strings.Contains(err.Error(), "invalid branch ID") {
			t.Fatalf("InvalidateBranchState(%q): expected invalid-branch-ID rejection, got %v", txID, err)
		}
		if _, statErr := os.Stat(escapedPath); statErr != nil {
			t.Fatalf("InvalidateBranchState(%q): escaped file %q was removed", txID, escapedPath)
		}
	}

	// Happy path: a legitimately-named state file is removed and the in-memory
	// record invalidated.
	const txID = "legit-tx"
	if err := s.CreateBranchDB(ctx, txID); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.SaveBranchTransactionState(ctx, txID, store.BranchTransactionState{
		MainHeadAtLastSync: "head", SchemaHash: "schema",
	}); err != nil {
		t.Fatalf("SaveBranchTransactionState: %v", err)
	}
	statePath := filepath.Join(dir, "branches", txID+".state.json")
	if _, statErr := os.Stat(statePath); statErr != nil {
		t.Fatalf("state file %q was not written: %v", statePath, statErr)
	}
	if err := s.InvalidateBranchState(ctx, txID); err != nil {
		t.Fatalf("InvalidateBranchState(%q): %v", txID, err)
	}
	if _, statErr := os.Stat(statePath); !os.IsNotExist(statErr) {
		t.Fatalf("state file %q was not removed (stat err: %v)", statePath, statErr)
	}
	if _, err := s.LoadBranchTransactionState(ctx, txID); err == nil {
		t.Fatal("invalidated branch state was accepted")
	}
}

// Closed/failed stores must surface ErrDatabaseNotReady from the branch-state
// entry points rather than serving stale in-memory state or mutating the state
// file (learnings rule: a store primitive must fail loudly on a closed/failed
// store, never silently return state or perform I/O).
func TestBranchState_ClosedOrFailedStoreReturnsNotReady(t *testing.T) {
	t.Run("closed store", func(t *testing.T) {
		s, err := openInMemory()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if _, err := s.LoadBranchTransactionState(ctx, "tx-state"); !errors.Is(err, store.ErrDatabaseNotReady) {
			t.Fatalf("LoadBranchTransactionState on closed store: expected ErrDatabaseNotReady, got %v", err)
		}
		if err := s.InvalidateBranchState(ctx, "tx-state"); !errors.Is(err, store.ErrDatabaseNotReady) {
			t.Fatalf("InvalidateBranchState on closed store: expected ErrDatabaseNotReady, got %v", err)
		}
	})
	t.Run("failed store", func(t *testing.T) {
		s, err := openInMemory()
		if err != nil {
			t.Fatal(err)
		}
		defer closeStore(t, s)
		db := s.(*ladybugDB)
		db.failed = true
		ctx := context.Background()
		if _, err := s.LoadBranchTransactionState(ctx, "tx-state"); !errors.Is(err, store.ErrDatabaseNotReady) {
			t.Fatalf("LoadBranchTransactionState on failed store: expected ErrDatabaseNotReady, got %v", err)
		}
		if err := s.InvalidateBranchState(ctx, "tx-state"); !errors.Is(err, store.ErrDatabaseNotReady) {
			t.Fatalf("InvalidateBranchState on failed store: expected ErrDatabaseNotReady, got %v", err)
		}
	})
}
