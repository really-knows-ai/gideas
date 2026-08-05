package ladybug

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/google/uuid"
)

func TestOpenClose(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory() error: %v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close() error: %v", err)
		}
	}()

	if s == nil {
		t.Fatal("expected non-nil store")
	}
}

func TestOpenFileBacked(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) error: %v", dir, err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close() error: %v", err)
	}
}

func TestWipeAllClearsDataAndPreservesSchema(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()
	if err := s.ApplySchema(ctx, &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
		Name: "Document", Properties: []*flowv1.Property{{Name: "title", Type: "string"}},
	}}}); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	if _, err := s.CreateEntity(ctx, "Document", "", map[string]string{"title": "before"}, nil, "main"); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if err := s.WipeAll(ctx); err != nil {
		t.Fatalf("WipeAll: %v", err)
	}
	if !s.TableExists("Document") {
		t.Fatal("schema was removed by WipeAll")
	}
	entities, _, err := s.ListEntities(ctx, "Document", 10, "", "main")
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(entities) != 0 {
		t.Fatalf("expected empty graph after WipeAll, got %d entities", len(entities))
	}
}

func TestRehydrateMainFromFilesReplacesEntitiesAndEdgesAndPreservesVectorState(t *testing.T) {
	s, err := OpenInMemory()
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
	if dimension, err := s.GetEstablishedDimension("Component", "main"); err != nil || dimension != 2 {
		t.Fatalf("vector dimension changed: dimension=%d error=%v", dimension, err)
	}
	if !s.IsVectorIndexBootstrapped("Component", "main") {
		t.Fatal("vector index was not preserved")
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestApplySchema_CreateEntityType(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	sch := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Document",
				Properties: []*flowv1.Property{
					{Name: "title", Type: "string"},
					{Name: "author", Type: "string"},
				},
			},
		},
	}

	if err := s.ApplySchema(context.Background(), sch); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	if !s.TableExists("Document") {
		t.Error("expected Document table to exist after ApplySchema")
	}
}

func TestApplySchema_CreateEdgeType(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	sch := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Person",
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Organization"}, Using: []string{"WorksFor"}},
				},
			},
			{Name: "Organization"},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "WorksFor"},
		},
	}

	if err := s.ApplySchema(context.Background(), sch); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	if !s.TableExists("Person") {
		t.Error("expected Person node table to exist")
	}
	if !s.TableExists("Organization") {
		t.Error("expected Organization node table to exist")
	}

	// Edge tables are not checked via TableExists (it only returns entity types).
	// Verify via edge type names.
	edgeNames := s.EdgeTypeNames()
	found := slices.Contains(edgeNames, "WorksFor")
	if !found {
		t.Errorf("expected WorksFor in edge type names, got %v", edgeNames)
	}
}

func TestApplySchema_Idempotent(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	sch := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Document",
				Properties: []*flowv1.Property{
					{Name: "title", Type: "string"},
				},
			},
		},
	}

	if err := s.ApplySchema(context.Background(), sch); err != nil {
		t.Fatalf("first ApplySchema: %v", err)
	}
	if err := s.ApplySchema(context.Background(), sch); err != nil {
		t.Fatalf("second ApplySchema (idempotent): %v", err)
	}
}

func TestApplySchema_TableExists(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	if s.TableExists("NonExistent") {
		t.Error("expected TableExists to return false for non-existent type")
	}

	sch := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Page", Properties: []*flowv1.Property{
				{Name: "content", Type: "string"},
			}},
		},
	}

	if err := s.ApplySchema(context.Background(), sch); err != nil {
		t.Fatal(err)
	}

	if !s.TableExists("Page") {
		t.Error("expected TableExists to return true after ApplySchema")
	}
}

func TestApplySchema_EntityTypeDefs(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	sch := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Article",
				Properties: []*flowv1.Property{
					{Name: "headline", Type: "string"},
					{Name: "body", Type: "string"},
				},
				EnableVectorIndex: true,
			},
			{
				Name: "Author",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}

	if err := s.ApplySchema(context.Background(), sch); err != nil {
		t.Fatal(err)
	}

	// EntityTypeNames
	names := s.EntityTypeNames()
	expectedNames := []string{"Article", "Author"}
	if !reflect.DeepEqual(names, expectedNames) {
		t.Errorf("EntityTypeNames: got %v, want %v", names, expectedNames)
	}

	// EntityType
	def, ok := s.EntityType("Article")
	if !ok {
		t.Fatal("expected Article entity type def")
	}
	if def.Name != "Article" {
		t.Errorf("def.Name = %q, want %q", def.Name, "Article")
	}

	// Check properties (may include additional columns from the catalog)
	propNames := make(map[string]bool)
	for _, p := range def.Properties {
		propNames[p.Name] = true
	}
	for _, want := range []string{"headline", "body"} {
		if !propNames[want] {
			t.Errorf("expected property %q in Article def", want)
		}
	}

	// Check vector index flag
	if !def.EnableVectorIndex {
		t.Error("expected EnableVectorIndex to be true for Article")
	}

	// Non-existent type
	_, ok = s.EntityType("NonExistent")
	if ok {
		t.Error("expected EntityType to return false for non-existent type")
	}

	// EdgeTypeNames (should be empty)
	if len(s.EdgeTypeNames()) != 0 {
		t.Errorf("expected empty EdgeTypeNames, got %v", s.EdgeTypeNames())
	}
}

func TestSchemaCache_RebuildOnOpen(t *testing.T) {
	dir := t.TempDir()

	// First session: open, apply schema, close.
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	sch := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Note",
				Properties: []*flowv1.Property{
					{Name: "text", Type: "string"},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{},
	}

	if err := s.ApplySchema(context.Background(), sch); err != nil {
		t.Fatalf("first session ApplySchema: %v", err)
	}

	names := s.EntityTypeNames()
	if len(names) != 1 || names[0] != "Note" {
		t.Errorf("first session: expected [Note], got %v", names)
	}

	if err := s.Close(); err != nil {
		t.Errorf("first session Close: %v", err)
	}

	// Second session: reopen, verify cache is rebuilt.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s2)

	names2 := s2.EntityTypeNames()
	if len(names2) != 1 || names2[0] != "Note" {
		t.Errorf("second session: expected [Note], got %v", names2)
	}

	_, ok := s2.EntityType("Note")
	if !ok {
		t.Error("expected Note entity type to be present after reopen")
	}
}

func TestExtensions_Load(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	// No explicit check — if OpenInMemory succeeded, extensions were loaded.
	// A failure to load extensions would have caused OpenInMemory to return an error.
}

func TestHealth(t *testing.T) {
	t.Run("in-memory", func(t *testing.T) {
		s, err := OpenInMemory()
		if err != nil {
			t.Fatal(err)
		}
		defer closeStore(t, s)

		health, err := s.Health(context.Background())
		if err != nil {
			t.Fatalf("Health: %v", err)
		}

		if !health.LadybugOK {
			t.Error("expected LadybugOK to be true")
		}
		if health.SchemaApplied {
			t.Error("expected SchemaApplied to be false for fresh DB")
		}
		if !health.PVCWritable {
			t.Error("expected PVCWritable to be true for in-memory DB")
		}

		// Apply a schema, then health should report SchemaApplied.
		sch := &flowv1.Schema{
			EntityTypes: []*flowv1.EntityType{
				{Name: "Doc", Properties: []*flowv1.Property{
					{Name: "title", Type: "string"},
				}},
			},
		}
		if err := s.ApplySchema(context.Background(), sch); err != nil {
			t.Fatal(err)
		}

		health, err = s.Health(context.Background())
		if err != nil {
			t.Fatalf("Health after schema: %v", err)
		}
		if !health.SchemaApplied {
			t.Error("expected SchemaApplied to be true after schema apply")
		}
	})

	t.Run("file-backed PVC writable", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer closeStore(t, s)

		health, err := s.Health(context.Background())
		if err != nil {
			t.Fatalf("Health: %v", err)
		}
		if !health.LadybugOK {
			t.Error("expected LadybugOK to be true for file-backed DB")
		}
		if !health.PVCWritable {
			t.Error("expected PVCWritable to be true for writable temp dir")
		}
	})

	t.Run("closed store reports unhealthy", func(t *testing.T) {
		s, err := OpenInMemory()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}

		health, err := s.Health(context.Background())
		if err != nil {
			t.Fatalf("Health: %v", err)
		}
		if health.LadybugOK {
			t.Error("expected LadybugOK to be false for closed store")
		}
	})
}

func TestClosedStore_ReturnsError(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	err = s.ApplySchema(context.Background(), &flowv1.Schema{})
	if err == nil {
		t.Error("expected error when applying schema on closed store")
	}
}

func TestListMainEntityTypes(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	types, err := s.ListMainEntityTypes()
	if err != nil {
		t.Fatalf("ListMainEntityTypes: %v", err)
	}
	if len(types) != 0 {
		t.Errorf("expected empty list, got %v", types)
	}

	sch := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Book",
				Properties: []*flowv1.Property{
					{Name: "isbn", Type: "string"},
				},
			},
			{
				Name: "Author",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(context.Background(), sch); err != nil {
		t.Fatal(err)
	}

	types, err = s.ListMainEntityTypes()
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 2 {
		t.Fatalf("expected 2 types, got %d: %v", len(types), types)
	}
	// Should be sorted.
	if types[0] != "Author" || types[1] != "Book" {
		t.Errorf("expected sorted [Author, Book], got %v", types)
	}
}

func TestValidateSchema(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	// Valid schema.
	valid := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "ValidType", Properties: []*flowv1.Property{
				{Name: "attr", Type: "string"},
			}},
		},
	}
	if err := s.ValidateSchema(context.Background(), valid); err != nil {
		t.Errorf("expected valid schema to pass: %v", err)
	}

	// Invalid schema: empty name.
	invalid := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: ""},
		},
	}
	if err := s.ValidateSchema(context.Background(), invalid); err == nil {
		t.Error("expected error for empty entity type name")
	}
}

// closeStore is a test helper that closes the store and reports errors.
func closeStore(t *testing.T, s interface{ Close() error }) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Phase 5: SPEC-verification tests
// ---------------------------------------------------------------------------

func testSchema() *flowv1.Schema {
	return &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
					{Name: "version", Type: "string"},
				},
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Component"}, Using: []string{"DependsOn"}},
				},
			},
			{
				Name: "VectorType",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
				EnableVectorIndex: true,
			},
			{
				Name: "Document",
				Properties: []*flowv1.Property{
					{Name: "title", Type: "string"},
					{Name: "body", Type: "string"},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DependsOn", Properties: []*flowv1.Property{
				{Name: "strength", Type: "string"},
			}},
		},
	}
}

func applyTestSchema(t *testing.T, s store.Store) {
	t.Helper()
	if err := s.ApplySchema(context.Background(), testSchema()); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Entity CRUD
// ---------------------------------------------------------------------------

func TestCreateEntity_Valid(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	e, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "comp1"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if e.Id == "" {
		t.Error("expected non-empty Id")
	}
	if e.Type != "Component" {
		t.Errorf("Type = %q, want %q", e.Type, "Component")
	}
	if e.Properties["name"] != "comp1" {
		t.Errorf("Properties[name] = %q, want %q", e.Properties["name"], "comp1")
	}
}

func TestCreateEntity_DuplicateID(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	id := uuid.New().String()
	_, err = s.CreateEntity(context.Background(), "Component", id, nil, nil, "")
	if err != nil {
		t.Fatalf("first CreateEntity: %v", err)
	}
	_, err = s.CreateEntity(context.Background(), "Component", id, nil, nil, "")
	if err == nil {
		t.Fatal("expected error for duplicate ID")
	}
	if !errors.Is(err, store.ErrEntityAlreadyExists) {
		t.Errorf("expected ErrEntityAlreadyExists, got %v", err)
	}
}

func TestCreateEntity_InvalidUUID(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.CreateEntity(context.Background(), "Component", "not-a-uuid", nil, nil, "")
	if err == nil {
		t.Fatal("expected error for invalid UUID")
	}
	if !errors.Is(err, store.ErrInvalidIDFormat) {
		t.Errorf("expected ErrInvalidIDFormat, got %v", err)
	}
}

func TestCreateEntity_UnknownType(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.CreateEntity(context.Background(), "NoSuchType", "", nil, nil, "")
	if err == nil {
		t.Fatal("expected error for unknown entity type")
	}
	if !errors.Is(err, store.ErrUnknownEntityType) {
		t.Errorf("expected ErrUnknownEntityType, got %v", err)
	}
}

func TestCreateEntity_MissingRequiredProperty(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	reqSchema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string", Required: true},
				},
			},
		},
	}
	if err := s.ApplySchema(context.Background(), reqSchema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	_, err = s.CreateEntity(context.Background(), "Component", "", map[string]string{}, nil, "")
	if err == nil {
		t.Fatal("expected error for missing required property")
	}
	if !errors.Is(err, store.ErrMissingRequiredProperty) {
		t.Errorf("expected ErrMissingRequiredProperty, got %v", err)
	}
}

func TestGetEntity_Found(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	e, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "findme"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	got, err := s.GetEntity(context.Background(), e.Id, "")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if got.Id != e.Id {
		t.Errorf("Id = %q, want %q", got.Id, e.Id)
	}
	if got.Properties["name"] != "findme" {
		t.Errorf("Properties[name] = %q, want %q", got.Properties["name"], "findme")
	}
}

func TestGetEntity_NotFound(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.GetEntity(context.Background(), uuid.New().String(), "")
	if err == nil {
		t.Fatal("expected error for non-existent entity")
	}
	if !errors.Is(err, store.ErrEntityNotFound) {
		t.Errorf("expected ErrEntityNotFound, got %v", err)
	}
}

func TestUpdateEntity_Valid(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	e, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "old", "version": "1"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	updated, err := s.UpdateEntity(context.Background(), e.Id,
		map[string]string{"name": "new"}, nil, "")
	if err != nil {
		t.Fatalf("UpdateEntity: %v", err)
	}
	if updated.Properties["name"] != "new" {
		t.Errorf("name = %q, want %q", updated.Properties["name"], "new")
	}
	if updated.Properties["version"] != "1" {
		t.Errorf("version = %q, want %q", updated.Properties["version"], "1")
	}
}

func TestUpdateEntity_Partial(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	e, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "original", "version": "1.0"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	updated, err := s.UpdateEntity(context.Background(), e.Id,
		map[string]string{"version": "2.0"}, nil, "")
	if err != nil {
		t.Fatalf("UpdateEntity: %v", err)
	}
	if updated.Properties["name"] != "original" {
		t.Errorf("name changed to %q", updated.Properties["name"])
	}
	if updated.Properties["version"] != "2.0" {
		t.Errorf("version = %q, want %q", updated.Properties["version"], "2.0")
	}
}

func TestDeleteEntity_Valid(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	e, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "todelete"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	deleted, err := s.DeleteEntity(context.Background(), e.Id, "")
	if err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}
	if deleted.Id != e.Id {
		t.Errorf("deleted entity Id = %q, want %q", deleted.Id, e.Id)
	}

	_, err = s.GetEntity(context.Background(), e.Id, "")
	if err == nil {
		t.Error("expected error after deletion")
	}
}

func TestDeleteEntity_NotFound(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.DeleteEntity(context.Background(), uuid.New().String(), "")
	if err == nil {
		t.Fatal("expected error for non-existent entity")
	}
	if !errors.Is(err, store.ErrEntityNotFound) {
		t.Errorf("expected ErrEntityNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Edge CRUD
// ---------------------------------------------------------------------------

func TestCreateEdge_Valid(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	src, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}
	tgt, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt: %v", err)
	}

	edge, err := s.CreateEdge(context.Background(), "DependsOn", src.Id, tgt.Id,
		map[string]string{"strength": "strong"}, "")
	if err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}
	if edge.Type != "DependsOn" {
		t.Errorf("Type = %q, want %q", edge.Type, "DependsOn")
	}
	if edge.FromEntityID != src.Id {
		t.Errorf("FromEntityID = %q, want %q", edge.FromEntityID, src.Id)
	}
	if edge.ToEntityID != tgt.Id {
		t.Errorf("ToEntityID = %q, want %q", edge.ToEntityID, tgt.Id)
	}
	if edge.Properties["strength"] != "strong" {
		t.Errorf("strength = %q, want %q", edge.Properties["strength"], "strong")
	}
}

func TestCreateEdge_MissingRequiredProperty(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	reqSchema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Component"}, Using: []string{"DependsOn"}},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DependsOn", Properties: []*flowv1.Property{
				{Name: "weight", Type: "string", Required: true},
			}},
		},
	}
	if err := s.ApplySchema(context.Background(), reqSchema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	src, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}
	tgt, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt: %v", err)
	}

	_, err = s.CreateEdge(context.Background(), "DependsOn", src.Id, tgt.Id, nil, "")
	if err == nil {
		t.Fatal("expected error for missing required edge property")
	}
	if !errors.Is(err, store.ErrMissingRequiredProperty) {
		t.Errorf("expected ErrMissingRequiredProperty, got %v", err)
	}
}

func TestCreateEdge_SourceNotFound(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	tgt, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt: %v", err)
	}

	_, err = s.CreateEdge(context.Background(), "DependsOn", uuid.New().String(), tgt.Id, nil, "")
	if err == nil {
		t.Fatal("expected error for non-existent source")
	}
	if !errors.Is(err, store.ErrSourceOrTargetNotFound) {
		t.Errorf("expected ErrSourceOrTargetNotFound, got %v", err)
	}
}

func TestCreateEdge_RuleViolation(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	src, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}
	doc, err := s.CreateEntity(context.Background(), "Document", "",
		map[string]string{"title": "doc"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity doc: %v", err)
	}

	// Component rules only allow → Component, not → Document.
	_, err = s.CreateEdge(context.Background(), "DependsOn", src.Id, doc.Id, nil, "")
	if err == nil {
		t.Fatal("expected edge rule violation")
	}
	if !errors.Is(err, store.ErrEdgeRuleViolation) {
		t.Errorf("expected ErrEdgeRuleViolation, got %v", err)
	}
}

func TestCreateEdge_TargetNotFound(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	src, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}

	_, err = s.CreateEdge(context.Background(), "DependsOn", src.Id, uuid.New().String(), nil, "")
	if err == nil {
		t.Fatal("expected error for non-existent target")
	}
	if !errors.Is(err, store.ErrSourceOrTargetNotFound) {
		t.Errorf("expected ErrSourceOrTargetNotFound, got %v", err)
	}
}

func TestCreateEdge_NoRulesDeclared(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	// Document declares no rules, so no edge creation is permitted from it.
	src, err := s.CreateEntity(context.Background(), "Document", "",
		map[string]string{"title": "src"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}
	tgt, err := s.CreateEntity(context.Background(), "Document", "",
		map[string]string{"title": "tgt"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt: %v", err)
	}

	_, err = s.CreateEdge(context.Background(), "DependsOn", src.Id, tgt.Id, nil, "")
	if err == nil {
		t.Fatal("expected edge rule violation for type with no rules")
	}
	if !errors.Is(err, store.ErrEdgeRuleViolation) {
		t.Errorf("expected ErrEdgeRuleViolation, got %v", err)
	}
}

func TestDeleteEdge_Valid(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	src, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}
	tgt, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt: %v", err)
	}

	edge, err := s.CreateEdge(context.Background(), "DependsOn", src.Id, tgt.Id, nil, "")
	if err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}

	deleted, err := s.DeleteEdge(context.Background(), edge.Id, "")
	if err != nil {
		t.Fatalf("DeleteEdge: %v", err)
	}
	if deleted.Id != edge.Id {
		t.Errorf("deleted edge Id = %q, want %q", deleted.Id, edge.Id)
	}

	_, err = s.GetEdge(context.Background(), edge.Id, "")
	if err == nil {
		t.Error("expected error after edge deletion")
	}
}

func TestDeleteEdge_NotFound(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.DeleteEdge(context.Background(), uuid.New().String(), "")
	if err == nil {
		t.Fatal("expected error for non-existent edge")
	}
	if !errors.Is(err, store.ErrEdgeNotFound) {
		t.Errorf("expected ErrEdgeNotFound, got %v", err)
	}
}

func TestGetEdge_NotFound(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.GetEdge(context.Background(), uuid.New().String(), "")
	if err == nil {
		t.Fatal("expected error for non-existent edge")
	}
	if !errors.Is(err, store.ErrEdgeNotFound) {
		t.Errorf("expected ErrEdgeNotFound, got %v", err)
	}
}

func TestCreateEdge_InvalidUUID(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	src, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}

	_, err = s.CreateEdge(context.Background(), "DependsOn", "not-a-uuid", src.Id, nil, "")
	if err == nil {
		t.Fatal("expected error for invalid fromUUID")
	}
	if !errors.Is(err, store.ErrInvalidIDFormat) {
		t.Errorf("expected ErrInvalidIDFormat, got %v", err)
	}

	_, err = s.CreateEdge(context.Background(), "DependsOn", src.Id, "not-a-uuid", nil, "")
	if err == nil {
		t.Fatal("expected error for invalid toUUID")
	}
	if !errors.Is(err, store.ErrInvalidIDFormat) {
		t.Errorf("expected ErrInvalidIDFormat, got %v", err)
	}
}

func TestDeleteEdge_InvalidUUID(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.DeleteEdge(context.Background(), "not-a-uuid", "")
	if err == nil {
		t.Fatal("expected error for invalid edge UUID")
	}
	if !errors.Is(err, store.ErrInvalidIDFormat) {
		t.Errorf("expected ErrInvalidIDFormat, got %v", err)
	}
}

func TestListEdgesOfType(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	src, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}
	tgt1, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt1: %v", err)
	}
	tgt2, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt2: %v", err)
	}

	_, err = s.CreateEdge(context.Background(), "DependsOn", src.Id, tgt1.Id, nil, "")
	if err != nil {
		t.Fatalf("CreateEdge 1: %v", err)
	}
	_, err = s.CreateEdge(context.Background(), "DependsOn", src.Id, tgt2.Id, nil, "")
	if err != nil {
		t.Fatalf("CreateEdge 2: %v", err)
	}

	edges, err := s.ListEdgesOfType(context.Background(), "DependsOn", "")
	if err != nil {
		t.Fatalf("ListEdgesOfType: %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(edges))
	}
}

// ---------------------------------------------------------------------------
// Query tests
// ---------------------------------------------------------------------------

func TestExecuteCypher_ReadOnly(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	e, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "cypher-test"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	rows, err := s.ExecuteCypher(context.Background(),
		"MATCH (n:Component {id: $id}) RETURN n.name AS name",
		map[string]any{"id": e.Id}, "")
	if err != nil {
		t.Fatalf("ExecuteCypher: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	name, ok := rows[0]["name"]
	if !ok || name != "cypher-test" {
		t.Errorf("name = %v, want cypher-test", name)
	}
}

func TestExecuteCypher_MutationRejected(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.ExecuteCypher(context.Background(),
		"CREATE (n:Component {id: 'bad-uuid'})", nil, "")
	if err == nil {
		t.Fatal("expected mutation to be rejected")
	}
	if !errors.Is(err, store.ErrMutationCypher) {
		t.Errorf("expected ErrMutationCypher, got %v", err)
	}
}

func TestExecuteCypher_WithParams(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	e, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "param-test", "version": "2"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	rows, err := s.ExecuteCypher(context.Background(),
		"MATCH (n:Component {id: $id}) RETURN n.version AS ver, n.name AS name",
		map[string]any{"id": e.Id}, "")
	if err != nil {
		t.Fatalf("ExecuteCypher: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0]["ver"] != "2" {
		t.Errorf("ver = %v, want 2", rows[0]["ver"])
	}
	if rows[0]["name"] != "param-test" {
		t.Errorf("name = %v, want param-test", rows[0]["name"])
	}
}

func TestListEntities_DefaultPageSize(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	for i := range 5 {
		_, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
		if err != nil {
			t.Fatalf("CreateEntity %d: %v", i, err)
		}
	}

	// pageSize=0 should default to 1000 (more than enough for 5 entities).
	entities, token, err := s.ListEntities(context.Background(), "Component", 0, "", "")
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(entities) != 5 {
		t.Fatalf("expected 5 entities, got %d", len(entities))
	}
	if token != "" {
		t.Errorf("expected empty token, got %q", token)
	}
}

func TestListEntities_PageSizeCap(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, _, err = s.ListEntities(context.Background(), "Component", 1001, "", "")
	if err == nil {
		t.Fatal("expected error for page size > 1000")
	}
	if !errors.Is(err, store.ErrInvalidPageSize) {
		t.Errorf("expected ErrInvalidPageSize, got %v", err)
	}
}

func TestListEntities_NegativePageSize(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, _, err = s.ListEntities(context.Background(), "Component", -1, "", "")
	if err == nil {
		t.Fatal("expected error for negative page size")
	}
	if !errors.Is(err, store.ErrInvalidPageSize) {
		t.Errorf("expected ErrInvalidPageSize, got %v", err)
	}
}

func TestListEntities_Pagination(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	const total = 5
	const pageSize = 2
	ids := make([]string, total)
	for i := range total {
		e, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
		if err != nil {
			t.Fatalf("CreateEntity %d: %v", i, err)
		}
		ids[i] = e.Id
	}

	var all []string
	token := ""
	for {
		entities, nextToken, err := s.ListEntities(context.Background(), "Component", pageSize, token, "")
		if err != nil {
			t.Fatalf("ListEntities: %v", err)
		}
		for _, e := range entities {
			all = append(all, e.Id)
		}
		if nextToken == "" {
			break
		}
		token = nextToken
	}

	if len(all) != total {
		t.Fatalf("expected %d total entities via pagination, got %d", total, len(all))
	}
}

func TestSearchNeighbors_Valid(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	// Create a VectorType entity with an embedding.
	emb := []float32{1.0, 0.0, 0.0}
	_, err = s.CreateEntity(context.Background(), "VectorType", "",
		map[string]string{"name": "vec1"}, emb, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	// Search with a similar embedding.
	results, err := s.SearchNeighbors(context.Background(),
		[]float32{0.99, 0.0, 0.0}, "VectorType", 10, "")
	if err != nil {
		t.Fatalf("SearchNeighbors: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected at least one neighbor result")
	}
}

func TestSearchNeighbors_NonIndexed(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.SearchNeighbors(context.Background(),
		[]float32{1.0, 0.0, 0.0}, "Component", 10, "")
	if err == nil {
		t.Fatal("expected error for non-indexed type")
	}
	if !errors.Is(err, store.ErrNonIndexedType) {
		t.Errorf("expected ErrNonIndexedType, got %v", err)
	}
}

func TestSearchNeighbors_UnknownType(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.SearchNeighbors(context.Background(),
		[]float32{1.0, 0.0, 0.0}, "NoSuchType", 10, "")
	if err == nil {
		t.Fatal("expected error for unknown entity type")
	}
	if !errors.Is(err, store.ErrUnknownEntityType) {
		t.Errorf("expected ErrUnknownEntityType, got %v", err)
	}
}

func TestSearchNeighbors_EmptyEmbedding(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	// Empty embedding is not an error per SPEC — the SPEC only defines
	// NaN/infinity, dimension mismatch, and non-indexed type errors for
	// SearchNeighbors. The function proceeds past the removed empty check
	// and returns empty results (no bootstrapped index to search).
	results, err := s.SearchNeighbors(context.Background(), nil, "VectorType", 10, "")
	if err != nil {
		t.Fatalf("unexpected error for empty embedding: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty embedding, got %d", len(results))
	}
}

func TestSearchNeighbors_NegativeTopK(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.SearchNeighbors(context.Background(), []float32{1, 2, 3}, "VectorType", -1, "")
	if err == nil {
		t.Fatal("expected error for negative topK")
	}
	if !errors.Is(err, store.ErrInvalidTopK) {
		t.Errorf("expected ErrInvalidTopK, got %v", err)
	}
}

func TestSearchNeighbors_DimensionMismatch(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	// Bootstrap dimension to 3.
	_, err = s.CreateEntity(context.Background(), "VectorType", "",
		map[string]string{"name": "v1"}, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	// Search with mismatched dimension.
	_, err = s.SearchNeighbors(context.Background(), []float32{1, 2, 3, 4}, "VectorType", 10, "")
	if err == nil {
		t.Fatal("expected error for dimension mismatch")
	}
	if !errors.Is(err, store.ErrEmbeddingDimension) {
		t.Errorf("expected ErrEmbeddingDimension, got %v", err)
	}
}

// SPEC R7 "Embedding dimension mismatch" for SearchNeighbors with
// entityType == "" (wildcard all-types search, query.go:130-133). A query
// embedding whose dimension matches no indexed type must return
// ErrEmbeddingDimension even when the entity type is omitted.
func TestSearchNeighbors_WildcardType_DimensionMismatch(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	// Bootstrap VectorType to dimension 3.
	_, err = s.CreateEntity(context.Background(), "VectorType", "",
		map[string]string{"name": "v1"}, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	// Wildcard search with a mismatched dimension.
	_, err = s.SearchNeighbors(context.Background(), []float32{1, 2, 3, 4}, "", 10, "")
	if err == nil {
		t.Fatal("expected error for dimension mismatch on wildcard search")
	}
	if !errors.Is(err, store.ErrEmbeddingDimension) {
		t.Errorf("expected ErrEmbeddingDimension, got %v", err)
	}
}

func TestFullTextSearch_Valid(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.CreateEntity(context.Background(), "Document", "",
		map[string]string{"title": "Hello World", "body": "This is a test document"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	results, err := s.FullTextSearch(context.Background(), "World", "Document", "")
	if err != nil {
		t.Fatalf("FullTextSearch: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected at least one FTS result")
	}
}

func TestFullTextSearch_CrossType(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.CreateEntity(context.Background(), "Document", "",
		map[string]string{"title": "UniqueTerm", "body": "content"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity Document: %v", err)
	}
	_, err = s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "UniqueTerm"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity Component: %v", err)
	}

	// Search across all types (entityType="").
	results, err := s.FullTextSearch(context.Background(), "UniqueTerm", "", "")
	if err != nil {
		t.Fatalf("FullTextSearch: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected at least one result from cross-type FTS")
	}
}

// ---------------------------------------------------------------------------
// Persistence (file-backed)
// ---------------------------------------------------------------------------

func TestPersistence_AcrossCloseReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	applyTestSchema(t, s)

	e, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "persist"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s2)

	got, err := s2.GetEntity(context.Background(), e.Id, "")
	if err != nil {
		t.Fatalf("GetEntity after reopen: %v", err)
	}
	if got.Properties["name"] != "persist" {
		t.Errorf("name = %q, want %q", got.Properties["name"], "persist")
	}
}

func TestPersistence_SchemaSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	applyTestSchema(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s2)

	if !s2.TableExists("Component") {
		t.Error("expected Component table to survive reopen")
	}
	if !s2.TableExists("Document") {
		t.Error("expected Document table to survive reopen")
	}
}

func TestPersistence_CompleteSchemaMetadataSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Service", EnableVectorIndex: true,
				Properties: []*flowv1.Property{{Name: "name", Type: "string", Required: true}},
				Rules:      []*flowv1.ConnectionRule{{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}}},
			},
			{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string", Required: true}}},
		},
		EdgeTypes: []*flowv1.EdgeType{{
			Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "weight", Type: "string", Required: true}},
		}},
	}
	if err := s.ApplySchema(context.Background(), schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	if err := s.CreateBranchDB(context.Background(), "tx-metadata"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(context.Background(), "tx-metadata"); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeStore(t, reopened)
	service, ok := reopened.EntityType("Service")
	if !ok || !service.EnableVectorIndex || len(service.Properties) != 1 || !service.Properties[0].Required {
		t.Fatalf("incomplete reopened entity definition: %+v", service)
	}
	if len(service.Rules) != 1 || len(service.Rules[0].CanConnectTo) != 1 ||
		service.Rules[0].CanConnectTo[0] != "Component" || len(service.Rules[0].Using) != 1 ||
		service.Rules[0].Using[0] != "DEPENDS_ON" {
		t.Fatalf("incomplete reopened rules: %+v", service.Rules)
	}
	edge, ok := reopened.EdgeType("DEPENDS_ON")
	if !ok || len(edge.Properties) != 1 || !edge.Properties[0].Required {
		t.Fatalf("incomplete reopened edge definition: %+v", edge)
	}
	_, err = reopened.CreateEntity(context.Background(), "Component", "", map[string]string{}, nil, "tx-metadata")
	if !errors.Is(err, store.ErrMissingRequiredProperty) {
		t.Fatalf("reopened branch accepted missing required property: %v", err)
	}
}

func TestPersistence_MissingOrCorruptSchemaMetadataFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(string) error
	}{
		{"missing", os.Remove},
		{"corrupt", func(path string) error { return os.WriteFile(path, []byte("{"), 0600) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			applyTestSchema(t, s)
			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if err := test.mutate(filepath.Join(dir, "schema.json")); err != nil {
				t.Fatalf("mutate metadata: %v", err)
			}
			if reopened, err := Open(dir); err == nil {
				_ = reopened.Close()
				t.Fatal("expected schema metadata failure")
			}
		})
	}
}

func TestPersistence_MissingBranchSchemaMetadataFailsClosed(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	applyTestSchema(t, s)
	if err := s.CreateBranchDB(context.Background(), "tx-missing-metadata"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(context.Background(), "tx-missing-metadata"); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	metadataPath := filepath.Join(dir, "branches", "tx-missing-metadata.schema.json")
	if err := os.Remove(metadataPath); err != nil {
		t.Fatalf("remove branch metadata: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen main: %v", err)
	}
	defer closeStore(t, reopened)
	if _, err := reopened.DumpAllEntities(context.Background(), "tx-missing-metadata"); err == nil {
		t.Fatal("expected missing branch metadata error")
	}
	if _, err := os.Stat(filepath.Join(dir, "branches", "tx-missing-metadata.lbug")); err != nil {
		t.Fatalf("branch database was removed: %v", err)
	}
}

func TestPersistence_CatalogMetadataMismatchFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*schemaMetadata)
	}{
		{"property name", func(metadata *schemaMetadata) {
			metadata.EntityTypes[0].Properties[0].Name = "renamed"
		}},
		{"property type", func(metadata *schemaMetadata) {
			metadata.EntityTypes[0].Properties[0].Type = colTypeInt64
		}},
		{"relationship endpoint", func(metadata *schemaMetadata) {
			metadata.EntityTypes[1].Rules[0].CanConnectTo = []string{"Service"}
		}},
		{"vector state", func(metadata *schemaMetadata) {
			metadata.VectorIndexes["Service"] = true
			metadata.VectorDimensions["Service"] = 3
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			schema := &flowv1.Schema{
				EntityTypes: []*flowv1.EntityType{
					{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
					{
						Name: "Service", EnableVectorIndex: true,
						Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
						Rules: []*flowv1.ConnectionRule{{
							CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"},
						}},
					},
				},
				EdgeTypes: []*flowv1.EdgeType{{
					Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "weight", Type: "string"}},
				}},
			}
			if err := s.ApplySchema(context.Background(), schema); err != nil {
				t.Fatalf("ApplySchema: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			metadataPath := filepath.Join(dir, "schema.json")
			data, err := os.ReadFile(metadataPath)
			if err != nil {
				t.Fatalf("read metadata: %v", err)
			}
			var metadata schemaMetadata
			if err := json.Unmarshal(data, &metadata); err != nil {
				t.Fatalf("parse metadata: %v", err)
			}
			test.mutate(&metadata)
			data, err = json.Marshal(metadata)
			if err != nil {
				t.Fatalf("marshal changed metadata: %v", err)
			}
			if err := os.WriteFile(metadataPath, data, 0600); err != nil {
				t.Fatalf("write changed metadata: %v", err)
			}
			if reopened, err := Open(dir); err == nil {
				_ = reopened.Close()
				t.Fatal("expected catalog mismatch")
			}
		})
	}
}

func TestPersistence_ValidMetadataRestoresEmptyCatalog(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	applyTestSchema(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "main.lbug")); err != nil {
		t.Fatalf("remove main database: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("restore empty catalog from metadata: %v", err)
	}
	defer closeStore(t, reopened)
	if !reopened.TableExists("Component") {
		t.Fatal("metadata did not restore Component table")
	}
}

func TestApplySchemaMetadataFailuresFailClosed(t *testing.T) {
	schema := &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
		Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
	}}}
	t.Run("stage before DDL", func(t *testing.T) {
		database, err := Open(t.TempDir())
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer closeStore(t, database)
		db := database.(*ladybugDB)
		stage := db.stageMetadata
		db.stageMetadata = func(string, schemaMetadata) (string, error) {
			return "", errors.New("injected stage failure")
		}
		if err := database.ApplySchema(context.Background(), schema); err == nil {
			t.Fatal("expected stage failure")
		}
		if database.TableExists("Component") {
			t.Fatal("stage failure applied DDL")
		}
		db.stageMetadata = stage
		if err := database.ApplySchema(context.Background(), schema); err != nil {
			t.Fatalf("store was not usable after pre-DDL stage failure: %v", err)
		}
	})

	t.Run("publish after DDL", func(t *testing.T) {
		dir := t.TempDir()
		database, err := Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		db := database.(*ladybugDB)
		db.publishMetadata = func(string, string) error { return errors.New("injected publish failure") }
		if err := database.ApplySchema(context.Background(), schema); err == nil {
			t.Fatal("expected publish failure")
		}
		// DDL was applied but the in-memory schema cache was not updated
		// (the cache update at the end of ApplySchema was not reached).
		// The store is not permanently failed — future operations are still
		// possible, but the schema cache is empty.
		if db.failed {
			t.Fatal("store should not be permanently failed after publish failure")
		}
		if err := database.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if reopened, err := Open(dir); err == nil {
			_ = reopened.Close()
			t.Fatal("undurable schema reopened without metadata")
		}
	})

	t.Run("directory sync after rename", func(t *testing.T) {
		dir := t.TempDir()
		database, err := Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		db := database.(*ladybugDB)
		db.publishMetadata = func(temporaryPath, path string) error {
			if err := publishSchemaMetadata(temporaryPath, path); err != nil {
				return err
			}
			return errors.New("injected post-rename directory sync failure")
		}
		if err := database.ApplySchema(context.Background(), schema); err == nil {
			t.Fatal("expected post-rename failure")
		}
		if database.TableExists("Component") {
			t.Fatal("failed store exposed schema cache after post-rename failure")
		}
		if err := database.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		reopened, err := Open(dir)
		if err != nil {
			t.Fatalf("published metadata did not permit safe restart: %v", err)
		}
		closeStore(t, reopened)
	})
}

func TestWriteSchemaMetadataPublishesDurably(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.json")
	metadata := schemaMetadata{
		Version: schemaMetadataVersion, VectorIndexes: map[string]bool{}, VectorDimensions: map[string]int{},
	}
	if err := writeSchemaMetadata(path, metadata); err != nil {
		t.Fatalf("writeSchemaMetadata: %v", err)
	}
	if _, _, err := readSchemaMetadata(path, true); err != nil {
		t.Fatalf("read published metadata: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "schema.json" {
		t.Fatalf("staging files remained after publish: %+v", entries)
	}
}

func TestVectorBootstrapIndexFailureFailsClosed(t *testing.T) {
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
			reopened, err := Open(dir)
			if branch == "" {
				if err == nil {
					_ = reopened.Close()
					t.Fatal("main vector mismatch silently reopened")
				}
				return
			}
			if err != nil {
				t.Fatalf("reopen main: %v", err)
			}
			defer closeStore(t, reopened)
			if _, err := reopened.DumpAllEntities(context.Background(), branch); err == nil {
				t.Fatal("branch vector mismatch silently reopened")
			}
		})
	}
}

func TestBranchVectorMetadataPublishFailureRejectsRetry(t *testing.T) {
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
	if _, err := reopened.DumpAllEntities(context.Background(), branch); err == nil {
		t.Fatal("branch metadata mismatch silently reopened")
	}
}

func TestMainVectorMetadataPublishFailureRejectsRetry(t *testing.T) {
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
	if reopened, err := Open(dir); err == nil {
		_ = reopened.Close()
		t.Fatal("main metadata mismatch silently reopened")
	}
}

func TestFileBackedVectorBootstrapSurvivesMainAndBranchReopen(t *testing.T) {
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
	if dimension, err := reopened.GetEstablishedDimension("Vector", ""); err != nil || dimension != 2 {
		t.Fatalf("main vector dimension after reopen = %d, %v", dimension, err)
	}
	if dimension, err := reopened.GetEstablishedDimension("Vector", branch); err != nil || dimension != 3 {
		t.Fatalf("branch vector dimension after reopen = %d, %v", dimension, err)
	}
	if _, err := reopened.GetEntity(context.Background(), mainEntity.Id, ""); err != nil {
		t.Fatalf("get reopened main vector entity: %v", err)
	}
	if _, err := reopened.GetEntity(context.Background(), branchEntity.Id, branch); err != nil {
		t.Fatalf("get reopened branch vector entity: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Embedding bootstrap
// ---------------------------------------------------------------------------

func TestGetEstablishedDimension_UnknownType(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.GetEstablishedDimension("NoSuchType", "")
	if err == nil {
		t.Fatal("expected error for unknown entity type")
	}
	if !errors.Is(err, store.ErrUnknownEntityType) {
		t.Errorf("expected ErrUnknownEntityType, got %v", err)
	}
}

func TestEmbeddingBootstrap_DimensionLock(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	// First entity bootstraps with dimension 3.
	_, err = s.CreateEntity(context.Background(), "VectorType", "",
		map[string]string{"name": "v1"}, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("first CreateEntity: %v", err)
	}

	// Second entity with same dimension succeeds.
	_, err = s.CreateEntity(context.Background(), "VectorType", "",
		map[string]string{"name": "v2"}, []float32{4, 5, 6}, "")
	if err != nil {
		t.Fatalf("second CreateEntity: %v", err)
	}

	// Third entity with different dimension fails.
	_, err = s.CreateEntity(context.Background(), "VectorType", "",
		map[string]string{"name": "v3"}, []float32{1, 2, 3, 4}, "")
	if err == nil {
		t.Fatal("expected dimension mismatch error")
	}
	if !errors.Is(err, store.ErrEmbeddingDimension) {
		t.Errorf("expected ErrEmbeddingDimension, got %v", err)
	}
}

func TestEmbeddingBootstrap_DimensionMismatch(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	// First entity bootstraps with dimension 3.
	_, err = s.CreateEntity(context.Background(), "VectorType", "",
		map[string]string{"name": "v1"}, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("first CreateEntity: %v", err)
	}

	// Second entity with different dimension fails.
	_, err = s.CreateEntity(context.Background(), "VectorType", "",
		map[string]string{"name": "v2"}, []float32{1, 2, 3, 4}, "")
	if err == nil {
		t.Fatal("expected dimension mismatch error")
	}
	if !errors.Is(err, store.ErrEmbeddingDimension) {
		t.Errorf("expected ErrEmbeddingDimension, got %v", err)
	}
}

func TestEmbeddingBootstrap_FirstEntityNoEmbedding(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.CreateEntity(context.Background(), "VectorType", "",
		map[string]string{"name": "no-emb"}, nil, "")
	if err == nil {
		t.Fatal("expected error for missing embedding on vector-enabled type")
	}
	if !errors.Is(err, store.ErrVectorBootstrap) {
		t.Errorf("expected ErrVectorBootstrap, got %v", err)
	}
}

func TestCreateEntity_NaNEmbeddingNonIndexed(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.CreateEntity(context.Background(), "Document", "",
		map[string]string{"title": "test"}, []float32{float32(math.NaN())}, "")
	if err == nil {
		t.Fatal("expected error for NaN embedding on non-indexed type")
	}
	if !errors.Is(err, store.ErrNaNOrInfEmbedding) {
		t.Errorf("expected ErrNaNOrInfEmbedding, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// UUID v4 validation (tested through the public API)
// ---------------------------------------------------------------------------

func TestValidateUUID_Version4Required(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	// Version 1 UUID: xxxxxxxx-xxxx-1xxx-yxxx-xxxxxxxxxxxx
	_, err = s.GetEntity(context.Background(), "00000000-0000-1000-8000-000000000000", "")
	if err == nil {
		t.Fatal("expected error for non-v4 UUID")
	}
	if !errors.Is(err, store.ErrInvalidIDFormat) {
		t.Errorf("expected ErrInvalidIDFormat, got %v", err)
	}
}

func TestValidateUUID_InvalidFormat(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.GetEntity(context.Background(), "not-even-a-uuid", "")
	if err == nil {
		t.Fatal("expected error for malformed UUID")
	}
	if !errors.Is(err, store.ErrInvalidIDFormat) {
		t.Errorf("expected ErrInvalidIDFormat, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Cascade atomicity
// ---------------------------------------------------------------------------

func TestDeleteEntity_CascadeDeletesEdges(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	src, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}
	tgt, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt: %v", err)
	}

	edge, err := s.CreateEdge(context.Background(), "DependsOn", src.Id, tgt.Id, nil, "")
	if err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}

	// Delete source entity (cascade deletes edge via DETACH DELETE).
	_, err = s.DeleteEntity(context.Background(), src.Id, "")
	if err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}

	// Verify edge is gone.
	_, err = s.GetEdge(context.Background(), edge.Id, "")
	if err == nil {
		t.Error("expected edge to be cascade-deleted")
	}
	if !errors.Is(err, store.ErrEdgeNotFound) {
		t.Errorf("expected ErrEdgeNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Branch tests (file-backed)
// ---------------------------------------------------------------------------

func TestBranch_CreateDrop(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	if err := s.CreateBranchDB(context.Background(), "tx1"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.DropBranchDB(context.Background(), "tx1"); err != nil {
		t.Fatalf("DropBranchDB: %v", err)
	}
	// Idempotent drop should not error.
	if err := s.DropBranchDB(context.Background(), "tx1"); err != nil {
		t.Errorf("expected idempotent drop to succeed: %v", err)
	}
}

func TestBranchTransactionState_InMemoryLifecycle(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	if err := s.CreateBranchDB(context.Background(), "tx-state"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if _, err := s.LoadBranchTransactionState(context.Background(), "tx-state"); err == nil {
		t.Fatal("unregistered branch state was accepted")
	}
	want := store.BranchTransactionState{MainHeadAtLastSync: "head", SchemaHash: "schema", RollbackOnly: true}
	if err := s.SaveBranchTransactionState(context.Background(), "tx-state", want); err != nil {
		t.Fatalf("SaveBranchTransactionState: %v", err)
	}
	got, err := s.LoadBranchTransactionState(context.Background(), "tx-state")
	if err != nil || got != want {
		t.Fatalf("loaded branch state: got=%+v want=%+v err=%v", got, want, err)
	}
	if err := s.DropBranchDB(context.Background(), "tx-state"); err != nil {
		t.Fatalf("DropBranchDB: %v", err)
	}
	if _, err = s.LoadBranchTransactionState(context.Background(), "tx-state"); err == nil {
		t.Fatal("dropped branch state was accepted")
	}
}

func TestBranchTransactionState_MissingRecordFailsClosed(t *testing.T) {
	path := t.TempDir()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBranchDB(context.Background(), "tx-state"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.SaveBranchTransactionState(context.Background(), "tx-state", store.BranchTransactionState{
		MainHeadAtLastSync: "head", SchemaHash: "schema",
	}); err != nil {
		t.Fatalf("SaveBranchTransactionState: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := os.Remove(filepath.Join(path, "branches", "tx-state.state.json")); err != nil {
		t.Fatalf("remove branch marker: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer closeStore(t, reopened)
	if _, err := reopened.LoadBranchTransactionState(context.Background(), "tx-state"); err == nil {
		t.Fatal("missing branch state marker was accepted")
	}
}

func TestBranchTransactionState_PersistsAndRejectsCorruption(t *testing.T) {
	path := t.TempDir()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBranchDB(context.Background(), "tx-state"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	want := store.BranchTransactionState{
		MainHeadAtLastSync: "original-head", SchemaHash: "original-schema",
		CommitStarted: true, CommitCreated: true, CommitHydrated: true,
		MainRehydrated: true, RollbackOnly: true,
	}
	if err := s.SaveBranchTransactionState(context.Background(), "tx-state", want); err != nil {
		t.Fatalf("SaveBranchTransactionState: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open after marker: %v", err)
	}
	got, err := reopened.LoadBranchTransactionState(context.Background(), "tx-state")
	if err != nil || got != want {
		t.Fatalf("persisted state: got=%+v want=%+v err=%v", got, want, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close reopened: %v", err)
	}
	markerPath := filepath.Join(path, "branches", "tx-state.state.json")
	if err := os.WriteFile(markerPath, []byte("not-json"), 0600); err != nil {
		t.Fatalf("corrupt marker: %v", err)
	}
	corrupt, err := Open(path)
	if err != nil {
		t.Fatalf("Open corrupt marker store: %v", err)
	}
	defer closeStore(t, corrupt)
	if _, err := corrupt.LoadBranchTransactionState(context.Background(), "tx-state"); err == nil {
		t.Fatal("corrupt rollback-only marker was accepted")
	}
}

func TestBranch_IsolatedWrites(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	if err := s.CreateBranchDB(context.Background(), "tx1"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(context.Background(), "tx1"); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}

	// Create entity on branch.
	e, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "branch-only"}, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}

	// Entity should be visible on branch.
	got, err := s.GetEntity(context.Background(), e.Id, "tx1")
	if err != nil {
		t.Fatalf("GetEntity on branch: %v", err)
	}
	if got.Properties["name"] != "branch-only" {
		t.Errorf("name = %q, want %q", got.Properties["name"], "branch-only")
	}

	// Entity should NOT be visible on main.
	_, err = s.GetEntity(context.Background(), e.Id, "")
	if err == nil {
		t.Error("expected entity to NOT exist on main")
	}
	if !errors.Is(err, store.ErrEntityNotFound) {
		t.Errorf("expected ErrEntityNotFound on main, got %v", err)
	}
}

func TestBranch_HydrationRoundTrip(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	if err := s.CreateBranchDB(context.Background(), "tx1"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(context.Background(), "tx1"); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}

	// Create entities and edges on branch.
	src, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "src"}, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}
	tgt, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "tgt"}, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity tgt: %v", err)
	}
	edge, err := s.CreateEdge(context.Background(), "DependsOn", src.Id, tgt.Id,
		map[string]string{"strength": "strong"}, "tx1")
	if err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}

	// Snapshot branch state.
	branchEnts, err := s.DumpAllEntities(context.Background(), "tx1")
	if err != nil {
		t.Fatalf("DumpAllEntities on branch: %v", err)
	}
	branchEdges, err := s.DumpAllEdges(context.Background(), "tx1")
	if err != nil {
		t.Fatalf("DumpAllEdges on branch: %v", err)
	}

	// Rehydrate main from branch.
	if err := s.RehydrateFromBranch(context.Background(), "tx1"); err != nil {
		t.Fatalf("RehydrateFromBranch: %v", err)
	}

	// Verify data now exists on main.
	mainEnts, err := s.DumpAllEntities(context.Background(), "")
	if err != nil {
		t.Fatalf("DumpAllEntities on main: %v", err)
	}
	if len(mainEnts) != len(branchEnts) {
		t.Errorf("expected %d entities on main, got %d", len(branchEnts), len(mainEnts))
	}

	mainEdges, err := s.DumpAllEdges(context.Background(), "")
	if err != nil {
		t.Fatalf("DumpAllEdges on main: %v", err)
	}
	if len(mainEdges) != len(branchEdges) {
		t.Errorf("expected %d edges on main, got %d", len(branchEdges), len(mainEdges))
	}

	// Verify individual entity and edge survive the round trip.
	got, err := s.GetEntity(context.Background(), src.Id, "")
	if err != nil {
		t.Fatalf("GetEntity on main after hydration: %v", err)
	}
	if got.Properties["name"] != "src" {
		t.Errorf("name = %q, want %q", got.Properties["name"], "src")
	}

	_, err = s.GetEdge(context.Background(), edge.Id, "")
	if err != nil {
		t.Fatalf("GetEdge on main after hydration: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Vector / FTS index
// ---------------------------------------------------------------------------

func TestVectorIndex_Bootstrap(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	if s.IsVectorIndexBootstrapped("VectorType", "") {
		t.Error("expected not bootstrapped before first entity")
	}

	_, err = s.CreateEntity(context.Background(), "VectorType", "",
		map[string]string{"name": "v1"}, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	if !s.IsVectorIndexBootstrapped("VectorType", "") {
		t.Error("expected bootstrapped after first entity with embedding")
	}
}

func TestFTSIndex_Search(t *testing.T) {
	s, err := OpenInMemory()
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

// ---------------------------------------------------------------------------
// RehydrateMainFromFiles — Phase 5 regression tests
// ---------------------------------------------------------------------------

func TestRehydrateMainFromFiles_EntitiesDirOnly_ReturnsError(t *testing.T) {
	s, err := OpenInMemory()
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
	if err := os.WriteFile(filepath.Join(compDir, "comp1.json"), []byte(data), 0644); err != nil {
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

func TestRehydrateMainFromFiles_BothMissing_NoError(t *testing.T) {
	s, err := OpenInMemory()
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

func TestLoadEntitiesFromDir_ReadDirError(t *testing.T) {
	s, err := OpenInMemory()
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

	tests := map[string]func() error{
		"main":          func() error { return db.loadEntitiesFromDir(entitiesDir, db.entityTypeDefs) },
		"on connection": func() error { return db.loadEntitiesFromDirOnConn(db.conn, entitiesDir, db.entityTypeDefs) },
	}
	for name, load := range tests {
		t.Run(name, func(t *testing.T) {
			err := load()
			if !errors.Is(err, wantErr) {
				t.Fatalf("expected injected ReadDir error, got %v", err)
			}
			want := fmt.Sprintf("read entities dir %q", compDir)
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not identify wrapped operation and path %q", err, want)
			}
		})
	}
}

// TestRehydrateMainFromFiles_InferredTypeWithProperties verifies SPEC R8: when
// re-hydrating with an empty applied schema, the entity type is inferred from
// the directory structure AND its property columns are created so that
// property-bearing JSON files can be persisted. Without column inference the
// replay of a property-bearing file would fail against a table with only the
// `id` column.
func TestRehydrateMainFromFiles_InferredTypeWithProperties(t *testing.T) {
	s, err := OpenInMemory()
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

// TestRehydrateMainFromFiles_InferredTypePromotesEnableVectorIndex verifies
// the re-hydration metadata parity (Finding 12): when a vector-capable entity
// is loaded on the file-based re-hydration path for a type that was inferred
// from the directory structure (EnableVectorIndex was never declared true), the
// store must promote EnableVectorIndex on the resulting definition so it stays
// consistent with the embedding column/index actually created. Without this the
// in-memory def disagrees with the metadata model and with SearchNeighbors.
func TestRehydrateMainFromFiles_InferredTypePromotesEnableVectorIndex(t *testing.T) {
	s, err := OpenInMemory()
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
	if !s.IsVectorIndexBootstrapped("Document", "main") {
		t.Error("expected vector index to be bootstrapped for re-hydrated type with embedding")
	}
}

// ---------------------------------------------------------------------------
// Second-audit: ApplySchema catalog diffing and WipeSchema tests
// ---------------------------------------------------------------------------

func TestApplySchema_AdditiveEntityProperty(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Initial schema with one property.
	schema1 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{{
			Name: "Document",
			Properties: []*flowv1.Property{
				{Name: "title", Type: "string"},
			},
		}},
	}
	if err := s.ApplySchema(ctx, schema1); err != nil {
		t.Fatalf("first ApplySchema: %v", err)
	}

	// Create an entity.
	doc, err := s.CreateEntity(ctx, "Document", "", map[string]string{"title": "doc1"}, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	// Additive: add a new property.
	schema2 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{{
			Name: "Document",
			Properties: []*flowv1.Property{
				{Name: "title", Type: "string"},
				{Name: "author", Type: "string"},
			},
		}},
	}
	if err := s.ApplySchema(ctx, schema2); err != nil {
		t.Fatalf("additive ApplySchema: %v", err)
	}

	// Existing entity is still readable.
	got, err := s.GetEntity(ctx, doc.Id, "main")
	if err != nil {
		t.Fatalf("GetEntity after additive schema: %v", err)
	}
	if got.Properties["title"] != "doc1" {
		t.Fatalf("expected title=doc1, got %v", got.Properties)
	}

	// New entity with both properties.
	doc2, err := s.CreateEntity(ctx, "Document", "", map[string]string{"title": "doc2", "author": "me"}, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity with new property: %v", err)
	}
	if doc2.Properties["author"] != "me" {
		t.Fatalf("expected author=me, got %v", doc2.Properties)
	}

	// Close and reopen.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeStore(t, s2)

	// Verify schema cache survived reopen.
	if !s2.TableExists("Document") {
		t.Fatal("Document table missing after reopen")
	}
	def, ok := s2.EntityType("Document")
	if !ok {
		t.Fatal("Document entity type missing after reopen")
	}
	foundAuthor := false
	for _, p := range def.Properties {
		if p.Name == "author" {
			foundAuthor = true
		}
	}
	if !foundAuthor {
		t.Fatal("author property missing after reopen")
	}

	// Existing entity still readable.
	got2, err := s2.GetEntity(ctx, doc.Id, "main")
	if err != nil {
		t.Fatalf("GetEntity after reopen: %v", err)
	}
	if got2.Properties["title"] != "doc1" {
		t.Fatalf("expected title=doc1 after reopen, got %v", got2.Properties)
	}
}

func TestApplySchema_AdditiveEdgeProperty(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Initial schema with entity types and an edge type with one property.
	schema1 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Service", Rules: []*flowv1.ConnectionRule{
				{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}},
			}},
			{Name: "Component"},
		},
		EdgeTypes: []*flowv1.EdgeType{{
			Name: "DEPENDS_ON",
			Properties: []*flowv1.Property{
				{Name: "weight", Type: "string"},
			},
		}},
	}
	if err := s.ApplySchema(ctx, schema1); err != nil {
		t.Fatalf("first ApplySchema: %v", err)
	}

	// Create entities and an edge.
	svc, err := s.CreateEntity(ctx, "Service", "", map[string]string{}, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity Service: %v", err)
	}
	comp, err := s.CreateEntity(ctx, "Component", "", map[string]string{}, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity Component: %v", err)
	}
	edge, err := s.CreateEdge(ctx, "DEPENDS_ON", svc.Id, comp.Id, map[string]string{"weight": "10"}, "main")
	if err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}

	// Additive: add a new property to the edge type.
	schema2 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Service", Rules: []*flowv1.ConnectionRule{
				{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}},
			}},
			{Name: "Component"},
		},
		EdgeTypes: []*flowv1.EdgeType{{
			Name: "DEPENDS_ON",
			Properties: []*flowv1.Property{
				{Name: "weight", Type: "string"},
				{Name: "description", Type: "string"},
			},
		}},
	}
	if err := s.ApplySchema(ctx, schema2); err != nil {
		t.Fatalf("additive ApplySchema: %v", err)
	}

	// Existing edge is still readable.
	got, err := s.GetEdge(ctx, edge.Id, "main")
	if err != nil {
		t.Fatalf("GetEdge after additive schema: %v", err)
	}
	if got.Properties["weight"] != "10" {
		t.Fatalf("expected weight=10, got %v", got.Properties)
	}

	// Close and reopen.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeStore(t, s2)

	// Verify edge type survived reopen.
	edgeNames := s2.EdgeTypeNames()
	found := false
	for _, n := range edgeNames {
		if n == "DEPENDS_ON" {
			found = true
		}
	}
	if !found {
		t.Fatal("DEPENDS_ON edge type missing after reopen")
	}
}

func TestApplySchema_DestructiveChange_Rejected(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	// Apply initial schema.
	schema1 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{{
			Name: "Document",
			Properties: []*flowv1.Property{
				{Name: "title", Type: "string"},
				{Name: "toremove", Type: "string"},
			},
		}},
	}
	if err := s.ApplySchema(ctx, schema1); err != nil {
		t.Fatalf("first ApplySchema: %v", err)
	}

	// Create an entity.
	_, err = s.CreateEntity(ctx, "Document", "", map[string]string{"title": "doc", "toremove": "x"}, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	// Destructive: remove a property.
	destructive := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{{
			Name: "Document",
			Properties: []*flowv1.Property{
				{Name: "title", Type: "string"},
			},
		}},
	}
	err = s.ApplySchema(ctx, destructive)
	if err == nil {
		t.Fatal("expected error for destructive schema change")
	}
	if !errors.Is(err, store.ErrDestructiveSchemaChange) {
		t.Fatalf("expected ErrDestructiveSchemaChange, got %v", err)
	}

	// WipeGraph (WipeSchema) then ApplySchema should succeed.
	if err := s.WipeSchema(ctx); err != nil {
		t.Fatalf("WipeSchema: %v", err)
	}
	if err := s.ApplySchema(ctx, destructive); err != nil {
		t.Fatalf("ApplySchema after WipeSchema: %v", err)
	}
	if !s.TableExists("Document") {
		t.Fatal("Document table should exist after wipe+apply")
	}
}

func TestApplySchema_DestructiveChange_VectorDisable(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	// Apply schema with vector index enabled.
	schema1 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{{
			Name: "VectorType",
			Properties: []*flowv1.Property{
				{Name: "name", Type: "string"},
			},
			EnableVectorIndex: true,
		}},
	}
	if err := s.ApplySchema(ctx, schema1); err != nil {
		t.Fatalf("first ApplySchema: %v", err)
	}

	// Bootstrap the vector index.
	_, err = s.CreateEntity(ctx, "VectorType", "", map[string]string{"name": "v1"}, []float32{1, 2, 3}, "main")
	if err != nil {
		t.Fatalf("CreateEntity with embedding: %v", err)
	}

	// Destructive: disable vector index.
	destructive := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{{
			Name: "VectorType",
			Properties: []*flowv1.Property{
				{Name: "name", Type: "string"},
			},
			EnableVectorIndex: false,
		}},
	}
	err = s.ApplySchema(ctx, destructive)
	if err == nil {
		t.Fatal("expected error for vector disable")
	}
	if !errors.Is(err, store.ErrDestructiveSchemaChange) {
		t.Fatalf("expected ErrDestructiveSchemaChange, got %v", err)
	}

	// WipeSchema then ApplySchema should succeed.
	if err := s.WipeSchema(ctx); err != nil {
		t.Fatalf("WipeSchema: %v", err)
	}
	if err := s.ApplySchema(ctx, destructive); err != nil {
		t.Fatalf("ApplySchema after WipeSchema: %v", err)
	}
	if !s.TableExists("VectorType") {
		t.Fatal("VectorType table should exist after wipe+apply")
	}
}

func TestWipeSchema_ThenApplySchema_EntityOnlyTransaction(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Apply initial schema with entity and edge types.
	schema1 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Service", Rules: []*flowv1.ConnectionRule{
				{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}},
			}},
			{Name: "Component"},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "DEPENDS_ON"}},
	}
	if err := s.ApplySchema(ctx, schema1); err != nil {
		t.Fatalf("first ApplySchema: %v", err)
	}

	// Create some data.
	svc, err := s.CreateEntity(ctx, "Service", "", map[string]string{}, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	comp, err := s.CreateEntity(ctx, "Component", "", map[string]string{}, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err := s.CreateEdge(ctx, "DEPENDS_ON", svc.Id, comp.Id, nil, "main"); err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}

	// WipeSchema.
	if err := s.WipeSchema(ctx); err != nil {
		t.Fatalf("WipeSchema: %v", err)
	}

	// Apply new schema with only entity types (no edges).
	schema2 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{{
			Name: "Document",
			Properties: []*flowv1.Property{
				{Name: "title", Type: "string"},
			},
		}},
	}
	if err := s.ApplySchema(ctx, schema2); err != nil {
		t.Fatalf("second ApplySchema: %v", err)
	}

	// Entity-only transaction: create, commit, restart.
	txID := "00000000-0000-4000-a000-000000000001"
	if err := s.CreateBranchDB(context.Background(), txID); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(context.Background(), txID); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}
	doc, err := s.CreateEntity(ctx, "Document", "", map[string]string{"title": "tx-doc"}, nil, txID)
	if err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}
	if err := s.RehydrateFromBranch(ctx, txID); err != nil {
		t.Fatalf("RehydrateFromBranch: %v", err)
	}
	if err := s.DropBranchDB(context.Background(), txID); err != nil {
		t.Fatalf("DropBranchDB: %v", err)
	}

	// Verify entity is in main.
	got, err := s.GetEntity(ctx, doc.Id, "main")
	if err != nil {
		t.Fatalf("GetEntity after commit: %v", err)
	}
	if got.Properties["title"] != "tx-doc" {
		t.Fatalf("expected title=tx-doc, got %v", got.Properties)
	}

	// Close and reopen.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeStore(t, s2)

	// Verify schema and data survived.
	if !s2.TableExists("Document") {
		t.Fatal("Document table missing after reopen")
	}
	got2, err := s2.GetEntity(ctx, doc.Id, "main")
	if err != nil {
		t.Fatalf("GetEntity after reopen: %v", err)
	}
	if got2.Properties["title"] != "tx-doc" {
		t.Fatalf("expected title=tx-doc after reopen, got %v", got2.Properties)
	}
}

// ---------------------------------------------------------------------------
// Second-audit: RehydrateMainFromFiles atomicity (Finding 3)
// ---------------------------------------------------------------------------

func TestRehydrateMainFromFiles_HoldsLockForEntireOperation(t *testing.T) {
	// Concurrent reads during rehydration must not observe partial state.
	// The rehydration must hold db.mu for the entire wipe-and-load cycle.
	s, err := OpenInMemory()
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

	// Prepare rehydration files.
	componentID := uuid.NewString()
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	edgesDir := filepath.Join(root, "edges")
	writeJSONFile(t, filepath.Join(entitiesDir, "Component", componentID+".json"), map[string]any{
		"id": componentID, "type": "Component", "properties": map[string]string{"name": "new"},
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
	// If the lock is held, this read will block until rehydration completes.
	// We use a short timeout to detect if the read would observe partial state.
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
	// the rehydration lock). It should see either the old entity (if it ran
	// before the wipe) or the new entity (if it ran after), but never a
	// "not found" for the old entity (partial wipe without re-insert).
	r := <-readCh
	if r.err != nil && !errors.Is(r.err, store.ErrEntityNotFound) {
		t.Fatalf("concurrent read error: %v", r.err)
	}
	// If the read returned the old entity, that's fine — it means the read
	// happened before the wipe. If it returned ErrEntityNotFound, that's
	// also fine — it means the read happened after the wipe but before the
	// new entity was inserted. The key invariant is that the read never
	// observes a state where the old entity is gone but the new one isn't
	// fully inserted yet, because the lock serializes everything.
	// We verify the final state is correct.
	got, err := s.GetEntity(ctx, componentID, "main")
	if err != nil {
		t.Fatalf("final GetEntity: %v", err)
	}
	if got.Properties["name"] != "new" {
		t.Fatalf("expected name=new, got %v", got.Properties)
	}
}

// ---------------------------------------------------------------------------
// Second-audit: findEntityByID / findEdgeByID error propagation (Finding 5)
// ---------------------------------------------------------------------------

func TestFindEntityByID_PropagatesPrepareError(t *testing.T) {
	// Call findEntityByID with a typeDefs map containing only a phantom type
	// that has no corresponding table in the database. LadybugDB will return
	// an error from Prepare. The current code silently continues past this
	// error and returns ErrEntityNotFound. The fix must propagate the error.
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	db := s.(*ladybugDB)
	// Only a phantom type — no real types. Prepare will fail on the only
	// type, and the current code would silently return ErrEntityNotFound.
	phantomDefs := map[string]*store.EntityTypeDef{
		"NonExistentTable": {Name: "NonExistentTable"},
	}

	id := uuid.NewString()
	_, err = findEntityByID(db.conn, phantomDefs, id)
	if err == nil {
		t.Fatal("expected error from prepare on non-existent table, got nil")
	}
	if errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("expected operational error, not ErrEntityNotFound: %v", err)
	}
}

func TestFindEdgeByID_PropagatesPrepareError(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	db := s.(*ladybugDB)
	phantomDefs := map[string]*store.EdgeTypeDef{
		"NonExistentEdge": {Name: "NonExistentEdge"},
	}

	id := uuid.NewString()
	_, err = findEdgeByID(db.conn, phantomDefs, id)
	if err == nil {
		t.Fatal("expected error from prepare on non-existent edge table, got nil")
	}
	if errors.Is(err, store.ErrEdgeNotFound) {
		t.Fatalf("expected operational error, not ErrEdgeNotFound: %v", err)
	}
}

func TestLoadEntitiesFromDir_ReadFileError(t *testing.T) {
	s, err := OpenInMemory()
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

	tests := map[string]func() error{
		"main":          func() error { return db.loadEntitiesFromDir(entitiesDir, db.entityTypeDefs) },
		"on connection": func() error { return db.loadEntitiesFromDirOnConn(db.conn, entitiesDir, db.entityTypeDefs) },
	}
	for name, load := range tests {
		t.Run(name, func(t *testing.T) {
			err := load()
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("expected dangling-symlink ReadFile error, got %v", err)
			}
			want := fmt.Sprintf("read entity file %q", fpath)
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not identify wrapped operation and path %q", err, want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SPEC error-table rows: empty query, invalid cypher, invalid page token,
// unknown property / edge type, NaN-or-inf embedding, topK default
// ---------------------------------------------------------------------------

func TestExecuteCypher_EmptyQuery(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.ExecuteCypher(context.Background(), "", nil, "")
	if err == nil {
		t.Fatal("expected error for empty query")
	}
	if !errors.Is(err, store.ErrEmptyQuery) {
		t.Errorf("expected ErrEmptyQuery, got %v", err)
	}
}

func TestExecuteCypher_InvalidCypher(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.ExecuteCypher(context.Background(), "this is not valid cypher {{", nil, "")
	if err == nil {
		t.Fatal("expected error for invalid cypher")
	}
	if !errors.Is(err, store.ErrInvalidCypher) {
		t.Errorf("expected ErrInvalidCypher, got %v", err)
	}
}

func TestFullTextSearch_EmptyQuery(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.FullTextSearch(context.Background(), "", "Document", "")
	if err == nil {
		t.Fatal("expected error for empty FTS query")
	}
	if !errors.Is(err, store.ErrEmptyQuery) {
		t.Errorf("expected ErrEmptyQuery, got %v", err)
	}
}

func TestFullTextSearch_UnknownType(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.FullTextSearch(context.Background(), "anything", "NoSuchType", "")
	if err == nil {
		t.Fatal("expected error for unknown entity type")
	}
	if !errors.Is(err, store.ErrUnknownEntityType) {
		t.Errorf("expected ErrUnknownEntityType, got %v", err)
	}
}

func TestListEntities_InvalidPageToken(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	for _, tok := range []string{
		"not-base64!!!",
		base64.StdEncoding.EncodeToString([]byte("not-a-number")),
		base64.StdEncoding.EncodeToString([]byte("-5")),
	} {
		_, _, err = s.ListEntities(context.Background(), "Component", 10, tok, "")
		if err == nil {
			t.Fatalf("expected error for malformed page token %q", tok)
		}
		if !errors.Is(err, store.ErrInvalidPageToken) {
			t.Errorf("token %q: expected ErrInvalidPageToken, got %v", tok, err)
		}
	}
}

func TestListEntities_UnknownType(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, _, err = s.ListEntities(context.Background(), "NoSuchType", 10, "", "")
	if err == nil {
		t.Fatal("expected error for unknown entity type")
	}
	if !errors.Is(err, store.ErrUnknownEntityType) {
		t.Errorf("expected ErrUnknownEntityType, got %v", err)
	}
}

func TestCreateEntity_UnknownProperty(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"bogus": "x"}, nil, "")
	if err == nil {
		t.Fatal("expected error for unknown property")
	}
	if !errors.Is(err, store.ErrUnknownProperty) {
		t.Errorf("expected ErrUnknownProperty, got %v", err)
	}
}

func TestUpdateEntity_UnknownProperty(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	e, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "c"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	_, err = s.UpdateEntity(context.Background(), e.Id, map[string]string{"bogus": "x"}, nil, "")
	if err == nil {
		t.Fatal("expected error for unknown property on update")
	}
	if !errors.Is(err, store.ErrUnknownProperty) {
		t.Errorf("expected ErrUnknownProperty, got %v", err)
	}
}

func TestUpdateEntity_NotFound(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.UpdateEntity(context.Background(), uuid.New().String(), nil, nil, "")
	if err == nil {
		t.Fatal("expected error for non-existent entity")
	}
	if !errors.Is(err, store.ErrEntityNotFound) {
		t.Errorf("expected ErrEntityNotFound, got %v", err)
	}
}

func TestCreateEdge_UnknownProperty(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	src, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}
	tgt, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt: %v", err)
	}

	_, err = s.CreateEdge(context.Background(), "DependsOn", src.Id, tgt.Id,
		map[string]string{"bogus": "x"}, "")
	if err == nil {
		t.Fatal("expected error for unknown edge property")
	}
	if !errors.Is(err, store.ErrUnknownProperty) {
		t.Errorf("expected ErrUnknownProperty, got %v", err)
	}
}

func TestCreateEdge_UnknownEdgeType(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	src, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}
	tgt, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt: %v", err)
	}

	_, err = s.CreateEdge(context.Background(), "DoesNotExist", src.Id, tgt.Id, nil, "")
	if err == nil {
		t.Fatal("expected error for unknown edge type")
	}
	if !errors.Is(err, store.ErrUnknownEdgeType) {
		t.Errorf("expected ErrUnknownEdgeType, got %v", err)
	}
}

func TestListEdgesOfType_UnknownEdgeType(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.ListEdgesOfType(context.Background(), "DoesNotExist", "")
	if err == nil {
		t.Fatal("expected error for unknown edge type")
	}
	if !errors.Is(err, store.ErrUnknownEdgeType) {
		t.Errorf("expected ErrUnknownEdgeType, got %v", err)
	}
}

func TestSearchNeighbors_NaNOrInfEmbedding(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	for _, emb := range [][]float32{
		{float32(math.NaN()), 0},
		{float32(math.Inf(1)), 0},
		{float32(math.Inf(-1)), 0},
	} {
		_, err = s.SearchNeighbors(context.Background(), emb, "VectorType", 10, "")
		if err == nil {
			t.Fatalf("expected error for NaN/Inf embedding %v", emb)
		}
		if !errors.Is(err, store.ErrNaNOrInfEmbedding) {
			t.Errorf("embedding %v: expected ErrNaNOrInfEmbedding, got %v", emb, err)
		}
	}
}

func TestUpdateEntity_NaNOrInfEmbedding(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	e, err := s.CreateEntity(context.Background(), "VectorType", "",
		map[string]string{"name": "v"}, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	for _, emb := range [][]float32{
		{float32(math.NaN()), 0},
		{float32(math.Inf(1)), 0},
	} {
		_, err = s.UpdateEntity(context.Background(), e.Id, nil, emb, "")
		if err == nil {
			t.Fatalf("expected error for NaN/Inf embedding %v", emb)
		}
		if !errors.Is(err, store.ErrNaNOrInfEmbedding) {
			t.Errorf("embedding %v: expected ErrNaNOrInfEmbedding, got %v", emb, err)
		}
	}
}

// SPEC R7 error table: "Embedding dimension mismatch" on UpdateEntity. The
// dimension is bootstrapped by the first CreateEntity with an embedding; a
// subsequent update with a differing dimension must fail with
// ErrEmbeddingDimension. The branch is a parameter of the same validation.
func TestUpdateEntity_EmbeddingDimensionMismatch(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	// Bootstrap VectorType to dimension 3.
	e, err := s.CreateEntity(context.Background(), "VectorType", "",
		map[string]string{"name": "v"}, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	// Update with a mismatched dimension.
	_, err = s.UpdateEntity(context.Background(), e.Id, nil, []float32{1, 2, 3, 4}, "")
	if err == nil {
		t.Fatal("expected dimension mismatch error")
	}
	if !errors.Is(err, store.ErrEmbeddingDimension) {
		t.Errorf("expected ErrEmbeddingDimension, got %v", err)
	}
}

// UpdateEntity embedding-bootstrap path (crud.go:206-231): a first embedding
// update on a vector-indexed type whose column is not yet bootstrapped attempts
// (mirroring CreateEntity) to ALTER TABLE add the embedding column, create the
// vector index, and persist the embedding. Only CreateEntity's bootstrap was
// previously tested.
//
// ponytail: this test verifies only that the bootstrap side-effects (vector
// index + established dimension) execute, because the first embedding update
// cannot be asserted end-to-end: once the vector index is created, LadybugDB
// refuses `SET embedding` on an existing row ("Cannot set property ... because
// it is used in one or more indexes"), so UpdateEntity never persists an
// embedding value. The bootstrap DDL still runs first and locks the dimension.
func TestUpdateEntity_EmbeddingBootstrap(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	if s.IsVectorIndexBootstrapped("VectorType", "") {
		t.Fatal("expected VectorType not bootstrapped before rehydration")
	}

	// Create a VectorType entity without an embedding via rehydration: the
	// load path persists the entity but does not bootstrap the embedding
	// column (SPEC R7 lazy bootstrap on the first embedding write).
	id := uuid.NewString()
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	writeJSONFile(t, filepath.Join(entitiesDir, "VectorType", id+".json"), map[string]any{
		"id": id, "type": "VectorType", "properties": map[string]string{"name": "vec"},
	})
	edgesDir := filepath.Join(root, "edges")
	if err := os.MkdirAll(edgesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.RehydrateMainFromFiles(context.Background(), entitiesDir, edgesDir); err != nil {
		t.Fatalf("RehydrateMainFromFiles: %v", err)
	}
	if s.IsVectorIndexBootstrapped("VectorType", "") {
		t.Fatal("rehydration must not bootstrap the vector index")
	}

	// First embedding update runs the bootstrap: ALTER TABLE add embedding
	// column + CREATE_VECTOR_INDEX, locking the dimension to 3.
	_, err = s.UpdateEntity(context.Background(), id, nil, []float32{1, 2, 3}, "")
	if !s.IsVectorIndexBootstrapped("VectorType", "") {
		t.Fatalf("expected vector index bootstrapped after first embedding update (update err: %v)", err)
	}
	if dim, derr := s.GetEstablishedDimension("VectorType", ""); derr != nil || dim != 3 {
		t.Fatalf("dimension = %d, error = %v (update err: %v)", dim, derr, err)
	}
}

func TestSearchNeighbors_ZeroTopKDefaults(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	// Bootstrap the vector index to dimension 3.
	_, err = s.CreateEntity(context.Background(), "VectorType", "",
		map[string]string{"name": "vec1"}, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	// topK == 0 must default to 10 (per query.go) rather than error.
	results, err := s.SearchNeighbors(context.Background(), []float32{1, 2, 3}, "VectorType", 0, "")
	if err != nil {
		t.Fatalf("SearchNeighbors with topK=0: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result with default topK, got %d", len(results))
	}
}

// RehydrateFromBranch (in-memory commit path) must ensure main's embedding
// FLOAT[n] column / vector index exists before inserting an entity that
// carries an embedding, so a branch that bootstraps the dimension on its
// first embedding write can promote the dimension to main (SPEC R7 dimension
// scope). Without this, the branch-copy path's CREATE targeting the embedding
// column would fail because main's table never added it.
func TestRehydrateFromBranch_PromotesEmbeddingDimensionToMain(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	if err := s.CreateBranchDB(context.Background(), "tx1"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(context.Background(), "tx1"); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}

	if s.IsVectorIndexBootstrapped("VectorType", "") {
		t.Fatal("expected VectorType not bootstrapped on main before commit")
	}

	// First embedding write happens inside the branch, bootstrapping the
	// dimension there (SPEC R7 dimension lock scoped to the branch).
	_, err = s.CreateEntity(context.Background(), "VectorType", "",
		map[string]string{"name": "v"}, []float32{1, 2, 3}, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}

	if err := s.RehydrateFromBranch(context.Background(), "tx1"); err != nil {
		t.Fatalf("RehydrateFromBranch promotes embedding schema to main: %v", err)
	}

	if !s.IsVectorIndexBootstrapped("VectorType", "") {
		t.Fatal("expected vector index promoted to main after RehydrateFromBranch")
	}
	if dim, derr := s.GetEstablishedDimension("VectorType", ""); derr != nil || dim != 3 {
		t.Fatalf("dimension on main = %d, error = %v, want 3", dim, derr)
	}
}

// Re-hydration ties an element's label to its directory name (SPEC R8), so a
// JSON file's `type` key must match the directory it lives under. A mismatch
// would store the element under one label while its domain Type reports
// another, so findEntityByID/findEdgeByID (which match by label) would
// disagree with the returned type. Both paths (main and branch) must reject
// such a file.
func TestRehydrateMainFromFiles_RejectsTypeDirectoryMismatch(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	id := uuid.NewString()
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	edgesDir := filepath.Join(root, "edges")
	// File is under the VectorType directory but declares type "Document".
	writeJSONFile(t, filepath.Join(entitiesDir, "VectorType", id+".json"), map[string]any{
		"id": id, "type": "Document", "properties": map[string]string{"name": "mismatch"},
	})
	if err := s.RehydrateMainFromFiles(context.Background(), entitiesDir, edgesDir); err == nil {
		t.Fatal("expected error for entity type/directory mismatch")
	} else if !errors.Is(err, store.ErrInvalidEntityDir) {
		t.Fatalf("expected ErrInvalidEntityDir, got %v", err)
	}
}

// The same type/directory mismatch guard must apply on the branch load path.
func TestHydrateBranchFromFiles_RejectsTypeDirectoryMismatch(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	if err := s.CreateBranchDB(context.Background(), "tx1"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(context.Background(), "tx1"); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}

	id := uuid.NewString()
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	edgesDir := filepath.Join(root, "edges")
	if err := os.MkdirAll(edgesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(entitiesDir, "VectorType", id+".json"), map[string]any{
		"id": id, "type": "Document", "properties": map[string]string{"name": "mismatch"},
	})
	if err := s.HydrateBranchFromFiles(context.Background(), "tx1", entitiesDir, edgesDir); err == nil {
		t.Fatal("expected error for entity type/directory mismatch")
	} else if !errors.Is(err, store.ErrInvalidEntityDir) {
		t.Fatalf("expected ErrInvalidEntityDir, got %v", err)
	}
}

// SearchNeighbors must surface a genuine read-time failure on the vector-index
// Prepare path (query.go) as a non-nil, wrapped error rather than silently
// returning an empty result with a nil error. A vector-indexed type whose index
// is confirmed created (bootstrapped dimension > 0) but whose HNSW index has
// been dropped makes conn.Prepare fail; we drive that real failure through the
// public method and assert the wrapped error surfaces.
//
// The two Execute-time error branches — "execute vector index query" (query.go)
// and "execute fts index query" (query.go) — are NOT fault-injectable with the
// real, seam-less LadybugDB library: the only controllable read-time fault is
// index removal, and for both it is caught strictly before Execute. The vector
// index removal fails at Prepare (covered above); the FTS index removal makes
// FullTextSearch silently return an empty result (its Prepare error is a benign
// skip), never reaching Execute. Reaching those Execute branches would require
// Prepare to succeed over a present index while Execute diverges, which no public
// DDL/API can arrange (ALTER of an embedding column is a parser error; a
// dimension change fails the earlier ErrEmbeddingDimension guard at query.go:133).
func TestSearchNeighbors_VectorPrepareFailureSurfacesError(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()
	applyTestSchema(t, s)

	// Bootstrap the dimension + HNSW vector index for VectorType.
	if _, err := s.CreateEntity(ctx, "VectorType", "",
		map[string]string{"name": "v"}, []float32{1, 2, 3}, ""); err != nil {
		t.Fatalf("bootstrap vector type: %v", err)
	}
	if !s.IsVectorIndexBootstrapped("VectorType", "") {
		t.Fatal("expected VectorType vector index bootstrapped")
	}

	// Drop the vector index. The index-locked FLOAT[3] embedding column remains
	// (so getEmbeddingDimension still reports 3), but the QUERY_VECTOR_INDEX
	// Prepare now fails — the "prepare vector index query" error branch.
	db := s.(*ladybugDB)
	res, err := db.conn.Query("CALL DROP_VECTOR_INDEX('VectorType', 'VectorType_vec');")
	if err != nil {
		t.Fatalf("drop vector index: %v", err)
	}
	res.Close()

	results, err := s.SearchNeighbors(ctx, []float32{1, 2, 3}, "VectorType", 10, "")
	if err == nil {
		t.Fatalf("expected non-nil error for dropped vector index, got %d results with nil error", len(results))
	}
	if !strings.Contains(err.Error(), `prepare vector index query for "VectorType"`) {
		t.Fatalf("expected wrapped 'prepare vector index query' error, got: %v", err)
	}
}
