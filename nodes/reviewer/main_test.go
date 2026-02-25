package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// Tests — ReviewAgent: Schema Validation via Run()
// ---------------------------------------------------------------------------

func TestReviewAgent_ValidOutput(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newReviewerSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": [{"message": "issue found", "severity": "medium", "cited_laws": []}]}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestReviewAgent(t, mp, spy, cfg, "")

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

func TestReviewAgent_EmptyFeedback(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newReviewerSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestReviewAgent(t, mp, spy, cfg, "")

	out, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err != nil {
		t.Fatalf("expected valid output, got error: %v", err)
	}
	if len(out.Feedback) != 0 {
		t.Fatalf("expected 0 feedback items, got %d", len(out.Feedback))
	}
}

func TestReviewAgent_RejectsInvalidSeverity(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newReviewerSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": [{"message": "issue", "severity": "extreme", "cited_laws": []}]}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestReviewAgent(t, mp, spy, cfg, "")

	_, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err == nil {
		t.Fatal("expected invalid severity to fail schema validation")
	}
	if !strings.Contains(err.Error(), "output validation failed") {
		t.Fatalf("expected 'output validation failed' in error, got: %v", err)
	}
}

func TestReviewAgent_RejectsEmptyMessage(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newReviewerSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": [{"message": "", "severity": "low", "cited_laws": []}]}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestReviewAgent(t, mp, spy, cfg, "")

	_, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err == nil {
		t.Fatal("expected empty message to fail schema validation")
	}
}

func TestReviewAgent_RejectsAdditionalProperties(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newReviewerSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": [], "extra": "bad"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestReviewAgent(t, mp, spy, cfg, "")

	_, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err == nil {
		t.Fatal("expected additional properties to fail schema validation")
	}
}

// ---------------------------------------------------------------------------
// Tests — ReviewAgent: Template Rendering
// ---------------------------------------------------------------------------

func TestReviewAgent_PromptContainsLawsAndHistory(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newReviewerSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestReviewAgent(t, mp, spy, cfg, "")

	laws := []lawData{
		{ID: "law-1", Tier: 2, Goal: "Must evoke a season"},
		{ID: "law-2", Tier: 1, Goal: "Use natural imagery"},
	}
	history := []historyData{
		{State: "RESOLVED", Message: "old issue fixed"},
	}

	_, err := agent.Run(context.Background(), "write about autumn", "autumn moon", laws, history)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	query := string(mp.capturedQuery)
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

func TestReviewAgent_PromptOmitsEmptySections(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newReviewerSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestReviewAgent(t, mp, spy, cfg, "")

	_, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	query := string(mp.capturedQuery)
	if strings.Contains(query, "GOVERNANCE LAWS") {
		t.Errorf("query should not contain GOVERNANCE LAWS when no laws, got:\n%s", query)
	}
	if strings.Contains(query, "PREVIOUS FEEDBACK HISTORY") {
		t.Errorf("query should not contain PREVIOUS FEEDBACK HISTORY when no feedback, got:\n%s", query)
	}
}

func TestReviewAgent_DivisionSuffixInSystemPrompt(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newReviewerSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		},
	}

	suffix := "Pay special attention to information disclosure and injection risks."
	agent := newTestReviewAgent(t, mp, spy, cfg, suffix)

	_, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(mp.capturedSystem, suffix) {
		t.Errorf("system prompt should contain division suffix %q, got:\n%s",
			suffix, mp.capturedSystem)
	}
}

func TestReviewAgent_EmptyDivisionSuffixOmitted(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newReviewerSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestReviewAgent(t, mp, spy, cfg, "")

	_, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// With no suffix, the system prompt should end cleanly after the severity list.
	if strings.Contains(mp.capturedSystem, "Pay special attention") {
		t.Errorf("system prompt should not contain division suffix when empty, got:\n%s",
			mp.capturedSystem)
	}
}

func TestReviewAgent_SystemPromptContainsConfig(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newReviewerSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestReviewAgent(t, mp, spy, cfg, "")

	_, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(mp.capturedSystem, "review") {
		t.Errorf("system prompt should contain review artefact name, got:\n%s",
			mp.capturedSystem)
	}
}

// ---------------------------------------------------------------------------
// Tests — handleReview: Integration
// ---------------------------------------------------------------------------

func TestHandleReview_HappyPath(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newReviewerSpy()

	// Set up artefact contents the handler will read.
	lawsJSON, _ := json.Marshal([]lawData{
		{ID: "law-1", Tier: 2, Goal: "Must evoke a season"},
	})
	historyJSON, _ := json.Marshal([]historyData{
		{State: "RESOLVED", Message: "old issue fixed"},
	})
	divisionJSON, _ := json.Marshal(divisionData{
		Name:         "security",
		PromptSuffix: "Focus on security risks.",
	})

	spy.ArtefactContents["input"] = []byte("write about autumn")
	spy.ArtefactContents["review"] = []byte("autumn moon\nsilent night")
	spy.ArtefactContents["laws"] = lawsJSON
	spy.ArtefactContents["history"] = historyJSON
	spy.ArtefactContents["division"] = divisionJSON

	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": [{"message": "weak imagery", "severity": "medium", "cited_laws": ["law-1"]}]}`),
			Cost:   defaultCost(),
		},
	}

	client := newSpyClient(t, spy)

	// Create agent with division suffix and override model.
	agent, err := NewReviewAgent(client, cfg, "Focus on security risks.")
	if err != nil {
		t.Fatalf("NewReviewAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(agent.agent, mp)

	// Simulate handleReview's artefact reading + agent invocation + output storage.
	laws := []lawData{{ID: "law-1", Tier: 2, Goal: "Must evoke a season"}}
	history := []historyData{{State: "RESOLVED", Message: "old issue fixed"}}

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

	// Verify the review-output can be serialized.
	outJSON, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("failed to marshal review output: %v", err)
	}

	// Verify it round-trips.
	var parsed reviewOutput
	if err := json.Unmarshal(outJSON, &parsed); err != nil {
		t.Fatalf("failed to unmarshal review output: %v", err)
	}
	if len(parsed.Feedback) != 1 {
		t.Fatalf("round-trip: expected 1 feedback item, got %d", len(parsed.Feedback))
	}
}

func TestHandleReview_EmptyLaws(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newReviewerSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestReviewAgent(t, mp, spy, cfg, "")

	// Empty laws — reviewer should still work.
	out, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if len(out.Feedback) != 0 {
		t.Fatalf("expected 0 feedback items, got %d", len(out.Feedback))
	}
}

func TestHandleReview_ReviewOutputFormat(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newReviewerSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": [
				{"message": "issue one", "severity": "low", "cited_laws": []},
				{"message": "issue two", "severity": "critical", "cited_laws": ["law-1", "law-2"]}
			]}`),
			Cost: defaultCost(),
		},
	}

	agent := newTestReviewAgent(t, mp, spy, cfg, "")

	out, err := agent.Run(context.Background(), "petition", "content",
		[]lawData{{ID: "law-1", Tier: 2, Goal: "test"}},
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
	if out.Feedback[0].Severity != "low" {
		t.Fatalf("expected severity 'low', got %q", out.Feedback[0].Severity)
	}
	if len(out.Feedback[0].CitedLaws) != 0 {
		t.Fatalf("expected 0 cited laws, got %d", len(out.Feedback[0].CitedLaws))
	}

	// Verify second item.
	if out.Feedback[1].Message != "issue two" {
		t.Fatalf("expected 'issue two', got %q", out.Feedback[1].Message)
	}
	if out.Feedback[1].Severity != "critical" {
		t.Fatalf("expected severity 'critical', got %q", out.Feedback[1].Severity)
	}
	if len(out.Feedback[1].CitedLaws) != 2 {
		t.Fatalf("expected 2 cited laws, got %d", len(out.Feedback[1].CitedLaws))
	}
}

// ---------------------------------------------------------------------------
// Tests — Division artefact deserialization
// ---------------------------------------------------------------------------

func TestDivisionData_Deserialization(t *testing.T) {
	tests := []struct {
		name           string
		json           string
		expectedName   string
		expectedSuffix string
	}{
		{
			name:           "with suffix",
			json:           `{"name":"security","promptSuffix":"Focus on security."}`,
			expectedName:   "security",
			expectedSuffix: "Focus on security.",
		},
		{
			name:           "without suffix",
			json:           `{"name":"general","promptSuffix":""}`,
			expectedName:   "general",
			expectedSuffix: "",
		},
		{
			name:           "empty name",
			json:           `{"name":"","promptSuffix":""}`,
			expectedName:   "",
			expectedSuffix: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d divisionData
			if err := json.Unmarshal([]byte(tt.json), &d); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}
			if d.Name != tt.expectedName {
				t.Fatalf("expected name %q, got %q", tt.expectedName, d.Name)
			}
			if d.PromptSuffix != tt.expectedSuffix {
				t.Fatalf("expected suffix %q, got %q", tt.expectedSuffix, d.PromptSuffix)
			}
		})
	}
}

func TestLawData_Deserialization(t *testing.T) {
	input := `[{"id":"law-1","tier":2,"goal":"Must evoke a season"},{"id":"law-2","tier":1,"goal":"Use imagery"}]`
	var laws []lawData
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
	var history []historyData
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
