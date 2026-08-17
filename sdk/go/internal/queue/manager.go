package queue

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
)

// Default configuration values.
const (
	defaultAPIPort  = "8080"
	defaultPeerPort = "50053"
)

// Option configures NewManager.
type Option func(*config)

type config struct {
	storagePath  string
	shardID      string
	queueName    string
	serviceName  string
	namespace    string
	peerResolver PeerResolver
	apiPort      string
	peerPort     string
	customRoutes func(mux *http.ServeMux)
}

// WithQueueName sets the queue name for scoping queue items.
// Defaults to FLOW_NODE_ID environment variable, then "default".
func WithQueueName(name string) Option {
	return func(c *config) { c.queueName = name }
}

// WithCustomRoutes registers additional HTTP routes on the QueueManager's
// REST API mux. The provided function is called after the standard HITL
// routes are registered, so it can add node-specific endpoints (e.g. GET
// /choices for hitl-sort) on the same server without forking the SDK.
func WithCustomRoutes(fn func(mux *http.ServeMux)) Option {
	return func(c *config) { c.customRoutes = fn }
}

// Manager is the concrete QueueManager wiring store + mesh + REST API.
type Manager struct {
	store     *queueStore
	mesh      *queueMesh
	shardID   string
	queueName string
	apiPort   string
	httpSrv   *http.Server
	peer      *queuePeerServer
	decisions sync.Map // workitemID → chan string
}

// NewManager creates a new QueueManager. Call Start() to initialise
// the SQLite store, mesh discovery, and HTTP server.
func NewManager(opts ...Option) (*Manager, error) {
	cfg := &config{
		apiPort:  defaultAPIPort,
		peerPort: defaultPeerPort,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	// Environment overrides.
	if v := os.Getenv("FLOW_STORAGE_PATH"); v != "" && cfg.storagePath == "" {
		cfg.storagePath = v
	}
	if v := os.Getenv("HOSTNAME"); v != "" && cfg.shardID == "" {
		cfg.shardID = v
	}
	if v := os.Getenv("FLOW_SERVICE_NAME"); v != "" && cfg.serviceName == "" {
		cfg.serviceName = v
	}
	if v := os.Getenv("FLOW_NAMESPACE"); v != "" && cfg.namespace == "" {
		cfg.namespace = v
	}
	if v := os.Getenv("FLOW_HITL_PORT"); v != "" && cfg.apiPort == defaultAPIPort {
		cfg.apiPort = v
	}

	if cfg.shardID == "" {
		cfg.shardID = "shard-0"
	}
	if cfg.queueName == "" {
		cfg.queueName = os.Getenv("FLOW_NODE_ID")
	}
	if cfg.queueName == "" {
		cfg.queueName = "default"
	}

	return &Manager{
		shardID:   cfg.shardID,
		queueName: cfg.queueName,
		apiPort:   cfg.apiPort,
	}, nil
}

// Start initialises the SQLite store, mesh discovery, and HTTP server.
func (qm *Manager) Start(ctx context.Context, opts ...Option) error {
	// Re-apply options to pick up any late configuration.
	cfg := &config{
		apiPort:  qm.apiPort,
		peerPort: defaultPeerPort,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	// Apply the effective API port so a Start-time override (e.g. "0" for
	// an ephemeral test port) actually takes effect instead of silently
	// falling back to the NewManager-time default.
	qm.apiPort = cfg.apiPort

	// Determine storage path.
	storagePath := cfg.storagePath
	if storagePath == "" {
		if v := os.Getenv("FLOW_STORAGE_PATH"); v != "" {
			storagePath = v
		}
	}

	var dbPath string
	if storagePath == ":memory:" {
		dbPath = ":memory:"
	} else if storagePath != "" {
		dbPath = filepath.Join(storagePath, "queue.db")
	} else {
		dbPath = "queue.db"
	}

	store, err := newQueueStore(dbPath, qm.shardID, qm.queueName)
	if err != nil {
		return fmt.Errorf("open queue store: %w", err)
	}
	qm.store = store

	// Resolve peer discovery.
	resolver := cfg.peerResolver
	if resolver == nil {
		serviceName := cfg.serviceName
		if serviceName == "" {
			if v := os.Getenv("FLOW_SERVICE_NAME"); v != "" {
				serviceName = v
			}
		}
		namespace := cfg.namespace
		if namespace == "" {
			if v := os.Getenv("FLOW_NAMESPACE"); v != "" {
				namespace = v
			}
		}
		if serviceName != "" && namespace != "" {
			peerPort := cfg.peerPort
			if peerPort == "" {
				peerPort = defaultPeerPort
			}
			resolver = &DNSResolver{
				ServiceName: serviceName,
				Namespace:   namespace,
				Port:        peerPort,
			}
		} else {
			// No discovery config — standalone mode (no peers).
			resolver = &staticResolver{}
		}
	}

	qm.mesh = newQueueMesh(store, qm.shardID, resolver, cfg.peerPort)
	qm.peer = &queuePeerServer{
		store: store,
		onDecide: func(workitemID, choice string) {
			// Signal any local WaitForDecision callers. Uses Load so
			// WaitForDecision always finds the channel; it cleans up after
			// consuming. Double-signaling does not occur in the current
			// architecture: Decide signals local decisions, onDecide signals
			// remote gRPC decisions — separate paths. The send is
			// non-blocking so a full decision channel can never hang the
			// gRPC request handler.
			if ch, ok := qm.decisions.Load(workitemID); ok {
				select {
				case ch.(chan string) <- choice:
				default:
				}
			}
		},
	}

	qm.mesh.start(ctx)

	// Start HTTP server.
	mux := newRouter(qm)
	if cfg.customRoutes != nil {
		cfg.customRoutes(mux)
	}
	qm.httpSrv = &http.Server{
		Addr:    ":" + qm.apiPort,
		Handler: mux,
	}
	go func() {
		slog.Info("flow hitl: REST API listening", "port", qm.apiPort)
		if err := qm.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("flow hitl: HTTP server error", "error", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the HTTP server, mesh, and store.
// Any goroutines blocked on WaitForDecision are unblocked (returning nil).
func (qm *Manager) Stop() error {
	if qm.httpSrv != nil {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = qm.httpSrv.Shutdown(shutCtx)
	}
	// Unblock any WaitForDecision callers and drain the decisions map.
	qm.decisions.Range(func(key, value any) bool {
		ch := value.(chan string)
		select {
		case ch <- "":
		default:
		}
		qm.decisions.Delete(key)
		return true
	})
	if qm.mesh != nil {
		_ = qm.mesh.stop()
	}
	if qm.store != nil {
		_ = qm.store.close()
	}
	return nil
}

// RegisterGRPC registers the QueuePeerService on the given gRPC server.
func (qm *Manager) RegisterGRPC(srv *grpc.Server) {
	if qm.peer != nil {
		flowv1.RegisterQueuePeerServiceServer(srv, qm.peer)
	}
}

// --- QueueManager interface implementation ---

func (qm *Manager) Enqueue(ctx context.Context, workitemID string) error {
	if err := qm.store.enqueue(ctx, workitemID); err != nil {
		return err
	}
	// Create a decision channel so WaitForDecision can block.
	qm.decisions.Store(workitemID, make(chan string, 1))
	return nil
}

func (qm *Manager) GetGlobalQueue(ctx context.Context, filter QueueFilter) ([]QueueItem, error) {
	return qm.mesh.getGlobalQueue(ctx, filter)
}

func (qm *Manager) GetItem(ctx context.Context, workitemID string) (*QueueItem, error) {
	return qm.mesh.routeGetItem(ctx, workitemID)
}

func (qm *Manager) Claim(ctx context.Context, workitemID string) (*QueueItem, error) {
	return qm.mesh.routeClaim(ctx, workitemID)
}

func (qm *Manager) Release(ctx context.Context, workitemID string) (*QueueItem, error) {
	return qm.mesh.routeRelease(ctx, workitemID)
}

func (qm *Manager) Decide(ctx context.Context, workitemID, choice string) error {
	if err := qm.mesh.routeDecide(ctx, workitemID, choice); err != nil {
		return err
	}
	// Signal any WaitForDecision callers.
	// ponytail: Uses Load (not LoadAndDelete) so WaitForDecision always finds the
	// channel. If no caller waits, entries leak until Stop. A cleanup sweep can be
	// added if leaks become a concern.
	// The send is non-blocking so a full decision channel can never hang the
	// HTTP /decide handler; a redundant signal is dropped rather than blocking.
	if ch, ok := qm.decisions.Load(workitemID); ok {
		select {
		case ch.(chan string) <- choice:
		default:
		}
	}
	return nil
}

func (qm *Manager) WaitForDecision(ctx context.Context, workitemID string) (string, error) {
	v, ok := qm.decisions.Load(workitemID)
	if !ok {
		return "", ErrQueueItemNotFound
	}
	ch := v.(chan string)
	select {
	case choice := <-ch:
		qm.decisions.Delete(workitemID)
		return choice, nil
	case <-ctx.Done():
		// Keep the channel registered even when this waiter gives up. The
		// item may still be pending in the store, so a late Decide (local
		// REST /decide or a remote peer's DecideItem) must still be captured
		// here and delivered to a subsequent waiter instead of being
		// persisted and lost. Entries are removed on delivery or by Stop.
		return "", ctx.Err()
	}
}

// staticResolver is a no-op PeerResolver that returns no peers.
// Used when no service name / namespace is configured (standalone mode).
type staticResolver struct{}

func (r *staticResolver) Resolve(_ context.Context) ([]string, error) {
	return nil, nil
}
