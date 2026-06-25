package flow

import (
	"context"
	"fmt"
	"os"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	// DefaultFederationAddress is the default gRPC endpoint for the Federation service.
	DefaultFederationAddress = "localhost:50061"

	// EnvFederationAddress overrides the default Federation gRPC address.
	EnvFederationAddress = "FEDERATION_ADDRESS"
)

// PetitionTarget holds the authority Flow identity and Embassy endpoint
// returned by GetPetitionTarget.
type PetitionTarget struct {
	AuthorityFlowIdentity string
	EmbassyEndpoint       string
}

// FederationClient provides SDK helpers for the Federation service RPCs.
type FederationClient struct {
	conn       *grpc.ClientConn
	federation flowv1.FederationServiceClient
}

// NewFederationClient connects to the Federation service.
func NewFederationClient() (*FederationClient, error) {
	address := DefaultFederationAddress
	if envAddr := os.Getenv(EnvFederationAddress); envAddr != "" {
		address = envAddr
	}
	return newFederationClient(address)
}

// NewFederationClientForTest creates a FederationClient connected to the given address.
func NewFederationClientForTest(address string) (*FederationClient, error) {
	return newFederationClient(address)
}

func newFederationClient(address string) (*FederationClient, error) {
	if address == "" {
		address = DefaultFederationAddress
	}

	conn, err := grpc.NewClient(
		address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("flow sdk: federation client: failed to connect to federation at %s: %w", address, err)
	}

	return &FederationClient{
		conn:       conn,
		federation: flowv1.NewFederationServiceClient(conn),
	}, nil
}

// Close releases the underlying Federation gRPC connection.
func (c *FederationClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// GetPetitionTarget returns the authority Flow identity and Embassy endpoint
// for the given petition scope/domain.
func (c *FederationClient) GetPetitionTarget(
	ctx context.Context, scope string,
) (*PetitionTarget, error) {
	if c.federation == nil {
		return nil, fmt.Errorf("flow sdk: federation client: no federation connection (set FEDERATION_ADDRESS)")
	}

	resp, err := c.federation.GetPetitionTarget(ctx, &flowv1.GetPetitionTargetRequest{
		Scope: scope,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: federation client: get petition target failed: %w", err)
	}
	return &PetitionTarget{
		AuthorityFlowIdentity: resp.GetAuthorityFlowIdentity(),
		EmbassyEndpoint:       resp.GetEmbassyEndpoint(),
	}, nil
}

// DiscoverEndpoints returns Flow endpoints within the federation, optionally
// filtered by state. Pass an empty stateFilter to return all endpoints.
func (c *FederationClient) DiscoverEndpoints(
	ctx context.Context, stateFilter string,
) ([]*flowv1.FlowEndpoint, error) {
	if c.federation == nil {
		return nil, fmt.Errorf("flow sdk: federation client: no federation connection (set FEDERATION_ADDRESS)")
	}

	resp, err := c.federation.DiscoverEndpoints(ctx, &flowv1.DiscoverEndpointsRequest{
		StateFilter: stateFilter,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: federation client: discover endpoints failed: %w", err)
	}
	return resp.GetEndpoints(), nil
}

// ---------------------------------------------------------------------------
// Publication
// ---------------------------------------------------------------------------

// SubmitPublication submits a local Tier 3 law for publication admission.
// Returns the federation service response indicating acceptance or rejection.
func (c *FederationClient) SubmitPublication(
	ctx context.Context, law *flowv1.Law, sourceFlowIdentity string,
) (*flowv1.SubmitPublicationResponse, error) {
	if c.federation == nil {
		return nil, fmt.Errorf("flow sdk: federation client: no federation connection (set FEDERATION_ADDRESS)")
	}

	resp, err := c.federation.SubmitPublication(ctx, &flowv1.SubmitPublicationRequest{
		Law:                law,
		SourceFlowIdentity: sourceFlowIdentity,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: federation client: submit publication failed: %w", err)
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// Streaming Subscriptions
// ---------------------------------------------------------------------------

// LawUpdateStream wraps the server-streaming response for law updates.
type LawUpdateStream struct {
	stream grpc.ServerStreamingClient[flowv1.PublishedLawEvent]
}

// Recv returns the next published law event from the stream.
func (s *LawUpdateStream) Recv() (*flowv1.PublishedLawEvent, error) {
	return s.stream.Recv()
}

// SubscribeLawUpdates opens a server-streaming subscription for published
// law distribution events. The caller should read from the returned stream
// until io.EOF or context cancellation.
func (c *FederationClient) SubscribeLawUpdates(
	ctx context.Context, subscriberFlowIdentity string,
) (*LawUpdateStream, error) {
	if c.federation == nil {
		return nil, fmt.Errorf("flow sdk: federation client: no federation connection (set FEDERATION_ADDRESS)")
	}

	stream, err := c.federation.SubscribeLawUpdates(ctx, &flowv1.SubscribeLawUpdatesRequest{
		SubscriberFlowIdentity: subscriberFlowIdentity,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: federation client: subscribe law updates failed: %w", err)
	}
	return &LawUpdateStream{stream: stream}, nil
}

// PetitionOutcomeStream wraps the server-streaming response for petition
// outcome events.
type PetitionOutcomeStream struct {
	stream grpc.ServerStreamingClient[flowv1.PetitionOutcomeEvent]
}

// Recv returns the next petition outcome event from the stream.
func (s *PetitionOutcomeStream) Recv() (*flowv1.PetitionOutcomeEvent, error) {
	return s.stream.Recv()
}

// SubscribePetitionOutcomes opens a server-streaming subscription for
// petition outcome events (accepted/rejected). The caller should read from
// the returned stream until io.EOF or context cancellation.
func (c *FederationClient) SubscribePetitionOutcomes(
	ctx context.Context, subscriberFlowIdentity string,
) (*PetitionOutcomeStream, error) {
	if c.federation == nil {
		return nil, fmt.Errorf("flow sdk: federation client: no federation connection (set FEDERATION_ADDRESS)")
	}

	stream, err := c.federation.SubscribePetitionOutcomes(ctx, &flowv1.SubscribePetitionOutcomesRequest{
		SubscriberFlowIdentity: subscriberFlowIdentity,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: federation client: subscribe petition outcomes failed: %w", err)
	}
	return &PetitionOutcomeStream{stream: stream}, nil
}
