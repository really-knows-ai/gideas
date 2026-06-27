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
		{"hearing-entry has law-reference artefact with empty stamps", func(t *testing.T, ff foundryFlow) {
			entry, ok := ff.Spec.EntryContracts["hearing-entry"]
			if !ok {
				t.Fatal("entryContracts missing 'hearing-entry'")
			}
			artefacts, ok := entry["law-reference"]
			if !ok {
				t.Fatal("hearing-entry missing 'law-reference' artefact")
			}
			if len(artefacts) != 0 {
				t.Errorf("hearing-entry/law-reference expected empty stamps, got %v", artefacts)
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
		{"clerk-exit/petition has appraise-security approval", func(t *testing.T, ff foundryFlow) {
			exit, ok := ff.Spec.ExitContracts["clerk-exit"]
			if !ok {
				t.Fatal("exitContracts missing 'clerk-exit'")
			}
			stamps, ok := exit["petition"]
			if !ok {
				t.Fatal("clerk-exit missing 'petition' artefact")
			}
			expected := []string{"appraise-security", "approval"}
			if len(stamps) != len(expected) {
				t.Fatalf("clerk-exit/petition stamps: want %v, got %v", expected, stamps)
			}
			for i, s := range expected {
				if stamps[i] != s {
					t.Errorf("clerk-exit/petition stamp[%d]: want %q, got %q", i, s, stamps[i])
				}
			}
		}},
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
		requiredGroups := []string{"main-cycle", "judiciary", "clerk-cycle"}
		for _, g := range requiredGroups {
			if _, ok := ff.Spec.NodeGroups[g]; !ok {
				t.Errorf("nodeGroups missing %q", g)
			}
		}
	})

	t.Run("contents", func(t *testing.T) {
		wantMainCycle := []string{
			"forge", "sort", "quench", "appraisal", "appraiser", "refine",
		}
		wantJudiciary := []string{
			"facilitator", "arbiter", "juror", "tribunal",
			"friction-watcher", "ttl-watcher",
			"hitl-appraisal", "arbiter-hitl-resolve", "tribunal-hitl-resolve",
			"law-applicator",
		}
		wantClerkCycle := []string{
			"clerk-forge", "clerk-sort", "clerk-appraisal", "clerk-refine",
			"clerk-facilitator", "codification", "codify-smt",
			"clerk-done-router", "hitl-gate",
		}

		assertGroupEqual(t, ff.Spec.NodeGroups, "main-cycle", wantMainCycle)
		assertGroupEqual(t, ff.Spec.NodeGroups, "judiciary", wantJudiciary)
		assertGroupEqual(t, ff.Spec.NodeGroups, "clerk-cycle", wantClerkCycle)
	})

	t.Run("total_count", func(t *testing.T) {
		total := 0
		for _, group := range ff.Spec.NodeGroups {
			total += len(group.Nodes)
		}
		if total != 25 {
			t.Errorf("total node-group entries: want 25, got %d", total)
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
		{name: "sort/arbiter", nodeID: "sort", outputs: map[string]string{"arbiter": "facilitator"}},
		{name: "facilitator/resolved", nodeID: "facilitator", outputs: map[string]string{"resolved": "sort"}},
		{name: "arbiter/hung", nodeID: "arbiter", outputs: map[string]string{"hung": "arbiter-hitl-resolve"}},
		{name: "juror", nodeID: "juror", empty: true},
		{name: "tribunal/hung", nodeID: "tribunal", outputs: map[string]string{"hung": "tribunal-hitl-resolve"}},
		{name: "friction-watcher/default", nodeID: "friction-watcher", outputs: map[string]string{"default": "tribunal"}},
		{name: "ttl-watcher/default", nodeID: "ttl-watcher", outputs: map[string]string{"default": "tribunal"}},
		{name: "hitl-appraisal/approved", nodeID: "hitl-appraisal", outputs: map[string]string{"approved": "hitl-gate"}},
		{name: "arbiter-hitl-resolve/resolution", nodeID: "arbiter-hitl-resolve",
			outputs: map[string]string{"resolution": "arbiter"}},
		{name: "tribunal-hitl-resolve/resolution", nodeID: "tribunal-hitl-resolve",
			outputs: map[string]string{"resolution": "tribunal"}},
		{name: "clerk-forge/default", nodeID: "clerk-forge", outputs: map[string]string{"default": "codification"}},
		{name: "clerk-sort", nodeID: "clerk-sort", outputs: map[string]string{
			"appraisal": "clerk-appraisal",
			"refine":    "clerk-refine",
			"arbiter":   "clerk-facilitator",
			"done":      "clerk-done-router",
		}, expectCount: 4},
		{name: "clerk-appraisal/default", nodeID: "clerk-appraisal", outputs: map[string]string{"default": "clerk-sort"}},
		{name: "clerk-refine/default", nodeID: "clerk-refine", outputs: map[string]string{"default": "codification"}},
		{name: "clerk-facilitator/resolved", nodeID: "clerk-facilitator",
			outputs: map[string]string{"resolved": "clerk-sort"}},
		{name: "codification/default", nodeID: "codification", outputs: map[string]string{"default": "clerk-sort"}},
		{name: "codify-smt", nodeID: "codify-smt", empty: true},
		{name: "clerk-done-router", nodeID: "clerk-done-router", outputs: map[string]string{
			"law-applicator": "law-applicator",
			"hitl-appraisal": "hitl-appraisal",
		}},
		{name: "hitl-gate/law-applicator", nodeID: "hitl-gate",
			outputs: map[string]string{"law-applicator": "law-applicator"}},
		{name: "law-applicator/embassy", nodeID: "law-applicator", outputs: map[string]string{"embassy": "embassy"}},
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
		{name: "sort/suspend", nodeID: "sort", has: []string{"SUSPEND:workitem"}},
		{name: "facilitator", nodeID: "facilitator", has: []string{
			"SUSPEND:workitem",
			"CREATE:workitem/child",
			"READ:artefact/petition",
			"READ:artefact/haiku",
			"READ:feedback",
			"READ:law",
		}},
		{name: "arbiter", nodeID: "arbiter", has: []string{
			"SUSPEND:workitem",
			"CREATE:workitem/child",
		}},
		{name: "juror", nodeID: "juror", has: []string{
			"READ:artefact/question",
			"READ:artefact/evidence",
			"READ:artefact/allowed-outcomes",
			"READ:artefact/prior-round-reasoning",
			"WRITE:artefact/verdict",
		}},
		{name: "tribunal", nodeID: "tribunal", has: []string{
			"READ:law",
			"CREATE:workitem/child",
		}, hasNot: []string{"SUSPEND:workitem"}},
		{name: "hitl-appraisal/feedback", nodeID: "hitl-appraisal", has: []string{"WRITE:feedback"}},
		{name: "clerk-forge/verdict-context", nodeID: "clerk-forge", has: []string{"READ:artefact/verdict-context"}},
		{name: "clerk-appraisal", nodeID: "clerk-appraisal", has: []string{
			"CREATE:workitem/child",
			"STAMP:artefact/*/appraise-*",
		}, hasNot: []string{"STAMP:artefact/petition/review"}},
		{name: "codification", nodeID: "codification", has: []string{"CREATE:workitem/child"}},
		{name: "appraisal/wildcard", nodeID: "appraisal", has: []string{
			"STAMP:artefact/*/appraise-*",
		}, hasNot: []string{"STAMP:artefact/haiku/appraisal"}},
		{name: "law-applicator/write-law", nodeID: "law-applicator", has: []string{"WRITE:law"}},
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
		image  string
		entry  string
		exit   string
	}{
		{name: "clerk-forge/image", nodeID: "clerk-forge", image: "forge:latest"},
		{name: "friction-watcher/entry", nodeID: "friction-watcher", entry: "hearing-entry"},
		{name: "ttl-watcher/entry", nodeID: "ttl-watcher", entry: "hearing-entry"},
		{name: "hitl-appraisal/exit", nodeID: "hitl-appraisal", exit: "clerk-exit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fn, ok := nodes[tt.nodeID]
			if !ok {
				t.Fatalf("FoundryNode %q not found in flow.yaml", tt.nodeID)
			}
			if tt.image != "" && fn.Spec.Image != tt.image {
				t.Errorf("%q image: want %q, got %q", tt.nodeID, tt.image, fn.Spec.Image)
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
		{"haiku has appraise-* wildcard and not review", "haiku", func(t *testing.T, ga governedArtefact) {
			if !slices.Contains(ga.Spec.Stamps, "appraise-*") {
				t.Error("haiku GovernedArtefact stamps missing 'appraise-*'")
			}
			if slices.Contains(ga.Spec.Stamps, "review") {
				t.Error("haiku GovernedArtefact stamps should NOT contain 'review' (replaced by appraise-*)")
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

	if exit, ok := ff.Spec.ExitContracts["clerk-exit"]; ok {
		for artefact, stamps := range exit {
			for _, s := range stamps {
				if s == "review" {
					t.Errorf("clerk-exit/%q still contains 'review' stamp (should be appraise-security)", artefact)
				}
			}
		}
	}
}
