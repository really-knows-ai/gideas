// Friction Ledger is the sole friction aggregation and threshold-evaluation
// service for Foundry Flow. It subscribes to the Event Bus telemetry channel
// for friction events, persists them to SQLite, evaluates per-law thresholds,
// and publishes threshold-crossing events to the friction channel.
//
// Usage:
//
//	go run ./frictionledger/cmd/main.go
//	FRICTION_LEDGER_PORT=50057 FRICTION_LEDGER_DB_PATH=/data/frictionledger.db \
//	  EVENT_BUS_ADDRESS=flow-eventbus:50056 go run ./frictionledger/cmd/main.go
package main

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/gideas/flow/frictionledger/internal/service"
	"github.com/gideas/flow/frictionledger/internal/store/sqlite"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

const (
	defaultPort       = "50057"
	defaultDBPath     = "/data/frictionledger.db"
	defaultBusAddress = "localhost:50056"

	envPort       = "FRICTION_LEDGER_PORT"
	envDBPath     = "FRICTION_LEDGER_DB_PATH"
	envBusAddress = "EVENT_BUS_ADDRESS"
)

func main() {
	port := envOrDefault(envPort, defaultPort)
	dbPath := envOrDefault(envDBPath, defaultDBPath)
	busAddr := envOrDefault(envBusAddress, defaultBusAddress)

	slog.Info("Friction Ledger starting",
		"port", port,
		"db_path", dbPath,
		"event_bus_address", busAddr,
	)

	store, err := sqlite.New(dbPath)
	if err != nil {
		slog.Error("Failed to initialise SQLite store", "error", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	thresholds := loadThresholds()

	ledgerSrv := service.NewFrictionLedgerServer(store, newEventID, thresholds)

	// Connect to Event Bus and start subscription.
	busConn, busClient, err := service.ConnectEventBus(busAddr)
	if err != nil {
		slog.Error("Failed to connect to Event Bus", "error", err)
		os.Exit(1)
	}
	defer func() { _ = busConn.Close() }()

	ledgerSrv.StartSubscription(busClient)

	// Start gRPC server.
	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		slog.Error("Failed to listen", "port", port, "error", err)
		os.Exit(1)
	}

	srv := grpc.NewServer()
	flowv1.RegisterFrictionLedgerServiceServer(srv, ledgerSrv)
	reflection.Register(srv)

	// Graceful shutdown on SIGTERM/SIGINT.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		slog.Info("Received signal, shutting down gracefully", "signal", sig)
		ledgerSrv.Stop()
		srv.GracefulStop()
		_ = busConn.Close()
		_ = store.Close()
	}()

	slog.Info("Friction Ledger listening", "address", lis.Addr().String())
	if err := srv.Serve(lis); err != nil {
		slog.Error("Friction Ledger server error", "error", err)
		os.Exit(1)
	}

	slog.Info("Friction Ledger stopped")
}

// newEventID returns a random hex-encoded identifier for event records.
func newEventID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return fmt.Sprintf("%x", b)
}

// loadThresholds reads per-tier friction threshold configuration from
// environment variables. Format: FRICTION_THRESHOLD_TIER1 through
// FRICTION_THRESHOLD_TIER5 with float64 string values.
func loadThresholds() service.ThresholdConfig {
	thresholds := make(service.ThresholdConfig)
	tiers := []struct {
		tier int32
		env  string
	}{
		{int32(flowv1.LawTier_LAW_TIER_FINDING), "FRICTION_THRESHOLD_TIER1"},
		{int32(flowv1.LawTier_LAW_TIER_RULING), "FRICTION_THRESHOLD_TIER2"},
		{int32(flowv1.LawTier_LAW_TIER_LOCAL_STATUTE), "FRICTION_THRESHOLD_TIER3"},
		{int32(flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION), "FRICTION_THRESHOLD_TIER4"},
		{int32(flowv1.LawTier_LAW_TIER_FEDERAL_ACCORD), "FRICTION_THRESHOLD_TIER5"},
	}

	for _, t := range tiers {
		v := os.Getenv(t.env)
		if v == "" {
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			slog.Warn("Invalid friction threshold, ignoring",
				"env", t.env, "value", v, "error", err)
			continue
		}
		thresholds[t.tier] = f
		slog.Info("Friction threshold configured",
			"tier", t.tier, "threshold", f)
	}

	if len(thresholds) == 0 {
		return nil
	}
	return thresholds
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
