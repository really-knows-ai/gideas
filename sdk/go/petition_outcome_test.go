package flow

import (
	"context"
	"io"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// --- Slice 12.6.2 tests: petition-outcome event helpers ---

func TestIsPetitionAccepted_True(t *testing.T) {
	evt := &flowv1.PetitionOutcomeEvent{
		PetitionId:     "pet-1",
		Outcome:        flowv1.PetitionOutcome_PETITION_OUTCOME_ACCEPTED,
		PublishedLawId: "new-law-1",
	}
	if !IsPetitionAccepted(evt) {
		t.Fatal("expected IsPetitionAccepted to return true for ACCEPTED event")
	}
}

func TestIsPetitionAccepted_False(t *testing.T) {
	evt := &flowv1.PetitionOutcomeEvent{
		PetitionId: "pet-2",
		Outcome:    flowv1.PetitionOutcome_PETITION_OUTCOME_REJECTED,
	}
	if IsPetitionAccepted(evt) {
		t.Fatal("expected IsPetitionAccepted to return false for REJECTED event")
	}
}

func TestIsPetitionAccepted_Nil(t *testing.T) {
	if IsPetitionAccepted(nil) {
		t.Fatal("expected IsPetitionAccepted to return false for nil event")
	}
}

func TestIsPetitionAccepted_Unspecified(t *testing.T) {
	evt := &flowv1.PetitionOutcomeEvent{
		PetitionId: "pet-3",
		Outcome:    flowv1.PetitionOutcome_PETITION_OUTCOME_UNSPECIFIED,
	}
	if IsPetitionAccepted(evt) {
		t.Fatal("expected IsPetitionAccepted to return false for UNSPECIFIED outcome")
	}
}

func TestIsPetitionRejected_True(t *testing.T) {
	evt := &flowv1.PetitionOutcomeEvent{
		PetitionId: "pet-2",
		Outcome:    flowv1.PetitionOutcome_PETITION_OUTCOME_REJECTED,
		Rejection: &flowv1.PublicationRejection{
			Reason:          flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_CONFLICT,
			RemediationText: "Conflicts with existing law",
		},
	}
	if !IsPetitionRejected(evt) {
		t.Fatal("expected IsPetitionRejected to return true for REJECTED event")
	}
}

func TestIsPetitionRejected_False(t *testing.T) {
	evt := &flowv1.PetitionOutcomeEvent{
		PetitionId: "pet-1",
		Outcome:    flowv1.PetitionOutcome_PETITION_OUTCOME_ACCEPTED,
	}
	if IsPetitionRejected(evt) {
		t.Fatal("expected IsPetitionRejected to return false for ACCEPTED event")
	}
}

func TestIsPetitionRejected_Nil(t *testing.T) {
	if IsPetitionRejected(nil) {
		t.Fatal("expected IsPetitionRejected to return false for nil event")
	}
}

func TestIsPetitionRejected_Unspecified(t *testing.T) {
	evt := &flowv1.PetitionOutcomeEvent{
		PetitionId: "pet-3",
		Outcome:    flowv1.PetitionOutcome_PETITION_OUTCOME_UNSPECIFIED,
	}
	if IsPetitionRejected(evt) {
		t.Fatal("expected IsPetitionRejected to return false for UNSPECIFIED outcome")
	}
}

func TestPetitionOutcomeStream_DeserializesFromFederationStream(t *testing.T) {
	spy := &federationSpyServer{
		petitionOutcomeEvents: []*flowv1.PetitionOutcomeEvent{
			{
				PetitionId:     "pet-accepted",
				Outcome:        flowv1.PetitionOutcome_PETITION_OUTCOME_ACCEPTED,
				PublishedLawId: "law-from-petition",
			},
			{
				PetitionId: "pet-rejected",
				Outcome:    flowv1.PetitionOutcome_PETITION_OUTCOME_REJECTED,
				Rejection: &flowv1.PublicationRejection{
					Reason:            flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_OUT_OF_SCOPE,
					ConflictingLawIds: []string{"conflict-1"},
					RemediationText:   "Scope mismatch",
				},
			},
		},
	}
	client := setupFederationTestClient(t, spy)

	stream, err := client.SubscribePetitionOutcomes(context.Background(), "watcher-flow")
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes() returned error: %v", err)
	}

	// Read and verify first event via helpers.
	evt1, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv() first event returned error: %v", err)
	}
	if !IsPetitionAccepted(evt1) {
		t.Fatal("expected first event to be accepted")
	}
	if IsPetitionRejected(evt1) {
		t.Fatal("expected first event NOT to be rejected")
	}
	if evt1.GetPetitionId() != "pet-accepted" {
		t.Fatalf("expected petition_id pet-accepted, got %q", evt1.GetPetitionId())
	}
	if evt1.GetPublishedLawId() != "law-from-petition" {
		t.Fatalf("expected published_law_id law-from-petition, got %q", evt1.GetPublishedLawId())
	}

	// Read and verify second event via helpers.
	evt2, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv() second event returned error: %v", err)
	}
	if !IsPetitionRejected(evt2) {
		t.Fatal("expected second event to be rejected")
	}
	if IsPetitionAccepted(evt2) {
		t.Fatal("expected second event NOT to be accepted")
	}
	if evt2.GetPetitionId() != "pet-rejected" {
		t.Fatalf("expected petition_id pet-rejected, got %q", evt2.GetPetitionId())
	}
	if evt2.GetRejection().GetReason() != flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_OUT_OF_SCOPE {
		t.Fatalf("expected OUT_OF_SCOPE rejection reason, got %v", evt2.GetRejection().GetReason())
	}
	if evt2.GetRejection().GetRemediationText() != "Scope mismatch" {
		t.Fatalf("expected remediation text 'Scope mismatch', got %q", evt2.GetRejection().GetRemediationText())
	}

	// Expect EOF.
	_, err = stream.Recv()
	if err != io.EOF {
		t.Fatalf("expected EOF after all events, got %v", err)
	}
}
