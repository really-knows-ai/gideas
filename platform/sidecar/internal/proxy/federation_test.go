package proxy

import (
	"context"
	"io"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// captureFederationServer captures Federation RPC calls for assertions.
type captureFederationServer struct {
	flowv1.UnimplementedFederationServiceServer

	// Unary RPC captures.
	lastJoinReq              *flowv1.JoinFederationRequest
	lastLeaveReq             *flowv1.LeaveFederationRequest
	lastGetMembershipReq     *flowv1.GetMembershipRequest
	lastDiscoverReq          *flowv1.DiscoverEndpointsRequest
	lastGetPetitionReq       *flowv1.GetPetitionTargetRequest
	lastSubmitPublicationReq *flowv1.SubmitPublicationRequest

	// Streaming RPC captures.
	lastSubscribeLawReq      *flowv1.SubscribeLawUpdatesRequest
	lastSubscribePetitionReq *flowv1.SubscribePetitionOutcomesRequest

	// Metadata captured from the most recent RPC.
	capturedMD metadata.MD

	// Streaming events to send.
	lawEvents      []*flowv1.PublishedLawEvent
	petitionEvents []*flowv1.PetitionOutcomeEvent
}

func (s *captureFederationServer) JoinFederation(
	ctx context.Context, req *flowv1.JoinFederationRequest,
) (*flowv1.JoinFederationResponse, error) {
	s.lastJoinReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.JoinFederationResponse{}, nil
}

func (s *captureFederationServer) LeaveFederation(
	ctx context.Context, req *flowv1.LeaveFederationRequest,
) (*flowv1.LeaveFederationResponse, error) {
	s.lastLeaveReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.LeaveFederationResponse{Acknowledged: true}, nil
}

func (s *captureFederationServer) GetMembership(
	ctx context.Context, req *flowv1.GetMembershipRequest,
) (*flowv1.GetMembershipResponse, error) {
	s.lastGetMembershipReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.GetMembershipResponse{}, nil
}

func (s *captureFederationServer) DiscoverEndpoints(
	ctx context.Context, req *flowv1.DiscoverEndpointsRequest,
) (*flowv1.DiscoverEndpointsResponse, error) {
	s.lastDiscoverReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.DiscoverEndpointsResponse{
		Endpoints: []*flowv1.FlowEndpoint{
			{FlowIdentity: "flow-a", EmbassyAddress: "embassy-a:50051"},
		},
	}, nil
}

func (s *captureFederationServer) GetPetitionTarget(
	ctx context.Context, req *flowv1.GetPetitionTargetRequest,
) (*flowv1.GetPetitionTargetResponse, error) {
	s.lastGetPetitionReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.GetPetitionTargetResponse{
		AuthorityFlowIdentity: "authority-flow",
		EmbassyEndpoint:       "embassy:50051",
	}, nil
}

func (s *captureFederationServer) SubmitPublication(
	ctx context.Context, req *flowv1.SubmitPublicationRequest,
) (*flowv1.SubmitPublicationResponse, error) {
	s.lastSubmitPublicationReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.SubmitPublicationResponse{Accepted: true}, nil
}

func (s *captureFederationServer) SubscribeLawUpdates(
	req *flowv1.SubscribeLawUpdatesRequest,
	stream grpc.ServerStreamingServer[flowv1.PublishedLawEvent],
) error {
	s.lastSubscribeLawReq = req
	if md, ok := metadata.FromIncomingContext(stream.Context()); ok {
		s.capturedMD = md
	}
	for _, evt := range s.lawEvents {
		if err := stream.Send(evt); err != nil {
			return err
		}
	}
	return nil
}

func (s *captureFederationServer) SubscribePetitionOutcomes(
	req *flowv1.SubscribePetitionOutcomesRequest,
	stream grpc.ServerStreamingServer[flowv1.PetitionOutcomeEvent],
) error {
	s.lastSubscribePetitionReq = req
	if md, ok := metadata.FromIncomingContext(stream.Context()); ok {
		s.capturedMD = md
	}
	for _, evt := range s.petitionEvents {
		if err := stream.Send(evt); err != nil {
			return err
		}
	}
	return nil
}

type federationTestEnv struct {
	proxy *FederationProxy
	spy   *captureFederationServer
}

func setupFederationProxy(t *testing.T) *federationTestEnv {
	t.Helper()

	spy := &captureFederationServer{}
	conn := dialBufconn(t, func(s *grpc.Server) {
		flowv1.RegisterFederationServiceServer(s, spy)
	})

	p := &FederationProxy{
		client: flowv1.NewFederationServiceClient(conn),
		conn:   conn,
	}

	return &federationTestEnv{
		proxy: p,
		spy:   spy,
	}
}

// ---------------------------------------------------------------------------
// Unary RPC tests
// ---------------------------------------------------------------------------

func TestFederationProxy_JoinFederation_ForwardsRequest(t *testing.T) {
	env := setupFederationProxy(t)

	md := metadata.Pairs("x-flow-workitem-id", "wi-join")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := env.proxy.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		FlowIdentity:   "flow-1",
		BootstrapToken: "tok-123",
	})
	if err != nil {
		t.Fatalf("JoinFederation: %v", err)
	}

	if env.spy.lastJoinReq == nil {
		t.Fatal("JoinFederation was not forwarded to backend")
	}
	if env.spy.lastJoinReq.GetFlowIdentity() != "flow-1" {
		t.Fatalf("expected flow_identity=flow-1, got %q", env.spy.lastJoinReq.GetFlowIdentity())
	}

	vals := env.spy.capturedMD.Get("x-flow-workitem-id")
	if len(vals) != 1 || vals[0] != "wi-join" {
		t.Fatalf("expected metadata propagation, got %v", vals)
	}
}

func TestFederationProxy_LeaveFederation_ForwardsRequest(t *testing.T) {
	env := setupFederationProxy(t)

	md := metadata.Pairs("x-flow-namespace", "ns-leave")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := env.proxy.LeaveFederation(ctx, &flowv1.LeaveFederationRequest{
		FlowIdentity: "flow-leaving",
	})
	if err != nil {
		t.Fatalf("LeaveFederation: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	if env.spy.lastLeaveReq == nil {
		t.Fatal("LeaveFederation was not forwarded to backend")
	}
	if env.spy.lastLeaveReq.GetFlowIdentity() != "flow-leaving" {
		t.Fatalf("expected flow_identity=flow-leaving, got %q", env.spy.lastLeaveReq.GetFlowIdentity())
	}

	vals := env.spy.capturedMD.Get("x-flow-namespace")
	if len(vals) != 1 || vals[0] != "ns-leave" {
		t.Fatalf("expected metadata propagation, got %v", vals)
	}
}

func TestFederationProxy_GetMembership_ForwardsRequest(t *testing.T) {
	env := setupFederationProxy(t)

	md := metadata.Pairs("x-flow-node-id", "node-member")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := env.proxy.GetMembership(ctx, &flowv1.GetMembershipRequest{
		FlowIdentity: "flow-member",
	})
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}

	if env.spy.lastGetMembershipReq == nil {
		t.Fatal("GetMembership was not forwarded to backend")
	}
	if env.spy.lastGetMembershipReq.GetFlowIdentity() != "flow-member" {
		t.Fatalf("expected flow_identity=flow-member, got %q", env.spy.lastGetMembershipReq.GetFlowIdentity())
	}

	vals := env.spy.capturedMD.Get("x-flow-node-id")
	if len(vals) != 1 || vals[0] != "node-member" {
		t.Fatalf("expected metadata propagation, got %v", vals)
	}
}

func TestFederationProxy_DiscoverEndpoints_ForwardsRequest(t *testing.T) {
	env := setupFederationProxy(t)

	resp, err := env.proxy.DiscoverEndpoints(context.Background(), &flowv1.DiscoverEndpointsRequest{
		StateFilter: "active",
	})
	if err != nil {
		t.Fatalf("DiscoverEndpoints: %v", err)
	}

	if env.spy.lastDiscoverReq == nil {
		t.Fatal("DiscoverEndpoints was not forwarded to backend")
	}
	if env.spy.lastDiscoverReq.GetStateFilter() != "active" {
		t.Fatalf("expected state_filter=active, got %q", env.spy.lastDiscoverReq.GetStateFilter())
	}

	if len(resp.GetEndpoints()) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(resp.GetEndpoints()))
	}
	if resp.GetEndpoints()[0].GetFlowIdentity() != "flow-a" {
		t.Fatalf("expected flow_identity=flow-a, got %q", resp.GetEndpoints()[0].GetFlowIdentity())
	}
}

func TestFederationProxy_GetPetitionTarget_ForwardsRequest(t *testing.T) {
	env := setupFederationProxy(t)

	resp, err := env.proxy.GetPetitionTarget(context.Background(), &flowv1.GetPetitionTargetRequest{
		Scope: "security",
	})
	if err != nil {
		t.Fatalf("GetPetitionTarget: %v", err)
	}

	if env.spy.lastGetPetitionReq == nil {
		t.Fatal("GetPetitionTarget was not forwarded to backend")
	}
	if env.spy.lastGetPetitionReq.GetScope() != "security" {
		t.Fatalf("expected scope=security, got %q", env.spy.lastGetPetitionReq.GetScope())
	}

	if resp.GetAuthorityFlowIdentity() != "authority-flow" {
		t.Fatalf("expected authority_flow_identity=authority-flow, got %q", resp.GetAuthorityFlowIdentity())
	}
}

func TestFederationProxy_SubmitPublication_ForwardsRequest(t *testing.T) {
	env := setupFederationProxy(t)

	md := metadata.Pairs("x-flow-workitem-id", "wi-pub")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := env.proxy.SubmitPublication(ctx, &flowv1.SubmitPublicationRequest{
		SourceFlowIdentity: "src-flow",
		Law:                &flowv1.Law{Id: "law-pub-1"},
	})
	if err != nil {
		t.Fatalf("SubmitPublication: %v", err)
	}

	if env.spy.lastSubmitPublicationReq == nil {
		t.Fatal("SubmitPublication was not forwarded to backend")
	}
	if env.spy.lastSubmitPublicationReq.GetSourceFlowIdentity() != "src-flow" {
		t.Fatalf("expected source_flow_identity=src-flow, got %q",
			env.spy.lastSubmitPublicationReq.GetSourceFlowIdentity())
	}
	if !resp.GetAccepted() {
		t.Fatal("expected accepted=true")
	}

	vals := env.spy.capturedMD.Get("x-flow-workitem-id")
	if len(vals) != 1 || vals[0] != "wi-pub" {
		t.Fatalf("expected metadata propagation, got %v", vals)
	}
}

// ---------------------------------------------------------------------------
// Server-streaming RPC tests
// ---------------------------------------------------------------------------

func TestFederationProxy_SubscribeLawUpdates_StreamsEvents(t *testing.T) {
	env := setupFederationProxy(t)

	env.spy.lawEvents = []*flowv1.PublishedLawEvent{
		{Law: &flowv1.Law{Id: "law-stream-1"}, PublisherFlowIdentity: "pub-1"},
		{Law: &flowv1.Law{Id: "law-stream-2"}, PublisherFlowIdentity: "pub-2"},
	}

	// Use a full gRPC client-server round-trip through bufconn to test streaming.
	spy := env.spy
	conn := dialBufconn(t, func(s *grpc.Server) {
		flowv1.RegisterFederationServiceServer(s, env.proxy)
	})
	client := flowv1.NewFederationServiceClient(conn)

	md := metadata.Pairs("x-flow-workitem-id", "wi-law-stream")
	ctx := metadata.NewOutgoingContext(context.Background(), md)

	stream, err := client.SubscribeLawUpdates(ctx, &flowv1.SubscribeLawUpdatesRequest{
		SubscriberFlowIdentity: "sub-flow-1",
	})
	if err != nil {
		t.Fatalf("SubscribeLawUpdates: %v", err)
	}

	var received []*flowv1.PublishedLawEvent
	for {
		evt, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		received = append(received, evt)
	}

	if len(received) != 2 {
		t.Fatalf("expected 2 events, got %d", len(received))
	}
	if received[0].GetLaw().GetId() != "law-stream-1" {
		t.Fatalf("expected first event law_id=law-stream-1, got %q", received[0].GetLaw().GetId())
	}
	if received[1].GetLaw().GetId() != "law-stream-2" {
		t.Fatalf("expected second event law_id=law-stream-2, got %q", received[1].GetLaw().GetId())
	}

	// Verify request was forwarded to backend spy.
	if spy.lastSubscribeLawReq == nil {
		t.Fatal("SubscribeLawUpdates was not forwarded to backend")
	}
	if spy.lastSubscribeLawReq.GetSubscriberFlowIdentity() != "sub-flow-1" {
		t.Fatalf("expected subscriber_flow_identity=sub-flow-1, got %q",
			spy.lastSubscribeLawReq.GetSubscriberFlowIdentity())
	}

	// Verify metadata propagation.
	vals := spy.capturedMD.Get("x-flow-workitem-id")
	if len(vals) != 1 || vals[0] != "wi-law-stream" {
		t.Fatalf("expected metadata propagation, got %v", vals)
	}
}

func TestFederationProxy_SubscribePetitionOutcomes_StreamsEvents(t *testing.T) {
	env := setupFederationProxy(t)

	env.spy.petitionEvents = []*flowv1.PetitionOutcomeEvent{
		{PetitionId: "pet-1", PublishedLawId: "law-out-1"},
		{PetitionId: "pet-2", PublishedLawId: "law-out-2"},
	}

	spy := env.spy
	conn := dialBufconn(t, func(s *grpc.Server) {
		flowv1.RegisterFederationServiceServer(s, env.proxy)
	})
	client := flowv1.NewFederationServiceClient(conn)

	md := metadata.Pairs("x-flow-namespace", "ns-pet-stream")
	ctx := metadata.NewOutgoingContext(context.Background(), md)

	stream, err := client.SubscribePetitionOutcomes(ctx, &flowv1.SubscribePetitionOutcomesRequest{
		SubscriberFlowIdentity: "sub-flow-2",
	})
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes: %v", err)
	}

	var received []*flowv1.PetitionOutcomeEvent
	for {
		evt, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		received = append(received, evt)
	}

	if len(received) != 2 {
		t.Fatalf("expected 2 events, got %d", len(received))
	}
	if received[0].GetPetitionId() != "pet-1" {
		t.Fatalf("expected first event petition_id=pet-1, got %q", received[0].GetPetitionId())
	}
	if received[1].GetPetitionId() != "pet-2" {
		t.Fatalf("expected second event petition_id=pet-2, got %q", received[1].GetPetitionId())
	}

	// Verify request was forwarded to backend spy.
	if spy.lastSubscribePetitionReq == nil {
		t.Fatal("SubscribePetitionOutcomes was not forwarded to backend")
	}
	if spy.lastSubscribePetitionReq.GetSubscriberFlowIdentity() != "sub-flow-2" {
		t.Fatalf("expected subscriber_flow_identity=sub-flow-2, got %q",
			spy.lastSubscribePetitionReq.GetSubscriberFlowIdentity())
	}

	// Verify metadata propagation.
	vals := spy.capturedMD.Get("x-flow-namespace")
	if len(vals) != 1 || vals[0] != "ns-pet-stream" {
		t.Fatalf("expected metadata propagation, got %v", vals)
	}
}
