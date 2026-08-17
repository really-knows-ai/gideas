package service

import (
	"context"
	"crypto/ed25519"
	"sync"
	"sync/atomic"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// HardMaxTimeout is the hard maximum lifetime (inclusive ceiling) for any
// transaction. It pins the constructor default, recovered-transaction timeout,
// and the ExtendTimeout ceiling (enforced via tm.hardMaxTimeout) to a single
// named constant so the three cannot diverge independently on future edits.
const HardMaxTimeout = 7 * 24 * time.Hour

// TelemetryPublisher provides non-blocking telemetry event submission.
type TelemetryPublisher interface {
	Submit(req *flowv1.PublishRequest)
}

// CartographerServer implements flowv1.CartographerServiceServer.
type CartographerServer struct {
	flowv1.UnimplementedCartographerServiceServer

	store       store.Store
	gitstore    gitstore.GitStore
	verifier    *CapabilityVerifier
	ladybugPath string

	txManager   *TransactionManager
	txAdmission sync.RWMutex

	writeLock sync.Mutex

	dbReady atomic.Bool

	readSecretFn func(ctx context.Context, name string) (map[string]string, error)
	remoteURL    string

	auditor        TelemetryPublisher
	newIDFn        func() string
	podNamespace   string
	defaultTimeout time.Duration

	syncWorker *SyncWorker

	gcStop     chan struct{}
	gcStopOnce sync.Once
}

// NewCartographerServer creates a new CartographerServer.
func NewCartographerServer(
	s store.Store,
	gs gitstore.GitStore,
	operatorKey, sidecarKey ed25519.PublicKey,
	readSecretFn func(ctx context.Context, name string) (map[string]string, error),
	remoteURL string,
	stalenessWindow time.Duration,
	podNamespace string,
	defaultTimeout time.Duration,
	changeLogCap int,
	opts ...CartographerOption,
) *CartographerServer {
	if podNamespace == "" {
		podNamespace = "default"
	}
	srv := &CartographerServer{
		store:          s,
		gitstore:       gs,
		verifier:       NewCapabilityVerifier(operatorKey, sidecarKey, stalenessWindow),
		readSecretFn:   readSecretFn,
		remoteURL:      remoteURL,
		newIDFn:        uuid.NewString,
		podNamespace:   podNamespace,
		defaultTimeout: defaultTimeout,
		auditor:        nil,
		gcStop:         make(chan struct{}),
		// HardMaxTimeout (7 days) is pinned to the package-level const and passed
		// to NewTransactionManager, which stores it as tm.hardMaxTimeout. ExtendTimeout
		// enforces the ceiling via that field, and RecoverOpenTransactions reuses the
		// same const; all three share one source of truth.
		txManager: NewTransactionManager(HardMaxTimeout, changeLogCap),
	}
	for _, o := range opts {
		o(srv)
	}
	return srv
}

// CartographerOption configures a CartographerServer.
type CartographerOption func(*CartographerServer)

func WithAuditPublisher(pub TelemetryPublisher) CartographerOption {
	return func(s *CartographerServer) { s.auditor = pub }
}

func WithLadybugPath(path string) CartographerOption {
	return func(s *CartographerServer) { s.ladybugPath = path }
}

// WithSyncWorker sets the background sync worker for remote synchronisation.
// It also hands the worker a reference to the server's single main-LadybugDB
// write lock (writeLock / lockMainStore) so the worker's post-pull re-hydration
// of main.lbug serialises through the same lock as every sibling main-store
// writer (SPEC R5 "single write lock", R10 "under the LadybugDB write lock").
func WithSyncWorker(sw *SyncWorker) CartographerOption {
	return func(s *CartographerServer) {
		s.syncWorker = sw
		sw.writeLock = &s.writeLock
	}
}

// Verifier returns the capability verifier.
func (s *CartographerServer) Verifier() *CapabilityVerifier { return s.verifier }

func (s *CartographerServer) MarkDBReady() { s.dbReady.Store(true) }
func (s *CartographerServer) StartGC()     { go s.startGC() }

// StopGC stops the garbage-collection loop. The close is guarded by a
// sync.Once: StopGC can be called concurrently (e.g. the shutdown path and a
// test teardown racing it), and the select/default close-once idiom is a data
// race on the channel — two concurrent calls can both observe it unclosed and
// both close it, panicking with "close of closed channel".
func (s *CartographerServer) StopGC() {
	s.gcStopOnce.Do(func() { close(s.gcStop) })
}

// Publish telemetry event.
func (s *CartographerServer) publishTelemetry(eventType string, attrs map[string]string) {
	if s.auditor != nil {
		s.auditor.Submit(&flowv1.PublishRequest{
			Channel: "telemetry",
			Event: &flowv1.FlowEvent{
				EventId:       s.newIDFn(),
				EventType:     eventType,
				FlowNamespace: s.podNamespace,
				NodeId:        "cartographer",
				Timestamp:     timestamppb.Now(),
				Attributes:    attrs,
			},
		})
	}
}

func (s *CartographerServer) withGitLock(fn func() error) error {
	return s.gitstore.WithGitLock(fn)
}

func (s *CartographerServer) lockMainStore() {
	s.writeLock.Lock()
}
