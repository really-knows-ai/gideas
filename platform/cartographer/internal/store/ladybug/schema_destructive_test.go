package ladybug

import (
	"context"
	"errors"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

func TestApplySchema_DestructiveChange_Rejected(t *testing.T) {
	s, err := openInMemory()
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
	s, err := openInMemory()
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
	s, err := openInMemory()
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
	s, err := openInMemory()
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
	db.entityTypeDefs["Document"].Properties[1].Type = driftedColumnType
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
	s, err := openInMemory()
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
	db.edgeTypeDefs["DEPENDS_ON"].Properties[0].Type = driftedColumnType
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
	s, err := openInMemory()
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
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
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
	assertVectorIndexState(t, s, "Document", "main", false,
		"vector index must not be bootstrapped while EnableVectorIndex is false")

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
	assertVectorIndexState(t, s, "Document", "main", false,
		"the false→true transition must stay lazy — no entity written yet")

	// A first embedding write now bootstraps the dimension (SPEC R7 lazy).
	if _, err := s.CreateEntity(ctx, "Document", "",
		map[string]string{"title": "vec"}, []float32{1, 2, 3}, "main"); err != nil {
		t.Fatalf("CreateEntity with embedding after enable: %v", err)
	}
	assertVectorIndexState(t, s, "Document", "main", true,
		"expected vector index bootstrapped after first embedding write")
	if dim, derr := s.GetEstablishedDimension(context.Background(), "Document", "main"); derr != nil || dim != 3 {
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
	assertVectorIndexState(t, s2, "Document", "main", true, "lazy vector index was not restored on reopen")
	if dim, derr := s2.GetEstablishedDimension(context.Background(), "Document", "main"); derr != nil || dim != 3 {
		t.Fatalf("restored dimension = %d, error = %v, want 3", dim, derr)
	}
}
