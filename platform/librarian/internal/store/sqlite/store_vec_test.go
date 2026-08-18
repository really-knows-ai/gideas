package sqlite

import (
	"context"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Vec Embedding Tests (sqlite-vec)
// ---------------------------------------------------------------------------

// vecEmbeddingCount returns the number of law_embedding_map rows for a law.
// It replaces the deleted HasVecEmbedding assertion.
func vecEmbeddingCount(t *testing.T, s *Store, lawID string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM law_embedding_map WHERE law_id = ?`, lawID,
	).Scan(&count); err != nil {
		t.Fatalf("count vec embedding map: %v", err)
	}
	return count
}

func TestUpsertVecEmbedding_Basic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	embedding := []float32{0.1, 0.2, 0.3, 0.4}
	err := s.UpsertVecEmbedding(ctx, "law-vec-1", embedding)
	if err != nil {
		t.Fatalf("UpsertVecEmbedding: %v", err)
	}

	if got := vecEmbeddingCount(t, s, "law-vec-1"); got != 1 {
		t.Fatalf("expected 1 vec embedding, got %d", got)
	}
}

func TestUpsertVecEmbedding_Update(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Insert.
	if err := s.UpsertVecEmbedding(ctx, "law-vec-u", []float32{1.0, 0.0, 0.0, 0.0}); err != nil {
		t.Fatalf("first UpsertVecEmbedding: %v", err)
	}

	// Update with a new embedding.
	if err := s.UpsertVecEmbedding(ctx, "law-vec-u", []float32{0.0, 1.0, 0.0, 0.0}); err != nil {
		t.Fatalf("second UpsertVecEmbedding: %v", err)
	}

	// Should still have exactly one entry.
	if got := vecEmbeddingCount(t, s, "law-vec-u"); got != 1 {
		t.Fatalf("expected 1 vec embedding after update, got %d", got)
	}
}

func TestUpsertVecEmbedding_DimensionMismatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Wrong dimension (3 instead of 4).
	err := s.UpsertVecEmbedding(ctx, "law-vec-bad", []float32{0.1, 0.2, 0.3})
	if err == nil {
		t.Fatal("expected error for dimension mismatch")
	}
	if !strings.Contains(err.Error(), "dimension mismatch") {
		t.Fatalf("expected dimension mismatch error, got: %v", err)
	}
}

func TestDeleteVecEmbedding_Basic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.UpsertVecEmbedding(ctx, "law-vec-del", []float32{0.1, 0.2, 0.3, 0.4}); err != nil {
		t.Fatalf("UpsertVecEmbedding: %v", err)
	}

	if err := s.DeleteVecEmbedding(ctx, "law-vec-del"); err != nil {
		t.Fatalf("DeleteVecEmbedding: %v", err)
	}

	if got := vecEmbeddingCount(t, s, "law-vec-del"); got != 0 {
		t.Fatalf("expected vec embedding to be deleted, got %d rows", got)
	}
}

func TestDeleteVecEmbedding_NonExistent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Delete a non-existent embedding — should not error.
	err := s.DeleteVecEmbedding(ctx, "law-vec-ghost")
	if err != nil {
		t.Fatalf("expected no error for non-existent embedding, got: %v", err)
	}
}
