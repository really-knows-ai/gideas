package gitstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/uuid"
)

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
// local main already points at the remote's HEAD, so the FetchAndMerge call is a
// no-op (up-to-date). It is not meant to seed an empty local repo.
func configureAnonymousRemote(t *testing.T, gs *gitStore, remoteURL string) {
	t.Helper()
	if err := gs.SetRemote(ctx(), remoteURL, func() (transport.AuthMethod, error) { return nil, nil }); err != nil {
		t.Fatalf("SetRemote: %v", err)
	}
	if _, err := gs.FetchAndMerge(ctx(), "origin", "main"); err != nil {
		t.Fatalf("anonymous FetchAndMerge: %v", err)
	}
}

// setupTestStore creates a gitStore with in-memory storage and memfs,
// initialised with a main branch and entities/ + edges/ directories.
// The SPEC R8 filesystem-error paths (disk full, permission denied, I/O
// failures) are covered by the disk-backed ladybug store tests
// (ladybug_test.go TestRehydrateFiles_IOErrorFailsLoudly).
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

	// Commit init, mirroring New()'s init commit (gitstore.go): isInitCommit
	// (remote.go) requires the "cartographer" author, so a "test"-authored
	// init would make IsEmpty report non-empty for an init-only repo, breaking
	// the SPEC R10 clone-vs-pull decision.
	if _, err := wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "cartographer",
			Email: "cartographer@foundry.flow",
		},
		Committer: &object.Signature{
			Name:  "cartographer",
			Email: "cartographer@foundry.flow",
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

// committedTreeFile returns nil when path is present in HEAD's committed
// tree, or object.ErrFileNotFound when the path is absent (also when an
// intermediate directory is missing — tree.File wraps both). Asserting
// against the committed tree rather than only the working tree pins the
// durable-deletion contract: RemoveEntityFiles/RemoveEdgeFiles remove from
// the working tree, and only AddAll + Commit record the deletion in
// committed history.
func committedTreeFile(gs *gitStore, path string) error {
	head, err := gs.repo.Head()
	if err != nil {
		return err
	}
	commit, err := gs.repo.CommitObject(head.Hash())
	if err != nil {
		return err
	}
	tree, err := commit.Tree()
	if err != nil {
		return err
	}
	_, err = tree.File(path)
	return err
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

// TestNewEmptyBasePath pins New's exported ErrEmptyBasePath guard
// (gitstore.go New's "" check): a store must never be constructed with an
// empty base path, and callers must be able to distinguish the guard via
// errors.Is rather than a nil-interface or ad-hoc error string.
func TestNewEmptyBasePath(t *testing.T) {
	store, err := New("")
	if !errors.Is(err, ErrEmptyBasePath) {
		t.Fatalf("New(\"\") = %v, want ErrEmptyBasePath", err)
	}
	if store != nil {
		t.Fatalf("New(\"\") returned non-nil store %T, want nil", store)
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
		// Commit the baseline so the committed tree pins both files before
		// removal (the durable-deletion contract is asserted against the
		// committed tree, not only the working tree).
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "pre-remove"); err != nil {
			return err
		}
		if err := committedTreeFile(gs, "entities/Component/"+e1ID+".json"); err != nil {
			return fmt.Errorf("baseline file %s missing from committed tree: %w", e1ID, err)
		}

		// Remove the first entity
		if err := gs.RemoveEntityFiles(ctx(), "Component", []string{e1ID}); err != nil {
			return err
		}

		// Verify e1 is gone, e2 remains (working tree)
		_, err1 := gs.fs.Stat("entities/Component/" + e1ID + ".json")
		if err1 == nil {
			return fmt.Errorf("removed file %s still exists", e1ID)
		}
		_, err2 := gs.fs.Stat("entities/Component/" + e2ID + ".json")
		if err2 != nil {
			return fmt.Errorf("remaining file %s missing: %w", e2ID, err2)
		}

		// AddAll + Commit the staged deletion: the committed tree must no
		// longer contain the removed file while the remaining file stays
		// present (SPEC durable-deletion contract: fs.Remove -> AddAll ->
		// Commit leaves the file gone from the committed tree).
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "post-remove"); err != nil {
			return err
		}
		if err := committedTreeFile(gs, "entities/Component/"+e1ID+".json"); !errors.Is(err, object.ErrFileNotFound) {
			return fmt.Errorf("removed file %s still present in committed tree after commit: %v", e1ID, err)
		}
		if err := committedTreeFile(gs, "entities/Component/"+e2ID+".json"); err != nil {
			return fmt.Errorf("remaining file %s missing from committed tree: %w", e2ID, err)
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

// TestReadAllEntityFilesIDFilenameConflict: a JSON entity file whose embedded id
// differs from its filename (external corruption) must surface an error, not be
// silently loaded under the conflicting id (SPEC R8 corruption recovery). Without
// the guard, R8 re-hydration would load the entity under a never-written id while
// the intended-UUID file disappears from view.
func TestReadAllEntityFilesIDFilenameConflict(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		eID := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: eID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		// Rewrite the file's embedded id to a different (still valid) UUID under
		// the original filename, simulating filename/body divergence.
		otherID := validUUID(t)
		path := "entities/Component/" + eID + ".json"
		ej := struct {
			ID         uuid.UUID         `json:"id"`
			Type       string            `json:"type"`
			Properties map[string]string `json:"properties,omitempty"`
			CreatedAt  time.Time         `json:"created_at"`
			UpdatedAt  time.Time         `json:"updated_at"`
		}{ID: uuid.MustParse(otherID), Type: "Component", CreatedAt: now, UpdatedAt: now}
		data, err := json.Marshal(ej)
		if err != nil {
			return err
		}
		f, err := gs.fs.Create(path)
		if err != nil {
			return fmt.Errorf("recreate conflicting entity file: %w", err)
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return fmt.Errorf("write conflicting entity file: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close conflicting entity file: %w", err)
		}
		if _, err := gs.ReadAllEntityFiles(ctx(), "Component"); err == nil {
			return fmt.Errorf("expected error for conflicting entity id, got nil")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllEntityFilesIDFilenameConflict: %v", err)
	}
}

// TestReadAllEntityFilesTypeDirectoryMismatch: a JSON entity file under
// entities/<Type>/ whose embedded type disagrees with the directory (external
// corruption) must surface ErrEntityTypeMismatch, not be silently loaded under
// a never-written type. This mirrors the write path (writeEntityFile rejects
// the mismatch) and the id-vs-filename guard above.
func TestReadAllEntityFilesTypeDirectoryMismatch(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		eID := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: eID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		// Rewrite the file's embedded type to a different type under the
		// original directory, simulating type/body divergence.
		path := "entities/Component/" + eID + ".json"
		ej := struct {
			ID         uuid.UUID         `json:"id"`
			Type       string            `json:"type"`
			Properties map[string]string `json:"properties,omitempty"`
			CreatedAt  time.Time         `json:"created_at"`
			UpdatedAt  time.Time         `json:"updated_at"`
		}{ID: uuid.MustParse(eID), Type: "Service", CreatedAt: now, UpdatedAt: now}
		data, err := json.Marshal(ej)
		if err != nil {
			return err
		}
		f, err := gs.fs.Create(path)
		if err != nil {
			return fmt.Errorf("recreate type-mismatched entity file: %w", err)
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return fmt.Errorf("write type-mismatched entity file: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close type-mismatched entity file: %w", err)
		}
		_, err = gs.ReadAllEntityFiles(ctx(), "Component")
		if !errors.Is(err, ErrEntityTypeMismatch) {
			return fmt.Errorf("expected ErrEntityTypeMismatch, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllEntityFilesTypeDirectoryMismatch: %v", err)
	}
}
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
		// Commit the baseline so the committed tree pins both files before
		// removal (the durable-deletion contract is asserted against the
		// committed tree, not only the working tree).
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "pre-remove"); err != nil {
			return err
		}
		if err := committedTreeFile(gs, "edges/DEPENDS_ON/"+e1ID+".json"); err != nil {
			return fmt.Errorf("baseline file %s missing from committed tree: %w", e1ID, err)
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

		// AddAll + Commit the staged deletion: the committed tree must no
		// longer contain the removed file while the remaining file stays
		// present (SPEC durable-deletion contract: fs.Remove -> AddAll ->
		// Commit leaves the file gone from the committed tree).
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "post-remove"); err != nil {
			return err
		}
		if err := committedTreeFile(gs, "edges/DEPENDS_ON/"+e1ID+".json"); !errors.Is(err, object.ErrFileNotFound) {
			return fmt.Errorf("removed edge file %s still present in committed tree after commit: %v", e1ID, err)
		}
		if err := committedTreeFile(gs, "edges/DEPENDS_ON/"+e2ID+".json"); err != nil {
			return fmt.Errorf("remaining edge file %s missing from committed tree: %w", e2ID, err)
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

// TestReadAllEdgeFilesIDFilenameConflict: a JSON edge file whose embedded id
// differs from its filename (external corruption) must surface an error, not be
// silently loaded under the conflicting id (SPEC R8 corruption recovery).
func TestReadAllEdgeFilesIDFilenameConflict(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		eID := validUUID(t)
		fromID := validUUID(t)
		toID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)
		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{
			{ID: eID, Type: "DEPENDS_ON", FromEntityID: fromID, ToEntityID: toID, CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		// Rewrite the file's embedded id to a different (still valid) UUID under
		// the original filename, simulating filename/body divergence.
		otherID := validUUID(t)
		path := "edges/DEPENDS_ON/" + eID + ".json"
		ej := struct {
			ID           uuid.UUID         `json:"id"`
			Type         string            `json:"type"`
			FromEntityID uuid.UUID         `json:"from_entity_id"`
			ToEntityID   uuid.UUID         `json:"to_entity_id"`
			Properties   map[string]string `json:"properties,omitempty"`
			CreatedAt    time.Time         `json:"created_at"`
			UpdatedAt    time.Time         `json:"updated_at"`
		}{
			ID:           uuid.MustParse(otherID),
			Type:         "DEPENDS_ON",
			FromEntityID: uuid.MustParse(fromID),
			ToEntityID:   uuid.MustParse(toID),
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		data, err := json.Marshal(ej)
		if err != nil {
			return err
		}
		f, err := gs.fs.Create(path)
		if err != nil {
			return fmt.Errorf("recreate conflicting edge file: %w", err)
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return fmt.Errorf("write conflicting edge file: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close conflicting edge file: %w", err)
		}
		if _, err := gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON"); err == nil {
			return fmt.Errorf("expected error for conflicting edge id, got nil")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllEdgeFilesIDFilenameConflict: %v", err)
	}
}

// TestReadAllEdgeFilesTypeDirectoryMismatch: a JSON edge file under
// edges/<Type>/ whose embedded type disagrees with the directory (external
// corruption) must surface ErrEdgeTypeMismatch, not be silently loaded under a
// never-written type. This mirrors the write path (writeEdgeFile rejects the
// mismatch) and the id-vs-filename guard above.
func TestReadAllEdgeFilesTypeDirectoryMismatch(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		eID := validUUID(t)
		fromID := validUUID(t)
		toID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)
		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{
			{ID: eID, Type: "DEPENDS_ON", FromEntityID: fromID, ToEntityID: toID, CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		// Rewrite the file's embedded type to a different type under the
		// original directory, simulating type/body divergence.
		path := "edges/DEPENDS_ON/" + eID + ".json"
		ej := struct {
			ID           uuid.UUID         `json:"id"`
			Type         string            `json:"type"`
			FromEntityID uuid.UUID         `json:"from_entity_id"`
			ToEntityID   uuid.UUID         `json:"to_entity_id"`
			Properties   map[string]string `json:"properties,omitempty"`
			CreatedAt    time.Time         `json:"created_at"`
			UpdatedAt    time.Time         `json:"updated_at"`
		}{
			ID:           uuid.MustParse(eID),
			Type:         "CONNECTS_TO",
			FromEntityID: uuid.MustParse(fromID),
			ToEntityID:   uuid.MustParse(toID),
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		data, err := json.Marshal(ej)
		if err != nil {
			return err
		}
		f, err := gs.fs.Create(path)
		if err != nil {
			return fmt.Errorf("recreate type-mismatched edge file: %w", err)
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return fmt.Errorf("write type-mismatched edge file: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close type-mismatched edge file: %w", err)
		}
		_, err = gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON")
		if !errors.Is(err, ErrEdgeTypeMismatch) {
			return fmt.Errorf("expected ErrEdgeTypeMismatch, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllEdgeFilesTypeDirectoryMismatch: %v", err)
	}
}

// TestReadAllEdgeFilesZeroFromToGuard: a JSON edge file whose embedded from
// or to endpoint is a zero UUID (external corruption) must surface
// ErrInvalidUUID, not be silently loaded with a nil-UUID endpoint. This
// mirrors the write path (writeEdgeFile rejects zero/non-v4 endpoints via
// ErrInvalidUUID) and the sibling id-vs-filename and type-vs-directory
// guards (SPEC R8 corruption recovery).
func TestReadAllEdgeFilesZeroFromToGuard(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		eID := validUUID(t)
		fromID := validUUID(t)
		toID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)
		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{
			{ID: eID, Type: "DEPENDS_ON", FromEntityID: fromID, ToEntityID: toID, CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		path := "edges/DEPENDS_ON/" + eID + ".json"
		// The real EdgeJSON unmarshals from json:"from"/json:"to" keys, so the
		// rewrite must emit those exact keys for the zero UUID to genuinely
		// exercise the decode path.
		rewrite := func(from, to uuid.UUID) error {
			ej := struct {
				ID           uuid.UUID         `json:"id"`
				Type         string            `json:"type"`
				FromEntityID uuid.UUID         `json:"from"`
				ToEntityID   uuid.UUID         `json:"to"`
				Properties   map[string]string `json:"properties,omitempty"`
				CreatedAt    time.Time         `json:"created_at"`
				UpdatedAt    time.Time         `json:"updated_at"`
			}{
				ID:           uuid.MustParse(eID),
				Type:         "DEPENDS_ON",
				FromEntityID: from,
				ToEntityID:   to,
				CreatedAt:    now,
				UpdatedAt:    now,
			}
			data, err := json.Marshal(ej)
			if err != nil {
				return err
			}
			f, err := gs.fs.Create(path)
			if err != nil {
				return fmt.Errorf("recreate zero-endpoint edge file: %w", err)
			}
			if _, err := f.Write(data); err != nil {
				_ = f.Close()
				return fmt.Errorf("write zero-endpoint edge file: %w", err)
			}
			return f.Close()
		}
		// (a) Zero from endpoint.
		if err := rewrite(uuid.Nil, uuid.MustParse(toID)); err != nil {
			return err
		}
		if _, err := gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON"); !errors.Is(err, ErrInvalidUUID) {
			return fmt.Errorf("expected ErrInvalidUUID for zero from endpoint, got %v", err)
		}
		// (b) Zero to endpoint.
		if err := rewrite(uuid.MustParse(fromID), uuid.Nil); err != nil {
			return err
		}
		if _, err := gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON"); !errors.Is(err, ErrInvalidUUID) {
			return fmt.Errorf("expected ErrInvalidUUID for zero to endpoint, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllEdgeFilesZeroFromToGuard: %v", err)
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

// TestCreateBranchRefOnlyExisting pins the branch-already-exists contract for
// a branch that exists only as a ref: SetBranchRef writes no config entry, so
// CreateBranch must check the ref itself and refuse to silently overwrite it
// (which would repoint the branch at main and discard its commits).
func TestCreateBranchRefOnlyExisting(t *testing.T) {
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
// (SPEC:754): CreateBranch must branch from main, not from the current HEAD.
// After an abandoned failed Commit leaves the working tree checked out on a
// transaction branch, a new transaction must not inherit that branch's commits
// — branching from HEAD would leak the abandoned transaction's changes into
// the next transaction via HardResetToBranch, breaking transaction isolation.
func TestCreateBranchFromMainWhenHeadNotOnMain(t *testing.T) {
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
// T7: Remote operations
// ============================================================================

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

// TestIsEmpty pins the SPEC R10 clone-vs-pull / empty-repo classification:
// a fresh init-only repo (only New()'s "init" commit authored by cartographer)
// is empty; any repo with a data commit, a wipe commit, or an "init"-message
// commit from a different author is not. Each branch must be asserted for real
// — the WithGitLock result is propagated, never discarded.
func TestIsEmpty(t *testing.T) {
	t.Run("fresh init returns true", func(t *testing.T) {
		gs := setupTestStore(t)
		err := gs.WithGitLock(func() error {
			empty, err := gs.IsEmpty(ctx())
			if err != nil {
				return err
			}
			if !empty {
				return fmt.Errorf("expected empty for init-only repo")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("fresh init: %v", err)
		}
	})

	t.Run("with data commit returns false", func(t *testing.T) {
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
		if err != nil {
			t.Fatalf("with data commit: %v", err)
		}
	})

	t.Run("wiped-but-committed returns false", func(t *testing.T) {
		gs := setupTestStore(t)
		err := gs.WithGitLock(func() error {
			// Commit a "wipe" commit on top of init, mirroring the production
			// WipeGraph sequence (git rm -r entities+edges, commit "wipe"):
			// a wipe commit must be classified as data (not empty), so the
			// SPEC R10 clone-vs-pull decision never re-clones a wiped repo.
			if err := gs.GitRm(ctx(), "entities"); err != nil {
				return err
			}
			if err := gs.GitRm(ctx(), "edges"); err != nil {
				return err
			}
			if err := gs.Commit(ctx(), "wipe"); err != nil {
				return err
			}

			empty, err := gs.IsEmpty(ctx())
			if err != nil {
				return err
			}
			if empty {
				return fmt.Errorf("expected non-empty after wipe commit")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("wiped-but-committed: %v", err)
		}
	})

	t.Run("remote init commit with different author returns false", func(t *testing.T) {
		gs := setupTestStore(t)
		err := gs.WithGitLock(func() error {
			// Simulate a commit whose message is "init" but from a different
			// author (e.g. a cloned remote's initial commit). This must NOT
			// be treated as New()'s init commit. A content change is required
			// first: committing on a clean tree would be ErrEmptyCommit and
			// would not create the commit at all.
			now := time.Now().UTC().Round(time.Millisecond)
			if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
				{ID: validUUID(t), Type: "Component", CreatedAt: now, UpdatedAt: now},
			}); err != nil {
				return err
			}
			if err := gs.AddAll(ctx(), "."); err != nil {
				return err
			}
			if _, err := gs.wt.Commit("init", &git.CommitOptions{
				Author: &object.Signature{
					Name:  "developer",
					Email: "dev@remote.example",
				},
			}); err != nil {
				return err
			}

			empty, err := gs.IsEmpty(ctx())
			if err != nil {
				return err
			}
			if empty {
				return fmt.Errorf("expected non-empty for remote init commit with different author")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("remote init commit with different author: %v", err)
		}
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

// TestFetchAndMergeNoRemote pins FetchAndMerge's no-remote guard: with an
// empty remoteURL, FetchAndMerge must fail with ErrNoRemote (and a zero hash)
// before touching the network. This branch is a live production path — the
// sync worker's fetchAndRehydrate special-cases the sentinel as a benign no-op
// (sync_worker.go:325), feeding the SPEC error-table row "Remote not
// configured" (SPEC:979). PushRemote's sibling guard is pinned by
// TestPushRemoteNoRemote.
func TestFetchAndMergeNoRemote(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		hash, err := gs.FetchAndMerge(ctx(), "origin", "main")
		if !errors.Is(err, ErrNoRemote) {
			return fmt.Errorf("expected ErrNoRemote, got %v", err)
		}
		if !hash.IsZero() {
			return fmt.Errorf("expected zero hash on ErrNoRemote, got %v", hash)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("FetchAndMergeNoRemote: %v", err)
	}
}

// TestPushRemoteWithAuth verifies PushRemote's auth contract: ErrAuthConfigMissing
// is returned only when the operation cannot be attempted — a URL that demands
// credentials (ssh:// or an https:// URL embedding a user) with no auth provider
// configured, or a configured authFn that errors. A public remote (no credentials
// demanded) is pushed anonymously: a nil authFn and a nil-auth authFn result both
// proceed past the auth guard, so the failure is a transport error, never the
// mislabelled auth-config sentinel. (The push itself is driven against port 1 on
// loopback — connection refused, no network — so the guard-pass is observable as a
// non-auth failure; the full anonymous-push success path is pinned by
// TestPushRemoteAnonymous.)
func TestPushRemoteWithAuth(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// A URL that demands credentials (embedded user) with no auth provider
		// → ErrAuthConfigMissing via the requiresAuth pre-flight.
		if err := gs.SetRemote(ctx(), "https://user@example.com/repo.git", nil); err != nil {
			return err
		}
		err := gs.PushRemote(ctx())
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing for credentials-requiring URL with nil authFn, got %v", err)
		}

		// An ssh URL always demands credentials → ErrAuthConfigMissing with a
		// nil authFn.
		if err := gs.SetRemote(ctx(), "ssh://git@example.com/repo.git", nil); err != nil {
			return err
		}
		err = gs.PushRemote(ctx())
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing for ssh URL with nil authFn, got %v", err)
		}

		// Set authFn that returns an error (readSecretFn failure / invalid
		// credential) → ErrAuthConfigMissing (SPEC error row "Remote auth
		// config missing (Sync)" → FAILED_PRECONDITION)
		if err := gs.SetRemote(ctx(), "https://example.com/repo.git", func() (transport.AuthMethod, error) {
			return nil, fmt.Errorf("auth resolution failure")
		}); err != nil {
			return err
		}
		err = gs.PushRemote(ctx())
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing, got %v", err)
		}

		// A plain public https URL (no credentials demanded) with a nil authFn:
		// the anonymous push is attempted — the failure is a transport error,
		// never ErrAuthConfigMissing.
		if err := gs.SetRemote(ctx(), "https://127.0.0.1:1/repo.git", nil); err != nil {
			return err
		}
		err = gs.PushRemote(ctx())
		if err == nil || errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected transport failure for public remote with nil authFn, got %v", err)
		}

		// Same for a configured authFn returning nil — an explicit anonymous
		// selection (e.g. the production buildResolveAuthFn closure for a public
		// remote with no secretRef).
		if err := gs.SetRemote(ctx(), "https://127.0.0.1:1/repo.git", func() (transport.AuthMethod, error) {
			return nil, nil
		}); err != nil {
			return err
		}
		err = gs.PushRemote(ctx())
		if err == nil || errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected transport failure for anonymous-selecting authFn, got %v", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("PushRemoteWithAuth: %v", err)
	}
}

// TestPushRemoteAnonymous verifies the operator requirement: a remote configured
// without a secretRef must push anonymously (SPEC R10 "remote configured ⇒ pushes
// happen"). Both a nil authFn (auth not configured) and a configured authFn
// returning nil (explicit anonymous selection) must push successfully to a remote
// that does not demand credentials. Uses the local-bare-repo technique from
// TestRemotePushPull: a file:// URL demands no credentials (requiresAuth false,
// exactly like a plain public https URL), so the anonymous push is driven
// end-to-end without the network, and the remote ref must advance.
func TestPushRemoteAnonymous(t *testing.T) {
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

	localDir := filepath.Join(tmpDir, "local")
	store, err := New(localDir)
	if err != nil {
		t.Fatalf("New local: %v", err)
	}
	defer func() { _ = store.Close() }()
	gs := store.(*gitStore)

	for _, tc := range []struct {
		name   string
		authFn func() (transport.AuthMethod, error)
	}{
		{name: "nil authFn", authFn: nil},
		{name: "authFn selecting anonymous access", authFn: func() (transport.AuthMethod, error) {
			return nil, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := store.WithGitLock(func() error {
				gs.remoteURL = "file://" + bareDir
				gs.authFn = tc.authFn

				// Create the "origin" remote directly on the go-git repo so the
				// subtest drives remote state by hand (SetRemote's
				// ensureRemoteExists wiring is exercised elsewhere).
				_, err := gs.repo.CreateRemote(&config.RemoteConfig{
					Name: "origin",
					URLs: []string{gs.remoteURL},
				})
				if err != nil && !errors.Is(err, git.ErrRemoteExists) {
					return fmt.Errorf("create remote: %w", err)
				}

				// Make a commit on local
				now := time.Now().UTC().Round(time.Millisecond)
				if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
					{ID: validUUID(t), Type: "Component", CreatedAt: now, UpdatedAt: now},
				}); err != nil {
					return err
				}
				if err := gs.AddAll(ctx(), "."); err != nil {
					return err
				}
				if err := gs.Commit(ctx(), "transaction:anonymous-push"); err != nil {
					return err
				}

				// Push anonymously to the remote
				if err := gs.PushRemote(ctx()); err != nil {
					return fmt.Errorf("anonymous push: %w", err)
				}

				// Verify the remote ref advanced
				remoteRepo, err := git.PlainOpen(bareDir)
				if err != nil {
					return fmt.Errorf("open remote: %w", err)
				}
				ref, err := remoteRepo.Reference(plumbing.ReferenceName("refs/heads/main"), true)
				if err != nil {
					return fmt.Errorf("remote ref: %w", err)
				}
				if ref.Hash().IsZero() {
					return fmt.Errorf("expected non-zero hash on remote after anonymous push")
				}
				return nil
			})
			if err != nil {
				t.Fatalf("TestPushRemoteAnonymous[%s]: %v", tc.name, err)
			}
		})
	}
}

func TestCloneSingleBranchNoAuth(t *testing.T) {
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
	// Serve the remote over go-git's native file:// transport — no external
	// git binary, honoring SPEC R5's "pure Go, no external git binary" policy.
	// CloneSingleBranch's URL validation accepts file:// remotes, and file://
	// demands no credentials (requiresAuth false), so the nil-authFn clone
	// path is exercised end-to-end without the network.
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
		return gs.CloneSingleBranch(ctx(), "file://"+remoteDir, "main")
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
	configureAnonymousRemote(t, gs, "file://"+remoteDir)

	const updatedGraphContent = `{"graph":"updated"}`
	updatedHash := pushGraphUpdate(t, tmpDir, remoteDir, updatedGraphContent)

	if _, err := gs.FetchAndMerge(ctx(), "origin", "main"); err != nil {
		t.Fatalf("anonymous FetchAndMerge: %v", err)
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

// TestCloneSingleBranchCleansUntracked pins the SPEC R10 clone-on-init
// contract: after the forced checkout the working tree must reflect exactly
// the cloned state (re-hydration reads the cloned tree). A transaction that
// crashed between file-write and git-commit on a prior run strands uncommitted
// files in the working tree that IsEmpty cannot detect (main still points at
// the init commit), so without the post-checkout clean — mirroring
// setLocalRefAndCheckout — they survive the clone and are re-hydrated into
// main.lbug as phantom graph data.
func TestCloneSingleBranchCleansUntracked(t *testing.T) {
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
	if _, err := sourceWT.Commit("main graph", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("commit graph file: %v", err)
	}

	remoteDir := filepath.Join(tmpDir, "remote.git")
	if _, err := git.PlainClone(remoteDir, true, &git.CloneOptions{URL: "file://" + sourceDir}); err != nil {
		t.Fatalf("create bare remote: %v", err)
	}

	store, err := New(filepath.Join(tmpDir, "local"))
	if err != nil {
		t.Fatalf("new local store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	gs := store.(*gitStore)

	// Simulate a transaction that crashed between file-write and git-commit
	// on a prior run: an uncommitted file stranded in the re-hydration
	// directory. main still points at the init commit, so IsEmpty reports the
	// repo empty and the clone-on-init path runs with this file in the tree.
	stranded := filepath.Join(tmpDir, "local", "graph-repo", "entities", "phantom.json")
	if err := os.WriteFile(stranded, []byte(`{"id":"phantom","type":"Person"}`), 0644); err != nil {
		t.Fatalf("write stranded file: %v", err)
	}

	err = store.WithGitLock(func() error {
		return gs.CloneSingleBranch(ctx(), "file://"+remoteDir, "main")
	})
	if err != nil {
		t.Fatalf("CloneSingleBranchCleansUntracked: %v", err)
	}

	if _, err := os.Stat(stranded); !os.IsNotExist(err) {
		t.Fatalf("stranded untracked file survived the clone: %v", err)
	}
	cloned, err := gs.fs.Open("data.txt")
	if err != nil {
		t.Fatalf("open checked-out payload file: %v", err)
	}
	got, err := io.ReadAll(cloned)
	_ = cloned.Close()
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
}

func TestCloneSingleBranchInvalidScheme(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		err := gs.CloneSingleBranch(ctx(), "ftp://example.com/repo.git", "main")
		if !errors.Is(err, ErrUnsupportedURLScheme) {
			return fmt.Errorf("expected ErrUnsupportedURLScheme, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("CloneSingleBranchInvalidScheme: %v", err)
	}
}

// TestRequiresAuth drives the requiresAuth helper directly for each URL class:
// ssh:// always requires auth, https:// requires auth only when it embeds a
// user (basic auth), and non-ssh/no-user URLs (public https remotes, file://
// scratch paths, malformed inputs) do not. This mirrors the helper's exact
// semantics (remote.go:56) rather than inserted intent.
func TestRequiresAuth(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"ssh always requires auth", "ssh://git@example.com/repo.git", true},
		{"https with embedded user requires auth", "https://user@host/repo.git", true},
		{"plain https public remote no auth", "https://host/repo.git", false},
		{"file scratch path no auth", "file:///tmp/repo.git", false},
		{"malformed url no auth", "://not-a-url", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requiresAuth(tt.url); got != tt.want {
				t.Fatalf("requiresAuth(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

// TestCloneSingleAuthConfigMissing verifies the authenticated-URL pre-flight
// rejection: an ssh:// clone URL with no configured auth provider returns
// ErrAuthConfigMissing before any fetch is attempted (SPEC R1/R10 Init).
func TestCloneSingleAuthConfigMissing(t *testing.T) {
	gs := setupTestStore(t)
	if gs.authFn != nil {
		t.Fatal("expected nil auth provider")
	}
	err := gs.WithGitLock(func() error {
		err := gs.CloneSingleBranch(ctx(), "ssh://git@example.com/repo.git", "main")
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("CloneSingleAuthConfigMissing: %v", err)
	}
}

// TestCloneSingleAuthConfigMissingHTTPS verifies the authenticated-URL
// pre-flight rejection for an https:// URL with an embedded user (basic
// auth) and no configured auth provider — the https counterpart of
// TestCloneSingleAuthConfigMissing (which covers ssh://). This pins the
// requiresAuth URL-scheme branch for https-with-embedded-user at the
// CloneSingleBranch level (remote.go:416).
func TestCloneSingleAuthConfigMissingHTTPS(t *testing.T) {
	gs := setupTestStore(t)
	if gs.authFn != nil {
		t.Fatal("expected nil auth provider")
	}
	err := gs.WithGitLock(func() error {
		err := gs.CloneSingleBranch(ctx(), "https://user@example.com/repo.git", "main")
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestCloneSingleAuthConfigMissingHTTPS: %v", err)
	}
}

// TestCloneSingleAuthSentinelPreserved verifies that CloneSingleBranch
// preserves the ErrAuthConfigMissing sentinel returned by a configured authFn
// — the missing-expected-key sub-case of the SPEC error-table row "Remote auth
// config missing (Sync)". CloneSingleBranch is the clone-on-init path, so the
// credential failure must surface as ErrAuthConfigMissing for mapGitError to
// return FAILED_PRECONDITION.
func TestCloneSingleAuthSentinelPreserved(t *testing.T) {
	gs := setupTestStore(t)
	gs.authFn = func() (transport.AuthMethod, error) {
		return nil, ErrAuthConfigMissing
	}
	err := gs.WithGitLock(func() error {
		err := gs.CloneSingleBranch(ctx(), "ssh://git@example.com/repo.git", "main")
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("CloneSingleAuthSentinelPreserved: %v", err)
	}
}

// TestCloneSingleAuthFnFailureCollapsesToAuthConfigMissing verifies that
// CloneSingleBranch surfaces ErrAuthConfigMissing when a configured authFn
// returns a generic (non-sentinel) error — the readSecretFn-failure (Secret
// missing) and invalid-credential (unparseable ssh-privatekey PEM) sub-cases
// of the SPEC error-table row "Remote auth config missing (Sync)"
// on the clone-on-init path.
func TestCloneSingleAuthFnFailureCollapsesToAuthConfigMissing(t *testing.T) {
	gs := setupTestStore(t)
	gs.authFn = func() (transport.AuthMethod, error) {
		return nil, fmt.Errorf("secrets: secret %q not found", "cartographer-remote-auth")
	}
	err := gs.WithGitLock(func() error {
		err := gs.CloneSingleBranch(ctx(), "ssh://git@example.com/repo.git", "main")
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestCloneSingleAuthFnFailureCollapsesToAuthConfigMissing: %v", err)
	}
}

// TestPushRejectedSentinel guards the exported ErrPushRejected sentinel that
// PushRemote maps from git.ErrNonFastForwardUpdate (remote.go:379). It cannot
// be reached deterministically through a genuine push (go-git's receive-pack
// does not wrap rejections in ErrNonFastForwardUpdate — see the ponytail at
// remote.go:366), so this locks the sentinel's identity and its distinctness
// from the sibling remote sentinels the service mapGitError must tell apart.
func TestPushRejectedSentinel(t *testing.T) {
	if ErrPushRejected == nil {
		t.Fatal("ErrPushRejected must be a non-nil exported sentinel")
	}
	for _, other := range []error{ErrPullDiverged, ErrAuthConfigMissing, ErrNoRemote} {
		if errors.Is(ErrPushRejected, other) {
			t.Fatalf("ErrPushRejected aliases %v; sentinels must stay distinct", other)
		}
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

// TestEnsureRemoteExistsURLChange verifies the URL-change branch of
// ensureRemoteExists: when the origin remote already exists but its URL
// differs from the configured remote URL (REMOTE_URL changed across pod
// restarts on the same PVC), the remote must be deleted and recreated with
// the new URL. This destructive delete+recreate transition is pinned
// directly — a regression that skipped the branch would leave origin
// pointing at the stale URL.
func TestEnsureRemoteExistsURLChange(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Configure the remote with the original URL (create-on-missing).
		if err := gs.SetRemote(ctx(), "https://example.com/original.git", func() (transport.AuthMethod, error) {
			return nil, nil
		}); err != nil {
			return err
		}
		remote, err := gs.repo.Remote("origin")
		if err != nil {
			return fmt.Errorf("get remote: %w", err)
		}
		if got := remote.Config().URLs; len(got) != 1 || got[0] != "https://example.com/original.git" {
			return fmt.Errorf("initial remote URLs = %v, want [https://example.com/original.git]", got)
		}

		// Reconfigure with a different URL — origin exists but differs, so
		// ensureRemoteExists must delete and recreate it with the new URL.
		if err := gs.SetRemote(ctx(), "https://example.com/changed.git", func() (transport.AuthMethod, error) {
			return nil, nil
		}); err != nil {
			return err
		}
		remote, err = gs.repo.Remote("origin")
		if err != nil {
			return fmt.Errorf("get recreated remote: %w", err)
		}
		if got := remote.Config().URLs; len(got) != 1 || got[0] != "https://example.com/changed.git" {
			return fmt.Errorf("recreated remote URLs = %v, want [https://example.com/changed.git]", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestEnsureRemoteExistsURLChange: %v", err)
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

// TestReadAllEdgeFilesEmptyTypeDir tests reading an existing-but-empty type
// directory returns an empty slice (not an error), mirroring the entity-side
// TestReadAllEntityFilesEmptyTypeDir for the edge load path.
func TestReadAllEdgeFilesEmptyTypeDir(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Create an empty type directory (no JSON files)
		if err := gs.fs.MkdirAll("edges/EmptyType", 0755); err != nil {
			return err
		}
		files, err := gs.ReadAllEdgeFiles(ctx(), "EmptyType")
		if err != nil {
			return err
		}
		if len(files) != 0 {
			return fmt.Errorf("expected 0 files for empty type dir")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllEdgeFilesEmptyTypeDir: %v", err)
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

// TestRemotePushPull exercises PushRemote and FetchAndMerge using temp
// directory repos with file:// URLs set directly on the gitStore.
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
		if _, err := gs.FetchAndMerge(ctx(), "origin", "main"); err != nil {
			return fmt.Errorf("fetch: %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("RemotePushPull: %v", err)
	}
}

// TestCloneSingleBranchFromRemote clones a remote via internal operations
// (the same flow CloneSingleBranch composes), driven by hand so the reopen
// and backend re-wiring steps can be asserted individually.
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

		auth, err := gs.resolveAuth()
		if err != nil {
			return fmt.Errorf("resolve auth: %w", err)
		}

		// Configure remote directly (bypassing CloneSingleBranch — this test
		// drives the internal operations by hand: fetch, ref setup, checkout,
		// and reopen, asserting the backend re-wiring step in isolation).
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
		oldBackend := gs.backend
		gs.repo = reopened
		gs.wt = wt
		gs.fs = wt.Filesystem
		gs.backend = reopened.Storer

		// The reopened backend must be re-wired in step with repo/wt/fs, or a
		// swap-in/memory storer would silently write to the pre-reopen backend.
		if gs.backend == oldBackend || gs.backend != reopened.Storer {
			return fmt.Errorf("backend not re-wired to reopened storer")
		}

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

// failRefStorer wraps a storage.Storer and fails every reference lookup with
// the configured error, simulating a backend that cannot resolve refs for
// reasons other than a missing ref (ErrReferenceNotFound).
type failRefStorer struct {
	storage.Storer
	failErr error
}

func (f *failRefStorer) Reference(name plumbing.ReferenceName) (*plumbing.Reference, error) {
	return nil, f.failErr
}

// TestIsEmptyMainRefError pins both main-ref resolution branches of IsEmpty:
// a missing main ref (ErrReferenceNotFound) reports empty, while a backend
// that fails ref resolution for any other reason surfaces the
// "resolve main ref: %w" error branch of IsEmpty (remote.go) instead of being
// swallowed as empty.
func TestIsEmptyMainRefError(t *testing.T) {
	// Create a gitStore with in-memory storage; a failing backend is swapped
	// in below to trigger the non-ErrReferenceNotFound resolution error.
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

	gs := &gitStore{
		repo:     repo,
		wt:       wt,
		fs:       fs,
		backend:  storer,
		basePath: t.TempDir(),
	}

	err = gs.WithGitLock(func() error {
		// Delete the main ref to exercise the ErrReferenceNotFound path in
		// IsEmpty: a repo with no main ref is reported empty.
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

		// Swap in a backend whose ref lookups fail for a non-ErrReferenceNotFound
		// reason: IsEmpty must surface the "resolve main ref" error branch
		// (remote.go) rather than treat the failure as empty.
		refErr := errors.New("ref lookup failed")
		broken := &failRefStorer{Storer: storer, failErr: refErr}
		gs.backend = broken
		gs.repo = &git.Repository{Storer: broken}

		empty, err = gs.IsEmpty(ctx())
		if err == nil {
			return fmt.Errorf("expected resolve main ref error, got empty=%v", empty)
		}
		if !errors.Is(err, refErr) {
			return fmt.Errorf("expected wrapped ref lookup error, got %v", err)
		}
		if !strings.Contains(err.Error(), "resolve main ref") {
			return fmt.Errorf("expected 'resolve main ref' wrap, got %v", err)
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

// TestFetchAndMerge_BootstrapFromInitOnly exercises the SPEC R10
// clone-vs-pull bootstrap path: an init-only local repo (created by New())
// pulls from a remote that shares the same init commit and has additional
// history. FetchAndMerge must fast-forward local main from the init commit
// to the remote's HEAD without returning ErrPullDiverged.
func TestFetchAndMerge_BootstrapFromInitOnly(t *testing.T) {
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	// Create the "seed" repo whose init commit will be shared between local
	// and remote. This is the same init commit that New() produces: a single
	// commit with message "init" containing entities/ + edges/ dirs.
	seedDir := filepath.Join(tmpDir, "seed")
	seedStore, err := New(seedDir)
	if err != nil {
		t.Fatalf("New seed: %v", err)
	}
	_ = seedStore.Close()

	// Clone the seed as a bare remote — the bare remote now has the same
	// "init" commit as the local will have.
	_, err = git.PlainClone(bareDir, true, &git.CloneOptions{
		URL: "file://" + filepath.Join(seedDir, "graph-repo"),
	})
	if err != nil {
		t.Fatalf("clone bare: %v", err)
	}

	// Advance the remote by one commit (simulating a remote with history).
	workDir := filepath.Join(tmpDir, "work")
	workRepo, err := git.PlainClone(workDir, false, &git.CloneOptions{
		URL: "file://" + bareDir,
	})
	if err != nil {
		t.Fatalf("clone work: %v", err)
	}
	workWT, err := workRepo.Worktree()
	if err != nil {
		t.Fatalf("work worktree: %v", err)
	}
	f, err := workWT.Filesystem.Create("remote-data.txt")
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	_, _ = f.Write([]byte("remote content"))
	_ = f.Close()
	if _, err := workWT.Add("remote-data.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	remoteHash, err := workWT.Commit("remote data commit", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := workRepo.Push(&git.PushOptions{}); err != nil {
		t.Fatalf("push: %v", err)
	}

	// Create a fresh local repo via New() — this produces the same "init"
	// commit as the seed, so local main IS an ancestor of remote main.
	localDir := filepath.Join(tmpDir, "local")
	localStore, err := New(localDir)
	if err != nil {
		t.Fatalf("New local: %v", err)
	}
	defer func() { _ = localStore.Close() }()

	gs := localStore.(*gitStore)

	err = localStore.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		// Verify local is empty (init-only) before pull
		empty, emptyErr := gs.IsEmpty(ctx())
		if emptyErr != nil {
			return fmt.Errorf("IsEmpty: %w", emptyErr)
		}
		if !empty {
			return fmt.Errorf("expected empty init-only repo")
		}

		// FetchAndMerge must fast-forward from init to remote HEAD
		newHash, pullErr := gs.FetchAndMerge(ctx(), "origin", "main")
		if pullErr != nil {
			return fmt.Errorf("FetchAndMerge bootstrap: %w", pullErr)
		}
		if newHash != remoteHash {
			return fmt.Errorf("expected hash %s, got %s", remoteHash, newHash)
		}

		// Local main must now point at remote HEAD
		mainRef, refErr := gs.repo.Reference(plumbing.NewBranchReferenceName("main"), true)
		if refErr != nil {
			return fmt.Errorf("main ref: %w", refErr)
		}
		if mainRef.Hash() != remoteHash {
			return fmt.Errorf("main ref = %s, want %s", mainRef.Hash(), remoteHash)
		}

		// Remote data must be visible in the working tree
		if _, statErr := gs.fs.Stat("remote-data.txt"); statErr != nil {
			return fmt.Errorf("remote data file missing after bootstrap: %w", statErr)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("TestFetchAndMerge_BootstrapFromInitOnly: %v", err)
	}
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
// HEAD when the remote has new commits (fast-forward). The local repo is
// cloned before the remote is advanced, so at fetch time local main trails
// the remote tip and FetchAndMerge must take the isAncestor ->
// setLocalRefAndCheckout fast-forward path rather than the early
// up-to-date return.
func TestFetchAndMerge_FastForward(t *testing.T) {
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	setupBareRemote(t, tmpDir, bareDir)

	// Clone the local repo first, before the remote is advanced, so local
	// main matches the pre-advance remote tip.
	gs := cloneFromBare(t, tmpDir, bareDir)
	originalHash := remoteHEAD(t, bareDir)

	// Advance the remote by one commit pushed from a separate "writer" clone.
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
	remoteFile, err := writerWT.Filesystem.Create("remote-data.txt")
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	_, _ = remoteFile.Write([]byte("remote content"))
	_ = remoteFile.Close()
	if _, err := writerWT.Add("remote-data.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := writerWT.Commit("remote data commit", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := writer.Push(&git.PushOptions{}); err != nil {
		t.Fatalf("push: %v", err)
	}

	remoteHash := remoteHEAD(t, bareDir)
	if remoteHash == originalHash {
		t.Fatalf("expected remote to advance past %s", originalHash)
	}

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

		// The local main ref must have moved off the pre-advance head.
		mainRef, refErr := gs.repo.Reference(plumbing.NewBranchReferenceName("main"), true)
		if refErr != nil {
			return fmt.Errorf("main ref: %w", refErr)
		}
		if mainRef.Hash() != remoteHash {
			return fmt.Errorf("main ref = %s, want %s", mainRef.Hash(), remoteHash)
		}

		// Remote data must be visible in the working tree after the
		// fast-forward checkout.
		if _, statErr := gs.fs.Stat("remote-data.txt"); statErr != nil {
			return fmt.Errorf("remote data file missing after fast-forward: %w", statErr)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("TestFetchAndMerge_FastForward: %v", err)
	}
}

// TestFetchAndMerge_Diverged asserts that FetchAndMerge fails with
// ErrPullDiverged when local main and the remote have diverged (neither side
// is an ancestor of the other) and leaves the local main ref unchanged. This
// is the delivered divergence behavior of the sync pull path: service mapGitError
// maps ErrPullDiverged to FAILED_PRECONDITION, matching SPEC R10 / error-table
// row "Remote pull diverged" (line 926).
func TestFetchAndMerge_Diverged(t *testing.T) {
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

	err = gs.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		_, err = gs.FetchAndMerge(ctx(), "origin", "main")
		if !errors.Is(err, ErrPullDiverged) {
			if err == nil {
				return fmt.Errorf("expected ErrPullDiverged, got nil")
			}
			return fmt.Errorf("expected ErrPullDiverged, got %v", err)
		}

		// The local main ref must be left unchanged on divergence.
		localRef, refErr := gs.repo.Reference(plumbing.ReferenceName("refs/heads/main"), true)
		if refErr != nil {
			return fmt.Errorf("resolve local ref: %w", refErr)
		}
		if localRef.Hash() != localCommitHash {
			return fmt.Errorf("local main ref changed on divergence: got %s, want %s", localRef.Hash(), localCommitHash)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestFetchAndMerge_Diverged: %v", err)
	}
}

// TestFetchAndMerge_DivergencePersistsAcrossCycles pins the persistent-
// divergence contract of FetchAndMerge: after the first divergent cycle
// advances the tracking ref to the remote tip while local main is left
// behind (ErrPullDiverged, local ref unchanged), the next fetch is a no-op
// (git.NoErrAlreadyUpToDate) but the local branch is STILL diverged. The
// up-to-date branch must re-run the ancestry classification and continue to
// surface ErrPullDiverged — never silently report the divergence as
// up-to-date — so the sync worker and Sync() keep delivering the SPEC R10
// "Sync diverged" failure (FAILED_PRECONDITION, SPEC error-table row 977)
// and its telemetry on every cycle, and BeginTransaction's implicit sync
// never sees a clean cycle over stale local state (GIT_PLAN.md:138).
func TestFetchAndMerge_DivergencePersistsAcrossCycles(t *testing.T) {
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	setupBareRemote(t, tmpDir, bareDir)

	// Clone the bare remote to create a "writer" that will push to remote.
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

	// Make a local commit (diverging from remote).
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

	// Make a different commit on the remote (push from writer).
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

	err = gs.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		// Cycle 1: the fetch advances the tracking ref to the remote tip and
		// the ancestry classification surfaces the divergence.
		_, err = gs.FetchAndMerge(ctx(), "origin", "main")
		if !errors.Is(err, ErrPullDiverged) {
			if err == nil {
				return fmt.Errorf("cycle 1: expected ErrPullDiverged, got nil")
			}
			return fmt.Errorf("cycle 1: expected ErrPullDiverged, got %v", err)
		}

		// Cycle 2: the fetch is a no-op (tracking ref already at the remote
		// tip → git.NoErrAlreadyUpToDate) but local main is STILL diverged.
		// The up-to-date branch must re-classify, not silently succeed.
		_, err = gs.FetchAndMerge(ctx(), "origin", "main")
		if !errors.Is(err, ErrPullDiverged) {
			if err == nil {
				return fmt.Errorf("cycle 2: expected ErrPullDiverged, got nil")
			}
			return fmt.Errorf("cycle 2: expected ErrPullDiverged, got %v", err)
		}

		// Local main must remain unchanged across both divergent cycles.
		localRef, refErr := gs.repo.Reference(plumbing.ReferenceName("refs/heads/main"), true)
		if refErr != nil {
			return fmt.Errorf("resolve local ref: %w", refErr)
		}
		if localRef.Hash() != localCommitHash {
			return fmt.Errorf("local main ref changed on divergence: got %s, want %s", localRef.Hash(), localCommitHash)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestFetchAndMerge_DivergencePersistsAcrossCycles: %v", err)
	}
}

// TestFetchAndMerge_LocalAhead pins the local-ahead (remote strictly behind)
// classification of FetchAndMerge: local main has advanced past the remote
// (e.g. a fire-and-forget push that failed transiently — SPEC:788), so there
// is nothing to pull and the call must succeed as up-to-date, never fail with
// ErrPullDiverged. The remote-behind state is reached with distinct
// local/remote tips by dropping the remote-tracking ref first (simulating a
// remote (re)configuration on a repo that has not fetched since —
// ensureRemoteExists deletes/recreates origin on URL change), forcing the
// fetch to re-create the tracking ref from the remote's behind-local tip.
func TestFetchAndMerge_LocalAhead(t *testing.T) {
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	setupBareRemote(t, tmpDir, bareDir)
	gs := cloneFromBare(t, tmpDir, bareDir)

	// Local commits a change the remote has never seen — local main is now
	// strictly ahead of the remote.
	localFile, err := gs.wt.Filesystem.Create("local.txt")
	if err != nil {
		t.Fatalf("create local file: %v", err)
	}
	_, _ = localFile.Write([]byte("local content"))
	_ = localFile.Close()
	if _, err := gs.wt.Add("local.txt"); err != nil {
		t.Fatalf("add local: %v", err)
	}
	localCommitHash, err := gs.wt.Commit("local ahead", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	})
	if err != nil {
		t.Fatalf("local commit: %v", err)
	}

	err = gs.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		// Drop the tracking ref so the fetch re-creates it from the remote's
		// (behind-local) tip and the ancestry classification runs on distinct
		// local/remote tips rather than short-circuiting as
		// NoErrAlreadyUpToDate.
		if err := gs.backend.RemoveReference(plumbing.ReferenceName("refs/remotes/origin/main")); err != nil {
			return fmt.Errorf("remove tracking ref: %w", err)
		}

		newHash, err := gs.FetchAndMerge(ctx(), "origin", "main")
		if err != nil {
			return fmt.Errorf("FetchAndMerge local-ahead: %w", err)
		}
		if newHash != localCommitHash {
			return fmt.Errorf("expected local hash %s, got %s", localCommitHash, newHash)
		}

		// Local main must be left unchanged — there is nothing to pull.
		localRef, refErr := gs.repo.Reference(plumbing.ReferenceName("refs/heads/main"), true)
		if refErr != nil {
			return fmt.Errorf("resolve local ref: %w", refErr)
		}
		if localRef.Hash() != localCommitHash {
			return fmt.Errorf("local main ref changed on local-ahead: got %s, want %s", localRef.Hash(), localCommitHash)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestFetchAndMerge_LocalAhead: %v", err)
	}
}

// TestFetchAndMerge_NonMainBranch asserts that FetchAndMerge honors its branch
// parameter: it fetches the given branch's refspec and reads the matching
// tracking ref, so a non-"main" branch is pulled correctly. Previously the
// fetch refspec was hardwired to "refs/heads/main", so for a non-main branch
// the tracking-ref read at "refs/remotes/<remote>/<branch>" was stale/absent.
func TestFetchAndMerge_NonMainBranch(t *testing.T) {
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	setupBareRemote(t, tmpDir, bareDir)

	// Clone the bare remote to get a local repo that only knows main.
	gs := cloneFromBare(t, tmpDir, bareDir)

	// Clone the bare remote again as a "writer", commit a new commit, and push
	// it as a non-main "feature" branch so it appears on the remote AFTER the
	// local repo was created (the local repo must not already have the feature
	// tracking ref, otherwise the pull would be a no-op).
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
	featureFile, err := writerWT.Filesystem.Create("feature.txt")
	if err != nil {
		t.Fatalf("create feature file: %v", err)
	}
	_, _ = featureFile.Write([]byte("feature content"))
	_ = featureFile.Close()
	if _, err := writerWT.Add("feature.txt"); err != nil {
		t.Fatalf("add feature: %v", err)
	}
	if _, err := writerWT.Commit("feature commit", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("feature commit: %v", err)
	}
	if err := writer.Push(&git.PushOptions{
		RefSpecs: []config.RefSpec{config.RefSpec("refs/heads/main:refs/heads/feature")},
	}); err != nil {
		t.Fatalf("push feature branch: %v", err)
	}

	// The feature branch's tip hash on the remote.
	remoteRepo, err := git.PlainOpen(bareDir)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	featureRef, err := remoteRepo.Reference(plumbing.ReferenceName("refs/heads/feature"), true)
	if err != nil {
		t.Fatalf("remote feature ref: %v", err)
	}
	featureHash := featureRef.Hash()

	// Local repo starts on main; FetchAndMerge the non-main "feature" branch.
	err = gs.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		newHash, err := gs.FetchAndMerge(ctx(), "origin", "feature")
		if err != nil {
			return fmt.Errorf("FetchAndMerge(feature): %w", err)
		}
		if newHash != featureHash {
			return fmt.Errorf("expected feature hash %s, got %s", featureHash, newHash)
		}

		// The local branch must point at the feature tip.
		localRef, refErr := gs.repo.Reference(plumbing.ReferenceName("refs/heads/feature"), true)
		if refErr != nil {
			return fmt.Errorf("resolve local feature ref: %w", refErr)
		}
		if localRef.Hash() != featureHash {
			return fmt.Errorf("local feature ref: got %s, want %s", localRef.Hash(), featureHash)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestFetchAndMerge_NonMainBranch: %v", err)
	}
}

// TestFetchAndMerge_PullAfterWipe exercises the pull-after-wipe fast-forward
// path: the remote has a "wipe" commit that removed data, the local was
// cloned before the wipe and has stale untracked files from a pre-wipe type.
// After FetchAndMerge fast-forwards local main to the remote's wipe commit,
// the Force:true checkout replaces tracked files and the explicit
// wt.Clean removes stale untracked files — the working tree must exactly
// match the remote state.
func TestFetchAndMerge_PullAfterWipe(t *testing.T) {
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	// Create a seed repo with a data commit.
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
	sf, err := seedWT.Filesystem.Create("data.txt")
	if err != nil {
		t.Fatalf("create data: %v", err)
	}
	_, _ = sf.Write([]byte("initial data"))
	_ = sf.Close()
	if _, err := seedWT.Add("data.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := seedWT.Commit("data commit", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("commit data: %v", err)
	}

	// Clone as bare remote.
	_, err = git.PlainClone(bareDir, true, &git.CloneOptions{
		URL: "file://" + seedDir,
	})
	if err != nil {
		t.Fatalf("clone bare: %v", err)
	}

	// Clone local from the bare remote (local now has data.txt).
	localDir := filepath.Join(tmpDir, "local")
	localRepo, err := git.PlainClone(localDir, false, &git.CloneOptions{
		URL: "file://" + bareDir,
	})
	if err != nil {
		t.Fatalf("clone local: %v", err)
	}
	localWT, err := localRepo.Worktree()
	if err != nil {
		t.Fatalf("local worktree: %v", err)
	}

	// Create a stale untracked file in the local working tree — simulates
	// a previously wiped type's leftover files.
	if err := localWT.Filesystem.MkdirAll("entities/OldType", 0755); err != nil {
		t.Fatalf("mkdir stale dir: %v", err)
	}
	stale, err := localWT.Filesystem.Create("entities/OldType/stale.json")
	if err != nil {
		t.Fatalf("create stale file: %v", err)
	}
	_ = stale.Close()

	gs := &gitStore{
		repo:     localRepo,
		wt:       localWT,
		fs:       localWT.Filesystem,
		backend:  localRepo.Storer,
		basePath: t.TempDir(),
	}

	// Push a "wipe" commit to the remote that removes data.txt.
	workDir := filepath.Join(tmpDir, "work")
	workRepo, err := git.PlainClone(workDir, false, &git.CloneOptions{
		URL: "file://" + bareDir,
	})
	if err != nil {
		t.Fatalf("clone work: %v", err)
	}
	workWT, err := workRepo.Worktree()
	if err != nil {
		t.Fatalf("work worktree: %v", err)
	}
	if _, err := workWT.Remove("data.txt"); err != nil {
		t.Fatalf("remove data: %v", err)
	}
	if _, err := workWT.Commit("wipe", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("commit wipe: %v", err)
	}
	wipeHash, err := workRepo.Head()
	if err != nil {
		t.Fatalf("work head: %v", err)
	}
	if err := workRepo.Push(&git.PushOptions{}); err != nil {
		t.Fatalf("push wipe: %v", err)
	}

	err = gs.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		// Fast-forward from data to wipe
		newHash, pullErr := gs.FetchAndMerge(ctx(), "origin", "main")
		if pullErr != nil {
			return fmt.Errorf("FetchAndMerge: %w", pullErr)
		}
		if newHash != wipeHash.Hash() {
			return fmt.Errorf("expected wipe hash %s, got %s", wipeHash.Hash(), newHash)
		}

		// data.txt must be gone (tracked file removed by wipe commit)
		if _, statErr := gs.fs.Stat("data.txt"); statErr == nil {
			return fmt.Errorf("data.txt should be removed after wipe fast-forward")
		}

		// Stale untracked file must be cleaned by the post-checkout wt.Clean
		if _, statErr := gs.fs.Stat("entities/OldType/stale.json"); statErr == nil {
			return fmt.Errorf("stale untracked file should be cleaned after pull-after-wipe fast-forward")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("TestFetchAndMerge_PullAfterWipe: %v", err)
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
		if _, err := gs.FetchAndMerge(ctx(), "origin", "main"); err != nil {
			return fmt.Errorf("pull: %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("TestPullAlreadyUpToDate2: %v", err)
	}
}
