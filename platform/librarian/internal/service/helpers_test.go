package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/foundry/flow/librarian/internal/embed"
	"github.com/foundry/flow/librarian/internal/store/sqlite"
)

// testEmbeddingDims matches the store test dimension.
const testEmbeddingDims = 4

// Test constants reused across LawGroup tests.
const (
	testBundleMode    = "bundle"
	testLawByLawMode  = "law-by-law"
	testGroupSecurity = "security"
)

var idCounter int

func testIDGen() string {
	idCounter++
	return fmt.Sprintf("law-%04d", idCounter)
}

func newTestServer(t *testing.T) *LibrarianServer {
	t.Helper()
	idCounter = 0
	store, err := sqlite.New(":memory:", sqlite.WithEmbeddingDimension(testEmbeddingDims))
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	srv := NewLibrarianServer(store, nil, testIDGen, 0.85)
	t.Cleanup(func() {
		srv.Wait()
		_ = store.Close()
	})
	return srv
}

// stubEmbedder returns a deterministic embedding for any input text.
// The embedding is derived from the text length to make vectors reproducible.
type stubEmbedder struct {
	dims int
}

var _ embed.Embedder = (*stubEmbedder)(nil)

func (s *stubEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	v := make([]float32, s.dims)
	for i := range v {
		v[i] = float32(len(text)+i) / 100.0
	}
	return v, nil
}

func newTestServerWithEmbedder(t *testing.T) *LibrarianServer {
	t.Helper()
	idCounter = 0
	store, err := sqlite.New(":memory:", sqlite.WithEmbeddingDimension(testEmbeddingDims))
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	emb := &stubEmbedder{dims: testEmbeddingDims}
	srv := NewLibrarianServer(store, emb, testIDGen, 0.85)
	t.Cleanup(func() {
		srv.Wait()
		_ = store.Close()
	})
	return srv
}

// vecEmbeddingStored reports whether a vec embedding exists for the given law
// by searching the vec0 table with the deterministic stub embedding for the
// law's goal text. Replaces the deleted HasVecEmbedding assertion.
func vecEmbeddingStored(t *testing.T, srv *LibrarianServer, lawID, goal string) bool {
	t.Helper()
	emb := &stubEmbedder{dims: testEmbeddingDims}
	query, err := emb.Embed(context.Background(), goal)
	if err != nil {
		t.Fatalf("embed goal: %v", err)
	}
	results, err := srv.store.SearchVecSimilar(context.Background(), query, 10)
	if err != nil {
		t.Fatalf("SearchVecSimilar: %v", err)
	}
	for _, r := range results {
		if r.LawID == lawID {
			return true
		}
	}
	return false
}
