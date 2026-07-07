package tui

import (
	"github.com/gideas/flow/tools/flowctl/internal/tui/types"
)

// ─── Namespace messages ───────────────────────────────────────────────────

// NamespacesLoadedMsg is sent when the namespace list is loaded.
// Stub (plural naming): replaced by Phase 03's NamespaceListLoadedMsg (singular).
type NamespacesLoadedMsg struct {
	Namespaces []string
	Current    string // current kube context namespace, "" if none
}

// NamespaceSelectedMsg is sent when a namespace is selected.
type NamespaceSelectedMsg struct {
	Namespace string
}

// NamespaceLoadErrorMsg is sent when namespace loading fails.
// Stub (LoadError): replaced by Phase 03's NamespaceFallbackMsg.
type NamespaceLoadErrorMsg struct {
	Err error
}

// ─── Workitem list messages ───────────────────────────────────────────────

// WorkitemsLoadedMsg is sent when the Workitem list is loaded.
type WorkitemsLoadedMsg struct {
	Items []types.WorkitemSummary
}

// WorkitemSelectedMsg is sent when a Workitem is selected.
type WorkitemSelectedMsg struct {
	Name string // workitem name to open
}

// WorkitemUpdateMsg is sent when a single Workitem changes via watch.
type WorkitemUpdateMsg struct {
	Item types.WorkitemSummary
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
type ArtefactsLoadedMsg struct {
	WorkitemID string
	Artefacts  []types.ArtefactNode
}

// ArtefactExpandedMsg is sent when an artefact is expanded with content.
type ArtefactExpandedMsg struct {
	WorkitemID    string
	ArtefactID    string
	Content       string
	IsBinary      bool
	FeedbackItems []types.FeedbackItem
}

// ArtefactCollapsedMsg is sent when an artefact is collapsed.
type ArtefactCollapsedMsg struct {
	ArtefactID string
}

// TopologyLoadedMsg is sent when topology data is loaded.
type TopologyLoadedMsg struct {
	Nodes []types.TopologyNode
	Edges []types.TopologyEdge
}

// ─── HITL messages ────────────────────────────────────────────────────────

// HitlProbeResultMsg is sent when a HITL queue probe succeeds or completes.
type HitlProbeResultMsg struct {
	WorkitemID string
	NodeName   string
	QueueItem  interface{} // stub; becomes *api.QueueItem in Phase 05
	Choices    []types.Choice
	HasCancel  bool
}

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
