// Command cartographer is the Active Knowledge Graph service for Foundry Flow.
package main

import (
	"context"
	"crypto/ed25519"
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
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	// -----------------------------------------------------------------------
	// 1. Read environment variables
	// -----------------------------------------------------------------------
	ladybugDBPath := getEnv("LADYBUG_DB_PATH", "/data")
	cartographerPort := getEnv("CARTOGRAPHER_PORT", "50051")
	remoteURL := os.Getenv("REMOTE_URL")
	remoteAuthSecretRef := os.Getenv("REMOTE_AUTH_SECRET_REF")
	remotePullOnInit := parseBoolEnv("REMOTE_PULL_ON_INIT", false)
	podNamespace := getEnv("POD_NAMESPACE", "default")
	eventBusAddress := os.Getenv("EVENT_BUS_ADDRESS")

	// SPEC R5 fail-fast env guard: an unparseable duration is fatal. The
	// fail-fast decision (return an error) is factored into parseDurationEnv
	// so it is unit-testable without os.Exit; the caller owns the process exit.
	transactionTimeout, err := parseDurationEnv("TRANSACTION_TIMEOUT", "30m")
	if err != nil {
		slog.Error("invalid TRANSACTION_TIMEOUT", "error", err)
		os.Exit(1)
	}
	// SPEC R5 fail-fast guard: a non-positive TRANSACTION_TIMEOUT parses
	// cleanly but makes every BeginTransaction fail at runtime with
	// INVALID_ARGUMENT ("requestedTimeout must be positive",
	// transaction_manager.go:170-172), so it must fail startup just like an
	// unparseable value. Mirrors the SYNC_INTERVAL positivity guard below.
	if transactionTimeout <= 0 {
		slog.Error("invalid TRANSACTION_TIMEOUT", "value", transactionTimeout.String(),
			"error", "must be a positive duration")
		os.Exit(1)
	}

	stalenessWindow, err := parseDurationEnv("CAPABILITY_STALENESS_WINDOW", "30s")
	if err != nil {
		slog.Error("invalid CAPABILITY_STALENESS_WINDOW", "error", err)
		os.Exit(1)
	}

	// SPEC R10 sync worker "wakes every minute (configurable)": the periodic
	// interval is an env knob whose default is sourced from the worker's own
	// DefaultSyncInterval constant, so the wiring default and the worker
	// default share one source of truth. Fail-fast on unparseable or
	// non-positive values: time.NewTicker panics on a non-positive interval,
	// so a bad SYNC_INTERVAL must fail startup with a clear message rather
	// than crash the worker goroutine mid-run.
	syncInterval, err := parseDurationEnv("SYNC_INTERVAL", service.DefaultSyncInterval.String())
	if err != nil {
		slog.Error("invalid SYNC_INTERVAL", "error", err)
		os.Exit(1)
	}
	if syncInterval <= 0 {
		slog.Error("invalid SYNC_INTERVAL", "value", syncInterval.String(),
			"error", "must be a positive duration")
		os.Exit(1)
	}

	// Validate and load verification keys early (fail-fast on missing keys).
	operatorKey := loadVerificationKey("OPERATOR_VERIFICATION_KEY")
	sidecarKey := loadVerificationKey("SIDECAR_VERIFICATION_KEY")

	// -----------------------------------------------------------------------
	// 2. Open LadybugDB database at <path>/main.lbug
	// -----------------------------------------------------------------------
	dbStore, dbErr := ladybug.Open(ladybugDBPath)

	// -----------------------------------------------------------------------
	// 3. Initialise gitstore at <path>/graph-repo/
	// -----------------------------------------------------------------------
	gs, gsErr := gitstore.New(ladybugDBPath)
	if gsErr != nil {
		slog.Error("Failed to initialise gitstore", "error", gsErr)
		os.Exit(1)
	}
	slog.Info("Git repository open", "path", filepath.Join(ladybugDBPath, "graph-repo"))

	// -----------------------------------------------------------------------
	// 4. Fail closed on main.lbug open failure (SPEC R8 corruption-only recovery)
	// -----------------------------------------------------------------------
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

	// SPEC R8 (SPEC.md:509-519): after ladybug.Open recovered a corrupted
	// main.lbug by deleting and re-opening it fresh, main holds schema metadata
	// but no graph data. When the git repository has commits, re-hydrate main
	// from the file-per-element representation so the service does not serve a
	// vacuous empty graph while committed data exists. The working tree is
	// switched back to main (RestoreMain + CleanUntracked) before files are
	// read: after a crash (SIGKILL/eviction) the tree can be stranded on a
	// transaction branch whose snapshot predates main's current commits, and
	// re-hydrating a healthy main.lbug from that stale snapshot would silently
	// roll back committed data. With the transaction-only write model there
	// are no non-transactional writes to main.lbug that git does not already
	// contain, so re-hydration from git is always complete and safe: any
	// non-empty repo is re-hydrated unconditionally; a fresh install is a
	// no-op (an empty git repo has no committed state to recover). A failure
	// here is fatal: serving an empty graph after a corrupt-reopen silently
	// drops all committed data, so fail loudly instead.
	if err := rehydrateMainAfterRecovery(context.Background(), dbStore, gs); err != nil {
		slog.Error("Failed to re-hydrate main from git after open (SPEC R8 recovery)",
			"error", err,
		)
		os.Exit(1)
	}

	// -----------------------------------------------------------------------
	// 5. Set up Kubernetes client for Secret reading
	// -----------------------------------------------------------------------
	var readSecretFn func(ctx context.Context, name string) (map[string]string, error)

	k8sConfig, inClusterErr := rest.InClusterConfig()
	if inClusterErr != nil {
		var kubeErr error
		k8sConfig, kubeErr = clientcmd.BuildConfigFromFlags("", "")
		if kubeErr != nil {
			slog.Warn("Kubeconfig fallback also failed (expected when running outside cluster)", "error", kubeErr)
		}
	}

	if k8sConfig != nil {
		clientset, kErr := kubernetes.NewForConfig(k8sConfig)
		if kErr != nil {
			slog.Warn("Failed to create Kubernetes clientset", "error", kErr)
			readSecretFn = func(_ context.Context, _ string) (map[string]string, error) {
				return nil, fmt.Errorf("kubernetes client unavailable: %w", kErr)
			}
		} else {
			readSecretFn = newReadSecretFn(clientset, podNamespace)
			slog.Info("Kubernetes client initialised", "namespace", podNamespace)
		}
	} else {
		slog.Warn("Kubernetes client not configured — running outside cluster")
		readSecretFn = func(ctx context.Context, name string) (map[string]string, error) {
			return nil, fmt.Errorf("kubernetes client not configured")
		}
	}

	// -----------------------------------------------------------------------
	// 5a. Configure remote auth on gitstore
	// -----------------------------------------------------------------------
	if remoteURL != "" {
		resolveAuthFn := buildResolveAuthFn(remoteAuthSecretRef, readSecretFn, remoteURL)
		if err := gs.SetRemote(context.Background(), remoteURL, resolveAuthFn); err != nil {
			// ponytail: a misconfigured remote (unsupported or erroneous URL
			// scheme, parse failure, or missing host) is warn-logged but not
			// fatal at startup. SetRemote validates the URL before storing it
			// (remote.go validateRemoteURL), so after this failure g.remoteURL
			// stays empty even though REMOTE_URL is set — and the sync worker
			// is still created below because it keys off the env var, not
			// g.remoteURL. The misconfiguration therefore degrades silently:
			// the first cycle's fetch returns ErrNoRemote ("no remote
			// configured"), which the fetch path treats as a benign no-op, and
			// a pending catch-up push surfaces FAILED_PRECONDITION "no remote
			// configured" via mapGitError (errors.go) on the WithAck/Commit
			// surface — never INVALID_ARGUMENT. ErrUnsupportedURLScheme is
			// unreachable in the sync cycle: the scheme was already validated
			// here, and resolveAuth folds any resolver error (including
			// buildResolveAuthFn's unsupported-scheme default branch) into
			// ErrAuthConfigMissing. The one loud signal is the pullOnInit=true
			// + secretRef startup path, whose pre-flight re-parses the URL and
			// exits the process on an unsupported scheme (tryRemotePullOnInit).
			// Upgrade path: make the fatal behavior unconditional — fail
			// startup (os.Exit) on SetRemote error whenever remoteURL is
			// explicitly set; revisit if silent degradation for optional
			// remotes becomes a product requirement.
			slog.Warn("Failed to configure remote", "error", err)
		} else {
			slog.Info("Remote configured", "url", remoteURL)
		}
	}

	// -----------------------------------------------------------------------
	// 6. Connect to Event Bus
	// -----------------------------------------------------------------------
	var auditPub *eventbus.AsyncPublisher
	var eventBusCloser func() error

	if eventBusAddress != "" {
		ebConn, cErr := grpc.NewClient(eventBusAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
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
			slog.Error("Failed to connect to Event Bus", "address", eventBusAddress, "error", cErr)
			os.Exit(1)
		}
		eventBusCloser = ebConn.Close
		ebClient := flowv1.NewFlowEventBusServiceClient(ebConn)
		auditPub = eventbus.NewAsyncPublisher(ebClient)
		slog.Info("Event Bus connected for telemetry", "address", eventBusAddress)
	} else {
		slog.Info("Event Bus not configured, telemetry publishing disabled")
	}

	// -----------------------------------------------------------------------
	// 7. Optional remote pull on init + startup catch-up push decision
	// -----------------------------------------------------------------------
	// initCatchUpPush records whether R10 Init found locally-committed-but-
	// unpushed data that the sync worker's first cycle must push (SPEC R10 Init,
	// SPEC.md:640-641: "The sync worker pushes any locally-committed-but-
	// unpushed data on its first cycle (startup catch-up push), including any
	// unsent commits from a prior pod lifetime"). The decision is independent
	// of pullOnInit: the pull is optional, but the startup catch-up push is
	// not — a prior pod that terminated before its push completed left its
	// commits in the local git repo (the push flag itself is in-memory and is
	// lost on restart), and only the worker's first cycle can deliver them.
	// The worker is constructed after this init path, so tryRemotePullOnInit /
	// startupCatchUpPushNeeded report the decision and main wires it into the
	// worker before the first cycle runs.
	var initCatchUpPush bool
	if remotePullOnInit && remoteURL != "" {
		catchUpPush, err := tryRemotePullOnInit(gs, remoteURL, remoteAuthSecretRef, readSecretFn, auditPub,
			// SPEC R10 Init: after clone-on-init seeds the git working tree,
			// re-hydrate main from the cloned file-per-element representation so
			// the graph is not empty. With the transaction-only write model there
			// are no non-transactional writes to main.lbug that git does not
			// already contain, so re-hydration from git is always complete and
			// safe. The clone path runs only when the local repo has no
			// graph-data commits (IsEmpty), so there is no local committed graph
			// for the clone to supersede.
			func() error {
				entitiesDir := filepath.Join(ladybugDBPath, "graph-repo/entities")
				edgesDir := filepath.Join(ladybugDBPath, "graph-repo/edges")
				return dbStore.RehydrateMainFromFiles(context.Background(), entitiesDir, edgesDir)
			},
		)
		if err != nil {
			slog.Error("Remote init failed; aborting startup", "error", err)
			os.Exit(1)
		}
		initCatchUpPush = catchUpPush
	} else if remoteURL != "" {
		// pullOnInit=false (the common default): no clone runs, but the
		// startup catch-up push still applies — unsent commits from a prior pod
		// lifetime must be delivered by the worker's first cycle
		// (SPEC.md:640-641; GIT_PLAN.md:136's restart rationale depends on it).
		// A repo-state check failure is logged and non-blocking, mirroring
		// tryRemotePullOnInit's IsEmpty handling (SPEC R10 Init).
		initCatchUpPush = startupCatchUpPushNeeded(context.Background(), gs)
	}

	// -----------------------------------------------------------------------
	// 8,9. Construct CartographerServer with options
	// -----------------------------------------------------------------------
	var opts []service.CartographerOption
	if auditPub != nil {
		opts = append(opts, service.WithAuditPublisher(auditPub))
	}
	opts = append(opts, service.WithLadybugPath(ladybugDBPath))

	// Create the background sync worker if a remote URL is configured. Its
	// goroutine is started only after server construction and transaction
	// recovery (see the SPEC R10 / GIT_PLAN Phase 2 item 8 note below).
	var syncW *service.SyncWorker
	if remoteURL != "" {
		// Permanent sync failures emit an operator-visible Event Bus telemetry
		// event (SPEC R10 / GIT_PLAN "log loudly + telemetry"), so the worker
		// shares the server's audit publisher when one is configured, and stamps
		// the same flow namespace (FlowNamespace: podNamespace) the server's
		// publishTelemetry uses, so the two emitters stay consistent.
		var syncOpts []service.SyncWorkerOption
		if auditPub != nil {
			syncOpts = append(syncOpts, service.SyncWorkerWithAuditPublisher(auditPub))
		}
		syncOpts = append(syncOpts, service.SyncWorkerWithPodNamespace(podNamespace))
		syncOpts = append(syncOpts, service.SyncWorkerWithSyncInterval(syncInterval))
		syncW = service.NewSyncWorker(remoteURL, gs, dbStore, service.RealClock{}, syncOpts...)
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
		dbStore, gs, operatorKey, sidecarKey,
		readSecretFn, remoteURL, stalenessWindow,
		podNamespace, transactionTimeout, store.DefaultChangeLogCap,
		opts...,
	)
	slog.Info("Cartographer server constructed")

	// -----------------------------------------------------------------------
	// 10. Recover open transactions
	// -----------------------------------------------------------------------
	if err := server.RecoverOpenTransactions(context.Background()); err != nil {
		slog.Error("Failed to recover open transactions", "error", err)
		os.Exit(1)
	}
	slog.Info("Open transactions recovered")

	// SPEC R10 / GIT_PLAN Phase 2 item 8 ("after the Cartographer server is
	// constructed, create and start the syncWorker"): the worker goroutine is
	// started only after server construction AND transaction recovery. Run()
	// executes an immediate first cycle (fetch → restore-main → clean →
	// re-hydrate), which must not run concurrently with the recovery path:
	// recovery's main-file reads (buildMainFileLookups, cartographer_server.go)
	// happen outside the git lock after ListBranches, so a concurrent first
	// cycle could snapshot or re-hydrate a stale or mid-recovery working tree
	// in the crash-strand scenario. SetPushNeeded (above) is still applied
	// before the first cycle runs.
	if syncW != nil {
		go syncW.Run()
		slog.Info("Background sync worker started")
	}

	// -----------------------------------------------------------------------
	// 11. Mark dbReady
	// -----------------------------------------------------------------------
	server.MarkDBReady()

	// -----------------------------------------------------------------------
	// 12. Create gRPC server with health probe, capability interceptor, reflection
	// -----------------------------------------------------------------------
	// SPEC R5: before the first ApplySchema, the standard health service
	// reports SERVING. The setup is factored into newHealthServer so the
	// startup health state is unit-testable without booting main.
	healthSrv := newHealthServer()

	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(server.Verifier().VerifyInterceptor),
		grpc.ChainStreamInterceptor(server.Verifier().VerifyStreamInterceptor),
	)

	flowv1.RegisterCartographerServiceServer(grpcServer, server)
	grpc_health_v1.RegisterHealthServer(grpcServer, healthSrv)
	reflection.Register(grpcServer)

	lis, lErr := net.Listen("tcp", ":"+cartographerPort)
	if lErr != nil {
		slog.Error("Failed to listen", "port", cartographerPort, "error", lErr)
		os.Exit(1)
	}
	slog.Info("gRPC server listening", "address", lis.Addr().String())

	// -----------------------------------------------------------------------
	// 13. Start GC goroutine
	// -----------------------------------------------------------------------
	server.StartGC()

	// -----------------------------------------------------------------------
	// 14. Handle graceful shutdown on SIGTERM/SIGINT
	// -----------------------------------------------------------------------
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	shutdownDone := make(chan struct{})
	go waitForShutdown(shutdownDone, sigCh, healthSrv, grpcServer, server, dbStore, gs, auditPub, eventBusCloser, syncW)

	// -----------------------------------------------------------------------
	// 15. Serve
	// -----------------------------------------------------------------------
	slog.Info("Cartographer ready")
	if err := grpcServer.Serve(lis); isFatalServeError(err) {
		slog.Error("gRPC serve error", "error", err)
		os.Exit(1)
	}
	// Serve returns nil (or ErrServerStopped) once the shutdown goroutine called
	// GracefulStop/Stop. Wait for that goroutine to finish its durability teardown
	// (dbStore.Close, git RestoreMain/CleanUntracked, auditPub.Stop, event bus
	// close) before main returns, so the process does not exit (terminating the
	// goroutine) mid-cleanup and the terminationGracePeriodSeconds budget is
	// honoured.
	<-shutdownDone
}

// rehydrateMainAfterRecovery re-synchronizes main LadybugDB from the git
// working tree when the startup open left main holding no graph data but the
// git repository has commits (SPEC R8 corruption recovery, SPEC.md:509-519).
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
// any locally-committed-but-unpushed data (SPEC R10 Init / GIT_PLAN.md:33);
// the caller (main) sets the worker's push flag from this before the first
// cycle runs. The push itself is deliberately NOT performed here: routing it
// through the worker's cycle keeps the R10 error-table retry contract.
func tryRemotePullOnInit(
	gs gitstore.GitStore,
	remoteURL string,
	remoteAuthSecretRef string,
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
		data, err := readSecretFn(context.Background(), remoteAuthSecretRef)
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
		if err := gs.WithGitLock(func() error {
			return gs.CloneSingleBranch(context.Background(), remoteURL, "main")
		}); err != nil {
			slog.Warn("Initial clone failed (non-blocking)", "error", err)
			if auditPub != nil {
				auditPub.Submit(&flowv1.PublishRequest{
					Channel: "telemetry",
					Event: &flowv1.FlowEvent{
						EventId:    fmt.Sprintf("cartographer-clone-%d", time.Now().UnixNano()),
						EventType:  "cartographer.clone_failed",
						NodeId:     "cartographer",
						Attributes: map[string]string{"error": err.Error(), "url": remoteURL},
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
	// SPEC R10 Init / GIT_PLAN.md:33: when the local repo already has commits,
	// the sync worker's first cycle pushes any locally-committed-but-unpushed
	// data (startup catch-up push), including unsent commits from a prior pod
	// lifetime. The push is NOT attempted here: the sync worker is constructed
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
		data, err := readSecretFn(context.Background(), remoteAuthSecretRef)
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
		// select the durability teardown runs (StopGC, dbStore.Close, git
		// RestoreMain/CleanUntracked) and *consumes the same process budget* —
		// it has roughly 100s - 30s = ~70s left before kubelet SIGKILLs the pod.
		// Failure mode: if the teardown (a slow dbStore.Close flushing the branch
		// connections + main handle, or a slow git lock acquisition + RestoreMain
		// + CleanUntracked) exceeds that leftover ~70s window, the process is
		// SIGKILLed mid-teardown and git is left on a stranded transaction branch
		// that the next startup's R8 re-hydration must reconcile. If deployment.yaml's grace
		// period is ever lowered below this 30s budget, a slow drain consumes the
		// whole window and the durability steps never run at all. Ceiling: the
		// budget number lives only on each side (code 30s / manifest 100s) with no
		// single source of truth or guard that one stays below the other — the 70s
		// headroom silently shrinks whenever one is changed without the other.
		// Upgrade path: derive both from one shared constant, or make the teardown
		// independently bounded and surface (log) when it approaches the grace
		// window so operators can size terminationGracePeriodSeconds from evidence.
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
	// gitStore.Close is a documented no-op (interface conformance only), so an
	// error is not worth distinguishing from nil.
	_ = gs.Close()

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

// parseBoolEnv parses a boolean environment variable case-insensitively via
// strconv.ParseBool (accepts "true"/"false"/"1"/"0"/"t"/"f"), falling back to
// defaultVal on empty or unparseable values instead of silently dropping
// pull-on-init at startup.
func parseBoolEnv(key string, defaultVal bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return defaultVal
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		slog.Warn("Invalid boolean env var (falling back to default)", "var", key, "value", v, "error", err)
		return defaultVal
	}
	return b
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

// parseVerificationKey returns the editor verification public key from a
// environment variable, or an error if it is absent or malformed. The operator
// provisions the public key as raw 32-byte Ed25519 bytes in the Secret's `key`
// field (see operator foundrygraph_keys.go), so the env var holds the raw key —
// no base64 decoding.
func parseVerificationKey(envVar string) (ed25519.PublicKey, error) {
	keyBytes := os.Getenv(envVar)
	if keyBytes == "" {
		return nil, fmt.Errorf("missing required environment variable %s", envVar)
	}
	if len(keyBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid verification key length: expected %d, got %d", ed25519.PublicKeySize, len(keyBytes))
	}
	return ed25519.PublicKey([]byte(keyBytes)), nil
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
