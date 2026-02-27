// Advocate is the human-in-the-loop judiciary node of the Foundry Cycle.
//
// The Advocate handles three escalation types:
//
//  1. Hung jury from Deliberation Gate — deadlock dispute from an Arbiter
//     or Tribunal hearing. Human picks from the allowed outcomes.
//  2. Tier 3+ from Tribunal Router — a promote verdict on a Tier 2 law
//     (needs HITL ratification) or any Tier 3+ verdict.
//  3. Tier 3+ from Judiciary Gate — petition review ratification for
//     Tier 3 laws, or Tier 4-5 governance flow.
//
// The escalation type is determined from the "advocate-context" artefact,
// which is a structured JSON block written by the upstream node before
// routing to the Advocate. Fields:
//
//	type:          arbiter-hung | tribunal-hung | tribunal-promote | judiciary-ratify
//	artefact_kind: <kind>          (for arbiter-hung)
//	law_id:        <id>            (for tribunal/judiciary types)
//	feedback_ids:  [<id>, ...]     (for arbiter-hung)
//	choices:       [<choice>, ...] (allowed human choices)
//	law_goal:      <text>          (for tribunal types)
//	law_applies_to: [<kind>, ...]  (for tribunal types)
//	law_tier:      <int>           (for tribunal types)
//
// The node:
//  1. Reads the "advocate-context" artefact for escalation metadata.
//  2. Presents choices to the human via the HITL queue.
//  3. Enqueue → PauseTimer → WaitForDecision → ResumeTimer.
//  4. Stores a "human-decision" artefact recording the choice and context.
//  5. Routes the decision:
//     - All accept/actionable decisions: route to "clerk" for codification.
//     - Reject (tier 3 ratification / judiciary ratification): Complete().
//
// The Advocate no longer calls DraftLaw or LinkRuling directly.
// Those responsibilities are handled downstream by the Clerk node and
// Judiciary Gate respectively.
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
	// outputClerk is the well-known output name for routing to the Clerk node.
	outputClerk = "clerk"

	// advocateContextArtefact is the artefact ID carrying escalation metadata.
	advocateContextArtefact = "advocate-context"

	// humanDecisionArtefact is the artefact ID storing the human's decision.
	humanDecisionArtefact = "human-decision"
)

// escalationType identifies the kind of escalation the Advocate is handling.
type escalationType string

const (
	escalationArbiterHung     escalationType = "arbiter-hung"
	escalationTribunalHung    escalationType = "tribunal-hung"
	escalationTribunalPromote escalationType = "tribunal-promote"
	escalationJudiciaryRatify escalationType = "judiciary-ratify"
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
	LawTier      int32          `json:"law_tier,omitempty"`
}

// humanDecision is the JSON structure stored as the "human-decision" artefact.
// It records the full context of the HITL decision for downstream consumption
// by the Clerk node.
type humanDecision struct {
	EscalationType escalationType `json:"escalation_type"`
	Choice         string         `json:"choice"`
	ArtefactKind   string         `json:"artefact_kind,omitempty"`
	LawID          string         `json:"law_id,omitempty"`
	FeedbackIDs    []string       `json:"feedback_ids,omitempty"`
	LawGoal        string         `json:"law_goal,omitempty"`
	LawAppliesTo   []string       `json:"law_applies_to,omitempty"`
	LawTier        int32          `json:"law_tier,omitempty"`
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
// For all actionable decisions, the Advocate stores a "human-decision"
// artefact and routes to the Clerk node. Rejection decisions Complete()
// the workitem without further action.
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
	case escalationJudiciaryRatify:
		return applyJudiciaryRatifyDecision(ctx, client, advCtx, choice)
	default:
		return fmt.Errorf("advocate: unknown escalation type %q", advCtx.Type)
	}
}

// applyArbiterHungDecision handles a hung jury from a Deliberation Gate
// (Arbiter path). The human picks favour_refiner or favour_reviewer.
// Stores the decision and routes to Clerk for codification.
func applyArbiterHungDecision(
	ctx context.Context,
	client *flow.Client,
	advCtx *advocateContext,
	choice string,
) error {
	decision := humanDecision{
		EscalationType: escalationArbiterHung,
		Choice:         choice,
		ArtefactKind:   advCtx.ArtefactKind,
		FeedbackIDs:    advCtx.FeedbackIDs,
	}

	if err := storeHumanDecision(ctx, client, &decision); err != nil {
		return err
	}

	slog.Info("advocate: arbiter-hung resolved by human",
		"choice", choice,
		"artefact_kind", advCtx.ArtefactKind)

	if _, err := client.RouteToOutput(ctx, outputClerk); err != nil {
		return fmt.Errorf("advocate: route to clerk (arbiter-hung): %w", err)
	}
	return nil
}

// applyTribunalHungDecision handles a hung jury from a Deliberation Gate
// (Tribunal path). The human picks promote, retire, or demote.
// Stores the decision and routes to Clerk for codification.
func applyTribunalHungDecision(
	ctx context.Context,
	client *flow.Client,
	advCtx *advocateContext,
	choice string,
) error {
	decision := humanDecision{
		EscalationType: escalationTribunalHung,
		Choice:         choice,
		LawID:          advCtx.LawID,
		LawGoal:        advCtx.LawGoal,
		LawAppliesTo:   advCtx.LawAppliesTo,
		LawTier:        advCtx.LawTier,
	}

	if err := storeHumanDecision(ctx, client, &decision); err != nil {
		return err
	}

	slog.Info("advocate: tribunal-hung resolved by human",
		"law_id", advCtx.LawID,
		"choice", choice)

	if _, err := client.RouteToOutput(ctx, outputClerk); err != nil {
		return fmt.Errorf("advocate: route to clerk (tribunal-hung): %w", err)
	}
	return nil
}

// applyTribunalPromoteDecision handles a Tier 2→3 promotion ratification
// (from Tribunal Router). The human accepts or rejects.
// Accept: stores decision and routes to Clerk for codification.
// Reject: completes without further action.
func applyTribunalPromoteDecision(
	ctx context.Context,
	client *flow.Client,
	advCtx *advocateContext,
	choice string,
) error {
	if choice == "reject" {
		slog.Info("advocate: tier 3 ratification rejected",
			"law_id", advCtx.LawID)

		if _, err := client.Complete(ctx, ""); err != nil {
			return fmt.Errorf("advocate: complete (tribunal-promote reject): %w", err)
		}
		return nil
	}

	// Accept: store decision and route to Clerk.
	decision := humanDecision{
		EscalationType: escalationTribunalPromote,
		Choice:         choice,
		LawID:          advCtx.LawID,
		LawGoal:        advCtx.LawGoal,
		LawAppliesTo:   advCtx.LawAppliesTo,
		LawTier:        advCtx.LawTier,
	}

	if err := storeHumanDecision(ctx, client, &decision); err != nil {
		return err
	}

	slog.Info("advocate: tier 3 promotion ratified by human",
		"law_id", advCtx.LawID)

	if _, err := client.RouteToOutput(ctx, outputClerk); err != nil {
		return fmt.Errorf("advocate: route to clerk (tribunal-promote accept): %w", err)
	}
	return nil
}

// applyJudiciaryRatifyDecision handles Tier 3+ petition review ratification
// (from Judiciary Gate). The human accepts or rejects.
// Accept: stores decision and routes to Clerk.
// Reject: completes without further action.
func applyJudiciaryRatifyDecision(
	ctx context.Context,
	client *flow.Client,
	advCtx *advocateContext,
	choice string,
) error {
	if choice == "reject" {
		slog.Info("advocate: judiciary ratification rejected",
			"law_id", advCtx.LawID)

		if _, err := client.Complete(ctx, ""); err != nil {
			return fmt.Errorf("advocate: complete (judiciary-ratify reject): %w", err)
		}
		return nil
	}

	// Accept: store decision and route to Clerk.
	decision := humanDecision{
		EscalationType: escalationJudiciaryRatify,
		Choice:         choice,
		LawID:          advCtx.LawID,
		LawGoal:        advCtx.LawGoal,
		LawAppliesTo:   advCtx.LawAppliesTo,
		LawTier:        advCtx.LawTier,
	}

	if err := storeHumanDecision(ctx, client, &decision); err != nil {
		return err
	}

	slog.Info("advocate: judiciary ratification accepted by human",
		"law_id", advCtx.LawID)

	if _, err := client.RouteToOutput(ctx, outputClerk); err != nil {
		return fmt.Errorf("advocate: route to clerk (judiciary-ratify accept): %w", err)
	}
	return nil
}

// storeHumanDecision marshals and stores the human-decision artefact.
func storeHumanDecision(ctx context.Context, client *flow.Client, decision *humanDecision) error {
	data, err := json.Marshal(decision)
	if err != nil {
		return fmt.Errorf("advocate: marshal human-decision: %w", err)
	}
	if _, err := client.StoreArtefact(ctx, humanDecisionArtefact, "", data); err != nil {
		return fmt.Errorf("advocate: store human-decision: %w", err)
	}
	return nil
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
