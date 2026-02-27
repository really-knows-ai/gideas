package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Happy path tests
// ---------------------------------------------------------------------------

func TestArbiter_FanOut_CorrectJurorCount(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:      "fb-1",
			Source:  "appraise",
			State:   flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
			Message: "Haiku lacks seasonal reference",
		},
	}

	client := setupArbiterTest(t, spy)
	cfg := &arbiterConfig{JurySize: 3}

	if err := handleArbiter(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleArbiter() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Should have created 3 child Workitems (one per juror).
	if len(spy.CreatedChildren) != 3 {
		t.Fatalf("expected 3 children, got %d", len(spy.CreatedChildren))
	}

	// Each child should be routed to the juror node.
	if len(spy.RoutedChildren) != 3 {
		t.Fatalf("expected 3 routed children, got %d", len(spy.RoutedChildren))
	}
	for i, rc := range spy.RoutedChildren {
		if rc.TargetNode != defaultJurorNode {
			t.Errorf("child %d routed to %q, expected %q", i, rc.TargetNode, defaultJurorNode)
		}
	}
}

func TestArbiter_FanOut_ChildArtefactsCorrect(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:      "fb-1",
			Source:  "appraise",
			State:   flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
			Message: "Missing kigo",
		},
	}

	client := setupArbiterTest(t, spy)
	cfg := &arbiterConfig{JurySize: 1}

	if err := handleArbiter(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleArbiter() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.CreatedChildren) != 1 {
		t.Fatalf("expected 1 child, got %d", len(spy.CreatedChildren))
	}

	childID := spy.CreatedChildren[0]

	// Verify question artefact.
	questionKey := childID + ":" + artefactQuestion
	q, ok := spy.ChildStoredArtefacts[questionKey]
	if !ok {
		t.Fatal("question artefact not stored on child")
	}
	if !strings.Contains(string(q), "reviewer's feedback be upheld") {
		t.Errorf("unexpected question: %s", string(q))
	}

	// Verify evidence artefact.
	evidenceKey := childID + ":" + artefactEvidence
	ev, ok := spy.ChildStoredArtefacts[evidenceKey]
	if !ok {
		t.Fatal("evidence artefact not stored on child")
	}
	if len(ev) == 0 {
		t.Fatal("evidence artefact is empty")
	}

	// Verify allowed-outcomes artefact.
	outcomesKey := childID + ":" + artefactOutcomes
	out, ok := spy.ChildStoredArtefacts[outcomesKey]
	if !ok {
		t.Fatal("allowed-outcomes artefact not stored on child")
	}
	var outcomes []string
	if err := json.Unmarshal(out, &outcomes); err != nil {
		t.Fatalf("failed to parse allowed-outcomes: %v", err)
	}
	if len(outcomes) != 2 || outcomes[0] != "favour_refiner" || outcomes[1] != "favour_reviewer" {
		t.Fatalf("expected [favour_refiner, favour_reviewer], got %v", outcomes)
	}
}

func TestArbiter_VerdictContextStored(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:      "fb-1",
			Source:  "appraise",
			State:   flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
			Message: "Missing kigo",
		},
		{
			Id:      "fb-2",
			Source:  "appraise",
			State:   flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
			Message: "Too many syllables",
		},
	}

	client := setupArbiterTest(t, spy)
	cfg := &arbiterConfig{JurySize: 1}

	if err := handleArbiter(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleArbiter() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	vctx := spy.getStoredVerdictContext()
	if vctx == nil {
		t.Fatal("verdict-context artefact not stored")
	}

	if vctx.Trigger != "deadlock-resolution" {
		t.Errorf("expected trigger=deadlock-resolution, got %s", vctx.Trigger)
	}
	if vctx.Action != "create" {
		t.Errorf("expected action=create, got %s", vctx.Action)
	}
	if vctx.Tier != int32(flowv1.LawTier_LAW_TIER_RULING) {
		t.Errorf("expected tier=%d (RULING), got %d", int32(flowv1.LawTier_LAW_TIER_RULING), vctx.Tier)
	}
	if len(vctx.AppliesTo) != 1 || vctx.AppliesTo[0] != "haiku" {
		t.Errorf("expected applies_to=[haiku], got %v", vctx.AppliesTo)
	}
	if len(vctx.FeedbackIDs) != 2 {
		t.Fatalf("expected 2 feedback IDs, got %d", len(vctx.FeedbackIDs))
	}
	ids := map[string]bool{}
	for _, id := range vctx.FeedbackIDs {
		ids[id] = true
	}
	if !ids["fb-1"] || !ids["fb-2"] {
		t.Errorf("expected feedback IDs fb-1 and fb-2, got %v", vctx.FeedbackIDs)
	}
}

func TestArbiter_RoutesToDeliberationGate(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:     "fb-1",
			Source: "appraise",
			State:  flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
		},
	}

	client := setupArbiterTest(t, spy)
	cfg := &arbiterConfig{JurySize: 1}

	if err := handleArbiter(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleArbiter() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputGate {
		t.Fatalf("expected route to %q, got %v", outputGate, spy.RoutedOutputs)
	}
}

func TestArbiter_PauseResumeTimer(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:     "fb-1",
			Source: "appraise",
			State:  flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
		},
	}

	client := setupArbiterTest(t, spy)
	cfg := &arbiterConfig{JurySize: 1}

	if err := handleArbiter(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleArbiter() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// AwaitChildren pauses timer while waiting and resumes on return.
	if !spy.PauseTimerCalled {
		t.Error("expected PauseTimer to be called during AwaitChildren")
	}
	if !spy.ResumeTimerCalled {
		t.Error("expected ResumeTimer to be called after AwaitChildren")
	}
}

// ---------------------------------------------------------------------------
// Evidence assembly tests
// ---------------------------------------------------------------------------

func TestArbiter_EvidenceBundleContainsAllSections(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:      "fb-ev",
			State:   flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
			Source:  "appraise",
			Message: "Missing kigo",
			History: []*flowv1.FeedbackEvent{
				{Actor: "appraise", Action: "add_feedback", Message: "Missing kigo"},
				{Actor: "refine", Action: "refuse", Message: "Kigo is implicit"},
			},
		},
	}
	spy.ArtefactContent = []byte("An old silent pond / A frog jumps into the pond / Splash! Silence again")
	spy.Laws = []*flowv1.Law{
		{
			Id:   "law-1",
			Goal: "Haiku must contain a seasonal reference",
			Tier: flowv1.LawTier_LAW_TIER_FINDING,
		},
	}
	spy.FrictionAggregates = []*flowv1.FrictionAggregate{
		{LawId: "law-1", NodeId: "appraise", EventCount: 5, TotalMagnitude: 12.5},
	}

	client := setupArbiterTest(t, spy)
	cfg := &arbiterConfig{JurySize: 1}

	if err := handleArbiter(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleArbiter() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Evidence is stored on the child as the "evidence" artefact.
	if len(spy.CreatedChildren) != 1 {
		t.Fatalf("expected 1 child, got %d", len(spy.CreatedChildren))
	}
	childID := spy.CreatedChildren[0]
	evidenceKey := childID + ":" + artefactEvidence
	ev, ok := spy.ChildStoredArtefacts[evidenceKey]
	if !ok {
		t.Fatal("evidence artefact not found on child")
	}
	capturedEvidence := string(ev)

	// Verify all sections are present.
	sections := []string{
		"## Artefact",
		"An old silent pond",
		"## Deadlocked Feedback",
		"fb-ev",
		"Missing kigo",
		"## Relevant Laws",
		"law-1",
		"## Friction Summary",
		"magnitude=12.50",
	}
	for _, section := range sections {
		if !strings.Contains(capturedEvidence, section) {
			t.Errorf("evidence missing section %q", section)
		}
	}
}

// ---------------------------------------------------------------------------
// Configuration tests
// ---------------------------------------------------------------------------

func TestArbiter_CustomConfig(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-cfg", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED, Source: "appraise"},
	}

	client := setupArbiterTest(t, spy)
	cfg := &arbiterConfig{
		JurySize:   7,
		JurorNode:  "custom-juror",
		GateOutput: "custom-gate",
	}

	if err := handleArbiter(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleArbiter() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Should create 7 children.
	if len(spy.CreatedChildren) != 7 {
		t.Fatalf("expected 7 children, got %d", len(spy.CreatedChildren))
	}

	// Each should route to custom-juror.
	for i, rc := range spy.RoutedChildren {
		if rc.TargetNode != "custom-juror" {
			t.Errorf("child %d routed to %q, expected custom-juror", i, rc.TargetNode)
		}
	}

	// Should route to custom-gate output.
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "custom-gate" {
		t.Fatalf("expected route to custom-gate, got %v", spy.RoutedOutputs)
	}
}

func TestArbiter_DefaultConfig(t *testing.T) {
	cfg := &arbiterConfig{}
	if cfg.jurySize() != defaultJurySize {
		t.Fatalf("expected default jurySize=%d, got %d", defaultJurySize, cfg.jurySize())
	}
	if cfg.jurorNode() != defaultJurorNode {
		t.Fatalf("expected default jurorNode=%q, got %q", defaultJurorNode, cfg.jurorNode())
	}
	if cfg.gateOutput() != outputGate {
		t.Fatalf("expected default gateOutput=%q, got %q", outputGate, cfg.gateOutput())
	}
}

// ---------------------------------------------------------------------------
// No deadlocked feedback
// ---------------------------------------------------------------------------

func TestArbiter_NoDeadlockedFeedback_RoutesToGate(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-ok", State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED},
	}

	client := setupArbiterTest(t, spy)
	cfg := &arbiterConfig{}

	if err := handleArbiter(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleArbiter() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Should NOT fan out.
	if len(spy.CreatedChildren) != 0 {
		t.Fatalf("expected 0 children for no deadlock, got %d", len(spy.CreatedChildren))
	}

	// Should route to gate.
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputGate {
		t.Fatalf("expected route to %q, got %v", outputGate, spy.RoutedOutputs)
	}
}

// ---------------------------------------------------------------------------
// Error propagation tests
// ---------------------------------------------------------------------------

func TestArbiter_Error_GetFlowTopologyFails(t *testing.T) {
	spy := newArbiterSpy()
	spy.GetFlowTopologyErr = fmt.Errorf("topology unavailable")
	client := setupArbiterTest(t, spy)

	err := handleArbiter(context.Background(), client, &arbiterConfig{})
	if err == nil {
		t.Fatal("expected error from GetFlowTopology failure")
	}
}

func TestArbiter_Error_GetFeedbackFails(t *testing.T) {
	spy := newArbiterSpy()
	spy.GetFeedbackErr = fmt.Errorf("feedback unavailable")
	client := setupArbiterTest(t, spy)

	err := handleArbiter(context.Background(), client, &arbiterConfig{})
	if err == nil {
		t.Fatal("expected error from GetFeedback failure")
	}
}

func TestArbiter_Error_GetArtefactFails(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-err", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED, Source: "appraise"},
	}
	spy.GetArtefactErr = fmt.Errorf("artefact unavailable")
	client := setupArbiterTest(t, spy)

	err := handleArbiter(context.Background(), client, &arbiterConfig{})
	if err == nil {
		t.Fatal("expected error from GetArtefact failure")
	}
}

func TestArbiter_Error_FanOutFails(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-err", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED, Source: "appraise"},
	}
	spy.CreateChildErr = fmt.Errorf("cannot create child")
	client := setupArbiterTest(t, spy)

	err := handleArbiter(context.Background(), client, &arbiterConfig{JurySize: 1})
	if err == nil {
		t.Fatal("expected error from FanOut failure")
	}
}

func TestArbiter_Error_AwaitChildrenFails(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-err", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED, Source: "appraise"},
	}
	spy.GetChildrenErr = fmt.Errorf("children unavailable")
	client := setupArbiterTest(t, spy)

	err := handleArbiter(context.Background(), client, &arbiterConfig{JurySize: 1})
	if err == nil {
		t.Fatal("expected error from AwaitChildren failure")
	}
}

// ---------------------------------------------------------------------------
// Goal synthesis
// ---------------------------------------------------------------------------

func TestSynthesizeGoal(t *testing.T) {
	items := []*flowv1.FeedbackItem{
		{Id: "fb-1", Source: "appraise"},
	}
	goal := synthesizeGoal("haiku", items)
	if !strings.Contains(goal, "haiku") {
		t.Errorf("goal missing artefact kind: %s", goal)
	}
	if !strings.Contains(goal, "fb-1") {
		t.Errorf("goal missing feedback ID: %s", goal)
	}
	if !strings.Contains(goal, "appraise") {
		t.Errorf("goal missing source: %s", goal)
	}
}

func TestSynthesizeGoal_Empty(t *testing.T) {
	goal := synthesizeGoal("haiku", nil)
	if !strings.Contains(goal, "haiku") {
		t.Errorf("goal missing artefact kind: %s", goal)
	}
}
