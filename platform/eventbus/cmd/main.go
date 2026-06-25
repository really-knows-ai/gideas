// Flow Event Bus is a durable pub/sub bus for all Flow runtime events.
//
// Events are persisted to SQLite before fan-out. Retention is per-channel
// and operator-configurable via a single JSON environment variable.
//
// Usage:
//
//	go run ./eventbus/cmd/main.go
//	EVENT_BUS_PORT=50056 EVENT_BUS_DB_PATH=/data/eventbus.db go run ./eventbus/cmd/main.go
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gideas/flow/eventbus/internal/service"
	"github.com/gideas/flow/eventbus/internal/store/sqlite"
	"github.com/gideas/flow/pkg/randid"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

const (
	defaultPort   = "50056"
	defaultDBPath = "/data/eventbus.db"
	envPort       = "EVENT_BUS_PORT"
	envDBPath     = "EVENT_BUS_DB_PATH"

	// envRetentionConfig holds a JSON object mapping channel names to
	// retention policies. Example:
	//   {"telemetry":{"duration":"24h","size":"100MB"},"audit":{"duration":"168h"}}
	envRetentionConfig = "EVENT_BUS_RETENTION_CONFIG"
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

	busSrv := service.NewEventBusServer(store, randid.NewRandomID, retention)
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

// retentionEntry mirrors the JSON structure for a single channel's
// retention policy.
type retentionEntry struct {
	Duration string `json:"duration"`
	Size     string `json:"size"`
}

// loadRetention reads per-channel retention configuration from a single
// JSON environment variable. The JSON is a map of channel name to an
// object with optional "duration" and "size" fields. Example:
//
//	{"telemetry":{"duration":"24h","size":"100MB"},"audit":{"duration":"168h"}}
//
// Returns nil if the env var is unset or empty.
func loadRetention() map[string]service.RetentionConfig {
	raw := os.Getenv(envRetentionConfig)
	if raw == "" {
		return nil
	}

	var entries map[string]retentionEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		slog.Warn("Invalid retention config JSON, ignoring",
			"env", envRetentionConfig, "error", err)
		return nil
	}

	ret := make(map[string]service.RetentionConfig, len(entries))
	anySet := false
	for ch, entry := range entries {
		var cfg service.RetentionConfig
		if entry.Duration != "" {
			d, err := time.ParseDuration(entry.Duration)
			if err != nil {
				slog.Warn("Invalid retention duration, ignoring",
					"channel", ch, "value", entry.Duration, "error", err)
			} else {
				cfg.Duration = d
				anySet = true
			}
		}
		if entry.Size != "" {
			sz, err := parseByteSize(entry.Size)
			if err != nil {
				slog.Warn("Invalid retention size, ignoring",
					"channel", ch, "value", entry.Size, "error", err)
			} else {
				cfg.Size = sz
				anySet = true
			}
		}
		ret[ch] = cfg
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
