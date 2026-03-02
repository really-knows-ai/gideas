// Librarian is the law management service for the Foundry Flow Control
// Plane.
//
// It manages the Flow's body of law: creation, versioning, querying,
// retirement, and lifecycle actions. Data is persisted to a SQLite database
// at the path specified by LIBRARIAN_DB_PATH (default: /data/librarian.db).
//
// Usage:
//
//	go run ./librarian/cmd/main.go
//	LIBRARIAN_PORT=50058 LIBRARIAN_DB_PATH=/data/librarian.db go run ./librarian/cmd/main.go
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

	"github.com/gideas/flow/librarian/internal/embed"
	"github.com/gideas/flow/librarian/internal/service"
	"github.com/gideas/flow/librarian/internal/store/sqlite"
	"github.com/gideas/flow/pkg/eventbus"

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
	// Event Bus connection for audit publishing
	// -------------------------------------------------------------------

	var (
		serverOpt []service.LibrarianOption
		auditPub  *eventbus.AsyncPublisher
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
		ebClient := flowv1.NewFlowEventBusServiceClient(ebConn)
		auditPub = eventbus.NewAsyncPublisher(ebClient)
		serverOpt = append(serverOpt, service.WithAuditPublisher(auditPub))
		slog.Info("Event Bus connected", "address", eventBusAddr)
	} else {
		slog.Info("Event Bus not configured, audit publishing disabled")
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
