package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaEmbedder_Success(t *testing.T) {
	expected := []float32{0.1, 0.2, 0.3}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}

		var req ollamaEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "test-model" {
			t.Errorf("expected model test-model, got %s", req.Model)
		}
		if req.Input != "hello world" {
			t.Errorf("expected input 'hello world', got %s", req.Input)
		}

		resp := ollamaEmbedResponse{Embeddings: [][]float32{expected}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	embedder := NewOllamaEmbedder(srv.URL, "test-model")
	got, err := embedder.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != len(expected) {
		t.Fatalf("expected %d floats, got %d", len(expected), len(got))
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("got[%d] = %f, want %f", i, got[i], expected[i])
		}
	}
}

func TestOllamaEmbedder_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	embedder := NewOllamaEmbedder(srv.URL, "test-model")
	_, err := embedder.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
}

func TestOllamaEmbedder_MalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"not_embeddings": true}`))
	}))
	defer srv.Close()

	embedder := NewOllamaEmbedder(srv.URL, "test-model")
	_, err := embedder.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error for empty embeddings, got nil")
	}
}

func TestOllamaEmbedder_EmptyEmbeddings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ollamaEmbedResponse{Embeddings: [][]float32{}})
	}))
	defer srv.Close()

	embedder := NewOllamaEmbedder(srv.URL, "test-model")
	_, err := embedder.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error for empty embeddings array, got nil")
	}
}

func TestCosineSimilarity_Identical(t *testing.T) {
	a := []float32{1, 0, 0}
	sim, err := CosineSimilarity(a, a)
	if err != nil {
		t.Fatalf("CosineSimilarity: %v", err)
	}
	if sim < 0.999 {
		t.Fatalf("expected ~1.0 for identical vectors, got %f", sim)
	}
}

func TestCosineSimilarity_Orthogonal(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	sim, err := CosineSimilarity(a, b)
	if err != nil {
		t.Fatalf("CosineSimilarity: %v", err)
	}
	if sim > 0.001 || sim < -0.001 {
		t.Fatalf("expected ~0.0 for orthogonal vectors, got %f", sim)
	}
}

func TestCosineSimilarity_Opposite(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{-1, 0, 0}
	sim, err := CosineSimilarity(a, b)
	if err != nil {
		t.Fatalf("CosineSimilarity: %v", err)
	}
	if sim > -0.999 {
		t.Fatalf("expected ~-1.0 for opposite vectors, got %f", sim)
	}
}

func TestCosineSimilarity_LengthMismatch(t *testing.T) {
	_, err := CosineSimilarity([]float32{1, 0}, []float32{1, 0, 0})
	if err == nil {
		t.Fatal("expected error for length mismatch, got nil")
	}
}

func TestCosineSimilarity_ZeroVector(t *testing.T) {
	_, err := CosineSimilarity([]float32{0, 0, 0}, []float32{1, 0, 0})
	if err == nil {
		t.Fatal("expected error for zero vector, got nil")
	}
}
