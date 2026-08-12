package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/tools/flowctl/internal/api"
	"github.com/foundry/flow/tools/flowctl/manifestfs"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// defaultGraphName is the conventional singleton FoundryGraph resource
	// name (SPEC R1).
	//
	// ponytail: "flow-graph" is an operational knob duplicated across two
	// surfaces — this CLI constant and the operator proxy's defaultGraphName
	// (platform/operator/internal/controller/foundrygraph_proxyserver.go:82).
	// The two live in separate Go modules, and the operator's constant sits
	// in an internal package, so a shared source of truth would require
	// exporting a new non-internal constant and adding a cross-module
	// dependency. The CLI sends this value as x-flow-graph-name metadata and
	// the proxy falls back to its own constant only when the metadata is
	// absent, so a drift between the two is not caught at the constant level:
	// it surfaces late as a confusing "FoundryGraph <name> not found"
	// FAILED_PRECONDITION from the CLI's CR lookup (or a proxy route miss).
	// Upgrade path: export the operator's constant into a shared non-internal
	// package and reference it from both binaries.
	defaultGraphName = "flow-graph"

	// operatorProxyPort is the default operator gRPC proxy port forwarded to
	// by the CLI.
	//
	// ponytail: the proxy port is an operational knob with multiple drift
	// surfaces. On the operator side (platform/operator/cmd/main.go) the
	// precedence is --proxy-bind-address if set, else the
	// OPERATOR_PROXY_PORT env var, else :50053 — the env var is a fallback
	// consulted only when the flag is empty, never an override. The CLI side
	// reads OPERATOR_PROXY_PORT from the user's local machine, which cannot
	// observe the operator deployment's configured proxy port, then falls
	// back to the same hard-coded 50053. An operator started with a different
	// --proxy-bind-address (e.g. :50054) is silently unreachable and the CLI
	// cannot detect the divergence. Upgrade path: a discovery mechanism that
	// queries the operator for its bound proxy address, or a shared source of
	// truth for the port, instead of the env var + hard-coded fallback.
	operatorProxyPort = 50053

	// supportedExportFormats matches the Cartographer's accepted formats
	// (see platform/cartographer/internal/service/export.go). The format is a
	// plain string on the wire, so the CLI must mirror the server's set to fail
	// fast instead of burning the whole port-forward/gRPC flow.
	formatJSON    = "json"
	formatGraphML = "graphml"
)

// exportStreamTimeout bounds the whole ExportGraph stream lifetime (lazy
// dial + establishment + every Recv), mirroring how the SDK's session.timeout
// bounds its ExportGraph sibling path (sdk/go/graph.go ExportGraph): grpc-go
// pins the context passed to a streaming RPC to the stream for its whole
// lifetime, so a per-operation deadline applied at the ExportGraph call covers
// both the initial dial and the Recv loop, making a stalled port-forward or a
// proxy that accepts the stream but never sends data fail with
// DEADLINE_EXCEEDED instead of hanging flowctl graph export indefinitely.
// It is a var rather than a const so tests can shorten it.
//
// ponytail: the bound is a single total-stream deadline, not an idle timeout
// that resets per chunk, so an export that legitimately streams longer than
// the bound is cut even while chunks keep arriving. This mirrors the SDK
// sibling path's semantics (session.timeout is a total per-call deadline
// pinned to the stream). 5m follows the repo's long-running-operation default
// (the cartographer's git-operation and operator-readiness deadlines are both
// 5m). Upgrade path: a --timeout flag, or an idle-reset bound if very large
// slow exports need an unbounded total duration.
var exportStreamTimeout = 5 * time.Minute

// operatorLabelSelector identifies the deployed operator pod for namespace
// resolution and pod lookup. It is sourced from the embedded operator
// deployment manifest (manifestfs.OperatorPodLabelSelector) rather than a
// hand-written literal, so it cannot silently drift from the deployed
// manager.yaml pod label.
var operatorLabelSelector = manifestfs.OperatorPodLabelSelector

func validateExportFormat(format string) error {
	switch format {
	case formatJSON, formatGraphML:
		return nil
	default:
		return fmt.Errorf("invalid export format %q: supported formats are %q or %q",
			format, formatJSON, formatGraphML)
	}
}

// resolveOperatorProxyPort returns the operator gRPC proxy port to port-forward.
// It mirrors the operator's own override behaviour (OPERATOR_PROXY_PORT, default
// 50053, see SPEC R6): the env var wins, otherwise the hard-coded default.
func resolveOperatorProxyPort() (int, error) {
	if v := os.Getenv("OPERATOR_PROXY_PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("OPERATOR_PROXY_PORT parse error: %w", err)
		}
		return port, nil
	}
	return operatorProxyPort, nil
}

// newGraphExporterFn returns the graph exporter factory. Overridable in tests.
var newGraphExporterFn func() (graphExporter, error) = func() (graphExporter, error) {
	return newGraphExporter()
}

// newExportFileFn creates the --output file. Overridable in tests to inject
// write/flush and close failures into the file-output path.
var newExportFileFn func(path string) (io.WriteCloser, error) = func(path string) (io.WriteCloser, error) {
	return os.Create(path)
}

func newGraphExportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export the knowledge graph in a standard format",
		Long: `Export the full Foundry knowledge graph for external visualisation
and analysis tools. The graph is exported as a byte stream and written to
stdout or a file.`,
		RunE: runGraphExport,
	}

	cmd.Flags().String("format", "json", "Output format: \"json\" or \"graphml\"")
	cmd.Flags().String("namespace", "", "Kubernetes namespace (overrides context/FLOW_NAMESPACE)")
	cmd.Flags().String("graph-name", defaultGraphName, "FoundryGraph resource name")
	cmd.Flags().String("output", "", "Output file path (default: stdout)")

	return cmd
}

func runGraphExport(cmd *cobra.Command, args []string) error {
	format, _ := cmd.Flags().GetString("format")
	nsFlag, _ := cmd.Flags().GetString("namespace")
	graphName, _ := cmd.Flags().GetString("graph-name")
	outputPath, _ := cmd.Flags().GetString("output")

	// Resolve namespace: flag > FLOW_NAMESPACE > kube context > "default"
	namespace := nsFlag
	if namespace == "" {
		namespace = os.Getenv("FLOW_NAMESPACE")
	}
	if namespace == "" {
		namespace = api.GetCurrentContextNamespace()
	}

	ctx := cmd.Context()

	exporter, err := newGraphExporterFn()
	if err != nil {
		return err
	}

	params := graphExportParams{
		namespace: namespace,
		graphName: graphName,
		format:    format,
	}

	// stdout output.
	if outputPath == "" || outputPath == "-" {
		return runGraphExportWith(ctx, exporter, params, os.Stdout)
	}

	// Validate the format before creating the output file, so a format typo
	// never truncates (and then removes) a pre-existing file.
	if err := validateExportFormat(format); err != nil {
		return err
	}

	// Output to a file: any stream or write failure removes the partial file so
	// the caller doesn't mistake truncated data for a full graph.
	//
	// ponytail: the output file is created with os.Create, which truncates any
	// pre-existing file at the path before the export has connected, and
	// removeStreamOutput deletes the file on every failure branch — so a
	// failed export destroys pre-existing content even when it never reached
	// the operator (a connectivity/token failure included). Failure modes:
	// (1) a typo'd --output pointing at an existing file is data loss, not an
	// error; (2) an overwrite whose export fails silently leaves no trace of
	// the previous graph for comparison. Consequence: --output is only safe
	// for paths the caller intends to fully replace. Upgrade path: stream to
	// a temporary file in the same directory and rename it over the target
	// only on success, leaving pre-existing content untouched on any failure.
	f, err := newExportFileFn(outputPath)
	if err != nil {
		return fmt.Errorf("create output file %q: %w", outputPath, err)
	}
	w := bufio.NewWriter(f)
	if err := runGraphExportWith(ctx, exporter, params, w); err != nil {
		removeStreamOutput(f, outputPath)
		return err
	}
	if err := w.Flush(); err != nil {
		removeStreamOutput(f, outputPath)
		return fmt.Errorf("flush output file %q: %w", outputPath, err)
	}
	if err := f.Close(); err != nil {
		removeStreamOutput(f, outputPath)
		return fmt.Errorf("close output file %q: %w", outputPath, err)
	}
	return nil
}

// graphExportParams carries the resolved command inputs shared by the export
// runner and its collaborator seam.
type graphExportParams struct {
	namespace string
	graphName string
	format    string
}

// graphExportStream abstracts the server-streaming ExportGraph receive, so
// tests can drive chunks and terminal errors without a gRPC server.
type graphExportStream interface {
	Recv() (*flowv1.ExportGraphResponse, error)
}

// graphExporter decouples the command's external interactions (Kubernetes
// lookups, operator port-forward, bearer token, gRPC dial) from its control
// flow so every error branch can be exercised with a fake. The production
// implementation is prodGraphExporter (see newGraphExporter).
type graphExporter interface {
	// checkConnectivity probes the Kubernetes API server.
	checkConnectivity(ctx context.Context) error
	// lookupFoundryGraph returns an error if the FoundryGraph CR is absent.
	lookupFoundryGraph(ctx context.Context, namespace, name string) error
	// resolveOperatorNamespace returns the namespace hosting the operator pod.
	resolveOperatorNamespace(ctx context.Context) (string, error)
	// findReadyOperatorPod locates a Ready operator pod in namespace.
	findReadyOperatorPod(ctx context.Context, namespace string) (string, bool, error)
	// forwardOperatorPod opens the port-forward and returns the local port.
	forwardOperatorPod(ctx context.Context, namespace, podName string) (int, func(), error)
	// bearerToken returns the kubeconfig bearer token.
	bearerToken() (string, error)
	// dialOperatorStream connects to the operator proxy and opens the export
	// stream, returning it plus a function to close the underlying connection.
	dialOperatorStream(ctx context.Context, localPort int, token, namespace, name, format string) (graphExportStream, func(), error)
}

// runGraphExportWith drives the graph export control flow, returning a
// descriptive error for each failure stage. Writing goes to out; callers that
// buffer or own a file are responsible for flushing, closing, and cleanup.
func runGraphExportWith(ctx context.Context, exporter graphExporter, params graphExportParams, out io.Writer) error {
	// Reject an unsupported format before any Kubernetes or gRPC work so a
	// format typo fails fast in every call path (the CLI and any programmatic
	// driver), instead of burning the whole port-forward/gRPC flow only to
	// fail with INVALID_ARGUMENT from the Cartographer.
	if err := validateExportFormat(params.format); err != nil {
		return err
	}
	if err := exporter.checkConnectivity(ctx); err != nil {
		return err
	}

	// Look up the FoundryGraph CR as a validation gate.
	if err := exporter.lookupFoundryGraph(ctx, params.namespace, params.graphName); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("FAILED_PRECONDITION: no Cartographer endpoint available in namespace %q "+
				"(FoundryGraph %q not found)", params.namespace, params.graphName)
		}
		return fmt.Errorf("failed to look up FoundryGraph %q: %w", params.graphName, err)
	}

	// Resolve the operator namespace and open the port-forward.
	operatorNS, err := exporter.resolveOperatorNamespace(ctx)
	if err != nil {
		// ponytail: resolving the operator namespace relies on a
		// cluster-wide Pod List, which Kubernetes RBAC-filters instead of
		// rejecting: an identity without pod-list access gets an empty
		// result (not a 403), so ResolveNamespace reports the same "no Ready
		// operator pod found" error as a genuinely absent operator. Failure
		// modes: (1) a namespace-scoped kubeconfig identity silently fails
		// export and is misled into checking operator health; (2) a real
		// empty cluster and an RBAC-filtered list are indistinguishable.
		// Consequence: every flowctl user needs cluster-wide pod-list RBAC
		// even though the operator deploys to the well-known foundry-system
		// namespace (see manifestfs/operator/deployment.yaml), and the
		// misdiagnosis wastes debugging time. Ceiling: the cluster-wide
		// label scan is the only resolution path. Upgrade path: resolve the
		// operator namespace from the embedded manifest's foundry-system
		// deployment (namespaced pod-list RBAC) with the cluster scan as
		// fallback, or query the operator proxy for its own namespace over
		// gRPC.
		return fmt.Errorf("cannot resolve operator namespace: %w\n"+
			"Ensure the operator is running and your kubeconfig identity has "+
			"cluster-wide 'list' permission on pods.", err)
	}

	podName, found, err := exporter.findReadyOperatorPod(ctx, operatorNS)
	if err != nil {
		return fmt.Errorf("finding Ready operator pod: %w", err)
	}
	if !found {
		return fmt.Errorf("no Ready operator pod found in namespace %q", operatorNS)
	}

	localPort, stopForward, err := exporter.forwardOperatorPod(ctx, operatorNS, podName)
	if err != nil {
		return fmt.Errorf("port-forward to operator pod failed: %w\n"+
			"Ensure your kubeconfig identity has 'create' permission on "+
			"pods/portforward.", err)
	}
	defer stopForward()

	token, err := exporter.bearerToken()
	if err != nil {
		return fmt.Errorf("cannot resolve bearer token from kubeconfig — " +
			"export requires a kubeconfig authenticated with a static bearer token")
	}

	stream, stopStream, err := exporter.dialOperatorStream(ctx, localPort, token, params.namespace, params.graphName, params.format)
	if err != nil {
		return fmt.Errorf("export graph failed: %w", err)
	}
	defer stopStream()

	// Read chunks from the stream and write to output.
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%s\n"+
				"Possible causes: API server restart, operator pod restart, or "+
				"network partition.", annotateRPCError("stream error", err))
		}

		data := chunk.GetChunk()
		if len(data) > 0 {
			if _, err := out.Write(data); err != nil {
				return fmt.Errorf("write error: %w", err)
			}
		}
	}
	return nil
}

// prodGraphExporter is the production graphExporter backed by a real K8sClient
// and PortForwardManager, talking to the operator's gRPC proxy.
type prodGraphExporter struct {
	k8s *api.K8sClient
	pfm *api.PortForwardManager
}

// newGraphExporter connects to Kubernetes and wires the production exporter.
func newGraphExporter() (*prodGraphExporter, error) {
	k8s, err := api.NewK8sClient("")
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Kubernetes: %w\n"+
			"Verify KUBECONFIG or ~/.kube/config points to a Foundry Flow cluster.", err)
	}
	pfm := api.NewPortForwardManager(k8s.GetRESTConfig(), k8s.CoreClient, nil)
	return &prodGraphExporter{k8s: k8s, pfm: pfm}, nil
}

func (g *prodGraphExporter) checkConnectivity(ctx context.Context) error {
	checkCtx, checkCancel := context.WithTimeout(ctx, 5*time.Second)
	defer checkCancel()
	return api.CheckConnectivity(checkCtx, g.k8s)
}

func (g *prodGraphExporter) lookupFoundryGraph(ctx context.Context, namespace, name string) error {
	fgGVR := schema.GroupVersionResource{
		Group:    "flow.foundry.io",
		Version:  "v1",
		Resource: "foundrygraphs",
	}
	fgClient := g.k8s.DynamicClient().Resource(fgGVR).Namespace(namespace)
	_, err := fgClient.Get(ctx, name, metav1.GetOptions{})
	return err
}

func (g *prodGraphExporter) resolveOperatorNamespace(ctx context.Context) (string, error) {
	return g.k8s.ResolveNamespace(ctx, operatorLabelSelector)
}

func (g *prodGraphExporter) findReadyOperatorPod(ctx context.Context, namespace string) (string, bool, error) {
	return g.pfm.FindReadyPod(ctx, namespace, operatorLabelSelector)
}

func (g *prodGraphExporter) forwardOperatorPod(ctx context.Context, namespace, podName string) (int, func(), error) {
	proxyPort, err := resolveOperatorProxyPort()
	if err != nil {
		return 0, nil, err
	}
	forwardID, localPort, err := g.pfm.ForwardPod(ctx, namespace, podName, proxyPort)
	if err != nil {
		return 0, nil, err
	}
	return localPort, func() { _ = g.pfm.Close(forwardID) }, nil
}

// bearerToken returns the kubeconfig's bearer token for the operator proxy's
// TokenReview (SPEC Graph Export Flow step 3).
//
// ponytail: only RESTConfig.BearerToken is read, so kubeconfigs whose AuthInfo
// authenticates via an exec credential plugin (GKE, EKS, ...) or a client
// certificate resolve to an empty token and graph export fails — client-go
// never copies exec-plugin or certificate credentials into
// RESTConfig.BearerToken (exec tokens are fetched lazily by the transport's
// credential plugin; certificates live in CertData/KeyData). Failure modes:
// (1) a perfectly valid cluster identity using exec/cert auth is rejected
// with "no bearer token"; (2) the error cannot fall back to a certificate
// because the proxy's TokenReview only accepts a token through the
// authorization metadata. Consequence: flowctl graph export requires a
// kubeconfig carrying a static bearer token, matching the SPEC's "attaches
// its kubeconfig bearer token" flow (SPEC Graph Export Flow step 1). Upgrade
// path: resolve the exec plugin's credential via client-go's ExecProvider
// credential cache and render it into the authorization header before dialing
// the proxy, or have the proxy accept the CLI's TLS client certificate.
func (g *prodGraphExporter) bearerToken() (string, error) {
	if g.k8s.RESTConfig == nil || g.k8s.RESTConfig.BearerToken == "" {
		return "", fmt.Errorf("kubeconfig has no bearer token")
	}
	return g.k8s.RESTConfig.BearerToken, nil
}

func (g *prodGraphExporter) dialOperatorStream(ctx context.Context, localPort int, token, namespace, name, format string) (graphExportStream, func(), error) {
	addr := fmt.Sprintf("localhost:%d", localPort)
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("dial operator proxy at %s: %w", addr, err)
	}

	client := flowv1.NewCartographerServiceClient(conn)

	// Attach bearer token and routing metadata.
	callCtx := metadata.AppendToOutgoingContext(ctx,
		"authorization", "Bearer "+token,
		"x-flow-namespace", namespace,
		"x-flow-graph-name", name,
	)

	// Bound the stream with exportStreamTimeout (see its comment): the
	// deadline context is pinned to the stream by grpc-go for its whole
	// lifetime, so it covers the lazy dial/establishment and every Recv.
	streamCtx, cancel := context.WithTimeout(callCtx, exportStreamTimeout)
	stream, err := client.ExportGraph(streamCtx, &flowv1.ExportGraphRequest{Format: format})
	if err != nil {
		cancel()
		_ = conn.Close()
		return nil, nil, err
	}
	return stream, func() {
		cancel()
		_ = conn.Close()
	}, nil
}

// removeStreamOutput closes and removes the partial output file written before
// a stream failure, so the caller doesn't mistake truncated data for a complete
// graph. It is a no-op for stdout output.
func removeStreamOutput(outputFile io.Closer, outputPath string) {
	if outputFile != nil {
		outputFile.Close()
		os.Remove(outputPath)
	}
}

// annotateRPCError prefixes err with the gRPC status code when err carries
// one, so callers can distinguish the operator's mid-stream INTERNAL failure
// (SPEC R11) from a dial/setup Unavailable. Non-gRPC errors are returned
// unchanged wrapped by prefix.
func annotateRPCError(prefix string, err error) error {
	if st, ok := status.FromError(err); ok && st.Code() != codes.Unknown {
		return fmt.Errorf("%s (%s): %w", prefix, st.Code(), err)
	}
	return fmt.Errorf("%s: %w", prefix, err)
}
