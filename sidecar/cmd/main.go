// Sidecar is the in-pod gRPC proxy for Foundry Flow nodes.
//
// It listens on a single port and multiplexes all Flow services
// (SidecarService, OperatorService, ArchivistService, LibrarianService,
// JuryService, ClerkService, FlowMonitorService). The SidecarService
// handles both node-facing RPCs (Heartbeat) and operator-facing RPCs
// (AssignWork). Other services are proxied to their real gRPC endpoints
// when the corresponding address environment variable is set.
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
	envLibrarianAddress    = "LIBRARIAN_ADDRESS"
	envMonitorAddress      = "MONITOR_ADDRESS"
	envJuryAddress         = "JURY_ADDRESS"
	envClerkAddress        = "CLERK_ADDRESS"
	envCapabilities        = "FLOW_CAPABILITIES"
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
	librarianAddr := os.Getenv(envLibrarianAddress)
	monitorAddr := os.Getenv(envMonitorAddress)
	juryAddr := os.Getenv(envJuryAddress)
	clerkAddr := os.Getenv(envClerkAddress)
	capabilities := os.Getenv(envCapabilities)

	slog.Info("Sidecar starting",
		"port", port,
		"node_id", nodeID,
		"operator_address", operatorAddr,
		"node_address", nodeAddr,
		"archivist_address", archivistAddr,
		"librarian_address", librarianAddr,
		"monitor_address", monitorAddr,
		"jury_address", juryAddr,
		"clerk_address", clerkAddr,
		"capabilities", capabilities,
		"phase", "brain-stem",
	)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		slog.Error("Failed to listen", "port", port, "error", err)
		os.Exit(1)
	}

	// Create the SidecarServer first so we can wire its session store
	// into the identity injection interceptor.
	sidecarSrv := service.NewSidecarServer(nodeID, nodeAddr)

	// The identity interceptor enriches incoming metadata with
	// authoritative flow_id, workitem_id, and node_id from the active
	// assignment session. This ensures that all proxied RPCs carry the
	// correct identity context regardless of what the node SDK sends.
	// See: specs/05-reference/grpc-api.md#identity-injection
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(service.IdentityInterceptor(sidecarSrv, capabilities)),
	)

	// Register service handlers.
	// SidecarService handles Heartbeat (node-facing) and AssignWork (operator-facing).
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

	// LibrarianService: proxy to real Librarian if address is set, otherwise skip.
	var librarianCloser func() error
	if librarianAddr != "" {
		librarianProxy, err := proxy.NewLibrarianProxy(librarianAddr, monitorAddr)
		if err != nil {
			slog.Error("Failed to connect to Librarian", "address", librarianAddr, "error", err)
			os.Exit(1)
		}
		flowv1.RegisterLibrarianServiceServer(srv, librarianProxy)
		librarianCloser = librarianProxy.Close
		slog.Info("Librarian proxy enabled", "address", librarianAddr, "monitor_address", monitorAddr)
	} else {
		librarianCloser = func() error { return nil }
		slog.Info("Librarian proxy disabled (no LIBRARIAN_ADDRESS set)")
	}

	// JuryService: proxy to real Jury if address is set, otherwise skip.
	var juryCloser func() error
	if juryAddr != "" {
		juryProxy, err := proxy.NewJuryProxy(juryAddr)
		if err != nil {
			slog.Error("Failed to connect to Jury", "address", juryAddr, "error", err)
			os.Exit(1)
		}
		flowv1.RegisterJuryServiceServer(srv, juryProxy)
		juryCloser = juryProxy.Close
		slog.Info("Jury proxy enabled", "address", juryAddr)
	} else {
		juryCloser = func() error { return nil }
		slog.Info("Jury proxy disabled (no JURY_ADDRESS set)")
	}

	// ClerkService: proxy to real Clerk if address is set, otherwise skip.
	var clerkCloser func() error
	if clerkAddr != "" {
		clerkProxy, err := proxy.NewClerkProxy(clerkAddr)
		if err != nil {
			slog.Error("Failed to connect to Clerk", "address", clerkAddr, "error", err)
			os.Exit(1)
		}
		flowv1.RegisterClerkServiceServer(srv, clerkProxy)
		clerkCloser = clerkProxy.Close
		slog.Info("Clerk proxy enabled", "address", clerkAddr)
	} else {
		clerkCloser = func() error { return nil }
		slog.Info("Clerk proxy disabled (no CLERK_ADDRESS set)")
	}

	// FlowMonitorService: proxy to real Monitor if address is set, otherwise skip.
	var monitorCloser func() error
	if monitorAddr != "" {
		monitorProxy, err := proxy.NewMonitorProxy(monitorAddr)
		if err != nil {
			slog.Error("Failed to connect to Monitor", "address", monitorAddr, "error", err)
			os.Exit(1)
		}
		flowv1.RegisterFlowMonitorServiceServer(srv, monitorProxy)
		monitorCloser = monitorProxy.Close
		slog.Info("Monitor proxy enabled", "address", monitorAddr)
	} else {
		monitorCloser = func() error { return nil }
		slog.Info("Monitor proxy disabled (no MONITOR_ADDRESS set)")
	}

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
		_ = librarianCloser()
		_ = juryCloser()
		_ = clerkCloser()
		_ = monitorCloser()
		_ = sidecarSrv.Close()
	}()

	slog.Info("Sidecar listening", "address", lis.Addr().String())
	if err := srv.Serve(lis); err != nil {
		slog.Error("Sidecar server error", "error", err)
		os.Exit(1)
	}

	slog.Info("Sidecar stopped")
}
