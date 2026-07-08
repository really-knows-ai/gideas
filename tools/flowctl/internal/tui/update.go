package tui

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/gideas/flow/tools/flowctl/internal/api"
	"github.com/gideas/flow/tools/flowctl/internal/tui/components"
	"github.com/gideas/flow/tools/flowctl/internal/tui/types"
)

// defaultChoices for the HITL prompt, used by fakeHitlProbe and HitlProbeResultMsg.
var defaultChoices = []types.Choice{
	{Value: "approve", Label: "Approve", Type: "route"},
	{Value: "cancel", Label: "Cancel", Type: "cancel"},
}

// ─── Type Conversion Helpers ────────────────────────────────────────────────

// toArtefactNode converts an api.ArtefactInfo to a types.ArtefactNode for the TUI component.
func toArtefactNode(info api.ArtefactInfo) types.ArtefactNode {
	return types.ArtefactNode{
		ArtefactID: info.ID,
		GovernedBy: info.GovernedArtefact,
		Expanded:   false,
		Content:    "",
		IsBinary:   false,
		BinarySize: 0,
		Feedback:   nil,
	}
}

// toFeedbackItem converts an api.FeedbackItem to a types.FeedbackItem for the TUI component.
func toFeedbackItem(item api.FeedbackItem) types.FeedbackItem {
	return types.FeedbackItem{
		ID:         item.ID,
		State:      feedbackStateToString(item.State),
		SourceNode: item.SourceNode,
		Message:    item.Message,
		Timestamp:  item.CreatedAt.Format(time.RFC3339),
	}
}

// feedbackStateToString converts a FeedbackState int to its string representation.
func feedbackStateToString(s api.FeedbackState) string {
	switch s {
	case api.FeedbackStateNew:
		return "NEW"
	case api.FeedbackStateActioned:
		return "ACTIONED"
	case api.FeedbackStateWontFix:
		return "WONT_FIX"
	case api.FeedbackStateRejected:
		return "REJECTED"
	case api.FeedbackStateDeadlocked:
		return "DEADLOCKED"
	case api.FeedbackStateResolved:
		return "RESOLVED"
	default:
		return "UNSPECIFIED"
	}
}

// toChoice converts an api.Choice to a types.Choice for the TUI component.
func toChoice(c api.Choice) types.Choice {
	return types.Choice{
		Value: c.Value,
		Label: c.Label,
		Type:  c.Type,
	}
}

// toChoices converts a slice of api.Choice to a slice of types.Choice.
func toChoices(apiChoices []api.Choice) []types.Choice {
	if len(apiChoices) == 0 {
		return nil
	}
	result := make([]types.Choice, len(apiChoices))
	for i, c := range apiChoices {
		result[i] = toChoice(c)
	}
	return result
}

// formatArtefactContent formats artefact content for display.
// Returns (content string, isBinary bool, binarySize int).
func formatArtefactContent(raw []byte) (string, bool, int) {
	if len(raw) == 0 {
		return "", false, 0
	}
	if utf8.Valid(raw) {
		return string(raw), false, 0
	}
	// Binary: store raw content; the component handles hex rendering.
	return string(raw), true, len(raw)
}

// convertAPIFeedback converts a slice of api.FeedbackItem to types.FeedbackItem.
func convertAPIFeedback(items []api.FeedbackItem) []types.FeedbackItem {
	result := make([]types.FeedbackItem, len(items))
	for i, item := range items {
		result[i] = toFeedbackItem(item)
	}
	return result
}

// ─── Root Update ───────────────────────────────────────────────────────────

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		// Global key handlers
		switch msg.String() {
		case "ctrl+c", "q":
			// Clean up HITL port-forward before quitting
			if m.hitlState != nil && m.pfm != nil {
				m.hitlState.Close(m.pfm)
			}
			return m, tea.Quit
		}

		// Action keys are intercepted here and converted to semantic messages
		// before reaching component-level key handlers.

		// NamespaceSelect: enter -> NamespaceSelectedMsg
		if m.screen == ScreenNamespaceSelect && msg.String() == "enter" {
			selected := m.namespaceSelector.Namespaces[m.namespaceSelector.Cursor]
			return m.routeMsg(NamespaceSelectedMsg{Namespace: selected})
		}

		// WorkitemList: enter -> WorkitemSelectedMsg
		if m.screen == ScreenWorkitemList && msg.String() == "enter" {
			if m.workitemList.Cursor < len(m.workitemList.Items) {
				return m.routeMsg(WorkitemSelectedMsg{Name: m.workitemList.Items[m.workitemList.Cursor].Name})
			}
			return m, nil
		}

		// WorkitemList: n -> CreateStartMsg
		if m.screen == ScreenWorkitemList && msg.String() == "n" {
			return m.routeMsg(CreateStartMsg{})
		}

		// WorkitemList: d -> DeleteConfirmMsg
		if m.screen == ScreenWorkitemList && msg.String() == "d" {
			if m.workitemList.Cursor < len(m.workitemList.Items) {
				item := m.workitemList.Items[m.workitemList.Cursor]
				return m.routeMsg(DeleteConfirmMsg{WorkitemName: item.Name, Phase: item.State})
			}
			return m, nil
		}

		// WorkitemList: r -> RefreshMsg
		if m.screen == ScreenWorkitemList && msg.String() == "r" {
			return m.routeMsg(NamespaceRefreshMsg{Namespace: m.namespace})
		}

		// WorkitemList: esc/backspace -> return to namespace selection
		if m.screen == ScreenWorkitemList && (msg.String() == "esc" || msg.String() == "backspace") {
			m.screen = ScreenNamespaceSelect
			m.err = nil
			return m, nil
		}

		// WorkitemDetail: n -> CreateStartMsg (only when HITL is not active)
		if m.screen == ScreenWorkitemDetail && msg.String() == "n" && (m.hitlState == nil || !m.hitlState.Active()) {
			return m.routeMsg(CreateStartMsg{})
		}

		// WorkitemDetail: r -> RefreshMsg (only when HITL is not active)
		if m.screen == ScreenWorkitemDetail && msg.String() == "r" && (m.hitlState == nil || !m.hitlState.Active()) {
			return m.routeMsg(RefreshMsg{})
		}

		// WorkitemDetail: esc/backspace -> return to list
		if m.screen == ScreenWorkitemDetail && (msg.String() == "esc" || msg.String() == "backspace") {
			prevItems := m.workitemList.Items
			prevCursor := m.workitemList.Cursor
			prevNamespace := m.workitemList.Namespace
			m.screen = ScreenWorkitemList
			m.workitemList.Items = prevItems
			m.workitemList.Cursor = prevCursor
			m.workitemList.Namespace = prevNamespace
			m.err = nil
			return m, nil
		}

		// Navigation keys pass through to component-level key handlers
		return m.routeKeyMsg(msg)

	case ErrorMsg:
		m.err = fmt.Errorf("%s: %s", msg.Source, msg.Message)
		return m, nil

	case WorkitemUpdateMsg:
		// Handle WorkitemUpdateMsg at root level so NODE change detection
		// works regardless of current screen.
		cmd := m.handleWorkitemUpdate(msg)
		return m, cmd

	default:
		return m.routeMsg(msg)
	}
}

// ─── Message Routing ───────────────────────────────────────────────────────

// routeKeyMsg dispatches keyboard navigation to component-level key handlers.
func (m *Model) routeKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.screen {
	case ScreenNamespaceSelect:
		return m.updateNamespaceSelectKeys(msg)
	case ScreenWorkitemList:
		return m.updateWorkitemListKeys(msg)
	case ScreenWorkitemDetail:
		return m.updateWorkitemDetailKeys(msg)
	case ScreenCreateWizard:
		return m.updateCreateWizardKeys(msg)
	}
	return m, nil
}

// routeMsg dispatches semantic messages to screen-level handlers.
func (m *Model) routeMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.screen {
	case ScreenNamespaceSelect:
		return m.updateNamespaceSelect(msg)
	case ScreenWorkitemList:
		return m.updateWorkitemList(msg)
	case ScreenWorkitemDetail:
		return m.updateWorkitemDetail(msg)
	case ScreenCreateWizard:
		return m.updateCreateWizard(msg)
	}
	return m, nil
}

// ─── Namespace Loading ─────────────────────────────────────────────────────

func (m *Model) loadNamespaces() tea.Msg {
	if m.k8s == nil {
		return NamespaceFallbackMsg{Namespace: "default", Error: fmt.Errorf("no K8s client")}
	}
	namespaces, err := m.k8s.ListNamespaces(m.ctx)
	if err != nil {
		fallback := api.GetCurrentContextNamespace()
		if m.cfg.Namespace != "" {
			fallback = m.cfg.Namespace
		}
		return NamespaceFallbackMsg{Namespace: fallback, Error: err}
	}
	return NamespaceListLoadedMsg{Namespaces: namespaces}
}

func (m *Model) loadWorkitems() tea.Cmd {
	return func() tea.Msg {
		if m.k8s == nil {
			return WorkitemLoadErrorMsg{Error: fmt.Errorf("no K8s client")}
		}
		items, err := m.k8s.ListWorkitems(m.ctx, m.namespace)
		if err != nil {
			return WorkitemLoadErrorMsg{Error: err}
		}
		// Compute child counts in a batch before returning
		m.computeChildCounts(m.ctx, items)
		return WorkitemsLoadedMsg{Items: items}
	}
}

// computeChildCounts queries children for every workitem in the list and
// sets ChildrenCount on each item. This is O(n) queries where n is the number
// of workitems, acceptable for typical namespace sizes (< 500).
// ponytail: This method modifies items in place from a Cmd goroutine. The
// items slice is local to the Cmd function, not the model's stored slice,
// so there is no data race. However, if called from the debounced refresh,
// it operates on m.workitemList.Items which IS shared; see debouncedChildCountRefresh.
func (m *Model) computeChildCounts(ctx context.Context, items []api.WorkitemSummary) {
	if m.k8s == nil {
		return
	}
	for i := range items {
		children, err := m.k8s.ListChildren(ctx, m.namespace, items[i].Name)
		if err != nil {
			continue
		}
		items[i].ChildrenCount = len(children)
	}
}

// startWorkitemWatch starts a background goroutine that watches Workitems.
// It calls WatchWithBackoff which blocks until ctx is cancelled.
// The handler sends messages to the TUI program via program.Send().
func (m *Model) startWorkitemWatch() tea.Cmd {
	if m.k8s == nil {
		return nil
	}
	return func() tea.Msg {
		m.k8s.WatchWithBackoff(m.ctx, m.namespace, func(event watch.Event) {
			m.handleWatchEvent(event)
		}, api.WatchOptions{
			OnDisconnect: func(err error) {
				m.Program.Send(WatchDisconnectedMsg{Error: err})
			},
			OnReconnect: func() {
				m.Program.Send(WatchReconnectedMsg{})
			},
		})
		return nil
	}
}

// handleWatchEvent converts a watch event into a TUI message and sends it.
func (m *Model) handleWatchEvent(event watch.Event) {
	if m.Program == nil {
		return
	}
	switch event.Type {
	case watch.Added, watch.Modified:
		summary := api.ExtractSummary(event.Object)
		m.Program.Send(WorkitemUpdateMsg{Event: event.Type, Item: summary})
	case watch.Deleted:
		u, ok := event.Object.(*unstructured.Unstructured)
		if !ok {
			return
		}
		m.Program.Send(WorkitemDeletedMsg{Name: u.GetName()})
	}
}

// debouncedChildCountRefresh resets a 200ms debounce timer and returns a Cmd
// that fires when the timer expires.
// ponytail: The Cmd goroutine reads m.workitemList.Items to get workitem names
// for child count queries. In practice bubbletea serializes Cmd execution, but
// concurrent reads while the main loop writes is a data race by Go's definition.
// The race window is small (200ms debounce) and the impact is negligible
// (stale child count). Upgrade: pass a workitem name snapshot from the Update handler.
func (m *Model) debouncedChildCountRefresh() tea.Cmd {
	if m.childCountDebounce == nil {
		m.childCountDebounce = time.NewTimer(200 * time.Millisecond)
	} else {
		if !m.childCountDebounce.Stop() {
			select {
			case <-m.childCountDebounce.C:
			default:
			}
		}
		m.childCountDebounce.Reset(200 * time.Millisecond)
	}
	return func() tea.Msg {
		<-m.childCountDebounce.C

		// Build a snapshot of workitem names from the model
		names := make([]string, len(m.workitemList.Items))
		for i, item := range m.workitemList.Items {
			names[i] = item.Name
		}

		counts := make(map[string]int, len(names))
		for _, name := range names {
			if m.k8s == nil || (m.ctx != nil && m.ctx.Err() != nil) {
				return nil
			}
			children, err := m.k8s.ListChildren(m.ctx, m.namespace, name)
			if err != nil {
				continue
			}
			counts[name] = len(children)
		}
		return ChildCountsUpdatedMsg{Counts: counts}
	}
}

// handleWorkitemUpdate processes a WorkitemUpdateMsg at the root level.
// It updates the workitem list and, when on the detail screen, detects NODE
// changes to trigger HITL probe restart.
func (m *Model) handleWorkitemUpdate(msg WorkitemUpdateMsg) tea.Cmd {
	// Update the list
	switch msg.Event {
	case watch.Added:
		m.workitemList.Items = append(m.workitemList.Items, msg.Item)
	case watch.Modified:
		for i, item := range m.workitemList.Items {
			if item.Name == msg.Item.Name {
				// Detect NODE change for HITL probe restart on detail screen
				if m.screen == ScreenWorkitemDetail && m.workitemDetail.workitemName == item.Name {
					if item.Node != msg.Item.Node && m.hitlState != nil {
						// NODE changed — close HITL and start new probe cycle
						m.hitlState.Close(m.pfm)
						m.hitlState.ResetForNewWorkitem()
						m.workitemDetail.hitl.Visible = false
						if m.k8s != nil && msg.Item.Node != "" && msg.Item.Node != "-" {
							m.workitemList.Items[i] = msg.Item
							return tea.Batch(
								m.debouncedChildCountRefresh(),
								m.hitlState.Probe(m.ctx, m.k8s.CoreClient, m.namespace,
									msg.Item.Node, msg.Item.Name, m.pfm),
							)
						}
						// Update the model with the new node info even if probe can't start
						m.workitemDetail.workitemName = msg.Item.Name
						m.workitemDetail.detail.Node = msg.Item.Node
					}
				}
				m.workitemList.Items[i] = msg.Item
				break
			}
		}
	}
	return m.debouncedChildCountRefresh()
}

// ─── Screen-level handlers ─────────────────────────────────────────────────

// updateNamespaceSelect handles semantic messages for the namespace select screen.
func (m *Model) updateNamespaceSelect(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case NamespaceListLoadedMsg:
		m.namespaceSelector = m.namespaceSelector.SetNamespaces(msg.Namespaces, api.GetCurrentContextNamespace())
		m.err = nil

	case NamespaceFallbackMsg:
		// Fall back to the context namespace with error
		m.namespaceSelector.Loading = false
		m.namespaceSelector.Error = msg.Error.Error()
		// Auto-select fallback namespace
		m.namespace = msg.Namespace
		// Transition to workitem list with the fallback namespace
		m.workitemList.Namespace = msg.Namespace
		m.screen = ScreenWorkitemList
		return m, m.loadWorkitems()

	case NamespaceSelectedMsg:
		// Resolve system namespace for subsequent Archivist port-forward
		sysNS := msg.Namespace
		if m.k8s != nil {
			var err error
			sysNS, err = m.k8s.ResolveSystemNamespace(m.ctx, m.cfg.SystemNamespace, msg.Namespace)
			if err != nil {
				sysNS = msg.Namespace
			}
		}
		m.systemNS = sysNS
		m.namespace = msg.Namespace
		m.workitemList.Namespace = msg.Namespace
		m.screen = ScreenWorkitemList
		return m, m.loadWorkitems()
	}
	return m, nil
}

// updateWorkitemList handles semantic messages for the Workitem list screen.
func (m *Model) updateWorkitemList(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case WorkitemsLoadedMsg:
		m.workitemList.Items = msg.Items
		m.workitemList.Loading = false
		m.workitemList.Cursor = 0
		// Start the watch in background
		return m, m.startWorkitemWatch()

	case WorkitemLoadErrorMsg:
		m.workitemList.Loading = false
		m.workitemList.Error = msg.Error.Error()

	// Note: WorkitemUpdateMsg is handled at root level in handleWorkitemUpdate.
	// It is NOT dispatched to this handler.

	case WorkitemDeletedMsg:
		for i, item := range m.workitemList.Items {
			if item.Name == msg.Name {
				m.workitemList.Items = append(m.workitemList.Items[:i], m.workitemList.Items[i+1:]...)
				break
			}
		}

	case ChildCountsUpdatedMsg:
		// Update child counts on the relevant items
		for i, item := range m.workitemList.Items {
			if count, ok := msg.Counts[item.Name]; ok {
				m.workitemList.Items[i].ChildrenCount = count
			}
		}

	case WatchDisconnectedMsg:
		m.workitemList.Disconnected = true

	case WatchReconnectedMsg:
		m.workitemList.Disconnected = false

	case NamespaceRefreshMsg:
		m.workitemList.Loading = true
		m.workitemList.Items = nil
		m.namespace = msg.Namespace
		return m, m.loadWorkitems()

	case WorkitemSelectedMsg:
		// Fetch full Workitem detail for real data
		m.workitemDetail.workitemName = msg.Name
		m.workitemDetail.loading = true
		m.workitemDetail.loaded = true
		m.workitemDetail.statusBar.ScreenName = "Workitem Detail"
		m.workitemDetail.statusBar.WorkitemName = msg.Name
		m.workitemDetail.statusBar.Namespace = m.workitemList.Namespace
		m.workitemDetail.statusBar.Connected = true
		m.errorBanner = ""

		// Start loading topology, artefacts, and HITL in parallel
		cmds := []tea.Cmd{
			m.loadWorkitemDetail(msg.Name),
			m.loadTopology(),
			m.loadArtefacts(msg.Name),
		}

		m.screen = ScreenWorkitemDetail
		m.err = nil
		return m, tea.Batch(cmds...)

	case CreateStartMsg:
		// Transition to create wizard with fake data
		m.createWizard = components.NewCreateWizard()
		m.createWizard.FoundryFlows = []string{"main-flow"}
		m.createWizard.EntryNodes = []string{"forge", "human-entry"}
		m.createWizard.Artefacts = []string{"petition", "haiku"}
		m.screen = ScreenCreateWizard

	case DeleteConfirmMsg:
		// Delete blocked if not Completed/Failed
		for _, item := range m.workitemList.Items {
			if item.Name == msg.WorkitemName {
				if item.State != "Completed" && item.State != "Failed" {
					m.err = fmt.Errorf("cannot delete Workitem in %s state (only Completed/Failed allowed)", item.State)
				}
				break
			}
		}

	case DeleteResultMsg:
		if msg.Err != nil {
			m.err = fmt.Errorf("delete failed: %s (failed children: %v)", msg.Err, msg.FailedChildren)
		}
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
			return ErrorMsg{Source: "workitem-detail", Message: fmt.Sprintf("fetch detail: %v", err)}
		}
		return WorkitemDetailLoadedMsg{Detail: detail}
	}
}

// loadArtefacts connects to the Archivist and loads artefacts.
func (m *Model) loadArtefacts(workitemID string) tea.Cmd {
	return func() tea.Msg {
		// 1. Find the Archivist pod if needed
		if m.pfm == nil {
			return ErrorMsg{Source: "archivist-forward", Message: "no port-forward manager"}
		}

		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}

		// 2. Find a Ready Archivist pod
		archivistPod, found, err := m.pfm.FindReadyPod(m.systemNS, "app.kubernetes.io/name=flow-archivist")
		if err != nil {
			return ErrorMsg{Source: "archivist-forward", Message: fmt.Sprintf("find archivist pod: %v", err)}
		}
		if !found {
			return ErrorMsg{Source: "archivist-forward", Message: "no Ready archivist pod found in namespace " + m.systemNS}
		}

		// 3. Forward port 50054
		_, localPort, err := m.pfm.ForwardPod(ctx, m.systemNS, archivistPod, 50054)
		if err != nil {
			return ErrorMsg{Source: "archivist-forward", Message: fmt.Sprintf("port-forward: %v", err)}
		}

		// 4. Create Archivist client
		archivist, err := api.NewArchivistClient(fmt.Sprintf("localhost:%d", localPort))
		if err != nil {
			return ErrorMsg{Source: "archivist-forward", Message: fmt.Sprintf("connect: %v", err)}
		}

		// Store for reuse
		if m.archivist != nil {
			m.archivist.Close()
		}
		m.archivist = archivist

		// 5. List artefacts
		artefacts, err := archivist.ListArtefacts(ctx, m.namespace, workitemID)
		if err != nil {
			return ArtefactLoadErrorMsg{WorkitemID: workitemID, Error: err}
		}

		return ArtefactsLoadedMsg{WorkitemID: workitemID, Artefacts: artefacts}
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

// updateWorkitemDetail handles semantic messages for the detail screen.
func (m *Model) updateWorkitemDetail(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			probeCmd := m.hitlState.Probe(m.ctx, m.k8s.CoreClient, m.namespace,
				currentNode, msg.Detail.Name, m.pfm)
			if probeCmd != nil {
				cmds = append(cmds, probeCmd)
			}
			return m, tea.Batch(cmds...)
		}

	case ArtefactsLoadedMsg:
		// Preserve expansion state across refreshes
		expandedSet := make(map[string]bool)
		for _, art := range m.workitemDetail.artefacts.Artefacts {
			if art.Expanded {
				expandedSet[art.ArtefactID] = true
			}
		}

		// Convert and sort artefacts
		nodes := make([]types.ArtefactNode, len(msg.Artefacts))
		for i, a := range msg.Artefacts {
			nodes[i] = toArtefactNode(a)
			if expandedSet[a.ID] {
				nodes[i].Expanded = true
				// Trigger re-fetch for expanded artefacts
				// This is batched; individual expand triggers content/feedback loading
			}
		}
		m.workitemDetail.artefacts.Artefacts = nodes
		m.workitemDetail.artefacts.Loading = false
		m.errorBanner = ""

		// For expanded artefacts, re-fetch content and feedback
		var cmds []tea.Cmd
		for _, art := range m.workitemDetail.artefacts.Artefacts {
			if art.Expanded && m.archivist != nil {
				cmds = append(cmds, m.fetchArtefactContent(msg.WorkitemID, art.ArtefactID))
			}
		}
		if len(cmds) > 0 {
			return m, tea.Batch(cmds...)
		}

	case ArtefactLoadErrorMsg:
		m.workitemDetail.artefacts.Loading = false
		if msg.ArtefactID == "" {
			// List failed
			m.workitemDetail.artefacts.Error = fmt.Sprintf("Artefacts unavailable: %v", msg.Error)
		} else {
			// Per-artefact failure — show in the artefact node
			for i, art := range m.workitemDetail.artefacts.Artefacts {
				if art.ArtefactID == msg.ArtefactID {
					m.workitemDetail.artefacts.Artefacts[i].Content = fmt.Sprintf("Failed to load: %v", msg.Error)
					break
				}
			}
		}

	case ArtefactExpandedMsg:
		for i, art := range m.workitemDetail.artefacts.Artefacts {
			if art.ArtefactID == msg.ArtefactID {
				m.workitemDetail.artefacts.Artefacts[i].Expanded = true
				if msg.IsBinary {
					m.workitemDetail.artefacts.Artefacts[i].IsBinary = true
					m.workitemDetail.artefacts.Artefacts[i].BinarySize = msg.BinarySize
				}
				m.workitemDetail.artefacts.Artefacts[i].Content = msg.Content
				m.workitemDetail.artefacts.Artefacts[i].Feedback = convertAPIFeedback(msg.FeedbackItems)
				break
			}
		}

	case ArtefactCollapsedMsg:
		for i, art := range m.workitemDetail.artefacts.Artefacts {
			if art.ArtefactID == msg.ArtefactID {
				m.workitemDetail.artefacts.Artefacts[i].Expanded = false
				m.workitemDetail.artefacts.Artefacts[i].Content = ""
				m.workitemDetail.artefacts.Artefacts[i].Feedback = nil
				break
			}
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

	case components.HitlProbeResultMsg:
		// HitlState was already populated by the Probe cmd goroutine.
		// Populate the rendering component from the message.
		m.workitemDetail.hitl.Visible = true
		m.workitemDetail.hitl.QueueItemID = msg.WorkitemID
		m.workitemDetail.hitl.Choices = toChoices(msg.Choices)
		m.workitemDetail.hitl.Loading = false
		m.statusMessage = ""

	case components.HitlProbeRetryMsg:
		if m.hitlState != nil && !m.hitlState.Exhausted() {
			return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return HitlProbeTriggerMsg{}
			})
		}

	case components.HitlProbeExhaustedMsg:
		if m.hitlState != nil {
			m.hitlState.Close(m.pfm)
		}
		m.statusMessage = msg.Diagnostic

	case components.HitlChoicesBlockedMsg:
		if m.hitlState != nil {
			m.hitlState.Close(m.pfm)
		}
		m.statusMessage = fmt.Sprintf("Unable to load choices: %s", msg.Err)

	case HitlProbeTriggerMsg:
		if m.hitlState != nil && !m.hitlState.Exhausted() && m.workitemDetail.workitemName != "" {
			return m, m.hitlState.Probe(m.ctx, m.k8s.CoreClient, m.namespace,
				m.hitlState.GetNodeName(), m.hitlState.GetWorkitemID(), m.pfm)
		}

	case HitlDecidedMsg:
		m.workitemDetail.hitl.Visible = false
		m.workitemDetail.hitl.Error = ""
		m.statusMessage = fmt.Sprintf("Decision '%s' submitted", msg.Choice)
		// Trigger refresh of detail and artefacts
		var cmds []tea.Cmd
		if m.workitemDetail.workitemName != "" {
			cmds = append(cmds, m.loadWorkitemDetail(m.workitemDetail.workitemName))
		}
		if refreshCmd := m.RefreshArtefacts(); refreshCmd != nil {
			cmds = append(cmds, refreshCmd)
		}
		if len(cmds) > 0 {
			return m, tea.Batch(cmds...)
		}
		return m, nil

	case HitlErrorMsg:
		if !msg.Retryable {
			m.workitemDetail.hitl.Visible = false
		} else {
			m.workitemDetail.hitl.Error = msg.Err.Error()
			m.workitemDetail.hitl.ErrorRetry = msg.Retryable
		}
		// Handle specific error codes per the spec error table
		if msg.Err != nil {
			switch {
			case api.IsQueueItemNotFound(msg.Err):
				if m.hitlState != nil {
					m.hitlState.Close(m.pfm)
				}
				m.workitemDetail.hitl.Visible = false
				m.statusMessage = "Queue item no longer exists — refreshing..."
				var cmds []tea.Cmd
				if m.workitemDetail.workitemName != "" {
					cmds = append(cmds, m.loadWorkitemDetail(m.workitemDetail.workitemName))
				}
				if refreshCmd := m.RefreshArtefacts(); refreshCmd != nil {
					cmds = append(cmds, refreshCmd)
				}
				if len(cmds) > 0 {
					return m, tea.Batch(cmds...)
				}
				return m, nil
			case api.IsAlreadyClaimed(msg.Err):
				m.statusMessage = "Already claimed by another client — press 'r' to retry"
			case api.IsInvalidState(msg.Err):
				m.statusMessage = "Item in unexpected state — press 'r' to retry"
			case api.IsQueueUnavailable(msg.Err):
				if m.hitlState != nil && m.hitlState.GetPendingChoice() != "" {
					m.statusMessage = "Queue unavailable — retrying..."
					choice := m.hitlState.GetPendingChoice()
					return m, func() tea.Msg {
						err := m.hitlState.RetryQueueUnavailable(m.ctx, func(ctx context.Context) error {
							return m.hitlState.ClaimAndDecide(ctx, choice)
						})
						if err != nil {
							return HitlErrorMsg{
								WorkitemID: m.selectedWorkitemName(),
								Err:        err,
								Retryable:  false,
							}
						}
						return HitlDecidedMsg{
							WorkitemID: m.selectedWorkitemName(),
							Choice:     choice,
						}
					}
				}
			case api.IsBadRequest(msg.Err):
				m.statusMessage = fmt.Sprintf("Invalid request: %s", msg.Err)
			default:
				m.statusMessage = fmt.Sprintf("HITL error: %s — press 'r' to retry", msg.Err)
			}
		}
		if msg.DebugHint != "" {
			m.debugHint = msg.DebugHint
		}

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
			m.loadTopology(),
		}
		if m.archivist != nil {
			cmds = append(cmds, m.RefreshArtefacts())
		} else {
			cmds = append(cmds, m.loadArtefacts(workitemID))
		}
		return m, tea.Batch(cmds...)

	case CreateStartMsg:
		// Open create wizard from detail screen
		m.createWizard = components.NewCreateWizard()
		m.createWizard.FoundryFlows = []string{"main-flow"}
		m.createWizard.EntryNodes = []string{"forge", "human-entry"}
		m.createWizard.Artefacts = []string{"petition", "haiku"}
		m.screen = ScreenCreateWizard

	case ErrorMsg:
		// Show as error banner at top of detail screen
		m.errorBanner = fmt.Sprintf("⚠ %s: %s", msg.Source, msg.Message)
		// Clear error banner after 10 seconds
		return m, tea.Tick(10*time.Second, func(t time.Time) tea.Msg {
			return ClearErrorBannerMsg{}
		})

	case ClearErrorBannerMsg:
		m.errorBanner = ""
	}
	return m, nil
}

// ClearErrorBannerMsg clears the error banner.
type ClearErrorBannerMsg struct{}

// fetchArtefactContent fetches content and feedback for an expanded artefact.
func (m *Model) fetchArtefactContent(workitemID, artefactID string) tea.Cmd {
	return func() tea.Msg {
		if m.archivist == nil {
			return ArtefactExpandedMsg{
				WorkitemID:    workitemID,
				ArtefactID:    artefactID,
				Content:       "Archivist not connected",
				FeedbackItems: nil,
			}
		}

		// Derive context from model or use background
		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}

		// Get content
		content, err := m.archivist.GetArtefact(ctx, m.namespace, workitemID, artefactID)
		if err != nil {
			return ArtefactExpandedMsg{
				WorkitemID:    workitemID,
				ArtefactID:    artefactID,
				Content:       fmt.Sprintf("Failed to load content: %v", err),
				FeedbackItems: nil,
			}
		}

		// Format content (text or hex preview)
		contentStr, isBinary, binarySize := formatArtefactContent(content)

		// Get feedback
		feedback, err := m.archivist.GetFeedback(ctx, m.namespace, workitemID, artefactID)
		if err != nil {
			// Non-fatal: show content without feedback
			feedback = nil
		}

		return ArtefactExpandedMsg{
			WorkitemID:    workitemID,
			ArtefactID:    artefactID,
			Content:       contentStr,
			IsBinary:      isBinary,
			BinarySize:    binarySize,
			FeedbackItems: feedback,
		}
	}
}

// updateCreateWizard handles semantic messages for the create wizard.
func (m *Model) updateCreateWizard(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case CreateStartMsg:
		// Initialise wizard with fake data
		m.createWizard = components.NewCreateWizard()
		m.createWizard.FoundryFlows = []string{"main-flow"}
		m.createWizard.EntryNodes = []string{"forge", "human-entry"}
		m.createWizard.Artefacts = []string{"petition", "haiku"}
		m.createWizard.Step = 0

	case CreateFieldUpdatedMsg:
		switch msg.Field {
		case "prompt":
			m.createWizard.Fields.PromptText = msg.Value
		case "entryNode":
			m.createWizard.Fields.EntryNode = msg.Value
		case "artefactID":
			m.createWizard.Fields.ArtefactID = msg.Value
		case "governedArtefact":
			m.createWizard.Fields.GovernedArtefact = msg.Value
		}

	case CreateConfirmMsg:
		// Simulate success with fake name
		seq := rand.Intn(99999)
		m.createWizard.SuccessName = fmt.Sprintf("wi-%05d", seq)
		m.createWizard.Step = 5

	case CreateSuccessMsg:
		m.workitemDetail.workitemName = msg.WorkitemName
		m.workitemDetail.loading = false
		m.workitemDetail.loaded = true
		// Populate with fake data
		m.workitemDetail.topology = fakeTopology()
		m.workitemDetail.artefacts = fakeArtefacts()
		m.screen = ScreenWorkitemDetail
		m.err = nil

	case CreateErrorMsg:
		m.createWizard.Error = msg.Err.Error()

	case CreateCancelMsg:
		// Return to workitem list
		m.screen = ScreenWorkitemList
	}
	return m, nil
}

// ─── Component-level key handlers ──────────────────────────────────────────

func (m *Model) updateNamespaceSelectKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.namespaceSelector, _ = m.namespaceSelector.Update(msg)
	return m, nil
}

func (m *Model) updateWorkitemListKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.workitemList, _ = m.workitemList.Update(msg)
	return m, nil
}

func (m *Model) updateWorkitemDetailKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.workitemDetail.artefacts, _ = m.workitemDetail.artefacts.Update(msg)

	// HITL action keys — only when HITL is active and not confirming cancel
	if m.hitlState != nil && m.hitlState.Active() && m.workitemDetail.hitl.Visible && !m.workitemDetail.hitl.ConfirmingCancel {
		key := msg.String()

		// Handle 'r' for retry
		if key == "r" {
			m.workitemDetail.hitl.Error = ""
			return m, nil
		}

		// Handle 'y'/'n' for cancel confirmation (handled by component Update too)
		if key == "y" && m.workitemDetail.hitl.ConfirmingCancel {
			// Confirm cancel — claim and decide with the pending choice
			pendingChoice := m.workitemDetail.hitl.PendingChoice
			m.workitemDetail.hitl.ConfirmingCancel = false
			m.workitemDetail.hitl.PendingChoice = ""
			m.workitemDetail.hitl.Loading = true
			if m.ctx == nil {
				m.ctx = context.Background()
			}
			return m, func() tea.Msg {
				err := m.hitlState.ClaimAndDecide(m.ctx, pendingChoice)
				if err != nil {
					return HitlErrorMsg{
						WorkitemID: m.hitlState.GetWorkitemID(),
						Err:        err,
						Retryable:  api.IsQueueUnavailable(err) || api.IsAlreadyClaimed(err) || api.IsInvalidState(err),
					}
				}
				return HitlDecidedMsg{
					WorkitemID: m.hitlState.GetWorkitemID(),
					Choice:     pendingChoice,
				}
			}
		}

		// Match choice keys: first letter of each choice label
		choices := m.workitemDetail.hitl.Choices
		if len(choices) == 0 {
			choices = defaultChoices
		}
		for _, ch := range choices {
			if len(ch.Label) == 0 {
				continue
			}
			shortcut := strings.ToLower(string(ch.Label[0]))
			if key == shortcut {
				if ch.Type == "cancel" {
					// Show confirmation prompt
					m.workitemDetail.hitl.ConfirmingCancel = true
					m.workitemDetail.hitl.PendingChoice = ch.Value
					return m, nil
				}
				// Direct route — claim and decide
				m.workitemDetail.hitl.Loading = true
				if m.ctx == nil {
					m.ctx = context.Background()
				}
				return m, func() tea.Msg {
					err := m.hitlState.ClaimAndDecide(m.ctx, ch.Value)
					if err != nil {
						return HitlErrorMsg{
							WorkitemID: m.hitlState.GetWorkitemID(),
							Err:        err,
							Retryable:  api.IsQueueUnavailable(err) || api.IsAlreadyClaimed(err) || api.IsInvalidState(err),
						}
					}
					return HitlDecidedMsg{
						WorkitemID: m.hitlState.GetWorkitemID(),
						Choice:     ch.Value,
					}
				}
			}
		}
	}

	// Handle artefact expand/collapse at root level for network calls
	if msg.String() == "enter" || msg.String() == "right" {
		artefacts := &m.workitemDetail.artefacts
		if artefacts.Loading || len(artefacts.Artefacts) == 0 {
			return m, nil
		}
		cursor := artefacts.Cursor
		if cursor < 0 || cursor >= len(artefacts.Artefacts) {
			return m, nil
		}
		art := artefacts.Artefacts[cursor]
		if art.Expanded {
			// Already expanded — collapse handled by component; no network call needed
			return m, nil
		}
		// Expanded — fetch content and feedback
		workitemID := m.workitemDetail.workitemName
		if workitemID != "" && m.archivist != nil {
			return m, m.fetchArtefactContent(workitemID, art.ArtefactID)
		}
	}

	return m, nil
}

func (m *Model) updateCreateWizardKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.createWizard, _ = m.createWizard.Update(msg)
	return m, nil
}

// ─── Fake data generators (placeholder — Phase 05+ replaces with real data) ─

func fakeTopology() components.FlowTopologyModel {
	return components.FlowTopologyModel{
		Loading: false,
		Nodes: []types.TopologyNode{
			{Name: "forge", Color: types.TopologyVisited},
			{Name: "sort", Color: types.TopologyCurrent},
			{Name: "human-approval", Color: types.TopologyUnvisited},
			{Name: "refine", Color: types.TopologyUnvisited},
		},
		Edges: []types.TopologyEdge{
			{From: "forge", To: "sort"},
			{From: "sort", To: "human-approval"},
			{From: "sort", To: "refine"},
			{From: "human-approval", To: "refine"},
		},
	}
}

func fakeArtefacts() components.ArtefactTreeModel {
	return components.ArtefactTreeModel{
		Loading: false,
		Artefacts: []types.ArtefactNode{
			{
				ArtefactID: "haiku",
				GovernedBy: "haiku",
				Expanded:   false,
				Content:    "",
				IsBinary:   false,
				Feedback: []types.FeedbackItem{
					{ID: "fb-1", State: "NEW", SourceNode: "reviewer", Message: "missing seasonal reference", Timestamp: "2024-01-01T00:00:01Z"},
					{ID: "fb-2", State: "RESOLVED", SourceNode: "sort", Message: "syllable count fixed", Timestamp: "2024-01-01T00:00:02Z"},
				},
			},
			{
				ArtefactID: "petition",
				GovernedBy: "petition",
				Expanded:   false,
				Content:    "",
				IsBinary:   false,
				Feedback:   nil,
			},
		},
	}
}

func fakeHitlProbe(name string) components.HitlPromptModel {
	return components.HitlPromptModel{
		Visible:     true,
		QueueItemID: name,
		Choices:     defaultChoices,
		Loading:     false,
	}
}
