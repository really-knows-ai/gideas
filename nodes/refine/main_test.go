package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/handlers"
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
			actioned := []flow.ActionedFeedback{
				{FeedbackID: testFeedbackID, Message: "issue", FixDescription: "fix"},
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
	actioned := []flow.ActionedFeedback{
		{FeedbackID: testFeedbackID, Message: "issue", FixDescription: "fix"},
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
	actioned := []flow.ActionedFeedback{
		{FeedbackID: testFeedbackID, Message: "syllable count wrong", FixDescription: "will fix syllables"},
		{FeedbackID: "fb-2", Message: "theme mismatch", FixDescription: "will align with petition"},
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
	actioned := []flow.ActionedFeedback{
		{FeedbackID: testFeedbackID, Message: "issue", FixDescription: "fix"},
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
	actioned := []flow.ActionedFeedback{
		{FeedbackID: testFeedbackID, Message: "issue", FixDescription: "fix"},
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
// Tests — HandleRefine (full orchestration via shared handler)
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

	triageOut := `{"decision": "action", "message": "will fix syllables"}`
	revisionOut := `{"haiku": "revised haiku content"}`

	mp := &mockModel{
		outputs: []*flow.InferOutput{
			{Output: []byte(triageOut), Cost: defaultCost()},
			{Output: []byte(revisionOut), Cost: defaultCost()},
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

	handlerCfg := handlers.RefineConfig{
		InputArtefacts:   cfg.InputArtefacts,
		OutputArtefact:   cfg.OutputArtefact,
		GovernedArtefact: cfg.GovernedArtefact,
	}

	if err := handlers.HandleRefine(context.Background(), client, triage, revision, handlerCfg); err != nil {
		t.Fatalf("HandleRefine() returned error: %v", err)
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

	refuseOut := `{"decision": "refuse", "message": "law says so",` +
		` "justification_type": "citation", "citation_ids": ["law-1"]}`
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(refuseOut),
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

	handlerCfg := handlers.RefineConfig{
		InputArtefacts:   cfg.InputArtefacts,
		OutputArtefact:   cfg.OutputArtefact,
		GovernedArtefact: cfg.GovernedArtefact,
	}

	if err := handlers.HandleRefine(context.Background(), client, triage, revision, handlerCfg); err != nil {
		t.Fatalf("HandleRefine() returned error: %v", err)
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

	actionOut := `{"decision": "action", "message": "will fix syllables"}`
	refuseOut := `{"decision": "refuse", "message": "law says so",` +
		` "justification_type": "citation", "citation_ids": ["law-1"]}`
	revisionOut := `{"haiku": "revised haiku for mixed"}`

	mp := &mockModel{
		outputs: []*flow.InferOutput{
			{Output: []byte(actionOut), Cost: defaultCost()},
			{Output: []byte(refuseOut), Cost: defaultCost()},
			{Output: []byte(revisionOut), Cost: defaultCost()},
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

	handlerCfg := handlers.RefineConfig{
		InputArtefacts:   cfg.InputArtefacts,
		OutputArtefact:   cfg.OutputArtefact,
		GovernedArtefact: cfg.GovernedArtefact,
	}

	if err := handlers.HandleRefine(context.Background(), client, triage, revision, handlerCfg); err != nil {
		t.Fatalf("HandleRefine() returned error: %v", err)
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

	handlerCfg := handlers.RefineConfig{
		InputArtefacts:   cfg.InputArtefacts,
		OutputArtefact:   cfg.OutputArtefact,
		GovernedArtefact: cfg.GovernedArtefact,
	}

	if err := handlers.HandleRefine(context.Background(), client, triage, revision, handlerCfg); err != nil {
		t.Fatalf("HandleRefine() returned error: %v", err)
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
			decision: "action",
			message:  "will fix syllables",
		},
		{
			name: "refuse",
			raw: `{"decision": "refuse", "message": "law supports me",` +
				` "justification_type": "citation", "citation_ids": ["law-42"]}`,
			decision: "refuse",
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

// ---------------------------------------------------------------------------
// Tests — ConfigMap prompt overrides
// ---------------------------------------------------------------------------

func TestTriageAgent_ConfigMapOverrideSystemPrompt(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.TriageSystemPrompt = "You are an overridden {{.OutputArtefact}} triage system."

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

	if !strings.Contains(mp.capturedSystem, "overridden haiku triage system") {
		t.Errorf("system prompt should use override, got:\n%s", mp.capturedSystem)
	}
}

func TestTriageAgent_ConfigMapOverrideQueryTemplate(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.TriageQueryTemplate = "CUSTOM QUERY: {{.FeedbackMessage}}"

	spy := newRefineSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"decision": "action", "message": "will fix"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestTriageAgent(t, mp, spy, cfg)
	fb := &flowv1.FeedbackItem{Id: testFeedbackID, Message: "custom feedback msg"}

	_, err := agent.Run(context.Background(), fb, "petition", "haiku", nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	query := string(mp.capturedQuery)
	if !strings.Contains(query, "CUSTOM QUERY: custom feedback msg") {
		t.Errorf("query prompt should use override, got:\n%s", query)
	}
}

func TestRevisionAgent_ConfigMapOverrideSystemPrompt(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.RevisionSystemPrompt = "You are an overridden {{.OutputArtefact}} revision system."

	spy := newRefineSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"haiku": "revised content"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestRevisionAgent(t, mp, spy, cfg)
	actioned := []flow.ActionedFeedback{
		{FeedbackID: testFeedbackID, Message: "issue", FixDescription: "fix"},
	}

	_, err := agent.Run(context.Background(), "petition", "haiku", nil, actioned)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(mp.capturedSystem, "overridden haiku revision system") {
		t.Errorf("system prompt should use override, got:\n%s", mp.capturedSystem)
	}
}

func TestRevisionAgent_ConfigMapOverrideQueryTemplate(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.RevisionQueryTemplate = "CUSTOM REVISION: {{.Fixes}}"

	spy := newRefineSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"haiku": "revised content"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestRevisionAgent(t, mp, spy, cfg)
	actioned := []flow.ActionedFeedback{
		{FeedbackID: testFeedbackID, Message: "the issue", FixDescription: "the fix"},
	}

	_, err := agent.Run(context.Background(), "petition", "haiku", nil, actioned)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	query := string(mp.capturedQuery)
	if !strings.Contains(query, "CUSTOM REVISION:") {
		t.Errorf("query prompt should use override, got:\n%s", query)
	}
	if !strings.Contains(query, "the fix") {
		t.Errorf("query prompt should contain fix description, got:\n%s", query)
	}
}

func TestTriageAgent_EmptyOverrideUsesDefault(t *testing.T) {
	cfg := defaultTestConfig()
	// Leave overrides empty — should use baked-in defaults.

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

	// Default system prompt should contain the standard text.
	if !strings.Contains(mp.capturedSystem, "poet deciding how to handle feedback") {
		t.Errorf("system prompt should use default when override is empty, got:\n%s", mp.capturedSystem)
	}
}

func TestRevisionAgent_EmptyOverrideUsesDefault(t *testing.T) {
	cfg := defaultTestConfig()
	// Leave overrides empty — should use baked-in defaults.

	spy := newRefineSpy()
	mp := &mockModel{
		output: &flow.InferOutput{
			Output: []byte(`{"haiku": "revised content"}`),
			Cost:   defaultCost(),
		},
	}

	agent := newTestRevisionAgent(t, mp, spy, cfg)
	actioned := []flow.ActionedFeedback{
		{FeedbackID: testFeedbackID, Message: "issue", FixDescription: "fix"},
	}

	_, err := agent.Run(context.Background(), "petition", "haiku", nil, actioned)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// Default system prompt should contain the standard text.
	if !strings.Contains(mp.capturedSystem, "poet revising your work") {
		t.Errorf("system prompt should use default when override is empty, got:\n%s", mp.capturedSystem)
	}
}
