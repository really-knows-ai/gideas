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
	"google.golang.org/grpc/reflection"
)

const (
	// DefaultNodePort is the default gRPC port the SDK server listens on
	// for incoming Process calls from the Sidecar.
	DefaultNodePort = "50053"

	// EnvNodePort is the environment variable to override the SDK server port.
	EnvNodePort = "FLOW_NODE_PORT"
)

// Handler is the function signature for user-provided work processing logic.
// It receives the workitem context (flow_id, workitem_id, node_id) and returns
// an error if processing fails.
type Handler func(ctx context.Context, workitemCtx *flowv1.WorkitemContext) error

// StartOption configures the SDK server started by Start.
type StartOption func(*startConfig)

type startConfig struct {
	port         string
	queueManager *queueManagerImpl
}

// WithNodePort overrides the default SDK server listen port.
func WithNodePort(port string) StartOption {
	return func(c *startConfig) {
		c.port = port
	}
}

// WithQueueManager configures the SDK server with a HITL QueueManager.
// When provided, Start() will initialise the queue store, mesh, and HTTP
// server, and register the QueuePeerService on the gRPC server alongside
// NodeService.
func WithQueueManager(qm *queueManagerImpl) StartOption {
	return func(c *startConfig) {
		c.queueManager = qm
	}
}

// Start launches a gRPC server implementing the NodeService.Process RPC.
// When the Sidecar calls Process, the provided handler is invoked with the
// workitem context. The server runs until SIGTERM/SIGINT is received, at
// which point it performs a graceful shutdown.
//
// This is the primary entry point for push-based Foundry Flow nodes.
// The communication path is:
//
//	Operator -> (network) -> Sidecar:AssignWork -> (localhost) -> SDK:Process
func Start(handler Handler, opts ...StartOption) error {
	cfg := &startConfig{
		port: DefaultNodePort,
	}

	// Allow env override.
	if envPort := os.Getenv(EnvNodePort); envPort != "" {
		cfg.port = envPort
	}

	// Apply functional options (take precedence over env).
	for _, o := range opts {
		o(cfg)
	}

	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", cfg.port))
	if err != nil {
		return fmt.Errorf("flow sdk: failed to listen on port %s: %w", cfg.port, err)
	}

	srv := grpc.NewServer()
	nodeServer := &nodeServiceServer{handler: handler}
	flowv1.RegisterNodeServiceServer(srv, nodeServer)

	// If a QueueManager is configured, start it and register its gRPC service.
	if cfg.queueManager != nil {
		if err := cfg.queueManager.Start(context.Background()); err != nil {
			_ = lis.Close()
			return fmt.Errorf("flow sdk: failed to start queue manager: %w", err)
		}
		cfg.queueManager.RegisterGRPC(srv)
	}

	reflection.Register(srv)

	// Graceful shutdown on SIGTERM/SIGINT.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		slog.Info("Received signal, shutting down SDK server", "signal", sig)
		if cfg.queueManager != nil {
			_ = cfg.queueManager.Stop()
		}
		srv.GracefulStop()
	}()

	slog.Info("SDK server listening", "address", lis.Addr().String())
	if err := srv.Serve(lis); err != nil {
		return fmt.Errorf("flow sdk: server error: %w", err)
	}

	slog.Info("SDK server stopped")
	return nil
}

// nodeServiceServer implements the flowv1.NodeServiceServer interface,
// delegating to the user-provided Handler.
type nodeServiceServer struct {
	flowv1.UnimplementedNodeServiceServer
	handler Handler
}

// Process is called by the Sidecar to deliver a work assignment.
func (s *nodeServiceServer) Process(ctx context.Context, req *flowv1.AssignWorkRequest) (*flowv1.Ack, error) {
	wctx := req.GetContext()
	slog.Info("Processing work assignment",
		"flow_id", wctx.GetFlowId(),
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	if err := s.handler(ctx, wctx); err != nil {
		slog.Error("Handler returned error",
			"workitem_id", wctx.GetWorkitemId(),
			"error", err,
		)
		return &flowv1.Ack{
			Accepted: false,
			Message:  err.Error(),
		}, nil
	}

	return &flowv1.Ack{
		Accepted: true,
		Message:  "processed",
	}, nil
}
