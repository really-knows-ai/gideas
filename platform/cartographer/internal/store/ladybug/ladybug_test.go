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
	"github.com/foundry/flow/cartographer/internal/gitstore"
	schemavalidator "github.com/foundry/flow/cartographer/internal/schema"
	"github.com/foundry/flow/cartographer/internal/schemaerrors"
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

// driftedColumnType is a non-"string" type used to simulate a catalog column
// (or persisted schema metadata) whose type no longer matches the declared
// "string": the ApplySchema catalog diff must reject it (SPEC error-table row
// "Table structure mismatch") and reopen must fail closed on corrupted
// metadata.
const driftedColumnType = "INT64"

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
	if dimension, err := s.GetEstablishedDimension(
		context.Background(), "Component", "main",
	); err != nil || dimension != 2 {
		t.Fatalf("vector dimension changed: dimension=%d error=%v", dimension, err)
	}
	if ok, err := s.IsVectorIndexBootstrapped(context.Background(), "Component", "main"); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if !ok {
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
			} else if !errors.Is(err, schemaerrors.ErrReservedWord) {
				t.Fatalf("expected schemaerrors.ErrReservedWord, got: %v", err)
			}
		})
	}
}

// A nil schema (an ApplySchemaRequest whose schema field is unset, forwarded
// by the service handler unguarded) must be rejected with the schemaerrors
// package's ErrNilSchema sentinel (→ INVALID_ARGUMENT at the gRPC boundary via
// mapStoreError/isSchemaError), never applied: after schema.Validate passes,
// the catalog diff (diffSchemaAgainstCatalog → collectFromToPairs) reads
// s.EntityTypes on the nil pointer and would panic — surfacing as gRPC
// INTERNAL instead of the correct INVALID_ARGUMENT.
func TestApplySchema_NilSchema_Rejected(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	err = s.ApplySchema(context.Background(), nil)
	if err == nil {
		t.Fatal("expected ApplySchema to reject a nil schema, got nil")
	} else if !errors.Is(err, schemaerrors.ErrNilSchema) {
		t.Fatalf("expected schemaerrors.ErrNilSchema, got: %v", err)
	}
}

// TestUntypedPlaceholder_Lifecycle pins the SPEC R1 `_untyped` reserved-name
// contract's happy-path lifecycle at the store layer (the name-rejection path
// is pinned by TestApplySchema_RejectsUntypedPlaceholderName): an edgeless
// edge type (no rule declares FROM/TO endpoint pairs for it) falls back to a
// placeholder `_untyped` NODE table in createRelTableOnConn; after a
// file-backed reopen the placeholder is legitimately present in the catalog but
// absent from schema metadata, so validateMetadataAgainstCatalog's skip lets
// the reopen succeed and the metadata-derived schema cache excludes it from
// EntityTypeNames/TableExists/ListMainEntityTypes; and WipeSchema enumerates
// every catalog table and drops the placeholder with the rest.
func TestUntypedPlaceholder_Lifecycle(t *testing.T) {
	t.Run("edgeless edge type creates the placeholder node table", func(t *testing.T) {
		s, err := OpenInMemory()
		if err != nil {
			t.Fatal(err)
		}
		defer closeStore(t, s)

		// An edge type no rule references is edgeless: collectFromToPairs yields
		// no FROM/TO endpoint pair, so createRelTableOnConn creates the
		// placeholder `_untyped` NODE table for the rel table's endpoint clauses
		// (SPEC R1 "Reserved internal name").
		if err := s.ApplySchema(context.Background(), &flowv1.Schema{
			EdgeTypes: []*flowv1.EdgeType{{Name: "REFERENCES"}},
		}); err != nil {
			t.Fatalf("ApplySchema: %v", err)
		}
		if kind := tableKindOnConn(t, s.(*ladybugDB).conn, untypedTableName); strings.ToUpper(kind) != tableTypeNode {
			t.Fatalf("expected placeholder %s NODE table, got kind %q", untypedTableName, kind)
		}
		// The placeholder is internal: the metadata-derived schema cache never
		// exposes it as an entity type.
		if s.TableExists(untypedTableName) {
			t.Fatalf("TableExists(%q) exposed the internal placeholder as an entity type", untypedTableName)
		}
	})

	t.Run("excluded from entity type listings after a file-backed reopen", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ApplySchema(context.Background(), &flowv1.Schema{
			EntityTypes: []*flowv1.EntityType{{
				Name:       "Document",
				Properties: []*flowv1.Property{{Name: "title", Type: "string"}},
			}},
			EdgeTypes: []*flowv1.EdgeType{{Name: "REFERENCES"}},
		}); err != nil {
			t.Fatalf("ApplySchema: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		// Reopen rebuilds the catalog cache from show_tables (which carries the
		// placeholder) and then restores the cache from schema metadata;
		// validateMetadataAgainstCatalog's skip of the placeholder is what lets
		// this reopen succeed instead of bricking on "database entity type
		// _untyped is absent from schema metadata".
		reopened, err := Open(dir)
		if err != nil {
			t.Fatalf("reopen after edgeless edge type: %v", err)
		}
		defer closeStore(t, reopened)

		// The placeholder table physically survives the reopen...
		if kind := tableKindOnConn(t, reopened.(*ladybugDB).conn, untypedTableName); strings.ToUpper(kind) != tableTypeNode {
			t.Fatalf("placeholder %s table missing after reopen, got kind %q", untypedTableName, kind)
		}
		// ...but the metadata-derived schema cache excludes it from every
		// entity-type listing surface.
		if got := reopened.EntityTypeNames(); slices.Contains(got, untypedTableName) {
			t.Errorf("EntityTypeNames exposed placeholder %q: %v", untypedTableName, got)
		}
		if reopened.TableExists(untypedTableName) {
			t.Errorf("TableExists(%q) reported the placeholder as an entity type", untypedTableName)
		}
		types, err := reopened.ListMainEntityTypes()
		if err != nil {
			t.Fatalf("ListMainEntityTypes: %v", err)
		}
		if slices.Contains(types, untypedTableName) {
			t.Errorf("ListMainEntityTypes exposed placeholder %q: %v", untypedTableName, types)
		}
		// The user entity type and the edgeless edge type survive the reopen.
		if !slices.Contains(reopened.EntityTypeNames(), "Document") {
			t.Errorf("Document entity type missing after reopen: %v", reopened.EntityTypeNames())
		}
		if !slices.Contains(reopened.EdgeTypeNames(), "REFERENCES") {
			t.Errorf("REFERENCES edge type missing after reopen: %v", reopened.EdgeTypeNames())
		}
	})

	t.Run("removed by WipeSchema", func(t *testing.T) {
		s, err := OpenInMemory()
		if err != nil {
			t.Fatal(err)
		}
		defer closeStore(t, s)
		if err := s.ApplySchema(context.Background(), &flowv1.Schema{
			EdgeTypes: []*flowv1.EdgeType{{Name: "REFERENCES"}},
		}); err != nil {
			t.Fatalf("ApplySchema: %v", err)
		}
		db := s.(*ladybugDB)
		if kind := tableKindOnConn(t, db.conn, untypedTableName); strings.ToUpper(kind) != tableTypeNode {
			t.Fatalf("expected placeholder %s NODE table before wipe, got kind %q", untypedTableName, kind)
		}
		if err := s.WipeSchema(context.Background()); err != nil {
			t.Fatalf("WipeSchema: %v", err)
		}
		// WipeSchema enumerates every table from show_tables — the placeholder
		// is a NODE table like any other and is dropped with the rest.
		if kind := tableKindOnConn(t, db.conn, untypedTableName); kind != "" {
			t.Fatalf("placeholder %s table survived WipeSchema (kind %q)", untypedTableName, kind)
		}
		if kind := tableKindOnConn(t, db.conn, "REFERENCES"); kind != "" {
			t.Fatalf("edgeless edge table REFERENCES survived WipeSchema (kind %q)", kind)
		}
	})
}

// tableKindOnConn returns the catalog kind (NODE/REL) of the named table on
// the given connection, or "" when the table does not exist.
func tableKindOnConn(t *testing.T, conn *lbug.Connection, name string) string {
	t.Helper()
	result, err := conn.Query("CALL show_tables() RETURN *;")
	if err != nil {
		t.Fatalf("show_tables: %v", err)
	}
	defer result.Close()
	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			t.Fatalf("next table row: %v", err)
		}
		values, err := tuple.GetAsSlice()
		tuple.Close()
		if err != nil {
			t.Fatalf("table row values: %v", err)
		}
		if len(values) >= 3 && fmt.Sprintf("%v", values[1]) == name {
			return fmt.Sprintf("%v", values[2])
		}
	}
	return ""
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

func TestCreateEntity_NonCanonicalUUIDSpellingRejected(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	id := uuid.New().String()
	if _, err := s.CreateEntity(context.Background(), "Component", id, nil, nil, ""); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	// The same UUID in a non-canonical spelling must be rejected at the ID
	// format gate, never stored as a second entity — accepting it would let
	// two spellings of one UUID become two entities and bypass the
	// ALREADY_EXISTS check (SPEC:942). The rejection surfaces on the
	// INVALID_ARGUMENT-style store.ErrInvalidIDFormat path (SPEC:941).
	_, err = s.CreateEntity(context.Background(), "Component", strings.ToUpper(id), nil, nil, "")
	if err == nil {
		t.Fatal("expected error for non-canonical UUID spelling")
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

// A genuine DB failure from the source/target existence probe must propagate as
// an operational error, not be masked as ErrSourceOrTargetNotFound (a transient
// DB failure must never surface to the client as "source entity not found" /
// NOT_FOUND). Regression: CreateEdge wrapped every findEntityByID error —
// including Prepare/Execute failures — in ErrSourceOrTargetNotFound.
func TestCreateEdge_PropagatesProbeOperationalError(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	db := s.(*ladybugDB)
	db.mu.Lock()
	// A phantom entity-type def with no backing table: findEntityByID's Prepare
	// fails with an operational error. Replacing the defs (rather than adding to
	// them) makes the failure deterministic regardless of map iteration order —
	// the probe can never succeed against a real type.
	db.entityTypeDefs = map[string]*store.EntityTypeDef{
		"NonExistentTable": {Name: "NonExistentTable"},
	}
	db.mu.Unlock()

	_, err = s.CreateEdge(ctx, "DependsOn", uuid.NewString(), uuid.NewString(), nil, "")
	if err == nil {
		t.Fatal("expected an operational error from the phantom-type probe")
	}
	if errors.Is(err, store.ErrSourceOrTargetNotFound) {
		t.Fatalf("expected the probe's operational error to propagate, not ErrSourceOrTargetNotFound: %v", err)
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

// TestExecuteCypher_MutationClausesClassified asserts the SPEC
// syntax-before-read-only check order (SPEC:1015) for the mutation/DDL clause
// set that R7 §5 and the error table enumerate (CREATE, SET, DELETE, MERGE,
// REMOVE, DROP, DDL index/constraint, FOREACH-as-mutation, and CALL with
// mutating procedures):
//
//   - Clauses the LadybugDB v0.17.0 grammar parses (CREATE, SET, DELETE,
//     MERGE, DROP) are classified non-read-only by the IsReadOnly guard and
//     surface ErrMutationCypher (mapped to PERMISSION_DENIED, error-table row
//     "ExecuteCypher with mutation statement", SPEC:976) — never executed as
//     read-only.
//   - Clauses the grammar cannot parse (top-level FOREACH, `MATCH ... REMOVE
//     ...`, index/constraint DDL) fail at Prepare *before* the IsReadOnly guard
//     runs. Per SPEC R3 "a statement that fails to parse is rejected with
//     INVALID_ARGUMENT — this comes from Prepare, unchanged" (SPEC:260), the
//     "Invalid Cypher syntax" row (SPEC:979), and the R7 §5 grammar-gap note
//     (SPEC:493-497, grammar-unparseable clauses surface INVALID_ARGUMENT
//     "never as PERMISSION_DENIED"), they surface ErrInvalidCypher
//     (INVALID_ARGUMENT) — the syntax gate precedes read-only enforcement, so a
//     statement that fails to parse is INVALID_ARGUMENT regardless of mutation
//     keywords in its text.
//
// A mutating CALL follows the same grammar-parse rule (SPEC R7 §5 "CALL with
// mutating procedures"): `CALL delete.mutations() RETURN *;` carries an
// enumerated mutation keyword in its dotted procedure name, but the dotted
// name is as grammar-unparseable as `load.csv` on v0.17.0, so it fails at
// Prepare and surfaces ErrInvalidCypher — as do the index-DDL procedures
// (CREATE/DROP_VECTOR_INDEX, CREATE/DROP_FTS_INDEX). A mutation/DDL statement
// the grammar cannot parse is never executed as read-only, but the syntax
// error surfaces before read-only enforcement ever runs; this is pinned in the
// prepare-fail loop and the "mutating CALLs grammar cannot parse" subtest
// below.
func TestExecuteCypher_MutationClausesClassified(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	mutationCases := []struct {
		name   string
		cypher string
	}{
		{"create", "CREATE (n:Component {id: 'bad-uuid'})"},
		{"set", "MATCH (n:Component) SET n.name = 'x'"},
		{"delete", "MATCH (n:Component) DELETE n"},
		{"merge", "MERGE (n:Component {id: 'bad-uuid'})"},
		{"drop", "DROP TABLE Component"},
	}
	for _, tc := range mutationCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ExecuteCypher(context.Background(), tc.cypher, nil, "")
			if !errors.Is(err, store.ErrMutationCypher) {
				t.Errorf("expected ErrMutationCypher for %q, got %v", tc.cypher, err)
			}
		})
	}

	// Grammar-gap mutations fail at Prepare, so the syntax gate surfaces
	// ErrInvalidCypher (SPEC R3 / SPEC:260; row "Invalid Cypher syntax",
	// SPEC:979; R7 §5 note SPEC:493-497).
	prepareFailCases := []struct {
		name   string
		cypher string
	}{
		{"create-drop-entity", "CREATE (n:Component {id: 'bad-uuid'}) DROP n"},
		{"remove", "MATCH (n:Component) REMOVE n.name"},
		{"ddl-index", "CREATE INDEX Component_name IF NOT EXISTS FOR (n:Component) ON (n.name)"},
		{"ddl-constraint", "CREATE CONSTRAINT IF NOT EXISTS FOR (n:Component) REQUIRE n.id IS UNIQUE"},
		{"foreach-as-mutation", "FOREACH (x IN ['aaa'] | CREATE (n:Component {id: x}))"},
		// A mutating-procedure CALL carrying a bare enumerated mutation keyword
		// in its procedure name (SPEC R7 §5: "CALL with mutating procedures")
		// is as grammar-unparseable as `load.csv` — the dotted name fails at
		// Prepare, so the syntax gate surfaces ErrInvalidCypher (SPEC:493-497):
		// the mutation keyword in its text does not override the
		// syntax-before-read-only order (SPEC:1015).
		{"call-mutating-procedure-keyword", "CALL delete.mutations() RETURN *;"},
	}
	for _, tc := range prepareFailCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ExecuteCypher(context.Background(), tc.cypher, nil, "")
			if !errors.Is(err, store.ErrInvalidCypher) {
				t.Errorf("expected ErrInvalidCypher for %q, got %v", tc.cypher, err)
			}
		})
	}

	// The rest of the "CALL with mutating procedures" clause class (SPEC:486)
	// is grammar-unparseable: the LadybugDB index-DDL procedures
	// (CREATE/DROP_VECTOR_INDEX, CREATE/DROP_FTS_INDEX) and the
	// LOAD-CSV-style procedure `load.csv` fail at Prepare, and their mutation
	// keywords are hidden behind non-word characters (CREATE_VECTOR_INDEX,
	// load.csv). They are still rejected — never executed as read-only — but
	// as ErrInvalidCypher (INVALID_ARGUMENT), never ErrMutationCypher
	// (PERMISSION_DENIED), following the SPEC's LOAD-CSV note (SPEC:493-497): a
	// statement the v0.17.0 grammar cannot parse surfaces the syntax error,
	// never PERMISSION_DENIED.
	t.Run("mutating-call-procedures-grammar-cannot-parse-invalid-argument", func(t *testing.T) {
		cases := []string{
			"CALL CREATE_VECTOR_INDEX('VectorType', 'VectorType_vec', 'embedding', metric := 'cosine');",
			"CALL DROP_VECTOR_INDEX('VectorType', 'VectorType_vec');",
			"CALL CREATE_FTS_INDEX('Document', 'Document_fts', ['title']);",
			"CALL DROP_FTS_INDEX('Document', 'Document_fts');",
			"CALL load.csv('file:///tmp/rows.csv') RETURN row;",
		}
		for _, cypher := range cases {
			_, err := s.ExecuteCypher(context.Background(), cypher, nil, "")
			if errors.Is(err, store.ErrMutationCypher) {
				t.Errorf("a mutating CALL the grammar cannot parse must surface INVALID_ARGUMENT, "+
					"never PERMISSION_DENIED (LOAD-CSV note, SPEC:493-497): %q got %v", cypher, err)
			}
			if !errors.Is(err, store.ErrInvalidCypher) {
				t.Errorf("expected ErrInvalidCypher for %q, got %v", cypher, err)
			}
		}
	})
}

// TestExecuteCypher_ReadOnlyClausesClassified asserts that each read-only
// clause form the SPEC R7 §5 read-only clause set enumerates (SPEC:480-481 —
// WITH, UNWIND, LOAD CSV, CALL with read-only procedures, alongside the
// MATCH/RETURN forms the historical tests pinned) passes the stmt.IsReadOnly()
// guard and executes through the real ExecuteCypher store path. The existing
// read-only success tests cover only MATCH ... RETURN forms; this test pins
// the rest of the SPEC-enumerated clause set.
//
// WITH, UNWIND, and read-only CALL clauses (show_tables, table_info) prepare
// and execute end-to-end on LadybugDB v0.17.0. LOAD CSV is the one clause in
// the SPEC set the v0.17.0 grammar does not parse: `LOAD CSV FROM ...` fails
// at Prepare with a parser exception, so it cannot be executed end-to-end.
// What is pinned for it is the classification — a read-only clause must never
// be rejected as a mutation, so LOAD CSV surfaces ErrInvalidCypher
// (INVALID_ARGUMENT), never ErrMutationCypher (PERMISSION_DENIED).
func TestExecuteCypher_ReadOnlyClausesClassified(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	// A Component entity gives WITH/MATCH clauses a row to project.
	if _, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "cypher-test"}, nil, ""); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	t.Run("executable read-only clauses", func(t *testing.T) {
		cases := []struct {
			name     string
			cypher   string
			wantRows int
			// wantValue, when non-empty, is the expected first column of the
			// first row (a deterministic projection).
			wantValue string
		}{
			{"with", "MATCH (n:Component) WITH n.name AS name RETURN name", 1, "cypher-test"},
			{"unwind", "UNWIND [1, 2, 3] AS x RETURN x", 3, ""},
			{"call-show-tables", "CALL show_tables() RETURN *", 0, ""},
			{"call-table-info", "CALL table_info('Component') RETURN *", 0, ""},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				rows, err := s.ExecuteCypher(context.Background(), tc.cypher, nil, "")
				if err != nil {
					t.Fatalf("ExecuteCypher(%q): %v", tc.cypher, err)
				}
				// Catalog calls (show_tables/table_info) have engine-defined row
				// counts; pin only that they execute and return rows. The
				// projection cases pin exact counts and the projected value.
				if tc.wantRows > 0 {
					if len(rows) != tc.wantRows {
						t.Fatalf("expected %d rows, got %d", tc.wantRows, len(rows))
					}
				} else if len(rows) == 0 {
					t.Fatalf("expected at least 1 row, got 0")
				}
				if tc.wantValue != "" {
					if len(rows[0].Values) == 0 {
						t.Fatalf("expected a value in row 0, got %v", rows[0].Values)
					}
					if got := fmt.Sprintf("%v", rows[0].Values[0]); got != tc.wantValue {
						t.Errorf("row 0 value = %q, want %q", got, tc.wantValue)
					}
				}
			})
		}
	})

	t.Run("load-csv-classified-read-only", func(t *testing.T) {
		// The SPEC R7 §5 read-only clause set lists LOAD CSV (SPEC:481), but
		// LadybugDB v0.17.0's grammar does not parse Neo4j's LOAD CSV clause
		// (`LOAD CSV FROM ...` fails at Prepare with "Parser exception"). The
		// store cannot execute it end-to-end, so the pinnable property is the
		// classification: LOAD CSV is a read-only clause and must never be
		// rejected as a mutation. It surfaces ErrInvalidCypher (INVALID_ARGUMENT)
		// via the Prepare failure — the v0.17.0 grammar limitation — not
		// ErrMutationCypher (PERMISSION_DENIED).
		_, err := s.ExecuteCypher(context.Background(),
			"LOAD CSV FROM 'file:///tmp/rows.csv' AS row RETURN row", nil, "")
		if errors.Is(err, store.ErrMutationCypher) {
			t.Fatalf("LOAD CSV is a read-only clause per SPEC R7 §5 and must not be classified as mutation, got %v", err)
		}
		if !errors.Is(err, store.ErrInvalidCypher) {
			t.Fatalf("LOAD CSV must surface ErrInvalidCypher (v0.17.0 grammar cannot parse it), got %v", err)
		}
	})
}

// TestExecuteCypher_StringLiteralKeywordNotMutation pins the SPEC check-order
// consequence that a statement failing at Prepare surfaces ErrInvalidCypher
// (INVALID_ARGUMENT) regardless of mutation keywords in its text: the syntax
// gate precedes read-only enforcement (SPEC:1015), the "Invalid Cypher syntax"
// row is INVALID_ARGUMENT (SPEC:979), and SPEC R3 mandates INVALID_ARGUMENT for
// every statement that fails to parse (SPEC:260). A malformed read-only
// statement that happens to quote a mutation keyword inside a string literal or
// comment (e.g. `MATCH (n:Component) RETURN n 'delete'`) therefore keeps
// INVALID_ARGUMENT, never PERMISSION_DENIED.
func TestExecuteCypher_StringLiteralKeywordNotMutation(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	// The trailing 'delete' is a string literal in a statement the grammar
	// rejects at Prepare. Classifying it as a mutation would flip the SPEC's
	// syntax-before-read-only ordering into PERMISSION_DENIED.
	_, err = s.ExecuteCypher(context.Background(),
		"MATCH (n:Component) RETURN n 'delete'", nil, "")
	if errors.Is(err, store.ErrMutationCypher) {
		t.Fatalf("a malformed read-only statement quoting a mutation keyword must not be classified as mutation, got %v", err)
	}
	if !errors.Is(err, store.ErrInvalidCypher) {
		t.Fatalf("expected ErrInvalidCypher, got %v", err)
	}

	// A mutation keyword inside a comment must also be ignored: the statement
	// is genuinely malformed (unbalanced paren), and the `delete` keyword lives
	// only inside the /* */ comment.
	_, err = s.ExecuteCypher(context.Background(),
		"MATCH (n:Component RETURN n /* delete */", nil, "")
	if errors.Is(err, store.ErrMutationCypher) {
		t.Fatalf("a mutation keyword inside a comment must not be classified as mutation, got %v", err)
	}
	if !errors.Is(err, store.ErrInvalidCypher) {
		t.Fatalf("expected ErrInvalidCypher, got %v", err)
	}
}

// TestExecuteCypher_BareMutationKeywordTrailingReturnNotMutation pins the
// SPEC check-order consequence that a statement failing at Prepare surfaces
// ErrInvalidCypher (INVALID_ARGUMENT) regardless of mutation keywords in its
// text: a syntactically-invalid read-only statement whose text uses a bare
// mutation keyword AFTER a RETURN clause (e.g. `MATCH (n:Component) RETURN n
// DELETE`) is rejected at Prepare, and the SPEC check order "empty query →
// Cypher syntax → read-only enforcement → capability" (SPEC:1015) plus SPEC R3
// (SPEC:260) and the grammar-unparseable note (SPEC:493-497, "never as
// PERMISSION_DENIED") require INVALID_ARGUMENT. The same boundary must hold on
// the ExtractEntityTypes seam, whose error classification is identical to
// ExecuteCypher's (SPEC check order).
func TestExecuteCypher_BareMutationKeywordTrailingReturnNotMutation(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	cyphers := []string{
		"MATCH (n:Component) RETURN n DELETE",
		"MATCH (n) RETURN n DELETE",
	}
	for _, cypher := range cyphers {
		_, err := s.ExecuteCypher(context.Background(), cypher, nil, "")
		if errors.Is(err, store.ErrMutationCypher) {
			t.Fatalf("a bare mutation keyword trailing RETURN is syntax, not a clause: "+
				"expected ErrInvalidCypher for %q, got %v", cypher, err)
		}
		if !errors.Is(err, store.ErrInvalidCypher) {
			t.Fatalf("expected ErrInvalidCypher for %q, got %v", cypher, err)
		}

		_, err = s.ExtractEntityTypes(context.Background(), cypher)
		if errors.Is(err, store.ErrMutationCypher) {
			t.Fatalf("ExtractEntityTypes: a bare mutation keyword trailing RETURN must not "+
				"be classified as mutation for %q, got %v", cypher, err)
		}
		if !errors.Is(err, store.ErrInvalidCypher) {
			t.Fatalf("ExtractEntityTypes: expected ErrInvalidCypher for %q, got %v", cypher, err)
		}
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

	// A statement that fails Prepare with a mutation keyword quoted inside a
	// string literal must keep ErrInvalidCypher, matching ExecuteCypher's
	// error classification — the syntax gate precedes read-only enforcement
	// (SPEC R3 / SPEC:260, SPEC:1015).
	t.Run("invalid syntax with string-literal mutation keyword returns ErrInvalidCypher", func(t *testing.T) {
		_, err := s.ExtractEntityTypes(ctx, "MATCH (n:Component) RETURN n 'delete'")
		if errors.Is(err, store.ErrMutationCypher) {
			t.Errorf("a malformed read-only statement quoting a mutation keyword "+
				"must not be classified as mutation, got %v", err)
		}
		if !errors.Is(err, store.ErrInvalidCypher) {
			t.Errorf("expected ErrInvalidCypher, got %v", err)
		}
	})

	// Each mutation/DDL clause the SPEC R7 §5 and the error table enumerate
	// must be rejected before any capability decision. Clauses the v0.17.0
	// grammar parses (CREATE, SET, DELETE, MERGE, DROP) are classified
	// non-read-only by IsReadOnly and surface ErrMutationCypher — never
	// ErrInvalidCypher, so read-only enforcement precedes capability. Clauses
	// the grammar cannot prepare (REMOVE, FOREACH, index/constraint DDL) fail
	// at the syntax gate and surface ErrInvalidCypher, which precedes
	// read-only enforcement (SPEC:1015) — identical to ExecuteCypher.
	mutations := []struct {
		name   string
		cypher string
	}{
		{"create", "CREATE (n:Component {id: 'bad-uuid'})"},
		{"set", "MATCH (n:Component) SET n.name = 'x'"},
		{"delete", "MATCH (n:Component) DELETE n"},
		{"merge", "MERGE (n:Component {id: 'bad-uuid'})"},
		{"drop", "DROP TABLE Component"},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ExtractEntityTypes(ctx, tc.cypher)
			if !errors.Is(err, store.ErrMutationCypher) {
				t.Errorf("expected ErrMutationCypher for %q, got %v", tc.cypher, err)
			}
		})
	}

	// Grammar-gap mutations fail at Prepare, so the syntax gate surfaces
	// ErrInvalidCypher (SPEC R3 / SPEC:260; row "Invalid Cypher syntax",
	// SPEC:979; R7 §5 note SPEC:493-497).
	prepareFailMutations := []struct {
		name   string
		cypher string
	}{
		{"remove-syntax-gate", "MATCH (n:Component) REMOVE n.name"},
		{"foreach-syntax-gate", "FOREACH (x IN ['aaa'] | CREATE (n:Component {id: x}))"},
	}
	for _, tc := range prepareFailMutations {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ExtractEntityTypes(ctx, tc.cypher)
			if !errors.Is(err, store.ErrInvalidCypher) {
				t.Errorf("expected ErrInvalidCypher for %q, got %v", tc.cypher, err)
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

	// A bare (non-parenthesised) label predicate in a WHERE clause —
	// `WHERE m:Service` — is rejected by the LadybugDB v0.17.0 grammar at
	// Prepare (the parenthesised pattern-predicate form `WHERE (m:Service)` is
	// the supported shape). The seam therefore never extracts labels for this
	// shape: the syntax gate surfaces ErrInvalidCypher before any capability
	// decision (SPEC:1015 check order), so no per-type-grant bypass is
	// reachable through it today. The analyzer's own handling of bare
	// `identifier:Label` predicates is pinned by TestExtractEntityTypeLabels
	// (bare-where-predicate), so a future grammar that accepts the shape cannot
	// silently extract only the parenthesised labels.
	t.Run("bare WHERE label predicate is rejected by the grammar", func(t *testing.T) {
		_, err := s.ExtractEntityTypes(ctx,
			"MATCH (n:Component)-[r]->(m) WHERE m:Service RETURN m")
		if !errors.Is(err, store.ErrInvalidCypher) {
			t.Errorf("expected ErrInvalidCypher (grammar rejects bare label predicates), got %v", err)
		}
	})

	// A statement mixing a classifiable label with an unclassifiable one must
	// fail closed (nil labels → READ:graph/entity/* wildcard fallback), never
	// return the partial subset: a caller holding only the extracted type must
	// not be able to execute a query that also touches the missed type
	// (SPEC R3 every-referenced-type rule, SPEC:260 wildcard bound). The
	// backtick-quoted label references the existing Component table so the
	// binder accepts the statement, but `` `Component` `` is not a bare Cypher
	// identifier, so the analyzer abandons the extraction.
	t.Run("unclassifiable label fails closed to wildcard", func(t *testing.T) {
		labels, err := s.ExtractEntityTypes(ctx, "MATCH (a:Component), (b:`Component`) RETURN a")
		if err != nil {
			t.Fatalf("ExtractEntityTypes: %v", err)
		}
		if labels != nil {
			t.Errorf("expected nil labels (wildcard fallback), got %v", labels)
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
		// Bare (non-parenthesised) label predicates in WHERE clauses reference
		// the same labels as node patterns and must be extracted, so a query
		// like `MATCH (n:Component)-[r]->(m) WHERE m:Service RETURN m` is not
		// authorised for Service rows by a caller holding only the Component
		// per-type grant (SPEC R3). Regression for the pre-fix bypass where
		// only [Component] was extracted.
		{"bare-where-predicate",
			"MATCH (n:Component)-[r]->(m) WHERE m:Service RETURN m",
			[]string{"Component", "Service"}},
		{"bare-where-predicate-only",
			"MATCH (n) WHERE n:Service RETURN n", []string{"Service"}},
		{"bare-where-predicate-whitespace-after-colon",
			"MATCH (n) WHERE n: Service RETURN n", []string{"Service"}},
		// A bare label predicate whose label form the analyzer cannot classify
		// fails closed to the READ:graph/entity/* wildcard fallback, never a
		// partial subset.
		{"bare-where-param-label-fails-closed",
			"MATCH (n:Component) WHERE n:$label RETURN n", nil},
		{"bare-where-backtick-label-fails-closed",
			"MATCH (n:Component) WHERE n:`Label With Space` RETURN n", nil},
		{"unlabelled-nodes-nil", "MATCH (n) RETURN n", nil},
		{"no-match-nil", "RETURN 1", nil},
		// SPEC:260 fail-closed rule: a pattern the analyzer cannot classify
		// (backtick-quoted, parameterised labels) abandons the extraction and
		// returns nil so the caller falls back to READ:graph/entity/* — never a
		// partial subset that could widen access beyond the extracted types.
		{"backtick-label-fails-closed", "MATCH (c:`Label With Space`) RETURN c", nil},
		{"parameterised-label-fails-closed", "MATCH (c:$label) RETURN c", nil},
		{"partial-extraction-fails-closed",
			"MATCH (a:Component)-[:R]->(b:`Label With Space`) RETURN a", nil},
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

// TestFullTextSearch_NoResultCap pins that FullTextSearch returns every
// matching document — no silent per-type cap. SPEC R2 defines
// FullTextSearch(query, entityType?) with no result limit and the error table
// defines no cap; LadybugDB's QUERY_FTS_INDEX TOP argument is optional and
// defaults to retrieving all documents, so the store must not inject one. A
// search matching more than 100 documents must return all of them, not the
// capped subset.
func TestFullTextSearch_NoResultCap(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	const want = 120 // > the old hard-coded TOP := 100.
	for i := range want {
		if _, err := s.CreateEntity(ctx, "Document", "",
			map[string]string{"title": fmt.Sprintf("needle doc %d", i), "body": "content"}, nil, ""); err != nil {
			t.Fatalf("CreateEntity %d: %v", i, err)
		}
	}

	results, err := s.FullTextSearch(ctx, "needle", "Document", "")
	if err != nil {
		t.Fatalf("FullTextSearch: %v", err)
	}
	if len(results) != want {
		t.Errorf("expected all %d matching documents, got %d", want, len(results))
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

// SPEC R9 recovery point 4 rolls back a transaction whose branch .lbug is
// absent; this pins the sibling crash window inside ReplicateSchemaToBranch.
// The branch schema metadata (branches/<txID>.schema.json) is written only
// after ReplicateSchemaToBranch's DDL loop, so a crash after ≥1 table is
// created but before the metadata write leaves a branch .lbug with a
// non-empty catalog and no schema metadata. The client never received the
// txID (the BeginTransaction response is sent only after the metadata write
// succeeds), so the transaction is provably harmless and the reopen must
// classify the partial branch exactly like the absent-.lbug case —
// ErrBranchNotFound, which RecoverOpenTransactions turns into a rollback via
// cleanupTransaction/DropBranchDB — instead of surfacing a hard error that
// bricks startup. A present-but-corrupt metadata file stays a loud failure
// (the guard matches only the not-exist read error).
func TestPersistence_MissingBranchSchemaMetadataRollsBackOnReopen(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	applyTestSchema(t, s)
	if err := s.CreateBranchDB(ctx, "tx-missing-metadata"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(ctx, "tx-missing-metadata"); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Remove the branch schema metadata: the crash residue of a replication
	// interrupted before the metadata write is a non-empty catalog with an
	// absent branches/<txID>.schema.json (on disk indistinguishable from a
	// metadata file lost after the fact).
	metadataPath := filepath.Join(dir, "branches", "tx-missing-metadata.schema.json")
	if err := os.Remove(metadataPath); err != nil {
		t.Fatalf("remove branch metadata: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen main: %v", err)
	}
	defer closeStore(t, reopened)
	// The partial branch must be classified for rollback, not hard-failed.
	if _, err := reopened.DumpAllEntities(ctx, "tx-missing-metadata"); !errors.Is(err, store.ErrBranchNotFound) {
		t.Fatalf("DumpAllEntities = %v, want ErrBranchNotFound (rollback classification)", err)
	}
	// The rollback path (RecoverOpenTransactions' cleanup) drops the branch via
	// DropBranchDB; it must succeed and remove the persisted .lbug.
	if err := reopened.DropBranchDB(ctx, "tx-missing-metadata"); err != nil {
		t.Fatalf("DropBranchDB: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "branches", "tx-missing-metadata.lbug")); !os.IsNotExist(err) {
		t.Fatalf("branch .lbug was not removed by rollback: %v", err)
	}
	// After the rollback the branch is fully absent: classification stays
	// ErrBranchNotFound, never a resurrected partial branch.
	if _, err := reopened.DumpAllEntities(ctx, "tx-missing-metadata"); !errors.Is(err, store.ErrBranchNotFound) {
		t.Fatalf("DumpAllEntities after rollback = %v, want ErrBranchNotFound", err)
	}
}

// SPEC R9 recovery point 4 rolls back a transaction whose branch .lbug is
// absent; this pins the sibling crash window: a crash between CreateBranchDB
// and ReplicateSchemaToBranch leaves a present-but-empty branch .lbug with no
// schema metadata (ReplicateSchemaToBranch writes branches/<txID>.schema.json
// only after its DDL loop). In that window the branch has no tables and no
// data — the transaction never made a durable change — so the reopen must
// classify it exactly like the absent-.lbug case (ErrBranchNotFound, which
// RecoverOpenTransactions turns into a rollback via cleanupTransaction/
// DropBranchDB) instead of surfacing a hard error that bricks startup. The
// non-empty-catalog sibling — a crash mid-DDL in ReplicateSchemaToBranch
// (TestPersistence_MissingBranchSchemaMetadataRollsBackOnReopen) — is
// classified identically: the client never received the txID, so the partial
// branch is just as harmless.
func TestBranch_EmptyBranchNoMetadataRollsBackOnReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	applyTestSchema(t, s)

	const branch = "tx-crash-window"
	if err := s.CreateBranchDB(ctx, branch); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	// Simulate the crash: ReplicateSchemaToBranch never runs, so the branch
	// schema metadata file is never written and the branch catalog stays empty.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Confirm the mid-sequence crash state on disk: present-but-empty .lbug,
	// absent .schema.json.
	if _, err := os.Stat(filepath.Join(dir, "branches", branch+".lbug")); err != nil {
		t.Fatalf("expected persisted empty branch .lbug: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "branches", branch+".schema.json")); !os.IsNotExist(err) {
		t.Fatalf("expected absent branch schema metadata: %v", err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen main: %v", err)
	}
	defer closeStore(t, reopened)

	if _, err := reopened.DumpAllEntities(ctx, branch); !errors.Is(err, store.ErrBranchNotFound) {
		t.Fatalf("DumpAllEntities = %v, want ErrBranchNotFound (rollback classification)", err)
	}
	// The rollback path (RecoverOpenTransactions' cleanup) drops the branch via
	// DropBranchDB; it must succeed and remove the persisted .lbug.
	if err := reopened.DropBranchDB(ctx, branch); err != nil {
		t.Fatalf("DropBranchDB: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "branches", branch+".lbug")); !os.IsNotExist(err) {
		t.Fatalf("branch .lbug was not removed by rollback: %v", err)
	}
	// After the rollback the branch is fully absent: classification stays
	// ErrBranchNotFound, never a resurrected empty branch.
	if _, err := reopened.DumpAllEntities(ctx, branch); !errors.Is(err, store.ErrBranchNotFound) {
		t.Fatalf("DumpAllEntities after rollback = %v, want ErrBranchNotFound", err)
	}
}

// SPEC R9 recovery point 4 rolls back a transaction whose branch .lbug is
// absent (e.g. PVC corruption); a present-but-corrupt branch .lbug is the same
// loss mechanism and must be classified identically — ErrBranchNotFound, which
// RecoverOpenTransactions turns into a rollback via cleanupTransaction/
// DropBranchDB — instead of surfacing a hard error that wedges startup until a
// human deletes the file (the pre-fix behavior). The classification mirrors
// main's R8 corruption heuristic (corruptionCandidates): only a present,
// OS-readable file the engine cannot open is treated as corruption; an
// unreadable file is an operational failure and must stay a hard error so the
// rollback path never deletes a branch DB that was never corrupt.
func TestBranch_CorruptBranchLbugRollsBackOnReopen(t *testing.T) {
	t.Run("readable corrupt .lbug rolls back", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		ctx := context.Background()
		applyTestSchema(t, s)

		const branch = "tx-corrupt-lbug"
		if err := s.CreateBranchDB(ctx, branch); err != nil {
			t.Fatalf("CreateBranchDB: %v", err)
		}
		if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
			t.Fatalf("ReplicateSchemaToBranch: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		// Corrupt the persisted branch .lbug in place (PVC corruption).
		path := filepath.Join(dir, "branches", branch+".lbug")
		if err := os.WriteFile(path, []byte("not a ladybug database"), 0600); err != nil {
			t.Fatalf("corrupt branch .lbug: %v", err)
		}
		if !corruptionCandidates(path) {
			t.Fatal("expected corrupt branch .lbug to be classified as a corruption candidate")
		}

		reopened, err := Open(dir)
		if err != nil {
			t.Fatalf("reopen main: %v", err)
		}
		defer closeStore(t, reopened)

		// A corrupt branch .lbug must be classified as ErrBranchNotFound — the
		// recovery path (RecoverOpenTransactions) rolls the transaction back.
		if _, err := reopened.DumpAllEntities(ctx, branch); !errors.Is(err, store.ErrBranchNotFound) {
			t.Fatalf("DumpAllEntities = %v, want ErrBranchNotFound (rollback classification)", err)
		}
		// The rollback path drops the branch via DropBranchDB; it must succeed
		// and remove the corrupt persisted .lbug.
		if err := reopened.DropBranchDB(ctx, branch); err != nil {
			t.Fatalf("DropBranchDB: %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("corrupt branch .lbug was not removed by rollback: %v", err)
		}
		// After the rollback the branch is fully absent: classification stays
		// ErrBranchNotFound, never a resurrected corrupt branch.
		if _, err := reopened.DumpAllEntities(ctx, branch); !errors.Is(err, store.ErrBranchNotFound) {
			t.Fatalf("DumpAllEntities after rollback = %v, want ErrBranchNotFound", err)
		}
	})

	t.Run("unreadable .lbug stays a hard error", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		ctx := context.Background()
		applyTestSchema(t, s)

		const branch = "tx-unreadable-lbug"
		if err := s.CreateBranchDB(ctx, branch); err != nil {
			t.Fatalf("CreateBranchDB: %v", err)
		}
		if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
			t.Fatalf("ReplicateSchemaToBranch: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		path := filepath.Join(dir, "branches", branch+".lbug")
		if err := os.Chmod(path, 0000); err != nil {
			t.Fatalf("chmod 000: %v", err)
		}
		// Not a corruption candidate: the open must fail loudly and the file
		// must be preserved, mirroring main's R8 operational-failure handling.
		if corruptionCandidates(path) {
			t.Fatal("unreadable file must not be a corruption candidate")
		}

		reopened, err := Open(dir)
		if err != nil {
			t.Fatalf("reopen main: %v", err)
		}
		defer closeStore(t, reopened)
		if _, err := reopened.DumpAllEntities(ctx, branch); errors.Is(err, store.ErrBranchNotFound) {
			t.Fatalf("DumpAllEntities = %v, want a hard open failure (not ErrBranchNotFound)", err)
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("unreadable branch .lbug was removed: %v", statErr)
		}
	})
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
			metadata.EntityTypes[0].Properties[0].Type = driftedColumnType
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

	t.Run("publish before DDL", func(t *testing.T) {
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
		// The schema metadata is published BEFORE the DDL loop (write-ahead),
		// so a publish failure aborts before any DDL executes: the store is
		// left untouched rather than advanced past the persisted metadata.
		if database.TableExists("Component") {
			t.Fatal("publish failure applied DDL")
		}
		if db.failed {
			t.Fatal("store should not be permanently failed after publish failure")
		}
		if err := database.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		// With no metadata and no DDL, the reopen succeeds on a fresh empty
		// database instead of wedging on an advanced catalog with no metadata.
		reopened, err := Open(dir)
		if err != nil {
			t.Fatalf("reopen after publish failure must succeed: %v", err)
		}
		closeStore(t, reopened)
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

// TestVectorBootstrapIndexFailureHealsOnReopen pins the crash-atomicity of the
// CreateEntity vector bootstrap (crud.go): an index-creation failure leaves the
// crash residue of the window between ALTER TABLE ADD embedding and
// CREATE_VECTOR_INDEX — the embedding column exists in the catalog, the vector
// index does not, and schema.json records neither. The pre-fix store failed
// every subsequent Open on this residue ("vector index does not match schema
// metadata"); the Open-time reconcile must complete the interrupted bootstrap
// and adopt the vector state so the store reopens usable.
func TestVectorBootstrapIndexFailureHealsOnReopen(t *testing.T) {
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
			if ok, ierr := reopened.IsVectorIndexBootstrapped(context.Background(), "Vector", branch); ierr != nil || !ok {
				t.Fatalf("interrupted vector index not completed on reopen: %v", ierr)
			}
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
	if ok, ierr := reopened.IsVectorIndexBootstrapped(context.Background(), "Vector", branch); ierr != nil || !ok {
		t.Fatalf("branch vector state not recovered: %v", ierr)
	}
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
	if ok, ierr := reopened.IsVectorIndexBootstrapped(context.Background(), "Vector", ""); ierr != nil || !ok {
		t.Fatalf("vector state not recovered: %v", ierr)
	}
	if _, cerr := reopened.CreateEntity(
		context.Background(), "Vector", "", map[string]string{"name": "after"}, []float32{3, 4}, "",
	); cerr != nil {
		t.Fatalf("store not usable after recovery: %v", cerr)
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
	if ok, err := database.IsVectorIndexBootstrapped(context.Background(), "Vector", ""); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if ok {
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
	if ok, err := database.IsVectorIndexBootstrapped(context.Background(), "Vector", ""); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if !ok {
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
	if dimension, derr := reopened.GetEstablishedDimension(
		context.Background(), "Vector", "",
	); derr != nil || dimension != 3 {
		t.Fatalf("main vector dimension after reopen = %d, error = %v, want 3", dimension, derr)
	}
}

// ---------------------------------------------------------------------------
// Vector-bootstrap crash windows (write-and-reopen recovery)
// ---------------------------------------------------------------------------
//
// Every vector bootstrap path runs its DDL (ALTER TABLE ADD embedding +
// CREATE_VECTOR_INDEX) before its schema-metadata publish. A crash caught
// between the two leaves the catalog carrying vector state that schema.json
// does not record, which used to brick every subsequent Open
// (validateMetadataAgainstCatalog → "vector index does not match schema
// metadata" → startup os.Exit(1)). Each test fabricates that crash residue
// (revert schema.json to its pre-bootstrap content after the DDL ran) and pins
// the Open-time reconcile's recovery: the store reopens with the dimension
// adopted and remains usable.

// vectorBootstrapCrashSchema is the minimal vector-enabled schema used by the
// crash-window tests.
func vectorBootstrapCrashSchema() *flowv1.Schema {
	return &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
		Name: "Vector", EnableVectorIndex: true,
		Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
	}}}
}

func TestVectorBootstrapCrashWindow_UpdateEntityHealsOnReopen(t *testing.T) {
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
	if ok, ierr := reopened.IsVectorIndexBootstrapped(ctx, "Vector", ""); ierr != nil || !ok {
		t.Fatalf("vector index not recovered: %v", ierr)
	}
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
	if ok, ierr := reopened.IsVectorIndexBootstrapped(ctx, "Vector", ""); ierr != nil || !ok {
		t.Fatalf("vector index not recovered: %v", ierr)
	}
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

	_, err = s.GetEstablishedDimension(context.Background(), "NoSuchType", "")
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
	if _, err := s.LoadBranchTransactionState(context.Background(), "tx-state"); err != nil &&
		!errors.Is(err, store.ErrBranchStateMissing) {
		t.Fatalf("unregistered branch state: expected ErrBranchStateMissing, got %v", err)
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
	if _, err = s.LoadBranchTransactionState(context.Background(), "tx-state"); err != nil &&
		!errors.Is(err, store.ErrBranchStateMissing) {
		t.Fatalf("dropped branch state: expected ErrBranchStateMissing, got %v", err)
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
	if _, err := reopened.LoadBranchTransactionState(context.Background(), "tx-state"); err != nil &&
		!errors.Is(err, store.ErrBranchStateMissing) {
		t.Fatalf("missing branch state marker: expected ErrBranchStateMissing, got %v", err)
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

	if ok, err := s.IsVectorIndexBootstrapped(context.Background(), "VectorType", ""); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if ok {
		t.Error("expected not bootstrapped before first entity")
	}

	_, err = s.CreateEntity(context.Background(), "VectorType", "",
		map[string]string{"name": "v1"}, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	if ok, err := s.IsVectorIndexBootstrapped(context.Background(), "VectorType", ""); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if !ok {
		t.Error("expected bootstrapped after first entity with embedding")
	}
}

// IsVectorIndexBootstrapped must surface a genuine catalog-read failure as a
// non-nil, wrapped error rather than swallowing it into "not bootstrapped": a
// caller that treats a nil error as authoritative vector state would silently
// lose the bootstrap signal on a transient catalog failure (LEARNINGS:
// storage-layer silent divergence — sibling scans vectorIndexesOnConn and
// collectVectorIndexes fail loudly for the same reason). Regression: every
// failure — lock acquisition, getEmbeddingDimension, and the show_indexes
// query/row/parse — returned false with no error channel. The injected failure
// here is a real getEmbeddingDimension error: a phantom vector-enabled entity
// type whose table carries an `embedding` column of a non-FLOAT type is
// anomalous for a vector-enabled type (crud.go getEmbeddingDimension), driving
// the error out of the public method instead of the "not bootstrapped" state.
func TestIsVectorIndexBootstrapped_PropagatesReadFailure(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	db := s.(*ladybugDB)
	res, err := db.conn.Query("CREATE NODE TABLE BadVec (id STRING PRIMARY KEY, embedding DOUBLE);")
	if err != nil {
		t.Fatalf("create phantom table: %v", err)
	}
	res.Close()
	// Replace the defs (rather than adding) so the probe can never succeed
	// against a real type.
	db.mu.Lock()
	db.entityTypeDefs = map[string]*store.EntityTypeDef{
		"BadVec": {Name: "BadVec", EnableVectorIndex: true},
	}
	db.mu.Unlock()

	ok, err := s.IsVectorIndexBootstrapped(context.Background(), "BadVec", "")
	if err == nil {
		t.Fatal("expected the embedding-dimension read failure to surface")
	}
	if ok {
		t.Fatal("a failing catalog read must not report the index as bootstrapped")
	}
	if !strings.Contains(err.Error(), "anomalous embedding column type") {
		t.Fatalf("expected the wrapped anomalous-column error, got %v", err)
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

// HydrateBranchFromFiles must apply the same partial-wipe completeness guard as
// RehydrateMainFromFiles (branch.go:517-521): a working tree where entities/
// exists but edges/ was removed (SPEC R2 WipeGraph mid-wipe failure →
// INTERNAL) must fail loudly on the branch load path too — silently loading
// entities and skipping every edge would hydrate an incomplete graph with no
// signal.
func TestHydrateBranchFromFiles_EntitiesDirOnly_ReturnsError(t *testing.T) {
	s, err := OpenInMemory()
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

	// The former "main" / "on connection" loader pair was merged into
	// loadEntitiesFromDirOnConn (branch.go item 2 dedup); pin the merged
	// function's error propagation once.
	loadErr := db.loadEntitiesFromDirOnConn(db.conn, entitiesDir, db.entityTypeDefs)
	if !errors.Is(loadErr, wantErr) {
		t.Fatalf("expected injected ReadDir error, got %v", loadErr)
	}
	want := fmt.Sprintf("read entities dir %q", compDir)
	if !strings.Contains(loadErr.Error(), want) {
		t.Fatalf("error %q does not identify wrapped operation and path %q", loadErr, want)
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
	if ok, err := s.IsVectorIndexBootstrapped(context.Background(), "Document", "main"); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if !ok {
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

// TestApplySchema_AdditiveRequiredEntityProperty_ForwardOnly pins the SPEC R6
// forward-only required-property branch for a newly-added property with
// `required: true` (SPEC:410-413): CreateEntity rejects new entities missing the
// property, but a pre-existing entity created before the property was added is
// not retroactively invalidated — it stays readable, and UpdateEntity does not
// require the property either.
func TestApplySchema_AdditiveRequiredEntityProperty_ForwardOnly(t *testing.T) {
	s, err := OpenInMemory()
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

// TestApplySchema_RuleModification_PreexistingEdgeRemainsValid pins the SPEC R6
// forward-only rules branch (SPEC:413-415): CreateEdge validates against the
// current rules, but existing edges created under previous rules remain valid
// and are not retroactively re-validated. The rule modification must preserve
// the edge type's FROM/TO pair set (a pair change is destructive), so the edit
// grants the source type a second, brand-new edge type — a non-destructive rule
// change. The pre-existing edge must stay readable and listed while new edge
// creation validates against the modified rules.
func TestApplySchema_RuleModification_PreexistingEdgeRemainsValid(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	// Initial schema: Service may connect to Component only via DEPENDS_ON.
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

	svc, err := s.CreateEntity(ctx, "Service", "", nil, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity Service: %v", err)
	}
	comp, err := s.CreateEntity(ctx, "Component", "", nil, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity Component: %v", err)
	}
	edge, err := s.CreateEdge(ctx, "DEPENDS_ON", svc.Id, comp.Id, nil, "main")
	if err != nil {
		t.Fatalf("CreateEdge under the initial rules: %v", err)
	}

	// Non-destructive rule modification: Service's rule gains a second edge type
	// LINKS_TO (a new edge type, so DEPENDS_ON's FROM/TO pair set is unchanged
	// and the apply stays additive).
	schema2 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Service", Rules: []*flowv1.ConnectionRule{
				{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON", "LINKS_TO"}},
			}},
			{Name: "Component"},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "DEPENDS_ON"}, {Name: "LINKS_TO"}},
	}
	if err := s.ApplySchema(ctx, schema2); err != nil {
		t.Fatalf("rule-modifying ApplySchema: %v", err)
	}

	// The pre-existing edge created under the previous rules stays valid — it is
	// readable and listed, not retroactively re-validated against the new rules.
	got, err := s.GetEdge(ctx, edge.Id, "main")
	if err != nil {
		t.Fatalf("GetEdge after the rule modification must not fail: %v", err)
	}
	if got.Type != edge.Type {
		t.Fatalf("expected a %s edge, got %+v", edge.Type, got)
	}
	listed, err := s.ListEdgesOfType(ctx, "DEPENDS_ON", "main")
	if err != nil {
		t.Fatalf("ListEdgesOfType after the rule modification: %v", err)
	}
	if len(listed) != 1 || listed[0].Id != edge.Id {
		t.Fatalf("expected the pre-existing edge to remain listed, got %+v", listed)
	}

	// New edge creation validates against the CURRENT rules: the newly added
	// LINKS_TO edge type is now permitted...
	if _, err := s.CreateEdge(ctx, "LINKS_TO", svc.Id, comp.Id, nil, "main"); err != nil {
		t.Fatalf("CreateEdge via the newly permitted edge type must succeed: %v", err)
	}
	// ...while a direction no rule ever declared stays forbidden.
	_, err = s.CreateEdge(ctx, "DEPENDS_ON", comp.Id, svc.Id, nil, "main")
	if !errors.Is(err, store.ErrEdgeRuleViolation) {
		t.Fatalf("expected ErrEdgeRuleViolation for an unpermitted reverse edge, got %v", err)
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

// TestApplySchema_MixedAdditiveAndDestructive_AllOrNothing pins the ordering
// requirement that every destructive check runs in the pre-DDL catalog diff: a
// schema that BOTH adds a new entity type (additive) AND changes an existing
// edge type's FROM/TO endpoint set (destructive) must fail all-or-nothing
// before any DDL executes. The pre-fix code detected the endpoint change only
// inside alterRelTable — after the entity-type DDL loop had already created the
// new entity's table — so a rejected ApplySchema left partial DDL applied with
// schema.json unpublished, wedging the next file-backed Open in
// validateMetadataAgainstCatalog ("database entity type W is absent from schema
// metadata"). The schema also carries an unchanged edgeless edge type whose rel
// table persists the `_untyped` placeholder pair, pinning that the diff's
// endpoint comparison normalizes empty requested pairs to the placeholder (an
// edgeless edge type must never false-positive the new check).
func TestApplySchema_MixedAdditiveAndDestructive_AllOrNothing(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	// Initial schema: X connects to Y via edge R only, plus an edgeless edge
	// type S (no rules reference it, so its rel table carries the `_untyped`
	// placeholder pair).
	schema1 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "X", Rules: []*flowv1.ConnectionRule{
				{CanConnectTo: []string{"Y"}, Using: []string{"R"}},
			}},
			{Name: "Y"},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "R"}, {Name: "S"}},
	}
	if err := s.ApplySchema(ctx, schema1); err != nil {
		t.Fatalf("first ApplySchema: %v", err)
	}
	db := s.(*ladybugDB)

	// Mixed schema: (a) ADDITIVE — new entity types Z and W; (b) DESTRUCTIVE —
	// a new X→Z rule on the existing edge R adds a FROM/TO pair the rel table
	// cannot express. S stays edgeless and unchanged. S is listed before R so a
	// broken `_untyped` normalization would surface as an error naming S
	// instead of R.
	mixed := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "X", Rules: []*flowv1.ConnectionRule{
				{CanConnectTo: []string{"Y"}, Using: []string{"R"}},
				{CanConnectTo: []string{"Z"}, Using: []string{"R"}},
			}},
			{Name: "Y"},
			{Name: "Z"},
			{Name: "W"},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "S"}, {Name: "R"}},
	}
	err = s.ApplySchema(ctx, mixed)
	if err == nil {
		t.Fatal("expected destructive schema change for the mixed additive+destructive schema")
	}
	if !errors.Is(err, store.ErrDestructiveSchemaChange) {
		t.Fatalf("expected ErrDestructiveSchemaChange, got %v", err)
	}
	// The rejection must name the destructive edge R — S's edgeless placeholder
	// normalization must have passed first.
	if !strings.Contains(err.Error(), `edge "R"`) {
		t.Fatalf("expected the error to name edge R, got %v", err)
	}

	// All-or-nothing: the additive part must not have been applied. The W and Z
	// tables must not exist in the physical catalog (the pre-fix code created
	// them in the entity-type DDL loop before alterRelTable rejected R).
	if kind := tableKindOnConn(t, db.conn, "W"); kind != "" {
		t.Fatalf("additive entity type W partially applied (catalog kind %q) before the destructive rejection", kind)
	}
	if kind := tableKindOnConn(t, db.conn, "Z"); kind != "" {
		t.Fatalf("additive entity type Z partially applied (catalog kind %q) before the destructive rejection", kind)
	}

	// The store is not wedged: schema.json was never rewritten, so a
	// close/reopen must succeed (the pre-fix code left the catalog ahead of the
	// metadata and the Open failed in validateMetadataAgainstCatalog).
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen after rejected mixed schema must succeed, got %v", err)
	}
	defer closeStore(t, reopened)

	// The additive part alone applies cleanly afterwards.
	additive := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "X", Rules: []*flowv1.ConnectionRule{
				{CanConnectTo: []string{"Y"}, Using: []string{"R"}},
			}},
			{Name: "Y"},
			{Name: "Z"},
			{Name: "W"},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "R"}, {Name: "S"}},
	}
	if err := reopened.ApplySchema(ctx, additive); err != nil {
		t.Fatalf("applying only the additive part after the rejection must succeed, got %v", err)
	}
	if !reopened.TableExists("W") {
		t.Fatal("W table should exist after applying the additive part")
	}
}

// TestApplySchemaCrashWindow_PartialCatalogRecoveredOnReopen pins the
// crash-atomicity recovery for ApplySchema. The schema metadata (schema.json)
// is published BEFORE the DDL loop (write-ahead), so a crash between the
// publish and the DDL completion leaves the metadata describing the full
// intended schema while the catalog holds only the tables and columns the DDL
// had already reached. The pre-fix ordering (DDL loop then publish) left the
// opposite asymmetry — a non-empty catalog with no matching schema.json — which
// Open refused forever (permanent crash-loop brick with no recovery path). The
// repair must converge the partial catalog onto the metadata: create the
// tables the DDL never reached, add the columns an interrupted ALTER never
// added, rebuild the extended type's FTS index, and preserve data written
// before the crash.
func TestApplySchemaCrashWindow_PartialCatalogRecoveredOnReopen(t *testing.T) {
	ctx := context.Background()
	schema1 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "A", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
				Rules: []*flowv1.ConnectionRule{{CanConnectTo: []string{"B"}, Using: []string{"R"}}}},
			{Name: "B"},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "R"}},
	}
	// A strict superset of schema1: a new property on A (interrupted ALTER),
	// a new entity type C and a new edgeless edge type S (tables the DDL never
	// reached).
	schema2 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "A", Properties: []*flowv1.Property{
				{Name: "name", Type: "string"}, {Name: "title", Type: "string"},
			}, Rules: []*flowv1.ConnectionRule{{CanConnectTo: []string{"B"}, Using: []string{"R"}}}},
			{Name: "B"},
			{Name: "C"},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "R"}, {Name: "S"}},
	}

	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.ApplySchema(ctx, schema1); err != nil {
		t.Fatalf("ApplySchema schema1: %v", err)
	}
	survivor, err := s.CreateEntity(ctx, "A", "", map[string]string{"name": "survivor"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Fabricate the crash residue: dir/schema.json now describes the full
	// schema2 while the catalog still holds only schema1's tables. Apply
	// schema2 to a scratch store to obtain its exact serialized metadata
	// (vector state captured the same way ApplySchema would), then drop it
	// over dir's schema.json.
	fabDir := t.TempDir()
	fab, err := Open(fabDir)
	if err != nil {
		t.Fatalf("Open fabricator: %v", err)
	}
	if err := fab.ApplySchema(ctx, schema2); err != nil {
		t.Fatalf("ApplySchema schema2: %v", err)
	}
	if err := fab.Close(); err != nil {
		t.Fatalf("Close fabricator: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(fabDir, "schema.json"))
	if err != nil {
		t.Fatalf("read fabricated metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "schema.json"), data, 0600); err != nil {
		t.Fatalf("write fabricated metadata: %v", err)
	}

	// The pre-fix store rejected this partial state forever on Open. The
	// repair must converge the catalog onto the metadata and succeed.
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("Open after simulated crash must recover, got %v", err)
	}
	defer closeStore(t, reopened)

	// Tables the DDL never reached were created.
	if !reopened.TableExists("C") {
		t.Fatal("repair did not create the missing node table C")
	}
	if _, ok := reopened.EdgeType("S"); !ok {
		t.Fatal("repair did not create the missing edge table S")
	}
	// Columns an interrupted ALTER never added were added.
	adef, ok := reopened.EntityType("A")
	if !ok {
		t.Fatal("repaired store lost entity type A")
	}
	if !propertyDefPresent(adef.Properties, "title") {
		t.Fatalf("repair did not add the missing property title to A: %+v", adef.Properties)
	}
	// Data written before the crash survives.
	ent, err := reopened.GetEntity(ctx, survivor.Id, "")
	if err != nil {
		t.Fatalf("pre-crash entity lost after recovery: %v", err)
	}
	if ent.Properties["name"] != "survivor" {
		t.Fatalf("pre-crash entity property = %q, want %q", ent.Properties["name"], "survivor")
	}
	// The rebuilt FTS index covers the repaired string column: a search over
	// the newly added title column must find a match (query.go silently skips
	// index-less types, so this fails if the repair forgot the FTS rebuild).
	needle, err := reopened.CreateEntity(ctx, "A", "",
		map[string]string{"name": "n", "title": "repairneedle"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity with title: %v", err)
	}
	matches, err := reopened.FullTextSearch(ctx, "repairneedle", "A", "")
	if err != nil {
		t.Fatalf("FullTextSearch over repaired column: %v", err)
	}
	found := false
	for i := range matches {
		if matches[i].Id == needle.Id {
			found = true
		}
	}
	if !found {
		t.Fatalf("FTS index was not rebuilt over the repaired title column; matches=%d", len(matches))
	}
}

// TestReopen_RecreatesMissingFTSIndex pins the crash-repair's
// ensureFTSIndexOnConn branch: a node table whose _fts index is absent (the
// crash caught between CREATE NODE TABLE and its CREATE_FTS_INDEX inside
// createNodeTableOnConn) must have the index recreated on the next Open so
// FullTextSearch keeps covering the type instead of silently skipping it.
func TestReopen_RecreatesMissingFTSIndex(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.ApplySchema(ctx, &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
		Name: "Document", Properties: []*flowv1.Property{{Name: "title", Type: "string"}},
	}}}); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	doc, err := s.CreateEntity(ctx, "Document", "", map[string]string{"title": "needle"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	db := s.(*ladybugDB)
	// Simulate the crash residue: drop the FTS index directly on the catalog.
	r, err := db.conn.Query("CALL DROP_FTS_INDEX('Document', 'Document_fts');")
	if err != nil {
		t.Fatalf("drop FTS index: %v", err)
	}
	r.Close()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer closeStore(t, reopened)
	matches, err := reopened.FullTextSearch(ctx, "needle", "Document", "")
	if err != nil {
		t.Fatalf("FullTextSearch: %v", err)
	}
	for i := range matches {
		if matches[i].Id == doc.Id {
			return
		}
	}
	t.Fatalf("FTS index was not recreated on reopen; matches=%d", len(matches))
}

// propertyDefPresent reports whether the property list carries a property with
// the given name.
func propertyDefPresent(props []store.PropertyDef, name string) bool {
	for _, p := range props {
		if p.Name == name {
			return true
		}
	}
	return false
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
	if ok, err := s.IsVectorIndexBootstrapped(context.Background(), "Document", "main"); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if ok {
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
	if ok, err := s.IsVectorIndexBootstrapped(context.Background(), "Document", "main"); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if ok {
		t.Fatal("the false→true transition must stay lazy — no entity written yet")
	}

	// A first embedding write now bootstraps the dimension (SPEC R7 lazy).
	if _, err := s.CreateEntity(ctx, "Document", "",
		map[string]string{"title": "vec"}, []float32{1, 2, 3}, "main"); err != nil {
		t.Fatalf("CreateEntity with embedding after enable: %v", err)
	}
	if ok, err := s.IsVectorIndexBootstrapped(context.Background(), "Document", "main"); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if !ok {
		t.Fatal("expected vector index bootstrapped after first embedding write")
	}
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
	if ok, err := s2.IsVectorIndexBootstrapped(context.Background(), "Document", "main"); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if !ok {
		t.Fatal("lazy vector index was not restored on reopen")
	}
	if dim, derr := s2.GetEstablishedDimension(context.Background(), "Document", "main"); derr != nil || dim != 3 {
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
	// that has no corresponding table in the database. LadybugDB returns an
	// error from Prepare, and findEntityByID propagates it as an operational
	// error rather than swallowing it into ErrEntityNotFound.
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	db := s.(*ladybugDB)
	// Only a phantom type — no real types. Prepare fails on the only type,
	// and findEntityByID propagates that failure as an operational error.
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

	// The former "main" / "on connection" loader pair was merged into
	// loadEntitiesFromDirOnConn (branch.go item 2 dedup); pin the merged
	// function's error propagation once.
	loadErr := db.loadEntitiesFromDirOnConn(db.conn, entitiesDir, db.entityTypeDefs)
	if !errors.Is(loadErr, os.ErrNotExist) {
		t.Fatalf("expected dangling-symlink ReadFile error, got %v", loadErr)
	}
	want := fmt.Sprintf("read entity file %q", fpath)
	if !strings.Contains(loadErr.Error(), want) {
		t.Fatalf("error %q does not identify wrapped operation and path %q", loadErr, want)
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

// SPEC R7: the NaN/Inf embedding rejection applies "regardless of indexing
// status" — a non-indexed entity type accepts an embedding of any dimension but
// must still reject NaN/Inf before the value is discarded. UpdateEntity's
// NaN/Inf guard (crud.go) runs unconditionally, before any EnableVectorIndex
// gate; this pins the non-indexed branch, mirroring the CreateEntity non-indexed
// test (TestCreateEntity_NaNEmbeddingNonIndexed).
func TestUpdateEntity_NaNOrInfEmbedding_NonIndexedType(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	e, err := s.CreateEntity(context.Background(), "Document", "",
		map[string]string{"title": "t"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	for _, emb := range [][]float32{
		{float32(math.NaN()), 0},
		{float32(math.Inf(1)), 0},
		{float32(math.Inf(-1)), 0},
	} {
		_, err = s.UpdateEntity(context.Background(), e.Id, nil, emb, "")
		if err == nil {
			t.Fatalf("expected error for NaN/Inf embedding %v on non-indexed type", emb)
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

// UpdateEntity embedding-bootstrap path (crud.go): a first embedding
// update on a vector-indexed type whose column is not yet bootstrapped attempts
// (mirroring CreateEntity) to ALTER TABLE add the embedding column, persist the
// embedding, create the vector index, and publish the vector schema metadata.
// Only CreateEntity's bootstrap was previously tested.
//
// LadybugDB refuses to rewrite the embedding of an existing row while the
// vector index exists ("Cannot set property ... because it is used in one or
// more indexes"), so UpdateEntity defers index creation until after the row
// write on the bootstrap path and drops/recreates the index on the established
// path. The bootstrap DDL still locks the dimension first.
func TestUpdateEntity_EmbeddingBootstrap(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	if ok, err := s.IsVectorIndexBootstrapped(context.Background(), "VectorType", ""); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if ok {
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
	if ok, err := s.IsVectorIndexBootstrapped(context.Background(), "VectorType", ""); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if ok {
		t.Fatal("rehydration must not bootstrap the vector index")
	}

	// First embedding update runs the bootstrap: ALTER TABLE add embedding
	// column, persist the embedding, then CREATE_VECTOR_INDEX, locking the
	// dimension to 3. The update succeeds — the deferred index creation lets the
	// row's embedding be written.
	updated, err := s.UpdateEntity(context.Background(), id, nil, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("bootstrap-then-persist UpdateEntity: %v", err)
	}
	if !reflect.DeepEqual(updated.Embedding, []float32{1, 2, 3}) {
		t.Fatalf("embedding after bootstrap update = %v, want [1 2 3]", updated.Embedding)
	}
	if ok, err := s.IsVectorIndexBootstrapped(context.Background(), "VectorType", ""); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if !ok {
		t.Fatalf("expected vector index bootstrapped after first embedding update (update err: %v)", err)
	}
	if dim, derr := s.GetEstablishedDimension(context.Background(), "VectorType", ""); derr != nil || dim != 3 {
		t.Fatalf("dimension = %d, error = %v (update err: %v)", dim, derr, err)
	}
	got, err := s.GetEntity(context.Background(), id, "")
	if err != nil {
		t.Fatalf("GetEntity after bootstrap update: %v", err)
	}
	if !reflect.DeepEqual(got.Embedding, []float32{1, 2, 3}) {
		t.Fatalf("persisted embedding = %v, want [1 2 3]", got.Embedding)
	}
}

// SPEC R7 parity (crud.go): a post-bootstrap UpdateEntity supplying an
// embedding whose dimension MATCHES the established dimension (dim > 0,
// len(embedding) == dim) rewrites the row's embedding: the store drops the
// vector index, writes the new embedding, and recreates the index. This is the
// SPEC success branch the error-table rows (dimension mismatch, NaN/Inf) imply:
// a matching, NaN-free embedding update is accepted, never rejected.
func TestUpdateEntity_EmbeddingRewriteSuccess(t *testing.T) {
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
	if ok, err := s.IsVectorIndexBootstrapped(context.Background(), "VectorType", ""); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if !ok {
		t.Fatal("expected VectorType bootstrapped after create")
	}

	// Matching-dimension update: the dimension guard passes (3 == 3) and the
	// embedding rewrite succeeds (drop index → SET → recreate index).
	updated, err := s.UpdateEntity(ctx, e.Id, map[string]string{"name": "v2"}, []float32{4, 5, 6}, "")
	if err != nil {
		t.Fatalf("matching-dimension embedding update: %v", err)
	}
	if !reflect.DeepEqual(updated.Embedding, []float32{4, 5, 6}) {
		t.Fatalf("updated embedding = %v, want [4 5 6]", updated.Embedding)
	}
	if updated.Properties["name"] != "v2" {
		t.Fatalf("updated properties = %+v, want name=v2", updated.Properties)
	}
	// The dimension is unchanged (locked by the original create) and the vector
	// index is back in place.
	if dim, derr := s.GetEstablishedDimension(context.Background(), "VectorType", ""); derr != nil || dim != 3 {
		t.Fatalf("dimension = %d, error = %v, want 3", dim, derr)
	}
	if ok, err := s.IsVectorIndexBootstrapped(context.Background(), "VectorType", ""); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if !ok {
		t.Fatal("expected vector index recreated after embedding rewrite")
	}
	got, err := s.GetEntity(ctx, e.Id, "")
	if err != nil {
		t.Fatalf("GetEntity after embedding rewrite: %v", err)
	}
	if !reflect.DeepEqual(got.Embedding, []float32{4, 5, 6}) {
		t.Fatalf("persisted embedding = %v, want [4 5 6]", got.Embedding)
	}
	// The rewritten embedding is searchable through the recreated index.
	results, err := s.SearchNeighbors(ctx, []float32{4, 5, 6}, "VectorType", 10, "")
	if err != nil {
		t.Fatalf("SearchNeighbors after embedding rewrite: %v", err)
	}
	if len(results) != 1 || results[0].Entity.Id != e.Id {
		t.Fatalf("expected 1 neighbor (the rewritten entity), got %+v", results)
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

	if ok, err := s.IsVectorIndexBootstrapped(context.Background(), "VectorType", ""); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if ok {
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
	if ok, err := s.IsVectorIndexBootstrapped(context.Background(), "VectorType", branch); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if !ok {
		t.Fatal("expected VectorType bootstrapped on branch")
	}

	// Rollback: drop the branch.
	if err := s.DropBranchDB(ctx, branch); err != nil {
		t.Fatalf("DropBranchDB: %v", err)
	}

	// Main must remain un-bootstrapped (SPEC R7 branch scope).
	if ok, err := s.IsVectorIndexBootstrapped(context.Background(), "VectorType", ""); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if ok {
		t.Fatal("main must not be bootstrapped after branch rollback")
	}
	dim, err := s.GetEstablishedDimension(context.Background(), "VectorType", "")
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
	if dim, err := s.GetEstablishedDimension(context.Background(), "VectorType", ""); err != nil || dim != 3 {
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

	if ok, err := s.IsVectorIndexBootstrapped(context.Background(), "VectorType", ""); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if ok {
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

	if ok, err := s.IsVectorIndexBootstrapped(context.Background(), "VectorType", ""); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if !ok {
		t.Fatal("expected vector index promoted to main after RehydrateFromBranch")
	}
	if dim, derr := s.GetEstablishedDimension(context.Background(), "VectorType", ""); derr != nil || dim != 3 {
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
	ctx := context.Background()

	id := uuid.NewString()
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	edgesDir := filepath.Join(root, "edges")
	// File is under the VectorType directory but declares type "Document". It
	// also carries an embedding, pinning the ordering of the directory-mismatch
	// guard against the embedding-bootstrap DDL (branch.go
	// loadEntitiesFromDirOnConn):
	// the guard must reject the file BEFORE ensureEmbeddingLoadSchema ALTERs the
	// directory-named table and locks a vector dimension on it (a file about to
	// be rejected must never mutate schema state — SPEC R8 fail-loudly).
	writeJSONFile(t, filepath.Join(entitiesDir, "VectorType", id+".json"), map[string]any{
		"id": id, "type": "Document", "properties": map[string]string{"name": "mismatch"},
		"embedding": []float32{1, 2, 3},
	})
	if err := s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err == nil {
		t.Fatal("expected error for entity type/directory mismatch")
	} else if !errors.Is(err, store.ErrInvalidEntityDir) {
		t.Fatalf("expected ErrInvalidEntityDir, got %v", err)
	}
	// The guard must have rejected the file before the embedding bootstrap ran:
	// VectorType must not have gained an embedding column / vector index as a
	// side effect of the rejected file.
	if ok, err := s.IsVectorIndexBootstrapped(ctx, "VectorType", ""); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if ok {
		t.Fatal("embedding bootstrap DDL must not run before the type/directory mismatch guard")
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
	ctx := context.Background()

	if err := s.CreateBranchDB(ctx, "tx1"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(ctx, "tx1"); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}

	id := uuid.NewString()
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	edgesDir := filepath.Join(root, "edges")
	if err := os.MkdirAll(edgesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Same ordering pin as the main path: the embedding must not bootstrap the
	// branch VectorType table before the mismatch guard rejects the file.
	writeJSONFile(t, filepath.Join(entitiesDir, "VectorType", id+".json"), map[string]any{
		"id": id, "type": "Document", "properties": map[string]string{"name": "mismatch"},
		"embedding": []float32{1, 2, 3},
	})
	if err := s.HydrateBranchFromFiles(ctx, "tx1", entitiesDir, edgesDir); err == nil {
		t.Fatal("expected error for entity type/directory mismatch")
	} else if !errors.Is(err, store.ErrInvalidEntityDir) {
		t.Fatalf("expected ErrInvalidEntityDir, got %v", err)
	}
	if ok, err := s.IsVectorIndexBootstrapped(ctx, "VectorType", "tx1"); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if ok {
		t.Fatal("embedding bootstrap DDL must not run before the type/directory mismatch guard")
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
	if ok, err := s.IsVectorIndexBootstrapped(context.Background(), "VectorType", ""); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if !ok {
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
	if ok, err := ftsIndexExists(db.conn, "Document"); err != nil {
		t.Fatalf("check FTS index: %v", err)
	} else if ok {
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

// A catalog table with NO schema-metadata entry must fail closed on reopen:
// validateMetadataAgainstCatalog rejects any catalog type absent from the
// metadata. ApplySchema publishes the metadata before the DDL loop
// (write-ahead), so its own failures can no longer produce this state — but it
// remains reachable from external tampering or a foreign catalog, and Open
// must reject it loudly rather than silently serving or dropping the orphaned
// table. This drives that fail-closed property by reconstructing the
// divergence directly: the first table's metadata is intact; a second table
// exists in the catalog but was never published to metadata.
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
	r, err := db.conn.Query(orphanDDL)
	if err != nil {
		t.Fatalf("create orphaned Second table: %v", err)
	}
	r.Close()
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
	edgeID := uuid.NewString()
	writeJSONFile(t, filepath.Join(edgesDir, "DependsOn", edgeID+".json"), map[string]any{
		"id": edgeID, "type": "DependsOn", "from": fromID, "to": uuid.NewString(),
	})

	err = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
	if err == nil {
		t.Fatal("expected loud failure for an edge whose endpoint entity is absent")
	}
	if !errors.Is(err, store.ErrSourceOrTargetNotFound) {
		t.Fatalf("expected ErrSourceOrTargetNotFound, got %v", err)
	}
}

// An endpoint entity that exists under a label outside the edge type's fixed
// FROM/TO endpoint set (SPEC R2: the endpoint clauses are fixed at rel-table
// CREATE time) must fail loudly on the edge load path instead of silently
// dropping the edge. The former probe MATCH (n {id: $id}) matched by id alone,
// so an id present under a non-endpoint label passed the probe while the
// subsequent CREATE silently no-opped against the rel table's endpoint clauses
// — the wrong-label edge vanished on re-hydration. The label-constrained probe
// surfaces ErrSourceOrTargetNotFound, mirroring the typed write path
// (crud.go CreateEdge's findEntityByID + rule validation).
func TestRehydrateFiles_WrongLabelEndpointFailsLoudly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		branch bool
	}{
		{"main", false},
		{"branch", true},
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

			// The edge's from-endpoint id exists only under the Document label,
			// outside DependsOn's FROM/TO endpoint set (Component→Component), so
			// the id satisfies an untyped probe but not the label-constrained one.
			fromID := uuid.NewString()
			toID := uuid.NewString()
			root := t.TempDir()
			entitiesDir := filepath.Join(root, "entities")
			edgesDir := filepath.Join(root, "edges")
			writeJSONFile(t, filepath.Join(entitiesDir, "Document", fromID+".json"), map[string]any{
				"id": fromID, "type": "Document",
			})
			writeJSONFile(t, filepath.Join(entitiesDir, "Component", toID+".json"), map[string]any{
				"id": toID, "type": "Component",
			})
			edgeID := uuid.NewString()
			writeJSONFile(t, filepath.Join(edgesDir, "DependsOn", edgeID+".json"), map[string]any{
				"id": edgeID, "type": "DependsOn", "from": fromID, "to": toID,
			})

			var loadErr error
			if tc.branch {
				loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
			} else {
				loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			}
			if loadErr == nil {
				t.Fatal("expected loud failure for an edge whose from-endpoint id exists only under a non-endpoint label")
			}
			if !errors.Is(loadErr, store.ErrSourceOrTargetNotFound) {
				t.Fatalf("expected ErrSourceOrTargetNotFound, got %v", loadErr)
			}
		})
	}
}

// The endpoint probe must enforce the edge type's FROM/TO endpoint PAIRS, not
// per-role label-union membership. For a multi-pair edge type (rules
// Alpha→Beta and Beta→Alpha both via CONNECTS) the union of FROM labels equals
// the union of TO labels ({Alpha, Beta}), so an edge whose endpoints both
// resolve to Alpha-typed entities passes a union-membership probe for both
// roles while the rel table's per-pair endpoint clauses (FROM Alpha TO Beta,
// FROM Beta TO Alpha) cannot serve an Alpha→Alpha edge — the edge would not be
// loaded (or, on an engine that silently no-ops an unmatched pair, silently
// dropped). The write path rejects the same edge via validateEdgeRulesFor
// (crud.go → ErrEdgeRuleViolation), so the load path must fail loudly with
// ErrSourceOrTargetNotFound instead of relying on coincidental engine errors.
func TestRehydrateFiles_CrossPairEndpointFailsLoudly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		branch bool
	}{
		{"main", false},
		{"branch", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := OpenInMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer closeStore(t, s)
			ctx := context.Background()

			// Multi-pair edge type: CONNECTS permits Alpha→Beta and Beta→Alpha.
			if err := s.ApplySchema(ctx, &flowv1.Schema{
				EntityTypes: []*flowv1.EntityType{
					{Name: "Alpha", Rules: []*flowv1.ConnectionRule{
						{CanConnectTo: []string{"Beta"}, Using: []string{"CONNECTS"}},
					}},
					{Name: "Beta", Rules: []*flowv1.ConnectionRule{
						{CanConnectTo: []string{"Alpha"}, Using: []string{"CONNECTS"}},
					}},
				},
				EdgeTypes: []*flowv1.EdgeType{{Name: "CONNECTS"}},
			}); err != nil {
				t.Fatalf("ApplySchema: %v", err)
			}

			const branch = "tx1"
			if tc.branch {
				if err := s.CreateBranchDB(ctx, branch); err != nil {
					t.Fatalf("CreateBranchDB: %v", err)
				}
				if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
					t.Fatalf("ReplicateSchemaToBranch: %v", err)
				}
			}

			// Both endpoints are Alpha-typed: Alpha→Alpha is NOT one of
			// CONNECTS' FROM/TO pairs (Alpha→Beta, Beta→Alpha).
			fromID := uuid.NewString()
			toID := uuid.NewString()
			root := t.TempDir()
			entitiesDir := filepath.Join(root, "entities")
			edgesDir := filepath.Join(root, "edges")
			writeJSONFile(t, filepath.Join(entitiesDir, "Alpha", fromID+".json"), map[string]any{
				"id": fromID, "type": "Alpha",
			})
			writeJSONFile(t, filepath.Join(entitiesDir, "Alpha", toID+".json"), map[string]any{
				"id": toID, "type": "Alpha",
			})
			edgeID := uuid.NewString()
			writeJSONFile(t, filepath.Join(edgesDir, "CONNECTS", edgeID+".json"), map[string]any{
				"id": edgeID, "type": "CONNECTS", "from": fromID, "to": toID,
			})

			var loadErr error
			if tc.branch {
				loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
			} else {
				loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			}
			if loadErr == nil {
				t.Fatal("expected loud failure for an edge whose endpoint pair is not in the edge type's FROM/TO pair set")
			}
			if !errors.Is(loadErr, store.ErrSourceOrTargetNotFound) {
				t.Fatalf("expected ErrSourceOrTargetNotFound, got %v", loadErr)
			}
		})
	}
}

// The store load path must decode the created_at/updated_at fields the gitstore
// write path persists on every entity/edge file (gitstore EntityJSON/EdgeJSON)
// instead of fabricating time.Now() at the re-hydration moment (LEARNINGS: "a
// read path must decode every field the write path persists (never fabricate a
// value in place of persisted state)"). The load structs previously omitted the
// fields, so json.Unmarshal dropped them and the load stamped "now" — persisted
// timestamps silently shifted to the re-hydration moment. This test writes
// entity/edge files through the gitstore write path, re-hydrates them through
// the store load path, and asserts the persisted timestamps survive untouched.
func TestRehydrateMainFromFiles_PersistedTimestampsSurvive(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	// Write the graph through the gitstore write path, which persists
	// created_at/updated_at on every file.
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}

	created := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := time.Date(2024, 6, 7, 8, 9, 10, 0, time.UTC)
	fromID := uuid.NewString()
	toID := uuid.NewString()
	edgeID := uuid.NewString()
	if err := gs.WriteEntityFiles(ctx, "Component", []gitstore.Entity{
		{ID: fromID, Type: "Component", Properties: map[string]string{"name": "a"}, CreatedAt: created, UpdatedAt: updated},
		{ID: toID, Type: "Component", Properties: map[string]string{"name": "b"}, CreatedAt: created, UpdatedAt: updated},
	}); err != nil {
		t.Fatalf("WriteEntityFiles: %v", err)
	}
	if err := gs.WriteEdgeFiles(ctx, "DependsOn", []gitstore.Edge{
		{ID: edgeID, Type: "DependsOn", FromEntityID: fromID, ToEntityID: toID,
			Properties: map[string]string{"strength": strengthValue}, CreatedAt: created, UpdatedAt: updated},
	}); err != nil {
		t.Fatalf("WriteEdgeFiles: %v", err)
	}

	entitiesDir, edgesDir := gs.HydrationDirs()
	if err := s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		t.Fatalf("RehydrateMainFromFiles: %v", err)
	}

	// The load path must have accepted the timestamp-bearing files and loaded
	// the graph content.
	if _, err := s.GetEntity(ctx, fromID, ""); err != nil {
		t.Fatalf("GetEntity(from): %v", err)
	}
	if _, err := s.GetEntity(ctx, toID, ""); err != nil {
		t.Fatalf("GetEntity(to): %v", err)
	}
	if _, err := s.GetEdge(ctx, edgeID, ""); err != nil {
		t.Fatalf("GetEdge: %v", err)
	}

	// The persisted timestamps must survive the store round-trip unchanged:
	// re-reading the files via the gitstore read path (the sibling read path
	// that already decodes them) must return the exact values written, not a
	// re-hydration-time "now".
	entities, err := gs.ReadAllEntityFiles(ctx, "Component")
	if err != nil {
		t.Fatalf("ReadAllEntityFiles: %v", err)
	}
	if len(entities) != 2 {
		t.Fatalf("ReadAllEntityFiles returned %d entities, want 2", len(entities))
	}
	for _, ent := range entities {
		if !ent.CreatedAt.Equal(created) {
			t.Errorf("entity %s created_at = %v, want %v", ent.ID, ent.CreatedAt, created)
		}
		if !ent.UpdatedAt.Equal(updated) {
			t.Errorf("entity %s updated_at = %v, want %v", ent.ID, ent.UpdatedAt, updated)
		}
	}
	edges, err := gs.ReadAllEdgeFiles(ctx, "DependsOn")
	if err != nil {
		t.Fatalf("ReadAllEdgeFiles: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("ReadAllEdgeFiles returned %d edges, want 1", len(edges))
	}
	for _, edge := range edges {
		if !edge.CreatedAt.Equal(created) {
			t.Errorf("edge %s created_at = %v, want %v", edge.ID, edge.CreatedAt, created)
		}
		if !edge.UpdatedAt.Equal(updated) {
			t.Errorf("edge %s updated_at = %v, want %v", edge.ID, edge.UpdatedAt, updated)
		}
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

// SPEC R8 corruption recovery + SPEC:982 "Sync re-hydration failed → INTERNAL
// (e.g. disk full, filesystem corruption)": the re-hydration loaders must
// surface a filesystem I/O error loudly on every load path — a file the OS
// cannot read (here: permission-denied, mode 000) must fail the whole
// re-hydration, never be silently skipped and reported as success, which would
// re-hydrate an incomplete graph with no signal (LEARNINGS: store-layer I/O
// errors must be propagated, never discarded). This disk-backed coverage closes
// the SPEC-coverage gap that the gitstore setupTestStore ponytail used to admit
// (gitstore_test.go): the sync worker's SPEC:982 INTERNAL row depends on
// RehydrateMainFromFiles failing loudly when the working tree cannot be read.
func TestRehydrateFiles_IOErrorFailsLoudly(t *testing.T) {
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
			var blocked string
			if tc.edge {
				blocked = filepath.Join(edgesDir, "DependsOn", "edge.json")
				writeJSONFile(t, blocked, map[string]any{
					"id": uuid.NewString(), "type": "DependsOn", "from": uuid.NewString(), "to": uuid.NewString(),
				})
			} else {
				blocked = filepath.Join(entitiesDir, "Component", "ent.json")
				writeJSONFile(t, blocked, map[string]any{
					"id": uuid.NewString(), "type": "Component", "properties": map[string]string{"name": "io-error"},
				})
			}
			// Make the file unreadable so the loaders' os.ReadFile fails with
			// EACCES. Restore permissions so t.TempDir can clean up.
			if err := os.Chmod(blocked, 0000); err != nil {
				t.Fatalf("chmod 000: %v", err)
			}
			defer func() {
				if err := os.Chmod(blocked, 0600); err != nil {
					t.Errorf("restore chmod: %v", err)
				}
			}()

			var loadErr error
			if tc.branch {
				loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
			} else {
				loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			}
			if loadErr == nil {
				t.Fatal("expected loud failure for an unreadable file, got nil")
			}
			want := "read entity file"
			if tc.edge {
				want = "read edge file"
			}
			if !strings.Contains(loadErr.Error(), want) {
				t.Fatalf("expected the file-read error to be propagated, got %v", loadErr)
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

// CreateEntity's data-integrity probe (crud.go) enforces global (cross-type) ID
// uniqueness on the runtime write path; insertEntityOnConn must enforce the
// same invariant on the re-hydration load path. Two element files carrying the
// same ID under different type directories are corrupt/hand-edited git state —
// the id column is PRIMARY KEY only within each node table, so both would
// otherwise insert silently, after which findEntityByID resolves the ID
// nondeterministically (map iteration order). The load must fail loudly with
// ErrEntityAlreadyExists on every load path that inserts entities (main and
// branch).
func TestRehydrateFiles_CrossTypeDuplicateIDFailsLoudly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		branch bool
	}{
		{"main", false},
		{"branch", true},
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

			dupID := uuid.NewString()
			root := t.TempDir()
			entitiesDir := filepath.Join(root, "entities")
			edgesDir := filepath.Join(root, "edges")
			if err := os.MkdirAll(edgesDir, 0o755); err != nil {
				t.Fatal(err)
			}
			// Same ID under two different type directories. Filenames are the
			// canonical <id>.json (as the gitstore write path writes them) so
			// the cross-type duplicate-ID guard is reached — a filename/id
			// mismatch would be rejected by the id↔filename guard instead.
			writeJSONFile(t, filepath.Join(entitiesDir, "Component", dupID+".json"), map[string]any{
				"id": dupID, "type": "Component",
			})
			writeJSONFile(t, filepath.Join(entitiesDir, "Document", dupID+".json"), map[string]any{
				"id": dupID, "type": "Document",
			})

			var loadErr error
			if tc.branch {
				loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
			} else {
				loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			}
			if loadErr == nil {
				t.Fatal("expected loud failure for a cross-type duplicate entity ID")
			}
			if !errors.Is(loadErr, store.ErrEntityAlreadyExists) {
				t.Fatalf("expected ErrEntityAlreadyExists, got %v", loadErr)
			}
		})
	}
}

// The raw filename base must equal the embedded id on every load path — the
// sibling gitstore read path's invariant (ReadAllEntityFiles/ReadAllEdgeFiles
// reject a file whose embedded id conflicts with its filename base,
// gitstore/entity.go:105-106, edge.go:104-105). A well-formed file is <id>.json
// whose embedded id equals the filename base (writeEntityFile/writeEdgeFile); a
// corrupt/hand-edited file such as wrongname.json containing a canonical id
// would otherwise load silently under an id never written to that path —
// resurrecting a previously deleted element on re-hydration with no signal
// (LEARNINGS: store/gitstore read-path silent divergence). Every main/branch ×
// entity/edge load path must fail loudly with the sentinel.
func TestRehydrateFiles_EmbeddedIDConflictsWithFilenameFailsLoudly(t *testing.T) {
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
				// Edge file with a canonical embedded id under a filename that
				// does not match it.
				writeJSONFile(t, filepath.Join(edgesDir, "DependsOn", "wrongname.json"), map[string]any{
					"id": uuid.NewString(), "type": "DependsOn",
					"from": uuid.NewString(), "to": uuid.NewString(),
				})
			} else {
				// Entity file with a canonical embedded id under a filename that
				// does not match it.
				writeJSONFile(t, filepath.Join(entitiesDir, "Component", "wrongname.json"), map[string]any{
					"id": uuid.NewString(), "type": "Component",
					"properties": map[string]string{"name": "filename-mismatch"},
				})
			}

			var loadErr error
			if tc.branch {
				loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
			} else {
				loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			}
			if loadErr == nil {
				t.Fatal("expected loud failure for a file whose filename conflicts with its embedded id")
			}
			if !errors.Is(loadErr, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, loadErr)
			}
		})
	}
}

// nonCanonicalUUIDSpellings are the four spellings of a single valid RFC4122
// v4 UUID that google/uuid.Parse accepts but the canonical RFC4122 §3 string
// form (the lowercase 8-4-4-4-12 dashed string) does not. SPEC:162 requires
// the canonical form; the gitstore sibling tests the same spellings against
// its read/write paths (uuid_guard_test.go).
var nonCanonicalUUIDSpellings = []string{
	"550E8400-E29B-41D4-A716-446655440000",          // uppercase hex
	"550e8400e29b41d4a716446655440000",              // 32-char no-hyphen
	"{550e8400-e29b-41d4-a716-446655440000}",        // braced {...}
	"urn:uuid:550e8400-e29b-41d4-a716-446655440000", // urn:uuid: prefix
}

// SPEC R2 requires entity and edge IDs to be the canonical RFC4122 §3 UUID v4
// string form: the store's write path gates every ID through validateUUID
// (store.ErrInvalidIDFormat), and the sibling gitstore read path rejects a
// non-canonical embedded id with ErrInvalidUUID (entity.go/edge.go). The
// re-hydration loaders must reject the same shape on every load path
// (main/branch × entity/edge): a corrupt/hand-edited file whose embedded id is
// a non-canonical spelling of a valid UUID (uppercase hex, no-hyphen, braced,
// urn:uuid:) would otherwise load silently under that spelling — a second row
// for one UUID the write path would never produce. Fail loudly instead (SPEC
// R8). Each file's filename matches its embedded (non-canonical) id so the
// filename↔id guard passes and the canonical-form guard is the one that fires.
func TestRehydrateFiles_NonCanonicalUUIDFailsLoudly(t *testing.T) {
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
			for _, id := range nonCanonicalUUIDSpellings {
				t.Run(id, func(t *testing.T) {
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
						writeJSONFile(t, filepath.Join(edgesDir, "DependsOn", id+".json"), map[string]any{
							"id": id, "type": "DependsOn",
							"from": uuid.NewString(), "to": uuid.NewString(),
						})
					} else {
						writeJSONFile(t, filepath.Join(entitiesDir, "Component", id+".json"), map[string]any{
							"id": id, "type": "Component",
							"properties": map[string]string{"name": "non-canonical"},
						})
					}

					var loadErr error
					if tc.branch {
						loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
					} else {
						loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
					}
					if loadErr == nil {
						t.Fatal("expected loud failure for a file whose embedded id is a non-canonical UUID v4 spelling")
					}
					if !errors.Is(loadErr, store.ErrInvalidIDFormat) {
						t.Fatalf("expected ErrInvalidIDFormat, got %v", loadErr)
					}
				})
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

// Closed/failed stores must surface ErrDatabaseNotReady from the branch-state
// entry points rather than serving stale in-memory state or mutating the state
// file (learnings rule: a store primitive must fail loudly on a closed/failed
// store, never silently return state or perform I/O).
func TestBranchState_ClosedOrFailedStoreReturnsNotReady(t *testing.T) {
	t.Run("closed store", func(t *testing.T) {
		s, err := OpenInMemory()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if _, err := s.LoadBranchTransactionState(ctx, "tx-state"); !errors.Is(err, store.ErrDatabaseNotReady) {
			t.Fatalf("LoadBranchTransactionState on closed store: expected ErrDatabaseNotReady, got %v", err)
		}
		if err := s.InvalidateBranchState(ctx, "tx-state"); !errors.Is(err, store.ErrDatabaseNotReady) {
			t.Fatalf("InvalidateBranchState on closed store: expected ErrDatabaseNotReady, got %v", err)
		}
	})
	t.Run("failed store", func(t *testing.T) {
		s, err := OpenInMemory()
		if err != nil {
			t.Fatal(err)
		}
		defer closeStore(t, s)
		db := s.(*ladybugDB)
		db.failed = true
		ctx := context.Background()
		if _, err := s.LoadBranchTransactionState(ctx, "tx-state"); !errors.Is(err, store.ErrDatabaseNotReady) {
			t.Fatalf("LoadBranchTransactionState on failed store: expected ErrDatabaseNotReady, got %v", err)
		}
		if err := s.InvalidateBranchState(ctx, "tx-state"); !errors.Is(err, store.ErrDatabaseNotReady) {
			t.Fatalf("InvalidateBranchState on failed store: expected ErrDatabaseNotReady, got %v", err)
		}
	})
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

// SPEC R7 (SPEC:457-458): a non-indexed entity type accepts an embedding of
// any dimension but does not persist or index it. TestCreateEntity_
// NonIndexedTypeDiscardsEmbedding pins CreateEntity's discard; this pins the
// UpdateEntity sibling — for a non-vector type the `def.EnableVectorIndex &&
// hasNewEmb` guard (crud.go:249) is skipped, so the supplied embedding is
// neither bootstrapped nor written to the SET clause, and the update succeeds.
func TestUpdateEntity_NonIndexedTypeDiscardsEmbedding(t *testing.T) {
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
		t.Fatalf("non-indexed type must accept an embedding on create: %v", err)
	}
	if e.Embedding != nil {
		t.Fatalf("expected nil Embedding on returned entity for non-indexed type, got %v", e.Embedding)
	}

	// UpdateEntity the same entity with a fresh embedding and a property
	// change: the update must succeed, apply the property, and discard the
	// embedding (the returned entity's Embedding stays nil).
	updated, err := s.UpdateEntity(ctx, e.Id,
		map[string]string{"title": "doc-v2"}, []float32{4, 5, 6}, "")
	if err != nil {
		t.Fatalf("non-indexed type must accept an embedding on update: %v", err)
	}
	if updated.Embedding != nil {
		t.Fatalf("expected nil Embedding on returned entity from UpdateEntity, got %v", updated.Embedding)
	}
	if updated.Properties["title"] != "doc-v2" {
		t.Fatalf("expected the property update to apply while the embedding is discarded, got %+v", updated.Properties)
	}

	// The embedding must not be persisted: GetEntity returns nil Embedding,
	// and the non-vector type never gains a vector index.
	got, err := s.GetEntity(ctx, e.Id, "")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if got.Embedding != nil {
		t.Fatalf("expected non-indexed type to discard the update embedding, got %v", got.Embedding)
	}
	if ok, err := s.IsVectorIndexBootstrapped(ctx, "Document", ""); err != nil {
		t.Fatalf("IsVectorIndexBootstrapped: %v", err)
	} else if ok {
		t.Fatal("expected Document to have no vector index after an embedding-bearing update")
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
