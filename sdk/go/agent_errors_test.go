package flow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"text/template"
	"time"
)

// ---------------------------------------------------------------------------
// Tests — Run: Error Handling
// ---------------------------------------------------------------------------

func TestAgent_Run_ProviderError(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-err-001")

	providerErr := errors.New("LLM provider unavailable")
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*InferOutput, error) {
		return nil, providerErr
	}

	agent := newTestAgent(t, env, inferFn)

	_, err := agent.Run(context.Background(), struct{ Input string }{Input: "input"})
	if err == nil {
		t.Fatal("Run() should return error when provider fails")
	}
	if !strings.Contains(err.Error(), "provider infer failed") {
		t.Fatalf("expected 'provider infer failed' in error, got: %v", err)
	}
	if !errors.Is(err, providerErr) {
		t.Fatalf("expected error to wrap original provider error")
	}

	// No validation or telemetry should have been attempted.
	calls := env.spy.getTelemetryCalls()
	if len(calls) != 0 {
		t.Fatalf("expected no telemetry calls when provider fails, got %d", len(calls))
	}
}

func TestAgent_Run_NilResult(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-err-nil")

	inferFn := func(_ context.Context, _, _ string, _ []byte) (*InferOutput, error) {
		return nil, nil
	}

	agent := newTestAgent(t, env, inferFn)

	_, err := agent.Run(context.Background(), struct{ Input string }{Input: "input"})
	if err == nil {
		t.Fatal("Run() should return error when provider returns nil result")
	}
	if !strings.Contains(err.Error(), "nil result") {
		t.Fatalf("expected 'nil result' in error, got: %v", err)
	}
}

func TestAgent_Run_TemplateRenderError(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-err-tmpl")

	inferFn := func(_ context.Context, _, _ string, _ []byte) (*InferOutput, error) {
		return &InferOutput{Output: []byte(`{"haiku": "test"}`)}, nil
	}

	// Template that references a method that doesn't exist on the data type.
	badTmpl := template.Must(template.New("query").Parse("{{.NonExistentMethod}}"))

	agent, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModelName("test-model"),
		WithQueryTemplate(badTmpl),
		withHeartbeatInterval(time.Hour),
	)
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}
	agent.inferFn = inferFn

	_, err = agent.Run(context.Background(), struct{ Input string }{Input: "input"})
	if err == nil {
		t.Fatal("Run() should return error when template rendering fails")
	}
	if !strings.Contains(err.Error(), "query template render failed") {
		t.Fatalf("expected 'query template render failed' in error, got: %v", err)
	}
}
