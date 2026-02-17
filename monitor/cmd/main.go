// Flow Monitor is the central telemetry and friction aggregation service
// for the Foundry Flow Control Plane.
//
// It serves as a mandatory runtime output surface for nodes and a query
// source for the Librarian's law lifecycle triggers. Data is persisted to
// a SQLite database at the path specified by MONITOR_DB_PATH (default:
// /data/monitor.db).
//
// Usage:
//
//	go run ./monitor/cmd/main.go
//	MONITOR_PORT=50055 MONITOR_DB_PATH=/data/monitor.db go run ./monitor/cmd/main.go
package main

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/gideas/flow/monitor/internal/service"
	"github.com/gideas/flow/monitor/internal/store/sqlite"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

const (
	defaultPort   = "50055"
	defaultDBPath = "/data/monitor.db"
	envPort       = "MONITOR_PORT"
	envDBPath     = "MONITOR_DB_PATH"
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

	slog.Info("Flow Monitor starting", "port", port, "db_path", dbPath)

	// Initialise the SQLite store.
	store, err := sqlite.New(dbPath)
	if err != nil {
		slog.Error("Failed to initialise SQLite store", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		slog.Error("Failed to listen", "port", port, "error", err)
		os.Exit(1)
	}

	srv := grpc.NewServer()

	monitorSrv := service.NewMonitorServer(store, newEventID)
	flowv1.RegisterFlowMonitorServiceServer(srv, monitorSrv)

	// Enable gRPC reflection for debugging with grpcurl.
	reflection.Register(srv)

	// Graceful shutdown on SIGTERM/SIGINT.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		slog.Info("Received signal, shutting down gracefully", "signal", sig)
		srv.GracefulStop()
		store.Close()
	}()

	slog.Info("Flow Monitor listening", "address", lis.Addr().String())
	if err := srv.Serve(lis); err != nil {
		slog.Error("Flow Monitor server error", "error", err)
		os.Exit(1)
	}

	slog.Info("Flow Monitor stopped")
}

// newEventID returns a random hex-encoded identifier for event records.
func newEventID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return fmt.Sprintf("%x", b)
}
