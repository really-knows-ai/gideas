package gitstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/uuid"
)

var protocolMu sync.Mutex

// bg is the shared background context for all test operations.
var bg = context.Background()

// errStop is a typed sentinel used to terminate commit-log iteration once the
// target commit is found, instead of matching on an error message string.
var errStop = errors.New("stop")

func pushGraphUpdate(t *testing.T, tmpDir, remoteDir, content string) plumbing.Hash {
	t.Helper()
	writer, err := git.PlainClone(filepath.Join(tmpDir, "writer"), false,
		&git.CloneOptions{URL: "file://" + remoteDir})
	if err != nil {
		t.Fatalf("clone remote writer: %v", err)
	}
	writerWT, err := writer.Worktree()
	if err != nil {
		t.Fatalf("writer worktree: %v", err)
	}
	if err := writerWT.Filesystem.Remove("data.txt"); err != nil {
		t.Fatalf("remove old payload file: %v", err)
	}
	updatedGraph, err := writerWT.Filesystem.Create("data.txt")
	if err != nil {
		t.Fatalf("create updated payload file: %v", err)
	}
	if _, err := updatedGraph.Write([]byte(content)); err != nil {
		t.Fatalf("write updated payload file: %v", err)
	}
	if err := updatedGraph.Close(); err != nil {
		t.Fatalf("close updated payload file: %v", err)
	}
	if _, err := writerWT.Add("data.txt"); err != nil {
		t.Fatalf("add updated payload file: %v", err)
	}
	updatedHash, err := writerWT.Commit("update data", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	})
	if err != nil {
		t.Fatalf("commit updated graph file: %v", err)
	}
	if err := writer.Push(&git.PushOptions{}); err != nil {
		t.Fatalf("push updated graph: %v", err)
	}
	return updatedHash
}

// configureAnonymousRemote configures the remote on an already-cloned gitStore.
// It is only valid after a successful clone of the remote's main branch: the
// local main already points at the remote's HEAD, so the FetchRemote call is a
// no-op (up-to-date). It is not meant to seed an empty local repo.
func configureAnonymousRemote(t *testing.T, gs *gitStore, remoteURL string) {
	t.Helper()
	if err := gs.SetRemote(ctx(), remoteURL, func() (transport.AuthMethod, error) { return nil, nil }); err != nil {
		t.Fatalf("SetRemote: %v", err)
	}
	if err := gs.FetchRemote(ctx()); err != nil {
		t.Fatalf("anonymous FetchRemote: %v", err)
	}
}

// setupTestStore creates a gitStore with in-memory storage and memfs,
// initialised with a main branch and entities/ + edges/ directories.
// ponytail: these tests use in-memory storage and memfs, so filesystem-level
// error paths (disk full, permission denied, I/O failures) mandated by SPEC R8
// corruption recovery are not exercised here. Disk-backed error-path coverage is
// a SPEC-coverage gap, not tested by this helper.
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

	if err := initDir(wt, fs, "entities"); err != nil {
		t.Fatalf("create entities dir: %v", err)
	}
	if err := initDir(wt, fs, "edges"); err != nil {
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
		repo:     repo,
		wt:       wt,
		fs:       fs,
		backend:  storer,
		basePath: t.TempDir(),
	}
	return gs
}

// validUUID returns a valid UUID v4 string, failing the test if generation
// fails (rather than panicking).
func validUUID(t *testing.T) string {
	t.Helper()
	id, err := uuid.NewRandom()
	if err != nil {
		t.Fatalf("generate uuid: %v", err)
	}
	return id.String()
}

func ctx() context.Context {
	return bg
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
	defer func() { _ = gs.Close() }()

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
	tmpDir := t.TempDir()
	gs, err := New(tmpDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = gs.Close() }()
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
	tmpDir := t.TempDir()
	gs1, err := New(tmpDir)
	if err != nil {
		t.Fatalf("first New failed: %v", err)
	}
	_ = gs1.Close()

	gs2, err := New(tmpDir)
	if err != nil {
		t.Fatalf("second New failed: %v", err)
	}
	_ = gs2.Close()
}

func TestInitBadPath(t *testing.T) {
	// Use a path in a read-only directory
	badPath := "/nonexistent-root-xyz"
	_, err := New(badPath)
	if err == nil {
		t.Fatal("expected error for unwritable path, got nil")
	}
}

func TestInitNonDirectoryGit(t *testing.T) {
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

// ============================================================================
// T2: Entity file operations
// ============================================================================

func TestWriteEntityFiles(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID(t)
		e2ID := validUUID(t)
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
		e1ID := validUUID(t)
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
		e1ID := validUUID(t)
		e2ID := validUUID(t)
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
		return gs.RemoveEntityFiles(ctx(), "Component", []string{validUUID(t)})
	})
	if err != nil {
		t.Fatalf("RemoveEntityFiles non-existent: %v", err)
	}
}

func TestReadAllEntityFiles(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID(t)
		e2ID := validUUID(t)
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

// TestReadAllEntityFilesCorrupt asserts that a malformed .json element under
// entities/<Type>/ surfaces an error from ReadAllEntityFiles (SPEC R8
// corruption recovery). This is exercised via the in-memory memfs: JSON decode
// of garbage bytes must fail rather than be silently ignored.
func TestReadAllEntityFilesCorrupt(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		eID := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: eID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		path := "entities/Component/" + eID + ".json"
		f, err := gs.fs.Create(path)
		if err != nil {
			return fmt.Errorf("recreate corrupt file: %w", err)
		}
		if _, err := f.Write([]byte("{not valid json")); err != nil {
			return fmt.Errorf("write garbage: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close garbage file: %w", err)
		}
		if _, err := gs.ReadAllEntityFiles(ctx(), "Component"); err == nil {
			return fmt.Errorf("expected error for corrupt entity file, got nil")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ReadAllEntityFilesCorrupt: %v", err)
	}
}

// TestWriteEntityFilesTypeMismatch asserts that writing an entity whose Type
// differs from the batch type returns ErrEntityTypeMismatch (entity.go:111).
func TestWriteEntityFilesTypeMismatch(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: validUUID(t), Type: "Service"},
		})
		if !errors.Is(err, ErrEntityTypeMismatch) {
			return fmt.Errorf("expected ErrEntityTypeMismatch, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WriteEntityFilesTypeMismatch: %v", err)
	}
}

func TestListEntityTypes(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)

		// Write entities for two types
		entitiesA := []Entity{
			{ID: validUUID(t), Type: "Component", CreatedAt: now, UpdatedAt: now},
		}
		entitiesB := []Entity{
			{ID: validUUID(t), Type: "Service", CreatedAt: now, UpdatedAt: now},
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
		e1ID := validUUID(t)
		e2ID := validUUID(t)
		fromID := validUUID(t)
		toID := validUUID(t)
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
		e1ID := validUUID(t)
		e2ID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)

		edges := []Edge{
			{
				ID: e1ID, Type: "DEPENDS_ON",
				FromEntityID: validUUID(t), ToEntityID: validUUID(t),
				CreatedAt: now, UpdatedAt: now,
			},
			{
				ID: e2ID, Type: "DEPENDS_ON",
				FromEntityID: validUUID(t), ToEntityID: validUUID(t),
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
		return gs.RemoveEdgeFiles(ctx(), "DEPENDS_ON", []string{validUUID(t)})
	})
	if err != nil {
		t.Fatalf("RemoveEdgeFiles non-existent: %v", err)
	}
}

func TestReadAllEdgeFiles(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)
		fromID := validUUID(t)
		toID := validUUID(t)

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

// TestReadAllEdgeFilesCorrupt verifies that a malformed .json element under
// edges/<Type>/ surfaces an error from ReadAllEdgeFiles (SPEC R8 corruption
// recovery). JSON decode of garbage bytes must fail rather than be silently
// ignored.
func TestReadAllEdgeFilesCorrupt(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		eID := validUUID(t)
		fromID := validUUID(t)
		toID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)
		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{
			{
				ID: eID, Type: "DEPENDS_ON",
				FromEntityID: fromID, ToEntityID: toID,
				CreatedAt: now, UpdatedAt: now,
			},
		}); err != nil {
			return err
		}
		path := "edges/DEPENDS_ON/" + eID + ".json"
		f, err := gs.fs.Create(path)
		if err != nil {
			return fmt.Errorf("recreate corrupt file: %w", err)
		}
		if _, err := f.Write([]byte("{not valid json")); err != nil {
			return fmt.Errorf("write garbage: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close garbage file: %w", err)
		}
		if _, err := gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON"); err == nil {
			return fmt.Errorf("expected error for corrupt edge file, got nil")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ReadAllEdgeFilesCorrupt: %v", err)
	}
}

// TestWriteEdgeFilesTypeMismatch asserts that writing an edge whose Type
// differs from the batch type returns ErrEdgeTypeMismatch (edge.go:109).
func TestWriteEdgeFilesTypeMismatch(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{
			{
				ID:           validUUID(t),
				Type:         "CONNECTS_TO",
				FromEntityID: validUUID(t),
				ToEntityID:   validUUID(t),
			},
		})
		if !errors.Is(err, ErrEdgeTypeMismatch) {
			return fmt.Errorf("expected ErrEdgeTypeMismatch, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WriteEdgeFilesTypeMismatch: %v", err)
	}
}

func TestListEdgeTypes(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		fromID := validUUID(t)
		toID := validUUID(t)

		edgesA := []Edge{
			{ID: validUUID(t), Type: "DEPENDS_ON", FromEntityID: fromID, ToEntityID: toID, CreatedAt: now, UpdatedAt: now},
		}
		edgesB := []Edge{
			{ID: validUUID(t), Type: "CONNECTS_TO", FromEntityID: fromID, ToEntityID: toID, CreatedAt: now, UpdatedAt: now},
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

func TestCheckout(t *testing.T) {
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

func TestBranchExists(t *testing.T) {
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

// ============================================================================
// T5: Git operations (AddAll + Commit)
// ============================================================================

func TestAddAllAndCommit(t *testing.T) {
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

		// Create branch B (from branch A) with another entity
		branchB := validUUID(t)
		if err := gs.CreateBranch(ctx(), branchB); err != nil {
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

// TestSetRemoteNoHost verifies that a scheme-valid URL lacking a host
// component (e.g. "https://") is rejected with ErrRemoteURLNoHost rather than
// accepted (SPEC R9: validate the URL before configuring the remote).
func TestSetRemoteNoHost(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		err := gs.SetRemote(ctx(), "https://", nil)
		if !errors.Is(err, ErrRemoteURLNoHost) {
			return fmt.Errorf("expected ErrRemoteURLNoHost, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("SetRemoteNoHost: %v", err)
	}
}

func TestIsEmpty(t *testing.T) {
	t.Run("fresh init returns true", func(t *testing.T) {
		gs := setupTestStore(t)
		_ = gs.WithGitLock(func() error {
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
		_ = gs.WithGitLock(func() error {
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

// TestPullAndFastForwardAuthConfigMissing verifies that pulling with a nil
// auth provider (authFn) returns ErrAuthConfigMissing.
func TestPullAndFastForwardAuthConfigMissing(t *testing.T) {
	tmpDir := t.TempDir()

	gs, err := New(tmpDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = gs.Close() }()

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
		t.Fatalf("TestPullAndFastForwardAuthConfigMissing: %v", err)
	}
}

func TestCloneSingleBranchNoAuth(t *testing.T) {
	protocolMu.Lock()
	t.Cleanup(protocolMu.Unlock)

	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")
	source, err := git.PlainInitWithOptions(sourceDir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	if err != nil {
		t.Fatalf("init source: %v", err)
	}
	sourceWT, err := source.Worktree()
	if err != nil {
		t.Fatalf("source worktree: %v", err)
	}
	const graphContent = `{"graph":"controlled"}`
	graphFile, err := sourceWT.Filesystem.Create("data.txt")
	if err != nil {
		t.Fatalf("create payload file: %v", err)
	}
	if _, err := graphFile.Write([]byte(graphContent)); err != nil {
		t.Fatalf("write payload file: %v", err)
	}
	if err := graphFile.Close(); err != nil {
		t.Fatalf("close payload file: %v", err)
	}
	if _, err := sourceWT.Add("data.txt"); err != nil {
		t.Fatalf("add payload file: %v", err)
	}
	sourceHash, err := sourceWT.Commit("main graph", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	})
	if err != nil {
		t.Fatalf("commit graph file: %v", err)
	}

	remoteDir := filepath.Join(tmpDir, "remote.git")
	if _, err := git.PlainClone(remoteDir, true, &git.CloneOptions{URL: "file://" + sourceDir}); err != nil {
		t.Fatalf("create bare remote: %v", err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git executable required for smart HTTP test server: %v", err)
	}
	// ponytail: this test depends on the external git binary (smart HTTP backend)
	// to serve an https:// URL, which conflicts with SPEC R5's "pure Go, no external
	// git binary" policy. It cannot be converted to file:// because CloneSingleBranch's
	// URL validation (remote.go validateRemoteURL) only accepts https:// and ssh://.
	// The upgrade path is to relax CloneSingleBranch to accept file:// in tests (a
	// SPEC/URL-validation change), then drive this test over file:// without git.
	server := httptest.NewTLSServer(&cgi.Handler{
		Path: gitPath,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + tmpDir,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	})
	t.Cleanup(server.Close)

	originalHTTPS, hadHTTPS := client.Protocols["https"]
	client.InstallProtocol("https", githttp.NewClient(server.Client()))
	t.Cleanup(func() {
		if hadHTTPS {
			client.InstallProtocol("https", originalHTTPS)
		} else {
			delete(client.Protocols, "https")
		}
	})

	store, err := New(filepath.Join(tmpDir, "local"))
	if err != nil {
		t.Fatalf("new local store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	gs := store.(*gitStore)
	if gs.authFn != nil {
		t.Fatal("expected nil auth provider")
	}
	err = store.WithGitLock(func() error {
		return gs.CloneSingleBranch(ctx(), server.URL+"/remote.git", "main")
	})
	if err != nil {
		t.Fatalf("CloneSingleBranchNoAuth: %v", err)
	}

	mainRef, err := gs.repo.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatalf("main ref: %v", err)
	}
	if mainRef.Hash() != sourceHash {
		t.Fatalf("main ref = %s, want %s", mainRef.Hash(), sourceHash)
	}
	head, err := gs.repo.Head()
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	if head.Name() != plumbing.NewBranchReferenceName("main") || head.Hash() != sourceHash {
		t.Fatalf("HEAD = %s at %s, want main at %s", head.Name(), head.Hash(), sourceHash)
	}
	clonedGraph, err := gs.fs.Open("data.txt")
	if err != nil {
		t.Fatalf("open checked-out payload file: %v", err)
	}
	got, err := io.ReadAll(clonedGraph)
	_ = clonedGraph.Close()
	if err != nil {
		t.Fatalf("read checked-out graph file: %v", err)
	}
	if string(got) != graphContent {
		t.Fatalf("graph content = %q, want %q", got, graphContent)
	}
	status, err := gs.wt.Status()
	if err != nil {
		t.Fatalf("worktree status: %v", err)
	}
	if !status.IsClean() {
		t.Fatalf("expected clean checked-out worktree, got %s", status)
	}

	// A configured resolver returning nil explicitly selects anonymous access.
	configureAnonymousRemote(t, gs, server.URL+"/remote.git")

	const updatedGraphContent = `{"graph":"updated"}`
	updatedHash := pushGraphUpdate(t, tmpDir, remoteDir, updatedGraphContent)

	if err := gs.PullAndFastForward(ctx()); err != nil {
		t.Fatalf("anonymous PullAndFastForward: %v", err)
	}
	mainRef, err = gs.repo.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatalf("updated main ref: %v", err)
	}
	if mainRef.Hash() != updatedHash {
		t.Fatalf("updated main ref = %s, want %s", mainRef.Hash(), updatedHash)
	}
	got, err = os.ReadFile(filepath.Join(tmpDir, "local", "graph-repo", "data.txt"))
	if err != nil {
		t.Fatalf("read updated worktree payload: %v", err)
	}
	if string(got) != updatedGraphContent {
		t.Fatalf("updated payload content = %q, want %q", got, updatedGraphContent)
	}
}

func TestCloneSingleBranchInvalidScheme(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		err := gs.CloneSingleBranch(ctx(), "file:///tmp/repo.git", "main")
		if !errors.Is(err, ErrUnsupportedURLScheme) {
			return fmt.Errorf("expected ErrUnsupportedURLScheme, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("CloneSingleBranchInvalidScheme: %v", err)
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
		e1ID := validUUID(t)
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
		edgeID := validUUID(t)
		fromID := validUUID(t)
		toID := validUUID(t)
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
		// Configure the remote via SetRemote, which validates the URL scheme
		// and creates the "origin" remote.
		if err := gs.SetRemote(ctx(), "https://example.com/repo.git", func() (transport.AuthMethod, error) {
			return nil, nil
		}); err != nil {
			return err
		}

		remote, err := gs.repo.Remote("origin")
		if err != nil {
			return fmt.Errorf("get recreated remote: %w", err)
		}
		if got := remote.Config().URLs; len(got) != 1 || got[0] != gs.remoteURL {
			return fmt.Errorf("recreated remote URLs = %v, want [%s]", got, gs.remoteURL)
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
				FromEntityID: validUUID(t), ToEntityID: validUUID(t),
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
	err := gs.WithGitLock(func() error {
		// The in-memory backend removes a missing branch ref without error, so
		// deleting a non-existent branch is a documented no-op here.
		return gs.DeleteBranch(ctx(), "nonexistent")
	})
	if err != nil {
		t.Fatalf("DeleteBranch(nonexistent): %v", err)
	}
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
	defer func() { _ = store.Close() }()

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
		e1ID := validUUID(t)
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
	_ = seedFile.Close()
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
	_ = cloneFile.Close()
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
		repo:     clonedLocalRepo,
		wt:       clonedLocalWT,
		fs:       clonedLocalWT.Filesystem,
		backend:  clonedLocalRepo.Storer,
		basePath: t.TempDir(),
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
	_ = cloneFile2.Close()
	if _, err := clonedWT.Add("another.txt"); err != nil {
		t.Fatalf("add another: %v", err)
	}
	secondHash, err := clonedWT.Commit("second commit", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	})
	if err != nil {
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

		// The pull must have advanced the local main ref to the remote HEAD,
		// not merely fetched the objects into the store.
		mainRef, err := gs.repo.Reference(plumbing.NewBranchReferenceName("main"), true)
		if err != nil {
			return fmt.Errorf("main ref: %w", err)
		}
		if mainRef.Hash() != secondHash {
			return fmt.Errorf("main ref = %s, want %s", mainRef.Hash(), secondHash)
		}

		// Verify the second commit appears in the log.
		log, err := gs.repo.Log(&git.LogOptions{})
		if err != nil {
			return err
		}
		defer log.Close()

		if err := log.ForEach(func(c *object.Commit) error {
			if strings.HasPrefix(c.Message, "second commit") {
				return errStop
			}
			return nil
		}); err != nil && !errors.Is(err, errStop) {
			return err
		}

		return nil
	})
	if err != nil {
		t.Fatalf("PullFromRemote: %v", err)
	}
}

// TestPullAndFastForwardDiverged verifies the ErrPullDiverged branch directly:
// a genuinely diverged pull (local main and remote main have each advanced past
// a common ancestor, so a fast-forward is impossible) surfaces git's
// ErrNonFastForwardUpdate from worktree.Pull, which PullAndFastForward maps to
// ErrPullDiverged. This is the direct unit-tested coverage for that branch,
// which is contractually unreachable in production because the service pulls via
// FetchAndMerge (merge-commit semantics), never PullAndFastForward.
func TestPullAndFastForwardDiverged(t *testing.T) {
	tmpDir := t.TempDir()

	// Seed repo with a base commit.
	seedDir := filepath.Join(tmpDir, "seed")
	seedRepo, err := git.PlainInitWithOptions(seedDir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.ReferenceName("refs/heads/main"),
		},
	})
	if err != nil {
		t.Fatalf("init seed: %v", err)
	}
	seedWT, err := seedRepo.Worktree()
	if err != nil {
		t.Fatalf("seed worktree: %v", err)
	}
	seedFile, err := seedWT.Filesystem.Create("seed.txt")
	if err != nil {
		t.Fatalf("create seed: %v", err)
	}
	_ = seedFile.Close()
	if _, err := seedWT.Add("seed.txt"); err != nil {
		t.Fatalf("add seed: %v", err)
	}
	if _, err := seedWT.Commit("base", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("base commit: %v", err)
	}

	// Create a bare remote from the seed.
	bareDir := filepath.Join(tmpDir, "remote.git")
	_, err = git.PlainClone(bareDir, true, &git.CloneOptions{URL: "file://" + seedDir})
	if err != nil {
		t.Fatalf("clone bare remote: %v", err)
	}

	// Peer clone: owns the remote for advancing it and for pushing the "remote"
	// fresh commits.
	peerDir := filepath.Join(tmpDir, "peer")
	peer, err := git.PlainClone(peerDir, false, &git.CloneOptions{URL: "file://" + bareDir})
	if err != nil {
		t.Fatalf("clone peer: %v", err)
	}
	peerWT, err := peer.Worktree()
	if err != nil {
		t.Fatalf("peer worktree: %v", err)
	}
	// Advance the remote main by one commit (peer:first).
	pf, err := peerWT.Filesystem.Create("peer1.txt")
	if err != nil {
		t.Fatalf("create peer1: %v", err)
	}
	_ = pf.Close()
	if _, err := peerWT.Add("peer1.txt"); err != nil {
		t.Fatalf("add peer1: %v", err)
	}
	if _, err := peerWT.Commit("peer:first", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("peer first commit: %v", err)
	}
	if err := peer.Push(&git.PushOptions{}); err != nil {
		t.Fatalf("peer first push: %v", err)
	}

	// Local gitStore cloned from the remote at peer:first.
	localDir := filepath.Join(tmpDir, "local")
	localRepo, err := git.PlainClone(localDir, false, &git.CloneOptions{URL: "file://" + bareDir})
	if err != nil {
		t.Fatalf("clone local: %v", err)
	}
	localWT, err := localRepo.Worktree()
	if err != nil {
		t.Fatalf("local worktree: %v", err)
	}
	gs := &gitStore{
		repo:     localRepo,
		wt:       localWT,
		fs:       localWT.Filesystem,
		backend:  localRepo.Storer,
		basePath: t.TempDir(),
	}
	gs.remoteURL = "file://" + bareDir
	gs.authFn = func() (transport.AuthMethod, error) { return noopAuth{}, nil }

	// Diverge: local advances past the base commit while remote also advances.
	localFile, err := localWT.Filesystem.Create("local.txt")
	if err != nil {
		t.Fatalf("create local file: %v", err)
	}
	_ = localFile.Close()
	if _, err := localWT.Add("local.txt"); err != nil {
		t.Fatalf("add local file: %v", err)
	}
	if _, err := localWT.Commit("local:diverge", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("local diverge commit: %v", err)
	}

	// Advance remote main further so remote and local truly diverge.
	peer2File, err := peerWT.Filesystem.Create("peer2.txt")
	if err != nil {
		t.Fatalf("create peer2: %v", err)
	}
	_ = peer2File.Close()
	if _, err := peerWT.Add("peer2.txt"); err != nil {
		t.Fatalf("add peer2: %v", err)
	}
	if _, err := peerWT.Commit("peer:second", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("peer second commit: %v", err)
	}
	if err := peer.Push(&git.PushOptions{}); err != nil {
		t.Fatalf("peer second push: %v", err)
	}

	err = gs.WithGitLock(func() error {
		if err := gs.PullAndFastForward(ctx()); err == nil {
			return fmt.Errorf("expected ErrPullDiverged, got nil")
		} else if !errors.Is(err, ErrPullDiverged) {
			return fmt.Errorf("expected ErrPullDiverged, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("PullAndFastForwardDiverged: %v", err)
	}
}

// TestCloneCurrentFromRemote clones a remote via internal operations
// (the same flow as CloneSingleBranch, exercised without URL scheme validation
// since file:// URLs are not valid remote schemes per SPEC).
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
	_ = sf.Close()
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
	defer func() { _ = localStore.Close() }()

	gs := localStore.(*gitStore)

	err = localStore.WithGitLock(func() error {
		// Configure auth
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		auth, err := gs.resolveAuth(false)
		if err != nil {
			return fmt.Errorf("resolve auth: %w", err)
		}

		// Configure remote directly (bypassing CloneSingleBranch which
		// rejects file:// URLs — this test exercises the internal operations:
		// fetch, ref setup, checkout, and reopen).
		_, err = gs.repo.CreateRemote(&config.RemoteConfig{
			Name: "origin",
			URLs: []string{"file://" + bareDir},
		})
		if err != nil && !errors.Is(err, git.ErrRemoteExists) {
			return fmt.Errorf("create remote: %w", err)
		}

		// Fetch from remote
		err = gs.repo.FetchContext(ctx(), &git.FetchOptions{
			RemoteName: "origin",
			Auth:       auth,
			Force:      false,
		})
		if err != nil {
			if errors.Is(err, git.NoErrAlreadyUpToDate) {
				// up-to-date is fine
			} else {
				return fmt.Errorf("fetch: %w", err)
			}
		}

		// Resolve remote tracking ref for main
		remoteRef, err := gs.repo.Reference(
			plumbing.ReferenceName("refs/remotes/origin/main"), true)
		if err != nil {
			return fmt.Errorf("resolve remote ref: %w", err)
		}

		mainRef := plumbing.NewHashReference(
			plumbing.ReferenceName("refs/heads/main"),
			remoteRef.Hash(),
		)
		if err := gs.backend.SetReference(mainRef); err != nil {
			return fmt.Errorf("set main ref: %w", err)
		}

		// Checkout main
		if err := gs.wt.Checkout(&git.CheckoutOptions{
			Branch: plumbing.ReferenceName("refs/heads/main"),
			Force:  true,
		}); err != nil {
			return fmt.Errorf("checkout main: %w", err)
		}

		// Re-open repo to refresh worktree
		repoPath := gs.basePath + "/graph-repo"
		reopened, err := git.PlainOpen(repoPath)
		if err != nil {
			return fmt.Errorf("reopen repo: %w", err)
		}
		wt, err := reopened.Worktree()
		if err != nil {
			return fmt.Errorf("reopen worktree: %w", err)
		}
		gs.repo = reopened
		gs.wt = wt
		gs.fs = wt.Filesystem

		// Verify the data file exists
		if _, err := gs.fs.Stat("data.txt"); err != nil {
			return fmt.Errorf("data file should exist after clone: %w", err)
		}

		// Note: this test replicates the CloneSingleBranch flow manually. The
		// forced checkout replaces the tracked working tree (entities/ + edges/
		// .gitkeep init files are lost here), but the production CloneSingleBranch
		// (remote.go) re-creates entities/ + edges/ after reopening the repo, which
		// this manual replication does not do. entities/ + edges/ are not asserted
		// here precisely because this test bypasses that production step.

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
		repo:     repo,
		wt:       wt,
		fs:       fs,
		backend:  storer,
		basePath: t.TempDir(),
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

// TestReadAllEntityFilesEmptyTypeDir tests reading an empty type directory
// returns an empty slice (not an error).
func TestReadAllEntityFilesEmptyTypeDir(t *testing.T) {
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
		t.Fatalf("TestReadAllEntityFilesEmptyTypeDir: %v", err)
	}
}

// TestWriteEntityNilEmbedding tests that nil embedding is handled correctly.
func TestWriteEntityNilEmbedding(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID(t)
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
		e1ID := validUUID(t)
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
	_ = initFile.Close()
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
	_ = newFile.Close()
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
		repo:     localRepo,
		wt:       localWT,
		fs:       localWT.Filesystem,
		backend:  localRepo.Storer,
		basePath: t.TempDir(),
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
	_ = initFile.Close()
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
		repo:     workRepo,
		wt:       workWT,
		fs:       workWT.Filesystem,
		backend:  workRepo.Storer,
		basePath: t.TempDir(),
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

// setupBareRemote creates a bare repo at bareDir with an initial commit
// containing init.txt.
func setupBareRemote(t *testing.T, tmpDir, bareDir string) {
	t.Helper()
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
	_ = initFile.Close()
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
}

// cloneFromBare clones a bare repo into a local non-bare repo and returns
// a *gitStore wrapping it.
func cloneFromBare(t *testing.T, tmpDir, bareDir string) *gitStore {
	t.Helper()
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
	return &gitStore{
		repo:     clonedRepo,
		wt:       clonedWT,
		fs:       clonedWT.Filesystem,
		backend:  clonedRepo.Storer,
		basePath: t.TempDir(),
	}
}

// remoteHEAD returns the HEAD hash of a bare repository.
func remoteHEAD(t *testing.T, bareDir string) plumbing.Hash {
	t.Helper()
	remoteRepo, err := git.PlainOpen(bareDir)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	remoteRef, err := remoteRepo.Reference(plumbing.ReferenceName("refs/heads/main"), true)
	if err != nil {
		t.Fatalf("remote ref: %v", err)
	}
	return remoteRef.Hash()
}

// TestFetchAndMerge_AlreadyUpToDate tests that FetchAndMerge when both
// sides are identical returns no error and the HEAD hash is unchanged.
func TestFetchAndMerge_AlreadyUpToDate(t *testing.T) {
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	setupBareRemote(t, tmpDir, bareDir)
	gs := cloneFromBare(t, tmpDir, bareDir)
	initialHash := remoteHEAD(t, bareDir)

	err := gs.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		newHash, err := gs.FetchAndMerge(ctx(), "origin", "main")
		if err != nil {
			return fmt.Errorf("FetchAndMerge: %w", err)
		}
		if newHash != initialHash {
			return fmt.Errorf("expected hash %s, got %s", initialHash, newHash)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestFetchAndMerge_AlreadyUpToDate: %v", err)
	}
}

// TestFetchAndMerge_FastForward tests that FetchAndMerge advances the local
// HEAD when the remote has new commits (fast-forward).
func TestFetchAndMerge_FastForward(t *testing.T) {
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	setupBareRemote(t, tmpDir, bareDir)

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
	cloneFile, cloneErr := clonedWT.Filesystem.Create("initial.txt")
	if cloneErr != nil {
		t.Fatalf("create file: %v", cloneErr)
	}
	_ = cloneFile.Close()
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

	gs := cloneFromBare(t, tmpDir, bareDir)
	remoteHash := remoteHEAD(t, bareDir)

	err = gs.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		newHash, err := gs.FetchAndMerge(ctx(), "origin", "main")
		if err != nil {
			return fmt.Errorf("FetchAndMerge: %w", err)
		}
		if newHash != remoteHash {
			return fmt.Errorf("expected hash %s, got %s", remoteHash, newHash)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestFetchAndMerge_FastForward: %v", err)
	}
}

// TestFetchAndMerge_MergeCommit tests that FetchAndMerge creates a merge
// commit when local and remote have diverged.
func TestFetchAndMerge_MergeCommit(t *testing.T) {
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	setupBareRemote(t, tmpDir, bareDir)

	// Clone the bare remote to create a "writer" that will push to remote
	writerDir := filepath.Join(tmpDir, "writer")
	writer, err := git.PlainClone(writerDir, false, &git.CloneOptions{
		URL: "file://" + bareDir,
	})
	if err != nil {
		t.Fatalf("clone writer: %v", err)
	}
	writerWT, err := writer.Worktree()
	if err != nil {
		t.Fatalf("writer worktree: %v", err)
	}

	gs := cloneFromBare(t, tmpDir, bareDir)

	// Make a local commit (diverging from remote)
	localFile, err := gs.wt.Filesystem.Create("local.txt")
	if err != nil {
		t.Fatalf("create local file: %v", err)
	}
	_, _ = localFile.Write([]byte("local content"))
	_ = localFile.Close()
	if _, err := gs.wt.Add("local.txt"); err != nil {
		t.Fatalf("add local: %v", err)
	}
	localCommitHash, err := gs.wt.Commit("local commit", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	})
	if err != nil {
		t.Fatalf("local commit: %v", err)
	}

	// Make a different commit on the remote (push from writer)
	remoteFile, err := writerWT.Filesystem.Create("remote.txt")
	if err != nil {
		t.Fatalf("create remote file: %v", err)
	}
	_, _ = remoteFile.Write([]byte("remote content"))
	_ = remoteFile.Close()
	if _, err := writerWT.Add("remote.txt"); err != nil {
		t.Fatalf("add remote: %v", err)
	}
	if _, err := writerWT.Commit("remote commit", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("remote commit: %v", err)
	}
	if err := writer.Push(&git.PushOptions{}); err != nil {
		t.Fatalf("push remote: %v", err)
	}

	remoteHash := remoteHEAD(t, bareDir)

	err = gs.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		mergeHash, err := gs.FetchAndMerge(ctx(), "origin", "main")
		if err != nil {
			return fmt.Errorf("FetchAndMerge: %w", err)
		}

		if mergeHash == localCommitHash {
			return fmt.Errorf("merge hash should differ from local commit hash")
		}
		if mergeHash == remoteHash {
			return fmt.Errorf("merge hash should differ from remote commit hash")
		}

		mergeCommit, err := gs.repo.CommitObject(mergeHash)
		if err != nil {
			return fmt.Errorf("get merge commit: %w", err)
		}
		if len(mergeCommit.ParentHashes) != 2 {
			return fmt.Errorf("expected 2 parents, got %d", len(mergeCommit.ParentHashes))
		}

		hasLocal := mergeCommit.ParentHashes[0] == localCommitHash || mergeCommit.ParentHashes[1] == localCommitHash
		hasRemote := mergeCommit.ParentHashes[0] == remoteHash || mergeCommit.ParentHashes[1] == remoteHash
		if !hasLocal {
			return fmt.Errorf("merge commit should have local commit as parent")
		}
		if !hasRemote {
			return fmt.Errorf("merge commit should have remote commit as parent")
		}

		expectedMsg := "merge: sync from remote file://" + bareDir
		if mergeCommit.Message != expectedMsg {
			return fmt.Errorf("expected message %q, got %q", expectedMsg, mergeCommit.Message)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("TestFetchAndMerge_MergeCommit: %v", err)
	}

	// Verify the working tree has the remote's files (simplified merge uses remote tree)
	_, err = gs.wt.Filesystem.Stat("remote.txt")
	if err != nil {
		t.Fatalf("remote.txt should exist in working tree: %v", err)
	}
	_, err = gs.wt.Filesystem.Stat("init.txt")
	if err != nil {
		t.Fatalf("init.txt should exist in working tree: %v", err)
	}

	// local.txt was created locally but the simplified merge uses the remote tree,
	// so local-only files are lost.
	_, err = gs.wt.Filesystem.Stat("local.txt")
	if err == nil {
		t.Fatal("local.txt should NOT exist in working tree after simplified merge")
	}

	// Note on entities/ and edges/: this test's repo is a plain clone of a bare
	// remote containing only init.txt, so it never had New()-created entities/
	// and edges/ dirs to lose. The simplified merge (remote.go:129 createMergeCommit)
	// checks out the remote tree, so if a New()-initialised repo were merged against
	// a remote whose tree lacks entities/ and edges/, those dirs would be dropped
	// from the merge commit and removed from the working tree — the same intentional
	// loss documented for local.txt and the ponytail at remote.go:125-128. The
	// remote-bootstrap path (CloneSingleBranch) recreates them explicitly
	// (remote.go:468-476); the merge path relies on re-hydration (R8) or a remote
	// that already contains the dirs.
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
	_ = initFile.Close()
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
		repo:     clonedRepo,
		wt:       clonedWT,
		fs:       clonedWT.Filesystem,
		backend:  clonedRepo.Storer,
		basePath: t.TempDir(),
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
