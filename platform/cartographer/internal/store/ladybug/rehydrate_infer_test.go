package ladybug

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/google/uuid"
)

// TestReplicateSchemaToBranch_RealInferredPairsAfterBothLostRehydration pins the
// stale-structural-pointer rule on the SPEC R8 both-lost recovery corner
// (corrupt main.lbug + absent schema.json + committed git data): every edge
// type is inferred from the directory structure, so RehydrateMainFromFiles must
// re-wire db.edgePairs from the catalog. Without it the next BeginTransaction's
// ReplicateSchemaToBranch reads a nil pair map and creates the branch rel table
// with `_untyped` placeholder endpoint clauses, after which
// HydrateBranchFromFiles' ensureEdgeLoadSchema early-returns for the already
// registered type and every branch edge is silently dropped (insertEdgeOnConn's
// CREATE silently no-ops against the mismatched endpoints).
func TestReplicateSchemaToBranch_RealInferredPairsAfterBothLostRehydration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	schema := &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
		Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
	}}}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Lose both: corrupt main.lbug and remove schema.json.
	dbPath := filepath.Join(dir, "main.lbug")
	if err := os.WriteFile(dbPath, []byte("not a ladybug database"), 0600); err != nil {
		t.Fatalf("corrupt main.lbug: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "schema.json")); err != nil {
		t.Fatalf("remove schema.json: %v", err)
	}

	// Open recovers a fresh empty database — the R8 both-lost corner where
	// every edge type present in the committed git data is inferred.
	recovered, err := Open(dir)
	if err != nil {
		t.Fatalf("Open after both-lost corruption: %v", err)
	}
	defer closeStore(t, recovered)

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

	if err := recovered.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		t.Fatalf("RehydrateMainFromFiles: %v", err)
	}

	// The re-hydrated store must have re-wired db.edgePairs so the branch rel
	// table is created with the real inferred FROM/TO endpoint pairs.
	db := recovered.(*ladybugDB)
	if got := db.edgePairs["DependsOn"]; !equalFromToPairs(got, []fromToPair{{From: "Component", To: "Component"}}) {
		t.Fatalf("main edgePairs for DependsOn = %v, want Component->Component", got)
	}

	const branch = "tx1"
	if err := recovered.CreateBranchDB(ctx, branch); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := recovered.ReplicateSchemaToBranch(ctx, branch); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}
	br := db.branches[branch]
	gotPairs, err := connectionPairsOnConn(br.conn, "DependsOn")
	if err != nil {
		t.Fatalf("read branch rel endpoints: %v", err)
	}
	if !equalFromToPairs(gotPairs, []fromToPair{{From: "Component", To: "Component"}}) {
		t.Fatalf("branch rel table DependsOn endpoints = %v, want Component->Component (not the _untyped placeholder)",
			gotPairs)
	}

	// HydrateBranchFromFiles' ensureEdgeLoadSchema early-returns for the
	// already-registered type, so the branch rel table above is the one edges
	// land on — the edge must survive, never silently dropped.
	if err := recovered.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir); err != nil {
		t.Fatalf("HydrateBranchFromFiles: %v", err)
	}
	got, err := recovered.GetEdge(ctx, edgeID, branch)
	if err != nil {
		t.Fatalf("branch edge silently dropped: %v", err)
	}
	if got.FromEntityID != fromID || got.ToEntityID != toID {
		t.Fatalf("branch edge endpoints = %q -> %q, want %q -> %q",
			got.FromEntityID, got.ToEntityID, fromID, toID)
	}
}
// TestRehydrateMainFromFiles_InferredEdgeTypeSurvivesFileBackedReopen pins the
// both-lost write-and-reopen cycle for an inferred EDGE type (SPEC R8): the
// schema metadata persisted by the re-hydration path must carry the edge type's
// real FROM/TO endpoint pairs. Without them, a subsequent Open's
// applySchemaMetadata derives an empty pair set for an inferred type (it carries
// no connection rules) and validateMetadataAgainstCatalog normalizes the
// expected endpoints to the `_untyped` placeholder, which fails the comparison
// against the rel table's real endpoints ("relationship endpoints do not match
// schema metadata") — bricking every file-backed Open after a both-lost
// recovery that inferred edge types. The same lossy write affects the branch
// metadata (ReplicateSchemaToBranch), so the reopened branch metadata must
// validate too.
func TestRehydrateMainFromFiles_InferredEdgeTypeSurvivesFileBackedReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	schema := &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
		Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
	}}}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Lose both: corrupt main.lbug and remove schema.json.
	dbPath := filepath.Join(dir, "main.lbug")
	if err := os.WriteFile(dbPath, []byte("not a ladybug database"), 0600); err != nil {
		t.Fatalf("corrupt main.lbug: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "schema.json")); err != nil {
		t.Fatalf("remove schema.json: %v", err)
	}

	// Open recovers a fresh empty database — the R8 both-lost corner where every
	// edge type present in the committed git data is inferred.
	recovered, err := Open(dir)
	if err != nil {
		t.Fatalf("Open after both-lost corruption: %v", err)
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

	if err := recovered.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		t.Fatalf("RehydrateMainFromFiles: %v", err)
	}
	got, err := recovered.GetEdge(ctx, edgeID, "main")
	if err != nil {
		t.Fatalf("re-hydrated edge not served: %v", err)
	}
	if got.FromEntityID != fromID || got.ToEntityID != toID {
		t.Fatalf("re-hydrated edge endpoints = %q -> %q, want %q -> %q",
			got.FromEntityID, got.ToEntityID, fromID, toID)
	}

	// Replicate the inferred schema to a branch: the branch metadata write
	// mirrors the main write and must persist the same endpoint pairs.
	const branch = "tx1"
	if err := recovered.CreateBranchDB(ctx, branch); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := recovered.ReplicateSchemaToBranch(ctx, branch); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}

	if err := recovered.Close(); err != nil {
		t.Fatalf("Close after re-hydration: %v", err)
	}

	// Reopen the file-backed store: must succeed (the persisted main metadata
	// carries the inferred DependsOn FROM/TO pairs), the edge endpoints must
	// survive, and the persisted branch metadata must validate against the
	// branch catalog on the lazy branch reopen.
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen after both-lost re-hydration: %v", err)
	}
	defer closeStore(t, s2)
	db := s2.(*ladybugDB)
	if got := db.edgePairs["DependsOn"]; !equalFromToPairs(got, []fromToPair{{From: "Component", To: "Component"}}) {
		t.Fatalf("reopened main edgePairs for DependsOn = %v, want Component->Component", got)
	}
	got, err = s2.GetEdge(ctx, edgeID, "main")
	if err != nil {
		t.Fatalf("edge lost across reopen: %v", err)
	}
	if got.FromEntityID != fromID || got.ToEntityID != toID {
		t.Fatalf("edge endpoints after reopen = %q -> %q, want %q -> %q",
			got.FromEntityID, got.ToEntityID, fromID, toID)
	}
	if _, err := s2.DumpAllEntities(ctx, branch); err != nil {
		t.Fatalf("persisted branch metadata failed branch reopen validation: %v", err)
	}
}
func TestRehydrateMainFromFiles_BothMissing_NoError(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed rehydration from working tree")
	}
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	// Both directories absent — empty graph, no error.
	entitiesDir := filepath.Join(t.TempDir(), "no-entities")
	edgesDir := filepath.Join(t.TempDir(), "no-edges")

	ctx := context.Background()
	if err := s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		t.Fatalf("expected no error for both missing, got %v", err)
	}
}
// TestRehydrateMainFromFiles_InferredTypeWithProperties verifies SPEC R8: when
// re-hydrating with an empty applied schema, the entity type is inferred from
// the directory structure AND its property columns are created so that
// property-bearing JSON files can be persisted. Without column inference the
// replay of a property-bearing file would fail against a table with only the
// `id` column.
func TestRehydrateMainFromFiles_InferredTypeWithProperties(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed rehydration from working tree")
	}
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	// Apply an explicit empty schema so entDefs has length 0 — forcing the
	// inferred-type path during rehydration.
	if err := s.ApplySchema(ctx, &flowv1.Schema{}); err != nil {
		t.Fatalf("ApplySchema empty: %v", err)
	}

	docID := uuid.NewString()
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	edgesDir := filepath.Join(root, "edges")
	writeJSONFile(t, filepath.Join(entitiesDir, "Document", docID+".json"), map[string]any{
		"id": docID, "type": "Document",
		"properties": map[string]string{"title": "inferred", "body": "content"},
	})
	// Create empty edges directory so the completeness check passes.
	if err := os.MkdirAll(edgesDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		t.Fatalf("RehydrateMainFromFiles: %v", err)
	}

	// The entity with all properties must have been persisted.
	got, err := s.GetEntity(ctx, docID, "main")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if got.Properties["title"] != "inferred" {
		t.Fatalf("title = %q, want %q", got.Properties["title"], "inferred")
	}
	if got.Properties["body"] != "content" {
		t.Fatalf("body = %q, want %q", got.Properties["body"], "content")
	}
	if !s.TableExists("Document") {
		t.Fatal("expected Document table to be inferred")
	}
}
// TestRehydrateMainFromFiles_InferredEdgeTypeWithProperties verifies SPEC R8's
// directory-inference scope covers edge types as well as entity types: when
// re-hydrating with an empty applied schema (the corrupt main.lbug / lost
// schema.json recovery corner), an edge type absent from the applied schema
// must have its rel table (and endpoint pair) inferred from the directory
// structure so its files load instead of failing with a raw engine error
// against a non-existent rel table. Regression: the edge loaders called
// insertEdgeOnConn directly with no rel-table creation, so every edge insert
// for an inferred type failed with a raw engine error and re-hydration could
// not recover edge data.
func TestRehydrateMainFromFiles_InferredEdgeTypeWithProperties(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed rehydration from working tree")
	}
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	// Apply an explicit empty schema so edgeDefs has length 0 — forcing the
	// inferred-type path during rehydration.
	if err := s.ApplySchema(ctx, &flowv1.Schema{}); err != nil {
		t.Fatalf("ApplySchema empty: %v", err)
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

	if err := s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		t.Fatalf("RehydrateMainFromFiles: %v", err)
	}

	// The edge with all properties must have been persisted.
	got, err := s.GetEdge(ctx, edgeID, "main")
	if err != nil {
		t.Fatalf("GetEdge: %v", err)
	}
	if got.FromEntityID != fromID || got.ToEntityID != toID {
		t.Fatalf("edge endpoints = %q -> %q, want %q -> %q",
			got.FromEntityID, got.ToEntityID, fromID, toID)
	}
	if got.Properties["strength"] != strengthValue {
		t.Fatalf("strength = %q, want %q", got.Properties["strength"], strengthValue)
	}
	if _, ok := s.EdgeType("DependsOn"); !ok {
		t.Fatal("expected DependsOn edge type to be inferred")
	}
}
// TestHydrateBranchFromFiles_InferredEdgeTypeWithProperties pins the same SPEC
// R8 directory-inference scope on the branch hydration path: an edge type
// absent from the applied schema must have its rel table inferred so the
// branch's edge files load instead of failing with a raw engine error.
func TestHydrateBranchFromFiles_InferredEdgeTypeWithProperties(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed rehydration from working tree")
	}
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	// Apply an explicit empty schema so edgeDefs has length 0 — forcing the
	// inferred-type path during branch hydration.
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

	got, err := s.GetEdge(ctx, edgeID, branch)
	if err != nil {
		t.Fatalf("GetEdge on branch: %v", err)
	}
	if got.FromEntityID != fromID || got.ToEntityID != toID {
		t.Fatalf("edge endpoints = %q -> %q, want %q -> %q",
			got.FromEntityID, got.ToEntityID, fromID, toID)
	}
	if got.Properties["strength"] != strengthValue {
		t.Fatalf("strength = %q, want %q", got.Properties["strength"], strengthValue)
	}
	// EdgeType() reads main's cache only; the branch's inferred type is proven
	// by listing the type's edges on the branch (ListEdgesOfType reads the
	// branch's edge-type cache and rejects unknown types).
	if _, err := s.ListEdgesOfType(ctx, "DependsOn", branch); err != nil {
		t.Fatalf("ListEdgesOfType on branch: %v", err)
	}
}
