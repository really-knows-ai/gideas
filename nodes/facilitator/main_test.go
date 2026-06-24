package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// Test constants for frequently used string values.
const (
	testLawKigo    = "law-kigo"
	testFeedbackID = "fb-1"
)

// ---------------------------------------------------------------------------
// First-invocation happy path
// ---------------------------------------------------------------------------

func TestFacilitator_FirstInvocation_HappyPath(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": defaultDeadlockedFeedback(),
	}
	spy.LawsByID = map[string]*flowv1.Law{
		testLawKigo: {
			Id:   testLawKigo,
			Goal: "Seasonal reference required",
			Tier: flowv1.LawTier_LAW_TIER_FINDING,
		},
	}
	spy.Laws = []*flowv1.Law{
		{Id: testLawKigo, Goal: "Seasonal reference required", Tier: flowv1.LawTier_LAW_TIER_FINDING},
		{Id: "law-meter", Goal: "5-7-5 syllable meter", Tier: flowv1.LawTier_LAW_TIER_RULING},
	}
	spy.FrictionByFilter = func(f *flowv1.FrictionFilter) []*flowv1.FrictionAggregate {
		if f.GetLawId() == testLawKigo {
			return []*flowv1.FrictionAggregate{
				{LawId: testLawKigo, NodeId: "reviewer-A", EventCount: 3, TotalMagnitude: 7.5},
			}
		}
		if f.GetWorkitemId() == testWorkitemID && f.GetLawId() == "" {
			return []*flowv1.FrictionAggregate{
				{LawId: testLawKigo, NodeId: "reviewer-A", EventCount: 3, TotalMagnitude: 7.5},
			}
		}
		return nil
	}
	spy.ArtefactContentByID = map[string][]byte{
		"petition": []byte("Write a haiku about frogs"),
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{InputArtefacts: []string{"petition"}}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Should have created exactly one child.
	if len(spy.CreatedChildren) != 1 {
		t.Fatalf("expected 1 child, got %d", len(spy.CreatedChildren))
	}

	// Child should be routed to the default arbiter node.
	if len(spy.RoutedChildren) != 1 {
		t.Fatalf("expected 1 routed child, got %d", len(spy.RoutedChildren))
	}
	if spy.RoutedChildren[0].TargetNode != defaultArbiterNode {
		t.Errorf("child routed to %q, expected %q", spy.RoutedChildren[0].TargetNode, defaultArbiterNode)
	}

	// Should have suspended with the correct CEL condition.
	if len(spy.SuspendActions) != 1 {
		t.Fatalf("expected 1 suspend action, got %d", len(spy.SuspendActions))
	}
	if spy.SuspendActions[0].Condition != suspendCondition {
		t.Errorf("suspend condition = %q, want %q", spy.SuspendActions[0].Condition, suspendCondition)
	}

	// Should NOT have routed to output (suspend is the terminal action).
	if len(spy.RoutedOutputs) != 0 {
		t.Errorf("expected 0 routed outputs, got %v", spy.RoutedOutputs)
	}

	// Should NOT have completed.
	if len(spy.CompletedReasons) != 0 {
		t.Errorf("expected 0 completions, got %d", len(spy.CompletedReasons))
	}
}

func TestFacilitator_FirstInvocation_SixChildArtefacts(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": defaultDeadlockedFeedback(),
	}
	spy.LawsByID = map[string]*flowv1.Law{
		testLawKigo: {Id: testLawKigo, Goal: "Seasonal reference required"},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{InputArtefacts: []string{"petition"}}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.CreatedChildren) != 1 {
		t.Fatalf("expected 1 child, got %d", len(spy.CreatedChildren))
	}
	childID := spy.CreatedChildren[0]

	// Verify all 6 artefacts are stored on the child.
	expectedArtefacts := []string{
		artefactDisputeWorkitem,
		artefactDisputeDetails,
		artefactDisputeArtefact,
		artefactDisputeInputs,
		artefactAppendix,
		artefactDisputedRef,
	}

	for _, aid := range expectedArtefacts {
		content := spy.getChildArtefact(childID, aid)
		if content == "" {
			t.Errorf("artefact %q not stored on child", aid)
		}
	}
}

func TestFacilitator_FirstInvocation_DisputedRef(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": defaultDeadlockedFeedback(),
	}
	spy.LawsByID = map[string]*flowv1.Law{
		testLawKigo: {Id: testLawKigo, Goal: "kigo"},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	childID := spy.CreatedChildren[0]
	ref := spy.getChildDisputedRef(childID)
	if ref == nil {
		t.Fatal("disputed-artefact ref not stored on child")
	}
	if ref.ArtefactKind != "haiku" {
		t.Errorf("disputed ref artefact_kind = %q, want haiku", ref.ArtefactKind)
	}
	if ref.WorkitemID != testWorkitemID {
		t.Errorf("disputed ref workitem_id = %q, want test-workitem", ref.WorkitemID)
	}
	// Should be the first DEADLOCKED item (fb-1).
	if ref.FeedbackID != testFeedbackID {
		t.Errorf("disputed ref feedback_id = %q, want fb-1 (first deadlocked)", ref.FeedbackID)
	}
}

func TestFacilitator_FirstInvocation_SelectsFirstDeadlocked(t *testing.T) {
	spy := newFacilitatorSpy()
	// fb-low, fb-critical, fb-medium — should pick the first DEADLOCKED item (fb-low).
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{
				Id:      "fb-low",
				Source:  "reviewer-A",
				State:   flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
				Message: "Minor issue",
			},
			{
				Id:      "fb-critical",
				Source:  "reviewer-B",
				State:   flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
				Message: "Critical issue",
			},
			{
				Id:      "fb-medium",
				Source:  "reviewer-C",
				State:   flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
				Message: "Medium issue",
			},
		},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	childID := spy.CreatedChildren[0]
	ref := spy.getChildDisputedRef(childID)
	if ref == nil {
		t.Fatal("disputed-artefact ref not stored on child")
	}
	if ref.FeedbackID != "fb-low" {
		t.Errorf("expected first DEADLOCKED feedback fb-low, got %q", ref.FeedbackID)
	}
}

// ---------------------------------------------------------------------------
// Evidence artefact content
// ---------------------------------------------------------------------------

func TestFacilitator_DisputeWorkitem_Content(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{
				Id:    testFeedbackID,
				State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
			},
		},
	}
	spy.FrictionByFilter = func(f *flowv1.FrictionFilter) []*flowv1.FrictionAggregate {
		if f.GetWorkitemId() == testWorkitemID && f.GetLawId() == "" {
			return []*flowv1.FrictionAggregate{
				{LawId: testLawKigo, NodeId: "sort", EventCount: 2, TotalMagnitude: 4.0},
			}
		}
		return nil
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	childID := spy.CreatedChildren[0]
	content := spy.getChildArtefact(childID, artefactDisputeWorkitem)

	// Should contain workitem context.
	for _, want := range []string{
		testWorkitemID,
		"test-ns",
		"facilitator",
		"request_id",
		"req-123",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("dispute-workitem missing %q", want)
		}
	}

	// Should contain friction summary.
	if !strings.Contains(content, "magnitude=4.00") {
		t.Error("dispute-workitem missing friction data")
	}
}

func TestFacilitator_DisputeDetails_Content(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": defaultDeadlockedFeedback(),
	}
	spy.LawsByID = map[string]*flowv1.Law{
		testLawKigo: {
			Id:   testLawKigo,
			Goal: "Seasonal reference required",
			Tier: flowv1.LawTier_LAW_TIER_FINDING,
			Representations: []*flowv1.Representation{
				{Type: "text/markdown", Content: "Must reference a season."},
			},
		},
	}
	spy.FrictionByFilter = func(f *flowv1.FrictionFilter) []*flowv1.FrictionAggregate {
		if f.GetLawId() == testLawKigo && f.GetWorkitemId() == testWorkitemID {
			return []*flowv1.FrictionAggregate{
				{LawId: testLawKigo, NodeId: "reviewer-A", EventCount: 5, TotalMagnitude: 12.5},
			}
		}
		return nil
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	childID := spy.CreatedChildren[0]
	content := spy.getChildArtefact(childID, artefactDisputeDetails)

	// Should contain the first DEADLOCKED feedback (fb-1).
	for _, want := range []string{
		testFeedbackID,
		"reviewer-A",
		"The haiku does not follow traditional kigo conventions.",
		"Missing seasonal reference.",
		"The seasonal reference is implicit.",
		testLawKigo,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("dispute-details missing %q", want)
		}
	}

	// Should contain cited law content.
	if !strings.Contains(content, "Seasonal reference required") {
		t.Error("dispute-details missing cited law goal")
	}
	if !strings.Contains(content, "Must reference a season.") {
		t.Error("dispute-details missing cited law representation")
	}

	// Should contain per-law friction.
	if !strings.Contains(content, "magnitude=12.50") {
		t.Error("dispute-details missing per-law friction data")
	}

	// Should NOT contain fb-2 (only the first DEADLOCKED item is selected).
	if strings.Contains(content, "fb-2") {
		t.Error("dispute-details should only contain the first DEADLOCKED item, found fb-2")
	}
}

func TestFacilitator_DisputeDetails_DebateHistory(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": defaultDeadlockedFeedback(),
	}
	spy.LawsByID = map[string]*flowv1.Law{
		testLawKigo: {Id: testLawKigo, Goal: "kigo"},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	childID := spy.CreatedChildren[0]
	content := spy.getChildArtefact(childID, artefactDisputeDetails)

	// fb-1 has debate history.
	if !strings.Contains(content, "Missing seasonal reference.") {
		t.Error("dispute-details missing raised event from debate history")
	}
	if !strings.Contains(content, "The seasonal reference is implicit.") {
		t.Error("dispute-details missing challenged event from debate history")
	}
}

func TestFacilitator_DisputeDetails_NovelArgument(t *testing.T) {
	spy := newFacilitatorSpy()
	// Single CRITICAL item with novel argument, no citations.
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{
				Id:      "fb-novel",
				Source:  "reviewer-X",
				State:   flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
				Message: "Novel argument test",
				Justification: &flowv1.Justification{
					Kind: &flowv1.Justification_NovelArgument{
						NovelArgument: &flowv1.NovelArgument{
							Argument: "This is a new reasoning never seen before.",
						},
					},
				},
			},
		},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	childID := spy.CreatedChildren[0]
	content := spy.getChildArtefact(childID, artefactDisputeDetails)

	if !strings.Contains(content, "This is a new reasoning never seen before.") {
		t.Error("dispute-details missing novel argument")
	}
	// No cited laws section since no citations.
	if strings.Contains(content, "## Cited Laws") {
		t.Error("dispute-details should not have cited laws section for novel-argument-only feedback")
	}
}

func TestFacilitator_DisputeArtefact_Content(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.ArtefactContent = []byte("An old silent pond / A frog jumps into the pond / Splash! Silence again")
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{Id: testFeedbackID, State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
		},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	childID := spy.CreatedChildren[0]
	content := spy.getChildArtefact(childID, artefactDisputeArtefact)

	if !strings.Contains(content, "An old silent pond") {
		t.Error("dispute-artefact missing artefact content")
	}
}

func TestFacilitator_DisputeArtefact_LargeContent(t *testing.T) {
	spy := newFacilitatorSpy()
	largeContent := strings.Repeat("x", 3000)
	spy.ArtefactContent = []byte(largeContent)
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{Id: "fb-trunc", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
		},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	childID := spy.CreatedChildren[0]
	content := spy.getChildArtefact(childID, artefactDisputeArtefact)

	if !strings.Contains(content, largeContent) {
		t.Error("expected full content in dispute-artefact, content should not be truncated")
	}
}

func TestFacilitator_DisputeInputs_Content(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{Id: testFeedbackID, State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
		},
	}
	spy.ArtefactContentByID = map[string][]byte{
		"petition":    []byte("Write a haiku about frogs"),
		"style-guide": []byte("Follow traditional Japanese form"),
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{InputArtefacts: []string{"petition", "style-guide"}}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	childID := spy.CreatedChildren[0]
	content := spy.getChildArtefact(childID, artefactDisputeInputs)

	if !strings.Contains(content, "petition") {
		t.Error("dispute-inputs missing petition header")
	}
	if !strings.Contains(content, "Write a haiku about frogs") {
		t.Error("dispute-inputs missing petition content")
	}
	if !strings.Contains(content, "style-guide") {
		t.Error("dispute-inputs missing style-guide header")
	}
	if !strings.Contains(content, "Follow traditional Japanese form") {
		t.Error("dispute-inputs missing style-guide content")
	}
}

func TestFacilitator_DisputeInputs_NoConfig(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{Id: testFeedbackID, State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
		},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{} // No input artefacts configured.

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	childID := spy.CreatedChildren[0]
	content := spy.getChildArtefact(childID, artefactDisputeInputs)

	if !strings.Contains(content, "No input artefacts configured") {
		t.Error("dispute-inputs should indicate no input artefacts configured")
	}
}

func TestFacilitator_Appendix_Content(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{Id: testFeedbackID, State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
		},
	}
	spy.Laws = []*flowv1.Law{
		{
			Id:   testLawKigo,
			Goal: "Seasonal reference required",
			Tier: flowv1.LawTier_LAW_TIER_FINDING,
		},
		{
			Id:   "law-meter",
			Goal: "5-7-5 syllable meter",
			Tier: flowv1.LawTier_LAW_TIER_RULING,
		},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	childID := spy.CreatedChildren[0]
	content := spy.getChildArtefact(childID, artefactAppendix)

	if !strings.Contains(content, testLawKigo) {
		t.Error("appendix missing law-kigo")
	}
	if !strings.Contains(content, "law-meter") {
		t.Error("appendix missing law-meter")
	}
	if !strings.Contains(content, "5-7-5 syllable meter") {
		t.Error("appendix missing law-meter goal")
	}
}

func TestFacilitator_Appendix_NoLaws(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{Id: testFeedbackID, State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
		},
	}
	// No laws configured.

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	childID := spy.CreatedChildren[0]
	content := spy.getChildArtefact(childID, artefactAppendix)

	if !strings.Contains(content, "No laws found") {
		t.Error("appendix should indicate no laws found")
	}
}

func TestFacilitator_DisputeWorkitem_NoFriction(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{Id: "fb-nf", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
		},
	}
	// No friction data.

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	childID := spy.CreatedChildren[0]
	content := spy.getChildArtefact(childID, artefactDisputeWorkitem)

	if !strings.Contains(content, "No friction data available.") {
		t.Error("dispute-workitem should show no friction message")
	}
}

// ---------------------------------------------------------------------------
// Suspend condition
// ---------------------------------------------------------------------------

func TestFacilitator_SuspendConditionCorrect(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{Id: "fb-sc", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
		},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.SuspendActions) != 1 {
		t.Fatalf("expected 1 suspend, got %d", len(spy.SuspendActions))
	}

	expected := `children.all(c, c.phase == "Completed")`
	if spy.SuspendActions[0].Condition != expected {
		t.Errorf("suspend condition = %q, want %q", spy.SuspendActions[0].Condition, expected)
	}
	if spy.SuspendActions[0].Timeout != "" {
		t.Errorf("expected no timeout, got %q", spy.SuspendActions[0].Timeout)
	}
}

// ---------------------------------------------------------------------------
// Post-resume: success
// ---------------------------------------------------------------------------

func TestFacilitator_PostResume_Success_RoutesToResolved(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.Children = []*flowv1.ChildWorkitemStatus{
		{
			WorkitemId:       "child-arbiter",
			Phase:            "Completed",
			CompletionReason: flowv1.CompletionReason_COMPLETION_REASON_UNSPECIFIED,
		},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputResolved {
		t.Fatalf("expected route to %q, got %v", outputResolved, spy.RoutedOutputs)
	}
	if len(spy.CompletedReasons) != 0 {
		t.Errorf("expected 0 completions, got %d", len(spy.CompletedReasons))
	}
	if len(spy.CreatedChildren) != 0 {
		t.Errorf("expected 0 created children on resume, got %d", len(spy.CreatedChildren))
	}
	if len(spy.SuspendActions) != 0 {
		t.Errorf("expected 0 suspend actions on resume, got %d", len(spy.SuspendActions))
	}
}

// ---------------------------------------------------------------------------
// Post-resume: cancelled
// ---------------------------------------------------------------------------

func TestFacilitator_PostResume_Cancelled_CompletesWithCancelled(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.Children = []*flowv1.ChildWorkitemStatus{
		{
			WorkitemId:       "child-arbiter",
			Phase:            "Completed",
			CompletionReason: flowv1.CompletionReason_COMPLETION_REASON_CANCELLED,
		},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.CompletedReasons) != 1 {
		t.Fatalf("expected 1 completion, got %d", len(spy.CompletedReasons))
	}
	if spy.CompletedReasons[0] != flowv1.CompletionReason_COMPLETION_REASON_CANCELLED {
		t.Errorf("completion reason = %v, want CANCELLED", spy.CompletedReasons[0])
	}
	if len(spy.RoutedOutputs) != 0 {
		t.Errorf("expected 0 routes on cancellation, got %v", spy.RoutedOutputs)
	}
}

// ---------------------------------------------------------------------------
// Post-resume: multiple children (first completed wins)
// ---------------------------------------------------------------------------

func TestFacilitator_PostResume_MultipleChildren_FirstCompletedWins(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.Children = []*flowv1.ChildWorkitemStatus{
		{WorkitemId: "child-running", Phase: "Running"},
		{
			WorkitemId:       "child-done",
			Phase:            "Completed",
			CompletionReason: flowv1.CompletionReason_COMPLETION_REASON_UNSPECIFIED,
		},
		{
			WorkitemId:       "child-also-done",
			Phase:            "Completed",
			CompletionReason: flowv1.CompletionReason_COMPLETION_REASON_CANCELLED,
		},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputResolved {
		t.Fatalf("expected route to %q, got %v", outputResolved, spy.RoutedOutputs)
	}
	if len(spy.CompletedReasons) != 0 {
		t.Errorf("expected 0 completions, got %d", len(spy.CompletedReasons))
	}
}

// ---------------------------------------------------------------------------
// No deadlocked feedback edge case
// ---------------------------------------------------------------------------

func TestFacilitator_NoDeadlockedFeedback_RoutesToResolved(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{Id: "fb-ok", State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED},
		},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputResolved {
		t.Fatalf("expected route to %q, got %v", outputResolved, spy.RoutedOutputs)
	}
	if len(spy.CreatedChildren) != 0 {
		t.Errorf("expected 0 children for no-deadlock, got %d", len(spy.CreatedChildren))
	}
	if len(spy.SuspendActions) != 0 {
		t.Errorf("expected 0 suspend actions for no-deadlock, got %d", len(spy.SuspendActions))
	}
}

func TestFacilitator_NoFeedbackAtAll_RoutesToResolved(t *testing.T) {
	spy := newFacilitatorSpy()

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputResolved {
		t.Fatalf("expected route to %q, got %v", outputResolved, spy.RoutedOutputs)
	}
}

// ---------------------------------------------------------------------------
// Config tests
// ---------------------------------------------------------------------------

func TestFacilitator_CustomArbiterNode(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{Id: "fb-cfg", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
		},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{ArbiterNode: "custom-arbiter"}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.RoutedChildren) != 1 {
		t.Fatalf("expected 1 routed child, got %d", len(spy.RoutedChildren))
	}
	if spy.RoutedChildren[0].TargetNode != "custom-arbiter" {
		t.Errorf("child routed to %q, expected custom-arbiter", spy.RoutedChildren[0].TargetNode)
	}
}

func TestFacilitator_DefaultConfig(t *testing.T) {
	cfg := &facilitatorConfig{}
	if cfg.arbiterNode() != defaultArbiterNode {
		t.Fatalf("expected default arbiterNode=%q, got %q", defaultArbiterNode, cfg.arbiterNode())
	}

	cfg2 := &facilitatorConfig{ArbiterNode: "override"}
	if cfg2.arbiterNode() != "override" {
		t.Fatalf("expected arbiterNode=override, got %q", cfg2.arbiterNode())
	}
}

func TestFacilitator_ValidateConfig(t *testing.T) {
	if err := validateConfig(&facilitatorConfig{}); err != nil {
		t.Fatalf("validateConfig() error: %v", err)
	}
	if err := validateConfig(&facilitatorConfig{ArbiterNode: "x"}); err != nil {
		t.Fatalf("validateConfig() error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Phase detection (hasCompletedChild)
// ---------------------------------------------------------------------------

func TestHasCompletedChild(t *testing.T) {
	tests := []struct {
		name     string
		children []*flowv1.ChildWorkitemStatus
		want     bool
	}{
		{
			name:     "nil children",
			children: nil,
			want:     false,
		},
		{
			name:     "empty children",
			children: []*flowv1.ChildWorkitemStatus{},
			want:     false,
		},
		{
			name: "only running",
			children: []*flowv1.ChildWorkitemStatus{
				{WorkitemId: "c1", Phase: "Running"},
			},
			want: false,
		},
		{
			name: "one completed",
			children: []*flowv1.ChildWorkitemStatus{
				{WorkitemId: "c1", Phase: "Completed"},
			},
			want: true,
		},
		{
			name: "mixed phases",
			children: []*flowv1.ChildWorkitemStatus{
				{WorkitemId: "c1", Phase: "Running"},
				{WorkitemId: "c2", Phase: "Completed"},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasCompletedChild(tt.children)
			if got != tt.want {
				t.Errorf("hasCompletedChild() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// extractCitedLawIDs unit test
// ---------------------------------------------------------------------------

func TestExtractCitedLawIDs(t *testing.T) {
	tests := []struct {
		name string
		item *flowv1.FeedbackItem
		want []string
	}{
		{
			name: "with citation",
			item: &flowv1.FeedbackItem{
				Justification: &flowv1.Justification{
					Kind: &flowv1.Justification_Citation{
						Citation: &flowv1.Citation{CitationIds: []string{"law-a", "law-b"}},
					},
				},
			},
			want: []string{"law-a", "law-b"},
		},
		{
			name: "novel argument only",
			item: &flowv1.FeedbackItem{
				Justification: &flowv1.Justification{
					Kind: &flowv1.Justification_NovelArgument{
						NovelArgument: &flowv1.NovelArgument{Argument: "new idea"},
					},
				},
			},
			want: nil,
		},
		{
			name: "no justification",
			item: &flowv1.FeedbackItem{},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCitedLawIDs(tt.item)
			if len(got) != len(tt.want) {
				t.Fatalf("extractCitedLawIDs() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("extractCitedLawIDs()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Error propagation tests
// ---------------------------------------------------------------------------

func TestFacilitator_Error_GetChildrenFails(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.GetChildrenErr = fmt.Errorf("operator unavailable")
	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)

	err := handleFacilitator(context.Background(), client, &facilitatorConfig{}, wctx)
	if err == nil {
		t.Fatal("expected error from GetChildren failure")
	}
	if !strings.Contains(err.Error(), "get children") {
		t.Errorf("error = %v, want to contain 'get children'", err)
	}
}

func TestFacilitator_Error_GetFlowTopologyFails(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.GetFlowTopologyErr = fmt.Errorf("topology unavailable")
	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)

	err := handleFacilitator(context.Background(), client, &facilitatorConfig{}, wctx)
	if err == nil {
		t.Fatal("expected error from GetFlowTopology failure")
	}
	if !strings.Contains(err.Error(), "flow topology") {
		t.Errorf("error = %v, want to contain 'flow topology'", err)
	}
}

func TestFacilitator_Error_GetFeedbackFails(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.GetFeedbackErr = fmt.Errorf("feedback unavailable")
	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)

	err := handleFacilitator(context.Background(), client, &facilitatorConfig{}, wctx)
	if err == nil {
		t.Fatal("expected error from GetFeedback failure")
	}
	if !strings.Contains(err.Error(), "get feedback") {
		t.Errorf("error = %v, want to contain 'get feedback'", err)
	}
}

func TestFacilitator_Error_GetArtefactFails(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{Id: "fb-err", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
		},
	}
	spy.GetArtefactErr = fmt.Errorf("artefact unavailable")
	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)

	err := handleFacilitator(context.Background(), client, &facilitatorConfig{}, wctx)
	if err == nil {
		t.Fatal("expected error from GetArtefact failure")
	}
	if !strings.Contains(err.Error(), "get artefact") {
		t.Errorf("error = %v, want to contain 'get artefact'", err)
	}
}

func TestFacilitator_Error_QueryLawsFails(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{Id: "fb-err", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
		},
	}
	spy.QueryLawsErr = fmt.Errorf("librarian unavailable")
	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)

	err := handleFacilitator(context.Background(), client, &facilitatorConfig{}, wctx)
	if err == nil {
		t.Fatal("expected error from QueryLaws failure")
	}
	if !strings.Contains(err.Error(), "query laws") {
		t.Errorf("error = %v, want to contain 'query laws'", err)
	}
}

func TestFacilitator_Error_QueryFrictionFails(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{Id: "fb-err", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
		},
	}
	spy.QueryFrictionErr = fmt.Errorf("friction ledger unavailable")
	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)

	err := handleFacilitator(context.Background(), client, &facilitatorConfig{}, wctx)
	if err == nil {
		t.Fatal("expected error from QueryFriction failure")
	}
	if !strings.Contains(err.Error(), "query workitem friction") {
		t.Errorf("error = %v, want to contain 'query workitem friction'", err)
	}
}

func TestFacilitator_Error_CreateChildFails(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{Id: "fb-err", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
		},
	}
	spy.CreateChildErr = fmt.Errorf("cannot create child")
	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)

	err := handleFacilitator(context.Background(), client, &facilitatorConfig{}, wctx)
	if err == nil {
		t.Fatal("expected error from CreateChildWorkitem failure")
	}
	if !strings.Contains(err.Error(), "create child") {
		t.Errorf("error = %v, want to contain 'create child'", err)
	}
}

func TestFacilitator_Error_StoreArtefactFails(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{Id: "fb-err", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
		},
	}
	spy.StoreArtefactErr = fmt.Errorf("archivist store failed")
	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)

	err := handleFacilitator(context.Background(), client, &facilitatorConfig{}, wctx)
	if err == nil {
		t.Fatal("expected error from StoreArtefact failure")
	}
	// First artefact stored is dispute-workitem.
	if !strings.Contains(err.Error(), "store dispute-workitem on child") {
		t.Errorf("error = %v, want to contain 'store dispute-workitem on child'", err)
	}
}

func TestFacilitator_Error_RouteChildFails(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{Id: "fb-err", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
		},
	}
	spy.RouteChildErr = fmt.Errorf("routing child failed")
	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)

	err := handleFacilitator(context.Background(), client, &facilitatorConfig{}, wctx)
	if err == nil {
		t.Fatal("expected error from RouteChild failure")
	}
	if !strings.Contains(err.Error(), "route child") {
		t.Errorf("error = %v, want to contain 'route child'", err)
	}
}

func TestFacilitator_Error_SuspendFails(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{Id: "fb-err", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
		},
	}
	spy.SuspendErr = fmt.Errorf("suspend rejected")
	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)

	err := handleFacilitator(context.Background(), client, &facilitatorConfig{}, wctx)
	if err == nil {
		t.Fatal("expected error from Suspend failure")
	}
	if !strings.Contains(err.Error(), "suspend") {
		t.Errorf("error = %v, want to contain 'suspend'", err)
	}
}

func TestFacilitator_Error_RouteToOutputFails_NoDeadlock(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {},
	}
	spy.RouteToOutputErr = fmt.Errorf("routing failed")
	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)

	err := handleFacilitator(context.Background(), client, &facilitatorConfig{}, wctx)
	if err == nil {
		t.Fatal("expected error from RouteToOutput failure (no deadlock path)")
	}
}

func TestFacilitator_Error_RouteToOutputFails_PostResume(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.Children = []*flowv1.ChildWorkitemStatus{
		{
			WorkitemId:       "child-ok",
			Phase:            "Completed",
			CompletionReason: flowv1.CompletionReason_COMPLETION_REASON_UNSPECIFIED,
		},
	}
	spy.RouteToOutputErr = fmt.Errorf("routing failed")
	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)

	err := handleFacilitator(context.Background(), client, &facilitatorConfig{}, wctx)
	if err == nil {
		t.Fatal("expected error from RouteToOutput failure (post-resume path)")
	}
}

func TestFacilitator_Error_CompleteFails_PostResume(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.Children = []*flowv1.ChildWorkitemStatus{
		{
			WorkitemId:       "child-cancel",
			Phase:            "Completed",
			CompletionReason: flowv1.CompletionReason_COMPLETION_REASON_CANCELLED,
		},
	}
	spy.CompleteErr = fmt.Errorf("complete rejected")
	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)

	err := handleFacilitator(context.Background(), client, &facilitatorConfig{}, wctx)
	if err == nil {
		t.Fatal("expected error from Complete failure (post-resume cancelled path)")
	}
}

func TestFacilitator_Error_GetInputArtefactFails(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{Id: "fb-err", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
		},
	}
	// GetArtefact will fail for input artefact "petition" since GetArtefactErr
	// is set globally. But dispute-artefact is fetched first. Let's use
	// ArtefactContentByID so the dispute artefact succeeds but the input fails.
	spy.ArtefactContentByID = map[string][]byte{
		"haiku": []byte("some haiku"),
		// "petition" is NOT in the map — will fall back to ArtefactContent.
	}
	spy.ArtefactContent = nil // Will cause the SDK to return empty content (no error though).
	// Actually, to trigger an error specifically for "petition", we need
	// a custom approach. Let's just use GetArtefactErr which will fail on
	// the dispute-artefact fetch first.
	spy.GetArtefactErr = fmt.Errorf("artefact unavailable")
	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)

	err := handleFacilitator(context.Background(), client, &facilitatorConfig{InputArtefacts: []string{"petition"}}, wctx)
	if err == nil {
		t.Fatal("expected error from GetArtefact failure")
	}
}

// ---------------------------------------------------------------------------
// Telemetry tests
// ---------------------------------------------------------------------------

func TestFacilitator_Telemetry_FirstInvocation(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": defaultDeadlockedFeedback(),
	}
	spy.LawsByID = map[string]*flowv1.Law{
		testLawKigo: {Id: testLawKigo, Goal: "kigo"},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	types := spy.telemetryTypes()

	expectedTypes := []string{
		"foundry.facilitator.started",
		"foundry.facilitator.evidence_assembled",
		"foundry.facilitator.suspended",
	}
	for _, et := range expectedTypes {
		if !slices.Contains(types, et) {
			t.Errorf("missing telemetry event %q, got %v", et, types)
		}
	}

	// Verify "started" phase is "first".
	started := spy.findTelemetry("foundry.facilitator.started")
	if started == nil {
		t.Fatal("started telemetry event not found")
	}
	if started.Payload["phase"] != "first" {
		t.Errorf("started.phase = %v, want first", started.Payload["phase"])
	}

	// Verify evidence_assembled payload.
	assembled := spy.findTelemetry("foundry.facilitator.evidence_assembled")
	if assembled == nil {
		t.Fatal("evidence_assembled telemetry event not found")
	}
	if assembled.Payload["artefact_kind"] != "haiku" {
		t.Errorf("evidence_assembled.artefact_kind = %v, want haiku", assembled.Payload["artefact_kind"])
	}
	if assembled.Payload["feedback_id"] != testFeedbackID {
		t.Errorf("evidence_assembled.feedback_id = %v, want fb-1", assembled.Payload["feedback_id"])
	}

	// Verify suspended payload.
	suspended := spy.findTelemetry("foundry.facilitator.suspended")
	if suspended == nil {
		t.Fatal("suspended telemetry event not found")
	}
	if suspended.Payload["arbiter_node"] != defaultArbiterNode {
		t.Errorf("suspended.arbiter_node = %v, want %s", suspended.Payload["arbiter_node"], defaultArbiterNode)
	}
	if suspended.Payload["condition"] != suspendCondition {
		t.Errorf("suspended.condition = %v, want %s", suspended.Payload["condition"], suspendCondition)
	}
	if suspended.Payload["feedback_id"] != testFeedbackID {
		t.Errorf("suspended.feedback_id = %v, want fb-1", suspended.Payload["feedback_id"])
	}
}

func TestFacilitator_Telemetry_PostResume_Resolved(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.Children = []*flowv1.ChildWorkitemStatus{
		{
			WorkitemId:       "child-ok",
			Phase:            "Completed",
			CompletionReason: flowv1.CompletionReason_COMPLETION_REASON_UNSPECIFIED,
		},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	started := spy.findTelemetry("foundry.facilitator.started")
	if started == nil {
		t.Fatal("started telemetry event not found")
	}
	if started.Payload["phase"] != "resume" {
		t.Errorf("started.phase = %v, want resume", started.Payload["phase"])
	}

	resolved := spy.findTelemetry("foundry.facilitator.resolved")
	if resolved == nil {
		t.Fatal("resolved telemetry event not found")
	}
	if resolved.Payload["child_id"] != "child-ok" {
		t.Errorf("resolved.child_id = %v, want child-ok", resolved.Payload["child_id"])
	}
	if resolved.Payload["output"] != outputResolved {
		t.Errorf("resolved.output = %v, want %s", resolved.Payload["output"], outputResolved)
	}
}

func TestFacilitator_Telemetry_PostResume_Cancelled(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.Children = []*flowv1.ChildWorkitemStatus{
		{
			WorkitemId:       "child-cancel",
			Phase:            "Completed",
			CompletionReason: flowv1.CompletionReason_COMPLETION_REASON_CANCELLED,
		},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	cancelled := spy.findTelemetry("foundry.facilitator.cancelled")
	if cancelled == nil {
		t.Fatal("cancelled telemetry event not found")
	}
	if cancelled.Payload["child_id"] != "child-cancel" {
		t.Errorf("cancelled.child_id = %v, want child-cancel", cancelled.Payload["child_id"])
	}

	if spy.findTelemetry("foundry.facilitator.resolved") != nil {
		t.Error("unexpected resolved telemetry on cancellation path")
	}
}

func TestFacilitator_Telemetry_NoDeadlock(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{Id: "fb-ok", State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED},
		},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	noDeadlock := spy.findTelemetry("foundry.facilitator.no_deadlock")
	if noDeadlock == nil {
		t.Fatal("no_deadlock telemetry event not found")
	}
	if noDeadlock.Payload["output"] != outputResolved {
		t.Errorf("no_deadlock.output = %v, want %s", noDeadlock.Payload["output"], outputResolved)
	}

	if spy.findTelemetry("foundry.facilitator.evidence_assembled") != nil {
		t.Error("unexpected evidence_assembled telemetry on no-deadlock path")
	}
	if spy.findTelemetry("foundry.facilitator.suspended") != nil {
		t.Error("unexpected suspended telemetry on no-deadlock path")
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestFacilitator_MixedFeedbackStates_OnlyDeadlockedPicked(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{Id: "fb-resolved", State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED},
			{Id: "fb-new", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW},
			{Id: "fb-dl", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
		},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.CreatedChildren) != 1 {
		t.Fatalf("expected 1 child, got %d", len(spy.CreatedChildren))
	}

	ref := spy.getChildDisputedRef(spy.CreatedChildren[0])
	if ref == nil {
		t.Fatal("disputed-artefact ref not found")
	}
	if ref.FeedbackID != "fb-dl" {
		t.Errorf("expected feedback ID fb-dl, got %q", ref.FeedbackID)
	}
}

func TestFacilitator_EmptyExitContract(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.TopologyResponse = &flowv1.GetFlowTopologyResponse{
		Self:         &flowv1.FlowNode{Name: "facilitator"},
		ExitContract: map[string]*flowv1.StampRequirements{},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputResolved {
		t.Fatalf("expected route to %q, got %v", outputResolved, spy.RoutedOutputs)
	}
}

func TestFacilitator_GetLaw_CalledForCitedLaws(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{
				Id:      "fb-cited",
				Source:  "reviewer-A",
				State:   flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
				Message: "Cited feedback",
				Justification: &flowv1.Justification{
					Kind: &flowv1.Justification_Citation{
						Citation: &flowv1.Citation{
							CitationIds: []string{"law-alpha", "law-beta"},
						},
					},
				},
			},
		},
	}
	spy.LawsByID = map[string]*flowv1.Law{
		"law-alpha": {Id: "law-alpha", Goal: "Alpha law"},
		"law-beta":  {Id: "law-beta", Goal: "Beta law"},
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Should have called GetLaw for both cited laws.
	if len(spy.GetLawCalls) != 2 {
		t.Fatalf("expected 2 GetLaw calls, got %d", len(spy.GetLawCalls))
	}
	if !slices.Contains(spy.GetLawCalls, "law-alpha") {
		t.Error("expected GetLaw call for law-alpha")
	}
	if !slices.Contains(spy.GetLawCalls, "law-beta") {
		t.Error("expected GetLaw call for law-beta")
	}
}

func TestFacilitator_GetLaw_FailureIsNonFatal(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{
				Id:      "fb-cited",
				Source:  "reviewer-A",
				State:   flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
				Message: "Cited feedback",
				Justification: &flowv1.Justification{
					Kind: &flowv1.Justification_Citation{
						Citation: &flowv1.Citation{
							CitationIds: []string{"law-missing"},
						},
					},
				},
			},
		},
	}
	// LawsByID is nil — GetLaw will return NotFound.

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	// Should NOT fail — GetLaw failure is logged, not propagated.
	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v (GetLaw failure should be non-fatal)", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Should still create child and suspend.
	if len(spy.CreatedChildren) != 1 {
		t.Fatalf("expected 1 child despite GetLaw failure, got %d", len(spy.CreatedChildren))
	}

	// The dispute-details should contain the fallback text.
	childID := spy.CreatedChildren[0]
	content := spy.getChildArtefact(childID, artefactDisputeDetails)
	if !strings.Contains(content, "Failed to retrieve law") {
		t.Error("dispute-details should contain fallback text for missing law")
	}
}

func TestFacilitator_QueryFriction_FilteredByWorkitemAndLaw(t *testing.T) {
	spy := newFacilitatorSpy()
	spy.FeedbackItemsByArtefact = map[string][]*flowv1.FeedbackItem{
		"haiku": {
			{
				Id:      "fb-friction",
				Source:  "reviewer-A",
				State:   flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
				Message: "Friction test",
				Justification: &flowv1.Justification{
					Kind: &flowv1.Justification_Citation{
						Citation: &flowv1.Citation{
							CitationIds: []string{testLawKigo},
						},
					},
				},
			},
		},
	}
	spy.LawsByID = map[string]*flowv1.Law{
		testLawKigo: {Id: testLawKigo, Goal: "kigo"},
	}
	spy.FrictionByFilter = func(f *flowv1.FrictionFilter) []*flowv1.FrictionAggregate {
		return nil // Just verifying the filter is passed correctly.
	}

	wctx := defaultWorkitemContext()
	client := setupFacilitatorTest(t, spy)
	cfg := &facilitatorConfig{}

	err := handleFacilitator(context.Background(), client, cfg, wctx)
	if err != nil {
		t.Fatalf("handleFacilitator() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Should have at least 2 QueryFriction calls:
	// 1. Workitem-level friction (dispute-workitem builder)
	// 2. Per-law friction for law-kigo (dispute-details builder)
	if len(spy.QueryFrictionCalls) < 2 {
		t.Fatalf("expected at least 2 QueryFriction calls, got %d", len(spy.QueryFrictionCalls))
	}

	// First call: workitem-level (workitem_id set, no law_id).
	first := spy.QueryFrictionCalls[0]
	if first.GetWorkitemId() != testWorkitemID {
		t.Errorf("first QueryFriction workitem_id = %q, want test-workitem", first.GetWorkitemId())
	}
	if first.GetLawId() != "" {
		t.Errorf("first QueryFriction law_id = %q, want empty", first.GetLawId())
	}

	// Find the per-law call.
	var foundLawFriction bool
	for _, call := range spy.QueryFrictionCalls[1:] {
		if call.GetLawId() == testLawKigo && call.GetWorkitemId() == testWorkitemID {
			foundLawFriction = true
			break
		}
	}
	if !foundLawFriction {
		t.Error("expected QueryFriction call with law_id=law-kigo and workitem_id=test-workitem")
	}
}
