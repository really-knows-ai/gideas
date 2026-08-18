package ladybug

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/google/uuid"
)

func TestUpdateEntity_Valid(t *testing.T) {
	s, err := openInMemory()
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
	s, err := openInMemory()
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
	s, err := openInMemory()
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

func TestUpdateEntity_UnknownProperty(t *testing.T) {
	s, err := openInMemory()
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
	s, err := openInMemory()
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

func TestUpdateEntity_NaNOrInfEmbedding(t *testing.T) {
	s, err := openInMemory()
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
	s, err := openInMemory()
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
	s, err := openInMemory()
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
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	assertVectorIndexState(t, s, "VectorType", "", false, "expected VectorType not bootstrapped before rehydration")

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
	assertVectorIndexState(t, s, "VectorType", "", false, "rehydration must not bootstrap the vector index")

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
	assertVectorIndexState(t, s, "VectorType", "", true, "expected vector index bootstrapped after first embedding update")
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
	s, err := openInMemory()
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
	assertVectorIndexState(t, s, "VectorType", "", true, "expected VectorType bootstrapped after create")

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
	assertVectorIndexState(t, s, "VectorType", "", true, "expected vector index recreated after embedding rewrite")
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

// SPEC R7 (SPEC:457-458): a non-indexed entity type accepts an embedding of
// any dimension but does not persist or index it. TestCreateEntity_
// NonIndexedTypeDiscardsEmbedding pins CreateEntity's discard; this pins the
// UpdateEntity sibling — for a non-vector type the `def.EnableVectorIndex &&
// hasNewEmb` guard (crud.go:249) is skipped, so the supplied embedding is
// neither bootstrapped nor written to the SET clause, and the update succeeds.
func TestUpdateEntity_NonIndexedTypeDiscardsEmbedding(t *testing.T) {
	s, err := openInMemory()
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
	assertVectorIndexState(t, s, "Document", "", false,
		"expected Document to have no vector index after an embedding-bearing update")
}
