// Facilitator is the lifecycle owner for deadlock resolution in the Foundry
// judiciary architecture.
//
// It is a generic node — one image (facilitator:latest), multiple CRD
// instances (e.g. facilitator, clerk-facilitator). When Sort detects
// deadlocked feedback it routes to the Facilitator, which assembles a
// set of evidence artefacts, creates a child workitem for the Arbiter,
// and suspends. When the Arbiter child completes, the Facilitator resumes
// and routes the result back into the cycle.
//
// The handler has two phases, distinguished by whether completed children
// exist:
//
//  1. First invocation (no children):
//     - Discover topology and exit contract to enumerate artefact kinds.
//     - Scan feedback for DEADLOCKED items; select the first one.
//     - Assemble evidence as five separate artefacts on the child:
//     dispute-workitem, dispute-details, dispute-artefact,
//     dispute-inputs, appendix.
//     - Store a disputed-artefact JSON reference on the child.
//     - Route the child to the Arbiter.
//     - Suspend with condition: children.all(c, c.phase == "Completed")
//
//  2. Post-resume (completed child exists):
//     - Check the child's CompletionReason.
//     - Cancelled → Complete(WithReason(cancelled))
//     - Success   → RouteToOutput("resolved")
//
// Configuration (YAML via NODE_CONFIG_PATH, default /etc/foundry/node-config.yaml):
//
//	arbiterNode: arbiter
//	inputArtefacts:
//	  - petition
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/artefacts"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
)

// ── Constants ────────────────────────────────────────────────────────────

const (
	// Child artefact IDs — each is a separate artefact stored on the
	// child workitem for the Arbiter to consume independently.

	// artefactDisputeWorkitem carries workitem context and friction summary.
	artefactDisputeWorkitem = "dispute-workitem"

	// artefactDisputeDetails carries the single disputed feedback item
	// with full history, cited laws, and per-law friction.
	artefactDisputeDetails = "dispute-details"

	// artefactDisputeArtefact carries the raw content of the artefact
	// that received the deadlocked feedback.
	artefactDisputeArtefact = "dispute-artefact"

	// artefactDisputeInputs carries the concatenated content of all
	// configured input artefacts (e.g. user story, spec).
	artefactDisputeInputs = "dispute-inputs"

	// artefactAppendix carries all laws applicable to the artefact kind.
	artefactAppendix = "appendix"

	// artefactDisputedRef is the artefact ID for the JSON reference to the
	// disputed artefact, stored on the child workitem. Contains the
	// artefact kind, parent workitem ID, and the disputed feedback ID.
	artefactDisputedRef = "disputed-artefact"

	// outputResolved is the output name used when the Arbiter child
	// completes successfully and the dispute is resolved.
	outputResolved = "resolved"

	// defaultArbiterNode is the fallback target node when arbiterNode is
	// not specified in the config.
	defaultArbiterNode = "arbiter"

	// suspendCondition is the CEL expression passed to Suspend. The
	// Operator evaluates it on child phase-change events and resumes the
	// Facilitator when all children reach Completed.
	suspendCondition = `children.all(c, c.phase == "Completed")`
)

// ── Config ───────────────────────────────────────────────────────────────

// facilitatorConfig holds the Facilitator's runtime configuration.
type facilitatorConfig struct {
	// ArbiterNode is the FoundryNode name to route the child workitem to.
	// Defaults to "arbiter" if empty.
	ArbiterNode string `yaml:"arbiterNode"`

	// InputArtefacts lists the artefact IDs that describe the flow's
	// creative brief (e.g. "petition", "spec"). These are fetched from
	// the parent workitem and included in the dispute-inputs evidence
	// artefact so the Arbiter understands what the flow is trying to
	// produce.
	InputArtefacts []string `yaml:"inputArtefacts"`
}

// arbiterNode returns the configured arbiter target, falling back to the
// default when unset.
func (c *facilitatorConfig) arbiterNode() string {
	if c.ArbiterNode != "" {
		return c.ArbiterNode
	}
	return defaultArbiterNode
}

// validateConfig checks that the configuration is usable.
func validateConfig(_ *facilitatorConfig) error {
	return nil
}

// ── Entry point ──────────────────────────────────────────────────────────

func main() {
	slog.Info("facilitator: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("facilitator: server failed", "error", err)
		os.Exit(1)
	}
}

// ── Handler (layer 2) ────────────────────────────────────────────────────

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("facilitator: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("facilitator: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	cfg, err := nodeconfig.Load[facilitatorConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("facilitator: load config: %w", err)
	}

	if err := validateConfig(cfg); err != nil {
		return fmt.Errorf("facilitator: invalid config: %w", err)
	}

	return handleFacilitator(ctx, client, cfg, wctx)
}

// ── Core logic (layer 3 — testable) ─────────────────────────────────────

// handleFacilitator contains all Facilitator logic, separated from handler
// boilerplate for testability.
//
// Phase detection: if GetChildren returns any completed children, this is a
// post-resume invocation. Otherwise it is the first invocation.
func handleFacilitator(
	ctx context.Context,
	client *flow.Client,
	cfg *facilitatorConfig,
	wctx *flowv1.WorkitemContext,
) error {
	// ── Heartbeat ────────────────────────────────────────────────────
	_, _ = client.Heartbeat(ctx)

	// ── Phase detection ──────────────────────────────────────────────
	// Use the raw Operator stub because the SDK's GetChildren() strips
	// CompletionReason from the response. We need CompletionReason for
	// the post-resume path.
	resp, err := client.Operator.GetChildren(ctx, &flowv1.GetChildrenRequest{})
	if err != nil {
		return fmt.Errorf("facilitator: get children: %w", err)
	}

	children := resp.GetChildren()
	if hasCompletedChild(children) {
		emitTelemetry(ctx, client, "foundry.facilitator.started", map[string]any{
			"phase": "resume",
		})
		return handlePostResume(ctx, client, children)
	}

	emitTelemetry(ctx, client, "foundry.facilitator.started", map[string]any{
		"phase": "first",
	})
	return handleFirstInvocation(ctx, client, cfg, wctx)
}

// hasCompletedChild returns true if at least one child is in the Completed
// phase, indicating this is a post-resume invocation.
func hasCompletedChild(children []*flowv1.ChildWorkitemStatus) bool {
	for _, ch := range children {
		if ch.GetPhase() == flow.PhaseCompleted {
			return true
		}
	}
	return false
}

// ── First invocation ─────────────────────────────────────────────────────

// handleFirstInvocation runs on the initial dispatch: discovers topology,
// scans for deadlocked feedback, assembles evidence artefacts, creates an
// Arbiter child, and suspends.
func handleFirstInvocation(
	ctx context.Context,
	client *flow.Client,
	cfg *facilitatorConfig,
	wctx *flowv1.WorkitemContext,
) error {
	// ── Step 1: Discover topology ────────────────────────────────────
	topology, err := client.GetFlowTopology(ctx)
	if err != nil {
		return fmt.Errorf("facilitator: get flow topology: %w", err)
	}

	exitContract := topology.GetExitContract()

	// ── Step 2: Find deadlocked feedback ─────────────────────────────
	artefactKind, disputed, err := selectDisputedFeedback(ctx, client, exitContract)
	if err != nil {
		return err
	}

	// No deadlocked feedback — route to resolved with a warning.
	if disputed == nil {
		slog.Warn("facilitator: no deadlocked feedback found, routing to resolved")
		emitTelemetry(ctx, client, "foundry.facilitator.no_deadlock", map[string]any{
			"output": outputResolved,
		})
		if _, err := client.RouteToOutput(ctx, outputResolved); err != nil {
			return fmt.Errorf("facilitator: route to resolved (no deadlock): %w", err)
		}
		return nil
	}

	slog.Info("facilitator: disputed feedback selected",
		"artefact_kind", artefactKind,
		"feedback_id", disputed.GetId(),
	)

	// ── Step 3: Assemble evidence artefacts ──────────────────────────

	disputeWorkitem, err := buildDisputeWorkitem(ctx, client, wctx)
	if err != nil {
		return err
	}

	disputeDetails := buildDisputeDetails(ctx, client, disputed)

	disputeArtefactContent, err := buildDisputeArtefact(ctx, client, artefactKind)
	if err != nil {
		return err
	}

	disputeInputs, err := buildDisputeInputs(ctx, client, cfg.InputArtefacts)
	if err != nil {
		return err
	}

	appendix, err := buildAppendix(ctx, client, artefactKind)
	if err != nil {
		return err
	}

	emitTelemetry(ctx, client, "foundry.facilitator.evidence_assembled", map[string]any{
		"artefact_kind": artefactKind,
		"feedback_id":   disputed.GetId(),
	})

	// ── Step 4: Build disputed-artefact reference ────────────────────
	disputedRef := disputedArtefactRef{
		ArtefactKind: artefactKind,
		WorkitemID:   client.WorkitemID(),
		FeedbackID:   disputed.GetId(),
	}
	disputedRefJSON, err := json.Marshal(disputedRef)
	if err != nil {
		return fmt.Errorf("facilitator: marshal disputed-artefact ref: %w", err)
	}

	// ── Step 5: Create child, store artefacts, route to Arbiter ──────
	child, err := client.CreateChildWorkitem(ctx)
	if err != nil {
		return fmt.Errorf("facilitator: create child workitem: %w", err)
	}

	slog.Info("facilitator: child workitem created",
		"child_id", child.ID(),
		"arbiter_node", cfg.arbiterNode(),
	)

	// Store each evidence artefact on the child.
	childArtefacts := []struct {
		id      string
		content []byte
	}{
		{artefactDisputeWorkitem, []byte(disputeWorkitem)},
		{artefactDisputeDetails, []byte(disputeDetails)},
		{artefactDisputeArtefact, disputeArtefactContent},
		{artefactDisputeInputs, []byte(disputeInputs)},
		{artefactAppendix, []byte(appendix)},
		{artefactDisputedRef, disputedRefJSON},
	}

	for _, ca := range childArtefacts {
		if _, err := child.StoreArtefact(ctx, ca.id, "", ca.content); err != nil {
			return fmt.Errorf("facilitator: store %s on child: %w", ca.id, err)
		}
	}

	if _, err := child.RouteTo(ctx, cfg.arbiterNode()); err != nil {
		return fmt.Errorf("facilitator: route child to arbiter: %w", err)
	}

	// ── Step 6: Suspend ──────────────────────────────────────────────
	slog.Info("facilitator: suspending",
		"condition", suspendCondition,
		"child_id", child.ID(),
	)
	if err := client.Suspend(ctx, flow.WithCondition(suspendCondition)); err != nil {
		return fmt.Errorf("facilitator: suspend: %w", err)
	}

	emitTelemetry(ctx, client, "foundry.facilitator.suspended", map[string]any{
		"child_id":      child.ID(),
		"arbiter_node":  cfg.arbiterNode(),
		"artefact_kind": artefactKind,
		"feedback_id":   disputed.GetId(),
		"condition":     suspendCondition,
	})

	return nil
}

// ── Telemetry ────────────────────────────────────────────────────────────

// emitTelemetry records a structured telemetry event. Errors are logged
// but not propagated — telemetry failures must not block the handler.
func emitTelemetry(ctx context.Context, client *flow.Client, eventType string, payload map[string]any) {
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("facilitator: marshal telemetry payload", "event", eventType, "error", err)
		return
	}
	if err := client.RecordTelemetry(ctx, eventType, data); err != nil {
		slog.Warn("facilitator: record telemetry", "event", eventType, "error", err)
	}
}

// ── Deadlock selection ───────────────────────────────────────────────────

// disputedArtefactRef is the JSON structure stored as the "disputed-artefact"
// artefact on the child workitem. It tells the Arbiter which artefact on
// which workitem is under dispute, and which single feedback item triggered
// the dispute.
type disputedArtefactRef struct {
	ArtefactKind string `json:"artefact_kind"`
	WorkitemID   string `json:"workitem_id"`
	FeedbackID   string `json:"feedback_id"`
}

// selectDisputedFeedback scans all artefact kinds in the exit contract for
// deadlocked feedback and returns the first one found.
//
// Returns ("", nil, nil) when no deadlocked feedback exists.
func selectDisputedFeedback(
	ctx context.Context,
	client *flow.Client,
	exitContract map[string]*flowv1.StampRequirements,
) (string, *flowv1.FeedbackItem, error) {
	var (
		bestKind string
		bestItem *flowv1.FeedbackItem
	)

	for kind := range exitContract {
		items, err := client.GetFeedback(ctx, kind)
		if err != nil {
			return "", nil, fmt.Errorf("facilitator: get feedback for %s: %w", kind, err)
		}

		for _, item := range items {
			if item.GetState() != flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED {
				continue
			}
			if bestItem == nil {
				bestKind = kind
				bestItem = item
			}
		}
	}

	return bestKind, bestItem, nil
}

// ── Evidence artefact builders ───────────────────────────────────────────

// buildDisputeWorkitem produces the "dispute-workitem" Markdown artefact:
// workitem context (ID, namespace, node, metadata) and workitem-level
// friction summary.
func buildDisputeWorkitem(
	ctx context.Context,
	client *flow.Client,
	wctx *flowv1.WorkitemContext,
) (string, error) {
	var b strings.Builder

	b.WriteString("# Dispute Workitem\n\n")
	b.WriteString("## Workitem Context\n\n")
	fmt.Fprintf(&b, "- **Workitem ID**: %s\n", wctx.GetWorkitemId())
	fmt.Fprintf(&b, "- **Flow Namespace**: %s\n", wctx.GetFlowNamespace())
	fmt.Fprintf(&b, "- **Node ID**: %s\n", wctx.GetNodeId())

	if meta := wctx.GetMetadata(); len(meta) > 0 {
		b.WriteString("\n**Metadata**:\n\n")
		// Sort keys for deterministic output.
		keys := make([]string, 0, len(meta))
		for k := range meta {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "- `%s`: %s\n", k, meta[k])
		}
	}

	b.WriteString("\n## Friction Summary\n\n")
	friction, err := client.QueryFriction(ctx, &flowv1.FrictionFilter{
		WorkitemId: client.WorkitemID(),
	})
	if err != nil {
		return "", fmt.Errorf("facilitator: query workitem friction: %w", err)
	}
	if len(friction) == 0 {
		b.WriteString("No friction data available.\n")
	} else {
		for _, agg := range friction {
			fmt.Fprintf(&b, "- law=%s node=%s events=%d magnitude=%.2f\n",
				agg.GetLawId(), agg.GetNodeId(), agg.GetEventCount(), agg.GetTotalMagnitude())
		}
	}

	return b.String(), nil
}

// buildDisputeDetails produces the "dispute-details" Markdown artefact:
// the single disputed feedback item with full debate history, plus each
// cited law and its per-law friction for this workitem. Failures to
// retrieve individual cited laws or their friction are logged but do not
// fail the function — they are best-effort enrichment.
func buildDisputeDetails(
	ctx context.Context,
	client *flow.Client,
	item *flowv1.FeedbackItem,
) string {
	var b strings.Builder

	b.WriteString("# Dispute Details\n\n")

	// ── Feedback item ────────────────────────────────────────────────
	b.WriteString("## Disputed Feedback\n\n")
	fmt.Fprintf(&b, "- **ID**: %s\n", item.GetId())
	fmt.Fprintf(&b, "- **Source**: %s\n", item.GetSource())
	fmt.Fprintf(&b, "- **State**: %s\n", item.GetState().String())
	fmt.Fprintf(&b, "- **Message**: %s\n", item.GetMessage())

	if j := item.GetJustification(); j != nil {
		b.WriteString("\n### Justification\n\n")
		if c := j.GetCitation(); c != nil {
			fmt.Fprintf(&b, "- **Citation**: law_ids=%v\n", c.GetCitationIds())
		}
		if n := j.GetNovelArgument(); n != nil {
			fmt.Fprintf(&b, "- **Novel argument**: %s\n", n.GetArgument())
		}
	}

	b.WriteString("\n### Debate History\n\n")
	for _, event := range item.GetHistory() {
		fmt.Fprintf(&b, "- [%s] %s: %s\n",
			event.GetAction(),
			event.GetActor(),
			event.GetMessage())
	}
	if len(item.GetHistory()) == 0 {
		b.WriteString("No debate history.\n")
	}

	// ── Cited laws and their friction ────────────────────────────────
	citedIDs := extractCitedLawIDs(item)
	if len(citedIDs) > 0 {
		b.WriteString("\n## Cited Laws\n\n")
		for _, lawID := range citedIDs {
			law, err := client.GetLaw(ctx, lawID)
			if err != nil {
				slog.Warn("facilitator: get cited law failed, skipping",
					"law_id", lawID, "error", err)
				fmt.Fprintf(&b, "### %s\n\nFailed to retrieve law.\n\n", lawID)
				continue
			}

			fmt.Fprintf(&b, "### %s (Tier %d)\n\n", law.GetId(), int32(law.GetTier()))
			fmt.Fprintf(&b, "- **Goal**: %s\n", law.GetGoal())
			for _, rep := range law.GetRepresentations() {
				fmt.Fprintf(&b, "- **%s**: %s\n", rep.GetType(), rep.GetContent())
			}

			// Per-law friction for this workitem.
			friction, err := client.QueryFriction(ctx, &flowv1.FrictionFilter{
				LawId:      lawID,
				WorkitemId: client.WorkitemID(),
			})
			if err != nil {
				slog.Warn("facilitator: query friction for cited law failed",
					"law_id", lawID, "error", err)
			} else if len(friction) > 0 {
				b.WriteString("\n**Friction**:\n\n")
				for _, agg := range friction {
					fmt.Fprintf(&b, "- node=%s events=%d magnitude=%.2f\n",
						agg.GetNodeId(), agg.GetEventCount(), agg.GetTotalMagnitude())
				}
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

// extractCitedLawIDs returns the law IDs cited in the feedback item's
// justification. Returns nil if there is no citation.
func extractCitedLawIDs(item *flowv1.FeedbackItem) []string {
	j := item.GetJustification()
	if j == nil {
		return nil
	}
	c := j.GetCitation()
	if c == nil {
		return nil
	}
	return c.GetCitationIds()
}

// buildDisputeArtefact fetches the raw artefact content for the disputed
// artefact kind and returns it as bytes.
func buildDisputeArtefact(
	ctx context.Context,
	client *flow.Client,
	artefactKind string,
) ([]byte, error) {
	resp, err := client.GetArtefact(ctx, artefactKind)
	if err != nil {
		return nil, fmt.Errorf("facilitator: get artefact %s: %w", artefactKind, err)
	}
	return resp.GetContent(), nil
}

// buildDisputeInputs fetches each configured input artefact and
// concatenates them with headers into a single Markdown document.
func buildDisputeInputs(
	ctx context.Context,
	client *flow.Client,
	inputArtefacts []string,
) (string, error) {
	if len(inputArtefacts) == 0 {
		return "# Dispute Inputs\n\nNo input artefacts configured.\n", nil
	}

	content, err := artefacts.FetchInputs(ctx, client, inputArtefacts)
	if err != nil {
		return "", fmt.Errorf("facilitator: %w", err)
	}
	return "# Dispute Inputs\n\n" + content, nil
}

// buildAppendix produces the "appendix" Markdown artefact: all laws
// applicable to the disputed artefact kind.
func buildAppendix(
	ctx context.Context,
	client *flow.Client,
	artefactKind string,
) (string, error) {
	var b strings.Builder

	b.WriteString("# Appendix: All Laws\n\n")
	laws, err := client.QueryLaws(ctx, artefactKind, "")
	if err != nil {
		return "", fmt.Errorf("facilitator: query laws for %s: %w", artefactKind, err)
	}
	if len(laws) == 0 {
		b.WriteString("No laws found for this artefact kind.\n")
	} else {
		for _, law := range laws {
			fmt.Fprintf(&b, "## %s (Tier %d)\n\n", law.GetId(), int32(law.GetTier()))
			fmt.Fprintf(&b, "- **Goal**: %s\n", law.GetGoal())
			for _, rep := range law.GetRepresentations() {
				fmt.Fprintf(&b, "- **%s**: %s\n", rep.GetType(), rep.GetContent())
			}
			b.WriteString("\n")
		}
	}

	return b.String(), nil
}

// ── Post-resume ──────────────────────────────────────────────────────────

// handlePostResume runs after the Operator re-dispatches the Facilitator
// because the suspend condition was met (all children completed). It checks
// the child's CompletionReason and either routes to the resolved output or
// completes with cancellation.
//
// The Facilitator creates exactly one child (the Arbiter). We find the
// first completed child and inspect its reason. If the Arbiter was
// cancelled (e.g. HITL abort), the Facilitator propagates the cancellation.
// Otherwise, the dispute is resolved and we route back into the cycle.
func handlePostResume(
	ctx context.Context,
	client *flow.Client,
	children []*flowv1.ChildWorkitemStatus,
) error {
	// Find the first completed child.
	var completed *flowv1.ChildWorkitemStatus
	for _, ch := range children {
		if ch.GetPhase() == flow.PhaseCompleted {
			completed = ch
			break
		}
	}

	// Defensive: should not happen since hasCompletedChild gates entry,
	// but handle gracefully.
	if completed == nil {
		return fmt.Errorf("facilitator: post-resume but no completed child found")
	}

	slog.Info("facilitator: post-resume",
		"child_id", completed.GetWorkitemId(),
		"completion_reason", completed.GetCompletionReason().String(),
	)

	reason := completed.GetCompletionReason()

	if reason == flowv1.CompletionReason_COMPLETION_REASON_CANCELLED {
		slog.Info("facilitator: child cancelled, propagating cancellation")
		emitTelemetry(ctx, client, "foundry.facilitator.cancelled", map[string]any{
			"child_id": completed.GetWorkitemId(),
		})
		if _, err := client.Complete(ctx, flow.WithReason(
			flowv1.CompletionReason_COMPLETION_REASON_CANCELLED,
		)); err != nil {
			return fmt.Errorf("facilitator: complete with cancelled: %w", err)
		}
		return nil
	}

	// Success (UNSPECIFIED = normal completion).
	slog.Info("facilitator: child succeeded, routing to resolved")
	emitTelemetry(ctx, client, "foundry.facilitator.resolved", map[string]any{
		"child_id": completed.GetWorkitemId(),
		"output":   outputResolved,
	})
	if _, err := client.RouteToOutput(ctx, outputResolved); err != nil {
		return fmt.Errorf("facilitator: route to resolved: %w", err)
	}
	return nil
}
