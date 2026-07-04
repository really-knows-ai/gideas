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
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	// defaultDeadlockThreshold is the fallback when deadlockThreshold is
	// unset or invalid in the config file.
	defaultDeadlockThreshold int32 = 3

	// outputArbiter is the well-known output name for escalation to the
	// human-arbiter node when deadlock is detected.
	outputArbiter = "human-arbiter"

	// outputRefine is the well-known output name for routing to refinement.
	outputRefine = "refine"

	// outputAppraisal is the well-known output name for routing to appraisal adjudication.
	outputAppraisal = "appraisal"

	// pendingHoldTimeout is the suspension timeout for workitems held
	// pending a dispute resolution. Defaults to 2 weeks (the platform's
	// default max suspend timeout).
	pendingHoldTimeout = 336 * time.Hour
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

	workitem, err := client.GetWorkitem()
	if err != nil {
		return fmt.Errorf("sort: get workitem: %w", err)
	}

	cfg, err := nodeconfig.Load[sortConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("sort: load config: %w", err)
	}

	return handleSort(ctx, workitem, client, cfg)
}

// handleSort contains the Sort gate logic, separated from the handler
// boilerplate for testability.
func handleSort(ctx context.Context, workitem *flow.Workitem, client *flow.Client, cfg *sortConfig) error {
	if err := workitem.Heartbeat(); err != nil {
		return fmt.Errorf("sort: heartbeat: %w", err)
	}

	threshold := cfg.threshold()
	nodeOrder := parseNodeOrder(cfg.NodeOrder)

	// ── Step 0: Discover topology ─────────────────────────────────────
	// ponytail: Using RawOperator escape hatch for self/outputs because the
	// new Flow domain does not expose GetSelf() or node outputs. This can
	// be cleaned up when the Flow domain gains these accessors.
	topology, err := client.RawOperator().GetFlowTopology(ctx, &flowv1.GetFlowTopologyRequest{})
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
		deadlockedItem, err := checkDeadlock(workitem, kind, threshold)
		if err != nil {
			return err
		}
		if deadlockedItem != nil {
			// Check if any cited laws have active dispute records.
			// If so, suspend (pending-hold) instead of routing to arbiter.
			// ponytail: findActiveDisputeForFeedback needs the proto
			// FeedbackItem for Justification access; the domain Feedback
			// does not expose GetJustification(). Clean up in Phase 10
			// when Feedback gains the missing accessors or Proto() escape hatch.
			deadlockedProto := deadlockedItem.PB()
			if deadlockedProto != nil {
				if petitionID, ok := findActiveDisputeForFeedback(ctx, deadlockedProto); ok {
					slog.Info("sort: suspending pending-hold (active dispute record)",
						"artefact_kind", kind,
						"petition_id", petitionID)
					condition := fmt.Sprintf("dispute_retired(%q)", petitionID)
					if err := workitem.Suspend(
						flow.WithCondition(condition),
						flow.WithSuspendTimeout(pendingHoldTimeout),
					); err != nil {
						return fmt.Errorf("sort: suspend pending-hold: %w", err)
					}
					return nil
				}
			}
			slog.Info("sort: routing to arbiter (deadlocked feedback)",
				"artefact_kind", kind)
			if err := workitem.RouteTo(outputArbiter); err != nil {
				return fmt.Errorf("sort: route to arbiter: %w", err)
			}
			return nil
		}

		// ── Step 2: Check stamps in nodeOrder ─────────────────────────
		for _, nodeName := range nodeOrder {
			stamps := stampsProvidedBy(nodeName, kind, stampProviders)
			for _, stamp := range stamps {
				art, artErr := workitem.GetArtefact(kind)
				if artErr != nil {
					return fmt.Errorf("sort: get artefact %s: %w", kind, artErr)
				}
				hasStamp, hsErr := art.HasStamp(stamp)
				if hsErr != nil {
					return fmt.Errorf("sort: check stamp %s: %w", stamp, hsErr)
				}
				if hasStamp {
					// Stamp present — check for unaddressed feedback (NEW/REJECTED) from this provider.
					unaddressedFromProvider, err := hasUnaddressedFeedbackFrom(workitem, kind, nodeName)
					if err != nil {
						return err
					}
					if unaddressedFromProvider {
						slog.Info("sort: routing to refine (unaddressed feedback from provider)",
							"artefact_kind", kind,
							"provider", nodeName,
							"stamp", stamp)
						if err := workitem.RouteTo(outputRefine); err != nil {
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
					if err := workitem.RouteTo(outputName); err != nil {
						return fmt.Errorf("sort: route to %s: %w", outputName, err)
					}
					return nil
				}
			}
		}

		// ── Step 3: All stamps from nodeOrder present ─────────────────
		// Check for addressed feedback (ACTIONED/WONT_FIX) needing adjudication.
		hasAddressed, err := hasAddressedFeedback(workitem, kind)
		if err != nil {
			return err
		}
		if hasAddressed {
			slog.Info("sort: routing to appraisal (addressed feedback needs adjudication)",
				"artefact_kind", kind)
			if err := workitem.RouteTo(outputAppraisal); err != nil {
				return fmt.Errorf("sort: route to appraisal: %w", err)
			}
			return nil
		}

		// Apply any stamps Sort itself can provide.
		myStamps := stampsProvidedBy(selfNode.GetName(), kind, stampProviders)
		if len(myStamps) > 0 {
			art, artErr := workitem.GetArtefact(kind)
			if artErr != nil {
				return fmt.Errorf("sort: get artefact %s: %w", kind, artErr)
			}
			for _, stamp := range myStamps {
				if slices.Contains(requiredStamps, stamp) {
					slog.Info("sort: stamping artefact",
						"artefact_kind", kind,
						"stamp", stamp)
					if err := art.Stamp(stamp); err != nil {
						return fmt.Errorf("sort: stamp %s: %w", stamp, err)
					}
				}
			}
		}
	}

	// ── Step 4: All governance satisfied → complete ───────────────────
	slog.Info("sort: all governance requirements met, completing workitem")
	if err := workitem.Complete(); err != nil {
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
		kindMap = providers["*"]
	}
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

// hasUnaddressedFeedbackFrom checks whether there is NEW or REJECTED feedback
// from the specified source node on the given artefact kind.
func hasUnaddressedFeedbackFrom(
	workitem *flow.Workitem, artefactID, sourceNode string,
) (bool, error) {
	items, err := workitem.GetFeedback(artefactID)
	if err != nil {
		return false, fmt.Errorf("sort: get feedback for %s: %w", artefactID, err)
	}
	for _, item := range items {
		if item.GetSource() != sourceNode {
			continue
		}
		state := item.GetState()
		if state == flow.FeedbackStateNew ||
			state == flow.FeedbackStateRejected {
			return true, nil
		}
	}
	return false, nil
}

// hasAddressedFeedback checks whether any ACTIONED or WONT_FIX feedback
// exists on the given artefact kind (any source).
func hasAddressedFeedback(
	workitem *flow.Workitem, artefactID string,
) (bool, error) {
	items, err := workitem.GetFeedback(artefactID)
	if err != nil {
		return false, fmt.Errorf("sort: get feedback for %s: %w", artefactID, err)
	}
	for _, item := range items {
		state := item.GetState()
		if state == flow.FeedbackStateActioned ||
			state == flow.FeedbackStateWontFix {
			return true, nil
		}
	}
	return false, nil
}

// findActiveDisputeForFeedback extracts law IDs from the deadlocked feedback
// item's citation justification and queries the Librarian for active dispute
// records. If any cited law has an active dispute, the petition_id of the
// first matching dispute record is returned.
//
// Returns ("", false) when there is no citation, no cited law IDs, or no
// active dispute records matching any cited law. Errors from GetActiveDisputes
// are logged and treated as "no dispute found" to keep the arbiter path as
// the safe fallback.
//
// ponytail: This function creates a direct gRPC connection because the new
// SDK surface no longer exposes Client.Librarian. In Phase 10, replace this
// with a proper Workitem.GetActiveDisputes() method that reuses the session.
func findActiveDisputeForFeedback(
	ctx context.Context, item *flowv1.FeedbackItem,
) (string, bool) {
	citation := item.GetJustification().GetCitation()
	if citation == nil {
		return "", false
	}
	lawIDs := citation.GetCitationIds()
	if len(lawIDs) == 0 {
		return "", false
	}

	// Create a direct gRPC connection to the Sidecar for the Librarian call.
	sidecarAddr := os.Getenv(flow.EnvSidecarAddress)
	if sidecarAddr == "" {
		sidecarAddr = flow.DefaultSidecarAddress
	}
	conn, err := grpc.NewClient(
		sidecarAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		slog.Warn("sort: failed to connect for dispute check, falling back to arbiter", "error", err)
		return "", false
	}
	defer func() { _ = conn.Close() }()
	librarian := flowv1.NewLibrarianServiceClient(conn)

	// Query each cited law for an active dispute record.
	for _, lawID := range lawIDs {
		resp, err := librarian.GetActiveDisputes(ctx,
			&flowv1.GetActiveDisputesRequest{LawId: lawID})
		if err != nil {
			slog.Warn("sort: GetActiveDisputes failed, falling back to arbiter",
				"law_id", lawID, "error", err)
			continue
		}
		if len(resp.GetRecords()) > 0 {
			return resp.GetRecords()[0].GetPetitionId(), true
		}
	}
	return "", false
}

// checkDeadlock scans feedback items for deadlock conditions on the
// specified artefact kind.
//
// For each non-resolved feedback item:
//   - If already DEADLOCKED → return the item (route to Arbiter, no state change).
//   - If depth >= threshold → call DeadlockFeedback(), return the item.
//
// First match wins — one routing decision per Sort invocation. Returns nil
// when no feedback item is deadlocked.
func checkDeadlock(
	workitem *flow.Workitem, artefactID string, threshold int32,
) (*flow.Feedback, error) {
	items, err := workitem.GetFeedback(artefactID)
	if err != nil {
		return nil, fmt.Errorf("sort: get feedback: %w", err)
	}

	for _, item := range items {
		if item.GetState() == flow.FeedbackStateResolved {
			continue
		}

		// Already deadlocked from a prior cycle — route to Arbiter.
		if item.GetState() == flow.FeedbackStateDeadlocked {
			slog.Info("sort: found deadlocked feedback item",
				"feedback_id", item.GetID())
			return item, nil
		}

		depth, err := item.GetDepth()
		if err != nil {
			return nil, fmt.Errorf(
				"sort: get feedback depth for %s: %w", item.GetID(), err)
		}

		if depth >= threshold {
			slog.Info("sort: deadlocking feedback item",
				"feedback_id", item.GetID(),
				"depth", depth,
				"threshold", threshold)
			if err := item.Deadlock(); err != nil {
				return nil, fmt.Errorf(
					"sort: deadlock feedback %s: %w", item.GetID(), err)
			}
			return item, nil
		}
	}

	return nil, nil
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
