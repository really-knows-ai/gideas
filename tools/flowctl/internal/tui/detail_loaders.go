package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foundry/flow/tools/flowctl/internal/api"
	"github.com/foundry/flow/tools/flowctl/internal/tui/components"
	"github.com/foundry/flow/tools/flowctl/internal/tui/types"
)

// handleDetailTopology applies detail and topology state with current/visited
// node colouring.
func (m *Model) handleDetailTopology(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case WorkitemDetailLoadedMsg:
		m.workitemDetail.detail = msg.Detail
		m.workitemDetail.loading = false
		m.workitemDetail.statusBar.State = msg.Detail.State

		// Update topology with correct colours based on currentAssignee and thrashCounters
		currentNode := msg.Detail.Node
		if currentNode == "-" {
			currentNode = ""
		}
		thrashCounters := msg.Detail.ThrashCounters
		for i, node := range m.workitemDetail.topology.Nodes {
			if node.Name == currentNode {
				m.workitemDetail.topology.Nodes[i].Color = types.TopologyCurrent
			} else if _, visited := thrashCounters[node.Name]; visited {
				m.workitemDetail.topology.Nodes[i].Color = types.TopologyVisited
			}
		}

		// Start HITL probe if node is non-empty and non-terminal
		if m.hitlState != nil && m.k8s != nil && currentNode != "" {
			m.hitlState.ResetForNewWorkitem()
			cmds := []tea.Cmd{m.RefreshArtefacts()}
			if probeCmd := m.probeHitl(msg.Detail.Name); probeCmd != nil {
				cmds = append(cmds, probeCmd)
			}
			return m, tea.Batch(cmds...)
		}

	case TopologyLoadedMsg:
		// Preserve the detail's node coloring if detail was loaded before topology
		currentNode := ""
		thrashCounters := make(map[string]int32)
		if m.workitemDetail.detail != nil {
			currentNode = m.workitemDetail.detail.Node
			if currentNode == "-" {
				currentNode = ""
			}
			thrashCounters = m.workitemDetail.detail.ThrashCounters
		}

		coloredNodes := make([]types.TopologyNode, len(msg.Nodes))
		for i, n := range msg.Nodes {
			coloredNodes[i] = n
			if n.Name == currentNode {
				coloredNodes[i].Color = types.TopologyCurrent
			} else if _, visited := thrashCounters[n.Name]; visited {
				coloredNodes[i].Color = types.TopologyVisited
			}
		}

		m.workitemDetail.topology.Nodes = coloredNodes
		m.workitemDetail.topology.Edges = msg.Edges
		m.workitemDetail.topology.Loading = false
	}
	return m, nil
}

// probeHitl resolves the queue serving workitemID through the queue-service
// (SPEC R-5.1: queue interactions go only through the queue-service, never a
// node), then starts the HITL probe. When the queue-service cannot be reached
// or no queue serves the item, the resolution port-forward (if opened) is
// closed and the failure is recorded on HitlState: retry commands re-arm the
// probe cycle while attempts remain, and the cycle stops once attempts are
// exhausted — preserving Probe's retry/exhaust contract.
func (m *Model) probeHitl(workitemID string) tea.Cmd {
	if m.hitlState == nil || m.k8s == nil || m.pfm == nil {
		return nil
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	// retryOrExhaust records a failed resolution attempt on HitlState and
	// returns the retry/exhaust command, mirroring Probe's retryOrExhaust
	// semantics so the 3s probe tick stops once attempts are exhausted.
	retryOrExhaust := func(diagnostic string) tea.Cmd {
		return func() tea.Msg {
			return m.hitlState.RecordProbeFailure(workitemID, diagnostic)
		}
	}

	podName, found, err := m.pfm.FindReadyPod(ctx, m.namespace, components.QueueServiceLabel)
	switch {
	case err != nil:
		return retryOrExhaust("find queue-service pod: " + err.Error())
	case !found:
		return retryOrExhaust("no ready queue-service pod found")
	}
	forwardID, localPort, err := m.pfm.ForwardPod(ctx, m.namespace, podName, components.QueueServiceRESTPort)
	if err != nil {
		return retryOrExhaust("port-forward to queue-service: " + err.Error())
	}
	// ResolveQueueForItem lists GET /queues and probes each queue's item by
	// name, so the client's queueName is irrelevant here (unknown until
	// resolution completes).
	client := api.NewHitlClient(fmt.Sprintf("http://localhost:%d", localPort), "")
	queueName, err := client.ResolveQueueForItem(ctx, workitemID)
	if err != nil {
		// Nothing else tracks the resolution forward (it is not stored in
		// hitlState.forwardID), so close it before retrying.
		m.pfm.Close(forwardID)
		return retryOrExhaust("queue-service has not enqueued the workitem: " + err.Error())
	}
	return m.hitlState.Probe(ctx, m.k8s.CoreClient, m.namespace, queueName, workitemID, m.pfm)
}

// handleBanner applies banner/error display state on the detail screen,
// including returning to the list when the viewed workitem is deleted.
func (m *Model) handleBanner(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case ErrorMsg:
		// Show as error banner at top of detail screen
		m.errorBanner = fmt.Sprintf("⚠ %s: %s", msg.Source, msg.Message)
		m.logIfEnabled("ERROR", msg.Source, msg.Message)
		// Clear error banner after 10 seconds
		return m, tea.Tick(10*time.Second, func(t time.Time) tea.Msg {
			return ClearErrorBannerMsg{}
		})

	case WorkitemDeletedMsg:
		if m.screen == ScreenWorkitemDetail && m.workitemDetail.workitemName == msg.Name {
			// The viewed workitem was deleted — clear detail and return to list
			m.workitemDetail = WorkitemDetailModel{
				statusBar: components.NewStatusBar(),
				topology:  components.NewFlowTopology(),
				artefacts: components.NewArtefactTree(),
				hitl:      components.NewHitlPrompt(),
			}
			m.screen = ScreenWorkitemList
			m.banner = fmt.Sprintf("Workitem %s was deleted", msg.Name)
			m.bannerSource = "watch"
			m.logIfEnabled("INFO", "watch", fmt.Sprintf("workitem %s deleted while viewing detail", msg.Name))
		}

	case ClearErrorBannerMsg:
		m.errorBanner = ""

	case BannerMsg:
		m.banner = msg.Message
		m.bannerSource = msg.Source
		m.bannerTimeout = true
		m.logIfEnabled(strings.ToUpper(msg.Level), "banner", msg.Message)
		return m, tea.Tick(10*time.Second, func(t time.Time) tea.Msg {
			return BannerDismissMsg{Source: msg.Source}
		})

	case BannerDismissMsg:
		if m.bannerSource == msg.Source {
			m.banner = ""
			m.bannerSource = ""
			m.bannerTimeout = false
		}
	}
	return m, nil
}

// handleRefreshCreate handles refresh and create-wizard screen transitions.
func (m *Model) handleRefreshCreate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case RefreshMsg:
		m.workitemDetail.loading = true
		m.workitemDetail.artefacts.Loading = true
		m.workitemDetail.topology.Loading = true
		m.errorBanner = ""

		workitemID := m.workitemDetail.workitemName
		if workitemID == "" {
			return m, nil
		}

		cmds := []tea.Cmd{
			m.loadWorkitemDetail(workitemID),
			m.loadTopology(),
		}
		if m.archivist != nil {
			cmds = append(cmds, m.RefreshArtefacts())
		} else {
			cmds = append(cmds, m.loadArtefacts(workitemID))
		}
		return m, tea.Batch(cmds...)

	case CreateStartMsg:
		// Open create wizard from detail screen and load data
		m.createWizard = components.NewCreateWizard()
		m.createWizard.Loading = true
		m.screen = ScreenCreateWizard
		m.createHasCRD = false
		m.createHasArtefact = false
		return m, m.loadWizardData()
	}
	return m, nil
}

// loadWorkitemDetail fetches the Workitem detail and builds topology/artefacts.
func (m *Model) loadWorkitemDetail(name string) tea.Cmd {
	return func() tea.Msg {
		if m.k8s == nil {
			return nil
		}
		detail, err := m.k8s.GetWorkitem(m.ctx, m.namespace, name)
		if err != nil {
			m.logIfEnabled("ERROR", "workitem-detail", fmt.Sprintf("fetch detail for %s: %v", name, err))
			return ErrorMsg{Source: "workitem-detail", Message: fmt.Sprintf("fetch detail: %v", err)}
		}
		return WorkitemDetailLoadedMsg{Detail: detail}
	}
}

// loadTopology fetches FoundryNodes and builds the topology graph.
func (m *Model) loadTopology() tea.Cmd {
	return func() tea.Msg {
		if m.k8s == nil {
			return nil
		}
		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		nodes, err := m.k8s.ListFoundryNodes(ctx, m.namespace)
		if err != nil {
			m.logIfEnabled("ERROR", "topology", fmt.Sprintf("ListFoundryNodes: %v", err))
			return ErrorMsg{Source: "topology", Message: fmt.Sprintf("ListFoundryNodes: %v", err)}
		}

		// Build topology nodes
		topoNodes := make([]types.TopologyNode, 0, len(nodes))
		nodeSet := make(map[string]bool)
		for _, n := range nodes {
			nodeSet[n.Name] = true
			// Default to unvisited; colouring happens after detail is loaded
			topoNodes = append(topoNodes, types.TopologyNode{
				Name:  n.Name,
				Color: types.TopologyUnvisited,
			})
		}

		// Build topology edges, skipping missing targets
		topoEdges := make([]types.TopologyEdge, 0)
		for _, n := range nodes {
			for _, target := range n.Targets {
				if nodeSet[target] {
					topoEdges = append(topoEdges, types.TopologyEdge{
						From: n.Name,
						To:   target,
					})
				}
			}
		}

		return TopologyLoadedMsg{Nodes: topoNodes, Edges: topoEdges}
	}
}
