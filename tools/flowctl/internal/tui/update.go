package tui

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/tools/flowctl/internal/api"
	"github.com/foundry/flow/tools/flowctl/internal/tui/types"
)

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
		return flowv1.FeedbackState(s).String()
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
			// Clean up all resources before quitting
			m.closeAll()
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

		// Delete confirmation: y -> execute cascade delete
		if m.deleteConfirmWorkitem != "" && msg.String() == "y" {
			name := m.deleteConfirmWorkitem
			m.deleteConfirmWorkitem = ""
			m.workitemList.Loading = true
			m.err = nil
			return m, func() tea.Msg {
				m.logIfEnabled("INFO", "delete", "cascading delete for "+name)
				result := api.DeleteWorkitemCascade(m.ctx, m.k8s, m.namespace, name)
				if !result.Success {
					m.logIfEnabled("ERROR", "delete", fmt.Sprintf("cascade failed for %s: %s", name, result.Error))
					if len(result.Failed) > 0 {
						return DeleteResultMsg{
							WorkitemName:    name,
							Err:             errors.New(result.Error),
							FailedChildren:  result.Failed,
							DeletedChildren: result.Deleted,
						}
					}
					return DeleteResultMsg{
						WorkitemName: name,
						Err:          errors.New(result.Error),
					}
				}
				return DeleteResultMsg{
					WorkitemName:    name,
					DeletedChildren: result.Deleted,
				}
			}
		}

		// Delete confirmation: any non-y key -> cancel
		if m.deleteConfirmWorkitem != "" {
			m.deleteConfirmWorkitem = ""
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

		// CreateWizard: esc/backspace -> cancel and return to list
		if m.screen == ScreenCreateWizard && (msg.String() == "esc" || msg.String() == "backspace") {
			return m.routeMsg(CreateCancelMsg{})
		}

		// Navigation keys pass through to component-level key handlers
		return m.routeKeyMsg(msg)

	case ErrorMsg:
		// When archivist-forward errors occur on the detail screen, clear loading
		// and set error on the artefacts panel so it stops showing "Loading..."
		if m.screen == ScreenWorkitemDetail && (msg.Source == "archivist-forward" || msg.Source == "archivist-list") {
			m.workitemDetail.artefacts.Loading = false
			m.workitemDetail.artefacts.Error = msg.Message
			m.banner = msg.Message
			m.bannerSource = msg.Source
			m.logIfEnabled("ERROR", msg.Source, msg.Message)
			return m, nil
		}
		m.err = fmt.Errorf("%s: %s", msg.Source, msg.Message)
		m.logIfEnabled("ERROR", msg.Source, msg.Message)
		return m, nil

	case WorkitemUpdateMsg:
		// Handle WorkitemUpdateMsg at root level so NODE change detection
		// works regardless of current screen.
		cmd := m.handleWorkitemUpdate(msg)
		return m, cmd

	case WatchDisconnectedMsg:
		m.banner = "Reconnecting..."
		m.bannerSource = "watch"
		m.logIfEnabled("WARN", "watch", fmt.Sprintf("watch disconnected: %v", msg.Error))
		return m, nil

	case WatchReconnectedMsg:
		if m.bannerSource == "watch" {
			m.banner = ""
			m.bannerSource = ""
		}
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
		m.logIfEnabled("WARN", "namespace", "no K8s client; falling back to default")
		return NamespaceFallbackMsg{Namespace: "default", Error: fmt.Errorf("no K8s client")}
	}
	namespaces, err := m.k8s.ListNamespaces(m.ctx)
	if err != nil {
		// Distinguish RBAC denial (403) from transient API/server errors.
		// Only RBAC denial should auto-fallback to the current context namespace;
		// transient errors (network, timeout, 5xx) are fatal and should be surfaced.
		if api.IsForbiddenError(err) {
			fallback := api.GetCurrentContextNamespace()
			if m.cfg.NamespaceExplicit {
				fallback = m.cfg.Namespace
			} else if fallback == "" {
				fallback = "default"
			}
			m.logIfEnabled("WARN", "namespace", fmt.Sprintf("namespace list denied by RBAC; falling back to %s", fallback))
			return NamespaceFallbackMsg{Namespace: fallback, Error: err}
		}
		m.logIfEnabled("ERROR", "namespace", fmt.Sprintf("namespace list failed: %v", err))
		return NamespaceFallbackMsg{Namespace: "", Error: err, IsFatal: true}
	}
	return NamespaceListLoadedMsg{Namespaces: namespaces}
}

func (m *Model) loadWorkitems() tea.Cmd {
	return func() tea.Msg {
		if m.k8s == nil {
			m.logIfEnabled("ERROR", "workitem", "no K8s client available")
			return WorkitemLoadErrorMsg{Error: fmt.Errorf("no K8s client")}
		}
		items, err := m.k8s.ListWorkitems(m.ctx, m.namespace)
		if err != nil {
			m.logIfEnabled("ERROR", "workitem", fmt.Sprintf("list workitems: %v", err))
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
// Requires m.watchCtx to be set before calling — set by the WorkitemsLoadedMsg handler.
func (m *Model) startWorkitemWatch() tea.Cmd {
	if m.k8s == nil || m.watchCtx == nil {
		return nil
	}
	return func() tea.Msg {
		m.k8s.WatchWithBackoff(m.watchCtx, m.namespace, func(event watch.Event) {
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
// The workitem name snapshot is captured at creation time (Update handler main
// goroutine) to avoid a data race on m.workitemList.Items from the Cmd goroutine.
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
	// Capture a snapshot of workitem names at creation time (main goroutine)
	names := make([]string, len(m.workitemList.Items))
	for i, item := range m.workitemList.Items {
		names[i] = item.Name
	}
	return func() tea.Msg {
		<-m.childCountDebounce.C

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
	var cmds []tea.Cmd
	// Update the list
	switch msg.Event {
	case watch.Added:
		m.workitemList.Items = append(m.workitemList.Items, msg.Item)
	case watch.Modified:
		for i, item := range m.workitemList.Items {
			if item.Name == msg.Item.Name {
				// Detect NODE change for HITL probe restart on detail screen
				if m.screen == ScreenWorkitemDetail && m.workitemDetail.workitemName == item.Name {
					if m.workitemDetail.detail != nil {
						m.workitemDetail.detail.WorkitemSummary = msg.Item
					}
					if item.Node != msg.Item.Node && m.hitlState != nil {
						// NODE changed — close HITL and start new probe cycle
						m.hitlState.Close(m.pfm)
						m.hitlState.ResetForNewWorkitem()
						m.workitemDetail.hitl.Visible = false
						if m.k8s != nil && msg.Item.Node != "" && msg.Item.Node != "-" {
							m.workitemList.Items[i] = msg.Item
							cmds = append(cmds,
								m.hitlState.Probe(m.ctx, m.k8s.CoreClient, m.namespace, msg.Item.Node, msg.Item.Name, m.pfm),
							)
						}
					}
				}
				m.workitemList.Items[i] = msg.Item
				break
			}
		}
	}
	// Refresh artefacts when on the detail screen for the updated workitem
	if m.screen == ScreenWorkitemDetail && m.workitemDetail.workitemName == msg.Item.Name {
		cmds = append(cmds,
			m.debouncedChildCountRefresh(),
			m.loadWorkitemDetail(msg.Item.Name),
			m.RefreshArtefacts(),
		)
		return tea.Batch(cmds...)
	}
	return m.debouncedChildCountRefresh()
}

// logIfEnabled logs a message if the log writer is configured.
func (m *Model) logIfEnabled(level, component, message string) {
	if m.logWriter != nil {
		m.logWriter.Log(level, component, message)
	}
}
