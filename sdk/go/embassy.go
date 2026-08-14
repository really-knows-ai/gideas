package flow

import (
	"context"
	"fmt"
	"os"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	// DefaultEmbassyAddress is the default gRPC endpoint for the Embassy service.
	DefaultEmbassyAddress = "localhost:50059"

	// EnvEmbassyAddress overrides the default Embassy gRPC address.
	EnvEmbassyAddress = "EMBASSY_ADDRESS"
)

// EmbassyOption configures the EmbassyClient.
type EmbassyOption func(*embassyConfig)

type embassyConfig struct {
	address string
}

// WithEmbassyAddress overrides the default Embassy gRPC address.
func WithEmbassyAddress(addr string) EmbassyOption {
	return func(c *embassyConfig) {
		c.address = addr
	}
}

// EmbassyClient provides SDK helpers for the Embassy transfer protocol.
type EmbassyClient struct {
	conn    *grpc.ClientConn
	embassy flowv1.EmbassyServiceClient
}

// EmbassyExportStream wraps the Embassy export stream.
type EmbassyExportStream struct {
	ctx    context.Context
	cancel context.CancelFunc
	stream grpc.ServerStreamingClient[flowv1.PackageChunk]
}

// NewEmbassyClient connects to the Embassy service.
func NewEmbassyClient(opts ...EmbassyOption) (*EmbassyClient, error) {
	cfg := &embassyConfig{address: DefaultEmbassyAddress}
	for _, opt := range opts {
		opt(cfg)
	}
	if envAddr := os.Getenv(EnvEmbassyAddress); envAddr != "" {
		cfg.address = envAddr
	}
	return newEmbassyClient(cfg.address)
}

// NewEmbassyClientForTest creates an EmbassyClient connected to the given
// address. Named to make misuse obvious — this is a cross-module test seam
// used by the embassy node's export tests against a spy server.
func NewEmbassyClientForTest(address string) (*EmbassyClient, error) {
	return newEmbassyClient(address)
}

func newEmbassyClient(address string) (*EmbassyClient, error) {
	if address == "" {
		address = DefaultEmbassyAddress
	}

	conn, err := grpc.NewClient(
		address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("flow sdk: embassy client: failed to connect to embassy at %s: %w", address, err)
	}

	return &EmbassyClient{
		conn:    conn,
		embassy: flowv1.NewEmbassyServiceClient(conn),
	}, nil
}

// Close releases the underlying Embassy gRPC connection.
func (c *EmbassyClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// PreflightManifest validates a transfer manifest before package streaming.
func (c *EmbassyClient) PreflightManifest(
	manifest *flowv1.TransferManifest, remoteFlowIdentity string,
) (*flowv1.PreflightManifestResponse, error) {
	if c.embassy == nil {
		return nil, fmt.Errorf("flow sdk: embassy client: no embassy connection (set EMBASSY_ADDRESS)")
	}

	// ponytail: uses context.Background() per call. If per-client timeout
	// configuration is needed later, EmbassyClient can store a base context.
	ctx := context.Background()
	resp, err := c.embassy.PreflightManifest(ctx, &flowv1.PreflightManifestRequest{
		Manifest:   manifest,
		TreatyName: remoteFlowIdentity,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: embassy client: preflight manifest failed: %w", err)
	}
	return resp, nil
}

// StreamPackage sends a package stream to the receiving Embassy.
func (c *EmbassyClient) StreamPackage(
	packageChunks []*flowv1.PackageChunk,
) (*flowv1.StreamPackageResponse, error) {
	if c.embassy == nil {
		return nil, fmt.Errorf("flow sdk: embassy client: no embassy connection (set EMBASSY_ADDRESS)")
	}

	// ponytail: uses context.Background() per call. If per-client timeout
	// configuration is needed later, EmbassyClient can store a base context.
	ctx := context.Background()
	stream, err := c.embassy.StreamPackage(ctx)
	if err != nil {
		return nil, fmt.Errorf("flow sdk: embassy client: open stream package failed: %w", err)
	}

	for _, chunk := range packageChunks {
		if err := stream.Send(chunk); err != nil {
			return nil, fmt.Errorf("flow sdk: embassy client: send package chunk failed: %w", err)
		}
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		return nil, fmt.Errorf("flow sdk: embassy client: close stream package failed: %w", err)
	}
	return resp, nil
}

// ExportPackage starts a package export stream for the given Workitem and import type.
func (c *EmbassyClient) ExportPackage(
	workitemID, governedArtefact string,
) (*EmbassyExportStream, error) {
	if c.embassy == nil {
		return nil, fmt.Errorf("flow sdk: embassy client: no embassy connection (set EMBASSY_ADDRESS)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := c.embassy.ExportPackage(ctx, &flowv1.ExportPackageRequest{
		WorkitemId: workitemID,
		ImportType: governedArtefact,
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("flow sdk: embassy client: export package failed: %w", err)
	}
	return &EmbassyExportStream{ctx: ctx, cancel: cancel, stream: stream}, nil
}

// Recv returns the next exported package chunk from the Embassy stream.
func (s *EmbassyExportStream) Recv() (*flowv1.PackageChunk, error) {
	return s.stream.Recv()
}

// Stop cancels the stream. Subsequent Recv calls return a context-cancelled error.
func (s *EmbassyExportStream) Stop() {
	s.cancel()
}
