package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foundry/flow/tools/flowctl/internal/api"
	"github.com/foundry/flow/tools/flowctl/internal/config"
	"github.com/foundry/flow/tools/flowctl/internal/tui/components"
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
	cfg       *config.Config
	k8s       *api.K8sClient
	pfm       api.PortForwarder
	width     int
	height    int
	screen    Screen
	err       error
	Program   *tea.Program // exported so main.go can set it after tea.NewProgram
	ctx       context.Context
	namespace string // resolved after namespace selection
	systemNS  string // resolved after namespace selection

	// Archivist client (created on Workitem detail entry)
	archivist *api.ArchivistClient

	// Error banner displayed at top of screen
	errorBanner string

	// Transient banner (from BannerMsg) that auto-dismisses after timeout
	banner        string
	bannerSource  string
	bannerTimeout bool // true if banner has an auto-dismiss timer

	// Log writer for FLOW_LOG_FILE
	logWriter *LogWriter

	// Create wizard data — populated by loadWizardData for contract-based artefact filtering
	wizardEntryContracts map[string]interface{} // entry contract name -> governed artefact keys
	wizardNodeEntryMap   map[string]string      // node name -> entry contract name

	// HITL lifecycle manager (created on Init with cfg.HitlPort)
	hitlState *components.HitlState

	// Status message displayed in the detail view status bar
	statusMessage string
	// Debug hint shown when --hitl-port != 8080 and all probes fail
	debugHint string

	// Delete confirmation state — non-empty means we are awaiting y/N
	deleteConfirmWorkitem string

	// Create retry state — tracks whether CRD and artefact were already created
	createHasCRD      bool
	createHasArtefact bool

	// Sub-states for each screen
	namespaceSelector components.NamespaceSelectorModel
	workitemList      components.WorkitemListModel
	workitemDetail    WorkitemDetailModel
	createWizard      components.CreateWizardModel

	// Debounce timer for child-count recomputation on watch batches
	childCountDebounce *time.Timer

	// childCountGeneration is bumped on every debounce re-arm so that a stale
	// in-flight refresh can be detected and its message discarded before being
	// applied over newer state. Only touched from the Update loop goroutine.
	childCountGeneration uint64
	// childCountWake is the per-arming wake channel. Re-arming closes the
	// previous channel to release a still-pending waiter rather than strand it
	// on the shared timer channel (one fire would wake only one of many
	// waiters). Only touched from the Update loop goroutine.
	childCountWake chan struct{}

	// Watch context and cancel for explicit K8s watch lifecycle management
	watchCtx    context.Context
	watchCancel context.CancelFunc
}

// WorkitemDetailModel holds the sub-components for the detail screen.
type WorkitemDetailModel struct {
	loading      bool
	loaded       bool
	workitemName string

	// The resolved Workitem detail (populated on selection)
	detail *api.WorkitemDetail

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
	m.hitlState = components.NewHitlState(cfg.HitlPort)
	m.logWriter = NewLogWriter(cfg.LogFile)
	return m
}

// NewModelWithPFM creates a root Model with K8s client, PortForwarder, config, and context.
func NewModelWithPFM(k8s *api.K8sClient, pfm api.PortForwarder, cfg *config.Config, ctx context.Context) Model {
	m := NewModel(k8s, cfg, ctx)
	m.pfm = pfm
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
	// If --namespace or FLOW_NAMESPACE was explicitly set, skip the selector
	if m.cfg.NamespaceExplicit {
		m.namespace = m.cfg.Namespace
		// Resolve system namespace for subsequent Archivist port-forward
		sysNS := m.namespace
		if m.k8s != nil {
			var err error
			sysNS, err = m.k8s.ResolveSystemNamespace(m.ctx, m.cfg.SystemNamespace, m.namespace)
			if err != nil {
				sysNS = m.namespace
			}
		}
		m.systemNS = sysNS
		return tea.Batch(m.loadWorkitems(), m.connectArchivist())
	}
	return func() tea.Msg {
		return m.loadNamespaces()
	}
}

// selectedWorkitemName returns the name of the currently selected workitem.
func (m *Model) selectedWorkitemName() string {
	if m.workitemDetail.workitemName != "" {
		return m.workitemDetail.workitemName
	}
	if m.workitemList.Cursor >= 0 && m.workitemList.Cursor < len(m.workitemList.Items) {
		return m.workitemList.Items[m.workitemList.Cursor].Name
	}
	return ""
}

// closeAll closes all open connections: HITL port-forward, Archivist port-forward,
// K8s watch, gRPC connection. Called on Ctrl+C or q.
func (m *Model) closeAll() {
	// 0. Cancel K8s watch context (stops the watch goroutine)
	if m.watchCancel != nil {
		m.watchCancel()
	}
	// 1. Close HITL port-forward (if open)
	if m.hitlState != nil && m.pfm != nil {
		m.hitlState.Close(m.pfm)
	}
	// 2. Close Archivist port-forward (if any)
	if m.pfm != nil {
		_ = m.pfm.CloseAll()
	}
	// 3. Close gRPC connection
	if m.archivist != nil {
		m.archivist.Close()
	}
	// 4. Close log writer
	if m.logWriter != nil {
		m.logWriter.Close()
	}
}

// RefreshArtefacts re-fetches artefacts and feedback for the currently selected workitem.
// It preserves expansion state so expanded artefacts stay expanded after refresh.
func (m *Model) RefreshArtefacts() tea.Cmd {
	if m.workitemDetail.workitemName == "" || m.archivist == nil {
		return nil
	}
	return func() tea.Msg {
		artefacts, err := m.archivist.ListArtefacts(context.Background(), m.namespace, m.workitemDetail.workitemName)
		if err != nil {
			return ArtefactLoadErrorMsg{WorkitemID: m.workitemDetail.workitemName, Error: err}
		}
		return ArtefactsLoadedMsg{WorkitemID: m.workitemDetail.workitemName, Artefacts: artefacts}
	}
}
