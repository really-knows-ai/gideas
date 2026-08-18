package gitstore

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

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
