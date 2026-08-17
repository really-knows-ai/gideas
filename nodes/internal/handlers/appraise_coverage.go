package handlers

import (
	"encoding/json"

	flow "github.com/foundry/flow/sdk/go"
)

// coverageEntry is a per-unit coverage aggregate in the appraisal.coverage
// audit event. The JSON wire schema is implementation-defined (not a
// SPEC-named surface) — tolerant parsers ignore extra fields.
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
	// Violations is a per-appraiser diagnostic: the count of review
	// findings from this appraiser's completed child. It is not covered
	// by any appraisal SPEC requirement (the coverage-map JSON wire schema
	// is implementation-defined, not a SPEC-named surface) but is consumed
	// by emitAttestationEvent to derive the per-appraiser verdict and the
	// coverage-entry Violations total. Tolerant parsers ignore extra JSON
	// fields, so carrying it here is safe.
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
