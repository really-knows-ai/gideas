package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gideas/flow/nodes/internal/handlers"
	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// Tests — AppraiserAgent: Schema Validation via Run()
// ---------------------------------------------------------------------------

func TestAppraiserAgent_ValidOutput(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"feedback": [{"message": "issue found", "cited_laws": []}]}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestAppraiserAgent(t, inferFn, spy, cfg, "", nil)

	out, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err != nil {
		t.Fatalf("expected valid output, got error: %v", err)
	}
	if len(out.Feedback) != 1 {
		t.Fatalf("expected 1 feedback item, got %d", len(out.Feedback))
	}
	if out.Feedback[0].Message != "issue found" {
		t.Fatalf("expected message 'issue found', got %q", out.Feedback[0].Message)
	}
}

func TestAppraiserAgent_EmptyFeedback(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestAppraiserAgent(t, inferFn, spy, cfg, "", nil)

	out, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err != nil {
		t.Fatalf("expected valid output, got error: %v", err)
	}
	if len(out.Feedback) != 0 {
		t.Fatalf("expected 0 feedback items, got %d", len(out.Feedback))
	}
}

func TestAppraiserAgent_RejectsEmptyMessage(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"feedback": [{"message": "", "cited_laws": []}]}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestAppraiserAgent(t, inferFn, spy, cfg, "", nil)

	_, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err == nil {
		t.Fatal("expected empty message to fail schema validation")
	}
}

func TestAppraiserAgent_RejectsAdditionalProperties(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"feedback": [], "extra": "bad"}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestAppraiserAgent(t, inferFn, spy, cfg, "", nil)

	_, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err == nil {
		t.Fatal("expected additional properties to fail schema validation")
	}
}

// ---------------------------------------------------------------------------
// Tests — AppraiserAgent: Template Rendering
// ---------------------------------------------------------------------------

func TestAppraiserAgent_PromptContainsLawsAndHistory(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()
	var capturedQuery []byte
	inferFn := func(_ context.Context, _, _ string, queryPrompt []byte) (*flow.InferOutput, error) {
		capturedQuery = queryPrompt
		return &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestAppraiserAgent(t, inferFn, spy, cfg, "", nil)

	laws := []flow.ReviewLaw{
		{ID: "law-1", Tier: 2, Goal: "Must evoke a season"},
		{ID: "law-2", Tier: 1, Goal: "Use natural imagery"},
	}
	history := []flow.ReviewHistory{
		{State: "RESOLVED", Message: "old issue fixed"},
	}

	_, err := agent.Run(context.Background(), "write about autumn", "autumn moon", laws, history)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	query := string(capturedQuery)
	checks := []string{
		"Must evoke a season",
		"Use natural imagery",
		"law-1",
		"law-2",
		"old issue fixed",
		"Do NOT re-raise",
		"write about autumn",
		"autumn moon",
	}
	for _, want := range checks {
		if !strings.Contains(query, want) {
			t.Errorf("query prompt should contain %q, got:\n%s", want, query)
		}
	}
}

func TestAppraiserAgent_PromptOmitsEmptySections(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()
	var capturedQuery []byte
	inferFn := func(_ context.Context, _, _ string, queryPrompt []byte) (*flow.InferOutput, error) {
		capturedQuery = queryPrompt
		return &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestAppraiserAgent(t, inferFn, spy, cfg, "", nil)

	_, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	query := string(capturedQuery)
	if strings.Contains(query, "GOVERNANCE LAWS") {
		t.Errorf("query should not contain GOVERNANCE LAWS when no laws, got:\n%s", query)
	}
	if strings.Contains(query, "PREVIOUS FEEDBACK HISTORY") {
		t.Errorf("query should not contain PREVIOUS FEEDBACK HISTORY when no feedback, got:\n%s", query)
	}
}

func TestAppraiserAgent_AppraiserPersonalityInSystemPrompt(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()
	var capturedSystem string
	inferFn := func(_ context.Context, _, systemPrompt string, _ []byte) (*flow.InferOutput, error) {
		capturedSystem = systemPrompt
		return &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	personality := "Pay special attention to information disclosure and injection risks."
	agent := newTestAppraiserAgent(t, inferFn, spy, cfg, personality, nil)

	_, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(capturedSystem, personality) {
		t.Errorf("system prompt should contain personality %q, got:\n%s",
			personality, capturedSystem)
	}
}

func TestAppraiserAgent_EmptyPersonalityOmitted(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()
	var capturedSystem string
	inferFn := func(_ context.Context, _, systemPrompt string, _ []byte) (*flow.InferOutput, error) {
		capturedSystem = systemPrompt
		return &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestAppraiserAgent(t, inferFn, spy, cfg, "", nil)

	_, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// With no personality, the system prompt should end cleanly after the deviation list.
	if strings.Contains(capturedSystem, "Pay special attention") {
		t.Errorf("system prompt should not contain personality when empty, got:\n%s",
			capturedSystem)
	}
}

func TestAppraiserAgent_SystemPromptContainsConfig(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()
	var capturedSystem string
	inferFn := func(_ context.Context, _, systemPrompt string, _ []byte) (*flow.InferOutput, error) {
		capturedSystem = systemPrompt
		return &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestAppraiserAgent(t, inferFn, spy, cfg, "", nil)

	_, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(capturedSystem, "review") {
		t.Errorf("system prompt should contain review artefact name, got:\n%s",
			capturedSystem)
	}
}

// ---------------------------------------------------------------------------
// Tests — Agent Integration
// ---------------------------------------------------------------------------

func TestAppraiserAgent_HappyPath(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()

	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"feedback": [{"message": "weak imagery", "cited_laws": ["law-1"]}]}`),
			Cost:   defaultCost(),
		}, nil
	}

	client := newSpyClient(t, spy)

	// Create agent with appraiser suffix and override model.
	agent, err := NewAppraiserAgent(client, cfg, "Focus on security risks.", nil)
	if err != nil {
		t.Fatalf("NewAppraiserAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(agent.agent, inferFn)

	laws := []flow.ReviewLaw{{ID: "law-1", Tier: 2, Goal: "Must evoke a season"}}
	history := []flow.ReviewHistory{{State: "RESOLVED", Message: "old issue fixed"}}

	out, err := agent.Run(context.Background(), "write about autumn", "autumn moon\nsilent night", laws, history)
	if err != nil {
		t.Fatalf("agent.Run() returned error: %v", err)
	}

	if len(out.Feedback) != 1 {
		t.Fatalf("expected 1 feedback item, got %d", len(out.Feedback))
	}
	if out.Feedback[0].Message != "weak imagery" {
		t.Fatalf("expected 'weak imagery', got %q", out.Feedback[0].Message)
	}

	// Verify the review result can be serialized.
	outJSON, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("failed to marshal review output: %v", err)
	}

	// Verify it round-trips.
	var parsed flow.ReviewResult
	if err := json.Unmarshal(outJSON, &parsed); err != nil {
		t.Fatalf("failed to unmarshal review output: %v", err)
	}
	if len(parsed.Feedback) != 1 {
		t.Fatalf("round-trip: expected 1 feedback item, got %d", len(parsed.Feedback))
	}
}

func TestAppraiserAgent_EmptyLaws(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestAppraiserAgent(t, inferFn, spy, cfg, "", nil)

	// Empty laws — reviewer should still work.
	out, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if len(out.Feedback) != 0 {
		t.Fatalf("expected 0 feedback items, got %d", len(out.Feedback))
	}
}

func TestAppraiserAgent_ReviewOutputFormat(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"feedback": [
				{"message": "issue one", "cited_laws": []},
				{"message": "issue two", "cited_laws": ["law-1", "law-2"]}
			]}`),
			Cost: defaultCost(),
		}, nil
	}

	agent := newTestAppraiserAgent(t, inferFn, spy, cfg, "", nil)

	out, err := agent.Run(context.Background(), "petition", "content",
		[]flow.ReviewLaw{{ID: "law-1", Tier: 2, Goal: "test"}},
		nil,
	)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if len(out.Feedback) != 2 {
		t.Fatalf("expected 2 feedback items, got %d", len(out.Feedback))
	}

	// Verify first item.
	if out.Feedback[0].Message != "issue one" {
		t.Fatalf("expected 'issue one', got %q", out.Feedback[0].Message)
	}
	if len(out.Feedback[0].CitedLaws) != 0 {
		t.Fatalf("expected 0 cited laws, got %d", len(out.Feedback[0].CitedLaws))
	}

	// Verify second item.
	if out.Feedback[1].Message != "issue two" {
		t.Fatalf("expected 'issue two', got %q", out.Feedback[1].Message)
	}
	if len(out.Feedback[1].CitedLaws) != 2 {
		t.Fatalf("expected 2 cited laws, got %d", len(out.Feedback[1].CitedLaws))
	}
}

// ---------------------------------------------------------------------------
// Tests — Handler types deserialization (handlers package types)
// ---------------------------------------------------------------------------

func TestAppraiserPersonalityData_Deserialization(t *testing.T) {
	tests := []struct {
		name                string
		json                string
		expectedID          string
		expectedPersonality string
	}{
		{
			name:                "with personality",
			json:                `{"id":"skeptic","personality":"Focus on security."}`,
			expectedID:          "skeptic",
			expectedPersonality: "Focus on security.",
		},
		{
			name:                "empty personality",
			json:                `{"id":"auditor","personality":""}`,
			expectedID:          "auditor",
			expectedPersonality: "",
		},
		{
			name:                "empty id",
			json:                `{"id":"","personality":"Strict."}`,
			expectedID:          "",
			expectedPersonality: "Strict.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d handlers.AppraiserPersonalityData
			if err := json.Unmarshal([]byte(tt.json), &d); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}
			if d.ID != tt.expectedID {
				t.Fatalf("expected id %q, got %q", tt.expectedID, d.ID)
			}
			if d.Personality != tt.expectedPersonality {
				t.Fatalf("expected personality %q, got %q", tt.expectedPersonality, d.Personality)
			}
		})
	}
}

func TestLawData_Deserialization(t *testing.T) {
	input := `[{"id":"law-1","tier":2,"goal":"Must evoke a season"},{"id":"law-2","tier":1,"goal":"Use imagery"}]`
	var laws []handlers.LawData
	if err := json.Unmarshal([]byte(input), &laws); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(laws) != 2 {
		t.Fatalf("expected 2 laws, got %d", len(laws))
	}
	if laws[0].ID != "law-1" || laws[0].Tier != 2 || laws[0].Goal != "Must evoke a season" {
		t.Fatalf("unexpected law[0]: %+v", laws[0])
	}
}

func TestHistoryData_Deserialization(t *testing.T) {
	input := `[{"state":"RESOLVED","message":"old issue fixed"}]`
	var history []handlers.HistoryData
	if err := json.Unmarshal([]byte(input), &history); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 history item, got %d", len(history))
	}
	if history[0].State != "RESOLVED" || history[0].Message != "old issue fixed" {
		t.Fatalf("unexpected history[0]: %+v", history[0])
	}
}

// ---------------------------------------------------------------------------
// Tests — ConfigMap prompt overrides
// ---------------------------------------------------------------------------

func TestAppraiserAgent_SystemPromptOverride(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()
	var capturedSystem string
	inferFn := func(_ context.Context, _, systemPrompt string, _ []byte) (*flow.InferOutput, error) {
		capturedSystem = systemPrompt
		return &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	customSystem := `You are a custom reviewer.
{{- if .AppraiserPersonality}}

{{.AppraiserPersonality}}
{{- end}}`

	opts := &AppraiserAgentOpts{SystemPrompt: customSystem}
	agent := newTestAppraiserAgent(t, inferFn, spy, cfg, "", opts)

	_, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(capturedSystem, "custom reviewer") {
		t.Errorf("system prompt should contain custom override, got:\n%s", capturedSystem)
	}
	// The default template text should NOT be present.
	if strings.Contains(capturedSystem, "governed creative pipeline") {
		t.Errorf("system prompt should not contain default template text when overridden, got:\n%s", capturedSystem)
	}
}

func TestAppraiserAgent_QueryTemplateOverride(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()
	var capturedQuery []byte
	inferFn := func(_ context.Context, _, _ string, queryPrompt []byte) (*flow.InferOutput, error) {
		capturedQuery = queryPrompt
		return &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	customQuery := `CUSTOM QUERY: {{.InputContent}} | {{.ReviewContent}}`

	opts := &AppraiserAgentOpts{QueryTemplate: customQuery}
	agent := newTestAppraiserAgent(t, inferFn, spy, cfg, "", opts)

	_, err := agent.Run(context.Background(), "my petition", "my content", nil, nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	query := string(capturedQuery)
	if !strings.Contains(query, "CUSTOM QUERY:") {
		t.Errorf("query should contain custom override, got:\n%s", query)
	}
	if !strings.Contains(query, "my petition") {
		t.Errorf("query should contain input content, got:\n%s", query)
	}
	if !strings.Contains(query, "my content") {
		t.Errorf("query should contain review content, got:\n%s", query)
	}
}

func TestAppraiserAgent_NilOptsUsesDefaults(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()
	var capturedSystem string
	inferFn := func(_ context.Context, _, systemPrompt string, _ []byte) (*flow.InferOutput, error) {
		capturedSystem = systemPrompt
		return &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	// nil opts — should use baked-in defaults.
	agent := newTestAppraiserAgent(t, inferFn, spy, cfg, "", nil)

	_, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(capturedSystem, "governed creative pipeline") {
		t.Errorf("system prompt should contain default template text, got:\n%s", capturedSystem)
	}
}

func TestAppraiserAgent_SystemPromptOverrideWithPersonality(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()
	var capturedSystem string
	inferFn := func(_ context.Context, _, systemPrompt string, _ []byte) (*flow.InferOutput, error) {
		capturedSystem = systemPrompt
		return &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	customSystem := `Custom reviewer.
{{- if .AppraiserPersonality}}

Personality: {{.AppraiserPersonality}}
{{- end}}`

	personality := "Focus on security."
	opts := &AppraiserAgentOpts{SystemPrompt: customSystem}
	agent := newTestAppraiserAgent(t, inferFn, spy, cfg, personality, opts)

	_, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(capturedSystem, "Personality: Focus on security.") {
		t.Errorf("system prompt should render personality in custom template, got:\n%s", capturedSystem)
	}
}

// ---------------------------------------------------------------------------
// Tests — Appraiser Personality
// ---------------------------------------------------------------------------

func TestAppraiserAgent_AppraiserPersonalityPresent(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()
	var capturedSystem string
	inferFn := func(_ context.Context, _, systemPrompt string, _ []byte) (*flow.InferOutput, error) {
		capturedSystem = systemPrompt
		return &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	personality := "You are strict but fair. Evaluate every claim."
	agent := newTestAppraiserAgent(t, inferFn, spy, cfg, personality, nil)

	_, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(capturedSystem, personality) {
		t.Errorf("system prompt should contain appraiser personality %q, got:\n%s",
			personality, capturedSystem)
	}
}

func TestAppraiserAgent_NoAppraiserPersonality_DefaultPrompt(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()
	var capturedSystem string
	inferFn := func(_ context.Context, _, systemPrompt string, _ []byte) (*flow.InferOutput, error) {
		capturedSystem = systemPrompt
		return &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	// Empty personality — should use default system prompt.
	agent := newTestAppraiserAgent(t, inferFn, spy, cfg, "", nil)

	_, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(capturedSystem, "governed creative pipeline") {
		t.Errorf("system prompt should contain default text when personality is empty, got:\n%s",
			capturedSystem)
	}
}

// ---------------------------------------------------------------------------
// Tests — Appraiser and Pass in Handler Output
// ---------------------------------------------------------------------------

func TestReviewOutput_ContainsAppraiserAndPass(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"feedback": [{"message": "test", "cited_laws": []}]}`),
			Cost:   defaultCost(),
		}, nil
	}

	// Set up appraiser and pass artefacts in the spy.
	spy.ArtefactContents[handlers.ArtefactAppraiserPersonality] = []byte(
		`{"id":"skeptic","personality":"You are strict."}`)
	spy.ArtefactContents[handlers.ArtefactPass] = []byte(
		`{"pass":2,"of":3}`)
	spy.ArtefactContents[handlers.ArtefactLaws] = []byte(`[]`)
	spy.ArtefactContents[handlers.ArtefactHistory] = []byte(`[]`)
	spy.ArtefactContents["input"] = []byte("test petition")
	spy.ArtefactContents["review"] = []byte("test content")

	client := newSpyClient(t, spy)
	agent := newTestAppraiserAgent(t, inferFn, spy, cfg, "You are strict.", nil)

	handlerCfg := handlers.ReviewConfig{
		InputArtefacts: cfg.InputArtefacts,
		ReviewArtefact: cfg.ReviewArtefact,
	}

	err := handlers.HandleReview(context.Background(), client, agent, handlerCfg)
	if err != nil {
		t.Fatalf("HandleReview() returned error: %v", err)
	}

	// Check the stored review-output artefact.
	stored, ok := spy.StoredArtefacts[handlers.ArtefactReviewOutput]
	if !ok {
		t.Fatal("review-output artefact was not stored")
	}

	var output map[string]any
	if err := json.Unmarshal(stored, &output); err != nil {
		t.Fatalf("failed to unmarshal stored artefact: %v", err)
	}

	if appraiser, exists := output["appraiser"]; !exists || appraiser != "skeptic" {
		t.Errorf("expected appraiser 'skeptic' in output, got %v", appraiser)
	}

	// Pass is int; json unmarshals to float64.
	if pass, exists := output["pass"]; !exists || int(pass.(float64)) != 2 {
		t.Errorf("expected pass 2 in output, got %v", pass)
	}
}

func TestReviewOutput_OmitsPassWhenAbsent(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	// Set up artefacts WITHOUT a pass artefact.
	spy.ArtefactContents[handlers.ArtefactAppraiserPersonality] = []byte(
		`{"id":"auditor","personality":"Audit for compliance."}`)
	spy.ArtefactContents[handlers.ArtefactLaws] = []byte(`[]`)
	spy.ArtefactContents[handlers.ArtefactHistory] = []byte(`[]`)
	spy.ArtefactContents["input"] = []byte("test petition")
	spy.ArtefactContents["review"] = []byte("test content")

	client := newSpyClient(t, spy)
	agent := newTestAppraiserAgent(t, inferFn, spy, cfg, "", nil)

	handlerCfg := handlers.ReviewConfig{
		InputArtefacts: cfg.InputArtefacts,
		ReviewArtefact: cfg.ReviewArtefact,
	}

	err := handlers.HandleReview(context.Background(), client, agent, handlerCfg)
	if err != nil {
		t.Fatalf("HandleReview() returned error: %v", err)
	}

	stored, ok := spy.StoredArtefacts[handlers.ArtefactReviewOutput]
	if !ok {
		t.Fatal("review-output artefact was not stored")
	}

	var output map[string]any
	if err := json.Unmarshal(stored, &output); err != nil {
		t.Fatalf("failed to unmarshal stored artefact: %v", err)
	}

	if _, exists := output["pass"]; exists {
		t.Errorf("expected pass field to be omitted when pass artefact absent, got %v", output["pass"])
	}

	if appraiser, exists := output["appraiser"]; !exists || appraiser != "auditor" {
		t.Errorf("expected appraiser 'auditor' in output, got %v", appraiser)
	}
}

// ---------------------------------------------------------------------------
// Tests — Appraiser Artefact (Backward Compat)
// ---------------------------------------------------------------------------

func TestReviewFlow_WithoutAppraiserArtefact(t *testing.T) {
	// Verify the reviewer works when the parent sends appraiser+pass
	// artefacts (new flow) and no appraiser artefact is present.
	cfg := defaultTestConfig()
	spy := newAppraiserSpy()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	spy.ArtefactContents[handlers.ArtefactAppraiserPersonality] = []byte(
		`{"id":"skeptic","personality":"You are strict."}`)
	spy.ArtefactContents[handlers.ArtefactPass] = []byte(
		`{"pass":1,"of":1}`)
	spy.ArtefactContents[handlers.ArtefactLaws] = []byte(`[]`)
	spy.ArtefactContents[handlers.ArtefactHistory] = []byte(`[]`)
	spy.ArtefactContents["input"] = []byte("test petition")
	spy.ArtefactContents["review"] = []byte("test content")

	client := newSpyClient(t, spy)
	agent := newTestAppraiserAgent(t, inferFn, spy, cfg, "You are strict.", nil)

	handlerCfg := handlers.ReviewConfig{
		InputArtefacts: cfg.InputArtefacts,
		ReviewArtefact: cfg.ReviewArtefact,
	}

	// Should succeed without appraiser artefact.
	err := handlers.HandleReview(context.Background(), client, agent, handlerCfg)
	if err != nil {
		t.Fatalf("HandleReview() failed without appraiser artefact: %v", err)
	}

	if _, ok := spy.StoredArtefacts[handlers.ArtefactReviewOutput]; !ok {
		t.Fatal("expected review-output artefact to be stored")
	}
}
