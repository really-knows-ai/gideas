package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

const testFeedbackID = "fb-1"

// ---------------------------------------------------------------------------
// Helpers for constructing test agents
// ---------------------------------------------------------------------------

func newTestEvalAgent(t *testing.T, mp *mockProvider, spy *appraiseSpy, cfg *appraiseConfig) *EvalAgent {
	t.Helper()
	client := newSpyClient(t, spy)
	model := flow.NewModel(cfg.Model, mp)
	agent, err := NewEvalAgent(client, model, cfg)
	if err != nil {
		t.Fatalf("NewEvalAgent() failed: %v", err)
	}
	return agent
}

func newTestReviewAgent(t *testing.T, mp *mockProvider, spy *appraiseSpy, cfg *appraiseConfig) *ReviewAgent {
	t.Helper()
	client := newSpyClient(t, spy)
	model := flow.NewModel(cfg.Model, mp)
	agent, err := NewReviewAgent(client, model, cfg)
	if err != nil {
		t.Fatalf("NewReviewAgent() failed: %v", err)
	}
	return agent
}

func newTestFindingAgent(t *testing.T, mp *mockProvider, spy *appraiseSpy, cfg *appraiseConfig) *FindingAgent {
	t.Helper()
	client := newSpyClient(t, spy)
	model := flow.NewModel(cfg.Model, mp)
	agent, err := NewFindingAgent(client, model, cfg)
	if err != nil {
		t.Fatalf("NewFindingAgent() failed: %v", err)
	}
	return agent
}

// ---------------------------------------------------------------------------
// Tests — EvalAgent: Schema Validation via Run()
// ---------------------------------------------------------------------------

func TestEvalAgent_ValidAccept(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": "fix is adequate"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestEvalAgent(t, mp, spy, cfg)
	fb := &flowv1.FeedbackItem{
		Id:      testFeedbackID,
		Message: "test feedback",
		State:   flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
	}

	out, err := agent.Run(context.Background(), fb, "petition text", "haiku text", "actioned")
	if err != nil {
		t.Fatalf("expected valid output, got error: %v", err)
	}
	if out.Verdict != "accept" {
		t.Fatalf("expected verdict 'accept', got %q", out.Verdict)
	}
	if out.Reason != "fix is adequate" {
		t.Fatalf("expected reason 'fix is adequate', got %q", out.Reason)
	}
}

func TestEvalAgent_ValidReject(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"verdict": "reject", "reason": "fix is incomplete"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestEvalAgent(t, mp, spy, cfg)
	fb := &flowv1.FeedbackItem{
		Id:      testFeedbackID,
		Message: "test feedback",
		State:   flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
	}

	out, err := agent.Run(context.Background(), fb, "petition text", "haiku text", "actioned")
	if err != nil {
		t.Fatalf("expected valid output, got error: %v", err)
	}
	if out.Verdict != "reject" {
		t.Fatalf("expected verdict 'reject', got %q", out.Verdict)
	}
}

func TestEvalAgent_RejectsInvalidVerdict(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"verdict": "maybe", "reason": "unsure"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestEvalAgent(t, mp, spy, cfg)
	fb := &flowv1.FeedbackItem{Id: testFeedbackID, Message: "test"}

	_, err := agent.Run(context.Background(), fb, "", "", "actioned")
	if err == nil {
		t.Fatal("expected invalid verdict to fail schema validation")
	}
	if !strings.Contains(err.Error(), "output validation failed") {
		t.Fatalf("expected 'output validation failed' in error, got: %v", err)
	}
}

func TestEvalAgent_RejectsEmptyReason(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": ""}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestEvalAgent(t, mp, spy, cfg)
	fb := &flowv1.FeedbackItem{Id: testFeedbackID, Message: "test"}

	_, err := agent.Run(context.Background(), fb, "", "", "actioned")
	if err == nil {
		t.Fatal("expected empty reason to fail schema validation")
	}
}

func TestEvalAgent_RejectsAdditionalProperties(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": "ok", "extra": "bad"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestEvalAgent(t, mp, spy, cfg)
	fb := &flowv1.FeedbackItem{Id: testFeedbackID, Message: "test"}

	_, err := agent.Run(context.Background(), fb, "", "", "actioned")
	if err == nil {
		t.Fatal("expected additional properties to fail schema validation")
	}
}

// ---------------------------------------------------------------------------
// Tests — EvalAgent: Template Rendering
// ---------------------------------------------------------------------------

func TestEvalAgent_PromptContainsContext(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": "looks good"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestEvalAgent(t, mp, spy, cfg)
	fb := &flowv1.FeedbackItem{
		Id:       testFeedbackID,
		Message:  "syllable count is wrong",
		Severity: flowv1.Severity_SEVERITY_HIGH,
		State:    flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
		History: []*flowv1.FeedbackEvent{
			{Action: "fix", Actor: "refine", Message: "adjusted syllables"},
		},
	}

	_, err := agent.Run(context.Background(), fb, "write about autumn", "autumn moon\nsilent night", "actioned")
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	query := string(mp.capturedQuery)
	checks := []string{
		"write about autumn",
		"autumn moon",
		"syllable count is wrong",
		"adjusted syllables",
		"FIXED",
	}
	for _, want := range checks {
		if !strings.Contains(query, want) {
			t.Errorf("query prompt should contain %q, got:\n%s", want, query)
		}
	}
}

func TestEvalAgent_WontFixPromptContainsJustification(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": "justified"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestEvalAgent(t, mp, spy, cfg)
	fb := &flowv1.FeedbackItem{
		Id:      testFeedbackID,
		Message: "test feedback",
		State:   flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
		Justification: &flowv1.Justification{
			Kind: &flowv1.Justification_NovelArgument{
				NovelArgument: &flowv1.NovelArgument{
					Argument: "artistic license permits this",
				},
			},
		},
	}

	_, err := agent.Run(context.Background(), fb, "petition", "content", "wont_fix")
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	query := string(mp.capturedQuery)
	if !strings.Contains(query, "artistic license permits this") {
		t.Errorf("query should contain novel argument, got:\n%s", query)
	}
	if !strings.Contains(query, "REFUSED") {
		t.Errorf("query should contain REFUSED instruction, got:\n%s", query)
	}
}

func TestEvalAgent_SystemPromptContainsConfig(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": "ok"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestEvalAgent(t, mp, spy, cfg)
	fb := &flowv1.FeedbackItem{Id: testFeedbackID, Message: "test"}

	_, err := agent.Run(context.Background(), fb, "", "", "actioned")
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(mp.capturedSystem, "haiku") {
		t.Errorf("system prompt should contain review artefact name, got:\n%s", mp.capturedSystem)
	}
	if !strings.Contains(mp.capturedSystem, "petition") {
		t.Errorf("system prompt should contain input artefact name, got:\n%s", mp.capturedSystem)
	}
}

func TestEvalAgent_ModelPassedToProvider(t *testing.T) {
	cfg := &appraiseConfig{
		InputArtefact:    "petition",
		ReviewArtefact:   "haiku",
		GovernedArtefact: "haiku",
		StampName:        "review",
		Model:            "custom-model-v2",
	}
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": "ok"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestEvalAgent(t, mp, spy, cfg)
	fb := &flowv1.FeedbackItem{Id: testFeedbackID, Message: "test"}

	_, err := agent.Run(context.Background(), fb, "", "", "actioned")
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if mp.capturedModel != "custom-model-v2" {
		t.Fatalf("expected model 'custom-model-v2', got %q", mp.capturedModel)
	}
}

// ---------------------------------------------------------------------------
// Tests — ReviewAgent: Schema Validation via Run()
// ---------------------------------------------------------------------------

func TestReviewAgent_ValidOutput(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": [{"message": "issue found", "severity": "medium", "cited_laws": []}]}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestReviewAgent(t, mp, spy, cfg)

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
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestReviewAgent(t, mp, spy, cfg)

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
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": [{"message": "issue", "severity": "extreme", "cited_laws": []}]}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestReviewAgent(t, mp, spy, cfg)

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
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": [{"message": "", "severity": "low", "cited_laws": []}]}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestReviewAgent(t, mp, spy, cfg)

	_, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err == nil {
		t.Fatal("expected empty message to fail schema validation")
	}
}

func TestReviewAgent_RejectsAdditionalProperties(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": [], "extra": "bad"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestReviewAgent(t, mp, spy, cfg)

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
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestReviewAgent(t, mp, spy, cfg)

	laws := []*flowv1.Law{
		{Id: "law-1", Tier: 2, Goal: "Must evoke a season"},
		{Id: "law-2", Tier: 1, Goal: "Use natural imagery"},
	}
	existingFeedback := []*flowv1.FeedbackItem{
		{State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED, Message: "old issue fixed"},
	}

	_, err := agent.Run(context.Background(), "write about autumn", "autumn moon", laws, existingFeedback)
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
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestReviewAgent(t, mp, spy, cfg)

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

func TestReviewAgent_SystemPromptContainsConfig(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"feedback": []}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestReviewAgent(t, mp, spy, cfg)

	_, err := agent.Run(context.Background(), "petition", "content", nil, nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(mp.capturedSystem, "haiku") {
		t.Errorf("system prompt should contain review artefact name, got:\n%s", mp.capturedSystem)
	}
}

// ---------------------------------------------------------------------------
// Tests — FindingAgent: Schema Validation via Run()
// ---------------------------------------------------------------------------

func TestFindingAgent_ValidOutput(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"findings": [{"goal": "Be concise", "applies_to": ["haiku"], "rationale": "Brevity matters"}]}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestFindingAgent(t, mp, spy, cfg)
	items := []*flowv1.FeedbackItem{
		{
			Id:      testFeedbackID,
			Message: "test",
			Justification: &flowv1.Justification{
				Kind: &flowv1.Justification_NovelArgument{
					NovelArgument: &flowv1.NovelArgument{Argument: "novel"},
				},
			},
		},
	}

	out, err := agent.Run(context.Background(), items)
	if err != nil {
		t.Fatalf("expected valid output, got error: %v", err)
	}
	if len(out.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(out.Findings))
	}
	if out.Findings[0].Goal != "Be concise" {
		t.Fatalf("expected goal 'Be concise', got %q", out.Findings[0].Goal)
	}
}

func TestFindingAgent_EmptyFindings(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"findings": []}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestFindingAgent(t, mp, spy, cfg)
	items := []*flowv1.FeedbackItem{
		{Id: testFeedbackID, Message: "test", Justification: &flowv1.Justification{
			Kind: &flowv1.Justification_NovelArgument{
				NovelArgument: &flowv1.NovelArgument{Argument: "novel"},
			},
		}},
	}

	out, err := agent.Run(context.Background(), items)
	if err != nil {
		t.Fatalf("expected valid output, got error: %v", err)
	}
	if len(out.Findings) != 0 {
		t.Fatalf("expected 0 findings, got %d", len(out.Findings))
	}
}

func TestFindingAgent_NilItems_ShortCircuits(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{} // no output configured — should not be called

	agent := newTestFindingAgent(t, mp, spy, cfg)

	out, err := agent.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected nil items to short-circuit, got error: %v", err)
	}
	if out != nil {
		t.Fatalf("expected nil output, got %+v", out)
	}
}

func TestFindingAgent_EmptyItems_ShortCircuits(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{} // no output configured — should not be called

	agent := newTestFindingAgent(t, mp, spy, cfg)

	out, err := agent.Run(context.Background(), []*flowv1.FeedbackItem{})
	if err != nil {
		t.Fatalf("expected empty items to short-circuit, got error: %v", err)
	}
	if out != nil {
		t.Fatalf("expected nil output, got %+v", out)
	}
}

func TestFindingAgent_RejectsEmptyGoal(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"findings": [{"goal": "", "applies_to": ["haiku"], "rationale": "reason"}]}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestFindingAgent(t, mp, spy, cfg)
	items := []*flowv1.FeedbackItem{
		{Id: testFeedbackID, Message: "test", Justification: &flowv1.Justification{
			Kind: &flowv1.Justification_NovelArgument{
				NovelArgument: &flowv1.NovelArgument{Argument: "novel"},
			},
		}},
	}

	_, err := agent.Run(context.Background(), items)
	if err == nil {
		t.Fatal("expected empty goal to fail schema validation")
	}
}

func TestFindingAgent_RejectsAdditionalProperties(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"findings": [], "extra": "bad"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestFindingAgent(t, mp, spy, cfg)
	items := []*flowv1.FeedbackItem{
		{Id: testFeedbackID, Message: "test", Justification: &flowv1.Justification{
			Kind: &flowv1.Justification_NovelArgument{
				NovelArgument: &flowv1.NovelArgument{Argument: "novel"},
			},
		}},
	}

	_, err := agent.Run(context.Background(), items)
	if err == nil {
		t.Fatal("expected additional properties to fail schema validation")
	}
}

// ---------------------------------------------------------------------------
// Tests — FindingAgent: Template Rendering
// ---------------------------------------------------------------------------

func TestFindingAgent_PromptContainsDiscussions(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"findings": []}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestFindingAgent(t, mp, spy, cfg)
	items := []*flowv1.FeedbackItem{
		{
			Id:       testFeedbackID,
			Message:  "the moon reference is weak",
			Severity: flowv1.Severity_SEVERITY_MEDIUM,
			State:    flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
			Justification: &flowv1.Justification{
				Kind: &flowv1.Justification_NovelArgument{
					NovelArgument: &flowv1.NovelArgument{
						Argument: "abstract imagery is valid",
					},
				},
			},
			History: []*flowv1.FeedbackEvent{
				{Action: "refuse", Actor: "refine", Message: "artistic choice"},
			},
		},
	}

	_, err := agent.Run(context.Background(), items)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	query := string(mp.capturedQuery)
	checks := []string{
		"the moon reference is weak",
		"abstract imagery is valid",
		"artistic choice",
		"Refusal was accepted",
	}
	for _, want := range checks {
		if !strings.Contains(query, want) {
			t.Errorf("query prompt should contain %q, got:\n%s", want, query)
		}
	}
}

func TestFindingAgent_SystemPromptContainsConfig(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"findings": []}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestFindingAgent(t, mp, spy, cfg)
	items := []*flowv1.FeedbackItem{
		{Id: testFeedbackID, Message: "test", Justification: &flowv1.Justification{
			Kind: &flowv1.Justification_NovelArgument{
				NovelArgument: &flowv1.NovelArgument{Argument: "novel"},
			},
		}},
	}

	_, err := agent.Run(context.Background(), items)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(mp.capturedSystem, "haiku") {
		t.Errorf("system prompt should contain review artefact name, got:\n%s", mp.capturedSystem)
	}
}

// ---------------------------------------------------------------------------
// Tests — evaluateFeedback (Phase 1 orchestration)
// ---------------------------------------------------------------------------

func TestEvaluateFeedback_Verdicts(t *testing.T) {
	tests := []struct {
		name          string
		verdict       string
		reason        string
		state         flowv1.FeedbackState
		checkAccepted func(*appraiseSpy) bool
		checkRejected func(*appraiseSpy) (string, bool)
	}{
		{
			name:    "accept_fix",
			verdict: "accept",
			reason:  "fix is good",
			state:   flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
			checkAccepted: func(s *appraiseSpy) bool {
				return len(s.AcceptedFixes) == 1 && s.AcceptedFixes[0] == testFeedbackID
			},
		},
		{
			name:    "reject_fix",
			verdict: "reject",
			reason:  "fix is incomplete",
			state:   flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
			checkRejected: func(s *appraiseSpy) (string, bool) {
				msg, ok := s.RejectedFixes[testFeedbackID]
				return msg, ok
			},
		},
		{
			name:    "accept_refusal",
			verdict: "accept",
			reason:  "refusal justified",
			state:   flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
			checkAccepted: func(s *appraiseSpy) bool {
				return len(s.AcceptedRefusals) == 1 && s.AcceptedRefusals[0] == testFeedbackID
			},
		},
		{
			name:    "reject_refusal",
			verdict: "reject",
			reason:  "refusal unjustified",
			state:   flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
			checkRejected: func(s *appraiseSpy) (string, bool) {
				msg, ok := s.RejectedRefusals[testFeedbackID]
				return msg, ok
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultTestConfig()
			spy := newAppraiseSpy()
			mp := &mockProvider{
				output: &flow.InferOutput{
					Output: fmt.Appendf(nil,
						`{"verdict": %q, "reason": %q}`, tt.verdict, tt.reason),
					Cost: defaultCost(),
				},
			}

			client := newSpyClient(t, spy)
			model := flow.NewModel(cfg.Model, mp)
			eval, err := NewEvalAgent(client, model, cfg)
			if err != nil {
				t.Fatalf("NewEvalAgent() failed: %v", err)
			}

			feedback := []*flowv1.FeedbackItem{
				{Id: testFeedbackID, State: tt.state, Message: "test"},
			}

			_, err = evaluateFeedback(context.Background(), eval, client,
				feedback, "petition", "content")
			if err != nil {
				t.Fatalf("evaluateFeedback() returned error: %v", err)
			}

			spy.mu.Lock()
			defer spy.mu.Unlock()

			if tt.checkAccepted != nil {
				if !tt.checkAccepted(spy) {
					t.Fatalf("accepted check failed for %s", tt.name)
				}
			}
			if tt.checkRejected != nil {
				msg, ok := tt.checkRejected(spy)
				if !ok {
					t.Fatalf("expected rejection for %s, not found", tt.name)
				}
				if msg != tt.reason {
					t.Fatalf("expected rejection message %q, got %q", tt.reason, msg)
				}
			}
		})
	}
}

func TestEvaluateFeedback_SkipsNonEvaluableStates(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{} // no output — should not be called

	client := newSpyClient(t, spy)
	model := flow.NewModel(cfg.Model, mp)
	eval, err := NewEvalAgent(client, model, cfg)
	if err != nil {
		t.Fatalf("NewEvalAgent() failed: %v", err)
	}

	feedback := []*flowv1.FeedbackItem{
		{Id: testFeedbackID, State: flowv1.FeedbackState_FEEDBACK_STATE_NEW, Message: "new"},
		{Id: "fb-2", State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED, Message: "resolved"},
		{Id: "fb-3", State: flowv1.FeedbackState_FEEDBACK_STATE_REJECTED, Message: "rejected"},
	}

	resolved, err := evaluateFeedback(context.Background(), eval, client, feedback, "petition", "content")
	if err != nil {
		t.Fatalf("evaluateFeedback() returned error: %v", err)
	}
	if resolved != nil {
		t.Fatalf("expected nil resolved, got %v", resolved)
	}
}

func TestEvaluateFeedback_ParallelMultipleItems(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": "ok"}`),
			Cost:   defaultCost(),
		},
	}

	client := newSpyClient(t, spy)
	model := flow.NewModel(cfg.Model, mp)
	eval, err := NewEvalAgent(client, model, cfg)
	if err != nil {
		t.Fatalf("NewEvalAgent() failed: %v", err)
	}

	feedback := []*flowv1.FeedbackItem{
		{Id: testFeedbackID, State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED, Message: "fix 1"},
		{Id: "fb-2", State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED, Message: "fix 2"},
		{Id: "fb-3", State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX, Message: "refusal"},
	}

	_, err = evaluateFeedback(context.Background(), eval, client, feedback, "petition", "content")
	if err != nil {
		t.Fatalf("evaluateFeedback() returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()
	totalOps := len(spy.AcceptedFixes) + len(spy.AcceptedRefusals)
	if totalOps != 3 {
		t.Fatalf("expected 3 total operations, got %d (fixes=%d, refusals=%d)",
			totalOps, len(spy.AcceptedFixes), len(spy.AcceptedRefusals))
	}
}

func TestEvaluateFeedback_ReturnsNovelResolvedItems(t *testing.T) {
	tests := []struct {
		name  string
		state flowv1.FeedbackState
	}{
		{"accept_fix", flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED},
		{"accept_refusal", flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultTestConfig()
			spy := newAppraiseSpy()
			mp := &mockProvider{
				output: &flow.InferOutput{
					Output: []byte(`{"verdict": "accept", "reason": "ok"}`),
					Cost:   defaultCost(),
				},
			}

			client := newSpyClient(t, spy)
			model := flow.NewModel(cfg.Model, mp)
			eval, err := NewEvalAgent(client, model, cfg)
			if err != nil {
				t.Fatalf("NewEvalAgent() failed: %v", err)
			}

			feedback := []*flowv1.FeedbackItem{
				{
					Id:    testFeedbackID,
					State: tt.state,
					Justification: &flowv1.Justification{
						Kind: &flowv1.Justification_NovelArgument{
							NovelArgument: &flowv1.NovelArgument{Argument: "novel insight"},
						},
					},
					Message: "test",
				},
			}

			resolved, err := evaluateFeedback(
				context.Background(), eval, client, feedback, "petition", "content")
			if err != nil {
				t.Fatalf("evaluateFeedback() returned error: %v", err)
			}
			if len(resolved) != 1 {
				t.Fatalf("expected 1 novel resolved item, got %d", len(resolved))
			}
			if resolved[0].GetId() != testFeedbackID {
				t.Fatalf("expected resolved item fb-1, got %s", resolved[0].GetId())
			}
		})
	}
}

func TestEvaluateFeedback_NoNovelItems_ReturnsNil(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": "ok"}`),
			Cost:   defaultCost(),
		},
	}

	client := newSpyClient(t, spy)
	model := flow.NewModel(cfg.Model, mp)
	eval, err := NewEvalAgent(client, model, cfg)
	if err != nil {
		t.Fatalf("NewEvalAgent() failed: %v", err)
	}

	// ACTIONED item without justification.
	feedback := []*flowv1.FeedbackItem{
		{Id: testFeedbackID, State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED, Message: "test"},
	}

	resolved, err := evaluateFeedback(
		context.Background(), eval, client, feedback, "petition", "content")
	if err != nil {
		t.Fatalf("evaluateFeedback() returned error: %v", err)
	}
	if len(resolved) != 0 {
		t.Fatalf("expected no novel resolved items, got %d", len(resolved))
	}
}

func TestEvaluateFeedback_CitationNotNovel(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": "ok"}`),
			Cost:   defaultCost(),
		},
	}

	client := newSpyClient(t, spy)
	model := flow.NewModel(cfg.Model, mp)
	eval, err := NewEvalAgent(client, model, cfg)
	if err != nil {
		t.Fatalf("NewEvalAgent() failed: %v", err)
	}

	// WONT_FIX with Citation justification — not novel.
	feedback := []*flowv1.FeedbackItem{
		{
			Id:    testFeedbackID,
			State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
			Justification: &flowv1.Justification{
				Kind: &flowv1.Justification_Citation{
					Citation: &flowv1.Citation{CitationIds: []string{"law-1"}},
				},
			},
			Message: "test",
		},
	}

	resolved, err := evaluateFeedback(
		context.Background(), eval, client, feedback, "petition", "content")
	if err != nil {
		t.Fatalf("evaluateFeedback() returned error: %v", err)
	}
	if len(resolved) != 0 {
		t.Fatalf("expected citation not to be novel, got %d resolved", len(resolved))
	}
}

func TestEvaluateFeedback_RejectedVerdictNotNovel(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"verdict": "reject", "reason": "not good"}`),
			Cost:   defaultCost(),
		},
	}

	client := newSpyClient(t, spy)
	model := flow.NewModel(cfg.Model, mp)
	eval, err := NewEvalAgent(client, model, cfg)
	if err != nil {
		t.Fatalf("NewEvalAgent() failed: %v", err)
	}

	// Rejected verdict + NovelArgument → not in resolved.
	feedback := []*flowv1.FeedbackItem{
		{
			Id:    testFeedbackID,
			State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
			Justification: &flowv1.Justification{
				Kind: &flowv1.Justification_NovelArgument{
					NovelArgument: &flowv1.NovelArgument{Argument: "novel insight"},
				},
			},
			Message: "test",
		},
	}

	resolved, err := evaluateFeedback(
		context.Background(), eval, client, feedback, "petition", "content")
	if err != nil {
		t.Fatalf("evaluateFeedback() returned error: %v", err)
	}
	if len(resolved) != 0 {
		t.Fatalf("expected rejected verdict not to produce novel resolved, got %d", len(resolved))
	}
}

// ---------------------------------------------------------------------------
// Tests — mintFindings (Phase 3 orchestration)
// ---------------------------------------------------------------------------

func TestMintFindings_RecordsFindings(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockProvider{
		output: &flow.InferOutput{
			Output: []byte(`{"findings": [{"goal": "Be concise", "applies_to": ["haiku"], "rationale": "Brevity matters"}]}`),
			Cost:   defaultCost(),
		},
	}

	client := newSpyClient(t, spy)
	model := flow.NewModel(cfg.Model, mp)
	finding, err := NewFindingAgent(client, model, cfg)
	if err != nil {
		t.Fatalf("NewFindingAgent() failed: %v", err)
	}

	items := []*flowv1.FeedbackItem{
		{
			Id: testFeedbackID, Message: "test",
			Justification: &flowv1.Justification{
				Kind: &flowv1.Justification_NovelArgument{
					NovelArgument: &flowv1.NovelArgument{Argument: "novel"},
				},
			},
		},
	}

	if err := mintFindings(context.Background(), finding, client, items); err != nil {
		t.Fatalf("mintFindings() returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.RecordedFindings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(spy.RecordedFindings))
	}
	f := spy.RecordedFindings[0]
	if f.Goal != "Be concise" {
		t.Fatalf("expected goal 'Be concise', got %q", f.Goal)
	}
	if len(f.AppliesTo) != 1 || f.AppliesTo[0] != "haiku" {
		t.Fatalf("expected applies_to [haiku], got %v", f.AppliesTo)
	}
	if len(f.Representations) != 1 || f.Representations[0].GetContent() != "Brevity matters" {
		t.Fatalf("expected rationale representation, got %v", f.Representations)
	}
	if f.Representations[0].GetType() != "text/markdown" {
		t.Fatalf("expected type text/markdown, got %q", f.Representations[0].GetType())
	}
}

func TestMintFindings_FindingCount(t *testing.T) {
	tests := []struct {
		name          string
		llmOutput     string
		expectedCount int
	}{
		{
			name: "multiple_findings",
			llmOutput: `{"findings": [
				{"goal": "First", "applies_to": ["haiku"], "rationale": "reason 1"},
				{"goal": "Second", "applies_to": ["haiku"], "rationale": "reason 2"}
			]}`,
			expectedCount: 2,
		},
		{
			name:          "empty_findings",
			llmOutput:     `{"findings": []}`,
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultTestConfig()
			spy := newAppraiseSpy()
			mp := &mockProvider{
				output: &flow.InferOutput{
					Output: []byte(tt.llmOutput),
					Cost:   defaultCost(),
				},
			}

			client := newSpyClient(t, spy)
			model := flow.NewModel(cfg.Model, mp)
			finding, err := NewFindingAgent(client, model, cfg)
			if err != nil {
				t.Fatalf("NewFindingAgent() failed: %v", err)
			}

			items := []*flowv1.FeedbackItem{
				{Id: testFeedbackID, Message: "test", Justification: &flowv1.Justification{
					Kind: &flowv1.Justification_NovelArgument{
						NovelArgument: &flowv1.NovelArgument{Argument: "novel"},
					},
				}},
			}

			if err := mintFindings(context.Background(), finding, client, items); err != nil {
				t.Fatalf("mintFindings() returned error: %v", err)
			}

			spy.mu.Lock()
			defer spy.mu.Unlock()
			if len(spy.RecordedFindings) != tt.expectedCount {
				t.Fatalf("expected %d findings, got %d",
					tt.expectedCount, len(spy.RecordedFindings))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests — parseSeverity
// ---------------------------------------------------------------------------

func TestParseSeverity(t *testing.T) {
	tests := []struct {
		input    string
		expected flowv1.Severity
	}{
		{"low", flowv1.Severity_SEVERITY_LOW},
		{"LOW", flowv1.Severity_SEVERITY_LOW},
		{"medium", flowv1.Severity_SEVERITY_MEDIUM},
		{"high", flowv1.Severity_SEVERITY_HIGH},
		{"critical", flowv1.Severity_SEVERITY_CRITICAL},
		{"unknown", flowv1.Severity_SEVERITY_MEDIUM},
		{"", flowv1.Severity_SEVERITY_MEDIUM},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseSeverity(tt.input)
			if got != tt.expected {
				t.Fatalf("parseSeverity(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests — hasNovelArgument
// ---------------------------------------------------------------------------

func TestHasNovelArgument(t *testing.T) {
	tests := []struct {
		name     string
		fb       *flowv1.FeedbackItem
		expected bool
	}{
		{
			name:     "nil feedback",
			fb:       &flowv1.FeedbackItem{},
			expected: false,
		},
		{
			name: "no justification",
			fb: &flowv1.FeedbackItem{
				Id: testFeedbackID,
			},
			expected: false,
		},
		{
			name: "citation justification",
			fb: &flowv1.FeedbackItem{
				Justification: &flowv1.Justification{
					Kind: &flowv1.Justification_Citation{
						Citation: &flowv1.Citation{CitationIds: []string{"law-1"}},
					},
				},
			},
			expected: false,
		},
		{
			name: "novel argument with content",
			fb: &flowv1.FeedbackItem{
				Justification: &flowv1.Justification{
					Kind: &flowv1.Justification_NovelArgument{
						NovelArgument: &flowv1.NovelArgument{Argument: "novel insight"},
					},
				},
			},
			expected: true,
		},
		{
			name: "novel argument empty string",
			fb: &flowv1.FeedbackItem{
				Justification: &flowv1.Justification{
					Kind: &flowv1.Justification_NovelArgument{
						NovelArgument: &flowv1.NovelArgument{Argument: ""},
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasNovelArgument(tt.fb)
			if got != tt.expected {
				t.Fatalf("hasNovelArgument() = %v, want %v", got, tt.expected)
			}
		})
	}
}
