package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/artefacts"
	flow "github.com/gideas/flow/sdk/go"
)

// RefineConfig holds handler-level configuration for the Refine handler.
// Agent-level config (prompts, model, schema) is encapsulated in the
// concrete triage and revision agents.
type RefineConfig struct {
	InputArtefacts   []string // artefact IDs for the creative brief (e.g. ["petition"])
	OutputArtefact   string   // artefact ID to revise and store back (e.g. "haiku")
	GovernedArtefact string   // GovernedArtefact CR name (e.g. "haiku")
}

// Refine-specific constants for triage decisions.
const (
	decisionAction = "action"
	decisionRefuse = "refuse"

	justTypeCitation      = "citation"
	justTypeNovelArgument = "novel_argument"

	contemptMessage = "Complying with judicial ruling"
)

// HandleRefine executes the Refine node handler logic using the provided
// contract implementations. The handler is generic — it works with any
// TriageContract + RevisionContract pair.
//
// Steps: fetch inputs → get output artefact → query laws → get feedback →
// Phase 1 triage (sequential) → Phase 2 revision (if any actioned) → store →
// route to "default" output.
func HandleRefine(
	ctx context.Context,
	client *flow.Client,
	triage flow.TriageContract,
	revision flow.RevisionContract,
	cfg RefineConfig,
) error {
	// ---------------------------------------------------------------
	// Pre-inference: read artefacts, query laws, get existing feedback
	// ---------------------------------------------------------------

	inputContent, err := artefacts.FetchInputs(ctx, client, cfg.InputArtefacts)
	if err != nil {
		return fmt.Errorf("refine: read inputs: %w", err)
	}

	outputResp, err := client.GetArtefact(ctx, cfg.OutputArtefact)
	if err != nil {
		return fmt.Errorf("refine: read %s: %w", cfg.OutputArtefact, err)
	}
	reviewContent := string(outputResp.GetContent())

	slog.Info("refine: context",
		"input_artefacts", cfg.InputArtefacts,
		"output_artefact", cfg.OutputArtefact,
	)

	laws, _ := client.QueryLaws(ctx, cfg.GovernedArtefact, "")

	feedbackItems, err := client.GetFeedback(ctx, cfg.GovernedArtefact)
	if err != nil {
		return fmt.Errorf("refine: get feedback: %w", err)
	}

	// ---------------------------------------------------------------
	// Phase 1: Per-item triage (sequential)
	// ---------------------------------------------------------------

	actionedItems, err := triageFeedback(ctx, triage, client,
		feedbackItems, inputContent, reviewContent, laws)
	if err != nil {
		return fmt.Errorf("refine: triage feedback: %w", err)
	}

	// ---------------------------------------------------------------
	// Phase 2: Revision — produce revised content addressing actioned items
	// ---------------------------------------------------------------

	var revised string
	if len(actionedItems) > 0 {
		revised, err = revision.Run(ctx, inputContent, reviewContent, laws, actionedItems)
		if err != nil {
			return fmt.Errorf("refine: revision run: %w", err)
		}
		slog.Info("refine: revised content", "length", len(revised))
	} else {
		// All feedback refused — store the existing content unchanged.
		revised = reviewContent
		slog.Info("refine: no actioned items — content unchanged")
	}

	// ---------------------------------------------------------------
	// Post-inference: store revised content and route back to Sort
	// ---------------------------------------------------------------

	storeResp, err := client.StoreArtefact(ctx, cfg.OutputArtefact, cfg.GovernedArtefact, []byte(revised))
	if err != nil {
		return fmt.Errorf("refine: store revised %s: %w", cfg.OutputArtefact, err)
	}
	slog.Info("refine: stored revised content",
		"artefact", cfg.OutputArtefact,
		"version_hash", storeResp.GetVersionHash(),
		"is_new_version", storeResp.GetIsNewVersion(),
	)

	if _, err := client.RouteToOutput(ctx, "default"); err != nil {
		return fmt.Errorf("refine: route to sort: %w", err)
	}

	slog.Info("refine: routed to sort",
		"workitem_id", os.Getenv(flow.EnvWorkitemID))
	return nil
}

// triageFeedback runs sequential LLM triage for NEW and REJECTED feedback items.
// Each item is processed one at a time. For each, the canWontFix flag determines
// whether the LLM may refuse (canWontFix=true) or must action (canWontFix=false).
// Returns the list of items that were actioned (for Phase 2 context).
func triageFeedback(
	ctx context.Context,
	triage flow.TriageContract,
	client *flow.Client,
	feedback []*flowv1.FeedbackItem,
	inputContent, reviewContent string,
	laws []*flowv1.Law,
) ([]flow.ActionedFeedback, error) {
	type triageTask struct {
		item          *flowv1.FeedbackItem
		forceActioned bool // contempt guard — skip LLM
	}

	var tasks []triageTask
	for _, fb := range feedback {
		state := fb.GetState()
		if state != flowv1.FeedbackState_FEEDBACK_STATE_NEW &&
			state != flowv1.FeedbackState_FEEDBACK_STATE_REJECTED {
			continue
		}

		// Contempt guard: linked ruling on a REJECTED item forces action.
		if fb.GetLinkedRuling() != "" && state == flowv1.FeedbackState_FEEDBACK_STATE_REJECTED {
			tasks = append(tasks, triageTask{
				item:          fb,
				forceActioned: true,
			})
			continue
		}

		tasks = append(tasks, triageTask{
			item: fb,
		})
	}

	if len(tasks) == 0 {
		slog.Info("refine: no feedback items to triage")
		return nil, nil
	}

	slog.Info("refine: triaging feedback items", "count", len(tasks))

	// Process items sequentially (one at a time, no goroutines).
	var actioned []flow.ActionedFeedback
	for _, task := range tasks {
		// Contempt guard: force action without LLM.
		if task.forceActioned {
			fbID := task.item.GetId()
			slog.Info("refine: contempt guard — forcing action",
				"feedback_id", fbID)
			if err := client.ResolveFeedback(ctx, fbID, contemptMessage); err != nil {
				return nil, fmt.Errorf("refine: resolve feedback %s: %w", fbID, err)
			}
			actioned = append(actioned, flow.ActionedFeedback{
				FeedbackID:     fbID,
				Message:        task.item.GetMessage(),
				FixDescription: contemptMessage,
			})
			continue
		}

		// Run LLM triage for this item.
		out, err := triage.Run(ctx, task.item, inputContent, reviewContent, laws)
		if err != nil {
			return nil, fmt.Errorf("refine: triage feedback %s: %w",
				task.item.GetId(), err)
		}

		fbID := task.item.GetId()
		canWontFix := task.item.GetCanWontFix()

		// Belt-and-suspenders: refuse is not allowed for canWontFix=false.
		if !canWontFix && out.Decision == decisionRefuse {
			return nil, fmt.Errorf(
				"refine: cannot refuse canWontFix=false feedback %s", fbID)
		}

		switch out.Decision {
		case decisionAction:
			slog.Info("refine: actioning feedback",
				"feedback_id", fbID, "message", out.Message)
			if err := client.ResolveFeedback(ctx, fbID, out.Message); err != nil {
				return nil, fmt.Errorf("refine: resolve feedback %s: %w", fbID, err)
			}
			actioned = append(actioned, flow.ActionedFeedback{
				FeedbackID:     fbID,
				Message:        task.item.GetMessage(),
				FixDescription: out.Message,
			})

		case decisionRefuse:
			justification, err := buildJustification(*out)
			if err != nil {
				return nil, fmt.Errorf("refine: build justification for %s: %w", fbID, err)
			}
			slog.Info("refine: refusing feedback",
				"feedback_id", fbID,
				"justification_type", out.JustificationType,
				"message", out.Message)
			if err := client.RefuseFeedback(ctx, fbID, justification); err != nil {
				return nil, fmt.Errorf("refine: refuse feedback %s: %w", fbID, err)
			}

		default:
			return nil, fmt.Errorf("refine: unexpected decision %q for feedback %s",
				out.Decision, fbID)
		}
	}

	return actioned, nil
}

// buildJustification converts the triage result into a proto Justification
// for the RefuseFeedback call.
func buildJustification(out flow.TriageResult) (*flowv1.Justification, error) {
	switch out.JustificationType {
	case justTypeCitation:
		if len(out.CitationIDs) == 0 {
			return nil, fmt.Errorf("citation justification requires at least one citation_id")
		}
		return &flowv1.Justification{
			Kind: &flowv1.Justification_Citation{
				Citation: &flowv1.Citation{CitationIds: out.CitationIDs},
			},
		}, nil

	case justTypeNovelArgument:
		if out.Argument == "" {
			return nil, fmt.Errorf("novel_argument justification requires a non-empty argument")
		}
		return &flowv1.Justification{
			Kind: &flowv1.Justification_NovelArgument{
				NovelArgument: &flowv1.NovelArgument{Argument: out.Argument},
			},
		}, nil

	default:
		return nil, fmt.Errorf("refuse decision requires justification_type (citation or novel_argument), got %q",
			out.JustificationType)
	}
}
