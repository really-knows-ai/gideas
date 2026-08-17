package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flow "github.com/foundry/flow/sdk/go"
)

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
