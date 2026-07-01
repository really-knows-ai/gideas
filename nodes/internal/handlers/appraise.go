package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/artefacts"
	flow "github.com/gideas/flow/sdk/go"
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

// AppraiserPersonalityConfig defines a single appraiser persona.
// ponytail: duplicated in nodes/appraisal/main.go;
// promote to SDK if a third definition appears.
type AppraiserPersonalityConfig struct {
	ID          string
	Personality string
}

// Appraisal-specific constants.
const (
	verdictAccept                = "accept"
	verdictReject                = "reject"
	ArtefactAppraiserPersonality = "appraiserPersonality"
	ArtefactPass                 = "pass"
	EventAppraisalCoverage       = "appraisal.coverage"
	EventAppraisalAttestation    = "appraisal.attestation"
	stampAppraiseSecurity        = "appraise-security"
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
	// Phase 1: Evaluate ACTIONED and WONT_FIX feedback items (parallel)
	// ---------------------------------------------------------------

	novelResolved, err := evaluateFeedback(
		ctx, eval, workitem,
		existingFeedback, inputContent, reviewContent)
	if err != nil {
		return fmt.Errorf("appraisal: evaluate feedback: %w", err)
	}

	// ---------------------------------------------------------------
	// Phase 2: Fan-out review — delegate to child Reviewer nodes
	// ---------------------------------------------------------------

	result, err := fanOutAppraisal(
		ctx, workitem, cfg, existingFeedback,
		inputContent, reviewContent)
	if err != nil {
		return fmt.Errorf("appraisal: fan-out review: %w", err)
	}

	slog.Info("appraisal: review complete",
		"feedback_count", len(result.feedback),
		"dispatch_count", len(result.dispatchMatrix))

	// Post-fan-out: stamping, coverage, events (only if dispatches exist).
	if len(result.dispatchMatrix) > 0 {
		applyAppraisalStamps(ctx, workitem, cfg.GovernedArtefact,
			result.dispatchMatrix, result.childStatuses,
			result.childByDispatchIdx, result.unitsByGroup, result.groups,
			result.skippedIndices)
		coverage := buildCoverageMap(
			result.dispatchMatrix, result.childStatuses,
			result.childResults, result.childByDispatchIdx,
		)
		emitCoverageEvent(ctx, client, coverage, os.Getenv(flow.EnvWorkitemID))
		emitAttestationEvent(ctx, client, coverage, os.Getenv(flow.EnvWorkitemID))
	} else if len(cfg.Appraisers) > 0 {
		// No dispatches but appraisers exist — pass-through: stamp appraise-security
		// so sort can complete the exit contract.
		slog.Info("appraisal: no dispatches — applying pass-through stamp")
		art, artErr := workitem.GetArtefact(cfg.GovernedArtefact)
		if artErr != nil {
			return fmt.Errorf("appraisal: get artefact: %w", artErr)
		}
		if err := art.Stamp(stampAppraiseSecurity); err != nil {
			return fmt.Errorf("appraisal: stamp %s: %w", stampAppraiseSecurity, err)
		}
	} else {
		slog.Info("appraisal: no appraisers — skipping stamps and events")
	}

	// ---------------------------------------------------------------
	// Post-inference: raise feedback, cite laws
	// ---------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Phase 2: Fan-Out Review
// ---------------------------------------------------------------------------

// reviewItem is a single feedback observation from a child Reviewer's output.
type reviewItem struct {
	Message   string   `json:"message"`
	CitedLaws []string `json:"cited_laws"`
}

// reviewOutput is the Go representation of a child Reviewer's review-output
// artefact.
type reviewOutput struct {
	Feedback []reviewItem `json:"feedback"`
}

// fanOutResult holds the complete output of the fan-out review phase,
// including merge feedback, the dispatch matrix, child statuses, and
// resolved group configs for post-processing (stamping, coverage, events).
type fanOutResult struct {
	feedback           []reviewItem
	dispatchMatrix     []flow.DispatchEntry
	unitsByGroup       map[string][]flow.Unit
	childStatuses      []flow.ChildWorkitemStatus
	childResults       []flow.ChildResult
	groups             map[string]*flow.LawGroup
	childByDispatchIdx map[int]string // dispatch matrix index → child workitem ID (empty if skipped)
	skippedIndices     map[int]bool   // indices in dispatchMatrix that were skipped (unknown appraiser)
}

// fanOutAppraisal computes the dispatch matrix, fans out to Reviewer children
// via FanOut/AwaitChildren, collects review-output from completed children,
// merges feedback, and returns the full result for post-processing.
//
//nolint:cyclop,funlen,gocyclo // Orchestration — sequential steps are inherently complex.
func fanOutAppraisal(
	ctx context.Context,
	workitem *flow.Workitem,
	cfg AppraisalConfig,
	existingFeedback []*flow.Feedback,
	inputContent, reviewContent string,
) (*fanOutResult, error) {
	// Step 1: Get law groups with text/markdown representations only.
	// Appraisers receive subjective law content, not code.
	lawGroupList, err := workitem.GetLawGroups("text/markdown")
	if err != nil {
		return nil, fmt.Errorf("appraisal: query laws: %w", err)
	}

	// Step 2: Convert law groups to proto laws and partition by group.
	var allLaws []*flowv1.Law
	for _, lg := range lawGroupList {
		laws, lErr := lg.GetLaws()
		if lErr != nil {
			return nil, fmt.Errorf("appraisal: get laws for group %s: %w", lg.Name(), lErr)
		}
		for _, l := range laws {
			allLaws = append(allLaws, l.PB())
		}
	}
	lawsByGroup := flow.PartitionLawsByGroup(allLaws)

	slog.Info("appraisal: fan-out review",
		"group_count", len(lawGroupList),
		"total_laws", len(allLaws),
	)

	// If no laws, return empty — nothing to review against.
	if len(lawsByGroup) == 0 {
		return &fanOutResult{}, nil
	}
	if len(cfg.Appraisers) == 0 {
		slog.Warn("appraisal: no appraisers configured, skipping fan-out")
		return &fanOutResult{}, nil
	}

	// Step 3: Resolve group configs from GetLawGroups response.
	groups := make(map[string]*flow.LawGroup, len(lawGroupList))
	for _, lg := range lawGroupList {
		groups[lg.Name()] = lg
	}

	// Step 4: Extract appraiser IDs and compute units + dispatch matrix.
	appraiserIDs := make([]string, len(cfg.Appraisers))
	appraiserMap := make(map[string]string, len(cfg.Appraisers))
	for i, a := range cfg.Appraisers {
		appraiserIDs[i] = a.ID
		appraiserMap[a.ID] = a.Personality
	}

	unitsByGroup := flow.ComputeUnits(lawsByGroup, groups)
	for gn, units := range unitsByGroup {
		if len(units) == 0 {
			slog.Info("appraisal: group has no laws, skipping",
				"group", gn)
		}
	}
	dispatchEntries := flow.ComputeDispatchMatrix(unitsByGroup, appraiserIDs, groups)

	if len(dispatchEntries) == 0 {
		slog.Info("appraisal: no dispatch entries — skipping fan-out")
		return &fanOutResult{}, nil
	}

	// Step 5: Serialize shared artefacts (history).
	historyItems := make([]HistoryData, 0, len(existingFeedback))
	for _, fb := range existingFeedback {
		historyItems = append(historyItems, HistoryData{
			State:   fb.GetState().String(),
			Message: fb.GetMessage(),
		})
	}
	historyJSON, err := json.Marshal(historyItems)
	if err != nil {
		return nil, fmt.Errorf("marshal history: %w", err)
	}

	// Step 6: Build FanOutTasks — one per dispatch entry.
	tasks := make([]flow.FanOutTask, 0, len(dispatchEntries))
	skippedIndices := make(map[int]bool)
	for i, de := range dispatchEntries {
		// Skip dispatch entries with missing or unknown appraiser IDs.
		if de.Appraiser == "" || appraiserMap[de.Appraiser] == "" {
			slog.Error("appraisal: unknown appraiser ID in dispatch entry",
				"appraiser_id", de.Appraiser, "group", de.Group)
			skippedIndices[i] = true
			continue
		}
		// Build laws artefact.
		var lawData []LawData
		groupLaws := lawsByGroup[de.Group]
		if de.Unit.Mode == flow.GroupModeLawByLaw {
			// Law-by-law: only the single law for this unit.
			for _, l := range groupLaws {
				if l.GetId() == de.Unit.LawIDs[0] {
					lawData = append(lawData, lawToData(l))
					break
				}
			}
		} else {
			// Bundle: all laws in the group.
			for _, l := range groupLaws {
				lawData = append(lawData, lawToData(l))
			}
		}
		lawsJSON, jErr := json.Marshal(lawData)
		if jErr != nil {
			return nil, fmt.Errorf("marshal laws for group %s: %w", de.Group, jErr)
		}

		// Build appraiser artefact.
		personality := appraiserMap[de.Appraiser]
		appraiserJSON, jErr := json.Marshal(map[string]string{
			"id":          de.Appraiser,
			"personality": personality,
		})
		if jErr != nil {
			return nil, fmt.Errorf("marshal appraiser: %w", jErr)
		}

		// Build pass artefact.
		passes := int(groups[de.Group].Passes())
		passJSON, jErr := json.Marshal(map[string]int{
			"pass": de.Pass,
			"of":   passes,
		})
		if jErr != nil {
			return nil, fmt.Errorf("marshal pass: %w", jErr)
		}

		task := flow.FanOutTask{
			TargetNode: cfg.ReviewerNode,
			Artefacts: []flow.ChildArtefact{
				{ID: "inputs", GovernedArtefact: "review-data", Content: []byte(inputContent)},
				{ID: ArtefactReview, GovernedArtefact: "review-data", Content: []byte(reviewContent)},
				{ID: ArtefactLaws, GovernedArtefact: "review-data", Content: lawsJSON},
				{ID: ArtefactHistory, GovernedArtefact: "review-data", Content: historyJSON},
				{ID: ArtefactAppraiserPersonality, GovernedArtefact: "review-data", Content: appraiserJSON},
				{ID: ArtefactPass, GovernedArtefact: "review-data", Content: passJSON},
			},
		}
		tasks = append(tasks, task)
	}

	if len(skippedIndices) > 0 {
		slog.Warn("appraisal: skipped dispatch entries with unknown appraiser IDs", "count", len(skippedIndices))
	}
	slog.Info("appraisal: fan-out tasks built", "task_count", len(tasks))

	// Step 7: FanOut — create children.
	children, err := workitem.FanOut(tasks)
	if err != nil {
		return nil, fmt.Errorf("fan-out: %w", err)
	}

	// Build map: dispatch matrix index → child workitem ID.
	// Entries that were skipped have an empty string.
	childByDispatchIdx := make(map[int]string, len(dispatchEntries))
	childIdx := 0
	for i := range dispatchEntries {
		if skippedIndices[i] {
			childByDispatchIdx[i] = ""
		} else if childIdx < len(children) {
			childByDispatchIdx[i] = children[childIdx].ID()
			childIdx++
		}
	}

	// Step 8: AwaitAll — wait for all children to reach terminal state.
	statuses, err := workitem.AwaitAll()
	if err != nil {
		return nil, fmt.Errorf("await children: %w", err)
	}

	// Step 9: Collect review-output from completed children.
	var merged []reviewItem
	var childResults []flow.ChildResult

	// Build a set of workitem IDs that are completed.
	completedIDs := make(map[string]bool)
	for _, s := range statuses {
		if s.Phase == flow.PhaseCompleted {
			completedIDs[s.WorkitemID] = true
		}
	}

	for _, s := range statuses {
		if !completedIDs[s.WorkitemID] {
			continue
		}
		// Use FindArtefact to read child artefact by constructing its scoped ID.
		// ponytail: FindArtefact with scoped artefact ID replaces
		// GetChildArtefact. Verify the Archivist supports the
		// "childWID/artefactID" pattern (Phase 10).
		childArt, findErr := workitem.FindArtefact(s.WorkitemID + "/" + ArtefactReviewOutput)
		if findErr != nil {
			slog.Warn("appraisal: child completed but no review-output",
				"workitem_id", s.WorkitemID, "error", findErr)
			childResults = append(childResults, flow.ChildResult{
				Status: s, Artefacts: map[string][]byte{ArtefactReviewOutput: nil},
			})
			continue
		}
		childContent, cErr := childArt.GetContent()
		if cErr != nil {
			slog.Warn("appraisal: child completed but no review-output content",
				"workitem_id", s.WorkitemID, "error", cErr)
			childResults = append(childResults, flow.ChildResult{
				Status: s, Artefacts: map[string][]byte{ArtefactReviewOutput: nil},
			})
			continue
		}
		var out reviewOutput
		if uErr := json.Unmarshal(childContent, &out); uErr != nil {
			return nil, fmt.Errorf("unmarshal review-output from child %s: %w", s.WorkitemID, uErr)
		}
		merged = append(merged, out.Feedback...)
		childResults = append(childResults, flow.ChildResult{
			Status: s, Artefacts: map[string][]byte{ArtefactReviewOutput: childContent},
		})
	}

	slog.Info("appraisal: fan-out complete",
		"children_total", len(statuses),
		"children_completed", len(completedIDs),
		"feedback_items", len(merged))

	return &fanOutResult{
		feedback:           merged,
		dispatchMatrix:     dispatchEntries,
		unitsByGroup:       unitsByGroup,
		childStatuses:      statuses,
		childResults:       childResults,
		groups:             groups,
		childByDispatchIdx: childByDispatchIdx,
		skippedIndices:     skippedIndices,
	}, nil
}

// ---------------------------------------------------------------------------
// Post-fan-out: stamping
// ---------------------------------------------------------------------------

// lawToData converts a proto Law to LawData for serialization to review children.
// Representations are already filtered to text/markdown by the QueryLaws call.
func lawToData(l *flowv1.Law) LawData {
	reps := l.GetRepresentations()
	contents := make([]string, 0, len(reps))
	for _, r := range reps {
		if r.GetContent() != "" {
			contents = append(contents, r.GetContent())
		}
	}
	return LawData{
		ID:              l.GetId(),
		Tier:            int32(l.GetTier()),
		Goal:            l.GetGoal(),
		Representations: contents,
	}
}

// applyAppraisalStamps applies per-group and per-law stamps based on dispatch
// completion. A group/law is stamped only if ALL dispatches for that scope
// completed successfully. Stamping failures are logged but do not fail.
func applyAppraisalStamps(
	ctx context.Context,
	workitem *flow.Workitem,
	governedArtefact string,
	dispatchMatrix []flow.DispatchEntry,
	childStatuses []flow.ChildWorkitemStatus,
	childByDispatchIdx map[int]string,
	unitsByGroup map[string][]flow.Unit,
	groups map[string]*flow.LawGroup,
	skippedIndices map[int]bool,
) {
	// Build a set of failed child workitem IDs.
	failedIDs := make(map[string]bool)
	for _, s := range childStatuses {
		if s.Phase == flow.PhaseFailed {
			failedIDs[s.WorkitemID] = true
		}
	}

	// Map each dispatch entry to its child's workitem ID by index.
	// Entries that were skipped (unknown appraiser) are treated as failed
	// so they don't get stamps.
	entryFailed := make([]bool, len(dispatchMatrix))
	for i := range dispatchMatrix {
		if skippedIndices[i] {
			entryFailed[i] = true
		} else if wid := childByDispatchIdx[i]; wid != "" && failedIDs[wid] {
			entryFailed[i] = true
		}
	}

	// Per-group and per-unit failure tracking.
	groupFailed := make(map[string]bool)
	unitFailed := make(map[string]bool) // unitID → failed
	for i, d := range dispatchMatrix {
		if entryFailed[i] {
			groupFailed[d.Group] = true
			unitFailed[d.Unit.UnitID] = true
		}
	}

	// Fetch the artefact once for stamping.
	art, artErr := workitem.GetArtefact(governedArtefact)
	if artErr != nil {
		slog.Error("appraisal: failed to get artefact for stamping",
			"error", artErr)
		return
	}

	// Apply stamps per group.
	groupOrder := make([]string, 0, len(unitsByGroup))
	for k := range unitsByGroup {
		groupOrder = append(groupOrder, k)
	}
	sort.Strings(groupOrder)
	for _, groupName := range groupOrder {
		unitList := unitsByGroup[groupName]
		if len(unitList) == 0 {
			continue
		}
		groupCfg := groups[groupName]
		if groupCfg == nil {
			groupCfg = flow.NewLawGroup("", flow.GroupModeBundle, 1)
		}

		// Group stamp: only if ALL dispatches for this group completed.
		if !groupFailed[groupName] {
			stampName := fmt.Sprintf("appraise-%s", groupName)
			if err := art.Stamp(stampName); err != nil {
				slog.Warn("appraisal: failed to stamp group",
					"group", groupName, "stamp", stampName, "error", err)
			} else {
				slog.Info("appraisal: group stamp applied", "stamp", stampName)
			}
		} else {
			slog.Warn("appraisal: group has failed dispatches, skipping group stamp",
				"group", groupName)
		}

		// Per-law stamps (law-by-law mode only) — evaluated independently
		// per law, not gated on the group stamp.
		if groupCfg.Mode() == flow.GroupModeLawByLaw {
			for _, unit := range unitList {
				if unitFailed[unit.UnitID] {
					continue
				}
				for _, lawID := range unit.LawIDs {
					lawStamp := fmt.Sprintf("appraise-%s-%s", groupName, lawID)
					if err := art.Stamp(lawStamp); err != nil {
						slog.Warn("appraisal: failed to stamp law",
							"group", groupName, "law", lawID, "stamp", lawStamp, "error", err)
					} else {
						slog.Info("appraisal: law stamp applied", "stamp", lawStamp)
					}
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Post-fan-out: coverage map
// ---------------------------------------------------------------------------

type coverageEntry struct {
	UnitID      string      `json:"unit_id"`
	Group       string      `json:"group"`
	Mode        string      `json:"mode"`
	LawID       string      `json:"law_id"` // empty for bundle
	Evaluations []evalEntry `json:"evaluations"`
	Violations  int         `json:"violations"`
}

type evalEntry struct {
	Appraiser string `json:"appraiser"`
	Pass      int    `json:"pass"`
	Completed bool   `json:"completed"`
	// ponytail: Violations is not in spec R11; kept for debugging and
	// per-appraiser verdict computation. Extra JSON fields are tolerated
	// by tolerant parsers.
	Violations int `json:"violations"`
}

// buildCoverageMap builds a per-unit coverage map from the dispatch matrix,
// child statuses, and child results (review-output).
func buildCoverageMap(
	dispatchMatrix []flow.DispatchEntry,
	childStatuses []flow.ChildWorkitemStatus,
	childResults []flow.ChildResult,
	childByDispatchIdx map[int]string,
) map[string]coverageEntry {
	// Build a map: workitemID → violation count from child results.
	violationsByID := make(map[string]int)
	for _, cr := range childResults {
		raw, ok := cr.Artefacts[ArtefactReviewOutput]
		if !ok || raw == nil {
			continue
		}
		var out reviewOutput
		if err := json.Unmarshal(raw, &out); err != nil {
			continue
		}
		violationsByID[cr.Status.WorkitemID] = len(out.Feedback)
	}

	// Build workitemID → completed map.
	completedByID := make(map[string]bool)
	for _, s := range childStatuses {
		if s.Phase == flow.PhaseCompleted {
			completedByID[s.WorkitemID] = true
		}
	}

	// Group dispatch entries by unit ID.
	type dispatchInfo struct {
		appraiser string
		pass      int
		wid       string // child workitem ID
	}
	dispatchesByUnit := make(map[string][]dispatchInfo)
	for i, d := range dispatchMatrix {
		wid := childByDispatchIdx[i]
		dispatchesByUnit[d.Unit.UnitID] = append(dispatchesByUnit[d.Unit.UnitID], dispatchInfo{
			appraiser: d.Appraiser,
			pass:      d.Pass,
			wid:       wid,
		})
	}

	coverage := make(map[string]coverageEntry, len(dispatchesByUnit))
	for unitID, dispatches := range dispatchesByUnit {
		entry := coverageEntry{
			UnitID: unitID,
		}
		// Look up in dispatchMatrix for group/mode.
		for _, d := range dispatchMatrix {
			if d.Unit.UnitID == unitID {
				entry.Group = d.Group
				entry.Mode = string(d.Unit.Mode)
				if d.Unit.Mode == flow.GroupModeLawByLaw && len(d.Unit.LawIDs) > 0 {
					entry.LawID = d.Unit.LawIDs[0]
				}
				break
			}
		}

		entry.Evaluations = make([]evalEntry, 0, len(dispatches))
		for _, di := range dispatches {
			completed := completedByID[di.wid]
			v := 0
			if completed {
				v = violationsByID[di.wid]
			}
			entry.Evaluations = append(entry.Evaluations, evalEntry{
				Appraiser:  di.appraiser,
				Pass:       di.pass,
				Completed:  completed,
				Violations: v,
			})
			if completed {
				entry.Violations += v
			}
		}
		coverage[unitID] = entry
	}
	return coverage
}

// ---------------------------------------------------------------------------
// Post-fan-out: event emission
// ---------------------------------------------------------------------------

// emitCoverageEvent publishes an appraisal.coverage audit event.
// Errors are logged but do not fail the stage.
func emitCoverageEvent(ctx context.Context, client *flow.Client, coverage map[string]coverageEntry, cycleID string) {
	units := make([]coverageEntry, 0, len(coverage))
	for _, u := range coverage {
		units = append(units, u)
	}
	payload := map[string]any{
		"stage":    "appraisal",
		"cycle_id": cycleID,
		"units":    units,
	}
	if err := client.PublishAuditEvent(ctx,
		EventAppraisalCoverage, payload,
		client.WorkitemID(), client.FlowNamespace(),
	); err != nil {
		slog.Error("appraisal: publish coverage event failed", "error", err)
	} else {
		slog.Info("appraisal: coverage event published")
	}
}

// emitAttestationEvent publishes an appraisal.attestation audit event.
// Errors are logged but do not fail the stage.
func emitAttestationEvent(ctx context.Context, client *flow.Client, coverage map[string]coverageEntry, cycleID string) {
	totalViolations := 0
	totalEvals := 0
	completedEvals := 0
	violationsByAppraiser := make(map[string]int)

	for _, u := range coverage {
		totalViolations += u.Violations
		for _, e := range u.Evaluations {
			totalEvals++
			if e.Completed {
				completedEvals++
			}
			violationsByAppraiser[e.Appraiser] += e.Violations
		}
	}

	// Derive status.
	status := "incomplete"
	if completedEvals > 0 && totalViolations == 0 {
		status = "pass"
	} else if completedEvals > 0 && totalViolations > 0 {
		status = "fail"
	}

	appraiserVerdicts := make([]map[string]string, 0, len(violationsByAppraiser))
	for appraiser, violations := range violationsByAppraiser {
		verdict := "resolved"
		if violations > 0 {
			verdict = "violations"
		}
		appraiserVerdicts = append(appraiserVerdicts, map[string]string{
			"appraiser": appraiser,
			"verdict":   verdict,
		})
	}

	payload := map[string]any{
		"stage":              "appraisal",
		"cycle_id":           cycleID,
		"status":             status,
		"violations_total":   totalViolations,
		"appraiser_verdicts": appraiserVerdicts,
	}
	if err := client.PublishAuditEvent(ctx,
		EventAppraisalAttestation, payload,
		client.WorkitemID(), client.FlowNamespace(),
	); err != nil {
		slog.Error("appraisal: publish attestation event failed", "error", err)
	} else {
		slog.Info("appraisal: attestation event published", "status", status)
	}
}

// ---------------------------------------------------------------------------
// Phase 1: Parallel Fix/Refusal Evaluation
// ---------------------------------------------------------------------------

// evaluateFeedback runs parallel EvalContract calls for ACTIONED and WONT_FIX
// feedback items. Each item gets a focused inference call that decides
// accept or reject.
//
// Returns the subset of feedback items that were resolved (accepted) AND
// carry a NovelArgument justification. These are candidates for Tier 1
// Finding promotion in the learning-capture phase.
func evaluateFeedback(
	ctx context.Context,
	eval flow.EvalContract,
	workitem *flow.Workitem,
	feedback []*flow.Feedback,
	inputContent, reviewContent string,
) ([]*flow.Feedback, error) {
	type evalTask struct {
		fb   *flow.Feedback
		kind string
	}

	var tasks []evalTask
	for _, fb := range feedback {
		switch fb.GetState() {
		case flow.FeedbackStateActioned:
			tasks = append(tasks, evalTask{fb: fb, kind: "actioned"})
		case flow.FeedbackStateWontFix:
			tasks = append(tasks, evalTask{fb: fb, kind: "wont_fix"})
		}
	}

	if len(tasks) == 0 {
		slog.Info("appraisal: no feedback items to evaluate")
		return nil, nil
	}

	slog.Info("appraisal: evaluating feedback items", "count", len(tasks))

	type evalResultItem struct {
		task evalTask
		out  *flow.EvalResult
		err  error
	}

	results := make([]evalResultItem, len(tasks))
	var wg sync.WaitGroup
	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, t evalTask) {
			defer wg.Done()
			// EvalContract.Run takes *flowv1.FeedbackItem — use PB() escape hatch.
			out, err := eval.Run(ctx, t.fb.PB(), inputContent, reviewContent, t.kind)
			results[idx] = evalResultItem{task: t, out: out, err: err}
		}(i, task)
	}
	wg.Wait()

	// Apply verdicts sequentially (gRPC calls to Archivist).
	// Collect resolved items that carry a novel argument justification.
	var novelResolved []*flow.Feedback

	for _, r := range results {
		if r.err != nil {
			return nil, fmt.Errorf(
				"appraisal: eval feedback %s: %w",
				r.task.fb.GetID(), r.err)
		}

		fbID := r.task.fb.GetID()
		state := r.task.fb.GetState()
		protoFB := r.task.fb.PB()

		switch {
		case state == flow.FeedbackStateActioned &&
			r.out.Verdict == verdictAccept:
			slog.Info("appraisal: accepting fix",
				"feedback_id", fbID, "reason", r.out.Reason)
			if err := r.task.fb.AcceptFix(); err != nil {
				return nil, fmt.Errorf("appraisal: accept fix %s: %w", fbID, err)
			}
			if hasNovelArgument(protoFB) {
				novelResolved = append(novelResolved, r.task.fb)
			}

		case state == flow.FeedbackStateActioned &&
			r.out.Verdict == verdictReject:
			slog.Info("appraisal: rejecting fix",
				"feedback_id", fbID, "reason", r.out.Reason)
			if err := r.task.fb.RejectFix(r.out.Reason); err != nil {
				return nil, fmt.Errorf("appraisal: reject fix %s: %w", fbID, err)
			}

		case state == flow.FeedbackStateWontFix &&
			r.out.Verdict == verdictAccept:
			slog.Info("appraisal: accepting refusal",
				"feedback_id", fbID, "reason", r.out.Reason)
			if err := r.task.fb.AcceptRefusal(); err != nil {
				return nil, fmt.Errorf(
					"appraisal: accept refusal %s: %w", fbID, err)
			}
			if hasNovelArgument(protoFB) {
				novelResolved = append(novelResolved, r.task.fb)
			}

		case state == flow.FeedbackStateWontFix &&
			r.out.Verdict == verdictReject:
			slog.Info("appraisal: rejecting refusal",
				"feedback_id", fbID, "reason", r.out.Reason)
			if err := r.task.fb.RejectRefusal(r.out.Reason); err != nil {
				return nil, fmt.Errorf(
					"appraisal: reject refusal %s: %w", fbID, err)
			}
		}
	}

	return novelResolved, nil
}

// ---------------------------------------------------------------------------
// Phase 3: Learning Capture — Mint Tier 1 Findings
// ---------------------------------------------------------------------------

// mintFindings runs the FindingContract agent to distill governance learnings
// from resolved feedback items that carried novel arguments. Each finding is
// recorded as a Tier 1 Finding via the Librarian.
func mintFindings(
	ctx context.Context,
	finding flow.FindingContract,
	client *flow.Client,
	novelItems []*flow.Feedback,
) error {
	slog.Info("appraisal: capturing learnings from resolved "+
		"novel arguments", "count", len(novelItems))

	// Convert domain feedback items to proto for the FindingContract.
	protoItems := make([]*flowv1.FeedbackItem, len(novelItems))
	for i, fb := range novelItems {
		protoItems[i] = fb.PB()
	}

	out, err := finding.Run(ctx, protoItems)
	if err != nil {
		return fmt.Errorf("finding inference: %w", err)
	}
	if out == nil {
		return nil
	}

	slog.Info("appraisal: LLM produced findings",
		"count", len(out.Findings))

	for i, f := range out.Findings {
		lawID, err := client.RecordFinding(
			f.Goal, f.AppliesTo,
			[]*flowv1.Representation{
				{Type: "text/markdown", Content: f.Rationale},
			},
		)
		if err != nil {
			return fmt.Errorf("record finding[%d]: %w", i, err)
		}
		slog.Info("appraisal: minted Tier 1 Finding",
			"law_id", lawID,
			"goal", f.Goal,
			"applies_to", f.AppliesTo,
		)
	}

	return nil
}
