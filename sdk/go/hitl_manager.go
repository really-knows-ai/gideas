package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
)

// Default configuration values.
const (
	defaultAPIPort  = "8080"
	defaultPeerPort = "50053"
)

// QueueManagerOption configures NewQueueManager.
type QueueManagerOption func(*queueManagerConfig)

type queueManagerConfig struct {
	storagePath  string
	shardID      string
	serviceName  string
	namespace    string
	client       *Client
	peerResolver PeerResolver
	apiPort      string
	peerPort     string
	customRoutes func(mux *http.ServeMux)
}

// WithStoragePath sets the directory for queue.db.
// Overridden by FLOW_STORAGE_PATH environment variable if set.
func WithStoragePath(path string) QueueManagerOption {
	return func(c *queueManagerConfig) { c.storagePath = path }
}

// WithShardID sets the shard identity. Defaults to HOSTNAME env.
func WithShardID(id string) QueueManagerOption {
	return func(c *queueManagerConfig) { c.shardID = id }
}

// WithServiceName sets the headless service name for DNS peer discovery.
// Overridden by FLOW_SERVICE_NAME environment variable if set.
func WithServiceName(name string) QueueManagerOption {
	return func(c *queueManagerConfig) { c.serviceName = name }
}

// WithNamespace sets the Kubernetes namespace for DNS peer discovery.
// Overridden by FLOW_NAMESPACE environment variable if set.
func WithNamespace(ns string) QueueManagerOption {
	return func(c *queueManagerConfig) { c.namespace = ns }
}

// WithClient sets the SDK Client for telemetry emission.
func WithClient(c *Client) QueueManagerOption {
	return func(cfg *queueManagerConfig) { cfg.client = c }
}

// WithPeerResolver injects a custom PeerResolver (for testing).
func WithPeerResolver(r PeerResolver) QueueManagerOption {
	return func(c *queueManagerConfig) { c.peerResolver = r }
}

// WithAPIPort sets the REST API listen port. Default "8080".
// Overridden by FLOW_HITL_PORT environment variable if set.
func WithAPIPort(port string) QueueManagerOption {
	return func(c *queueManagerConfig) { c.apiPort = port }
}

// WithPeerPort sets the gRPC port for peer connections. Default "50053".
func WithPeerPort(port string) QueueManagerOption {
	return func(c *queueManagerConfig) { c.peerPort = port }
}

// WithCustomRoutes registers additional HTTP routes on the QueueManager's
// REST API mux. The provided function is called after the standard HITL
// routes are registered, so it can add node-specific endpoints (e.g. GET
// /choices for hitl-sort) on the same server without forking the SDK.
func WithCustomRoutes(fn func(mux *http.ServeMux)) QueueManagerOption {
	return func(c *queueManagerConfig) { c.customRoutes = fn }
}

// queueManagerImpl is the concrete QueueManager wiring store + mesh + REST API.
type queueManagerImpl struct {
	store     *queueStore
	mesh      *queueMesh
	client    *Client
	shardID   string
	apiPort   string
	httpSrv   *http.Server
	peer      *queuePeerServer
	decisions sync.Map // workitemID → chan string
}

// NewQueueManager creates a new QueueManager. Call Start() to initialise
// the SQLite store, mesh discovery, and HTTP server.
func NewQueueManager(opts ...QueueManagerOption) (*queueManagerImpl, error) {
	cfg := &queueManagerConfig{
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

	return &queueManagerImpl{
		client:  cfg.client,
		shardID: cfg.shardID,
		apiPort: cfg.apiPort,
	}, nil
}

// Start initialises the SQLite store, mesh discovery, and HTTP server.
func (qm *queueManagerImpl) Start(ctx context.Context, opts ...QueueManagerOption) error {
	// Re-apply options to pick up any late configuration.
	cfg := &queueManagerConfig{
		apiPort:  qm.apiPort,
		peerPort: defaultPeerPort,
	}
	for _, opt := range opts {
		opt(cfg)
	}

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

	store, err := newQueueStore(dbPath, qm.shardID)
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
				SelfShardID: qm.shardID,
				Port:        peerPort,
			}
		} else {
			// No discovery config — standalone mode (no peers).
			resolver = &staticResolver{}
		}
	}

	qm.mesh = newQueueMesh(store, qm.shardID, resolver, cfg.peerPort, qm.emitTelemetry)
	qm.peer = &queuePeerServer{
		store: store,
		onDecide: func(workitemID, choice string) {
			// Signal any local WaitForDecision callers. Uses Load so
			// WaitForDecision always finds the channel; it cleans up after
			// consuming. Double-signaling from both Decide() and the gRPC
			// handler is safe — the second caller just sends into the
			// buffered channel (the first choice wins, second is dropped).
			if ch, ok := qm.decisions.Load(workitemID); ok {
				ch.(chan string) <- choice
			}
		},
	}

	qm.mesh.start(ctx)

	// Start HTTP server.
	mux := newHITLRouter(qm)
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
func (qm *queueManagerImpl) Stop() error {
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

// SetClient wires the SDK Client for telemetry emission.
// Called from the handler after NewClient().
func (qm *queueManagerImpl) SetClient(c *Client) {
	qm.client = c
}

// RegisterGRPC registers the QueuePeerService on the given gRPC server.
func (qm *queueManagerImpl) RegisterGRPC(srv *grpc.Server) {
	if qm.peer != nil {
		flowv1.RegisterQueuePeerServiceServer(srv, qm.peer)
	}
}

// --- QueueManager interface implementation ---

func (qm *queueManagerImpl) Enqueue(ctx context.Context, workitemID string) error {
	if err := qm.store.enqueue(ctx, workitemID); err != nil {
		return err
	}
	// Create a decision channel so WaitForDecision can block.
	qm.decisions.Store(workitemID, make(chan string, 1))
	depth, _ := qm.store.countByStatus(ctx, nil)
	qm.emitTelemetry(ctx, "foundry.hitl.enqueued", map[string]any{
		"workitemId": workitemID,
		"nodeId":     qm.shardID,
		"queueDepth": depth,
	})
	return nil
}

func (qm *queueManagerImpl) GetGlobalQueue(ctx context.Context, filter QueueFilter) ([]QueueItem, error) {
	return qm.mesh.getGlobalQueue(ctx, filter)
}

func (qm *queueManagerImpl) GetLocalQueue(ctx context.Context, filter QueueFilter) ([]QueueItem, error) {
	items, _, err := qm.store.getLocal(ctx, filter)
	return items, err
}

func (qm *queueManagerImpl) GetItem(ctx context.Context, workitemID string) (*QueueItem, error) {
	return qm.mesh.routeGetItem(ctx, workitemID)
}

func (qm *queueManagerImpl) Claim(ctx context.Context, workitemID string) (*QueueItem, error) {
	item, err := qm.mesh.routeClaim(ctx, workitemID)
	if err != nil {
		return nil, err
	}
	waitTime := time.Duration(0)
	if item.ClaimedAt != nil {
		waitTime = item.ClaimedAt.Sub(item.EnqueuedAt)
	}
	qm.emitTelemetry(ctx, "foundry.hitl.claimed", map[string]any{
		"workitemId": workitemID,
		"waitTime":   waitTime.String(),
	})
	return item, nil
}

func (qm *queueManagerImpl) Release(ctx context.Context, workitemID string) (*QueueItem, error) {
	// Capture claimed_at before release for telemetry.
	existing, _ := qm.mesh.routeGetItem(ctx, workitemID)
	item, err := qm.mesh.routeRelease(ctx, workitemID)
	if err != nil {
		return nil, err
	}
	claimDuration := time.Duration(0)
	if existing != nil && existing.ClaimedAt != nil {
		claimDuration = time.Since(*existing.ClaimedAt)
	}
	qm.emitTelemetry(ctx, "foundry.hitl.released", map[string]any{
		"workitemId":    workitemID,
		"claimDuration": claimDuration.String(),
	})
	return item, nil
}

func (qm *queueManagerImpl) Decide(ctx context.Context, workitemID, choice string) error {
	// Capture enqueued_at before decide for telemetry.
	existing, _ := qm.mesh.routeGetItem(ctx, workitemID)
	if err := qm.mesh.routeDecide(ctx, workitemID, choice); err != nil {
		return err
	}
	// Signal any WaitForDecision callers.
	// ponytail: Uses Load (not LoadAndDelete) so WaitForDecision always finds the
	// channel. If no caller waits, entries leak until Stop. A cleanup sweep can be
	// added if leaks become a concern.
	if ch, ok := qm.decisions.Load(workitemID); ok {
		ch.(chan string) <- choice
	}
	decisionTime := time.Duration(0)
	if existing != nil {
		decisionTime = time.Since(existing.EnqueuedAt)
	}
	qm.emitTelemetry(ctx, "foundry.hitl.decided", map[string]any{
		"workitemId":   workitemID,
		"decisionTime": decisionTime.String(),
	})
	return nil
}

func (qm *queueManagerImpl) GetPeers(_ context.Context) ([]string, error) {
	return qm.mesh.getPeers(), nil
}

func (qm *queueManagerImpl) WaitForDecision(ctx context.Context, workitemID string) (string, error) {
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
		// Clean up the orphaned channel so it doesn't leak in the map.
		qm.decisions.Delete(workitemID)
		return "", ctx.Err()
	}
}

// emitTelemetry sends a telemetry event via the Client. Non-blocking — failures
// are logged but not propagated.
func (qm *queueManagerImpl) emitTelemetry(ctx context.Context, event string, payload map[string]any) {
	if qm.client == nil {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("flow hitl: telemetry marshal failed", "event", event, "error", err)
		return
	}
	if err := qm.client.RecordTelemetry(ctx, event, data); err != nil {
		slog.Warn("flow hitl: telemetry emission failed (non-blocking)", "event", event, "error", err)
	}
}

// staticResolver is a no-op PeerResolver that returns no peers.
// Used when no service name / namespace is configured (standalone mode).
type staticResolver struct{}

func (r *staticResolver) Resolve(_ context.Context) ([]string, error) {
	return nil, nil
}
