// Package embed defines the Embedder interface for computing vector
// embeddings and provides utility functions for similarity computation.
package embed

import (
	"context"
	"fmt"
	"math"
)

// Embedder computes vector embeddings for text.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// CosineSimilarity computes the cosine similarity between two vectors.
// Returns an error if the vectors are of different lengths or both zero.
func CosineSimilarity(a, b []float32) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("vector length mismatch: %d vs %d", len(a), len(b))
	}
	if len(a) == 0 {
		return 0, fmt.Errorf("empty vectors")
	}

	var dot, normA, normB float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}

	if normA == 0 || normB == 0 {
		return 0, fmt.Errorf("zero-magnitude vector")
	}

	return dot / (math.Sqrt(normA) * math.Sqrt(normB)), nil
}
