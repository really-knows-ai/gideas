package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"text/template"

	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestCodifySMT_HappyPath(t *testing.T) {
	spy := newCodifySpy()
	seedGoal(spy, "Enforce consistent naming conventions", []string{"haiku"}, 2, "create")

	client := setupCodifyTest(t, spy)

	smtContent := `; Goal: Enforce consistent naming conventions
(declare-sort Artefact 0)
(declare-fun naming-consistent (Artefact) Bool)
(assert (forall ((a Artefact)) (naming-consistent a)))`

	agentJSON := mustMarshal(t, agentOutput{SMTContent: smtContent})
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{Output: agentJSON}, nil
	}

	agent := buildTestAgent(t, client, inferFn, defaultSystemPrompt)

	cfg := &codifyConfig{}
	goal := &codificationGoal{
		Goal:      "Enforce consistent naming conventions",
		AppliesTo: []string{"haiku"},
		Tier:      2,
		Action:    "create",
	}

	err := runCodify(context.Background(), client, agent, cfg, goal)
	if err != nil {
		t.Fatalf("runCodify: %v", err)
	}

	// Verify codification-result was stored.
	stored, ok := spy.StoredArtefacts[artefactCodificationResult]
	if !ok {
		t.Fatal("codification-result artefact was not stored")
	}

	var result codificationResult
	if err := json.Unmarshal(stored, &result); err != nil {
		t.Fatalf("unmarshal stored result: %v", err)
	}
	if result.Type != "application/smt-lib" {
		t.Errorf("result type = %q, want %q", result.Type, "application/smt-lib")
	}
	if result.Content != smtContent {
		t.Errorf("result content = %q, want %q", result.Content, smtContent)
	}

	// Verify completion.
	if !spy.Completed {
		t.Error("Complete was not called")
	}
}

func TestCodifySMT_CustomOutputFormat(t *testing.T) {
	spy := newCodifySpy()
	seedGoal(spy, "Check quality", []string{"haiku"}, 1, "demote")

	client := setupCodifyTest(t, spy)

	agentJSON := mustMarshal(t, agentOutput{SMTContent: "(assert true)"})
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{Output: agentJSON}, nil
	}

	agent := buildTestAgent(t, client, inferFn, defaultSystemPrompt)

	cfg := &codifyConfig{OutputFormat: "application/custom-smt"}
	goal := &codificationGoal{
		Goal:      "Check quality",
		AppliesTo: []string{"haiku"},
		Tier:      1,
		Action:    "create",
	}

	err := runCodify(context.Background(), client, agent, cfg, goal)
	if err != nil {
		t.Fatalf("runCodify: %v", err)
	}

	stored := spy.StoredArtefacts[artefactCodificationResult]
	var result codificationResult
	if err := json.Unmarshal(stored, &result); err != nil {
		t.Fatalf("unmarshal stored result: %v", err)
	}
	if result.Type != "application/custom-smt" {
		t.Errorf("result type = %q, want %q", result.Type, "application/custom-smt")
	}
}

func TestCodifySMT_QueryIncludesGoalContext(t *testing.T) {
	spy := newCodifySpy()
	seedGoal(spy, "Enforce naming", []string{"haiku", "sonnet"}, 3, "create")

	client := setupCodifyTest(t, spy)

	agentJSON := mustMarshal(t, agentOutput{SMTContent: "(assert true)"})
	var capturedQuery []byte
	inferFn := func(_ context.Context, _, _ string, query []byte) (*flow.InferOutput, error) {
		capturedQuery = query
		return &flow.InferOutput{Output: agentJSON}, nil
	}

	agent := buildTestAgent(t, client, inferFn, defaultSystemPrompt)

	cfg := &codifyConfig{}
	goal := &codificationGoal{
		Goal:      "Enforce naming",
		AppliesTo: []string{"haiku", "sonnet"},
		Tier:      3,
		Action:    "create",
	}

	err := runCodify(context.Background(), client, agent, cfg, goal)
	if err != nil {
		t.Fatalf("runCodify: %v", err)
	}

	queryStr := string(capturedQuery)
	if !containsSubstring(queryStr, "Enforce naming") {
		t.Error("query does not include goal")
	}
	if !containsSubstring(queryStr, "haiku") {
		t.Error("query does not include applies_to entries")
	}
	if !containsSubstring(queryStr, "sonnet") {
		t.Error("query does not include second applies_to entry")
	}
}

func TestCodifySMT_SystemPromptUsesDefault(t *testing.T) {
	spy := newCodifySpy()
	seedGoal(spy, "Some goal", []string{"haiku"}, 1, "create")

	client := setupCodifyTest(t, spy)

	agentJSON := mustMarshal(t, agentOutput{SMTContent: "(assert true)"})
	var capturedSystem string
	inferFn := func(_ context.Context, _, systemPrompt string, _ []byte) (*flow.InferOutput, error) {
		capturedSystem = systemPrompt
		return &flow.InferOutput{Output: agentJSON}, nil
	}

	agent := buildTestAgent(t, client, inferFn, defaultSystemPrompt)

	cfg := &codifyConfig{}
	goal := &codificationGoal{
		Goal:      "Some goal",
		AppliesTo: []string{"haiku"},
		Tier:      1,
		Action:    "create",
	}

	err := runCodify(context.Background(), client, agent, cfg, goal)
	if err != nil {
		t.Fatalf("runCodify: %v", err)
	}

	if !containsSubstring(capturedSystem, "SMT-LIB") {
		t.Error("default system prompt should mention SMT-LIB")
	}
}

func TestCodifySMT_CustomSystemPrompt(t *testing.T) {
	spy := newCodifySpy()
	seedGoal(spy, "Some goal", []string{"haiku"}, 1, "create")

	client := setupCodifyTest(t, spy)

	agentJSON := mustMarshal(t, agentOutput{SMTContent: "(assert true)"})
	var capturedSystem string
	inferFn := func(_ context.Context, _, systemPrompt string, _ []byte) (*flow.InferOutput, error) {
		capturedSystem = systemPrompt
		return &flow.InferOutput{Output: agentJSON}, nil
	}

	customPrompt := "You are a custom SMT translator."
	agent := buildTestAgent(t, client, inferFn, customPrompt)

	cfg := &codifyConfig{SystemPrompt: customPrompt}
	goal := &codificationGoal{
		Goal:      "Some goal",
		AppliesTo: []string{"haiku"},
		Tier:      1,
		Action:    "create",
	}

	err := runCodify(context.Background(), client, agent, cfg, goal)
	if err != nil {
		t.Fatalf("runCodify: %v", err)
	}

	if capturedSystem != customPrompt {
		t.Errorf("system prompt = %q, want %q", capturedSystem, customPrompt)
	}
}

func TestCodifySMT_Error_GoalArtefactMissing(t *testing.T) {
	spy := newCodifySpy()
	// Deliberately don't seed the goal artefact.

	client := setupCodifyTest(t, spy)
	cfg := &codifyConfig{}

	err := handleCodify(context.Background(), client, cfg)
	if err == nil {
		t.Fatal("expected error when goal artefact missing")
	}
	if !containsSubstring(err.Error(), "codification-goal") {
		t.Errorf("error should mention codification-goal: %v", err)
	}
}

func TestCodifySMT_Error_GoalArtefactInvalidJSON(t *testing.T) {
	spy := newCodifySpy()
	spy.Artefacts[artefactCodificationGoal] = []byte("not-json")

	client := setupCodifyTest(t, spy)
	cfg := &codifyConfig{}

	err := handleCodify(context.Background(), client, cfg)
	if err == nil {
		t.Fatal("expected error when goal artefact is invalid JSON")
	}
	if !containsSubstring(err.Error(), "parse codification-goal") {
		t.Errorf("error should mention parse: %v", err)
	}
}

func TestCodifySMT_Error_GoalFieldEmpty(t *testing.T) {
	spy := newCodifySpy()
	seedGoal(spy, "", []string{"haiku"}, 1, "create")

	client := setupCodifyTest(t, spy)
	cfg := &codifyConfig{}

	err := handleCodify(context.Background(), client, cfg)
	if err == nil {
		t.Fatal("expected error when goal field is empty")
	}
	if !containsSubstring(err.Error(), "empty goal") {
		t.Errorf("error should mention empty goal: %v", err)
	}
}

func TestCodifySMT_Error_AgentInferFails(t *testing.T) {
	spy := newCodifySpy()
	seedGoal(spy, "Some goal", []string{"haiku"}, 1, "create")

	client := setupCodifyTest(t, spy)

	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return nil, fmt.Errorf("inference exploded")
	}

	agent := buildTestAgent(t, client, inferFn, defaultSystemPrompt)

	cfg := &codifyConfig{}
	goal := &codificationGoal{
		Goal:      "Some goal",
		AppliesTo: []string{"haiku"},
		Tier:      1,
		Action:    "create",
	}

	err := runCodify(context.Background(), client, agent, cfg, goal)
	if err == nil {
		t.Fatal("expected error when inference fails")
	}
	if !containsSubstring(err.Error(), "agent run") {
		t.Errorf("error should mention agent run: %v", err)
	}
}

func TestCodifySMT_Error_AgentOutputEmpty(t *testing.T) {
	spy := newCodifySpy()
	seedGoal(spy, "Some goal", []string{"haiku"}, 1, "create")

	client := setupCodifyTest(t, spy)

	// Agent returns valid JSON but with empty smt_content.
	agentJSON := mustMarshal(t, agentOutput{SMTContent: ""})
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{Output: agentJSON}, nil
	}

	// The schema requires minLength=1 for smt_content, so the agent
	// framework will reject it during validation. We test this path.
	agent := buildTestAgent(t, client, inferFn, defaultSystemPrompt)

	cfg := &codifyConfig{}
	goal := &codificationGoal{
		Goal:      "Some goal",
		AppliesTo: []string{"haiku"},
		Tier:      1,
		Action:    "create",
	}

	err := runCodify(context.Background(), client, agent, cfg, goal)
	if err == nil {
		t.Fatal("expected error when smt_content is empty")
	}
}

func TestCodifySMT_Error_StoreOrCompleteFails(t *testing.T) {
	tests := []struct {
		name     string
		setupSpy func(*codifySpy)
		errMsg   string
		errMatch string
	}{
		{"store result fails", func(s *codifySpy) { s.StoreArtefactErr = status.Errorf(codes.Internal, "store broken") },
			"expected error when store fails", "store codification-result"},
		{"complete fails", func(s *codifySpy) { s.CompleteErr = status.Errorf(codes.Internal, "complete broken") },
			"expected error when complete fails", "complete"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := newCodifySpy()
			seedGoal(spy, "Some goal", []string{"haiku"}, 1, "create")
			tt.setupSpy(spy)

			client := setupCodifyTest(t, spy)

			agentJSON := mustMarshal(t, agentOutput{SMTContent: "(assert true)"})
			inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
				return &flow.InferOutput{Output: agentJSON}, nil
			}

			agent := buildTestAgent(t, client, inferFn, defaultSystemPrompt)

			cfg := &codifyConfig{}
			goal := &codificationGoal{
				Goal:      "Some goal",
				AppliesTo: []string{"haiku"},
				Tier:      1,
				Action:    "create",
			}

			err := runCodify(context.Background(), client, agent, cfg, goal)
			if err == nil {
				t.Fatal(tt.errMsg)
			}
			if !containsSubstring(err.Error(), tt.errMatch) {
				t.Errorf("error should mention %s: %v", tt.errMatch, err)
			}
		})
	}
}

func TestCodifyConfig_DefaultOutputFormat(t *testing.T) {
	cfg := &codifyConfig{}
	if cfg.outputFormat() != "application/smt-lib" {
		t.Errorf("default outputFormat = %q, want %q", cfg.outputFormat(), "application/smt-lib")
	}
}

func TestCodifyConfig_CustomOutputFormat(t *testing.T) {
	cfg := &codifyConfig{OutputFormat: "application/custom"}
	if cfg.outputFormat() != "application/custom" {
		t.Errorf("outputFormat = %q, want %q", cfg.outputFormat(), "application/custom")
	}
}

func TestCodifyConfig_DefaultSystemPrompt(t *testing.T) {
	cfg := &codifyConfig{}
	if cfg.systemPrompt() != defaultSystemPrompt {
		t.Error("default system prompt does not match")
	}
}

func TestCodifyConfig_CustomSystemPrompt(t *testing.T) {
	cfg := &codifyConfig{SystemPrompt: "Custom prompt"}
	if cfg.systemPrompt() != "Custom prompt" {
		t.Errorf("systemPrompt = %q, want %q", cfg.systemPrompt(), "Custom prompt")
	}
}

func TestBuildOutputSchema_Valid(t *testing.T) {
	schemaBytes, err := buildOutputSchema()
	if err != nil {
		t.Fatalf("buildOutputSchema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}

	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema missing properties")
	}
	if _, ok := props["smt_content"]; !ok {
		t.Fatal("schema missing smt_content property")
	}

	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatal("schema missing required")
	}
	if len(required) != 1 || required[0] != "smt_content" {
		t.Errorf("required = %v, want [smt_content]", required)
	}
}

// ---------------------------------------------------------------------------
// Test Helpers
// ---------------------------------------------------------------------------

// buildTestAgent creates a FoundryAgent with a custom InferFunc for testing.
func buildTestAgent(t *testing.T, client *flow.Client, inferFn flow.InferFunc, sysPrompt string) *flow.Agent {
	t.Helper()

	schemaBytes, err := buildOutputSchema()
	if err != nil {
		t.Fatalf("buildOutputSchema: %v", err)
	}
	queryTmpl, err := template.New("codify-query").Parse(queryTemplateText)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	agent, err := flow.NewAgent(client,
		flow.WithSchema(schemaBytes),
		flow.WithModelName("test-model"),
		flow.WithSystemPrompt(sysPrompt),
		flow.WithQueryTemplate(queryTmpl),
	)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	flow.OverrideModelForTest(agent, inferFn)
	return agent
}

// mustMarshal is a test helper that marshals v to JSON or fails the test.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return data
}

// containsSubstring checks if s contains substr.
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && contains(s, substr))
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
