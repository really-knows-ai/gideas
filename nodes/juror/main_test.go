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
// Mock Model (same pattern as forge tests)
// ---------------------------------------------------------------------------

type mockModel struct {
	output         *flow.InferOutput
	err            error
	capturedSystem string
	capturedQuery  []byte
}

func (m *mockModel) Infer(_ context.Context, systemPrompt string, query []byte) (*flow.InferOutput, error) {
	m.capturedSystem = systemPrompt
	m.capturedQuery = query
	if m.err != nil {
		return nil, m.err
	}
	return m.output, nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestJuror_HappyPath(t *testing.T) {
	spy := newJurorSpy()
	seedArtefacts(spy, "Should the feedback be upheld?", "Some evidence",
		[]string{"favour_refiner", "favour_reviewer"}, "")

	client := setupJurorTest(t, spy)

	verdictJSON := `{"outcome":"favour_refiner","reasoning":"The refiner's argument is stronger."}`
	mm := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(verdictJSON),
		},
	}

	// Build agent manually (same as handleJuror does) so we can inject mock.
	schemaBytes, err := buildOutputSchema([]string{"favour_refiner", "favour_reviewer"})
	if err != nil {
		t.Fatalf("buildOutputSchema: %v", err)
	}
	queryTmpl, err := template.New("juror-query").Parse(queryTemplateText)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	agent, err := flow.NewAgent(client,
		flow.WithSchema(schemaBytes),
		flow.WithModel(mm),
		flow.WithSystemPrompt(defaultPrompts["textualist"]),
		flow.WithQueryTemplate(queryTmpl),
	)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	err = runJuror(
		context.Background(), client, agent,
		"Should the feedback be upheld?", "Some evidence",
		[]string{"favour_refiner", "favour_reviewer"}, "",
	)
	if err != nil {
		t.Fatalf("runJuror: %v", err)
	}

	// Verify verdict was stored.
	stored, ok := spy.StoredArtefacts[artefactVerdict]
	if !ok {
		t.Fatal("verdict artefact was not stored")
	}

	var verdict struct {
		Outcome   string `json:"outcome"`
		Reasoning string `json:"reasoning"`
	}
	if err := json.Unmarshal(stored, &verdict); err != nil {
		t.Fatalf("unmarshal stored verdict: %v", err)
	}
	if verdict.Outcome != "favour_refiner" {
		t.Errorf("verdict outcome = %q, want %q", verdict.Outcome, "favour_refiner")
	}

	// Verify completion.
	if !spy.Completed {
		t.Error("Complete was not called")
	}
}

func TestJuror_WithPriorRoundReasoning(t *testing.T) {
	spy := newJurorSpy()
	priorRound := "Juror 1 voted \"favour_reviewer\":\nStrong evidence of rule violation."
	seedArtefacts(spy, "Should the feedback be upheld?", "Evidence",
		[]string{"favour_refiner", "favour_reviewer"}, priorRound)

	client := setupJurorTest(t, spy)

	verdictJSON := `{"outcome":"favour_reviewer","reasoning":"Persuaded by prior arguments."}`
	mm := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(verdictJSON),
		},
	}

	schemaBytes, err := buildOutputSchema([]string{"favour_refiner", "favour_reviewer"})
	if err != nil {
		t.Fatalf("buildOutputSchema: %v", err)
	}
	queryTmpl, err := template.New("juror-query").Parse(queryTemplateText)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	agent, err := flow.NewAgent(client,
		flow.WithSchema(schemaBytes),
		flow.WithModel(mm),
		flow.WithSystemPrompt(defaultPrompts["textualist"]),
		flow.WithQueryTemplate(queryTmpl),
	)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	err = runJuror(
		context.Background(), client, agent,
		"Should the feedback be upheld?", "Evidence",
		[]string{"favour_refiner", "favour_reviewer"}, priorRound,
	)
	if err != nil {
		t.Fatalf("runJuror: %v", err)
	}

	// Verify prior round was included in the query.
	if len(mm.capturedQuery) == 0 {
		t.Fatal("query was not captured")
	}
	queryStr := string(mm.capturedQuery)
	if !containsSubstring(queryStr, "Prior round arguments") {
		t.Error("query does not include prior round arguments section")
	}
	if !containsSubstring(queryStr, "favour_reviewer") {
		t.Error("query does not include prior round content")
	}

	if !spy.Completed {
		t.Error("Complete was not called")
	}
}

func TestJuror_Error_QuestionArtefactMissing(t *testing.T) {
	spy := newJurorSpy()
	// Deliberately don't seed the question artefact.
	seedArtefacts(spy, "", "", []string{"a"}, "")
	delete(spy.Artefacts, artefactQuestion)

	client := setupJurorTest(t, spy)
	cfg := &jurorConfig{Personality: "textualist"}

	err := handleJuror(context.Background(), client, cfg)
	if err == nil {
		t.Fatal("expected error when question artefact missing")
	}
	if !containsSubstring(err.Error(), "question") {
		t.Errorf("error should mention question: %v", err)
	}
}

func TestJuror_Error_EvidenceArtefactMissing(t *testing.T) {
	spy := newJurorSpy()
	spy.Artefacts[artefactQuestion] = []byte("question")
	// No evidence artefact.

	client := setupJurorTest(t, spy)
	cfg := &jurorConfig{Personality: "textualist"}

	err := handleJuror(context.Background(), client, cfg)
	if err == nil {
		t.Fatal("expected error when evidence artefact missing")
	}
	if !containsSubstring(err.Error(), "evidence") {
		t.Errorf("error should mention evidence: %v", err)
	}
}

func TestJuror_Error_OutcomesArtefactMissing(t *testing.T) {
	spy := newJurorSpy()
	spy.Artefacts[artefactQuestion] = []byte("question")
	spy.Artefacts[artefactEvidence] = []byte("evidence")
	// No outcomes artefact.

	client := setupJurorTest(t, spy)
	cfg := &jurorConfig{Personality: "textualist"}

	err := handleJuror(context.Background(), client, cfg)
	if err == nil {
		t.Fatal("expected error when outcomes artefact missing")
	}
	if !containsSubstring(err.Error(), "allowed-outcomes") {
		t.Errorf("error should mention allowed-outcomes: %v", err)
	}
}

func TestJuror_Error_AgentInferFails(t *testing.T) {
	spy := newJurorSpy()
	seedArtefacts(spy, "question", "evidence", []string{"a", "b"}, "")

	client := setupJurorTest(t, spy)

	mm := &mockModel{
		err: fmt.Errorf("inference exploded"),
	}

	schemaBytes, err := buildOutputSchema([]string{"a", "b"})
	if err != nil {
		t.Fatalf("buildOutputSchema: %v", err)
	}
	queryTmpl, err := template.New("juror-query").Parse(queryTemplateText)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	agent, err := flow.NewAgent(client,
		flow.WithSchema(schemaBytes),
		flow.WithModel(mm),
		flow.WithSystemPrompt("test"),
		flow.WithQueryTemplate(queryTmpl),
	)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	err = runJuror(context.Background(), client, agent, "question", "evidence", []string{"a", "b"}, "")
	if err == nil {
		t.Fatal("expected error when inference fails")
	}
	if !containsSubstring(err.Error(), "agent run") {
		t.Errorf("error should mention agent run: %v", err)
	}
}

func TestJuror_Error_StoreVerdictFails(t *testing.T) {
	spy := newJurorSpy()
	seedArtefacts(spy, "question", "evidence", []string{"a", "b"}, "")
	spy.StoreArtefactErr = status.Errorf(codes.Internal, "store broken")

	client := setupJurorTest(t, spy)

	verdictJSON := `{"outcome":"a","reasoning":"because"}`
	mm := &mockModel{
		output: &flow.InferOutput{Output: []byte(verdictJSON)},
	}

	schemaBytes, err := buildOutputSchema([]string{"a", "b"})
	if err != nil {
		t.Fatalf("buildOutputSchema: %v", err)
	}
	queryTmpl, err := template.New("juror-query").Parse(queryTemplateText)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	agent, err := flow.NewAgent(client,
		flow.WithSchema(schemaBytes),
		flow.WithModel(mm),
		flow.WithSystemPrompt("test"),
		flow.WithQueryTemplate(queryTmpl),
	)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	err = runJuror(context.Background(), client, agent, "question", "evidence", []string{"a", "b"}, "")
	if err == nil {
		t.Fatal("expected error when store verdict fails")
	}
	if !containsSubstring(err.Error(), "store verdict") {
		t.Errorf("error should mention store verdict: %v", err)
	}
}

func TestJuror_Config_DefaultPersonality(t *testing.T) {
	cfg := &jurorConfig{}
	if cfg.personality() != "textualist" {
		t.Errorf("default personality = %q, want %q", cfg.personality(), "textualist")
	}
}

func TestJuror_Config_CustomPersonality(t *testing.T) {
	cfg := &jurorConfig{Personality: "reformer"}
	if cfg.personality() != "reformer" {
		t.Errorf("personality = %q, want %q", cfg.personality(), "reformer")
	}
}

func TestJuror_Config_SystemPromptOverride(t *testing.T) {
	cfg := &jurorConfig{Personality: "textualist", SystemPrompt: "Custom prompt"}
	if cfg.systemPrompt() != "Custom prompt" {
		t.Errorf("systemPrompt = %q, want %q", cfg.systemPrompt(), "Custom prompt")
	}
}

func TestJuror_Config_DefaultSystemPrompt(t *testing.T) {
	for personality, expectedPrompt := range defaultPrompts {
		cfg := &jurorConfig{Personality: personality}
		if cfg.systemPrompt() != expectedPrompt {
			t.Errorf("default prompt for %s does not match", personality)
		}
	}
}

func TestBuildOutputSchema_Valid(t *testing.T) {
	schemaBytes, err := buildOutputSchema([]string{"a", "b", "c"})
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
	outcome, ok := props["outcome"].(map[string]any)
	if !ok {
		t.Fatal("schema missing outcome property")
	}
	enumVals, ok := outcome["enum"].([]any)
	if !ok {
		t.Fatal("outcome missing enum")
	}
	if len(enumVals) != 3 {
		t.Errorf("enum length = %d, want 3", len(enumVals))
	}
}

func TestBuildOutputSchema_Empty(t *testing.T) {
	_, err := buildOutputSchema([]string{})
	if err == nil {
		t.Fatal("expected error for empty outcomes")
	}
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
