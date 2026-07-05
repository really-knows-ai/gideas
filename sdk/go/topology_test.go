package flow

import (
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// testTopology returns a reusable topology fixture with 3 nodes and an exit contract.
func testTopology() *flowv1.GetFlowTopologyResponse {
	return &flowv1.GetFlowTopologyResponse{
		Nodes: map[string]*flowv1.FlowNode{
			"sort": {
				Name: "sort",
				Capabilities: []string{
					"READ:flow",
					"STAMP:artefact/haiku/review",
				},
			},
			"forge": {
				Name: "forge",
				Capabilities: []string{
					"WRITE:artefact",
					"STAMP:artefact/haiku/appraise-*",
					"STAMP:artefact/law/law-*",
				},
			},
			"appraise": {
				Name: "appraise",
				Capabilities: []string{
					"READ:flow",
					"STAMP:artefact/*/review",
				},
			},
		},
		ExitContract: map[string]*flowv1.StampRequirements{
			"haiku": {Stamps: []string{"review", "appraise-security"}},
			"law":   {Stamps: []string{"law-group-content", "law-abc-def"}},
		},
	}
}

// ---------------------------------------------------------------------------
// Flow tests
// ---------------------------------------------------------------------------

func TestFlow_GetName(t *testing.T) {
	f := newFlow(testTopology(), "test-ns")
	if got := f.GetName(); got != "test-ns" {
		t.Errorf("GetName() = %q, want %q", got, "test-ns")
	}
}

func TestFlow_GetExitContract(t *testing.T) {
	f := newFlow(testTopology(), "test-ns")
	ec := f.GetExitContract()

	// Check haiku entry.
	haikuStamps, ok := ec["haiku"]
	if !ok {
		t.Fatal("missing exit contract key: haiku")
	}
	if len(haikuStamps) != 2 {
		t.Fatalf("haiku stamps length = %d, want 2", len(haikuStamps))
	}
	if haikuStamps[0] != "review" {
		t.Errorf("haiku stamps[0] = %q, want %q", haikuStamps[0], "review")
	}
	if haikuStamps[1] != "appraise-security" {
		t.Errorf("haiku stamps[1] = %q, want %q", haikuStamps[1], "appraise-security")
	}

	// Check law entry.
	lawStamps, ok := ec["law"]
	if !ok {
		t.Fatal("missing exit contract key: law")
	}
	if len(lawStamps) != 2 {
		t.Fatalf("law stamps length = %d, want 2", len(lawStamps))
	}
	if lawStamps[0] != "law-group-content" {
		t.Errorf("law stamps[0] = %q, want %q", lawStamps[0], "law-group-content")
	}
}

func TestFlow_GetExitContract_nil(t *testing.T) {
	f := newFlow(&flowv1.GetFlowTopologyResponse{}, "ns")
	ec := f.GetExitContract()
	if ec != nil {
		t.Errorf("expected nil exit contract for empty response, got %v", ec)
	}
}

func TestFlow_GetNodes(t *testing.T) {
	f := newFlow(testTopology(), "test-ns")
	nodes := f.GetNodes()
	if len(nodes) != 3 {
		t.Fatalf("GetNodes() returned %d nodes, want 3", len(nodes))
	}

	names := make(map[string]bool)
	for _, n := range nodes {
		names[n.GetName()] = true
	}
	if !names["sort"] {
		t.Error("missing node: sort")
	}
	if !names["forge"] {
		t.Error("missing node: forge")
	}
	if !names["appraise"] {
		t.Error("missing node: appraise")
	}
}

func TestFlow_GetNodes_idempotent(t *testing.T) {
	f := newFlow(testTopology(), "test-ns")
	nodes1 := f.GetNodes()
	nodes2 := f.GetNodes()
	if len(nodes1) != len(nodes2) {
		t.Fatal("idempotency check: different lengths")
	}
	for i := range nodes1 {
		if nodes1[i].GetName() != nodes2[i].GetName() {
			t.Errorf("idempotency check: node %d name mismatch: %q vs %q",
				i, nodes1[i].GetName(), nodes2[i].GetName())
		}
		// Verify capabilities are identical too.
		c1 := nodes1[i].GetCapabilities()
		c2 := nodes2[i].GetCapabilities()
		if len(c1) != len(c2) {
			t.Errorf("idempotency check: node %q capability length mismatch",
				nodes1[i].GetName())
			continue
		}
		for j := range c1 {
			if c1[j] != c2[j] {
				t.Errorf("idempotency check: node %q cap[%d] mismatch",
					nodes1[i].GetName(), j)
			}
		}
	}
}

func TestFlow_GetNodes_empty(t *testing.T) {
	f := newFlow(&flowv1.GetFlowTopologyResponse{}, "ns")
	nodes := f.GetNodes()
	if len(nodes) != 0 {
		t.Errorf("expected empty nodes, got %d", len(nodes))
	}
}

func TestFlow_GetNodeOrder(t *testing.T) {
	f := newFlow(testTopology(), "test-ns")
	order := f.GetNodeOrder()
	if len(order) != 3 {
		t.Fatalf("GetNodeOrder() returned %d names, want 3", len(order))
	}
	expected := []string{"appraise", "forge", "sort"}
	for i, name := range order {
		if name != expected[i] {
			t.Errorf("GetNodeOrder()[%d] = %q, want %q", i, name, expected[i])
		}
	}
}

func TestFlow_GetNodeOrder_empty(t *testing.T) {
	f := newFlow(&flowv1.GetFlowTopologyResponse{}, "ns")
	order := f.GetNodeOrder()
	if order != nil {
		t.Errorf("expected nil for empty topology, got %v", order)
	}
}

// ---------------------------------------------------------------------------
// Node tests
// ---------------------------------------------------------------------------

func TestNode_GetName(t *testing.T) {
	n := newNode(&flowv1.FlowNode{Name: "test-node"})
	if got := n.GetName(); got != "test-node" {
		t.Errorf("GetName() = %q, want %q", got, "test-node")
	}
}

func TestNode_GetCapabilities(t *testing.T) {
	caps := []string{"READ:flow", "WRITE:artefact"}
	n := newNode(&flowv1.FlowNode{Name: "test", Capabilities: caps})
	got := n.GetCapabilities()
	if len(got) != len(caps) {
		t.Fatalf("GetCapabilities() length = %d, want %d", len(got), len(caps))
	}
	for i := range caps {
		if got[i] != caps[i] {
			t.Errorf("GetCapabilities()[%d] = %q, want %q", i, got[i], caps[i])
		}
	}
}

func TestNode_HasCapability(t *testing.T) {
	n := newNode(&flowv1.FlowNode{
		Name: "test",
		Capabilities: []string{
			"READ:flow",
			"WRITE:artefact",
		},
	})
	tests := []struct {
		name string
		cap  string
		want bool
	}{
		{"has READ:flow", "READ:flow", true},
		{"has WRITE:artefact", "WRITE:artefact", true},
		{"missing STAMP capability", "STAMP:artefact/haiku/review", false},
		{"empty string", "", false},
		{"substring no match", "READ", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := n.HasCapability(tt.cap); got != tt.want {
				t.Errorf("HasCapability(%q) = %v, want %v", tt.cap, got, tt.want)
			}
		})
	}
}

func TestNode_HasCapability_nil_caps(t *testing.T) {
	n := newNode(&flowv1.FlowNode{Name: "empty"})
	if n.HasCapability("anything") {
		t.Error("HasCapability on nil caps should return false")
	}
}

func TestNode_HasStampCapability(t *testing.T) {
	n := newNode(&flowv1.FlowNode{
		Name: "test",
		Capabilities: []string{
			"STAMP:artefact/haiku/review",      // exact match
			"STAMP:artefact/haiku/appraise-*",  // wildcard stamp segment
			"STAMP:artefact/*/review",          // wildcard kind segment
			"STAMP:artefact/law/law-*",         // law-* prefix
			"ATTEST:artefact/*/review",         // ATTEST wildcard kind segment
			"ATTEST:artefact/haiku/appraise-*", // ATTEST wildcard stamp segment
		},
	})
	tests := []struct {
		name  string
		kind  string
		stamp string
		want  bool
	}{
		{"exact match haiku/review", "haiku", "review", true},
		{"wildcard stamp segment appraise-*", "haiku", "appraise-security", true},
		{"wildcard kind segment */review haiku", "haiku", "review", true},
		{"wildcard kind segment */review law", "law", "review", true},
		{"law-* prefix match law-group-content", "law", "law-group-content", true},
		{"law-* prefix match law-abc-def", "law", "law-abc-def", true},
		{"no match wrong stamp", "haiku", "approval", false},
		{"no match wrong kind and stamp", "doc", "nonesuch", false},

		// ATTEST: capability matching (capabilities added to node above).
		{"ATTEST wildcard kind match doc/review", "doc", "review", true},
		{"ATTEST wildcard stamp appraise-security", "haiku", "appraise-security", true},
		{"ATTEST no match wrong kind", "doc", "nonesuch", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := n.HasStampCapability(tt.kind, tt.stamp); got != tt.want {
				t.Errorf("HasStampCapability(%q, %q) = %v, want %v",
					tt.kind, tt.stamp, got, tt.want)
			}
		})
	}
}

func TestNode_HasStampCapability_nonstamp_node(t *testing.T) {
	// Node with only non-stamp capabilities.
	n := newNode(&flowv1.FlowNode{
		Name: "non-stamp",
		Capabilities: []string{
			"READ:flow",
			"WRITE:artefact",
		},
	})
	if n.HasStampCapability("haiku", "appraise-security") {
		t.Error("HasStampCapability should return false for node with only non-stamp capabilities")
	}
}

func TestNode_HasStampCapability_empty_caps(t *testing.T) {
	n := newNode(&flowv1.FlowNode{Name: "empty"})
	if n.HasStampCapability("haiku", "review") {
		t.Error("HasStampCapability with empty caps should return false")
	}
}
