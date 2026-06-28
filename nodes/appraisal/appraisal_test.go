package main

import (
	"context"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

const testFeedbackID = "fb-1"

// ---------------------------------------------------------------------------
// Helpers for constructing test agents
// ---------------------------------------------------------------------------

func newTestEvalAgent(t *testing.T, inferFn flow.InferFunc, spy *appraisalSpy, cfg *appraisalConfig) *EvalAgent {
	t.Helper()
	client := newSpyClient(t, spy)
	agent, err := NewEvalAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewEvalAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(agent.agent, inferFn)
	return agent
}

func newTestFindingAgent(t *testing.T, inferFn flow.InferFunc, spy *appraisalSpy, cfg *appraisalConfig) *FindingAgent {
	t.Helper()
	client := newSpyClient(t, spy)
	agent, err := NewFindingAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewFindingAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(agent.agent, inferFn)
	return agent
}

// ---------------------------------------------------------------------------
// Tests — EvalAgent: Schema Validation via Run()
// ---------------------------------------------------------------------------

func TestEvalAgent_ValidAccept(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraisalSpy()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": "fix is adequate"}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestEvalAgent(t, inferFn, spy, cfg)
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
	spy := newAppraisalSpy()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"verdict": "reject", "reason": "fix is incomplete"}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestEvalAgent(t, inferFn, spy, cfg)
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
	spy := newAppraisalSpy()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"verdict": "maybe", "reason": "unsure"}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestEvalAgent(t, inferFn, spy, cfg)
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
	spy := newAppraisalSpy()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": ""}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestEvalAgent(t, inferFn, spy, cfg)
	fb := &flowv1.FeedbackItem{Id: testFeedbackID, Message: "test"}

	_, err := agent.Run(context.Background(), fb, "", "", "actioned")
	if err == nil {
		t.Fatal("expected empty reason to fail schema validation")
	}
}

func TestEvalAgent_RejectsAdditionalProperties(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraisalSpy()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": "ok", "extra": "bad"}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestEvalAgent(t, inferFn, spy, cfg)
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
	spy := newAppraisalSpy()
	var capturedQuery []byte
	inferFn := func(_ context.Context, _, _ string, queryPrompt []byte) (*flow.InferOutput, error) {
		capturedQuery = queryPrompt
		return &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": "looks good"}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestEvalAgent(t, inferFn, spy, cfg)
	fb := &flowv1.FeedbackItem{
		Id:      testFeedbackID,
		Message: "syllable count is wrong",
		State:   flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
		History: []*flowv1.FeedbackEvent{
			{Action: "fix", Actor: "refine", Message: "adjusted syllables"},
		},
	}

	_, err := agent.Run(context.Background(), fb, "write about autumn", "autumn moon\nsilent night", "actioned")
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	query := string(capturedQuery)
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
	spy := newAppraisalSpy()
	var capturedQuery []byte
	inferFn := func(_ context.Context, _, _ string, queryPrompt []byte) (*flow.InferOutput, error) {
		capturedQuery = queryPrompt
		return &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": "justified"}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestEvalAgent(t, inferFn, spy, cfg)
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

	query := string(capturedQuery)
	if !strings.Contains(query, "artistic license permits this") {
		t.Errorf("query should contain novel argument, got:\n%s", query)
	}
	if !strings.Contains(query, "REFUSED") {
		t.Errorf("query should contain REFUSED instruction, got:\n%s", query)
	}
}

func TestEvalAgent_SystemPromptContainsConfig(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraisalSpy()
	var capturedSystem string
	inferFn := func(_ context.Context, _, systemPrompt string, _ []byte) (*flow.InferOutput, error) {
		capturedSystem = systemPrompt
		return &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": "ok"}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestEvalAgent(t, inferFn, spy, cfg)
	fb := &flowv1.FeedbackItem{Id: testFeedbackID, Message: "test"}

	_, err := agent.Run(context.Background(), fb, "", "", "actioned")
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(capturedSystem, "haiku") {
		t.Errorf("system prompt should contain review artefact name, got:\n%s", capturedSystem)
	}
	if !strings.Contains(capturedSystem, "petition") {
		t.Errorf("system prompt should contain input artefact name, got:\n%s", capturedSystem)
	}
}

// ---------------------------------------------------------------------------
// Tests — FindingAgent: Schema Validation via Run()
// ---------------------------------------------------------------------------

func TestFindingAgent_ValidOutput(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraisalSpy()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"findings": [{"goal": "Be concise", "applies_to": ["haiku"], "rationale": "Brevity matters"}]}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestFindingAgent(t, inferFn, spy, cfg)
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
	spy := newAppraisalSpy()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"findings": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestFindingAgent(t, inferFn, spy, cfg)
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
	spy := newAppraisalSpy()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		t.Fatal("should not be called")
		return nil, nil
	}

	agent := newTestFindingAgent(t, inferFn, spy, cfg)

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
	spy := newAppraisalSpy()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		t.Fatal("should not be called")
		return nil, nil
	}

	agent := newTestFindingAgent(t, inferFn, spy, cfg)

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
	spy := newAppraisalSpy()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"findings": [{"goal": "", "applies_to": ["haiku"], "rationale": "reason"}]}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestFindingAgent(t, inferFn, spy, cfg)
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
	spy := newAppraisalSpy()
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*flow.InferOutput, error) {
		return &flow.InferOutput{
			Output: []byte(`{"findings": [], "extra": "bad"}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestFindingAgent(t, inferFn, spy, cfg)
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
	spy := newAppraisalSpy()
	var capturedQuery []byte
	inferFn := func(_ context.Context, _, _ string, queryPrompt []byte) (*flow.InferOutput, error) {
		capturedQuery = queryPrompt
		return &flow.InferOutput{
			Output: []byte(`{"findings": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestFindingAgent(t, inferFn, spy, cfg)
	items := []*flowv1.FeedbackItem{
		{
			Id:      testFeedbackID,
			Message: "the moon reference is weak",
			State:   flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
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

	query := string(capturedQuery)
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
	spy := newAppraisalSpy()
	var capturedSystem string
	inferFn := func(_ context.Context, _, systemPrompt string, _ []byte) (*flow.InferOutput, error) {
		capturedSystem = systemPrompt
		return &flow.InferOutput{
			Output: []byte(`{"findings": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestFindingAgent(t, inferFn, spy, cfg)
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

	if !strings.Contains(capturedSystem, "haiku") {
		t.Errorf("system prompt should contain review artefact name, got:\n%s", capturedSystem)
	}
}

// ---------------------------------------------------------------------------
// Tests — ConfigMap Prompt Overrides
// ---------------------------------------------------------------------------

func TestEvalAgent_SystemPromptOverride(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.EvalSystemPrompt = `Custom eval system prompt for {{.ReviewArtefact}}.`

	spy := newAppraisalSpy()
	var capturedSystem string
	inferFn := func(_ context.Context, _, systemPrompt string, _ []byte) (*flow.InferOutput, error) {
		capturedSystem = systemPrompt
		return &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": "ok"}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestEvalAgent(t, inferFn, spy, cfg)
	fb := &flowv1.FeedbackItem{Id: testFeedbackID, Message: "test"}

	_, err := agent.Run(context.Background(), fb, "", "", "actioned")
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(capturedSystem, "Custom eval system prompt for haiku.") {
		t.Errorf("expected overridden system prompt, got:\n%s", capturedSystem)
	}
	// Must NOT contain the default system prompt text.
	if strings.Contains(capturedSystem, "reviewer evaluating a previous feedback item") {
		t.Error("system prompt should not contain default text when overridden")
	}
}

func TestEvalAgent_QueryTemplateOverride(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.EvalQueryTemplate = `Custom query: {{.FeedbackMessage}}`

	spy := newAppraisalSpy()
	var capturedQuery []byte
	inferFn := func(_ context.Context, _, _ string, queryPrompt []byte) (*flow.InferOutput, error) {
		capturedQuery = queryPrompt
		return &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": "ok"}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestEvalAgent(t, inferFn, spy, cfg)
	fb := &flowv1.FeedbackItem{Id: testFeedbackID, Message: "test message"}

	_, err := agent.Run(context.Background(), fb, "", "", "actioned")
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	query := string(capturedQuery)
	if !strings.Contains(query, "Custom query: test message") {
		t.Errorf("expected overridden query prompt, got:\n%s", query)
	}
	// Must NOT contain the default query template text.
	if strings.Contains(query, "## CONTEXT") {
		t.Error("query prompt should not contain default text when overridden")
	}
}

func TestFindingAgent_SystemPromptOverride(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.FindingSystemPrompt = `Custom finding system for {{.ReviewArtefact}}.`

	spy := newAppraisalSpy()
	var capturedSystem string
	inferFn := func(_ context.Context, _, systemPrompt string, _ []byte) (*flow.InferOutput, error) {
		capturedSystem = systemPrompt
		return &flow.InferOutput{
			Output: []byte(`{"findings": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestFindingAgent(t, inferFn, spy, cfg)
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

	if !strings.Contains(capturedSystem, "Custom finding system for haiku.") {
		t.Errorf("expected overridden system prompt, got:\n%s", capturedSystem)
	}
	if strings.Contains(capturedSystem, "governance analyst") {
		t.Error("system prompt should not contain default text when overridden")
	}
}

func TestFindingAgent_QueryTemplateOverride(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.FindingQueryTemplate = `Custom finding query: {{.GovernedArtefact}}`

	spy := newAppraisalSpy()
	var capturedQuery []byte
	inferFn := func(_ context.Context, _, _ string, queryPrompt []byte) (*flow.InferOutput, error) {
		capturedQuery = queryPrompt
		return &flow.InferOutput{
			Output: []byte(`{"findings": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestFindingAgent(t, inferFn, spy, cfg)
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

	query := string(capturedQuery)
	if !strings.Contains(query, "Custom finding query: haiku") {
		t.Errorf("expected overridden query prompt, got:\n%s", query)
	}
	if strings.Contains(query, "RESOLVED DISCUSSIONS") {
		t.Error("query prompt should not contain default text when overridden")
	}
}

func TestEvalAgent_DefaultPromptsWhenOverrideEmpty(t *testing.T) {
	cfg := defaultTestConfig()
	// Leave EvalSystemPrompt and EvalQueryTemplate empty (defaults).

	spy := newAppraisalSpy()
	var capturedSystem string
	var capturedQuery []byte
	inferFn := func(_ context.Context, _, systemPrompt string, queryPrompt []byte) (*flow.InferOutput, error) {
		capturedSystem = systemPrompt
		capturedQuery = queryPrompt
		return &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": "ok"}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestEvalAgent(t, inferFn, spy, cfg)
	fb := &flowv1.FeedbackItem{Id: testFeedbackID, Message: "test"}

	_, err := agent.Run(context.Background(), fb, "", "", "actioned")
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// Should contain default system prompt text.
	if !strings.Contains(capturedSystem, "reviewer evaluating a previous feedback item") {
		t.Errorf("expected default system prompt, got:\n%s", capturedSystem)
	}
	// Should contain default query template text.
	query := string(capturedQuery)
	if !strings.Contains(query, "## CONTEXT") {
		t.Errorf("expected default query template, got:\n%s", query)
	}
}

func TestFindingAgent_DefaultPromptsWhenOverrideEmpty(t *testing.T) {
	cfg := defaultTestConfig()
	// Leave FindingSystemPrompt and FindingQueryTemplate empty (defaults).

	spy := newAppraisalSpy()
	var capturedSystem string
	var capturedQuery []byte
	inferFn := func(_ context.Context, _, systemPrompt string, queryPrompt []byte) (*flow.InferOutput, error) {
		capturedSystem = systemPrompt
		capturedQuery = queryPrompt
		return &flow.InferOutput{
			Output: []byte(`{"findings": []}`),
			Cost:   defaultCost(),
		}, nil
	}

	agent := newTestFindingAgent(t, inferFn, spy, cfg)
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

	if !strings.Contains(capturedSystem, "governance analyst") {
		t.Errorf("expected default system prompt, got:\n%s", capturedSystem)
	}
	query := string(capturedQuery)
	if !strings.Contains(query, "RESOLVED DISCUSSIONS") {
		t.Errorf("expected default query template, got:\n%s", query)
	}
}
