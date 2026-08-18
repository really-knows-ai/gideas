package ladybug

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/google/uuid"
)

// TestHydrateBranchFromFiles_InferredTypesSurviveFileBackedReopen pins the
// file-backed write-and-reopen cycle for types INFERRED during branch
// hydration (SPEC R8): HydrateBranchFromFiles registers inferred entity/edge
// types in the branch's in-memory defs but must also persist them to
// branches/<txID>.schema.json. ReplicateSchemaToBranch writes that metadata
// before hydration runs, so without a post-hydration rewrite a crash + restart
// reopens the branch (branchLocked → restoreBranchSchemaMetadata), whose
// validateMetadataAgainstCatalog fails hard with "database entity type X is
// absent from schema metadata" — and RecoverOpenTransactions treats that
// non-ErrBranchNotFound error as a hard startup failure instead of rolling
// back the one affected branch.
func TestHydrateBranchFromFiles_InferredTypesSurviveFileBackedReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()

	// An empty applied schema forces every hydrated type to be inferred from
	// the directory structure (SPEC R8).
	if err := s.ApplySchema(ctx, &flowv1.Schema{}); err != nil {
		t.Fatalf("ApplySchema empty: %v", err)
	}
	const branch = "tx1"
	if err := s.CreateBranchDB(ctx, branch); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}

	fromID := uuid.NewString()
	toID := uuid.NewString()
	edgeID := uuid.NewString()
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	edgesDir := filepath.Join(root, "edges")
	writeJSONFile(t, filepath.Join(entitiesDir, "Component", fromID+".json"), map[string]any{
		"id": fromID, "type": "Component",
	})
	writeJSONFile(t, filepath.Join(entitiesDir, "Component", toID+".json"), map[string]any{
		"id": toID, "type": "Component",
	})
	writeJSONFile(t, filepath.Join(edgesDir, "DependsOn", edgeID+".json"), map[string]any{
		"id": edgeID, "type": "DependsOn", "from": fromID, "to": toID,
		"properties": map[string]string{"strength": strengthValue},
	})

	if err := s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir); err != nil {
		t.Fatalf("HydrateBranchFromFiles: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen: the lazy branch reopen (DumpAllEntities) validates the persisted
	// branch metadata against the branch catalog. The inferred types and the
	// DependsOn FROM/TO pairs must be in branches/<txID>.schema.json or the
	// reopen wedges instead of recovering the branch.
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeStore(t, s2)
	entities, err := s2.DumpAllEntities(ctx, branch)
	if err != nil {
		t.Fatalf("reopened branch metadata failed validation: %v", err)
	}
	if len(entities) != 2 {
		t.Fatalf("reopened branch entities = %d, want 2", len(entities))
	}
	got, err := s2.GetEdge(ctx, edgeID, branch)
	if err != nil {
		t.Fatalf("branch edge lost across reopen: %v", err)
	}
	if got.FromEntityID != fromID || got.ToEntityID != toID {
		t.Fatalf("edge endpoints after reopen = %q -> %q, want %q -> %q",
			got.FromEntityID, got.ToEntityID, fromID, toID)
	}
	if got.Properties["strength"] != strengthValue {
		t.Fatalf("edge strength after reopen = %q, want %q", got.Properties["strength"], strengthValue)
	}
}

// TestRehydrateMainFromFiles_InferredTypeSurvivesFileBackedReopen pins the
// file-backed write-and-reopen cycle for an inferred type (SPEC R8): the
// inferred property type must be persisted to schema.json as the proto type
// "string" so the next Open's validateSchemaMetadata reconstructs a schema that
// schema.Validate accepts. Regression: the inference point used to store the
// catalog type "STRING", which validateSchemaMetadata fed back into
// schema.Validate and got rejected with ErrInvalidPropertyType — bricking the
// next file-backed Open.
func TestRehydrateMainFromFiles_InferredTypeSurvivesFileBackedReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%q): %v", dir, err)
	}
	ctx := context.Background()

	// Force the inferred-type path: empty applied schema.
	if err := s.ApplySchema(ctx, &flowv1.Schema{}); err != nil {
		t.Fatalf("ApplySchema empty: %v", err)
	}

	docID := uuid.NewString()
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	edgesDir := filepath.Join(root, "edges")
	writeJSONFile(t, filepath.Join(entitiesDir, "Document", docID+".json"), map[string]any{
		"id": docID, "type": "Document",
		"properties": map[string]string{"title": "inferred"},
	})
	if err := os.MkdirAll(edgesDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		t.Fatalf("RehydrateMainFromFiles: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen the file-backed store: must succeed and the inferred property must
	// survive as the proto type "string".
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen Open(%q): %v", dir, err)
	}
	defer closeStore(t, s2)

	def, ok := s2.EntityType("Document")
	if !ok {
		t.Fatal("expected Document type to survive reopen")
	}
	for _, p := range def.Properties {
		if p.Name == "title" && p.Type != "string" {
			t.Fatalf("inferred property title type = %q, want %q", p.Type, "string")
		}
	}
}

// TestRehydrateMainFromFiles_InferredTypePromotesEnableVectorIndex verifies
// the re-hydration metadata parity (Finding 12): when a vector-capable entity
// is loaded on the file-based re-hydration path for a type that was inferred
// from the directory structure (EnableVectorIndex was never declared true), the
// store must promote EnableVectorIndex on the resulting definition so it stays
// consistent with the embedding column/index actually created. Without this the
// in-memory def disagrees with the metadata model and with SearchNeighbors.
func TestRehydrateMainFromFiles_InferredTypePromotesEnableVectorIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed rehydration from working tree")
	}
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	// Force the inferred-type path: empty applied schema.
	if err := s.ApplySchema(ctx, &flowv1.Schema{}); err != nil {
		t.Fatalf("ApplySchema empty: %v", err)
	}

	id := uuid.NewString()
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	edgesDir := filepath.Join(root, "edges")
	writeJSONFile(t, filepath.Join(entitiesDir, "Document", id+".json"), map[string]any{
		"id": id, "type": "Document", "properties": map[string]string{"title": "inferred"},
		"embedding": []float32{1, 2, 3},
	})
	if err := os.MkdirAll(edgesDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		t.Fatalf("RehydrateMainFromFiles: %v", err)
	}

	def, ok := s.EntityType("Document")
	if !ok {
		t.Fatal("expected Document type to be inferred and present")
	}
	if !def.EnableVectorIndex {
		t.Error("expected EnableVectorIndex to be promoted to true for re-hydrated type with embedding")
	}
	assertVectorIndexState(t, s, "Document", "main", true,
		"expected vector index to be bootstrapped for re-hydrated type with embedding")
}
