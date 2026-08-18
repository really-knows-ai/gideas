package ladybug

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	schemavalidator "github.com/foundry/flow/cartographer/internal/schema"
	"github.com/foundry/flow/cartographer/internal/schemaerrors"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

func TestSchemaCache_RebuildOnOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
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
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	// No explicit check — if openInMemory succeeded, extensions were loaded.
	// A failure to load extensions would have caused openInMemory to return an error.
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
			s, err := openInMemory()
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
	s, err := openInMemory()
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
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	t.Run("edgeless edge type creates the placeholder node table", func(t *testing.T) {
		s, err := openInMemory()
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
		s, err := openInMemory()
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
