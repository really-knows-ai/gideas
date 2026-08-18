package gitstore

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// ============================================================================
// T6: FastForwardMerge
// ============================================================================

func TestFastForwardMerge(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)

		// Create entity A on main and commit
		eA := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: eA, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "main entity"); err != nil {
			return err
		}

		// Create branch and add entity B, commit
		txID := validUUID(t)
		if err := gs.CreateBranch(ctx(), txID); err != nil {
			return err
		}
		if err := gs.Checkout(ctx(), txID); err != nil {
			return err
		}

		eB := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: eB, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "branch entity"); err != nil {
			return err
		}

		// Merge into main
		if err := gs.FastForwardMerge(ctx(), txID, "main"); err != nil {
			return err
		}

		// After merge, working tree is on main — both entities should exist
		if _, err := gs.fs.Stat("entities/Component/" + eA + ".json"); err != nil {
			return fmt.Errorf("entity A should exist after merge: %w", err)
		}
		if _, err := gs.fs.Stat("entities/Component/" + eB + ".json"); err != nil {
			return fmt.Errorf("entity B should exist after merge: %w", err)
		}

		// Branch should still exist (merge does not delete source)
		exists, err := gs.BranchExists(ctx(), txID)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("source branch should still exist after merge")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("FastForwardMerge: %v", err)
	}
}

func TestFastForwardMergeDiverged(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)

		// Entity A on main
		eA := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: eA, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "main: A"); err != nil {
			return err
		}

		// Create branch, add entity B
		txID := validUUID(t)
		if err := gs.CreateBranch(ctx(), txID); err != nil {
			return err
		}
		if err := gs.Checkout(ctx(), txID); err != nil {
			return err
		}
		eB := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: eB, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "branch: B"); err != nil {
			return err
		}

		// Go back to main and add entity C (diverge)
		if err := gs.RestoreMain(ctx()); err != nil {
			return err
		}
		eC := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: eC, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "main: C"); err != nil {
			return err
		}

		// Attempt merge — should diverge
		mergeErr := gs.FastForwardMerge(ctx(), txID, "main")
		if mergeErr == nil {
			return fmt.Errorf("expected ErrMergeDiverged")
		}
		if !errors.Is(mergeErr, ErrMergeDiverged) {
			return fmt.Errorf("expected ErrMergeDiverged, got %v", mergeErr)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("FastForwardMergeDiverged: %v", err)
	}
}

func TestFastForwardMergeEmptyBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txID := validUUID(t)

		// Create branch without any commits (same HEAD as main)
		if err := gs.CreateBranch(ctx(), txID); err != nil {
			return err
		}

		// Merge — should be a no-op (already up-to-date)
		if err := gs.FastForwardMerge(ctx(), txID, "main"); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		t.Fatalf("FastForwardMergeEmptyBranch: %v", err)
	}
}

func TestFastForwardMergeNonDefaultInto(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)

		// Create branch A with entity
		branchA := validUUID(t)
		if err := gs.CreateBranch(ctx(), branchA); err != nil {
			return err
		}
		if err := gs.Checkout(ctx(), branchA); err != nil {
			return err
		}
		eA := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: eA, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "branch A"); err != nil {
			return err
		}

		// Create branch B, then pin it to branch A's tip (CreateBranch now
		// branches from main per SPEC Hydration step 1, so the chain is
		// rebuilt explicitly) with another entity.
		branchB := validUUID(t)
		if err := gs.CreateBranch(ctx(), branchB); err != nil {
			return err
		}
		branchAHash, err := gs.BranchHEAD(ctx(), branchA)
		if err != nil {
			return err
		}
		if err := gs.SetBranchRef(ctx(), branchB, branchAHash); err != nil {
			return err
		}
		if err := gs.Checkout(ctx(), branchB); err != nil {
			return err
		}
		eB := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: eB, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "branch B"); err != nil {
			return err
		}

		// Merge B into A (non-default into)
		if err := gs.FastForwardMerge(ctx(), branchB, branchA); err != nil {
			return err
		}

		// Verify both entities exist under A
		if _, err := gs.fs.Stat("entities/Component/" + eA + ".json"); err != nil {
			return fmt.Errorf("entity A should exist: %w", err)
		}
		if _, err := gs.fs.Stat("entities/Component/" + eB + ".json"); err != nil {
			return fmt.Errorf("entity B should exist after merge: %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("FastForwardMergeNonDefaultInto: %v", err)
	}
}

// TestFastForwardMergeBranchNotFound tests the ErrBranchNotFound paths
// in FastForwardMerge.
func TestFastForwardMergeBranchNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Non-existent source branch
		err := gs.FastForwardMerge(ctx(), "nonexistent-source", "main")
		if !errors.Is(err, ErrBranchNotFound) {
			return fmt.Errorf("expected ErrBranchNotFound for source, got %v", err)
		}

		// Non-existent target
		txID := validUUID(t)
		if err := gs.CreateBranch(ctx(), txID); err != nil {
			return err
		}
		err = gs.FastForwardMerge(ctx(), txID, "nonexistent-into")
		if !errors.Is(err, ErrBranchNotFound) {
			return fmt.Errorf("expected ErrBranchNotFound for into, got %v", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("TestFastForwardMergeBranchNotFound: %v", err)
	}
}
