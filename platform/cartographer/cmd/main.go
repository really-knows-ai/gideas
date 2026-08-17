// Command cartographer is the Active Knowledge Graph service for Foundry Flow.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/service"
	"github.com/foundry/flow/cartographer/internal/store"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/pkg/eventbus"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	gogitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/skeema/knownhosts"
	gossh "golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/protobuf/types/known/timestamppb"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Defaults for the SPEC R5 environment-variable table. These constants are the
// cartographer-side single source of truth for the binary's env-var fallbacks
// (versioning.transactionTimeout "30m", CAPABILITY_STALENESS_WINDOW "30s");
// the operator's rendered Deployment/CRD defaults are separate config surfaces.
const (
	defaultTransactionTimeout        = "30m"
	defaultCapabilityStalenessWindow = "30s"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	// 1. Resolve and fail-fast validate every boot-path environment variable
	// (SPEC R5 boot-path env parsing).
	cfg := loadStartupConfig()

	// 2-4. Open the main LadybugDB and the git store at the same PVC root,
	// failing closed on an open failure (SPEC R8 corruption-only recovery) and
	// re-hydrating main from the git working tree whenever the repo has commits.
	dbStore, gs := openAndRecoverStores(cfg.ladybugDBPath)

	// 5. Set up the Kubernetes Secret reader for remote auth (SPEC R1).
	readSecretFn := newSecretReader(cfg.podNamespace)

	// 5a. Configure remote auth on the git store when a remote is configured.
	configureRemoteAuth(gs, cfg, readSecretFn)

	// 6. Connect to the Event Bus for telemetry (audit tombstoning,
	// remote-sync failures); nil publisher when no address is configured.
	auditPub, eventBusCloser := connectEventBus(cfg.eventBusAddress)

	// 7. Optional remote pull on init (SPEC R10 Init) plus the startup
	// catch-up push decision, which is independent of pullOnInit.
	initCatchUpPush := runPullOnInit(gs, cfg, readSecretFn, auditPub, dbStore)

	// 8-11. Construct the CartographerServer — options, background sync worker,
	// open-transaction recovery, and dbReady.
	server, syncW := constructServer(dbStore, gs, cfg, readSecretFn, auditPub, initCatchUpPush)

	// 12. Create the gRPC server with the health probe (SPEC R5: SERVING
	// before the first ApplySchema), capability interceptor, and reflection.
	healthSrv := newHealthServer()
	grpcServer, lis := runGRPCServer(server, healthSrv, cfg.cartographerPort)

	// 13. Start the GC goroutine.
	server.StartGC()

	// 14. Handle graceful shutdown on SIGTERM/SIGINT.
	shutdownDone := watchShutdown(healthSrv, grpcServer, server, dbStore, gs, auditPub, eventBusCloser, syncW)

	// 15. Serve. Serve returns nil (or ErrServerStopped) once the shutdown
	// goroutine called GracefulStop/Stop. Join that goroutine's durability
	// teardown (dbStore.Close, git RestoreMain/CleanUntracked, auditPub.Stop,
	// event bus close) before main returns, so the process does not exit
	// (terminating the goroutine) mid-cleanup and the
	// terminationGracePeriodSeconds budget is honoured.
	slog.Info("Cartographer ready")
	if err := grpcServer.Serve(lis); isFatalServeError(err) {
		slog.Error("gRPC serve error", "error", err)
		os.Exit(1)
	}
	<-shutdownDone
}

// startupConfig carries every value main() resolves from the environment
// (SPEC R5 boot-path env parsing) before any component is constructed. Each
// field is either optional (empty/zero = absent) or fail-fast validated: an
// unparseable boolean, an unparseable/non-positive duration, or a
// missing/malformed verification key exits the process rather than silently
// running with a wrong value.
type startupConfig struct {
	ladybugDBPath       string
	cartographerPort    string
	remoteURL           string
	remoteAuthSecretRef string
	remotePullOnInit    bool
	podNamespace        string
	eventBusAddress     string

	transactionTimeout time.Duration
	stalenessWindow    time.Duration
	syncInterval       time.Duration

	operatorKey ed25519.PublicKey
	sidecarKey  ed25519.PublicKey
}

// loadStartupConfig reads and fail-fast validates every environment variable
// the Cartographer boot path consumes (SPEC R5 environment-variable table).
// All env fail-fast guards live here — so the individual parsers
// (parseBoolEnv, parseDurationEnv, parseVerificationKey) stay unit-testable
// without os.Exit and the wrappers (loadPullOnInit, loadDuration,
// loadPositiveDuration, loadVerificationKey) own the process exit — keeping
// main() a thin coordinator.
func loadStartupConfig() startupConfig {
	cfg := startupConfig{
		ladybugDBPath:       getEnv("LADYBUG_DB_PATH", "/data"),
		cartographerPort:    getEnv("CARTOGRAPHER_PORT", "50051"),
		remoteURL:           os.Getenv("REMOTE_URL"),
		remoteAuthSecretRef: os.Getenv("REMOTE_AUTH_SECRET_REF"),
		podNamespace:        getEnv("POD_NAMESPACE", "default"),
		eventBusAddress:     os.Getenv("EVENT_BUS_ADDRESS"),
	}
	// SPEC R5 fail-fast env guard: an unparseable boolean is fatal. The
	// fail-fast decision (return an error) is factored into parseBoolEnv so it
	// is unit-testable without os.Exit; loadPullOnInit owns the process exit,
	// mirroring loadVerificationKey below.
	cfg.remotePullOnInit = loadPullOnInit()
	// SPEC R5 fail-fast guard: a non-positive TRANSACTION_TIMEOUT parses
	// cleanly but makes every BeginTransaction fail at runtime with
	// INVALID_ARGUMENT ("invalid transaction timeout duration: duration must be
	// positive", surfaced by TransactionManager.Create via
	// errInvalidTransactionTimeoutDuration in the service errors.go), so it
	// must fail startup just like an unparseable value. Mirrors the
	// SYNC_INTERVAL positivity guard below.
	cfg.transactionTimeout = loadPositiveDuration("TRANSACTION_TIMEOUT", defaultTransactionTimeout)
	// SPEC R5: no positivity guard for the staleness window — a negative
	// CAPABILITY_STALENESS_WINDOW disables the staleness check entirely.
	cfg.stalenessWindow = loadDuration("CAPABILITY_STALENESS_WINDOW", defaultCapabilityStalenessWindow)
	// SPEC R10 sync worker "wakes every minute (configurable)": the periodic
	// interval is an env knob whose default is sourced from the worker's own
	// DefaultSyncInterval constant, so the wiring default and the worker
	// default share one source of truth. Fail-fast on unparseable or
	// non-positive values: time.NewTicker panics on a non-positive interval,
	// so a bad SYNC_INTERVAL must fail startup with a clear message rather
	// than crash the worker goroutine mid-run.
	cfg.syncInterval = loadPositiveDuration("SYNC_INTERVAL", service.DefaultSyncInterval.String())
	// Validate and load verification keys early (fail-fast on missing keys).
	cfg.operatorKey = loadVerificationKey("OPERATOR_VERIFICATION_KEY")
	cfg.sidecarKey = loadVerificationKey("SIDECAR_VERIFICATION_KEY")
	return cfg
}

// openAndRecoverStores opens the main LadybugDB database at <path>/main.lbug
// and initialises the git store at <path>/graph-repo/, failing startup on an
// operational open failure (SPEC R8 corruption-only recovery), then runs the
// startup re-hydration of main from the git working tree whenever the git
// repository has commits.
func openAndRecoverStores(ladybugDBPath string) (store.Store, gitstore.GitStore) {
	dbStore, dbErr := ladybug.Open(ladybugDBPath)

	gs, gsErr := gitstore.New(ladybugDBPath)
	if gsErr != nil {
		slog.Error("Failed to initialise gitstore", "error", gsErr)
		os.Exit(1)
	}
	slog.Info("Git repository open", "path", filepath.Join(ladybugDBPath, "graph-repo"))

	// SPEC R8 corruption recovery is scoped entirely to a genuinely corrupted
	// main.lbug. ladybug.Open performs the destructive half of that recovery:
	// on a readable-but-unparseable file (corruptionCandidates) it removes
	// main.lbug and re-opens a fresh, empty database internally, returning nil
	// error. The re-hydration half — restoring the committed graph from the git
	// file-per-element representation — is NOT done by Open; it runs in
	// rehydrateMainAfterRecovery below. Any non-nil error returned here is
	// therefore an operational (IO/permission) or post-open failure
	// (OpenConnection / extension LOAD / rebuildSchemaCache /
	// restoreMainSchemaMetadata) — it does NOT prove main.lbug is corrupt. The
	// store deliberately refuses to delete a database it cannot prove corrupt
	// (see corruptionCandidate in ladybug.go), so deleting main.lbug here could
	// destroy a possibly-valid database. Fail closed without touching the file.
	if dbErr != nil {
		slog.Error("Failed to open main.lbug; refusing to delete database (failure is not proven corruption)",
			"path", filepath.Join(ladybugDBPath, "main.lbug"),
			"error", dbErr,
		)
		os.Exit(1)
	}

	// SPEC R8 (SPEC.md:526): after ladybug.Open recovered a corrupted main.lbug
	// by deleting and re-opening it fresh, main holds schema metadata but no
	// graph data. When the git repository has commits, re-hydrate main from the
	// file-per-element representation so the service does not serve a vacuous
	// empty graph while committed data exists.
	// ponytail: re-hydration runs unconditionally (any non-empty repo), not
	// gated on actual corruption recovery, because ladybug.Open gives no signal
	// that recovery occurred (the delete+reopen runs internally in ladybug.Open —
	// the section 2 Open call, via corruptionCandidates/removeCorruptedMain in
	// ladybug.go — and both the failure and recovery paths return nil error to
	// the caller). Cost: every
	// pod restart DETACH-DELETEs a healthy main.lbug and rebuilds it from the
	// git working tree — a full graph re-load on each restart, paid even when
	// main.lbug was never corrupted. The ordering is deliberately
	// trust-git-over-main.lbug: the transaction-only write model (SPEC R2)
	// commits every mutation to git before main is re-hydrated (the git commit
	// precedes RehydrateMainFromFiles in CommitTransaction), so the rebuild
	// reads only data git already holds. The unconditional scope also covers
	// the crash-mid-re-hydration staleness case — a pod killed during
	// CommitTransaction's main re-hydration (after the transaction branch
	// commit, before the fast-forward merge) leaves main.lbug partially wiped
	// or stale, and this rebuild restores a complete, consistent main. Scope
	// note: the rebuild reads main's tree, which does not contain an un-merged
	// transaction commit — a crash after re-hydration but before the merge is
	// recovered by the retried CommitTransaction, whose recovered state has
	// CommitHydrated cleared (RecoverOpenTransactions) so it re-hydrates main
	// from the transaction branch before merging. Failure mode: if the R2
	// invariant ever broke (a future non-transactional write path), the
	// rebuild would silently drop healthy main.lbug data; mitigated today by
	// the RestoreMain + CleanUntracked that reset the working tree to main's
	// HEAD before any files are read (rehydrateMainAfterRecovery). Upgrade
	// path: surface a recovered-from-corruption signal from ladybug.Open (or
	// gate on a main-is-empty probe) so a healthy main.lbug is left untouched.
	// A failure here is fatal: serving an empty graph after a corrupt-reopen
	// silently drops all committed data, so fail loudly instead.
	if err := rehydrateMainAfterRecovery(context.Background(), dbStore, gs); err != nil {
		handleStartupRehydrateFailure(context.Background(), dbStore, err)
	}
	return dbStore, gs
}

// newSecretReader sets up the Kubernetes client used for SPEC R1 Secret reads
// and returns the shared readSecretFn consumed by the git auth resolver
// (buildResolveAuthFn) and tryRemotePullOnInit's pre-flight check. Inside the
// cluster the in-cluster config is used; the kubeconfig fallback covers local
// runs. Every failure degrades to a reader that returns a descriptive error so
// callers fail closed (never anonymous).
func newSecretReader(podNamespace string) func(ctx context.Context, name string) (map[string]string, error) {
	k8sConfig, inClusterErr := rest.InClusterConfig()
	if inClusterErr != nil {
		var kubeErr error
		k8sConfig, kubeErr = clientcmd.BuildConfigFromFlags("", "")
		if kubeErr != nil {
			slog.Warn("Kubeconfig fallback also failed (expected when running outside cluster)", "error", kubeErr)
		}
	}
	if k8sConfig == nil {
		slog.Warn("Kubernetes client not configured — running outside cluster")
		return func(ctx context.Context, name string) (map[string]string, error) {
			return nil, fmt.Errorf("kubernetes client not configured")
		}
	}
	clientset, kErr := kubernetes.NewForConfig(k8sConfig)
	if kErr != nil {
		slog.Warn("Failed to create Kubernetes clientset", "error", kErr)
		return func(_ context.Context, _ string) (map[string]string, error) {
			return nil, fmt.Errorf("kubernetes client unavailable: %w", kErr)
		}
	}
	slog.Info("Kubernetes client initialised", "namespace", podNamespace)
	return newReadSecretFn(clientset, podNamespace)
}

// configureRemoteAuth wires REMOTE_URL onto the git store with its Secret-backed
// auth resolver (SPEC R1 secret data keys). A rejected remote URL (SetRemote
// rejecting: unsupported scheme, parse failure, no host — validateRemoteURL,
// remote.go) is a deployment misconfiguration: with pullOnInit=true the process
// aborts startup loudly, with pullOnInit=false the failure degrades to the sync
// worker's logged, non-blocking error class (SPEC error-table note at the
// failure site below).
func configureRemoteAuth(
	gs gitstore.GitStore,
	cfg startupConfig,
	readSecretFn func(ctx context.Context, name string) (map[string]string, error),
) {
	if cfg.remoteURL == "" {
		return
	}
	resolveAuthFn := buildResolveAuthFn(cfg.remoteAuthSecretRef, readSecretFn, cfg.remoteURL)
	if err := gs.SetRemote(context.Background(), cfg.remoteURL, resolveAuthFn); err != nil {
		// SPEC error-table row 987 ("Unsupported remote URL scheme" →
		// INVALID_ARGUMENT) and the R1 remote scheme set: a URL SetRemote
		// rejects (unsupported scheme, parse failure, no host —
		// validateRemoteURL, remote.go) is a deployment misconfiguration.
		// The fail-startup clause is scoped to pullOnInit: true (SPEC.md:122:
		// "An empty Secret or one missing the expected key causes the
		// Cartographer to fail startup if pullOnInit is true" — the same
		// clause tryRemotePullOnInit's pre-flight implements), and R10 Init
		// (SPEC.md:636-641) logs clone/init failures and does not block
		// startup. So: with pullOnInit=true a rejected remote aborts startup
		// loudly (a misconfigured remote that the pod is supposed to clone on
		// init is a deployment error worth surfacing immediately); with
		// pullOnInit=false (the default) the remote is only used by the sync
		// worker's runtime cycles, whose errors are logged and non-blocking —
		// so a rejected URL degrades to that same logged, non-blocking class
		// instead of crash-looping. In both cases the worker is still created
		// (keyed on REMOTE_URL); with a rejected remote its first cycle
		// surfaces ErrNoRemote, which classifySyncError classes
		// non-recoverable (sync_worker.go): fetchAndRehydrate logs the
		// failure loudly and emits a cartographer.push_failed Event Bus
		// telemetry event on every woken or timer cycle before returning the
		// error — and WithAck/Commit fail with FAILED_PRECONDITION "no
		// remote configured" via mapGitError, the same runtime surface a
		// pullOnInit=false misconfiguration produces today.
		if cfg.remotePullOnInit {
			slog.Error("Failed to configure remote; aborting startup", "url", cfg.remoteURL, "error", err)
			os.Exit(1)
		}
		slog.Warn("Failed to configure remote (non-fatal: pullOnInit=false); remote sync degraded",
			"url", cfg.remoteURL, "error", err)
		return
	}
	slog.Info("Remote configured", "url", cfg.remoteURL)
}

// connectEventBus dials the Event Bus when an address is configured, returning
// the async telemetry publisher and the connection closer, or (nil, nil) when
// telemetry is disabled.
func connectEventBus(address string) (*eventbus.AsyncPublisher, func() error) {
	if address == "" {
		slog.Info("Event Bus not configured, telemetry publishing disabled")
		return nil, nil
	}
	ebConn, cErr := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if cErr != nil {
		// ponytail: fail-fast on an unreachable Event Bus. grpc.NewClient is
		// lazy — it only validates the target form and never dials, so cErr
		// here is a malformed-address parse failure, not a down-bus. The
		// rationale for failing closed on it: a misconfigured telemetry
		// address is a deployment misconfiguration the operator wants to
		// see immediately, and telemetry events published by mutations
		// (audit tombstoning, remote-sync failures) would otherwise be
		// silently dropped on every write. Ceiling: this treats a
		// configuration typo as fatal even though the Event Bus is a
		// non-blocking, fire-and-forget side channel — the durable graph
		// service would run fine with telemetry off (as it does when
		// EVENT_BUS_ADDRESS is unset). Upgrade path: dial lazily via a
		// non-blocking reconnector so a bad address degrades to
		// telemetry-disabled rather than failing startup; revisit if the
		// Event Bus becomes a hard dependency.
		slog.Error("Failed to connect to Event Bus", "address", address, "error", cErr)
		os.Exit(1)
	}
	ebClient := flowv1.NewFlowEventBusServiceClient(ebConn)
	auditPub := eventbus.NewAsyncPublisher(ebClient)
	slog.Info("Event Bus connected for telemetry", "address", address)
	return auditPub, ebConn.Close
}

// runPullOnInit performs the SPEC R10 Init pre-flight and returns the startup
// catch-up push decision: whether the sync worker's first cycle must push
// locally-committed-but-unpushed data (SPEC R10 Init, SPEC.md:640-641: "The
// sync worker pushes any locally-committed-but-unpushed data on its first cycle
// (startup catch-up push), including any unsent commits from a prior pod
// lifetime"). The decision is independent of pullOnInit: the pull is optional,
// but the startup catch-up push is not — a prior pod that terminated before its
// push completed left its commits in the local git repo (the push flag itself
// is in-memory and is lost on restart), and only the worker's first cycle can
// deliver them. The worker is constructed after this init path, so this helper
// only reports the decision and main wires it into the worker before the first
// cycle runs.
func runPullOnInit(
	gs gitstore.GitStore,
	cfg startupConfig,
	readSecretFn func(ctx context.Context, name string) (map[string]string, error),
	auditPub *eventbus.AsyncPublisher,
	dbStore store.Store,
) bool {
	if cfg.remotePullOnInit && cfg.remoteURL != "" {
		catchUpPush, err := tryRemotePullOnInit(
			gs, cfg.remoteURL, cfg.remoteAuthSecretRef, cfg.podNamespace, readSecretFn, auditPub,
			// SPEC R10 Init: after clone-on-init seeds the git working tree,
			// re-hydrate main from the cloned file-per-element representation so
			// the graph is not empty. With the transaction-only write model there
			// are no non-transactional writes to main.lbug that git does not
			// already contain, so re-hydration from git is always complete and
			// safe. The clone path runs only when the local repo has no
			// graph-data commits (IsEmpty), so there is no local committed graph
			// for the clone to supersede.
			func() error {
				entitiesDir, edgesDir := gs.HydrationDirs()
				return dbStore.RehydrateMainFromFiles(context.Background(), entitiesDir, edgesDir)
			},
		)
		if err != nil {
			slog.Error("Remote init failed; aborting startup", "error", err)
			os.Exit(1)
		}
		return catchUpPush
	}
	if cfg.remoteURL != "" {
		// pullOnInit=false (the common default): no clone runs, but the
		// startup catch-up push still applies — unsent commits from a prior pod
		// lifetime must be delivered by the worker's first cycle (SPEC R10 Init:
		// "pushes any locally-committed-but-unpushed data on its first cycle
		// (startup catch-up push), including any unsent commits from a prior pod
		// lifetime").
		// A repo-state check failure is logged and non-blocking, mirroring
		// tryRemotePullOnInit's IsEmpty handling (SPEC R10 Init).
		return startupCatchUpPushNeeded(context.Background(), gs)
	}
	return false
}

// constructServer builds the CartographerServer — options (audit publisher,
// ladybug path), the background sync worker, the per-transaction change-log
// cap — then recovers open transactions, starts the worker goroutine, and
// marks the database ready. The worker goroutine is started only after server
// construction AND transaction recovery: Run() executes an immediate first
// cycle (fetch → restore-main → clean → re-hydrate), which must not run
// concurrently with the recovery path — recovery's main-file reads
// (buildMainFileLookups, cartographer_server.go) happen outside the git lock
// after ListBranches, so a concurrent first cycle could snapshot or re-hydrate
// a stale or mid-recovery working tree in the crash-strand scenario.
// SetPushNeeded (initCatchUpPush) is applied before the first cycle runs.
func constructServer(
	dbStore store.Store,
	gs gitstore.GitStore,
	cfg startupConfig,
	readSecretFn func(ctx context.Context, name string) (map[string]string, error),
	auditPub *eventbus.AsyncPublisher,
	initCatchUpPush bool,
) (*service.CartographerServer, *service.SyncWorker) {
	var opts []service.CartographerOption
	if auditPub != nil {
		opts = append(opts, service.WithAuditPublisher(auditPub))
	}
	opts = append(opts, service.WithLadybugPath(cfg.ladybugDBPath))

	// Create the background sync worker if a remote URL is configured. Its
	// goroutine is started only after server construction and transaction
	// recovery (see the doc comment above).
	var syncW *service.SyncWorker
	if cfg.remoteURL != "" {
		// Permanent sync failures emit an operator-visible Event Bus telemetry
		// event (SPEC R10 error classification "log loudly + telemetry"), so the
		// worker shares the server's audit publisher when one is configured, and
		// stamps the same flow namespace (FlowNamespace: podNamespace) the
		// server's publishTelemetry uses, so the two emitters stay consistent.
		var syncOpts []service.SyncWorkerOption
		if auditPub != nil {
			syncOpts = append(syncOpts, service.SyncWorkerWithAuditPublisher(auditPub))
		}
		syncOpts = append(syncOpts, service.SyncWorkerWithPodNamespace(cfg.podNamespace))
		syncOpts = append(syncOpts, service.SyncWorkerWithSyncInterval(cfg.syncInterval))
		syncW = service.NewSyncWorker(cfg.remoteURL, gs, dbStore, service.RealClock{}, syncOpts...)
		opts = append(opts, service.WithSyncWorker(syncW))
		// SPEC R10 Init / SPEC.md:640-641: when init found committed-but-
		// unpushed data (non-empty repo booting with a remote configured —
		// independent of pullOnInit: the pull is optional, the startup
		// catch-up push is not), flag the push before the worker's first cycle
		// runs so the startup catch-up push goes through the worker's
		// error-table contract — recoverable failures retried within the cycle
		// with backoff (up to 3 attempts) and the push flag left set for the
		// next cycle; non-recoverable failures logged immediately + telemetry,
		// flag left set — instead of a one-shot synchronous push that was
		// logged + telemetry only and never retried until the next Commit() or
		// a restart.
		if initCatchUpPush {
			syncW.SetPushNeeded()
		}
	}

	// The per-transaction change-log admission cap (100K entries, SPEC change-log
	// capacity requirement) is passed to NewChangeLogWithCap inside the
	// TransactionManager; exceeding it yields ErrChangeLogFull →
	// RESOURCE_EXHAUSTED with transaction rollback. The cap is sourced from
	// store.DefaultChangeLogCap — the store-layer constant (SPEC.md:888-889)
	// that gitstore's default NewChangeLog and the startup-recovery path
	// (RecoverOpenTransactions) also read — so the admission ceiling and the
	// recovery ceiling cannot silently diverge.
	// ponytail: the service package's own tests mirror the cap by rote (100000
	// literals); a future cap change must touch the store constant plus every
	// service-test literal.
	server := service.NewCartographerServer(
		dbStore, gs, cfg.operatorKey, cfg.sidecarKey,
		readSecretFn, cfg.remoteURL, cfg.stalenessWindow,
		cfg.podNamespace, cfg.transactionTimeout, store.DefaultChangeLogCap,
		opts...,
	)
	slog.Info("Cartographer server constructed")

	if err := server.RecoverOpenTransactions(context.Background()); err != nil {
		slog.Error("Failed to recover open transactions", "error", err)
		os.Exit(1)
	}
	slog.Info("Open transactions recovered")

	if syncW != nil {
		go syncW.Run()
		slog.Info("Background sync worker started")
	}
	server.MarkDBReady()
	return server, syncW
}

// runGRPCServer builds the gRPC server wired with the capability verification
// interceptors, the SPEC R5 health service (SERVING before the first
// ApplySchema — newHealthServer), and reflection, then binds the TCP listener
// on the configured port. The shutdown path flips the health service to
// NOT_SERVING.
func runGRPCServer(
	server *service.CartographerServer,
	healthSrv *health.Server,
	port string,
) (*grpc.Server, net.Listener) {
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(server.Verifier().VerifyInterceptor),
		grpc.ChainStreamInterceptor(server.Verifier().VerifyStreamInterceptor),
	)

	flowv1.RegisterCartographerServiceServer(grpcServer, server)
	grpc_health_v1.RegisterHealthServer(grpcServer, healthSrv)
	reflection.Register(grpcServer)

	lis, lErr := net.Listen("tcp", ":"+port)
	if lErr != nil {
		slog.Error("Failed to listen", "port", port, "error", lErr)
		os.Exit(1)
	}
	slog.Info("gRPC server listening", "address", lis.Addr().String())
	return grpcServer, lis
}

// watchShutdown registers SIGINT/SIGTERM and runs the graceful-shutdown
// teardown (waitForShutdown) in a background goroutine. It returns the
// shutdownDone channel main joins after Serve returns, so the durability
// teardown (dbStore.Close, git RestoreMain/CleanUntracked, auditPub.Stop,
// event bus close) completes before the process exits and the
// terminationGracePeriodSeconds budget is honoured.
func watchShutdown(
	healthSrv *health.Server,
	grpcServer *grpc.Server,
	server *service.CartographerServer,
	dbStore store.Store,
	gs gitstore.GitStore,
	auditPub *eventbus.AsyncPublisher,
	eventBusCloser func() error,
	syncW *service.SyncWorker,
) <-chan struct{} {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	shutdownDone := make(chan struct{})
	go waitForShutdown(shutdownDone, sigCh, healthSrv, grpcServer, server, dbStore, gs, auditPub, eventBusCloser, syncW)
	return shutdownDone
}

// rehydrateMainAfterRecovery re-synchronizes main LadybugDB from the git
// working tree whenever the git repository has commits (SPEC R8 corruption
// recovery, SPEC.md:526); the unconditional scope and its every-restart cost
// are documented in the ponytail at the call site in main().
// ladybug.Open performs the destructive half of R8 recovery — deleting a
// corrupted main.lbug and re-opening a fresh, empty database — but does not
// restore the committed graph; that is this function's job. The working tree
// is switched back to main (RestoreMain + CleanUntracked) before any files
// are read: after a crash (SIGKILL/eviction) the tree can be stranded on a
// transaction branch whose snapshot predates main's current commits, and
// re-hydrating a healthy main.lbug from that stale snapshot would silently
// roll back committed data that landed on main after the transaction began.
// With the transaction-only write model there are no non-transactional writes
// to main.lbug that git does not already contain, so re-hydration from git is
// always complete and safe: whenever the repo is not empty, main is re-hydrated
// unconditionally. A fresh install is a no-op (an empty git repo has no
// committed state to recover). Any error is propagated so the caller can fail
// startup loudly.
func rehydrateMainAfterRecovery(ctx context.Context, dbStore store.Store, gs gitstore.GitStore) error {
	// IsEmpty must be called with the git lock held (GitStore interface
	// contract); startup is single-threaded, but the check still goes through
	// WithGitLock so no caller can observe an unlocked gitstore read.
	var empty bool
	if err := gs.WithGitLock(func() error {
		var err error
		empty, err = gs.IsEmpty(ctx)
		if err != nil {
			return err
		}
		if empty {
			return nil
		}
		// The working tree may be checked out on a stranded transaction branch
		// after a crash: restore main and clean the tree before files are
		// read, so a healthy main.lbug is never rebuilt from a stale branch
		// snapshot (SPEC R8 re-hydration reads main's committed state; the
		// sync worker applies the same restore-main-before-read discipline).
		if err := gs.RestoreMain(ctx); err != nil {
			return fmt.Errorf("restore main before re-hydration: %w", err)
		}
		if err := gs.CleanUntracked(ctx); err != nil {
			return fmt.Errorf("clean working tree before re-hydration: %w", err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("prepare git working tree for recovery: %w", err)
	}
	if empty {
		return nil
	}
	entitiesDir, edgesDir := gs.HydrationDirs()
	if err := dbStore.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		return fmt.Errorf("re-hydrate main from git files: %w", err)
	}
	slog.Info("Main re-hydrated from git working tree (SPEC R8 recovery)")
	return nil
}

// mainServesGraph reports whether main.lbug currently holds at least one
// entity — the signal that a failed startup re-hydration leaves a complete
// graph to serve rather than a vacuous one. A graph with zero entities can
// hold no edges (edges require endpoint entities), so the entity probe is the
// completeness signal. Any probe error fails closed (reports "no graph") so
// the caller keeps the conservative fatal behavior.
func mainServesGraph(ctx context.Context, dbStore store.Store) bool {
	types, err := dbStore.ListMainEntityTypes()
	if err != nil {
		return false
	}
	for _, entityType := range types {
		if entities, _, err := dbStore.ListEntities(ctx, entityType, 1, "", "main"); err == nil && len(entities) > 0 {
			return true
		}
	}
	return false
}

// startupRehydrateFailureFatal reports whether a startup re-hydration failure
// (rehydrateMainAfterRecovery returning an error) must fail the process. It is
// fatal only when main.lbug holds no graph data: serving a vacuous main while
// git holds committed data hides that data (SPEC R8), but crash-looping a pod
// whose main.lbug holds a complete graph would make the SPEC "Sync re-hydration
// failed" row's R8 escape hatch unreachable. See handleStartupRehydrateFailure.
func startupRehydrateFailureFatal(ctx context.Context, dbStore store.Store) bool {
	return !mainServesGraph(ctx, dbStore)
}

// handleStartupRehydrateFailure applies the fatality decision to a failed
// startup re-hydration. A failure here used to be unconditionally fatal: after
// a corrupt-reopen main.lbug is empty, and serving it would silently drop
// committed data. But the startup rebuild runs unconditionally (ponytail at the
// call site in main()), so a failure on a HEALTHY main.lbug — e.g. a corrupt
// remote merge whose files RehydrateMainFromFiles rejects without wiping main
// (atomic re-hydration) — would crash-loop a pod whose main.lbug holds a
// complete graph, making the SPEC error-table row "Sync re-hydration failed"
// escape hatch ("see R8 for automatic recovery on next startup") unreachable.
// The fatality is gated on main actually holding no data: a vacuous main.lbug
// must never be served while git holds commits, but a healthy one is served
// (loudly logged) while the sync worker's cycles retry the re-hydration.
func handleStartupRehydrateFailure(ctx context.Context, dbStore store.Store, err error) {
	if startupRehydrateFailureFatal(ctx, dbStore) {
		slog.Error("Failed to re-hydrate main from git after open (SPEC R8 recovery); "+
			"main.lbug holds no graph data — refusing to serve a vacuous graph",
			"error", err,
		)
		os.Exit(1)
	}
	slog.Error("Re-hydration from git failed at startup; main.lbug holds a complete graph — "+
		"serving it and deferring recovery to the sync worker (SPEC R8)",
		"error", err,
	)
}

// startupCatchUpPushNeeded reports whether the sync worker's first cycle must
// run the startup catch-up push (SPEC R10 Init, SPEC.md:640-641): the local
// git repository holds graph-data commits (non-empty), which may include unsent
// commits from a prior pod lifetime (the push flag itself is in-memory and is
// lost on restart). It backs the pullOnInit=false path, where
// tryRemotePullOnInit does not run — its pre-flight auth check and clone are
// gated on pullOnInit, but the catch-up push is not. A repository-state check
// failure is logged and treated as "no catch-up needed" (non-blocking),
// mirroring tryRemotePullOnInit's IsEmpty handling (SPEC R10 Init).
func startupCatchUpPushNeeded(ctx context.Context, gs gitstore.GitStore) bool {
	var empty bool
	if err := gs.WithGitLock(func() error {
		var err error
		empty, err = gs.IsEmpty(ctx)
		return err
	}); err != nil {
		slog.Warn("Failed to check git repo state on init (non-blocking)", "error", err)
		return false
	}
	return !empty
}

// tryRemotePullOnInit performs the SPEC R10 Init pre-flight auth check and the
// clone-vs-catch-up decision. It returns catchUpPush=true when the local repo
// already has commits (non-empty) and the sync worker's first cycle must push
// any locally-committed-but-unpushed data (SPEC R10 Init: "pushes any
// locally-committed-but-unpushed data on its first cycle (startup catch-up
// push)"); the caller (main) sets the worker's push flag from this before the
// first cycle runs. The push itself is deliberately NOT performed here: routing it
// through the worker's cycle keeps the R10 error-table retry contract.
// podNamespace stamps the clone-failure telemetry event (FlowNamespace),
// matching the server's publishTelemetry and the sync worker's publishFailure
// emitters so all three events in this binary carry the same flow attribution.
func tryRemotePullOnInit(
	gs gitstore.GitStore,
	remoteURL string,
	remoteAuthSecretRef string,
	podNamespace string,
	readSecretFn func(ctx context.Context, name string) (map[string]string, error),
	auditPub *eventbus.AsyncPublisher,
	rehydrate func() error,
) (catchUpPush bool, err error) {
	// SPEC fail-startup clause (R1 Secret data keys, SPEC.md:122): an empty
	// Secret or one missing the expected key causes the Cartographer to fail
	// startup when pullOnInit is true — "the git operation cannot be attempted
	// at all". The clause is unconditional (its trigger is pullOnInit, not the
	// repo state), so the pre-flight auth check runs before the repo-state
	// branch and gates BOTH init paths: the clone path (empty repo) and the
	// catch-up push path (non-empty repo). A credential that is present but
	// expired/revoked still passes pre-flight and surfaces as a git-level auth
	// failure at clone/push time, which is logged and deferred (non-blocking) —
	// matching the SPEC's missing-vs-revoked distinction.
	authFn := func() error {
		if remoteAuthSecretRef == "" {
			return nil
		}
		parsedURL, parseErr := url.Parse(remoteURL)
		if parseErr != nil {
			return fmt.Errorf("pre-flight auth: parse URL: %w", parseErr)
		}
		// SPEC.md:91-100 defines secret data keys only for ssh:// and
		// https://; a file:// remote has no auth keys and proceeds
		// anonymously (SPEC error-table row 987 lists file:// as a supported
		// scheme). Short-circuit before the Secret read so a configured
		// secretRef — or a Secret-read failure — cannot block it.
		if parsedURL.Scheme == "file" {
			return nil
		}
		if readSecretFn == nil {
			return gitstore.ErrAuthConfigMissing
		}
		// The pre-flight Secret read is a network-touching boot step, so it
		// carries the same per-operation deadline the sync worker applies to
		// every git operation (service.DefaultGitOperationTimeout, SPEC R10 /
		// SPEC:981): a hung or unreachable k8s API server must fail startup
		// within a bounded window instead of blocking it indefinitely. Mirrors
		// the clone-on-init deadline below.
		readCtx, cancel := context.WithTimeout(context.Background(), service.DefaultGitOperationTimeout)
		defer cancel()
		data, err := readSecretFn(readCtx, remoteAuthSecretRef)
		if err != nil {
			return fmt.Errorf("pre-flight auth: read secret: %w", err)
		}
		switch parsedURL.Scheme {
		case "ssh":
			// Spec missing-expected-key rule: a present-but-empty data key is
			// equivalent to an absent one — fail closed on either.
			if len(data["ssh-privatekey"]) == 0 {
				return gitstore.ErrAuthConfigMissing
			}
		case "https":
			if len(data["password"]) == 0 {
				return gitstore.ErrAuthConfigMissing
			}
		default:
			return gitstore.ErrUnsupportedURLScheme
		}
		return nil
	}
	if authErr := authFn(); authErr != nil {
		return false, authErr
	}

	// IsEmpty must be called with the git lock held (GitStore interface
	// contract); startup is single-threaded, but the check still goes through
	// WithGitLock so no caller can observe an unlocked gitstore read.
	var empty bool
	if err := gs.WithGitLock(func() error {
		var err error
		empty, err = gs.IsEmpty(context.Background())
		return err
	}); err != nil {
		// SPEC R10 Init: repository-state check failures are logged, not fatal.
		slog.Warn("Failed to check git repo state on init (non-blocking)", "error", err)
		return false, nil
	}
	if empty {
		slog.Info("Pulling from remote on init", "url", remoteURL)
		// SPEC R10 / SPEC:981: the sync worker bounds every git operation by
		// the configurable per-operation deadline (gitOp, default
		// service.DefaultGitOperationTimeout, five minutes). The clone-on-init
		// path is a git operation too, so it carries the same deadline: a hung
		// remote aborts the clone with a context error instead of blocking
		// startup forever (the failure is then logged and non-blocking per SPEC
		// R10 Init — clone failures never fail startup).
		cloneCtx, cancel := context.WithTimeout(context.Background(), service.DefaultGitOperationTimeout)
		defer cancel()
		if err := gs.WithGitLock(func() error {
			return gs.CloneSingleBranch(cloneCtx, remoteURL, "main")
		}); err != nil {
			slog.Warn("Initial clone failed (non-blocking)", "error", err)
			if auditPub != nil {
				// The event is stamped with the pod's flow namespace and a
				// timestamp, matching the server's publishTelemetry
				// (cartographer_server.go) and the sync worker's publishFailure
				// (sync_worker.go): the AsyncPublisher forwards requests
				// verbatim, so the event must carry its own attribution or it
				// is stored un-attributable to a flow.
				auditPub.Submit(&flowv1.PublishRequest{
					Channel: "telemetry",
					Event: &flowv1.FlowEvent{
						EventId:       fmt.Sprintf("cartographer-clone-%d", time.Now().UnixNano()),
						EventType:     "cartographer.clone_failed",
						FlowNamespace: podNamespace,
						NodeId:        "cartographer",
						Timestamp:     timestamppb.Now(),
						Attributes:    map[string]string{"error": err.Error(), "url": remoteURL},
					},
				})
			}
		} else {
			slog.Info("Initial clone from remote succeeded")
			// SPEC R10 Init: the cloned git working tree seeded main.lbug's
			// file-per-element source, so re-hydrate main from those files. A
			// re-hydration failure is fatal: the identical condition (empty
			// main.lbug + committed git) is fatal in the R8 recovery path
			// (rehydrateMainAfterRecovery), and serving a vacuous empty graph
			// while graph-repo/ holds the cloned history hides committed data
			// behind a correctly-provisioned-but-empty service (SPEC R8
			// self-healing guarantee: re-hydration recovers the full graph
			// state). The error propagates to the caller, which exits startup.
			if rehydrate != nil {
				if rehydrateErr := rehydrate(); rehydrateErr != nil {
					return false, fmt.Errorf("re-hydrate main from cloned remote tree: %w", rehydrateErr)
				}
			}
		}
		// A fresh clone seeded main from the remote: there is no
		// locally-committed-but-unpushed data to catch up, so no push flag.
		return false, nil
	}
	// SPEC R10 Init: when the local repo already has commits, the sync worker's
	// first cycle pushes any locally-committed-but-unpushed data (startup
	// catch-up push), including unsent commits from a prior pod lifetime. The
	// push is NOT attempted here: the sync worker is constructed
	// after this init path, so this function only reports that a catch-up push
	// is needed and main.go flags the worker (SetPushNeeded) before its first
	// cycle runs. Routing the push through the worker keeps the R10 error-table
	// contract — recoverable failures are retried within the cycle with backoff
	// (up to 3 attempts) and the push flag is left set so the next cycle
	// retries; non-recoverable failures log immediately + telemetry and leave
	// the flag set — instead of a one-shot synchronous push that was logged +
	// telemetry only and never retried until the next Commit() or a restart. A
	// missing or invalid Secret has already failed startup via the pre-flight
	// check above (SPEC fail-startup clause, R1 Secret data keys).
	slog.Info("Remote configured, queueing catch-up push for the sync worker's first cycle", "url", remoteURL)
	return true, nil
}

func buildResolveAuthFn(
	remoteAuthSecretRef string,
	readSecretFn func(ctx context.Context, name string) (map[string]string, error),
	remoteURL string,
) func() (transport.AuthMethod, error) {
	return func() (transport.AuthMethod, error) {
		if remoteAuthSecretRef == "" {
			return nil, nil
		}
		parsedURL, parseErr := url.Parse(remoteURL)
		if parseErr != nil {
			return nil, parseErr
		}
		// SPEC.md:91-100 defines secret data keys only for ssh:// and https://;
		// a file:// remote has no auth keys, so a configured secretRef resolves
		// to explicit anonymous access (SPEC error-table row 987 lists file://
		// as a supported scheme). Short-circuit before the Secret read — or its
		// nil-reader guard — so neither a missing reader nor a Secret-read
		// failure can block a file:// remote. This mirrors gitstore's
		// requiresAuth, which never demands credentials for file:// remotes.
		if parsedURL.Scheme == "file" {
			return nil, nil
		}
		// Mirror tryRemotePullOnInit's pre-flight: a configured Secret ref with
		// no way to read it must fail closed (ErrAuthConfigMissing) rather than
		// widen to anonymous access — the git operation cannot be attempted
		// (SPEC error-table row "Remote auth config missing (Sync)" →
		// FAILED_PRECONDITION).
		if readSecretFn == nil {
			return nil, gitstore.ErrAuthConfigMissing
		}
		// go-git invokes this resolver without a context (the authFn signature
		// carries none), so the Secret read bounds itself with the sync worker's
		// per-operation deadline (service.DefaultGitOperationTimeout, SPEC R10 /
		// SPEC:981): a hung k8s API server aborts the read with a context error
		// instead of blocking the worker's git operation past its deadline and
		// wedging the worker.
		readCtx, cancel := context.WithTimeout(context.Background(), service.DefaultGitOperationTimeout)
		defer cancel()
		data, err := readSecretFn(readCtx, remoteAuthSecretRef)
		if err != nil {
			return nil, err
		}
		switch parsedURL.Scheme {
		case "ssh":
			sshUser := parsedURL.User.Username()
			if sshUser == "" {
				sshUser = "git"
			}
			keyPEM, hasKey := data["ssh-privatekey"]
			if hasKey && keyPEM != "" {
				signer, signErr := gogitssh.NewPublicKeys(sshUser, []byte(keyPEM), "")
				if signErr != nil {
					return nil, signErr
				}
				// SPEC R1: if known_hosts is present, reject connections to
				// unknown hosts (fail closed). If absent, skip verification.
				// Presence alone — even an empty value — fails closed: an empty
				// known_hosts means there are no known hosts, so every host is
				// unknown. This matches the missing-expected-key rule applied to
				// ssh-privatekey above (a present-but-empty data key is
				// equivalent to an absent one).
				if kh, hasKnownHosts := data["known_hosts"]; hasKnownHosts {
					tmp, tmpErr := os.CreateTemp("", "known_hosts-*")
					if tmpErr != nil {
						return nil, tmpErr
					}
					if _, writeErr := tmp.WriteString(kh); writeErr != nil {
						_ = tmp.Close()
						_ = os.Remove(tmp.Name())
						return nil, writeErr
					}
					tmpName := tmp.Name()
					_ = tmp.Close()
					cb, khErr := knownhosts.New(tmpName)
					_ = os.Remove(tmpName)
					if khErr != nil {
						return nil, khErr
					}
					signer.HostKeyCallback = cb.HostKeyCallback()
				} else {
					signer.HostKeyCallback = gossh.InsecureIgnoreHostKey()
				}
				return signer, nil
			}
			return nil, gitstore.ErrAuthConfigMissing
		case "https":
			httpsUser := parsedURL.User.Username()
			if httpsUser == "" {
				httpsUser = data["username"]
			}
			if password, hasPW := data["password"]; hasPW && password != "" {
				return &http.BasicAuth{Username: httpsUser, Password: password}, nil
			}
			return nil, gitstore.ErrAuthConfigMissing
		default:
			return nil, gitstore.ErrUnsupportedURLScheme
		}
	}
}

// isFatalServeError reports whether a grpc.Server.Serve return is a genuine
// serve failure that must abort the process. nil and grpc.ErrServerStopped are
// the normal outcomes of the shutdown goroutine calling GracefulStop/Stop —
// including the startup race where a signal lands before Serve is fully
// registered (Serve then returns ErrServerStopped) — and must fall through to
// the teardown join rather than exit 1.
func isFatalServeError(err error) bool {
	return err != nil && !errors.Is(err, grpc.ErrServerStopped)
}

func waitForShutdown(
	shutdownDone chan<- struct{},
	sigCh chan os.Signal,
	healthSrv *health.Server,
	grpcServer *grpc.Server,
	server *service.CartographerServer,
	dbStore store.Store,
	gs gitstore.GitStore,
	auditPub *eventbus.AsyncPublisher,
	eventBusCloser func() error,
	syncW *service.SyncWorker,
) {
	sig := <-sigCh
	slog.Info("Received signal, shutting down", "signal", sig)

	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)

	done := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		// ponytail: the 30s GracefulStop budget is hardcoded and coupled to the
		// Deployment's `terminationGracePeriodSeconds: 100` (deployment.yaml).
		// GracefulStop is allowed at most this long; immediately after this
		// select the remaining shutdown steps run and *consume the same process
		// budget* — the pod has roughly 100s - 30s = ~70s left before kubelet
		// SIGKILLs it.
		//
		// The first of those steps is syncW.Stop() (the next statement in
		// waitForShutdown), and it is the ONE step in this teardown that is NOT
		// bounded by any deadline:
		// Stop() signals the worker and blocks until its loop exits, and the
		// worker's final cycle on shutdown is a full sync cycle (fetch → merge →
		// re-hydrate → push; sync_worker.go run/runSyncCycle). Each git
		// operation in that cycle carries the worker's per-operation deadline
		// (gitOp, DefaultGitOperationTimeout = 5m), and both the fetch and push
		// legs retry up to 3 attempts with backoff — so with a hung remote,
		// Stop() can block for tens of minutes (fetch ≤3 × 5m + push ≤3 × 5m +
		// backoff), far past the ~100s window. kubelet then SIGKILLs the pod
		// mid-Stop: the final cycle is abandoned (worst case, a pending push
		// never delivered — recovered by the next startup's catch-up push) and,
		// critically, the durability teardown below (dbStore.Close, git
		// RestoreMain/CleanUntracked) never runs at all, so git can be left on a
		// stranded transaction branch that the next startup's R8 re-hydration
		// must reconcile. In the common case the worker's cycle is fast
		// (sub-second when the remote is healthy and idle), so this only
		// manifests when the remote is hung at shutdown time.
		//
		// Ceiling: the budget number lives only on each side (code 30s /
		// manifest 100s) with no single source of truth or guard that one stays
		// below the other — the 70s headroom silently shrinks whenever one is
		// changed without the other — and the final sync cycle's duration is
		// bounded only by the per-operation gitOp deadlines multiplied by the
		// fetch/push retry counts, which are invisible here.
		// Upgrade path: derive both from one shared constant, and bound the
		// final cycle the same way the budget's other steps are bounded — e.g.
		// run syncW.Stop() under a select with a bounded timer (or give the
		// worker a StopWithTimeout) so the durability teardown always starts
		// within the leftover window, and surface (log) when the teardown
		// approaches the grace window so operators can size
		// terminationGracePeriodSeconds from evidence.
		grpcServer.Stop()
	}

	if syncW != nil {
		slog.Info("Shutting down sync worker")
		syncW.Stop()
	}

	server.StopGC()
	// Close failures on the main durability store propagate: a failed close can
	// leave the branch LADYBUGDB connections and the main handle unflushed, so a
	// subsequent SIGKILL mid-teardown could lose the tail of the graph. There is
	// no caller left to receive the error after main() returns, so surface it.
	if err := dbStore.Close(); err != nil {
		slog.Error("Shutdown: failed to close main db", "error", err)
	}

	// RestoreMain (git working-tree switch back to main) and CleanUntracked
	// (removing residual untracked files) are the highest-consequence shutdown
	// drops in this teardown: failing them leaves the working tree stranded on a
	// transaction branch or carrying stale files, which the next startup's R8
	// re-hydration would interpret as the live graph. Any failure -- the lock
	// acquisition itself, RestoreMain, or CleanUntracked -- is therefore
	// propagated up out of WithGitLock rather than discarded (Go-idiom
	// store-layer I/O-error propagation), so a stranded tree is surfaced to the
	// operator instead of being silently re-read as live on restart. There is
	// no caller left to receive a returned error after main() returns, so
	// surface it with a correlation message.
	if err := gs.WithGitLock(func() error {
		if err := gs.RestoreMain(context.Background()); err != nil {
			slog.Error("Shutdown: failed to restore git working tree to main", "error", err)
			return err
		}
		if err := gs.CleanUntracked(context.Background()); err != nil {
			slog.Error("Shutdown: failed to clean residual untracked git files", "error", err)
			return err
		}
		return nil
	}); err != nil {
		slog.Error("Shutdown: git working-tree teardown failed; tree may be left stranded and "+
			"misread as live on next startup's R8 re-hydration", "error", err)
	}

	if auditPub != nil {
		auditPub.Stop()
	}
	if eventBusCloser != nil {
		if err := eventBusCloser(); err != nil {
			slog.Error("Shutdown: failed to close event bus connection", "error", err)
		}
	}

	slog.Info("Cartographer shut down")
	close(shutdownDone)
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// newHealthServer returns the standard gRPC health service with the empty
// (whole-server) service reported as SERVING. SPEC R5 requires the health
// service to report SERVING before the first ApplySchema, so main registers
// this state at startup; the shutdown path flips it to NOT_SERVING.
func newHealthServer() *health.Server {
	srv := health.NewServer()
	srv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	return srv
}

// loadPullOnInit reads the REMOTE_PULL_ON_INIT knob (default false), failing
// fast on an unparseable value (SPEC R5 fail-fast env guard). The fail-fast
// decision (return an error) is factored into parseBoolEnv so it is
// unit-testable without os.Exit; this wrapper owns the process exit, mirroring
// loadVerificationKey.
func loadPullOnInit() bool {
	v, err := parseBoolEnv(false)
	if err != nil {
		slog.Error("invalid REMOTE_PULL_ON_INIT", "error", err)
		os.Exit(1)
	}
	return v
}

// parseBoolEnv parses the REMOTE_PULL_ON_INIT boolean environment variable
// case-insensitively via strconv.ParseBool (accepts "true"/"false"/"1"/"0"/"t"/"f"),
// falling back to defaultVal on an empty/unset value. An unparseable value
// returns an error (SPEC R5 fail-fast, mirroring parseDurationEnv): the caller
// exits the process rather than silently running with a wrong pull-on-init
// setting.
func parseBoolEnv(defaultVal bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv("REMOTE_PULL_ON_INIT"))
	if v == "" {
		return defaultVal, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("invalid boolean value %q for REMOTE_PULL_ON_INIT: %w", v, err)
	}
	return b, nil
}

// parseDurationEnv parses a duration environment variable via
// time.ParseDuration, falling back to defaultVal on an empty value. An
// unparseable value returns an error (SPEC R5 fail-fast: the caller exits the
// process rather than silently running with a wrong timeout/window).
func parseDurationEnv(key, defaultVal string) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return time.ParseDuration(defaultVal)
	}
	return time.ParseDuration(v)
}

// loadDuration parses a duration environment variable, failing startup fast on
// an unparseable value (SPEC R5 fail-fast env guard). The fail-fast decision
// (return an error) is factored into parseDurationEnv so it is unit-testable
// without os.Exit; this wrapper owns the process exit, mirroring loadPullOnInit
// and loadVerificationKey.
func loadDuration(key, defaultVal string) time.Duration {
	d, err := parseDurationEnv(key, defaultVal)
	if err != nil {
		slog.Error("invalid "+key, "error", err)
		os.Exit(1)
	}
	return d
}

// loadPositiveDuration is loadDuration plus the SPEC R5 non-positive guard: a
// non-positive duration parses cleanly but breaks the runtime at serve time
// (time.NewTicker panics on a non-positive interval; a non-positive
// TRANSACTION_TIMEOUT makes every BeginTransaction fail with INVALID_ARGUMENT),
// so it must fail startup just like an unparseable value.
func loadPositiveDuration(key, defaultVal string) time.Duration {
	d := loadDuration(key, defaultVal)
	if d <= 0 {
		slog.Error("invalid "+key, "value", d.String(), "error", "must be a positive duration")
		os.Exit(1)
	}
	return d
}

func loadVerificationKey(envVar string) ed25519.PublicKey {
	// SPEC R5 fail-closed env guard: missing or malformed verification keys
	// are fatal. The fail-closed decision (return an error) is factored into
	// parseVerificationKey so it is unit-testable without os.Exit; the caller
	// owns the process exit.
	key, err := parseVerificationKey(envVar)
	if err != nil {
		slog.Error("Invalid verification key", "var", envVar, "error", err)
		os.Exit(1)
	}
	return key
}

// parseVerificationKey returns the editor verification public key from an
// environment variable, or an error if it is absent or malformed. The operator
// provisions the public key base64-encoded in the Secret's `key` field (see
// operator foundrygraph_keys.go reconcileSecrets): the per-namespace Secret is
// consumed by the Cartographer through a secretKeyRef env var, and POSIX execve
// truncates env values at the first NUL byte — ~12% of random Ed25519 public
// keys contain a NUL byte, so a raw 32-byte key would be silently truncated and
// fail closed on every verification. The env var therefore holds the base64
// encoding of the raw key, which is decoded here.
func parseVerificationKey(envVar string) (ed25519.PublicKey, error) {
	b64 := os.Getenv(envVar)
	if b64 == "" {
		return nil, fmt.Errorf("missing required environment variable %s", envVar)
	}
	keyBytes, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("invalid verification key encoding (expected base64): %w", err)
	}
	if len(keyBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid verification key length: expected %d, got %d", ed25519.PublicKeySize, len(keyBytes))
	}
	return ed25519.PublicKey(keyBytes), nil
}

// newReadSecretFn builds the SPEC R1 (SPEC.md:103) Secret reader: the Cartographer
// reads the referenced Secret via its pod's ServiceAccount on each remote
// operation, so rotation takes effect without restart. The returned function
// fetches the Secret by name in the pod namespace and decodes every Data byte
// slice into a string, propagating the clientset's error unchanged on a failed
// fetch. The clientset is an interface so tests can drive the real k8s wrapper
// with the in-memory fake clientset.
func newReadSecretFn(clientset kubernetes.Interface, namespace string) func(
	ctx context.Context, name string,
) (map[string]string, error) {
	return func(ctx context.Context, name string) (map[string]string, error) {
		secret, err := clientset.CoreV1().Secrets(namespace).Get(ctx, name, v1.GetOptions{})
		if err != nil {
			return nil, err
		}
		result := make(map[string]string, len(secret.Data))
		for k, v := range secret.Data {
			result[k] = string(v)
		}
		return result, nil
	}
}
