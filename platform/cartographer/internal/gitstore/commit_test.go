package gitstore

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// ============================================================================
// T5: Git operations (AddAll + Commit)
// ============================================================================

func TestAddAllAndCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
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

		// Verify commit exists in log
		log, err := gs.repo.Log(&git.LogOptions{})
		if err != nil {
			return err
		}
		defer log.Close()

		found := false
		if err := log.ForEach(func(c *object.Commit) error {
			if c.Message == "test commit" {
				found = true
				return errStop
			}
			return nil
		}); err != nil && !errors.Is(err, errStop) {
			return err
		}
		if !found {
			return fmt.Errorf("commit not found in log")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("AddAllAndCommit: %v", err)
	}
}

func TestCommitNoAdd(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Commit without adding — empty diff is not an error
		if err := gs.Commit(ctx(), "no changes"); err != nil {
			return fmt.Errorf("expected no error for empty commit, got: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("CommitNoAdd: %v", err)
	}
}

func TestCommitEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// AddAll with no changes then commit
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "empty commit"); err != nil {
			return fmt.Errorf("expected no error for empty commit, got: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("CommitEmpty: %v", err)
	}
}

func TestCommitExistsOnBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)

		// Write and commit with transaction prefix
		e1ID := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: e1ID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "transaction:"+txID); err != nil {
			return err
		}

		found, err := gs.CommitExistsOnBranch(ctx(), txID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("expected commit to exist")
		}

		// Non-existent txID should return false (a valid UUID with no matching commit)
		found, err = gs.CommitExistsOnBranch(ctx(), validUUID(t))
		if err != nil {
			return err
		}
		if found {
			return fmt.Errorf("expected false for non-existent txID")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("CommitExistsOnBranch: %v", err)
	}
}

func TestCommitExistsOnBranchMatchesPrefix(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txID := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{{
			ID: validUUID(t), Type: "Component",
		}}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "transaction:"+txID+"-suffix\nbody"); err != nil {
			return err
		}
		found, err := gs.CommitExistsOnBranch(ctx(), txID)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("prefix-only transaction commit not matched")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("CommitExistsOnBranch prefix match: %v", err)
	}
}

func TestCommitExistsOnBranchScopedToBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txA := validUUID(t)
		txB := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)

		// Create branch A and commit with message "transaction:a"
		if err := gs.CreateBranch(ctx(), txA); err != nil {
			return err
		}
		if err := gs.Checkout(ctx(), txA); err != nil {
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
		if err := gs.Commit(ctx(), "transaction:"+txA); err != nil {
			return err
		}

		// Create branch B and commit with message "transaction:b"
		if err := gs.CreateBranch(ctx(), txB); err != nil {
			return err
		}
		if err := gs.Checkout(ctx(), txB); err != nil {
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
		if err := gs.Commit(ctx(), "transaction:"+txB); err != nil {
			return err
		}

		// Checkout A and verify only A's commit is visible
		if err := gs.Checkout(ctx(), txA); err != nil {
			return err
		}

		foundA, err := gs.CommitExistsOnBranch(ctx(), txA)
		if err != nil {
			return err
		}
		if !foundA {
			return fmt.Errorf("expected commit A to exist on branch A")
		}

		foundB, err := gs.CommitExistsOnBranch(ctx(), txB)
		if err != nil {
			return err
		}
		if foundB {
			return fmt.Errorf("expected commit B to NOT be visible on branch A (isolation)")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("CommitExistsOnBranchScopedToBranch: %v", err)
	}
}

func TestGitRm(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		e1ID := validUUID(t)

		// Write entity and commit
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: e1ID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "pre-rm"); err != nil {
			return err
		}

		// Remove the entity directory
		if err := gs.GitRm(ctx(), "entities/Component"); err != nil {
			return err
		}

		// Verify file is removed from working tree
		if _, err := gs.fs.Stat("entities/Component/" + e1ID + ".json"); err == nil {
			return fmt.Errorf("file should be removed by GitRm")
		}

		// Verify the deletion is staged in the index (wt.Remove stages it)
		status, err := gs.wt.Status()
		if err != nil {
			return err
		}
		if entry, ok := status["entities/Component/"+e1ID+".json"]; !ok || entry.Staging != git.Deleted {
			return fmt.Errorf("expected staged deletion, got %v", entry)
		}

		// AddAll and commit the deletion
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "post-rm"); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		t.Fatalf("GitRm: %v", err)
	}
}

func TestGitRmDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		e1ID := validUUID(t)
		e2ID := validUUID(t)

		// Write entities for two types and commit
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: e1ID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.WriteEntityFiles(ctx(), "Service", []Entity{
			{ID: e2ID, Type: "Service", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "pre-rm"); err != nil {
			return err
		}

		// Remove entire entities directory
		if err := gs.GitRm(ctx(), "entities"); err != nil {
			return err
		}

		// Verify all entity files are removed
		if _, err := gs.fs.Stat("entities/Component/" + e1ID + ".json"); err == nil {
			return fmt.Errorf("Component file should be removed")
		}
		if _, err := gs.fs.Stat("entities/Service/" + e2ID + ".json"); err == nil {
			return fmt.Errorf("Service file should be removed")
		}

		// Verify the deletions are staged in the index
		status, err := gs.wt.Status()
		if err != nil {
			return err
		}
		if entry, ok := status["entities/Component/"+e1ID+".json"]; !ok || entry.Staging != git.Deleted {
			return fmt.Errorf("expected staged deletion for Component file, got %v", entry)
		}
		if entry, ok := status["entities/Service/"+e2ID+".json"]; !ok || entry.Staging != git.Deleted {
			return fmt.Errorf("expected staged deletion for Service file, got %v", entry)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("GitRmDirectory: %v", err)
	}
}

func TestGitRmNonExistent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Removing non-existent path should be no-op
		err := gs.GitRm(ctx(), "entities/NonExistent/something.json")
		if err != nil {
			return fmt.Errorf("expected no error for non-existent path, got: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("GitRmNonExistent: %v", err)
	}
}

// TestGitRmSingleFile tests removing individual files via GitRm.
func TestGitRmSingleFile(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		e1ID := validUUID(t)

		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: e1ID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "pre-rm"); err != nil {
			return err
		}

		// Remove a single file
		path := "entities/Component/" + e1ID + ".json"
		if err := gs.GitRm(ctx(), path); err != nil {
			return err
		}

		if _, err := gs.fs.Stat(path); err == nil {
			return fmt.Errorf("file should be removed")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("TestGitRmSingleFile: %v", err)
	}
}
