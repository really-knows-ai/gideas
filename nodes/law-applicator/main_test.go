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
// Helpers
// ---------------------------------------------------------------------------

// seedPetition marshals a petition and stores it in the spy's artefact map.
func seedPetition(t *testing.T, spy *applicatorSpy, pet petition) {
	t.Helper()
	data, err := json.Marshal(pet)
	if err != nil {
		t.Fatalf("marshal petition: %v", err)
	}
	spy.Artefacts[artefactPetition] = data
}

// ---------------------------------------------------------------------------
// Happy Path: single create change
// ---------------------------------------------------------------------------

func TestApplicator_Create_WritesLawAndCompletes(t *testing.T) {
	spy := newApplicatorSpy()
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
	spy.WriteLawResponses = []*flowv1.WriteLawResponse{
		{LawId: "created-law-1", VersionHash: "v1"},
	}

	client := setupApplicatorTest(t, spy)

	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator: %v", err)
	}

	assertCompleted(t, spy)

	// Verify WriteLaw was called with correct law.
	if len(spy.WrittenLaws) != 1 {
		t.Fatalf("expected 1 WriteLaw call, got %d", len(spy.WrittenLaws))
	}
	if spy.WrittenLaws[0].GetGoal() != "enforce naming conventions" {
		t.Errorf("WriteLaw goal = %q, want %q", spy.WrittenLaws[0].GetGoal(), "enforce naming conventions")
	}

	// Verify approval stamp.
	stampData := assertStampStored(t, spy)
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
	if stamp.LawResults[0].LawID != "created-law-1" {
		t.Errorf("law result ID = %q, want %q", stamp.LawResults[0].LawID, "created-law-1")
	}
	if stamp.LawResults[0].VersionHash != "v1" {
		t.Errorf("law result version = %q, want %q", stamp.LawResults[0].VersionHash, "v1")
	}
}

// ---------------------------------------------------------------------------
// Happy Path: single retire change
// ---------------------------------------------------------------------------

func TestApplicator_Retire_RetiresLawAndCompletes(t *testing.T) {
	spy := newApplicatorSpy()
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

	client := setupApplicatorTest(t, spy)

	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator: %v", err)
	}

	assertCompleted(t, spy)

	if len(spy.RetiredLawIDs) != 1 || spy.RetiredLawIDs[0] != "old-law-42" {
		t.Errorf("expected RetireLaw('old-law-42'), got %v", spy.RetiredLawIDs)
	}

	// Verify stamp records the retire action.
	stampData := assertStampStored(t, spy)
	var stamp approvalStamp
	if err := json.Unmarshal(stampData, &stamp); err != nil {
		t.Fatalf("unmarshal stamp: %v", err)
	}
	if len(stamp.LawResults) != 1 || stamp.LawResults[0].Action != "retire" {
		t.Errorf("expected retire action in stamp, got %v", stamp.LawResults)
	}
	if stamp.LawResults[0].LawID != "old-law-42" {
		t.Errorf("stamp law ID = %q, want %q", stamp.LawResults[0].LawID, "old-law-42")
	}
}

// ---------------------------------------------------------------------------
// Happy Path: single demote change
// ---------------------------------------------------------------------------

func TestApplicator_Demote_FetchesAndWritesLawAndCompletes(t *testing.T) {
	spy := newApplicatorSpy()
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
	spy.LawsByID["demote-law-7"] = &flowv1.Law{
		Id:   "demote-law-7",
		Goal: "original goal",
		Tier: flowv1.LawTier_LAW_TIER_RULING,
	}
	spy.WriteLawResponses = []*flowv1.WriteLawResponse{
		{LawId: "demote-law-7", VersionHash: "v2"},
	}

	client := setupApplicatorTest(t, spy)

	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator: %v", err)
	}

	assertCompleted(t, spy)

	// Verify GetLaw was called for the demote target.
	if len(spy.RequestedLawIDs) != 1 || spy.RequestedLawIDs[0] != "demote-law-7" {
		t.Errorf("expected GetLaw('demote-law-7'), got %v", spy.RequestedLawIDs)
	}

	// Verify WriteLaw was called with updated tier.
	if len(spy.WrittenLaws) != 1 {
		t.Fatalf("expected 1 WriteLaw call (demote), got %d", len(spy.WrittenLaws))
	}
	if spy.WrittenLaws[0].GetTier() != flowv1.LawTier_LAW_TIER_FINDING {
		t.Errorf("demoted law tier = %v, want FINDING", spy.WrittenLaws[0].GetTier())
	}
	// Original goal should be preserved.
	if spy.WrittenLaws[0].GetGoal() != "original goal" {
		t.Errorf("demoted law goal = %q, want %q", spy.WrittenLaws[0].GetGoal(), "original goal")
	}

	// Verify stamp.
	stampData := assertStampStored(t, spy)
	var stamp approvalStamp
	if err := json.Unmarshal(stampData, &stamp); err != nil {
		t.Fatalf("unmarshal stamp: %v", err)
	}
	if len(stamp.LawResults) != 1 || stamp.LawResults[0].Action != "demote" {
		t.Errorf("expected demote action in stamp, got %v", stamp.LawResults)
	}
	if stamp.LawResults[0].VersionHash != "v2" {
		t.Errorf("stamp version = %q, want %q", stamp.LawResults[0].VersionHash, "v2")
	}
}

// ---------------------------------------------------------------------------
// Happy Path: multiple changes in one petition
// ---------------------------------------------------------------------------

func TestApplicator_MultipleChanges_AllApplied(t *testing.T) {
	spy := newApplicatorSpy()
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

	client := setupApplicatorTest(t, spy)

	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator: %v", err)
	}

	assertCompleted(t, spy)

	if len(spy.WrittenLaws) != 1 {
		t.Fatalf("expected 1 WriteLaw call, got %d", len(spy.WrittenLaws))
	}
	if len(spy.RetiredLawIDs) != 1 || spy.RetiredLawIDs[0] != "retiring-law" {
		t.Fatalf("expected 1 RetireLaw call for 'retiring-law', got %v", spy.RetiredLawIDs)
	}

	stampData := assertStampStored(t, spy)
	var stamp approvalStamp
	if err := json.Unmarshal(stampData, &stamp); err != nil {
		t.Fatalf("unmarshal stamp: %v", err)
	}
	if len(stamp.LawResults) != 2 {
		t.Errorf("expected 2 law results, got %d", len(stamp.LawResults))
	}
}

// ---------------------------------------------------------------------------
// Happy Path: create with multiple representations
// ---------------------------------------------------------------------------

func TestApplicator_Create_RepresentationsPassedToLibrarian(t *testing.T) {
	spy := newApplicatorSpy()
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

	client := setupApplicatorTest(t, spy)

	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator: %v", err)
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
	if reps[0].GetContent() != "# Rule" {
		t.Errorf("rep[0].Content = %q, want %q", reps[0].GetContent(), "# Rule")
	}
	if reps[1].GetType() != "application/smt-lib" {
		t.Errorf("rep[1].Type = %q, want %q", reps[1].GetType(), "application/smt-lib")
	}
}

// ---------------------------------------------------------------------------
// Happy Path: empty changes list
// ---------------------------------------------------------------------------

func TestApplicator_EmptyChanges_StampsAndCompletes(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Context: petitionContext{Trigger: "deadlock-resolution"},
			Changes: []petitionChange{},
		},
	})

	client := setupApplicatorTest(t, spy)

	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator: %v", err)
	}

	assertCompleted(t, spy)

	stampData := assertStampStored(t, spy)
	var stamp approvalStamp
	if err := json.Unmarshal(stampData, &stamp); err != nil {
		t.Fatalf("unmarshal stamp: %v", err)
	}
	if !stamp.Applied {
		t.Error("expected stamp.Applied = true")
	}
	if len(stamp.LawResults) != 0 {
		t.Errorf("expected 0 law results for empty changes, got %d", len(stamp.LawResults))
	}
}

// ---------------------------------------------------------------------------
// Happy Path: heartbeat is called
// ---------------------------------------------------------------------------

func TestApplicator_HeartbeatCalled(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Changes: []petitionChange{},
		},
	})

	client := setupApplicatorTest(t, spy)

	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator: %v", err)
	}

	if spy.HeartbeatCount == 0 {
		t.Error("expected at least one Heartbeat call")
	}
}

// ---------------------------------------------------------------------------
// Error: petition artefact missing
// ---------------------------------------------------------------------------

func TestApplicator_Error_MissingPetition(t *testing.T) {
	spy := newApplicatorSpy()
	// No petition seeded.

	client := setupApplicatorTest(t, spy)

	err := handleLawApplicator(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when petition is missing")
	}
	assertNotCompleted(t, spy)
	assertNoStampStored(t, spy)
}

// ---------------------------------------------------------------------------
// Error: invalid petition JSON
// ---------------------------------------------------------------------------

func TestApplicator_Error_InvalidPetitionJSON(t *testing.T) {
	spy := newApplicatorSpy()
	spy.Artefacts[artefactPetition] = []byte("broken json{{{")

	client := setupApplicatorTest(t, spy)

	err := handleLawApplicator(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when petition has invalid JSON")
	}
	assertNotCompleted(t, spy)
	assertNoStampStored(t, spy)
}

// ---------------------------------------------------------------------------
// Error: GetArtefact returns generic error
// ---------------------------------------------------------------------------

func TestApplicator_Error_GetArtefactFails(t *testing.T) {
	spy := newApplicatorSpy()
	spy.GetArtefactErr = status.Errorf(codes.Internal, "archivist down")

	client := setupApplicatorTest(t, spy)

	err := handleLawApplicator(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when GetArtefact fails")
	}
	assertNotCompleted(t, spy)
}

// ---------------------------------------------------------------------------
// Error: unknown petition action
// ---------------------------------------------------------------------------

func TestApplicator_Error_UnknownPetitionAction(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Changes: []petitionChange{
				{Action: "unknown-action"},
			},
		},
	})

	client := setupApplicatorTest(t, spy)

	err := handleLawApplicator(context.Background(), client)
	if err == nil {
		t.Fatal("expected error for unknown petition action")
	}
	assertNotCompleted(t, spy)
	assertNoStampStored(t, spy)
}

// ---------------------------------------------------------------------------
// Error: WriteLaw fails during create
// ---------------------------------------------------------------------------

func TestApplicator_Error_WriteLawFails(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Changes: []petitionChange{
				{Action: "create", Tier: 1, Goal: "fail law"},
			},
		},
	})
	spy.WriteLawErr = status.Errorf(codes.Internal, "librarian write failed")

	client := setupApplicatorTest(t, spy)

	err := handleLawApplicator(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when WriteLaw fails")
	}
	assertNotCompleted(t, spy)
	assertNoStampStored(t, spy)
}

// ---------------------------------------------------------------------------
// Error: RetireLaw fails
// ---------------------------------------------------------------------------

func TestApplicator_Error_RetireLawFails(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Changes: []petitionChange{
				{Action: "retire", LawID: "retire-fail"},
			},
		},
	})
	spy.RetireLawErr = status.Errorf(codes.Internal, "retire failed")

	client := setupApplicatorTest(t, spy)

	err := handleLawApplicator(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when RetireLaw fails")
	}
	assertNotCompleted(t, spy)
	assertNoStampStored(t, spy)
}

// ---------------------------------------------------------------------------
// Error: GetLaw fails during demote
// ---------------------------------------------------------------------------

func TestApplicator_Error_DemoteGetLawFails(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Changes: []petitionChange{
				{Action: "demote", LawID: "demote-fail", Tier: 1},
			},
		},
	})
	// "demote-fail" is NOT in LawsByID, so GetLaw will return NotFound.

	client := setupApplicatorTest(t, spy)

	err := handleLawApplicator(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when GetLaw for demote target fails")
	}
	assertNotCompleted(t, spy)
	assertNoStampStored(t, spy)
}

// ---------------------------------------------------------------------------
// Error: WriteLaw fails during demote (after GetLaw succeeds)
// ---------------------------------------------------------------------------

func TestApplicator_Error_DemoteWriteLawFails(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Changes: []petitionChange{
				{Action: "demote", LawID: "demote-law", Tier: 1},
			},
		},
	})
	spy.LawsByID["demote-law"] = &flowv1.Law{
		Id:   "demote-law",
		Goal: "original",
		Tier: flowv1.LawTier_LAW_TIER_RULING,
	}
	spy.WriteLawErr = status.Errorf(codes.Internal, "write failed on demote")

	client := setupApplicatorTest(t, spy)

	err := handleLawApplicator(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when WriteLaw fails during demote")
	}
	assertNotCompleted(t, spy)
	assertNoStampStored(t, spy)
}

// ---------------------------------------------------------------------------
// Error: StoreArtefact fails (stamp store)
// ---------------------------------------------------------------------------

func TestApplicator_Error_StoreArtefactFails(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Changes: []petitionChange{
				{Action: "create", Tier: 1, Goal: "store fail"},
			},
		},
	})
	spy.StoreArtefactErr = status.Errorf(codes.Internal, "archivist store down")

	client := setupApplicatorTest(t, spy)

	err := handleLawApplicator(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when StoreArtefact fails")
	}
	// WriteLaw should have succeeded, but completion should not happen.
	if len(spy.WrittenLaws) != 1 {
		t.Errorf("expected 1 WriteLaw call before store failure, got %d", len(spy.WrittenLaws))
	}
	assertNotCompleted(t, spy)
}

// ---------------------------------------------------------------------------
// Error: Complete fails
// ---------------------------------------------------------------------------

func TestApplicator_Error_CompleteFails(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Changes: []petitionChange{
				{Action: "create", Tier: 1, Goal: "complete fail"},
			},
		},
	})
	spy.CompleteErr = status.Errorf(codes.Unavailable, "sidecar down")

	client := setupApplicatorTest(t, spy)

	err := handleLawApplicator(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when Complete fails")
	}
}

// ---------------------------------------------------------------------------
// Create: AppliesTo and Tier are passed to Librarian
// ---------------------------------------------------------------------------

func TestApplicator_Create_AppliesToAndTierPassedToLibrarian(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			Changes: []petitionChange{
				{
					Action:    "create",
					Tier:      2,
					Goal:      "tier test",
					AppliesTo: []string{"*.go", "*.ts"},
				},
			},
		},
	})

	client := setupApplicatorTest(t, spy)

	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator: %v", err)
	}

	assertCompleted(t, spy)

	if len(spy.WrittenLaws) != 1 {
		t.Fatalf("expected 1 WriteLaw, got %d", len(spy.WrittenLaws))
	}
	law := spy.WrittenLaws[0]
	if law.GetTier() != flowv1.LawTier_LAW_TIER_RULING {
		t.Errorf("law tier = %v, want RULING (2)", law.GetTier())
	}
	if len(law.GetAppliesTo()) != 2 {
		t.Fatalf("expected 2 AppliesTo entries, got %d", len(law.GetAppliesTo()))
	}
	if law.GetAppliesTo()[0] != "*.go" || law.GetAppliesTo()[1] != "*.ts" {
		t.Errorf("AppliesTo = %v, want [*.go *.ts]", law.GetAppliesTo())
	}
}

// ===========================================================================
// Slice 13.11.1 — Tier detection
// ===========================================================================

// ---------------------------------------------------------------------------
// T1-2 petition -> Complete (regression guard)
// ---------------------------------------------------------------------------

func TestApplicator_T1Petition_Completes(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			PetitionID: "pet-t1",
			Context:    petitionContext{Trigger: "deadlock-resolution"},
			Changes: []petitionChange{
				{Action: "create", Tier: 1, Goal: "tier-1 law", AppliesTo: []string{"*.go"}},
			},
		},
	})

	client := setupApplicatorTest(t, spy)

	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator: %v", err)
	}

	assertCompleted(t, spy)
	assertNotRouted(t, spy)
}

func TestApplicator_T2Petition_Completes(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			PetitionID: "pet-t2",
			Context:    petitionContext{Trigger: "deadlock-resolution"},
			Changes: []petitionChange{
				{Action: "create", Tier: 2, Goal: "tier-2 law", AppliesTo: []string{"*.go"}},
			},
		},
	})

	client := setupApplicatorTest(t, spy)

	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator: %v", err)
	}

	assertCompleted(t, spy)
	assertNotRouted(t, spy)
}

// ---------------------------------------------------------------------------
// T3 petition -> Complete (regression guard)
// ---------------------------------------------------------------------------

func TestApplicator_T3Petition_Completes(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			PetitionID: "pet-t3",
			Context:    petitionContext{Trigger: "friction-hearing"},
			Changes: []petitionChange{
				{Action: "create", Tier: 3, Goal: "tier-3 local statute", AppliesTo: []string{"*.go"}},
			},
		},
	})

	client := setupApplicatorTest(t, spy)

	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator: %v", err)
	}

	assertCompleted(t, spy)
	assertNotRouted(t, spy)
}

// ---------------------------------------------------------------------------
// T4 petition -> does NOT Complete, routes to Embassy
// ---------------------------------------------------------------------------

func TestApplicator_T4Petition_DoesNotComplete(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			PetitionID: "pet-t4",
			Context:    petitionContext{Trigger: "friction-hearing"},
			Changes: []petitionChange{
				{Action: "create", Tier: 4, Goal: "state statute", AppliesTo: []string{"*.go"}},
			},
		},
	})

	client := setupApplicatorTest(t, spy)

	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator: %v", err)
	}

	assertNotCompleted(t, spy)
	assertRoutedTo(t, spy, "embassy")
}

// ---------------------------------------------------------------------------
// T5 petition -> does NOT Complete, routes to Embassy
// ---------------------------------------------------------------------------

func TestApplicator_T5Petition_DoesNotComplete(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			PetitionID: "pet-t5",
			Context:    petitionContext{Trigger: "friction-hearing"},
			Changes: []petitionChange{
				{Action: "create", Tier: 5, Goal: "federal statute", AppliesTo: []string{"*.go"}},
			},
		},
	})

	client := setupApplicatorTest(t, spy)

	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator: %v", err)
	}

	assertNotCompleted(t, spy)
	assertRoutedTo(t, spy, "embassy")
}

// ---------------------------------------------------------------------------
// Mixed tiers: T2 create + T4 retire -> T4-5 path wins (max tier)
// ---------------------------------------------------------------------------

func TestApplicator_MixedTiers_MaxTierWins(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			PetitionID: "pet-mixed",
			Context:    petitionContext{Trigger: "friction-hearing"},
			Changes: []petitionChange{
				{Action: "create", Tier: 2, Goal: "tier-2 law", AppliesTo: []string{"*.go"}},
				{Action: "retire", Tier: 4, LawID: "old-state-law"},
			},
		},
	})

	client := setupApplicatorTest(t, spy)

	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator: %v", err)
	}

	assertNotCompleted(t, spy)
	assertRoutedTo(t, spy, "embassy")
}

// ===========================================================================
// Slice 13.11.2 — Create dispute record on T4-5 path
// ===========================================================================

// ---------------------------------------------------------------------------
// T4-5 petition: creates dispute record with petition_id and cited_law_ids
// ---------------------------------------------------------------------------

func TestApplicator_T4_CreatesDisputeRecord(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			PetitionID: "dispute-pet-1",
			Context:    petitionContext{Trigger: "friction-hearing"},
			Changes: []petitionChange{
				{Action: "retire", Tier: 4, LawID: "law-to-retire"},
				{Action: "create", Tier: 5, Goal: "new federal law", AppliesTo: []string{"*.go"}},
			},
		},
	})
	spy.WriteLawResponses = []*flowv1.WriteLawResponse{
		{LawId: "new-federal-law-id", VersionHash: "v1"},
	}

	client := setupApplicatorTest(t, spy)

	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator: %v", err)
	}

	// Verify CreateDisputeRecord was called.
	if len(spy.DisputeRecordCalls) != 1 {
		t.Fatalf("expected 1 CreateDisputeRecord call, got %d", len(spy.DisputeRecordCalls))
	}
	call := spy.DisputeRecordCalls[0]
	if call.PetitionID != "dispute-pet-1" {
		t.Errorf("dispute record petition_id = %q, want %q", call.PetitionID, "dispute-pet-1")
	}
	// cited_law_ids should include the retire target and the newly created law.
	wantIDs := map[string]bool{"law-to-retire": true, "new-federal-law-id": true}
	gotIDs := make(map[string]bool, len(call.CitedLawIDs))
	for _, id := range call.CitedLawIDs {
		gotIDs[id] = true
	}
	for want := range wantIDs {
		if !gotIDs[want] {
			t.Errorf("cited_law_ids missing %q, got %v", want, call.CitedLawIDs)
		}
	}
}

// ---------------------------------------------------------------------------
// petition_id is read from petition.petition_id
// ---------------------------------------------------------------------------

func TestApplicator_T4_PetitionIDFromPetition(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			PetitionID: "my-unique-petition-id",
			Context:    petitionContext{Trigger: "friction-hearing"},
			Changes: []petitionChange{
				{Action: "create", Tier: 4, Goal: "state law", AppliesTo: []string{"*.go"}},
			},
		},
	})

	client := setupApplicatorTest(t, spy)

	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator: %v", err)
	}

	if len(spy.DisputeRecordCalls) != 1 {
		t.Fatalf("expected 1 CreateDisputeRecord call, got %d", len(spy.DisputeRecordCalls))
	}
	if spy.DisputeRecordCalls[0].PetitionID != "my-unique-petition-id" {
		t.Errorf("petition_id = %q, want %q", spy.DisputeRecordCalls[0].PetitionID, "my-unique-petition-id")
	}
}

// ---------------------------------------------------------------------------
// cited_law_ids: extracted from changes (existing law_id fields + new create IDs)
// ---------------------------------------------------------------------------

func TestApplicator_T4_CitedLawIDsExtracted(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			PetitionID: "cite-test",
			Context:    petitionContext{Trigger: "friction-hearing"},
			Changes: []petitionChange{
				{Action: "retire", Tier: 5, LawID: "retired-law-a"},
				{Action: "retire", Tier: 5, LawID: "retired-law-b"},
			},
		},
	})

	client := setupApplicatorTest(t, spy)

	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator: %v", err)
	}

	if len(spy.DisputeRecordCalls) != 1 {
		t.Fatalf("expected 1 CreateDisputeRecord call, got %d", len(spy.DisputeRecordCalls))
	}
	ids := spy.DisputeRecordCalls[0].CitedLawIDs
	wantIDs := map[string]bool{"retired-law-a": true, "retired-law-b": true}
	gotIDs := make(map[string]bool, len(ids))
	for _, id := range ids {
		gotIDs[id] = true
	}
	for want := range wantIDs {
		if !gotIDs[want] {
			t.Errorf("cited_law_ids missing %q, got %v", want, ids)
		}
	}
}

// ---------------------------------------------------------------------------
// CreateDisputeRecord fails with AlreadyExists -> log warning, continue
// ---------------------------------------------------------------------------

func TestApplicator_T4_CreateDisputeAlreadyExists_Continues(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			PetitionID: "already-exists-pet",
			Context:    petitionContext{Trigger: "friction-hearing"},
			Changes: []petitionChange{
				{Action: "create", Tier: 4, Goal: "state law", AppliesTo: []string{"*.go"}},
			},
		},
	})
	spy.CreateDisputeRecordErr = status.Errorf(codes.AlreadyExists, "dispute already exists")

	client := setupApplicatorTest(t, spy)

	// Should NOT return error.
	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator should not fail on AlreadyExists: %v", err)
	}

	assertNotCompleted(t, spy)
	assertRoutedTo(t, spy, "embassy")
}

// ---------------------------------------------------------------------------
// CreateDisputeRecord fails with other error -> return error
// ---------------------------------------------------------------------------

func TestApplicator_T4_CreateDisputeOtherError_ReturnsError(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			PetitionID: "error-pet",
			Context:    petitionContext{Trigger: "friction-hearing"},
			Changes: []petitionChange{
				{Action: "create", Tier: 4, Goal: "state law", AppliesTo: []string{"*.go"}},
			},
		},
	})
	spy.CreateDisputeRecordErr = status.Errorf(codes.Internal, "librarian crashed")

	client := setupApplicatorTest(t, spy)

	err := handleLawApplicator(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when CreateDisputeRecord fails with Internal")
	}
}

// ---------------------------------------------------------------------------
// T1-2 petition: no dispute record created (regression)
// ---------------------------------------------------------------------------

func TestApplicator_T2_NoDisputeRecord(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			PetitionID: "low-tier-pet",
			Context:    petitionContext{Trigger: "deadlock-resolution"},
			Changes: []petitionChange{
				{Action: "create", Tier: 2, Goal: "tier-2 law", AppliesTo: []string{"*.go"}},
			},
		},
	})

	client := setupApplicatorTest(t, spy)

	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator: %v", err)
	}

	assertCompleted(t, spy)
	if len(spy.DisputeRecordCalls) != 0 {
		t.Errorf("expected no CreateDisputeRecord calls for T2, got %d", len(spy.DisputeRecordCalls))
	}
}

// ===========================================================================
// Slice 13.11.3 — Route to Embassy on T4-5 path
// ===========================================================================

// ---------------------------------------------------------------------------
// T4-5 petition: after dispute record, routes to "embassy"
// ---------------------------------------------------------------------------

func TestApplicator_T4_RoutesToEmbassy(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			PetitionID: "route-test-pet",
			Context:    petitionContext{Trigger: "friction-hearing"},
			Changes: []petitionChange{
				{Action: "create", Tier: 4, Goal: "state law", AppliesTo: []string{"*.go"}},
			},
		},
	})

	client := setupApplicatorTest(t, spy)

	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator: %v", err)
	}

	assertNotCompleted(t, spy)
	assertRoutedTo(t, spy, "embassy")
	assertStampStored(t, spy) // stamp is still stored before routing
}

// ---------------------------------------------------------------------------
// T3 petition: still calls Complete, not Route (regression)
// ---------------------------------------------------------------------------

func TestApplicator_T3_CompletesNotRoutes(t *testing.T) {
	spy := newApplicatorSpy()
	seedPetition(t, spy, petition{
		Petition: petitionBody{
			PetitionID: "t3-complete-pet",
			Context:    petitionContext{Trigger: "friction-hearing"},
			Changes: []petitionChange{
				{Action: "create", Tier: 3, Goal: "local statute", AppliesTo: []string{"*.go"}},
			},
		},
	})

	client := setupApplicatorTest(t, spy)

	if err := handleLawApplicator(context.Background(), client); err != nil {
		t.Fatalf("handleLawApplicator: %v", err)
	}

	assertCompleted(t, spy)
	assertNotRouted(t, spy)
	if len(spy.DisputeRecordCalls) != 0 {
		t.Errorf("expected no CreateDisputeRecord calls for T3, got %d", len(spy.DisputeRecordCalls))
	}
}
