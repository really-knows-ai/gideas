package embassy

import (
	"context"
	"fmt"
	"os"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
)

const (
	// DefaultFederationAddress is the default gRPC endpoint for the Federation service.
	DefaultFederationAddress = "localhost:50061"

	// EnvFederationAddress overrides the default Federation gRPC address.
	EnvFederationAddress = "FEDERATION_ADDRESS"
)

// FederationOption configures the FederationClient.
type FederationOption func(*federationConfig)

type federationConfig struct {
	address string
}

// PetitionTarget holds the authority Flow identity and Embassy endpoint
// returned by GetPetitionTarget.
type PetitionTarget struct {
	AuthorityFlowIdentity string
	EmbassyEndpoint       string
}

// FlowEndpoint represents a discovered federation endpoint.
type FlowEndpoint = flowv1.FlowEndpoint

// FederationClient provides SDK helpers for the Federation service RPCs.
type FederationClient struct {
	conn       *grpc.ClientConn
	federation flowv1.FederationServiceClient
}

// NewFederationClient connects to the Federation service.
func NewFederationClient(opts ...FederationOption) (*FederationClient, error) {
	cfg := &federationConfig{address: DefaultFederationAddress}
	for _, opt := range opts {
		opt(cfg)
	}
	if envAddr := os.Getenv(EnvFederationAddress); envAddr != "" {
		cfg.address = envAddr
	}
	return newFederationClient(cfg.address)
}

// NewFederationClientForTest creates a FederationClient connected to the
// given address. Named to make misuse obvious — this is a cross-module test
// seam used by node packages (petition-watcher) to test against spy servers.
func NewFederationClientForTest(address string) (*FederationClient, error) {
	return newFederationClient(address)
}

func newFederationClient(address string) (*FederationClient, error) {
	conn, err := dialClientConn(address, DefaultFederationAddress, "federation")
	if err != nil {
		return nil, err
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
func (c *FederationClient) GetPetitionTarget(scope string) (*PetitionTarget, error) {
	if c.federation == nil {
		return nil, fmt.Errorf("flow sdk: federation client: no federation connection (set FEDERATION_ADDRESS)")
	}

	// ponytail: uses context.Background() per call. If per-client timeout
	// configuration is needed later, FederationClient can store a base context.
	ctx := context.Background()
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
func (c *FederationClient) DiscoverEndpoints(stateFilter string) ([]*FlowEndpoint, error) {
	if c.federation == nil {
		return nil, fmt.Errorf("flow sdk: federation client: no federation connection (set FEDERATION_ADDRESS)")
	}

	// ponytail: uses context.Background() per call. If per-client timeout
	// configuration is needed later, FederationClient can store a base context.
	ctx := context.Background()
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
// Reports an error if the publication is rejected or the RPC fails.
func (c *FederationClient) SubmitPublication(law *flowv1.Law, sourceFlowIdentity string) error {
	if c.federation == nil {
		return fmt.Errorf("flow sdk: federation client: no federation connection (set FEDERATION_ADDRESS)")
	}

	// ponytail: uses context.Background() per call. If per-client timeout
	// configuration is needed later, FederationClient can store a base context.
	ctx := context.Background()
	resp, err := c.federation.SubmitPublication(ctx, &flowv1.SubmitPublicationRequest{
		Law:                law,
		SourceFlowIdentity: sourceFlowIdentity,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: federation client: submit publication failed: %w", err)
	}
	if !resp.GetAccepted() {
		return fmt.Errorf("flow sdk: federation client: submit publication rejected: %s",
			resp.GetRejection().GetRemediationText())
	}
	return nil
}

// ---------------------------------------------------------------------------
// Streaming Subscriptions
// ---------------------------------------------------------------------------

// LawUpdateWatcher wraps the server-streaming subscription for law updates.
type LawUpdateWatcher = StreamHandle[flowv1.PublishedLawEvent]

// SubscribeLawUpdates opens a server-streaming subscription for published
// law distribution events. The caller should read from the returned watcher
// until io.EOF or Stop.
func (c *FederationClient) SubscribeLawUpdates(subscriberFlowIdentity string) (*LawUpdateWatcher, error) {
	if c.federation == nil {
		return nil, fmt.Errorf("flow sdk: federation client: no federation connection (set FEDERATION_ADDRESS)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := c.federation.SubscribeLawUpdates(ctx, &flowv1.SubscribeLawUpdatesRequest{
		SubscriberFlowIdentity: subscriberFlowIdentity,
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("flow sdk: federation client: subscribe law updates failed: %w", err)
	}
	return NewStreamHandle(cancel, stream), nil
}

// PetitionOutcomeWatcher wraps the server-streaming subscription for petition
// outcome events.
type PetitionOutcomeWatcher = StreamHandle[flowv1.PetitionOutcomeEvent]

// SubscribePetitionOutcomes opens a server-streaming subscription for
// petition outcome events (accepted/rejected). The caller should read from
// the returned watcher until io.EOF or Stop.
func (c *FederationClient) SubscribePetitionOutcomes(subscriberFlowIdentity string) (*PetitionOutcomeWatcher, error) {
	if c.federation == nil {
		return nil, fmt.Errorf("flow sdk: federation client: no federation connection (set FEDERATION_ADDRESS)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := c.federation.SubscribePetitionOutcomes(ctx, &flowv1.SubscribePetitionOutcomesRequest{
		SubscriberFlowIdentity: subscriberFlowIdentity,
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("flow sdk: federation client: subscribe petition outcomes failed: %w", err)
	}
	return NewStreamHandle(cancel, stream), nil
}
