package sqlite

import (
	"testing"
)

const testGroupSecurity = "security"
const testLawSecID = "law-sec"

// testEmbeddingDims is a small dimension used for tests so we don't need
// to create 2048-dimensional vectors.
const testEmbeddingDims = 4

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(":memory:", WithEmbeddingDimension(testEmbeddingDims))
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
