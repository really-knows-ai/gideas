// Sort is the central routing hub and gate of the Foundry Cycle.
//
// Sort discovers the Flow topology at assignment time via GetFlowTopology
// and makes routing decisions dynamically — no hardcoded stamp names, output
// names, or routing targets. The algorithm:
//
//  1. Call GetFlowTopology() to discover self, peer nodes, and exit contract.
//  2. Build stamp-provider maps from node capabilities.
//  3. Check for deadlock FIRST (scans all feedback items).
//  4. Walk nodeOrder: for each provider node, check its stamps in order.
//     If a stamp is present but the provider left unresolved feedback → refine.
//     If a stamp is missing → route to provider via self's output.
//  5. Apply any stamps Sort itself can provide from the exit contract.
//  6. All governance satisfied → Complete().
//
// Configuration (YAML via NODE_CONFIG_PATH, default /etc/foundry/node-config.yaml):
//
//	nodeOrder:          comma-separated node names defining stamp-checking
//	                    order. e.g. "quench,appraise". Required.
//	deadlockThreshold:  feedback depth at which items are escalated to the Arbiter.
//	                    Default: 3.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
)

const (
	// defaultDeadlockThreshold is the fallback when deadlockThreshold is
	// unset or invalid in the config file.
	defaultDeadlockThreshold int32 = 3

	// outputArbiter is the well-known output name for escalation to the Arbiter.
	// This is the one convention Sort retains — the arbiter output name.
	outputArbiter = "arbiter"

	// outputRefine is the well-known output name for routing to refinement.
	outputRefine = "refine"
)

// sortConfig holds Sort's runtime configuration, loaded from a YAML file.
type sortConfig struct {
	// NodeOrder is a comma-separated list of node names defining the order
	// in which stamps are checked. e.g. "quench,appraise".
	NodeOrder string `yaml:"nodeOrder"`

	// DeadlockThreshold is the feedback depth at which items are escalated
	// to the Arbiter. Zero or negative values fall back to defaultDeadlockThreshold.
	DeadlockThreshold int32 `yaml:"deadlockThreshold"`
}

// threshold returns the effective deadlock threshold, applying the default
// when the configured value is not a valid positive integer.
func (c *sortConfig) threshold() int32 {
	if c.DeadlockThreshold < 1 {
		return defaultDeadlockThreshold
	}
	return c.DeadlockThreshold
}

func main() {
	slog.Info("sort: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("sort: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("sort: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("sort: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	cfg, err := nodeconfig.Load[sortConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("sort: load config: %w", err)
	}

	return handleSort(ctx, client, cfg)
}

// handleSort contains the Sort gate logic, separated from the handler
// boilerplate for testability.
func handleSort(ctx context.Context, client *flow.Client, cfg *sortConfig) error {
	_, _ = client.Heartbeat(ctx)

	threshold := cfg.threshold()
	nodeOrder := parseNodeOrder(cfg.NodeOrder)

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
			slog.Info("sort: routing to arbiter (deadlocked feedback)",
				"artefact_kind", kind)
			_, err = client.RouteToOutput(ctx, outputArbiter)
			if err != nil {
				return fmt.Errorf("sort: route to arbiter: %w", err)
			}
			return nil
		}

		// ── Step 2: Check stamps in nodeOrder ─────────────────────────
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
						slog.Info("sort: routing to refine (unresolved feedback from provider)",
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
					slog.Info("sort: routing to provider (missing stamp)",
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

		// ── Step 3: All stamps from nodeOrder present ─────────────────
		// Apply any stamps Sort itself can provide.
		myStamps := stampsProvidedBy(selfNode.GetName(), kind, stampProviders)
		for _, stamp := range myStamps {
			if containsString(requiredStamps, stamp) {
				slog.Info("sort: stamping artefact",
					"artefact_kind", kind,
					"stamp", stamp)
				if _, err := client.StampArtefact(ctx, kind, stamp); err != nil {
					return fmt.Errorf("sort: stamp %s: %w", stamp, err)
				}
			}
		}
	}

	// ── Step 4: All governance satisfied → complete ───────────────────
	slog.Info("sort: all governance requirements met, completing workitem")
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
//   - If already DEADLOCKED → return true (route to Arbiter, no state change).
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

		// Already deadlocked from a prior cycle — route to Arbiter.
		if item.GetState() == flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED {
			slog.Info("sort: found deadlocked feedback item",
				"feedback_id", item.GetId())
			return true, nil
		}

		depth, err := client.GetFeedbackDepth(ctx, item.GetId())
		if err != nil {
			return false, fmt.Errorf(
				"sort: get feedback depth for %s: %w", item.GetId(), err)
		}

		if depth >= threshold {
			slog.Info("sort: deadlocking feedback item",
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

// parseNodeOrder parses a comma-separated node order string into a string
// slice. Empty or whitespace entries are discarded.
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

// containsString returns true if the slice contains the target string.
func containsString(slice []string, target string) bool {
	return slices.Contains(slice, target)
}
