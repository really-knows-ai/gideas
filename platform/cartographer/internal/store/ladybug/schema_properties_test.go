package ladybug

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// TestEntityPropertiesNamedToAndType pins SPEC R1's implicit-column-collision
// scope: from/to/type are reserved only for *edge* properties and embedding
// only for vector-enabled entity types, so a NODE table declaring a property
// named `to` or `type` is SPEC-valid and passes schema.Validate. The schema
// cache must retain such columns as real properties (not drop them as if they
// were structural rel-table columns), or CreateEntity rejects the property with
// ErrUnknownProperty and a file-backed reopen fails closed when the metadata
// property is absent from the catalog.
func TestEntityPropertiesNamedToAndType(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
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
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
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
