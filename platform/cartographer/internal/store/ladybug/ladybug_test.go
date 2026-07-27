package ladybug

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

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

func TestSearchNeighbors_EmptyEmbedding(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.SearchNeighbors(context.Background(), nil, "VectorType", 10, "")
	if err == nil {
		t.Fatal("expected error for empty embedding")
	}
	if !errors.Is(err, store.ErrEmbeddingRequired) {
		t.Errorf("expected ErrEmbeddingRequired, got %v", err)
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

// ---------------------------------------------------------------------------
// Embedding bootstrap
// ---------------------------------------------------------------------------

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

	if err := s.CreateBranchDB("tx1"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.DropBranchDB("tx1"); err != nil {
		t.Fatalf("DropBranchDB: %v", err)
	}
	// Idempotent drop should not error.
	if err := s.DropBranchDB("tx1"); err != nil {
		t.Errorf("expected idempotent drop to succeed: %v", err)
	}
}

func TestBranch_IsolatedWrites(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	if err := s.CreateBranchDB("tx1"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch("tx1"); err != nil {
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

	if err := s.CreateBranchDB("tx1"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch("tx1"); err != nil {
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
