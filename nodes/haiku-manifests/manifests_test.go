package manifests_test

import (
	"bytes"
	"io"
	"os"
	"slices"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"
)

const containerNameSidecar = "sidecar"

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
// FoundryNode types
// ---------------------------------------------------------------------------

type foundryNodeOutput struct {
	Name   string `yaml:"name"`
	Target string `yaml:"target"`
}

type foundryNodeSpec struct {
	Image        string              `yaml:"image"`
	Entry        string              `yaml:"entry,omitempty"`
	Exit         string              `yaml:"exit,omitempty"`
	Outputs      []foundryNodeOutput `yaml:"outputs"`
	Capabilities []string            `yaml:"capabilities"`
}

type foundryNode struct {
	k8sObject `yaml:",inline"`
	Spec      foundryNodeSpec `yaml:"spec"`
}

// ---------------------------------------------------------------------------
// Deployment types
// ---------------------------------------------------------------------------

type envVar struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type volumeMount struct {
	Name      string `yaml:"name"`
	MountPath string `yaml:"mountPath"`
	ReadOnly  bool   `yaml:"readOnly"`
}

type deploymentContainer struct {
	Name         string        `yaml:"name"`
	Image        string        `yaml:"image"`
	Env          []envVar      `yaml:"env"`
	VolumeMounts []volumeMount `yaml:"volumeMounts"`
}

type configMapVolumeSource struct {
	Name string `yaml:"name"`
}

type volume struct {
	Name      string                 `yaml:"name"`
	ConfigMap *configMapVolumeSource `yaml:"configMap,omitempty"`
}

type deploymentSpec struct {
	Template struct {
		Metadata struct {
			Labels map[string]string `yaml:"labels"`
		} `yaml:"metadata"`
		Spec struct {
			Containers []deploymentContainer `yaml:"containers"`
			Volumes    []volume              `yaml:"volumes"`
		} `yaml:"spec"`
	} `yaml:"template"`
}

type deployment struct {
	k8sObject `yaml:",inline"`
	Spec      deploymentSpec `yaml:"spec"`
}

// ---------------------------------------------------------------------------
// Helpers: FoundryNode and Deployment finders
// ---------------------------------------------------------------------------

// findFoundryNodes parses flow.yaml and returns a map of name → foundryNode
// for every document with Kind == "FoundryNode".
func findFoundryNodes(t *testing.T) map[string]foundryNode {
	t.Helper()

	docs := parseMultiDocYAML(t, "flow.yaml")
	nodes := make(map[string]foundryNode)
	for _, doc := range docs {
		var obj k8sObject
		if err := yaml.Unmarshal(doc, &obj); err != nil {
			t.Fatalf("unmarshalling k8sObject: %v", err)
		}
		if obj.Kind == "FoundryNode" {
			var fn foundryNode
			if err := yaml.Unmarshal(doc, &fn); err != nil {
				t.Fatalf("unmarshalling FoundryNode %q: %v", obj.Metadata.Name, err)
			}
			nodes[fn.Metadata.Name] = fn
		}
	}
	return nodes
}

// findDeployments parses deployments.yaml and returns a map keyed by the
// FLOW_NODE_ID env var value from the sidecar container.
func findDeployments(t *testing.T) map[string]deployment {
	t.Helper()

	docs := parseMultiDocYAML(t, "deployments.yaml")
	deps := make(map[string]deployment)
	for _, doc := range docs {
		var obj k8sObject
		if err := yaml.Unmarshal(doc, &obj); err != nil {
			t.Fatalf("unmarshalling k8sObject: %v", err)
		}
		if obj.Kind == "Deployment" {
			var d deployment
			if err := yaml.Unmarshal(doc, &d); err != nil {
				t.Fatalf("unmarshalling Deployment %q: %v", obj.Metadata.Name, err)
			}
			// Key by FLOW_NODE_ID from the sidecar container.
			for _, c := range d.Spec.Template.Spec.Containers {
				if c.Name == containerNameSidecar {
					for _, e := range c.Env {
						if e.Name == "FLOW_NODE_ID" {
							deps[e.Value] = d
							break
						}
					}
				}
			}
		}
	}
	return deps
}

// ---------------------------------------------------------------------------
// ConfigMap types
// ---------------------------------------------------------------------------

type configMap struct {
	k8sObject `yaml:",inline"`
	Data      map[string]string `yaml:"data"`
}

// findConfigMaps parses configmaps.yaml and returns a map of name → configMap
// for every document with Kind == "ConfigMap".
func findConfigMaps(t *testing.T) map[string]configMap {
	t.Helper()

	docs := parseMultiDocYAML(t, "configmaps.yaml")
	cms := make(map[string]configMap)
	for _, doc := range docs {
		var obj k8sObject
		if err := yaml.Unmarshal(doc, &obj); err != nil {
			t.Fatalf("unmarshalling k8sObject: %v", err)
		}
		if obj.Kind == "ConfigMap" {
			var cm configMap
			if err := yaml.Unmarshal(doc, &cm); err != nil {
				t.Fatalf("unmarshalling ConfigMap %q: %v", obj.Metadata.Name, err)
			}
			cms[cm.Metadata.Name] = cm
		}
	}
	return cms
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

// ---------------------------------------------------------------------------
// Sort FoundryNode – judiciary outputs & suspend capability
// ---------------------------------------------------------------------------

func TestSort_HasArbiterOutput(t *testing.T) {
	nodes := findFoundryNodes(t)
	sn, ok := nodes["sort"]
	if !ok {
		t.Fatal("FoundryNode 'sort' not found in flow.yaml")
	}

	for _, o := range sn.Spec.Outputs {
		if o.Name == "arbiter" {
			if o.Target != "facilitator" {
				t.Errorf("sort output 'arbiter': want target %q, got %q", "facilitator", o.Target)
			}
			return
		}
	}
	t.Error("sort FoundryNode missing output 'arbiter'")
}

func TestSort_HasSuspendCapability(t *testing.T) {
	nodes := findFoundryNodes(t)
	sn, ok := nodes["sort"]
	if !ok {
		t.Fatal("FoundryNode 'sort' not found in flow.yaml")
	}

	if slices.Contains(sn.Spec.Capabilities, "SUSPEND:workitem") {
		return
	}
	t.Error("sort FoundryNode missing capability 'SUSPEND:workitem'")
}

func TestSort_SidecarHasLibrarianAddress(t *testing.T) {
	deps := findDeployments(t)
	d, ok := deps["sort"]
	if !ok {
		t.Fatal("Deployment with FLOW_NODE_ID='sort' not found in deployments.yaml")
	}

	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Name == containerNameSidecar {
			for _, e := range c.Env {
				if e.Name == "LIBRARIAN_ADDRESS" {
					if e.Value != "flow-librarian:50058" {
						t.Errorf("LIBRARIAN_ADDRESS: want %q, got %q",
							"flow-librarian:50058", e.Value)
					}
					return
				}
			}
			t.Error("sort sidecar container missing LIBRARIAN_ADDRESS env var")
			return
		}
	}
	t.Error("sort Deployment missing sidecar container")
}

// ---------------------------------------------------------------------------
// Facilitator tests (Slice 14.1.2)
// ---------------------------------------------------------------------------

func TestFacilitator_FoundryNode_ResolvedOutput(t *testing.T) {
	nodes := findFoundryNodes(t)
	fn, ok := nodes["facilitator"]
	if !ok {
		t.Fatal("FoundryNode 'facilitator' not found in flow.yaml")
	}

	for _, o := range fn.Spec.Outputs {
		if o.Name == "resolved" {
			if o.Target != "sort" {
				t.Errorf("facilitator output 'resolved': want target %q, got %q", "sort", o.Target)
			}
			return
		}
	}
	t.Error("facilitator FoundryNode missing output 'resolved'")
}

func TestFacilitator_FoundryNode_Capabilities(t *testing.T) {
	nodes := findFoundryNodes(t)
	fn, ok := nodes["facilitator"]
	if !ok {
		t.Fatal("FoundryNode 'facilitator' not found in flow.yaml")
	}

	required := []string{
		"SUSPEND:workitem",
		"CREATE:workitem/child",
		"READ:artefact/petition",
		"READ:artefact/haiku",
		"READ:feedback",
		"READ:law",
	}
	for _, cap := range required {
		if !slices.Contains(fn.Spec.Capabilities, cap) {
			t.Errorf("facilitator FoundryNode missing capability %q", cap)
		}
	}
}

func TestFacilitator_Sidecar_LibrarianAndFrictionLedger(t *testing.T) {
	deps := findDeployments(t)
	d, ok := deps["facilitator"]
	if !ok {
		t.Fatal("Deployment with FLOW_NODE_ID='facilitator' not found in deployments.yaml")
	}

	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Name == containerNameSidecar {
			envMap := make(map[string]string)
			for _, e := range c.Env {
				envMap[e.Name] = e.Value
			}
			if v, ok := envMap["LIBRARIAN_ADDRESS"]; !ok {
				t.Error("facilitator sidecar missing LIBRARIAN_ADDRESS")
			} else if v != "flow-librarian:50058" {
				t.Errorf("LIBRARIAN_ADDRESS: want %q, got %q", "flow-librarian:50058", v)
			}
			if v, ok := envMap["FRICTION_LEDGER_ADDRESS"]; !ok {
				t.Error("facilitator sidecar missing FRICTION_LEDGER_ADDRESS")
			} else if v != "flow-frictionledger:50057" {
				t.Errorf("FRICTION_LEDGER_ADDRESS: want %q, got %q", "flow-frictionledger:50057", v)
			}
			return
		}
	}
	t.Error("facilitator Deployment missing sidecar container")
}

func TestFacilitator_ConfigMap_ArbiterNode(t *testing.T) {
	cms := findConfigMaps(t)
	cm, ok := cms["facilitator-config"]
	if !ok {
		t.Fatal("ConfigMap 'facilitator-config' not found in configmaps.yaml")
	}

	raw, ok := cm.Data["node-config.yaml"]
	if !ok {
		t.Fatal("facilitator-config missing 'node-config.yaml' key")
	}

	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshalling facilitator config data: %v", err)
	}

	if v, ok := cfg["arbiterNode"]; !ok {
		t.Error("facilitator config missing 'arbiterNode' field")
	} else if v != "arbiter" {
		t.Errorf("facilitator config arbiterNode: want %q, got %v", "arbiter", v)
	}
}

// ---------------------------------------------------------------------------
// Arbiter tests (Slice 14.1.3)
// ---------------------------------------------------------------------------

func TestArbiter_FoundryNode_HungOutput(t *testing.T) {
	nodes := findFoundryNodes(t)
	fn, ok := nodes["arbiter"]
	if !ok {
		t.Fatal("FoundryNode 'arbiter' not found in flow.yaml")
	}

	for _, o := range fn.Spec.Outputs {
		if o.Name == "hung" {
			if o.Target != "arbiter-hitl-resolve" {
				t.Errorf("arbiter output 'hung': want target %q, got %q",
					"arbiter-hitl-resolve", o.Target)
			}
			return
		}
	}
	t.Error("arbiter FoundryNode missing output 'hung'")
}

func TestArbiter_FoundryNode_Capabilities(t *testing.T) {
	nodes := findFoundryNodes(t)
	fn, ok := nodes["arbiter"]
	if !ok {
		t.Fatal("FoundryNode 'arbiter' not found in flow.yaml")
	}

	required := []string{
		"SUSPEND:workitem",
		"CREATE:workitem/child",
	}
	for _, cap := range required {
		if !slices.Contains(fn.Spec.Capabilities, cap) {
			t.Errorf("arbiter FoundryNode missing capability %q", cap)
		}
	}
}

func TestArbiter_ConfigMap_AllFields(t *testing.T) {
	cms := findConfigMaps(t)
	cm, ok := cms["arbiter-config"]
	if !ok {
		t.Fatal("ConfigMap 'arbiter-config' not found in configmaps.yaml")
	}

	raw, ok := cm.Data["node-config.yaml"]
	if !ok {
		t.Fatal("arbiter-config missing 'node-config.yaml' key")
	}

	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshalling arbiter config data: %v", err)
	}

	requiredFields := []string{
		"jurySize", "jurorNode", "consensusStrategy",
		"maxRounds", "clerkNode", "hungOutput",
	}
	for _, field := range requiredFields {
		if _, ok := cfg[field]; !ok {
			t.Errorf("arbiter config missing field %q", field)
		}
	}
}

func TestArbiter_Deployment_ConfigMapMount(t *testing.T) {
	deps := findDeployments(t)
	d, ok := deps["arbiter"]
	if !ok {
		t.Fatal("Deployment with FLOW_NODE_ID='arbiter' not found in deployments.yaml")
	}

	for _, vol := range d.Spec.Template.Spec.Volumes {
		if vol.Name == "node-config" && vol.ConfigMap != nil {
			if vol.ConfigMap.Name != "arbiter-config" {
				t.Errorf("arbiter volume configMap name: want %q, got %q",
					"arbiter-config", vol.ConfigMap.Name)
			}
			return
		}
	}
	t.Error("arbiter Deployment missing node-config volume with arbiter-config ConfigMap")
}

// ---------------------------------------------------------------------------
// Juror tests (Slice 14.1.4)
// ---------------------------------------------------------------------------

func TestJuror_FoundryNode_EmptyOutputs(t *testing.T) {
	nodes := findFoundryNodes(t)
	fn, ok := nodes["juror"]
	if !ok {
		t.Fatal("FoundryNode 'juror' not found in flow.yaml")
	}

	if len(fn.Spec.Outputs) != 0 {
		t.Errorf("juror FoundryNode should have empty outputs, got %d", len(fn.Spec.Outputs))
	}
}

func TestJuror_FoundryNode_Capabilities(t *testing.T) {
	nodes := findFoundryNodes(t)
	fn, ok := nodes["juror"]
	if !ok {
		t.Fatal("FoundryNode 'juror' not found in flow.yaml")
	}

	required := []string{
		"READ:artefact/question",
		"READ:artefact/evidence",
		"READ:artefact/allowed-outcomes",
		"READ:artefact/prior-round-reasoning",
		"WRITE:artefact/verdict",
	}
	for _, cap := range required {
		if !slices.Contains(fn.Spec.Capabilities, cap) {
			t.Errorf("juror FoundryNode missing capability %q", cap)
		}
	}
}

func TestJuror_Deployment_NodeHasOllamaBaseURL(t *testing.T) {
	deps := findDeployments(t)
	d, ok := deps["juror"]
	if !ok {
		t.Fatal("Deployment with FLOW_NODE_ID='juror' not found in deployments.yaml")
	}

	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Name == "node" {
			for _, e := range c.Env {
				if e.Name == "OLLAMA_BASE_URL" {
					return
				}
			}
			t.Error("juror node container missing OLLAMA_BASE_URL env var")
			return
		}
	}
	t.Error("juror Deployment missing node container")
}

func TestJuror_ConfigMap_Personality(t *testing.T) {
	cms := findConfigMaps(t)
	cm, ok := cms["juror-config"]
	if !ok {
		t.Fatal("ConfigMap 'juror-config' not found in configmaps.yaml")
	}

	raw, ok := cm.Data["node-config.yaml"]
	if !ok {
		t.Fatal("juror-config missing 'node-config.yaml' key")
	}

	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshalling juror config data: %v", err)
	}

	if v, ok := cfg["personality"]; !ok {
		t.Error("juror config missing 'personality' field")
	} else if v != "textualist" {
		t.Errorf("juror config personality: want %q, got %v", "textualist", v)
	}
}
