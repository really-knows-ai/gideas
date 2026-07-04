// Queue-service provides a unified REST frontend for all QueueManager instances
// in a namespace. It discovers shards via DNS (headless service), proxies every
// operation to the owning pod via the QueuePeerService gRPC protocol, and stores
// no queue state locally.
//
// Usage:
//
//	go run ./platform/queue/cmd/main.go
//	QUEUE_NAMES=human-arbiter,human-approval FLOW_NAMESPACE=default go run ./platform/queue/cmd/main.go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gideas/flow/queue/internal/peer"
	"github.com/gideas/flow/queue/internal/rest"
	flow "github.com/gideas/flow/sdk/go"
)

const (
	defaultPort     = "8080"
	defaultPeerPort = "50053"
)

func main() {
	queueNames := parseQueueNames()
	namespace := os.Getenv("FLOW_NAMESPACE")
	httpPort := envOrDefault("QUEUE_SERVICE_PORT", defaultPort)
	peerPort := envOrDefault("QUEUE_PEER_PORT", defaultPeerPort)

	if len(queueNames) == 0 {
		slog.Error("QUEUE_NAMES is empty or not set")
		os.Exit(1)
	}
	if namespace == "" {
		slog.Error("FLOW_NAMESPACE is not set")
		os.Exit(1)
	}

	slog.Info("Queue-service starting",
		"queues", queueNames,
		"namespace", namespace,
		"http_port", httpPort,
		"peer_port", peerPort,
	)

	// Create one PeerClient per configured queue name.
	peers := make(map[string]*peer.PeerClient, len(queueNames))
	for _, qn := range queueNames {
		peers[qn] = peer.NewPeerClient(&flow.DNSResolver{
			ServiceName: qn,
			Namespace:   namespace,
			SelfShardID: "queue-service",
			Port:        peerPort,
		})
	}

	handler := rest.NewHandler(queueNames, peers)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%s", httpPort),
		Handler: mux,
	}

	// Graceful shutdown on SIGTERM/SIGINT.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		slog.Info("Received signal, shutting down gracefully", "signal", sig)
		_ = srv.Shutdown(context.Background())
		for _, pc := range peers {
			_ = pc.Close()
		}
	}()

	slog.Info("Queue-service listening", "address", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("Queue-service server error", "error", err)
		os.Exit(1)
	}

	slog.Info("Queue-service stopped")
}

// parseQueueNames reads QUEUE_NAMES from the environment, splits on comma,
// trims whitespace, and discards empty entries.
func parseQueueNames() []string {
	raw := os.Getenv("QUEUE_NAMES")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// envOrDefault returns the value of the named environment variable, or def if
// it is empty or unset.
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
