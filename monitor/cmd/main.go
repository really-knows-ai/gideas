// Flow Monitor is a stateless pipeline adapter for the Foundry Flow
// Control Plane. It subscribes to the Event Bus telemetry and audit
// channels and exports:
//   - Prometheus metrics on HTTP /metrics (port 2112 by default)
//   - JSON Lines to stdout for audit events
//
// The Monitor persists only a small checkpoint file for replay position
// (not a data store). See: specs/02-flow/04-system-services.md
// (Service Invariant #16).
//
// Usage:
//
//	EVENT_BUS_ADDRESS=localhost:50056 go run ./monitor/cmd/main.go
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/gideas/flow/monitor/internal/subscriber"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	defaultPort           = "2112"
	defaultCheckpointPath = "/data/monitor-checkpoint.json"
	defaultEventBusAddr   = "localhost:50056"

	envPort           = "FLOW_MONITOR_PORT"
	envCheckpointPath = "FLOW_MONITOR_CHECKPOINT_PATH"
	envEventBusAddr   = "EVENT_BUS_ADDRESS"
)

func main() {
	port := os.Getenv(envPort)
	if port == "" {
		port = defaultPort
	}

	cpPath := os.Getenv(envCheckpointPath)
	if cpPath == "" {
		cpPath = defaultCheckpointPath
	}

	busAddr := os.Getenv(envEventBusAddr)
	if busAddr == "" {
		busAddr = defaultEventBusAddr
	}

	slog.Info("Flow Monitor starting",
		"port", port,
		"checkpoint_path", cpPath,
		"event_bus_address", busAddr,
	)

	// Load checkpoint for replay position.
	checkpoint, err := subscriber.NewFileCheckpoint(cpPath)
	if err != nil {
		slog.Error("Failed to load checkpoint", "error", err)
		os.Exit(1)
	}

	// Connect to Event Bus.
	busConn, busClient, err := subscriber.ConnectEventBus(busAddr)
	if err != nil {
		slog.Error("Failed to connect to Event Bus", "error", err)
		os.Exit(1)
	}
	defer func() { _ = busConn.Close() }()

	// Start subscribers.
	telemetrySub := subscriber.NewTelemetrySubscriber(busClient, checkpoint)
	telemetrySub.Start()

	auditSub := subscriber.NewAuditSubscriber(busClient, checkpoint)
	auditSub.Start()

	// HTTP server for Prometheus metrics.
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	httpSrv := &http.Server{
		Addr:    fmt.Sprintf(":%s", port),
		Handler: mux,
	}

	// Graceful shutdown on SIGTERM/SIGINT.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		slog.Info("Received signal, shutting down gracefully", "signal", sig)
		telemetrySub.Stop()
		auditSub.Stop()
		_ = busConn.Close()
		_ = httpSrv.Close()
	}()

	slog.Info("Flow Monitor listening", "address", httpSrv.Addr)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("Flow Monitor HTTP server error", "error", err)
		os.Exit(1)
	}

	slog.Info("Flow Monitor stopped")
}
