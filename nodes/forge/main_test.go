package main

import (
	"context"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// Mock Model for forge tests
// ---------------------------------------------------------------------------

type mockModel struct {
	output *flow.InferOutput
	err    error

	capturedSystem string
	capturedQuery  []byte
}

func (m *mockModel) Infer(
	_ context.Context, systemPrompt string, queryPrompt []byte,
) (*flow.InferOutput, error) {
	m.capturedSystem = systemPrompt
	m.capturedQuery = queryPrompt
	return m.output, m.err
}

// ---------------------------------------------------------------------------
// Test helper — creates a ForgeAgent with a mock model
// ---------------------------------------------------------------------------

func newTestForgeAgent(t *testing.T, mm *mockModel, cfg *forgeConfig) *ForgeAgent {
	t.Helper()
	client := newSpyClient(t)
	agent, err := NewForgeAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewForgeAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(agent.agent, mm)
	return agent
}

func defaultTestConfig() *forgeConfig {
	return &forgeConfig{
		InputArtefact:    "petition",
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
	mm := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"haiku": "autumn moonlight\na worm digs silently\ninto the chestnut"}`),
			Cost: &flow.CostMetadata{
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		},
	}

	agent := newTestForgeAgent(t, mm, cfg)

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
	mm := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"haiku": ""}`),
			Cost: &flow.CostMetadata{
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		},
	}

	agent := newTestForgeAgent(t, mm, cfg)

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
	mm := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"poem": "not a haiku"}`),
			Cost: &flow.CostMetadata{
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		},
	}

	agent := newTestForgeAgent(t, mm, cfg)

	_, err := agent.Run(context.Background(), "write a haiku", nil)
	if err == nil {
		t.Fatal("expected missing field to fail schema validation")
	}
}

func TestForgeAgent_RejectsAdditionalProperties(t *testing.T) {
	cfg := defaultTestConfig()
	mm := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"haiku": "test haiku", "extra": "not allowed"}`),
			Cost: &flow.CostMetadata{
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		},
	}

	agent := newTestForgeAgent(t, mm, cfg)

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
		InputArtefact:    "petition",
		OutputArtefact:   "document",
		GovernedArtefact: "document",
		OutputField:      "document",
	}

	mm := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"document": "generated document content"}`),
			Cost: &flow.CostMetadata{
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		},
	}

	agent := newTestForgeAgent(t, mm, cfg)

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
	mm := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"haiku": "test haiku"}`),
			Cost: &flow.CostMetadata{
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		},
	}

	agent := newTestForgeAgent(t, mm, cfg)

	_, err := agent.Run(context.Background(), "write about autumn leaves", nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// Verify the query prompt contains the input.
	if !strings.Contains(string(mm.capturedQuery), "write about autumn leaves") {
		t.Fatalf("query prompt should contain input, got: %s", mm.capturedQuery)
	}
}

func TestForgeAgent_PromptContainsLaws(t *testing.T) {
	cfg := defaultTestConfig()
	mm := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"haiku": "test haiku"}`),
			Cost: &flow.CostMetadata{
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		},
	}

	agent := newTestForgeAgent(t, mm, cfg)

	laws := []*flowv1.Law{
		{Goal: "The haiku must evoke a season"},
		{Goal: "Use only natural imagery"},
	}

	_, err := agent.Run(context.Background(), "write a haiku", laws)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// Verify the query prompt contains the laws.
	query := string(mm.capturedQuery)
	if !strings.Contains(query, "The haiku must evoke a season") {
		t.Fatalf("query prompt should contain first law, got: %s", query)
	}
	if !strings.Contains(query, "Use only natural imagery") {
		t.Fatalf("query prompt should contain second law, got: %s", query)
	}
}

func TestForgeAgent_SystemPromptContainsOutputField(t *testing.T) {
	cfg := defaultTestConfig()
	mm := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"haiku": "test haiku"}`),
			Cost: &flow.CostMetadata{
				Model:        "test-model",
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		},
	}

	agent := newTestForgeAgent(t, mm, cfg)

	_, err := agent.Run(context.Background(), "write a haiku", nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// Verify the system prompt references the output field.
	if !strings.Contains(mm.capturedSystem, `"haiku"`) {
		t.Fatalf("system prompt should contain output field name, got: %s", mm.capturedSystem)
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
