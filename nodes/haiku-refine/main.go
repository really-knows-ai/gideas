// Refine is the revision node of the Haiku Foundry Cycle.
//
// It reads the "petition" (creative brief), the current "haiku", and any
// unresolved feedback, then uses an LLM (gpt-oss:120b-cloud via Ollama) to
// produce a revised haiku that addresses the feedback. The revised haiku
// replaces the existing artefact (creating a new version), and all unresolved
// feedback items are marked as actioned.
//
// Always routes back to Sort for governance triage of the new version.
//
// Environment:
//
//	OLLAMA_BASE_URL   — Ollama API endpoint (default: http://localhost:11434)
//	REFINE_MODEL      — Model name (default: gpt-oss:120b-cloud)
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/ollama"
	flow "github.com/gideas/flow/sdk/go"
)

const (
	defaultModel = "gpt-oss:120b-cloud"
	envModel     = "REFINE_MODEL"
)

func main() {
	slog.Info("haiku-refine: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("haiku-refine: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("haiku-refine: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("refine: create client: %w", err)
	}
	defer client.Close()

	client.Heartbeat(ctx)

	// Read the petition.
	petitionResp, err := client.GetArtefact(ctx, "petition")
	if err != nil {
		return fmt.Errorf("refine: read petition: %w", err)
	}
	petition := string(petitionResp.GetContent())

	// Read the current haiku.
	haikuResp, err := client.GetArtefact(ctx, "haiku")
	if err != nil {
		return fmt.Errorf("refine: read haiku: %w", err)
	}
	haiku := string(haikuResp.GetContent())

	// Read feedback.
	feedbackItems, err := client.GetFeedback(ctx, "haiku")
	if err != nil {
		return fmt.Errorf("refine: get feedback: %w", err)
	}

	// Collect unresolved feedback for the prompt.
	var feedbackLines []string
	var unresolvedIDs []string
	for _, fb := range feedbackItems {
		state := fb.GetState()
		if state != flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED {
			feedbackLines = append(feedbackLines, fmt.Sprintf("- %s", fb.GetMessage()))
			// Track NEW feedback items that we need to mark as actioned.
			if state == flowv1.FeedbackState_FEEDBACK_STATE_NEW {
				unresolvedIDs = append(unresolvedIDs, fb.GetId())
			}
		}
	}

	slog.Info("haiku-refine: context",
		"petition", petition,
		"current_haiku", haiku,
		"feedback_count", len(feedbackLines),
	)

	// Query applicable laws for governance guidance.
	laws, _ := client.QueryLaws(ctx, "text/haiku", "")
	var lawContext string
	if len(laws) > 0 {
		lawContext = "\nApplicable governance laws:\n"
		for _, law := range laws {
			lawContext += fmt.Sprintf("- %s\n", law.GetGoal())
		}
	}

	// Build the revision prompt.
	feedbackText := "No specific feedback — the haiku failed syllable validation (must be exactly 5-7-5)."
	if len(feedbackLines) > 0 {
		feedbackText = "Feedback to address:\n" + strings.Join(feedbackLines, "\n")
	}

	model := os.Getenv(envModel)
	if model == "" {
		model = defaultModel
	}

	prompt := fmt.Sprintf(`You are a haiku poet revising your work. You must address the feedback while staying true to the original request.

ORIGINAL REQUEST (petition): %s

CURRENT HAIKU:
%s

%s
%s
Write a revised haiku (three lines: EXACTLY 5 syllables, 7 syllables, 5 syllables) that addresses the feedback while remaining faithful to the petition.

IMPORTANT: Output ONLY the three lines of the haiku, nothing else. No title, no explanation, no quotes. Count syllables carefully.`, petition, haiku, feedbackText, lawContext)

	llm := ollama.New()
	revised, err := llm.Generate(ctx, model, prompt)
	if err != nil {
		return fmt.Errorf("refine: generate revised haiku: %w", err)
	}
	slog.Info("haiku-refine: revised haiku", "haiku", revised)

	// Store the revised haiku (creates a new version, old stamps are invalidated).
	storeResp, err := client.StoreArtefact(ctx, "haiku", "text/haiku", []byte(revised))
	if err != nil {
		return fmt.Errorf("refine: store revised haiku: %w", err)
	}
	slog.Info("haiku-refine: stored revised haiku",
		"version_hash", storeResp.GetVersionHash(),
		"is_new_version", storeResp.GetIsNewVersion(),
	)

	// Mark all unresolved feedback as actioned.
	for _, fbID := range unresolvedIDs {
		if err := client.ResolveFeedback(ctx, fbID, "Revised haiku to address feedback"); err != nil {
			slog.Warn("haiku-refine: failed to resolve feedback", "feedback_id", fbID, "error", err)
		} else {
			slog.Info("haiku-refine: feedback marked actioned", "feedback_id", fbID)
		}
	}

	// Route back to Sort — Sort will see the new version has no stamps and
	// route to Quench for re-validation.
	if _, err := client.RouteToOutput(ctx, "default"); err != nil {
		return fmt.Errorf("refine: route to sort: %w", err)
	}

	slog.Info("haiku-refine: routed to sort", "workitem_id", wctx.GetWorkitemId())
	return nil
}
