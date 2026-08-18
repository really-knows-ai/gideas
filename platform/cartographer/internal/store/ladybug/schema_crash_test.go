package ladybug

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

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
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
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
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
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
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
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
