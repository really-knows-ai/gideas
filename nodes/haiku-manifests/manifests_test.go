package manifests_test

import (
	"bytes"
	"io"
	"os"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Generic YAML types for parsing Kubernetes manifests
// ---------------------------------------------------------------------------

type k8sObject struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name      string            `yaml:"name"`
		Namespace string            `yaml:"namespace"`
		Labels    map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
}

type foundryFlowSpec struct {
	EntryContracts   map[string]map[string][]string `yaml:"entryContracts"`
	ExitContracts    map[string]map[string][]string `yaml:"exitContracts"`
	GovernancePolicy map[string]any                 `yaml:"governancePolicy"`
	NodeGroups       map[string][]string            `yaml:"nodeGroups"`
}

type foundryFlow struct {
	k8sObject `yaml:",inline"`
	Spec      foundryFlowSpec `yaml:"spec"`
}

// ---------------------------------------------------------------------------
// Helper: parse a multi-document YAML file into a slice of raw documents
// ---------------------------------------------------------------------------

// parseMultiDocYAML reads a multi-document YAML file and returns each document
// as raw bytes. Empty documents (e.g. comment-only separators) are skipped.
func parseMultiDocYAML(t *testing.T, path string) [][]byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var docs [][]byte
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var raw yaml.Node
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decoding YAML from %s: %v", path, err)
		}
		// Re-marshal so we can unmarshal into typed structs later.
		b, err := yaml.Marshal(&raw)
		if err != nil {
			t.Fatalf("re-marshalling YAML node: %v", err)
		}
		docs = append(docs, b)
	}
	return docs
}

// findFoundryFlow locates the single FoundryFlow document in flow.yaml.
func findFoundryFlow(t *testing.T) foundryFlow {
	t.Helper()

	docs := parseMultiDocYAML(t, "flow.yaml")
	for _, doc := range docs {
		var obj k8sObject
		if err := yaml.Unmarshal(doc, &obj); err != nil {
			t.Fatalf("unmarshalling k8sObject: %v", err)
		}
		if obj.Kind == "FoundryFlow" {
			var ff foundryFlow
			if err := yaml.Unmarshal(doc, &ff); err != nil {
				t.Fatalf("unmarshalling FoundryFlow: %v", err)
			}
			return ff
		}
	}
	t.Fatal("no FoundryFlow document found in flow.yaml")
	return foundryFlow{} // unreachable
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestFoundryFlow_HearingEntryContract(t *testing.T) {
	ff := findFoundryFlow(t)

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
}

func TestFoundryFlow_ClerkExitContract(t *testing.T) {
	ff := findFoundryFlow(t)

	exit, ok := ff.Spec.ExitContracts["clerk-exit"]
	if !ok {
		t.Fatal("exitContracts missing 'clerk-exit'")
	}

	stamps, ok := exit["petition"]
	if !ok {
		t.Fatal("clerk-exit missing 'petition' artefact")
	}

	expected := []string{"review", "approval"}
	if len(stamps) != len(expected) {
		t.Fatalf("clerk-exit/petition stamps: want %v, got %v", expected, stamps)
	}
	for i, s := range expected {
		if stamps[i] != s {
			t.Errorf("clerk-exit/petition stamp[%d]: want %q, got %q", i, s, stamps[i])
		}
	}
}

func TestFoundryFlow_NodeGroups_Exist(t *testing.T) {
	ff := findFoundryFlow(t)

	requiredGroups := []string{"main-cycle", "judiciary", "clerk-cycle"}
	for _, g := range requiredGroups {
		if _, ok := ff.Spec.NodeGroups[g]; !ok {
			t.Errorf("nodeGroups missing %q", g)
		}
	}
}

func TestFoundryFlow_NodeGroups_Contents(t *testing.T) {
	ff := findFoundryFlow(t)

	wantMainCycle := []string{
		"forge", "sort", "quench", "appraise", "reviewer", "refine",
	}
	wantJudiciary := []string{
		"facilitator", "arbiter", "juror", "tribunal",
		"friction-watcher", "ttl-watcher",
		"hitl-appraise", "arbiter-hitl-resolve", "tribunal-hitl-resolve",
		"law-applicator",
	}
	wantClerkCycle := []string{
		"clerk-forge", "clerk-sort", "clerk-appraise", "clerk-refine",
		"clerk-facilitator", "codification", "codify-smt",
		"clerk-done-router", "hitl-gate",
	}

	assertGroupEqual(t, ff.Spec.NodeGroups, "main-cycle", wantMainCycle)
	assertGroupEqual(t, ff.Spec.NodeGroups, "judiciary", wantJudiciary)
	assertGroupEqual(t, ff.Spec.NodeGroups, "clerk-cycle", wantClerkCycle)
}

func TestFoundryFlow_NodeGroups_TotalCount(t *testing.T) {
	ff := findFoundryFlow(t)

	total := 0
	for _, members := range ff.Spec.NodeGroups {
		total += len(members)
	}

	// 6 main-cycle + 10 judiciary + 9 clerk-cycle = 25
	if total != 25 {
		t.Errorf("total node-group entries: want 25, got %d", total)
	}
}

func TestFoundryFlow_PreservesExistingFields(t *testing.T) {
	ff := findFoundryFlow(t)

	// standard-entry must still exist
	if _, ok := ff.Spec.EntryContracts["standard-entry"]; !ok {
		t.Error("entryContracts missing 'standard-entry'")
	}

	// standard-exit must still exist
	if _, ok := ff.Spec.ExitContracts["standard-exit"]; !ok {
		t.Error("exitContracts missing 'standard-exit'")
	}

	// governancePolicy must still exist
	if ff.Spec.GovernancePolicy == nil {
		t.Error("governancePolicy is nil")
	}
	if _, ok := ff.Spec.GovernancePolicy["maxVisits"]; !ok {
		t.Error("governancePolicy missing 'maxVisits'")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func assertGroupEqual(t *testing.T, groups map[string][]string, name string, want []string) {
	t.Helper()

	got, ok := groups[name]
	if !ok {
		t.Errorf("nodeGroups missing %q", name)
		return
	}

	// Compare sorted to be order-independent for membership, but also check
	// exact order since the spec defines a deliberate ordering.
	if len(got) != len(want) {
		t.Errorf("nodeGroups[%q]: want %d entries, got %d\n  want: %v\n  got:  %v",
			name, len(want), len(got), want, got)
		return
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("nodeGroups[%q][%d]: want %q, got %q", name, i, want[i], got[i])
		}
	}

	// Also verify no duplicates.
	seen := make(map[string]bool)
	for _, n := range got {
		if seen[n] {
			t.Errorf("nodeGroups[%q] has duplicate entry %q", name, n)
		}
		seen[n] = true
	}
}

// TestFoundryFlow_NodeGroups_NoDuplicatesAcrossGroups verifies that no node
// name appears in more than one group.
func TestFoundryFlow_NodeGroups_NoDuplicatesAcrossGroups(t *testing.T) {
	ff := findFoundryFlow(t)

	seen := make(map[string]string) // node → group
	for group, members := range ff.Spec.NodeGroups {
		for _, node := range members {
			if prev, ok := seen[node]; ok {
				t.Errorf("node %q appears in both %q and %q", node, prev, group)
			}
			seen[node] = group
		}
	}

	// Verify all names are sorted for deterministic output.
	allNodes := make([]string, 0, len(seen))
	for n := range seen {
		allNodes = append(allNodes, n)
	}
	sort.Strings(allNodes)
	t.Logf("all %d unique nodes across groups: %v", len(allNodes), allNodes)
}
