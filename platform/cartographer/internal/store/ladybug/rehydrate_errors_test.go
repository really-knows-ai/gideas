package ladybug

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	"github.com/google/uuid"
)

func TestLoadEntitiesFromDir_ReadDirError(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed rehydration from working tree")
	}
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	db := s.(*ladybugDB)
	entitiesDir := t.TempDir()
	compDir := filepath.Join(entitiesDir, "Component")
	if err := os.MkdirAll(compDir, 0755); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("injected readdir failure")
	db.readDir = func(path string) ([]os.DirEntry, error) {
		if path == compDir {
			return nil, wantErr
		}
		return os.ReadDir(path)
	}

	// The former "main" / "on connection" loader pair was merged into
	// loadEntitiesFromDirOnConn (branch.go item 2 dedup); pin the merged
	// function's error propagation once.
	loadErr := db.loadEntitiesFromDirOnConn(db.conn, entitiesDir, db.entityTypeDefs)
	if !errors.Is(loadErr, wantErr) {
		t.Fatalf("expected injected ReadDir error, got %v", loadErr)
	}
	want := fmt.Sprintf("read entities dir %q", compDir)
	if !strings.Contains(loadErr.Error(), want) {
		t.Fatalf("error %q does not identify wrapped operation and path %q", loadErr, want)
	}
}

func TestRehydrateMainFromFiles_HoldsLockForEntireOperation(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	// Concurrent reads during rehydration must not observe partial state.
	// The rehydration must hold db.mu for the entire wipe-and-load cycle.
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	// Pre-populate with one entity.
	old, err := s.CreateEntity(ctx, "Component", "",
		map[string]string{"name": "old"}, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	// Prepare rehydration files. The pre-existing entity is re-inserted from
	// the files too, so old.Id is present in main both before the wipe and
	// after re-hydration — a concurrent read of it can never legitimately
	// observe ErrEntityNotFound.
	componentID := uuid.NewString()
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	edgesDir := filepath.Join(root, "edges")
	writeJSONFile(t, filepath.Join(entitiesDir, "Component", componentID+".json"), map[string]any{
		"id": componentID, "type": "Component", "properties": map[string]string{"name": "new"},
	})
	writeJSONFile(t, filepath.Join(entitiesDir, "Component", old.Id+".json"), map[string]any{
		"id": old.Id, "type": "Component", "properties": map[string]string{"name": "old"},
	})
	// Create empty edges directory so the check passes.
	if err := os.MkdirAll(edgesDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Start rehydration in background.
	rehydrateDone := make(chan error, 1)
	go func() {
		rehydrateDone <- s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
	}()

	// While rehydration is in progress, attempt a concurrent read.
	// If db.mu is held for the entire cycle, this read either runs before the
	// wipe (sees the old entity) or blocks until rehydration completes (sees
	// the re-inserted old entity) — never a "not found" in between.
	type readResult struct {
		entity *store.Entity
		err    error
	}
	readCh := make(chan readResult, 1)
	go func() {
		e, err := s.GetEntity(ctx, old.Id, "main")
		readCh <- readResult{e, err}
	}()

	// Wait for rehydration to finish.
	if err := <-rehydrateDone; err != nil {
		t.Fatalf("RehydrateMainFromFiles: %v", err)
	}

	// Now the concurrent read should have completed (it was serialized behind
	// the rehydration lock). Because the files re-insert the old entity, the
	// read of old.Id must always succeed: it runs either before the wipe
	// (old present) or after the re-insert (old present again). ErrEntityNotFound
	// would mean the read observed the wipe without the re-insert — exactly the
	// partial-wipe outcome the held lock is supposed to prevent.
	r := <-readCh
	if r.err != nil {
		t.Fatalf("concurrent read observed a partial wipe during RehydrateMainFromFiles: %v", r.err)
	}
	// We verify the final state is correct.
	got, err := s.GetEntity(ctx, componentID, "main")
	if err != nil {
		t.Fatalf("final GetEntity: %v", err)
	}
	if got.Properties["name"] != "new" {
		t.Fatalf("expected name=new, got %v", got.Properties)
	}
}

func TestLoadEntitiesFromDir_ReadFileError(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed rehydration from working tree")
	}
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	db := s.(*ladybugDB)
	entitiesDir := t.TempDir()
	compDir := filepath.Join(entitiesDir, "Component")
	if err := os.MkdirAll(compDir, 0755); err != nil {
		t.Fatal(err)
	}
	fpath := filepath.Join(compDir, "comp1.json")
	if err := os.Symlink(filepath.Join(compDir, "missing.json"), fpath); err != nil {
		t.Fatal(err)
	}

	// The former "main" / "on connection" loader pair was merged into
	// loadEntitiesFromDirOnConn (branch.go item 2 dedup); pin the merged
	// function's error propagation once.
	loadErr := db.loadEntitiesFromDirOnConn(db.conn, entitiesDir, db.entityTypeDefs)
	if !errors.Is(loadErr, os.ErrNotExist) {
		t.Fatalf("expected dangling-symlink ReadFile error, got %v", loadErr)
	}
	want := fmt.Sprintf("read entity file %q", fpath)
	if !strings.Contains(loadErr.Error(), want) {
		t.Fatalf("error %q does not identify wrapped operation and path %q", loadErr, want)
	}
}

// Re-hydration ties an element's label to its directory name (SPEC R8), so a
// JSON file's `type` key must match the directory it lives under. A mismatch
// would store the element under one label while its domain Type reports
// another, so findEntityByID/findEdgeByID (which match by label) would
// disagree with the returned type. Both paths (main and branch) must reject
// such a file.
func TestRehydrateMainFromFiles_RejectsTypeDirectoryMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed rehydration from working tree")
	}
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	id := uuid.NewString()
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	edgesDir := filepath.Join(root, "edges")
	// File is under the VectorType directory but declares type "Document". It
	// also carries an embedding, pinning the ordering of the directory-mismatch
	// guard against the embedding-bootstrap DDL (branch.go
	// loadEntitiesFromDirOnConn):
	// the guard must reject the file BEFORE ensureEmbeddingLoadSchema ALTERs the
	// directory-named table and locks a vector dimension on it (a file about to
	// be rejected must never mutate schema state — SPEC R8 fail-loudly).
	writeJSONFile(t, filepath.Join(entitiesDir, "VectorType", id+".json"), map[string]any{
		"id": id, "type": "Document", "properties": map[string]string{"name": "mismatch"},
		"embedding": []float32{1, 2, 3},
	})
	if err := s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err == nil {
		t.Fatal("expected error for entity type/directory mismatch")
	} else if !errors.Is(err, store.ErrInvalidEntityDir) {
		t.Fatalf("expected ErrInvalidEntityDir, got %v", err)
	}
	// The guard must have rejected the file before the embedding bootstrap ran:
	// VectorType must not have gained an embedding column / vector index as a
	// side effect of the rejected file.
	assertVectorIndexState(t, s, "VectorType", "", false,
		"embedding bootstrap DDL must not run before the type/directory mismatch guard")
}

// The same type/directory mismatch guard must apply on the branch load path.
func TestHydrateBranchFromFiles_RejectsTypeDirectoryMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed rehydration from working tree")
	}
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	if err := s.CreateBranchDB(ctx, "tx1"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(ctx, "tx1"); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}

	id := uuid.NewString()
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	edgesDir := filepath.Join(root, "edges")
	if err := os.MkdirAll(edgesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Same ordering pin as the main path: the embedding must not bootstrap the
	// branch VectorType table before the mismatch guard rejects the file.
	writeJSONFile(t, filepath.Join(entitiesDir, "VectorType", id+".json"), map[string]any{
		"id": id, "type": "Document", "properties": map[string]string{"name": "mismatch"},
		"embedding": []float32{1, 2, 3},
	})
	if err := s.HydrateBranchFromFiles(ctx, "tx1", entitiesDir, edgesDir); err == nil {
		t.Fatal("expected error for entity type/directory mismatch")
	} else if !errors.Is(err, store.ErrInvalidEntityDir) {
		t.Fatalf("expected ErrInvalidEntityDir, got %v", err)
	}
	assertVectorIndexState(t, s, "VectorType", "tx1", false,
		"embedding bootstrap DDL must not run before the type/directory mismatch guard (branch)")
}
