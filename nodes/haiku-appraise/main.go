// Appraise is the reviewer node of the Haiku Foundry Cycle.
//
// It reads the "petition" (creative brief) and "haiku" artefacts, then uses an
// LLM (kimi-k2.5:cloud via Ollama) to review how well the haiku adheres to the
// petition and the active governance laws.
//
// Appraise always stamps "review" — meaning "I have appraised this version",
// not "this version is valid". The stamp follows the same semantic as the
// quench/linter stamp: it records inspection, not approval.
//
// The LLM produces zero or more feedback items. Each item either cites one or
// more governance laws (by ID) or offers a novel observation with no citations.
// If the LLM finds no issues, it returns an empty array and the haiku proceeds.
//
// If there is existing ACTIONED feedback (a fix was applied by Refine),
// Appraise accepts it before re-reviewing.
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

// feedbackItem is the JSON structure for a single piece of LLM feedback.
type feedbackItem struct {
	Message   string   `json:"message"`    // specific, actionable observation
	CitedLaws []string `json:"cited_laws"` // law IDs (empty = novel observation)
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
		lawBlock = "\n## GOVERNANCE LAWS\n\nThe following laws are active. The haiku MUST comply with all of them.\nIf a law is violated, cite it by ID in your feedback.\n\n"
		for _, law := range laws {
			lawBlock += fmt.Sprintf("- [%s] (Tier %d): %s\n", law.GetId(), law.GetTier(), law.GetGoal())
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

	prompt := fmt.Sprintf(`You are a haiku reviewer for a governed creative pipeline.

Your job is to review the haiku and produce feedback. You are NOT approving or rejecting — you are producing observations. If you have no issues, return an empty list.

Every piece of feedback must either:
1. CITE one or more governance laws by ID — the haiku violates or insufficiently addresses the law.
2. Offer a NOVEL observation — something not covered by any law but worth improving. Use an empty cited_laws array for these.

---

## THE PETITION

The haiku was written to fulfil this creative brief:

> %s

The haiku must faithfully address the petition's theme, subject, and mood.

---

## THE HAIKU UNDER REVIEW

%s

---%s%s

---

## RESPONSE FORMAT

Respond with ONLY a JSON array of feedback items. Each item has:
- "message": a specific, actionable observation (1-2 sentences)
- "cited_laws": array of law IDs this feedback references (empty array if novel observation)

If the haiku is excellent and you have no feedback, return an empty array: []

Examples:

No issues:
[]

Law violation:
[{"message": "The haiku names the season directly ('in winter') rather than evoking it through imagery.", "cited_laws": ["%s"]}]

Novel observation:
[{"message": "The final line feels rushed — consider a more contemplative closing image.", "cited_laws": []}]

Multiple issues:
[{"message": "...", "cited_laws": ["id1"]}, {"message": "...", "cited_laws": []}]

Output ONLY the JSON array. No markdown fences, no explanation, no other text.`,
		petition, haiku, lawBlock, historyBlock,
		// Example law ID — use the first real law ID if available, otherwise a placeholder.
		func() string {
			if len(laws) > 0 {
				return laws[0].GetId()
			}
			return "example-law-id"
		}())

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

	var items []feedbackItem
	if err := json.Unmarshal([]byte(review), &items); err != nil {
		// Fallback: if JSON parsing fails, treat the raw text as a single novel observation.
		slog.Warn("haiku-appraise: failed to parse JSON response, treating as single feedback",
			"error", err, "raw", review)
		items = []feedbackItem{{Message: review, CitedLaws: nil}}
	}

	slog.Info("haiku-appraise: parsed review", "feedback_count", len(items))

	// Always stamp "review" — means "I have appraised this version".
	if _, err := client.StampArtefact(ctx, "haiku", "review"); err != nil {
		return fmt.Errorf("appraise: stamp review: %w", err)
	}
	slog.Info("haiku-appraise: review stamp applied")

	// Raise each feedback item and cite referenced laws.
	for i, item := range items {
		if item.Message == "" {
			continue
		}

		feedbackID, err := client.AddFeedback(ctx, "haiku", flowv1.Severity_SEVERITY_MEDIUM, item.Message)
		if err != nil {
			return fmt.Errorf("appraise: add feedback[%d]: %w", i, err)
		}
		slog.Info("haiku-appraise: feedback raised",
			"index", i,
			"feedback_id", feedbackID,
			"message", item.Message,
			"cited_laws", item.CitedLaws,
		)

		// Cite referenced laws.
		if len(item.CitedLaws) > 0 {
			if err := client.Cite(ctx, item.CitedLaws...); err != nil {
				slog.Error("haiku-appraise: failed to cite laws",
					"error", err, "law_ids", item.CitedLaws)
				// Non-fatal — continue.
			} else {
				slog.Info("haiku-appraise: cited laws", "law_ids", item.CitedLaws)
			}
		}
	}

	if len(items) == 0 {
		slog.Info("haiku-appraise: no feedback — haiku looks good")
	}

	// Always route back to Sort.
	if _, err := client.RouteToOutput(ctx, "default"); err != nil {
		return fmt.Errorf("appraise: route to sort: %w", err)
	}

	slog.Info("haiku-appraise: routed to sort", "workitem_id", wctx.GetWorkitemId())
	return nil
}
