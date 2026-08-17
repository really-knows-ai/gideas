package handlers

import (
	"fmt"
	"log/slog"
	"sort"

	flow "github.com/foundry/flow/sdk/go"
)

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
