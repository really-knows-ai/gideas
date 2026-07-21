package gitstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/uuid"
)

// setupTestStore creates a gitStore with in-memory storage and memfs,
// initialised with a main branch and entities/ + edges/ directories.
func setupTestStore(t *testing.T) *gitStore {
	t.Helper()
	fs := memfs.New()
	storer := memory.NewStorage()
	repo, err := git.InitWithOptions(storer, fs, git.InitOptions{
		DefaultBranch: plumbing.ReferenceName("refs/heads/main"),
	})
	if err != nil {
		t.Fatalf("InitWithOptions: %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}

	if err := createDirWithGitkeep(wt, fs, "entities"); err != nil {
		t.Fatalf("create entities dir: %v", err)
	}
	if err := createDirWithGitkeep(wt, fs, "edges"); err != nil {
		t.Fatalf("create edges dir: %v", err)
	}

	// Commit init
	if _, err := wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "test",
			Email: "test@test",
		},
	}); err != nil {
		t.Fatalf("Commit init: %v", err)
	}

	gs := &gitStore{
		repo:    repo,
		wt:      wt,
		fs:      fs,
		backend: storer,
	}
	return gs
}

// createDirWithGitkeep creates a directory with a .gitkeep file so go-git can stage it.
func createDirWithGitkeep(wt *git.Worktree, fs billy.Filesystem, name string) error {
	if err := fs.MkdirAll(name, 0755); err != nil {
		return err
	}
	keep := name + "/.gitkeep"
	f, err := fs.Create(keep)
	if err != nil {
		return err
	}
	f.Close()
	if _, err := wt.Add(keep); err != nil {
		return err
	}
	return nil
}

// validUUID returns a valid UUID v4 string.
func validUUID() string {
	return uuid.Must(uuid.NewRandom()).String()
}

func ctx() context.Context {
	return context.Background()
}

// ============================================================================
// T1: Repository initialisation
// ============================================================================

func TestInitNewRepo(t *testing.T) {
	tmpDir := t.TempDir()
	gs, err := New(tmpDir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer gs.Close()

	// Verify .git is initialised
	gitPath := filepath.Join(tmpDir, "graph-repo", ".git")
	if info, err := os.Stat(gitPath); err != nil || !info.IsDir() {
		t.Fatalf("expected .git directory at %s", gitPath)
	}

	// Verify initial commit "init" is present
	err = gs.WithGitLock(func() error {
		log, err := gs.(*gitStore).repo.Log(&git.LogOptions{})
		if err != nil {
			return err
		}
		defer log.Close()

		found := false
		if err := log.ForEach(func(c *object.Commit) error {
			if c.Message == "init" {
				found = true
				return fmt.Errorf("STOP")
			}
			return nil
		}); err != nil && err.Error() != "STOP" {
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
		fs := gs.(*gitStore).fs
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

func TestInitExistingRepo(t *testing.T) {
	tmpDir := t.TempDir()
	gs1, err := New(tmpDir)
	if err != nil {
		t.Fatalf("first New failed: %v", err)
	}
	gs1.Close()

	gs2, err := New(tmpDir)
	if err != nil {
		t.Fatalf("second New failed: %v", err)
	}
	gs2.Close()
}

func TestInitBadPath(t *testing.T) {
	// Use a path in a read-only directory
	badPath := "/nonexistent-root-xyz"
	_, err := New(badPath)
	if err == nil {
		t.Fatal("expected error for unwritable path, got nil")
	}
}

// ============================================================================
// T2: Entity file operations
// ============================================================================

func TestWriteEntityFiles(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID()
		e2ID := validUUID()
		now := time.Now().UTC().Round(time.Millisecond)

		entities := []Entity{
			{
				ID:         e1ID,
				Type:       "Component",
				Properties: map[string]string{"name": "auth-service", "version": "2.1.0"},
				CreatedAt:  now,
				UpdatedAt:  now,
			},
			{
				ID:         e2ID,
				Type:       "Component",
				Properties: map[string]string{"name": "db-service"},
				CreatedAt:  now,
				UpdatedAt:  now,
			},
		}

		if err := gs.WriteEntityFiles(ctx(), "Component", entities); err != nil {
			return err
		}

		// Verify files exist
		for _, ent := range entities {
			path := "entities/Component/" + ent.ID + ".json"
			fi, err := gs.fs.Stat(path)
			if err != nil {
				return fmt.Errorf("file %s not found: %w", path, err)
			}
			if fi.IsDir() {
				return fmt.Errorf("%s is a directory", path)
			}
		}

		// Verify content by reading back
		files, err := gs.ReadAllEntityFiles(ctx(), "Component")
		if err != nil {
			return err
		}
		if len(files) != 2 {
			return fmt.Errorf("expected 2 files, got %d", len(files))
		}

		return nil
	})
	if err != nil {
		t.Fatalf("WriteEntityFiles: %v", err)
	}
}

func TestWriteEntityFilesWithEmbedding(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID()
		now := time.Now().UTC().Round(time.Millisecond)

		entities := []Entity{
			{
				ID:         e1ID,
				Type:       "Component",
				Properties: map[string]string{"name": "test"},
				Embedding:  []float32{0.12, -0.34, 0.56},
				CreatedAt:  now,
				UpdatedAt:  now,
			},
		}

		if err := gs.WriteEntityFiles(ctx(), "Component", entities); err != nil {
			return err
		}

		files, err := gs.ReadAllEntityFiles(ctx(), "Component")
		if err != nil {
			return err
		}
		if len(files) != 1 {
			return fmt.Errorf("expected 1 file, got %d", len(files))
		}
		if len(files[0].Embedding) != 3 {
			return fmt.Errorf("expected 3 embedding values, got %d", len(files[0].Embedding))
		}
		if files[0].Embedding[0] != 0.12 || files[0].Embedding[1] != -0.34 || files[0].Embedding[2] != 0.56 {
			return fmt.Errorf("unexpected embedding: %v", files[0].Embedding)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WriteEntityFilesWithEmbedding: %v", err)
	}
}

func TestWriteEntityFilesEmpty(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		return gs.WriteEntityFiles(ctx(), "Component", []Entity{})
	})
	if err != nil {
		t.Fatalf("empty write failed: %v", err)
	}
}

func TestRemoveEntityFiles(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID()
		e2ID := validUUID()
		now := time.Now().UTC().Round(time.Millisecond)

		entities := []Entity{
			{ID: e1ID, Type: "Component", CreatedAt: now, UpdatedAt: now},
			{ID: e2ID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}

		if err := gs.WriteEntityFiles(ctx(), "Component", entities); err != nil {
			return err
		}

		// Remove the first entity
		if err := gs.RemoveEntityFiles(ctx(), "Component", []string{e1ID}); err != nil {
			return err
		}

		// Verify e1 is gone, e2 remains
		_, err1 := gs.fs.Stat("entities/Component/" + e1ID + ".json")
		if err1 == nil {
			return fmt.Errorf("removed file %s still exists", e1ID)
		}
		_, err2 := gs.fs.Stat("entities/Component/" + e2ID + ".json")
		if err2 != nil {
			return fmt.Errorf("remaining file %s missing: %w", e2ID, err2)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("RemoveEntityFiles: %v", err)
	}
}

func TestRemoveEntityFilesNonExistent(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Removing non-existent file should not error
		return gs.RemoveEntityFiles(ctx(), "Component", []string{validUUID()})
	})
	if err != nil {
		t.Fatalf("RemoveEntityFiles non-existent: %v", err)
	}
}

func TestReadAllEntityFiles(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID()
		e2ID := validUUID()
		now := time.Now().UTC().Round(time.Millisecond)

		entities := []Entity{
			{
				ID:         e1ID,
				Type:       "Component",
				Properties: map[string]string{"name": "first"},
				CreatedAt:  now,
				UpdatedAt:  now,
			},
			{
				ID:         e2ID,
				Type:       "Component",
				Properties: map[string]string{"name": "second"},
				CreatedAt:  now,
				UpdatedAt:  now,
			},
		}

		if err := gs.WriteEntityFiles(ctx(), "Component", entities); err != nil {
			return err
		}

		files, err := gs.ReadAllEntityFiles(ctx(), "Component")
		if err != nil {
			return err
		}
		if len(files) != 2 {
			return fmt.Errorf("expected 2 files, got %d", len(files))
		}

		// Check for both names
		names := make(map[string]bool)
		for _, f := range files {
			if f.Properties != nil {
				names[f.Properties["name"]] = true
			}
		}
		if !names["first"] || !names["second"] {
			return fmt.Errorf("missing entity names: %v", names)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("ReadAllEntityFiles: %v", err)
	}
}

func TestReadAllEntityFilesEmptyDir(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		files, err := gs.ReadAllEntityFiles(ctx(), "NonExistent")
		if err != nil {
			return err
		}
		if len(files) != 0 {
			return fmt.Errorf("expected empty slice, got %d", len(files))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ReadAllEntityFilesEmptyDir: %v", err)
	}
}

func TestListEntityTypes(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)

		// Write entities for two types
		entitiesA := []Entity{
			{ID: validUUID(), Type: "Component", CreatedAt: now, UpdatedAt: now},
		}
		entitiesB := []Entity{
			{ID: validUUID(), Type: "Service", CreatedAt: now, UpdatedAt: now},
		}

		if err := gs.WriteEntityFiles(ctx(), "Component", entitiesA); err != nil {
			return err
		}
		if err := gs.WriteEntityFiles(ctx(), "Service", entitiesB); err != nil {
			return err
		}

		types, err := gs.ListEntityTypes(ctx())
		if err != nil {
			return err
		}
		if len(types) != 2 {
			return fmt.Errorf("expected 2 types, got %d: %v", len(types), types)
		}
		if types[0] != "Component" || types[1] != "Service" {
			return fmt.Errorf("expected [Component Service], got %v", types)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("ListEntityTypes: %v", err)
	}
}

func TestListEntityTypesEmptyDir(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		types, err := gs.ListEntityTypes(ctx())
		if err != nil {
			return err
		}
		if len(types) != 0 {
			return fmt.Errorf("expected empty, got %v", types)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ListEntityTypesEmptyDir: %v", err)
	}
}

func TestListEntityTypesEmptyTypeDir(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Create empty type directory
		if err := gs.fs.MkdirAll("entities/EmptyType", 0755); err != nil {
			return err
		}

		types, err := gs.ListEntityTypes(ctx())
		if err != nil {
			return err
		}
		if len(types) != 0 {
			return fmt.Errorf("expected 0 types (empty dir excluded), got %v", types)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ListEntityTypesEmptyTypeDir: %v", err)
	}
}

// ============================================================================
// T3: Edge file operations
// ============================================================================

func TestWriteEdgeFiles(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID()
		e2ID := validUUID()
		fromID := validUUID()
		toID := validUUID()
		now := time.Now().UTC().Round(time.Millisecond)

		edges := []Edge{
			{
				ID:           e1ID,
				Type:         "DEPENDS_ON",
				FromEntityID: fromID,
				ToEntityID:   toID,
				Properties:   map[string]string{"weight": "high"},
				CreatedAt:    now,
				UpdatedAt:    now,
			},
			{
				ID:           e2ID,
				Type:         "DEPENDS_ON",
				FromEntityID: toID,
				ToEntityID:   fromID,
				CreatedAt:    now,
				UpdatedAt:    now,
			},
		}

		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", edges); err != nil {
			return err
		}

		for _, edge := range edges {
			path := "edges/DEPENDS_ON/" + edge.ID + ".json"
			if _, err := gs.fs.Stat(path); err != nil {
				return fmt.Errorf("file %s not found: %w", path, err)
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("WriteEdgeFiles: %v", err)
	}
}

func TestWriteEdgeFilesEmpty(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		return gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{})
	})
	if err != nil {
		t.Fatalf("empty edge write failed: %v", err)
	}
}

func TestRemoveEdgeFiles(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID()
		e2ID := validUUID()
		now := time.Now().UTC().Round(time.Millisecond)

		edges := []Edge{
			{
				ID: e1ID, Type: "DEPENDS_ON",
				FromEntityID: validUUID(), ToEntityID: validUUID(),
				CreatedAt: now, UpdatedAt: now,
			},
			{
				ID: e2ID, Type: "DEPENDS_ON",
				FromEntityID: validUUID(), ToEntityID: validUUID(),
				CreatedAt: now, UpdatedAt: now,
			},
		}

		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", edges); err != nil {
			return err
		}
		if err := gs.RemoveEdgeFiles(ctx(), "DEPENDS_ON", []string{e1ID}); err != nil {
			return err
		}

		if _, err := gs.fs.Stat("edges/DEPENDS_ON/" + e1ID + ".json"); err == nil {
			return fmt.Errorf("removed edge file still exists")
		}
		if _, err := gs.fs.Stat("edges/DEPENDS_ON/" + e2ID + ".json"); err != nil {
			return fmt.Errorf("remaining edge file missing")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RemoveEdgeFiles: %v", err)
	}
}

func TestRemoveEdgeFilesNonExistent(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		return gs.RemoveEdgeFiles(ctx(), "DEPENDS_ON", []string{validUUID()})
	})
	if err != nil {
		t.Fatalf("RemoveEdgeFiles non-existent: %v", err)
	}
}

func TestReadAllEdgeFiles(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID()
		now := time.Now().UTC().Round(time.Millisecond)
		fromID := validUUID()
		toID := validUUID()

		edges := []Edge{
			{
				ID: e1ID, Type: "DEPENDS_ON",
				FromEntityID: fromID, ToEntityID: toID,
				Properties: map[string]string{"weight": "low"},
				CreatedAt:  now, UpdatedAt: now,
			},
		}

		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", edges); err != nil {
			return err
		}

		files, err := gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON")
		if err != nil {
			return err
		}
		if len(files) != 1 {
			return fmt.Errorf("expected 1 file, got %d", len(files))
		}
		if files[0].Properties["weight"] != "low" {
			return fmt.Errorf("expected weight=low, got %q", files[0].Properties["weight"])
		}
		if files[0].FromEntityID != fromID {
			return fmt.Errorf("expected FromEntityID=%s, got %s", fromID, files[0].FromEntityID)
		}
		if files[0].ToEntityID != toID {
			return fmt.Errorf("expected ToEntityID=%s, got %s", toID, files[0].ToEntityID)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("ReadAllEdgeFiles: %v", err)
	}
}

func TestListEdgeTypes(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		fromID := validUUID()
		toID := validUUID()

		edgesA := []Edge{
			{ID: validUUID(), Type: "DEPENDS_ON", FromEntityID: fromID, ToEntityID: toID, CreatedAt: now, UpdatedAt: now},
		}
		edgesB := []Edge{
			{ID: validUUID(), Type: "CONNECTS_TO", FromEntityID: fromID, ToEntityID: toID, CreatedAt: now, UpdatedAt: now},
		}

		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", edgesA); err != nil {
			return err
		}
		if err := gs.WriteEdgeFiles(ctx(), "CONNECTS_TO", edgesB); err != nil {
			return err
		}

		types, err := gs.ListEdgeTypes(ctx())
		if err != nil {
			return err
		}
		if len(types) != 2 {
			return fmt.Errorf("expected 2 edge types, got %d", len(types))
		}

		return nil
	})
	if err != nil {
		t.Fatalf("ListEdgeTypes: %v", err)
	}
}

func TestListEdgeTypesEmptyDir(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		types, err := gs.ListEdgeTypes(ctx())
		if err != nil {
			return err
		}
		if len(types) != 0 {
			return fmt.Errorf("expected empty, got %v", types)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ListEdgeTypesEmptyDir: %v", err)
	}
}

func TestListEdgeTypesEmptyTypeDir(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		if err := gs.fs.MkdirAll("edges/EmptyType", 0755); err != nil {
			return err
		}
		types, err := gs.ListEdgeTypes(ctx())
		if err != nil {
			return err
		}
		if len(types) != 0 {
			return fmt.Errorf("expected 0 types (empty dir excluded), got %v", types)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ListEdgeTypesEmptyTypeDir: %v", err)
	}
}

// ============================================================================
// T4: Branch operations
// ============================================================================

func TestCreateBranch(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txID := validUUID()
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
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txID := validUUID()
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

func TestCheckout(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		e1ID := validUUID()

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
		txID := validUUID()
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
		e2ID := validUUID()
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
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		e1ID := validUUID()

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
		txID := validUUID()
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
		f.Close()

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
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txID := validUUID()
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

func TestBranchExists(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txID := validUUID()

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
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txID := validUUID()
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
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)

		// Get initial main HEAD
		hash1, err := gs.BranchHEAD(ctx(), "main")
		if err != nil {
			return err
		}

		// Make a commit on main
		e1ID := validUUID()
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
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txID := validUUID()
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
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txID := validUUID()
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
		f.Close()

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
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		tx1 := validUUID()
		tx2 := validUUID()

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

// ============================================================================
// T5: Git operations (AddAll + Commit)
// ============================================================================

func TestAddAllAndCommit(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		e1ID := validUUID()

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
				return fmt.Errorf("STOP")
			}
			return nil
		}); err != nil && err.Error() != "STOP" {
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
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txID := validUUID()
		now := time.Now().UTC().Round(time.Millisecond)

		// Write and commit with transaction prefix
		e1ID := validUUID()
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

		// Non-existent txID should return false
		found, err = gs.CommitExistsOnBranch(ctx(), "nonexistent-tx-id")
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

func TestCommitExistsOnBranchScopedToBranch(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txA := validUUID()
		txB := validUUID()
		now := time.Now().UTC().Round(time.Millisecond)

		// Create branch A and commit with message "transaction:a"
		if err := gs.CreateBranch(ctx(), txA); err != nil {
			return err
		}
		if err := gs.Checkout(ctx(), txA); err != nil {
			return err
		}
		eA := validUUID()
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
		eB := validUUID()
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
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		e1ID := validUUID()

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
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		e1ID := validUUID()
		e2ID := validUUID()

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

		return nil
	})
	if err != nil {
		t.Fatalf("GitRmDirectory: %v", err)
	}
}

func TestGitRmNonExistent(t *testing.T) {
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
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)

		// Create entity A on main and commit
		eA := validUUID()
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
		txID := validUUID()
		if err := gs.CreateBranch(ctx(), txID); err != nil {
			return err
		}
		if err := gs.Checkout(ctx(), txID); err != nil {
			return err
		}

		eB := validUUID()
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
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)

		// Entity A on main
		eA := validUUID()
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
		txID := validUUID()
		if err := gs.CreateBranch(ctx(), txID); err != nil {
			return err
		}
		if err := gs.Checkout(ctx(), txID); err != nil {
			return err
		}
		eB := validUUID()
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
		eC := validUUID()
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
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		txID := validUUID()

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
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)

		// Create branch A with entity
		branchA := validUUID()
		if err := gs.CreateBranch(ctx(), branchA); err != nil {
			return err
		}
		if err := gs.Checkout(ctx(), branchA); err != nil {
			return err
		}
		eA := validUUID()
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

		// Create branch B (from branch A) with another entity
		branchB := validUUID()
		if err := gs.CreateBranch(ctx(), branchB); err != nil {
			return err
		}
		if err := gs.Checkout(ctx(), branchB); err != nil {
			return err
		}
		eB := validUUID()
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
// T7: Remote operations
// ============================================================================

func TestHasRemote(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		has, err := gs.HasRemote(ctx())
		if err != nil {
			return err
		}
		if has {
			return fmt.Errorf("expected false for no remote")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("HasRemote: %v", err)
	}
}

func TestSetRemoteHasRemote(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		if err := gs.SetRemote(ctx(), "https://example.com/repo.git", nil); err != nil {
			return err
		}
		has, err := gs.HasRemote(ctx())
		if err != nil {
			return err
		}
		if !has {
			return fmt.Errorf("expected true after SetRemote")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("SetRemoteHasRemote: %v", err)
	}
}

func TestSetRemoteInvalidScheme(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		err := gs.SetRemote(ctx(), "ftp://example.com/repo.git", nil)
		if !errors.Is(err, ErrUnsupportedURLScheme) {
			return fmt.Errorf("expected ErrUnsupportedURLScheme, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("SetRemoteInvalidScheme: %v", err)
	}
}

func TestIsEmpty(t *testing.T) {
	t.Run("fresh init returns true", func(t *testing.T) {
		gs := setupTestStore(t)
		gs.WithGitLock(func() error {
			empty, err := gs.IsEmpty(ctx())
			if err != nil {
				return err
			}
			if !empty {
				return fmt.Errorf("expected empty for init-only repo")
			}
			return nil
		})
	})

	t.Run("with data commit returns false", func(t *testing.T) {
		gs := setupTestStore(t)
		gs.WithGitLock(func() error {
			now := time.Now().UTC().Round(time.Millisecond)
			e1ID := validUUID()

			if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
				{ID: e1ID, Type: "Component", CreatedAt: now, UpdatedAt: now},
			}); err != nil {
				return err
			}
			if err := gs.AddAll(ctx(), "."); err != nil {
				return err
			}
			if err := gs.Commit(ctx(), "transaction:test-1"); err != nil {
				return err
			}

			empty, err := gs.IsEmpty(ctx())
			if err != nil {
				return err
			}
			if empty {
				return fmt.Errorf("expected non-empty after data commit")
			}
			return nil
		})
	})
}

func TestPushRemoteNoRemote(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		err := gs.PushRemote(ctx())
		if !errors.Is(err, ErrNoRemote) {
			return fmt.Errorf("expected ErrNoRemote, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("PushRemoteNoRemote: %v", err)
	}
}

func TestFetchRemoteNoRemote(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		err := gs.FetchRemote(ctx())
		if !errors.Is(err, ErrNoRemote) {
			return fmt.Errorf("expected ErrNoRemote, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("FetchRemoteNoRemote: %v", err)
	}
}

func TestPullAndFastForwardNoRemote(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		err := gs.PullAndFastForward(ctx())
		if !errors.Is(err, ErrNoRemote) {
			return fmt.Errorf("expected ErrNoRemote, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("PullAndFastForwardNoRemote: %v", err)
	}
}

// TestPushRemoteWithAuth verifies PushRemote returns expected errors
// for auth-related issues.
func TestPushRemoteWithAuth(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		if err := gs.SetRemote(ctx(), "https://example.com/repo.git", nil); err != nil {
			return err
		}
		// authFn is nil → ErrAuthConfigMissing
		err := gs.PushRemote(ctx())
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing, got %v", err)
		}

		// Set authFn that returns an error → ErrRemoteAuthResolutionFailed
		if err := gs.SetRemote(ctx(), "https://example.com/repo.git", func() (transport.AuthMethod, error) {
			return nil, fmt.Errorf("auth resolution failure")
		}); err != nil {
			return err
		}
		err = gs.PushRemote(ctx())
		if !errors.Is(err, ErrRemoteAuthResolutionFailed) {
			return fmt.Errorf("expected ErrRemoteAuthResolutionFailed, got %v", err)
		}

		// Set authFn that returns nil auth → ErrAuthConfigMissing
		if err := gs.SetRemote(ctx(), "https://example.com/repo.git", func() (transport.AuthMethod, error) {
			return nil, nil
		}); err != nil {
			return err
		}
		err = gs.PushRemote(ctx())
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing (nil auth), got %v", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("PushRemoteWithAuth: %v", err)
	}
}

// TestFetchRemoteWithAuth verifies FetchRemote returns expected errors
// for auth-related issues.
func TestFetchRemoteWithAuth(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		if err := gs.SetRemote(ctx(), "https://example.com/repo.git", nil); err != nil {
			return err
		}
		err := gs.FetchRemote(ctx())
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("FetchRemoteWithAuth: %v", err)
	}
}

// TestPullWithAuth verifies PullAndFastForward returns expected errors
// for auth-related issues.
func TestPullWithAuth(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		if err := gs.SetRemote(ctx(), "https://example.com/repo.git", nil); err != nil {
			return err
		}
		err := gs.PullAndFastForward(ctx())
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("PullWithAuth: %v", err)
	}
}

// TestPullAlreadyUpToDate verifies that pulling when already up-to-date
// returns no error.
func TestPullAlreadyUpToDate(t *testing.T) {
	tmpDir := t.TempDir()

	gs, err := New(tmpDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer gs.Close()

	err = gs.WithGitLock(func() error {
		if err := gs.SetRemote(ctx(), "https://example.com/repo.git", nil); err != nil {
			return err
		}
		// We can't actually test a successful pull without a real remote,
		// but we can verify the error path (ErrAuthConfigMissing since authFn is nil)
		err := gs.PullAndFastForward(ctx())
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("PullAlreadyUpToDate: %v", err)
	}
}

func TestCloneSingleBranchNoAuth(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		err := gs.CloneSingleBranch(ctx(), "https://example.com/repo.git", "main")
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("CloneSingleBranchNoAuth: %v", err)
	}
}

// ============================================================================
// T8: GitLogOneline
// ============================================================================

func TestGitLogOneline(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)

		// Make commits with known prefixes
		e1 := validUUID()
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

		e2 := validUUID()
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

// ============================================================================
// Sequence test: full write → add → commit → read-back cycle
// ============================================================================

func TestFullWriteAddCommitReadBack(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)

		// Write entity
		e1ID := validUUID()
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{
				ID:         e1ID,
				Type:       "Component",
				Properties: map[string]string{"name": "test"},
				CreatedAt:  now,
				UpdatedAt:  now,
			},
		}); err != nil {
			return err
		}

		// Write edge
		edgeID := validUUID()
		fromID := validUUID()
		toID := validUUID()
		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{
			{
				ID:           edgeID,
				Type:         "DEPENDS_ON",
				FromEntityID: fromID,
				ToEntityID:   toID,
				Properties:   map[string]string{"weight": "high"},
				CreatedAt:    now,
				UpdatedAt:    now,
			},
		}); err != nil {
			return err
		}

		// Add and commit
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "transaction:full-cycle"); err != nil {
			return err
		}

		// Read back entity
		entities, err := gs.ReadAllEntityFiles(ctx(), "Component")
		if err != nil {
			return err
		}
		if len(entities) != 1 {
			return fmt.Errorf("expected 1 entity, got %d", len(entities))
		}
		if entities[0].Properties["name"] != "test" {
			return fmt.Errorf("expected name=test, got %q", entities[0].Properties["name"])
		}

		// Read back edge
		edges, err := gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON")
		if err != nil {
			return err
		}
		if len(edges) != 1 {
			return fmt.Errorf("expected 1 edge, got %d", len(edges))
		}
		if edges[0].Properties["weight"] != "high" {
			return fmt.Errorf("expected weight=high, got %q", edges[0].Properties["weight"])
		}

		// Verify commit exists
		found, err := gs.CommitExistsOnBranch(ctx(), "full-cycle")
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("expected commit to exist")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("FullWriteAddCommitReadBack: %v", err)
	}
}

// TestEnsureRemoteExists verifies the ensureRemoteExists helper by
// removing the remote and then calling a remote operation that recreates it.
func TestEnsureRemoteExists(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Set remote URL and authFn directly without calling SetRemote
		gs.remoteURL = "https://example.com/repo.git"
		gs.authFn = func() (transport.AuthMethod, error) {
			return nil, nil
		}

		// ensureRemoteExists should create the remote since it doesn't exist
		// and then FetchRemote proceeds to resolveAuth -> nil auth -> ErrAuthConfigMissing
		err := gs.FetchRemote(ctx())
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing, got %v", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("TestEnsureRemoteExists: %v", err)
	}
}

// TestFastForwardMergeBranchNotFound tests the ErrBranchNotFound paths
// in FastForwardMerge.
func TestFastForwardMergeBranchNotFound(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Non-existent source branch
		err := gs.FastForwardMerge(ctx(), "nonexistent-source", "main")
		if !errors.Is(err, ErrBranchNotFound) {
			return fmt.Errorf("expected ErrBranchNotFound for source, got %v", err)
		}

		// Non-existent target
		txID := validUUID()
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
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		e1ID := validUUID()

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

// TestEmptyEdgeWriteAndRead verifies empty edge write/read cycle.
func TestEmptyEdgeWriteAndRead(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Empty write should not error
		if err := gs.WriteEdgeFiles(ctx(), "EMPTY_TYPE", []Edge{}); err != nil {
			return err
		}
		// Reading from non-existent dir returns empty slice
		files, err := gs.ReadAllEdgeFiles(ctx(), "EMPTY_TYPE")
		if err != nil {
			return err
		}
		if len(files) != 0 {
			return fmt.Errorf("expected empty slice for non-existent edge type")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestEmptyEdgeWriteAndRead: %v", err)
	}
}

// TestHasRemoteError verifies that HasRemote returns (false, err) on error.
func TestHasRemoteError(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Fresh repo with no remote — should return (false, nil)
		has, err := gs.HasRemote(ctx())
		if err != nil {
			return err
		}
		if has {
			return fmt.Errorf("expected false for no remote")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestHasRemoteError: %v", err)
	}
}

// TestWriteEntityInvalidUUID verifies ErrInvalidUUID is returned.
func TestWriteEntityInvalidUUID(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		entities := []Entity{
			{ID: "not-a-uuid", Type: "Component", CreatedAt: now, UpdatedAt: now},
		}
		err := gs.WriteEntityFiles(ctx(), "Component", entities)
		if !errors.Is(err, ErrInvalidUUID) {
			return fmt.Errorf("expected ErrInvalidUUID, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestWriteEntityInvalidUUID: %v", err)
	}
}

// TestWriteEdgeInvalidUUID verifies ErrInvalidUUID is returned.
func TestWriteEdgeInvalidUUID(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		edges := []Edge{
			{
				ID: "not-a-uuid", Type: "DEPENDS_ON",
				FromEntityID: validUUID(), ToEntityID: validUUID(),
				CreatedAt: now, UpdatedAt: now,
			},
		}
		err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", edges)
		if !errors.Is(err, ErrInvalidUUID) {
			return fmt.Errorf("expected ErrInvalidUUID, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestWriteEdgeInvalidUUID: %v", err)
	}
}

// TestBranchHEADNotFound verifies BranchHEAD returns ErrBranchNotFound.
func TestBranchHEADNotFound(t *testing.T) {
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
	gs := setupTestStore(t)
	_ = gs.WithGitLock(func() error {
		// Should not panic — may return nil or an error depending on storage
		_ = gs.DeleteBranch(ctx(), "nonexistent")
		return nil
	})
}

// noopAuth is a transport.AuthMethod that does nothing — used for file:// URLs.
type noopAuth struct{}

func (a noopAuth) Name() string   { return "noop" }
func (a noopAuth) String() string { return "noop" }

// TestRemotePushPull exercises PushRemote, FetchRemote, and PullAndFastForward
// using temp directory repos with file:// URLs set directly on the gitStore.
func TestRemotePushPull(t *testing.T) {
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	// Create a bare remote repo
	_, err := git.PlainInitWithOptions(bareDir, &git.PlainInitOptions{
		Bare: true,
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.ReferenceName("refs/heads/main"),
		},
	})
	if err != nil {
		t.Fatalf("init bare remote: %v", err)
	}

	// Create local repo
	localDir := filepath.Join(tmpDir, "local")
	store, err := New(localDir)
	if err != nil {
		t.Fatalf("New local: %v", err)
	}
	defer store.Close()

	gs := store.(*gitStore)

	err = store.WithGitLock(func() error {
		// Configure the remote URL and authFn directly
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		// Create the "origin" remote directly on the go-git repo
		_, err := gs.repo.CreateRemote(&config.RemoteConfig{
			Name: "origin",
			URLs: []string{gs.remoteURL},
		})
		if err != nil {
			return fmt.Errorf("create remote: %w", err)
		}

		// Make a commit on local
		now := time.Now().UTC().Round(time.Millisecond)
		e1ID := validUUID()
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: e1ID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "transaction:push-test"); err != nil {
			return err
		}

		// Push to remote
		if err := gs.PushRemote(ctx()); err != nil {
			return fmt.Errorf("push: %w", err)
		}

		// Verify on remote
		remoteRepo, err := git.PlainOpen(bareDir)
		if err != nil {
			return fmt.Errorf("open remote: %w", err)
		}
		ref, err := remoteRepo.Reference(plumbing.ReferenceName("refs/heads/main"), true)
		if err != nil {
			return fmt.Errorf("remote ref: %w", err)
		}
		if ref.Hash().IsZero() {
			return fmt.Errorf("expected non-zero hash on remote")
		}

		// Fetch back from remote (should be up-to-date)
		if err := gs.FetchRemote(ctx()); err != nil {
			return fmt.Errorf("fetch: %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("RemotePushPull: %v", err)
	}
}

// TestPullFromRemote exercises pulling from a remote with commits.
func TestPullFromRemote(t *testing.T) {
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	// Create a non-bare working repo first, make a commit, then clone as bare
	workDir := filepath.Join(tmpDir, "work")
	workRepo, err := git.PlainInitWithOptions(workDir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.ReferenceName("refs/heads/main"),
		},
	})
	if err != nil {
		t.Fatalf("init work repo: %v", err)
	}
	wt, err := workRepo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}

	// Make initial commit in work repo
	seedFile, seedErr := wt.Filesystem.Create("seed.txt")
	if seedErr != nil {
		t.Fatalf("create seed file: %v", seedErr)
	}
	seedFile.Close()
	if _, err := wt.Add("seed.txt"); err != nil {
		t.Fatalf("add seed: %v", err)
	}
	if _, err := wt.Commit("seed commit", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	// Clone work repo as bare to create the "remote"
	_, err = git.PlainClone(bareDir, true, &git.CloneOptions{
		URL: "file://" + workDir,
	})
	if err != nil {
		t.Fatalf("clone bare: %v", err)
	}

	// Clone the bare remote, make a commit, and push back
	cloneDir := filepath.Join(tmpDir, "clone")
	cloned, err := git.PlainClone(cloneDir, false, &git.CloneOptions{
		URL: "file://" + bareDir,
	})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}

	clonedWT, err := cloned.Worktree()
	if err != nil {
		t.Fatalf("cloned worktree: %v", err)
	}

	// Make a commit on the clone
	cloneFile, cloneErr := clonedWT.Filesystem.Create("initial.txt")
	if cloneErr != nil {
		t.Fatalf("create file: %v", cloneErr)
	}
	cloneFile.Close()
	if _, err := clonedWT.Add("initial.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := clonedWT.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := cloned.Push(&git.PushOptions{}); err != nil {
		t.Fatalf("push: %v", err)
	}

	// Now create local repo by cloning from remote
	localDir := filepath.Join(tmpDir, "local")
	clonedLocalRepo, err := git.PlainClone(localDir, false, &git.CloneOptions{
		URL: "file://" + bareDir,
	})
	if err != nil {
		t.Fatalf("clone local: %v", err)
	}

	clonedLocalWT, err := clonedLocalRepo.Worktree()
	if err != nil {
		t.Fatalf("cloned worktree: %v", err)
	}

	// Create a gitStore from the cloned repo
	gs := &gitStore{
		repo:    clonedLocalRepo,
		wt:      clonedLocalWT,
		fs:      clonedLocalWT.Filesystem,
		backend: clonedLocalRepo.Storer,
	}

	err = gs.WithGitLock(func() error {
		// The cloned repo already has origin set up; just configure URL and auth
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		// Pull from remote (should be no-op, already up-to-date)
		if err := gs.PullAndFastForward(ctx()); err != nil {
			return fmt.Errorf("first pull: %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("PullFromRemote setup: %v", err)
	}

	// Make another commit on the remote (push from the clone)
	cloneFile2, err := clonedWT.Filesystem.Create("another.txt")
	if err != nil {
		t.Fatalf("create another: %v", err)
	}
	cloneFile2.Close()
	if _, err := clonedWT.Add("another.txt"); err != nil {
		t.Fatalf("add another: %v", err)
	}
	if _, err := clonedWT.Commit("second commit", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("second commit: %v", err)
	}
	if err := cloned.Push(&git.PushOptions{}); err != nil {
		t.Fatalf("push second: %v", err)
	}

	// Pull the new commit into local
	err = gs.WithGitLock(func() error {
		if err := gs.PullAndFastForward(ctx()); err != nil {
			return fmt.Errorf("second pull: %w", err)
		}

		// Verify the second commit exists
		log, err := gs.repo.Log(&git.LogOptions{})
		if err != nil {
			return err
		}
		log.Close()

		if err := log.ForEach(func(c *object.Commit) error {
			if strings.HasPrefix(c.Message, "second commit") {
				return fmt.Errorf("FOUND")
			}
			return nil
		}); err != nil && err.Error() != "FOUND" {
			return err
		}

		return nil
	})
	if err != nil {
		t.Fatalf("PullFromRemote: %v", err)
	}
}

// TestCloneSingleBranchFromRemote tests CloneSingleBranch with an actual remote.
func TestCloneSingleBranchFromRemote(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a source repo with a commit
	srcDir := filepath.Join(tmpDir, "src")
	srcRepo, err := git.PlainInitWithOptions(srcDir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.ReferenceName("refs/heads/main"),
		},
	})
	if err != nil {
		t.Fatalf("init src: %v", err)
	}
	srcWT, err := srcRepo.Worktree()
	if err != nil {
		t.Fatalf("src worktree: %v", err)
	}
	sf, err := srcWT.Filesystem.Create("data.txt")
	if err != nil {
		t.Fatalf("create data: %v", err)
	}
	sf.Close()
	if _, err := srcWT.Add("data.txt"); err != nil {
		t.Fatalf("add data: %v", err)
	}
	if _, err := srcWT.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Clone to a bare remote
	bareDir := filepath.Join(tmpDir, "bare.git")
	_, err = git.PlainClone(bareDir, true, &git.CloneOptions{
		URL: "file://" + srcDir,
	})
	if err != nil {
		t.Fatalf("clone bare: %v", err)
	}

	// Create a local repo (initialised by New)
	localDir := filepath.Join(tmpDir, "local")
	localStore, err := New(localDir)
	if err != nil {
		t.Fatalf("New local: %v", err)
	}
	defer localStore.Close()

	gs := localStore.(*gitStore)

	err = localStore.WithGitLock(func() error {
		// Configure auth
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		// Clone from remote
		if err := gs.CloneSingleBranch(ctx(), "file://"+bareDir, "main"); err != nil {
			return fmt.Errorf("CloneSingleBranch: %w", err)
		}

		// Verify the data file exists
		if _, err := gs.fs.Stat("data.txt"); err != nil {
			return fmt.Errorf("data file should exist after clone: %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("CloneSingleBranchFromRemote: %v", err)
	}
}

// TestIsEmptyMainRefError verifies the error path in IsEmpty when
// the main ref cannot be resolved for non-ErrReferenceNotFound reasons.
func TestIsEmptyMainRefError(t *testing.T) {
	// Create a gitStore with a broken backend to trigger errors
	fs := memfs.New()
	storer := memory.NewStorage()
	repo, err := git.InitWithOptions(storer, fs, git.InitOptions{
		DefaultBranch: plumbing.ReferenceName("refs/heads/main"),
	})
	if err != nil {
		t.Fatalf("InitWithOptions: %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}

	// Create a broken storer that fails on SetReference
	gs := &gitStore{
		repo:    repo,
		wt:      wt,
		fs:      fs,
		backend: storer,
	}

	err = gs.WithGitLock(func() error {
		// Delete the main ref to test ErrReferenceNotFound path in IsEmpty
		if err := gs.backend.RemoveReference(plumbing.ReferenceName("refs/heads/main")); err != nil {
			return err
		}
		empty, err := gs.IsEmpty(ctx())
		if err != nil {
			return err
		}
		if !empty {
			return fmt.Errorf("expected empty when main ref is missing")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestIsEmptyMainRefError: %v", err)
	}
}

// TestReadAllEntityFilesOpenError tests error handling in ReadAllEntityFiles.
func TestReadAllEntityFilesOpenError(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Create an empty type directory (no JSON files)
		if err := gs.fs.MkdirAll("entities/EmptyType", 0755); err != nil {
			return err
		}
		files, err := gs.ReadAllEntityFiles(ctx(), "EmptyType")
		if err != nil {
			return err
		}
		if len(files) != 0 {
			return fmt.Errorf("expected 0 files for empty type dir")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllEntityFilesOpenError: %v", err)
	}
}

// TestWriteEntityNilEmbedding tests that nil embedding is handled correctly.
func TestWriteEntityNilEmbedding(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID()
		now := time.Now().UTC().Round(time.Millisecond)

		entities := []Entity{
			{
				ID:        e1ID,
				Type:      "Component",
				Embedding: nil, // nil embedding
				CreatedAt: now,
				UpdatedAt: now,
			},
		}

		if err := gs.WriteEntityFiles(ctx(), "Component", entities); err != nil {
			return err
		}

		// Read back and verify embedding is nil
		files, err := gs.ReadAllEntityFiles(ctx(), "Component")
		if err != nil {
			return err
		}
		if len(files) != 1 {
			return fmt.Errorf("expected 1 file, got %d", len(files))
		}
		if files[0].Embedding != nil {
			return fmt.Errorf("expected nil embedding, got %v", files[0].Embedding)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestWriteEntityNilEmbedding: %v", err)
	}
}

// TestWriteEntityEmptyEmbeddingSlice tests that empty embedding slice is handled.
func TestWriteEntityEmptyEmbeddingSlice(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID()
		now := time.Now().UTC().Round(time.Millisecond)

		entities := []Entity{
			{
				ID:        e1ID,
				Type:      "Component",
				Embedding: []float32{}, // empty, non-nil slice
				CreatedAt: now,
				UpdatedAt: now,
			},
		}

		if err := gs.WriteEntityFiles(ctx(), "Component", entities); err != nil {
			return err
		}

		// Read back and verify embedding is empty but not nil
		files, err := gs.ReadAllEntityFiles(ctx(), "Component")
		if err != nil {
			return err
		}
		if len(files) != 1 {
			return fmt.Errorf("expected 1 file, got %d", len(files))
		}
		if files[0].Embedding == nil {
			return fmt.Errorf("expected non-nil empty embedding")
		}
		if len(files[0].Embedding) != 0 {
			return fmt.Errorf("expected 0-length embedding, got %d", len(files[0].Embedding))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestWriteEntityEmptyEmbeddingSlice: %v", err)
	}
}

// TestFetchRemoteSuccess tests FetchRemote when there are new commits on the remote.
func TestFetchRemoteSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	// Create bare remote with initial commit
	workDir := filepath.Join(tmpDir, "work")
	workRepo, err := git.PlainInitWithOptions(workDir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.ReferenceName("refs/heads/main"),
		},
	})
	if err != nil {
		t.Fatalf("init work: %v", err)
	}
	workWT, err := workRepo.Worktree()
	if err != nil {
		t.Fatalf("work worktree: %v", err)
	}
	initFile, _ := workWT.Filesystem.Create("init.txt")
	initFile.Close()
	if _, err := workWT.Add("init.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := workWT.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	_, err = git.PlainClone(bareDir, true, &git.CloneOptions{
		URL: "file://" + workDir,
	})
	if err != nil {
		t.Fatalf("clone bare: %v", err)
	}

	// Create local repo by cloning from bare
	localDir := filepath.Join(tmpDir, "local")
	_, err = git.PlainClone(localDir, false, &git.CloneOptions{
		URL: "file://" + bareDir,
	})
	if err != nil {
		t.Fatalf("clone local: %v", err)
	}

	// Make a new commit on the remote via work repo
	newFile, _ := workWT.Filesystem.Create("new.txt")
	newFile.Close()
	if _, err := workWT.Add("new.txt"); err != nil {
		t.Fatalf("add new: %v", err)
	}
	if _, err := workWT.Commit("new commit", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("commit new: %v", err)
	}
	// Push to bare — need to configure remote first
	if _, err := workRepo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{"file://" + bareDir},
	}); err != nil {
		t.Fatalf("create remote on work: %v", err)
	}
	if err := workRepo.Push(&git.PushOptions{
		RemoteName: "origin",
	}); err != nil {
		t.Fatalf("push new: %v", err)
	}

	// Open local repo and test fetch
	localRepo, err := git.PlainOpen(localDir)
	if err != nil {
		t.Fatalf("open local: %v", err)
	}
	localWT, err := localRepo.Worktree()
	if err != nil {
		t.Fatalf("local worktree: %v", err)
	}

	gs := &gitStore{
		repo:    localRepo,
		wt:      localWT,
		fs:      localWT.Filesystem,
		backend: localRepo.Storer,
	}

	err = gs.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		if err := gs.FetchRemote(ctx()); err != nil {
			return fmt.Errorf("fetch: %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("TestFetchRemoteSuccess: %v", err)
	}
}

// TestCommitExistsOnBranchNoCommit tests CommitExistsOnBranch when
// there are no commits on the current branch.
func TestCommitExistsOnBranchNoCommit(t *testing.T) {
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

// TestPushAlreadyUpToDate tests that pushing when already up-to-date
// returns no error (NoErrAlreadyUpToDate is handled as success).
func TestPushAlreadyUpToDate(t *testing.T) {
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	// Create bare remote with initial commit
	workDir := filepath.Join(tmpDir, "work")
	workRepo, err := git.PlainInitWithOptions(workDir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.ReferenceName("refs/heads/main"),
		},
	})
	if err != nil {
		t.Fatalf("init work: %v", err)
	}
	workWT, err := workRepo.Worktree()
	if err != nil {
		t.Fatalf("work worktree: %v", err)
	}
	initFile, _ := workWT.Filesystem.Create("init.txt")
	initFile.Close()
	if _, err := workWT.Add("init.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := workWT.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := workRepo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{"file://" + bareDir},
	}); err != nil {
		t.Fatalf("create remote: %v", err)
	}

	// Clone work repo as bare
	_, err = git.PlainClone(bareDir, true, &git.CloneOptions{
		URL: "file://" + workDir,
	})
	if err != nil {
		t.Fatalf("clone bare: %v", err)
	}

	// Open a gitStore on the work repo to test push via gitstore
	// The work and bare repos already have the same content.
	gs := &gitStore{
		repo:    workRepo,
		wt:      workWT,
		fs:      workWT.Filesystem,
		backend: workRepo.Storer,
	}

	err = gs.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		// Push — should be no-op (already up-to-date)
		if err := gs.PushRemote(ctx()); err != nil {
			return fmt.Errorf("push: %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("TestPushAlreadyUpToDate: %v", err)
	}
}

// TestPullAlreadyUpToDate2 tests that pulling when already up-to-date
// returns no error.
func TestPullAlreadyUpToDate2(t *testing.T) {
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	// Create a bare remote with a commit
	workDir := filepath.Join(tmpDir, "work")
	workRepo, err := git.PlainInitWithOptions(workDir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.ReferenceName("refs/heads/main"),
		},
	})
	if err != nil {
		t.Fatalf("init work: %v", err)
	}
	workWT, err := workRepo.Worktree()
	if err != nil {
		t.Fatalf("work worktree: %v", err)
	}
	initFile, _ := workWT.Filesystem.Create("init.txt")
	initFile.Close()
	if _, err := workWT.Add("init.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := workWT.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	_, err = git.PlainClone(bareDir, true, &git.CloneOptions{
		URL: "file://" + workDir,
	})
	if err != nil {
		t.Fatalf("clone bare: %v", err)
	}

	// Clone from bare to create local repo
	localDir := filepath.Join(tmpDir, "local")
	clonedRepo, err := git.PlainClone(localDir, false, &git.CloneOptions{
		URL: "file://" + bareDir,
	})
	if err != nil {
		t.Fatalf("clone local: %v", err)
	}
	clonedWT, err := clonedRepo.Worktree()
	if err != nil {
		t.Fatalf("get worktree: %v", err)
	}

	gs := &gitStore{
		repo:    clonedRepo,
		wt:      clonedWT,
		fs:      clonedWT.Filesystem,
		backend: clonedRepo.Storer,
	}

	err = gs.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		// First pull — should be no-op (already up-to-date)
		if err := gs.PullAndFastForward(ctx()); err != nil {
			return fmt.Errorf("pull: %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("TestPullAlreadyUpToDate2: %v", err)
	}
}


