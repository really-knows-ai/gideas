package components

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/foundry/flow/tools/flowctl/internal/tui/styles"
	"github.com/foundry/flow/tools/flowctl/internal/tui/types"
)

// FlowTopologyModel is the model for the flow topology graph.
type FlowTopologyModel struct {
	Nodes   []types.TopologyNode
	Edges   []types.TopologyEdge
	Loading bool
	Error   string
}

// NewFlowTopology creates a FlowTopologyModel in loading state.
func NewFlowTopology() FlowTopologyModel {
	return FlowTopologyModel{
		Loading: true,
	}
}

// View renders the flow topology graph as ASCII boxes and arrows.
func (m FlowTopologyModel) View() string {
	var b strings.Builder

	b.WriteString(lipgloss.NewStyle().Bold(true).Render("Flow Topology"))
	b.WriteString("\n")

	if m.Loading {
		b.WriteString("\n  Loading topology...")
		return b.String()
	}

	if m.Error != "" {
		b.WriteString(fmt.Sprintf("\n  Topology unavailable: %s", m.Error))
		return b.String()
	}

	if len(m.Nodes) == 0 {
		b.WriteString("\n  No topology data")
		return b.String()
	}

	// Build node color map and set
	nodeColor := make(map[string]types.TopologyColor)
	nodeSet := make(map[string]bool)
	for _, n := range m.Nodes {
		nodeColor[n.Name] = n.Color
		nodeSet[n.Name] = true
	}

	// Build adjacency and compute in-degrees
	adj := make(map[string][]string)
	inDegree := make(map[string]int)
	for _, n := range m.Nodes {
		adj[n.Name] = nil
		inDegree[n.Name] = 0
	}
	for _, e := range m.Edges {
		if !nodeSet[e.To] {
			continue // Silently skip dangling edges
		}
		adj[e.From] = append(adj[e.From], e.To)
		inDegree[e.To]++
	}

	// Find entry (no incoming edges)
	entry := ""
	if len(m.Nodes) > 0 {
		entry = m.Nodes[0].Name
	}
	for name, deg := range inDegree {
		if deg == 0 {
			entry = name
			break
		}
	}

	// BFS to assign layers (ponytail: simple layering; works for ~20 nodes)
	layers := make(map[int][]string) // depth → node names
	visited := make(map[string]bool)
	type bfsItem struct {
		name  string
		depth int
	}
	queue := []bfsItem{{name: entry, depth: 0}}
	visited[entry] = true
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		layers[cur.depth] = append(layers[cur.depth], cur.name)
		for _, next := range adj[cur.name] {
			if !visited[next] {
				visited[next] = true
				queue = append(queue, bfsItem{name: next, depth: cur.depth + 1})
			}
		}
	}

	// Place unreachable nodes at the bottom
	maxDepth := 0
	for d := range layers {
		if d > maxDepth {
			maxDepth = d
		}
	}
	for _, n := range m.Nodes {
		if !visited[n.Name] {
			maxDepth++
			layers[maxDepth] = append(layers[maxDepth], n.Name)
		}
	}

	// Sort layer keys
	layerKeys := make([]int, 0, len(layers))
	for k := range layers {
		layerKeys = append(layerKeys, k)
	}
	sort.Ints(layerKeys)

	// Track which nodes in each layer have edges to the next
	for layerIdx, depth := range layerKeys {
		nodes := layers[depth]
		nodeLayout := make([]layoutNode, len(nodes))

		for i, name := range nodes {
			style := styleForNode(nodeColor[name])
			box := fmt.Sprintf("┌%s┐", strings.Repeat("─", len(name)+2))
			label := fmt.Sprintf("│ %s │", name)
			width := len(name) + 4 // ┌ ─ ─ ┐
			nodeLayout[i] = layoutNode{
				name:  name,
				box:   style.Render(box),
				label: style.Render(label),
				width: width,
				style: style,
			}
		}

		// Draw node boxes (top line)
		for i, nl := range nodeLayout {
			b.WriteString(nl.box)
			if i < len(nodes)-1 {
				// Check if ANY horizontal edge in this layer crosses this gap — that is,
				// any edge from a node at or left of position i to a node at or right of
				// position i+1. This captures both adjacent and non-adjacent same-layer edges.
				hasEdge := false
				for j := 0; j <= i; j++ {
					for _, e := range m.Edges {
						if e.From == nodes[j] {
							for k := i + 1; k < len(nodes); k++ {
								if e.To == nodes[k] {
									hasEdge = true
									break
								}
							}
						}
						if !hasEdge && e.To == nodes[j] {
							for k := i + 1; k < len(nodes); k++ {
								if e.From == nodes[k] {
									hasEdge = true
									break
								}
							}
						}
						if hasEdge {
							break
						}
					}
					if hasEdge {
						break
					}
				}
				if hasEdge {
					b.WriteString("-->")
				} else {
					b.WriteString("    ")
				}
			}
		}
		b.WriteString("\n")

		// Draw node labels (middle line)
		for i, nl := range nodeLayout {
			b.WriteString(nl.label)
			if i < len(nodes)-1 {
				b.WriteString("    ")
			}
		}
		b.WriteString("\n")

		// Draw node bottom borders
		for i, nl := range nodeLayout {
			bottom := fmt.Sprintf("└%s┘", strings.Repeat("─", len(nl.name)+2))
			b.WriteString(nl.style.Render(bottom))
			if i < len(nodes)-1 {
				b.WriteString("    ")
			}
		}
		b.WriteString("\n")

		// Draw vertical edges to next layer
		if layerIdx < len(layerKeys)-1 {
			nextDepth := layerKeys[layerIdx+1]
			nextNodes := layers[nextDepth]

			// Determine which nodes in this layer have edges to next layer
			for _, nl := range nodeLayout {
				hasDownEdge := false
				for _, next := range adj[nl.name] {
					for _, nn := range nextNodes {
						if next == nn {
							hasDownEdge = true
							break
						}
					}
					if hasDownEdge {
						break
					}
				}
				pad := max(0, len(nl.name)-2)
				b.WriteString(fmt.Sprintf("  %s      ", strings.Repeat(" ", pad)))
			}
			b.WriteString("\n")

			// Arrow line
			for _, nl := range nodeLayout {
				hasDownEdge := false
				for _, next := range adj[nl.name] {
					for _, nn := range nextNodes {
						if next == nn {
							hasDownEdge = true
							break
						}
					}
					if hasDownEdge {
						break
					}
				}
				pad := max(0, len(nl.name)-2)
				if hasDownEdge {
					b.WriteString(fmt.Sprintf("  %s-->  ", strings.Repeat("-", pad)))
				} else {
					b.WriteString(fmt.Sprintf("  %s      ", strings.Repeat(" ", pad)))
				}
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

type layoutNode struct {
	name  string
	box   string
	label string
	width int
	style lipgloss.Style
}

func styleForNode(c types.TopologyColor) lipgloss.Style {
	switch c {
	case types.TopologyCurrent:
		return styles.StyleTopologyCurrent()
	case types.TopologyVisited:
		return styles.StyleTopologyVisited()
	default:
		return styles.StyleTopologyUnvisited()
	}
}
