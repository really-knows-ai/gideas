package flow

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
)

// EnvSidecarAddress is the environment variable to override the Sidecar
// gRPC address used by EntryClient. Falls back to DefaultSidecarAddress.
const EnvSidecarAddress = "SIDECAR_ADDRESS"

// EntryFunc is the function signature for entry-bound node logic.
// It runs as a long-lived goroutine alongside the handler server.
// Returning an error initiates graceful shutdown.
type EntryFunc func(ctx context.Context, client *EntryClient) error

// EntryClient provides operations available to entry-bound node logic.
// It connects to the Sidecar for CreateWorkitem (identity enriched via
// the Sidecar's namespace/node fallback) and directly to the Event Bus
// for Subscribe (same pattern as existing WatchChildren).
type EntryClient struct {
	sidecarConn  *grpc.ClientConn
	eventBusConn *grpc.ClientConn

	operator  flowv1.OperatorServiceClient
	eventBus  flowv1.FlowEventBusServiceClient
	librarian flowv1.LibrarianServiceClient
}

// NewEntryClientForTest creates an EntryClient connected to the given
// sidecar and event bus addresses. Named to make misuse obvious — this
// is intended for external node packages that need to unit-test entry
// functions with spy servers.
func NewEntryClientForTest(sidecarAddr, eventBusAddr string) (*EntryClient, error) {
	return newEntryClient(sidecarAddr, eventBusAddr)
}

// newEntryClient creates an EntryClient with the given addresses.
// sidecarAddr connects to the Sidecar for CreateWorkitem.
// eventBusAddr connects directly to the Event Bus for Subscribe.
// Either address may be empty; the corresponding methods will return errors.
func newEntryClient(sidecarAddr, eventBusAddr string) (*EntryClient, error) {
	ec := &EntryClient{}

	if sidecarAddr != "" {
		conn, err := grpc.NewClient(
			sidecarAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			return nil, fmt.Errorf("flow sdk: entry client: failed to connect to sidecar at %s: %w", sidecarAddr, err)
		}
		ec.sidecarConn = conn
		ec.operator = flowv1.NewOperatorServiceClient(conn)
		ec.librarian = flowv1.NewLibrarianServiceClient(conn)
	}

	if eventBusAddr != "" {
		conn, err := grpc.NewClient(
			eventBusAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			_ = ec.Close()
			return nil, fmt.Errorf("flow sdk: entry client: failed to connect to event bus at %s: %w", eventBusAddr, err)
		}
		ec.eventBusConn = conn
		ec.eventBus = flowv1.NewFlowEventBusServiceClient(conn)
	}

	return ec, nil
}

// CreateWorkitem creates a new Workitem with optional metadata.
// The metadata map is stored on the Workitem CRD and passed through
// to the handler via WorkitemContext.Metadata.
// The Sidecar's identity fallback provides namespace and node_id.
func (e *EntryClient) CreateWorkitem(ctx context.Context, metadata map[string]string) (string, error) {
	if e.operator == nil {
		return "", fmt.Errorf("flow sdk: entry client: no sidecar connection (set SIDECAR_ADDRESS)")
	}
	resp, err := e.operator.CreateWorkitem(ctx, &flowv1.CreateWorkitemRequest{
		Metadata: metadata,
	})
	if err != nil {
		return "", fmt.Errorf("flow sdk: entry client: create workitem failed: %w", err)
	}
	return resp.GetWorkitemId(), nil
}

// Subscribe opens a streaming subscription to the Event Bus.
// Returns an EventStream that yields events matching the channel
// and event type filter.
func (e *EntryClient) Subscribe(ctx context.Context, channel, eventType string) (*EventStream, error) {
	if e.eventBus == nil {
		return nil, fmt.Errorf("flow sdk: entry client: no event bus connection (set EVENT_BUS_ADDRESS)")
	}
	stream, err := e.eventBus.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel: channel,
		Filter: &flowv1.SubscribeFilter{
			EventType: eventType,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: entry client: subscribe failed: %w", err)
	}
	return &EventStream{stream: stream}, nil
}

// QueryLaws returns all laws matching the filter via the Librarian (proxied
// through the Sidecar). Pass empty strings for all laws.
func (e *EntryClient) QueryLaws(
	ctx context.Context, governedArtefact, representationType string,
) ([]*flowv1.Law, error) {
	if e.librarian == nil {
		return nil, fmt.Errorf("flow sdk: entry client: no sidecar connection for librarian (set SIDECAR_ADDRESS)")
	}
	var filter *flowv1.LawFilter
	if governedArtefact != "" || representationType != "" {
		filter = &flowv1.LawFilter{
			GovernedArtefact:   governedArtefact,
			RepresentationType: representationType,
		}
	}
	resp, err := e.librarian.QueryLaws(ctx, &flowv1.QueryLawsRequest{
		Filter: filter,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: entry client: query laws failed: %w", err)
	}
	return resp.GetLaws(), nil
}

// Close releases underlying gRPC connections.
func (e *EntryClient) Close() error {
	var firstErr error
	if e.eventBusConn != nil {
		if err := e.eventBusConn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if e.sidecarConn != nil {
		if err := e.sidecarConn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// EventStream wraps a server-streaming Event Bus subscription.
type EventStream struct {
	stream flowv1.FlowEventBusService_SubscribeClient
}

// Recv returns the next event from the stream. It blocks until an event
// is available or the stream ends. Returns io.EOF when the stream is closed.
func (s *EventStream) Recv() (*flowv1.FlowEvent, error) {
	return s.stream.Recv()
}

// Close cancels the underlying stream by requesting context cancellation.
// After Close, subsequent Recv calls will return an error.
func (s *EventStream) Close() error {
	return s.stream.CloseSend()
}

// StartEntry launches a node with both an entry loop and a handler server.
//
// The handler server listens for Process calls from the Sidecar (same as
// flow.Start). The entry function runs concurrently in a background
// goroutine with a cancellable context and an EntryClient.
//
// Shutdown sequence:
//  1. SIGTERM/SIGINT received.
//  2. Entry context is cancelled. Entry function should return.
//  3. gRPC server performs GracefulStop.
//  4. StartEntry returns.
//
// If the entry function returns an error, shutdown is initiated.
func StartEntry(entry EntryFunc, handler Handler, opts ...StartOption) error {
	cfg := &startConfig{
		port: DefaultNodePort,
	}

	if envPort := os.Getenv(EnvNodePort); envPort != "" {
		cfg.port = envPort
	}
	for _, o := range opts {
		o(cfg)
	}

	// Resolve addresses from environment.
	sidecarAddr := os.Getenv(EnvSidecarAddress)
	if sidecarAddr == "" {
		sidecarAddr = DefaultSidecarAddress
	}
	eventBusAddr := os.Getenv(EnvEventBusAddress)

	// Build the EntryClient.
	entryClient, err := newEntryClient(sidecarAddr, eventBusAddr)
	if err != nil {
		return err
	}
	defer func() { _ = entryClient.Close() }()

	// Set up the gRPC handler server.
	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", cfg.port))
	if err != nil {
		return fmt.Errorf("flow sdk: failed to listen on port %s: %w", cfg.port, err)
	}

	srv := grpc.NewServer()
	nodeServer := &nodeServiceServer{handler: handler}
	flowv1.RegisterNodeServiceServer(srv, nodeServer)
	reflection.Register(srv)

	// Entry context — cancelled on signal or entry error.
	entryCtx, entryCancel := context.WithCancel(context.Background())
	defer entryCancel()

	// Channel to receive entry function result.
	entryDone := make(chan error, 1)

	// Launch the entry function.
	go func() {
		entryDone <- entry(entryCtx, entryClient)
	}()

	// Graceful shutdown on SIGTERM/SIGINT or entry error.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

		select {
		case sig := <-sigCh:
			slog.Info("Received signal, shutting down entry node", "signal", sig)
		case err := <-entryDone:
			if err != nil {
				slog.Error("Entry function returned error, initiating shutdown", "error", err)
			} else {
				slog.Info("Entry function completed, initiating shutdown")
			}
		}

		entryCancel()
		srv.GracefulStop()
	}()

	slog.Info("Entry node server listening", "address", lis.Addr().String())
	if err := srv.Serve(lis); err != nil {
		return fmt.Errorf("flow sdk: entry server error: %w", err)
	}

	slog.Info("Entry node server stopped")
	return nil
}
