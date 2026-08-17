/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"net"
	"os"
	"strconv"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	flowv1gen "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/pkg/eventbus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"

	flowv1 "github.com/foundry/flow/operator/api/v1"
	"github.com/foundry/flow/operator/internal/controller"
	"github.com/foundry/flow/operator/internal/controller/scheduler"
	"github.com/foundry/flow/operator/internal/rpc"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(flowv1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var probeAddr string
	var grpcAddr string
	var eventBusAddr string
	var librarianAddr string
	var archivistAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&grpcAddr, "grpc-bind-address", ":50052", "The address the Operator gRPC server binds to.")
	flag.StringVar(&eventBusAddr, "event-bus-address", "",
		"The address of the Event Bus gRPC server for audit publishing (empty = disabled).")
	var (
		proxyAddr                 string
		readinessTimeoutStr       string
		cartographerPortStr       string
		cartographerImage         string
		capabilityStalenessWindow string
	)
	flag.StringVar(&proxyAddr, "proxy-bind-address", "",
		"The address the Cartographer gRPC proxy server binds to (default :50053).")
	flag.StringVar(&readinessTimeoutStr, "readiness-timeout", "",
		"Maximum time to wait for Cartographer pod readiness (default 5m).")
	flag.StringVar(&cartographerPortStr, "cartographer-port", "",
		"The gRPC port the Cartographer listens on (default 50051).")
	flag.StringVar(&cartographerImage, "cartographer-image", "",
		"The Cartographer container image to deploy (default flow-operator:latest).")
	flag.StringVar(&capabilityStalenessWindow, "capability-staleness-window", "",
		"Staleness window for capability attestations (Go duration, e.g. \"30s\"; "+
			"negative duration like \"-1s\" to disable; default 30s).")
	flag.StringVar(&librarianAddr, "librarian-address", "",
		"The address of the Librarian gRPC server for LawGroup sync (empty = disabled).")
	flag.StringVar(&archivistAddr, "archivist-address", "",
		"The address of the Archivist gRPC server for artefact state queries (empty = disabled).")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	// Cartographer-related config (SPEC R6): each value resolves from its CLI
	// flag, else its env-var override, else its SPEC default.
	proxyAddr = resolveProxyAddress(proxyAddr)
	readinessTimeoutStr = resolveReadinessTimeout(readinessTimeoutStr)
	cartographerPortStr = resolveCartographerPort(cartographerPortStr)
	cartographerImage = resolveCartographerImage(cartographerImage)
	capabilityStalenessWindow = resolveCapabilityStalenessWindow(capabilityStalenessWindow)

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("Disabling HTTP/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts
	webhookServerOptions := webhook.Options{
		TLSOpts: webhookTLSOpts,
	}

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}

	webhookServer := webhook.NewServer(webhookServerOptions)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "4fbbb497.foundry.io",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	// Connect to the Event Bus for audit and lifecycle event publishing.
	// Must be initialized before controllers so they can receive the publisher.
	var auditor *eventbus.AsyncPublisher
	ebAddr := eventBusAddr
	if ebAddr == "" {
		ebAddr = os.Getenv("EVENT_BUS_ADDRESS")
	}
	if ebAddr != "" {
		ebConn, ebErr := grpc.NewClient(
			ebAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if ebErr != nil {
			setupLog.Error(ebErr, "Failed to connect to Event Bus", "address", ebAddr)
			os.Exit(1)
		}
		ebClient := flowv1gen.NewFlowEventBusServiceClient(ebConn)
		auditor = eventbus.NewAsyncPublisher(ebClient)
		setupLog.Info("Event Bus connected for audit publishing", "address", ebAddr)
	} else {
		setupLog.Info("Event Bus not configured, audit publishing disabled")
	}

	// Connect to the Librarian for CRD-backed Law and LawGroup sync.
	var librarianClient flowv1gen.LibrarianServiceClient
	if librarianAddr != "" {
		libConn, libErr := grpc.NewClient(
			librarianAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if libErr != nil {
			setupLog.Error(libErr, "Failed to connect to Librarian", "address", librarianAddr)
			os.Exit(1)
		}
		librarianClient = flowv1gen.NewLibrarianServiceClient(libConn)
		setupLog.Info("Connected to Librarian for Law sync", "address", librarianAddr)
	} else {
		setupLog.Info("Librarian not configured, Law sync disabled")
	}

	if err := (&controller.FoundryFlowReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "FoundryFlow")
		os.Exit(1)
	}
	// Parse Cartographer port from --cartographer-port flag (default 50051).
	// Parsed early because both the FoundryNode reconciler (CARTOGRAPHER_ADDRESS
	// injection, SPEC R5) and the FoundryGraph reconciler consume it.
	cartographerPort64, err := strconv.ParseInt(cartographerPortStr, 10, 32)
	if err != nil {
		setupLog.Error(err, "invalid --cartographer-port", "value", cartographerPortStr)
		os.Exit(1)
	}
	cartographerPort := int32(cartographerPort64)

	if err := (&controller.FoundryNodeReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		CartographerPort: cartographerPort,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "FoundryNode")
		os.Exit(1)
	}
	// Connect to the Archivist for artefact state queries (required for exit contract validation).
	var artefactQuerier func(
		ctx context.Context, workitemID string, governedArtefacts []string,
	) ([]scheduler.ArtefactState, error)
	if archivistAddr != "" {
		archConn, archErr := grpc.NewClient(
			archivistAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if archErr != nil {
			setupLog.Error(archErr, "Failed to connect to Archivist", "address", archivistAddr)
			os.Exit(1)
		}
		archClient := flowv1gen.NewArchivistServiceClient(archConn)
		artefactQuerier = func(
			ctx context.Context, workitemID string, governedArtefacts []string,
		) ([]scheduler.ArtefactState, error) {
			resp, err := archClient.QueryArtefactState(ctx, &flowv1gen.QueryArtefactStateRequest{
				WorkitemId:        workitemID,
				GovernedArtefacts: governedArtefacts,
			})
			if err != nil {
				return nil, err
			}
			states := resp.GetArtefactStates()
			result := make([]scheduler.ArtefactState, len(states))
			for i, s := range states {
				result[i] = scheduler.ArtefactState{
					ArtefactID:       s.GetArtefactId(),
					GovernedArtefact: s.GetGovernedArtefact(),
					StampNames:       s.GetStampNames(),
				}
			}
			return result, nil
		}
		setupLog.Info("Connected to Archivist for artefact state queries", "address", archivistAddr)
	} else {
		setupLog.Info("Archivist not configured, exit contract validation disabled")
	}

	if err := (&controller.WorkitemReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		Auditor:         auditor,
		Librarian:       librarianClient,
		ArtefactQuerier: artefactQuerier,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "Workitem")
		os.Exit(1)
	}
	if err := (&controller.GovernedArtefactReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "GovernedArtefact")
		os.Exit(1)
	}
	if err := (&controller.LawReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Librarian: librarianClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "Law")
		os.Exit(1)
	}
	if err := (&controller.TreatyReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "Treaty")
		os.Exit(1)
	}
	if err := (&controller.FlowSupportServiceReconciler{
		ServiceReconciler: controller.ServiceReconciler{
			Client:        mgr.GetClient(),
			Scheme:        mgr.GetScheme(),
			ContainerName: "support-service",
			AppLabelName:  "flowsupportservice",
			LabelKey:      "flow.foundry.io/support",
			TypeName:      "FlowSupportService",
		},
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "FlowSupportService")
		os.Exit(1)
	}
	if err := (&controller.CodificationServiceReconciler{
		ServiceReconciler: controller.ServiceReconciler{
			Client:        mgr.GetClient(),
			Scheme:        mgr.GetScheme(),
			ContainerName: "codification-service",
			AppLabelName:  "codificationservice",
			LabelKey:      "flow.foundry.io/codification",
			TypeName:      "CodificationService",
		},
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "CodificationService")
		os.Exit(1)
	}
	if err := (&controller.LawGroupReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Librarian: librarianClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "LawGroup")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	// -----------------------------------------------------------------------
	// Cartographer Initialization
	// -----------------------------------------------------------------------
	operatorNamespace := os.Getenv("POD_NAMESPACE")
	if operatorNamespace == "" {
		operatorNamespace = "operator-system"
	}

	// Generate the operator's Ed25519 signing key pair on startup, if not already present.
	operatorKey, err := controller.InitializeOperatorSigningKey(context.Background(), mgr.GetClient(), operatorNamespace)
	if err != nil {
		setupLog.Error(err, "unable to initialize operator signing key")
		os.Exit(1)
	}

	// Generate (or re-read) the sidecar signing key pair on startup.
	if err := controller.InitializeSidecarSigningKey(
		context.Background(), mgr.GetClient(), operatorNamespace,
	); err != nil {
		setupLog.Error(err, "unable to initialize sidecar signing key")
		os.Exit(1)
	}

	// Read readiness timeout from --readiness-timeout flag (default 5m).
	readinessTimeout, err := time.ParseDuration(readinessTimeoutStr)
	if err != nil {
		setupLog.Error(err, "invalid --readiness-timeout", "value", readinessTimeoutStr)
		os.Exit(1)
	}

	// Parse proxy port from --proxy-bind-address (default ":50053"). The port is
	// validated but the numeric value is not retained: the proxy listener binds
	// the full --proxy-bind-address (proxyAddr) below.
	_, proxyPortStr, err := net.SplitHostPort(proxyAddr)
	if err != nil {
		setupLog.Error(err, "invalid --proxy-bind-address", "address", proxyAddr)
		os.Exit(1)
	}
	if _, err := strconv.Atoi(proxyPortStr); err != nil {
		setupLog.Error(err, "invalid port in --proxy-bind-address", "address", proxyAddr, "port", proxyPortStr)
		os.Exit(1)
	}

	// Create the shared proxy routing table.
	proxyRoutingTable := controller.NewProxyRoutingTable()

	// Validate --capability-staleness-window as a Go duration string.
	if _, err := time.ParseDuration(capabilityStalenessWindow); err != nil {
		setupLog.Error(err, "invalid --capability-staleness-window", "value", capabilityStalenessWindow)
		os.Exit(1)
	}

	// Create and register the FoundryGraph reconciler with all fields.
	if err := (&controller.FoundryGraphReconciler{
		Client:                    mgr.GetClient(),
		Scheme:                    mgr.GetScheme(),
		OperatorNamespace:         operatorNamespace,
		CartographerPort:          cartographerPort,
		ReadinessTimeout:          readinessTimeout,
		CartographerImage:         cartographerImage,
		EventBusAddress:           ebAddr,
		CapabilityStalenessWindow: capabilityStalenessWindow,
		ProxyRoutingTable:         proxyRoutingTable,
		CartographerDialer:        controller.DialCartographer,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "FoundryGraph")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")

	// -----------------------------------------------------------------------
	// gRPC Server — The Brain Stem
	// -----------------------------------------------------------------------
	// Spin up the Operator's gRPC server so the Sidecar can forward
	// SubmitResult (and future RPCs) to the control plane. The server
	// runs in a goroutine and shuts down gracefully when the manager's
	// context is cancelled.
	grpcLis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		setupLog.Error(err, "Failed to listen for gRPC", "address", grpcAddr)
		os.Exit(1)
	}

	grpcServer := grpc.NewServer()
	operatorSrv := rpc.NewOperatorServer(mgr.GetClient())
	operatorSrv.Auditor = auditor

	flowv1gen.RegisterOperatorServiceServer(grpcServer, operatorSrv)
	reflection.Register(grpcServer)

	// Run the gRPC server in a goroutine. It will be stopped when the
	// manager context signals shutdown.
	ctx := ctrl.SetupSignalHandler()
	go func() {
		setupLog.Info("Operator gRPC server listening", "address", grpcLis.Addr().String())
		if err := grpcServer.Serve(grpcLis); err != nil {
			setupLog.Error(err, "Operator gRPC server error")
		}
	}()
	go func() {
		<-ctx.Done()
		setupLog.Info("Shutting down Operator gRPC server")
		grpcServer.GracefulStop()
		if operatorSrv.Auditor != nil {
			operatorSrv.Auditor.Stop()
		}
	}()

	// -----------------------------------------------------------------------
	// Cartographer gRPC Proxy Server
	// -----------------------------------------------------------------------
	proxyLis, err := net.Listen("tcp", proxyAddr)
	if err != nil {
		setupLog.Error(err, "unable to listen for proxy", "address", proxyAddr)
		os.Exit(1)
	}
	proxySrv := grpc.NewServer()
	proxyServer := controller.NewProxyServer(proxyRoutingTable, mgr.GetClient(), controller.DialCartographer, operatorKey)
	flowv1gen.RegisterCartographerServiceServer(proxySrv, proxyServer)

	go func() {
		setupLog.Info("Cartographer proxy server listening", "address", proxyLis.Addr().String())
		if err := proxySrv.Serve(proxyLis); err != nil {
			setupLog.Error(err, "Cartographer proxy server error")
		}
	}()
	go func() {
		<-ctx.Done()
		setupLog.Info("Shutting down Cartographer proxy server")
		proxySrv.GracefulStop()
	}()

	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Cartographer config resolution (SPEC R6 env-var defaults)
// ---------------------------------------------------------------------------

// envDefault resolves a config value from a CLI flag, an environment-variable
// override, or a compiled-in default, in that order of precedence. Empty
// values are treated as unset.
func envDefault(flagValue, envValue, defaultValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if envValue != "" {
		return envValue
	}
	return defaultValue
}

// resolveProxyAddress resolves the Cartographer gRPC proxy bind address (SPEC
// R6): the --proxy-bind-address flag, else the OPERATOR_PROXY_PORT env var (a
// bare port, prefixed with ":" to form a bind address), else the SPEC default
// ":50053".
func resolveProxyAddress(flagAddr string) string {
	if flagAddr != "" {
		return flagAddr
	}
	if v := os.Getenv("OPERATOR_PROXY_PORT"); v != "" {
		return ":" + v
	}
	return ":50053"
}

// resolveReadinessTimeout resolves the Cartographer pod readiness timeout:
// the --readiness-timeout flag, else the CARTOGRAPHER_READINESS_TIMEOUT env
// var, else the SPEC default "5m".
func resolveReadinessTimeout(flagValue string) string {
	return envDefault(flagValue, os.Getenv("CARTOGRAPHER_READINESS_TIMEOUT"), "5m")
}

// resolveCartographerPort resolves the Cartographer gRPC port: the
// --cartographer-port flag, else the CARTOGRAPHER_PORT env var, else the SPEC
// default "50051".
func resolveCartographerPort(flagValue string) string {
	return envDefault(flagValue, os.Getenv("CARTOGRAPHER_PORT"), "50051")
}

// resolveCartographerImage resolves the Cartographer container image: the
// --cartographer-image flag, else the CARTOGRAPHER_IMAGE env var, else the
// compiled-in default (SPEC R6: the same image as the Operator release).
func resolveCartographerImage(flagValue string) string {
	return envDefault(flagValue, os.Getenv("CARTOGRAPHER_IMAGE"), controller.DefaultCartographerImage)
}

// resolveCapabilityStalenessWindow resolves the capability attestation
// staleness window: the --capability-staleness-window flag, else the
// CAPABILITY_STALENESS_WINDOW env var, else the SPEC default "30s" (shared
// controller.DefaultCapabilityStalenessWindow, the operator-side source of truth).
func resolveCapabilityStalenessWindow(flagValue string) string {
	return envDefault(flagValue, os.Getenv("CAPABILITY_STALENESS_WINDOW"), controller.DefaultCapabilityStalenessWindow)
}
