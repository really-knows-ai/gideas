// Package embassy implements the Embassy transfer-protocol and Federation
// client helpers plus the server-side Embassy scaffold used by the SDK.
package embassy

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

// StreamHandle wraps a server-streaming gRPC stream with the cancel function
// that terminates it. It is the shared backing type for the SDK's public
// stream handles (GraphExportStream, EmbassyExportStream, LawUpdateWatcher,
// PetitionOutcomeWatcher): Recv returns the next streamed element (io.EOF at
// stream end) and Stop cancels the stream so subsequent Recv calls return a
// context-cancelled error.
type StreamHandle[T any] struct {
	cancel context.CancelFunc
	stream grpc.ServerStreamingClient[T]
}

// NewStreamHandle wraps a server-streaming gRPC stream with its cancel
// function.
func NewStreamHandle[T any](cancel context.CancelFunc, stream grpc.ServerStreamingClient[T]) *StreamHandle[T] {
	return &StreamHandle[T]{cancel: cancel, stream: stream}
}

// Recv returns the next streamed element. It returns io.EOF at the end of
// the stream.
func (s *StreamHandle[T]) Recv() (*T, error) {
	return s.stream.Recv()
}

// Stop cancels the stream. Subsequent Recv calls return a context-cancelled
// error.
func (s *StreamHandle[T]) Stop() {
	s.cancel()
}

// EmbassyExportStream wraps the Embassy export stream.
type EmbassyExportStream = StreamHandle[flowv1.PackageChunk]

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

// dialClientConn opens an insecure gRPC connection to address, falling back
// to defaultAddr when address is empty and wrapping any error with the given
// service label. Shared by the Embassy and Federation client constructors.
func dialClientConn(address, defaultAddr, label string) (*grpc.ClientConn, error) {
	if address == "" {
		address = defaultAddr
	}
	conn, err := grpc.NewClient(
		address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("flow sdk: %s client: failed to connect to %s at %s: %w", label, label, address, err)
	}
	return conn, nil
}

func newEmbassyClient(address string) (*EmbassyClient, error) {
	conn, err := dialClientConn(address, DefaultEmbassyAddress, "embassy")
	if err != nil {
		return nil, err
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
	return NewStreamHandle(cancel, stream), nil
}
