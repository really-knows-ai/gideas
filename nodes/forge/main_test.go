package main

import (
	"context"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// Test helper — creates a ForgeAgent with a custom InferFunc
// ---------------------------------------------------------------------------

func newTestForgeAgent(t *testing.T, inferFn flow.InferFunc, cfg *forgeConfig) *ForgeAgent {
	t.Helper()
	client := newSpyClient(t)
	agent, err := NewForgeAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewForgeAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(agent.agent, inferFn)
	return agent
}

func defaultTestConfig() *forgeConfig {
	return &forgeConfig{
		InputArtefacts:   []string{"petition"},
		OutputArtefact:   "haiku",
		GovernedArtefact: "haiku",
		OutputField:      "haiku",
	}
}

// ---------------------------------------------------------------------------
// Tests — ForgeAgent: Schema Validation via Run()
// ---------------------------------------------------------------------------

func TestForgeAgent_ValidOutput(t *testing.T) {
	cfg := defaultTestConfig()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"haiku": "autumn moonlight\na worm digs silently\ninto the chestnut"}`),
			Cost: &flow.CostMetadata{
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		}, nil
	}

	agent := newTestForgeAgent(t, inferFn, cfg)

	result, err := agent.Run(context.Background(), "write a haiku about autumn", nil)
	if err != nil {
		t.Fatalf("expected valid output to pass, got error: %v", err)
	}

	expected := "autumn moonlight\na worm digs silently\ninto the chestnut"
	if result != expected {
		t.Fatalf("output mismatch:\ngot:  %q\nwant: %q", result, expected)
	}
}

func TestForgeAgent_RejectsEmptyOutput(t *testing.T) {
	cfg := defaultTestConfig()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"haiku": ""}`),
			Cost: &flow.CostMetadata{
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		}, nil
	}

	agent := newTestForgeAgent(t, inferFn, cfg)

	_, err := agent.Run(context.Background(), "write a haiku", nil)
	if err == nil {
		t.Fatal("expected empty output to fail schema validation")
	}
	if !strings.Contains(err.Error(), "output validation failed") {
		t.Fatalf("expected 'output validation failed' in error, got: %v", err)
	}
}

func TestForgeAgent_RejectsMissingField(t *testing.T) {
	cfg := defaultTestConfig()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"poem": "not a haiku"}`),
			Cost: &flow.CostMetadata{
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		}, nil
	}

	agent := newTestForgeAgent(t, inferFn, cfg)

	_, err := agent.Run(context.Background(), "write a haiku", nil)
	if err == nil {
		t.Fatal("expected missing field to fail schema validation")
	}
}

func TestForgeAgent_RejectsAdditionalProperties(t *testing.T) {
	cfg := defaultTestConfig()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"haiku": "test haiku", "extra": "not allowed"}`),
			Cost: &flow.CostMetadata{
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		}, nil
	}

	agent := newTestForgeAgent(t, inferFn, cfg)

	_, err := agent.Run(context.Background(), "write a haiku", nil)
	if err == nil {
		t.Fatal("expected additional properties to fail schema validation")
	}
}

// ---------------------------------------------------------------------------
// Tests — ForgeAgent: Output Field Extraction
// ---------------------------------------------------------------------------

func TestForgeAgent_CustomOutputField(t *testing.T) {
	cfg := &forgeConfig{
		InputArtefacts:   []string{"petition"},
		OutputArtefact:   "document",
		GovernedArtefact: "document",
		OutputField:      "document",
	}

	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"document": "generated document content"}`),
			Cost: &flow.CostMetadata{
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		}, nil
	}

	agent := newTestForgeAgent(t, inferFn, cfg)

	result, err := agent.Run(context.Background(), "write a document", nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if result != "generated document content" {
		t.Fatalf("expected 'generated document content', got %q", result)
	}
}

// ---------------------------------------------------------------------------
// Tests — ForgeAgent: Template Rendering
// ---------------------------------------------------------------------------

func TestForgeAgent_PromptContainsInput(t *testing.T) {
	cfg := defaultTestConfig()
	var capturedQuery []byte
	inferFn := func(_ context.Context, _, _ string, queryPrompt []byte) (*flow.InferOutput, error) {
		capturedQuery = queryPrompt
		return &flow.InferOutput{
			Output: []byte(`{"haiku": "test haiku"}`),
			Cost: &flow.CostMetadata{
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		}, nil
	}

	agent := newTestForgeAgent(t, inferFn, cfg)

	_, err := agent.Run(context.Background(), "write about autumn leaves", nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// Verify the query prompt contains the input.
	if !strings.Contains(string(capturedQuery), "write about autumn leaves") {
		t.Fatalf("query prompt should contain input, got: %s", capturedQuery)
	}
}

func TestForgeAgent_PromptContainsLaws(t *testing.T) {
	cfg := defaultTestConfig()
	var capturedQuery []byte
	inferFn := func(_ context.Context, _, _ string, queryPrompt []byte) (*flow.InferOutput, error) {
		capturedQuery = queryPrompt
		return &flow.InferOutput{
			Output: []byte(`{"haiku": "test haiku"}`),
			Cost: &flow.CostMetadata{
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		}, nil
	}

	agent := newTestForgeAgent(t, inferFn, cfg)

	laws := []*flowv1.Law{
		{Goal: "The haiku must evoke a season"},
		{Goal: "Use only natural imagery"},
	}

	_, err := agent.Run(context.Background(), "write a haiku", laws)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// Verify the query prompt contains the laws.
	query := string(capturedQuery)
	if !strings.Contains(query, "The haiku must evoke a season") {
		t.Fatalf("query prompt should contain first law, got: %s", query)
	}
	if !strings.Contains(query, "Use only natural imagery") {
		t.Fatalf("query prompt should contain second law, got: %s", query)
	}
}

func TestForgeAgent_SystemPromptContainsOutputField(t *testing.T) {
	cfg := defaultTestConfig()
	var capturedSystem string
	inferFn := func(_ context.Context, _, systemPrompt string, _ []byte) (*flow.InferOutput, error) {
		capturedSystem = systemPrompt
		return &flow.InferOutput{
			Output: []byte(`{"haiku": "test haiku"}`),
			Cost: &flow.CostMetadata{
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		}, nil
	}

	agent := newTestForgeAgent(t, inferFn, cfg)

	_, err := agent.Run(context.Background(), "write a haiku", nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// Verify the system prompt references the output field.
	if !strings.Contains(capturedSystem, `"haiku"`) {
		t.Fatalf("system prompt should contain output field name, got: %s", capturedSystem)
	}
}

// ---------------------------------------------------------------------------
// Tests — forgeOutputSchema
// ---------------------------------------------------------------------------

func TestForgeOutputSchema(t *testing.T) {
	schema := forgeOutputSchema("haiku")
	// Should be valid JSON.
	if !strings.Contains(string(schema), `"haiku"`) {
		t.Fatalf("schema should contain output field name, got: %s", schema)
	}
	if !strings.Contains(string(schema), `"additionalProperties":false`) {
		t.Fatalf("schema should disallow additional properties, got: %s", schema)
	}
	if !strings.Contains(string(schema), `"minLength":1`) {
		t.Fatalf("schema should require minLength 1, got: %s", schema)
	}
}

// ---------------------------------------------------------------------------
// Tests — ConfigMap Prompt Overrides
// ---------------------------------------------------------------------------

func TestForgeAgent_CustomSystemPromptOverride(t *testing.T) {
	customPrompt := `You are a poet. Output JSON with key "{{.OutputField}}".`
	cfg := &forgeConfig{
		InputArtefacts:   []string{"petition"},
		OutputArtefact:   "haiku",
		GovernedArtefact: "haiku",
		OutputField:      "haiku",
		SystemPrompt:     customPrompt,
	}

	var capturedSystem string
	inferFn := func(_ context.Context, _, systemPrompt string, _ []byte) (*flow.InferOutput, error) {
		capturedSystem = systemPrompt
		return &flow.InferOutput{
			Output: []byte(`{"haiku": "custom prompt haiku"}`),
			Cost: &flow.CostMetadata{
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		}, nil
	}

	agent := newTestForgeAgent(t, inferFn, cfg)

	_, err := agent.Run(context.Background(), "write a haiku", nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// The captured system prompt should contain the custom text, not the default.
	if !strings.Contains(capturedSystem, "You are a poet") {
		t.Fatalf("system prompt should contain custom override, got: %s", capturedSystem)
	}
	if strings.Contains(capturedSystem, "You are a creative writer") {
		t.Fatalf("system prompt should NOT contain default text when overridden, got: %s", capturedSystem)
	}
	// Template interpolation should still work — OutputField should be expanded.
	if !strings.Contains(capturedSystem, `"haiku"`) {
		t.Fatalf("system prompt should interpolate OutputField, got: %s", capturedSystem)
	}
}

func TestForgeAgent_CustomQueryTemplateOverride(t *testing.T) {
	customQuery := `Custom brief: {{.Input}}`
	cfg := &forgeConfig{
		InputArtefacts:   []string{"petition"},
		OutputArtefact:   "haiku",
		GovernedArtefact: "haiku",
		OutputField:      "haiku",
		QueryTemplate:    customQuery,
	}

	var capturedQuery []byte
	inferFn := func(_ context.Context, _, _ string, queryPrompt []byte) (*flow.InferOutput, error) {
		capturedQuery = queryPrompt
		return &flow.InferOutput{
			Output: []byte(`{"haiku": "custom query haiku"}`),
			Cost: &flow.CostMetadata{
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		}, nil
	}

	agent := newTestForgeAgent(t, inferFn, cfg)

	_, err := agent.Run(context.Background(), "write about cherry blossoms", nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	query := string(capturedQuery)
	if !strings.Contains(query, "Custom brief:") {
		t.Fatalf("query prompt should contain custom template text, got: %s", query)
	}
	if !strings.Contains(query, "write about cherry blossoms") {
		t.Fatalf("query prompt should contain the input, got: %s", query)
	}
	// Should NOT contain the default "Request:" prefix.
	if strings.Contains(query, "Request:") {
		t.Fatalf("query prompt should NOT contain default template text when overridden, got: %s", query)
	}
}

func TestForgeAgent_EmptyOverridesUseDefaults(t *testing.T) {
	cfg := &forgeConfig{
		InputArtefacts:   []string{"petition"},
		OutputArtefact:   "haiku",
		GovernedArtefact: "haiku",
		OutputField:      "haiku",
		SystemPrompt:     "", // empty — should use default
		QueryTemplate:    "", // empty — should use default
	}

	var capturedSystem string
	var capturedQuery []byte
	inferFn := func(_ context.Context, _, systemPrompt string, queryPrompt []byte) (*flow.InferOutput, error) {
		capturedSystem = systemPrompt
		capturedQuery = queryPrompt
		return &flow.InferOutput{
			Output: []byte(`{"haiku": "default prompt haiku"}`),
			Cost: &flow.CostMetadata{
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		}, nil
	}

	agent := newTestForgeAgent(t, inferFn, cfg)

	_, err := agent.Run(context.Background(), "write a haiku about rain", nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// System prompt should contain the default text.
	if !strings.Contains(capturedSystem, "You are a creative writer") {
		t.Fatalf("system prompt should use default when override is empty, got: %s", capturedSystem)
	}
	// Query prompt should contain the default "Request:" prefix.
	if !strings.Contains(string(capturedQuery), "Request:") {
		t.Fatalf("query prompt should use default when override is empty, got: %s", capturedQuery)
	}
}
