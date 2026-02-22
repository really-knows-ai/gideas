// Advocate is the human-in-the-loop judiciary node of the Foundry Cycle.
//
// The Advocate handles three escalation types:
//
//  1. Hung jury from Arbiter — deadlock dispute. Human picks
//     "favour_refiner" or "favour_reviewer".
//  2. Hung jury from Tribunal — hearing. Human picks "promote",
//     "retire", or "demote".
//  3. Tier 3 proposal from Tribunal — Tier 2 promote verdict.
//     Human ratifies (accept) or rejects.
//
// The escalation type is determined from the "advocate-context" artefact,
// which is a structured text block written by the upstream node before
// routing to the Advocate. Format:
//
//	type: arbiter-hung | tribunal-hung | tribunal-promote
//	artefact-kind: <kind>          (for arbiter-hung)
//	law-id: <id>                   (for tribunal-hung / tribunal-promote)
//	feedback-ids: <comma-sep>      (for arbiter-hung)
//	choices: <comma-sep>           (allowed human choices)
//
// The node:
//  1. Reads the "advocate-context" artefact for escalation metadata.
//  2. Presents choices to the human via the HITL queue.
//  3. Enqueue → PauseTimer → WaitForDecision → ResumeTimer.
//  4. Applies the human decision:
//     - Arbiter hung: Clerk mint Tier 2 Ruling, LinkRuling, route to Sort.
//     - Tribunal hung: Clerk action per human choice, Complete.
//     - Tier 3 accept: Clerk mint Tier 3 Local Statute, Complete.
//     - Tier 3 reject: Complete (no action).
//
// Configuration (YAML via NODE_CONFIG_PATH):
//
//	(no configuration required — behaviour driven by advocate-context artefact)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

const (
	// outputSort is the well-known output name for routing back to the Sort gate.
	outputSort = "sort"

	// advocateContextArtefact is the artefact ID carrying escalation metadata.
	advocateContextArtefact = "advocate-context"
)

// escalationType identifies the kind of escalation the Advocate is handling.
type escalationType string

const (
	escalationArbiterHung     escalationType = "arbiter-hung"
	escalationTribunalHung    escalationType = "tribunal-hung"
	escalationTribunalPromote escalationType = "tribunal-promote"
)

// advocateContext holds the parsed escalation metadata from the
// advocate-context artefact.
type advocateContext struct {
	Type         escalationType `json:"type"`
	ArtefactKind string         `json:"artefact_kind,omitempty"`
	LawID        string         `json:"law_id,omitempty"`
	FeedbackIDs  []string       `json:"feedback_ids,omitempty"`
	Choices      []string       `json:"choices"`
	LawGoal      string         `json:"law_goal,omitempty"`
	LawAppliesTo []string       `json:"law_applies_to,omitempty"`
}

func main() {
	slog.Info("advocate: starting")

	qm, err := flow.NewQueueManager(
		flow.WithCustomRoutes(func(mux *http.ServeMux) {
			mux.HandleFunc("GET /choices", handleChoices())
		}),
	)
	if err != nil {
		slog.Error("advocate: create queue manager failed", "error", err)
		os.Exit(1)
	}

	if err := flow.Start(handler(qm), flow.WithQueueManager(qm)); err != nil {
		slog.Error("advocate: server failed", "error", err)
		os.Exit(1)
	}
}

// handler returns a flow.Handler.
func handler(qm flow.QueueManager) flow.Handler {
	return func(ctx context.Context, wctx *flowv1.WorkitemContext) error {
		_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())

		client, err := flow.NewClient()
		if err != nil {
			return fmt.Errorf("advocate: create client: %w", err)
		}
		defer func() { _ = client.Close() }()

		return handleAdvocate(ctx, client, qm, wctx)
	}
}

// handleAdvocate is the core handler logic, extracted for testability.
func handleAdvocate(
	ctx context.Context,
	client *flow.Client,
	qm flow.QueueManager,
	wctx *flowv1.WorkitemContext,
) error {
	workitemID := wctx.GetWorkitemId()
	_, _ = client.Heartbeat(ctx)

	// ── Step 1: Read escalation context ──────────────────────────────
	advCtx, err := readAdvocateContext(ctx, client)
	if err != nil {
		return err
	}

	slog.Info("advocate: handling escalation",
		"workitem_id", workitemID,
		"type", string(advCtx.Type),
		"choices", advCtx.Choices)

	// Validate choices.
	if len(advCtx.Choices) == 0 {
		return fmt.Errorf("advocate: no choices in advocate-context")
	}
	validChoices := make(map[string]bool, len(advCtx.Choices))
	for _, c := range advCtx.Choices {
		validChoices[c] = true
	}

	// ── Step 2: Enqueue → Pause → Wait → Resume ─────────────────────
	if err := qm.Enqueue(ctx, workitemID); err != nil {
		return fmt.Errorf("advocate: enqueue: %w", err)
	}
	if err := client.PauseTimer(ctx); err != nil {
		return fmt.Errorf("advocate: pause timer: %w", err)
	}

	slog.Info("advocate: awaiting human decision", "workitem_id", workitemID)
	choice, err := qm.WaitForDecision(ctx, workitemID)
	if err != nil {
		return fmt.Errorf("advocate: wait for decision: %w", err)
	}
	if choice == "" {
		return fmt.Errorf("advocate: received empty choice (queue manager shut down before decision)")
	}
	if !validChoices[choice] {
		return fmt.Errorf("advocate: invalid choice %q: must be one of %v", choice, advCtx.Choices)
	}

	slog.Info("advocate: human decision received",
		"workitem_id", workitemID,
		"choice", choice)

	if err := client.ResumeTimer(ctx); err != nil {
		return fmt.Errorf("advocate: resume timer: %w", err)
	}

	// ── Step 3: Apply decision based on escalation type ──────────────
	return applyDecision(ctx, client, advCtx, choice)
}

// readAdvocateContext reads and parses the advocate-context artefact.
func readAdvocateContext(ctx context.Context, client *flow.Client) (*advocateContext, error) {
	resp, err := client.GetArtefact(ctx, advocateContextArtefact)
	if err != nil {
		return nil, fmt.Errorf("advocate: get advocate-context artefact: %w", err)
	}
	content := resp.GetContent()
	if len(content) == 0 {
		return nil, fmt.Errorf("advocate: advocate-context artefact is empty")
	}

	var advCtx advocateContext
	if err := json.Unmarshal(content, &advCtx); err != nil {
		return nil, fmt.Errorf("advocate: parse advocate-context: %w", err)
	}

	if advCtx.Type == "" {
		return nil, fmt.Errorf("advocate: advocate-context missing type field")
	}

	return &advCtx, nil
}

// applyDecision executes the human's decision based on escalation type.
func applyDecision(
	ctx context.Context,
	client *flow.Client,
	advCtx *advocateContext,
	choice string,
) error {
	switch advCtx.Type {
	case escalationArbiterHung:
		return applyArbiterHungDecision(ctx, client, advCtx, choice)
	case escalationTribunalHung:
		return applyTribunalHungDecision(ctx, client, advCtx, choice)
	case escalationTribunalPromote:
		return applyTribunalPromoteDecision(ctx, client, advCtx, choice)
	default:
		return fmt.Errorf("advocate: unknown escalation type %q", advCtx.Type)
	}
}

// applyArbiterHungDecision handles a hung jury from the Arbiter.
// The human picks favour_refiner or favour_reviewer.
func applyArbiterHungDecision(
	ctx context.Context,
	client *flow.Client,
	advCtx *advocateContext,
	choice string,
) error {
	// Build a synthetic verdict from the human decision.
	verdict := &flowv1.DeliberateResponse{
		Outcome: choice,
		Justifications: []*flowv1.JurorJustification{
			{JurorId: "human-advocate", Outcome: choice, Reasoning: "Human decision via HITL Advocate"},
		},
		RoundsUsed: 0, // human decision, not jury rounds
	}

	goal := fmt.Sprintf("Human ruling on %s dispute: %s", advCtx.ArtefactKind, choice)
	appliesTo := []string{advCtx.ArtefactKind}

	lawResp, err := client.DraftLaw(ctx, verdict, goal, int32(flowv1.LawTier_LAW_TIER_RULING), appliesTo)
	if err != nil {
		return fmt.Errorf("advocate: draft law (arbiter-hung): %w", err)
	}

	slog.Info("advocate: ruling minted from human decision",
		"law_id", lawResp.GetLawId(),
		"choice", choice)

	// Link ruling to each deadlocked feedback item.
	for _, fbID := range advCtx.FeedbackIDs {
		if _, err := client.LinkRuling(ctx, fbID, lawResp.GetLawId()); err != nil {
			return fmt.Errorf("advocate: link ruling to feedback %s: %w", fbID, err)
		}
	}

	// Route back to Sort for re-evaluation.
	if _, err := client.RouteToOutput(ctx, outputSort); err != nil {
		return fmt.Errorf("advocate: route to sort: %w", err)
	}
	return nil
}

// applyTribunalHungDecision handles a hung jury from the Tribunal.
// The human picks promote, retire, or demote.
func applyTribunalHungDecision(
	ctx context.Context,
	client *flow.Client,
	advCtx *advocateContext,
	choice string,
) error {
	verdict := &flowv1.DeliberateResponse{
		Outcome: choice,
		Justifications: []*flowv1.JurorJustification{
			{JurorId: "human-advocate", Outcome: choice, Reasoning: "Human decision via HITL Advocate"},
		},
	}

	tier := tierForTribunalChoice(choice)
	_, err := client.DraftLaw(ctx, verdict, advCtx.LawGoal, tier, advCtx.LawAppliesTo)
	if err != nil {
		return fmt.Errorf("advocate: draft law (tribunal-hung, choice=%s): %w", choice, err)
	}

	slog.Info("advocate: tribunal hung resolved by human",
		"law_id", advCtx.LawID,
		"choice", choice)

	if _, err := client.Complete(ctx, ""); err != nil {
		return fmt.Errorf("advocate: complete (tribunal-hung): %w", err)
	}
	return nil
}

// applyTribunalPromoteDecision handles a Tier 2→3 promotion ratification.
// The human accepts or rejects.
func applyTribunalPromoteDecision(
	ctx context.Context,
	client *flow.Client,
	advCtx *advocateContext,
	choice string,
) error {
	if choice == "accept" {
		verdict := &flowv1.DeliberateResponse{
			Outcome: "promote",
			Justifications: []*flowv1.JurorJustification{
				{JurorId: "human-advocate", Outcome: "promote", Reasoning: "Human ratification of Tier 3 promotion"},
			},
		}

		tier := int32(flowv1.LawTier_LAW_TIER_LOCAL_STATUTE)
		lawResp, err := client.DraftLaw(ctx, verdict, advCtx.LawGoal, tier, advCtx.LawAppliesTo)
		if err != nil {
			return fmt.Errorf("advocate: draft law (tier 3 ratification): %w", err)
		}

		slog.Info("advocate: tier 3 local statute ratified",
			"law_id", lawResp.GetLawId())
	} else {
		slog.Info("advocate: tier 3 ratification rejected",
			"law_id", advCtx.LawID)
	}

	if _, err := client.Complete(ctx, ""); err != nil {
		return fmt.Errorf("advocate: complete (tribunal-promote): %w", err)
	}
	return nil
}

// tierForTribunalChoice maps a tribunal hearing choice to the DraftLaw tier.
func tierForTribunalChoice(choice string) int32 {
	switch choice {
	case "promote":
		return int32(flowv1.LawTier_LAW_TIER_LOCAL_STATUTE)
	case "demote":
		return int32(flowv1.LawTier_LAW_TIER_FINDING)
	default: // "retire" or anything else — keep current tier
		return int32(flowv1.LawTier_LAW_TIER_RULING)
	}
}

// handleChoices returns the choices endpoint. The actual choices are
// per-workitem and come from the advocate-context artefact, so this
// endpoint returns a static message directing callers to read the artefact.
func handleChoices() http.HandlerFunc {
	type choicesInfo struct {
		Message string `json:"message"`
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(choicesInfo{
			Message: "Choices are per-workitem. Read the advocate-context artefact for available choices.",
		})
	}
}
