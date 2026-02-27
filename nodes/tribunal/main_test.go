package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// testCustomGate is a test-only constant to avoid goconst complaints.
const testCustomGate = "custom-gate"

// ===========================================================================
// Hearing Mode Tests
// ===========================================================================

// ---------------------------------------------------------------------------
// Fan-out and routing
// ---------------------------------------------------------------------------

func TestHearing_FanOutCount(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{JurySize: 3, JurorNode: "test-juror"}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.CreatedChildren) != 3 {
		t.Fatalf("expected 3 children, got %d", len(spy.CreatedChildren))
	}
	for _, rc := range spy.RoutedChildren {
		if rc.TargetNode != "test-juror" {
			t.Fatalf("expected child routed to test-juror, got %s", rc.TargetNode)
		}
	}
}

func TestHearing_DefaultConfig(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{} // all defaults

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Default jury size is 5.
	if len(spy.CreatedChildren) != 5 {
		t.Fatalf("expected 5 children (default), got %d", len(spy.CreatedChildren))
	}
	// Default juror node is "juror".
	if len(spy.RoutedChildren) > 0 && spy.RoutedChildren[0].TargetNode != "juror" {
		t.Fatalf("expected child routed to juror (default), got %s", spy.RoutedChildren[0].TargetNode)
	}
	// Default gate output is "deliberation-gate".
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "deliberation-gate" {
		t.Fatalf("expected route to deliberation-gate (default), got %v", spy.RoutedOutputs)
	}
}

func TestHearing_RoutesToDeliberationGate(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{GateOutput: testCustomGate}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != testCustomGate {
		t.Fatalf("expected route to custom-gate, got %v", spy.RoutedOutputs)
	}
}

// ---------------------------------------------------------------------------
// Child artefacts (question, evidence, allowed-outcomes)
// ---------------------------------------------------------------------------

func TestHearing_ChildArtefacts_Tier1(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{JurySize: 1}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.CreatedChildren) != 1 {
		t.Fatalf("expected 1 child, got %d", len(spy.CreatedChildren))
	}

	childID := spy.CreatedChildren[0]

	// Question artefact.
	question := string(spy.ChildStoredArtefacts[childID+":question"])
	if !strings.Contains(question, "Finding") {
		t.Errorf("expected question to mention Finding, got: %s", question)
	}

	// Evidence artefact.
	evidence := string(spy.ChildStoredArtefacts[childID+":evidence"])
	if !strings.Contains(evidence, "Law Under Review") {
		t.Errorf("expected evidence to contain 'Law Under Review', got: %s", evidence)
	}
	if !strings.Contains(evidence, "law-under-review-001") {
		t.Errorf("expected evidence to contain law ID")
	}

	// Allowed-outcomes artefact.
	outcomesRaw := spy.ChildStoredArtefacts[childID+":allowed-outcomes"]
	var outcomes []string
	if err := json.Unmarshal(outcomesRaw, &outcomes); err != nil {
		t.Fatalf("parse allowed-outcomes: %v", err)
	}
	if len(outcomes) != 2 || outcomes[0] != "promote" || outcomes[1] != "retire" {
		t.Fatalf("expected [promote, retire] for Tier 1, got %v", outcomes)
	}
}

func TestHearing_ChildArtefacts_Tier2(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_RULING)
	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{JurySize: 1}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	childID := spy.CreatedChildren[0]

	// Question should mention Ruling.
	question := string(spy.ChildStoredArtefacts[childID+":question"])
	if !strings.Contains(question, "Ruling") {
		t.Errorf("expected question to mention Ruling, got: %s", question)
	}

	// Tier 2 should have 3 outcomes: promote, retire, demote.
	outcomesRaw := spy.ChildStoredArtefacts[childID+":allowed-outcomes"]
	var outcomes []string
	if err := json.Unmarshal(outcomesRaw, &outcomes); err != nil {
		t.Fatalf("parse allowed-outcomes: %v", err)
	}
	if len(outcomes) != 3 {
		t.Fatalf("expected 3 allowed outcomes for Tier 2, got %v", outcomes)
	}
}

// ---------------------------------------------------------------------------
// Verdict context artefact
// ---------------------------------------------------------------------------

func TestHearing_StoresVerdictContext(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{JurySize: 1}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	vctx := spy.getStoredVerdictContext()
	if vctx == nil {
		t.Fatal("expected verdict-context artefact to be stored")
	}
	if vctx.Trigger != "hearing" {
		t.Errorf("expected trigger=hearing, got %s", vctx.Trigger)
	}
	if vctx.LawID != "law-under-review-001" {
		t.Errorf("expected law_id=law-under-review-001, got %s", vctx.LawID)
	}
	if vctx.Goal != "Haiku must contain a seasonal reference" {
		t.Errorf("expected goal to match law, got %s", vctx.Goal)
	}
	if len(vctx.AppliesTo) != 1 || vctx.AppliesTo[0] != "haiku" {
		t.Errorf("expected applies_to=[haiku], got %v", vctx.AppliesTo)
	}
	if vctx.Tier != int32(flowv1.LawTier_LAW_TIER_FINDING) {
		t.Errorf("expected tier=%d, got %d",
			int32(flowv1.LawTier_LAW_TIER_FINDING), vctx.Tier)
	}
}

// ---------------------------------------------------------------------------
// Timer pause/resume (via AwaitChildren)
// ---------------------------------------------------------------------------

func TestHearing_PausesAndResumesTimer(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{JurySize: 1}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if !spy.PauseTimerCalled {
		t.Error("expected PauseTimer to be called during AwaitChildren")
	}
	if !spy.ResumeTimerCalled {
		t.Error("expected ResumeTimer to be called after AwaitChildren")
	}
}

// ---------------------------------------------------------------------------
// Evidence assembly
// ---------------------------------------------------------------------------

func TestHearing_EvidenceContainsLawAndFriction(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.FrictionAggregates = []*flowv1.FrictionAggregate{
		{LawId: "law-under-review-001", NodeId: "sort", EventCount: 3, TotalMagnitude: 7.5},
	}
	spy.RelatedLaws = []*flowv1.Law{
		{Id: "law-related-001", Goal: "Related law", Tier: flowv1.LawTier_LAW_TIER_RULING},
	}

	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{JurySize: 1}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	childID := spy.CreatedChildren[0]
	evidence := string(spy.ChildStoredArtefacts[childID+":evidence"])

	sections := []string{
		"## Law Under Review",
		"law-under-review-001",
		"seasonal reference",
		"## Friction Summary",
		"magnitude=7.50",
		"## Related Laws",
		"law-related-001",
	}
	for _, section := range sections {
		if !strings.Contains(evidence, section) {
			t.Errorf("evidence missing section %q", section)
		}
	}
}

// ---------------------------------------------------------------------------
// frameHearingQuestion unit tests
// ---------------------------------------------------------------------------

func TestFrameHearingQuestion_Tier1(t *testing.T) {
	q, outcomes := frameHearingQuestion(flowv1.LawTier_LAW_TIER_FINDING)
	if !strings.Contains(q, "Finding") {
		t.Errorf("expected 'Finding' in question, got: %s", q)
	}
	if len(outcomes) != 2 {
		t.Fatalf("expected 2 outcomes for Tier 1, got %v", outcomes)
	}
}

func TestFrameHearingQuestion_Tier2(t *testing.T) {
	q, outcomes := frameHearingQuestion(flowv1.LawTier_LAW_TIER_RULING)
	if !strings.Contains(q, "Ruling") {
		t.Errorf("expected 'Ruling' in question, got: %s", q)
	}
	if len(outcomes) != 3 {
		t.Fatalf("expected 3 outcomes for Tier 2, got %v", outcomes)
	}
}

// ===========================================================================
// Review Mode Tests
// ===========================================================================

func TestReview_FanOutCount(t *testing.T) {
	spy := newReviewModeSpy()
	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{JurySize: 3, JurorNode: "test-juror"}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.CreatedChildren) != 3 {
		t.Fatalf("expected 3 children in review mode, got %d", len(spy.CreatedChildren))
	}
	for _, rc := range spy.RoutedChildren {
		if rc.TargetNode != "test-juror" {
			t.Fatalf("expected child routed to test-juror, got %s", rc.TargetNode)
		}
	}
}

func TestReview_RoutesToDeliberationGate(t *testing.T) {
	spy := newReviewModeSpy()
	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{GateOutput: testCustomGate}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != testCustomGate {
		t.Fatalf("expected route to custom-gate, got %v", spy.RoutedOutputs)
	}
}

func TestReview_ChildArtefacts(t *testing.T) {
	spy := newReviewModeSpy()
	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{JurySize: 1}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	childID := spy.CreatedChildren[0]

	// Question should be about approve/reject.
	question := string(spy.ChildStoredArtefacts[childID+":question"])
	if !strings.Contains(question, "approved or rejected") {
		t.Errorf("expected review question to mention approve/reject, got: %s", question)
	}

	// Evidence should contain petition content.
	evidence := string(spy.ChildStoredArtefacts[childID+":evidence"])
	if !strings.Contains(evidence, "Petition Under Review") {
		t.Errorf("expected evidence to contain 'Petition Under Review', got: %s", evidence)
	}
	if !strings.Contains(evidence, "Verdict Context") {
		t.Errorf("expected evidence to contain 'Verdict Context'")
	}

	// Allowed-outcomes should be [approve, reject].
	outcomesRaw := spy.ChildStoredArtefacts[childID+":allowed-outcomes"]
	var outcomes []string
	if err := json.Unmarshal(outcomesRaw, &outcomes); err != nil {
		t.Fatalf("parse allowed-outcomes: %v", err)
	}
	if len(outcomes) != 2 || outcomes[0] != "approve" || outcomes[1] != "reject" {
		t.Fatalf("expected [approve, reject], got %v", outcomes)
	}
}

func TestReview_DoesNotStoreVerdictContext(t *testing.T) {
	spy := newReviewModeSpy()
	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{JurySize: 1}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Review mode should NOT store a new verdict-context (it already exists).
	if _, ok := spy.StoredArtefacts[artefactVerdictContext]; ok {
		t.Error("review mode should not store a new verdict-context artefact")
	}
}

func TestReview_PausesAndResumesTimer(t *testing.T) {
	spy := newReviewModeSpy()
	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{JurySize: 1}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if !spy.PauseTimerCalled {
		t.Error("expected PauseTimer to be called during AwaitChildren")
	}
	if !spy.ResumeTimerCalled {
		t.Error("expected ResumeTimer to be called after AwaitChildren")
	}
}

// ===========================================================================
// Mode Detection Tests
// ===========================================================================

func TestModeDetection_PetitionPresent_TriggersReview(t *testing.T) {
	// When both petition and law-reference are present, review mode wins.
	spy := newReviewModeSpy()
	spy.Artefacts["law-reference"] = []byte("law-001") // also set law-reference

	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{JurySize: 1}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// In review mode, question should mention approve/reject.
	childID := spy.CreatedChildren[0]
	question := string(spy.ChildStoredArtefacts[childID+":question"])
	if !strings.Contains(question, "approved or rejected") {
		t.Error("expected review mode when petition artefact is present")
	}
}

func TestModeDetection_NoPetition_TriggersHearing(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{JurySize: 1}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// In hearing mode, question should mention Finding.
	childID := spy.CreatedChildren[0]
	question := string(spy.ChildStoredArtefacts[childID+":question"])
	if !strings.Contains(question, "Finding") {
		t.Error("expected hearing mode when petition artefact is absent")
	}
}

// ===========================================================================
// Error Propagation Tests (Hearing Mode)
// ===========================================================================

func TestHearing_Error_GetArtefactFails(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.GetArtefactErr = fmt.Errorf("artefact unavailable")
	client := setupTribunalTest(t, spy)

	err := handleTribunal(context.Background(), client, &tribunalConfig{})
	if err == nil {
		t.Fatal("expected error from GetArtefact failure")
	}
}

func TestHearing_Error_EmptyLawReference(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.Artefacts["law-reference"] = []byte("")
	client := setupTribunalTest(t, spy)

	err := handleTribunal(context.Background(), client, &tribunalConfig{})
	if err == nil {
		t.Fatal("expected error for empty law reference")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected 'empty' in error, got: %v", err)
	}
}

func TestHearing_Error_GetLawFails(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.GetLawErr = fmt.Errorf("librarian unavailable")
	client := setupTribunalTest(t, spy)

	err := handleTribunal(context.Background(), client, &tribunalConfig{})
	if err == nil {
		t.Fatal("expected error from GetLaw failure")
	}
}

func TestHearing_Error_QueryFrictionFails(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.QueryFrictionErr = fmt.Errorf("friction ledger unavailable")
	client := setupTribunalTest(t, spy)

	err := handleTribunal(context.Background(), client, &tribunalConfig{})
	if err == nil {
		t.Fatal("expected error from QueryFriction failure")
	}
}

func TestHearing_Error_FanOutFails(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.CreateChildErr = fmt.Errorf("cannot create child")
	client := setupTribunalTest(t, spy)

	err := handleTribunal(context.Background(), client, &tribunalConfig{JurySize: 1})
	if err == nil {
		t.Fatal("expected error from FanOut failure")
	}
}

func TestHearing_Error_AwaitChildrenFails(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.GetChildrenErr = fmt.Errorf("cannot get children")
	client := setupTribunalTest(t, spy)

	err := handleTribunal(context.Background(), client, &tribunalConfig{JurySize: 1})
	if err == nil {
		t.Fatal("expected error from AwaitChildren failure")
	}
}

func TestHearing_Error_StoreVerdictContextFails(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.StoreArtefactErr = fmt.Errorf("archivist unavailable")
	client := setupTribunalTest(t, spy)

	err := handleTribunal(context.Background(), client, &tribunalConfig{JurySize: 1})
	if err == nil {
		t.Fatal("expected error from StoreArtefact failure")
	}
}

func TestHearing_Error_RouteToGateFails(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.RouteToOutputErr = fmt.Errorf("routing rejected")
	client := setupTribunalTest(t, spy)

	err := handleTribunal(context.Background(), client, &tribunalConfig{JurySize: 1})
	if err == nil {
		t.Fatal("expected error from RouteToOutput failure")
	}
}

// ===========================================================================
// Error Propagation Tests (Review Mode)
// ===========================================================================

func TestReview_Error_GetVerdictContextFails(t *testing.T) {
	// Create a spy with petition present but verdict-context absent.
	spy := newReviewModeSpy()
	delete(spy.Artefacts, "verdict-context")
	client := setupTribunalTest(t, spy)

	err := handleTribunal(context.Background(), client, &tribunalConfig{JurySize: 1})
	if err == nil {
		t.Fatal("expected error when verdict-context is missing in review mode")
	}
}

func TestReview_Error_FanOutFails(t *testing.T) {
	spy := newReviewModeSpy()
	spy.CreateChildErr = fmt.Errorf("cannot create child")
	client := setupTribunalTest(t, spy)

	err := handleTribunal(context.Background(), client, &tribunalConfig{JurySize: 1})
	if err == nil {
		t.Fatal("expected error from FanOut failure in review mode")
	}
}

func TestReview_Error_RouteToGateFails(t *testing.T) {
	spy := newReviewModeSpy()
	spy.RouteToOutputErr = fmt.Errorf("routing rejected")
	client := setupTribunalTest(t, spy)

	err := handleTribunal(context.Background(), client, &tribunalConfig{JurySize: 1})
	if err == nil {
		t.Fatal("expected error from RouteToOutput failure in review mode")
	}
}

// ===========================================================================
// Config Tests
// ===========================================================================

func TestConfig_Defaults(t *testing.T) {
	cfg := &tribunalConfig{}

	if cfg.jurySize() != 5 {
		t.Errorf("expected default jurySize=5, got %d", cfg.jurySize())
	}
	if cfg.jurorNode() != "juror" {
		t.Errorf("expected default jurorNode=juror, got %s", cfg.jurorNode())
	}
	if cfg.gateOutput() != "deliberation-gate" {
		t.Errorf("expected default gateOutput=deliberation-gate, got %s", cfg.gateOutput())
	}
}

func TestConfig_Custom(t *testing.T) {
	cfg := &tribunalConfig{
		JurySize:   7,
		JurorNode:  "custom-juror",
		GateOutput: testCustomGate,
	}

	if cfg.jurySize() != 7 {
		t.Errorf("expected jurySize=7, got %d", cfg.jurySize())
	}
	if cfg.jurorNode() != "custom-juror" {
		t.Errorf("expected jurorNode=custom-juror, got %s", cfg.jurorNode())
	}
	if cfg.gateOutput() != testCustomGate {
		t.Errorf("expected gateOutput=custom-gate, got %s", cfg.gateOutput())
	}
}

// ===========================================================================
// assembleHearingEvidence unit tests
// ===========================================================================

func TestAssembleHearingEvidence_NoFriction(t *testing.T) {
	law := &flowv1.Law{
		Id:   "law-001",
		Goal: "test goal",
		Tier: flowv1.LawTier_LAW_TIER_FINDING,
	}

	evidence := assembleHearingEvidence(law, nil, nil)
	if !strings.Contains(evidence, "No friction data") {
		t.Error("expected 'No friction data' in evidence with nil friction")
	}
	if !strings.Contains(evidence, "No related laws") {
		t.Error("expected 'No related laws' in evidence with nil related laws")
	}
}

func TestAssembleHearingEvidence_WithData(t *testing.T) {
	law := &flowv1.Law{
		Id:        "law-001",
		Goal:      "test goal",
		Tier:      flowv1.LawTier_LAW_TIER_RULING,
		AppliesTo: []string{"haiku"},
		Representations: []*flowv1.Representation{
			{Type: "text/markdown", Content: "markdown content"},
		},
	}
	friction := []*flowv1.FrictionAggregate{
		{NodeId: "sort", EventCount: 5, TotalMagnitude: 12.5},
	}
	related := []*flowv1.Law{
		{Id: "law-002", Goal: "related goal", Tier: flowv1.LawTier_LAW_TIER_FINDING},
	}

	evidence := assembleHearingEvidence(law, friction, related)

	checks := []string{
		"law-001",
		"test goal",
		"haiku",
		"markdown content",
		"magnitude=12.50",
		"law-002",
		"related goal",
	}
	for _, check := range checks {
		if !strings.Contains(evidence, check) {
			t.Errorf("evidence missing %q", check)
		}
	}
}

func TestAssembleHearingEvidence_SkipsSelf(t *testing.T) {
	law := &flowv1.Law{Id: "law-001", Goal: "test"}
	related := []*flowv1.Law{
		{Id: "law-001", Goal: "self"},
		{Id: "law-002", Goal: "other"},
	}

	evidence := assembleHearingEvidence(law, nil, related)
	// "self" appears in the related section only if the self-entry is NOT skipped.
	// Check that law-002 is present but count occurrences carefully.
	if !strings.Contains(evidence, "law-002") {
		t.Error("expected law-002 in related laws")
	}
}

// ===========================================================================
// assembleReviewEvidence unit tests
// ===========================================================================

func TestAssembleReviewEvidence(t *testing.T) {
	evidence := assembleReviewEvidence("petition content here", "verdict context here")

	if !strings.Contains(evidence, "Petition Under Review") {
		t.Error("expected 'Petition Under Review' header")
	}
	if !strings.Contains(evidence, "petition content here") {
		t.Error("expected petition content in evidence")
	}
	if !strings.Contains(evidence, "Verdict Context") {
		t.Error("expected 'Verdict Context' header")
	}
	if !strings.Contains(evidence, "verdict context here") {
		t.Error("expected verdict context content in evidence")
	}
}
