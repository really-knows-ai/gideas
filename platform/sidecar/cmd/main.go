// Sidecar is the in-pod gRPC proxy for Foundry Flow nodes.
//
// It listens on a single port and multiplexes all Flow services
// (SidecarService, OperatorService, ArchivistService, LibrarianService,
// FrictionLedgerService, CartographerService). The SidecarService handles
// node-facing RPCs (Heartbeat, AddFriction, RecordTelemetry) and
// operator-facing RPCs (AssignWork). Other services are proxied to their
// real gRPC endpoints when the corresponding address environment variable
// is set.
//
// Usage:
//
//	FLOW_NODE_ID=my-node go run ./sidecar/cmd/main.go
//	OPERATOR_ADDRESS=localhost:50052 FLOW_NODE_ID=my-node go run ./sidecar/cmd/main.go
//	EVENT_BUS_ADDRESS=localhost:50056 FLOW_NODE_ID=my-node go run ./sidecar/cmd/main.go
//	CARTOGRAPHER_ADDRESS=localhost:50051 SIDECAR_SIGNING_KEY=<b64> FLOW_NODE_ID=my-node go run ./sidecar/cmd/main.go
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/sidecar/internal/buffer"
	"github.com/foundry/flow/sidecar/internal/proxy"
	"github.com/foundry/flow/sidecar/internal/queue"
	"github.com/foundry/flow/sidecar/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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
	envCartographerAddress = "CARTOGRAPHER_ADDRESS"
	envQueueServiceAddr    = "FLOW_QUEUE_SERVICE_ADDR"
	envSidecarSigningKey   = "SIDECAR_SIGNING_KEY"

	// defaultQueueHeartbeatInterval is the queue-shard registry heartbeat
	// period, mirroring the SDK's default.
	defaultQueueHeartbeatInterval = 15 * time.Second
)

func main() {
	port := os.Getenv(envPort)
	if port == "" {
		port = defaultPort
	}

	namespace := os.Getenv(envNamespace)

	nodeID := os.Getenv(envNodeID)
	if nodeID == "" {
		nodeID = os.Getenv("FLOW_NODE_NAME")
	}
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
	cartographerAddr := os.Getenv(envCartographerAddress)
	capabilities := os.Getenv(envCapabilities)

	// The Sidecar signs the node's capability attestation
	// (x-flow-capabilities-signature) with the shared sidecar Ed25519 private
	// key so the Cartographer can verify it on ingress (SPEC R3 / Capability
	// Authorisation Chain). The key is injected by the Node operator from the
	// per-namespace cartographer-sidecar-key Secret (data key private-key),
	// base64-encoded for byte-exact env transport. When the CartographerProxy
	// is enabled the key is mandatory: without it every node graph RPC would
	// be rejected by the Cartographer as unsigned, so the Sidecar fails fast
	// rather than booting a proxy that can only deny.
	var signingKey ed25519.PrivateKey
	if cartographerAddr != "" {
		keyBytes, err := base64.StdEncoding.DecodeString(os.Getenv(envSidecarSigningKey))
		if err != nil || len(keyBytes) != ed25519.PrivateKeySize {
			slog.Error("SIDECAR_SIGNING_KEY must be a base64-encoded Ed25519 private key when CARTOGRAPHER_ADDRESS is set",
				"error", err)
			os.Exit(1)
		}
		signingKey = ed25519.PrivateKey(keyBytes)
	}

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
		"cartographer_address", cartographerAddr,
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
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(service.IdentityInterceptor(sidecarSrv, namespace, nodeID, capabilities, signingKey)),
		grpc.StreamInterceptor(service.IdentityStreamInterceptor(sidecarSrv, namespace, nodeID, capabilities, signingKey)),
	)

	// Event Bus: dial and create telemetry buffer.
	var eventBusCloser func() error
	if eventBusAddr != "" {
		conn, err := grpc.NewClient(eventBusAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			slog.Error("Failed to connect to Event Bus", "address", eventBusAddr, "error", err)
			os.Exit(1)
		}
		client := flowv1.NewFlowEventBusServiceClient(conn)
		tb := buffer.NewTelemetryBuffer(client, 0) // 0 = default size
		sidecarSrv.TelemetryBuffer = tb
		eventBusCloser = func() error { return conn.Close() }

		slog.Info("Telemetry buffer enabled", "address", eventBusAddr)
	} else {
		eventBusCloser = func() error { return nil }
		slog.Info("Telemetry buffer disabled (no EVENT_BUS_ADDRESS set)")
	}

	// Register service handlers.
	// SidecarService handles Heartbeat, AddFriction, RecordTelemetry
	// (node-facing) and AssignWork (operator-facing).
	flowv1.RegisterSidecarServiceServer(srv, sidecarSrv)

	// ArchivistService: proxy to real Archivist if address is set.
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
		archivistCloser = func() error { return nil }
		slog.Info("Archivist proxy disabled (no ARCHIVIST_ADDRESS set)")
	}

	// OperatorService is proxied to the real Operator.
	// The SidecarServer tracks child Workitem IDs so the OperatorProxy can
	// register them in the session's local cache.
	operatorProxy, err := proxy.NewOperatorProxy(operatorAddr, sidecarSrv)
	if err != nil {
		slog.Error("Failed to connect to Operator", "address", operatorAddr, "error", err)
		os.Exit(1)
	}
	flowv1.RegisterOperatorServiceServer(srv, operatorProxy)

	// LibrarianService: proxy to real Librarian if address is set, otherwise skip.
	var librarianCloser func() error
	if librarianAddr != "" {
		librarianProxy, err := proxy.NewLibrarianProxy(librarianAddr, sidecarSrv.TelemetryBuffer)
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

	// CartographerService: proxy to the real Cartographer if address is set.
	// When unset or empty, the CartographerProxy is not created and
	// Cartographer-related RPCs are unavailable from that node (SPEC R5).
	cartographerCloser := registerCartographerProxy(srv, cartographerAddr)

	// Queue-shard role: serve the QueuePeerService and heartbeats the
	// queue-service registry when FLOW_QUEUE_SERVICE_ADDR is set. When unset,
	// the sidecar serves only its node — nothing queue-related is registered.
	queueServiceAddr := os.Getenv(envQueueServiceAddr)
	queuePeerCloser, err := registerQueuePeer(srv, queueServiceAddr, nodeID, nodeAddr, port)
	if err != nil {
		slog.Error("Failed to register queue peer", "error", err)
		os.Exit(1)
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
		_ = cartographerCloser()
		_ = queuePeerCloser()
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

// registerCartographerProxy wires the CartographerServiceServer proxy onto srv
// when cartographerAddr is non-empty. When unset or empty, the CartographerProxy
// is not created and Cartographer-related RPCs are unavailable from that node
// (SPEC R5). The function is a seam extracted from main() — where the block
// could not be reached by a test — so the R5 branch is pinnable. It returns a
// closer for the proxy's gRPC connection (a no-op when the proxy is not
// created).
func registerCartographerProxy(srv *grpc.Server, cartographerAddr string) func() error {
	if cartographerAddr == "" {
		slog.Info("Cartographer proxy disabled (no CARTOGRAPHER_ADDRESS set)")
		return func() error { return nil }
	}
	cartographerProxy, err := proxy.NewCartographerProxy(cartographerAddr)
	if err != nil {
		slog.Error("Failed to connect to Cartographer", "address", cartographerAddr, "error", err)
		os.Exit(1)
	}
	flowv1.RegisterCartographerServiceServer(srv, cartographerProxy)
	slog.Info("Cartographer proxy enabled", "address", cartographerAddr)
	return cartographerProxy.Close
}

// registerQueuePeer wires the sidecar's queue-shard role onto srv when
// queueServiceAddr is non-empty (FLOW_QUEUE_SERVICE_ADDR): it creates the
// in-memory mirror store + QueuePeerService server, registers the peer service,
// mints the random shard ID + shard address, and starts the registry client +
// heartbeat loop. When queueServiceAddr is empty the sidecar serves only its
// node — nothing queue-related is registered and a no-op closer is returned.
//
// The function is a seam extracted from main() (like registerCartographerProxy)
// so the queue-shard branch is pinnable from a test with a real *grpc.Server.
// host is the node's dialable host (FLOW_NODE_ADDRESS), port the peer listen
// port (FLOW_SIDECAR_PORT, default 50051); together they form the registered
// ShardAddr. The node registers a shard of its own queue: queueName is nodeID
// (FLOW_NODE_ID semantics — each node serves a shard of the queue bearing its
// own id). It returns a closer that stops the heartbeat loop and closes the
// store + registry conn.
func registerQueuePeer(
	srv *grpc.Server, queueServiceAddr, nodeID, host, port string,
) (func() error, error) {
	if queueServiceAddr == "" {
		slog.Info("Queue peer disabled (no FLOW_QUEUE_SERVICE_ADDR set)")
		return func() error { return nil }, nil
	}

	shardID := queue.NewShardID()
	shardAddr := queue.ShardAddr(host, port)
	queueName := nodeID

	store, err := queue.NewInMemoryStore(shardID, queueName)
	if err != nil {
		return nil, fmt.Errorf("create queue mirror store: %w", err)
	}

	peer := queue.NewPeerServer(store, shardID, queueName)
	flowv1.RegisterQueuePeerServiceServer(srv, peer)

	rc, err := queue.NewRegistryClient(
		queueServiceAddr, nil, queueName, shardID, shardAddr, defaultQueueHeartbeatInterval,
	)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("create queue registry client: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	// A shard that fails to register on boot is not a boot failure: the
	// heartbeat loop retries on the next tick.
	if err := rc.Register(ctx); err != nil {
		slog.Warn("queue-service registration failed; will retry on next heartbeat",
			"queue", queueName, "shard", shardID, "error", err)
	}

	wg.Add(1)
	go rc.HeartbeatLoop(ctx, &wg)

	slog.Info("Queue peer enabled",
		"address", queueServiceAddr,
		"node_id", nodeID,
		"shard_id", shardID,
		"shard_addr", shardAddr,
	)

	return func() error {
		cancel()
		wg.Wait()
		_ = rc.Close()
		return store.Close()
	}, nil
}
