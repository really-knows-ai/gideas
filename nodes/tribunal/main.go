// Tribunal is the hearing conductor and petition reviewer of the Foundry
// Judiciary.
//
// The Tribunal operates in two modes, detected by the artefacts present on
// the incoming Workitem:
//
// ## Hearing Mode (law-reference artefact present, no petition artefact)
//
// Convenes a hearing on an individual law to decide its lifecycle
// progression. Triggered by a Governance Flow workitem carrying a
// "law-reference" artefact identifying the law under review.
//
//  1. Read the "law-reference" artefact to get the law ID under review.
//  2. Retrieve the full law object from the Librarian.
//  3. Query friction data from the Friction Ledger.
//  4. Query related laws from the Librarian for context.
//  5. Assemble evidence: law goal, representations, friction summary,
//     related laws.
//  6. Frame the question based on law tier:
//     - Tier 1 (Finding):  "Should this Finding be promoted or retired?"
//     - Tier 2 (Ruling):   "Should this Ruling be promoted, retired, or demoted?"
//  7. Fan out to Juror nodes with question, evidence, and allowed outcomes.
//  8. Await juror children.
//  9. Store verdict-context artefact for downstream Clerk consumption.
//  10. Route to Deliberation Gate.
//
// ## Review Mode (petition artefact present)
//
// Reviews a petition drafted by the Clerk. The Tribunal reads the petition,
// assembles review evidence (petition content + verdict context), and fans
// out to Juror nodes for an approve/reject vote.
//
//  1. Read the "petition" artefact.
//  2. Read the "verdict-context" artefact for context.
//  3. Frame the review question: "Should this petition be approved or rejected?"
//  4. Fan out to Juror nodes with question, evidence, and allowed outcomes.
//  5. Await juror children.
//  6. Route to Deliberation Gate.
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

	// Input artefacts that determine mode.
	artefactLawReference   = "law-reference"
	artefactPetition       = "petition"
	artefactVerdictContext = "verdict-context"

	// Outcome constants used in hearing-mode question framing.
	outcomePromote = "promote"
	outcomeRetire  = "retire"
	outcomeDemote  = "demote"

	// Review-mode outcomes.
	outcomeApprove = "approve"
	outcomeReject  = "reject"
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

// tribunalConfig holds the Tribunal's runtime configuration.
type tribunalConfig struct {
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
func (c *tribunalConfig) jurySize() int32 {
	if c.JurySize < 1 {
		return defaultJurySize
	}
	return c.JurySize
}

// jurorNode returns the effective juror node name.
func (c *tribunalConfig) jurorNode() string {
	if c.JurorNode == "" {
		return defaultJurorNode
	}
	return c.JurorNode
}

// gateOutput returns the effective gate output name.
func (c *tribunalConfig) gateOutput() string {
	if c.GateOutput == "" {
		return outputGate
	}
	return c.GateOutput
}

// ---------------------------------------------------------------------------
// Verdict Context (written for downstream Clerk consumption)
// ---------------------------------------------------------------------------

// verdictContext carries the context that produced the verdict. Written by
// the Tribunal in hearing mode so that the downstream Clerk knows how to
// draft the petition.
type verdictContext struct {
	Trigger   string   `json:"trigger"`
	Goal      string   `json:"goal"`
	AppliesTo []string `json:"applies_to"`
	Tier      int32    `json:"tier"`
	LawID     string   `json:"law_id"` // the law being reviewed
	Action    string   `json:"action"` // "create", "retire", "demote"
}

func main() {
	slog.Info("tribunal: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("tribunal: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("tribunal: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("tribunal: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	cfg, err := nodeconfig.Load[tribunalConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("tribunal: load config: %w", err)
	}

	return handleTribunal(ctx, client, cfg)
}

// handleTribunal contains the Tribunal's core logic, separated from handler
// boilerplate for testability. It detects the mode by checking which
// artefacts are present and dispatches to the appropriate handler.
func handleTribunal(ctx context.Context, client *flow.Client, cfg *tribunalConfig) error {
	_, _ = client.Heartbeat(ctx)

	// Mode detection: if "petition" artefact is present, we are in review
	// mode. Otherwise, look for "law-reference" for hearing mode.
	_, petitionErr := client.GetArtefact(ctx, artefactPetition)
	if petitionErr == nil {
		return handleReviewMode(ctx, client, cfg)
	}

	return handleHearingMode(ctx, client, cfg)
}

// ---------------------------------------------------------------------------
// Hearing Mode
// ---------------------------------------------------------------------------

func handleHearingMode(ctx context.Context, client *flow.Client, cfg *tribunalConfig) error {
	// ── Step 1: Read the law-reference artefact ──────────────────────
	lawRef, err := client.GetArtefact(ctx, artefactLawReference)
	if err != nil {
		return fmt.Errorf("tribunal: get law-reference artefact: %w", err)
	}
	lawID := strings.TrimSpace(string(lawRef.GetContent()))
	if lawID == "" {
		return fmt.Errorf("tribunal: law-reference artefact is empty")
	}

	slog.Info("tribunal: hearing mode, reviewing law", "law_id", lawID)

	// ── Step 2: Get the full law object ──────────────────────────────
	law, err := client.GetLaw(ctx, lawID)
	if err != nil {
		return fmt.Errorf("tribunal: get law %s: %w", lawID, err)
	}

	// ── Step 3: Query friction data ──────────────────────────────────
	friction, err := client.QueryFriction(ctx, &flowv1.FrictionFilter{
		LawId: lawID,
	})
	if err != nil {
		return fmt.Errorf("tribunal: query friction for %s: %w", lawID, err)
	}

	// ── Step 4: Query related laws for context ───────────────────────
	var relatedLaws []*flowv1.Law
	if len(law.GetAppliesTo()) > 0 {
		relatedLaws, err = client.QueryLaws(ctx, law.GetAppliesTo()[0], "")
		if err != nil {
			return fmt.Errorf("tribunal: query related laws: %w", err)
		}
	}

	// ── Step 5: Assemble evidence ────────────────────────────────────
	evidenceText := assembleHearingEvidence(law, friction, relatedLaws)

	// ── Step 6: Frame question and determine allowed outcomes ────────
	tier := law.GetTier()
	question, allowedOutcomes := frameHearingQuestion(tier)

	// ── Step 7: Fan out to Juror nodes ───────────────────────────────
	outcomesJSON, err := json.Marshal(allowedOutcomes)
	if err != nil {
		return fmt.Errorf("tribunal: marshal allowed-outcomes: %w", err)
	}

	tasks := make([]flow.FanOutTask, cfg.jurySize())
	for i := range tasks {
		tasks[i] = flow.FanOutTask{
			TargetNode: cfg.jurorNode(),
			Artefacts: []flow.ChildArtefact{
				{ID: artefactQuestion, Content: []byte(question)},
				{ID: artefactEvidence, Content: []byte(evidenceText)},
				{ID: artefactOutcomes, Content: outcomesJSON},
			},
		}
	}

	if _, err := client.FanOut(ctx, tasks); err != nil {
		return fmt.Errorf("tribunal: juror fan-out: %w", err)
	}

	// ── Step 8: Await juror children ─────────────────────────────────
	if _, err := client.AwaitChildren(ctx, flow.WithPollingInterval(time.Millisecond)); err != nil {
		return fmt.Errorf("tribunal: await juror children: %w", err)
	}

	// ── Step 9: Store verdict-context for downstream Clerk ───────────
	vctx := verdictContext{
		Trigger:   "hearing",
		Goal:      law.GetGoal(),
		AppliesTo: law.GetAppliesTo(),
		Tier:      int32(tier),
		LawID:     lawID,
		Action:    hearingAction(tier),
	}
	vctxJSON, err := json.Marshal(vctx)
	if err != nil {
		return fmt.Errorf("tribunal: marshal verdict-context: %w", err)
	}
	if _, err := client.StoreArtefact(ctx, artefactVerdictContext, "", vctxJSON); err != nil {
		return fmt.Errorf("tribunal: store verdict-context: %w", err)
	}

	// ── Step 10: Route to Deliberation Gate ──────────────────────────
	slog.Info("tribunal: hearing fan-out complete, routing to deliberation gate",
		"law_id", lawID,
		"juror_count", cfg.jurySize(),
	)
	if _, err := client.RouteToOutput(ctx, cfg.gateOutput()); err != nil {
		return fmt.Errorf("tribunal: route to deliberation gate: %w", err)
	}
	return nil
}

// hearingAction returns the default action for a hearing. The actual
// outcome will be determined by the jury verdict (which happens downstream
// in the Deliberation Gate), but we provide a default for the
// verdict-context that the Clerk uses to understand the origin.
//
// Currently all hearing tiers default to "create". If future tiers need
// different defaults, extend this function.
func hearingAction(_ flowv1.LawTier) string {
	return "create"
}

// frameHearingQuestion returns the question and allowed outcomes for the
// given tier.
func frameHearingQuestion(tier flowv1.LawTier) (string, []string) {
	switch tier {
	case flowv1.LawTier_LAW_TIER_FINDING:
		return "Should this Finding be promoted to a Ruling, or retired?",
			[]string{outcomePromote, outcomeRetire}
	case flowv1.LawTier_LAW_TIER_RULING:
		return "Should this Ruling be promoted to a Local Statute, retired, or demoted to a Finding?",
			[]string{outcomePromote, outcomeRetire, outcomeDemote}
	default:
		return fmt.Sprintf("Should this Tier %d law be promoted, retired, or demoted?", int32(tier)),
			[]string{outcomePromote, outcomeRetire, outcomeDemote}
	}
}

// assembleHearingEvidence builds a markdown evidence bundle for the Jurors.
func assembleHearingEvidence(
	law *flowv1.Law,
	friction []*flowv1.FrictionAggregate,
	relatedLaws []*flowv1.Law,
) string {
	var b strings.Builder

	// ── Law under review ─────────────────────────────────────────────
	b.WriteString("## Law Under Review\n\n")
	fmt.Fprintf(&b, "- **ID**: %s\n", law.GetId())
	fmt.Fprintf(&b, "- **Goal**: %s\n", law.GetGoal())
	fmt.Fprintf(&b, "- **Tier**: %d (%s)\n", int32(law.GetTier()), law.GetTier().String())
	fmt.Fprintf(&b, "- **Applies To**: %s\n\n", strings.Join(law.GetAppliesTo(), ", "))

	b.WriteString("### Representations\n\n")
	for _, rep := range law.GetRepresentations() {
		fmt.Fprintf(&b, "**%s**:\n%s\n\n", rep.GetType(), rep.GetContent())
	}

	// ── Friction data ────────────────────────────────────────────────
	b.WriteString("## Friction Summary\n\n")
	if len(friction) == 0 {
		b.WriteString("No friction data recorded for this law.\n\n")
	} else {
		var totalMagnitude float64
		var totalEvents int32
		for _, agg := range friction {
			fmt.Fprintf(&b, "- node=%s events=%d magnitude=%.2f\n",
				agg.GetNodeId(), agg.GetEventCount(), agg.GetTotalMagnitude())
			totalMagnitude += agg.GetTotalMagnitude()
			totalEvents += agg.GetEventCount()
		}
		fmt.Fprintf(&b, "\n**Total**: %d events, %.2f cumulative magnitude\n\n",
			totalEvents, totalMagnitude)
	}

	// ── Related laws ─────────────────────────────────────────────────
	b.WriteString("## Related Laws\n\n")
	if len(relatedLaws) == 0 {
		b.WriteString("No related laws found.\n\n")
	} else {
		for _, rl := range relatedLaws {
			if rl.GetId() == law.GetId() {
				continue // skip self
			}
			fmt.Fprintf(&b, "- **%s** (Tier %d): %s\n",
				rl.GetId(), int32(rl.GetTier()), rl.GetGoal())
		}
		b.WriteString("\n")
	}

	return b.String()
}

// ---------------------------------------------------------------------------
// Review Mode
// ---------------------------------------------------------------------------

func handleReviewMode(ctx context.Context, client *flow.Client, cfg *tribunalConfig) error {
	// ── Step 1: Read the petition artefact ───────────────────────────
	petResp, err := client.GetArtefact(ctx, artefactPetition)
	if err != nil {
		return fmt.Errorf("tribunal: get petition artefact: %w", err)
	}
	petitionContent := string(petResp.GetContent())

	slog.Info("tribunal: review mode, reviewing petition")

	// ── Step 2: Read verdict-context for context ────────────────────
	vctxResp, err := client.GetArtefact(ctx, artefactVerdictContext)
	if err != nil {
		return fmt.Errorf("tribunal: get verdict-context artefact: %w", err)
	}
	vctxContent := string(vctxResp.GetContent())

	// ── Step 3: Frame review question ───────────────────────────────
	question := "Should this petition be approved or rejected? " +
		"Review the proposed law changes, justification, and formal representations " +
		"for correctness and completeness."
	allowedOutcomes := []string{outcomeApprove, outcomeReject}

	evidenceText := assembleReviewEvidence(petitionContent, vctxContent)

	// ── Step 4: Fan out to Juror nodes ──────────────────────────────
	outcomesJSON, err := json.Marshal(allowedOutcomes)
	if err != nil {
		return fmt.Errorf("tribunal: marshal allowed-outcomes: %w", err)
	}

	tasks := make([]flow.FanOutTask, cfg.jurySize())
	for i := range tasks {
		tasks[i] = flow.FanOutTask{
			TargetNode: cfg.jurorNode(),
			Artefacts: []flow.ChildArtefact{
				{ID: artefactQuestion, Content: []byte(question)},
				{ID: artefactEvidence, Content: []byte(evidenceText)},
				{ID: artefactOutcomes, Content: outcomesJSON},
			},
		}
	}

	if _, err := client.FanOut(ctx, tasks); err != nil {
		return fmt.Errorf("tribunal: juror fan-out (review): %w", err)
	}

	// ── Step 5: Await juror children ────────────────────────────────
	if _, err := client.AwaitChildren(ctx, flow.WithPollingInterval(time.Millisecond)); err != nil {
		return fmt.Errorf("tribunal: await juror children (review): %w", err)
	}

	// ── Step 6: Route to Deliberation Gate ──────────────────────────
	slog.Info("tribunal: review fan-out complete, routing to deliberation gate",
		"juror_count", cfg.jurySize(),
	)
	if _, err := client.RouteToOutput(ctx, cfg.gateOutput()); err != nil {
		return fmt.Errorf("tribunal: route to deliberation gate (review): %w", err)
	}
	return nil
}

// assembleReviewEvidence builds a markdown evidence bundle for petition
// review by the Jurors.
func assembleReviewEvidence(petitionContent, verdictContextContent string) string {
	var b strings.Builder

	b.WriteString("## Petition Under Review\n\n")
	b.WriteString(petitionContent)
	b.WriteString("\n\n")

	b.WriteString("## Verdict Context\n\n")
	b.WriteString(verdictContextContent)
	b.WriteString("\n\n")

	return b.String()
}
