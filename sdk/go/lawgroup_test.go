package flow

import (
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

const (
	lawL001 = "L001"
	lawL002 = "L002"
	lawL003 = "L003"
	lawL004 = "L004"
)

// ---------------------------------------------------------------------------
// PartitionLawsByGroup
// ---------------------------------------------------------------------------

func TestPartitionLawsByGroup_EmptyGroupFallsBack(t *testing.T) {
	laws := []*flowv1.Law{
		{Id: lawL001, Goal: "law 1"},
		{Id: lawL002, Group: "security"},
		{Id: lawL003, Goal: "law 3"},
	}
	got := PartitionLawsByGroup(laws)

	if len(got) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(got))
	}
	defLaws := got["default"]
	if len(defLaws) != 2 {
		t.Fatalf("expected 2 laws in default, got %d", len(defLaws))
	}
	if defLaws[0].GetId() != lawL001 {
		t.Fatalf("expected L001 first in default, got %s", defLaws[0].GetId())
	}
	if defLaws[1].GetId() != lawL003 {
		t.Fatalf("expected L003 second in default, got %s", defLaws[1].GetId())
	}
	secLaws := got["security"]
	if len(secLaws) != 1 || secLaws[0].GetId() != lawL002 {
		t.Fatalf("unexpected security group laws: %v", secLaws)
	}
}

func TestPartitionLawsByGroup_MixedGroups(t *testing.T) {
	laws := []*flowv1.Law{
		{Id: lawL001, Group: "security"},
		{Id: lawL002, Group: "style"},
		{Id: lawL003, Group: "security"},
		{Id: lawL004, Group: "performance"},
	}
	got := PartitionLawsByGroup(laws)

	if len(got) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(got))
	}
	if len(got["security"]) != 2 {
		t.Fatalf("expected 2 security laws, got %d", len(got["security"]))
	}
	if got["security"][0].GetId() != lawL001 {
		t.Fatalf("expected L001 first in security")
	}
	if got["security"][1].GetId() != lawL003 {
		t.Fatalf("expected L003 second in security")
	}
	if len(got["style"]) != 1 || got["style"][0].GetId() != lawL002 {
		t.Fatalf("unexpected style group")
	}
	if len(got["performance"]) != 1 || got["performance"][0].GetId() != lawL004 {
		t.Fatalf("unexpected performance group")
	}
}

func TestPartitionLawsByGroup_EmptyInput(t *testing.T) {
	got := PartitionLawsByGroup(nil)
	if len(got) != 0 {
		t.Fatalf("expected empty map for nil input, got %d entries", len(got))
	}
	got = PartitionLawsByGroup([]*flowv1.Law{})
	if len(got) != 0 {
		t.Fatalf("expected empty map for empty input, got %d entries", len(got))
	}
}

// ---------------------------------------------------------------------------
// ComputeUnits
// ---------------------------------------------------------------------------

func TestComputeUnits_BundleMode(t *testing.T) {
	lawsByGroup := map[string][]*flowv1.Law{
		"security": {
			{Id: lawL001},
			{Id: lawL002},
		},
		"style": {
			{Id: lawL003},
		},
	}
	groups := map[string]*LawGroup{
		"security": {Name: "security", Mode: GroupModeBundle, Passes: 1},
		"style":    {Name: "style", Mode: GroupModeBundle, Passes: 2},
	}
	got := ComputeUnits(lawsByGroup, groups)

	secUnits := got["security"]
	if len(secUnits) != 1 {
		t.Fatalf("expected 1 unit for security bundle, got %d", len(secUnits))
	}
	if len(secUnits[0].LawIDs) != 2 {
		t.Fatalf("expected 2 law IDs in security unit, got %d", len(secUnits[0].LawIDs))
	}
	if secUnits[0].LawIDs[0] != lawL001 || secUnits[0].LawIDs[1] != lawL002 {
		t.Fatalf("unexpected law IDs in security unit: %v", secUnits[0].LawIDs)
	}
	if secUnits[0].UnitID != "security::bundle::0" {
		t.Fatalf("unexpected unit ID: %s", secUnits[0].UnitID)
	}
	if secUnits[0].Mode != GroupModeBundle {
		t.Fatalf("expected bundle mode, got %s", secUnits[0].Mode)
	}

	styleUnits := got["style"]
	if len(styleUnits) != 1 {
		t.Fatalf("expected 1 unit for style bundle, got %d", len(styleUnits))
	}
	if len(styleUnits[0].LawIDs) != 1 || styleUnits[0].LawIDs[0] != lawL003 {
		t.Fatalf("unexpected style unit law IDs: %v", styleUnits[0].LawIDs)
	}
}

func TestComputeUnits_LawByLawMode(t *testing.T) {
	lawsByGroup := map[string][]*flowv1.Law{
		"security": {
			{Id: lawL001},
			{Id: lawL002},
			{Id: lawL003},
		},
	}
	groups := map[string]*LawGroup{
		"security": {Name: "security", Mode: GroupModeLawByLaw, Passes: 2},
	}
	got := ComputeUnits(lawsByGroup, groups)

	secUnits := got["security"]
	if len(secUnits) != 3 {
		t.Fatalf("expected 3 units for law-by-law, got %d", len(secUnits))
	}
	for i, unit := range secUnits {
		switch i {
		case 0:
			if unit.UnitID != "security::law-by-law::0" {
				t.Fatalf("unexpected unit ID at index 0: %s", unit.UnitID)
			}
		case 1:
			if unit.UnitID != "security::law-by-law::1" {
				t.Fatalf("unexpected unit ID at index 1: %s", unit.UnitID)
			}
		case 2:
			if unit.UnitID != "security::law-by-law::2" {
				t.Fatalf("unexpected unit ID at index 2: %s", unit.UnitID)
			}
		}
		if len(unit.LawIDs) != 1 {
			t.Fatalf("expected 1 law ID at index %d, got %d", i, len(unit.LawIDs))
		}
		if unit.Mode != GroupModeLawByLaw {
			t.Fatalf("expected law-by-law mode at index %d", i)
		}
	}
	expectedIDs := map[string]string{
		"security::law-by-law::0": lawL001,
		"security::law-by-law::1": lawL002,
		"security::law-by-law::2": lawL003,
	}
	for _, unit := range secUnits {
		wantLawID, ok := expectedIDs[unit.UnitID]
		if !ok {
			t.Fatalf("unexpected unit ID: %s", unit.UnitID)
		}
		if unit.LawIDs[0] != wantLawID {
			t.Fatalf("unit %s has law %s, want %s", unit.UnitID, unit.LawIDs[0], wantLawID)
		}
	}
}

func TestComputeUnits_EmptyGroup(t *testing.T) {
	lawsByGroup := map[string][]*flowv1.Law{
		"security": {},
		"style":    {{Id: lawL003}},
	}
	groups := map[string]*LawGroup{
		"security": {Name: "security", Mode: GroupModeBundle, Passes: 1},
	}
	got := ComputeUnits(lawsByGroup, groups)

	if len(got["security"]) != 0 {
		t.Fatalf("expected 0 units for empty group, got %d", len(got["security"]))
	}
	if len(got["style"]) != 1 {
		t.Fatalf("expected 1 unit for style, got %d", len(got["style"]))
	}
}

func TestComputeUnits_GroupAbsentFromMap(t *testing.T) {
	lawsByGroup := map[string][]*flowv1.Law{
		"security": {
			{Id: lawL001},
			{Id: lawL002},
		},
		"unconfigured": {
			{Id: lawL003},
		},
	}
	groups := map[string]*LawGroup{
		"security": {Name: "security", Mode: GroupModeBundle, Passes: 1},
	}
	got := ComputeUnits(lawsByGroup, groups)

	uncUnits := got["unconfigured"]
	if len(uncUnits) != 1 {
		t.Fatalf("expected 1 unit for absent group (bundle default), got %d", len(uncUnits))
	}
	if uncUnits[0].Mode != GroupModeBundle {
		t.Fatalf("expected bundle mode for absent group, got %s", uncUnits[0].Mode)
	}
	if len(uncUnits[0].LawIDs) != 1 || uncUnits[0].LawIDs[0] != lawL003 {
		t.Fatalf("unexpected law IDs in absent group unit: %v", uncUnits[0].LawIDs)
	}
	if uncUnits[0].UnitID != "unconfigured::bundle::0" {
		t.Fatalf("unexpected unit ID: %s", uncUnits[0].UnitID)
	}

	secUnits := got["security"]
	if len(secUnits) != 1 {
		t.Fatalf("expected 1 unit for security, got %d", len(secUnits))
	}
	if len(secUnits[0].LawIDs) != 2 {
		t.Fatalf("expected 2 law IDs in security, got %d", len(secUnits[0].LawIDs))
	}
}

// ---------------------------------------------------------------------------
// ComputeDispatchMatrix
// ---------------------------------------------------------------------------

func TestComputeDispatchMatrix_Count(t *testing.T) {
	unitsByGroup := map[string][]Unit{
		"security": {
			{UnitID: "security::bundle::0", Group: "security", Mode: GroupModeBundle, LawIDs: []string{lawL001, lawL002}},
		},
		"style": {
			{UnitID: "style::law-by-law::0", Group: "style", Mode: GroupModeLawByLaw, LawIDs: []string{lawL003}},
			{UnitID: "style::law-by-law::1", Group: "style", Mode: GroupModeLawByLaw, LawIDs: []string{lawL004}},
		},
	}
	groups := map[string]*LawGroup{
		"security": {Name: "security", Mode: GroupModeBundle, Passes: 3},
		"style":    {Name: "style", Mode: GroupModeLawByLaw, Passes: 2},
	}
	appraiserIDs := []string{"skeptic", "auditor"}

	got := ComputeDispatchMatrix(unitsByGroup, appraiserIDs, groups)

	// security: 1 unit × 2 appraisers × 3 passes = 6.
	// style:    2 units × 2 appraisers × 2 passes = 8.
	// total: 14.
	if len(got) != 14 {
		t.Fatalf("expected 14 dispatch entries, got %d", len(got))
	}
}

func TestComputeDispatchMatrix_PassOneBased(t *testing.T) {
	unitsByGroup := map[string][]Unit{
		"security": {
			{UnitID: "security::bundle::0", Group: "security", Mode: GroupModeBundle, LawIDs: []string{lawL001}},
		},
	}
	groups := map[string]*LawGroup{
		"security": {Name: "security", Mode: GroupModeBundle, Passes: 3},
	}
	appraiserIDs := []string{"skeptic"}

	got := ComputeDispatchMatrix(unitsByGroup, appraiserIDs, groups)

	if len(got) != 3 {
		t.Fatalf("expected 3 entries (1 unit × 1 appraiser × 3 passes), got %d", len(got))
	}
	for i, entry := range got {
		want := i + 1
		if entry.Pass != want {
			t.Fatalf("entry %d has pass=%d, want %d", i, entry.Pass, want)
		}
	}
	if got[0].Pass != 1 || got[1].Pass != 2 || got[2].Pass != 3 {
		t.Fatalf("pass indices not sequential 1,2,3: got %d,%d,%d", got[0].Pass, got[1].Pass, got[2].Pass)
	}
}

func TestComputeDispatchMatrix_EmptyAppraisers(t *testing.T) {
	unitsByGroup := map[string][]Unit{
		"security": {
			{UnitID: "security::bundle::0", Group: "security", Mode: GroupModeBundle, LawIDs: []string{lawL001}},
		},
	}
	groups := map[string]*LawGroup{
		"security": {Name: "security", Mode: GroupModeBundle, Passes: 1},
	}

	got := ComputeDispatchMatrix(unitsByGroup, nil, groups)
	if got != nil {
		t.Fatalf("expected nil for nil appraiser list, got %d entries", len(got))
	}
	got = ComputeDispatchMatrix(unitsByGroup, []string{}, groups)
	if got != nil {
		t.Fatalf("expected nil for empty appraiser list, got %d entries", len(got))
	}
}

func TestComputeDispatchMatrix_EmptyUnits(t *testing.T) {
	appraiserIDs := []string{"skeptic"}
	groups := map[string]*LawGroup{
		"security": {Name: "security", Mode: GroupModeBundle, Passes: 1},
	}

	got := ComputeDispatchMatrix(nil, appraiserIDs, groups)
	if len(got) != 0 {
		t.Fatalf("expected 0 entries for nil units, got %d", len(got))
	}
	got = ComputeDispatchMatrix(map[string][]Unit{}, appraiserIDs, groups)
	if len(got) != 0 {
		t.Fatalf("expected 0 entries for empty units, got %d", len(got))
	}
}

func TestComputeDispatchMatrix_EmptyUnitSlice(t *testing.T) {
	unitsByGroup := map[string][]Unit{
		"security": {}, // empty unit slice
		"style": {
			{UnitID: "style::bundle::0", Group: "style", Mode: GroupModeBundle, LawIDs: []string{lawL001}},
		},
	}
	groups := map[string]*LawGroup{
		"security": {Name: "security", Mode: GroupModeBundle, Passes: 1},
		"style":    {Name: "style", Mode: GroupModeBundle, Passes: 1},
	}
	appraiserIDs := []string{"skeptic"}

	got := ComputeDispatchMatrix(unitsByGroup, appraiserIDs, groups)

	// security has 0 units → skipped, style has 1 × 1 × 1 = 1 entry.
	if len(got) != 1 {
		t.Fatalf("expected 1 entry (only style group), got %d", len(got))
	}
	if got[0].Group != "style" {
		t.Fatalf("expected entry for style group, got %s", got[0].Group)
	}
}

// ---------------------------------------------------------------------------
// BuildDispatchMatrix (integration)
// ---------------------------------------------------------------------------

func TestBuildDispatchMatrix_Integration(t *testing.T) {
	laws := []*flowv1.Law{
		{Id: lawL001, Group: "security"},
		{Id: lawL002, Group: "security"},
		{Id: lawL003, Group: "style"},
		{Id: lawL004}, // empty group → "default"
	}
	groups := map[string]*LawGroup{
		"security": {Name: "security", Mode: GroupModeLawByLaw, Passes: 2},
		"style":    {Name: "style", Mode: GroupModeBundle, Passes: 1},
	}
	appraiserIDs := []string{"skeptic", "auditor"}

	got := BuildDispatchMatrix(laws, groups, appraiserIDs)

	// security: law-by-law, 2 laws × 2 appraisers × 2 passes = 8.
	// style:    bundle, 1 × 2 appraisers × 1 pass = 2.
	// default:  bundle (default), 1 × 2 appraisers × 1 pass = 2.
	// total: 12.
	if len(got) != 12 {
		t.Fatalf("expected 12 dispatch entries, got %d", len(got))
	}

	groupsSeen := make(map[string]int)
	for _, e := range got {
		groupsSeen[e.Group]++
	}
	if groupsSeen["security"] != 8 {
		t.Fatalf("expected 8 security entries, got %d", groupsSeen["security"])
	}
	if groupsSeen["style"] != 2 {
		t.Fatalf("expected 2 style entries, got %d", groupsSeen["style"])
	}
	if groupsSeen["default"] != 2 {
		t.Fatalf("expected 2 default entries, got %d", groupsSeen["default"])
	}
}

// ---------------------------------------------------------------------------
// Additional edge cases
// ---------------------------------------------------------------------------

func TestComputeDispatchMatrix_GroupAbsentForPassCount(t *testing.T) {
	// When a group has units but is absent from the groups map, pass count
	// defaults to 1 (built-in defaults).
	unitsByGroup := map[string][]Unit{
		"unknown": {
			{UnitID: "unknown::bundle::0", Group: "unknown", Mode: GroupModeBundle, LawIDs: []string{lawL001}},
		},
	}
	appraiserIDs := []string{"skeptic"}
	got := ComputeDispatchMatrix(unitsByGroup, appraiserIDs, nil)

	if len(got) != 1 {
		t.Fatalf("expected 1 entry (1 unit × 1 appraiser × 1 pass default), got %d", len(got))
	}
	if got[0].Pass != 1 {
		t.Fatalf("expected pass=1 from built-in defaults, got %d", got[0].Pass)
	}
}
