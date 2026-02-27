// Tribunal Router handles post-hearing routing in the Foundry Judiciary.
//
// After the Deliberation Gate reaches consensus on a Tribunal hearing, the
// Tribunal Router reads the deliberation result and law-reference artefacts,
// fetches the law's tier from the Librarian, and routes based on tier and
// outcome:
//
//   - Tier 1-2 verdict (no tier promotion) -> "clerk" (draft petition)
//   - Tier 2 promote to Tier 3            -> "advocate" (HITL ratification)
//   - Tier 3+                             -> "advocate" (petition/appeal)
//
// The Tribunal Router does not modify artefacts — it is a pure routing node
// that reads state and makes a single routing decision.
//
// Configuration (YAML via NODE_CONFIG_PATH):
//
//	(none required — all routing decisions are derived from artefact state)
//
// Artefact contract:
//
//   - Reads "deliberation-result" (JSON: outcome, justifications, rounds_used,
//     hung). Produced by the Deliberation Gate.
//   - Reads "law-reference" (plain-text law ID). Produced by the Operator when
//     admitting a hearing Workitem through the Tribunal's entry binding.
//
// Well-known outputs:
//
//   - "clerk"    — Tier 1-2 verdicts (draft petition for the proposed change)
//   - "advocate" — Tier 3+ verdicts, or Tier 2 promote-to-3 (HITL required)
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

	// Artefact IDs consumed by this node.
	artefactDeliberationResult = "deliberation-result"
	artefactLawReference       = "law-reference"

	// Tier thresholds for routing decisions.
	// Laws at or above this tier require HITL (Advocate) handling.
	tierHITLThreshold = 3 // LAW_TIER_LOCAL_STATUTE

	// Verdict outcome value that indicates promotion.
	verdictPromote = "promote"
)

// ---------------------------------------------------------------------------
// Deliberation Result (read from artefact, produced by Deliberation Gate)
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
// Entry Point
// ---------------------------------------------------------------------------

func main() {
	slog.Info("tribunal-router: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("tribunal-router: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("tribunal-router: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("tribunal-router: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	return handleTribunalRouter(ctx, client)
}

// ---------------------------------------------------------------------------
// Core Logic
// ---------------------------------------------------------------------------

func handleTribunalRouter(ctx context.Context, client *flow.Client) error {
	_, _ = client.Heartbeat(ctx)

	// -- Step 1: Read deliberation result artefact -----------------------
	resultResp, err := client.GetArtefact(ctx, artefactDeliberationResult)
	if err != nil {
		return fmt.Errorf("tribunal-router: get deliberation-result: %w", err)
	}

	var result deliberationResult
	if err := json.Unmarshal(resultResp.GetContent(), &result); err != nil {
		return fmt.Errorf("tribunal-router: unmarshal deliberation-result: %w", err)
	}

	slog.Info("tribunal-router: read deliberation result",
		"outcome", result.Outcome,
		"hung", result.Hung,
		"rounds_used", result.RoundsUsed,
	)

	// -- Step 2: Read law-reference artefact (plain-text law ID) ---------
	lawRefResp, err := client.GetArtefact(ctx, artefactLawReference)
	if err != nil {
		return fmt.Errorf("tribunal-router: get law-reference: %w", err)
	}

	lawID := strings.TrimSpace(string(lawRefResp.GetContent()))
	if lawID == "" {
		return fmt.Errorf("tribunal-router: law-reference artefact is empty")
	}

	slog.Info("tribunal-router: read law reference", "law_id", lawID)

	// -- Step 3: Fetch the law to determine its tier ---------------------
	law, err := client.GetLaw(ctx, lawID)
	if err != nil {
		return fmt.Errorf("tribunal-router: get law %q: %w", lawID, err)
	}

	tier := lawTierNumber(law.GetTier())

	slog.Info("tribunal-router: resolved law tier",
		"law_id", lawID,
		"tier", tier,
		"tier_name", law.GetTier().String(),
	)

	// -- Step 4: Route based on tier and outcome -------------------------
	output := routeByTierAndOutcome(tier, result.Outcome)

	slog.Info("tribunal-router: routing",
		"output", output,
		"tier", tier,
		"outcome", result.Outcome,
	)

	if _, err := client.RouteToOutput(ctx, output); err != nil {
		return fmt.Errorf("tribunal-router: route to %s: %w", output, err)
	}

	return nil
}

// routeByTierAndOutcome determines the output based on the law's tier and
// the deliberation outcome.
//
// Rules:
//  1. Tier >= 3 (Local Statute+) -> always Advocate (HITL required)
//  2. Tier 1-2 with "promote" outcome -> Advocate (promotion to Tier 3 needs
//     HITL ratification)
//  3. Tier 1-2 with any other outcome -> Clerk (draft petition for the change)
func routeByTierAndOutcome(tier int32, outcome string) string {
	if tier >= tierHITLThreshold {
		return outputAdvocate
	}
	if outcome == verdictPromote {
		return outputAdvocate
	}
	return outputClerk
}

// lawTierNumber converts a LawTier enum to its numeric tier value.
// Returns 0 for UNSPECIFIED.
func lawTierNumber(tier flowv1.LawTier) int32 {
	return int32(tier)
}
