package flow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Tests — OllamaProvider: Infer
// ---------------------------------------------------------------------------

func TestOllamaProvider_Infer_Success(t *testing.T) {
	ollamaResp := map[string]any{
		"response":          `{"haiku": "cold winter rain\nthe cat sleeps by the fire\nsteam from the teacup"}`,
		"done":              true,
		"prompt_eval_count": 42,
		"eval_count":        18,
		"total_duration":    1_500_000_000, // 1500ms in nanoseconds
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			http.NotFound(w, r)
			return
		}

		// Verify the request body contains the model and concatenated prompt.
		var reqBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Errorf("failed to decode request body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if reqBody["model"] != "test-model" {
			t.Errorf("expected model 'test-model', got %v", reqBody["model"])
		}

		// Verify system+query prompts are concatenated.
		prompt, _ := reqBody["prompt"].(string)
		if !strings.Contains(prompt, "You are a poet.") {
			t.Errorf("expected prompt to contain system prompt, got: %s", prompt)
		}
		if !strings.Contains(prompt, "write a haiku about winter") {
			t.Errorf("expected prompt to contain query prompt, got: %s", prompt)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ollamaResp)
	}))
	defer srv.Close()

	provider := newOllamaProvider(withBaseURL(srv.URL))

	result, err := provider.infer(
		context.Background(), "test-model", "You are a poet.",
		[]byte("write a haiku about winter"),
	)
	if err != nil {
		t.Fatalf("Infer() returned error: %v", err)
	}

	// Verify output is the raw response from Ollama (trimmed).
	expectedOutput := `{"haiku": "cold winter rain\nthe cat sleeps by the fire\nsteam from the teacup"}`
	if string(result.Output) != expectedOutput {
		t.Fatalf("output mismatch:\ngot:  %s\nwant: %s", result.Output, expectedOutput)
	}

	// Verify cost metadata.
	if result.Cost == nil {
		t.Fatal("expected non-nil Cost metadata")
	}
	if result.Cost.Model != "test-model" {
		t.Fatalf("expected model 'test-model', got %q", result.Cost.Model)
	}
	if result.Cost.InputTokens != 42 {
		t.Fatalf("expected InputTokens=42, got %d", result.Cost.InputTokens)
	}
	if result.Cost.OutputTokens != 18 {
		t.Fatalf("expected OutputTokens=18, got %d", result.Cost.OutputTokens)
	}
	if result.Cost.DurationMs != 1500 {
		t.Fatalf("expected DurationMs=1500, got %d", result.Cost.DurationMs)
	}
	if result.Cost.Extra["provider"] != "ollama" {
		t.Fatalf("expected Extra[provider]=ollama, got %v", result.Cost.Extra["provider"])
	}
}

func TestOllamaProvider_Infer_PromptConcatenation(t *testing.T) {
	var capturedPrompt string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		capturedPrompt, _ = reqBody["prompt"].(string)

		resp := map[string]any{
			"response":          `{"result": "ok"}`,
			"done":              true,
			"prompt_eval_count": 1,
			"eval_count":        1,
			"total_duration":    1_000_000,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	provider := newOllamaProvider(withBaseURL(srv.URL))

	// With system prompt — should concatenate with double newline.
	_, err := provider.infer(context.Background(), "m", "system prompt", []byte("query prompt"))
	if err != nil {
		t.Fatalf("Infer() returned error: %v", err)
	}
	expected := "system prompt\n\nquery prompt"
	if capturedPrompt != expected {
		t.Fatalf("prompt concatenation mismatch:\ngot:  %q\nwant: %q", capturedPrompt, expected)
	}

	// Without system prompt — should use query prompt only.
	_, err = provider.infer(context.Background(), "m", "", []byte("query only"))
	if err != nil {
		t.Fatalf("Infer() returned error: %v", err)
	}
	if capturedPrompt != "query only" {
		t.Fatalf("expected query-only prompt %q, got %q", "query only", capturedPrompt)
	}
}

func TestOllamaProvider_Infer_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	provider := newOllamaProvider(withBaseURL(srv.URL))

	_, err := provider.infer(context.Background(), "nonexistent-model", "sys", []byte("prompt"))
	if err == nil {
		t.Fatal("expected error when Ollama returns non-200")
	}
	if !strings.Contains(err.Error(), "unexpected status 404") {
		t.Fatalf("expected 'unexpected status 404' in error, got: %v", err)
	}
}

func TestOllamaProvider_Infer_MalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{not valid json"))
	}))
	defer srv.Close()

	provider := newOllamaProvider(withBaseURL(srv.URL))

	_, err := provider.infer(context.Background(), "model", "sys", []byte("prompt"))
	if err == nil {
		t.Fatal("expected error for malformed response")
	}
	if !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("expected 'decode response' in error, got: %v", err)
	}
}

func TestOllamaProvider_Infer_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":"late","done":true}`))
	}))
	defer srv.Close()

	provider := newOllamaProvider(
		withBaseURL(srv.URL),
		withTimeout(50*time.Millisecond),
	)

	_, err := provider.infer(context.Background(), "model", "sys", []byte("prompt"))
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "request failed") {
		t.Fatalf("expected 'request failed' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests — OllamaProvider: Construction
// ---------------------------------------------------------------------------

func TestOllamaProvider_DefaultBaseURL(t *testing.T) {
	// Clear any env var.
	t.Setenv("OLLAMA_BASE_URL", "")

	provider := newOllamaProvider()
	if provider.baseURL != ollamaDefaultBaseURL {
		t.Fatalf("expected default base URL %q, got %q", ollamaDefaultBaseURL, provider.baseURL)
	}
}

func TestOllamaProvider_EnvBaseURL(t *testing.T) {
	t.Setenv("OLLAMA_BASE_URL", "http://custom:1234/")

	provider := newOllamaProvider()
	// Trailing slash should be trimmed.
	if provider.baseURL != "http://custom:1234" {
		t.Fatalf("expected base URL 'http://custom:1234', got %q", provider.baseURL)
	}
}

func TestOllamaProvider_ExplicitBaseURL(t *testing.T) {
	t.Setenv("OLLAMA_BASE_URL", "http://from-env:1234")

	// Explicit option should override env var.
	provider := newOllamaProvider(withBaseURL("http://explicit:5678"))
	if provider.baseURL != "http://explicit:5678" {
		t.Fatalf("expected base URL 'http://explicit:5678', got %q", provider.baseURL)
	}
}
