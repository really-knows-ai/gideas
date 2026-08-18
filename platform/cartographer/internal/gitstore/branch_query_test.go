package gitstore

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestBranchExists(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txID := validUUID(t)

		// Non-existent
		exists, err := gs.BranchExists(ctx(), txID)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("expected false for non-existent")
		}

		// Create and check
		if err := gs.CreateBranch(ctx(), txID); err != nil {
			return err
		}
		exists, err = gs.BranchExists(ctx(), txID)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("expected true for existing")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("BranchExists: %v", err)
	}
}

func TestBranchHEAD(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txID := validUUID(t)
		if err := gs.CreateBranch(ctx(), txID); err != nil {
			return err
		}

		hash, err := gs.BranchHEAD(ctx(), txID)
		if err != nil {
			return err
		}
		if len(hash) != 40 {
			return fmt.Errorf("expected 40-char hash, got %d", len(hash))
		}

		// Non-existent branch
		_, err = gs.BranchHEAD(ctx(), "nonexistent")
		if !errors.Is(err, ErrBranchNotFound) {
			return fmt.Errorf("expected ErrBranchNotFound, got %v", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("BranchHEAD: %v", err)
	}
}

func TestBranchHEADDivergenceCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)

		// Get initial main HEAD
		hash1, err := gs.BranchHEAD(ctx(), "main")
		if err != nil {
			return err
		}

		// Make a commit on main
		e1ID := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: e1ID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "test commit"); err != nil {
			return err
		}

		// Get new HEAD
		hash2, err := gs.BranchHEAD(ctx(), "main")
		if err != nil {
			return err
		}

		if hash1 == hash2 {
			return fmt.Errorf("expected different hashes after commit, got same: %s", hash1)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("BranchHEADDivergenceCheck: %v", err)
	}
}

func TestSetBranchRef(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txID := validUUID(t)
		if err := gs.CreateBranch(ctx(), txID); err != nil {
			return err
		}

		// Get main HEAD
		mainHash, err := gs.BranchHEAD(ctx(), "main")
		if err != nil {
			return err
		}

		// Move branch ref to main HEAD
		if err := gs.SetBranchRef(ctx(), txID, mainHash); err != nil {
			return err
		}

		// Verify branch HEAD matches main
		branchHash, err := gs.BranchHEAD(ctx(), txID)
		if err != nil {
			return err
		}
		if branchHash != mainHash {
			return fmt.Errorf("expected branch HEAD %s, got %s", mainHash, branchHash)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("SetBranchRef: %v", err)
	}
}

func TestSetBranchRefInvalidHash(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		err := gs.SetBranchRef(ctx(), "main", "short")
		if err == nil {
			return fmt.Errorf("expected error for short hash")
		}
		err = gs.SetBranchRef(ctx(), "main", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz")
		if err == nil {
			return fmt.Errorf("expected error for invalid hex")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("SetBranchRefInvalidHash: %v", err)
	}
}

func TestDeleteBranch(t *testing.T) {
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
		if err != nil || !exists {
			return fmt.Errorf("expected branch to exist")
		}

		if err := gs.DeleteBranch(ctx(), txID); err != nil {
			return err
		}

		exists, err = gs.BranchExists(ctx(), txID)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("expected branch to be deleted")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
}

func TestCleanUntracked(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Create untracked file
		if err := gs.fs.MkdirAll("entities/Other", 0755); err != nil {
			return err
		}
		f, err := gs.fs.Create("entities/Other/untracked.json")
		if err != nil {
			return err
		}
		_ = f.Close()

		if err := gs.CleanUntracked(ctx()); err != nil {
			return err
		}

		// Verify untracked file is gone
		if _, err := gs.fs.Stat("entities/Other/untracked.json"); err == nil {
			return fmt.Errorf("untracked file should be removed")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("CleanUntracked: %v", err)
	}
}

func TestListBranches(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		tx1 := validUUID(t)
		tx2 := validUUID(t)

		if err := gs.CreateBranch(ctx(), tx1); err != nil {
			return err
		}
		if err := gs.CreateBranch(ctx(), tx2); err != nil {
			return err
		}

		branches, err := gs.ListBranches(ctx())
		if err != nil {
			return err
		}

		// Should have both branches but NOT main
		if len(branches) != 2 {
			return fmt.Errorf("expected 2 branches, got %d: %v", len(branches), branches)
		}

		found := make(map[string]bool)
		for _, b := range branches {
			if b == "main" {
				return fmt.Errorf("main should not be in ListBranches")
			}
			found[b] = true
		}
		if !found[tx1] || !found[tx2] {
			return fmt.Errorf("missing branches: %v", branches)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
}

// TestBranchHEADNotFound verifies BranchHEAD returns ErrBranchNotFound.
func TestBranchHEADNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		_, err := gs.BranchHEAD(ctx(), "nonexistent")
		if !errors.Is(err, ErrBranchNotFound) {
			return fmt.Errorf("expected ErrBranchNotFound, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestBranchHEADNotFound: %v", err)
	}
}

// TestDeleteBranchNonExistent verifies DeleteBranch handles non-existent
// branches without panicking (may succeed or error depending on storage backend).
func TestDeleteBranchNonExistent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// The in-memory backend removes a missing branch ref without error, so
		// deleting a non-existent branch is a documented no-op here.
		return gs.DeleteBranch(ctx(), "nonexistent")
	})
	if err != nil {
		t.Fatalf("DeleteBranch(nonexistent): %v", err)
	}
}
