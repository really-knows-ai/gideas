package embassy

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
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
