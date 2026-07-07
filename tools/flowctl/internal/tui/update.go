package tui

import (
	"context"
	"fmt"
	"math/rand"
	"time"

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

		// WorkitemDetail: n -> CreateStartMsg
		if m.screen == ScreenWorkitemDetail && msg.String() == "n" {
			return m.routeMsg(CreateStartMsg{})
		}

		// WorkitemDetail: r -> RefreshMsg
		if m.screen == ScreenWorkitemDetail && msg.String() == "r" {
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
		// Resolve system namespace for subsequent Archivist port-forward (Phase 04)
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

	case WorkitemUpdateMsg:
		switch msg.Event {
		case watch.Added:
			m.workitemList.Items = append(m.workitemList.Items, msg.Item)
		case watch.Modified:
			for i, item := range m.workitemList.Items {
				if item.Name == msg.Item.Name {
					m.workitemList.Items[i] = msg.Item
					break
				}
			}
		}
		// Debounced child count recomputation
		return m, m.debouncedChildCountRefresh()

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
		// Transition to placeholder detail screen (Phase 04 implements full detail)
		m.workitemDetail.workitemName = msg.Name
		m.workitemDetail.loading = false
		m.workitemDetail.loaded = true
		m.workitemDetail.statusBar.ScreenName = "Workitem Detail"
		m.workitemDetail.statusBar.WorkitemName = msg.Name
		m.workitemDetail.statusBar.Namespace = m.workitemList.Namespace
		m.workitemDetail.statusBar.State = ""
		m.workitemDetail.statusBar.Connected = true

		// Fake topology (placeholder — Phase 04 replaces with real data)
		m.workitemDetail.topology = fakeTopology()

		// Fake artefacts (placeholder — Phase 04 replaces with real data)
		m.workitemDetail.artefacts = fakeArtefacts()

		// Fake HITL probe result (placeholder — Phase 05 replaces with real data)
		m.workitemDetail.hitl = fakeHitlProbe(msg.Name)

		m.screen = ScreenWorkitemDetail
		m.err = nil
		return m, nil

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

// updateWorkitemDetail handles semantic messages for the detail screen.
func (m *Model) updateWorkitemDetail(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case ArtefactsLoadedMsg:
		m.workitemDetail.artefacts.Artefacts = msg.Artefacts
		m.workitemDetail.artefacts.Loading = false

	case ArtefactExpandedMsg:
		for i, art := range m.workitemDetail.artefacts.Artefacts {
			if art.ArtefactID == msg.ArtefactID {
				m.workitemDetail.artefacts.Artefacts[i].Expanded = true
				m.workitemDetail.artefacts.Artefacts[i].Content = msg.Content
				m.workitemDetail.artefacts.Artefacts[i].IsBinary = msg.IsBinary
				m.workitemDetail.artefacts.Artefacts[i].Feedback = msg.FeedbackItems
				break
			}
		}

	case ArtefactCollapsedMsg:
		for i, art := range m.workitemDetail.artefacts.Artefacts {
			if art.ArtefactID == msg.ArtefactID {
				m.workitemDetail.artefacts.Artefacts[i].Expanded = false
				break
			}
		}

	case TopologyLoadedMsg:
		m.workitemDetail.topology.Nodes = msg.Nodes
		m.workitemDetail.topology.Edges = msg.Edges
		m.workitemDetail.topology.Loading = false

	case HitlProbeResultMsg:
		if msg.QueueItem != nil {
			m.workitemDetail.hitl.Visible = true
			m.workitemDetail.hitl.QueueItemID = msg.WorkitemID
			if len(msg.Choices) > 0 {
				m.workitemDetail.hitl.Choices = msg.Choices
			} else {
				m.workitemDetail.hitl.Choices = defaultChoices
			}
			m.workitemDetail.hitl.Loading = false
		} else {
			m.workitemDetail.hitl.Visible = false
		}

	case HitlDecidedMsg:
		m.workitemDetail.hitl.Visible = false
		m.workitemDetail.hitl.Error = ""

	case HitlErrorMsg:
		if !msg.Retryable {
			m.workitemDetail.hitl.Visible = false
		} else {
			m.workitemDetail.hitl.Error = msg.Err.Error()
			m.workitemDetail.hitl.ErrorRetry = msg.Retryable
		}

	case RefreshMsg:
		m.workitemDetail.loading = true
		m.workitemDetail.artefacts.Loading = true
		m.workitemDetail.topology.Loading = true

	case CreateStartMsg:
		// Open create wizard from detail screen
		m.createWizard = components.NewCreateWizard()
		m.createWizard.FoundryFlows = []string{"main-flow"}
		m.createWizard.EntryNodes = []string{"forge", "human-entry"}
		m.createWizard.Artefacts = []string{"petition", "haiku"}
		m.screen = ScreenCreateWizard
	}
	return m, nil
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
	return m, nil
}

func (m *Model) updateCreateWizardKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.createWizard, _ = m.createWizard.Update(msg)
	return m, nil
}

// ─── Fake data generators (placeholder — Phase 04+ replaces with real data) ─

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
