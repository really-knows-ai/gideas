package flow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"text/template"
	"time"
)

// ---------------------------------------------------------------------------
// Tests — Run: Template Rendering
// ---------------------------------------------------------------------------

func TestAgent_Run_TemplateRendering(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-tmpl-001")

	var capturedSystem string
	var capturedQuery []byte
	inferFn := func(_ context.Context, _, systemPrompt string, queryPrompt []byte) (*InferOutput, error) {
		capturedSystem = systemPrompt
		capturedQuery = queryPrompt
		return &InferOutput{
			Output: []byte(`{"haiku": "test haiku"}`),
			Cost: &CostMetadata{
				Model:        testModel,
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		}, nil
	}

	tmpl := template.Must(template.New("query").Parse(
		"Write a haiku about {{.Topic}} with style {{.Style}}"))

	agent, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModelName("test-model"),
		WithQueryTemplate(tmpl),
		WithSystemPrompt("You are a haiku poet."),
		withHeartbeatInterval(time.Hour),
	)
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}
	agent.inferFn = inferFn

	data := struct {
		Topic string
		Style string
	}{Topic: "autumn", Style: "classical"}

	_, err = agent.Run(context.Background(), data)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// Verify the query prompt was rendered correctly.
	expectedQuery := "Write a haiku about autumn with style classical"
	if string(capturedQuery) != expectedQuery {
		t.Fatalf("query mismatch:\ngot:  %q\nwant: %q", string(capturedQuery), expectedQuery)
	}

	// Verify the system prompt was passed through.
	if capturedSystem != "You are a haiku poet." {
		t.Fatalf("system prompt mismatch: got %q", capturedSystem)
	}
}

// ---------------------------------------------------------------------------
// Tests — Run: Output Validation
// ---------------------------------------------------------------------------

func TestAgent_Run_OutputValidation_Pass(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-val-pass")

	validOutput := []byte(`{"haiku": "autumn moonlight"}`)

	inferFn := func(_ context.Context, _, _ string, _ []byte) (*InferOutput, error) {
		return &InferOutput{
			Output: validOutput,
			Cost: &CostMetadata{
				Model:        testModel,
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		}, nil
	}

	agent := newTestAgent(t, env, inferFn)

	got, err := agent.Run(context.Background(), struct{ Input string }{Input: "write a haiku"})
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if string(got) != string(validOutput) {
		t.Fatalf("Run() output = %s, want %s", got, validOutput)
	}
}

func TestAgent_Run_OutputValidation_Fail(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-val-fail")

	// Output missing required "haiku" field.
	invalidOutput := []byte(`{"title": "not a haiku"}`)

	inferFn := func(_ context.Context, _, _ string, _ []byte) (*InferOutput, error) {
		return &InferOutput{
			Output: invalidOutput,
			Cost: &CostMetadata{
				Model:        testModel,
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		}, nil
	}

	agent := newTestAgent(t, env, inferFn)

	_, err := agent.Run(context.Background(), struct{ Input string }{Input: "write a haiku"})
	if err == nil {
		t.Fatal("Run() should return error for invalid output")
	}
	if !strings.Contains(err.Error(), "output validation failed") {
		t.Fatalf("expected 'output validation failed' in error, got: %v", err)
	}

	// No telemetry should have been emitted for invalid output.
	calls := env.spy.getTelemetryCalls()
	if len(calls) != 0 {
		t.Fatalf("expected no telemetry calls for invalid output, got %d", len(calls))
	}
}

func TestAgent_Run_OutputNotJSON(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-val-notjson")

	inferFn := func(_ context.Context, _, _ string, _ []byte) (*InferOutput, error) {
		return &InferOutput{
			Output: []byte("this is not JSON"),
			Cost: &CostMetadata{
				Model:        testModel,
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		}, nil
	}

	agent := newTestAgent(t, env, inferFn)

	_, err := agent.Run(context.Background(), struct{ Input string }{Input: "input"})
	if err == nil {
		t.Fatal("Run() should return error for non-JSON output")
	}
	if !strings.Contains(err.Error(), "output validation failed") {
		t.Fatalf("expected 'output validation failed' in error, got: %v", err)
	}
}

func TestAgent_Run_StripsCodeFences(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-val-fences")

	tests := []struct {
		name   string
		output []byte
	}{
		{
			name:   "fenced with language tag",
			output: []byte("```json\n{\"haiku\": \"test haiku\"}\n```"),
		},
		{
			name:   "fenced without language tag",
			output: []byte("```\n{\"haiku\": \"test haiku\"}\n```"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inferFn := func(_ context.Context, _, _ string, _ []byte) (*InferOutput, error) {
				return &InferOutput{Output: tt.output}, nil
			}

			agent := newTestAgent(t, env, inferFn)
			got, err := agent.Run(context.Background(), struct{ Input string }{Input: "input"})
			if err != nil {
				t.Fatalf("Run() returned error: %v", err)
			}
			if string(got) != `{"haiku": "test haiku"}` {
				t.Fatalf("Run() output = %s", got)
			}
		})
	}
}

func TestAgent_Run_OutputValidation_RetrySuccess(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-val-retry-success")

	attempts := 0
	var retryQuery []byte
	inferFn := func(_ context.Context, _, _ string, queryPrompt []byte) (*InferOutput, error) {
		attempts++
		if attempts == 1 {
			return &InferOutput{Output: []byte("not JSON")}, nil
		}
		retryQuery = append([]byte(nil), queryPrompt...)
		return &InferOutput{Output: []byte(`{"haiku": "test haiku"}`)}, nil
	}

	agent, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModelName("test-model"),
		WithQueryTemplate(simpleQueryTemplate(t)),
		withHeartbeatInterval(time.Hour),
		WithOutputValidationRetries(1),
	)
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}
	agent.inferFn = inferFn

	got, err := agent.Run(context.Background(), struct{ Input string }{Input: "input"})
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	if string(got) != `{"haiku": "test haiku"}` {
		t.Fatalf("Run() output = %s", got)
	}
	if !strings.Contains(string(retryQuery), "previous response failed output validation") {
		t.Fatalf("retry query missing validation feedback: %q", string(retryQuery))
	}
}

func TestAgent_Run_OutputValidation_RetryExhausted(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-val-retry-exhausted")

	attempts := 0
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*InferOutput, error) {
		attempts++
		return &InferOutput{Output: []byte("not JSON")}, nil
	}

	agent, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModelName("test-model"),
		WithQueryTemplate(simpleQueryTemplate(t)),
		withHeartbeatInterval(time.Hour),
		WithOutputValidationRetries(2),
	)
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}
	agent.inferFn = inferFn

	_, err = agent.Run(context.Background(), struct{ Input string }{Input: "input"})
	if err == nil {
		t.Fatal("Run() should return error for exhausted invalid output retries")
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
	if !strings.Contains(err.Error(), "output validation failed") {
		t.Fatalf("expected 'output validation failed' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests — Run: Cost Telemetry
// ---------------------------------------------------------------------------

func TestAgent_Run_CostTelemetry(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-cost-001")

	inferFn := func(_ context.Context, _, _ string, _ []byte) (*InferOutput, error) {
		return &InferOutput{
			Output: []byte(`{"haiku": "test haiku"}`),
			Cost: &CostMetadata{
				Model:        "gpt-4o",
				InputTokens:  150,
				OutputTokens: 30,
				DurationMs:   2500,
				Extra:        map[string]any{"provider": "openai", "cached_tokens": int64(50)},
			},
		}, nil
	}

	agent := newTestAgent(t, env, inferFn)

	_, err := agent.Run(context.Background(), struct{ Input string }{Input: "input"})
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	calls := env.spy.getTelemetryCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 telemetry call, got %d", len(calls))
	}

	if calls[0].EventType != "foundry.cost.llm" {
		t.Fatalf("expected event type 'foundry.cost.llm', got %q", calls[0].EventType)
	}

	var payload map[string]any
	if err := json.Unmarshal(calls[0].Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal telemetry payload: %v", err)
	}

	// Check standard fields.
	if payload["model"] != "gpt-4o" {
		t.Fatalf("expected model=gpt-4o, got %v", payload["model"])
	}
	// JSON numbers unmarshal as float64.
	if payload["input_tokens"].(float64) != 150 {
		t.Fatalf("expected input_tokens=150, got %v", payload["input_tokens"])
	}
	if payload["output_tokens"].(float64) != 30 {
		t.Fatalf("expected output_tokens=30, got %v", payload["output_tokens"])
	}
	if payload["duration_ms"].(float64) != 2500 {
		t.Fatalf("expected duration_ms=2500, got %v", payload["duration_ms"])
	}

	// Check extra fields are merged.
	if payload["provider"] != "openai" {
		t.Fatalf("expected provider=openai, got %v", payload["provider"])
	}
	if payload["cached_tokens"].(float64) != 50 {
		t.Fatalf("expected cached_tokens=50, got %v", payload["cached_tokens"])
	}
}

func TestAgent_Run_NilCostMetadata(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-cost-nil")

	inferFn := func(_ context.Context, _, _ string, _ []byte) (*InferOutput, error) {
		return &InferOutput{
			Output: []byte(`{"haiku": "test haiku"}`),
			Cost:   nil, // Provider doesn't report costs.
		}, nil
	}

	agent := newTestAgent(t, env, inferFn)

	_, err := agent.Run(context.Background(), struct{ Input string }{Input: "input"})
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// No telemetry should be emitted when Cost is nil.
	calls := env.spy.getTelemetryCalls()
	if len(calls) != 0 {
		t.Fatalf("expected no telemetry calls when Cost is nil, got %d", len(calls))
	}
}

// ---------------------------------------------------------------------------
// Tests — Run: Multi-Step Accounting
// ---------------------------------------------------------------------------

func TestAgent_Run_MultiStep(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-multi-001")

	var output *InferOutput
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*InferOutput, error) {
		return output, nil
	}

	agent := newTestAgent(t, env, inferFn)

	// First step.
	output = &InferOutput{
		Output: []byte(`{"haiku": "first draft"}`),
		Cost: &CostMetadata{
			Model:        "gpt-4o",
			InputTokens:  100,
			OutputTokens: 20,
			DurationMs:   1000,
		},
	}
	if _, err := agent.Run(context.Background(), struct{ Input string }{Input: "generate"}); err != nil {
		t.Fatalf("Run() step 1 error: %v", err)
	}

	// Second step.
	output = &InferOutput{
		Output: []byte(`{"haiku": "revised draft"}`),
		Cost: &CostMetadata{
			Model:        "gpt-4o-mini",
			InputTokens:  200,
			OutputTokens: 25,
			DurationMs:   800,
		},
	}
	if _, err := agent.Run(context.Background(), struct{ Input string }{Input: "revise"}); err != nil {
		t.Fatalf("Run() step 2 error: %v", err)
	}

	calls := env.spy.getTelemetryCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 telemetry calls (one per step), got %d", len(calls))
	}

	// Verify each step has independent cost data.
	var p1, p2 map[string]any
	_ = json.Unmarshal(calls[0].Payload, &p1)
	_ = json.Unmarshal(calls[1].Payload, &p2)

	if p1["model"] != "gpt-4o" {
		t.Fatalf("step 1: expected model=gpt-4o, got %v", p1["model"])
	}
	if p2["model"] != "gpt-4o-mini" {
		t.Fatalf("step 2: expected model=gpt-4o-mini, got %v", p2["model"])
	}
	if p1["input_tokens"].(float64) != 100 {
		t.Fatalf("step 1: expected input_tokens=100, got %v", p1["input_tokens"])
	}
	if p2["input_tokens"].(float64) != 200 {
		t.Fatalf("step 2: expected input_tokens=200, got %v", p2["input_tokens"])
	}
}
