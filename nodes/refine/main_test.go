package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

const (
	testFeedbackID  = "fb-1"
	testRouteTarget = "default"
)

// ---------------------------------------------------------------------------
// Helpers for constructing test agents
// ---------------------------------------------------------------------------

func newTestTriageAgent(t *testing.T, mm *mockModel, spy *refineSpy, cfg *refineConfig) *TriageAgent {
	t.Helper()
	client := newSpyClient(t, spy)
	agent, err := NewTriageAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewTriageAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(agent.agent, mm)
	return agent
}

func newTestRevisionAgent(t *testing.T, mm *mockModel, spy *refineSpy, cfg *refineConfig) *RevisionAgent {
	t.Helper()
	client := newSpyClient(t, spy)
	agent, err := NewRevisionAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewRevisionAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(agent.agent, mm)
	return agent
}

// ---------------------------------------------------------------------------
// Tests — TriageAgent: Schema Validation via Run() (table-driven)
// ---------------------------------------------------------------------------

func TestTriageAgent_SchemaValidation(t *testing.T) {
	tests := []struct {
		name      string
		llmOutput string
		wantErr   bool
		errMsg    string
	}{
		{
			name:      "valid_action",
			llmOutput: `{"decision": "action", "message": "I will fix the syllable count"}`,
			wantErr:   false,
		},
		{
			name: "valid_refuse_citation",
			llmOutput: `{"decision": "refuse", "message": "existing law supports this",` +
				` "justification_type": "citation", "citation_ids": ["law-1"]}`,
			wantErr: false,
		},
		{
			name: "valid_refuse_novel_argument",
			llmOutput: `{"decision": "refuse", "message": "the feedback is subjective",` +
				` "justification_type": "novel_argument",` +
				` "argument": "seasonal imagery is not required by any law"}`,
			wantErr: false,
		},
		{
			name:      "rejects_invalid_decision",
			llmOutput: `{"decision": "maybe", "message": "not sure"}`,
			wantErr:   true,
			errMsg:    "output validation failed",
		},
		{
			name:      "rejects_empty_message",
			llmOutput: `{"decision": "action", "message": ""}`,
			wantErr:   true,
		},
		{
			name:      "rejects_missing_decision",
			llmOutput: `{"message": "some fix"}`,
			wantErr:   true,
		},
		{
			name:      "rejects_additional_properties",
			llmOutput: `{"decision": "action", "message": "fix it", "extra": "nope"}`,
			wantErr:   true,
		},
		{
			name:      "rejects_invalid_justification_type",
			llmOutput: `{"decision": "refuse", "message": "nope", "justification_type": "invalid"}`,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultTestConfig()
			spy := newRefineSpy()
			mp := &mockModel{
				output: &flow.InferOutput{
					Output: []byte(tt.llmOutput),
					Cost:   defaultCost(),
				},
			}

			agent := newTestTriageAgent(t, mp, spy, cfg)
			fb := &flowv1.FeedbackItem{
				Id:      testFeedbackID,
				Message: "test feedback",
				State:   flowv1.FeedbackState_FEEDBACK_STATE_NEW,
			}

			out, err := agent.Run(context.Background(), fb, "petition text", "haiku text", nil)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Fatalf("expected error to contain %q, got: %v", tt.errMsg, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
			if out == nil {
				t.Fatal("expected non-nil output")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests — TriageAgent: Template Rendering
// ---------------------------------------------------------------------------

func TestTriageAgent_PromptContainsContext(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"decision": "action", "message": "will fix"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestTriageAgent(t, mp, spy, cfg)
	fb := &flowv1.FeedbackItem{
		Id:       testFeedbackID,
		Message:  "syllable count is wrong",
		Severity: flowv1.Severity_SEVERITY_MEDIUM,
		History: []*flowv1.FeedbackEvent{
			{Actor: "appraise", Action: "add", Message: "raised syllable issue"},
		},
	}

	laws := []*flowv1.Law{
		{Id: "law-1", Tier: 1, Goal: "exactly 5-7-5 syllables"},
	}

	_, err := agent.Run(context.Background(), fb, "write about autumn", "autumn leaves fall", laws)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	query := string(mp.capturedQuery)
	checks := []string{
		"write about autumn",
		"autumn leaves fall",
		"syllable count is wrong",
		"raised syllable issue",
		"law-1",
		"exactly 5-7-5 syllables",
	}
	for _, want := range checks {
		if !strings.Contains(query, want) {
			t.Errorf("query prompt should contain %q, got:\n%s", want, query)
		}
	}
}

func TestTriageAgent_PromptContainsHistory(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"decision": "action", "message": "will fix"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestTriageAgent(t, mp, spy, cfg)
	fb := &flowv1.FeedbackItem{
		Id:      testFeedbackID,
		Message: "test issue",
		History: []*flowv1.FeedbackEvent{
			{Actor: "appraise", Action: "add", Message: "first observation"},
			{Actor: "refine", Action: "resolve", Message: "attempted fix"},
			{Actor: "appraise", Action: "reject", Message: "fix was insufficient"},
		},
	}

	_, err := agent.Run(context.Background(), fb, "petition", "haiku", nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	query := string(mp.capturedQuery)
	checks := []string{
		"first observation",
		"attempted fix",
		"fix was insufficient",
	}
	for _, want := range checks {
		if !strings.Contains(query, want) {
			t.Errorf("query prompt should contain history entry %q, got:\n%s", want, query)
		}
	}
}

func TestTriageAgent_PromptContainsLaws(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"decision": "action", "message": "will fix"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestTriageAgent(t, mp, spy, cfg)
	fb := &flowv1.FeedbackItem{Id: testFeedbackID, Message: "test"}
	laws := []*flowv1.Law{
		{Id: "law-1", Tier: 1, Goal: "exactly 5-7-5 syllables"},
		{Id: "law-2", Tier: 2, Goal: "use seasonal imagery"},
	}

	_, err := agent.Run(context.Background(), fb, "petition", "haiku", laws)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	query := string(mp.capturedQuery)
	checks := []string{"law-1", "law-2", "exactly 5-7-5 syllables", "use seasonal imagery"}
	for _, want := range checks {
		if !strings.Contains(query, want) {
			t.Errorf("query prompt should contain %q, got:\n%s", want, query)
		}
	}
}

func TestTriageAgent_PromptOmitsLawsWhenNone(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"decision": "action", "message": "will fix"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestTriageAgent(t, mp, spy, cfg)
	fb := &flowv1.FeedbackItem{Id: testFeedbackID, Message: "test"}

	_, err := agent.Run(context.Background(), fb, "petition", "haiku", nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	query := string(mp.capturedQuery)
	if strings.Contains(query, "APPLICABLE LAWS") {
		t.Errorf("query should not contain APPLICABLE LAWS when no laws, got:\n%s", query)
	}
}

func TestTriageAgent_SystemPromptContainsConfig(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"decision": "action", "message": "will fix"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestTriageAgent(t, mp, spy, cfg)
	fb := &flowv1.FeedbackItem{Id: testFeedbackID, Message: "test"}

	_, err := agent.Run(context.Background(), fb, "", "", nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(mp.capturedSystem, "haiku") {
		t.Errorf("system prompt should contain output artefact name, got:\n%s", mp.capturedSystem)
	}
}

// ---------------------------------------------------------------------------
// Tests — RevisionAgent: Schema Validation via Run() (table-driven)
// ---------------------------------------------------------------------------

func TestRevisionAgent_SchemaValidation(t *testing.T) {
	tests := []struct {
		name      string
		llmOutput string
		wantErr   bool
	}{
		{
			name:      "valid_output",
			llmOutput: `{"haiku": "autumn moonlight\na worm digs silently\ninto the chestnut"}`,
			wantErr:   false,
		},
		{
			name:      "rejects_empty_output_field",
			llmOutput: `{"haiku": ""}`,
			wantErr:   true,
		},
		{
			name:      "rejects_missing_output_field",
			llmOutput: `{}`,
			wantErr:   true,
		},
		{
			name:      "rejects_additional_properties",
			llmOutput: `{"haiku": "test haiku", "extra": "nope"}`,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultTestConfig()
			spy := newRefineSpy()
			mp := &mockModel{
				output: &flow.InferOutput{
					Output: []byte(tt.llmOutput),
					Cost:   defaultCost(),
				},
			}

			agent := newTestRevisionAgent(t, mp, spy, cfg)
			actioned := []actionedItem{
				{FeedbackID: testFeedbackID, Message: "issue", FixDesc: "fix"},
			}

			result, err := agent.Run(context.Background(), "petition", "haiku", nil, actioned)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
			if result == "" {
				t.Fatal("expected non-empty result")
			}
		})
	}
}

func TestRevisionAgent_ExtractsOutputField(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"haiku": "autumn moonlight\na worm digs silently\ninto the chestnut"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestRevisionAgent(t, mp, spy, cfg)
	actioned := []actionedItem{
		{FeedbackID: testFeedbackID, Message: "issue", FixDesc: "fix"},
	}

	result, err := agent.Run(context.Background(), "petition", "haiku", nil, actioned)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(result, "autumn moonlight") {
		t.Fatalf("expected result to contain haiku content, got: %q", result)
	}
}

// ---------------------------------------------------------------------------
// Tests — RevisionAgent: Template Rendering
// ---------------------------------------------------------------------------

func TestRevisionAgent_PromptContainsContext(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"haiku": "revised haiku"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestRevisionAgent(t, mp, spy, cfg)
	laws := []*flowv1.Law{
		{Id: "law-1", Tier: 1, Goal: "use seasonal imagery"},
	}
	actioned := []actionedItem{
		{FeedbackID: testFeedbackID, Message: "syllable count wrong", FixDesc: "will fix syllables"},
		{FeedbackID: "fb-2", Message: "theme mismatch", FixDesc: "will align with petition"},
	}

	_, err := agent.Run(context.Background(), "write about autumn", "autumn leaves fall", laws, actioned)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	query := string(mp.capturedQuery)
	checks := []string{
		"write about autumn",
		"autumn leaves fall",
		"law-1",
		"syllable count wrong",
		"will fix syllables",
		"will align with petition",
		"FIXES TO APPLY",
	}
	for _, want := range checks {
		if !strings.Contains(query, want) {
			t.Errorf("query prompt should contain %q, got:\n%s", want, query)
		}
	}
}

func TestRevisionAgent_PromptOmitsLawsWhenNone(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"haiku": "revised haiku"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestRevisionAgent(t, mp, spy, cfg)
	actioned := []actionedItem{
		{FeedbackID: testFeedbackID, Message: "issue", FixDesc: "fix"},
	}

	_, err := agent.Run(context.Background(), "petition", "haiku", nil, actioned)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	query := string(mp.capturedQuery)
	if strings.Contains(query, "GOVERNANCE LAWS") {
		t.Errorf("query should not contain GOVERNANCE LAWS when no laws, got:\n%s", query)
	}
	if !strings.Contains(query, "FIXES TO APPLY") {
		t.Errorf("query should contain FIXES TO APPLY even without laws, got:\n%s", query)
	}
}

func TestRevisionAgent_SystemPromptContainsConfig(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"haiku": "revised haiku"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestRevisionAgent(t, mp, spy, cfg)
	actioned := []actionedItem{
		{FeedbackID: testFeedbackID, Message: "issue", FixDesc: "fix"},
	}

	_, err := agent.Run(context.Background(), "petition", "haiku", nil, actioned)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(mp.capturedSystem, "haiku") {
		t.Errorf("system prompt should contain output artefact name, got:\n%s", mp.capturedSystem)
	}
}

// ---------------------------------------------------------------------------
// Tests — triageFeedback (Phase 1 orchestration)
// ---------------------------------------------------------------------------

func TestTriageFeedback_ActionPath(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"decision": "action", "message": "will fix syllable count"}`),
			Cost:   defaultCost(),
		},
	}

	client := newSpyClient(t, spy)
	triage, err := NewTriageAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewTriageAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(triage.agent, mp)

	feedback := []*flowv1.FeedbackItem{
		{
			Id:       testFeedbackID,
			State:    flowv1.FeedbackState_FEEDBACK_STATE_NEW,
			Message:  "syllable count is wrong",
			Severity: flowv1.Severity_SEVERITY_MEDIUM,
		},
	}

	actioned, err := triageFeedback(
		context.Background(), triage, client,
		feedback, "write a haiku", "test haiku", nil,
	)
	if err != nil {
		t.Fatalf("triageFeedback() error: %v", err)
	}

	if len(actioned) != 1 {
		t.Fatalf("expected 1 actioned item, got %d", len(actioned))
	}
	if actioned[0].FeedbackID != testFeedbackID {
		t.Fatalf("expected actioned feedback ID %q, got %q", testFeedbackID, actioned[0].FeedbackID)
	}
	if actioned[0].FixDesc != "will fix syllable count" {
		t.Fatalf("expected fix desc, got %q", actioned[0].FixDesc)
	}

	msg, ok := spy.ResolvedFeedback[testFeedbackID]
	if !ok {
		t.Fatalf("expected ResolveFeedback for %s", testFeedbackID)
	}
	if msg != "will fix syllable count" {
		t.Fatalf("expected resolve message 'will fix syllable count', got %q", msg)
	}
}

func TestTriageFeedback_RefuseWithCitation(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"decision": "refuse", "message": "law says so",` +
				` "justification_type": "citation", "citation_ids": ["law-1"]}`),
			Cost: defaultCost(),
		},
	}

	client := newSpyClient(t, spy)
	triage, err := NewTriageAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewTriageAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(triage.agent, mp)

	feedback := []*flowv1.FeedbackItem{
		{
			Id:       "fb-2",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_NEW,
			Message:  "use different imagery",
			Severity: flowv1.Severity_SEVERITY_LOW,
		},
	}

	actioned, err := triageFeedback(
		context.Background(), triage, client,
		feedback, "write a haiku", "test haiku", nil,
	)
	if err != nil {
		t.Fatalf("triageFeedback() error: %v", err)
	}

	if len(actioned) != 0 {
		t.Fatalf("expected 0 actioned items for refusal, got %d", len(actioned))
	}

	just, ok := spy.RefusedFeedback["fb-2"]
	if !ok {
		t.Fatal("expected RefuseFeedback for fb-2")
	}
	cit := just.GetCitation()
	if cit == nil {
		t.Fatal("expected Citation justification")
	}
	if len(cit.GetCitationIds()) != 1 || cit.GetCitationIds()[0] != "law-1" {
		t.Fatalf("unexpected citation IDs: %v", cit.GetCitationIds())
	}
}

func TestTriageFeedback_RefuseWithNovelArgument(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"decision": "refuse", "message": "imagery serves the petition",` +
				` "justification_type": "novel_argument",` +
				` "argument": "the autumn theme requires this exact imagery"}`),
			Cost: defaultCost(),
		},
	}

	client := newSpyClient(t, spy)
	triage, err := NewTriageAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewTriageAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(triage.agent, mp)

	feedback := []*flowv1.FeedbackItem{
		{
			Id:       "fb-3",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_NEW,
			Message:  "change the seasonal reference",
			Severity: flowv1.Severity_SEVERITY_LOW,
		},
	}

	actioned, err := triageFeedback(
		context.Background(), triage, client,
		feedback, "write a haiku", "test haiku", nil,
	)
	if err != nil {
		t.Fatalf("triageFeedback() error: %v", err)
	}

	if len(actioned) != 0 {
		t.Fatalf("expected 0 actioned items, got %d", len(actioned))
	}

	just, ok := spy.RefusedFeedback["fb-3"]
	if !ok {
		t.Fatal("expected RefuseFeedback for fb-3")
	}
	novel := just.GetNovelArgument()
	if novel == nil {
		t.Fatal("expected NovelArgument justification")
	}
	if novel.GetArgument() != "the autumn theme requires this exact imagery" {
		t.Fatalf("unexpected argument: %q", novel.GetArgument())
	}
}

func TestTriageFeedback_SkipsNonTriageableStates(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	mp := &mockModel{} // no output — should not be called

	client := newSpyClient(t, spy)
	triage, err := NewTriageAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewTriageAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(triage.agent, mp)

	feedback := []*flowv1.FeedbackItem{
		{Id: "fb-resolved", State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED, Message: "done"},
		{Id: "fb-actioned", State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED, Message: "already actioned"},
		{Id: "fb-wontfix", State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX, Message: "already refused"},
		{Id: "fb-deadlocked", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED, Message: "deadlocked"},
	}

	actioned, err := triageFeedback(
		context.Background(), triage, client,
		feedback, "petition", "haiku", nil,
	)
	if err != nil {
		t.Fatalf("triageFeedback() error: %v", err)
	}

	if len(actioned) != 0 {
		t.Fatalf("expected 0 actioned items, got %d", len(actioned))
	}
	if len(spy.ResolvedFeedback) != 0 || len(spy.RefusedFeedback) != 0 {
		t.Fatal("expected no feedback operations for non-triageable states")
	}
}

func TestTriageFeedback_RejectedStateIsTriageable(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"decision": "action", "message": "will try again"}`),
			Cost:   defaultCost(),
		},
	}

	client := newSpyClient(t, spy)
	triage, err := NewTriageAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewTriageAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(triage.agent, mp)

	feedback := []*flowv1.FeedbackItem{
		{
			Id:       "fb-rejected",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_REJECTED,
			Message:  "previous fix was inadequate",
			Severity: flowv1.Severity_SEVERITY_HIGH,
			History: []*flowv1.FeedbackEvent{
				{Actor: "refine", Action: "resolve", Message: "fixed syllables"},
				{Actor: "appraise", Action: "reject", Message: "fix was insufficient"},
			},
		},
	}

	actioned, err := triageFeedback(
		context.Background(), triage, client,
		feedback, "write a haiku", "test haiku", nil,
	)
	if err != nil {
		t.Fatalf("triageFeedback() error: %v", err)
	}

	if len(actioned) != 1 {
		t.Fatalf("expected 1 actioned item (REJECTED should be triageable), got %d", len(actioned))
	}

	_, ok := spy.ResolvedFeedback["fb-rejected"]
	if !ok {
		t.Fatal("expected ResolveFeedback for fb-rejected")
	}
}

func TestTriageFeedback_RejectedCanBeRefusedAgain(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"decision": "refuse", "message": "still disagree",` +
				` "justification_type": "novel_argument",` +
				` "argument": "stronger reasoning this time"}`),
			Cost: defaultCost(),
		},
	}

	client := newSpyClient(t, spy)
	triage, err := NewTriageAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewTriageAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(triage.agent, mp)

	feedback := []*flowv1.FeedbackItem{
		{
			Id:       "fb-rejected-again",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_REJECTED,
			Message:  "prior refusal was weak",
			Severity: flowv1.Severity_SEVERITY_MEDIUM,
		},
	}

	actioned, err := triageFeedback(
		context.Background(), triage, client,
		feedback, "write a haiku", "test haiku", nil,
	)
	if err != nil {
		t.Fatalf("triageFeedback() error: %v", err)
	}

	if len(actioned) != 0 {
		t.Fatalf("expected 0 actioned items (REJECTED re-refused), got %d", len(actioned))
	}

	just, ok := spy.RefusedFeedback["fb-rejected-again"]
	if !ok {
		t.Fatal("expected RefuseFeedback for fb-rejected-again")
	}
	if just.GetNovelArgument().GetArgument() != "stronger reasoning this time" {
		t.Fatalf("unexpected argument: %q", just.GetNovelArgument().GetArgument())
	}
}

func TestTriageFeedback_ContemptGuardForcesAction(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	mp := &mockModel{} // no output — contempt guard should skip LLM entirely

	client := newSpyClient(t, spy)
	triage, err := NewTriageAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewTriageAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(triage.agent, mp)

	feedback := []*flowv1.FeedbackItem{
		{
			Id:           "fb-contempt",
			State:        flowv1.FeedbackState_FEEDBACK_STATE_REJECTED,
			Message:      "must comply with ruling",
			Severity:     flowv1.Severity_SEVERITY_CRITICAL,
			LinkedRuling: "ruling-001",
		},
	}

	actioned, err := triageFeedback(
		context.Background(), triage, client,
		feedback, "write a haiku", "test haiku", nil,
	)
	if err != nil {
		t.Fatalf("triageFeedback() error: %v", err)
	}

	if len(actioned) != 1 {
		t.Fatalf("expected 1 actioned item (contempt guard), got %d", len(actioned))
	}
	if actioned[0].FixDesc != contemptMessage {
		t.Fatalf("expected fix desc %q, got %q", contemptMessage, actioned[0].FixDesc)
	}

	msg, ok := spy.ResolvedFeedback["fb-contempt"]
	if !ok {
		t.Fatal("expected ResolveFeedback for fb-contempt")
	}
	if msg != contemptMessage {
		t.Fatalf("expected resolve message %q, got %q", contemptMessage, msg)
	}

	// Should NOT have called RefuseFeedback.
	if len(spy.RefusedFeedback) != 0 {
		t.Fatalf("expected no refusals (contempt guard), got: %v", spy.RefusedFeedback)
	}
}

func TestTriageFeedback_ContemptGuardOnlyOnRejected(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"decision": "action", "message": "will fix it"}`),
			Cost:   defaultCost(),
		},
	}

	client := newSpyClient(t, spy)
	triage, err := NewTriageAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewTriageAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(triage.agent, mp)

	feedback := []*flowv1.FeedbackItem{
		{
			Id:           "fb-new-with-ruling",
			State:        flowv1.FeedbackState_FEEDBACK_STATE_NEW,
			Message:      "some issue",
			Severity:     flowv1.Severity_SEVERITY_MEDIUM,
			LinkedRuling: "ruling-002",
		},
	}

	actioned, err := triageFeedback(
		context.Background(), triage, client,
		feedback, "write a haiku", "test haiku", nil,
	)
	if err != nil {
		t.Fatalf("triageFeedback() error: %v", err)
	}

	if len(actioned) != 1 {
		t.Fatalf("expected 1 actioned item (NEW goes through LLM), got %d", len(actioned))
	}
	// Should have gone through LLM, not forced.
	if actioned[0].FixDesc == contemptMessage {
		t.Fatal("NEW item with linked_ruling should NOT be force-actioned via contempt guard")
	}
}

func TestTriageFeedback_ParallelMultipleItems(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"decision": "action", "message": "will fix it"}`),
			Cost:   defaultCost(),
		},
	}

	client := newSpyClient(t, spy)
	triage, err := NewTriageAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewTriageAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(triage.agent, mp)

	feedback := []*flowv1.FeedbackItem{
		{Id: "fb-a", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW, Message: "issue A"},
		{Id: "fb-b", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW, Message: "issue B"},
		{Id: "fb-c", State: flowv1.FeedbackState_FEEDBACK_STATE_REJECTED, Message: "issue C"},
	}

	actioned, err := triageFeedback(
		context.Background(), triage, client,
		feedback, "petition", "haiku", nil,
	)
	if err != nil {
		t.Fatalf("triageFeedback() error: %v", err)
	}

	if len(actioned) != 3 {
		t.Fatalf("expected 3 actioned items, got %d", len(actioned))
	}
	if len(spy.ResolvedFeedback) != 3 {
		t.Fatalf("expected 3 resolved items, got %d", len(spy.ResolvedFeedback))
	}
}

func TestTriageFeedback_EmptyFeedback(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	mp := &mockModel{} // no output — should not be called

	client := newSpyClient(t, spy)
	triage, err := NewTriageAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewTriageAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(triage.agent, mp)

	actioned, err := triageFeedback(
		context.Background(), triage, client,
		nil, "petition", "haiku", nil,
	)
	if err != nil {
		t.Fatalf("triageFeedback() error: %v", err)
	}

	if len(actioned) != 0 {
		t.Fatalf("expected 0 actioned items for empty feedback, got %d", len(actioned))
	}
}

// ---------------------------------------------------------------------------
// Tests — buildJustification
// ---------------------------------------------------------------------------

func TestBuildJustification(t *testing.T) {
	tests := []struct {
		name    string
		out     triageOutput
		wantErr bool
		errMsg  string
		check   func(*testing.T, *flowv1.Justification)
	}{
		{
			name: "citation_with_ids",
			out: triageOutput{
				JustificationType: justTypeCitation,
				CitationIDs:       []string{"law-1", "law-2"},
			},
			check: func(t *testing.T, j *flowv1.Justification) {
				cit := j.GetCitation()
				if cit == nil {
					t.Fatal("expected Citation justification")
				}
				if len(cit.GetCitationIds()) != 2 {
					t.Fatalf("expected 2 citation IDs, got %d", len(cit.GetCitationIds()))
				}
			},
		},
		{
			name: "novel_argument",
			out: triageOutput{
				JustificationType: justTypeNovelArgument,
				Argument:          "artistic reasoning",
			},
			check: func(t *testing.T, j *flowv1.Justification) {
				novel := j.GetNovelArgument()
				if novel == nil {
					t.Fatal("expected NovelArgument justification")
				}
				if novel.GetArgument() != "artistic reasoning" {
					t.Fatalf("unexpected argument: %q", novel.GetArgument())
				}
			},
		},
		{
			name: "citation_without_ids",
			out: triageOutput{
				JustificationType: justTypeCitation,
				CitationIDs:       nil,
			},
			wantErr: true,
			errMsg:  "at least one citation_id",
		},
		{
			name: "novel_argument_empty",
			out: triageOutput{
				JustificationType: justTypeNovelArgument,
				Argument:          "",
			},
			wantErr: true,
			errMsg:  "non-empty argument",
		},
		{
			name: "missing_type",
			out: triageOutput{
				JustificationType: "",
			},
			wantErr: true,
			errMsg:  "requires justification_type",
		},
		{
			name: "invalid_type",
			out: triageOutput{
				JustificationType: "precedent",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			just, err := buildJustification(tt.out)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Fatalf("expected error to contain %q, got: %v", tt.errMsg, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildJustification() error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, just)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests — handleRefine (full orchestration)
// ---------------------------------------------------------------------------

func TestHandleRefine_AllActioned(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:       testFeedbackID,
			State:    flowv1.FeedbackState_FEEDBACK_STATE_NEW,
			Message:  "syllable count is wrong",
			Severity: flowv1.Severity_SEVERITY_MEDIUM,
		},
	}

	triageOutput := `{"decision": "action", "message": "will fix syllables"}`
	revisionOutput := `{"haiku": "revised haiku content"}`

	mp := &mockModel{
		outputs: []*flow.InferOutput{
			{Output: []byte(triageOutput), Cost: defaultCost()},
			{Output: []byte(revisionOutput), Cost: defaultCost()},
		},
	}

	client := newSpyClient(t, spy)

	triage, err := NewTriageAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewTriageAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(triage.agent, mp)
	revision, err := NewRevisionAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewRevisionAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(revision.agent, mp)

	if err := handleRefine(context.Background(), client, triage, revision, cfg); err != nil {
		t.Fatalf("handleRefine() returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Verify feedback was resolved.
	if len(spy.ResolvedFeedback) != 1 {
		t.Fatalf("expected 1 resolved feedback, got %d", len(spy.ResolvedFeedback))
	}

	// Verify artefact was stored with revised content.
	if len(spy.StoredArtefacts) != 1 {
		t.Fatalf("expected 1 stored artefact, got %d", len(spy.StoredArtefacts))
	}
	if string(spy.StoredArtefacts[0].Content) != "revised haiku content" {
		t.Fatalf("expected revised content, got %q", string(spy.StoredArtefacts[0].Content))
	}

	// Verify routing.
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != testRouteTarget {
		t.Fatalf("expected route to 'default', got %v", spy.RoutedOutputs)
	}
}

func TestHandleRefine_AllRefused(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:       testFeedbackID,
			State:    flowv1.FeedbackState_FEEDBACK_STATE_NEW,
			Message:  "use different imagery",
			Severity: flowv1.Severity_SEVERITY_LOW,
		},
	}

	refuseOutput := `{"decision": "refuse", "message": "law says so",` +
		` "justification_type": "citation", "citation_ids": ["law-1"]}`
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(refuseOutput),
			Cost:   defaultCost(),
		},
	}

	client := newSpyClient(t, spy)

	triage, err := NewTriageAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewTriageAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(triage.agent, mp)
	revision, err := NewRevisionAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewRevisionAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(revision.agent, mp)

	if err := handleRefine(context.Background(), client, triage, revision, cfg); err != nil {
		t.Fatalf("handleRefine() returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Verify feedback was refused.
	if len(spy.RefusedFeedback) != 1 {
		t.Fatalf("expected 1 refused feedback, got %d", len(spy.RefusedFeedback))
	}

	// Verify artefact was stored with existing content (unchanged).
	if len(spy.StoredArtefacts) != 1 {
		t.Fatalf("expected 1 stored artefact, got %d", len(spy.StoredArtefacts))
	}
	if string(spy.StoredArtefacts[0].Content) != testContent {
		t.Fatalf("expected existing content 'test-content', got %q",
			string(spy.StoredArtefacts[0].Content))
	}

	// Verify routing.
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != testRouteTarget {
		t.Fatalf("expected route to 'default', got %v", spy.RoutedOutputs)
	}
}

func TestHandleRefine_MixedItems(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:       testFeedbackID,
			State:    flowv1.FeedbackState_FEEDBACK_STATE_NEW,
			Message:  "syllable count is wrong",
			Severity: flowv1.Severity_SEVERITY_MEDIUM,
		},
		{
			Id:       "fb-2",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_NEW,
			Message:  "use different imagery",
			Severity: flowv1.Severity_SEVERITY_LOW,
		},
	}

	actionOutput := `{"decision": "action", "message": "will fix syllables"}`
	refuseOutput := `{"decision": "refuse", "message": "law says so",` +
		` "justification_type": "citation", "citation_ids": ["law-1"]}`
	revisionOutput := `{"haiku": "revised haiku for mixed"}`

	mp := &mockModel{
		outputs: []*flow.InferOutput{
			{Output: []byte(actionOutput), Cost: defaultCost()},
			{Output: []byte(refuseOutput), Cost: defaultCost()},
			{Output: []byte(revisionOutput), Cost: defaultCost()},
		},
	}

	client := newSpyClient(t, spy)

	triage, err := NewTriageAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewTriageAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(triage.agent, mp)
	revision, err := NewRevisionAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewRevisionAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(revision.agent, mp)

	if err := handleRefine(context.Background(), client, triage, revision, cfg); err != nil {
		t.Fatalf("handleRefine() returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Verify: 1 resolved, 1 refused.
	if len(spy.ResolvedFeedback) != 1 {
		t.Fatalf("expected 1 resolved feedback, got %d", len(spy.ResolvedFeedback))
	}
	if len(spy.RefusedFeedback) != 1 {
		t.Fatalf("expected 1 refused feedback, got %d", len(spy.RefusedFeedback))
	}

	// Verify revision was called (actioned items > 0).
	if len(spy.StoredArtefacts) != 1 {
		t.Fatalf("expected 1 stored artefact, got %d", len(spy.StoredArtefacts))
	}
	storedContent := string(spy.StoredArtefacts[0].Content)
	if storedContent != "revised haiku for mixed" {
		t.Fatalf("expected revised content for mixed, got %q", storedContent)
	}

	// Verify routing.
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != testRouteTarget {
		t.Fatalf("expected route to 'default', got %v", spy.RoutedOutputs)
	}
}

func TestHandleRefine_NoFeedback(t *testing.T) {
	cfg := defaultTestConfig()
	spy := newRefineSpy()
	// No feedback items — spy default is empty

	mp := &mockModel{} // no output — should not be called

	client := newSpyClient(t, spy)

	triage, err := NewTriageAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewTriageAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(triage.agent, mp)
	revision, err := NewRevisionAgent(client, cfg)
	if err != nil {
		t.Fatalf("NewRevisionAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(revision.agent, mp)

	if err := handleRefine(context.Background(), client, triage, revision, cfg); err != nil {
		t.Fatalf("handleRefine() returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Verify existing content stored unchanged.
	if len(spy.StoredArtefacts) != 1 {
		t.Fatalf("expected 1 stored artefact, got %d", len(spy.StoredArtefacts))
	}
	if string(spy.StoredArtefacts[0].Content) != testContent {
		t.Fatalf("expected existing content 'test-content', got %q",
			string(spy.StoredArtefacts[0].Content))
	}
	if spy.StoredArtefacts[0].ArtefactID != "haiku" {
		t.Fatalf("expected artefact ID 'haiku', got %q", spy.StoredArtefacts[0].ArtefactID)
	}
	if spy.StoredArtefacts[0].GovernedArtefact != "haiku" {
		t.Fatalf("expected governed artefact 'haiku', got %q",
			spy.StoredArtefacts[0].GovernedArtefact)
	}

	// Verify routing.
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != testRouteTarget {
		t.Fatalf("expected route to 'default', got %v", spy.RoutedOutputs)
	}
}

// ---------------------------------------------------------------------------
// Tests — triageOutput unmarshalling (kept for completeness)
// ---------------------------------------------------------------------------

func TestTriageOutput_Unmarshal(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		decision string
		message  string
	}{
		{
			name:     "action",
			raw:      `{"decision": "action", "message": "will fix syllables"}`,
			decision: decisionAction,
			message:  "will fix syllables",
		},
		{
			name: "refuse",
			raw: `{"decision": "refuse", "message": "law supports me",` +
				` "justification_type": "citation", "citation_ids": ["law-42"]}`,
			decision: decisionRefuse,
			message:  "law supports me",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out triageOutput
			if err := json.Unmarshal([]byte(tt.raw), &out); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			if out.Decision != tt.decision {
				t.Fatalf("expected decision %q, got %q", tt.decision, out.Decision)
			}
			if out.Message != tt.message {
				t.Fatalf("expected message %q, got %q", tt.message, out.Message)
			}
		})
	}
}
