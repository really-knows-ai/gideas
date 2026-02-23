// Flow Event Bus is a durable pub/sub bus for all Flow runtime events.
//
// Events are persisted to SQLite before fan-out. Retention is per-channel
// and operator-configurable via environment variables.
//
// Usage:
//
//	go run ./eventbus/cmd/main.go
//	EVENT_BUS_PORT=50056 EVENT_BUS_DB_PATH=/data/eventbus.db go run ./eventbus/cmd/main.go
package main

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gideas/flow/eventbus/internal/service"
	"github.com/gideas/flow/eventbus/internal/store/sqlite"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

const (
	defaultPort   = "50056"
	defaultDBPath = "/data/eventbus.db"
	envPort       = "EVENT_BUS_PORT"
	envDBPath     = "EVENT_BUS_DB_PATH"
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

	slog.Info("Flow Event Bus starting", "port", port, "db_path", dbPath)

	store, err := sqlite.New(dbPath)
	if err != nil {
		slog.Error("Failed to initialise SQLite store", "error", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	retention := loadRetention()

	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		slog.Error("Failed to listen", "port", port, "error", err)
		os.Exit(1)
	}

	srv := grpc.NewServer()

	busSrv := service.NewEventBusServer(store, newEventID, retention)
	flowv1.RegisterFlowEventBusServiceServer(srv, busSrv)

	// Enable gRPC reflection for debugging with grpcurl.
	reflection.Register(srv)

	// Graceful shutdown on SIGTERM/SIGINT.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		slog.Info("Received signal, shutting down gracefully", "signal", sig)
		busSrv.Stop()
		srv.GracefulStop()
		_ = store.Close()
	}()

	slog.Info("Flow Event Bus listening", "address", lis.Addr().String())
	if err := srv.Serve(lis); err != nil {
		slog.Error("Flow Event Bus server error", "error", err)
		os.Exit(1)
	}

	slog.Info("Flow Event Bus stopped")
}

// newEventID returns a random hex-encoded identifier for event records.
func newEventID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return fmt.Sprintf("%x", b)
}

// loadRetention reads per-channel retention configuration from
// environment variables. Missing or unparseable values are treated
// as no limit for that dimension.
func loadRetention() map[int32]service.RetentionConfig {
	type channelEnv struct {
		channel     int32
		durationEnv string
		sizeEnv     string
	}
	channels := []channelEnv{
		{
			int32(flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY),
			"EVENT_BUS_RETENTION_TELEMETRY_DURATION",
			"EVENT_BUS_RETENTION_TELEMETRY_SIZE",
		},
		{
			int32(flowv1.EventChannel_EVENT_CHANNEL_AUDIT),
			"EVENT_BUS_RETENTION_AUDIT_DURATION",
			"EVENT_BUS_RETENTION_AUDIT_SIZE",
		},
		{
			int32(flowv1.EventChannel_EVENT_CHANNEL_FRICTION),
			"EVENT_BUS_RETENTION_FRICTION_DURATION",
			"EVENT_BUS_RETENTION_FRICTION_SIZE",
		},
	}

	ret := make(map[int32]service.RetentionConfig)
	anySet := false
	for _, ce := range channels {
		var cfg service.RetentionConfig
		if v := os.Getenv(ce.durationEnv); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				slog.Warn("Invalid retention duration, ignoring", "env", ce.durationEnv, "value", v, "error", err)
			} else {
				cfg.Duration = d
				anySet = true
			}
		}
		if v := os.Getenv(ce.sizeEnv); v != "" {
			sz, err := parseByteSize(v)
			if err != nil {
				slog.Warn("Invalid retention size, ignoring", "env", ce.sizeEnv, "value", v, "error", err)
			} else {
				cfg.Size = sz
				anySet = true
			}
		}
		ret[ce.channel] = cfg
	}
	if !anySet {
		return nil
	}
	return ret
}

// parseByteSize parses human-readable byte sizes like "100MB", "1GB".
func parseByteSize(s string) (int64, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid size: %q", s)
	}

	var multiplier int64
	suffix := s[len(s)-2:]
	numStr := s[:len(s)-2]

	switch suffix {
	case "KB", "kb":
		multiplier = 1024
	case "MB", "mb":
		multiplier = 1024 * 1024
	case "GB", "gb":
		multiplier = 1024 * 1024 * 1024
	case "TB", "tb":
		multiplier = 1024 * 1024 * 1024 * 1024
	default:
		// Try single-char suffix.
		suffix = s[len(s)-1:]
		numStr = s[:len(s)-1]
		switch suffix {
		case "B", "b":
			multiplier = 1
		default:
			return 0, fmt.Errorf("unknown size suffix: %q", s)
		}
	}

	var n int64
	if _, err := fmt.Sscanf(numStr, "%d", &n); err != nil {
		return 0, fmt.Errorf("parse size number: %w", err)
	}
	return n * multiplier, nil
}
