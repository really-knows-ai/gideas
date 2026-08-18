package ladybug

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
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

// openInMemory is the package-test constructor for an ephemeral in-memory
// LadybugDB. The exported OpenInMemory seam was removed as test-only dead
// production surface (LEARNINGS: test-only callers do not justify a production
// surface); this test-local replacement keeps the in-package tests on an
// in-memory engine. Cross-package tests (service) use a temp-dir file-backed
// Open instead.
func openInMemory() (store.Store, error) {
	database, err := lbug.OpenInMemoryDatabase(lbug.DefaultSystemConfig())
	if err != nil {
		return nil, fmt.Errorf("open in-memory database: %w", err)
	}

	conn, err := lbug.OpenConnection(database)
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("open connection: %w", err)
	}

	return newLadybugDB("", database, conn)
}

// closeStore is a test helper that closes the store and reports errors.
func closeStore(t *testing.T, s interface{ Close() error }) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// assertVectorIndexState pins the entity type's vector-index bootstrap state on
// the given branch (SPEC R7). Production reads that state through
// GetEstablishedDimension (the exported IsVectorIndexBootstrapped seam was
// removed as test-only dead code), so "bootstrapped" is asserted exactly as the
// production semantics: an established embedding dimension (dim > 0) AND a
// present HNSW vector index (the crash-window recovery tests rely on the index
// half of the check — a re-opened store can carry a locked dimension whose
// index was never recreated).
func assertVectorIndexState(t *testing.T, s store.Store, entityType, branch string, want bool, msg string) {
	t.Helper()
	dim, err := s.GetEstablishedDimension(context.Background(), entityType, branch)
	if err != nil {
		t.Fatalf("GetEstablishedDimension(%q, %q): %v", entityType, branch, err)
	}
	if !want {
		if dim != 0 {
			t.Fatalf("%s: vector index bootstrapped (dim=%d), want not bootstrapped", msg, dim)
		}
		return
	}
	db := s.(*ladybugDB)
	conn, _, unlock, err := db.lockForRead(branch)
	if err != nil {
		t.Fatalf("lockForRead(%q): %v", branch, err)
	}
	defer unlock()
	indexed, err := vectorIndexExists(conn, entityType)
	if err != nil {
		t.Fatalf("vectorIndexExists(%q): %v", entityType, err)
	}
	if dim == 0 || !indexed {
		t.Fatalf("%s: vector index not bootstrapped (dim=%d, indexed=%v)", msg, dim, indexed)
	}
}

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

// vectorBootstrapCrashSchema is the minimal vector-enabled schema used by the
// crash-window tests.
func vectorBootstrapCrashSchema() *flowv1.Schema {
	return &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
		Name: "Vector", EnableVectorIndex: true,
		Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
	}}}
}

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
