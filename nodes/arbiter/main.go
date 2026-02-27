// Arbiter is the deadlock resolution node of the Foundry Judiciary.
//
// When the Sort node detects that a feedback cycle has deadlocked (feedback
// depth exceeds threshold), it routes the Workitem to the Arbiter. The
// Arbiter gathers evidence from both sides of the dispute, fans out to
// Juror nodes for deliberation, and routes to the Deliberation Gate for
// consensus tallying.
//
// The algorithm:
//
//  1. Discover flow topology and artefact kinds from the exit contract.
//  2. For each artefact kind, scan feedback for DEADLOCKED items.
//  3. Assemble evidence: feedback debate history, artefact content excerpt,
//     cited laws from both sides, and friction cost summary.
//  4. Frame the question: "Should the reviewer's feedback be upheld, or
//     should the refiner's refusal stand?"
//  5. Fan out to Juror nodes with question, evidence, and allowed outcomes.
//  6. Await Juror children and route to Deliberation Gate.
//  7. Store verdict-context artefact for downstream Clerk consumption.
//
// The Arbiter no longer calls Deliberate() or DraftLaw() directly. Verdict
// tallying is handled by the Deliberation Gate. Law minting and ruling
// linkage are handled downstream by the Clerk and Judiciary Gate.
//
// Configuration (YAML via NODE_CONFIG_PATH, default /etc/foundry/node-config.yaml):
//
//	jurySize:     5                # number of jurors to fan out to
//	jurorNode:    juror            # name of the Juror FoundryNode
//	gateOutput:   deliberation-gate  # output name for routing to the Deliberation Gate
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// Well-known artefact IDs
// ---------------------------------------------------------------------------

const (
	// Artefacts written on each child Workitem for the Juror.
	artefactQuestion   = "question"
	artefactEvidence   = "evidence"
	artefactOutcomes   = "allowed-outcomes"
	artefactPriorRound = "prior-round-reasoning"

	// Artefacts written on the parent Workitem for downstream consumers.
	artefactVerdictContext = "verdict-context"
)

// ---------------------------------------------------------------------------
// Well-known output names
// ---------------------------------------------------------------------------

const (
	// outputGate is the default output name for routing to the Deliberation Gate.
	outputGate = "deliberation-gate"

	// defaultJurorNode is the default FoundryNode name for the Juror.
	defaultJurorNode = "juror"

	// defaultJurySize is the fallback number of jurors to fan out to.
	defaultJurySize int32 = 5
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// arbiterConfig holds the Arbiter's runtime configuration.
type arbiterConfig struct {
	// JurySize is the number of jurors to fan out to. Default: 5.
	JurySize int32 `yaml:"jurySize"`

	// JurorNode is the FoundryNode name to route child Workitems to.
	// Default: "juror".
	JurorNode string `yaml:"jurorNode"`

	// GateOutput is the output name for routing to the Deliberation Gate
	// after fan-out is complete. Default: "deliberation-gate".
	GateOutput string `yaml:"gateOutput"`
}

// jurySize returns the effective jury size, applying the default when unset.
func (c *arbiterConfig) jurySize() int32 {
	if c.JurySize < 1 {
		return defaultJurySize
	}
	return c.JurySize
}

// jurorNode returns the effective juror node name.
func (c *arbiterConfig) jurorNode() string {
	if c.JurorNode == "" {
		return defaultJurorNode
	}
	return c.JurorNode
}

// gateOutput returns the effective gate output name.
func (c *arbiterConfig) gateOutput() string {
	if c.GateOutput == "" {
		return outputGate
	}
	return c.GateOutput
}

// ---------------------------------------------------------------------------
// Verdict Context (written for downstream Clerk consumption)
// ---------------------------------------------------------------------------

// verdictContext carries the context that produced the verdict. Written by
// the Arbiter so that the downstream Clerk knows how to draft the petition.
type verdictContext struct {
	Trigger        string   `json:"trigger"`
	SourceWorkitem string   `json:"source_workitem"` //nolint:tagliatelle
	Goal           string   `json:"goal"`
	AppliesTo      []string `json:"applies_to"`
	Tier           int32    `json:"tier"`
	Action         string   `json:"action"`
	FeedbackIDs    []string `json:"feedback_ids"` // deadlocked feedback IDs for ruling linkage
}

func main() {
	slog.Info("arbiter: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("arbiter: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("arbiter: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("arbiter: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	cfg, err := nodeconfig.Load[arbiterConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("arbiter: load config: %w", err)
	}

	return handleArbiter(ctx, client, cfg)
}

// handleArbiter contains the Arbiter's core logic, separated from handler
// boilerplate for testability.
func handleArbiter(ctx context.Context, client *flow.Client, cfg *arbiterConfig) error {
	_, _ = client.Heartbeat(ctx)

	// ── Step 1: Discover topology ────────────────────────────────────
	topology, err := client.GetFlowTopology(ctx)
	if err != nil {
		return fmt.Errorf("arbiter: get flow topology: %w", err)
	}

	exitContract := topology.GetExitContract()

	// ── Step 2: For each artefact kind, find DEADLOCKED feedback ─────
	for kind := range exitContract {
		deadlockedItems, err := findDeadlockedFeedback(ctx, client, kind)
		if err != nil {
			return err
		}
		if len(deadlockedItems) == 0 {
			continue
		}

		// ── Step 3: Gather evidence ──────────────────────────────────
		evidence, err := assembleEvidence(ctx, client, kind, deadlockedItems)
		if err != nil {
			return err
		}

		// ── Step 4: Frame question and fan out to Jurors ─────────────
		question := "Should the reviewer's feedback be upheld, or should the refiner's refusal stand?"
		allowedOutcomes := []string{"favour_refiner", "favour_reviewer"}

		outcomesJSON, err := json.Marshal(allowedOutcomes)
		if err != nil {
			return fmt.Errorf("arbiter: marshal allowed-outcomes: %w", err)
		}

		// Build fan-out tasks — one per juror.
		tasks := make([]flow.FanOutTask, cfg.jurySize())
		for i := range tasks {
			tasks[i] = flow.FanOutTask{
				TargetNode: cfg.jurorNode(),
				Artefacts: []flow.ChildArtefact{
					{ID: artefactQuestion, Content: []byte(question)},
					{ID: artefactEvidence, Content: []byte(evidence)},
					{ID: artefactOutcomes, Content: outcomesJSON},
				},
			}
		}

		// Fan out.
		if _, err := client.FanOut(ctx, tasks); err != nil {
			return fmt.Errorf("arbiter: juror fan-out: %w", err)
		}

		// ── Step 5: Await juror children ─────────────────────────────
		if _, err := client.AwaitChildren(ctx, flow.WithPollingInterval(time.Millisecond)); err != nil {
			return fmt.Errorf("arbiter: await juror children: %w", err)
		}

		// ── Step 6: Store verdict-context for downstream Clerk ───────
		vctx := verdictContext{
			Trigger:     "deadlock-resolution",
			Goal:        synthesizeGoal(kind, deadlockedItems),
			AppliesTo:   []string{kind},
			Tier:        int32(flowv1.LawTier_LAW_TIER_RULING),
			Action:      "create",
			FeedbackIDs: feedbackIDs(deadlockedItems),
		}
		vctxJSON, err := json.Marshal(vctx)
		if err != nil {
			return fmt.Errorf("arbiter: marshal verdict-context: %w", err)
		}
		if _, err := client.StoreArtefact(ctx, artefactVerdictContext, "", vctxJSON); err != nil {
			return fmt.Errorf("arbiter: store verdict-context: %w", err)
		}

		// ── Step 7: Route to Deliberation Gate ───────────────────────
		slog.Info("arbiter: fan-out complete, routing to deliberation gate",
			"artefact_kind", kind,
			"juror_count", cfg.jurySize(),
		)
		if _, err := client.RouteToOutput(ctx, cfg.gateOutput()); err != nil {
			return fmt.Errorf("arbiter: route to deliberation gate: %w", err)
		}
		return nil
	}

	// No deadlocked feedback found (shouldn't happen, but handle gracefully).
	slog.Warn("arbiter: no deadlocked feedback found, routing to deliberation gate")
	if _, err := client.RouteToOutput(ctx, cfg.gateOutput()); err != nil {
		return fmt.Errorf("arbiter: route to gate (no deadlock): %w", err)
	}
	return nil
}

// findDeadlockedFeedback scans feedback items for the given artefact kind
// and returns those in DEADLOCKED state.
func findDeadlockedFeedback(
	ctx context.Context, client *flow.Client, artefactID string,
) ([]*flowv1.FeedbackItem, error) {
	items, err := client.GetFeedback(ctx, artefactID)
	if err != nil {
		return nil, fmt.Errorf("arbiter: get feedback for %s: %w", artefactID, err)
	}

	var deadlocked []*flowv1.FeedbackItem
	for _, item := range items {
		if item.GetState() == flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED {
			deadlocked = append(deadlocked, item)
		}
	}
	return deadlocked, nil
}

// assembleEvidence builds a markdown evidence bundle for the Jurors.
func assembleEvidence(
	ctx context.Context,
	client *flow.Client,
	artefactKind string,
	deadlockedItems []*flowv1.FeedbackItem,
) (string, error) {
	var b strings.Builder

	// ── Artefact content excerpt ─────────────────────────────────────
	b.WriteString("## Artefact\n\n")
	artefactResp, err := client.GetArtefact(ctx, artefactKind)
	if err != nil {
		return "", fmt.Errorf("arbiter: get artefact %s: %w", artefactKind, err)
	}
	content := string(artefactResp.GetContent())
	if len(content) > 2000 {
		content = content[:2000] + "\n\n[... truncated ...]"
	}
	b.WriteString(content)
	b.WriteString("\n\n")

	// ── Feedback debate history ──────────────────────────────────────
	b.WriteString("## Deadlocked Feedback\n\n")
	for _, item := range deadlockedItems {
		fmt.Fprintf(&b, "### Feedback: %s\n\n", item.GetId())
		fmt.Fprintf(&b, "- **Source**: %s\n", item.GetSource())
		fmt.Fprintf(&b, "- **Severity**: %s\n", item.GetSeverity().String())
		fmt.Fprintf(&b, "- **Message**: %s\n\n", item.GetMessage())

		if j := item.GetJustification(); j != nil {
			b.WriteString("**Justification**:\n")
			if c := j.GetCitation(); c != nil {
				fmt.Fprintf(&b, "- Citation: law_ids=%v\n", c.GetCitationIds())
			}
			if n := j.GetNovelArgument(); n != nil {
				fmt.Fprintf(&b, "- Novel argument: %s\n", n.GetArgument())
			}
			b.WriteString("\n")
		}

		b.WriteString("**Debate History**:\n\n")
		for _, event := range item.GetHistory() {
			fmt.Fprintf(&b, "- [%s] %s: %s\n",
				event.GetAction(),
				event.GetActor(),
				event.GetMessage())
		}
		b.WriteString("\n")
	}

	// ── Cited laws from both sides ───────────────────────────────────
	b.WriteString("## Relevant Laws\n\n")
	laws, err := client.QueryLaws(ctx, artefactKind, "")
	if err != nil {
		return "", fmt.Errorf("arbiter: query laws for %s: %w", artefactKind, err)
	}
	for _, law := range laws {
		fmt.Fprintf(&b, "### %s (Tier %d)\n\n", law.GetId(), int32(law.GetTier()))
		fmt.Fprintf(&b, "- **Goal**: %s\n", law.GetGoal())
		for _, rep := range law.GetRepresentations() {
			fmt.Fprintf(&b, "- **%s**: %s\n", rep.GetType(), rep.GetContent())
		}
		b.WriteString("\n")
	}

	// ── Friction cost summary ────────────────────────────────────────
	b.WriteString("## Friction Summary\n\n")
	friction, err := client.QueryFriction(ctx, &flowv1.FrictionFilter{})
	if err != nil {
		return "", fmt.Errorf("arbiter: query friction: %w", err)
	}
	if len(friction) == 0 {
		b.WriteString("No friction data available.\n\n")
	} else {
		for _, agg := range friction {
			fmt.Fprintf(&b, "- law=%s node=%s events=%d magnitude=%.2f\n",
				agg.GetLawId(), agg.GetNodeId(), agg.GetEventCount(), agg.GetTotalMagnitude())
		}
		b.WriteString("\n")
	}

	return b.String(), nil
}

// synthesizeGoal builds a goal string from the dispute context.
func synthesizeGoal(
	artefactKind string,
	deadlockedItems []*flowv1.FeedbackItem,
) string {
	if len(deadlockedItems) == 0 {
		return fmt.Sprintf("Ruling on %s dispute", artefactKind)
	}
	first := deadlockedItems[0]
	return fmt.Sprintf("Ruling on %s dispute (feedback %s from %s)",
		artefactKind, first.GetId(), first.GetSource())
}

// feedbackIDs extracts the IDs from a slice of feedback items.
func feedbackIDs(items []*flowv1.FeedbackItem) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.GetId()
	}
	return ids
}
