package manifests_test

import (
	"slices"
	"sort"
	"testing"
)

func TestFoundryFlow_EntryContracts(t *testing.T) {
	ff := findFoundryFlow(t)
	tests := []struct {
		name  string
		check func(*testing.T, foundryFlow)
	}{
		{"standard-entry has petition artefact with empty stamps", func(t *testing.T, ff foundryFlow) {
			entry, ok := ff.Spec.EntryContracts["standard-entry"]
			if !ok {
				t.Fatal("entryContracts missing 'standard-entry'")
			}
			artefacts, ok := entry["petition"]
			if !ok {
				t.Fatal("standard-entry missing 'petition' artefact")
			}
			if len(artefacts) != 0 {
				t.Errorf("standard-entry/petition expected empty stamps, got %v", artefacts)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { tt.check(t, ff) })
	}
}

func TestFoundryFlow_ExitContracts(t *testing.T) {
	ff := findFoundryFlow(t)
	tests := []struct {
		name  string
		check func(*testing.T, foundryFlow)
	}{
		{"standard-exit/haiku has linter appraise-security approval", func(t *testing.T, ff foundryFlow) {
			exit, ok := ff.Spec.ExitContracts["standard-exit"]
			if !ok {
				t.Fatal("exitContracts missing 'standard-exit'")
			}
			stamps, ok := exit["haiku"]
			if !ok {
				t.Fatal("standard-exit missing 'haiku' artefact")
			}
			expected := []string{"linter", "appraise-security", "approval"}
			if len(stamps) != len(expected) {
				t.Fatalf("standard-exit/haiku stamps: want %v, got %v", expected, stamps)
			}
			for i, s := range expected {
				if stamps[i] != s {
					t.Errorf("standard-exit/haiku stamp[%d]: want %q, got %q", i, s, stamps[i])
				}
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { tt.check(t, ff) })
	}
}

func TestFoundryFlow_NodeGroups(t *testing.T) {
	ff := findFoundryFlow(t)

	t.Run("groups_exist", func(t *testing.T) {
		if _, ok := ff.Spec.NodeGroups["main-cycle"]; !ok {
			t.Errorf("nodeGroups missing %q", "main-cycle")
		}
	})

	t.Run("contents", func(t *testing.T) {
		wantMainCycle := []string{
			"forge", "sort", "quench", "appraisal", "appraiser", "refine",
		}
		assertGroupEqual(t, ff.Spec.NodeGroups, "main-cycle", wantMainCycle)
	})

	t.Run("total_count", func(t *testing.T) {
		total := 0
		for _, group := range ff.Spec.NodeGroups {
			total += len(group.Nodes)
		}
		if total != 6 {
			t.Errorf("total node-group entries: want 6, got %d", total)
		}
	})

	t.Run("no_duplicates_across_groups", func(t *testing.T) {
		seen := make(map[string]string)
		for groupName, group := range ff.Spec.NodeGroups {
			for _, node := range group.Nodes {
				if prev, ok := seen[node]; ok {
					t.Errorf("node %q appears in both %q and %q", node, prev, groupName)
				}
				seen[node] = groupName
			}
		}
		allNodes := make([]string, 0, len(seen))
		for n := range seen {
			allNodes = append(allNodes, n)
		}
		sort.Strings(allNodes)
		t.Logf("all %d unique nodes across groups: %v", len(allNodes), allNodes)
	})

	t.Run("preserves_existing_fields", func(t *testing.T) {
		if _, ok := ff.Spec.EntryContracts["standard-entry"]; !ok {
			t.Error("entryContracts missing 'standard-entry'")
		}
		if _, ok := ff.Spec.ExitContracts["standard-exit"]; !ok {
			t.Error("exitContracts missing 'standard-exit'")
		}
		if ff.Spec.GovernancePolicy == nil {
			t.Error("governancePolicy is nil")
		}
		if _, ok := ff.Spec.GovernancePolicy["maxVisits"]; !ok {
			t.Error("governancePolicy missing 'maxVisits'")
		}
	})
}

func TestFoundryNode_Outputs(t *testing.T) {
	nodes := findFoundryNodes(t)
	tests := []struct {
		name        string
		nodeID      string
		outputs     map[string]string
		empty       bool
		expectCount int
	}{
		{name: "forge/default", nodeID: "forge", outputs: map[string]string{"default": "sort"}},
		{name: "quench/default", nodeID: "quench", outputs: map[string]string{"default": "sort"}},
		{name: "appraisal/default", nodeID: "appraisal", outputs: map[string]string{"default": "sort"}},
		{name: "appraiser", nodeID: "appraiser", empty: true},
		{name: "refine/default", nodeID: "refine", outputs: map[string]string{"default": "sort"}},
		{name: "sort/outputs", nodeID: "sort", outputs: map[string]string{
			"quench":    "quench",
			"appraisal": "appraisal",
			"refine":    "refine",
		}, expectCount: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fn, ok := nodes[tt.nodeID]
			if !ok {
				t.Fatalf("FoundryNode %q not found in flow.yaml", tt.nodeID)
			}
			if tt.empty {
				if len(fn.Spec.Outputs) != 0 {
					t.Errorf("%q FoundryNode should have empty outputs, got %d", tt.nodeID, len(fn.Spec.Outputs))
				}
				return
			}
			found := make(map[string]string)
			for _, o := range fn.Spec.Outputs {
				found[o.Name] = o.Target
			}
			for name, target := range tt.outputs {
				got, ok := found[name]
				if !ok {
					t.Errorf("%q missing output %q", tt.nodeID, name)
					continue
				}
				if got != target {
					t.Errorf("%q output %q: want target %q, got %q", tt.nodeID, name, target, got)
				}
			}
			if tt.expectCount > 0 && len(fn.Spec.Outputs) != tt.expectCount {
				t.Errorf("%q output count: want %d, got %d", tt.nodeID, tt.expectCount, len(fn.Spec.Outputs))
			}
		})
	}
}

func TestFoundryNode_Capabilities(t *testing.T) {
	nodes := findFoundryNodes(t)
	tests := []struct {
		name   string
		nodeID string
		has    []string
		hasNot []string
	}{
		{name: "forge", nodeID: "forge", has: []string{
			"READ:artefact", "WRITE:artefact/haiku", "READ:law",
		}},
		{name: "sort", nodeID: "sort", has: []string{
			"READ:artefact/haiku", "READ:feedback", "READ:flow", "STAMP:artefact/haiku/approval",
		}},
		{name: "quench", nodeID: "quench", has: []string{
			"READ:artefact/haiku", "READ:feedback", "WRITE:feedback/new", "STAMP:artefact/haiku/linter",
		}},
		{name: "appraisal", nodeID: "appraisal", has: []string{
			"READ:artefact/petition", "READ:artefact/haiku", "READ:feedback", "READ:law",
			"WRITE:feedback/new", "WRITE:feedback/resolved", "WRITE:feedback/rejected",
			"STAMP:artefact/haiku/appraise-security", "CREATE:workitem/child",
		}},
		{name: "appraiser", nodeID: "appraiser", has: []string{
			"READ:artefact/review-data", "WRITE:artefact/review-data",
		}},
		{name: "refine", nodeID: "refine", has: []string{
			"READ:artefact/petition", "READ:artefact/haiku", "WRITE:artefact/haiku",
			"READ:feedback", "WRITE:feedback/actioned", "WRITE:feedback/wont_fix", "READ:law",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fn, ok := nodes[tt.nodeID]
			if !ok {
				t.Fatalf("FoundryNode %q not found in flow.yaml", tt.nodeID)
			}
			for _, cap := range tt.has {
				if !slices.Contains(fn.Spec.Capabilities, cap) {
					t.Errorf("%q FoundryNode missing capability %q", tt.nodeID, cap)
				}
			}
			for _, cap := range tt.hasNot {
				if slices.Contains(fn.Spec.Capabilities, cap) {
					t.Errorf("%q FoundryNode should NOT have capability %q", tt.nodeID, cap)
				}
			}
		})
	}
}

func TestFoundryNode_ImageAndEntryExit(t *testing.T) {
	nodes := findFoundryNodes(t)
	tests := []struct {
		name   string
		nodeID string
		entry  string
		exit   string
	}{
		{name: "forge/entry", nodeID: "forge", entry: "standard-entry"},
		{name: "sort/exit", nodeID: "sort", exit: "standard-exit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fn, ok := nodes[tt.nodeID]
			if !ok {
				t.Fatalf("FoundryNode %q not found in flow.yaml", tt.nodeID)
			}
			if tt.entry != "" && fn.Spec.Entry != tt.entry {
				t.Errorf("%q entry: want %q, got %q", tt.nodeID, tt.entry, fn.Spec.Entry)
			}
			if tt.exit != "" && fn.Spec.Exit != tt.exit {
				t.Errorf("%q exit: want %q, got %q", tt.nodeID, tt.exit, fn.Spec.Exit)
			}
		})
	}
}

func TestGovernedArtefact_Stamps(t *testing.T) {
	gas := findGovernedArtefacts(t)
	tests := []struct {
		name       string
		artefactID string
		check      func(*testing.T, governedArtefact)
	}{
		{"haiku has appraise-* wildcard", "haiku", func(t *testing.T, ga governedArtefact) {
			if !slices.Contains(ga.Spec.Stamps, "appraise-*") {
				t.Error("haiku GovernedArtefact stamps missing 'appraise-*'")
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ga, ok := gas[tt.artefactID]
			if !ok {
				t.Fatalf("GovernedArtefact %q not found in flow.yaml", tt.artefactID)
			}
			tt.check(t, ga)
		})
	}
}

func TestExitContracts_NoReviewStamp(t *testing.T) {
	ff := findFoundryFlow(t)

	if exit, ok := ff.Spec.ExitContracts["standard-exit"]; ok {
		for artefact, stamps := range exit {
			for _, s := range stamps {
				if s == "review" {
					t.Errorf("standard-exit/%q still contains 'review' stamp (should be appraise-security)", artefact)
				}
			}
		}
	}
}
