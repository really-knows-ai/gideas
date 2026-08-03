// Command cartographer is the Active Knowledge Graph service for Foundry Flow.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
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
	remotePullOnInit := os.Getenv("REMOTE_PULL_ON_INIT") == "true"
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
	// 4. Handle main.lbug corruption recovery
	// -----------------------------------------------------------------------
	if dbErr != nil {
		slog.Warn("Failed to open main.lbug, attempting recovery",
			"path", filepath.Join(ladybugDBPath, "main.lbug"),
			"error", dbErr,
		)

		empty, isEmptyErr := gs.IsEmpty(context.Background())
		if isEmptyErr != nil {
			slog.Error("Failed to check git repo state during recovery", "error", isEmptyErr)
			os.Exit(1)
		}

		if rmErr := os.Remove(filepath.Join(ladybugDBPath, "main.lbug")); rmErr != nil {
			slog.Error("Failed to remove corrupt main.lbug, cannot recover", "error", rmErr)
			os.Exit(1)
		}

		if empty {
			dbStore, dbErr = ladybug.Open(ladybugDBPath)
			if dbErr != nil {
				slog.Error("Failed to create fresh database after recovery", "error", dbErr)
				os.Exit(1)
			}
			slog.Info("Recovery: created fresh database (empty git repo)")
		} else {
			dbStore, dbErr = ladybug.Open(ladybugDBPath)
			if dbErr != nil {
				slog.Error("Failed to open database for re-hydration", "error", dbErr)
				os.Exit(1)
			}
			// ponytail: These paths mirror the gitstore's internal working tree structure:
			// gitstore.New(basePath) creates the repo at <basePath>/graph-repo/ and the
			// working tree uses relative directories "entities/" and "edges/" (see
			// platform/cartographer/internal/gitstore/gitstore.go:114,148-153).  If the
			// gitstore changes its path convention (e.g. a configurable subdirectory or
			// flat file layout), these paths must be updated in tandem.
			entitiesDir := filepath.Join(ladybugDBPath, "graph-repo/entities")
			edgesDir := filepath.Join(ladybugDBPath, "graph-repo/edges")
			if err := dbStore.RehydrateMainFromFiles(context.Background(), entitiesDir, edgesDir); err != nil {
				slog.Error("Failed to re-hydrate main from git", "error", err)
				os.Exit(1)
			}
			slog.Info("Recovery: re-hydrated main from git working tree")
		}
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
		if err := tryRemotePullOnInit(gs, remoteURL, remoteAuthSecretRef, readSecretFn, auditPub); err != nil {
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

	go waitForShutdown(sigCh, healthSrv, grpcServer, server, dbStore, gs, auditPub, eventBusCloser)

	// -----------------------------------------------------------------------
	// 15. Serve
	// -----------------------------------------------------------------------
	slog.Info("Cartographer ready")
	if err := grpcServer.Serve(lis); err != nil {
		slog.Error("gRPC serve error", "error", err)
		os.Exit(1)
	}
}

func tryRemotePullOnInit(
	gs gitstore.GitStore,
	remoteURL string,
	remoteAuthSecretRef string,
	readSecretFn func(ctx context.Context, name string) (map[string]string, error),
	auditPub *eventbus.AsyncPublisher,
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
			if _, ok := data["ssh-privatekey"]; !ok {
				return gitstore.ErrAuthConfigMissing
			}
		case "https":
			if _, ok := data["password"]; !ok {
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
	if err == nil && empty {
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
				signer.HostKeyCallback = gossh.InsecureIgnoreHostKey()
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
	_ = dbStore.Close()

	_ = gs.WithGitLock(func() error {
		_ = gs.RestoreMain(context.Background())
		_ = gs.CleanUntracked(context.Background())
		return nil
	})
	_ = gs.Close()

	if auditPub != nil {
		auditPub.Stop()
	}
	if eventBusCloser != nil {
		_ = eventBusCloser()
	}

	slog.Info("Cartographer shut down")
	os.Exit(0)
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func loadVerificationKey(envVar string) ed25519.PublicKey {
	keyB64 := os.Getenv(envVar)
	if keyB64 == "" {
		slog.Error("Missing required environment variable", "var", envVar)
		os.Exit(1)
	}
	keyBytes, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		slog.Error("Failed to decode verification key", "var", envVar, "error", err)
		os.Exit(1)
	}
	if len(keyBytes) != ed25519.PublicKeySize {
		slog.Error("Invalid verification key length", "var", envVar, "expected", ed25519.PublicKeySize, "got", len(keyBytes))
		os.Exit(1)
	}
	return ed25519.PublicKey(keyBytes)
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
