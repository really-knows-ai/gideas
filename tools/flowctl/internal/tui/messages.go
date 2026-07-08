package tui

import (
	"github.com/gideas/flow/tools/flowctl/internal/api"
	"github.com/gideas/flow/tools/flowctl/internal/tui/types"
	"k8s.io/apimachinery/pkg/watch"
)

// ─── Namespace messages ───────────────────────────────────────────────────

// NamespaceListLoadedMsg is sent when the namespace list is loaded.
type NamespaceListLoadedMsg struct {
	Namespaces []string
}

// NamespaceFallbackMsg is sent when namespace loading fails.
type NamespaceFallbackMsg struct {
	Namespace string
	Error     error
}

// NamespaceSelectedMsg is sent when a namespace is selected.
type NamespaceSelectedMsg struct {
	Namespace string
}

// ─── Workitem list messages ───────────────────────────────────────────────

// WorkitemsLoadedMsg is sent when the Workitem list is loaded.
type WorkitemsLoadedMsg struct {
	Items []api.WorkitemSummary
}

// WorkitemLoadErrorMsg is sent when Workitem loading fails.
type WorkitemLoadErrorMsg struct {
	Error error
}

// WorkitemSelectedMsg is sent when a Workitem is selected.
type WorkitemSelectedMsg struct {
	Name string // workitem name to open
}

// WorkitemUpdateMsg is sent when a single Workitem changes via watch.
type WorkitemUpdateMsg struct {
	Event watch.EventType
	Item  api.WorkitemSummary
}

// WorkitemDeletedMsg is sent when a Workitem is deleted via watch.
type WorkitemDeletedMsg struct {
	Name string
}

// ChildCountsUpdatedMsg is sent when child counts are refreshed.
type ChildCountsUpdatedMsg struct {
	Counts map[string]int // workitem name -> child count
}

// WatchDisconnectedMsg is sent when the Kubernetes watch disconnects.
type WatchDisconnectedMsg struct {
	Error error
}

// WatchReconnectedMsg is sent when the Kubernetes watch reconnects.
type WatchReconnectedMsg struct{}

// NamespaceRefreshMsg is sent to reload the Workitem list for a namespace.
type NamespaceRefreshMsg struct {
	Namespace string
}

// ─── Workitem detail messages ─────────────────────────────────────────────

// ArtefactsLoadedMsg is sent when artefacts are loaded for a Workitem.
// Carries api.ArtefactInfo; update handler converts to types.ArtefactNode.
type ArtefactsLoadedMsg struct {
	WorkitemID string
	Artefacts  []api.ArtefactInfo
}

// ArtefactExpandedMsg is sent when an artefact is expanded with content.
// Carries api.FeedbackItem; update handler converts to types.FeedbackItem.
type ArtefactExpandedMsg struct {
	WorkitemID    string
	ArtefactID    string
	Content       string
	IsBinary      bool
	BinarySize    int
	FeedbackItems []api.FeedbackItem
}

// ArtefactFeedbackLoadedMsg is sent when feedback is loaded for a specific artefact.
type ArtefactFeedbackLoadedMsg struct {
	WorkitemID    string
	ArtefactID    string
	FeedbackItems []api.FeedbackItem
}

// ArtefactCollapsedMsg is sent when an artefact is collapsed.
type ArtefactCollapsedMsg struct {
	WorkitemID string
	ArtefactID string
}

// ArtefactLoadErrorMsg is sent when artefact loading fails.
type ArtefactLoadErrorMsg struct {
	WorkitemID string
	ArtefactID string // empty if ListArtefacts failed; set per-artefact if GetArtefact/GetFeedback failed
	Error      error
}

// TopologyLoadedMsg is sent when topology data is loaded.
type TopologyLoadedMsg struct {
	Nodes []types.TopologyNode
	Edges []types.TopologyEdge
}

// WorkitemDetailLoadedMsg is sent when a Workitem detail is loaded.
type WorkitemDetailLoadedMsg struct {
	Detail *api.WorkitemDetail
}

// ─── HITL messages ────────────────────────────────────────────────────────

// HitlProbeTriggerMsg is sent by a scheduled tea.Tick to trigger the next
// probe attempt.
type HitlProbeTriggerMsg struct{}

// HitlDecidedMsg is sent when a HITL decision is acknowledged.
type HitlDecidedMsg struct {
	WorkitemID string
	Choice     string
}

// HitlErrorMsg is sent when a HITL operation fails.
type HitlErrorMsg struct {
	WorkitemID string
	Err        error
	Retryable  bool
	DebugHint  string
}

// ─── Create wizard messages ───────────────────────────────────────────────

// CreateStartMsg signals the user initiated the create flow.
type CreateStartMsg struct{}

// CreateFieldUpdatedMsg is sent when a wizard field is updated.
type CreateFieldUpdatedMsg struct {
	Field string // "prompt", "entryNode", "artefactID", "governedArtefact"
	Value string
}

// CreateConfirmMsg signals the user confirmed the wizard.
type CreateConfirmMsg struct{}

// CreateSuccessMsg is sent when a Workitem is created successfully.
type CreateSuccessMsg struct {
	WorkitemName string
}

// CreateErrorMsg is sent when Workitem creation fails.
type CreateErrorMsg struct {
	Err         error
	Retry       bool
	HasCRD      bool // true if Workitem CRD was already created
	HasArtefact bool // true if artefact was already stored
}

// CreateCancelMsg signals the user cancelled the wizard.
type CreateCancelMsg struct{}

// WizardDataLoadedMsg is sent when initial wizard data is loaded from the API.
type WizardDataLoadedMsg struct {
	EntryNodes []string
	Artefacts  []string
	Blocked    string // "" if ok, "no_flow" or "multiple_flows"
	BlockedErr string
}

// ─── Delete messages ──────────────────────────────────────────────────────

// DeleteConfirmMsg is sent when the user confirms deletion.
type DeleteConfirmMsg struct {
	WorkitemName string
	Phase        string // current phase; blocked unless Completed or Failed
}

// DeleteResultMsg is sent when a delete operation completes.
type DeleteResultMsg struct {
	WorkitemName    string
	DeletedChildren []string
	Err             error
	FailedChildren  []string
}

// DeleteErrorMsg is sent when a delete operation fails.
type DeleteErrorMsg struct {
	WorkitemName string
	Err          error
}

// ─── Banner messages ───────────────────────────────────────────────────────

// BannerMsg sets a persistent or transient banner at the top of the screen.
type BannerMsg struct {
	Source  string // unique source key for dismissal tracking
	Message string
	Level   string // "info", "warn", "error"
}

// BannerDismissMsg clears a banner by source.
type BannerDismissMsg struct {
	Source string
}

// ─── System messages ──────────────────────────────────────────────────────

// QuitMsg signals the user wants to quit.
type QuitMsg struct{}

// RefreshMsg signals the user wants to refresh data.
type RefreshMsg struct{}

// ErrorMsg carries an error with a source identifier.
type ErrorMsg struct {
	Source  string // "archivist-forward", "archivist-list", etc.
	Message string
}
