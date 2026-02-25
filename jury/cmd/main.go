// Jury is the deliberation engine of Foundry Flow's Judiciary — a standalone
// gRPC service that empanels diverse AI jurors to reach consensus on governed
// questions.
//
// It listens on port 50059 (configurable via JURY_PORT) and implements the
// JuryService.Deliberate RPC. Each deliberation empanels a configurable number
// of jurors with distinct judicial philosophies, runs multi-round blind voting,
// and applies the requested consensus strategy.
//
// Usage:
//
//	go run ./jury/cmd/main.go
//	JURY_PORT=50059 go run ./jury/cmd/main.go
package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/jury/internal/service"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

const (
	defaultPort = "50059"

	envPort    = "JURY_PORT"
	envSidecar = "JURY_SIDECAR_ADDRESS"
)

func main() {
	port := os.Getenv(envPort)
	if port == "" {
		port = defaultPort
	}

	slog.Info("Jury starting", "port", port)

	// Create SDK client for juror agents (heartbeat, telemetry).
	// The Jury service connects to a sidecar for these management operations.
	sidecarAddr := os.Getenv(envSidecar)
	var clientOpts []flow.ClientOption
	if sidecarAddr != "" {
		clientOpts = append(clientOpts, flow.WithSidecarAddress(sidecarAddr))
	}

	client, err := flow.NewClient(clientOpts...)
	if err != nil {
		slog.Error("Failed to create SDK client", "error", err)
		os.Exit(1)
	}
	defer func() { _ = client.Close() }()

	// Create juror factory with default configs (no prompt overrides).
	// Each juror creates its own KimiK2Ollama model internally.
	factory := service.NewDefaultFactory(client, nil)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		slog.Error("Failed to listen", "port", port, "error", err)
		os.Exit(1)
	}

	srv := grpc.NewServer()

	jurySrv := service.NewJuryServer(factory)
	flowv1.RegisterJuryServiceServer(srv, jurySrv)

	// Enable gRPC reflection for debugging with grpcurl.
	reflection.Register(srv)

	// Graceful shutdown on SIGTERM/SIGINT.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		slog.Info("Received signal, shutting down gracefully", "signal", sig)
		srv.GracefulStop()
	}()

	slog.Info("Jury listening", "address", lis.Addr().String())
	if err := srv.Serve(lis); err != nil {
		slog.Error("Jury server error", "error", err)
		os.Exit(1)
	}

	slog.Info("Jury stopped")
}
