package ladybug

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/google/uuid"
)

func TestRehydrateMainFromFilesReplacesEntitiesAndEdgesAndPreservesVectorState(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()
	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", EnableVectorIndex: true, Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
			{
				Name: "Service", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
				Rules: []*flowv1.ConnectionRule{{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}}},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "DEPENDS_ON"}},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	oldComponent, err := s.CreateEntity(ctx, "Component", "", map[string]string{"name": "old"}, []float32{1, 2}, "main")
	if err != nil {
		t.Fatalf("create old component: %v", err)
	}
	oldService, err := s.CreateEntity(ctx, "Service", "", map[string]string{"name": "old"}, nil, "main")
	if err != nil {
		t.Fatalf("create old service: %v", err)
	}
	if _, err := s.CreateEdge(ctx, "DEPENDS_ON", oldService.Id, oldComponent.Id, nil, "main"); err != nil {
		t.Fatalf("create old edge: %v", err)
	}

	componentID := uuid.NewString()
	serviceID := uuid.NewString()
	edgeID := uuid.NewString()
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	edgesDir := filepath.Join(root, "edges")
	writeJSONFile(t, filepath.Join(entitiesDir, "Component", componentID+".json"), map[string]any{
		"id": componentID, "type": "Component", "properties": map[string]string{"name": "new"},
		"embedding": []float32{3, 4},
	})
	writeJSONFile(t, filepath.Join(entitiesDir, "Service", serviceID+".json"), map[string]any{
		"id": serviceID, "type": "Service", "properties": map[string]string{"name": "new"},
	})
	writeJSONFile(t, filepath.Join(edgesDir, "DEPENDS_ON", edgeID+".json"), map[string]any{
		"id": edgeID, "type": "DEPENDS_ON", "from": serviceID, "to": componentID,
	})
	if err := s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		t.Fatalf("RehydrateMainFromFiles: %v", err)
	}
	if _, err := s.GetEntity(ctx, oldComponent.Id, "main"); !errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("old entity survived replacement: %v", err)
	}
	component, err := s.GetEntity(ctx, componentID, "main")
	if err != nil || !reflect.DeepEqual(component.Embedding, []float32{3, 4}) {
		t.Fatalf("rehydrated component mismatch: entity=%+v error=%v", component, err)
	}
	if _, err := s.GetEdge(ctx, edgeID, "main"); err != nil {
		t.Fatalf("rehydrated edge missing: %v", err)
	}
	if dimension, err := s.GetEstablishedDimension(
		context.Background(), "Component", "main",
	); err != nil || dimension != 2 {
		t.Fatalf("vector dimension changed: dimension=%d error=%v", dimension, err)
	}
	assertVectorIndexState(t, s, "Component", "main", true, "vector index was not preserved")
}

func TestRehydrateMainFromFiles_EntitiesDirOnly_ReturnsError(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed rehydration from working tree")
	}
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	entitiesDir := t.TempDir()
	compDir := filepath.Join(entitiesDir, "Component")
	if err := os.MkdirAll(compDir, 0755); err != nil {
		t.Fatal(err)
	}
	data := `{"id":"00000000-0000-4000-a000-000000000001","type":"Component","properties":{"name":"test"}}`
	if err := os.WriteFile(
		filepath.Join(compDir, "00000000-0000-4000-a000-000000000001.json"),
		[]byte(data), 0644,
	); err != nil {
		t.Fatal(err)
	}

	// edgesDir is a non-existent path — should error because entities dir exists.
	edgesDir := filepath.Join(t.TempDir(), "nonexistent")

	ctx := context.Background()
	err = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
	if err == nil {
		t.Fatal("expected error when entitiesDir exists but edgesDir does not")
	}
	if !errors.Is(err, store.ErrInvalidEdgeDir) {
		t.Errorf("expected ErrInvalidEdgeDir, got %v", err)
	}
}

// TestRehydrateMainFromFiles_FailureKeepsMainConsistent pins the atomicity of
// re-hydration against the source: the DETACH DELETE must not run before the
// file tree is proven fully loadable. A corrupt source file (e.g. a corrupt
// merged JSON pulled by the sync worker) must fail with the pre-existing main
// graph untouched — otherwise the sync worker's cycle returns with main
// serving a silently-wiped graph (SPEC error-table row "Sync re-hydration
// failed": the R8 "automatic recovery on next startup" escape hatch
// presupposes a consistent graph to serve).
func TestRehydrateMainFromFiles_FailureKeepsMainConsistent(t *testing.T) {
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

	// Pre-existing main graph: an entity that is NOT in the file tree, so a
	// wipe-then-fail would destroy it and only the fixed validation-first
	// order keeps it.
	oldID := uuid.NewString()
	old, err := s.CreateEntity(ctx, "Component", oldID,
		map[string]string{"name": "old"}, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	// A corrupt file tree: the Component type directory carries only an
	// unparseable JSON file.
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	edgesDir := filepath.Join(root, "edges")
	if err := os.MkdirAll(filepath.Join(entitiesDir, "Component"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entitiesDir, "Component", "corrupt.json"),
		[]byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(edgesDir, 0755); err != nil {
		t.Fatal(err)
	}

	err = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
	if err == nil {
		t.Fatal("expected the corrupt file tree to fail re-hydration")
	}

	// The failed re-hydration must leave the pre-existing graph intact.
	got, err := s.GetEntity(ctx, oldID, "main")
	if err != nil {
		t.Fatalf("failed re-hydration wiped main.lbug: %v", err)
	}
	if got.Properties["name"] != old.Properties["name"] {
		t.Fatalf("pre-existing entity mutated by failed re-hydration: got %v, want %v",
			got.Properties, old.Properties)
	}
}

// HydrateBranchFromFiles must apply the same partial-wipe completeness guard as
// RehydrateMainFromFiles (branch.go:517-521): a working tree where entities/
// exists but edges/ was removed (SPEC R2 WipeGraph mid-wipe failure →
// INTERNAL) must fail loudly on the branch load path too — silently loading
// entities and skipping every edge would hydrate an incomplete graph with no
// signal.
func TestHydrateBranchFromFiles_EntitiesDirOnly_ReturnsError(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed rehydration from working tree")
	}
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	const branch = "tx1"
	if err := s.CreateBranchDB(context.Background(), branch); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(context.Background(), branch); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}

	entitiesDir := t.TempDir()
	compDir := filepath.Join(entitiesDir, "Component")
	if err := os.MkdirAll(compDir, 0755); err != nil {
		t.Fatal(err)
	}
	data := `{"id":"00000000-0000-4000-a000-000000000001","type":"Component","properties":{"name":"test"}}`
	if err := os.WriteFile(
		filepath.Join(compDir, "00000000-0000-4000-a000-000000000001.json"),
		[]byte(data), 0644,
	); err != nil {
		t.Fatal(err)
	}

	// edgesDir is a non-existent path — must error because entities dir exists.
	edgesDir := filepath.Join(t.TempDir(), "nonexistent")

	err = s.HydrateBranchFromFiles(context.Background(), branch, entitiesDir, edgesDir)
	if err == nil {
		t.Fatal("expected error when entitiesDir exists but edgesDir does not")
	}
	if !errors.Is(err, store.ErrInvalidEdgeDir) {
		t.Errorf("expected ErrInvalidEdgeDir, got %v", err)
	}
}

// SPEC R8 both-lost recovery corner: main.lbug corrupted AND schema.json
// absent while the git repo has commits. Open recovers a fresh empty database
// and re-hydration serves the full graph with inferred types, but schemaApplied
// must be set so Health() reports the schema as applied — it used to stay false
// indefinitely because only ApplySchema and restoreMainSchemaMetadataLocked
// (neither of which runs in this corner) set the flag.
func TestRehydrateMainFromFiles_SchemaAppliedAfterBothLostRecovery(t *testing.T) {
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

	// Open recovers (fresh empty DB) and finds no schema metadata — the R8
	// both-lost corner.
	recovered, err := Open(dir)
	if err != nil {
		t.Fatalf("Open after both-lost corruption: %v", err)
	}
	defer closeStore(t, recovered)

	health, err := recovered.Health(ctx)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.SchemaApplied {
		t.Fatal("fixture: expected SchemaApplied=false after both-lost open (no metadata to restore)")
	}

	// The git repo has commits: the entities dir carries committed files.
	id := uuid.NewString()
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	edgesDir := filepath.Join(root, "edges")
	writeJSONFile(t, filepath.Join(entitiesDir, "Component", id+".json"), map[string]any{
		"id": id, "type": "Component", "properties": map[string]string{"name": "recovered"},
	})
	if err := os.MkdirAll(edgesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := recovered.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		t.Fatalf("RehydrateMainFromFiles: %v", err)
	}

	ent, err := recovered.GetEntity(ctx, id, "")
	if err != nil {
		t.Fatalf("re-hydrated entity not served: %v", err)
	}
	if ent.Properties["name"] != "recovered" {
		t.Fatalf("re-hydrated entity property = %q, want %q", ent.Properties["name"], "recovered")
	}

	health, err = recovered.Health(ctx)
	if err != nil {
		t.Fatalf("Health after re-hydration: %v", err)
	}
	if !health.SchemaApplied {
		t.Fatal("expected SchemaApplied=true after successful re-hydration of the recovered graph")
	}
}

// The post-WipeGraph restart corner (SPEC R2): WipeSchema removed schema.json,
// dropped every table, and cleared the in-memory caches, so the startup rebuild
// (cmd rehydrateMainAfterRecovery) re-hydrates the wiped (empty) tree. That
// empty-tree re-hydration must NOT report the schema as applied — the store has
// no schema and no tables until the operator's next ApplySchema — while a
// subsequent real ApplySchema still restores the HealthCheck "schema applied"
// dimension (SPEC R2). Pin: this test fails if RehydrateMainFromFiles flips
// schemaApplied unconditionally on the empty tree (the pre-fix behaviour
// reported SchemaApplied=true against the wiped store).
func TestRehydrateMainFromFiles_EmptyTreeLeavesSchemaUnappliedAfterWipe(t *testing.T) {
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
	// WipeGraph's store primitive: drop every table, remove schema.json, and
	// clear the caches (schemaApplied=false on the live store).
	if err := s.WipeSchema(ctx); err != nil {
		t.Fatalf("WipeSchema: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Simulate the restart: reopen the wiped store. With no schema.json and an
	// empty catalog, Open restores no metadata and schemaApplied stays false.
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("Open after wipe: %v", err)
	}
	defer closeStore(t, reopened)
	health, err := reopened.Health(ctx)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.SchemaApplied {
		t.Fatal("fixture: expected SchemaApplied=false after reopening the wiped store")
	}

	// The startup rebuild re-hydrates the wiped tree: both directories are empty
	// (the "wipe" commit exists but carries no entities/edges).
	entitiesDir := filepath.Join(t.TempDir(), "entities")
	edgesDir := filepath.Join(t.TempDir(), "edges")
	if err := reopened.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		t.Fatalf("RehydrateMainFromFiles on empty tree: %v", err)
	}
	health, err = reopened.Health(ctx)
	if err != nil {
		t.Fatalf("Health after empty-tree re-hydration: %v", err)
	}
	if health.SchemaApplied {
		t.Fatal("empty-tree re-hydration after WipeGraph must leave SchemaApplied=false")
	}

	// A real schema application restores the dimension.
	if err := reopened.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema after wipe: %v", err)
	}
	health, err = reopened.Health(ctx)
	if err != nil {
		t.Fatalf("Health after re-apply: %v", err)
	}
	if !health.SchemaApplied {
		t.Fatal("expected SchemaApplied=true after ApplySchema")
	}
}
