package ladybug

import (
	"context"
	"reflect"
	"slices"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

func TestApplySchema_CreateEntityType(t *testing.T) {
	s, err := openInMemory()
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
	s, err := openInMemory()
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
	s, err := openInMemory()
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

// TestApplySchema_RetryAfterMidLoopErrorConverges pins the in-process retry
// idempotency of ApplySchema. The write-ahead metadata +
// restoreMainSchemaMetadataLocked convergence covers the crash case, but an
// in-process error mid-DDL-loop (e.g. a transient I/O failure after ≥1 ALTER
// TABLE ADD succeeded) leaves the catalog advanced while db.entityTypeDefs is
// refreshed only after the full loop succeeds. A retried ApplySchema then
// diffs against the stale cache and would re-issue the non-idempotent ALTER
// TABLE ADD against a column the catalog already holds — failing again on
// every retry until a pod restart. The diff must run against the live catalog
// so the retry converges.
func TestApplySchema_RetryAfterMidLoopErrorConverges(t *testing.T) {
	ctx := context.Background()
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	if err := s.ApplySchema(ctx, &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
		{Name: "Document", Properties: []*flowv1.Property{{Name: "title", Type: "string"}}},
	}}); err != nil {
		t.Fatalf("ApplySchema v1: %v", err)
	}

	db := s.(*ladybugDB)
	// Fabricate the exact residue a mid-loop failure leaves behind: the first
	// run's ALTER TABLE ADD for "author" succeeded in the catalog, but
	// ApplySchema aborted before db.entityTypeDefs was refreshed, so the cache
	// still records only the pre-ALTER property set (and the FTS index rebuild
	// that follows the ALTER loop never ran).
	r, err := db.conn.Query("ALTER TABLE `Document` ADD `author` STRING;")
	if err != nil {
		t.Fatalf("fabricate partial ALTER residue: %v", err)
	}
	r.Close()

	// The retry must converge instead of re-issuing the non-idempotent ALTER
	// for "author" against a column the catalog already holds.
	if err := s.ApplySchema(ctx, &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
		{Name: "Document", Properties: []*flowv1.Property{
			{Name: "title", Type: "string"},
			{Name: "author", Type: "string"},
			{Name: "body", Type: "string"},
		}},
	}}); err != nil {
		t.Fatalf("retried ApplySchema after mid-loop failure must converge, got: %v", err)
	}

	// The converged def records every property.
	def, ok := s.EntityType("Document")
	if !ok {
		t.Fatal("Document entity type missing after retry")
	}
	for _, want := range []string{"title", "author", "body"} {
		if !propertyDefPresent(def.Properties, want) {
			t.Fatalf("retry did not converge property %q: %+v", want, def.Properties)
		}
	}
	// The FTS index was rebuilt over the full string set — including the
	// "author" column the partial run added before it failed: a search over it
	// must find a match (query.go silently skips index-less types, so this
	// fails if the retry skipped the index rebuild).
	ent, err := s.CreateEntity(ctx, "Document", "", map[string]string{"author": "retryneedle"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity with author: %v", err)
	}
	matches, err := s.FullTextSearch(ctx, "retryneedle", "Document", "")
	if err != nil {
		t.Fatalf("FullTextSearch over converged column: %v", err)
	}
	found := false
	for i := range matches {
		if matches[i].Id == ent.Id {
			found = true
		}
	}
	if !found {
		t.Fatal("FTS index was not rebuilt over the converged author column")
	}
}

func TestApplySchema_TableExists(t *testing.T) {
	s, err := openInMemory()
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
	s, err := openInMemory()
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
