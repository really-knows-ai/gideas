package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// Test: haikuSchema compiles and validates correctly
// ---------------------------------------------------------------------------

func TestHaikuSchema_ValidOutput(t *testing.T) {
	// The schema must be accepted by NewAgent (compilation check).
	// We use a nil-safe approach: NewAgent needs a real client, but we
	// only want to test schema compilation. We test via Agent.Run instead.
	agent := newTestAgent(t)

	validJSON := `{"haiku": "autumn moonlight\na worm digs silently\ninto the chestnut"}`

	infer := func(_ context.Context, _ []byte) (*flow.InferResult, error) {
		return &flow.InferResult{
			Output:       []byte(validJSON),
			Model:        "test",
			InputTokens:  1,
			OutputTokens: 1,
			DurationMs:   1,
		}, nil
	}

	out, err := agent.Run(context.Background(), infer, nil)
	if err != nil {
		t.Fatalf("expected valid output to pass schema validation, got error: %v", err)
	}
	if string(out) != validJSON {
		t.Fatalf("output mismatch: got %s, want %s", out, validJSON)
	}
}

func TestHaikuSchema_RejectsEmptyHaiku(t *testing.T) {
	agent := newTestAgent(t)

	// minLength: 1 should reject an empty string.
	emptyHaiku := `{"haiku": ""}`

	infer := func(_ context.Context, _ []byte) (*flow.InferResult, error) {
		return &flow.InferResult{
			Output:       []byte(emptyHaiku),
			Model:        "test",
			InputTokens:  1,
			OutputTokens: 1,
			DurationMs:   1,
		}, nil
	}

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected empty haiku to fail schema validation")
	}
	if !strings.Contains(err.Error(), "output validation failed") {
		t.Fatalf("expected 'output validation failed' in error, got: %v", err)
	}
}

func TestHaikuSchema_RejectsMissingField(t *testing.T) {
	agent := newTestAgent(t)

	noHaikuField := `{"poem": "not a haiku"}`

	infer := func(_ context.Context, _ []byte) (*flow.InferResult, error) {
		return &flow.InferResult{
			Output:       []byte(noHaikuField),
			Model:        "test",
			InputTokens:  1,
			OutputTokens: 1,
			DurationMs:   1,
		}, nil
	}

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected missing 'haiku' field to fail schema validation")
	}
}

func TestHaikuSchema_RejectsAdditionalProperties(t *testing.T) {
	agent := newTestAgent(t)

	extraField := `{"haiku": "test haiku", "extra": "not allowed"}`

	infer := func(_ context.Context, _ []byte) (*flow.InferResult, error) {
		return &flow.InferResult{
			Output:       []byte(extraField),
			Model:        "test",
			InputTokens:  1,
			OutputTokens: 1,
			DurationMs:   1,
		}, nil
	}

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected additional properties to fail schema validation")
	}
}

// ---------------------------------------------------------------------------
// Test: makeInferFunc with mock Ollama server
// ---------------------------------------------------------------------------

func TestMakeInferFunc_ReturnsCorrectResult(t *testing.T) {
	// Stand up a fake Ollama /api/generate endpoint.
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

		// Verify the request body contains the model and prompt.
		var reqBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Errorf("failed to decode request body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if reqBody["model"] != "test-model" {
			t.Errorf("expected model 'test-model', got %v", reqBody["model"])
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ollamaResp)
	}))
	defer srv.Close()

	// Point the ollama client at our test server.
	t.Setenv("OLLAMA_BASE_URL", srv.URL)

	inferFn := makeInferFunc("test-model")
	result, err := inferFn(context.Background(), []byte("write a haiku about winter"))
	if err != nil {
		t.Fatalf("inferFn returned error: %v", err)
	}

	// Verify output is the raw response from Ollama.
	expectedOutput := `{"haiku": "cold winter rain\nthe cat sleeps by the fire\nsteam from the teacup"}`
	if string(result.Output) != expectedOutput {
		t.Fatalf("output mismatch:\ngot:  %s\nwant: %s", result.Output, expectedOutput)
	}

	// Verify cost metadata.
	if result.Model != "test-model" {
		t.Fatalf("expected model 'test-model', got %q", result.Model)
	}
	if result.InputTokens != 42 {
		t.Fatalf("expected InputTokens=42, got %d", result.InputTokens)
	}
	if result.OutputTokens != 18 {
		t.Fatalf("expected OutputTokens=18, got %d", result.OutputTokens)
	}
	if result.DurationMs != 1500 {
		t.Fatalf("expected DurationMs=1500, got %d", result.DurationMs)
	}
	if result.Extra["provider"] != "ollama" {
		t.Fatalf("expected Extra[provider]=ollama, got %v", result.Extra["provider"])
	}
}

func TestMakeInferFunc_OllamaError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	t.Setenv("OLLAMA_BASE_URL", srv.URL)

	inferFn := makeInferFunc("nonexistent-model")
	_, err := inferFn(context.Background(), []byte("prompt"))
	if err == nil {
		t.Fatal("expected error when Ollama returns non-200")
	}
	if !strings.Contains(err.Error(), "ollama generate") {
		t.Fatalf("expected 'ollama generate' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: haikuOutput unmarshalling
// ---------------------------------------------------------------------------

func TestHaikuOutput_Unmarshal(t *testing.T) {
	raw := `{"haiku": "first line\nsecond line\nthird line"}`
	var out haikuOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if !strings.Contains(out.Haiku, "first line") {
		t.Fatalf("expected haiku to contain 'first line', got %q", out.Haiku)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestAgent creates a FoundryAgent backed by a no-op gRPC spy for schema
// validation testing. It uses the node's haikuSchema variable.
func newTestAgent(t *testing.T) *flow.Agent {
	t.Helper()

	// We need a connected Client for NewAgent. Start a minimal bufconn
	// server that accepts heartbeat and telemetry calls.
	client := newSpyClient(t)

	agent, err := flow.NewAgent(client, haikuSchema,
		flow.WithHeartbeatInterval(1<<62)) // effectively disable heartbeat ticking
	if err != nil {
		t.Fatalf("NewAgent() with haikuSchema failed: %v", err)
	}
	return agent
}

// newSpyClient creates a flow.Client backed by a bufconn gRPC server with
// no-op implementations of all five service interfaces.
func newSpyClient(t *testing.T) *flow.Client {
	t.Helper()

	// Use a local TCP listener on an ephemeral port to avoid importing
	// bufconn (which is internal to the SDK test package).
	lis, err := newLocalListener()
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	srv := newSpyGRPCServer()
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	client, err := flow.NewClient(flow.WithSidecarAddress(lis.Addr().String()))
	if err != nil {
		t.Fatalf("NewClient() failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}
