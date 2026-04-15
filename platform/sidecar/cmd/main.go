// Sidecar is the in-pod gRPC proxy for Foundry Flow nodes.
//
// It listens on a single port and multiplexes all Flow services
// (SidecarService, OperatorService, ArchivistService, LibrarianService,
// FrictionLedgerService). The SidecarService handles node-facing RPCs
// (Heartbeat, AddFriction, RecordTelemetry) and operator-facing RPCs
// (AssignWork). Other services are proxied to their real gRPC endpoints
// when the corresponding address environment variable is set.
//
// Usage:
//
//	FLOW_NODE_ID=my-node go run ./sidecar/cmd/main.go
//	OPERATOR_ADDRESS=localhost:50052 FLOW_NODE_ID=my-node go run ./sidecar/cmd/main.go
//	EVENT_BUS_ADDRESS=localhost:50056 FLOW_NODE_ID=my-node go run ./sidecar/cmd/main.go
package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/sidecar/internal/buffer"
	"github.com/gideas/flow/sidecar/internal/mock"
	"github.com/gideas/flow/sidecar/internal/proxy"
	"github.com/gideas/flow/sidecar/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

const (
	defaultPort            = "50051"
	defaultOperatorAddress = "localhost:50052"
	envNamespace           = "FLOW_NAMESPACE"
	envNodeID              = "FLOW_NODE_ID"
	envPort                = "FLOW_SIDECAR_PORT"
	envOperatorAddress     = "OPERATOR_ADDRESS"
	envNodeAddress         = "FLOW_NODE_ADDRESS"
	envArchivistAddress    = "ARCHIVIST_ADDRESS"
	envLibrarianAddress    = "LIBRARIAN_ADDRESS"
	envEventBusAddress     = "EVENT_BUS_ADDRESS"
	envFrictionLedgerAddr  = "FRICTION_LEDGER_ADDRESS"
	envFederationAddress   = "FEDERATION_ADDRESS"
	envCapabilities        = "FLOW_CAPABILITIES"
)

func main() {
	port := os.Getenv(envPort)
	if port == "" {
		port = defaultPort
	}

	namespace := os.Getenv(envNamespace)

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
	eventBusAddr := os.Getenv(envEventBusAddress)
	frictionLedgerAddr := os.Getenv(envFrictionLedgerAddr)
	federationAddr := os.Getenv(envFederationAddress)
	capabilities := os.Getenv(envCapabilities)

	slog.Info("Sidecar starting",
		"port", port,
		"namespace", namespace,
		"node_id", nodeID,
		"operator_address", operatorAddr,
		"node_address", nodeAddr,
		"archivist_address", archivistAddr,
		"librarian_address", librarianAddr,
		"event_bus_address", eventBusAddr,
		"friction_ledger_address", frictionLedgerAddr,
		"federation_address", federationAddr,
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
	sidecarSrv := service.NewSidecarServer(namespace, nodeID, nodeAddr)

	// The identity interceptor enriches incoming metadata with
	// authoritative namespace, workitem_id, and node_id from the active
	// assignment session (or entry-bound fallback). This ensures that
	// all proxied RPCs carry the correct identity context regardless of
	// what the node SDK sends.
	// See: specs/05-reference/grpc-api.md#identity-injection
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(service.IdentityInterceptor(sidecarSrv, namespace, nodeID, capabilities)),
	)

	// Event Bus: create proxy and telemetry buffer.
	var eventBusCloser func() error
	var eventBusProxy *proxy.EventBusProxy
	if eventBusAddr != "" {
		ebProxy, err := proxy.NewEventBusProxy(eventBusAddr)
		if err != nil {
			slog.Error("Failed to connect to Event Bus", "address", eventBusAddr, "error", err)
			os.Exit(1)
		}
		eventBusProxy = ebProxy
		eventBusCloser = ebProxy.Close

		// Create telemetry buffer using the underlying gRPC client.
		tb := buffer.NewTelemetryBufferFromClient(ebProxy.Client(), 0) // 0 = default size
		sidecarSrv.TelemetryBuffer = tb

		slog.Info("Event Bus proxy enabled", "address", eventBusAddr)
	} else {
		eventBusCloser = func() error { return nil }
		slog.Info("Event Bus proxy disabled (no EVENT_BUS_ADDRESS set)")
	}

	// Register service handlers.
	// SidecarService handles Heartbeat, AddFriction, RecordTelemetry
	// (node-facing) and AssignWork (operator-facing).
	flowv1.RegisterSidecarServiceServer(srv, sidecarSrv)

	// ArchivistService: proxy to real Archivist if address is set, otherwise mock.
	var archivistCloser func() error
	if archivistAddr != "" {
		archivistProxy, err := proxy.NewArchivistProxy(archivistAddr, sidecarSrv)
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
	// The SidecarServer implements ChildTracker so the OperatorProxy can
	// register child Workitem IDs in the session's local cache.
	operatorProxy, err := proxy.NewOperatorProxy(operatorAddr, sidecarSrv)
	if err != nil {
		slog.Error("Failed to connect to Operator", "address", operatorAddr, "error", err)
		os.Exit(1)
	}
	flowv1.RegisterOperatorServiceServer(srv, operatorProxy)

	// LibrarianService: proxy to real Librarian if address is set, otherwise skip.
	var librarianCloser func() error
	if librarianAddr != "" {
		librarianProxy, err := proxy.NewLibrarianProxy(librarianAddr, eventBusProxy)
		if err != nil {
			slog.Error("Failed to connect to Librarian", "address", librarianAddr, "error", err)
			os.Exit(1)
		}
		flowv1.RegisterLibrarianServiceServer(srv, librarianProxy)
		librarianCloser = librarianProxy.Close
		slog.Info("Librarian proxy enabled", "address", librarianAddr, "event_bus_address", eventBusAddr)
	} else {
		librarianCloser = func() error { return nil }
		slog.Info("Librarian proxy disabled (no LIBRARIAN_ADDRESS set)")
	}

	// FrictionLedgerService: proxy to real Friction Ledger if address is set.
	var frictionLedgerCloser func() error
	if frictionLedgerAddr != "" {
		flProxy, err := proxy.NewFrictionLedgerProxy(frictionLedgerAddr)
		if err != nil {
			slog.Error("Failed to connect to Friction Ledger", "address", frictionLedgerAddr, "error", err)
			os.Exit(1)
		}
		flowv1.RegisterFrictionLedgerServiceServer(srv, flProxy)
		frictionLedgerCloser = flProxy.Close
		slog.Info("Friction Ledger proxy enabled", "address", frictionLedgerAddr)
	} else {
		frictionLedgerCloser = func() error { return nil }
		slog.Info("Friction Ledger proxy disabled (no FRICTION_LEDGER_ADDRESS set)")
	}

	// FederationService: proxy to real Federation service if address is set.
	var federationCloser func() error
	if federationAddr != "" {
		federationProxy, err := proxy.NewFederationProxy(federationAddr)
		if err != nil {
			slog.Error("Failed to connect to Federation", "address", federationAddr, "error", err)
			os.Exit(1)
		}
		flowv1.RegisterFederationServiceServer(srv, federationProxy)
		federationCloser = federationProxy.Close
		slog.Info("Federation proxy enabled", "address", federationAddr)
	} else {
		federationCloser = func() error { return nil }
		slog.Info("Federation proxy disabled (no FEDERATION_ADDRESS set)")
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
		if sidecarSrv.TelemetryBuffer != nil {
			sidecarSrv.TelemetryBuffer.Stop()
		}
		_ = operatorProxy.Close()
		_ = archivistCloser()
		_ = librarianCloser()
		_ = frictionLedgerCloser()
		_ = federationCloser()
		_ = eventBusCloser()
		_ = sidecarSrv.Close()
	}()

	slog.Info("Sidecar listening", "address", lis.Addr().String())
	if err := srv.Serve(lis); err != nil {
		slog.Error("Sidecar server error", "error", err)
		os.Exit(1)
	}

	slog.Info("Sidecar stopped")
}
