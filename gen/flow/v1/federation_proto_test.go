package flowv1_test

import (
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestFederationProtoGeneratedTypes(t *testing.T) {
	t.Parallel()

	// Membership types.
	joinReq := &flowv1.JoinFederationRequest{
		BootstrapToken:  "tok-abc",
		FlowIdentity:    "flow-ns-1",
		EmbassyEndpoint: "embassy.flow-ns-1:50060",
	}
	if joinReq.GetBootstrapToken() != "tok-abc" {
		t.Fatalf("expected bootstrap token tok-abc, got %q", joinReq.GetBootstrapToken())
	}
	if joinReq.GetFlowIdentity() != "flow-ns-1" {
		t.Fatalf("expected flow identity flow-ns-1, got %q", joinReq.GetFlowIdentity())
	}

	joinResp := &flowv1.JoinFederationResponse{
		IntermediateCaPem: "-----BEGIN CERTIFICATE-----",
		FederationConfig: &flowv1.FederationConfig{
			FederationId:   "fed-1",
			FederationName: "test-federation",
			RootCaPem:      "-----BEGIN CERTIFICATE-----",
		},
		States: []*flowv1.State{
			{StateId: "state-1", Name: "West"},
		},
		PublisherRoles: []*flowv1.PublisherRole{
			{Scope: "security", Level: "state"},
		},
	}
	if joinResp.GetFederationConfig().GetFederationId() != "fed-1" {
		t.Fatalf("expected federation id fed-1, got %q", joinResp.GetFederationConfig().GetFederationId())
	}
	if len(joinResp.GetStates()) != 1 {
		t.Fatalf("expected 1 state, got %d", len(joinResp.GetStates()))
	}
	if joinResp.GetStates()[0].GetName() != "West" {
		t.Fatalf("expected state name West, got %q", joinResp.GetStates()[0].GetName())
	}
	if len(joinResp.GetPublisherRoles()) != 1 {
		t.Fatalf("expected 1 publisher role, got %d", len(joinResp.GetPublisherRoles()))
	}

	// Discovery types.
	discoverReq := &flowv1.DiscoverEndpointsRequest{StateFilter: "state-1"}
	if discoverReq.GetStateFilter() != "state-1" {
		t.Fatalf("expected state filter state-1, got %q", discoverReq.GetStateFilter())
	}

	endpoint := &flowv1.FlowEndpoint{
		FlowIdentity:   "flow-ns-2",
		EmbassyAddress: "embassy.flow-ns-2:50060",
		StateIds:       []string{"state-1", "state-2"},
	}
	if endpoint.GetEmbassyAddress() != "embassy.flow-ns-2:50060" {
		t.Fatalf("expected embassy address, got %q", endpoint.GetEmbassyAddress())
	}

	petTargetReq := &flowv1.GetPetitionTargetRequest{Scope: "security"}
	if petTargetReq.GetScope() != "security" {
		t.Fatalf("expected scope security, got %q", petTargetReq.GetScope())
	}

	// Publication types.
	pubReq := &flowv1.SubmitPublicationRequest{
		Law:                &flowv1.Law{Id: "law-1", Goal: "enforce X"},
		SourceFlowIdentity: "flow-ns-1",
	}
	if pubReq.GetLaw().GetId() != "law-1" {
		t.Fatalf("expected law id law-1, got %q", pubReq.GetLaw().GetId())
	}

	pubResp := &flowv1.SubmitPublicationResponse{
		Accepted: false,
		Rejection: &flowv1.PublicationRejection{
			Reason:            flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_CONFLICT,
			ConflictingLawIds: []string{"law-2"},
			RemediationText:   "revise scope",
		},
	}
	if pubResp.GetRejection().GetReason() != flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_CONFLICT {
		t.Fatalf("expected CONFLICT rejection reason, got %v", pubResp.GetRejection().GetReason())
	}
	if len(pubResp.GetRejection().GetConflictingLawIds()) != 1 {
		t.Fatalf("expected 1 conflicting law, got %d", len(pubResp.GetRejection().GetConflictingLawIds()))
	}

	lawEvent := &flowv1.PublishedLawEvent{
		Law:                   &flowv1.Law{Id: "law-3"},
		MaterialisationTier:   flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION,
		PetitionId:            "pet-1",
		PublisherFlowIdentity: "flow-ns-1",
		PublishedAt:           timestamppb.Now(),
	}
	if lawEvent.GetMaterialisationTier() != flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION {
		t.Fatalf("expected Tier 4, got %v", lawEvent.GetMaterialisationTier())
	}
	if lawEvent.GetPetitionId() != "pet-1" {
		t.Fatalf("expected petition id pet-1, got %q", lawEvent.GetPetitionId())
	}

	// Petition-outcome types.
	outcomeEvent := &flowv1.PetitionOutcomeEvent{
		PetitionId:     "pet-1",
		Outcome:        flowv1.PetitionOutcome_PETITION_OUTCOME_ACCEPTED,
		PublishedLawId: "law-3",
		ResolvedAt:     timestamppb.Now(),
	}
	if outcomeEvent.GetOutcome() != flowv1.PetitionOutcome_PETITION_OUTCOME_ACCEPTED {
		t.Fatalf("expected ACCEPTED outcome, got %v", outcomeEvent.GetOutcome())
	}
	if outcomeEvent.GetPublishedLawId() != "law-3" {
		t.Fatalf("expected published law id law-3, got %q", outcomeEvent.GetPublishedLawId())
	}

	rejectedEvent := &flowv1.PetitionOutcomeEvent{
		PetitionId: "pet-2",
		Outcome:    flowv1.PetitionOutcome_PETITION_OUTCOME_REJECTED,
		Rejection: &flowv1.PublicationRejection{
			Reason:          flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_UNAUTHORISED,
			RemediationText: "not authorised",
		},
	}
	if rejectedEvent.GetRejection().GetReason() != flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_UNAUTHORISED {
		t.Fatalf("expected UNAUTHORISED rejection, got %v", rejectedEvent.GetRejection().GetReason())
	}
}

func TestFederationServiceClientInterfaceExists(t *testing.T) {
	t.Parallel()

	var client flowv1.FederationServiceClient
	if client != nil {
		t.Fatal("expected nil zero-value FederationServiceClient interface")
	}
}
