package ladybug

import (
	"context"
	"errors"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

func TestApplySchema_AdditiveEntityProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
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

// TestApplySchema_AdditiveRequiredEntityProperty_ForwardOnly pins the SPEC R6
// forward-only required-property branch for a newly-added property with
// `required: true` (SPEC:410-413): CreateEntity rejects new entities missing the
// property, but a pre-existing entity created before the property was added is
// not retroactively invalidated — it stays readable, and UpdateEntity does not
// require the property either.
func TestApplySchema_AdditiveRequiredEntityProperty_ForwardOnly(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	// Initial schema with one non-required property.
	schema1 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{{
			Name:       "Document",
			Properties: []*flowv1.Property{{Name: "title", Type: "string"}},
		}},
	}
	if err := s.ApplySchema(ctx, schema1); err != nil {
		t.Fatalf("first ApplySchema: %v", err)
	}

	// Create an entity before the required property exists.
	doc, err := s.CreateEntity(ctx, "Document", "", map[string]string{"title": "draft"}, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	// Additive: add a NEW required property.
	schema2 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{{
			Name: "Document",
			Properties: []*flowv1.Property{
				{Name: "title", Type: "string"},
				{Name: "author", Type: "string", Required: true},
			},
		}},
	}
	if err := s.ApplySchema(ctx, schema2); err != nil {
		t.Fatalf("additive ApplySchema with a required property: %v", err)
	}

	// The pre-existing entity lacks the newly-required property but must NOT be
	// retroactively invalidated: it stays readable.
	got, err := s.GetEntity(ctx, doc.Id, "main")
	if err != nil {
		t.Fatalf("GetEntity after adding a required property must not fail: %v", err)
	}
	if got.Properties["title"] != "draft" {
		t.Fatalf("expected title=draft, got %v", got.Properties)
	}

	// UpdateEntity does not require the newly-added property either (SPEC:413).
	updated, err := s.UpdateEntity(ctx, doc.Id, map[string]string{"title": "draft-v2"}, nil, "main")
	if err != nil {
		t.Fatalf("UpdateEntity omitting the newly-required property must succeed: %v", err)
	}
	if updated.Properties["title"] != "draft-v2" {
		t.Fatalf("expected title=draft-v2, got %v", updated.Properties)
	}

	// Forward-only enforcement: a NEW entity still missing the required property
	// is rejected.
	if _, err := s.CreateEntity(ctx, "Document", "",
		map[string]string{"title": "doc2"}, nil, "main"); !errors.Is(err, store.ErrMissingRequiredProperty) {
		t.Fatalf("expected ErrMissingRequiredProperty for a new entity lacking the required property, got %v", err)
	}

	// ...and one carrying it succeeds.
	doc2, err := s.CreateEntity(ctx, "Document", "", map[string]string{"title": "doc3", "author": "me"}, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity with the newly-required property: %v", err)
	}
	if doc2.Properties["author"] != "me" {
		t.Fatalf("expected author=me, got %v", doc2.Properties)
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
	opened, err := openInMemory()
	if err != nil {
		t.Fatalf("openInMemory: %v", err)
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

func TestApplySchema_AdditiveEdgeProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
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
