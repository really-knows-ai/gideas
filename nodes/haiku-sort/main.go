// Sort is the central routing hub and gate of the Foundry Cycle.
//
// Sort discovers the Flow topology at assignment time via GetFlowTopology
// and makes routing decisions dynamically — no hardcoded stamp names, output
// names, or routing targets. The algorithm:
//
//  1. Call GetFlowTopology() to discover self, peer nodes, and exit contract.
//  2. Build stamp-provider maps from node capabilities.
//  3. Check for deadlock FIRST (scans all feedback items).
//  4. Walk NODE_ORDER: for each provider node, check its stamps in order.
//     If a stamp is present but the provider left unresolved feedback → refine.
//     If a stamp is missing → route to provider via self's output.
//  5. Apply any stamps Sort itself can provide from the exit contract.
//  6. All governance satisfied → Complete().
//
// Environment:
//
//	NODE_ORDER          — comma-separated node names defining stamp-checking
//	                      order. e.g. "quench,appraise". Required.
//	DEADLOCK_THRESHOLD  — feedback depth at which items are escalated to Assay.
//	                      Default: 3.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

const (
	// defaultDeadlockThreshold is the fallback when DEADLOCK_THRESHOLD is
	// unset or invalid. Should come from FoundryNode CRD container env.
	defaultDeadlockThreshold int32 = 3

	// outputAssay is the well-known output name for escalation to Assay.
	// This is the one convention Sort retains — the assay output name.
	outputAssay = "assay"

	// outputRefine is the well-known output name for routing to refinement.
	outputRefine = "refine"
)

func main() {
	slog.Info("haiku-sort: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("haiku-sort: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("haiku-sort: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("sort: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	return handleSort(ctx, client)
}

// handleSort contains the Sort gate logic, separated from the handler
// boilerplate for testability.
func handleSort(ctx context.Context, client *flow.Client) error {
	_, _ = client.Heartbeat(ctx)

	threshold := readDeadlockThreshold()
	nodeOrder := parseNodeOrder(os.Getenv("NODE_ORDER"))

	// ── Step 0: Discover topology ─────────────────────────────────────
	topology, err := client.GetFlowTopology(ctx)
	if err != nil {
		return fmt.Errorf("sort: get flow topology: %w", err)
	}

	selfNode := topology.GetSelf()
	exitContract := topology.GetExitContract()

	// Build stamp-provider map: kind → stamp → provider node name.
	stampProviders := buildStampProviders(topology.GetNodes())

	// Build output-routing map: target node name → output name (from self's outputs).
	outputRoutes := buildOutputRoutes(selfNode)

	// ── For each artefact kind in exit contract ───────────────────────
	for kind, requirements := range exitContract {
		requiredStamps := requirements.GetStamps()

		// ── Step 1: Check deadlock FIRST ──────────────────────────────
		deadlocked, err := checkDeadlock(ctx, client, kind, threshold)
		if err != nil {
			return err
		}
		if deadlocked {
			slog.Info("haiku-sort: routing to assay (deadlocked feedback)",
				"artefact_kind", kind)
			_, err = client.RouteToOutput(ctx, outputAssay)
			if err != nil {
				return fmt.Errorf("sort: route to assay: %w", err)
			}
			return nil
		}

		// ── Step 2: Check stamps in NODE_ORDER ────────────────────────
		for _, nodeName := range nodeOrder {
			stamps := stampsProvidedBy(nodeName, kind, stampProviders)
			for _, stamp := range stamps {
				hasStamp, err := client.HasStamp(ctx, kind, stamp)
				if err != nil {
					return fmt.Errorf("sort: check stamp %s: %w", stamp, err)
				}
				if hasStamp {
					// Stamp present — check for unresolved feedback from this provider.
					unresolvedFromProvider, err := hasUnresolvedFeedbackFrom(ctx, client, kind, nodeName)
					if err != nil {
						return err
					}
					if unresolvedFromProvider {
						slog.Info("haiku-sort: routing to refine (unresolved feedback from provider)",
							"artefact_kind", kind,
							"provider", nodeName,
							"stamp", stamp)
						_, err = client.RouteToOutput(ctx, outputRefine)
						if err != nil {
							return fmt.Errorf("sort: route to refine: %w", err)
						}
						return nil
					}
				} else {
					// Stamp missing — route to provider node.
					outputName, ok := outputRoutes[nodeName]
					if !ok {
						return fmt.Errorf("sort: no output route to provider %q for stamp %q", nodeName, stamp)
					}
					slog.Info("haiku-sort: routing to provider (missing stamp)",
						"artefact_kind", kind,
						"provider", nodeName,
						"stamp", stamp,
						"output", outputName)
					_, err = client.RouteToOutput(ctx, outputName)
					if err != nil {
						return fmt.Errorf("sort: route to %s: %w", outputName, err)
					}
					return nil
				}
			}
		}

		// ── Step 3: All stamps from NODE_ORDER present ────────────────
		// Apply any stamps Sort itself can provide.
		myStamps := stampsProvidedBy(selfNode.GetName(), kind, stampProviders)
		for _, stamp := range myStamps {
			if containsString(requiredStamps, stamp) {
				slog.Info("haiku-sort: stamping artefact",
					"artefact_kind", kind,
					"stamp", stamp)
				if _, err := client.StampArtefact(ctx, kind, stamp); err != nil {
					return fmt.Errorf("sort: stamp %s: %w", stamp, err)
				}
			}
		}
	}

	// ── Step 4: All governance satisfied → complete ───────────────────
	slog.Info("haiku-sort: all governance requirements met, completing workitem")
	if _, err := client.Complete(ctx, ""); err != nil {
		return fmt.Errorf("sort: complete: %w", err)
	}

	return nil
}

// buildStampProviders builds a map of artefact kind → stamp name → provider node name
// from node capabilities. It looks for capabilities matching STAMP:artefact/<kind>/<stamp>.
func buildStampProviders(nodes map[string]*flowv1.FlowNode) map[string]map[string]string {
	providers := make(map[string]map[string]string)
	for _, node := range nodes {
		for _, cap := range node.GetCapabilities() {
			kind, stamp, ok := parseStampCapability(cap)
			if !ok {
				continue
			}
			if providers[kind] == nil {
				providers[kind] = make(map[string]string)
			}
			providers[kind][stamp] = node.GetName()
		}
	}
	return providers
}

// parseStampCapability parses a capability string of the form
// "STAMP:artefact/<kind>/<stamp>" and returns the kind and stamp name.
func parseStampCapability(cap string) (kind, stamp string, ok bool) {
	const prefix = "STAMP:artefact/"
	if !strings.HasPrefix(cap, prefix) {
		return "", "", false
	}
	rest := cap[len(prefix):]
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// buildOutputRoutes builds a map of target node name → output name
// from the calling node's configured outputs.
func buildOutputRoutes(self *flowv1.FlowNode) map[string]string {
	routes := make(map[string]string)
	for _, o := range self.GetOutputs() {
		routes[o.GetTarget()] = o.GetName()
	}
	return routes
}

// stampsProvidedBy returns the stamp names that the given node provides
// for the specified artefact kind, preserving the order of discovery.
func stampsProvidedBy(nodeName, kind string, providers map[string]map[string]string) []string {
	kindMap := providers[kind]
	if kindMap == nil {
		return nil
	}
	var stamps []string
	for stamp, provider := range kindMap {
		if provider == nodeName {
			stamps = append(stamps, stamp)
		}
	}
	return stamps
}

// hasUnresolvedFeedbackFrom checks whether there is unresolved feedback
// from the specified source node on the given artefact kind.
func hasUnresolvedFeedbackFrom(
	ctx context.Context, client *flow.Client, artefactID, sourceNode string,
) (bool, error) {
	items, err := client.GetFeedback(ctx, artefactID)
	if err != nil {
		return false, fmt.Errorf("sort: get feedback for %s: %w", artefactID, err)
	}
	for _, item := range items {
		if item.GetSource() != sourceNode {
			continue
		}
		state := item.GetState()
		if state != flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED &&
			state != flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED {
			return true, nil
		}
	}
	return false, nil
}

// checkDeadlock scans feedback items for deadlock conditions on the
// specified artefact kind.
//
// For each non-resolved feedback item:
//   - If already DEADLOCKED → return true (route to Assay, no state change).
//   - If depth >= threshold → call DeadlockFeedback(), return true.
//
// First match wins — one routing decision per Sort invocation.
func checkDeadlock(
	ctx context.Context, client *flow.Client, artefactID string, threshold int32,
) (bool, error) {
	items, err := client.GetFeedback(ctx, artefactID)
	if err != nil {
		return false, fmt.Errorf("sort: get feedback: %w", err)
	}

	for _, item := range items {
		if item.GetState() == flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED {
			continue
		}

		// Already deadlocked from a prior cycle — route to Assay.
		if item.GetState() == flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED {
			slog.Info("haiku-sort: found deadlocked feedback item",
				"feedback_id", item.GetId())
			return true, nil
		}

		depth, err := client.GetFeedbackDepth(ctx, item.GetId())
		if err != nil {
			return false, fmt.Errorf(
				"sort: get feedback depth for %s: %w", item.GetId(), err)
		}

		if depth >= threshold {
			slog.Info("haiku-sort: deadlocking feedback item",
				"feedback_id", item.GetId(),
				"depth", depth,
				"threshold", threshold)
			if _, err := client.DeadlockFeedback(ctx, item.GetId()); err != nil {
				return false, fmt.Errorf(
					"sort: deadlock feedback %s: %w", item.GetId(), err)
			}
			return true, nil
		}
	}

	return false, nil
}

// parseNodeOrder parses the NODE_ORDER environment variable value
// (comma-separated node names) into a string slice. Empty or whitespace
// entries are discarded.
func parseNodeOrder(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// readDeadlockThreshold reads the DEADLOCK_THRESHOLD environment variable.
// Returns defaultDeadlockThreshold if unset or not a valid positive integer.
func readDeadlockThreshold() int32 {
	raw := os.Getenv("DEADLOCK_THRESHOLD")
	if raw == "" {
		return defaultDeadlockThreshold
	}
	v, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || v < 1 {
		slog.Warn("haiku-sort: invalid DEADLOCK_THRESHOLD, using default",
			"value", raw,
			"default", defaultDeadlockThreshold)
		return defaultDeadlockThreshold
	}
	return int32(v)
}

// containsString returns true if the slice contains the target string.
func containsString(slice []string, target string) bool {
	return slices.Contains(slice, target)
}
