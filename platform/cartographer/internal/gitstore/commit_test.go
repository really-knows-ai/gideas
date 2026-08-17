package gitstore

import (
	"errors"
	"fmt"
	"strings"
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

// ============================================================================
// T8: GitLogOneline
// ============================================================================

func TestGitLogOneline(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)

		// Make commits with known prefixes
		e1 := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: e1, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "transaction:abc-123"); err != nil {
			return err
		}

		e2 := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: e2, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "wipe"); err != nil {
			return err
		}

		// Filter by prefix
		results, err := gs.GitLogOneline(ctx(), "transaction:")
		if err != nil {
			return err
		}
		if len(results) != 1 {
			return fmt.Errorf("expected 1 transaction: commit, got %d", len(results))
		}
		if !strings.Contains(results[0], "transaction:abc-123") {
			return fmt.Errorf("expected 'transaction:abc-123' in result, got %q", results[0])
		}

		return nil
	})
	if err != nil {
		t.Fatalf("GitLogOneline: %v", err)
	}
}

func TestGitLogOnelineNoMatch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		results, err := gs.GitLogOneline(ctx(), "nonexistent:")
		if err != nil {
			return err
		}
		if len(results) != 0 {
			return fmt.Errorf("expected empty results, got %d", len(results))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("GitLogOnelineNoMatch: %v", err)
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

// TestCommitExistsOnBranchNoCommit tests CommitExistsOnBranch when
// there are no commits on the current branch.
func TestCommitExistsOnBranchNoCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		found, err := gs.CommitExistsOnBranch(ctx(), "nonexistent")
		if err != nil {
			return err
		}
		if found {
			return fmt.Errorf("expected false for non-existent txID")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestCommitExistsOnBranchNoCommit: %v", err)
	}
}
