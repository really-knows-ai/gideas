package ladybug

import (
	"context"
	"errors"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
)

func TestGetEstablishedDimension_UnknownType(t *testing.T) {
	s, err := openInMemory()
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
	s, err := openInMemory()
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
	s, err := openInMemory()
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
	s, err := openInMemory()
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

// RehydrateFromBranch (in-memory commit path) must ensure main's embedding
// FLOAT[n] column / vector index exists before inserting an entity that
// carries an embedding, so a branch that bootstraps the dimension on its
// first embedding write can promote the dimension to main (SPEC R7 dimension
// scope). Without this, the branch-copy path's CREATE targeting the embedding
// column would fail because main's table never added it.
func TestRehydrateFromBranch_PromotesEmbeddingDimensionToMain(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed rehydration from working tree")
	}
	s, err := openInMemory()
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

	assertVectorIndexState(t, s, "VectorType", "", false, "expected VectorType not bootstrapped on main before commit")

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

	assertVectorIndexState(t, s, "VectorType", "", true,
		"expected vector index promoted to main after RehydrateFromBranch")
	if dim, derr := s.GetEstablishedDimension(context.Background(), "VectorType", ""); derr != nil || dim != 3 {
		t.Fatalf("dimension on main = %d, error = %v, want 3", dim, derr)
	}
}
