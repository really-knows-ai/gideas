// Command cartographer is the Active Knowledge Graph service for Foundry Flow.
package main

import (
	"context"
	"crypto/ed25519"
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
	transactionTimeoutStr := getEnv("TRANSACTION_TIMEOUT", "30m")
	remoteURL := os.Getenv("REMOTE_URL")
	remoteAuthSecretRef := os.Getenv("REMOTE_AUTH_SECRET_REF")
	remotePullOnInit := parseBoolEnv("REMOTE_PULL_ON_INIT", false)
	podNamespace := getEnv("POD_NAMESPACE", "default")
	stalenessWindowStr := getEnv("CAPABILITY_STALENESS_WINDOW", "30s")
	eventBusAddress := os.Getenv("EVENT_BUS_ADDRESS")

	transactionTimeout, err := time.ParseDuration(transactionTimeoutStr)
	if err != nil {
		slog.Error("invalid TRANSACTION_TIMEOUT", "error", err)
		os.Exit(1)
	}

	stalenessWindow, err := time.ParseDuration(stalenessWindowStr)
	if err != nil {
		slog.Error("invalid CAPABILITY_STALENESS_WINDOW", "error", err)
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
	// main.lbug. ladybug.Open already performs that recovery itself: on a
	// readable-but-unparseable file (corruptionCandidates) it removes main.lbug
	// and re-opens a fresh database internally, returning nil error. Any
	// non-nil error returned here is therefore an operational (IO/permission)
	// or post-open failure (OpenConnection / extension LOAD / rebuildSchemaCache
	// / restoreMainSchemaMetadata) — it does NOT prove main.lbug is corrupt.
	// The store deliberately refuses to delete a database it cannot prove
	// corrupt (see corruptionCandidate in ladybug.go), so deleting main.lbug
	// here would permanently destroy durable non-transactional writes from a
	// possibly-valid database. Fail closed without touching the file.
	if dbErr != nil {
		slog.Error("Failed to open main.lbug; refusing to delete database (failure is not proven corruption)",
			"path", filepath.Join(ladybugDBPath, "main.lbug"),
			"error", dbErr,
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
	// 7. Optional remote pull on init
	// -----------------------------------------------------------------------
	if remotePullOnInit && remoteURL != "" {
		if err := tryRemotePullOnInit(gs, remoteURL, remoteAuthSecretRef, readSecretFn, auditPub,
			// SPEC R10 Init: after clone-on-init seeds the git working tree,
			// re-hydrate the freshly-opened empty main.lbug from the cloned
			// file-per-element representation so the graph is not empty.
			func() error {
				entitiesDir := filepath.Join(ladybugDBPath, "graph-repo/entities")
				edgesDir := filepath.Join(ladybugDBPath, "graph-repo/edges")
				return dbStore.RehydrateMainFromFiles(context.Background(), entitiesDir, edgesDir)
			},
		); err != nil {
			slog.Error("Pre-flight auth config failure", "error", err)
			os.Exit(1)
		}
	}

	// -----------------------------------------------------------------------
	// 8,9. Construct CartographerServer with options
	// -----------------------------------------------------------------------
	var opts []service.CartographerOption
	if auditPub != nil {
		opts = append(opts, service.WithAuditPublisher(auditPub))
	}
	opts = append(opts, service.WithLadybugPath(ladybugDBPath))

	// ponytail: 100000 is the per-transaction change-log admission cap (100K
	// entries) mandated by the SPEC's change-log capacity requirement and
	// handed to NewChangeLogWithCap inside the TransactionManager; exceeding it
	// yields ErrChangeLogFull → RESOURCE_EXHAUSTED with transaction rollback.
	// The enforcement ceiling lives at the store layer and is threaded here via
	// the constructor argument. If this call-site literal and the store default
	// ever diverge, mutation-cap behavior silently differs from the documented
	// ceiling. Ceiling: hard-coded in cmd/main and mirrored only by tests that
	// pass 100000 by rote; no single definition of truth. Upgrade path: hoist a
	// shared constant (e.g. in the service package) that both this call and the
	// store default read, so divergence is a compile error rather than a drift.
	server := service.NewCartographerServer(
		dbStore, gs, operatorKey, sidecarKey,
		readSecretFn, remoteURL, stalenessWindow,
		podNamespace, transactionTimeout, 100000,
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

	// -----------------------------------------------------------------------
	// 11. Mark dbReady
	// -----------------------------------------------------------------------
	server.MarkDBReady()

	// -----------------------------------------------------------------------
	// 12. Create gRPC server with health probe, capability interceptor, reflection
	// -----------------------------------------------------------------------
	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

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
	go waitForShutdown(shutdownDone, sigCh, healthSrv, grpcServer, server, dbStore, gs, auditPub, eventBusCloser)

	// -----------------------------------------------------------------------
	// 15. Serve
	// -----------------------------------------------------------------------
	slog.Info("Cartographer ready")
	if err := grpcServer.Serve(lis); err != nil {
		slog.Error("gRPC serve error", "error", err)
		os.Exit(1)
	}
	// Serve returns here because the shutdown goroutine called GracefulStop/Stop.
	// Wait for that goroutine to finish its teardown before main returns, so the
	// process does not exit (terminating the goroutine) mid-cleanup.
	<-shutdownDone
}

func tryRemotePullOnInit(
	gs gitstore.GitStore,
	remoteURL string,
	remoteAuthSecretRef string,
	readSecretFn func(ctx context.Context, name string) (map[string]string, error),
	auditPub *eventbus.AsyncPublisher,
	rehydrate func() error,
) error {
	authFn := func() error {
		if remoteAuthSecretRef == "" {
			return nil
		}
		if readSecretFn == nil {
			return gitstore.ErrAuthConfigMissing
		}
		data, err := readSecretFn(context.Background(), remoteAuthSecretRef)
		if err != nil {
			return fmt.Errorf("pre-flight auth: read secret: %w", err)
		}
		parsedURL, parseErr := url.Parse(remoteURL)
		if parseErr != nil {
			return fmt.Errorf("pre-flight auth: parse URL: %w", parseErr)
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
		return authErr
	}
	empty, err := gs.IsEmpty(context.Background())
	if err != nil {
		// SPEC R10 Init: repository-state check failures are logged, not fatal.
		slog.Warn("Failed to check git repo state on init (non-blocking)", "error", err)
		return nil
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
			// file-per-element source, so re-hydrate main from those files.
			// If re-hydration fails, startup continues (headers a valid empty
			// graph); R8's recovery path will re-hydrate on the next start.
			if rehydrate != nil {
				if rehydrateErr := rehydrate(); rehydrateErr != nil {
					// ponytail: after clone-on-init seeds graph-repo/, a failure to
					// re-hydrate main.lbug from those files is logged and swallowed
					// (non-blocking), and startup proceeds. Consequence: mainDB is
					// served as an *empty* graph even though graph-repo/ holds the
					// cloned history, so the service is up but returns nothing until
					// the next start re-runs R8's re-hydration from the same files
					// (which, having a durable graph present, recovers the full
					// state). A caller that queries before that restart sees a
					// correctly-provisioned-but-vacuous graph with no error to
					// distinguish it from a genuinely empty flow. This partially
					// violates the SPEC R8 self-healing guarantee within a single
					// boot. Upgrade path: fail closed on a JOF (JS-mount-style)
					// here, or retry the re-hydration in a bounded loop before
					// serving; the swallow is kept because the common failure
					// (transient FS error during first boot) resolves on restart
					// and blocking would convert a soft miss into a hard outage.
					slog.Warn("Initial clone re-hydration failed (non-blocking)", "error", rehydrateErr)
				}
			}
		}
	} else {
		// SPEC R10 Init: when the local repo already has commits, catch-up push
		// so a local head ahead of the remote is propagated. Failures are logged
		// and deferred — they do not block startup; the next commit's
		// pull-before-push (or a later startup) will retry.
		slog.Info("Remote configured, performing catch-up push on init", "url", remoteURL)
		// ponytail: Catch-up push fires unconditionally whenever the local repo is
		// non-empty, with no ahead/behind resolution against the remote. Failure
		// modes: (1) a remote head ahead of ours makes go-git reject with a
		// non-fast-forward error — the push logs a warning and startup proceeds, so
		// the next commit's pull-before-push clears it; (2) heads are equal, the
		// push is a go-git no-op, so it is harmless in the absence of
		// server-side force-push policy; (3) a peer push in the gap between IsEmpty() and
		// the push in an HA/multi-replica deployment — that push fails
		// non-fast-forward and the warning path retries on the next startup or
		// commit. Cost: an extra network round-trip to the remote every boot, and
		// every start revalidates auth against a possibly-read-only or congested
		// remote. Upgrade path: compute ahead/behind (git ls-remote vs local ref)
		// and push only when actually ahead of the remote.
		if err := gs.WithGitLock(func() error {
			return gs.PushRemote(context.Background())
		}); err != nil {
			slog.Warn("Catch-up push failed on init (non-blocking)", "error", err)
			if auditPub != nil {
				auditPub.Submit(&flowv1.PublishRequest{
					Channel: "telemetry",
					Event: &flowv1.FlowEvent{
						EventId:    fmt.Sprintf("cartographer-catchup-%d", time.Now().UnixNano()),
						EventType:  "cartographer.push_failed",
						NodeId:     "cartographer",
						Attributes: map[string]string{"error": err.Error(), "url": remoteURL},
					},
				})
			}
		} else {
			slog.Info("Catch-up push succeeded")
		}
	}
	return nil
}

func buildResolveAuthFn(
	remoteAuthSecretRef string,
	readSecretFn func(ctx context.Context, name string) (map[string]string, error),
	remoteURL string,
) func() (transport.AuthMethod, error) {
	return func() (transport.AuthMethod, error) {
		if remoteAuthSecretRef == "" || readSecretFn == nil {
			return nil, nil
		}
		data, err := readSecretFn(context.Background(), remoteAuthSecretRef)
		if err != nil {
			return nil, err
		}
		parsedURL, parseErr := url.Parse(remoteURL)
		if parseErr != nil {
			return nil, parseErr
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
				if kh, hasKnownHosts := data["known_hosts"]; hasKnownHosts && kh != "" {
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
		grpcServer.Stop()
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
	// re-hydration would interpret as the live graph. Surface any failure so an
	// operator diagnosing a wrong-graph-on-restart can correlate it here.
	_ = gs.WithGitLock(func() error {
		if err := gs.RestoreMain(context.Background()); err != nil {
			slog.Error("Shutdown: failed to restore git working tree to main", "error", err)
		}
		if err := gs.CleanUntracked(context.Background()); err != nil {
			slog.Error("Shutdown: failed to clean residual untracked git files", "error", err)
		}
		return nil
	})
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

func newReadSecretFn(clientset *kubernetes.Clientset, namespace string) func(
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
