package gitstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// ============================================================================
// T1: Repository initialisation
// ============================================================================

func TestInitNewRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	tmpDir := t.TempDir()
	gs, err := New(tmpDir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Verify .git is initialised
	gitPath := filepath.Join(tmpDir, "graph-repo", ".git")
	if info, err := os.Stat(gitPath); err != nil || !info.IsDir() {
		t.Fatalf("expected .git directory at %s", gitPath)
	}

	// Verify initial commit "init" is present
	gsImpl, ok := gs.(*gitStore)
	if !ok {
		t.Fatalf("expected *gitStore, got %T", gs)
	}
	err = gs.WithGitLock(func() error {
		log, err := gsImpl.repo.Log(&git.LogOptions{})
		if err != nil {
			return err
		}
		defer log.Close()

		found := false
		if err := log.ForEach(func(c *object.Commit) error {
			if c.Message == "init" {
				found = true
				return errStop
			}
			return nil
		}); err != nil && !errors.Is(err, errStop) {
			return err
		}
		if !found {
			return fmt.Errorf("init commit not found")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("commit check: %v", err)
	}

	// Verify entities/ and edges/ directories exist in worktree
	err = gs.WithGitLock(func() error {
		fs := gsImpl.fs
		info, err := fs.Stat("entities")
		if err != nil || !info.IsDir() {
			return fmt.Errorf("entities dir missing: %v", err)
		}
		info, err = fs.Stat("edges")
		if err != nil || !info.IsDir() {
			return fmt.Errorf("edges dir missing: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("dir check: %v", err)
	}
}

// TestHydrationDirs asserts HydrationDirs points into the graph-repo working
// tree used by the service layer for main re-hydration.
func TestHydrationDirs(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	tmpDir := t.TempDir()
	gs, err := New(tmpDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	entitiesDir, edgesDir := gs.HydrationDirs()
	wantEntities := filepath.Join(tmpDir, "graph-repo", "entities")
	wantEdges := filepath.Join(tmpDir, "graph-repo", "edges")
	if entitiesDir != wantEntities {
		t.Errorf("entitiesDir = %q, want %q", entitiesDir, wantEntities)
	}
	if edgesDir != wantEdges {
		t.Errorf("edgesDir = %q, want %q", edgesDir, wantEdges)
	}
}

// TestInitExistingRepo opens an existing repository on disk.
func TestInitExistingRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	tmpDir := t.TempDir()
	if _, err := New(tmpDir); err != nil {
		t.Fatalf("first New failed: %v", err)
	}

	if _, err := New(tmpDir); err != nil {
		t.Fatalf("second New failed: %v", err)
	}
}

func TestInitBadPath(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	// Use a path in a read-only directory
	badPath := "/nonexistent-root-xyz"
	_, err := New(badPath)
	if err == nil {
		t.Fatal("expected error for unwritable path, got nil")
	}
}

// TestNewEmptyBasePath pins New's exported ErrEmptyBasePath guard
// (gitstore.go New's "" check): a store must never be constructed with an
// empty base path, and callers must be able to distinguish the guard via
// errors.Is rather than a nil-interface or ad-hoc error string.
func TestNewEmptyBasePath(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	store, err := New("")
	if !errors.Is(err, ErrEmptyBasePath) {
		t.Fatalf("New(\"\") = %v, want ErrEmptyBasePath", err)
	}
	if store != nil {
		t.Fatalf("New(\"\") returned non-nil store %T, want nil", store)
	}
}

func TestInitNonDirectoryGit(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	// A .git path that exists as a regular file must yield a clear error,
	// not the misleading "stat .git: %!w(<nil>)" from a nil stat error.
	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "graph-repo")
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		t.Fatalf("mkdir repo path: %v", err)
	}
	gitPath := filepath.Join(repoPath, ".git")
	if err := os.WriteFile(gitPath, []byte("not a repo"), 0644); err != nil {
		t.Fatalf("write .git file: %v", err)
	}

	_, err := New(tmpDir)
	if err == nil {
		t.Fatal("expected error for non-directory .git, got nil")
	}
	if strings.Contains(err.Error(), "%!w(<nil>)") || err.Error() == "stat .git: " {
		t.Fatalf("expected clear non-directory .git error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected descriptive error mentioning non-directory, got: %v", err)
	}
}

// TestNewRepairsCrashedInit pins the crash-window recovery for New's init
// sequence (gitstore.go): a process that dies between PlainInitWithOptions and
// the initial commit leaves .git present with an unborn HEAD and no
// refs/heads/main. New must re-run the init commit in that window (gated on
// the main ref, not on whether .git was just created) so the repo is usable —
// IsEmpty's main-ref lookup succeeds, and BeginTransaction's branch-from-main
// (CreateBranch) can resolve main (SPEC durability/recovery).
func TestNewRepairsCrashedInit(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	basePath := t.TempDir()
	repoPath := filepath.Join(basePath, "graph-repo")
	if _, err := git.PlainInitWithOptions(repoPath, &git.PlainInitOptions{
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.ReferenceName("refs/heads/main"),
		},
	}); err != nil {
		t.Fatalf("simulate crashed init (PlainInitWithOptions): %v", err)
	}
	// The simulated crash window: .git is present but the init commit never
	// landed (no refs/heads/main).
	if info, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil || !info.IsDir() {
		t.Fatalf("expected .git directory from crashed init: %v", err)
	}

	gs, err := New(basePath)
	if err != nil {
		t.Fatalf("New after crashed init: %v", err)
	}

	gsImpl, ok := gs.(*gitStore)
	if !ok {
		t.Fatalf("expected *gitStore, got %T", gs)
	}
	err = gs.WithGitLock(func() error {
		// The repair must leave refs/heads/main present and resolvable.
		mainRef, err := gsImpl.repo.Reference(plumbing.ReferenceName("refs/heads/main"), true)
		if err != nil {
			return fmt.Errorf("main ref missing after repair: %w", err)
		}
		commit, err := gsImpl.repo.CommitObject(mainRef.Hash())
		if err != nil {
			return fmt.Errorf("resolve repaired init commit: %w", err)
		}
		if !isInitCommit(commit) {
			return fmt.Errorf("expected cartographer init commit, got message=%q author=%q", commit.Message, commit.Author.Name)
		}
		// The repaired repo reports empty (only the init commit), so the SPEC
		// R10 clone-vs-pull decision sees a fresh repo rather than an error.
		empty, err := gsImpl.IsEmpty(ctx())
		if err != nil {
			return err
		}
		if !empty {
			return fmt.Errorf("expected repaired repo to be init-only (empty), got non-empty")
		}
		// The init commit carries the entities/ and edges/ directories.
		tree, err := commit.Tree()
		if err != nil {
			return err
		}
		if _, err := tree.File("entities/.gitkeep"); err != nil {
			return fmt.Errorf("entities/.gitkeep missing from repaired init commit: %w", err)
		}
		if _, err := tree.File("edges/.gitkeep"); err != nil {
			return fmt.Errorf("edges/.gitkeep missing from repaired init commit: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("repaired init check: %v", err)
	}
}
