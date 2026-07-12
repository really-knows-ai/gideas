package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/nodes/internal/artefacts"
	flow "github.com/foundry/flow/sdk/go"
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
	decisionEdit   = "edit"
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
	workitem *flow.Workitem,
	triage flow.TriageContract,
	revision flow.RevisionContract,
	cfg RefineConfig,
) error {
	// ---------------------------------------------------------------
	// Pre-inference: read artefacts, query laws, get existing feedback
	// ---------------------------------------------------------------

	inputContent, err := artefacts.FetchInputs(workitem, cfg.InputArtefacts)
	if err != nil {
		return fmt.Errorf("refine: read inputs: %w", err)
	}

	outputArt, err := workitem.GetArtefact(cfg.OutputArtefact)
	if err != nil {
		return fmt.Errorf("refine: read %s: %w", cfg.OutputArtefact, err)
	}
	outputContent, err := outputArt.GetContent()
	if err != nil {
		return fmt.Errorf("refine: get content %s: %w", cfg.OutputArtefact, err)
	}
	reviewContent := string(outputContent)

	slog.Info("refine: context",
		"input_artefacts", cfg.InputArtefacts,
		"output_artefact", cfg.OutputArtefact,
	)

	lawGroups, _ := workitem.GetLawGroups("")
	var protoLaws []*flowv1.Law
	for _, g := range lawGroups {
		laws, _ := g.GetLaws()
		for _, l := range laws {
			protoLaws = append(protoLaws, l.PB())
		}
	}

	feedbackItems, err := workitem.GetFeedback(cfg.GovernedArtefact)
	if err != nil {
		return fmt.Errorf("refine: get feedback: %w", err)
	}

	// ---------------------------------------------------------------
	// Phase 1: Per-item triage (sequential)
	// ---------------------------------------------------------------

	actionedItems, err := triageFeedback(ctx, triage,
		feedbackItems, inputContent, reviewContent, protoLaws)
	if err != nil {
		return fmt.Errorf("refine: triage feedback: %w", err)
	}

	// ---------------------------------------------------------------
	// Phase 2: Revision — produce revised content addressing actioned items
	// ---------------------------------------------------------------

	var revised string
	if len(actionedItems) > 0 {
		revised, err = revision.Run(ctx, inputContent, reviewContent, protoLaws, actionedItems)
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

	if err := outputArt.Store([]byte(revised)); err != nil {
		return fmt.Errorf("refine: store revised %s: %w", cfg.OutputArtefact, err)
	}
	slog.Info("refine: stored revised content",
		"artefact", cfg.OutputArtefact,
		"version_hash", outputArt.VersionHash(),
		"is_new_version", outputArt.IsNewVersion(),
	)

	if err := workitem.RouteTo("default"); err != nil {
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
	feedback []*flow.Feedback,
	inputContent, reviewContent string,
	laws []*flowv1.Law,
) ([]flow.ActionedFeedback, error) {
	type triageTask struct {
		fb            *flow.Feedback
		forceActioned bool // contempt guard — skip LLM
	}

	var tasks []triageTask
	for _, fb := range feedback {
		state := fb.GetState()
		if state != flow.FeedbackStateNew &&
			state != flow.FeedbackStateRejected {
			continue
		}

		// Contempt guard: feedback with a linked ruling from the judiciary
		// bypasses LLM triage and is force-actioned.
		forceActioned := fb.PB().GetLinkedRuling() != ""

		tasks = append(tasks, triageTask{
			fb:            fb,
			forceActioned: forceActioned,
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
			fbID := task.fb.GetID()
			slog.Info("refine: contempt guard — forcing action",
				"feedback_id", fbID)
			if err := task.fb.Resolve(contemptMessage); err != nil {
				return nil, fmt.Errorf("refine: resolve feedback %s: %w", fbID, err)
			}
			actioned = append(actioned, flow.ActionedFeedback{
				FeedbackID:     fbID,
				Message:        task.fb.GetMessage(),
				FixDescription: contemptMessage,
			})
			continue
		}

		// The TriageContract.Run takes *flowv1.FeedbackItem — get the proto via PB().
		// ponytail: Feedback domain does not expose GetCanWontFix(), so we use
		// the proto method. Add domain accessors in Phase 10.
		protoFB := task.fb.PB()
		if protoFB == nil {
			continue
		}

		// Run LLM triage for this item.
		out, err := triage.Run(ctx, protoFB, inputContent, reviewContent, laws)
		if err != nil {
			return nil, fmt.Errorf("refine: triage feedback %s: %w",
				protoFB.GetId(), err)
		}

		fbID := protoFB.GetId()
		canWontFix := protoFB.GetCanWontFix()

		// Belt-and-suspenders: refuse is not allowed for canWontFix=false.
		if !canWontFix && out.Decision == decisionRefuse {
			return nil, fmt.Errorf(
				"refine: cannot refuse canWontFix=false feedback %s", fbID)
		}

		switch out.Decision {
		case decisionRefuse:
			justification, err := buildJustification(*out)
			if err != nil {
				return nil, fmt.Errorf("refine: build justification for %s: %w", fbID, err)
			}
			slog.Info("refine: refusing feedback",
				"feedback_id", fbID,
				"justification_type", out.JustificationType,
				"message", out.Message)
			if err := task.fb.Refuse(justification); err != nil {
				return nil, fmt.Errorf("refine: refuse feedback %s: %w", fbID, err)
			}

		default:
			slog.Info("refine: actioning feedback",
				"feedback_id", fbID, "decision", out.Decision, "message", out.Message)
			if err := task.fb.Resolve(out.Message); err != nil {
				return nil, fmt.Errorf("refine: resolve feedback %s: %w", fbID, err)
			}
			actioned = append(actioned, flow.ActionedFeedback{
				FeedbackID:     fbID,
				Message:        protoFB.GetMessage(),
				FixDescription: out.Message,
			})
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
