package flow

import (
	"context"
	"io"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
)

const testScopeSecurity = "security"

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

	target, err := client.GetPetitionTarget(testScopeSecurity)
	if err != nil {
		t.Fatalf("GetPetitionTarget() returned error: %v", err)
	}
	if target.AuthorityFlowIdentity != "authority-flow-1" {
		t.Fatalf("expected authority identity authority-flow-1, got %q", target.AuthorityFlowIdentity)
	}
	if target.EmbassyEndpoint != "authority-flow-1.embassy:50059" {
		t.Fatalf("expected embassy endpoint authority-flow-1.embassy:50059, got %q", target.EmbassyEndpoint)
	}
	if spy.lastGetPetitionTarget.GetScope() != testScopeSecurity {
		t.Fatalf("expected scope %s, got %q", testScopeSecurity, spy.lastGetPetitionTarget.GetScope())
	}
}

func TestFederationClient_DiscoverEndpoints_NoFilter(t *testing.T) {
	spy := &federationSpyServer{}
	client := setupFederationTestClient(t, spy)

	endpoints, err := client.DiscoverEndpoints("")
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

	endpoints, err := client.DiscoverEndpoints("state-2")
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
	_, err := client.GetPetitionTarget("test")
	if err != nil {
		t.Fatalf("expected successful call on configured address, got error: %v", err)
	}
}

func TestFederationClient_GetPetitionTarget_NoConnection(t *testing.T) {
	client := &FederationClient{}
	_, err := client.GetPetitionTarget("test")
	if err == nil {
		t.Fatal("expected error when federation connection is missing")
	}
}

func TestFederationClient_DiscoverEndpoints_NoConnection(t *testing.T) {
	client := &FederationClient{}
	_, err := client.DiscoverEndpoints("")
	if err == nil {
		t.Fatal("expected error when federation connection is missing")
	}
}

// --- Slice 12.3.2 tests: publication + events ---

func TestFederationClient_SubmitPublication_Accepted(t *testing.T) {
	spy := &federationSpyServer{}
	client := setupFederationTestClient(t, spy)

	law := &Law{pb: &flowv1.Law{
		Id:   "law-1",
		Goal: "Test law",
	}}
	err := client.SubmitPublication(law, "flow-publisher")
	if err != nil {
		t.Fatalf("SubmitPublication() returned error: %v", err)
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

	err := client.SubmitPublication(&Law{pb: &flowv1.Law{Id: "law-2"}}, "flow-x")
	if err == nil {
		t.Fatal("expected error from SubmitPublication for rejected publication, got nil")
	}
}

func TestFederationClient_SubmitPublication_NoConnection(t *testing.T) {
	client := &FederationClient{}
	err := client.SubmitPublication(&Law{pb: &flowv1.Law{}}, "")
	if err == nil {
		t.Fatal("expected error when federation connection is missing")
	}
}

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

	watcher.Stop()
}
