package tui

import (
	"fmt"
	"math/rand"

	tea "github.com/charmbracelet/bubbletea"

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

		// NamespaceSelect: enter → NamespaceSelectedMsg
		if m.screen == ScreenNamespaceSelect && msg.String() == "enter" {
			selected := m.namespaceSelector.Namespaces[m.namespaceSelector.Cursor]
			return m.routeMsg(NamespaceSelectedMsg{Namespace: selected})
		}

		// WorkitemList: enter → WorkitemSelectedMsg
		if m.screen == ScreenWorkitemList && msg.String() == "enter" {
			if m.workitemList.Cursor < len(m.workitemList.Items) {
				return m.routeMsg(WorkitemSelectedMsg{Name: m.workitemList.Items[m.workitemList.Cursor].Name})
			}
			return m, nil
		}

		// WorkitemList: n → CreateStartMsg
		if m.screen == ScreenWorkitemList && msg.String() == "n" {
			return m.routeMsg(CreateStartMsg{})
		}

		// WorkitemList: d → DeleteConfirmMsg
		if m.screen == ScreenWorkitemList && msg.String() == "d" {
			if m.workitemList.Cursor < len(m.workitemList.Items) {
				item := m.workitemList.Items[m.workitemList.Cursor]
				return m.routeMsg(DeleteConfirmMsg{WorkitemName: item.Name, Phase: item.State})
			}
			return m, nil
		}

		// WorkitemList: r → RefreshMsg (no-op in Phase 02)
		if m.screen == ScreenWorkitemList && msg.String() == "r" {
			return m, nil
		}

		// WorkitemList: esc/backspace → return to namespace selection
		if m.screen == ScreenWorkitemList && (msg.String() == "esc" || msg.String() == "backspace") {
			m.screen = ScreenNamespaceSelect
			m.err = nil
			return m, nil
		}

		// WorkitemDetail: n → CreateStartMsg
		if m.screen == ScreenWorkitemDetail && msg.String() == "n" {
			return m.routeMsg(CreateStartMsg{})
		}

		// WorkitemDetail: r → RefreshMsg
		if m.screen == ScreenWorkitemDetail && msg.String() == "r" {
			return m.routeMsg(RefreshMsg{})
		}

		// WorkitemDetail: esc/backspace → return to list
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

// ─── Screen-level handlers ─────────────────────────────────────────────────

// updateNamespaceSelect handles semantic messages for the namespace select screen.
func (m *Model) updateNamespaceSelect(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case NamespacesLoadedMsg:
		m.namespaceSelector = m.namespaceSelector.SetNamespaces(msg.Namespaces, msg.Current)
		m.err = nil

	case NamespaceSelectedMsg:
		// Transition to WorkitemList with fake data
		m.workitemList.Namespace = msg.Namespace
		m.workitemList.Items = fakeWorkitemSummaries()
		m.workitemList.Loading = false
		m.workitemList.Cursor = 0
		m.workitemList.Watching = true
		m.screen = ScreenWorkitemList
		m.err = nil

	case NamespaceLoadErrorMsg:
		m.namespaceSelector.Loading = false
		m.namespaceSelector.Error = msg.Err.Error()
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

	case WorkitemUpdateMsg:
		for i, item := range m.workitemList.Items {
			if item.Name == msg.Item.Name {
				m.workitemList.Items[i] = msg.Item
				break
			}
		}

	case WatchDisconnectedMsg:
		m.workitemList.Disconnected = true

	case WatchReconnectedMsg:
		m.workitemList.Disconnected = false

	case NamespaceRefreshMsg:
		m.workitemList.Loading = true
		m.workitemList.Items = nil

	case WorkitemSelectedMsg:
		// Transition to detail with fake data
		m.workitemDetail.workitemName = msg.Name
		m.workitemDetail.loading = false
		m.workitemDetail.loaded = true
		m.workitemDetail.statusBar.ScreenName = "Workitem Detail"
		m.workitemDetail.statusBar.WorkitemName = msg.Name
		m.workitemDetail.statusBar.Namespace = m.workitemList.Namespace
		m.workitemDetail.statusBar.State = "Running"
		m.workitemDetail.statusBar.Connected = true

		// Fake topology
		m.workitemDetail.topology = fakeTopology()

		// Fake artefacts
		m.workitemDetail.artefacts = fakeArtefacts()

		// Fake HITL probe result
		m.workitemDetail.hitl = fakeHitlProbe(msg.Name)

		m.screen = ScreenWorkitemDetail
		m.err = nil

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

// ─── Fake data generators ──────────────────────────────────────────────────

func fakeWorkitemSummaries() []types.WorkitemSummary {
	return []types.WorkitemSummary{
		{Name: "wi-pending", State: "Pending", Node: "forge", ChildrenCount: 0, Age: "30s"},
		{Name: "wi-running", State: "Running", Node: "sort", ChildrenCount: 2, Age: "2m"},
		{Name: "wi-complete", State: "Completed", Node: "-", ChildrenCount: 1, Age: "12m"},
		{Name: "wi-failed", State: "Failed", Node: "-", ChildrenCount: 0, Age: "15m"},
		{Name: "wi-suspended", State: "Suspended", Node: "human-approval", ChildrenCount: 0, Age: "8m"},
	}
}

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
		Visible:   true,
		QueueItemID: name,
		Choices:   defaultChoices,
		Loading:   false,
	}
}


