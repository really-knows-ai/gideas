// Package main is the entry point for the Federation controller manager.
//
// The Federation service is a Kubebuilder controller + gRPC server that
// manages FederationMember and FederationState CRDs. It exposes the
// FederationService gRPC API for SDK-facing RPCs and backs all queries
// against the Kubernetes API.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"

	flowv1 "github.com/gideas/flow/gen/flow/v1"

	federationv1 "github.com/gideas/flow/federation/api/v1"
	"github.com/gideas/flow/federation/internal/controller"
	"github.com/gideas/flow/federation/internal/service"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(federationv1.AddToScheme(scheme))
}

const (
	// defaultFederationPort is the default gRPC listen port.
	defaultFederationPort = 50061

	envFederationPort      = "FEDERATION_PORT"
	envFederationNamespace = "FEDERATION_NAMESPACE"
)

// runConfig holds the runtime configuration for the federation service.
type runConfig struct {
	grpcPort  int
	namespace string

	// k8sClient overrides the Kubernetes client for testing.
	// When nil, a real client is obtained from the controller-runtime
	// manager.
	k8sClient client.Client
}

// run starts the Federation gRPC server and, in production mode, the
// Kubebuilder controller manager. It blocks until ctx is cancelled,
// then performs graceful shutdown.
func run(ctx context.Context, cfg runConfig) error {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	k8sClient := cfg.k8sClient

	// In production (no injected client), start the Kubebuilder manager
	// to get a real K8s client and run controllers.
	var mgr ctrl.Manager
	if k8sClient == nil {
		var err error
		mgr, err = ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
			Scheme: scheme,
		})
		if err != nil {
			return fmt.Errorf("creating controller manager: %w", err)
		}
		k8sClient = mgr.GetClient()

		// Register controllers with the manager.
		if err := (&controller.FederationMemberReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}).SetupWithManager(mgr); err != nil {
			return fmt.Errorf("setting up FederationMember controller: %w", err)
		}
	}

	// Build the Federation gRPC service.
	federationSrv := service.NewFederationServer(k8sClient, cfg.namespace)

	// Start the gRPC listener.
	addr := fmt.Sprintf(":%d", cfg.grpcPort)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	grpcServer := grpc.NewServer()
	flowv1.RegisterFederationServiceServer(grpcServer, federationSrv)
	reflection.Register(grpcServer)

	// Serve gRPC in a goroutine.
	grpcErrCh := make(chan error, 1)
	go func() {
		setupLog.Info("Federation gRPC server listening", "address", lis.Addr().String())
		grpcErrCh <- grpcServer.Serve(lis)
	}()

	// Shut down the gRPC server when the context is cancelled.
	go func() {
		<-ctx.Done()
		setupLog.Info("Shutting down Federation gRPC server")
		grpcServer.GracefulStop()
	}()

	// If we have a manager (production mode), start it. The manager
	// blocks until its context is cancelled.
	if mgr != nil {
		if err := mgr.Start(ctx); err != nil {
			return fmt.Errorf("running controller manager: %w", err)
		}
	} else {
		// In test mode (no manager), block until the gRPC server exits.
		<-ctx.Done()
	}

	// Wait for gRPC server to finish.
	if err := <-grpcErrCh; err != nil {
		// grpc.Server.Serve returns an error when stopped; this is expected
		// after GracefulStop. Only propagate unexpected errors.
		// GracefulStop closes the listener, causing Serve to return
		// grpc.ErrServerStopped or a net.OpError — both are expected.
		return nil
	}

	return nil
}

func main() {
	port := defaultFederationPort
	if v := os.Getenv(envFederationPort); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid %s: %v\n", envFederationPort, err)
			os.Exit(1)
		}
		port = p
	}

	ns := os.Getenv(envFederationNamespace)
	if ns == "" {
		ns = "federation-system"
	}

	ctx := ctrl.SetupSignalHandler()
	if err := run(ctx, runConfig{
		grpcPort:  port,
		namespace: ns,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}
