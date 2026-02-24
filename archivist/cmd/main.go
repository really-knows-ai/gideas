// Archivist is the "Memory" of Foundry Flow — a Content-Addressable Storage
// (CAS) service that manages artefact content and provenance.
//
// It separates Content (raw bytes, deduplicated by SHA-256 hash) from
// Provenance (version history keyed by workitem + artefact). The Sidecar
// forwards node artefact operations to this service. Data is persisted to a
// SQLite database at the path specified by ARCHIVIST_DB_PATH (default:
// /data/archivist.db).
//
// Usage:
//
//	go run ./archivist/cmd/main.go
//	ARCHIVIST_PORT=50054 ARCHIVIST_DB_PATH=/data/archivist.db go run ./archivist/cmd/main.go
package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/gideas/flow/archivist/internal/service"
	"github.com/gideas/flow/archivist/internal/store/sqlite"
	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/pkg/eventbus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
)

const (
	defaultPort   = "50054"
	defaultDBPath = "/data/archivist.db"

	envPort            = "ARCHIVIST_PORT"
	envDBPath          = "ARCHIVIST_DB_PATH"
	envEventBusAddress = "EVENT_BUS_ADDRESS"
)

func main() {
	port := os.Getenv(envPort)
	if port == "" {
		port = defaultPort
	}

	dbPath := os.Getenv(envDBPath)
	if dbPath == "" {
		dbPath = defaultDBPath
	}

	slog.Info("Archivist starting", "port", port, "db_path", dbPath)

	// Initialise the SQLite store.
	store, err := sqlite.New(dbPath)
	if err != nil {
		slog.Error("Failed to initialise SQLite store", "error", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	// Connect to the Event Bus for audit event publishing.
	var opts []service.ArchivistOption
	var eventBusCloser func() error
	var auditPub *eventbus.AsyncPublisher
	eventBusAddr := os.Getenv(envEventBusAddress)
	if eventBusAddr != "" {
		ebConn, ebErr := grpc.NewClient(
			eventBusAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if ebErr != nil {
			slog.Error("Failed to connect to Event Bus", "address", eventBusAddr, "error", ebErr)
			os.Exit(1)
		}
		eventBusCloser = ebConn.Close
		ebClient := flowv1.NewFlowEventBusServiceClient(ebConn)
		auditPub = eventbus.NewAsyncPublisher(ebClient)
		opts = append(opts, service.WithAuditPublisher(auditPub))
		slog.Info("Event Bus connected for audit publishing", "address", eventBusAddr)
	} else {
		slog.Info("Event Bus not configured, audit publishing disabled")
	}

	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		slog.Error("Failed to listen", "port", port, "error", err)
		os.Exit(1)
	}

	srv := grpc.NewServer()

	archivistSrv := service.NewArchivistServer(store, opts...)
	flowv1.RegisterArchivistServiceServer(srv, archivistSrv)

	// Enable gRPC reflection for debugging with grpcurl.
	reflection.Register(srv)

	// Graceful shutdown on SIGTERM/SIGINT.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		slog.Info("Received signal, shutting down gracefully", "signal", sig)
		srv.GracefulStop()
		if auditPub != nil {
			auditPub.Stop()
		}
		_ = store.Close()
		if eventBusCloser != nil {
			_ = eventBusCloser()
		}
	}()

	slog.Info("Archivist listening", "address", lis.Addr().String())
	if err := srv.Serve(lis); err != nil {
		slog.Error("Archivist server error", "error", err)
		os.Exit(1)
	}

	slog.Info("Archivist stopped")
}
