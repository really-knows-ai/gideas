package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gideas/flow/tools/flowctl/internal/api"
	"github.com/gideas/flow/tools/flowctl/internal/config"
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
	cfg        *config.Config
	k8s        *api.K8sClient
	width      int
	height     int
	screen     Screen
	err        error
	Program    *tea.Program // exported so main.go can set it after tea.NewProgram
	ctx        context.Context
	namespace  string // resolved after namespace selection
	systemNS   string // resolved after namespace selection

	// Sub-states for each screen
	namespaceSelector components.NamespaceSelectorModel
	workitemList      components.WorkitemListModel
	workitemDetail    WorkitemDetailModel
	createWizard      components.CreateWizardModel

	// Debounce timer for child-count recomputation on watch batches
	childCountDebounce *time.Timer
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

// NewModel creates a root Model with K8s client, config, and context.
func NewModel(k8s *api.K8sClient, cfg *config.Config, ctx context.Context) Model {
	m := initialModel()
	m.k8s = k8s
	m.cfg = cfg
	m.ctx = ctx
	return m
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

// Init starts the namespace loading sequence.
func (m *Model) Init() tea.Cmd {
	if m.cfg == nil {
		return nil
	}
	// If --namespace is set, skip the namespace selector entirely
	if m.cfg.Namespace != "" {
		m.namespace = m.cfg.Namespace
		return m.loadWorkitems()
	}
	return func() tea.Msg {
		return m.loadNamespaces()
	}
}
