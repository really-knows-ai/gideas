// Archivist is the "Memory" of Foundry Flow — a Content-Addressable Storage
// (CAS) service that manages artefact content and provenance.
//
// It separates Content (raw bytes, deduplicated by SHA-256 hash) from
// Provenance (version history keyed by workitem + artefact). The Sidecar
// forwards node artefact operations to this service.
//
// Usage:
//
//	go run ./archivist/cmd/main.go
//	ARCHIVIST_PORT=50054 go run ./archivist/cmd/main.go
package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/gideas/flow/archivist/internal/service"
	"github.com/gideas/flow/archivist/internal/store"
	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

const (
	defaultPort = "50054"
	envPort     = "ARCHIVIST_PORT"
)

func main() {
	port := os.Getenv(envPort)
	if port == "" {
		port = defaultPort
	}

	slog.Info("Archivist starting", "port", port)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		slog.Error("Failed to listen", "port", port, "error", err)
		os.Exit(1)
	}

	srv := grpc.NewServer()

	memStore := store.NewMemoryStore()
	archivistSrv := service.NewArchivistServer(memStore)
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
	}()

	slog.Info("Archivist listening", "address", lis.Addr().String())
	if err := srv.Serve(lis); err != nil {
		slog.Error("Archivist server error", "error", err)
		os.Exit(1)
	}

	slog.Info("Archivist stopped")
}
