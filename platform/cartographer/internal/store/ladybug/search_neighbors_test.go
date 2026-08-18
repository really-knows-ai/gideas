package ladybug

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

func TestSearchNeighbors_Valid(t *testing.T) {
	s, err := openInMemory()
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
	s, err := openInMemory()
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
	s, err := openInMemory()
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
	s, err := openInMemory()
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
	s, err := openInMemory()
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
	s, err := openInMemory()
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
	s, err := openInMemory()
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
		s, err := openInMemory()
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
		s, err := openInMemory()
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

// TestSearchNeighbors_WildcardHeterogeneousDimensions verifies that a wildcard
// (entityType == "") search skips entity types whose established vector
// dimension does not match the query embedding and aggregates only the
// matching-dimension types, instead of aborting on the first mismatched type.
func TestSearchNeighbors_WildcardHeterogeneousDimensions(t *testing.T) {
	s, err := openInMemory()
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

func TestSearchNeighbors_NaNOrInfEmbedding(t *testing.T) {
	s, err := openInMemory()
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

func TestSearchNeighbors_ZeroTopKDefaults(t *testing.T) {
	s, err := openInMemory()
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
	s, err := openInMemory()
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
	assertVectorIndexState(t, s, "VectorType", "", true, "expected VectorType vector index bootstrapped")

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

// A vector-enabled entity type that has been declared with EnableVectorIndex
// but whose embedding column has not yet been bootstrapped (dim == 0 — no
// entity written yet) is legitimately not searchable, not an error (query.go
// searchIndexedType skips silently, SPEC R7 lazy bootstrap). This pins the
// single-type (non-empty entityType) success branch: SearchNeighbors returns an
// empty result set with a nil error rather than erroring or fabricating data.
func TestSearchNeighbors_DeclaredNotBootstrappedType_SucceedsEmpty(t *testing.T) {
	s, err := openInMemory()
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
