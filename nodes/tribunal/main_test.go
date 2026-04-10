package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/tally"
)

func TestTribunal_HappyPath_ConsensusRound1_ClerkChildAndComplete(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	seedJurorVerdict(spy, "child-1", outcomePromote, "Strong evidence of durable value")
	seedJurorVerdict(spy, "child-2", outcomePromote, "Friction suggests promotion")
	seedJurorVerdict(spy, "child-3", outcomeRetire, "Retirement would simplify the system")

	client := setupTribunalTest(t, spy)
	cfg := defaultTestConfig()

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.CreatedChildren) != 4 {
		t.Fatalf("expected 4 children (3 jurors + 1 clerk), got %d", len(spy.CreatedChildren))
	}
	if len(spy.RoutedChildren) != 4 {
		t.Fatalf("expected 4 routed children, got %d", len(spy.RoutedChildren))
	}
	for i := range 3 {
		if spy.RoutedChildren[i].TargetNode != defaultJurorNode {
			t.Fatalf("juror child %d routed to %q, want %q", i, spy.RoutedChildren[i].TargetNode, defaultJurorNode)
		}
	}
	if spy.RoutedChildren[3].TargetNode != defaultClerkNode {
		t.Fatalf("clerk child routed to %q, want %q", spy.RoutedChildren[3].TargetNode, defaultClerkNode)
	}

	assertCompleted(t, spy)
	if len(spy.RoutedOutputs) != 0 {
		t.Fatalf("unexpected routed outputs: %v", spy.RoutedOutputs)
	}
	if !spy.PauseTimerCalled || !spy.ResumeTimerCalled {
		t.Fatalf("expected AwaitChildren to pause and resume timer")
	}

	vctx, _ := clerkChildVerdictContext(t, spy)
	if vctx.Trigger != "hearing" {
		t.Fatalf("trigger = %q, want hearing", vctx.Trigger)
	}
	if !strings.Contains(vctx.Decision, "law-under-review-001") {
		t.Fatalf("decision does not mention law id: %q", vctx.Decision)
	}
}

func TestTribunal_HungAfterMaxRounds_RoutesToHung(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	seedJurorVerdict(spy, "child-1", outcomePromote, "promote")
	seedJurorVerdict(spy, "child-2", outcomeRetire, "retire")
	seedJurorVerdict(spy, "child-3", "abstain", "unclear")

	client := setupTribunalTest(t, spy)
	cfg := defaultTestConfig()

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	assertRoutedTo(t, spy, defaultHungOutput)
	if len(spy.CompletedReasons) != 0 {
		t.Fatalf("unexpected completions: %v", spy.CompletedReasons)
	}
	if len(spy.CreatedChildren) != 3 {
		t.Fatalf("expected 3 juror children only, got %d", len(spy.CreatedChildren))
	}
}

func TestTribunal_MultiRoundRetry_ConsensusRound2(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	seedJurorVerdict(spy, "child-1", outcomePromote, "promote")
	seedJurorVerdict(spy, "child-2", outcomeRetire, "retire")
	seedJurorVerdict(spy, "child-3", "abstain", "unclear")
	seedJurorVerdict(spy, "child-4", outcomePromote, "peer arguments changed my view")
	seedJurorVerdict(spy, "child-5", outcomePromote, "promotion best fits the evidence")
	seedJurorVerdict(spy, "child-6", outcomeRetire, "still prefer retirement")

	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{JurySize: 3, MaxRounds: 2}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.CreatedChildren) != 7 {
		t.Fatalf("expected 7 children, got %d", len(spy.CreatedChildren))
	}
	assertCompleted(t, spy)

	for _, childID := range []string{"child-4", "child-5", "child-6"} {
		content, ok := spy.ChildStoredArtefacts[childID+":"+tally.ArtefactPriorRound]
		if !ok {
			t.Fatalf("missing prior-round-reasoning on %s", childID)
		}
		if !strings.Contains(string(content), "Prior round reasoning") {
			t.Fatalf("unexpected prior-round-reasoning on %s: %q", childID, string(content))
		}
	}
	for _, childID := range []string{"child-1", "child-2", "child-3"} {
		if _, ok := spy.ChildStoredArtefacts[childID+":"+tally.ArtefactPriorRound]; ok {
			t.Fatalf("round 1 child %s unexpectedly had prior-round-reasoning", childID)
		}
	}
}

func TestTribunal_VerdictContextIsProseOnly(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	seedJurorVerdict(spy, "child-1", outcomePromote, "Strong precedent for promotion")
	seedJurorVerdict(spy, "child-2", outcomePromote, "The law has proven durable")
	seedJurorVerdict(spy, "child-3", outcomeRetire, "Retirement would reduce noise")

	client := setupTribunalTest(t, spy)
	if err := handleTribunal(context.Background(), client, defaultTestConfig()); err != nil {
		t.Fatalf("handleTribunal: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	vctx, raw := clerkChildVerdictContext(t, spy)
	if vctx.Trigger != "hearing" {
		t.Fatalf("trigger = %q, want hearing", vctx.Trigger)
	}
	if vctx.Decision == "" {
		t.Fatal("expected non-empty decision")
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatalf("unmarshal raw verdict-context: %v", err)
	}
	if len(fields) != 2 {
		t.Fatalf("expected exactly 2 verdict-context fields, got %v", fields)
	}
	for _, required := range []string{"trigger", "decision"} {
		if _, ok := fields[required]; !ok {
			t.Fatalf("missing required verdict-context field %q in %v", required, fields)
		}
	}
	for _, forbidden := range []string{"law_id", "goal", "tier", "action", "applies_to"} {
		if _, ok := fields[forbidden]; ok {
			t.Fatalf("unexpected structured field %q in %v", forbidden, fields)
		}
	}
}

func TestTribunal_FireAndForget_CompletesWithoutSuspending(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	seedJurorVerdict(spy, "child-1", outcomePromote, "promote")
	seedJurorVerdict(spy, "child-2", outcomePromote, "promote")
	seedJurorVerdict(spy, "child-3", outcomeRetire, "retire")

	client := setupTribunalTest(t, spy)
	if err := handleTribunal(context.Background(), client, defaultTestConfig()); err != nil {
		t.Fatalf("handleTribunal: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	assertCompleted(t, spy)
	if len(spy.CreatedChildren) != 4 {
		t.Fatalf("expected clerk child to be created before completion, got %d children", len(spy.CreatedChildren))
	}
	if len(spy.RoutedOutputs) != 0 {
		t.Fatalf("unexpected route-to-output calls: %v", spy.RoutedOutputs)
	}
}

func TestTribunal_TierBasedQuestionFraming(t *testing.T) {
	tests := []struct {
		name         string
		tier         flowv1.LawTier
		wantQuestion string
		wantOutcomes []string
	}{
		{
			name:         "finding",
			tier:         flowv1.LawTier_LAW_TIER_FINDING,
			wantQuestion: "Finding",
			wantOutcomes: []string{outcomePromote, outcomeRetire},
		},
		{
			name:         "ruling",
			tier:         flowv1.LawTier_LAW_TIER_RULING,
			wantQuestion: "Ruling",
			wantOutcomes: []string{outcomePromote, outcomeRetire, outcomeDemote},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			question, outcomes := frameHearingQuestion(tt.tier)
			if !strings.Contains(question, tt.wantQuestion) {
				t.Fatalf("question %q does not contain %q", question, tt.wantQuestion)
			}
			if fmt.Sprint(outcomes) != fmt.Sprint(tt.wantOutcomes) {
				t.Fatalf("outcomes = %v, want %v", outcomes, tt.wantOutcomes)
			}
		})
	}
}

func TestTribunal_EvidenceAssemblyContainsLawFrictionAndRelatedLaws(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.Friction = []*flowv1.FrictionAggregate{{
		LawId:          "law-under-review-001",
		NodeId:         "sort",
		EventCount:     3,
		TotalMagnitude: 7.5,
	}}
	spy.RelatedLaws = []*flowv1.Law{{
		Id:   "law-related-001",
		Goal: "Related law goal",
		Tier: flowv1.LawTier_LAW_TIER_RULING,
	}}
	seedJurorVerdict(spy, "child-1", outcomePromote, "promote")
	seedJurorVerdict(spy, "child-2", outcomePromote, "promote")
	seedJurorVerdict(spy, "child-3", outcomeRetire, "retire")

	client := setupTribunalTest(t, spy)
	if err := handleTribunal(context.Background(), client, defaultTestConfig()); err != nil {
		t.Fatalf("handleTribunal: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	evidence := string(spy.ChildStoredArtefacts["child-1:"+tally.ArtefactEvidence])
	for _, want := range []string{
		"## Law Under Review",
		"law-under-review-001",
		"seasonal reference",
		"## Friction Summary",
		"magnitude=7.50",
		"## Related Laws",
		"law-related-001",
	} {
		if !strings.Contains(evidence, want) {
			t.Fatalf("evidence missing %q", want)
		}
	}
}

func TestTribunal_Error_MissingLawReferenceArtefact(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	delete(spy.Artefacts, artefactLawReference)

	client := setupTribunalTest(t, spy)
	err := handleTribunal(context.Background(), client, defaultTestConfig())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTribunal_Error_GetLawFails(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.GetLawErr = fmt.Errorf("librarian unavailable")

	client := setupTribunalTest(t, spy)
	err := handleTribunal(context.Background(), client, defaultTestConfig())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTribunal_Error_FanOutFails(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.CreateChildErr = fmt.Errorf("cannot create child")

	client := setupTribunalTest(t, spy)
	err := handleTribunal(context.Background(), client, &tribunalConfig{JurySize: 1})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTribunal_ConsensusStrategies(t *testing.T) {
	tests := []struct {
		name      string
		strategy  string
		votes     []string
		wantRoute bool
	}{
		{
			name:      "simple majority consensus",
			strategy:  "SIMPLE_MAJORITY",
			votes:     []string{outcomePromote, outcomePromote, outcomeRetire},
			wantRoute: false,
		},
		{
			name:      "unanimity hangs",
			strategy:  "UNANIMITY",
			votes:     []string{outcomePromote, outcomePromote, outcomeRetire},
			wantRoute: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
			for i, vote := range tt.votes {
				seedJurorVerdict(spy, fmt.Sprintf("child-%d", i+1), vote, vote)
			}

			client := setupTribunalTest(t, spy)
			cfg := &tribunalConfig{JurySize: 3, MaxRounds: 1, ConsensusStrategy: tt.strategy}

			if err := handleTribunal(context.Background(), client, cfg); err != nil {
				t.Fatalf("handleTribunal: %v", err)
			}

			spy.mu.Lock()
			defer spy.mu.Unlock()

			if tt.wantRoute {
				assertRoutedTo(t, spy, defaultHungOutput)
				return
			}
			assertCompleted(t, spy)
		})
	}
}

func TestTribunal_ConfigDefaultsAndCustomConfig(t *testing.T) {
	defaults := &tribunalConfig{}
	if defaults.jurySize() != defaultJurySize {
		t.Fatalf("jurySize default = %d, want %d", defaults.jurySize(), defaultJurySize)
	}
	if defaults.jurorNode() != defaultJurorNode {
		t.Fatalf("jurorNode default = %q, want %q", defaults.jurorNode(), defaultJurorNode)
	}
	if defaults.clerkNode() != defaultClerkNode {
		t.Fatalf("clerkNode default = %q, want %q", defaults.clerkNode(), defaultClerkNode)
	}
	if defaults.hungOutput() != defaultHungOutput {
		t.Fatalf("hungOutput default = %q, want %q", defaults.hungOutput(), defaultHungOutput)
	}
	if defaults.maxRounds() != defaultMaxRounds {
		t.Fatalf("maxRounds default = %d, want %d", defaults.maxRounds(), defaultMaxRounds)
	}

	custom := &tribunalConfig{
		JurySize:          7,
		JurorNode:         "jury-box",
		ConsensusStrategy: "UNANIMITY",
		MaxRounds:         4,
		ClerkNode:         "clerk-special",
		HungOutput:        "needs-human",
	}
	if custom.jurySize() != 7 || custom.jurorNode() != "jury-box" || custom.maxRounds() != 4 {
		t.Fatal("custom numeric/string config not applied")
	}
	if custom.clerkNode() != "clerk-special" || custom.hungOutput() != "needs-human" {
		t.Fatal("custom routing config not applied")
	}
	if custom.consensusStrategy() != flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_UNANIMITY {
		t.Fatal("custom consensus strategy not parsed")
	}
}

func TestTribunal_NoReviewMode_PetitionArtefactNotRead(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.Artefacts["petition"] = []byte("should be ignored")
	seedJurorVerdict(spy, "child-1", outcomePromote, "promote")
	seedJurorVerdict(spy, "child-2", outcomePromote, "promote")
	seedJurorVerdict(spy, "child-3", outcomeRetire, "retire")

	client := setupTribunalTest(t, spy)
	if err := handleTribunal(context.Background(), client, defaultTestConfig()); err != nil {
		t.Fatalf("handleTribunal: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	for _, req := range spy.GetArtefactRequests {
		if req == "petition" {
			t.Fatal("tribunal should not read petition artefact in hearing-only mode")
		}
	}
}

func TestAssembleHearingEvidence_NoFriction(t *testing.T) {
	law := &flowv1.Law{Id: "law-001", Goal: "test goal", Tier: flowv1.LawTier_LAW_TIER_FINDING}
	evidence := assembleHearingEvidence(law, nil, nil)
	if !strings.Contains(evidence, "No friction data") {
		t.Fatal("expected no friction message")
	}
	if !strings.Contains(evidence, "No related laws") {
		t.Fatal("expected no related laws message")
	}
}

func TestAssembleHearingEvidence_SkipsSelf(t *testing.T) {
	law := &flowv1.Law{Id: "law-001", Goal: "test"}
	related := []*flowv1.Law{{Id: "law-001", Goal: "self"}, {Id: "law-002", Goal: "other"}}
	evidence := assembleHearingEvidence(law, nil, related)
	if strings.Contains(evidence, "self") {
		t.Fatal("expected self law to be skipped from related laws")
	}
	if !strings.Contains(evidence, "law-002") {
		t.Fatal("expected related law in evidence")
	}
}
