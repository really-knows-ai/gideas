// haiku-watch monitors a Workitem's journey through the Haiku Foundry Cycle.
//
// It uses the Kubernetes Watch API to observe real-time status changes on a
// Workitem CRD, printing a clear timeline of phase transitions and node
// assignments. On each transition it queries the Archivist to display the
// current haiku text and any new feedback. When the workitem completes, it
// prints a full summary.
//
// Usage:
//
//	go run ./tools/haiku-watch --workitem haiku-001 [--namespace default]
//	go run ./tools/haiku-watch --workitem haiku-001 --archivist localhost:50054
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	workitemName  = flag.String("workitem", "haiku-001", "Name of the Workitem to watch")
	namespace     = flag.String("namespace", "default", "Kubernetes namespace")
	archivistAddr = flag.String("archivist", "",
		"Archivist gRPC address (e.g. localhost:50054). If empty, skip final haiku display.")
	kubeconfig = flag.String("kubeconfig", "", "Path to kubeconfig (defaults to in-cluster or ~/.kube/config)")
)

// Phase constants used in status comparisons and display.
const (
	phaseCompleted = "Completed"
	phaseFailed    = "Failed"
	phasePending   = "Pending"
	phaseRunning   = "Running"
	phaseRouting   = "Routing"
)

// nodeLabels maps node names to display labels for the timeline.
var nodeLabels = map[string]string{
	"forge":     "FORGE    (create)",
	"sort":      "SORT     (gate)",
	"quench":    "QUENCH   (validate)",
	"appraisal": "APPRAISAL (review)",
	"refine":    "REFINE   (revise)",
}

// phaseSymbols maps phases to visual indicators.
var phaseSymbols = map[string]string{
	phasePending:   "...",
	phaseRunning:   ">>>",
	phaseRouting:   "-->",
	phaseCompleted: "[+]",
	phaseFailed:    "[!]",
}

// eventState carries mutable state across watch events.
type eventState struct {
	prevPhase       string
	prevAssignee    string
	visitCount      int
	seenFeedbackIDs map[string]bool
	prevHaikuHash   string
}

// buildKubeClient creates a Kubernetes dynamic client from the configured
// kubeconfig path (or default loading rules).
func buildKubeClient() (dynamic.Interface, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if *kubeconfig != "" {
		loadingRules.ExplicitPath = *kubeconfig
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("building kubeconfig: %w", err)
	}
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client: %w", err)
	}
	return client, nil
}

// connectArchivist dials the Archivist gRPC endpoint and returns a client
// with its underlying connection. If addr is empty, both return values are nil.
func connectArchivist(addr string) (flowv1.ArchivistServiceClient, *grpc.ClientConn) {
	if addr == "" {
		return nil, nil
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not connect to archivist at %s: %v\n", addr, err)
		return nil, nil
	}
	return flowv1.NewArchivistServiceClient(conn), conn
}

// processEvent handles a single watch event, printing timeline rows and
// querying the archivist for live state. It returns true when the workitem
// has reached a terminal phase and the caller should stop watching.
func processEvent(
	ctx context.Context,
	event watch.Event,
	state *eventState,
	archClient flowv1.ArchivistServiceClient,
) bool {
	if event.Type == watch.Error {
		fmt.Fprintf(os.Stderr, "Watch error: %v\n", event.Object)
		return false
	}

	obj, ok := event.Object.(*unstructured.Unstructured)
	if !ok {
		return false
	}

	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	if status == nil {
		return false
	}

	phase, _ := status["phase"].(string)
	assignee, _ := status["currentAssignee"].(string)

	// Skip duplicate events.
	if phase == state.prevPhase && assignee == state.prevAssignee {
		return false
	}

	// Compact display: only show meaningful transitions.
	showRow := false
	switch {
	case phase == phaseCompleted || phase == phaseFailed:
		showRow = true
	case assignee != state.prevAssignee && assignee != "":
		showRow = true
	case phase == phaseRunning && assignee != "sort":
		showRow = true
	}

	// Count node visits.
	if assignee != state.prevAssignee && assignee != "" {
		state.visitCount++
	}

	if showRow {
		sym := phaseSymbols[phase]
		if sym == "" {
			sym = "???"
		}

		nodeLabel := nodeLabels[assignee]
		if nodeLabel == "" && assignee != "" {
			nodeLabel = assignee
		}
		if assignee == "" {
			nodeLabel = "(none)"
		}

		now := time.Now().Format("15:04:05")
		fmt.Printf("%-12s %-5s %-20s %s\n", now, sym, nodeLabel, phase)

		// Print thrash counters if present.
		if tc, ok := status["thrashCounters"]; ok {
			if counters, ok := tc.(map[string]any); ok && len(counters) > 0 {
				parts := make([]string, 0, len(counters))
				for node, count := range counters {
					parts = append(parts, fmt.Sprintf("%s:%v", node, count))
				}
				fmt.Printf("%-12s       visits: %s\n", "", strings.Join(parts, ", "))
			}
		}
	}

	state.prevPhase = phase
	state.prevAssignee = assignee

	// Live archivist queries: show haiku text and new feedback on meaningful transitions.
	if archClient != nil && showRow {
		printLiveState(ctx, archClient, *workitemName, &state.prevHaikuHash, state.seenFeedbackIDs)
	}

	// Terminal states.
	if phase == phaseCompleted || phase == phaseFailed {
		fmt.Println(strings.Repeat("-", 72))
		fmt.Printf("\nWorkitem %s after %d node visits.\n", strings.ToLower(phase), state.visitCount)

		if phase == phaseCompleted && *archivistAddr != "" {
			fetchAndPrintHaiku(ctx, *archivistAddr, *workitemName)
		} else if phase == phaseCompleted {
			fmt.Println("\nTip: pass --archivist=<host:port> to display the final haiku.")
		}
		return true
	}
	return false
}

func main() {
	flag.Parse()

	fmt.Println()
	fmt.Println("=== HAIKU FOUNDRY CYCLE ===")
	fmt.Printf("    Watching workitem: %s/%s\n", *namespace, *workitemName)
	fmt.Println()
	fmt.Println("    Topology:")
	fmt.Println("    Forge -> Sort -> Quench -> Sort -> Appraisal -> Sort -> Complete")
	fmt.Println("                |                         |")
	fmt.Println("                +--------- Refine <-------+")
	fmt.Println()
	fmt.Println(strings.Repeat("-", 72))
	fmt.Printf("%-12s %-5s %-20s %s\n", "TIME", "SYM", "NODE", "PHASE")
	fmt.Println(strings.Repeat("-", 72))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dynClient, err := buildKubeClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	gvr := schema.GroupVersionResource{
		Group:    "flow.gideas.io",
		Version:  "v1",
		Resource: "workitems",
	}

	// Watch the specific workitem.
	watcher, err := dynClient.Resource(gvr).Namespace(*namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("metadata.name=%s", *workitemName),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error watching workitem: %v\n", err)
		os.Exit(1)
	}
	defer watcher.Stop()

	archClient, archConn := connectArchivist(*archivistAddr)
	if archConn != nil {
		defer func() { _ = archConn.Close() }()
	}

	state := &eventState{
		seenFeedbackIDs: make(map[string]bool),
	}

	for event := range watcher.ResultChan() {
		if processEvent(ctx, event, state, archClient) {
			return
		}
	}
}

// printLiveState queries the Archivist for the current haiku and any new
// feedback, printing them inline in the timeline.
func printLiveState(
	ctx context.Context, client flowv1.ArchivistServiceClient,
	workitemID string, prevHash *string, seenFeedback map[string]bool,
) {
	// Fetch current haiku text.
	haikuResp, err := client.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId: workitemID,
		ArtefactId: "haiku",
	})
	if err == nil {
		hash := haikuResp.GetVersionHash()
		if hash != *prevHash && len(haikuResp.GetContent()) > 0 {
			// New or updated haiku — display it.
			haiku := strings.TrimSpace(string(haikuResp.GetContent()))
			lines := strings.Split(haiku, "\n")
			fmt.Printf("%-12s       \033[36m", "")
			for i, line := range lines {
				fmt.Print(strings.TrimSpace(line))
				if i < len(lines)-1 {
					fmt.Print(" / ")
				}
			}
			fmt.Printf("\033[0m\n")
			*prevHash = hash
		}
	}

	// Fetch feedback and print only new items.
	fbResp, err := client.GetFeedback(ctx, &flowv1.GetFeedbackRequest{
		WorkitemId: workitemID,
		ArtefactId: "haiku",
	})
	if err == nil {
		for _, fb := range fbResp.GetFeedbackItems() {
			id := fb.GetId()
			if seenFeedback[id] {
				continue
			}
			seenFeedback[id] = true

			msg := fb.GetMessage()
			// For multi-line feedback (e.g. quench syllable breakdown),
			// show only the first line in the timeline.
			if idx := strings.Index(msg, "\n"); idx > 0 {
				msg = msg[:idx]
			}
			canWontFix := fb.GetCanWontFix()
			canWontFixLabel := "refusable"
			if !canWontFix {
				canWontFixLabel = "fixed"
			}
			stateLabel := strings.TrimPrefix(fb.GetState().String(), "FEEDBACK_STATE_")
			fmt.Printf("%-12s       [%s/%s] %s\033[0m\n", "", stateLabel, canWontFixLabel, msg)
		}
	}
}

// fetchAndPrintHaiku queries the Archivist for the final haiku and petition.
func fetchAndPrintHaiku(ctx context.Context, addr, workitemID string) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nCould not connect to archivist at %s: %v\n", addr, err)
		return
	}
	defer func() { _ = conn.Close() }()

	client := flowv1.NewArchivistServiceClient(conn)

	// Get the petition.
	petResp, err := client.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId: workitemID,
		ArtefactId: "petition",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nCould not fetch petition: %v\n", err)
	} else {
		fmt.Printf("\n--- PETITION ---\n%s\n", string(petResp.GetContent()))
	}

	// Get the haiku.
	haikuResp, err := client.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId: workitemID,
		ArtefactId: "haiku",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nCould not fetch haiku: %v\n", err)
		return
	}

	fmt.Println("\n--- FINAL HAIKU ---")
	fmt.Println(string(haikuResp.GetContent()))

	// Get stamps.
	stampsResp, err := client.GetStamps(ctx, &flowv1.GetStampsRequest{
		WorkitemId: workitemID,
		ArtefactId: "haiku",
	})
	if err == nil && len(stampsResp.GetStamps()) > 0 {
		fmt.Println("\n--- STAMPS ---")
		for _, s := range stampsResp.GetStamps() {
			fmt.Printf("  [%s] applied by %s\n", s.GetName(), s.GetApplyingNode())
		}
	}

	// Get version history.
	metaResp, err := client.GetArtefactMetadata(ctx, &flowv1.GetArtefactMetadataRequest{
		WorkitemId: workitemID,
		ArtefactId: "haiku",
	})
	if err == nil {
		fmt.Printf("\n--- HISTORY ---\n  %d version(s)\n", len(metaResp.GetVersionHistory()))
	}

	// Get feedback.
	feedbackResp, err := client.GetFeedback(ctx, &flowv1.GetFeedbackRequest{
		WorkitemId: workitemID,
		ArtefactId: "haiku",
	})
	if err == nil && len(feedbackResp.GetFeedbackItems()) > 0 {
		fmt.Println("\n--- FEEDBACK HISTORY ---")
		for _, fb := range feedbackResp.GetFeedbackItems() {
			fmt.Printf("  [%s] %s\n", fb.GetState().String(), fb.GetMessage())
		}
	}

	fmt.Println()

	// Dump raw artefact state as JSON for completeness.
	stateResp, err := client.QueryArtefactState(ctx, &flowv1.QueryArtefactStateRequest{
		WorkitemId: workitemID,
	})
	if err == nil {
		fmt.Println("--- ARTEFACT STATE (JSON) ---")
		for _, as := range stateResp.GetArtefactStates() {
			data, _ := json.MarshalIndent(map[string]any{
				"artefact_id":       as.GetArtefactId(),
				"governed_artefact": as.GetGovernedArtefact(),
				"stamps":            as.GetStampNames(),
				"version_hash":      as.GetCurrentVersionHash(),
			}, "  ", "  ")
			fmt.Printf("  %s\n", string(data))
		}
	}
}
