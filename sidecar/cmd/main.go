// Sidecar is the in-pod gRPC proxy for Foundry Flow nodes.
//
// It listens on a single port and multiplexes all Flow services
// (SidecarService, OperatorService, ArchivistService). The SidecarService
// handles both node-facing RPCs (Heartbeat) and operator-facing RPCs
// (AssignWork). The OperatorService is proxied to the real Operator gRPC
// endpoint. ArchivistService is proxied to the real Archivist when
// ARCHIVIST_ADDRESS is set, otherwise falls back to a mock handler.
//
// Usage:
//
//	FLOW_NODE_ID=my-node go run ./sidecar/cmd/main.go
//	OPERATOR_ADDRESS=localhost:50052 FLOW_NODE_ID=my-node go run ./sidecar/cmd/main.go
//	ARCHIVIST_ADDRESS=localhost:50054 FLOW_NODE_ID=my-node go run ./sidecar/cmd/main.go
package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/sidecar/internal/mock"
	"github.com/gideas/flow/sidecar/internal/proxy"
	"github.com/gideas/flow/sidecar/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

const (
	defaultPort            = "50051"
	defaultOperatorAddress = "localhost:50052"
	envNodeID              = "FLOW_NODE_ID"
	envPort                = "FLOW_SIDECAR_PORT"
	envOperatorAddress     = "OPERATOR_ADDRESS"
	envNodeAddress         = "FLOW_NODE_ADDRESS"
	envArchivistAddress    = "ARCHIVIST_ADDRESS"
)

func main() {
	port := os.Getenv(envPort)
	if port == "" {
		port = defaultPort
	}

	nodeID := os.Getenv(envNodeID)
	if nodeID == "" {
		nodeID = "unknown-node"
	}

	operatorAddr := os.Getenv(envOperatorAddress)
	if operatorAddr == "" {
		operatorAddr = defaultOperatorAddress
	}

	nodeAddr := os.Getenv(envNodeAddress)
	// Defaults handled by service.NewSidecarServer if empty.

	archivistAddr := os.Getenv(envArchivistAddress)

	slog.Info("Sidecar starting",
		"port", port,
		"node_id", nodeID,
		"operator_address", operatorAddr,
		"node_address", nodeAddr,
		"archivist_address", archivistAddr,
		"phase", "brain-stem",
	)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		slog.Error("Failed to listen", "port", port, "error", err)
		os.Exit(1)
	}

	srv := grpc.NewServer()

	// Register service handlers.
	// SidecarService handles Heartbeat (node-facing) and AssignWork (operator-facing).
	sidecarSrv := service.NewSidecarServer(nodeID, nodeAddr)
	flowv1.RegisterSidecarServiceServer(srv, sidecarSrv)

	// ArchivistService: proxy to real Archivist if address is set, otherwise mock.
	var archivistCloser func() error
	if archivistAddr != "" {
		archivistProxy, err := proxy.NewArchivistProxy(archivistAddr)
		if err != nil {
			slog.Error("Failed to connect to Archivist", "address", archivistAddr, "error", err)
			os.Exit(1)
		}
		flowv1.RegisterArchivistServiceServer(srv, archivistProxy)
		archivistCloser = archivistProxy.Close
		slog.Info("Archivist proxy enabled", "address", archivistAddr)
	} else {
		flowv1.RegisterArchivistServiceServer(srv, &mock.ArchivistHandler{})
		archivistCloser = func() error { return nil }
		slog.Info("Archivist mock enabled (no ARCHIVIST_ADDRESS set)")
	}

	// OperatorService is proxied to the real Operator.
	operatorProxy, err := proxy.NewOperatorProxy(operatorAddr)
	if err != nil {
		slog.Error("Failed to connect to Operator", "address", operatorAddr, "error", err)
		os.Exit(1)
	}
	flowv1.RegisterOperatorServiceServer(srv, operatorProxy)

	// Enable gRPC reflection for debugging with grpcurl.
	reflection.Register(srv)

	// Graceful shutdown on SIGTERM/SIGINT.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		slog.Info("Received signal, shutting down gracefully", "signal", sig)
		srv.GracefulStop()
		_ = operatorProxy.Close()
		_ = archivistCloser()
		_ = sidecarSrv.Close()
	}()

	slog.Info("Sidecar listening", "address", lis.Addr().String())
	if err := srv.Serve(lis); err != nil {
		slog.Error("Sidecar server error", "error", err)
		os.Exit(1)
	}

	slog.Info("Sidecar stopped")
}
