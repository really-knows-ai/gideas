package proxy

import (
	"context"
	"fmt"
	"io"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// FederationProxy implements flowv1.FederationServiceServer by forwarding
// calls to the real Federation gRPC endpoint. The Sidecar exposes this as
// a passthrough so that nodes can reach the Federation service through the
// Sidecar's unified gRPC endpoint.
type FederationProxy struct {
	flowv1.UnimplementedFederationServiceServer
	client flowv1.FederationServiceClient
	conn   *grpc.ClientConn
}

// NewFederationProxy dials the Federation gRPC endpoint and returns a proxy
// handler ready to be registered on the Sidecar's gRPC server.
func NewFederationProxy(addr string) (*FederationProxy, error) {
	conn, err := grpc.NewClient(
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(metadataUnaryInterceptor),
		grpc.WithStreamInterceptor(metadataStreamInterceptor),
	)
	if err != nil {
		return nil, fmt.Errorf("dial federation: %w", err)
	}

	return &FederationProxy{
		client: flowv1.NewFederationServiceClient(conn),
		conn:   conn,
	}, nil
}

// Close releases the underlying gRPC connection to the Federation service.
func (p *FederationProxy) Close() error {
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Unary RPC forwarding
// ---------------------------------------------------------------------------

// JoinFederation forwards to the Federation service (passthrough).
func (p *FederationProxy) JoinFederation(
	ctx context.Context, req *flowv1.JoinFederationRequest,
) (*flowv1.JoinFederationResponse, error) {
	return p.client.JoinFederation(ctx, req)
}

// LeaveFederation forwards to the Federation service (passthrough).
func (p *FederationProxy) LeaveFederation(
	ctx context.Context, req *flowv1.LeaveFederationRequest,
) (*flowv1.LeaveFederationResponse, error) {
	return p.client.LeaveFederation(ctx, req)
}

// GetMembership forwards to the Federation service (passthrough).
func (p *FederationProxy) GetMembership(
	ctx context.Context, req *flowv1.GetMembershipRequest,
) (*flowv1.GetMembershipResponse, error) {
	return p.client.GetMembership(ctx, req)
}

// DiscoverEndpoints forwards to the Federation service (passthrough).
func (p *FederationProxy) DiscoverEndpoints(
	ctx context.Context, req *flowv1.DiscoverEndpointsRequest,
) (*flowv1.DiscoverEndpointsResponse, error) {
	return p.client.DiscoverEndpoints(ctx, req)
}

// GetPetitionTarget forwards to the Federation service (passthrough).
func (p *FederationProxy) GetPetitionTarget(
	ctx context.Context, req *flowv1.GetPetitionTargetRequest,
) (*flowv1.GetPetitionTargetResponse, error) {
	return p.client.GetPetitionTarget(ctx, req)
}

// SubmitPublication forwards to the Federation service (passthrough).
func (p *FederationProxy) SubmitPublication(
	ctx context.Context, req *flowv1.SubmitPublicationRequest,
) (*flowv1.SubmitPublicationResponse, error) {
	return p.client.SubmitPublication(ctx, req)
}

// ---------------------------------------------------------------------------
// Server-streaming RPC forwarding
// ---------------------------------------------------------------------------

// SubscribeLawUpdates proxies the server-streaming RPC to the Federation
// backend, forwarding each PublishedLawEvent to the caller.
func (p *FederationProxy) SubscribeLawUpdates(
	req *flowv1.SubscribeLawUpdatesRequest,
	stream grpc.ServerStreamingServer[flowv1.PublishedLawEvent],
) error {
	clientStream, err := p.client.SubscribeLawUpdates(stream.Context(), req)
	if err != nil {
		return err
	}
	for {
		msg, err := clientStream.Recv()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if err := stream.Send(msg); err != nil {
			return err
		}
	}
}

// SubscribePetitionOutcomes proxies the server-streaming RPC to the
// Federation backend, forwarding each PetitionOutcomeEvent to the caller.
func (p *FederationProxy) SubscribePetitionOutcomes(
	req *flowv1.SubscribePetitionOutcomesRequest,
	stream grpc.ServerStreamingServer[flowv1.PetitionOutcomeEvent],
) error {
	clientStream, err := p.client.SubscribePetitionOutcomes(stream.Context(), req)
	if err != nil {
		return err
	}
	for {
		msg, err := clientStream.Recv()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if err := stream.Send(msg); err != nil {
			return err
		}
	}
}
