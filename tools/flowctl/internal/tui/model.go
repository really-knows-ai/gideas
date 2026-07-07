package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/gideas/flow/tools/flowctl/internal/tui/components"
)

// Screen represents the current screen in the TUI.
type Screen int

const (
	ScreenNamespaceSelect Screen = iota
	ScreenWorkitemList
	ScreenWorkitemDetail
	ScreenCreateWizard
)

// Model is the root bubbletea Model for the flowctl TUI.
type Model struct {
	screen Screen
	width  int
	height int
	err    error

	// Sub-states for each screen
	namespaceSelector components.NamespaceSelectorModel
	workitemList      components.WorkitemListModel
	workitemDetail    WorkitemDetailModel
	createWizard      components.CreateWizardModel
}

// WorkitemDetailModel holds the sub-components for the detail screen.
type WorkitemDetailModel struct {
	loading      bool
	loaded       bool
	workitemName string

	// Sub-components
	statusBar components.StatusBarModel
	topology  components.FlowTopologyModel
	artefacts components.ArtefactTreeModel
	hitl      components.HitlPromptModel
}

// initialModel creates the root Model with all sub-components in their initial states.
func initialModel() Model {
	return Model{
		screen:            ScreenNamespaceSelect,
		namespaceSelector: components.NewNamespaceSelector(),
		workitemList:      components.NewWorkitemList(),
		workitemDetail: WorkitemDetailModel{
			statusBar: components.NewStatusBar(),
			topology:  components.NewFlowTopology(),
			artefacts: components.NewArtefactTree(),
			hitl:      components.NewHitlPrompt(),
		},
		createWizard: components.NewCreateWizard(),
	}
}

// Init returns nil — no startup commands in this phase.
// Phase 04 replaces this with tea.Batch to load namespaces on launch.
func (m *Model) Init() tea.Cmd {
	return nil
}
