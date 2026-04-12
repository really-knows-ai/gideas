package flow

import (
	"context"
	"io"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
)

// federationSpyServer implements the FederationServiceServer interface for
// test-time request capture and configurable responses.
type federationSpyServer struct {
	flowv1.UnimplementedFederationServiceServer

	// GetPetitionTarget
	lastGetPetitionTarget *flowv1.GetPetitionTargetRequest
	getPetitionTargetResp *flowv1.GetPetitionTargetResponse

	// DiscoverEndpoints
	lastDiscoverEndpoints *flowv1.DiscoverEndpointsRequest
	discoverEndpointsResp *flowv1.DiscoverEndpointsResponse

	// SubmitPublication
	lastSubmitPublication *flowv1.SubmitPublicationRequest
	submitPublicationResp *flowv1.SubmitPublicationResponse

	// SubscribeLawUpdates
	lastSubscribeLawUpdates *flowv1.SubscribeLawUpdatesRequest
	lawUpdateEvents         []*flowv1.PublishedLawEvent

	// SubscribePetitionOutcomes
	lastSubscribePetitionOutcomes *flowv1.SubscribePetitionOutcomesRequest
	petitionOutcomeEvents         []*flowv1.PetitionOutcomeEvent
}

func (s *federationSpyServer) GetPetitionTarget(
	_ context.Context, req *flowv1.GetPetitionTargetRequest,
) (*flowv1.GetPetitionTargetResponse, error) {
	s.lastGetPetitionTarget = req
	if s.getPetitionTargetResp != nil {
		return s.getPetitionTargetResp, nil
	}
	return &flowv1.GetPetitionTargetResponse{
		AuthorityFlowIdentity: "authority-flow-1",
		EmbassyEndpoint:       "authority-flow-1.embassy:50059",
	}, nil
}

func (s *federationSpyServer) DiscoverEndpoints(
	_ context.Context, req *flowv1.DiscoverEndpointsRequest,
) (*flowv1.DiscoverEndpointsResponse, error) {
	s.lastDiscoverEndpoints = req
	if s.discoverEndpointsResp != nil {
		return s.discoverEndpointsResp, nil
	}
	return &flowv1.DiscoverEndpointsResponse{
		Endpoints: []*flowv1.FlowEndpoint{
			{
				FlowIdentity:   "flow-a",
				EmbassyAddress: "flow-a.embassy:50059",
				StateIds:       []string{"state-1"},
			},
		},
	}, nil
}

func (s *federationSpyServer) SubmitPublication(
	_ context.Context, req *flowv1.SubmitPublicationRequest,
) (*flowv1.SubmitPublicationResponse, error) {
	s.lastSubmitPublication = req
	if s.submitPublicationResp != nil {
		return s.submitPublicationResp, nil
	}
	return &flowv1.SubmitPublicationResponse{Accepted: true}, nil
}

func (s *federationSpyServer) SubscribeLawUpdates(
	req *flowv1.SubscribeLawUpdatesRequest,
	stream grpc.ServerStreamingServer[flowv1.PublishedLawEvent],
) error {
	s.lastSubscribeLawUpdates = req
	for _, evt := range s.lawUpdateEvents {
		if err := stream.Send(evt); err != nil {
			return err
		}
	}
	return nil
}

func (s *federationSpyServer) SubscribePetitionOutcomes(
	req *flowv1.SubscribePetitionOutcomesRequest,
	stream grpc.ServerStreamingServer[flowv1.PetitionOutcomeEvent],
) error {
	s.lastSubscribePetitionOutcomes = req
	for _, evt := range s.petitionOutcomeEvents {
		if err := stream.Send(evt); err != nil {
			return err
		}
	}
	return nil
}

func setupFederationTestClient(t *testing.T, spy *federationSpyServer) *FederationClient {
	t.Helper()

	conn := setupStandaloneGRPCTestConn(t, func(srv *grpc.Server) {
		flowv1.RegisterFederationServiceServer(srv, spy)
	})

	return &FederationClient{
		conn:       conn,
		federation: flowv1.NewFederationServiceClient(conn),
	}
}

// --- Slice 12.3.1 tests: membership + discovery ---

func TestFederationClient_GetPetitionTarget_Success(t *testing.T) {
	spy := &federationSpyServer{}
	client := setupFederationTestClient(t, spy)

	target, err := client.GetPetitionTarget(context.Background(), "security")
	if err != nil {
		t.Fatalf("GetPetitionTarget() returned error: %v", err)
	}
	if target.AuthorityFlowIdentity != "authority-flow-1" {
		t.Fatalf("expected authority identity authority-flow-1, got %q", target.AuthorityFlowIdentity)
	}
	if target.EmbassyEndpoint != "authority-flow-1.embassy:50059" {
		t.Fatalf("expected embassy endpoint authority-flow-1.embassy:50059, got %q", target.EmbassyEndpoint)
	}
	if spy.lastGetPetitionTarget.GetScope() != "security" {
		t.Fatalf("expected scope security, got %q", spy.lastGetPetitionTarget.GetScope())
	}
}

func TestFederationClient_DiscoverEndpoints_NoFilter(t *testing.T) {
	spy := &federationSpyServer{}
	client := setupFederationTestClient(t, spy)

	endpoints, err := client.DiscoverEndpoints(context.Background(), "")
	if err != nil {
		t.Fatalf("DiscoverEndpoints() returned error: %v", err)
	}
	if len(endpoints) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(endpoints))
	}
	if endpoints[0].FlowIdentity != "flow-a" {
		t.Fatalf("expected flow identity flow-a, got %q", endpoints[0].FlowIdentity)
	}
	if endpoints[0].EmbassyAddress != "flow-a.embassy:50059" {
		t.Fatalf("expected embassy address flow-a.embassy:50059, got %q", endpoints[0].EmbassyAddress)
	}
	if spy.lastDiscoverEndpoints.GetStateFilter() != "" {
		t.Fatalf("expected empty state filter, got %q", spy.lastDiscoverEndpoints.GetStateFilter())
	}
}

func TestFederationClient_DiscoverEndpoints_WithFilter(t *testing.T) {
	spy := &federationSpyServer{
		discoverEndpointsResp: &flowv1.DiscoverEndpointsResponse{
			Endpoints: []*flowv1.FlowEndpoint{
				{
					FlowIdentity:   "flow-b",
					EmbassyAddress: "flow-b.embassy:50059",
					StateIds:       []string{"state-2"},
				},
				{
					FlowIdentity:   "flow-c",
					EmbassyAddress: "flow-c.embassy:50059",
					StateIds:       []string{"state-2"},
				},
			},
		},
	}
	client := setupFederationTestClient(t, spy)

	endpoints, err := client.DiscoverEndpoints(context.Background(), "state-2")
	if err != nil {
		t.Fatalf("DiscoverEndpoints() returned error: %v", err)
	}
	if len(endpoints) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(endpoints))
	}
	if spy.lastDiscoverEndpoints.GetStateFilter() != "state-2" {
		t.Fatalf("expected state filter state-2, got %q", spy.lastDiscoverEndpoints.GetStateFilter())
	}
}

func TestFederationClient_ConnectsToConfigurableAddress(t *testing.T) {
	spy := &federationSpyServer{}
	client := setupFederationTestClient(t, spy)

	// Verify the client was successfully created and can make calls.
	_, err := client.GetPetitionTarget(context.Background(), "test")
	if err != nil {
		t.Fatalf("expected successful call on configured address, got error: %v", err)
	}
}

func TestFederationClient_GetPetitionTarget_NoConnection(t *testing.T) {
	client := &FederationClient{}
	_, err := client.GetPetitionTarget(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error when federation connection is missing")
	}
}

func TestFederationClient_DiscoverEndpoints_NoConnection(t *testing.T) {
	client := &FederationClient{}
	_, err := client.DiscoverEndpoints(context.Background(), "")
	if err == nil {
		t.Fatal("expected error when federation connection is missing")
	}
}

// --- Slice 12.3.2 tests: publication + events ---

func TestFederationClient_SubmitPublication_Accepted(t *testing.T) {
	spy := &federationSpyServer{}
	client := setupFederationTestClient(t, spy)

	law := &flowv1.Law{
		Id:   "law-1",
		Goal: "Test law",
	}
	resp, err := client.SubmitPublication(context.Background(), law, "flow-publisher")
	if err != nil {
		t.Fatalf("SubmitPublication() returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("expected publication to be accepted")
	}
	if spy.lastSubmitPublication.GetSourceFlowIdentity() != "flow-publisher" {
		t.Fatalf("expected source identity flow-publisher, got %q",
			spy.lastSubmitPublication.GetSourceFlowIdentity())
	}
	if spy.lastSubmitPublication.GetLaw().GetId() != "law-1" {
		t.Fatalf("expected law ID law-1, got %q",
			spy.lastSubmitPublication.GetLaw().GetId())
	}
}

func TestFederationClient_SubmitPublication_Rejected(t *testing.T) {
	spy := &federationSpyServer{
		submitPublicationResp: &flowv1.SubmitPublicationResponse{
			Accepted: false,
			Rejection: &flowv1.PublicationRejection{
				Reason:            flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_CONFLICT,
				ConflictingLawIds: []string{"law-existing"},
				RemediationText:   "Conflicts with existing law-existing",
			},
		},
	}
	client := setupFederationTestClient(t, spy)

	resp, err := client.SubmitPublication(context.Background(), &flowv1.Law{Id: "law-2"}, "flow-x")
	if err != nil {
		t.Fatalf("SubmitPublication() returned error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("expected publication to be rejected")
	}
	if resp.GetRejection().GetReason() != flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_CONFLICT {
		t.Fatalf("expected CONFLICT rejection reason, got %v", resp.GetRejection().GetReason())
	}
}

func TestFederationClient_SubmitPublication_NoConnection(t *testing.T) {
	client := &FederationClient{}
	_, err := client.SubmitPublication(context.Background(), &flowv1.Law{}, "")
	if err == nil {
		t.Fatal("expected error when federation connection is missing")
	}
}

func TestFederationClient_SubscribeLawUpdates_ReturnsStreamReader(t *testing.T) {
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

	stream, err := client.SubscribeLawUpdates(context.Background(), "subscriber-flow-1")
	if err != nil {
		t.Fatalf("SubscribeLawUpdates() returned error: %v", err)
	}

	// Read first event.
	evt1, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv() first event returned error: %v", err)
	}
	if evt1.GetLaw().GetId() != "pub-law-1" {
		t.Fatalf("expected first law ID pub-law-1, got %q", evt1.GetLaw().GetId())
	}
	if evt1.GetMaterialisationTier() != flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION {
		t.Fatalf("expected tier STATE_CONSTITUTION, got %v", evt1.GetMaterialisationTier())
	}

	// Read second event.
	evt2, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv() second event returned error: %v", err)
	}
	if evt2.GetLaw().GetId() != "pub-law-2" {
		t.Fatalf("expected second law ID pub-law-2, got %q", evt2.GetLaw().GetId())
	}

	// Expect EOF.
	_, err = stream.Recv()
	if err != io.EOF {
		t.Fatalf("expected EOF after all events, got %v", err)
	}

	if spy.lastSubscribeLawUpdates.GetSubscriberFlowIdentity() != "subscriber-flow-1" {
		t.Fatalf("expected subscriber identity subscriber-flow-1, got %q",
			spy.lastSubscribeLawUpdates.GetSubscriberFlowIdentity())
	}
}

func TestFederationClient_SubscribeLawUpdates_NoConnection(t *testing.T) {
	client := &FederationClient{}
	_, err := client.SubscribeLawUpdates(context.Background(), "")
	if err == nil {
		t.Fatal("expected error when federation connection is missing")
	}
}

func TestFederationClient_SubscribePetitionOutcomes_ReturnsStreamReader(t *testing.T) {
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

	stream, err := client.SubscribePetitionOutcomes(context.Background(), "subscriber-flow-2")
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes() returned error: %v", err)
	}

	// Read first event: accepted.
	evt1, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv() first event returned error: %v", err)
	}
	if evt1.GetPetitionId() != "pet-1" {
		t.Fatalf("expected petition_id pet-1, got %q", evt1.GetPetitionId())
	}
	if evt1.GetOutcome() != flowv1.PetitionOutcome_PETITION_OUTCOME_ACCEPTED {
		t.Fatalf("expected ACCEPTED outcome, got %v", evt1.GetOutcome())
	}
	if evt1.GetPublishedLawId() != "new-law-1" {
		t.Fatalf("expected published_law_id new-law-1, got %q", evt1.GetPublishedLawId())
	}

	// Read second event: rejected.
	evt2, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv() second event returned error: %v", err)
	}
	if evt2.GetPetitionId() != "pet-2" {
		t.Fatalf("expected petition_id pet-2, got %q", evt2.GetPetitionId())
	}
	if evt2.GetOutcome() != flowv1.PetitionOutcome_PETITION_OUTCOME_REJECTED {
		t.Fatalf("expected REJECTED outcome, got %v", evt2.GetOutcome())
	}
	if evt2.GetRejection() == nil {
		t.Fatal("expected rejection report on rejected outcome")
	}

	// Expect EOF.
	_, err = stream.Recv()
	if err != io.EOF {
		t.Fatalf("expected EOF after all events, got %v", err)
	}

	if spy.lastSubscribePetitionOutcomes.GetSubscriberFlowIdentity() != "subscriber-flow-2" {
		t.Fatalf("expected subscriber identity subscriber-flow-2, got %q",
			spy.lastSubscribePetitionOutcomes.GetSubscriberFlowIdentity())
	}
}

func TestFederationClient_SubscribePetitionOutcomes_NoConnection(t *testing.T) {
	client := &FederationClient{}
	_, err := client.SubscribePetitionOutcomes(context.Background(), "")
	if err == nil {
		t.Fatal("expected error when federation connection is missing")
	}
}
