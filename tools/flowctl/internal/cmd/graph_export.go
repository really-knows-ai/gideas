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
	operatorLabelSelector = "app.kubernetes.io/name=flow-operator"
	defaultGraphName      = "flow-graph"
	operatorProxyPort     = 50053
)

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

	exporter, err := newGraphExporter()
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

	// Output to a file: any stream or write failure removes the partial file so
	// the caller doesn't mistake truncated data for a full graph.
	f, err := os.Create(outputPath)
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
		// ponytail: cluster-wide list pods RBAC is required. The List call
		// returns an empty result when RBAC filters it (not a 403).
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
			"use a token-based kubeconfig or ensure the exec plugin populates BearerToken")
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

	stream, err := client.ExportGraph(callCtx, &flowv1.ExportGraphRequest{Format: format})
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return stream, func() { _ = conn.Close() }, nil
}

// removeStreamOutput closes and removes the partial output file written before
// a stream failure, so the caller doesn't mistake truncated data for a complete
// graph. It is a no-op for stdout output.
func removeStreamOutput(outputFile *os.File, outputPath string) {
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