package queue

import (
	"context"
	"fmt"
	"log/slog"
	"net"
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
// /choices for hitl) on the same server without forking the SDK.
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

	// queueServiceAddr is the FLOW_QUEUE_SERVICE_ADDR value read once at
	// NewManager time. Empty ⇒ standalone (no registration) — current behavior.
	queueServiceAddr string
	// registry is non-nil when queueServiceAddr is set and Start succeeded in
	// registering. It owns the heartbeat loop.
	registry *queueRegistryClient
	// hbCancel cancels the heartbeat loop; hbWG tracks its goroutine.
	hbCancel context.CancelFunc
	hbWG     sync.WaitGroup
	// heartbeatInterval defaults to defaultHeartbeatInterval; settable by
	// same-package tests for a sub-second tick.
	heartbeatInterval time.Duration
	// registryDial dials the queue-service. nil ⇒ the production dialer;
	// settable by same-package tests to inject a bufconn dialer.
	registryDial registryDialer
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

	// FLOW_QUEUE_SERVICE_ADDR is read exactly once, at construction time, not
	// at Start: a t.Setenv after NewManager but before Start registers nothing.
	// Empty ⇒ standalone (no dial, no client).
	queueServiceAddr := os.Getenv("FLOW_QUEUE_SERVICE_ADDR")

	return &Manager{
		shardID:           cfg.shardID,
		queueName:         cfg.queueName,
		apiPort:           cfg.apiPort,
		queueServiceAddr:  queueServiceAddr,
		heartbeatInterval: defaultHeartbeatInterval,
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
		mesh:  qm.mesh,
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
	// Wire the promotion path (R-C4): the queuePeerServer.NotifyShardDead
	// handler invokes this hook on every surviving shard (tests drive it
	// directly). It runs outside the mesh lock; handleShardDead's markDead is
	// its only lock call.
	qm.mesh.onShardDead = func(dead string) {
		qm.mesh.handleShardDead(context.Background(), dead)
	}

	qm.mesh.start(ctx)

	// If a queue-service address was captured at construction, register this
	// shard and begin the heartbeat loop. Registration/heartbeat failures are
	// logged and retried — they must never fail the owning node (standalone
	// parity). The registered shard addr derives from the already-resolved
	// shardID, never from a second HOSTNAME read.
	if qm.queueServiceAddr != "" {
		shardAddr := net.JoinHostPort(qm.shardID, cfg.peerPort)
		interval := qm.heartbeatInterval
		if interval <= 0 {
			interval = defaultHeartbeatInterval
		}
		// The mesh's backup-selection view is the heartbeat-derived living set
		// (R-B3) — refreshed on each HeartbeatQueue response, corrected down by
		// deadShards on the mesh side. The SDK never reads the Queue CR.
		heartbeats := &heartbeatShardRegistry{}
		qm.mesh.registry = heartbeats
		reg, err := newQueueRegistryClient(
			qm.queueServiceAddr, qm.registryDial,
			qm.shardID, qm.queueName, shardAddr, interval,
		)
		if err != nil {
			slog.Warn("flow hitl: failed to connect to queue-service", "addr", qm.queueServiceAddr, "error", err)
		} else {
			qm.registry = reg
			reg.onHeartbeat = func(shards []Shard) { heartbeats.update(shards) }
			if err := reg.register(ctx); err != nil {
				slog.Warn("flow hitl: queue-service registration failed", "error", err)
			}
			hbCtx, hbCancel := context.WithCancel(ctx)
			qm.hbCancel = hbCancel
			qm.hbWG.Add(1)
			go reg.heartbeatLoop(hbCtx, &qm.hbWG)
		}
	}

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
	// Stop the heartbeat loop and wait for the in-flight tick to complete
	// (per the agent pattern), then deregister best-effort before the mesh
	// tears down so a peer teardown never races the registry goroutine.
	if qm.hbCancel != nil {
		qm.hbCancel()
		qm.hbWG.Wait()
	}
	if qm.registry != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := qm.registry.deregister(ctx); err != nil {
			slog.Warn("flow hitl: queue-service deregistration failed", "error", err)
		}
		cancel()
		_ = qm.registry.close()
		qm.registry = nil
	}
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
	// R-C2: one time-ordered parking-event ID per enqueue.
	gen := newGenerationID()
	// R-C1: pick one random backup from the living set (excluding self).
	backup := qm.mesh.chooseBackup([]string{qm.shardID})
	if err := qm.store.enqueue(ctx, workitemID, gen, backup); err != nil {
		return err
	}
	// R-C1: replicate to the chosen backup (records backup_shard on success,
	// resets to '' on failure = backfill-eligible).
	if backup != "" {
		if err := qm.mesh.replicateAndRecord(ctx, workitemID, gen, qm.shardID, qm.store.queueName, backup); err != nil {
			// ponytail: a failed replicate leaves the item backfill-eligible
			// (warn inside replicateAndRecord); owner remains authoritative.
			_ = err
		}
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
