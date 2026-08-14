package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foundry/flow/tools/flowctl/internal/api"
	"github.com/foundry/flow/tools/flowctl/internal/tui/components"
)

// updateNamespaceSelect handles semantic messages for the namespace select screen.
func (m *Model) updateNamespaceSelect(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case NamespaceListLoadedMsg:
		if len(msg.Namespaces) == 0 {
			// Zero namespaces — fall back to current context namespace like a denied listing
			fallback := api.GetCurrentContextNamespace()
			if fallback == "" {
				fallback = "default"
			}
			sysNS := fallback
			if m.k8s != nil {
				var err error
				sysNS, err = m.k8s.ResolveSystemNamespace(m.ctx, m.cfg.SystemNamespace, fallback)
				if err != nil {
					sysNS = fallback
				}
			}
			m.systemNS = sysNS
			m.namespace = fallback
			m.workitemList.Namespace = fallback
			m.screen = ScreenWorkitemList
			m.logIfEnabled("WARN", "namespace", "zero namespaces found; falling back to "+fallback)
			return m, tea.Batch(m.loadWorkitems(), m.connectArchivist())
		}
		m.namespaceSelector = m.namespaceSelector.SetNamespaces(msg.Namespaces, api.GetCurrentContextNamespace())
		m.err = nil

	case NamespaceFallbackMsg:
		if msg.IsFatal {
			// Transient API/server error — surface to user instead of silent fallback
			m.logIfEnabled("ERROR", "namespace", fmt.Sprintf("namespace list failed: %v", msg.Error))
			m.err = msg.Error
			return m, nil
		}
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
		// Log the fallback reason
		if msg.Error != nil {
			m.logIfEnabled("WARN", "namespace", fmt.Sprintf("namespace list denied; falling back to %s", msg.Namespace))
		}
		// Auto-select fallback namespace and skip the selector entirely
		m.namespace = msg.Namespace
		m.workitemList.Namespace = msg.Namespace
		m.screen = ScreenWorkitemList
		return m, tea.Batch(m.loadWorkitems(), m.connectArchivist())

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
		return m, tea.Batch(m.loadWorkitems(), m.connectArchivist())
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
		// Create a cancellable watch context for explicit lifecycle management
		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		m.watchCtx, m.watchCancel = context.WithCancel(ctx)
		// Start the watch in background
		return m, m.startWorkitemWatch()

	case WorkitemLoadErrorMsg:
		m.workitemList.Loading = false
		m.workitemList.Error = msg.Error.Error()
		m.logIfEnabled("ERROR", "workitem", fmt.Sprintf("load error: %v", msg.Error))

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
		// Transition to create wizard and load data
		m.createWizard = components.NewCreateWizard()
		m.createWizard.Loading = true
		m.screen = ScreenCreateWizard
		m.createHasCRD = false
		m.createHasArtefact = false
		return m, m.loadWizardData()

	case DeleteConfirmMsg:
		// Delete blocked if not Completed/Failed
		for _, item := range m.workitemList.Items {
			if item.Name == msg.WorkitemName {
				if item.State != "Completed" && item.State != "Failed" {
					m.err = fmt.Errorf("cannot delete Workitem in %s state (only Completed/Failed allowed)", item.State)
					m.logIfEnabled("WARN", "delete", m.err.Error())
				} else {
					// Valid terminal phase — show confirmation prompt
					m.deleteConfirmWorkitem = msg.WorkitemName
				}
				break
			}
		}

	case DeleteResultMsg:
		if msg.Err != nil {
			failed := make([]string, 0, len(msg.FailedChildren))
			for _, child := range msg.FailedChildren {
				failed = append(failed, fmt.Sprintf("%s: %s", child.WorkitemID, child.Error))
			}
			m.err = fmt.Errorf("delete failed: %s (failed children: %s)", msg.Err, strings.Join(failed, "; "))
			m.logIfEnabled("ERROR", "delete", m.err.Error())
		} else {
			// Success — show brief message
			m.banner = fmt.Sprintf("Deleted Workitem %s (%d children)", msg.WorkitemName, len(msg.DeletedChildren))
			m.bannerSource = "delete"
			// Auto-dismiss after 3 seconds
			return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return BannerDismissMsg{Source: "delete"}
			})
		}
	}
	return m, nil
}

func (m *Model) updateNamespaceSelectKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.namespaceSelector, _ = m.namespaceSelector.Update(msg)
	return m, nil
}

func (m *Model) updateWorkitemListKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.workitemList, _ = m.workitemList.Update(msg)
	return m, nil
}
