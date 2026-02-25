package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

const testFeedbackID = "fb-1"
const testLawID = "law-1"

// ---------------------------------------------------------------------------
// Helpers for constructing test agents
// ---------------------------------------------------------------------------

func newTestEvalAgent(t *testing.T, mm *mockModel, spy *appraiseSpy, cfg *appraiseConfig) *EvalAgent {
	t.Helper()
	client := newSpyClient(t, spy)
	agent, err := NewEvalAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewEvalAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(agent.agent, mm)
	return agent
}

func newTestFindingAgent(t *testing.T, mm *mockModel, spy *appraiseSpy, cfg *appraiseConfig) *FindingAgent {
	t.Helper()
	client := newSpyClient(t, spy)
	agent, err := NewFindingAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewFindingAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(agent.agent, mm)
	return agent
}

// ---------------------------------------------------------------------------
// Tests — EvalAgent: Schema Validation via Run()
// ---------------------------------------------------------------------------

func TestEvalAgent_ValidAccept(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockModel{
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
	mp := &mockModel{
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
	mp := &mockModel{
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
	mp := &mockModel{
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
	mp := &mockModel{
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
	mp := &mockModel{
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
	mp := &mockModel{
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
	mp := &mockModel{
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

// ---------------------------------------------------------------------------
// Tests — FindingAgent: Schema Validation via Run()
// ---------------------------------------------------------------------------

func TestFindingAgent_ValidOutput(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newAppraiseSpy()
	mp := &mockModel{
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
	mp := &mockModel{
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
	mp := &mockModel{} // no output configured — should not be called

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
	mp := &mockModel{} // no output configured — should not be called

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
	mp := &mockModel{
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
	mp := &mockModel{
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
	mp := &mockModel{
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
	mp := &mockModel{
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
			mp := &mockModel{
				output: &flow.InferOutput{
					Output: fmt.Appendf(nil,
						`{"verdict": %q, "reason": %q}`, tt.verdict, tt.reason),
					Cost: defaultCost(),
				},
			}

			client := newSpyClient(t, spy)
			eval, err := NewEvalAgent(client, cfg)
			if err != nil {
				t.Fatalf("NewEvalAgent() failed: %v", err)
			}
			flow.OverrideModelForTest(eval.agent, mp)

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
	mp := &mockModel{} // no output — should not be called

	client := newSpyClient(t, spy)
	eval, err := NewEvalAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewEvalAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(eval.agent, mp)

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
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": "ok"}`),
			Cost:   defaultCost(),
		},
	}

	client := newSpyClient(t, spy)
	eval, err := NewEvalAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewEvalAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(eval.agent, mp)

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
			mp := &mockModel{
				output: &flow.InferOutput{
					Output: []byte(`{"verdict": "accept", "reason": "ok"}`),
					Cost:   defaultCost(),
				},
			}

			client := newSpyClient(t, spy)
			eval, err := NewEvalAgent(client, cfg)
			if err != nil {
				t.Fatalf("NewEvalAgent() failed: %v", err)
			}
			flow.OverrideModelForTest(eval.agent, mp)

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
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": "ok"}`),
			Cost:   defaultCost(),
		},
	}

	client := newSpyClient(t, spy)
	eval, err := NewEvalAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewEvalAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(eval.agent, mp)

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
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"verdict": "accept", "reason": "ok"}`),
			Cost:   defaultCost(),
		},
	}

	client := newSpyClient(t, spy)
	eval, err := NewEvalAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewEvalAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(eval.agent, mp)

	// WONT_FIX with Citation justification — not novel.
	feedback := []*flowv1.FeedbackItem{
		{
			Id:    testFeedbackID,
			State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
			Justification: &flowv1.Justification{
				Kind: &flowv1.Justification_Citation{
					Citation: &flowv1.Citation{CitationIds: []string{testLawID}},
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
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"verdict": "reject", "reason": "not good"}`),
			Cost:   defaultCost(),
		},
	}

	client := newSpyClient(t, spy)
	eval, err := NewEvalAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewEvalAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(eval.agent, mp)

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
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"findings": [{"goal": "Be concise", "applies_to": ["haiku"], "rationale": "Brevity matters"}]}`),
			Cost:   defaultCost(),
		},
	}

	client := newSpyClient(t, spy)
	finding, err := NewFindingAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewFindingAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(finding.agent, mp)

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
			mp := &mockModel{
				output: &flow.InferOutput{
					Output: []byte(tt.llmOutput),
					Cost:   defaultCost(),
				},
			}

			client := newSpyClient(t, spy)
			finding, err := NewFindingAgent(client, cfg)
			if err != nil {
				t.Fatalf("NewFindingAgent() failed: %v", err)
			}
			flow.OverrideModelForTest(finding.agent, mp)

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
						Citation: &flowv1.Citation{CitationIds: []string{testLawID}},
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

// ---------------------------------------------------------------------------
// Tests — groupLawsByDivision
// ---------------------------------------------------------------------------

func TestGroupLawsByDivision_MixedDivisions(t *testing.T) {
	laws := []*flowv1.Law{
		{Id: testLawID, Division: "security"},
		{Id: "law-2", Division: "architecture"},
		{Id: "law-3", Division: "security"},
		{Id: "law-4"},
	}

	groups := groupLawsByDivision(laws)

	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}

	securityLaws := groups["security"]
	if len(securityLaws) != 2 {
		t.Fatalf("expected 2 security laws, got %d", len(securityLaws))
	}

	archLaws := groups["architecture"]
	if len(archLaws) != 1 {
		t.Fatalf("expected 1 architecture law, got %d", len(archLaws))
	}

	generalLaws := groups[defaultDivision]
	if len(generalLaws) != 1 {
		t.Fatalf("expected 1 general law, got %d", len(generalLaws))
	}
	if generalLaws[0].GetId() != "law-4" {
		t.Fatalf("expected general law to be law-4, got %s", generalLaws[0].GetId())
	}
}

func TestGroupLawsByDivision_AllEmpty(t *testing.T) {
	laws := []*flowv1.Law{
		{Id: testLawID},
		{Id: "law-2"},
	}

	groups := groupLawsByDivision(laws)

	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if len(groups[defaultDivision]) != 2 {
		t.Fatalf("expected 2 general laws, got %d", len(groups[defaultDivision]))
	}
}

func TestGroupLawsByDivision_NoLaws(t *testing.T) {
	groups := groupLawsByDivision(nil)
	if len(groups) != 0 {
		t.Fatalf("expected 0 groups for nil laws, got %d", len(groups))
	}
}

func TestGroupLawsByDivision_SingleDivision(t *testing.T) {
	laws := []*flowv1.Law{
		{Id: testLawID, Division: "security"},
		{Id: "law-2", Division: "security"},
	}

	groups := groupLawsByDivision(laws)

	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if len(groups["security"]) != 2 {
		t.Fatalf("expected 2 security laws, got %d", len(groups["security"]))
	}
}

// ---------------------------------------------------------------------------
// Tests — fanOutReview (Phase 2 orchestration)
// ---------------------------------------------------------------------------

func TestFanOutReview_SingleDivision(t *testing.T) {
	spy := newAppraiseSpy()
	cfg := defaultTestConfig()
	cfg.DivisionPrompts = map[string]string{
		"security": "Focus on security.",
	}
	spy.Laws = []*flowv1.Law{
		{Id: testLawID, Division: "security", Goal: "No secrets"},
	}

	// Pre-configure child review outputs.
	setupChildReviewOutputs(spy, reviewOutput{
		Feedback: []reviewItem{
			{Message: "found issue", Severity: "medium", CitedLaws: []string{testLawID}},
		},
	})

	client := newSpyClient(t, spy)

	feedback, err := fanOutReview(
		context.Background(), client, cfg,
		spy.Laws, nil,
		"petition text", "haiku text",
	)
	if err != nil {
		t.Fatalf("fanOutReview() returned error: %v", err)
	}

	if len(feedback) != 1 {
		t.Fatalf("expected 1 merged feedback item, got %d", len(feedback))
	}
	if feedback[0].Message != "found issue" {
		t.Fatalf("expected message 'found issue', got %q", feedback[0].Message)
	}
	if feedback[0].CitedLaws[0] != testLawID {
		t.Fatalf("expected cited law 'law-1', got %v", feedback[0].CitedLaws)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Verify fan-out: 1 child created, routed to "reviewer".
	if len(spy.FanOutTasks) != 1 {
		t.Fatalf("expected 1 fan-out task, got %d", len(spy.FanOutTasks))
	}
	if spy.FanOutTasks[0].TargetNode != "reviewer" {
		t.Fatalf("expected target 'reviewer', got %q", spy.FanOutTasks[0].TargetNode)
	}

	// Verify division artefact contains prompt suffix.
	divRaw := spy.FanOutTasks[0].Artefacts[artefactDivision]
	var div divisionData
	if err := json.Unmarshal(divRaw, &div); err != nil {
		t.Fatalf("failed to unmarshal division artefact: %v", err)
	}
	if div.Name != "security" {
		t.Fatalf("expected division name 'security', got %q", div.Name)
	}
	if div.PromptSuffix != "Focus on security." {
		t.Fatalf("expected prompt suffix 'Focus on security.', got %q", div.PromptSuffix)
	}
}

func TestFanOutReview_MultipleDivisions(t *testing.T) {
	spy := newAppraiseSpy()
	cfg := defaultTestConfig()
	spy.Laws = []*flowv1.Law{
		{Id: testLawID, Division: "security", Goal: "No secrets"},
		{Id: "law-2", Division: "style", Goal: "Be consistent"},
	}

	// Pre-configure child review outputs (2 children).
	setupChildReviewOutputs(spy,
		reviewOutput{Feedback: []reviewItem{
			{Message: "security issue", Severity: "high", CitedLaws: []string{testLawID}},
		}},
		reviewOutput{Feedback: []reviewItem{
			{Message: "style issue", Severity: "low", CitedLaws: []string{"law-2"}},
		}},
	)

	client := newSpyClient(t, spy)

	feedback, err := fanOutReview(
		context.Background(), client, cfg,
		spy.Laws, nil,
		"petition text", "haiku text",
	)
	if err != nil {
		t.Fatalf("fanOutReview() returned error: %v", err)
	}

	if len(feedback) != 2 {
		t.Fatalf("expected 2 merged feedback items, got %d", len(feedback))
	}

	// Sort by message for stable assertion.
	sort.Slice(feedback, func(i, j int) bool {
		return feedback[i].Message < feedback[j].Message
	})

	if feedback[0].Message != "security issue" {
		t.Fatalf("expected 'security issue', got %q", feedback[0].Message)
	}
	if feedback[1].Message != "style issue" {
		t.Fatalf("expected 'style issue', got %q", feedback[1].Message)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.FanOutTasks) != 2 {
		t.Fatalf("expected 2 fan-out tasks, got %d", len(spy.FanOutTasks))
	}
}

func TestFanOutReview_NoLaws(t *testing.T) {
	spy := newAppraiseSpy()
	cfg := defaultTestConfig()

	client := newSpyClient(t, spy)

	feedback, err := fanOutReview(
		context.Background(), client, cfg,
		nil, nil,
		"petition text", "haiku text",
	)
	if err != nil {
		t.Fatalf("fanOutReview() returned error: %v", err)
	}
	if feedback != nil {
		t.Fatalf("expected nil feedback for no laws, got %v", feedback)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.FanOutTasks) != 0 {
		t.Fatalf("expected 0 fan-out tasks, got %d", len(spy.FanOutTasks))
	}
}

func TestFanOutReview_EmptyDivisionDefaultsToGeneral(t *testing.T) {
	spy := newAppraiseSpy()
	cfg := defaultTestConfig()
	spy.Laws = []*flowv1.Law{
		{Id: testLawID, Goal: "General law"},
	}

	setupChildReviewOutputs(spy, reviewOutput{Feedback: []reviewItem{}})

	client := newSpyClient(t, spy)

	_, err := fanOutReview(
		context.Background(), client, cfg,
		spy.Laws, nil,
		"petition text", "haiku text",
	)
	if err != nil {
		t.Fatalf("fanOutReview() returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.FanOutTasks) != 1 {
		t.Fatalf("expected 1 fan-out task, got %d", len(spy.FanOutTasks))
	}

	divRaw := spy.FanOutTasks[0].Artefacts[artefactDivision]
	var div divisionData
	if err := json.Unmarshal(divRaw, &div); err != nil {
		t.Fatalf("failed to unmarshal division: %v", err)
	}
	if div.Name != defaultDivision {
		t.Fatalf("expected division name %q, got %q", defaultDivision, div.Name)
	}
}

func TestFanOutReview_LawsArtefactContainsCorrectData(t *testing.T) {
	spy := newAppraiseSpy()
	cfg := defaultTestConfig()
	spy.Laws = []*flowv1.Law{
		{Id: testLawID, Tier: 2, Division: "security", Goal: "No secrets"},
		{Id: "law-2", Tier: 1, Division: "security", Goal: "Be careful"},
	}

	setupChildReviewOutputs(spy, reviewOutput{Feedback: []reviewItem{}})

	client := newSpyClient(t, spy)

	_, err := fanOutReview(
		context.Background(), client, cfg,
		spy.Laws, nil,
		"petition text", "haiku text",
	)
	if err != nil {
		t.Fatalf("fanOutReview() returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	lawsRaw := spy.FanOutTasks[0].Artefacts[artefactLaws]
	var laws []lawData
	if err := json.Unmarshal(lawsRaw, &laws); err != nil {
		t.Fatalf("failed to unmarshal laws artefact: %v", err)
	}
	if len(laws) != 2 {
		t.Fatalf("expected 2 laws in artefact, got %d", len(laws))
	}

	// Sort for stable assertion.
	sort.Slice(laws, func(i, j int) bool {
		return laws[i].ID < laws[j].ID
	})

	if laws[0].ID != testLawID || laws[0].Tier != 2 || laws[0].Goal != "No secrets" {
		t.Fatalf("law-1 data mismatch: %+v", laws[0])
	}
	if laws[1].ID != "law-2" || laws[1].Tier != 1 || laws[1].Goal != "Be careful" {
		t.Fatalf("law-2 data mismatch: %+v", laws[1])
	}
}

func TestFanOutReview_HistoryArtefactContainsCorrectData(t *testing.T) {
	spy := newAppraiseSpy()
	cfg := defaultTestConfig()
	spy.Laws = []*flowv1.Law{
		{Id: testLawID, Division: "security", Goal: "No secrets"},
	}

	existingFeedback := []*flowv1.FeedbackItem{
		{
			State:   flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED,
			Message: "old issue",
		},
		{
			State:   flowv1.FeedbackState_FEEDBACK_STATE_NEW,
			Message: "new issue",
		},
	}

	setupChildReviewOutputs(spy, reviewOutput{Feedback: []reviewItem{}})

	client := newSpyClient(t, spy)

	_, err := fanOutReview(
		context.Background(), client, cfg,
		spy.Laws, existingFeedback,
		"petition text", "haiku text",
	)
	if err != nil {
		t.Fatalf("fanOutReview() returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	historyRaw := spy.FanOutTasks[0].Artefacts[artefactHistory]
	var history []historyData
	if err := json.Unmarshal(historyRaw, &history); err != nil {
		t.Fatalf("failed to unmarshal history artefact: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 history items, got %d", len(history))
	}
	if history[0].Message != "old issue" {
		t.Fatalf("expected first history message 'old issue', got %q", history[0].Message)
	}
	if history[1].Message != "new issue" {
		t.Fatalf("expected second history message 'new issue', got %q", history[1].Message)
	}
}

func TestFanOutReview_DivisionPromptSuffixFromConfig(t *testing.T) {
	spy := newAppraiseSpy()
	cfg := defaultTestConfig()
	cfg.DivisionPrompts = map[string]string{
		"security": "Extra security instructions.",
	}
	spy.Laws = []*flowv1.Law{
		{Id: testLawID, Division: "security", Goal: "No secrets"},
		{Id: "law-2", Goal: "General law"},
	}

	setupChildReviewOutputs(spy,
		reviewOutput{Feedback: []reviewItem{}},
		reviewOutput{Feedback: []reviewItem{}},
	)

	client := newSpyClient(t, spy)

	_, err := fanOutReview(
		context.Background(), client, cfg,
		spy.Laws, nil,
		"petition text", "haiku text",
	)
	if err != nil {
		t.Fatalf("fanOutReview() returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Find the task for each division.
	for _, task := range spy.FanOutTasks {
		divRaw := task.Artefacts[artefactDivision]
		var div divisionData
		if err := json.Unmarshal(divRaw, &div); err != nil {
			t.Fatalf("failed to unmarshal division: %v", err)
		}

		switch div.Name {
		case "security":
			if div.PromptSuffix != "Extra security instructions." {
				t.Fatalf("security division should have prompt suffix, got %q", div.PromptSuffix)
			}
		case defaultDivision:
			if div.PromptSuffix != "" {
				t.Fatalf("general division should have empty prompt suffix, got %q", div.PromptSuffix)
			}
		default:
			t.Fatalf("unexpected division %q", div.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests — handleAppraise (full orchestrator integration)
// ---------------------------------------------------------------------------

func TestHandleAppraise_HappyPath(t *testing.T) {
	spy := newAppraiseSpy()
	cfg := defaultTestConfig()
	spy.Laws = []*flowv1.Law{
		{Id: testLawID, Division: "security", Goal: "No secrets"},
	}

	// Set up child to return feedback.
	setupChildReviewOutputs(spy, reviewOutput{
		Feedback: []reviewItem{
			{Message: "found issue", Severity: "medium", CitedLaws: []string{testLawID}},
		},
	})

	client := newSpyClient(t, spy)

	// Create eval and finding agents (they won't be triggered — no ACTIONED/WONT_FIX feedback).
	evalMP := &mockModel{}
	eval, err := NewEvalAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewEvalAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(eval.agent, evalMP)

	findingMP := &mockModel{}
	finding, err := NewFindingAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewFindingAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(finding.agent, findingMP)

	err = handleAppraise(context.Background(), client, eval, finding, cfg)
	if err != nil {
		t.Fatalf("handleAppraise() returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Verify stamp was applied.
	if len(spy.StampedArtefacts) != 1 || spy.StampedArtefacts[0] != "review" {
		t.Fatalf("expected stamp 'review', got %v", spy.StampedArtefacts)
	}

	// Verify feedback was raised from child.
	if len(spy.AddedFeedback) != 1 {
		t.Fatalf("expected 1 feedback item raised, got %d", len(spy.AddedFeedback))
	}
	if spy.AddedFeedback[0].Message != "found issue" {
		t.Fatalf("expected feedback 'found issue', got %q", spy.AddedFeedback[0].Message)
	}

	// Verify cite was called.
	if len(spy.CitedLaws) != 1 || spy.CitedLaws[0][0] != testLawID {
		t.Fatalf("expected cited law 'law-1', got %v", spy.CitedLaws)
	}

	// Verify routed to output.
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "default" {
		t.Fatalf("expected route to 'default', got %v", spy.RoutedOutputs)
	}
}

func TestHandleAppraise_NoLaws_NoReview(t *testing.T) {
	spy := newAppraiseSpy()
	cfg := defaultTestConfig()
	// No laws configured.

	client := newSpyClient(t, spy)

	evalMP := &mockModel{}
	eval, err := NewEvalAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewEvalAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(eval.agent, evalMP)

	findingMP := &mockModel{}
	finding, err := NewFindingAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewFindingAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(finding.agent, findingMP)

	err = handleAppraise(context.Background(), client, eval, finding, cfg)
	if err != nil {
		t.Fatalf("handleAppraise() returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// No feedback should be raised (no laws = no fan-out).
	if len(spy.AddedFeedback) != 0 {
		t.Fatalf("expected 0 feedback, got %d", len(spy.AddedFeedback))
	}
	if len(spy.FanOutTasks) != 0 {
		t.Fatalf("expected 0 fan-out tasks, got %d", len(spy.FanOutTasks))
	}

	// Stamp and route should still happen.
	if len(spy.StampedArtefacts) != 1 {
		t.Fatalf("expected stamp, got %v", spy.StampedArtefacts)
	}
	if len(spy.RoutedOutputs) != 1 {
		t.Fatalf("expected route, got %v", spy.RoutedOutputs)
	}
}

func TestHandleAppraise_ChildReturnsNoFeedback(t *testing.T) {
	spy := newAppraiseSpy()
	cfg := defaultTestConfig()
	spy.Laws = []*flowv1.Law{
		{Id: testLawID, Division: "security", Goal: "No secrets"},
	}

	// Child returns empty feedback.
	setupChildReviewOutputs(spy, reviewOutput{Feedback: []reviewItem{}})

	client := newSpyClient(t, spy)

	evalMP := &mockModel{}
	eval, err := NewEvalAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewEvalAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(eval.agent, evalMP)

	findingMP := &mockModel{}
	finding, err := NewFindingAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewFindingAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(finding.agent, findingMP)

	err = handleAppraise(context.Background(), client, eval, finding, cfg)
	if err != nil {
		t.Fatalf("handleAppraise() returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.AddedFeedback) != 0 {
		t.Fatalf("expected 0 feedback for empty child output, got %d", len(spy.AddedFeedback))
	}
}
