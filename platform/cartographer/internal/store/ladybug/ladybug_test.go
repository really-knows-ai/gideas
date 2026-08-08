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
	"time"

	lbug "github.com/LadybugDB/go-ladybug"
	schemavalidator "github.com/foundry/flow/cartographer/internal/schema"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/google/uuid"
)

// strengthValue is the test value for the DependsOn edge's strength property,
// shared across the CRUD, branch, and re-hydration tests.
const strengthValue = "strong"

// inferredValue is the test property value for schema-absent types inferred
// from the directory structure, shared across the re-hydration inference tests.
const inferredValue = "inferred"

// embeddingPropertyValue is the test value of a non-vector entity type's
// `embedding` property (SPEC R1: the name is reserved only for vector-enabled
// types), shared across the round-trip assertions in
// TestNonVectorTypeEmbeddingProperty_RoundTrips.
const embeddingPropertyValue = "not-a-vector"

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

// The store's internal placeholder NODE table for edgeless rel types is named
// `_untyped` (schemavalidator.UntypedTableName). schema.Validate reserves it: a
// user entity or edge type with that name would alias the placeholder table
// (and be silently skipped by validateMetadataAgainstCatalog's structural check
// on reopen), so ApplySchema — which validates first — must reject it with the
// schema package's reserved-word sentinel (INVALID_ARGUMENT at the gRPC
// boundary via mapStoreError/isSchemaError).
func TestApplySchema_RejectsUntypedPlaceholderName(t *testing.T) {
	tests := []struct {
		name string
		s    *flowv1.Schema
	}{
		{"entity type named _untyped", &flowv1.Schema{
			EntityTypes: []*flowv1.EntityType{{Name: schemavalidator.UntypedTableName}},
		}},
		{"edge type named _untyped", &flowv1.Schema{
			EdgeTypes: []*flowv1.EdgeType{{Name: schemavalidator.UntypedTableName}},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := OpenInMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer closeStore(t, s)
			err = s.ApplySchema(context.Background(), tc.s)
			if err == nil {
				t.Fatal("expected ApplySchema to reject, got nil")
			} else if !errors.Is(err, schemavalidator.ErrReservedWord) {
				t.Fatalf("expected schemavalidator.ErrReservedWord, got: %v", err)
			}
		})
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

// TestCreateEntity_StructuralErrorBeforeDuplicateID asserts the check-order
// "structural validation → data-integrity" (SPEC ~943): a duplicate explicit id
// combined with an unknown or missing-required property must surface the
// structurally-prior INVALID_ARGUMENT error, not ErrEntityAlreadyExists.
func TestCreateEntity_StructuralErrorBeforeDuplicateID(t *testing.T) {
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

	id := uuid.New().String()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	// Seed an entity with the explicit id so a second create is a duplicate.
	if _, err := s.CreateEntity(context.Background(), "Component", id,
		map[string]string{"name": "first"}, nil, ""); err != nil {
		t.Fatalf("seed CreateEntity: %v", err)
	}

	// Duplicate id + unknown property → ErrUnknownProperty (INVALID_ARGUMENT),
	// not ErrEntityAlreadyExists.
	_, err = s.CreateEntity(context.Background(), "Component", id,
		map[string]string{"name": "second", "bogus": "x"}, nil, "")
	if !errors.Is(err, store.ErrUnknownProperty) {
		t.Fatalf("expected ErrUnknownProperty to take precedence, got %v", err)
	}

	// Duplicate id + missing required property → ErrMissingRequiredProperty
	// (INVALID_ARGUMENT), not ErrEntityAlreadyExists. Uses a fresh store whose
	// schema declares a required property.
	s2, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s2)
	if err := s2.ApplySchema(context.Background(), reqSchema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	id2 := uuid.New().String()
	if _, err := s2.CreateEntity(context.Background(), "Component", id2,
		map[string]string{"name": "first"}, nil, ""); err != nil {
		t.Fatalf("seed CreateEntity (required): %v", err)
	}
	_, err = s2.CreateEntity(context.Background(), "Component", id2,
		map[string]string{}, nil, "")
	if !errors.Is(err, store.ErrMissingRequiredProperty) {
		t.Fatalf("expected ErrMissingRequiredProperty to take precedence, got %v", err)
	}

	// The structural-before-data-integrity ordering (SPEC:946) extends to
	// embedding validation: a duplicate-ID create carrying an invalid
	// embedding must surface the structural INVALID_ARGUMENT, never
	// ErrEntityAlreadyExists. testSchema's VectorType is vector-indexed; seed a
	// VectorType entity with the same id to (a) make the second create a
	// duplicate and (b) bootstrap the dimension to 3.
	vecID := uuid.New().String()
	if _, err := s.CreateEntity(context.Background(), "VectorType", vecID,
		map[string]string{"name": "vec-first"}, []float32{1, 2, 3}, ""); err != nil {
		t.Fatalf("seed VectorType CreateEntity: %v", err)
	}

	// Duplicate id + NaN embedding → ErrNaNOrInfEmbedding (structural), not
	// ErrEntityAlreadyExists.
	_, err = s.CreateEntity(context.Background(), "VectorType", vecID,
		map[string]string{"name": "second"}, []float32{float32(math.NaN()), 0, 0}, "")
	if !errors.Is(err, store.ErrNaNOrInfEmbedding) {
		t.Fatalf("expected ErrNaNOrInfEmbedding to take precedence over duplicate id, got %v", err)
	}

	// Duplicate id + wrong-dimension embedding → ErrEmbeddingDimension
	// (structural), not ErrEntityAlreadyExists. VectorType's dimension is
	// locked to 3 by the seed above.
	_, err = s.CreateEntity(context.Background(), "VectorType", vecID,
		map[string]string{"name": "second"}, []float32{1, 2, 3, 4}, "")
	if !errors.Is(err, store.ErrEmbeddingDimension) {
		t.Fatalf("expected ErrEmbeddingDimension to take precedence over duplicate id, got %v", err)
	}

	// A duplicate-id create whose embedding is structurally valid still
	// surfaces the data-integrity check — matching dimension, no NaN.
	_, err = s.CreateEntity(context.Background(), "VectorType", vecID,
		map[string]string{"name": "second"}, []float32{4, 5, 6}, "")
	if !errors.Is(err, store.ErrEntityAlreadyExists) {
		t.Fatalf("expected ErrEntityAlreadyExists for structurally-valid duplicate create, got %v", err)
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

// TestUpdateEntity_OmitsRequiredProperty_Succeeds verifies SPEC R6
// "forward-only" property guarantee: UpdateEntity omitting a Required:true
// property must succeed because updates are partial — only the supplied
// properties are SET. The Required constraint applies only at create time.
func TestUpdateEntity_OmitsRequiredProperty_Succeeds(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	reqSchema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{{
			Name: "Component",
			Properties: []*flowv1.Property{
				{Name: "name", Type: "string", Required: true},
				{Name: "version", Type: "string"},
			},
		}},
	}
	if err := s.ApplySchema(context.Background(), reqSchema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	ctx := context.Background()

	// Create entity with the required property.
	e, err := s.CreateEntity(ctx, "Component", "",
		map[string]string{"name": "comp", "version": "1"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	// Update omitting the Required property — must succeed (forward-only).
	updated, err := s.UpdateEntity(ctx, e.Id,
		map[string]string{"version": "2"}, nil, "")
	if err != nil {
		t.Fatalf("UpdateEntity omitting Required property must succeed: %v", err)
	}
	if updated.Properties["version"] != "2" {
		t.Errorf("version = %q, want %q", updated.Properties["version"], "2")
	}
	// Required property must remain unchanged.
	if updated.Properties["name"] != "comp" {
		t.Errorf("name = %q, want %q", updated.Properties["name"], "comp")
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
		map[string]string{"strength": strengthValue}, "")
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
	if edge.Properties["strength"] != strengthValue {
		t.Errorf("strength = %q, want %q", edge.Properties["strength"], strengthValue)
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

// TestCreateEdge_StructuralErrorBeforeEntityExistence asserts the SPEC RPC
// check-order (CreateEdge: structural → entity existence): a request that
// carries BOTH a missing source entity AND a structurally invalid edge property
// surfaces the structural error (unknown/missing required property →
// INVALID_ARGUMENT) rather than the existence NOT_FOUND masking it.
func TestCreateEdge_StructuralErrorBeforeEntityExistence(t *testing.T) {
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

	missing := uuid.New().String()
	existingTarget, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "tgt"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity target: %v", err)
	}

	// Missing required property + missing source → ErrMissingRequiredProperty
	// (structural) takes precedence over ErrSourceOrTargetNotFound.
	_, err = s.CreateEdge(context.Background(), "DependsOn", missing, existingTarget.Id, nil, "")
	if !errors.Is(err, store.ErrMissingRequiredProperty) {
		t.Errorf("expected ErrMissingRequiredProperty to take precedence over missing source, got %v", err)
	}

	// Unknown property + missing source → ErrUnknownProperty (structural) takes
	// precedence over ErrSourceOrTargetNotFound.
	_, err = s.CreateEdge(context.Background(), "DependsOn", missing, existingTarget.Id,
		map[string]string{"weight": "x", "bogus": "y"}, "")
	if !errors.Is(err, store.ErrUnknownProperty) {
		t.Errorf("expected ErrUnknownProperty to take precedence over missing source, got %v", err)
	}

	// Well-formed property values + missing source → ErrSourceOrTargetNotFound
	// (entity existence, the next check, still fires when structure is valid).
	_, err = s.CreateEdge(context.Background(), "DependsOn", missing, existingTarget.Id,
		map[string]string{"weight": "heavy"}, "")
	if !errors.Is(err, store.ErrSourceOrTargetNotFound) {
		t.Errorf("expected ErrSourceOrTargetNotFound for structurally-valid missing source, got %v", err)
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

	// SPEC R7 point 3: "Edge deletion does not cascade to any entity" — both
	// endpoints must survive the edge's removal.
	if _, err := s.GetEntity(context.Background(), src.Id, ""); err != nil {
		t.Fatalf("source entity must survive edge deletion: %v", err)
	}
	if _, err := s.GetEntity(context.Background(), tgt.Id, ""); err != nil {
		t.Fatalf("target entity must survive edge deletion: %v", err)
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
	if len(rows[0].Values) != 1 {
		t.Fatalf("expected 1 value, got %d", len(rows[0].Values))
	}
	if got := rows[0].Values[0]; got != "cypher-test" {
		t.Errorf("name = %v, want cypher-test", got)
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

// TestExecuteCypher_MutationClausesClassified asserts that each mutation/DDL
// clause the SPEC R7 §5 and error-table row 913 enumerate (CREATE, SET, DELETE,
// MERGE, REMOVE, DROP, DDL index/constraint, and FOREACH-as-mutation) is REJECTED
// by ExecuteCypher with ErrMutationCypher (mapped to PERMISSION_DENIED) — never
// executed as read-only — not just the single CREATE clause the historical test
// covered.
//
// LadybugDB v0.17.0's parser does not recognise the full Neo4j clause grammar:
// forms like top-level FOREACH, `MATCH ... REMOVE ...`, and index/constraint DDL
// fail at Prepare *before* the IsReadOnly guard runs. Such statements are still
// mutations per SPEC:913 / SPEC:469-470, so they are classified by the
// mutation-keyword fallback (isMutationCypher) and surface ErrMutationCypher
// (PERMISSION_DENIED), not ErrInvalidCypher (INVALID_ARGUMENT). Genuinely
// invalid read-only syntax (no mutation keyword) keeps ErrInvalidCypher.
func TestExecuteCypher_MutationClausesClassified(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	cases := []struct {
		name   string
		cypher string
	}{
		{"create", "CREATE (n:Component {id: 'bad-uuid'})"},
		{"create-drop-entity", "CREATE (n:Component {id: 'bad-uuid'}) DROP n"},
		{"set", "MATCH (n:Component) SET n.name = 'x'"},
		{"delete", "MATCH (n:Component) DELETE n"},
		{"merge", "MERGE (n:Component {id: 'bad-uuid'})"},
		{"remove", "MATCH (n:Component) REMOVE n.name"},
		{"drop", "DROP TABLE Component"},
		{"ddl-index", "CREATE INDEX Component_name IF NOT EXISTS FOR (n:Component) ON (n.name)"},
		{"ddl-constraint", "CREATE CONSTRAINT IF NOT EXISTS FOR (n:Component) REQUIRE n.id IS UNIQUE"},
		{"foreach-as-mutation", "FOREACH (x IN ['aaa'] | CREATE (n:Component {id: x}))"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ExecuteCypher(context.Background(), tc.cypher, nil, "")
			if !errors.Is(err, store.ErrMutationCypher) {
				t.Errorf("expected ErrMutationCypher for %q, got %v", tc.cypher, err)
			}
		})
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
	// SPEC R2: each row is one flat tuple in the order LadybugDB returns the
	// columns — ver before name, matching the RETURN clause.
	if len(rows[0].Values) != 2 {
		t.Fatalf("expected 2 values, got %d", len(rows[0].Values))
	}
	if rows[0].Values[0] != "2" {
		t.Errorf("ver = %v, want 2", rows[0].Values[0])
	}
	if rows[0].Values[1] != "param-test" {
		t.Errorf("name = %v, want param-test", rows[0].Values[1])
	}
}

// ---------------------------------------------------------------------------
// ExtractEntityTypes (SPEC R3 server-authoritative statement analysis seam)
// ---------------------------------------------------------------------------

// extractTestSchema applies a schema with a Component and a Service entity
// type plus a DEPENDS_ON edge type — the label set the multi-type extraction
// tests reference (mirroring the service-layer test schema).
func extractTestSchema(t *testing.T, s store.Store) {
	t.Helper()
	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
			{Name: "Service", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "weight", Type: "string"}}}},
	}
	if err := s.ApplySchema(context.Background(), schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
}

// TestExtractEntityTypes pins the store's server-authoritative statement
// analysis seam directly — the layer that produces the extraction must carry
// the tests (R3 test-discipline). Error classification must match
// ExecuteCypher's exactly so the SPEC check order "empty query → Cypher
// syntax → read-only enforcement → capability" (SPEC:958) holds.
func TestExtractEntityTypes(t *testing.T) {
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	extractTestSchema(t, s)

	t.Run("empty query returns ErrEmptyQuery", func(t *testing.T) {
		_, err := s.ExtractEntityTypes(ctx, "")
		if !errors.Is(err, store.ErrEmptyQuery) {
			t.Errorf("expected ErrEmptyQuery, got %v", err)
		}
	})

	t.Run("invalid syntax returns ErrInvalidCypher", func(t *testing.T) {
		_, err := s.ExtractEntityTypes(ctx, "this is not valid cypher {{")
		if !errors.Is(err, store.ErrInvalidCypher) {
			t.Errorf("expected ErrInvalidCypher, got %v", err)
		}
	})

	// Each mutation/DDL clause the SPEC R7 §5 and error-table row 913
	// enumerate must surface ErrMutationCypher — either via IsReadOnly (the
	// grammar classifies CREATE/SET/DELETE/MERGE/DROP) or via the
	// mutation-keyword fallback for statements the v0.17.0 grammar cannot
	// prepare (REMOVE, FOREACH, index/constraint DDL) — never
	// ErrInvalidCypher, so read-only enforcement precedes capability.
	mutations := []struct {
		name   string
		cypher string
	}{
		{"create", "CREATE (n:Component {id: 'bad-uuid'})"},
		{"set", "MATCH (n:Component) SET n.name = 'x'"},
		{"delete", "MATCH (n:Component) DELETE n"},
		{"merge", "MERGE (n:Component {id: 'bad-uuid'})"},
		{"remove-keyword-fallback", "MATCH (n:Component) REMOVE n.name"},
		{"drop", "DROP TABLE Component"},
		{"foreach-as-mutation", "FOREACH (x IN ['aaa'] | CREATE (n:Component {id: x}))"},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ExtractEntityTypes(ctx, tc.cypher)
			if !errors.Is(err, store.ErrMutationCypher) {
				t.Errorf("expected ErrMutationCypher for %q, got %v", tc.cypher, err)
			}
		})
	}

	t.Run("valid read-only single type", func(t *testing.T) {
		labels, err := s.ExtractEntityTypes(ctx, "MATCH (n:Component) RETURN n")
		if err != nil {
			t.Fatalf("ExtractEntityTypes: %v", err)
		}
		if !slices.Equal(labels, []string{"Component"}) {
			t.Errorf("expected [Component], got %v", labels)
		}
	})

	t.Run("valid read-only multi type", func(t *testing.T) {
		labels, err := s.ExtractEntityTypes(ctx,
			"MATCH (a:Component)-[:DEPENDS_ON]->(b:Service) RETURN b")
		if err != nil {
			t.Fatalf("ExtractEntityTypes: %v", err)
		}
		if !slices.Equal(labels, []string{"Component", "Service"}) {
			t.Errorf("expected [Component Service], got %v", labels)
		}
	})

	t.Run("unlabelled match yields empty slice not error", func(t *testing.T) {
		labels, err := s.ExtractEntityTypes(ctx, "MATCH (n) RETURN n")
		if err != nil {
			t.Fatalf("ExtractEntityTypes: %v", err)
		}
		if labels != nil {
			t.Errorf("expected nil labels, got %v", labels)
		}
	})
}

// TestExtractEntityTypeLabels pins the pure-Go label analyzer directly — the
// pattern shapes (named/anonymous/multi-label nodes, inline property maps,
// relationship patterns, comment/string-literal stripping) that the
// server-side extraction depends on.
func TestExtractEntityTypeLabels(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cypher   string
		expected []string
	}{
		{"single", "MATCH (c:Component) RETURN c", []string{"Component"}},
		{"multi-type", "MATCH (a:Component)-[:DEPENDS_ON]->(b:Service) RETURN a, b",
			[]string{"Component", "Service"}},
		{"anonymous-node", "MATCH (a:Component) WHERE (a)--(:Service) RETURN a",
			[]string{"Component", "Service"}},
		{"multi-label", "MATCH (c:Component:Service) RETURN c", []string{"Component", "Service"}},
		{"property-map", "MATCH (c:Component {name: 'x'}) RETURN c", []string{"Component"}},
		{"property-map-compact", "MATCH (c:Component{name:'x'}) RETURN c", []string{"Component"}},
		{"nested-property-map", "MATCH (c:Component {meta: {a: 1}}) RETURN c", []string{"Component"}},
		{"line-comment-stripped",
			"MATCH (c:Component) RETURN c // (b:Service)", []string{"Component"}},
		{"block-comment-stripped",
			"MATCH (c:Component) RETURN c /* (b:Service) */", []string{"Component"}},
		{"string-literal-colon-stripped",
			"MATCH (c:Component {name: 'x:Service'}) RETURN c", []string{"Component"}},
		{"string-literal-node-shape-stripped",
			"MATCH (c:Component) RETURN '(:Service)' AS s", []string{"Component"}},
		{"duplicate-labels-deduped",
			"MATCH (a:Component)-->(b:Component) RETURN a, b", []string{"Component"}},
		{"multiple-match-clauses",
			"MATCH (c:Component) MATCH (s:Service) RETURN c, s", []string{"Component", "Service"}},
		{"unlabelled-nodes-nil", "MATCH (n) RETURN n", nil},
		{"no-match-nil", "RETURN 1", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			labels := extractEntityTypeLabels(tc.cypher)
			if !slices.Equal(labels, tc.expected) {
				t.Errorf("expected %v, got %v", tc.expected, labels)
			}
		})
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

// TestListEntities_PageTokenOverflowBoundary pins the offset pagination boundary that
// the query.go:ListEntities ponytail documents: any non-negative int64 page token is
// accepted, and `offset + pageSize` can overflow to a negative next-token value that the
// follow-up call rejects as ErrInvalidPageToken. With a real graph too small to reach
// such an offset no next token is emitted (SKIP past the rows yields nothing), which is
// exactly why the overflow is practically unreachable — the boundary test asserts the
// accepted-bound and the rejected-downstream-bound so the ceiling stays explicit.
func TestListEntities_PageTokenOverflowBoundary(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	const pageSize = 10

	// A non-negative offset at the int64 limit is parsed and accepted (not
	// ErrInvalidPageToken). On a small graph the SKIP exhausts the rows, so no
	// next token is emitted — no overflow, no error.
	maxOffset := math.MaxInt64
	maxTok := base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%d", int64(maxOffset)))
	entities, nextTok, err := s.ListEntities(ctx, "Component", pageSize, maxTok, "")
	if err != nil {
		t.Fatalf("largest accepted offset should not error, got %v", err)
	}
	if len(entities) != 0 || nextTok != "" {
		t.Fatalf("expected empty page and no next token at max offset, got entities=%d nextToken=%q", len(entities), nextTok)
	}

	// That same offset plus pageSize is what the next-token computation would
	// produce; it overflows to a negative value. Feed it back in as a token, as
	// the ponytail's failure mode describes, and the follow-up call rejects it.
	overflowed := int64(maxOffset) + int64(pageSize)
	if overflowed >= 0 {
		t.Fatalf("overflow guard ineffective: offset %d + pagesize %d did not overflow", maxOffset, pageSize)
	}
	overflowTok := base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%d", overflowed))
	_, _, err = s.ListEntities(ctx, "Component", pageSize, overflowTok, "")
	if !errors.Is(err, store.ErrInvalidPageToken) {
		t.Errorf("overflowed negative token should be rejected as ErrInvalidPageToken, got %v", err)
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

	// SPEC error-table: "Empty embedding in SearchNeighbors → INVALID_ARGUMENT".
	// The store must enforce this at its own SearchNeighbors boundary (not rely
	// on the service layer), so an empty embedding is rejected with
	// store.ErrEmptyEmbedding rather than silently returning empty results.
	_, err = s.SearchNeighbors(context.Background(), nil, "VectorType", 10, "")
	if err == nil {
		t.Fatal("expected INVALID_ARGUMENT error for empty embedding")
	}
	if !errors.Is(err, store.ErrEmptyEmbedding) {
		t.Errorf("expected ErrEmptyEmbedding, got %v", err)
	}
	if _, err := s.SearchNeighbors(context.Background(), []float32{}, "VectorType", 10, ""); err == nil {
		t.Fatal("expected INVALID_ARGUMENT error for zero-length embedding")
	} else if !errors.Is(err, store.ErrEmptyEmbedding) {
		t.Errorf("expected ErrEmptyEmbedding for zero-length vector, got %v", err)
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

// SPEC R5: before the first ApplySchema (or on a fresh graph with no
// bootstrapped vector index), a type-omitted (wildcard) SearchNeighbors is a
// non-type-referencing method and must succeed on an empty graph - the
// ErrEmbeddingDimension is reserved for a query dimension that matches no
// established index, and with no established index there is nothing to mismatch.
func TestSearchNeighbors_WildcardEmptyGraph_Succeeds(t *testing.T) {
	t.Run("no schema applied", func(t *testing.T) {
		s, err := OpenInMemory()
		if err != nil {
			t.Fatal(err)
		}
		defer closeStore(t, s)
		ctx := context.Background()

		results, err := s.SearchNeighbors(ctx, []float32{1, 2, 3}, "", 10, "")
		if err != nil {
			t.Fatalf("wildcard SearchNeighbors before ApplySchema should succeed, got %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected no results on an empty graph, got %d", len(results))
		}
	})

	t.Run("schema applied but no vector index bootstrapped", func(t *testing.T) {
		s, err := OpenInMemory()
		if err != nil {
			t.Fatal(err)
		}
		defer closeStore(t, s)
		// testSchema declares VectorType with EnableVectorIndex, but no entity
		// is created, so no embedding has bootstrapped its dimension (dim == 0).
		applyTestSchema(t, s)

		results, err := s.SearchNeighbors(context.Background(), []float32{1, 2, 3}, "", 10, "")
		if err != nil {
			t.Fatalf("wildcard SearchNeighbors on a not-yet-bootstrapped index should succeed, got %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected no results before vector index bootstrap, got %d", len(results))
		}
	})
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

// TestRehydrateFromBranch_PromotedVectorMetadataSurvivesReopen asserts that a
// branch-only (bootstrap-first) vector type — declared on main with
// EnableVectorIndex but only bootstrapped inside a branch — has its promoted
// vector index/dimension persisted into main's schema.json by RehydrateFromBranch
// so a reopen's validateMetadataAgainstCatalog does not fail closed. Without the
// persistence, main's catalog carries the promoted embedding column/index while
// main's metadata still records VectorIndexes=false/VectorDimensions=0 and the
// reopen bricks startup.
func TestRehydrateFromBranch_PromotedVectorMetadataSurvivesReopen(t *testing.T) {
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
	if database.IsVectorIndexBootstrapped("Vector", "") {
		t.Fatal("expected Vector not bootstrapped on main before branch write")
	}

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
	if !database.IsVectorIndexBootstrapped("Vector", "") {
		t.Fatal("expected vector index promoted to main after rehydrate")
	}

	if err := database.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen must validate cleanly against persisted main metadata.
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen after rehydrate: %v", err)
	}
	defer closeStore(t, reopened)
	if dimension, derr := reopened.GetEstablishedDimension("Vector", ""); derr != nil || dimension != 3 {
		t.Fatalf("main vector dimension after reopen = %d, error = %v, want 3", dimension, derr)
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
// ResolveEntityType — SPEC R3 authoritative source-entity-type lookup
// ---------------------------------------------------------------------------

// ResolveEntityType is the store primitive backing SPEC R3's authoritative
// source-entity-type lookup for the DeleteEdge/UpdateEntity/DeleteEntity
// capability checks (cartographer_server.go:1108/1163/1249/1345): the server
// resolves the entity's type, then checks the caller's capabilities against
// that type. Pins the found branch: an existing entity resolves to its type.
func TestResolveEntityType_Found(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	e, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "resolvable"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	entityType, err := s.ResolveEntityType(context.Background(), e.Id, "")
	if err != nil {
		t.Fatalf("ResolveEntityType: %v", err)
	}
	if entityType != e.Type {
		t.Errorf("resolved type = %q, want %q", entityType, e.Type)
	}
}

// Pins the not-found branch: an absent entity must surface the ErrEntityNotFound
// sentinel (learnings rule "Sentinel errors over zero-value returns") rather
// than a zero-value ("", nil).
func TestResolveEntityType_NotFound(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.ResolveEntityType(context.Background(), uuid.NewString(), "")
	if err == nil {
		t.Fatal("expected error for non-existent entity")
	}
	if !errors.Is(err, store.ErrEntityNotFound) {
		t.Errorf("expected ErrEntityNotFound, got %v", err)
	}
}

// Pins the branch-scoped path: with a txID argument the lookup resolves against
// that transaction's isolated LadybugDB instance (SPEC R2), never main. An
// entity created on the branch is resolvable on the branch and NOT on main; an
// entity on main is resolvable on main and NOT on the branch.
func TestResolveEntityType_BranchScoped(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	const branch = "tx1"
	if err := s.CreateBranchDB(ctx, branch); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}

	mainEntity, err := s.CreateEntity(ctx, "Document", "",
		map[string]string{"title": "main-only"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity on main: %v", err)
	}
	branchEntity, err := s.CreateEntity(ctx, "Component", "",
		map[string]string{"name": "branch-only"}, nil, branch)
	if err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}

	// Each scope resolves its own data.
	mainType, err := s.ResolveEntityType(ctx, mainEntity.Id, "")
	if err != nil || mainType != "Document" {
		t.Fatalf("resolve main entity on main: type=%q err=%v", mainType, err)
	}
	branchType, err := s.ResolveEntityType(ctx, branchEntity.Id, branch)
	if err != nil || branchType != branchEntity.Type {
		t.Fatalf("resolve branch entity on branch: type=%q err=%v", branchType, err)
	}

	// Isolation: branch data is invisible to main and vice versa.
	if _, err := s.ResolveEntityType(ctx, branchEntity.Id, ""); !errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("branch entity resolvable on main, want ErrEntityNotFound, got %v", err)
	}
	if _, err := s.ResolveEntityType(ctx, mainEntity.Id, branch); !errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("main entity resolvable on branch, want ErrEntityNotFound, got %v", err)
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
	want := store.BranchTransactionState{
		MainHeadAtLastSync: "head", SchemaHash: "schema", AppliedTimeout: 5 * time.Minute,
		RollbackOnly: true,
	}
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
		MainHeadAtLastSync: "head", SchemaHash: "schema", AppliedTimeout: 5 * time.Minute,
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
		AppliedTimeout: 5 * time.Minute,
		CommitStarted:  true, CommitCreated: true, CommitHydrated: true,
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
		map[string]string{"strength": strengthValue}, "tx1")
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
// Branch-scoped read/write-path coverage (SPEC R2)
//
// SPEC R2: "All read- and write-path methods accept an optional transactionId
// parameter. When present, the operation is scoped to that transaction's
// isolated LadybugDB instance. When absent, the operation is applied directly
// to main." The branch-scoped CreateEntity/CreateEdge/GetEntity/DumpAll*
// coverage exists above; these tests extend it to the remaining methods,
// each proving isolation by writing branch-only data and asserting it is
// invisible to main (or vice versa).
// ---------------------------------------------------------------------------

// setupBranch is the shared branch-test setup: apply the test schema and open
// a branch DB replicated from main's schema.
func setupBranch(t *testing.T, s store.Store) {
	t.Helper()
	applyTestSchema(t, s)
	ctx := context.Background()
	if err := s.CreateBranchDB(ctx, "tx1"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(ctx, "tx1"); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}
}

func TestBranch_ExecuteCypherScoped(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	setupBranch(t, s)
	ctx := context.Background()

	branchOnly, err := s.CreateEntity(ctx, "Component", "",
		map[string]string{"name": "cypher-branch"}, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}
	mainOnly, err := s.CreateEntity(ctx, "Component", "",
		map[string]string{"name": "cypher-main"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity on main: %v", err)
	}

	// The branch query sees only branch data.
	branchRows, err := s.ExecuteCypher(ctx, "MATCH (n:Component) RETURN n.id AS id", nil, "tx1")
	if err != nil {
		t.Fatalf("ExecuteCypher on branch: %v", err)
	}
	if len(branchRows) != 1 || branchRows[0].Values[0] != branchOnly.Id {
		t.Fatalf("branch ExecuteCypher must see only branch data, got %+v", branchRows)
	}
	// Main's query sees only main data.
	mainRows, err := s.ExecuteCypher(ctx, "MATCH (n:Component) RETURN n.id AS id", nil, "")
	if err != nil {
		t.Fatalf("ExecuteCypher on main: %v", err)
	}
	if len(mainRows) != 1 || mainRows[0].Values[0] != mainOnly.Id {
		t.Fatalf("main ExecuteCypher must see only main data, got %+v", mainRows)
	}
}

func TestBranch_SearchNeighborsScoped(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	setupBranch(t, s)
	ctx := context.Background()

	// Bootstrap VectorType to dimension 3 on the branch (branch-scoped
	// dimension lock, SPEC R7) and on main.
	if _, err := s.CreateEntity(ctx, "VectorType", "",
		map[string]string{"name": "branch-v"}, []float32{1, 2, 3}, "tx1"); err != nil {
		t.Fatalf("bootstrap branch vector: %v", err)
	}
	if _, err := s.CreateEntity(ctx, "VectorType", "",
		map[string]string{"name": "main-v"}, []float32{1, 2, 3}, ""); err != nil {
		t.Fatalf("bootstrap main vector: %v", err)
	}

	// A branch-scoped search sees only the branch's vector entity.
	branchResults, err := s.SearchNeighbors(ctx, []float32{1, 2, 3}, "VectorType", 10, "tx1")
	if err != nil {
		t.Fatalf("SearchNeighbors on branch: %v", err)
	}
	if len(branchResults) != 1 || branchResults[0].Entity.Properties["name"] != "branch-v" {
		t.Fatalf("branch SearchNeighbors must see only branch data, got %+v", branchResults)
	}
	// A main search sees only the main entity.
	mainResults, err := s.SearchNeighbors(ctx, []float32{1, 2, 3}, "VectorType", 10, "")
	if err != nil {
		t.Fatalf("SearchNeighbors on main: %v", err)
	}
	if len(mainResults) != 1 || mainResults[0].Entity.Properties["name"] != "main-v" {
		t.Fatalf("main SearchNeighbors must see only main data, got %+v", mainResults)
	}
}

func TestBranch_FullTextSearchScoped(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	setupBranch(t, s)
	ctx := context.Background()

	if _, err := s.CreateEntity(ctx, "Document", "",
		map[string]string{"title": "needle-branch", "body": "branch body"}, nil, "tx1"); err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}
	if _, err := s.CreateEntity(ctx, "Document", "",
		map[string]string{"title": "needle-main", "body": "main body"}, nil, ""); err != nil {
		t.Fatalf("CreateEntity on main: %v", err)
	}

	branchResults, err := s.FullTextSearch(ctx, "needle", "Document", "tx1")
	if err != nil {
		t.Fatalf("FullTextSearch on branch: %v", err)
	}
	if len(branchResults) != 1 || branchResults[0].Properties["title"] != "needle-branch" {
		t.Fatalf("branch FullTextSearch must see only branch data, got %+v", branchResults)
	}
	mainResults, err := s.FullTextSearch(ctx, "needle", "Document", "")
	if err != nil {
		t.Fatalf("FullTextSearch on main: %v", err)
	}
	if len(mainResults) != 1 || mainResults[0].Properties["title"] != "needle-main" {
		t.Fatalf("main FullTextSearch must see only main data, got %+v", mainResults)
	}
}

func TestBranch_ListEntitiesScoped(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	setupBranch(t, s)
	ctx := context.Background()

	branchOnly, err := s.CreateEntity(ctx, "Component", "",
		map[string]string{"name": "list-branch"}, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}
	mainOnly, err := s.CreateEntity(ctx, "Component", "",
		map[string]string{"name": "list-main"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity on main: %v", err)
	}

	branchEnts, _, err := s.ListEntities(ctx, "Component", 0, "", "tx1")
	if err != nil {
		t.Fatalf("ListEntities on branch: %v", err)
	}
	if len(branchEnts) != 1 || branchEnts[0].Id != branchOnly.Id {
		t.Fatalf("branch ListEntities must see only branch data, got %+v", branchEnts)
	}
	mainEnts, _, err := s.ListEntities(ctx, "Component", 0, "", "")
	if err != nil {
		t.Fatalf("ListEntities on main: %v", err)
	}
	if len(mainEnts) != 1 || mainEnts[0].Id != mainOnly.Id {
		t.Fatalf("main ListEntities must see only main data, got %+v", mainEnts)
	}
}

// TestBranch_UpdateEntityScoped verifies a branch-scoped UpdateEntity mutates
// only the transaction's isolated instance: the change is visible on the branch
// but not on main.
func TestBranch_UpdateEntityScoped(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	setupBranch(t, s)
	ctx := context.Background()

	// Create the entity on both main and the branch (replicated schema, so the
	// same id is valid in both scopes).
	e, err := s.CreateEntity(ctx, "Component", "",
		map[string]string{"name": "shared", "version": "1"}, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}
	if _, err := s.CreateEntity(ctx, "Component", e.Id,
		map[string]string{"name": "shared", "version": "1"}, nil, ""); err != nil {
		t.Fatalf("CreateEntity on main: %v", err)
	}

	updated, err := s.UpdateEntity(ctx, e.Id, map[string]string{"version": "2"}, nil, "tx1")
	if err != nil {
		t.Fatalf("UpdateEntity on branch: %v", err)
	}
	if updated.Properties["version"] != "2" {
		t.Fatalf("branch update version = %q, want %q", updated.Properties["version"], "2")
	}

	// The branch sees the update; main still holds the original value.
	branchGot, err := s.GetEntity(ctx, e.Id, "tx1")
	if err != nil {
		t.Fatalf("GetEntity on branch: %v", err)
	}
	if branchGot.Properties["version"] != "2" {
		t.Fatalf("branch entity version = %q, want %q", branchGot.Properties["version"], "2")
	}
	mainGot, err := s.GetEntity(ctx, e.Id, "")
	if err != nil {
		t.Fatalf("GetEntity on main: %v", err)
	}
	if mainGot.Properties["version"] != "1" {
		t.Fatalf("main entity version = %q, want %q (update must not leak)", mainGot.Properties["version"], "1")
	}
}

// TestBranch_DeleteEntityScoped verifies a branch-scoped DeleteEntity removes
// the entity from the transaction's isolated instance only: gone on the branch,
// still present on main.
func TestBranch_DeleteEntityScoped(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	setupBranch(t, s)
	ctx := context.Background()

	e, err := s.CreateEntity(ctx, "Component", "",
		map[string]string{"name": "to-delete"}, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}
	if _, err := s.CreateEntity(ctx, "Component", e.Id,
		map[string]string{"name": "to-delete"}, nil, ""); err != nil {
		t.Fatalf("CreateEntity on main: %v", err)
	}

	deleted, err := s.DeleteEntity(ctx, e.Id, "tx1")
	if err != nil {
		t.Fatalf("DeleteEntity on branch: %v", err)
	}
	if deleted.Id != e.Id {
		t.Fatalf("deleted entity Id = %q, want %q", deleted.Id, e.Id)
	}

	if _, err := s.GetEntity(ctx, e.Id, "tx1"); !errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("branch entity survived branch-scoped delete, want ErrEntityNotFound, got %v", err)
	}
	if _, err := s.GetEntity(ctx, e.Id, ""); err != nil {
		t.Fatalf("main entity must survive a branch-scoped delete: %v", err)
	}
}

// TestBranch_DeleteEdgeScoped verifies a branch-scoped DeleteEdge removes the
// edge from the transaction's isolated instance only: gone on the branch, still
// present on main.
func TestBranch_DeleteEdgeScoped(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	setupBranch(t, s)
	ctx := context.Background()

	// Create endpoints on the branch and an edge between them.
	src, err := s.CreateEntity(ctx, "Component", "", nil, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity src on branch: %v", err)
	}
	tgt, err := s.CreateEntity(ctx, "Component", "", nil, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity tgt on branch: %v", err)
	}
	edge, err := s.CreateEdge(ctx, "DependsOn", src.Id, tgt.Id,
		map[string]string{"strength": strengthValue}, "tx1")
	if err != nil {
		t.Fatalf("CreateEdge on branch: %v", err)
	}
	// Mirror the endpoints and edge on main so the delete targets a live edge.
	if _, err := s.CreateEntity(ctx, "Component", src.Id, nil, nil, ""); err != nil {
		t.Fatalf("CreateEntity src on main: %v", err)
	}
	if _, err := s.CreateEntity(ctx, "Component", tgt.Id, nil, nil, ""); err != nil {
		t.Fatalf("CreateEntity tgt on main: %v", err)
	}
	mainEdge, err := s.CreateEdge(ctx, "DependsOn", src.Id, tgt.Id,
		map[string]string{"strength": strengthValue}, "")
	if err != nil {
		t.Fatalf("CreateEdge on main: %v", err)
	}

	deleted, err := s.DeleteEdge(ctx, edge.Id, "tx1")
	if err != nil {
		t.Fatalf("DeleteEdge on branch: %v", err)
	}
	if deleted.Id != edge.Id {
		t.Fatalf("deleted edge Id = %q, want %q", deleted.Id, edge.Id)
	}

	if _, err := s.GetEdge(ctx, edge.Id, "tx1"); !errors.Is(err, store.ErrEdgeNotFound) {
		t.Fatalf("branch edge survived branch-scoped delete, want ErrEdgeNotFound, got %v", err)
	}
	if _, err := s.GetEdge(ctx, mainEdge.Id, ""); err != nil {
		t.Fatalf("main edge must survive a branch-scoped delete: %v", err)
	}
}

// TestBranch_GetEdgeScoped verifies a branch-scoped GetEdge reads the
// transaction's isolated instance: the branch sees its own edge, and a main
// read of the branch's edge ID fails.
func TestBranch_GetEdgeScoped(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	setupBranch(t, s)
	ctx := context.Background()

	src, err := s.CreateEntity(ctx, "Component", "", nil, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity src on branch: %v", err)
	}
	tgt, err := s.CreateEntity(ctx, "Component", "", nil, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity tgt on branch: %v", err)
	}
	edge, err := s.CreateEdge(ctx, "DependsOn", src.Id, tgt.Id,
		map[string]string{"strength": strengthValue}, "tx1")
	if err != nil {
		t.Fatalf("CreateEdge on branch: %v", err)
	}

	got, err := s.GetEdge(ctx, edge.Id, "tx1")
	if err != nil {
		t.Fatalf("GetEdge on branch: %v", err)
	}
	if got.Id != edge.Id {
		t.Fatalf("edge Id = %q, want %q", got.Id, edge.Id)
	}
	// The branch edge is not visible on main.
	if _, err := s.GetEdge(ctx, edge.Id, ""); !errors.Is(err, store.ErrEdgeNotFound) {
		t.Fatalf("branch edge visible on main, want ErrEdgeNotFound, got %v", err)
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
	s, err := OpenInMemory()
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
	s, err := OpenInMemory()
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

// TestRehydrateMainFromFiles_InferredTypeSurvivesFileBackedReopen pins the
// file-backed write-and-reopen cycle for an inferred type (SPEC R8): the
// inferred property type must be persisted to schema.json as the proto type
// "string" so the next Open's validateSchemaMetadata reconstructs a schema that
// schema.Validate accepts. Regression: the inference point used to store the
// catalog type "STRING", which validateSchemaMetadata fed back into
// schema.Validate and got rejected with ErrInvalidPropertyType — bricking the
// next file-backed Open.
func TestRehydrateMainFromFiles_InferredTypeSurvivesFileBackedReopen(t *testing.T) {
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

// TestCheckBranchSchemaCompatibility pins the SPEC R9 commit flow step 1 check:
// the branch DB's schema is validated against the current (main) schema.
// Additive changes (new properties, new types) and rule modifications are
// non-destructive (SPEC R2/R6) and pass; a property or type the branch's data
// lives under that is removed from the current schema is incompatible
// (ErrDestructiveSchemaChange).
func TestCheckBranchSchemaCompatibility(t *testing.T) {
	ctx := context.Background()
	opened, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	db := opened.(*ladybugDB)

	if err := db.ApplySchema(ctx, &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{{
			Name: "Component",
			Properties: []*flowv1.Property{
				{Name: "name", Type: "string"},
			},
		}},
	}); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	if err := db.CreateBranchDB(ctx, "tx1"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := db.ReplicateSchemaToBranch(ctx, "tx1"); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}

	// Additive push (new property, new entity type with rules, new edge type):
	// non-destructive per SPEC R2/R6, must not fail the compatibility check.
	additive := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
					{Name: "version", Type: "string"},
				},
			},
			{
				Name: "Service", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
				Rules: []*flowv1.ConnectionRule{{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}}},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "weight", Type: "string"}}},
		},
	}
	if err := db.ApplySchema(ctx, additive); err != nil {
		t.Fatalf("ApplySchema additive: %v", err)
	}
	if err := db.CheckBranchSchemaCompatibility(ctx, "tx1"); err != nil {
		t.Fatalf("additive schema push must be compatible, got %v", err)
	}

	// A property the branch's data lives under removed from the current schema
	// is incompatible. ApplySchema rejects destructive changes outright, so the
	// incompatible state is simulated directly — the check guards the state, not
	// the path that produced it.
	db.mu.Lock()
	db.entityTypeDefs["Component"].Properties = []store.PropertyDef{{Name: "version", Type: "string"}}
	db.mu.Unlock()
	if err := db.CheckBranchSchemaCompatibility(ctx, "tx1"); !errors.Is(err, store.ErrDestructiveSchemaChange) {
		t.Fatalf("removed property must be incompatible, got %v", err)
	}

	// A type the branch's data lives under removed from the current schema is
	// incompatible.
	db.mu.Lock()
	delete(db.entityTypeDefs, "Component")
	db.mu.Unlock()
	if err := db.CheckBranchSchemaCompatibility(ctx, "tx1"); !errors.Is(err, store.ErrDestructiveSchemaChange) {
		t.Fatalf("removed type must be incompatible, got %v", err)
	}
}

// TestEntityPropertiesNamedToAndType pins SPEC R1's implicit-column-collision
// scope: from/to/type are reserved only for *edge* properties and embedding
// only for vector-enabled entity types, so a NODE table declaring a property
// named `to` or `type` is SPEC-valid and passes schema.Validate. The schema
// cache must retain such columns as real properties (not drop them as if they
// were structural rel-table columns), or CreateEntity rejects the property with
// ErrUnknownProperty and a file-backed reopen fails closed when the metadata
// property is absent from the catalog.
func TestEntityPropertiesNamedToAndType(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{{
			Name: "Document",
			Properties: []*flowv1.Property{
				{Name: "to", Type: "string"},
				{Name: "type", Type: "string"},
				{Name: "title", Type: "string"},
			},
		}},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	// The properties must be present in the schema cache and usable by
	// CreateEntity (which rejects unknown properties with ErrUnknownProperty).
	doc, err := s.CreateEntity(ctx, "Document", "", map[string]string{
		"to": "someone", "type": "note", "title": "doc1",
	}, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity with to/type properties: %v", err)
	}
	if doc.Properties["to"] != "someone" || doc.Properties["type"] != "note" {
		t.Fatalf("created entity lost to/type properties: %+v", doc.Properties)
	}

	// Close and reopen: the properties must survive the catalog rebuild and the
	// metadata/catalog cross-check (validateMetadataAgainstCatalog).
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeStore(t, s2)

	def, ok := s2.EntityType("Document")
	if !ok {
		t.Fatal("Document entity type missing after reopen")
	}
	found := make(map[string]bool)
	for _, p := range def.Properties {
		found[p.Name] = true
	}
	if !found["to"] || !found["type"] {
		t.Fatalf("to/type properties dropped from schema cache after reopen: %v", def.Properties)
	}

	got, err := s2.GetEntity(ctx, doc.Id, "main")
	if err != nil {
		t.Fatalf("GetEntity after reopen: %v", err)
	}
	if got.Properties["to"] != "someone" || got.Properties["type"] != "note" {
		t.Fatalf("to/type property values lost after reopen: %+v", got.Properties)
	}

	// A create against the reopened store must still accept the properties.
	if _, err := s2.CreateEntity(ctx, "Document", "", map[string]string{
		"to": "someone-else", "type": "memo",
	}, nil, "main"); err != nil {
		t.Fatalf("CreateEntity with to/type properties after reopen: %v", err)
	}
}

// TestNonVectorTypeEmbeddingProperty_RoundTrips pins the SPEC R1 implicit-column
// collision scope for the name `embedding`: it is reserved only for
// vector-enabled entity types (SPEC:81, validate.go:113), so a non-vector type
// may legally declare a property named `embedding`. The property must
// round-trip through every boundary that special-cases the embedding column:
// entityFromNode (which previously skipped the key unconditionally, silently
// dropping the property on every read and thereby on the git file commit),
// getEmbeddingDimension's anomaly guard (which previously rejected a STRING
// embedding column as "anomalous", bricking Open's validateMetadataAgainstCatalog,
// ApplySchema re-apply's captureVectorState, and ReplicateSchemaToBranch).
// The shape is pinned with a file-backed write-and-reopen, an idempotent
// ApplySchema re-apply (SPEC R6), and a CreateBranchDB + ReplicateSchemaToBranch
// (the store-side begin-transaction sequence).
func TestNonVectorTypeEmbeddingProperty_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{{
			Name: "Document",
			Properties: []*flowv1.Property{
				{Name: "embedding", Type: "string"},
				{Name: "title", Type: "string"},
			},
		}},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	// Idempotent re-apply (SPEC R6) must succeed: ApplySchema's
	// captureVectorState reads the dimension for every type, and a STRING
	// embedding property column must not be treated as an anomalous vector
	// column.
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("idempotent ApplySchema re-apply: %v", err)
	}

	doc, err := s.CreateEntity(ctx, "Document", "", map[string]string{
		"embedding": embeddingPropertyValue, "title": "doc1",
	}, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity with embedding property: %v", err)
	}
	if doc.Properties["embedding"] != embeddingPropertyValue {
		t.Fatalf("created entity lost embedding property: %+v", doc.Properties)
	}
	if doc.Embedding != nil {
		t.Fatalf("non-vector type must not surface an Embedding vector, got %v", doc.Embedding)
	}

	// GetEntity must return the property (entityFromNode skip gate).
	got, err := s.GetEntity(ctx, doc.Id, "main")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if got.Properties["embedding"] != embeddingPropertyValue {
		t.Fatalf("embedding property lost on read: %+v", got.Properties)
	}

	// Begin-transaction sequence: ReplicateSchemaToBranch iterates every entity
	// type's dimension, and a STRING embedding property column must not brick it.
	if err := s.CreateBranchDB(ctx, "tx-embed-prop"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(ctx, "tx-embed-prop"); err != nil {
		t.Fatalf("ReplicateSchemaToBranch with non-vector embedding property: %v", err)
	}

	// Close and reopen: validateMetadataAgainstCatalog reads the dimension for
	// every type, and the STRING embedding column must not be rejected.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeStore(t, s2)

	got, err = s2.GetEntity(ctx, doc.Id, "main")
	if err != nil {
		t.Fatalf("GetEntity after reopen: %v", err)
	}
	if got.Properties["embedding"] != embeddingPropertyValue {
		t.Fatalf("embedding property lost after reopen: %+v", got.Properties)
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

// TestApplySchema_AddNewFromToPairOnExistingEdgeType_Rejected verifies the
// deliberate, documented divergence between SPEC R1/R2 (which treats a rule
// modification as non-destructive) and the storage engine: adding a rule that
// introduces a NEW FROM/TO pair on an existing edge type changes the rel
// table's endpoint clauses, which Ladybug fixes at CREATE time and cannot
// ALTER. Such a change must therefore be rejected as a destructive schema
// change, not silently applied.
func TestApplySchema_AddNewFromToPairOnExistingEdgeType_Rejected(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Initial schema: X connects to Y via edge R only.
	schema1 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "X", Rules: []*flowv1.ConnectionRule{
				{CanConnectTo: []string{"Y"}, Using: []string{"R"}},
			}},
			{Name: "Y"},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "R"}},
	}
	if err := s.ApplySchema(ctx, schema1); err != nil {
		t.Fatalf("first ApplySchema: %v", err)
	}

	// Extend the schema with a second rule that adds a NEW FROM/TO pair
	// (X→Z) on the EXISTING edge type R. SPEC R1 membership-OR makes this a
	// valid schema; the rel table cannot express the added pair, so the store
	// must reject it as a destructive change rather than silently accepting it.
	schema2 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "X", Rules: []*flowv1.ConnectionRule{
				{CanConnectTo: []string{"Y"}, Using: []string{"R"}},
				{CanConnectTo: []string{"Z"}, Using: []string{"R"}},
			}},
			{Name: "Y"},
			{Name: "Z"},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "R"}},
	}
	err = s.ApplySchema(ctx, schema2)
	if err == nil {
		t.Fatal("expected destructive schema change for a new FROM/TO pair on an existing edge type")
	}
	if !errors.Is(err, store.ErrDestructiveSchemaChange) {
		t.Fatalf("expected ErrDestructiveSchemaChange, got %v", err)
	}
}

// TestApplySchema_RedundantRulesDedupPairsSurviveReopen verifies that
// overlapping/redundant rules (valid per SPEC R1 membership-OR semantics,
// which merge the canConnectTo and using lists across rule entries) do NOT
// brick the store. The pair-derivation paths must dedup consistently so the
// metadata-derived pair set matches the rel table's endpoint clauses on reopen.
func TestApplySchema_RedundantRulesDedupPairsSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Two identical overlapping rules both yield a (T→X) pair via DEPENDS_ON:
	// the extraction produces exactly the same FROM/TO pair twice.
	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "T",
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"X"}, Using: []string{"DEPENDS_ON"}},
					{CanConnectTo: []string{"X"}, Using: []string{"DEPENDS_ON"}},
				},
			},
			{Name: "X"},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "DEPENDS_ON"}},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	// Create a matching entity and edge so a reopen that silently corrupts the
	// catalog comparison has observable data to lose.
	src, err := s.CreateEntity(ctx, "T", "", nil, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity T: %v", err)
	}
	tgt, err := s.CreateEntity(ctx, "X", "", nil, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity X: %v", err)
	}
	edge, err := s.CreateEdge(ctx, "DEPENDS_ON", src.Id, tgt.Id, nil, "main")
	if err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}

	// Reopen — the pre-fix code derived duplicate pairs on the reopen path and
	// failed the catalog comparison (equalFromToPairs), bricking the open.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen with redundant rules: %v", err)
	}
	defer closeStore(t, reopened)
	if _, err := reopened.GetEdge(ctx, edge.Id, "main"); err != nil {
		t.Fatalf("reopened edge missing: %v", err)
	}
}

// TestSearchNeighbors_WildcardHeterogeneousDimensions verifies that a wildcard
// (entityType == "") search skips entity types whose established vector
// dimension does not match the query embedding and aggregates only the
// matching-dimension types, instead of aborting on the first mismatched type.
func TestSearchNeighbors_WildcardHeterogeneousDimensions(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "TypeA", EnableVectorIndex: true, Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
			{Name: "TypeB", EnableVectorIndex: true, Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	// Bootstrap TypeA to dimension 3 and TypeB to dimension 5.
	if _, err := s.CreateEntity(ctx, "TypeA", "", map[string]string{"name": "a"}, []float32{1, 2, 3}, ""); err != nil {
		t.Fatalf("bootstrap TypeA: %v", err)
	}
	if _, err := s.CreateEntity(
		ctx, "TypeB", "", map[string]string{"name": "b"}, []float32{1, 2, 3, 4, 5}, "",
	); err != nil {
		t.Fatalf("bootstrap TypeB: %v", err)
	}

	// A dimension-3 query matches only TypeA; TypeB (dim 5) must be skipped,
	// not treated as an error that aborts the whole search.
	results, err := s.SearchNeighbors(ctx, []float32{0.9, 0, 0}, "", 10, "")
	if err != nil {
		t.Fatalf("SearchNeighbors with mixed dimensions: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected neighbors from the matching-dimension type")
	}
	for _, r := range results {
		if r.Entity.Type == "TypeB" {
			t.Fatalf("expected no TypeB neighbor (dimension 5) for a dimension-3 query, got %+v", r)
		}
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

// TestApplySchema_RemovedEntityType_Rejected pins the removed-entity-type
// branch of the ApplySchema catalog diff (schema.go diffSchemaAgainstCatalog).
// SPEC:86,205 name type removal as destructive (a subset schema constitutes
// removal of the omitted type) and SPEC:930 maps the resulting table-structure
// mismatch to FAILED_PRECONDITION; the store surfaces it as
// ErrDestructiveSchemaChange.
func TestApplySchema_RemovedEntityType_Rejected(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	// Apply initial schema with two entity types.
	schema1 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Document", Properties: []*flowv1.Property{{Name: "title", Type: "string"}}},
			{Name: "Note", Properties: []*flowv1.Property{{Name: "body", Type: "string"}}},
		},
	}
	if err := s.ApplySchema(ctx, schema1); err != nil {
		t.Fatalf("first ApplySchema: %v", err)
	}

	// Destructive: omit the applied "Note" entity type from the new schema.
	destructive := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Document", Properties: []*flowv1.Property{{Name: "title", Type: "string"}}},
		},
	}
	err = s.ApplySchema(ctx, destructive)
	if err == nil {
		t.Fatal("expected error for removed entity type")
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
	if !s.TableExists("Document") {
		t.Fatal("Document table should exist after wipe+apply")
	}
}

// TestApplySchema_RemovedEdgeType_Rejected pins the removed-edge-type branch of
// the ApplySchema catalog diff (schema.go diffSchemaAgainstCatalog). SPEC:86
// permits an empty or omitted edgeTypes array; a schema that omits an applied
// edge type constitutes its removal, which SPEC:205/930 name destructive
// (table-structure mismatch → FAILED_PRECONDITION), surfaced by the store as
// ErrDestructiveSchemaChange.
func TestApplySchema_RemovedEdgeType_Rejected(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	// Apply initial schema with an edge type under a FROM/TO rule.
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

	// Destructive: omit the applied DEPENDS_ON edge type (and the rule that
	// references it) from the new schema.
	destructive := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Service"},
			{Name: "Component"},
		},
		EdgeTypes: []*flowv1.EdgeType{},
	}
	err = s.ApplySchema(ctx, destructive)
	if err == nil {
		t.Fatal("expected error for removed edge type")
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
	if !s.TableExists("Service") {
		t.Fatal("Service table should exist after wipe+apply")
	}
}

// TestApplySchema_ChangedEntityPropertyType_Rejected pins the changed-property-
// type branch of the ApplySchema catalog diff (schema.go diffSchemaAgainstCatalog,
// entity side). SPEC:930's "existing column has a different type than declared"
// condition is a destructive table-structure mismatch (FAILED_PRECONDITION →
// ErrDestructiveSchemaChange). The physical column can only carry a non-string
// type via a drifted catalog state (schema.Validate accepts only "string"
// properties, so no public API creates such a column), so the cached catalog
// type is drifted directly — the same simulation pattern as
// TestCheckBranchSchemaCompatibility — and ApplySchema must reject the
// re-application with the sentinel.
func TestApplySchema_ChangedEntityPropertyType_Rejected(t *testing.T) {
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
				{Name: "num", Type: "string"},
			},
		}},
	}
	if err := s.ApplySchema(ctx, schema1); err != nil {
		t.Fatalf("first ApplySchema: %v", err)
	}

	// Drift the cached catalog type so the physical column no longer matches
	// the declared "string" (SPEC:930's different-column-type condition).
	db := s.(*ladybugDB)
	db.mu.Lock()
	db.entityTypeDefs["Document"].Properties[1].Type = "INT64"
	db.mu.Unlock()

	err = s.ApplySchema(ctx, schema1)
	if err == nil {
		t.Fatal("expected error for changed property type")
	}
	if !errors.Is(err, store.ErrDestructiveSchemaChange) {
		t.Fatalf("expected ErrDestructiveSchemaChange, got %v", err)
	}

	// WipeSchema clears the drifted cache; ApplySchema should succeed.
	if err := s.WipeSchema(ctx); err != nil {
		t.Fatalf("WipeSchema: %v", err)
	}
	if err := s.ApplySchema(ctx, schema1); err != nil {
		t.Fatalf("ApplySchema after WipeSchema: %v", err)
	}
}

// TestApplySchema_ChangedEdgePropertyType_Rejected pins the changed-property-
// type branch of the ApplySchema catalog diff (schema.go diffSchemaAgainstCatalog,
// edge side), mirroring the entity-side test: SPEC:930's different-column-type
// condition is destructive (FAILED_PRECONDITION → ErrDestructiveSchemaChange).
func TestApplySchema_ChangedEdgePropertyType_Rejected(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	// Apply initial schema with an edge type carrying a property.
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

	// Drift the cached catalog type so the physical column no longer matches
	// the declared "string".
	db := s.(*ladybugDB)
	db.mu.Lock()
	db.edgeTypeDefs["DEPENDS_ON"].Properties[0].Type = "INT64"
	db.mu.Unlock()

	err = s.ApplySchema(ctx, schema1)
	if err == nil {
		t.Fatal("expected error for changed property type")
	}
	if !errors.Is(err, store.ErrDestructiveSchemaChange) {
		t.Fatalf("expected ErrDestructiveSchemaChange, got %v", err)
	}

	// WipeSchema clears the drifted cache; ApplySchema should succeed.
	if err := s.WipeSchema(ctx); err != nil {
		t.Fatalf("WipeSchema: %v", err)
	}
	if err := s.ApplySchema(ctx, schema1); err != nil {
		t.Fatalf("ApplySchema after WipeSchema: %v", err)
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

// SPEC R2 (line ~197) and R6 (line ~386): changing enableVectorIndex from
// false to true on an existing entity type is non-destructive (adds the
// embedding column via ALTER TABLE ADD COLUMN). Unlike the destructive
// true→false transition (TestApplySchema_DestructiveChange_VectorDisable),
// the false→true transition must be applied additively with no error, must
// then allow an embedding CreateEntity to lazily bootstrap the dimension, and
// a close/reopen must restore the lazy vector index from the persisted schema.
func TestApplySchema_EnableVectorIndexFalseToTrue_NonDestructive(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Apply schema with the vector index disabled: the Document table is created
	// without an embedding column (lazy bootstrap never fires without an
	// embedding write and EnableVectorIndex is false).
	disabled := &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
		Name: "Document", EnableVectorIndex: false,
		Properties: []*flowv1.Property{{Name: "title", Type: "string"}},
	}}}
	if err := s.ApplySchema(ctx, disabled); err != nil {
		t.Fatalf("first ApplySchema (vector disabled): %v", err)
	}
	if _, err := s.CreateEntity(ctx, "Document", "",
		map[string]string{"title": "doc"}, []float32{1, 2, 3}, "main"); err != nil {
		t.Fatalf("CreateEntity on non-vector type should accept (and discard) an embedding: %v", err)
	}
	if s.IsVectorIndexBootstrapped("Document", "main") {
		t.Fatal("vector index must not be bootstrapped while EnableVectorIndex is false")
	}

	// Re-apply the same entity type with EnableVectorIndex true. Per SPEC R2/R6
	// this is additive (the embedding column is added via ALTER) and must NOT
	// surface ErrDestructiveSchemaChange.
	enabled := &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
		Name: "Document", EnableVectorIndex: true,
		Properties: []*flowv1.Property{{Name: "title", Type: "string"}},
	}}}
	if err := s.ApplySchema(ctx, enabled); err != nil {
		t.Fatalf("false→true ApplySchema must be non-destructive, got %v", err)
	}
	if s.IsVectorIndexBootstrapped("Document", "main") {
		t.Fatal("the false→true transition must stay lazy — no entity written yet")
	}

	// A first embedding write now bootstraps the dimension (SPEC R7 lazy).
	if _, err := s.CreateEntity(ctx, "Document", "",
		map[string]string{"title": "vec"}, []float32{1, 2, 3}, "main"); err != nil {
		t.Fatalf("CreateEntity with embedding after enable: %v", err)
	}
	if !s.IsVectorIndexBootstrapped("Document", "main") {
		t.Fatal("expected vector index bootstrapped after first embedding write")
	}
	if dim, derr := s.GetEstablishedDimension("Document", "main"); derr != nil || dim != 3 {
		t.Fatalf("dimension after enable = %d, error = %v, want 3", dim, derr)
	}

	// Reopen: the lazy vector index must be restored from persisted schema.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeStore(t, s2)
	if !s2.IsVectorIndexBootstrapped("Document", "main") {
		t.Fatal("lazy vector index was not restored on reopen")
	}
	if dim, derr := s2.GetEstablishedDimension("Document", "main"); derr != nil || dim != 3 {
		t.Fatalf("restored dimension = %d, error = %v, want 3", dim, derr)
	}
}

// WipeSchema drops every schema table and clears the in-memory schema cache,
// but a store primitive must not leave stale branch connections or persisted
// branch records dangling: an open branch connection cached the dropped tables
// (a later branch op would error on a vanished schema), and a persisted
// branches/<txID>.state.json would let SaveBranchTransactionState re-register a
// branch whose database and schema are gone. The store primitive must drop them
// itself — defense-in-depth behind the service-layer FAILED_PRECONDITION
// (SPEC row ~915) which only guards a live transaction.
func TestWipeSchema_ClosesOpenBranchesAndRemovesPersistedState(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	schema := &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
		Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
	}}}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	const txID = "00000000-0000-4000-a000-000000000001"
	if err := s.CreateBranchDB(ctx, txID); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(ctx, txID); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}
	if err := s.SaveBranchTransactionState(ctx, txID, store.BranchTransactionState{
		MainHeadAtLastSync: "head", SchemaHash: "schema",
	}); err != nil {
		t.Fatalf("SaveBranchTransactionState: %v", err)
	}
	// Confirm the durable branch state file and open connection exist pre-wipe.
	if _, err := os.Stat(filepath.Join(dir, "branches", txID+".state.json")); err != nil {
		t.Fatalf("expected persisted branch state before wipe: %v", err)
	}
	ldb := s.(*ladybugDB)
	if _, ok := ldb.branches[txID]; !ok {
		t.Fatal("expected branch connection registered before wipe")
	}

	if err := s.WipeSchema(ctx); err != nil {
		t.Fatalf("WipeSchema: %v", err)
	}
	// The open branch connection was closed and removed from the registry.
	if _, ok := ldb.branches[txID]; ok {
		t.Fatal("open branch survived WipeSchema")
	}
	// The durable branch state and database records were removed.
	if _, err := os.Stat(filepath.Join(dir, "branches", txID+".state.json")); !os.IsNotExist(err) {
		t.Fatalf("persisted branch state survived WipeSchema: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "branches", txID+".lbug")); !os.IsNotExist(err) {
		t.Fatalf("persisted branch database survived WipeSchema: %v", err)
	}
	// A post-wipe branch operation can no longer be issued against the stale
	// branch (previously it would operate against dropped tables).
	if err := s.ReplicateSchemaToBranch(ctx, txID); !errors.Is(err, store.ErrBranchNotFound) {
		t.Fatalf("expected ErrBranchNotFound after wipe, got %v", err)
	}
	// SaveBranchTransactionState can no longer re-register the stale branch.
	if err := s.SaveBranchTransactionState(ctx, txID, store.BranchTransactionState{
		MainHeadAtLastSync: "head", SchemaHash: "schema",
	}); !errors.Is(err, store.ErrBranchNotFound) {
		t.Fatalf("expected ErrBranchNotFound re-registering wiped branch state, got %v", err)
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

// TestListEntities_CheckOrder pins the SPEC:960 ListEntities structural check
// order — unknown entity type → pageSize → pageToken — at the store layer: when
// multiple inputs are invalid, the earliest check in that order is the error
// surfaced (entity type wins over pageSize and pageToken; pageSize wins over
// pageToken).
func TestListEntities_CheckOrder(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	badTok := base64.StdEncoding.EncodeToString([]byte("not-a-number"))

	// Unknown entity type surfaces before invalid pageSize (negative and over-max).
	for _, pageSize := range []int{-1, 1001} {
		_, _, err := s.ListEntities(context.Background(), "NoSuchType", pageSize, "", "")
		if !errors.Is(err, store.ErrUnknownEntityType) {
			t.Errorf("pageSize %d: expected ErrUnknownEntityType, got %v", pageSize, err)
		}
	}

	// Unknown entity type surfaces before an invalid page token.
	_, _, err = s.ListEntities(context.Background(), "NoSuchType", 10, badTok, "")
	if !errors.Is(err, store.ErrUnknownEntityType) {
		t.Errorf("expected ErrUnknownEntityType over ErrInvalidPageToken, got %v", err)
	}

	// Invalid pageSize surfaces before an invalid page token (known type).
	_, _, err = s.ListEntities(context.Background(), "Component", -1, badTok, "")
	if !errors.Is(err, store.ErrInvalidPageSize) {
		t.Errorf("expected ErrInvalidPageSize over ErrInvalidPageToken, got %v", err)
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
// Once the vector index is created, LadybugDB refuses `SET embedding` on an
// existing row ("Cannot set property ... because it is used in one or more
// indexes"), so UpdateEntity cannot persist an embedding value. The store
// surfaces the defined ErrEmbeddingUpdateUnsupported sentinel at its boundary
// (rather than leaking the raw engine error), while the bootstrap DDL still
// runs first and locks the dimension.
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
	// column + CREATE_VECTOR_INDEX, locking the dimension to 3, then rejects
	// the embedding change with the defined sentinel.
	_, err = s.UpdateEntity(context.Background(), id, nil, []float32{1, 2, 3}, "")
	if !errors.Is(err, store.ErrEmbeddingUpdateUnsupported) {
		t.Fatalf("expected ErrEmbeddingUpdateUnsupported, got %v", err)
	}
	if !s.IsVectorIndexBootstrapped("VectorType", "") {
		t.Fatalf("expected vector index bootstrapped after first embedding update (update err: %v)", err)
	}
	if dim, derr := s.GetEstablishedDimension("VectorType", ""); derr != nil || dim != 3 {
		t.Fatalf("dimension = %d, error = %v (update err: %v)", dim, derr, err)
	}
}

// SPEC R7 parity (crud.go:271-283): a post-bootstrap UpdateEntity supplying an
// embedding whose dimension MATCHES the established dimension (dim > 0,
// len(embedding) == dim) falls through both bootstrap (dim != 0) and the
// dimension-mismatch guard, and surfaces the defined
// ErrEmbeddingUpdateUnsupported sentinel — the vector index prevents rewriting
// an existing row's embedding. This is the third distinct branch of the
// sentinel, distinct from TestUpdateEntity_EmbeddingBootstrap (dim == 0
// bootstrap-then-reject) and TestUpdateEntity_EmbeddingDimensionMismatch
// (dim > 0, mismatched length).
func TestUpdateEntity_EmbeddingMatchingDimensionUnsupported(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	// Bootstrap VectorType to dimension 3 via a create.
	e, err := s.CreateEntity(ctx, "VectorType", "",
		map[string]string{"name": "v"}, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("bootstrap CreateEntity: %v", err)
	}
	if !s.IsVectorIndexBootstrapped("VectorType", "") {
		t.Fatal("expected VectorType bootstrapped after create")
	}

	// Matching-dimension update: the dimension guard passes (3 == 3) and the
	// sentinel surfaces without any bootstrap DDL.
	_, err = s.UpdateEntity(ctx, e.Id, nil, []float32{4, 5, 6}, "")
	if !errors.Is(err, store.ErrEmbeddingUpdateUnsupported) {
		t.Fatalf("expected ErrEmbeddingUpdateUnsupported for matching-dimension embedding, got %v", err)
	}
	// The dimension is unchanged (already locked by the create) and the entity
	// survives the rejected update.
	if dim, derr := s.GetEstablishedDimension("VectorType", ""); derr != nil || dim != 3 {
		t.Fatalf("dimension = %d, error = %v, want 3", dim, derr)
	}
	got, err := s.GetEntity(ctx, e.Id, "")
	if err != nil {
		t.Fatalf("GetEntity after rejected embedding update: %v", err)
	}
	if got.Properties["name"] != "v" {
		t.Fatalf("entity properties changed by rejected update: %+v", got.Properties)
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

// TestBranch_DropBranchDB_LeavesMainUnbootstrapped verifies SPEC R7 "branch
// scope": a vector dimension bootstrapped by a transaction branch, then rolled
// back via DropBranchDB, leaves main un-bootstrapped (GetEstablishedDimension
// == 0). The bootstrap DDL (ALTER TABLE ADD embedding + CREATE_VECTOR_INDEX)
// runs on the branch's own connection; dropping the branch must not leak that
// side-effect into main.
func TestBranch_DropBranchDB_LeavesMainUnbootstrapped(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	if s.IsVectorIndexBootstrapped("VectorType", "") {
		t.Fatal("expected VectorType not bootstrapped on main before branch")
	}

	const branch = "tx-drop-bootstrap"
	if err := s.CreateBranchDB(ctx, branch); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}

	// Bootstrap the vector dimension inside the branch.
	if _, err := s.CreateEntity(ctx, "VectorType", "",
		map[string]string{"name": "v"}, []float32{1, 2, 3}, branch); err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}
	if !s.IsVectorIndexBootstrapped("VectorType", branch) {
		t.Fatal("expected VectorType bootstrapped on branch")
	}

	// Rollback: drop the branch.
	if err := s.DropBranchDB(ctx, branch); err != nil {
		t.Fatalf("DropBranchDB: %v", err)
	}

	// Main must remain un-bootstrapped (SPEC R7 branch scope).
	if s.IsVectorIndexBootstrapped("VectorType", "") {
		t.Fatal("main must not be bootstrapped after branch rollback")
	}
	dim, err := s.GetEstablishedDimension("VectorType", "")
	if err != nil {
		t.Fatalf("GetEstablishedDimension: %v", err)
	}
	if dim != 0 {
		t.Fatalf("expected main dimension 0 after branch rollback, got %d", dim)
	}
}

// TestBranch_InheritsMainVectorDimension_RejectsConflict verifies that a
// branch opened over a pre-bootstrapped main inherits main's established
// vector dimension (via ReplicateSchemaToBranch copying the FLOAT[n] column
// and HNSW index) and rejects a CreateEntity whose embedding dimension
// conflicts. This is the store-layer path that surfaces the ABORTED Refresh
// conflict of SPEC R7 when the branch's dimension disagrees with main's.
func TestBranch_InheritsMainVectorDimension_RejectsConflict(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	// Bootstrap main to dimension 3.
	if _, err := s.CreateEntity(ctx, "VectorType", "",
		map[string]string{"name": "main-v"}, []float32{1, 2, 3}, ""); err != nil {
		t.Fatalf("bootstrap main vector: %v", err)
	}
	if dim, err := s.GetEstablishedDimension("VectorType", ""); err != nil || dim != 3 {
		t.Fatalf("main dimension = %d, err = %v", dim, err)
	}

	const branch = "tx-inherit-dim"
	if err := s.CreateBranchDB(ctx, branch); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}

	// Matching dimension on branch — should succeed.
	if _, err := s.CreateEntity(ctx, "VectorType", "",
		map[string]string{"name": "branch-v"}, []float32{4, 5, 6}, branch); err != nil {
		t.Fatalf("CreateEntity on branch with matching dimension: %v", err)
	}

	// Conflicting dimension on branch — must fail with ErrEmbeddingDimension.
	_, err = s.CreateEntity(ctx, "VectorType", "",
		map[string]string{"name": "conflict"}, []float32{1, 2, 3, 4, 5}, branch)
	if err == nil {
		t.Fatal("expected dimension mismatch error on branch")
	}
	if !errors.Is(err, store.ErrEmbeddingDimension) {
		t.Errorf("expected ErrEmbeddingDimension, got %v", err)
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

// ---------------------------------------------------------------------------
// Review: declared-but-not-bootstrapped single-type search, absent-FTS-index
// skip, corruption heuristic (SPEC R8), and mid-multi-table-DDL fail-closed
// ---------------------------------------------------------------------------

// A vector-enabled entity type that has been declared with EnableVectorIndex
// but whose embedding column has not yet been bootstrapped (dim == 0 — no
// entity written yet) is legitimately not searchable, not an error (query.go
// searchIndexedType skips silently, SPEC R7 lazy bootstrap). This pins the
// single-type (non-empty entityType) success branch: SearchNeighbors returns an
// empty result set with a nil error rather than erroring or fabricating data.
func TestSearchNeighbors_DeclaredNotBootstrappedType_SucceedsEmpty(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()
	// testSchema declares VectorType with EnableVectorIndex=true, but no entity
	// is ever created, so its vector index is never bootstrapped (dim == 0).
	applyTestSchema(t, s)

	results, err := s.SearchNeighbors(ctx, []float32{1, 2, 3}, "VectorType", 10, "")
	if err != nil {
		t.Fatalf("declared-but-not-bootstrapped single-type search should succeed with nil error, got %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty result set for not-yet-bootstrapped index, got %d", len(results))
	}
}

// FullTextSearch silently skips an entity type whose FTS index is absent
// (query.go FullTextSearch, ponytail at the Prepare-failure `continue`): the
// search returns a result set with nil error and no partial-result notice, so
// an index-less type contributes nothing. This pins the skip branch — dropping
// a table's FTS index and then searching it must NOT error and must return
// nothing rather than fabricating results.
func TestFullTextSearch_MissingIndexSilentlySkipped(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()
	applyTestSchema(t, s)

	if _, err := s.CreateEntity(ctx, "Document", "",
		map[string]string{"title": "needle"}, nil, ""); err != nil {
		t.Fatalf("CreateEntity Document: %v", err)
	}
	// Confirm the type is currently FTS-searchable.
	if matches, err := s.FullTextSearch(ctx, "needle", "Document", ""); err != nil || len(matches) == 0 {
		t.Fatalf("expected Document FTS searchable before drop, matches=%d err=%v", len(matches), err)
	}

	// Drop the FTS index; the table itself remains.
	db := s.(*ladybugDB)
	res, err := db.conn.Query("CALL DROP_FTS_INDEX('Document', 'Document_fts');")
	if err != nil {
		t.Fatalf("drop FTS index: %v", err)
	}
	res.Close()
	if ftsIndexExists(db.conn, "Document") {
		t.Fatal("expected Document FTS index dropped")
	}

	// Querying the index-less type must silently succeed with an empty result,
	// exercising the Prepare-fail `continue` (skip) branch.
	results, err := s.FullTextSearch(ctx, "needle", "Document", "")
	if err != nil {
		t.Fatalf("expected silent skip (nil error) for absent FTS index, got %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty result set for absent FTS index, got %d", len(results))
	}
}

// The corruption heuristic (ladybug.go corruptionCandidates, SPEC R8) classifies
// an OpenDatabase failure by file accessibility: a present-but-readable file is
// a corruption candidate (the engine could not parse genuine contents) while a
// file the OS layer cannot open is an operational (permission/I/O) failure that
// must NOT be treated as corrupt (removing it would destroy never-corrupt data).
// This unit test drives both outcomes.
func TestCorruptionCandidates_ReadableVersusUnreadable(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "main.lbug")
	if err := os.WriteFile(dbPath, []byte("corrupt-bytes"), 0600); err != nil {
		t.Fatalf("write readable file: %v", err)
	}

	// Readable file -> candidate for corruption recovery.
	if !corruptionCandidates(dbPath) {
		t.Fatal("expected a readable present file to be a corruption candidate")
	}

	// Unreadable file (mode 000) -> NOT a candidate; it is an operational
	// open problem, not engine-unparseable content.
	if err := os.Chmod(dbPath, 0000); err != nil {
		t.Fatalf("chmod 000: %v", err)
	}
	if corruptionCandidates(dbPath) {
		t.Fatal("expected an unreadable file to NOT be a corruption candidate")
	}
	// Restore permissions so the temp dir can be cleaned up.
	if err := os.Chmod(dbPath, 0600); err != nil {
		t.Fatalf("restore chmod: %v", err)
	}

	// A missing file is never a candidate (OpenDatabase creates it fresh).
	if corruptionCandidates(filepath.Join(dir, "absent.lbug")) {
		t.Fatal("expected an absent file to NOT be a corruption candidate")
	}
}

// Open's SPEC R8 repair path: a genuinely corrupted main.lbug (present and
// readable, but unparsable by the engine) is deleted and re-opened fresh, with
// schema rehydrated from the persisted metadata. An unreadable main.lbug is an
// operational failure and must NOT be deleted — Open fails and the file remains.
func TestOpenCorruptDatabase_RecoversOrFailsClosed(t *testing.T) {
	t.Run("readable corrupt file recovers", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		schema := &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
			Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
		}}}
		if err := s.ApplySchema(context.Background(), schema); err != nil {
			t.Fatalf("ApplySchema: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		dbPath := filepath.Join(dir, "main.lbug")
		// Overwrite with garbage the engine cannot parse.
		if err := os.WriteFile(dbPath, []byte("not a ladybug database"), 0600); err != nil {
			t.Fatalf("corrupt main.lbug: %v", err)
		}
		if !corruptionCandidates(dbPath) {
			t.Fatal("expected corrupt file to be classified as a corruption candidate")
		}
		recovered, err := Open(dir)
		if err != nil {
			t.Fatalf("Open should recover a corrupt readable main.lbug, got %v", err)
		}
		defer closeStore(t, recovered)
		// Recovery re-creates the schema from metadata.
		if !recovered.TableExists("Component") {
			t.Fatal("recovery did not rehydrate the Component table from metadata")
		}
		// The corrupt file was replaced by a freshly-created valid database.
		if _, err := os.Stat(dbPath); err != nil {
			t.Fatalf("recovered database file missing: %v", err)
		}
	})

	t.Run("unreadable file classified and preserved", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		dbPath := filepath.Join(dir, "main.lbug")
		if err := os.Chmod(dbPath, 0000); err != nil {
			t.Fatalf("chmod 000: %v", err)
		}
		// Not a corruption candidate: Open must fail WITHOUT removing the file.
		if corruptionCandidates(dbPath) {
			t.Fatal("unreadable file must not be a corruption candidate")
		}
		if reopened, err := Open(dir); err == nil {
			_ = reopened.Close()
			t.Fatal("expected Open to fail for an unreadable (non-corrupt) main.lbug")
		}
		if _, statErr := os.Stat(dbPath); statErr != nil {
			t.Fatalf("unreadable file was removed by Open: %v", statErr)
		}
	})
}

// ApplySchema applies DDL for a fresh multi-table schema before publishing
// metadata, so an intermediate (mid-loop, e.g. second-table) DDL failure leaves
// the already-created tables in the catalog with NO corresponding schema
// metadata (the metadata publish happens only after the full DDL loop). On a
// subsequent reopen, the orphan table fails the metadata/catalog cross-check
// and the store must fail closed. This drives that fail-closed-on-reopen
// property by reconstructing the exact partial state a mid-DDL failure leaves:
// the first table's metadata is intact; a second table exists in the catalog but
// was never published to metadata.
func TestApplySchema_MidMultiTableDDLFailureFailsClosedOnReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// First table: applied and published normally.
	if err := s.ApplySchema(context.Background(), &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
		{Name: "First", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
	}}); err != nil {
		t.Fatalf("ApplySchema First: %v", err)
	}
	// Simulate the residue of a second-table DDL failure: the second table was
	// created in the catalog, but because ApplySchema aborted before publish the
	// schema.json metadata still describes only "First".
	db := s.(*ladybugDB)
	orphanDDL := "CREATE NODE TABLE IF NOT EXISTS `Second` (id STRING PRIMARY KEY, name STRING);"
	if _, err := db.conn.Query(orphanDDL); err != nil {
		t.Fatalf("create orphaned Second table: %v", err)
	}
	if db.edgeTypeDefs["Second"] != nil || db.entityTypeDefs["Second"] != nil {
		t.Fatal("orphaned table must not be in the schema cache")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen: the orphaned catalog table has no metadata entry, so the
	// validate-MetadataAgainstCatalog cross-check must fail and Open must
	// reject the store (fail closed) rather than silently dropping the table.
	if reopened, err := Open(dir); err == nil {
		_ = reopened.Close()
		t.Fatal("expected fail-closed Open after a mid-DDL partial schema apply")
	}
}

// ---------------------------------------------------------------------------
// Special-fixer: silent-drop / silent-identity read-path guards and missing
// SPEC-branch tests
// ---------------------------------------------------------------------------

// An edge file whose from/to endpoint entities are absent from the graph must
// fail loudly instead of silently vanishing: insertEdgeOnConn's
// MATCH (a {id: $from}), (b {id: $to}) CREATE ... no-ops when an endpoint
// matches nothing, so without the endpoint-existence guard the edge would be
// dropped on the re-hydration read path with no error (learnings rule: never
// silently drop a row or swallow a not-exist on a read path).
func TestRehydrateMainFromFiles_EdgeWithMissingEndpointFailsLoudly(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	// Load one endpoint entity via files; the edge's `to` endpoint references
	// an ID absent from the graph (orphaned edge file).
	fromID := uuid.NewString()
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	edgesDir := filepath.Join(root, "edges")
	writeJSONFile(t, filepath.Join(entitiesDir, "Component", fromID+".json"), map[string]any{
		"id": fromID, "type": "Component",
	})
	writeJSONFile(t, filepath.Join(edgesDir, "DependsOn", uuid.NewString()+".json"), map[string]any{
		"id": uuid.NewString(), "type": "DependsOn", "from": fromID, "to": uuid.NewString(),
	})

	err = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
	if err == nil {
		t.Fatal("expected loud failure for an edge whose endpoint entity is absent")
	}
	if !errors.Is(err, store.ErrSourceOrTargetNotFound) {
		t.Fatalf("expected ErrSourceOrTargetNotFound, got %v", err)
	}
}

// A JSON element file with a missing `id` key must fail loudly on every load
// path (main/branch × entity/edge) instead of silently assigning a fresh UUID:
// a generated ID changes the element's identity and diverges from its filename,
// so the next serialisation would rewrite the element under a new name,
// orphaning the original file. The sibling checks (missing `type`, type/directory
// mismatch, unparseable content) all fail loudly; the missing `id` must too.
func TestRehydrateFiles_MissingIDFailsLoudly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		branch bool
		edge   bool
		want   error
	}{
		{"main entity", false, false, store.ErrInvalidEntityDir},
		{"main edge", false, true, store.ErrInvalidEdgeDir},
		{"branch entity", true, false, store.ErrInvalidEntityDir},
		{"branch edge", true, true, store.ErrInvalidEdgeDir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := OpenInMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer closeStore(t, s)
			applyTestSchema(t, s)
			ctx := context.Background()

			const branch = "tx1"
			if tc.branch {
				if err := s.CreateBranchDB(ctx, branch); err != nil {
					t.Fatalf("CreateBranchDB: %v", err)
				}
				if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
					t.Fatalf("ReplicateSchemaToBranch: %v", err)
				}
			}

			root := t.TempDir()
			entitiesDir := filepath.Join(root, "entities")
			edgesDir := filepath.Join(root, "edges")
			if tc.edge {
				// Edge file with every required key except `id`.
				writeJSONFile(t, filepath.Join(edgesDir, "DependsOn", "edge.json"), map[string]any{
					"type": "DependsOn", "from": uuid.NewString(), "to": uuid.NewString(),
				})
			} else {
				// Entity file with every required key except `id`.
				writeJSONFile(t, filepath.Join(entitiesDir, "Component", "ent.json"), map[string]any{
					"type": "Component", "properties": map[string]string{"name": "no-id"},
				})
			}

			var loadErr error
			if tc.branch {
				loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
			} else {
				loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			}
			if loadErr == nil {
				t.Fatal("expected loud failure for a file missing the required 'id' key")
			}
			if !errors.Is(loadErr, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, loadErr)
			}
		})
	}
}

// A JSON entity file whose `id` key is present but whose required `type` key
// is absent must fail loudly on every load path (branch.go:1113-1116 main,
// branch.go:1277-1280 branch → ErrInvalidEntityDir): a type-less file cannot
// be tied to its directory label, so the sibling missing-`id` guard's
// protection would be incomplete without it.
func TestRehydrateFiles_MissingTypeFailsLoudly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		branch bool
	}{
		{"main entity", false},
		{"branch entity", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := OpenInMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer closeStore(t, s)
			applyTestSchema(t, s)
			ctx := context.Background()

			const branch = "tx1"
			if tc.branch {
				if err := s.CreateBranchDB(ctx, branch); err != nil {
					t.Fatalf("CreateBranchDB: %v", err)
				}
				if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
					t.Fatalf("ReplicateSchemaToBranch: %v", err)
				}
			}

			root := t.TempDir()
			entitiesDir := filepath.Join(root, "entities")
			edgesDir := filepath.Join(root, "edges")
			// Entity file with every required key except `type`.
			writeJSONFile(t, filepath.Join(entitiesDir, "Component", "ent.json"), map[string]any{
				"id": uuid.NewString(), "properties": map[string]string{"name": "no-type"},
			})

			var loadErr error
			if tc.branch {
				loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
			} else {
				loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			}
			if loadErr == nil {
				t.Fatal("expected loud failure for a file missing the required 'type' key")
			}
			if !errors.Is(loadErr, store.ErrInvalidEntityDir) {
				t.Fatalf("expected ErrInvalidEntityDir, got %v", loadErr)
			}
		})
	}
}

// A JSON edge file missing any of the required `type`/`from`/`to` keys must
// fail loudly on every load path (branch.go:1197-1200 main,
// branch.go:1362-1365 branch → ErrInvalidEdgeDir), even when the `id` key is
// present.
func TestRehydrateFiles_MissingEdgeKeysFailsLoudly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		branch bool
		file   map[string]any
	}{
		{"main missing type", false, map[string]any{
			"id": uuid.NewString(), "from": uuid.NewString(), "to": uuid.NewString(),
		}},
		{"main missing endpoints", false, map[string]any{
			"id": uuid.NewString(), "type": "DependsOn",
		}},
		{"branch missing type", true, map[string]any{
			"id": uuid.NewString(), "from": uuid.NewString(), "to": uuid.NewString(),
		}},
		{"branch missing endpoints", true, map[string]any{
			"id": uuid.NewString(), "type": "DependsOn",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := OpenInMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer closeStore(t, s)
			applyTestSchema(t, s)
			ctx := context.Background()

			const branch = "tx1"
			if tc.branch {
				if err := s.CreateBranchDB(ctx, branch); err != nil {
					t.Fatalf("CreateBranchDB: %v", err)
				}
				if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
					t.Fatalf("ReplicateSchemaToBranch: %v", err)
				}
			}

			root := t.TempDir()
			entitiesDir := filepath.Join(root, "entities")
			edgesDir := filepath.Join(root, "edges")
			writeJSONFile(t, filepath.Join(edgesDir, "DependsOn", "edge.json"), tc.file)

			var loadErr error
			if tc.branch {
				loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
			} else {
				loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			}
			if loadErr == nil {
				t.Fatal("expected loud failure for an edge file missing type/from/to keys")
			}
			if !errors.Is(loadErr, store.ErrInvalidEdgeDir) {
				t.Fatalf("expected ErrInvalidEdgeDir, got %v", loadErr)
			}
		})
	}
}

// An unparseable JSON element file must fail loudly on every load path
// (branch.go:1109-1112 and 1193-1196 → ErrInvalidEntityDir/ErrInvalidEdgeDir)
// — the file guards treat unparseable content as a corrupt element file, never
// skipping or silently accepting it.
func TestRehydrateFiles_UnparseableJSONFailsLoudly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		branch bool
		edge   bool
		want   error
	}{
		{"main entity", false, false, store.ErrInvalidEntityDir},
		{"main edge", false, true, store.ErrInvalidEdgeDir},
		{"branch entity", true, false, store.ErrInvalidEntityDir},
		{"branch edge", true, true, store.ErrInvalidEdgeDir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := OpenInMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer closeStore(t, s)
			applyTestSchema(t, s)
			ctx := context.Background()

			const branch = "tx1"
			if tc.branch {
				if err := s.CreateBranchDB(ctx, branch); err != nil {
					t.Fatalf("CreateBranchDB: %v", err)
				}
				if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
					t.Fatalf("ReplicateSchemaToBranch: %v", err)
				}
			}

			root := t.TempDir()
			entitiesDir := filepath.Join(root, "entities")
			edgesDir := filepath.Join(root, "edges")
			if tc.edge {
				path := filepath.Join(edgesDir, "DependsOn", "edge.json")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				path := filepath.Join(entitiesDir, "Component", "ent.json")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			var loadErr error
			if tc.branch {
				loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
			} else {
				loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			}
			if loadErr == nil {
				t.Fatal("expected loud failure for an unparseable JSON element file")
			}
			if !errors.Is(loadErr, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, loadErr)
			}
		})
	}
}

// A committed file under a type directory absent from the applied schema must
// be inferred from the directory structure (SPEC R8) on every load path
// (main/branch × entity/edge), never silently skipped: the applied schema and
// the git file-per-element representation can diverge (corrupt main.lbug
// recovery, lost schema metadata, partial wipe), and R8 re-hydration must
// "recover the full graph state". Regression: the loaders skipped any type
// directory absent from a non-empty applied schema
// (`if _, ok := defs[typeName]; !ok && len(defs) > 0 { continue }`), dropping
// committed rows with no error and no inference — directory inference only ran
// when the applied schema was entirely empty.
func TestRehydrateFiles_InferredTypeWithAppliedSchema(t *testing.T) {
	for _, tc := range []struct {
		name   string
		branch bool
		edge   bool
	}{
		{"main entity", false, false},
		{"main edge", false, true},
		{"branch entity", true, false},
		{"branch edge", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := OpenInMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer closeStore(t, s)
			// Non-empty applied schema (Component/VectorType/Document/DependsOn);
			// the loaded types below are absent from it and must be inferred.
			applyTestSchema(t, s)
			ctx := context.Background()

			const branch = "tx1"
			if tc.branch {
				if err := s.CreateBranchDB(ctx, branch); err != nil {
					t.Fatalf("CreateBranchDB: %v", err)
				}
				if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
					t.Fatalf("ReplicateSchemaToBranch: %v", err)
				}
			}

			root := t.TempDir()
			entitiesDir := filepath.Join(root, "entities")
			edgesDir := filepath.Join(root, "edges")
			fromID := uuid.NewString()
			toID := uuid.NewString()
			// Endpoint entities under a schema-known type so an inferred edge
			// type's files pass insertEdgeOnConn's endpoint-existence guard.
			writeJSONFile(t, filepath.Join(entitiesDir, "Component", fromID+".json"), map[string]any{
				"id": fromID, "type": "Component",
			})
			writeJSONFile(t, filepath.Join(entitiesDir, "Component", toID+".json"), map[string]any{
				"id": toID, "type": "Component",
			})
			var loadedType, elementID string
			if tc.edge {
				// Edge under a type dir absent from the applied schema.
				loadedType, elementID = "Links", uuid.NewString()
				writeJSONFile(t, filepath.Join(edgesDir, loadedType, elementID+".json"), map[string]any{
					"id": elementID, "type": loadedType, "from": fromID, "to": toID,
					"properties": map[string]string{"strength": strengthValue},
				})
			} else {
				// Entity under a type dir absent from the applied schema.
				loadedType, elementID = "Widget", uuid.NewString()
				writeJSONFile(t, filepath.Join(entitiesDir, loadedType, elementID+".json"), map[string]any{
					"id": elementID, "type": loadedType,
					"properties": map[string]string{"name": inferredValue},
				})
				// Empty edges dir so the main path's completeness check
				// (entities dir exists ⇒ edges dir must exist) passes.
				if err := os.MkdirAll(edgesDir, 0o755); err != nil {
					t.Fatal(err)
				}
			}

			var loadErr error
			if tc.branch {
				loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
			} else {
				loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			}
			if loadErr != nil {
				t.Fatalf("re-hydration with schema-absent type dir: %v", loadErr)
			}

			// The schema-absent type was inferred and its elements loaded —
			// nothing was silently dropped. ListEntities/ListEdgesOfType read the
			// branch-scoped type caches and reject unregistered types, proving
			// the inferred type was registered on the load path itself.
			if tc.branch {
				if tc.edge {
					if _, err := s.ListEdgesOfType(ctx, loadedType, branch); err != nil {
						t.Fatalf("inferred edge type %q not registered on branch: %v", loadedType, err)
					}
					edge, err := s.GetEdge(ctx, elementID, branch)
					if err != nil {
						t.Fatalf("inferred edge %q not loaded on branch: %v", elementID, err)
					}
					if edge.Properties["strength"] != strengthValue {
						t.Fatalf("inferred edge property strength = %q, want %q",
							edge.Properties["strength"], strengthValue)
					}
				} else {
					if _, _, err := s.ListEntities(ctx, loadedType, 10, "", branch); err != nil {
						t.Fatalf("inferred entity type %q not registered on branch: %v", loadedType, err)
					}
					ent, err := s.GetEntity(ctx, elementID, branch)
					if err != nil {
						t.Fatalf("inferred entity %q not loaded on branch: %v", elementID, err)
					}
					if ent.Properties["name"] != inferredValue {
						t.Fatalf("inferred entity property name = %q, want %q",
							ent.Properties["name"], inferredValue)
					}
				}
			} else {
				if tc.edge {
					if _, ok := s.EdgeType(loadedType); !ok {
						t.Fatalf("expected edge type %q to be inferred on main", loadedType)
					}
					edge, err := s.GetEdge(ctx, elementID, "main")
					if err != nil {
						t.Fatalf("inferred edge %q not loaded on main: %v", elementID, err)
					}
					if edge.Properties["strength"] != strengthValue {
						t.Fatalf("inferred edge property strength = %q, want %q",
							edge.Properties["strength"], strengthValue)
					}
				} else {
					if _, ok := s.EntityType(loadedType); !ok {
						t.Fatalf("expected entity type %q to be inferred on main", loadedType)
					}
					ent, err := s.GetEntity(ctx, elementID, "main")
					if err != nil {
						t.Fatalf("inferred entity %q not loaded on main: %v", elementID, err)
					}
					if ent.Properties["name"] != inferredValue {
						t.Fatalf("inferred entity property name = %q, want %q",
							ent.Properties["name"], inferredValue)
					}
				}
			}
		})
	}
}

// branchLocked and LoadBranchTransactionState build filesystem paths from txID;
// a non-UUID branch string containing path separators would escape branches/ on
// a file-backed store (path traversal on read). Every other branch-path builder
// (CreateBranchDB, DropBranchDB, SaveBranchTransactionState) enforces
// filepath.Base(txID) == txID; these two read paths must too — defense in depth,
// since a future caller could skip the service-layer UUID-v4 gate.
func TestBranchReadPaths_RejectPathTraversalTxID(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	db := s.(*ladybugDB)
	ctx := context.Background()

	// Plant files at the escaped paths the traversal would touch:
	// filepath.Join(dir, "branches", "../escaped.lbug") resolves to
	// dir/escaped.lbug, and the .state.json variant to dir/escaped.state.json.
	// They must never be opened/read.
	if err := os.WriteFile(filepath.Join(dir, "escaped.lbug"), []byte("not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "escaped.state.json"), []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}

	for _, txID := range []string{"../escaped", ".", ".."} {
		db.mu.Lock()
		_, err = db.branchLocked(txID)
		db.mu.Unlock()
		if err == nil || !strings.Contains(err.Error(), "invalid branch ID") {
			t.Fatalf("branchLocked(%q): expected invalid-branch-ID rejection, got %v", txID, err)
		}

		_, err = s.LoadBranchTransactionState(ctx, txID)
		if err == nil || !strings.Contains(err.Error(), "invalid branch ID") {
			t.Fatalf("LoadBranchTransactionState(%q): expected invalid-branch-ID rejection, got %v", txID, err)
		}
	}
}

// InvalidateBranchState removes filesystem paths built from txID via os.Remove;
// a non-UUID branch string containing path separators would escape branches/ on
// a file-backed store (path traversal on delete). Every sibling path builder
// (CreateBranchDB, DropBranchDB, SaveBranchTransactionState,
// LoadBranchTransactionState, branchLocked) enforces filepath.Base(txID) == txID;
// the destructive remove path must too — defense in depth, since a future caller
// could skip the service-layer UUID-v4 gate.
func TestInvalidateBranchState_RejectPathTraversalTxID(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	// Plant a file at the escaped path the traversal would delete:
	// filepath.Join(dir, "branches", "../escaped.state.json") resolves to
	// dir/escaped.state.json. It must never be removed.
	escapedPath := filepath.Join(dir, "escaped.state.json")
	if err := os.WriteFile(escapedPath, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}

	for _, txID := range []string{"../escaped", ".", ".."} {
		err := s.InvalidateBranchState(ctx, txID)
		if err == nil || !strings.Contains(err.Error(), "invalid branch ID") {
			t.Fatalf("InvalidateBranchState(%q): expected invalid-branch-ID rejection, got %v", txID, err)
		}
		if _, statErr := os.Stat(escapedPath); statErr != nil {
			t.Fatalf("InvalidateBranchState(%q): escaped file %q was removed", txID, escapedPath)
		}
	}

	// Happy path: a legitimately-named state file is removed and the in-memory
	// record invalidated.
	const txID = "legit-tx"
	if err := s.CreateBranchDB(ctx, txID); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.SaveBranchTransactionState(ctx, txID, store.BranchTransactionState{
		MainHeadAtLastSync: "head", SchemaHash: "schema",
	}); err != nil {
		t.Fatalf("SaveBranchTransactionState: %v", err)
	}
	statePath := filepath.Join(dir, "branches", txID+".state.json")
	if _, statErr := os.Stat(statePath); statErr != nil {
		t.Fatalf("state file %q was not written: %v", statePath, statErr)
	}
	if err := s.InvalidateBranchState(ctx, txID); err != nil {
		t.Fatalf("InvalidateBranchState(%q): %v", txID, err)
	}
	if _, statErr := os.Stat(statePath); !os.IsNotExist(statErr) {
		t.Fatalf("state file %q was not removed (stat err: %v)", statePath, statErr)
	}
	if _, err := s.LoadBranchTransactionState(ctx, txID); err == nil {
		t.Fatal("invalidated branch state was accepted")
	}
}

// Learnings rule "Sentinel errors over zero-value returns": a failed store must
// surface ErrDatabaseNotReady from ListMainEntityTypes rather than silently
// reporting an empty type list with a nil error.
func TestListMainEntityTypes_FailedStoreReturnsSentinel(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	db := s.(*ladybugDB)
	db.failed = true

	types, err := s.ListMainEntityTypes()
	if !errors.Is(err, store.ErrDatabaseNotReady) {
		t.Fatalf("expected ErrDatabaseNotReady for failed store, got %v", err)
	}
	if types != nil {
		t.Fatalf("expected nil types for failed store, got %v", types)
	}
}

// SPEC R1 membership-OR rule composition (crud.go validateEdgeRulesFor): an
// edge is permitted when ANY rule entry authorizes it — a second rule can
// authorize a connection the first denies — and within a rule, canConnectTo and
// using are ANDed. Pins the two previously-untested branches: the
// OR-across-entries authorization, and the deny-by-using-mismatch (target type
// present in a rule's canConnectTo but the edge type absent from that rule's
// using).
func TestCreateEdge_RuleComposition(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Service",
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}},
					{CanConnectTo: []string{"Document"}, Using: []string{"LINKS_TO"}},
				},
			},
			{Name: "Component"},
			{Name: "Document"},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DEPENDS_ON"},
			{Name: "LINKS_TO"},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	svc, err := s.CreateEntity(ctx, "Service", "", nil, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity Service: %v", err)
	}
	comp, err := s.CreateEntity(ctx, "Component", "", nil, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity Component: %v", err)
	}
	doc, err := s.CreateEntity(ctx, "Document", "", nil, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity Document: %v", err)
	}

	// Rule 1 authorizes Service → Component via DEPENDS_ON.
	if _, err := s.CreateEdge(ctx, "DEPENDS_ON", svc.Id, comp.Id, nil, "main"); err != nil {
		t.Fatalf("rule 1 should authorize Service→Component via DEPENDS_ON: %v", err)
	}

	// OR across rule entries: rule 2 authorizes Service → Document via LINKS_TO
	// even though rule 1 (which only names Component) denies it.
	if _, err := s.CreateEdge(ctx, "LINKS_TO", svc.Id, doc.Id, nil, "main"); err != nil {
		t.Fatalf("rule 2 should authorize Service→Document via LINKS_TO: %v", err)
	}

	// Deny by using-mismatch: Document appears in rule 2's canConnectTo, but
	// DEPENDS_ON is absent from rule 2's using (and Document is absent from
	// rule 1's canConnectTo), so the connection must be denied.
	_, err = s.CreateEdge(ctx, "DEPENDS_ON", svc.Id, doc.Id, nil, "main")
	if !errors.Is(err, store.ErrEdgeRuleViolation) {
		t.Fatalf("expected ErrEdgeRuleViolation for using-mismatch, got %v", err)
	}
}

// SPEC R1:133 — "Only the source entity type's rules are evaluated — the
// target entity type's rules play no role in edge authorization." Source
// permits Source→Target via LINKS; Target's own rules authorize only
// Target→Source via a different edge type (REVERSES). If the target's rules
// were consulted for the Source→Target LINKS edge, the connection would be
// denied — it must succeed, proving the target's rules are never evaluated.
// (Target's rules must use a different edge type than Source's so the LINKS
// rel table has a single FROM label — LadybugDB rejects a rel table whose
// endpoint clauses bind multiple node labels.)
func TestCreateEdge_TargetRulesNotEvaluated(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Source",
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Target"}, Using: []string{"LINKS"}},
				},
			},
			{
				Name: "Target",
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Source"}, Using: []string{"REVERSES"}},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "LINKS"},
			{Name: "REVERSES"},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	src, err := s.CreateEntity(ctx, "Source", "", nil, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity Source: %v", err)
	}
	tgt, err := s.CreateEntity(ctx, "Target", "", nil, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity Target: %v", err)
	}

	// The source's rules authorize the connection; the target's rules (which
	// never name LINKS) must not be consulted for an edge into Target.
	if _, err := s.CreateEdge(ctx, "LINKS", src.Id, tgt.Id, nil, "main"); err != nil {
		t.Fatalf("edge authorized by source rules must succeed regardless of target rules, got %v", err)
	}

	// Directionality proof: the target's rules DO govern edges originating
	// from Target — a LINKS edge from Target is denied even though LINKS is a
	// declared edge type — while the same rules play no role for edges into
	// Target (asserted above).
	if _, err := s.CreateEdge(ctx, "LINKS", tgt.Id, src.Id, nil, "main"); !errors.Is(err, store.ErrEdgeRuleViolation) {
		t.Fatalf("expected ErrEdgeRuleViolation for LINKS from Target (its own rules govern its outgoing edges), got %v", err)
	}
}

// SPEC R2: "after bootstrap, entities created without an embedding store NULL
// in the vector column". The pre-bootstrap ErrVectorBootstrap rejection
// (TestEmbeddingBootstrap_FirstEntityNoEmbedding) applies only until the first
// embedding establishes the dimension.
func TestCreateEntity_PostBootstrapNilEmbeddingStoresNULL(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	// Bootstrap VectorType to dimension 3.
	first, err := s.CreateEntity(ctx, "VectorType", "",
		map[string]string{"name": "v1"}, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("bootstrap CreateEntity: %v", err)
	}
	if len(first.Embedding) != 3 {
		t.Fatalf("expected bootstrapped embedding persisted, got %v", first.Embedding)
	}

	// A nil-embedding create after bootstrap succeeds and stores NULL: the
	// returned entity's Embedding is nil, and GetEntity returns nil too.
	plain, err := s.CreateEntity(ctx, "VectorType", "",
		map[string]string{"name": "v2"}, nil, "")
	if err != nil {
		t.Fatalf("post-bootstrap nil-embedding create must succeed, got %v", err)
	}
	if plain.Embedding != nil {
		t.Fatalf("expected nil Embedding on returned post-bootstrap entity, got %v", plain.Embedding)
	}
	got, err := s.GetEntity(ctx, plain.Id, "")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if got.Embedding != nil {
		t.Fatalf("expected NULL embedding stored for post-bootstrap entity, got %v", got.Embedding)
	}
}

// SPEC R7 (SPEC:442-443,480-481): a non-indexed entity type accepts an
// embedding of any dimension but does not persist or index it — the returned
// entity's Embedding and a subsequent GetEntity's embedding are both nil
// (accept-and-discard).
func TestCreateEntity_NonIndexedTypeDiscardsEmbedding(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	// Document is not vector-indexed in testSchema.
	e, err := s.CreateEntity(ctx, "Document", "",
		map[string]string{"title": "doc"}, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("non-indexed type must accept an embedding: %v", err)
	}
	if e.Embedding != nil {
		t.Fatalf("expected nil Embedding on returned entity for non-indexed type, got %v", e.Embedding)
	}
	got, err := s.GetEntity(ctx, e.Id, "")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if got.Embedding != nil {
		t.Fatalf("expected non-indexed type to discard the embedding, got %v", got.Embedding)
	}
}

// SPEC:345-346: before the first ApplySchema (or on a graph with no
// string-property types), a type-omitted (entityType == "") FullTextSearch is a
// non-type-referencing method and must succeed on an empty/fresh graph — the
// store's wildcard branch (query.go:297-301) must return an empty result set
// with a nil error, mirroring SearchNeighbors' empty-graph behavior.
func TestFullTextSearch_WildcardEmptyGraph_Succeeds(t *testing.T) {
	t.Run("no schema applied", func(t *testing.T) {
		s, err := OpenInMemory()
		if err != nil {
			t.Fatal(err)
		}
		defer closeStore(t, s)
		ctx := context.Background()

		results, err := s.FullTextSearch(ctx, "anything", "", "")
		if err != nil {
			t.Fatalf("wildcard FullTextSearch before ApplySchema should succeed, got %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected no results on an empty graph, got %d", len(results))
		}
	})

	t.Run("schema with no string-property types", func(t *testing.T) {
		s, err := OpenInMemory()
		if err != nil {
			t.Fatal(err)
		}
		defer closeStore(t, s)
		ctx := context.Background()

		// A property-less entity type creates a table with only the id column:
		// no string properties → no FTS index → the type is legitimately
		// unsearchable and is silently skipped, leaving an empty result set with
		// a nil error.
		if err := s.ApplySchema(ctx, &flowv1.Schema{
			EntityTypes: []*flowv1.EntityType{{Name: "Empty"}},
		}); err != nil {
			t.Fatalf("ApplySchema: %v", err)
		}

		results, err := s.FullTextSearch(ctx, "anything", "", "")
		if err != nil {
			t.Fatalf("wildcard FullTextSearch on a schema without string-property types should succeed, got %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected no results, got %d", len(results))
		}
	})
}
