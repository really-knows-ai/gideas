package flow

import (
	"slices"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// Node wraps a single flow node's definition from the topology response.
// Constructed by Flow.GetNodes() or Client.GetNode().
type Node struct {
	name         string
	capabilities []string
}

// newNode constructs a Node from a proto FlowNode.
func newNode(pb *flowv1.FlowNode) *Node {
	return &Node{
		name:         pb.GetName(),
		capabilities: pb.GetCapabilities(),
	}
}

// GetName returns the node name.
func (n *Node) GetName() string { return n.name }

// GetCapabilities returns the node's capability list.
func (n *Node) GetCapabilities() []string { return n.capabilities }

// HasCapability returns true if the node has the named capability.
// Performs exact string match only — no wildcard support.
// For wildcard matching (e.g. STAMP:artefact/*/review) see HasStampCapability.
func (n *Node) HasCapability(name string) bool {
	return slices.Contains(n.capabilities, name)
}

// HasStampCapability returns true if the node has a
// STAMP:artefact/<kind>/<stamp> or ATTEST:artefact/<kind>/<stamp> capability
// matching the given kind and stamp.
// The node's capabilities are treated as patterns (may contain *), while the
// constructed concrete values (with both prefixes) are checked against each.
// Wildcard matching follows MatchCapability semantics (segment-by-segment
// filepath.Match; * does not cross /).
func (n *Node) HasStampCapability(kind, stamp string) bool {
	requiredSTAMP := "STAMP:artefact/" + kind + "/" + stamp
	requiredATTEST := "ATTEST:artefact/" + kind + "/" + stamp
	for _, cap := range n.capabilities {
		if MatchCapability(cap, requiredSTAMP) || MatchCapability(cap, requiredATTEST) {
			return true
		}
	}
	return false
}
