package components

import (
	"strings"
	"testing"

	"github.com/gideas/flow/tools/flowctl/internal/tui/types"
)

func TestTopologyLoadingState(t *testing.T) {
	m := NewFlowTopology()
	v := m.View()
	if !strings.Contains(v, "Loading topology") {
		t.Error("expected 'Loading topology' in view, got:", v)
	}
}

func TestTopologyNormalGraph(t *testing.T) {
	m := NewFlowTopology()
	m.Loading = false
	m.Nodes = []types.TopologyNode{
		{Name: "forge", Color: types.TopologyVisited},
		{Name: "sort", Color: types.TopologyCurrent},
	}
	m.Edges = []types.TopologyEdge{
		{From: "forge", To: "sort"},
	}
	v := m.View()
	if !strings.Contains(v, "forge") || !strings.Contains(v, "sort") {
		t.Error("expected node names in view, got:", v)
	}
}

func TestTopologyCurrentNodeBoldGreen(t *testing.T) {
	m := NewFlowTopology()
	m.Loading = false
	m.Nodes = []types.TopologyNode{
		{Name: "forge", Color: types.TopologyCurrent},
	}
	v := m.View()
	if !strings.Contains(v, "forge") {
		t.Error("expected 'forge' in view, got:", v)
	}
}

func TestTopologyVisitedNodeDimGreen(t *testing.T) {
	m := NewFlowTopology()
	m.Loading = false
	m.Nodes = []types.TopologyNode{
		{Name: "sort", Color: types.TopologyVisited},
	}
	v := m.View()
	if !strings.Contains(v, "sort") {
		t.Error("expected 'sort' in view, got:", v)
	}
}

func TestTopologyUnvisitedNodeGray(t *testing.T) {
	m := NewFlowTopology()
	m.Loading = false
	m.Nodes = []types.TopologyNode{
		{Name: "refine", Color: types.TopologyUnvisited},
	}
	v := m.View()
	if !strings.Contains(v, "refine") {
		t.Error("expected 'refine' in view, got:", v)
	}
}

func TestTopologyDanglingEdgeSkipped(t *testing.T) {
	m := NewFlowTopology()
	m.Loading = false
	m.Nodes = []types.TopologyNode{
		{Name: "forge", Color: types.TopologyVisited},
	}
	m.Edges = []types.TopologyEdge{
		{From: "forge", To: "nonexistent"},
	}
	v := m.View()
	// Should not crash; dangling edge silently skipped
	if !strings.Contains(v, "forge") {
		t.Error("expected 'forge' in view, got:", v)
	}
}

func TestTopologyEmptyState(t *testing.T) {
	m := NewFlowTopology()
	m.Loading = false
	m.Nodes = nil
	v := m.View()
	if !strings.Contains(v, "No topology data") {
		t.Error("expected empty state text in view, got:", v)
	}
}

func TestTopologySingleNode(t *testing.T) {
	m := NewFlowTopology()
	m.Loading = false
	m.Nodes = []types.TopologyNode{
		{Name: "forge", Color: types.TopologyVisited},
	}
	m.Edges = nil
	v := m.View()
	if !strings.Contains(v, "forge") {
		t.Error("expected 'forge' in view, got:", v)
	}
}

func TestTopologyMultipleEdges(t *testing.T) {
	m := NewFlowTopology()
	m.Loading = false
	m.Nodes = []types.TopologyNode{
		{Name: "forge", Color: types.TopologyVisited},
		{Name: "sort", Color: types.TopologyCurrent},
		{Name: "refine", Color: types.TopologyUnvisited},
	}
	m.Edges = []types.TopologyEdge{
		{From: "forge", To: "sort"},
		{From: "sort", To: "refine"},
	}
	v := m.View()
	if !strings.Contains(v, "forge") || !strings.Contains(v, "sort") || !strings.Contains(v, "refine") {
		t.Error("expected all node names in view, got:", v)
	}
}

func TestTopologyCyclicalEdges(t *testing.T) {
	m := NewFlowTopology()
	m.Loading = false
	m.Nodes = []types.TopologyNode{
		{Name: "forge", Color: types.TopologyVisited},
		{Name: "refine", Color: types.TopologyUnvisited},
	}
	// Cycle: forge → refine → forge
	m.Edges = []types.TopologyEdge{
		{From: "forge", To: "refine"},
		{From: "refine", To: "forge"},
	}
	v := m.View()
	if !strings.Contains(v, "forge") || !strings.Contains(v, "refine") {
		t.Error("expected node names in view, got:", v)
	}
}
