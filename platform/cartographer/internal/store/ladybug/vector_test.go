package ladybug

import (
	"context"
	"errors"
	"testing"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// TestVectorBootstrapIndexFailureHealsOnReopen pins the crash-atomicity of the
// CreateEntity vector bootstrap (crud.go): an index-creation failure leaves the
// crash residue of the window between ALTER TABLE ADD embedding and
// CREATE_VECTOR_INDEX — the embedding column exists in the catalog, the vector
// index does not, and schema.json records neither. The pre-fix store failed
// every subsequent Open on this residue ("vector index does not match schema
// metadata"); the Open-time reconcile must complete the interrupted bootstrap
// and adopt the vector state so the store reopens usable.
func TestVectorBootstrapIndexFailureHealsOnReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	for _, branch := range []string{"", "tx-vector-index-failure"} {
		name := "main"
		if branch != "" {
			name = "branch"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			database, err := Open(dir)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			db := database.(*ladybugDB)
			vectorSchema := &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
				Name: "Vector", EnableVectorIndex: true,
				Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
			}}}
			if err := database.ApplySchema(context.Background(), vectorSchema); err != nil {
				t.Fatalf("ApplySchema: %v", err)
			}
			if branch != "" {
				if err := database.CreateBranchDB(context.Background(), branch); err != nil {
					t.Fatalf("CreateBranchDB: %v", err)
				}
				if err := database.ReplicateSchemaToBranch(context.Background(), branch); err != nil {
					t.Fatalf("ReplicateSchemaToBranch: %v", err)
				}
			}
			createIndex := db.createVectorIndex
			db.createVectorIndex = func(*lbug.Connection, string) error {
				return errors.New("injected vector index failure")
			}
			if _, err := database.CreateEntity(
				context.Background(), "Vector", "", map[string]string{"name": "first"}, []float32{1, 2}, branch,
			); err == nil {
				t.Fatal("expected vector index failure")
			}
			db.createVectorIndex = createIndex
			if _, err := database.CreateEntity(
				context.Background(), "Vector", "", map[string]string{"name": "retry"}, []float32{1, 2}, branch,
			); !errors.Is(err, store.ErrDatabaseNotReady) {
				t.Fatalf("retry used failed database: %v", err)
			}
			if err := database.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			// The interrupted bootstrap must be completed and adopted on reopen,
			// not brick the store: the column FLOAT[2] exists in the catalog, the
			// vector index is missing, and schema.json records neither.
			reopened, err := Open(dir)
			if err != nil {
				t.Fatalf("reopen after interrupted vector bootstrap: %v", err)
			}
			defer closeStore(t, reopened)
			assertVectorIndexState(t, reopened, "Vector", branch, true, "interrupted vector index not completed on reopen")
			if dim, derr := reopened.GetEstablishedDimension(context.Background(), "Vector", branch); derr != nil || dim != 2 {
				t.Fatalf("vector dimension after recovery = %d, %v, want 2", dim, derr)
			}
			if _, cerr := reopened.CreateEntity(
				context.Background(), "Vector", "", map[string]string{"name": "after"}, []float32{3, 4}, branch,
			); cerr != nil {
				t.Fatalf("store not usable after recovery: %v", cerr)
			}
		})
	}
}

// TestBranchVectorMetadataPublishFailureRecoversOnReopen pins the
// crash-atomicity of the branch vector bootstrap: a metadata-publish failure
// leaves the crash residue of the window between the bootstrap DDL and the
// metadata publish — the branch catalog carries the embedding column/index
// while branches/<txID>.schema.json (written by ReplicateSchemaToBranch) does
// not. The pre-fix store failed every reopen of that branch (and
// RecoverOpenTransactions treats that as a hard startup failure); the Open-time
// reconcile must adopt the branch's catalog vector state so the branch reopens
// usable. The in-memory rejection of the retry (the branch is marked failed by
// the failed publish) is unchanged.
func TestBranchVectorMetadataPublishFailureRecoversOnReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	database, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db := database.(*ladybugDB)
	if err := database.ApplySchema(context.Background(), &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
		Name: "Vector", EnableVectorIndex: true,
		Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
	}}}); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	const branch = "tx-vector-metadata-failure"
	if err := database.CreateBranchDB(context.Background(), branch); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := database.ReplicateSchemaToBranch(context.Background(), branch); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}
	writeMetadata := db.writeMetadata
	db.writeMetadata = func(string, schemaMetadata) error {
		return errors.New("injected branch metadata publish failure")
	}
	if _, err := database.CreateEntity(
		context.Background(), "Vector", "", map[string]string{"name": "first"}, []float32{1, 2}, branch,
	); err == nil {
		t.Fatal("expected branch metadata publish failure")
	}
	db.writeMetadata = writeMetadata
	if _, err := database.CreateEntity(
		context.Background(), "Vector", "", map[string]string{"name": "retry"}, []float32{1, 2}, branch,
	); !errors.Is(err, store.ErrDatabaseNotReady) {
		t.Fatalf("retry used failed branch: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen main: %v", err)
	}
	defer closeStore(t, reopened)
	// The branch's bootstrapped vector state must be adopted on reopen rather
	// than bricking the branch (RecoverOpenTransactions aborts startup on a
	// branch reopen failure).
	if _, err := reopened.DumpAllEntities(context.Background(), branch); err != nil {
		t.Fatalf("branch unusable after metadata-publish crash residue: %v", err)
	}
	assertVectorIndexState(t, reopened, "Vector", branch, true, "branch vector state not recovered")
	if dim, derr := reopened.GetEstablishedDimension(context.Background(), "Vector", branch); derr != nil || dim != 2 {
		t.Fatalf("branch vector dimension after recovery = %d, %v, want 2", dim, derr)
	}
}

// TestMainVectorMetadataPublishFailureRecoversOnReopen pins the crash-atomicity
// of the CreateEntity vector bootstrap: a metadata-publish failure leaves the
// crash residue of the window between the bootstrap DDL (ALTER TABLE ADD
// embedding + CREATE_VECTOR_INDEX) and the metadata publish — the catalog
// carries the embedding column/index while schema.json does not record it. The
// pre-fix store failed every subsequent Open ("vector index does not match
// schema metadata"); the Open-time reconcile must adopt the catalog's vector
// state so the store reopens usable. The in-memory rejection of the retry (the
// store is marked failed by the failed publish) is unchanged.
func TestMainVectorMetadataPublishFailureRecoversOnReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	database, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db := database.(*ladybugDB)
	if err := database.ApplySchema(context.Background(), &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
		Name: "Vector", EnableVectorIndex: true,
		Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
	}}}); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	writeMetadata := db.writeMetadata
	db.writeMetadata = func(string, schemaMetadata) error {
		return errors.New("injected main metadata publish failure")
	}
	if _, err := database.CreateEntity(
		context.Background(), "Vector", "", map[string]string{"name": "first"}, []float32{1, 2}, "",
	); err == nil {
		t.Fatal("expected main metadata publish failure")
	}
	db.writeMetadata = writeMetadata
	if _, err := database.CreateEntity(
		context.Background(), "Vector", "", map[string]string{"name": "retry"}, []float32{1, 2}, "",
	); !errors.Is(err, store.ErrDatabaseNotReady) {
		t.Fatalf("retry used failed main database: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen after metadata-publish crash residue must recover: %v", err)
	}
	defer closeStore(t, reopened)
	if dim, derr := reopened.GetEstablishedDimension(context.Background(), "Vector", ""); derr != nil || dim != 2 {
		t.Fatalf("vector dimension after recovery = %d, %v, want 2", dim, derr)
	}
	assertVectorIndexState(t, reopened, "Vector", "", true, "vector state not recovered")
	if _, cerr := reopened.CreateEntity(
		context.Background(), "Vector", "", map[string]string{"name": "after"}, []float32{3, 4}, "",
	); cerr != nil {
		t.Fatalf("store not usable after recovery: %v", cerr)
	}
}

func TestFileBackedVectorBootstrapSurvivesMainAndBranchReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	database, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := database.ApplySchema(context.Background(), &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
		Name: "Vector", EnableVectorIndex: true,
		Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
	}}}); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	const branch = "tx-vector-success"
	if err := database.CreateBranchDB(context.Background(), branch); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := database.ReplicateSchemaToBranch(context.Background(), branch); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}
	mainEntity, err := database.CreateEntity(
		context.Background(), "Vector", "", map[string]string{"name": "main"}, []float32{1, 2}, "",
	)
	if err != nil {
		t.Fatalf("bootstrap main vector: %v", err)
	}
	branchEntity, err := database.CreateEntity(
		context.Background(), "Vector", "", map[string]string{"name": "branch"}, []float32{1, 2, 3}, branch,
	)
	if err != nil {
		t.Fatalf("bootstrap branch vector: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeStore(t, reopened)
	if dimension, err := reopened.GetEstablishedDimension(
		context.Background(), "Vector", "",
	); err != nil || dimension != 2 {
		t.Fatalf("main vector dimension after reopen = %d, %v", dimension, err)
	}
	if dimension, err := reopened.GetEstablishedDimension(
		context.Background(), "Vector", branch,
	); err != nil || dimension != 3 {
		t.Fatalf("branch vector dimension after reopen = %d, %v", dimension, err)
	}
	if _, err := reopened.GetEntity(context.Background(), mainEntity.Id, ""); err != nil {
		t.Fatalf("get reopened main vector entity: %v", err)
	}
	if _, err := reopened.GetEntity(context.Background(), branchEntity.Id, branch); err != nil {
		t.Fatalf("get reopened branch vector entity: %v", err)
	}
}

// TestRehydrateFromBranch_PromotedVectorMetadataSurvivesReopen asserts that a
// branch-only (bootstrap-first) vector type — declared on main with
// EnableVectorIndex but only bootstrapped inside a branch — has its promoted
// vector index/dimension persisted into main's schema.json by RehydrateFromBranch
// so a reopen's validateMetadataAgainstCatalog does not fail closed. Without the
// persistence, main's catalog carries the promoted embedding column/index while
// main's metadata still records VectorIndexes=false/VectorDimensions=0 and the
// reopen bricks startup.
func TestRehydrateFromBranch_PromotedVectorMetadataSurvivesReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	database, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Declare the vector type on main (Additive EnableVectorIndex) but do NOT
	// bootstrap the embedding column/index on main — that happens only on the
	// branch's first embedding write.
	if err := database.ApplySchema(context.Background(), &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
		Name: "Vector", EnableVectorIndex: true,
		Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
	}}}); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	assertVectorIndexState(t, database, "Vector", "", false,
		"expected Vector not bootstrapped on main before branch write")

	const branch = "tx-bootstrap-first"
	if err := database.CreateBranchDB(context.Background(), branch); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := database.ReplicateSchemaToBranch(context.Background(), branch); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}
	// The first embedding write happens on the branch, bootstrapping the
	// dimension only there.
	if _, err := database.CreateEntity(context.Background(), "Vector", "",
		map[string]string{"name": "branch"}, []float32{1, 2, 3}, branch); err != nil {
		t.Fatalf("bootstrap vector on branch: %v", err)
	}

	// Commit path: promote branch data (and the bootstrapped vector schema) to
	// main via RehydrateFromBranch.
	if err := database.RehydrateFromBranch(context.Background(), branch); err != nil {
		t.Fatalf("RehydrateFromBranch: %v", err)
	}
	assertVectorIndexState(t, database, "Vector", "", true, "expected vector index promoted to main after rehydrate")

	if err := database.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen must validate cleanly against persisted main metadata.
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen after rehydrate: %v", err)
	}
	defer closeStore(t, reopened)
	if dimension, derr := reopened.GetEstablishedDimension(
		context.Background(), "Vector", "",
	); derr != nil || dimension != 3 {
		t.Fatalf("main vector dimension after reopen = %d, error = %v, want 3", dimension, derr)
	}
}

func TestVectorIndex_Bootstrap(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	assertVectorIndexState(t, s, "VectorType", "", false, "expected not bootstrapped before first entity")

	_, err = s.CreateEntity(context.Background(), "VectorType", "",
		map[string]string{"name": "v1"}, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	assertVectorIndexState(t, s, "VectorType", "", true, "expected bootstrapped after first entity with embedding")
}

func TestFTSIndex_Search(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.CreateEntity(context.Background(), "Document", "",
		map[string]string{"title": "FTS Test Doc", "body": "This document is for FTS testing"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	results, err := s.FullTextSearch(context.Background(), "FTS", "Document", "")
	if err != nil {
		t.Fatalf("FullTextSearch: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected FTS results")
	}
}
