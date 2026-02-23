// Librarian is the law management service for the Foundry Flow Control
// Plane.
//
// It manages the Flow's body of law: creation, versioning, querying,
// retirement, and lifecycle actions. Data is persisted to a SQLite database
// at the path specified by LIBRARIAN_DB_PATH (default: /data/librarian.db).
//
// It also runs hearing triggers: subscribing to the friction channel on the
// Event Bus for threshold-crossing events, and periodically scanning laws
// for review-TTL-expiry. Both triggers create hearing Workitems via the
// Operator's CreateHearingWorkitem RPC.
//
// Usage:
//
//	go run ./librarian/cmd/main.go
//	LIBRARIAN_PORT=50058 LIBRARIAN_DB_PATH=/data/librarian.db go run ./librarian/cmd/main.go
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gideas/flow/librarian/internal/embed"
	"github.com/gideas/flow/librarian/internal/service"
	"github.com/gideas/flow/librarian/internal/store/sqlite"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
)

const (
	defaultPort                = "50058"
	defaultDBPath              = "/data/librarian.db"
	defaultOllamaURL           = "http://localhost:11434"
	defaultOllamaModel         = "qwen3-embedding:4b"
	defaultSimilarityThreshold = 0.85

	envPort                = "LIBRARIAN_PORT"
	envDBPath              = "LIBRARIAN_DB_PATH"
	envOllamaURL           = "OLLAMA_URL"
	envOllamaModel         = "OLLAMA_MODEL"
	envSimilarityThreshold = "LIBRARIAN_SIMILARITY_THRESHOLD"
	envEventBusAddress     = "EVENT_BUS_ADDRESS"
	envOperatorAddress     = "OPERATOR_ADDRESS"

	// Per-tier review TTL environment variables.
	envReviewTTLTier1 = "REVIEW_TTL_TIER1"
	envReviewTTLTier2 = "REVIEW_TTL_TIER2"
	envReviewTTLTier3 = "REVIEW_TTL_TIER3"
	envReviewTTLTier4 = "REVIEW_TTL_TIER4"
	envReviewTTLTier5 = "REVIEW_TTL_TIER5"
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

	ollamaURL := os.Getenv(envOllamaURL)
	if ollamaURL == "" {
		ollamaURL = defaultOllamaURL
	}

	ollamaModel := os.Getenv(envOllamaModel)
	if ollamaModel == "" {
		ollamaModel = defaultOllamaModel
	}

	threshold := defaultSimilarityThreshold
	if s := os.Getenv(envSimilarityThreshold); s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			threshold = v
		}
	}

	slog.Info("Librarian starting",
		"port", port,
		"db_path", dbPath,
		"ollama_url", ollamaURL,
		"ollama_model", ollamaModel,
		"similarity_threshold", threshold,
	)

	// Initialise the SQLite store.
	store, err := sqlite.New(dbPath)
	if err != nil {
		slog.Error("Failed to initialise SQLite store", "error", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	// Initialise the embedder. Nil-safe if Ollama is unreachable.
	var embedder embed.Embedder
	if ollamaURL != "" {
		embedder = embed.NewOllamaEmbedder(ollamaURL, ollamaModel)
		slog.Info("Embedder enabled", "url", ollamaURL, "model", ollamaModel)
	} else {
		slog.Info("Embedder disabled (no OLLAMA_URL set)")
	}

	// -------------------------------------------------------------------
	// Event Bus + Operator connections for hearing triggers
	// -------------------------------------------------------------------

	var (
		ebClient  flowv1.FlowEventBusServiceClient
		opClient  flowv1.OperatorServiceClient
		serverOpt []service.LibrarianOption
	)

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
		ebClient = flowv1.NewFlowEventBusServiceClient(ebConn)
		serverOpt = append(serverOpt, service.WithAuditPublisher(ebClient))
		slog.Info("Event Bus connected", "address", eventBusAddr)
	} else {
		slog.Info("Event Bus not configured, audit publishing and friction subscription disabled")
	}

	operatorAddr := os.Getenv(envOperatorAddress)
	if operatorAddr != "" {
		opConn, opErr := grpc.NewClient(
			operatorAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if opErr != nil {
			slog.Error("Failed to connect to Operator", "address", operatorAddr, "error", opErr)
			os.Exit(1)
		}
		opClient = flowv1.NewOperatorServiceClient(opConn)
		slog.Info("Operator connected", "address", operatorAddr)
	} else {
		slog.Info("Operator not configured, hearing triggers disabled")
	}

	// Parse per-tier review TTLs from environment.
	ttlConfig := service.ReviewTTLConfig{
		Tier1: parseDuration(envReviewTTLTier1),
		Tier2: parseDuration(envReviewTTLTier2),
		Tier3: parseDuration(envReviewTTLTier3),
		Tier4: parseDuration(envReviewTTLTier4),
		Tier5: parseDuration(envReviewTTLTier5),
	}

	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		slog.Error("Failed to listen", "port", port, "error", err)
		os.Exit(1)
	}

	srv := grpc.NewServer()

	librarianSrv := service.NewLibrarianServer(store, embedder, newLawID, threshold, serverOpt...)
	flowv1.RegisterLibrarianServiceServer(srv, librarianSrv)

	// Enable gRPC reflection for debugging with grpcurl.
	reflection.Register(srv)

	// Create a cancellable context for hearing triggers.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start hearing triggers in background.
	hearingTrigger := service.NewHearingTrigger(service.HearingTriggerConfig{
		Subscriber: ebClient,
		Operator:   opClient,
		Store:      store,
		TTLConfig:  ttlConfig,
		Auditor:    ebClient,
	})
	go hearingTrigger.Run(ctx)

	// Graceful shutdown on SIGTERM/SIGINT.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		slog.Info("Received signal, shutting down gracefully", "signal", sig)
		cancel() // Stop hearing triggers.
		srv.GracefulStop()
		_ = store.Close()
	}()

	slog.Info("Librarian listening", "address", lis.Addr().String())
	if err := srv.Serve(lis); err != nil {
		slog.Error("Librarian server error", "error", err)
		os.Exit(1)
	}

	slog.Info("Librarian stopped")
}

// newLawID returns a random hex-encoded identifier for law records.
func newLawID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return fmt.Sprintf("%x", b)
}

// parseDuration reads a Go duration string from an environment variable.
// Returns zero if unset or unparseable.
func parseDuration(envKey string) time.Duration {
	s := os.Getenv(envKey)
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		slog.Warn("Invalid duration", "env", envKey, "value", s, "error", err)
		return 0
	}
	return d
}
