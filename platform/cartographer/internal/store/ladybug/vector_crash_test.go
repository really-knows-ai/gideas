package ladybug

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func TestVectorBootstrapCrashWindow_UpdateEntityHealsOnReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.ApplySchema(ctx, vectorBootstrapCrashSchema()); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	// Create a Vector entity without an embedding via the file load path: the
	// only pre-bootstrap way to persist a no-embedding row (SPEC R7 rejects a
	// no-embedding CreateEntity before the bootstrap).
	id := uuid.NewString()
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	edgesDir := filepath.Join(root, "edges")
	writeJSONFile(t, filepath.Join(entitiesDir, "Vector", id+".json"), map[string]any{
		"id": id, "type": "Vector", "properties": map[string]string{"name": "v"},
	})
	if err := os.MkdirAll(edgesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		t.Fatalf("RehydrateMainFromFiles: %v", err)
	}
	// Snapshot the pre-bootstrap metadata: the schema.json a crash would leave
	// if it caught the process between the bootstrap DDL and the metadata
	// publish.
	metadataPath := filepath.Join(dir, "schema.json")
	preBootstrap, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read pre-bootstrap metadata: %v", err)
	}
	// The first embedding update runs the bootstrap DDL (locking dimension 3),
	// creates the index after the write, and persists the embedding.
	updated, err := s.UpdateEntity(ctx, id, nil, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("bootstrap-then-persist from UpdateEntity: %v", err)
	}
	if !reflect.DeepEqual(updated.Embedding, []float32{1, 2, 3}) {
		t.Fatalf("embedding after bootstrap update = %v, want [1 2 3]", updated.Embedding)
	}
	// Fabricate the crash residue: the catalog now carries the embedding
	// column/index, but schema.json still describes the pre-bootstrap state.
	if err := os.WriteFile(metadataPath, preBootstrap, 0600); err != nil {
		t.Fatalf("revert metadata: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("Open after UpdateEntity bootstrap crash must recover, got %v", err)
	}
	defer closeStore(t, reopened)
	if dim, derr := reopened.GetEstablishedDimension(ctx, "Vector", ""); derr != nil || dim != 3 {
		t.Fatalf("vector dimension after recovery = %d, %v, want 3", dim, derr)
	}
	assertVectorIndexState(t, reopened, "Vector", "", true, "vector index not recovered after update-bootstrap crash")
	// The healed store accepts a matching-dimension embedding write.
	if _, cerr := reopened.CreateEntity(
		ctx, "Vector", "", map[string]string{"name": "after"}, []float32{4, 5, 6}, "",
	); cerr != nil {
		t.Fatalf("CreateEntity after recovery: %v", cerr)
	}
}

// TestVectorBootstrapCrashWindow_EmbeddingRewriteHealsOnReopen pins the
// recovery of the embedding-rewrite crash window introduced by
// crud.go's UpdateEntity: a matching-dimension embedding rewrite drops the
// vector index before the write and recreates it after, so a crash caught
// between the DROP_VECTOR_INDEX and the CREATE_VECTOR_INDEX leaves the catalog
// carrying the FLOAT[n] column (dimension still locked) without its index while
// schema.json records it indexed. reconcileVectorStateFromCatalog must complete
// the missing index on Open, or validation bricks startup.
func TestVectorBootstrapCrashWindow_EmbeddingRewriteHealsOnReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.ApplySchema(ctx, vectorBootstrapCrashSchema()); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	// Bootstrap the dimension + vector index via a create with an embedding.
	ent, err := s.CreateEntity(ctx, "Vector", "", map[string]string{"name": "v"}, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("bootstrap CreateEntity: %v", err)
	}
	// A matching-dimension embedding rewrite succeeds (drop → SET → recreate).
	if updated, uerr := s.UpdateEntity(ctx, ent.Id, nil, []float32{4, 5, 6}, ""); uerr != nil {
		t.Fatalf("embedding rewrite: %v", uerr)
	} else if !reflect.DeepEqual(updated.Embedding, []float32{4, 5, 6}) {
		t.Fatalf("rewritten embedding = %v, want [4 5 6]", updated.Embedding)
	}
	// Fabricate the crash residue: the catalog now lacks the vector index
	// (the DROP committed, the CREATE never ran) while schema.json still
	// records the type as indexed with dimension 3.
	db := s.(*ladybugDB)
	res, err := db.conn.Query("CALL DROP_VECTOR_INDEX('Vector', 'Vector_vec');")
	if err != nil {
		t.Fatalf("drop vector index: %v", err)
	}
	res.Close()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("Open after embedding-rewrite crash must recover, got %v", err)
	}
	defer closeStore(t, reopened)
	if dim, derr := reopened.GetEstablishedDimension(ctx, "Vector", ""); derr != nil || dim != 3 {
		t.Fatalf("vector dimension after recovery = %d, %v, want 3", dim, derr)
	}
	assertVectorIndexState(t, reopened, "Vector", "", true, "vector index not recovered after embedding-rewrite crash")
	// The rewritten embedding survived the crash (the SET committed before it).
	got, gerr := reopened.GetEntity(ctx, ent.Id, "")
	if gerr != nil {
		t.Fatalf("GetEntity after recovery: %v", gerr)
	}
	if !reflect.DeepEqual(got.Embedding, []float32{4, 5, 6}) {
		t.Fatalf("persisted embedding after recovery = %v, want [4 5 6]", got.Embedding)
	}
}

func TestVectorBootstrapCrashWindow_RehydrateFromBranchHealsOnReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	ctx := context.Background()
	dir := t.TempDir()
	database, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := database.ApplySchema(ctx, vectorBootstrapCrashSchema()); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	// Snapshot main's pre-promotion metadata: the schema.json a crash would
	// leave if it caught the promote path between the copy-loop DDL and the
	// final persistMainVectorMetadataLocked.
	metadataPath := filepath.Join(dir, "schema.json")
	prePromote, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read pre-promotion metadata: %v", err)
	}
	const branch = "tx-promote-crash"
	if err := database.CreateBranchDB(ctx, branch); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := database.ReplicateSchemaToBranch(ctx, branch); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}
	// Bootstrap the dimension on the branch, then promote it to main.
	branchEntity, err := database.CreateEntity(ctx, "Vector", "",
		map[string]string{"name": "b"}, []float32{1, 2, 3}, branch)
	if err != nil {
		t.Fatalf("bootstrap vector on branch: %v", err)
	}
	if err := database.RehydrateFromBranch(ctx, branch); err != nil {
		t.Fatalf("RehydrateFromBranch: %v", err)
	}
	// Fabricate the crash residue: main's catalog carries the promoted vector
	// state, but main's schema.json is still the pre-promotion metadata.
	if err := os.WriteFile(metadataPath, prePromote, 0600); err != nil {
		t.Fatalf("revert metadata: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("Open after promote-path crash must recover, got %v", err)
	}
	defer closeStore(t, reopened)
	if dim, derr := reopened.GetEstablishedDimension(ctx, "Vector", ""); derr != nil || dim != 3 {
		t.Fatalf("promoted vector dimension after recovery = %d, %v, want 3", dim, derr)
	}
	// The entity promoted before the crash survives.
	if _, gerr := reopened.GetEntity(ctx, branchEntity.Id, ""); gerr != nil {
		t.Fatalf("promoted entity lost after recovery: %v", gerr)
	}
}

func TestVectorBootstrapCrashWindow_RehydrateMainFromFilesHealsOnReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.ApplySchema(ctx, vectorBootstrapCrashSchema()); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	// Snapshot the pre-load metadata: the schema.json a crash would leave if it
	// caught the file-load path between the embedding-bootstrap DDL and the
	// final persistMainVectorMetadataLocked.
	metadataPath := filepath.Join(dir, "schema.json")
	preLoad, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read pre-load metadata: %v", err)
	}
	id := uuid.NewString()
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	edgesDir := filepath.Join(root, "edges")
	writeJSONFile(t, filepath.Join(entitiesDir, "Vector", id+".json"), map[string]any{
		"id": id, "type": "Vector", "properties": map[string]string{"name": "v"},
		"embedding": []float32{1, 2, 3},
	})
	if err := os.MkdirAll(edgesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		t.Fatalf("RehydrateMainFromFiles: %v", err)
	}
	// Fabricate the crash residue: main's catalog carries the bootstrapped
	// vector state, but main's schema.json is still the pre-load metadata.
	if err := os.WriteFile(metadataPath, preLoad, 0600); err != nil {
		t.Fatalf("revert metadata: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("Open after file-load crash must recover, got %v", err)
	}
	defer closeStore(t, reopened)
	if dim, derr := reopened.GetEstablishedDimension(ctx, "Vector", ""); derr != nil || dim != 3 {
		t.Fatalf("vector dimension after recovery = %d, %v, want 3", dim, derr)
	}
	if _, gerr := reopened.GetEntity(ctx, id, ""); gerr != nil {
		t.Fatalf("loaded entity lost after recovery: %v", gerr)
	}
}

func TestVectorBootstrapCrashWindow_BranchFileLoadHealsOnReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	ctx := context.Background()
	dir := t.TempDir()
	database, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := database.ApplySchema(ctx, vectorBootstrapCrashSchema()); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	const branch = "tx-file-load-crash"
	if err := database.CreateBranchDB(ctx, branch); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := database.ReplicateSchemaToBranch(ctx, branch); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}
	// Snapshot the pre-hydration branch metadata: the schema.json a crash would
	// leave if it caught HydrateBranchFromFiles between the bootstrap DDL and
	// the final branch metadata write.
	branchMetadataPath := filepath.Join(dir, "branches", branch+".schema.json")
	preLoad, err := os.ReadFile(branchMetadataPath)
	if err != nil {
		t.Fatalf("read pre-hydration branch metadata: %v", err)
	}
	id := uuid.NewString()
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	edgesDir := filepath.Join(root, "edges")
	writeJSONFile(t, filepath.Join(entitiesDir, "Vector", id+".json"), map[string]any{
		"id": id, "type": "Vector", "properties": map[string]string{"name": "v"},
		"embedding": []float32{1, 2, 3},
	})
	if err := os.MkdirAll(edgesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := database.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir); err != nil {
		t.Fatalf("HydrateBranchFromFiles: %v", err)
	}
	// Fabricate the crash residue: the branch catalog carries the bootstrapped
	// vector state, but the branch metadata is still the pre-hydration state.
	if err := os.WriteFile(branchMetadataPath, preLoad, 0600); err != nil {
		t.Fatalf("revert branch metadata: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen main: %v", err)
	}
	defer closeStore(t, reopened)
	// The branch must reopen with its bootstrapped vector state adopted rather
	// than bricking startup via RecoverOpenTransactions.
	entities, err := reopened.DumpAllEntities(ctx, branch)
	if err != nil {
		t.Fatalf("branch unusable after file-load crash residue: %v", err)
	}
	if len(entities) != 1 {
		t.Fatalf("loaded branch entities after recovery = %d, want 1", len(entities))
	}
	if dim, derr := reopened.GetEstablishedDimension(ctx, "Vector", branch); derr != nil || dim != 3 {
		t.Fatalf("branch vector dimension after recovery = %d, %v, want 3", dim, derr)
	}
}
