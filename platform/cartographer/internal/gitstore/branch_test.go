package gitstore

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
)

// ============================================================================
// T4: Branch operations
// ============================================================================

func TestCreateBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txID := validUUID(t)
		if err := gs.CreateBranch(ctx(), txID); err != nil {
			return err
		}
		exists, err := gs.BranchExists(ctx(), txID)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("expected branch to exist")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
}

func TestCreateBranchInvalidUUID(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		err := gs.CreateBranch(ctx(), "not-a-uuid")
		if err == nil {
			return fmt.Errorf("expected ErrInvalidUUID")
		}
		if !errors.Is(err, ErrInvalidUUID) {
			return fmt.Errorf("expected ErrInvalidUUID, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("CreateBranchInvalidUUID: %v", err)
	}
}

func TestCreateBranchDuplicate(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txID := validUUID(t)
		if err := gs.CreateBranch(ctx(), txID); err != nil {
			return err
		}
		err := gs.CreateBranch(ctx(), txID)
		if err == nil {
			return fmt.Errorf("expected ErrBranchAlreadyExists")
		}
		if !errors.Is(err, ErrBranchAlreadyExists) {
			return fmt.Errorf("expected ErrBranchAlreadyExists, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("CreateBranchDuplicate: %v", err)
	}
}

// TestCreateBranchRefOnlyExisting pins the branch-already-exists contract for
// a branch that exists only as a ref: SetBranchRef writes no config entry, so
// CreateBranch must check the ref itself and refuse to silently overwrite it
// (which would repoint the branch at main and discard its commits).
func TestCreateBranchRefOnlyExisting(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txID := validUUID(t)
		initHash, err := gs.BranchHEAD(ctx(), "main")
		if err != nil {
			return err
		}

		// Advance main so the branch's pinned hash differs from where
		// CreateBranch would repoint it, making the no-overwrite assertion
		// meaningful.
		now := time.Now().UTC().Round(time.Millisecond)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: validUUID(t), Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "main data"); err != nil {
			return err
		}
		mainHash, err := gs.BranchHEAD(ctx(), "main")
		if err != nil {
			return err
		}
		if mainHash == initHash {
			return fmt.Errorf("test setup: main must advance past the init commit")
		}

		// SetBranchRef alone creates a ref with no config entry.
		if err := gs.SetBranchRef(ctx(), txID, initHash); err != nil {
			return err
		}

		// CreateBranch must detect the ref-only branch rather than silently
		// overwriting its ref.
		if err := gs.CreateBranch(ctx(), txID); err == nil {
			return fmt.Errorf("expected ErrBranchAlreadyExists")
		} else if !errors.Is(err, ErrBranchAlreadyExists) {
			return fmt.Errorf("expected ErrBranchAlreadyExists, got %v", err)
		}

		// The existing ref must be untouched.
		head, err := gs.BranchHEAD(ctx(), txID)
		if err != nil {
			return err
		}
		if head != initHash {
			return fmt.Errorf("branch ref overwritten: HEAD = %s, want %s", head, initHash)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("TestCreateBranchRefOnlyExisting: %v", err)
	}
}

// TestCreateBranchFromMainWhenHeadNotOnMain pins SPEC Hydration step 1
// (SPEC:803): CreateBranch must branch from main, not from the current HEAD.
// After an abandoned failed Commit leaves the working tree checked out on a
// transaction branch, a new transaction must not inherit that branch's commits
// — branching from HEAD would leak the abandoned transaction's changes into
// the next transaction via HardResetToBranch, breaking transaction isolation.
func TestCreateBranchFromMainWhenHeadNotOnMain(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)

		// Advance main by one commit so its tip differs from the init commit.
		mainEntity := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: mainEntity, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "main data"); err != nil {
			return err
		}
		mainHash, err := gs.BranchHEAD(ctx(), "main")
		if err != nil {
			return err
		}

		// Simulate an abandoned transaction: branch off main, check it out and
		// commit on it — HEAD is now on the stale transaction branch, whose tip
		// is strictly ahead of main.
		staleTx := validUUID(t)
		if err := gs.CreateBranch(ctx(), staleTx); err != nil {
			return err
		}
		if err := gs.Checkout(ctx(), staleTx); err != nil {
			return err
		}
		staleEntity := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: staleEntity, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "transaction:"+staleTx); err != nil {
			return err
		}
		staleHash, err := gs.BranchHEAD(ctx(), staleTx)
		if err != nil {
			return err
		}
		if staleHash == mainHash {
			return fmt.Errorf("test setup: stale branch tip must differ from main")
		}

		// A new transaction begun while HEAD is on the stale branch must still
		// branch from main (SPEC Hydration step 1).
		newTx := validUUID(t)
		if err := gs.CreateBranch(ctx(), newTx); err != nil {
			return err
		}
		newHash, err := gs.BranchHEAD(ctx(), newTx)
		if err != nil {
			return err
		}
		if newHash != mainHash {
			return fmt.Errorf("new branch HEAD = %s, want main %s (SPEC Hydration step 1)", newHash, mainHash)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("TestCreateBranchFromMainWhenHeadNotOnMain: %v", err)
	}
}

func TestCheckout(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		e1ID := validUUID(t)

		// Write entity on main and commit
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: e1ID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "main entity"); err != nil {
			return err
		}

		// Create and checkout a new branch
		txID := validUUID(t)
		if err := gs.CreateBranch(ctx(), txID); err != nil {
			return err
		}
		if err := gs.Checkout(ctx(), txID); err != nil {
			return err
		}

		// Entity file should exist on branch (same as main)
		if _, err := gs.fs.Stat("entities/Component/" + e1ID + ".json"); err != nil {
			return fmt.Errorf("entity not found on branch: %w", err)
		}

		// Write another entity on branch
		e2ID := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: e2ID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}

		// Switch back to main
		if err := gs.RestoreMain(ctx()); err != nil {
			return err
		}

		// First entity should exist on main
		if _, err := gs.fs.Stat("entities/Component/" + e1ID + ".json"); err != nil {
			return fmt.Errorf("entity e1 not on main: %w", err)
		}

		// Second entity should NOT exist on main
		if _, err := gs.fs.Stat("entities/Component/" + e2ID + ".json"); err == nil {
			return fmt.Errorf("entity e2 should not exist on main")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
}

func TestCheckoutCreateNew(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Checkout non-existent branch — should create from HEAD
		if err := gs.Checkout(ctx(), "new-branch"); err != nil {
			return err
		}
		exists, err := gs.BranchExists(ctx(), "new-branch")
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("expected new branch to exist")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("CheckoutCreateNew: %v", err)
	}
}

func TestHardResetToBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		e1ID := validUUID(t)

		// Write entity on main and commit
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: e1ID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "main entity"); err != nil {
			return err
		}

		// Create branch, modify working tree
		txID := validUUID(t)
		if err := gs.CreateBranch(ctx(), txID); err != nil {
			return err
		}
		if err := gs.Checkout(ctx(), txID); err != nil {
			return err
		}

		// Write a new file
		if err := gs.fs.MkdirAll("entities/Other", 0755); err != nil {
			return err
		}
		f, err := gs.fs.Create("entities/Other/untracked.json")
		if err != nil {
			return err
		}
		_ = f.Close()

		// Hard reset to main
		if err := gs.HardResetToBranch(ctx(), "main"); err != nil {
			return err
		}

		// The entity should still be there (it's committed on main)
		if _, err := gs.fs.Stat("entities/Component/" + e1ID + ".json"); err != nil {
			return fmt.Errorf("entity should exist after reset: %w", err)
		}

		// The untracked file should be gone
		if _, err := gs.fs.Stat("entities/Other/untracked.json"); err == nil {
			return fmt.Errorf("untracked file should be removed")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("HardResetToBranch: %v", err)
	}
}

func TestRestoreMain(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txID := validUUID(t)
		if err := gs.CreateBranch(ctx(), txID); err != nil {
			return err
		}
		if err := gs.Checkout(ctx(), txID); err != nil {
			return err
		}
		if err := gs.RestoreMain(ctx()); err != nil {
			return err
		}
		// Verify HEAD is on main
		ref, err := gs.repo.Reference(plumbing.HEAD, true)
		if err != nil {
			return err
		}
		if ref.Name() != plumbing.ReferenceName("refs/heads/main") {
			return fmt.Errorf("expected HEAD on main, got %s", ref.Name())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RestoreMain: %v", err)
	}
}
