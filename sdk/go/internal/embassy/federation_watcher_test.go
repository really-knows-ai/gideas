package embassy

import (
	"io"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// --- LawUpdateWatcher tests ---

func TestFederationClient_SubscribeLawUpdates_RecvEventsAndStop(t *testing.T) {
	spy := &federationSpyServer{
		lawUpdateEvents: []*flowv1.PublishedLawEvent{
			{
				Law:                   &flowv1.Law{Id: "pub-law-1"},
				MaterialisationTier:   flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION,
				PetitionId:            "pet-1",
				PublisherFlowIdentity: "authority-1",
			},
			{
				Law:                   &flowv1.Law{Id: "pub-law-2"},
				MaterialisationTier:   flowv1.LawTier_LAW_TIER_FEDERAL_ACCORD,
				PublisherFlowIdentity: "authority-2",
			},
		},
	}
	client := setupFederationTestClient(t, spy)

	watcher, err := client.SubscribeLawUpdates("subscriber-flow-1")
	if err != nil {
		t.Fatalf("SubscribeLawUpdates() returned error: %v", err)
	}

	// Read first event.
	evt1, err := watcher.Recv()
	if err != nil {
		t.Fatalf("Recv() first event returned error: %v", err)
	}
	if evt1.GetLaw().GetId() != "pub-law-1" {
		t.Fatalf("expected first law ID pub-law-1, got %q", evt1.GetLaw().GetId())
	}

	// Read second event.
	evt2, err := watcher.Recv()
	if err != nil {
		t.Fatalf("Recv() second event returned error: %v", err)
	}
	if evt2.GetLaw().GetId() != "pub-law-2" {
		t.Fatalf("expected second law ID pub-law-2, got %q", evt2.GetLaw().GetId())
	}

	// Stop and verify subsequent Recv returns error.
	watcher.Stop()
	_, err = watcher.Recv()
	if err == nil {
		t.Fatal("expected error from Recv() after Stop(), got nil")
	}

	if spy.lastSubscribeLawUpdates.GetSubscriberFlowIdentity() != "subscriber-flow-1" {
		t.Fatalf("expected subscriber identity subscriber-flow-1, got %q",
			spy.lastSubscribeLawUpdates.GetSubscriberFlowIdentity())
	}
}

func TestFederationClient_SubscribeLawUpdates_RecvThenStop(t *testing.T) {
	spy := &federationSpyServer{
		lawUpdateEvents: []*flowv1.PublishedLawEvent{
			{Law: &flowv1.Law{Id: "pub-law-1"}},
		},
	}
	client := setupFederationTestClient(t, spy)

	watcher, err := client.SubscribeLawUpdates("subscriber-flow-1")
	if err != nil {
		t.Fatalf("SubscribeLawUpdates() returned error: %v", err)
	}

	// Read one event.
	evt, err := watcher.Recv()
	if err != nil {
		t.Fatalf("Recv() first event returned error: %v", err)
	}
	if evt.GetLaw().GetId() != "pub-law-1" {
		t.Fatalf("expected law ID pub-law-1, got %q", evt.GetLaw().GetId())
	}

	// Stop must not panic.
	watcher.Stop()
}

func TestFederationClient_SubscribeLawUpdates_NoConnection(t *testing.T) {
	client := &FederationClient{}
	_, err := client.SubscribeLawUpdates("")
	if err == nil {
		t.Fatal("expected error when federation connection is missing")
	}
}

// --- PetitionOutcomeWatcher tests ---

func TestFederationClient_SubscribePetitionOutcomes_RecvEventsAndStop(t *testing.T) {
	spy := &federationSpyServer{
		petitionOutcomeEvents: []*flowv1.PetitionOutcomeEvent{
			{
				PetitionId:     "pet-1",
				Outcome:        flowv1.PetitionOutcome_PETITION_OUTCOME_ACCEPTED,
				PublishedLawId: "new-law-1",
			},
			{
				PetitionId: "pet-2",
				Outcome:    flowv1.PetitionOutcome_PETITION_OUTCOME_REJECTED,
				Rejection: &flowv1.PublicationRejection{
					Reason:          flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_OUT_OF_SCOPE,
					RemediationText: "Out of publisher scope",
				},
			},
		},
	}
	client := setupFederationTestClient(t, spy)

	watcher, err := client.SubscribePetitionOutcomes("subscriber-flow-2")
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes() returned error: %v", err)
	}

	// Read both events.
	evt1, err := watcher.Recv()
	if err != nil {
		t.Fatalf("Recv() first event returned error: %v", err)
	}
	if evt1.GetPetitionId() != "pet-1" {
		t.Fatalf("expected petition_id pet-1, got %q", evt1.GetPetitionId())
	}
	if evt1.GetOutcome() != flowv1.PetitionOutcome_PETITION_OUTCOME_ACCEPTED {
		t.Fatalf("expected ACCEPTED outcome, got %v", evt1.GetOutcome())
	}

	evt2, err := watcher.Recv()
	if err != nil {
		t.Fatalf("Recv() second event returned error: %v", err)
	}
	if evt2.GetPetitionId() != "pet-2" {
		t.Fatalf("expected petition_id pet-2, got %q", evt2.GetPetitionId())
	}
	if evt2.GetOutcome() != flowv1.PetitionOutcome_PETITION_OUTCOME_REJECTED {
		t.Fatalf("expected REJECTED outcome, got %v", evt2.GetOutcome())
	}

	// Stop and verify post-stop error.
	watcher.Stop()
	_, err = watcher.Recv()
	if err == nil {
		t.Fatal("expected error from Recv() after Stop(), got nil")
	}

	if spy.lastSubscribePetitionOutcomes.GetSubscriberFlowIdentity() != "subscriber-flow-2" {
		t.Fatalf("expected subscriber identity subscriber-flow-2, got %q",
			spy.lastSubscribePetitionOutcomes.GetSubscriberFlowIdentity())
	}
}

func TestFederationClient_SubscribePetitionOutcomes_RecvThenClose_WithStop(t *testing.T) {
	spy := &federationSpyServer{
		petitionOutcomeEvents: []*flowv1.PetitionOutcomeEvent{
			{PetitionId: "pet-1", Outcome: flowv1.PetitionOutcome_PETITION_OUTCOME_ACCEPTED},
		},
	}
	client := setupFederationTestClient(t, spy)

	watcher, err := client.SubscribePetitionOutcomes("subscriber-flow-2")
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes() returned error: %v", err)
	}

	// Read one event.
	_, err = watcher.Recv()
	if err != nil {
		t.Fatalf("Recv() first event returned error: %v", err)
	}

	// Stop mid-stream.
	watcher.Stop()

	// Subsequent Recv must return error.
	_, err = watcher.Recv()
	if err == nil {
		t.Fatal("expected error from Recv() after Stop(), got nil")
	}
}

func TestFederationClient_SubscribePetitionOutcomes_NoConnection(t *testing.T) {
	client := &FederationClient{}
	_, err := client.SubscribePetitionOutcomes("")
	if err == nil {
		t.Fatal("expected error when federation connection is missing")
	}
}

// --- Watcher lifecycle edge cases ---

func TestFederationClient_LawUpdateWatcher_StopBeforeRecv(t *testing.T) {
	spy := &federationSpyServer{
		lawUpdateEvents: []*flowv1.PublishedLawEvent{
			{Law: &flowv1.Law{Id: "pub-law-1"}},
		},
	}
	client := setupFederationTestClient(t, spy)

	watcher, err := client.SubscribeLawUpdates("subscriber-1")
	if err != nil {
		t.Fatalf("SubscribeLawUpdates() returned error: %v", err)
	}

	watcher.Stop()

	_, err = watcher.Recv()
	if err == nil {
		t.Fatal("expected error from Recv() after Stop(), got nil")
	}
}

func TestFederationClient_LawUpdateWatcher_EOFThenStop(t *testing.T) {
	// Server returns immediately (0 events) — Recv returns io.EOF, Stop is safe.
	spy := &federationSpyServer{}
	client := setupFederationTestClient(t, spy)

	watcher, err := client.SubscribeLawUpdates("subscriber-1")
	if err != nil {
		t.Fatalf("SubscribeLawUpdates() returned error: %v", err)
	}

	_, err = watcher.Recv()
	if err != io.EOF {
		t.Fatalf("expected io.EOF for empty stream, got %v", err)
	}

	// Stop after EOF must not panic.
	watcher.Stop()
}

func TestFederationClient_PetitionOutcomeWatcher_StopBeforeRecv(t *testing.T) {
	spy := &federationSpyServer{
		petitionOutcomeEvents: []*flowv1.PetitionOutcomeEvent{
			{PetitionId: "pet-1"},
		},
	}
	client := setupFederationTestClient(t, spy)

	watcher, err := client.SubscribePetitionOutcomes("subscriber-1")
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes() returned error: %v", err)
	}

	watcher.Stop()

	_, err = watcher.Recv()
	if err == nil {
		t.Fatal("expected error from Recv() after Stop(), got nil")
	}
}

func TestFederationClient_PetitionOutcomeWatcher_EOFThenStop(t *testing.T) {
	spy := &federationSpyServer{}
	client := setupFederationTestClient(t, spy)

	watcher, err := client.SubscribePetitionOutcomes("subscriber-1")
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes() returned error: %v", err)
	}

	_, err = watcher.Recv()
	if err != io.EOF {
		t.Fatalf("expected io.EOF for empty stream, got %v", err)
	}

	// Stop after EOF must not panic.
	watcher.Stop()
}
