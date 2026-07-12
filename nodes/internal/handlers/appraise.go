package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"

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
	client *flow.Client,
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
		// Cross-Workitem read: the child stores its own review-output under
		// its own workitem ID (bare artefact ID "review-data"), not under a
		// "parentID/childID/review-data" composite key. FindArtefact's
		// string-concatenation convention never matches the Archivist's
		// strict (workitem_id, artefact_id) lookup — TargetWorkitemId is
		// the real cross-Workitem read mechanism (see nodes/internal/tally
		// for the same, working pattern).
		getResp, findErr := client.RawArchivist().GetArtefact(ctx, &flowv1.GetArtefactRequest{
			WorkitemId:       workitem.ID(),
			ArtefactId:       ArtefactReviewOutput,
			TargetWorkitemId: s.WorkitemID,
		})
		if findErr != nil {
			slog.Warn("appraisal: child completed but no review-output",
				"workitem_id", s.WorkitemID, "error", findErr)
			childResults = append(childResults, flow.ChildResult{
				Status: s, Artefacts: map[string][]byte{ArtefactReviewOutput: nil},
			})
			continue
		}
		childContent := getResp.GetContent()
		var out reviewOutput
		if uErr := json.Unmarshal(childContent, &out); uErr != nil {
			return nil, fmt.Errorf("unmarshal review-output from child %s: %w", s.WorkitemID, uErr)
		}
		merged = append(merged, out.Feedback...)
		childResults = append(childResults, flow.ChildResult{
			Status: s, Artefacts: map[string][]byte{ArtefactReviewOutput: childContent},
		})
	}

	// Step 10: Consolidate — multiple reviewers often raise
	// near-identical complaints. Group by cited-laws set and keep
	// one representative per group to avoid swamping refinement
	// with duplicates.
	consolidated := consolidateFeedback(merged)

	slog.Info("appraisal: fan-out complete",
		"children_total", len(statuses),
		"children_completed", len(completedIDs),
		"feedback_items", len(merged),
		"consolidated", len(consolidated))

	return &fanOutResult{
		feedback:           consolidated,
		dispatchMatrix:     dispatchEntries,
		unitsByGroup:       unitsByGroup,
		childStatuses:      statuses,
		childResults:       childResults,
		groups:             groups,
		childByDispatchIdx: childByDispatchIdx,
		skippedIndices:     skippedIndices,
	}, nil
}

// consolidateFeedback groups feedback items by their cited-laws set and keeps
// one representative per group, deduplicating near-identical complaints from
// different reviewers before they reach refinement.
func consolidateFeedback(items []reviewItem) []reviewItem {
	if len(items) <= 1 {
		return items
	}

	type groupKey struct {
		hasLaws bool
		lawsKey string
	}
	groups := make(map[groupKey][]reviewItem, len(items))
	order := make([]groupKey, 0, len(items))

	for _, item := range items {
		lawIDs := item.CitedLaws
		hasLaws := len(lawIDs) > 0
		var lawsKey string
		if hasLaws {
			sorted := make([]string, len(lawIDs))
			copy(sorted, lawIDs)
			sort.Strings(sorted)
			lawsKey = strings.Join(sorted, ",")
		}
		key := groupKey{hasLaws: hasLaws, lawsKey: lawsKey}
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], item)
	}

	result := make([]reviewItem, 0, len(groups))
	for _, key := range order {
		bucket := groups[key]
		best := bucket[0]
		for _, item := range bucket[1:] {
			if len(item.Message) < len(best.Message) {
				best = item
			}
		}
		result = append(result, best)
	}

	return result
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
func emitCoverageEvent(_ context.Context, client *flow.Client, coverage map[string]coverageEntry, cycleID string) {
	units := make([]coverageEntry, 0, len(coverage))
	for _, u := range coverage {
		units = append(units, u)
	}
	payload := map[string]any{
		"stage":    "appraisal",
		"cycle_id": cycleID,
		"units":    units,
	}
	if err := client.PublishAuditEvent(EventAppraisalCoverage, payload,
		os.Getenv(flow.EnvWorkitemID), os.Getenv(flow.EnvFlowNamespace),
	); err != nil {
		slog.Error("appraisal: publish coverage event failed", "error", err)
	} else {
		slog.Info("appraisal: coverage event published")
	}
}

// emitAttestationEvent publishes an appraisal.attestation audit event.
// Errors are logged but do not fail the stage.
func emitAttestationEvent(_ context.Context, client *flow.Client, coverage map[string]coverageEntry, cycleID string) {
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
	if err := client.PublishAuditEvent(EventAppraisalAttestation, payload,
		os.Getenv(flow.EnvWorkitemID), os.Getenv(flow.EnvFlowNamespace),
	); err != nil {
		slog.Error("appraisal: publish attestation event failed", "error", err)
	} else {
		slog.Info("appraisal: attestation event published", "status", status)
	}
}

// ---------------------------------------------------------------------------
// Post-feedback: attestation stamps
// ---------------------------------------------------------------------------

// applyAttestationStamps applies per-law and per-group attestation stamps
// using SDK law.Attest() / group.Attest() methods. Stamps are applied only
// after feedback is raised (R5 requirement). Per-law pass/fail uses two
// signals in combination:
//
//  1. Infrastructure failure — child workitem for a unit did not complete,
//     meaning we have no review outcome at all (fail-closed).
//  2. Content feedback — the review found issues and the feedback item's
//     CitedLaws mentions the specific law.
//
// Either signal alone blocks attestation for that law. This replaces the
// old global HasUnresolvedFeedback gate with per-law granularity.
//
//nolint:gocyclo // Inherent branching: two modes × error paths × law iteration.
func applyAttestationStamps(
	workitem *flow.Workitem,
	governedArtefact string,
	result *fanOutResult,
) error {
	// Build a set of law IDs cited by unresolved feedback from this round.
	// result.feedback items carry CitedLaws from the original review results
	// (the reviewItem struct). This gives per-law granularity: only laws
	// with cited feedback skip attestation, not all laws in the artefact.
	lawsWithFeedback := make(map[string]bool)
	for _, item := range result.feedback {
		for _, lawID := range item.CitedLaws {
			lawsWithFeedback[lawID] = true
		}
	}

	// Infrastructure failure tracking: a child that failed means we have
	// no review outcome at all for that unit.
	failedIDs := make(map[string]bool)
	for _, s := range result.childStatuses {
		if s.Phase == flow.PhaseFailed {
			failedIDs[s.WorkitemID] = true
		}
	}
	entryFailed := make([]bool, len(result.dispatchMatrix))
	for i := range result.dispatchMatrix {
		if result.skippedIndices[i] {
			entryFailed[i] = true
		} else if wid := result.childByDispatchIdx[i]; wid != "" && failedIDs[wid] {
			entryFailed[i] = true
		}
	}
	unitFailed := make(map[string]bool)  // unitID → infrastructure failure
	groupFailed := make(map[string]bool) // groupName → infrastructure OR content failure
	for i, d := range result.dispatchMatrix {
		unitFailed[d.Unit.UnitID] = unitFailed[d.Unit.UnitID] || entryFailed[i]
		if entryFailed[i] {
			groupFailed[d.Group] = true
		}
		for _, lawID := range d.Unit.LawIDs {
			if lawsWithFeedback[lawID] {
				groupFailed[d.Group] = true
				break
			}
		}
	}

	// Fetch the artefact once for stamping.
	art, artErr := workitem.GetArtefact(governedArtefact)
	if artErr != nil {
		return fmt.Errorf("appraisal: get artefact for stamping: %w", artErr)
	}

	// Query law groups for the artefact's representation type.
	groups, err := workitem.GetLawGroups("text/markdown")
	if err != nil {
		return fmt.Errorf("appraisal: get law groups: %w", err)
	}

	// Build group lookup by name.
	groupMap := make(map[string]*flow.LawGroup, len(groups))
	for _, g := range groups {
		groupMap[g.Name()] = g
	}

	// Build completed-child lookup for R3 coverage check.
	completedChild := make(map[string]bool)
	for _, s := range result.childStatuses {
		if s.Phase == flow.PhaseCompleted {
			completedChild[s.WorkitemID] = true
		}
	}

	// Apply attestation stamps per group.
	groupOrder := make([]string, 0, len(result.unitsByGroup))
	for k := range result.unitsByGroup {
		groupOrder = append(groupOrder, k)
	}
	sort.Strings(groupOrder)
	for _, groupName := range groupOrder {
		unitList := result.unitsByGroup[groupName]
		if len(unitList) == 0 {
			continue
		}
		groupCfg := groupMap[groupName]
		if groupCfg == nil {
			// ponytail: group not found in GetLawGroups response — skip
			// attestation. Missing stamps will surface via
			// VerifyLawAttestations at completion time.
			slog.Warn("appraisal: group config not found, skipping attestation",
				"group", groupName)
			continue
		}

		switch groupCfg.Mode() {
		case flow.GroupModeLawByLaw:
			// Law-by-law: process each law independently. Passing laws
			// get individual attestation stamps even when other laws in
			// the same group fail (infrastructure) or have feedback
			// citing them (R5 per-law granularity).
			laws, lErr := groupCfg.GetLaws()
			if lErr != nil {
				slog.Warn("appraisal: get laws for group",
					"group", groupName, "error", lErr)
				continue
			}
			lawMap := make(map[string]*flow.Law, len(laws))
			for _, law := range laws {
				lawMap[law.ID()] = law
			}

			allPassed := true
			for _, unit := range unitList {
				if unitFailed[unit.UnitID] {
					// Infrastructure failure — no review outcome available,
					// fail-closed for all laws in this unit.
					slog.Info("appraisal: unit has infrastructure failure, skipping attestation",
						"unit", unit.UnitID, "group", groupName)
					allPassed = false
					continue
				}
				for _, lawID := range unit.LawIDs {
					if lawsWithFeedback[lawID] {
						// Law has unresolved feedback — skip attestation
						// for this law but continue with others.
						slog.Info("appraisal: law has unresolved feedback, skipping attestation",
							"law", lawID, "group", groupName)
						allPassed = false
						continue
					}
					law, ok := lawMap[lawID]
					if !ok {
						slog.Warn("appraisal: law not found in group",
							"law", lawID, "group", groupName)
						allPassed = false
						continue
					}
					// Only attest text/markdown — the only representation type
					// Appraisal evaluates. Other rep types (e.g. text/plain)
					// are attested by the node that evaluates them (e.g. Quench).
					for _, rep := range law.GetRepresentations() {
						if rep.GetType() != "text/markdown" {
							continue
						}
						if aErr := law.Attest(art, rep.GetType()); aErr != nil {
							slog.Warn("appraisal: law attest failed",
								"law", lawID, "rep", rep.GetType(), "error", aErr)
						} else {
							slog.Info("appraisal: law attestation applied",
								"law", lawID, "rep", rep.GetType())
						}
					}
				}
			}
			// Group attest stamp only when all laws passed, no
			// infrastructure failure, and no content feedback.
			if allPassed && !groupFailed[groupName] {
				if gErr := groupCfg.Attest(art); gErr != nil {
					slog.Warn("appraisal: group attest failed",
						"group", groupName, "error", gErr)
				} else {
					slog.Info("appraisal: group attestation applied", "group", groupName)
				}
			}

		case flow.GroupModeBundle:
			// Bundle mode: group attest covers all laws. Skip if any
			// law has unresolved feedback or infrastructure failure.
			if groupFailed[groupName] {
				slog.Warn("appraisal: group has failures (infrastructure or feedback), skipping group attest",
					"group", groupName)
				continue
			}

			// R3 ponytail enforcement: verify that every law in the group
			// had every representation covered by a completed appraiser
			// dispatch. Fail-closed — no silent skips.
			laws, lErr := groupCfg.GetLaws()
			if lErr != nil {
				slog.Warn("appraisal: get laws for group",
					"group", groupName, "error", lErr)
				continue
			}
			if !hasBundleCoverage(unitList, result, completedChild, laws) {
				slog.Warn("appraisal: bundle dispatch coverage incomplete, skipping group attest",
					"group", groupName)
				continue
			}

			if gErr := groupCfg.Attest(art); gErr != nil {
				slog.Warn("appraisal: group attest failed",
					"group", groupName, "error", gErr)
			} else {
				slog.Info("appraisal: group attestation applied", "group", groupName)
			}
		}
	}

	// Overall completion stamp: always applied as a record that Appraisal
	// ran its orchestration, regardless of dispatch outcomes.
	if err := art.Stamp(stampAppraisal); err != nil {
		return fmt.Errorf("appraisal: stamp %s: %w", stampAppraisal, err)
	}
	slog.Info("appraisal: completion stamp applied", "stamp", stampAppraisal)

	return nil
}

// hasBundleCoverage verifies that in bundle mode, every representation of
// every law was covered by at least one completed dispatch. This is a
// fail-closed check per R3 ponytail enforcement: if any representation was
// not dispatched or the child did not complete, the group is not attested.
//
// ponytail: Bundle mode assumes all laws in a group are dispatched as a single
// unit (one Unit with all LawIDs). Coverage is verified against that unit.
func hasBundleCoverage(
	unitList []flow.Unit,
	result *fanOutResult,
	completedChild map[string]bool,
	laws []*flow.Law,
) bool {
	// Find the bundle unit containing these laws.
	var bundleUnit *flow.Unit
	for i, u := range unitList {
		if u.Mode != flow.GroupModeBundle {
			continue
		}
		bundleUnit = &unitList[i]
		break
	}
	if bundleUnit == nil {
		return false
	}

	// Verify that every representation of every law was covered by at
	// least one completed dispatch entry. Fail-closed: if a law has a
	// representation type that Appraisal does not evaluate (e.g. text/plain
	// in a bundle group), no dispatch entry will match and the check fails,
	// correctly blocking attestation. Bundle groups must be homogeneous in
	// representation type — heterogeneous groups must use law-by-law mode.
	for _, law := range laws {
		for range law.GetRepresentations() {
			covered := false
			for i, d := range result.dispatchMatrix {
				if d.Unit.UnitID != bundleUnit.UnitID {
					continue
				}
				if result.skippedIndices[i] {
					continue
				}
				wid := result.childByDispatchIdx[i]
				if wid != "" && completedChild[wid] {
					covered = true
					break
				}
			}
			if !covered {
				return false
			}
		}
	}
	return true
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
	feedback []*flow.Feedback,
	fanOutObservations []reviewItem,
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

	// Build a summary of fresh observations from the fan-out review so the
	// eval agent can see whether a fix merely moved a violation to a different
	// law rather than resolving it.
	var augmentedReviewContent string
	if len(fanOutObservations) > 0 {
		var obs strings.Builder
		obs.WriteString("\n\n--- Fresh review observations (NOT yet raised as feedback) ---\n")
		for _, o := range fanOutObservations {
			obs.WriteString("- " + o.Message)
			if len(o.CitedLaws) > 0 {
				obs.WriteString(" [cited: " + strings.Join(o.CitedLaws, ", ") + "]")
			}
			obs.WriteString("\n")
		}
		augmentedReviewContent = reviewContent + obs.String()
	} else {
		augmentedReviewContent = reviewContent
	}

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
			out, err := eval.Run(ctx, t.fb.PB(), inputContent, augmentedReviewContent, t.kind)
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
