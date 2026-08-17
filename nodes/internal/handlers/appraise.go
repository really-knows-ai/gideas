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

// AppraisalConfig holds handler-level configuration for the Appraisal handler.
// Agent-level config (prompts, model, schema) is encapsulated in the
// concrete eval and finding agents.
type AppraisalConfig struct {
	InputArtefacts   []string                     // artefact IDs to read as input (e.g. ["petition"])
	ReviewArtefact   string                       // artefact ID to review (e.g. "haiku")
	GovernedArtefact string                       // GovernedArtefact CR name (e.g. "haiku")
	ReviewerNode     string                       // target node for fan-out review (e.g. "appraiser")
	Appraisers       []AppraiserPersonalityConfig // appraiser persona configs
}

// AppraiserPersonalityConfig defines a single appraiser persona. It is the
// single shared definition for the Appraise orchestrator and the Appraisal
// node's ConfigMap loader (nodes/appraisal/main.go); yaml tags drive the
// node's config deserialization.
type AppraiserPersonalityConfig struct {
	ID          string `yaml:"id"`
	Personality string `yaml:"personality"`
}

// Appraisal-specific constants.
const (
	verdictAccept                = "accept"
	verdictReject                = "reject"
	ArtefactAppraiserPersonality = "appraiserPersonality"
	ArtefactPass                 = "pass"
	EventAppraisalCoverage       = "appraisal.coverage"
	EventAppraisalAttestation    = "appraisal.attestation"
	stampAppraisal               = "appraisal"
)

// hasNovelArgument returns true if the feedback item carries a
// NovelArgument justification.
func hasNovelArgument(fb *flowv1.FeedbackItem) bool {
	j := fb.GetJustification()
	return j != nil && j.GetNovelArgument() != nil &&
		j.GetNovelArgument().GetArgument() != ""
}

// ---------------------------------------------------------------------------
// HandleEval (Appraise Phase 1)
// ---------------------------------------------------------------------------

// HandleEval executes the Phase 1 (evaluation) and Phase 2 (fan-out review)
// logic of the Appraise node, stamps the artefact, raises new feedback, and
// optionally runs Phase 3 (learning capture via FindingContract).
//
// This is the full Appraise orchestration handler.
//
//nolint:cyclop // Orchestration function — sequential phases are inherently complex.
func HandleAppraisal(
	ctx context.Context,
	workitem *flow.Workitem,
	client *flow.Client,
	eval flow.EvalContract,
	finding flow.FindingContract,
	cfg AppraisalConfig,
) error {
	// ---------------------------------------------------------------
	// Pre-inference: read artefacts, query laws, get existing feedback
	// ---------------------------------------------------------------

	inputContent, err := artefacts.FetchInputs(workitem, cfg.InputArtefacts)
	if err != nil {
		return fmt.Errorf("appraisal: read inputs: %w", err)
	}

	reviewArt, err := workitem.GetArtefact(cfg.ReviewArtefact)
	if err != nil {
		return fmt.Errorf("appraisal: read %s: %w", cfg.ReviewArtefact, err)
	}
	reviewContentBytes, err := reviewArt.GetContent()
	if err != nil {
		return fmt.Errorf("appraisal: get content %s: %w", cfg.ReviewArtefact, err)
	}
	reviewContent := string(reviewContentBytes)

	slog.Info("appraisal: reviewing",
		"input_artefacts", cfg.InputArtefacts,
		"review_artefact", cfg.ReviewArtefact,
	)

	existingFeedback, err := workitem.GetFeedback(cfg.GovernedArtefact)
	if err != nil {
		return fmt.Errorf("appraisal: get feedback: %w", err)
	}

	// ---------------------------------------------------------------
	// Phase 1: Fan-out review — delegate to child Reviewer nodes
	// ---------------------------------------------------------------
	// Run first so appraiser children see the current state and history,
	// producing new observations against laws and the creative brief.
	result, err := fanOutAppraisal(
		ctx, workitem, client, cfg, existingFeedback,
		inputContent, reviewContent)
	if err != nil {
		return fmt.Errorf("appraisal: fan-out review: %w", err)
	}

	// ---------------------------------------------------------------
	// Phase 2: Evaluate ACTIONED and WONT_FIX feedback items (parallel)
	// ---------------------------------------------------------------
	// Now that we have the full picture (existing feedback + fresh
	// observations from Phase 1), decide whether each fix truly resolves
	// the underlying law conflict or just moves it to a different law.

	novelResolved, err := evaluateFeedback(
		ctx, eval,
		existingFeedback, result.feedback, inputContent, reviewContent)
	if err != nil {
		return fmt.Errorf("appraisal: evaluate feedback: %w", err)
	}

	slog.Info("appraisal: review complete",
		"feedback_count", len(result.feedback),
		"dispatch_count", len(result.dispatchMatrix))

	// ---------------------------------------------------------------
	// Post-inference: raise feedback, cite laws
	// --------------------------------------------------------------

	for i, item := range result.feedback {
		if item.Message == "" {
			continue
		}

		feedbackID, err := workitem.AddFeedback(
			cfg.GovernedArtefact, true, item.Message)
		if err != nil {
			return fmt.Errorf("appraisal: add feedback[%d]: %w", i, err)
		}
		slog.Info("appraisal: feedback raised",
			"index", i,
			"feedback_id", feedbackID,
			"message", item.Message,
			"cited_laws", item.CitedLaws,
		)

		if len(item.CitedLaws) > 0 {
			if err := workitem.Cite(item.CitedLaws...); err != nil {
				slog.Error("appraisal: failed to cite laws",
					"error", err, "law_ids", item.CitedLaws)
			} else {
				slog.Info("appraisal: cited laws", "law_ids", item.CitedLaws)
			}
		}
	}

	if len(result.feedback) == 0 {
		slog.Info("appraisal: no feedback — content looks good")
	}

	// Post-fan-out: coverage and attestation events. Emitted after
	// feedback is raised so the audit trail reflects the complete
	// review state (R5 ordering).
	if len(result.dispatchMatrix) > 0 {
		coverage := buildCoverageMap(
			result.dispatchMatrix, result.childStatuses,
			result.childResults, result.childByDispatchIdx,
		)
		emitCoverageEvent(ctx, client, coverage, os.Getenv(flow.EnvWorkitemID))
		emitAttestationEvent(ctx, client, coverage, os.Getenv(flow.EnvWorkitemID))
	} else if len(cfg.Appraisers) > 0 {
		slog.Info("appraisal: no dispatches — pass-through stamp follows after feedback")
	} else {
		slog.Info("appraisal: no appraisers — skipping stamps and events")
	}

	// ---------------------------------------------------------------
	// Post-feedback: attestation stamps — applied only after feedback
	// is raised (R5 requirement).
	// ---------------------------------------------------------------

	if len(result.dispatchMatrix) > 0 {
		if err := applyAttestationStamps(workitem, cfg.GovernedArtefact, result); err != nil {
			return fmt.Errorf("appraisal: attestation stamps: %w", err)
		}
	} else if len(cfg.Appraisers) > 0 {
		// No dispatches but appraisers exist — pass-through: stamp the
		// completion signal so sort can complete the exit contract.
		slog.Info("appraisal: no dispatches — applying pass-through stamp")
		art, artErr := workitem.GetArtefact(cfg.GovernedArtefact)
		if artErr != nil {
			return fmt.Errorf("appraisal: get artefact: %w", artErr)
		}
		if err := art.Stamp(stampAppraisal); err != nil {
			return fmt.Errorf("appraisal: stamp %s: %w", stampAppraisal, err)
		}
	} else {
		slog.Info("appraisal: no appraisers — skipping stamps")
	}

	// ---------------------------------------------------------------
	// Phase 3: Learning capture — mint Tier 1 Findings from resolved
	// novel arguments
	// ---------------------------------------------------------------

	if len(novelResolved) > 0 {
		if err := mintFindings(ctx, finding, client, novelResolved); err != nil {
			return fmt.Errorf("appraisal: mint findings: %w", err)
		}
	} else {
		slog.Info("appraisal: no novel arguments resolved " +
			"— skipping learning capture")
	}

	// ---------------------------------------------------------------
	// Route onward
	// ---------------------------------------------------------------

	if err := workitem.RouteTo("default"); err != nil {
		return fmt.Errorf("appraisal: route to output: %w", err)
	}

	slog.Info("appraisal: routed to output",
		"workitem_id", os.Getenv(flow.EnvWorkitemID))
	return nil
}
