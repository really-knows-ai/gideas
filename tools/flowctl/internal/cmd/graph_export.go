package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/tools/flowctl/internal/api"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	operatorLabelSelector = "app.kubernetes.io/name=flow-operator"
	defaultGraphName      = "flow-graph"
	operatorProxyPort     = 50053
)

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

	// Step 2: Create K8sClient and check connectivity.
	k8s, err := api.NewK8sClient("")
	if err != nil {
		return fmt.Errorf("failed to connect to Kubernetes: %w\n"+
			"Verify KUBECONFIG or ~/.kube/config points to a Foundry Flow cluster.", err)
	}

	checkCtx, checkCancel := context.WithTimeout(ctx, 5*time.Second)
	defer checkCancel()
	if err := api.CheckConnectivity(checkCtx, k8s); err != nil {
		return err
	}

	// Step 3: Look up FoundryGraph CR as a validation gate.
	fgGVR := schema.GroupVersionResource{
		Group:    "flow.foundry.io",
		Version:  "v1",
		Resource: "foundrygraphs",
	}
	fgClient := k8s.DynamicClient().Resource(fgGVR).Namespace(namespace)
	fgObj, err := fgClient.Get(ctx, graphName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("FAILED_PRECONDITION: no Cartographer endpoint available in namespace %q "+
				"(FoundryGraph %q not found)", namespace, graphName)
		}
		return fmt.Errorf("failed to look up FoundryGraph %q: %w", graphName, err)
	}
	_ = fgObj // We just check it exists — the endpoint is discovered via operator proxy.

	// Step 4: Resolve operator namespace and port-forward.
	operatorNS, err := k8s.ResolveNamespace(ctx, operatorLabelSelector)
	if err != nil {
		// ponytail: cluster-wide list pods RBAC is required. The List call
		// returns an empty result when RBAC filters it (not a 403).
		return fmt.Errorf("cannot resolve operator namespace: %w\n"+
			"Ensure the operator is running and your kubeconfig identity has "+
			"cluster-wide 'list' permission on pods.", err)
	}

	pfm := api.NewPortForwardManager(k8s.GetRESTConfig(), k8s.CoreClient, nil)
	podName, found, err := pfm.FindReadyPod(ctx, operatorNS, operatorLabelSelector)
	if err != nil {
		return fmt.Errorf("finding Ready operator pod: %w", err)
	}
	if !found {
		return fmt.Errorf("no Ready operator pod found in namespace %q", operatorNS)
	}

	forwardID, localPort, err := pfm.ForwardPod(ctx, operatorNS, podName, operatorProxyPort)
	if err != nil {
		return fmt.Errorf("port-forward to operator pod failed: %w\n"+
			"Ensure your kubeconfig identity has 'create' permission on "+
			"pods/portforward.", err)
	}
	defer pfm.Close(forwardID)

	// Step 5: Dial localhost with bearer token and metadata.
	token := k8s.RESTConfig.BearerToken
	if token == "" {
		return fmt.Errorf("cannot resolve bearer token from kubeconfig — " +
			"use a token-based kubeconfig or ensure the exec plugin populates BearerToken")
	}

	addr := fmt.Sprintf("localhost:%d", localPort)
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("dial operator proxy at %s: %w", addr, err)
	}
	defer conn.Close()

	client := flowv1.NewCartographerServiceClient(conn)

	// Attach bearer token and routing metadata.
	callCtx := metadata.AppendToOutgoingContext(ctx,
		"authorization", "Bearer "+token,
		"x-flow-namespace", namespace,
		"x-flow-graph-name", graphName,
	)

	// Step 6: Call ExportGraph server-streaming RPC.
	stream, err := client.ExportGraph(callCtx, &flowv1.ExportGraphRequest{Format: format})
	if err != nil {
		return fmt.Errorf("export graph failed: %w", err)
	}

	// Determine output destination.
	var w io.Writer
	var outputFile *os.File
	if outputPath != "" && outputPath != "-" {
		f, err := os.Create(outputPath)
		if err != nil {
			return fmt.Errorf("create output file %q: %w", outputPath, err)
		}
		defer func() {
			if outputFile != nil {
				outputFile.Close()
			}
		}()
		outputFile = f
		w = bufio.NewWriter(f)
	} else {
		w = os.Stdout
	}

	// Read chunks from the stream and write to output.
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Stream break during gRPC Recv — transport failure.
			if outputFile != nil {
				outputFile.Close()
				os.Remove(outputPath)
			}
			// ponytail: partial output may have been written. For stdout no
			// cleanup is needed (shell captures partial output). For file output,
			// we remove the partial file.
			return fmt.Errorf("stream error: %w\n"+
				"Possible causes: API server restart, operator pod restart, or "+
				"network partition.", err)
		}

		data := chunk.GetChunk()
		if len(data) > 0 {
			if _, err := w.Write(data); err != nil {
				// Stream break mid-write — partial data may have been written.
				if outputFile != nil {
					outputFile.Close()
					os.Remove(outputPath)
				}
				return fmt.Errorf("write error: %w", err)
			}
		}
	}

	if f, ok := w.(*bufio.Writer); ok {
		f.Flush()
	}
	if outputFile != nil {
		outputFile.Close()
	}

	return nil
}

// Ensure unstructured is used (implicit reference).
var _ = &unstructured.Unstructured{}
