// Flow Queue Service is the central registration/lease/query frontend for the
// HITL queue mesh. It is non-storage: it maintains Queue custom-resource
// instances (the durable, etcd-backed registry of queues and their shards),
// serves the QueueRegistryService + single-item gRPC surfaces, and the REST
// browse surface. Queue items live on the shards, not here.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowv1api "github.com/foundry/flow/operator/api/v1"
	"github.com/foundry/flow/queue/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultGRPCPort = "50057"
	defaultRESTPort = "8081"
)

func main() {
	var grpcPort, restPort, ttlFlag, sweepIntervalFlag string
	flag.StringVar(&grpcPort, "grpc-port", "", "gRPC port (default 50057; env QUEUE_SERVICE_PORT)")
	flag.StringVar(&restPort, "rest-port", "", "REST port (default 8081; env QUEUE_REST_PORT)")
	flag.StringVar(&ttlFlag, "queue-lease-ttl", "",
		"Queue shard lease TTL (Go duration, e.g. \"45s\"; env QUEUE_LEASE_TTL; "+
			"default "+service.DefaultQueueLeaseTTL.String()+")")
	flag.StringVar(&sweepIntervalFlag, "convergence-sweep-interval", "",
		"Convergence backstop sweep cadence (Go duration, e.g. \"60s\"; env "+
			"QUEUE_SWEEP_INTERVAL; default "+service.DefaultSweepInterval.String()+")")
	flag.Parse()

	grpcPort = envDefault(grpcPort, os.Getenv("QUEUE_SERVICE_PORT"), defaultGRPCPort)
	restPort = envDefault(restPort, os.Getenv("QUEUE_REST_PORT"), defaultRESTPort)
	leaseTTL := resolveQueueLeaseTTL(ttlFlag)
	sweepInterval := resolveQueueSweepInterval(sweepIntervalFlag)

	slog.Info("Flow Queue Service starting", "grpc_port", grpcPort, "rest_port", restPort, "lease_ttl", leaseTTL)

	// Build the controller-runtime client (mirrors the operator bootstrap minus
	// the manager).
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(flowv1api.AddToScheme(scheme))

	cfg, err := rest.InClusterConfig()
	if err != nil {
		// Dev fallback: read a local kubeconfig (KUBECONFIG env or default).
		cfg, err = clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
		if err != nil {
			slog.Error("Failed to build Kubernetes client config (in-cluster and KUBECONFIG)", "error", err)
			os.Exit(1)
		}
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		slog.Error("Failed to create Kubernetes client", "error", err)
		os.Exit(1)
	}

	// Resolve the registry namespace (FLOW_NAMESPACE, default "default") and
	// assign it post-construction — the NewRegistry signature is unchanged.
	namespace := envDefault("", os.Getenv("FLOW_NAMESPACE"), "default")
	reg := service.NewRegistry(c, leaseTTL, service.DefaultLeaseSweepInterval)
	reg.Namespace = namespace

	// gRPC server: QueueRegistryService (registration/lease/single-item) +
	// reflection.
	lis, err := net.Listen("tcp", ":"+grpcPort)
	if err != nil {
		slog.Error("Failed to listen gRPC", "port", grpcPort, "error", err)
		os.Exit(1)
	}
	gs := grpc.NewServer()
	flowv1.RegisterQueueRegistryServiceServer(gs, reg)
	flowv1.RegisterQueueGatewayServiceServer(gs, service.NewGatewayServer(reg))
	reflection.Register(gs)

	// REST frontend.
	restSrv := service.NewRestServer(reg)
	httpSrv := &http.Server{Addr: ":" + restPort, Handler: restSrv.Handler()}

	// Start the lease-eviction sweep.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg.StartSweep(ctx)
	// Start the convergence backstop sweep alongside it.
	service.NewSweeper(reg, sweepInterval).Run(ctx)

	go func() {
		slog.Info("Flow Queue Service REST listening", "address", ":"+restPort)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Flow Queue Service REST error", "error", err)
		}
	}()

	// Graceful shutdown on SIGTERM/SIGINT.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		<-sigCh
		slog.Info("Received signal, shutting down gracefully")
		cancel()
		shutCtx, shutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdown()
		_ = httpSrv.Shutdown(shutCtx)
		gs.GracefulStop()
	}()

	slog.Info("Flow Queue Service gRPC listening", "address", lis.Addr().String())
	if err := gs.Serve(lis); err != nil {
		slog.Error("Flow Queue Service gRPC error", "error", err)
		os.Exit(1)
	}
	slog.Info("Flow Queue Service stopped")
}

// envDefault resolves a config value from a CLI flag, an environment-variable
// override, or a compiled-in default, in that order of precedence. Empty values
// are treated as unset.
func envDefault(flagValue, envValue, defaultValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if envValue != "" {
		return envValue
	}
	return defaultValue
}

// resolveQueueLeaseTTL resolves the queue shard lease TTL: the --queue-lease-ttl
// flag, else the QUEUE_LEASE_TTL env var, else DefaultQueueLeaseTTL. The
// boundary parses the resolved string to a time.Duration (mirroring the
// operator's resolveCapabilityStalenessWindow, which returns a string parsed by
// the consumer). A malformed value logs a warning and falls back to the default
// — a bad TTL must never crash the service.
func resolveQueueLeaseTTL(flagValue string) time.Duration {
	raw := envDefault(flagValue, os.Getenv("QUEUE_LEASE_TTL"), service.DefaultQueueLeaseTTL.String())
	d, err := time.ParseDuration(raw)
	if err != nil {
		slog.Warn("Invalid QUEUE_LEASE_TTL, falling back to default",
			"value", raw, "error", err, "default", service.DefaultQueueLeaseTTL)
		return service.DefaultQueueLeaseTTL
	}
	return d
}

// resolveQueueSweepInterval resolves the convergence backstop sweep cadence:
// the --convergence-sweep-interval flag, else the QUEUE_SWEEP_INTERVAL env var,
// else DefaultSweepInterval. A malformed value logs a warning and falls back to
// the default.
func resolveQueueSweepInterval(flagValue string) time.Duration {
	raw := envDefault(flagValue, os.Getenv("QUEUE_SWEEP_INTERVAL"), service.DefaultSweepInterval.String())
	d, err := time.ParseDuration(raw)
	if err != nil {
		slog.Warn("Invalid QUEUE_SWEEP_INTERVAL, falling back to default",
			"value", raw, "error", err, "default", service.DefaultSweepInterval)
		return service.DefaultSweepInterval
	}
	return d
}
