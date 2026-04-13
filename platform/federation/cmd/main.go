// Flow Federation Service is the control-plane authority for Flow federations.
//
// It manages membership, endpoint discovery, authority publisher roles,
// published law distribution, and petition-outcome events.
//
// Usage:
//
//	go run ./federation/cmd/main.go
//	FEDERATION_PORT=50061 FEDERATION_DB_PATH=/data/federation.db go run ./federation/cmd/main.go
package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/gideas/flow/federation/internal/service"
	"github.com/gideas/flow/federation/internal/store/sqlite"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

const (
	defaultPort   = "50061"
	defaultDBPath = "/data/federation.db"
	envPort       = "FEDERATION_PORT"
	envDBPath     = "FEDERATION_DB_PATH"
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

	slog.Info("Flow Federation Service starting", "port", port, "db_path", dbPath)

	store, err := sqlite.New(dbPath)
	if err != nil {
		slog.Error("Failed to initialise SQLite store", "error", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		slog.Error("Failed to listen", "port", port, "error", err)
		os.Exit(1)
	}

	srv := grpc.NewServer()

	fedSrv := service.NewFederationServer(store)
	flowv1.RegisterFederationServiceServer(srv, fedSrv)

	// Enable gRPC reflection for debugging with grpcurl.
	reflection.Register(srv)

	// Graceful shutdown on SIGTERM/SIGINT.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		slog.Info("Received signal, shutting down gracefully", "signal", sig)
		srv.GracefulStop()
		_ = store.Close()
	}()

	slog.Info("Flow Federation Service listening", "address", lis.Addr().String())
	if err := srv.Serve(lis); err != nil {
		slog.Error("Flow Federation Service server error", "error", err)
		os.Exit(1)
	}

	slog.Info("Flow Federation Service stopped")
}
