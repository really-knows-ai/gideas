// Appraise is the reviewer node of the Haiku Foundry Cycle.
//
// It reads the "petition" (creative brief) and "haiku" artefacts, then uses an
// LLM (kimi-k2.5:cloud via Ollama) to review how well the haiku adheres to the
// petition and general haiku quality standards.
//
// If there is existing ACTIONED feedback (a fix was applied), Appraise checks
// whether the fix is satisfactory and calls AcceptFix to resolve it.
//
// If the haiku needs improvement, Appraise raises new feedback. If the haiku is
// good, it stamps "review" on it (signalling subjective review passed).
//
// Always routes back to Sort.
//
// Environment:
//
//	OLLAMA_BASE_URL     — Ollama API endpoint (default: http://localhost:11434)
//	APPRAISE_MODEL      — Model name (default: kimi-k2.5:cloud)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/ollama"
	flow "github.com/gideas/flow/sdk/go"
)

// reviewResponse is the JSON structure the LLM must return.
type reviewResponse struct {
	Verdict   string   `json:"verdict"`    // "approved" or "feedback"
	Message   string   `json:"message"`    // required when verdict is "feedback"
	CitedLaws []string `json:"cited_laws"` // law IDs referenced in the review
}

const (
	defaultModel = "kimi-k2.5:cloud"
	envModel     = "APPRAISE_MODEL"
)

func main() {
	slog.Info("haiku-appraise: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("haiku-appraise: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("haiku-appraise: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("appraise: create client: %w", err)
	}
	defer client.Close()

	client.Heartbeat(ctx)

	// Read the petition and haiku.
	petitionResp, err := client.GetArtefact(ctx, "petition")
	if err != nil {
		return fmt.Errorf("appraise: read petition: %w", err)
	}
	petition := string(petitionResp.GetContent())

	haikuResp, err := client.GetArtefact(ctx, "haiku")
	if err != nil {
		return fmt.Errorf("appraise: read haiku: %w", err)
	}
	haiku := string(haikuResp.GetContent())

	slog.Info("haiku-appraise: reviewing",
		"petition", petition,
		"haiku", haiku,
	)

	// Check for existing feedback that was actioned (fix applied).
	existingFeedback, err := client.GetFeedback(ctx, "haiku")
	if err != nil {
		return fmt.Errorf("appraise: get feedback: %w", err)
	}

	// Accept any actioned fixes — the reviewer acknowledges the Refine node's work.
	for _, fb := range existingFeedback {
		if fb.GetState() == flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED {
			slog.Info("haiku-appraise: accepting fix for feedback", "feedback_id", fb.GetId())
			if err := client.AcceptFix(ctx, fb.GetId()); err != nil {
				return fmt.Errorf("appraise: accept fix: %w", err)
			}
		}
	}

	// Build review context including previous feedback history.
	var feedbackHistory string
	for _, fb := range existingFeedback {
		feedbackHistory += fmt.Sprintf("- [%s] %s\n", fb.GetState().String(), fb.GetMessage())
	}

	// Query applicable laws.
	laws, _ := client.QueryLaws(ctx, "text/haiku", "")

	// Build law context with IDs so the LLM can cite them.
	var lawBlock string
	if len(laws) > 0 {
		lawBlock = "\n## MANDATORY GOVERNANCE LAWS\n\nThe following laws are binding. The haiku MUST comply with ALL of them.\nIf any law is violated, you MUST reject the haiku and cite the violated law(s) by ID.\n\n"
		for _, law := range laws {
			lawBlock += fmt.Sprintf("- **[%s]** (Tier %d): %s\n", law.GetId(), law.GetTier(), law.GetGoal())
		}
	}

	// Build feedback history context.
	var historyBlock string
	if feedbackHistory != "" {
		historyBlock = "\n## PREVIOUS FEEDBACK HISTORY\n\n" + feedbackHistory
	}

	// Ask the LLM to review.
	model := os.Getenv(envModel)
	if model == "" {
		model = defaultModel
	}

	prompt := fmt.Sprintf(`You are a strict haiku reviewer for a governed creative pipeline.

Your job is to evaluate whether the haiku satisfies TWO requirements:
1. **THE PETITION** — the original creative brief that the haiku was written to fulfil.
2. **GOVERNANCE LAWS** — mandatory quality and style rules that all haikus must obey.

Both must be satisfied. If either is violated, reject the haiku.

---

## THE PETITION (primary requirement)

> %s

The haiku must faithfully address this petition's theme, subject, and mood.

---

## THE HAIKU UNDER REVIEW

%s

---%s%s

---

## YOUR TASK

Evaluate the haiku against the petition AND all governance laws.

- If the haiku satisfies the petition and complies with all laws: approve it.
- If the haiku violates the petition or any law: reject it with specific, actionable feedback.
  When rejecting for a law violation, include the law ID(s) in "cited_laws".

## RESPONSE FORMAT

Respond with ONLY a JSON object in one of these two forms:

Approved:
{"verdict": "approved", "message": "", "cited_laws": []}

Rejected (with feedback):
{"verdict": "feedback", "message": "<specific actionable feedback in 1-2 sentences>", "cited_laws": ["<law_id_1>", "<law_id_2>"]}

Output ONLY the JSON object. No markdown fences, no explanation, no other text.`,
		petition, haiku, lawBlock, historyBlock)

	llm := ollama.New()
	review, err := llm.Generate(ctx, model, prompt)
	if err != nil {
		return fmt.Errorf("appraise: review haiku: %w", err)
	}
	slog.Info("haiku-appraise: LLM review result", "review", review)

	// Parse JSON response from LLM.
	review = strings.TrimSpace(review)
	// Strip markdown code fences if the LLM wraps the JSON.
	review = strings.TrimPrefix(review, "```json")
	review = strings.TrimPrefix(review, "```")
	review = strings.TrimSuffix(review, "```")
	review = strings.TrimSpace(review)

	var result reviewResponse
	if err := json.Unmarshal([]byte(review), &result); err != nil {
		// Fallback: if JSON parsing fails, treat as feedback with the raw text.
		slog.Warn("haiku-appraise: failed to parse JSON response, treating as feedback", "error", err, "raw", review)
		result = reviewResponse{Verdict: "feedback", Message: review}
	}

	slog.Info("haiku-appraise: parsed review",
		"verdict", result.Verdict,
		"message", result.Message,
		"cited_laws", result.CitedLaws,
	)

	// Cite any referenced laws.
	if len(result.CitedLaws) > 0 {
		if err := client.Cite(ctx, result.CitedLaws...); err != nil {
			slog.Error("haiku-appraise: failed to cite laws", "error", err, "law_ids", result.CitedLaws)
			// Non-fatal — continue with the review verdict.
		} else {
			slog.Info("haiku-appraise: cited laws", "law_ids", result.CitedLaws)
		}
	}

	if strings.EqualFold(result.Verdict, "approved") {
		// Haiku passes review — stamp "review" (subjective review passed).
		if _, err := client.StampArtefact(ctx, "haiku", "review"); err != nil {
			return fmt.Errorf("appraise: stamp review: %w", err)
		}
		slog.Info("haiku-appraise: review stamp applied")
	} else {
		// Raise feedback with the LLM's message.
		feedback := result.Message
		if feedback == "" {
			feedback = "Haiku did not meet review standards."
		}

		feedbackID, err := client.AddFeedback(ctx, "haiku", flowv1.Severity_SEVERITY_MEDIUM, feedback)
		if err != nil {
			return fmt.Errorf("appraise: add feedback: %w", err)
		}
		slog.Info("haiku-appraise: feedback raised", "feedback_id", feedbackID, "message", feedback)
	}

	// Always route back to Sort.
	if _, err := client.RouteToOutput(ctx, "default"); err != nil {
		return fmt.Errorf("appraise: route to sort: %w", err)
	}

	slog.Info("haiku-appraise: routed to sort", "workitem_id", wctx.GetWorkitemId())
	return nil
}
