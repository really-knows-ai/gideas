package flow

import (
	"fmt"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// GroupMode is the evaluation mode for a law group.
type GroupMode string

const (
	GroupModeBundle   GroupMode = "bundle"
	GroupModeLawByLaw GroupMode = "law-by-law"
)

// LawGroup defines the evaluation contract for a law group.
type LawGroup struct {
	Name   string
	Mode   GroupMode
	Passes int32
}

// Unit is a single evaluation scope: one bundle unit or one law-by-law unit.
type Unit struct {
	UnitID string // canonical: "<group>::<mode>::<index>"
	Group  string // group name
	Mode   GroupMode
	LawIDs []string // one law for law-by-law, all group laws for bundle
}

// DispatchEntry pairs a unit with an appraiser for a specific pass.
type DispatchEntry struct {
	Group     string
	Unit      Unit
	Appraiser string // appraiser id
	Pass      int    // 1-based
}

// GetGroup returns the group name for a law, or "default" if empty.
func GetGroup(law *flowv1.Law) string {
	if g := law.GetGroup(); g != "" {
		return g
	}
	return "default"
}

// PartitionLawsByGroup groups laws by their Group field.
// Laws with an empty Group are placed under "default".
func PartitionLawsByGroup(laws []*flowv1.Law) map[string][]*flowv1.Law {
	if len(laws) == 0 {
		return map[string][]*flowv1.Law{}
	}
	out := make(map[string][]*flowv1.Law)
	for _, law := range laws {
		g := law.GetGroup()
		if g == "" {
			g = "default"
		}
		out[g] = append(out[g], law)
	}
	return out
}

// builtinDefaults is the default LawGroup config used when a group is absent
// from the groups map.
// ponytail: singleton map; if group-specific defaults become configurable
// this should be a parameter.
var builtinDefaults = &LawGroup{Mode: GroupModeBundle, Passes: 1}

// getConfig returns the LawGroup config for a group name, falling back to
// built-in defaults when the group is absent from the map.
func getConfig(groups map[string]*LawGroup, name string) *LawGroup {
	if g, ok := groups[name]; ok {
		return g
	}
	return builtinDefaults
}

// ComputeUnits builds evaluation units for each law group.
// Bundle mode produces one unit per group containing all law IDs.
// Law-by-law mode produces one unit per law, each containing a single law ID.
// Groups with zero laws produce an empty unit slice.
// Groups absent from the groups map use built-in defaults {mode:bundle, passes:1}.
func ComputeUnits(
	lawsByGroup map[string][]*flowv1.Law,
	groups map[string]*LawGroup,
) map[string][]Unit {
	out := make(map[string][]Unit, len(lawsByGroup))
	for groupName, laws := range lawsByGroup {
		cfg := getConfig(groups, groupName)
		var units []Unit
		switch cfg.Mode {
		case GroupModeLawByLaw:
			for i, law := range laws {
				units = append(units, Unit{
					UnitID: fmt.Sprintf("%s::%s::%d", groupName, cfg.Mode, i),
					Group:  groupName,
					Mode:   cfg.Mode,
					LawIDs: []string{law.GetId()},
				})
			}
		default: // bundle or unknown mode
			if len(laws) > 0 {
				ids := make([]string, 0, len(laws))
				for _, law := range laws {
					ids = append(ids, law.GetId())
				}
				units = append(units, Unit{
					UnitID: fmt.Sprintf("%s::%s::%d", groupName, cfg.Mode, 0),
					Group:  groupName,
					Mode:   cfg.Mode,
					LawIDs: ids,
				})
			}
		}
		out[groupName] = units
	}
	return out
}

// ComputeDispatchMatrix flattens units into a dispatch matrix.
// For each group, for each unit, for each appraiser, for each pass (1..Passes):
// produces one DispatchEntry. Entries carry the full Unit object.
// Nil or empty appraiserIDs produces nil (zero entries).
func ComputeDispatchMatrix(
	unitsByGroup map[string][]Unit,
	appraiserIDs []string,
	groups map[string]*LawGroup,
) []DispatchEntry {
	if len(appraiserIDs) == 0 {
		return nil
	}
	var entries []DispatchEntry
	for groupName, units := range unitsByGroup {
		cfg := getConfig(groups, groupName)
		for _, unit := range units {
			for _, appraiser := range appraiserIDs {
				for pass := int32(1); pass <= cfg.Passes; pass++ {
					entries = append(entries, DispatchEntry{
						Group:     groupName,
						Unit:      unit,
						Appraiser: appraiser,
						Pass:      int(pass),
					})
				}
			}
		}
	}
	return entries
}

// BuildDispatchMatrix is a convenience that chains PartitionLawsByGroup
// -> ComputeUnits -> ComputeDispatchMatrix.
func BuildDispatchMatrix(
	laws []*flowv1.Law,
	groups map[string]*LawGroup,
	appraiserIDs []string,
) []DispatchEntry {
	lawsByGroup := PartitionLawsByGroup(laws)
	unitsByGroup := ComputeUnits(lawsByGroup, groups)
	return ComputeDispatchMatrix(unitsByGroup, appraiserIDs, groups)
}
