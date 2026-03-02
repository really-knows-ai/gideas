// Judiciary Gate mirrors Sort for the judiciary inner cycle.
//
// After the Tribunal reviews a petition and the Deliberation Gate reaches
// consensus, the Judiciary Gate checks feedback resolution on the petition
// artefact, determines the law tier, and routes accordingly:
//
//   - Approved, all feedback resolved, Tier 1-2: apply the petition via
//     Librarian (WriteLaw / RetireLaw), store an approval stamp, Complete().
//   - Rejected or unresolved feedback: route to "clerk" for revision.
//   - Approved, Tier 3: route to "advocate" (HITL ratification required).
//   - Tier 4-5: route to "advocate" (Governance Flow).
//
// The gate reads:
//
//   - "deliberation-result" -- JSON from Deliberation Gate (outcome, hung)
//   - "petition" -- JSON petition drafted by Clerk (changes[], context)
//   - "law-reference" -- plain-text law ID (for tier lookup via Librarian)
//   - Feedback on the "petition" artefact (from Tribunal review)
//
// Configuration (YAML via NODE_CONFIG_PATH):
//
//	(none required -- all decisions derived from artefact state and tier)
//
// Well-known outputs:
//
//   - "clerk"    -- revision needed (unresolved feedback or rejected)
//   - "advocate" -- HITL ratification (Tier 3) or Governance Flow (Tier 4-5)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// Well-known constants
// ---------------------------------------------------------------------------

const (
	// Output names.
	outputClerk    = "clerk"
	outputAdvocate = "advocate"

	// Artefact IDs.
	artefactDeliberationResult = "deliberation-result"
	artefactPetition           = "petition"
	artefactLawReference       = "law-reference"
	artefactApprovalStamp      = "approval-stamp"

	// Feedback is attached to the petition artefact.
	feedbackArtefactID = "petition"

	// Tier thresholds.
	tierHITLRatification = 3 // Tier 3: HITL ratification via Advocate
	tierGovernanceFlow   = 4 // Tier 4-5: Governance Flow via Advocate

	// Outcome values from Deliberation Gate.
	outcomeApprove = "approve"
)

// ---------------------------------------------------------------------------
// Deliberation Result (read from artefact)
// ---------------------------------------------------------------------------

type deliberationResult struct {
	Outcome        string               `json:"outcome"`
	Justifications []jurorJustification `json:"justifications"`
	RoundsUsed     int32                `json:"rounds_used"`
	Hung           bool                 `json:"hung"`
}

type jurorJustification struct {
	JurorID   string `json:"juror_id"`
	Outcome   string `json:"outcome"`
	Reasoning string `json:"reasoning"`
}

// ---------------------------------------------------------------------------
// Petition (read from artefact, produced by Clerk)
// ---------------------------------------------------------------------------

type petition struct {
	Petition petitionBody `json:"petition"`
}

type petitionBody struct {
	Context            petitionContext  `json:"context"`
	Changes            []petitionChange `json:"changes"`
	ProseJustification string           `json:"prose_justification"`
}

type petitionContext struct {
	Trigger        string `json:"trigger"`
	SourceWorkitem string `json:"source_workitem"` //nolint:tagliatelle // JSON convention
	Verdict        string `json:"verdict"`
	Justification  string `json:"justification"`
}

type petitionChange struct {
	Action          string        `json:"action"`
	Tier            int32         `json:"tier,omitempty"`
	Goal            string        `json:"goal,omitempty"`
	AppliesTo       []string      `json:"applies_to,omitempty"`
	LawID           string        `json:"law_id,omitempty"`
	Justification   string        `json:"justification,omitempty"`
	Representations []petitionRep `json:"representations,omitempty"`
}

type petitionRep struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// ---------------------------------------------------------------------------
// Approval Stamp (stored as artefact on application)
// ---------------------------------------------------------------------------

type approvalStamp struct {
	Applied    bool             `json:"applied"`
	LawResults []lawApplyResult `json:"law_results"`
}

type lawApplyResult struct {
	Action      string `json:"action"`
	LawID       string `json:"law_id"`
	VersionHash string `json:"version_hash,omitempty"`
}

// ---------------------------------------------------------------------------
// Entry Point
// ---------------------------------------------------------------------------

func main() {
	slog.Info("judiciary-gate: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("judiciary-gate: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("judiciary-gate: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("judiciary-gate: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	return handleJudiciaryGate(ctx, client)
}

// ---------------------------------------------------------------------------
// Core Logic
// ---------------------------------------------------------------------------

func handleJudiciaryGate(ctx context.Context, client *flow.Client) error {
	_, _ = client.Heartbeat(ctx)

	// -- Step 1: Read deliberation result ---------------------------------
	resultResp, err := client.GetArtefact(ctx, artefactDeliberationResult)
	if err != nil {
		return fmt.Errorf("judiciary-gate: get deliberation-result: %w", err)
	}

	var result deliberationResult
	if err := json.Unmarshal(resultResp.GetContent(), &result); err != nil {
		return fmt.Errorf("judiciary-gate: parse deliberation-result: %w", err)
	}

	slog.Info("judiciary-gate: read deliberation result",
		"outcome", result.Outcome,
		"hung", result.Hung,
	)

	// -- Step 2: Read petition artefact -----------------------------------
	petResp, err := client.GetArtefact(ctx, artefactPetition)
	if err != nil {
		return fmt.Errorf("judiciary-gate: get petition: %w", err)
	}

	var pet petition
	if err := json.Unmarshal(petResp.GetContent(), &pet); err != nil {
		return fmt.Errorf("judiciary-gate: parse petition: %w", err)
	}

	// -- Step 3: Determine law tier from law-reference --------------------
	tier, err := resolveTier(ctx, client)
	if err != nil {
		return err
	}

	slog.Info("judiciary-gate: resolved tier", "tier", tier)

	// -- Step 4: Check feedback resolution on petition --------------------
	allResolved := checkFeedbackResolved(ctx, client)

	// -- Step 5: Route based on outcome, feedback, and tier ---------------
	return route(ctx, client, &result, &pet, tier, allResolved)
}

// ---------------------------------------------------------------------------
// Tier Resolution
// ---------------------------------------------------------------------------

// resolveTier reads the law-reference artefact and fetches the law's tier.
func resolveTier(ctx context.Context, client *flow.Client) (int32, error) {
	lawRefResp, err := client.GetArtefact(ctx, artefactLawReference)
	if err != nil {
		return 0, fmt.Errorf("judiciary-gate: get law-reference: %w", err)
	}

	lawID := strings.TrimSpace(string(lawRefResp.GetContent()))
	if lawID == "" {
		return 0, fmt.Errorf("judiciary-gate: law-reference artefact is empty")
	}

	law, err := client.GetLaw(ctx, lawID)
	if err != nil {
		return 0, fmt.Errorf("judiciary-gate: get law %q: %w", lawID, err)
	}

	return int32(law.GetTier()), nil
}

// ---------------------------------------------------------------------------
// Feedback Resolution Check
// ---------------------------------------------------------------------------

// checkFeedbackResolved returns true when all feedback items on the petition
// artefact are in the RESOLVED state (or there is no feedback at all).
// Feedback read failures are treated as "no feedback" (graceful degradation).
func checkFeedbackResolved(ctx context.Context, client *flow.Client) bool {
	items, err := client.GetFeedback(ctx, feedbackArtefactID)
	if err != nil {
		// No feedback method available or failure — treat as "no feedback".
		slog.Warn("judiciary-gate: could not read feedback, treating as resolved",
			"error", err,
		)
		return true
	}

	for _, item := range items {
		if item.GetState() != flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Routing
// ---------------------------------------------------------------------------

func route(
	ctx context.Context,
	client *flow.Client,
	result *deliberationResult,
	pet *petition,
	tier int32,
	allFeedbackResolved bool,
) error {
	// Rule 1: Tier 4-5 always goes to Advocate (Governance Flow).
	if tier >= tierGovernanceFlow {
		slog.Info("judiciary-gate: routing to advocate (governance flow)",
			"tier", tier,
		)
		if _, err := client.RouteToOutput(ctx, outputAdvocate); err != nil {
			return fmt.Errorf("judiciary-gate: route to advocate: %w", err)
		}
		return nil
	}

	// Rule 2: Tier 3 approved goes to Advocate (HITL ratification).
	if tier >= tierHITLRatification && result.Outcome == outcomeApprove {
		slog.Info("judiciary-gate: routing to advocate (HITL ratification)",
			"tier", tier,
		)
		if _, err := client.RouteToOutput(ctx, outputAdvocate); err != nil {
			return fmt.Errorf("judiciary-gate: route to advocate: %w", err)
		}
		return nil
	}

	// Rule 3: Not approved or unresolved feedback -> Clerk for revision.
	if result.Outcome != outcomeApprove || !allFeedbackResolved {
		reason := "rejected"
		if result.Outcome == outcomeApprove {
			reason = "unresolved feedback"
		}
		slog.Info("judiciary-gate: routing to clerk (revision needed)",
			"reason", reason,
		)
		if _, err := client.RouteToOutput(ctx, outputClerk); err != nil {
			return fmt.Errorf("judiciary-gate: route to clerk: %w", err)
		}
		return nil
	}

	// Rule 4: Approved, all feedback resolved, Tier 1-2 -> apply via
	// Librarian, store approval stamp, Complete().
	slog.Info("judiciary-gate: applying petition via librarian",
		"tier", tier,
		"changes", len(pet.Petition.Changes),
	)

	stamp, err := applyPetition(ctx, client, pet)
	if err != nil {
		return err
	}

	stampJSON, err := json.Marshal(stamp)
	if err != nil {
		return fmt.Errorf("judiciary-gate: marshal approval stamp: %w", err)
	}

	if _, err := client.StoreArtefact(ctx, artefactApprovalStamp, "", stampJSON); err != nil {
		return fmt.Errorf("judiciary-gate: store approval stamp: %w", err)
	}

	if _, err := client.Complete(ctx); err != nil {
		return fmt.Errorf("judiciary-gate: complete: %w", err)
	}

	slog.Info("judiciary-gate: petition applied, workitem completed")
	return nil
}

// ---------------------------------------------------------------------------
// Petition Application
// ---------------------------------------------------------------------------

// applyPetition processes each change in the petition and applies it via
// the Librarian. Returns an approval stamp recording the results.
func applyPetition(ctx context.Context, client *flow.Client, pet *petition) (*approvalStamp, error) {
	stamp := &approvalStamp{Applied: true}

	for _, change := range pet.Petition.Changes {
		result, err := applyChange(ctx, client, &change)
		if err != nil {
			return nil, err
		}
		stamp.LawResults = append(stamp.LawResults, *result)
	}

	return stamp, nil
}

// applyChange applies a single petition change via the Librarian.
func applyChange(ctx context.Context, client *flow.Client, change *petitionChange) (*lawApplyResult, error) {
	switch change.Action {
	case "create":
		return applyCreate(ctx, client, change)
	case "retire":
		return applyRetire(ctx, client, change)
	case "demote":
		return applyDemote(ctx, client, change)
	default:
		return nil, fmt.Errorf("judiciary-gate: unknown petition action %q", change.Action)
	}
}

// applyCreate writes a new law to the Librarian.
func applyCreate(ctx context.Context, client *flow.Client, change *petitionChange) (*lawApplyResult, error) {
	reps := make([]*flowv1.Representation, len(change.Representations))
	for i, r := range change.Representations {
		reps[i] = &flowv1.Representation{
			Type:    r.Type,
			Content: r.Content,
		}
	}

	law := &flowv1.Law{
		Goal:            change.Goal,
		Representations: reps,
		Tier:            flowv1.LawTier(change.Tier),
		AppliesTo:       change.AppliesTo,
	}

	resp, err := client.Librarian.WriteLaw(ctx, &flowv1.WriteLawRequest{Law: law})
	if err != nil {
		return nil, fmt.Errorf("judiciary-gate: write law: %w", err)
	}

	return &lawApplyResult{
		Action:      "create",
		LawID:       resp.GetLawId(),
		VersionHash: resp.GetVersionHash(),
	}, nil
}

// applyRetire retires an existing law via the Librarian.
func applyRetire(ctx context.Context, client *flow.Client, change *petitionChange) (*lawApplyResult, error) {
	_, err := client.Librarian.RetireLaw(ctx, &flowv1.RetireLawRequest{LawId: change.LawID})
	if err != nil {
		return nil, fmt.Errorf("judiciary-gate: retire law %q: %w", change.LawID, err)
	}

	return &lawApplyResult{
		Action: "retire",
		LawID:  change.LawID,
	}, nil
}

// applyDemote writes a law update with a lower tier to the Librarian.
func applyDemote(ctx context.Context, client *flow.Client, change *petitionChange) (*lawApplyResult, error) {
	// Demote: fetch the existing law, update its tier, write it back.
	existing, err := client.GetLaw(ctx, change.LawID)
	if err != nil {
		return nil, fmt.Errorf("judiciary-gate: get law %q for demote: %w", change.LawID, err)
	}

	existing.Tier = flowv1.LawTier(change.Tier)

	resp, err := client.Librarian.WriteLaw(ctx, &flowv1.WriteLawRequest{Law: existing})
	if err != nil {
		return nil, fmt.Errorf("judiciary-gate: demote law %q: %w", change.LawID, err)
	}

	return &lawApplyResult{
		Action:      "demote",
		LawID:       resp.GetLawId(),
		VersionHash: resp.GetVersionHash(),
	}, nil
}
