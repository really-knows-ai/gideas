package main

import (
	"context"
	"encoding/json"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Happy Path: Approved, Tier 1, all feedback resolved -> apply + complete
// ---------------------------------------------------------------------------

func TestGate_Approved_Tier1_Resolved_AppliesCreateAndCompletes(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    outcomeApprove,
		RoundsUsed: 1,
	})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Context: petitionContext{Trigger: "deadlock-resolution"},
			Changes: []petitionChange{
				{
					Action:    "create",
					Tier:      1,
					Goal:      "enforce naming conventions",
					AppliesTo: []string{"*.go"},
					Representations: []petitionRep{
						{Type: "text/markdown", Content: "# Law\n\nnaming rules"},
					},
				},
			},
		},
	})
	seedLawReference(spy, "law-001")
	spy.Laws["law-001"] = &flowv1.Law{
		Id:   "law-001",
		Tier: flowv1.LawTier_LAW_TIER_FINDING,
	}

	client := setupGateTest(t, spy)

	if err := handleJudiciaryGate(context.Background(), client); err != nil {
		t.Fatalf("handleJudiciaryGate: %v", err)
	}

	assertCompleted(t, spy)

	if len(spy.WrittenLaws) != 1 {
		t.Fatalf("expected 1 WriteLaw call, got %d", len(spy.WrittenLaws))
	}
	if spy.WrittenLaws[0].GetGoal() != "enforce naming conventions" {
		t.Errorf("WriteLaw goal = %q, want %q", spy.WrittenLaws[0].GetGoal(), "enforce naming conventions")
	}

	// Verify approval stamp was stored.
	stampData, ok := spy.StoredArtefacts[artefactApprovalStamp]
	if !ok {
		t.Fatal("expected approval-stamp artefact to be stored")
	}
	var stamp approvalStamp
	if err := json.Unmarshal(stampData, &stamp); err != nil {
		t.Fatalf("unmarshal approval stamp: %v", err)
	}
	if !stamp.Applied {
		t.Error("expected stamp.Applied = true")
	}
	if len(stamp.LawResults) != 1 {
		t.Fatalf("expected 1 law result, got %d", len(stamp.LawResults))
	}
	if stamp.LawResults[0].Action != "create" {
		t.Errorf("law result action = %q, want %q", stamp.LawResults[0].Action, "create")
	}
}

// ---------------------------------------------------------------------------
// Approved, Tier 2, resolved -> apply retire + complete
// ---------------------------------------------------------------------------

func TestGate_Approved_Tier2_Resolved_AppliesRetireAndCompletes(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    outcomeApprove,
		RoundsUsed: 2,
	})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Context: petitionContext{Trigger: "friction-hearing"},
			Changes: []petitionChange{
				{
					Action: "retire",
					LawID:  "old-law-42",
				},
			},
		},
	})
	seedLawReference(spy, "old-law-42")
	spy.Laws["old-law-42"] = &flowv1.Law{
		Id:   "old-law-42",
		Tier: flowv1.LawTier_LAW_TIER_RULING,
	}

	client := setupGateTest(t, spy)

	if err := handleJudiciaryGate(context.Background(), client); err != nil {
		t.Fatalf("handleJudiciaryGate: %v", err)
	}

	assertCompleted(t, spy)

	if len(spy.RetiredLawIDs) != 1 || spy.RetiredLawIDs[0] != "old-law-42" {
		t.Errorf("expected RetireLaw('old-law-42'), got %v", spy.RetiredLawIDs)
	}
}

// ---------------------------------------------------------------------------
// Approved, Tier 2, resolved -> apply demote + complete
// ---------------------------------------------------------------------------

func TestGate_Approved_Tier2_Resolved_AppliesDemoteAndCompletes(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    outcomeApprove,
		RoundsUsed: 1,
	})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Context: petitionContext{Trigger: "ttl-hearing"},
			Changes: []petitionChange{
				{
					Action: "demote",
					LawID:  "demote-law-7",
					Tier:   1,
				},
			},
		},
	})
	seedLawReference(spy, "demote-law-7")
	spy.Laws["demote-law-7"] = &flowv1.Law{
		Id:   "demote-law-7",
		Goal: "original goal",
		Tier: flowv1.LawTier_LAW_TIER_RULING,
	}

	client := setupGateTest(t, spy)

	if err := handleJudiciaryGate(context.Background(), client); err != nil {
		t.Fatalf("handleJudiciaryGate: %v", err)
	}

	assertCompleted(t, spy)

	if len(spy.WrittenLaws) != 1 {
		t.Fatalf("expected 1 WriteLaw call (demote), got %d", len(spy.WrittenLaws))
	}
	if spy.WrittenLaws[0].GetTier() != flowv1.LawTier_LAW_TIER_FINDING {
		t.Errorf("demoted law tier = %v, want FINDING", spy.WrittenLaws[0].GetTier())
	}
}

// ---------------------------------------------------------------------------
// Multiple changes in one petition
// ---------------------------------------------------------------------------

func TestGate_Approved_MultipleChanges_AllApplied(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    outcomeApprove,
		RoundsUsed: 1,
	})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Context: petitionContext{Trigger: "deadlock-resolution"},
			Changes: []petitionChange{
				{
					Action:    "create",
					Tier:      1,
					Goal:      "new rule",
					AppliesTo: []string{"*.ts"},
				},
				{
					Action: "retire",
					LawID:  "retiring-law",
				},
			},
		},
	})
	seedLawReference(spy, "law-multi")
	spy.Laws["law-multi"] = &flowv1.Law{
		Id:   "law-multi",
		Tier: flowv1.LawTier_LAW_TIER_FINDING,
	}

	client := setupGateTest(t, spy)

	if err := handleJudiciaryGate(context.Background(), client); err != nil {
		t.Fatalf("handleJudiciaryGate: %v", err)
	}

	assertCompleted(t, spy)

	if len(spy.WrittenLaws) != 1 {
		t.Fatalf("expected 1 WriteLaw call, got %d", len(spy.WrittenLaws))
	}
	if len(spy.RetiredLawIDs) != 1 || spy.RetiredLawIDs[0] != "retiring-law" {
		t.Fatalf("expected 1 RetireLaw call for 'retiring-law', got %v", spy.RetiredLawIDs)
	}

	stampData := spy.StoredArtefacts[artefactApprovalStamp]
	var stamp approvalStamp
	if err := json.Unmarshal(stampData, &stamp); err != nil {
		t.Fatalf("unmarshal stamp: %v", err)
	}
	if len(stamp.LawResults) != 2 {
		t.Errorf("expected 2 law results, got %d", len(stamp.LawResults))
	}
}

// ---------------------------------------------------------------------------
// Rejected petition -> route to Clerk
// ---------------------------------------------------------------------------

func TestGate_Rejected_RoutesToClerk(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    "reject",
		RoundsUsed: 2,
	})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Context: petitionContext{Trigger: "friction-hearing"},
			Changes: []petitionChange{{Action: "create", Tier: 1}},
		},
	})
	seedLawReference(spy, "law-rej")
	spy.Laws["law-rej"] = &flowv1.Law{
		Id:   "law-rej",
		Tier: flowv1.LawTier_LAW_TIER_FINDING,
	}

	client := setupGateTest(t, spy)

	if err := handleJudiciaryGate(context.Background(), client); err != nil {
		t.Fatalf("handleJudiciaryGate: %v", err)
	}

	assertRoutedTo(t, spy, outputClerk)
	if spy.Completed {
		t.Error("expected no Complete() on rejection")
	}
}

// ---------------------------------------------------------------------------
// Approved but unresolved feedback -> route to Clerk
// ---------------------------------------------------------------------------

func TestGate_Approved_UnresolvedFeedback_RoutesToClerk(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    outcomeApprove,
		RoundsUsed: 1,
	})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Context: petitionContext{Trigger: "deadlock-resolution"},
			Changes: []petitionChange{{Action: "create", Tier: 2}},
		},
	})
	seedLawReference(spy, "law-fb")
	spy.Laws["law-fb"] = &flowv1.Law{
		Id:   "law-fb",
		Tier: flowv1.LawTier_LAW_TIER_RULING,
	}
	// Unresolved feedback on the petition.
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:       "fb-1",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_NEW,
			Severity: flowv1.Severity_SEVERITY_HIGH,
			Message:  "needs revision",
		},
	}

	client := setupGateTest(t, spy)

	if err := handleJudiciaryGate(context.Background(), client); err != nil {
		t.Fatalf("handleJudiciaryGate: %v", err)
	}

	assertRoutedTo(t, spy, outputClerk)
}

// ---------------------------------------------------------------------------
// Approved, feedback resolved -> applies (feedback doesn't block)
// ---------------------------------------------------------------------------

func TestGate_Approved_ResolvedFeedback_Applies(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    outcomeApprove,
		RoundsUsed: 1,
	})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Context: petitionContext{Trigger: "deadlock-resolution"},
			Changes: []petitionChange{
				{Action: "create", Tier: 1, Goal: "resolved feedback law"},
			},
		},
	})
	seedLawReference(spy, "law-res")
	spy.Laws["law-res"] = &flowv1.Law{
		Id:   "law-res",
		Tier: flowv1.LawTier_LAW_TIER_FINDING,
	}
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:    "fb-resolved",
			State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED,
		},
	}

	client := setupGateTest(t, spy)

	if err := handleJudiciaryGate(context.Background(), client); err != nil {
		t.Fatalf("handleJudiciaryGate: %v", err)
	}

	assertCompleted(t, spy)
}

// ---------------------------------------------------------------------------
// Mixed feedback: one resolved, one new -> routes to Clerk
// ---------------------------------------------------------------------------

func TestGate_Approved_MixedFeedback_RoutesToClerk(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    outcomeApprove,
		RoundsUsed: 1,
	})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Context: petitionContext{Trigger: "deadlock-resolution"},
			Changes: []petitionChange{{Action: "create", Tier: 1}},
		},
	})
	seedLawReference(spy, "law-mix")
	spy.Laws["law-mix"] = &flowv1.Law{
		Id:   "law-mix",
		Tier: flowv1.LawTier_LAW_TIER_FINDING,
	}
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-ok", State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED},
		{Id: "fb-new", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW, Severity: flowv1.Severity_SEVERITY_MEDIUM},
	}

	client := setupGateTest(t, spy)

	if err := handleJudiciaryGate(context.Background(), client); err != nil {
		t.Fatalf("handleJudiciaryGate: %v", err)
	}

	assertRoutedTo(t, spy, outputClerk)
}

// ---------------------------------------------------------------------------
// Tier 3, Approved -> Advocate (HITL ratification)
// ---------------------------------------------------------------------------

func TestGate_Approved_Tier3_RoutesToAdvocate(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    outcomeApprove,
		RoundsUsed: 1,
	})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Context: petitionContext{Trigger: "friction-hearing"},
			Changes: []petitionChange{{Action: "create", Tier: 3}},
		},
	})
	seedLawReference(spy, "law-t3")
	spy.Laws["law-t3"] = &flowv1.Law{
		Id:   "law-t3",
		Tier: flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
	}

	client := setupGateTest(t, spy)

	if err := handleJudiciaryGate(context.Background(), client); err != nil {
		t.Fatalf("handleJudiciaryGate: %v", err)
	}

	assertRoutedTo(t, spy, outputAdvocate)
	if spy.Completed {
		t.Error("expected no Complete() for Tier 3 HITL ratification")
	}
}

// ---------------------------------------------------------------------------
// Tier 3, Rejected -> Clerk (not Advocate, because not approved)
// ---------------------------------------------------------------------------

func TestGate_Rejected_Tier3_RoutesToClerk(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    "reject",
		RoundsUsed: 2,
	})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Context: petitionContext{Trigger: "friction-hearing"},
			Changes: []petitionChange{{Action: "create", Tier: 3}},
		},
	})
	seedLawReference(spy, "law-t3r")
	spy.Laws["law-t3r"] = &flowv1.Law{
		Id:   "law-t3r",
		Tier: flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
	}

	client := setupGateTest(t, spy)

	if err := handleJudiciaryGate(context.Background(), client); err != nil {
		t.Fatalf("handleJudiciaryGate: %v", err)
	}

	assertRoutedTo(t, spy, outputClerk)
}

// ---------------------------------------------------------------------------
// Tier 4 -> always Advocate (Governance Flow)
// ---------------------------------------------------------------------------

func TestGate_Tier4_RoutesToAdvocate(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    outcomeApprove,
		RoundsUsed: 1,
	})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Context: petitionContext{Trigger: "ttl-hearing"},
			Changes: []petitionChange{{Action: "create", Tier: 4}},
		},
	})
	seedLawReference(spy, "law-t4")
	spy.Laws["law-t4"] = &flowv1.Law{
		Id:   "law-t4",
		Tier: flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION,
	}

	client := setupGateTest(t, spy)

	if err := handleJudiciaryGate(context.Background(), client); err != nil {
		t.Fatalf("handleJudiciaryGate: %v", err)
	}

	assertRoutedTo(t, spy, outputAdvocate)
}

// ---------------------------------------------------------------------------
// Tier 5 -> always Advocate (Governance Flow)
// ---------------------------------------------------------------------------

func TestGate_Tier5_RoutesToAdvocate(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    "reject",
		RoundsUsed: 3,
	})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Context: petitionContext{Trigger: "friction-hearing"},
			Changes: []petitionChange{{Action: "retire", LawID: "fed-law"}},
		},
	})
	seedLawReference(spy, "fed-law")
	spy.Laws["fed-law"] = &flowv1.Law{
		Id:   "fed-law",
		Tier: flowv1.LawTier_LAW_TIER_FEDERAL_ACCORD,
	}

	client := setupGateTest(t, spy)

	if err := handleJudiciaryGate(context.Background(), client); err != nil {
		t.Fatalf("handleJudiciaryGate: %v", err)
	}

	assertRoutedTo(t, spy, outputAdvocate)
}

// ---------------------------------------------------------------------------
// Tier 4 Rejected -> still Advocate (governance flow overrides rejection)
// ---------------------------------------------------------------------------

func TestGate_Tier4_Rejected_StillAdvocate(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    "reject",
		RoundsUsed: 2,
	})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Context: petitionContext{Trigger: "ttl-hearing"},
			Changes: []petitionChange{{Action: "create", Tier: 4}},
		},
	})
	seedLawReference(spy, "law-t4r")
	spy.Laws["law-t4r"] = &flowv1.Law{
		Id:   "law-t4r",
		Tier: flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION,
	}

	client := setupGateTest(t, spy)

	if err := handleJudiciaryGate(context.Background(), client); err != nil {
		t.Fatalf("handleJudiciaryGate: %v", err)
	}

	assertRoutedTo(t, spy, outputAdvocate)
}

// ---------------------------------------------------------------------------
// Create law: representations are passed through to Librarian
// ---------------------------------------------------------------------------

func TestGate_Create_RepresentationsPassedToLibrarian(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    outcomeApprove,
		RoundsUsed: 1,
	})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Context: petitionContext{Trigger: "deadlock-resolution"},
			Changes: []petitionChange{
				{
					Action:    "create",
					Tier:      2,
					Goal:      "reps test",
					AppliesTo: []string{"*.py"},
					Representations: []petitionRep{
						{Type: "text/markdown", Content: "# Rule"},
						{Type: "application/smt-lib", Content: "(assert true)"},
					},
				},
			},
		},
	})
	seedLawReference(spy, "law-reps")
	spy.Laws["law-reps"] = &flowv1.Law{
		Id:   "law-reps",
		Tier: flowv1.LawTier_LAW_TIER_RULING,
	}

	client := setupGateTest(t, spy)

	if err := handleJudiciaryGate(context.Background(), client); err != nil {
		t.Fatalf("handleJudiciaryGate: %v", err)
	}

	assertCompleted(t, spy)
	if len(spy.WrittenLaws) != 1 {
		t.Fatalf("expected 1 WriteLaw, got %d", len(spy.WrittenLaws))
	}

	reps := spy.WrittenLaws[0].GetRepresentations()
	if len(reps) != 2 {
		t.Fatalf("expected 2 representations, got %d", len(reps))
	}
	if reps[0].GetType() != "text/markdown" {
		t.Errorf("rep[0].Type = %q, want %q", reps[0].GetType(), "text/markdown")
	}
	if reps[1].GetType() != "application/smt-lib" {
		t.Errorf("rep[1].Type = %q, want %q", reps[1].GetType(), "application/smt-lib")
	}
}

// ---------------------------------------------------------------------------
// Law reference whitespace is trimmed
// ---------------------------------------------------------------------------

func TestGate_LawReference_WhitespaceTrimmed(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    outcomeApprove,
		RoundsUsed: 1,
	})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Changes: []petitionChange{{Action: "create", Tier: 1, Goal: "trim test"}},
		},
	})
	spy.Artefacts[artefactLawReference] = []byte("  trimmed-law \n")
	spy.Laws["trimmed-law"] = &flowv1.Law{
		Id:   "trimmed-law",
		Tier: flowv1.LawTier_LAW_TIER_FINDING,
	}

	client := setupGateTest(t, spy)

	if err := handleJudiciaryGate(context.Background(), client); err != nil {
		t.Fatalf("handleJudiciaryGate: %v", err)
	}

	if len(spy.RequestedLawIDs) == 0 || spy.RequestedLawIDs[0] != "trimmed-law" {
		t.Errorf("requested law ID = %v, want [trimmed-law]", spy.RequestedLawIDs)
	}
}

// ---------------------------------------------------------------------------
// Error: deliberation-result missing
// ---------------------------------------------------------------------------

func TestGate_Error_MissingDeliberationResult(t *testing.T) {
	spy := newGateSpy()
	seedPetition(t, spy, petition{})
	seedLawReference(spy, "law-x")
	spy.Laws["law-x"] = &flowv1.Law{Id: "law-x", Tier: flowv1.LawTier_LAW_TIER_FINDING}

	client := setupGateTest(t, spy)

	err := handleJudiciaryGate(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when deliberation-result is missing")
	}
}

// ---------------------------------------------------------------------------
// Error: invalid deliberation-result JSON
// ---------------------------------------------------------------------------

func TestGate_Error_InvalidDeliberationResultJSON(t *testing.T) {
	spy := newGateSpy()
	spy.Artefacts[artefactDeliberationResult] = []byte("not valid json{{{")
	seedPetition(t, spy, petition{})
	seedLawReference(spy, "law-y")
	spy.Laws["law-y"] = &flowv1.Law{Id: "law-y", Tier: flowv1.LawTier_LAW_TIER_FINDING}

	client := setupGateTest(t, spy)

	err := handleJudiciaryGate(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when deliberation-result has invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// Error: petition artefact missing
// ---------------------------------------------------------------------------

func TestGate_Error_MissingPetition(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{Outcome: outcomeApprove})
	seedLawReference(spy, "law-z")
	spy.Laws["law-z"] = &flowv1.Law{Id: "law-z", Tier: flowv1.LawTier_LAW_TIER_FINDING}

	client := setupGateTest(t, spy)

	err := handleJudiciaryGate(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when petition is missing")
	}
}

// ---------------------------------------------------------------------------
// Error: invalid petition JSON
// ---------------------------------------------------------------------------

func TestGate_Error_InvalidPetitionJSON(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{Outcome: outcomeApprove})
	spy.Artefacts[artefactPetition] = []byte("broken json")
	seedLawReference(spy, "law-w")
	spy.Laws["law-w"] = &flowv1.Law{Id: "law-w", Tier: flowv1.LawTier_LAW_TIER_FINDING}

	client := setupGateTest(t, spy)

	err := handleJudiciaryGate(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when petition has invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// Error: law-reference missing
// ---------------------------------------------------------------------------

func TestGate_Error_MissingLawReference(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{Outcome: outcomeApprove})
	seedPetition(t, spy, petition{})
	// No law-reference seeded.

	client := setupGateTest(t, spy)

	err := handleJudiciaryGate(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when law-reference is missing")
	}
}

// ---------------------------------------------------------------------------
// Error: law-reference is empty/whitespace
// ---------------------------------------------------------------------------

func TestGate_Error_EmptyLawReference(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{Outcome: outcomeApprove})
	seedPetition(t, spy, petition{})
	spy.Artefacts[artefactLawReference] = []byte("  \n  ")

	client := setupGateTest(t, spy)

	err := handleJudiciaryGate(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when law-reference is empty/whitespace")
	}
}

// ---------------------------------------------------------------------------
// Error: GetLaw fails
// ---------------------------------------------------------------------------

func TestGate_Error_GetLawFails(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{Outcome: outcomeApprove})
	seedPetition(t, spy, petition{})
	seedLawReference(spy, "law-fail")
	spy.GetLawErr = status.Errorf(codes.Internal, "librarian down")

	client := setupGateTest(t, spy)

	err := handleJudiciaryGate(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when GetLaw fails")
	}
}

// ---------------------------------------------------------------------------
// Error: RouteToOutput fails
// ---------------------------------------------------------------------------

func TestGate_Error_RouteToOutputFails(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{Outcome: "reject"})
	seedPetition(t, spy, petition{
		Petition: petitionBody{Changes: []petitionChange{{Action: "create", Tier: 1}}},
	})
	seedLawReference(spy, "law-rf")
	spy.Laws["law-rf"] = &flowv1.Law{Id: "law-rf", Tier: flowv1.LawTier_LAW_TIER_FINDING}
	spy.RouteToOutputErr = status.Errorf(codes.Unavailable, "sidecar down")

	client := setupGateTest(t, spy)

	err := handleJudiciaryGate(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when RouteToOutput fails")
	}
}

// ---------------------------------------------------------------------------
// Error: WriteLaw fails during application
// ---------------------------------------------------------------------------

func TestGate_Error_WriteLawFails(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{Outcome: outcomeApprove})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Changes: []petitionChange{
				{Action: "create", Tier: 1, Goal: "fail law"},
			},
		},
	})
	seedLawReference(spy, "law-wf")
	spy.Laws["law-wf"] = &flowv1.Law{Id: "law-wf", Tier: flowv1.LawTier_LAW_TIER_FINDING}
	spy.WriteLawErr = status.Errorf(codes.Internal, "librarian write failed")

	client := setupGateTest(t, spy)

	err := handleJudiciaryGate(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when WriteLaw fails")
	}
}

// ---------------------------------------------------------------------------
// Error: RetireLaw fails during application
// ---------------------------------------------------------------------------

func TestGate_Error_RetireLawFails(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{Outcome: outcomeApprove})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Changes: []petitionChange{
				{Action: "retire", LawID: "retire-fail"},
			},
		},
	})
	seedLawReference(spy, "retire-fail")
	spy.Laws["retire-fail"] = &flowv1.Law{Id: "retire-fail", Tier: flowv1.LawTier_LAW_TIER_FINDING}
	spy.RetireLawErr = status.Errorf(codes.Internal, "retire failed")

	client := setupGateTest(t, spy)

	err := handleJudiciaryGate(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when RetireLaw fails")
	}
}

// ---------------------------------------------------------------------------
// Error: GetArtefact returns generic error
// ---------------------------------------------------------------------------

func TestGate_Error_GetArtefactFails(t *testing.T) {
	spy := newGateSpy()
	spy.GetArtefactErr = status.Errorf(codes.Internal, "archivist down")

	client := setupGateTest(t, spy)

	err := handleJudiciaryGate(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when GetArtefact fails")
	}
}

// ---------------------------------------------------------------------------
// Error: unknown petition action
// ---------------------------------------------------------------------------

func TestGate_Error_UnknownPetitionAction(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{Outcome: outcomeApprove})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Changes: []petitionChange{
				{Action: "unknown-action"},
			},
		},
	})
	seedLawReference(spy, "law-unk")
	spy.Laws["law-unk"] = &flowv1.Law{Id: "law-unk", Tier: flowv1.LawTier_LAW_TIER_FINDING}

	client := setupGateTest(t, spy)

	err := handleJudiciaryGate(context.Background(), client)
	if err == nil {
		t.Fatal("expected error for unknown petition action")
	}
}

// ---------------------------------------------------------------------------
// Error: demote GetLaw fails
// ---------------------------------------------------------------------------

func TestGate_Error_DemoteGetLawFails(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{Outcome: outcomeApprove})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Changes: []petitionChange{
				{Action: "demote", LawID: "demote-fail", Tier: 1},
			},
		},
	})
	seedLawReference(spy, "ref-law")
	spy.Laws["ref-law"] = &flowv1.Law{Id: "ref-law", Tier: flowv1.LawTier_LAW_TIER_FINDING}
	// Note: "demote-fail" is NOT in Laws, so GetLaw for demote will fail.

	client := setupGateTest(t, spy)

	err := handleJudiciaryGate(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when GetLaw for demote target fails")
	}
}

// ---------------------------------------------------------------------------
// Feedback read failure is treated as resolved (graceful degradation)
// ---------------------------------------------------------------------------

func TestGate_FeedbackReadFailure_TreatedAsResolved(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{Outcome: outcomeApprove})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Changes: []petitionChange{
				{Action: "create", Tier: 1, Goal: "feedback fail graceful"},
			},
		},
	})
	seedLawReference(spy, "law-fbf")
	spy.Laws["law-fbf"] = &flowv1.Law{Id: "law-fbf", Tier: flowv1.LawTier_LAW_TIER_FINDING}
	spy.GetFeedbackErr = status.Errorf(codes.Internal, "archivist feedback down")

	client := setupGateTest(t, spy)

	// Should succeed despite feedback read failure (treated as "no feedback").
	if err := handleJudiciaryGate(context.Background(), client); err != nil {
		t.Fatalf("handleJudiciaryGate: %v", err)
	}

	assertCompleted(t, spy)
}

// ---------------------------------------------------------------------------
// No feedback at all is treated as resolved
// ---------------------------------------------------------------------------

func TestGate_NoFeedback_TreatedAsResolved(t *testing.T) {
	spy := newGateSpy()
	seedDeliberationResult(t, spy, deliberationResult{Outcome: outcomeApprove})
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Changes: []petitionChange{
				{Action: "create", Tier: 2, Goal: "no feedback law"},
			},
		},
	})
	seedLawReference(spy, "law-nf")
	spy.Laws["law-nf"] = &flowv1.Law{Id: "law-nf", Tier: flowv1.LawTier_LAW_TIER_RULING}

	client := setupGateTest(t, spy)

	if err := handleJudiciaryGate(context.Background(), client); err != nil {
		t.Fatalf("handleJudiciaryGate: %v", err)
	}

	assertCompleted(t, spy)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func seedDeliberationResult(t *testing.T, spy *gateSpy, result deliberationResult) {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal deliberation result: %v", err)
	}
	spy.Artefacts[artefactDeliberationResult] = data
}

func seedPetition(t *testing.T, spy *gateSpy, pet petition) {
	t.Helper()
	data, err := json.Marshal(pet)
	if err != nil {
		t.Fatalf("marshal petition: %v", err)
	}
	spy.Artefacts[artefactPetition] = data
}

func seedLawReference(spy *gateSpy, lawID string) {
	spy.Artefacts[artefactLawReference] = []byte(lawID)
}
