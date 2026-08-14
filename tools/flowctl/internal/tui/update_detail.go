package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foundry/flow/tools/flowctl/internal/api"
	"github.com/foundry/flow/tools/flowctl/internal/tui/components"
	"github.com/foundry/flow/tools/flowctl/internal/tui/types"
)

// updateWorkitemDetail handles semantic messages for the detail screen.
// It dispatches to per-message-family handlers so each family stays small:
// detail/topology state, artefact tree, HITL state machine, banners/errors,
// and screen transitions (refresh/create wizard).
func (m *Model) updateWorkitemDetail(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case WorkitemDetailLoadedMsg, TopologyLoadedMsg:
		return m.handleDetailTopology(msg)
	case ArtefactsLoadedMsg, ArtefactLoadErrorMsg, ArtefactExpandedMsg, ArtefactFeedbackLoadedMsg, ArtefactCollapsedMsg:
		return m.handleArtefacts(msg)
	case components.HitlProbeResultMsg, components.HitlProbeRetryMsg, components.HitlProbeExhaustedMsg,
		components.HitlChoicesBlockedMsg, HitlProbeTriggerMsg, HitlReleasedMsg, HitlDecidedMsg, HitlErrorMsg:
		return m.handleHitl(msg)
	case ErrorMsg, ClearErrorBannerMsg, BannerMsg, BannerDismissMsg, WorkitemDeletedMsg:
		return m.handleBanner(msg)
	case RefreshMsg, CreateStartMsg:
		return m.handleRefreshCreate(msg)
	}
	return m, nil
}

// handleDetailTopology applies detail and topology state with current/visited
// node colouring.
func (m *Model) handleDetailTopology(msg tea.Msg) (tea.Model, tea.Cmd) {
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
	}
	return m, nil
}

// handleArtefacts applies artefact tree state (load, expand, collapse, errors).
func (m *Model) handleArtefacts(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
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
			}
		}
		sort.Slice(nodes, func(i, j int) bool {
			return nodes[i].ArtefactID < nodes[j].ArtefactID
		})
		m.workitemDetail.artefacts.Artefacts = nodes
		m.workitemDetail.artefacts.Loading = false
		m.errorBanner = ""

		// Fetch content for expanded artefacts and feedback for every artefact
		var cmds []tea.Cmd
		if m.archivist != nil {
			for _, art := range m.workitemDetail.artefacts.Artefacts {
				if art.Expanded {
					// Expanded artefacts: fetch content + feedback together
					cmds = append(cmds, m.fetchArtefactContent(msg.WorkitemID, art.ArtefactID))
				} else {
					// Collapsed artefacts: fetch feedback only
					cmds = append(cmds, m.fetchArtefactFeedback(msg.WorkitemID, art.ArtefactID))
				}
			}
		}
		if len(cmds) > 0 {
			return m, tea.Batch(cmds...)
		}

	case ArtefactLoadErrorMsg:
		m.workitemDetail.artefacts.Loading = false
		m.logIfEnabled("ERROR", "archivist", fmt.Sprintf("artefact load error for %s/%s: %v", msg.WorkitemID, msg.ArtefactID, msg.Error))
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

	case ArtefactFeedbackLoadedMsg:
		for i, art := range m.workitemDetail.artefacts.Artefacts {
			if art.ArtefactID == msg.ArtefactID {
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
	}
	return m, nil
}

// handleHitl drives the HITL probe/decision state machine on the detail screen.
func (m *Model) handleHitl(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case components.HitlProbeResultMsg:
		// HitlState was already populated by the Probe cmd goroutine.
		// Populate the rendering component from the message.
		m.workitemDetail.hitl.Visible = true
		m.workitemDetail.hitl.QueueItemID = msg.WorkitemID
		m.workitemDetail.hitl.Choices = toChoices(msg.Choices)
		m.workitemDetail.hitl.ChoicesLoaded = msg.ChoicesLoaded
		m.workitemDetail.hitl.DefaultChoices = msg.DefaultChoices
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
		m.logIfEnabled("ERROR", "hitl", fmt.Sprintf("probe exhausted for %s/%s: %s", msg.NodeName, msg.WorkitemID, msg.Diagnostic))

	case components.HitlChoicesBlockedMsg:
		if m.hitlState != nil {
			m.hitlState.Close(m.pfm)
		}
		m.workitemDetail.hitl.Visible = true
		m.workitemDetail.hitl.Error = fmt.Sprintf("choices: %s", msg.Err)
		m.workitemDetail.hitl.ErrorRetry = true
		m.statusMessage = fmt.Sprintf("Unable to load choices: %s", msg.Err)
		m.logIfEnabled("ERROR", "hitl", fmt.Sprintf("choices blocked: %s", msg.Err))

	case HitlProbeTriggerMsg:
		if m.hitlState != nil && !m.hitlState.Exhausted() && m.workitemDetail.workitemName != "" {
			return m, m.hitlState.Probe(m.ctx, m.k8s.CoreClient, m.namespace,
				m.hitlState.GetNodeName(), m.hitlState.GetWorkitemID(), m.pfm)
		}

	case HitlReleasedMsg:
		m.workitemDetail.hitl.Visible = false
		m.workitemDetail.hitl.Error = ""
		m.statusMessage = "Claim released"
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
		m.logIfEnabled("ERROR", "hitl", fmt.Sprintf("HITL error for %s: %v (retryable=%v)", msg.WorkitemID, msg.Err, msg.Retryable))
		if !msg.Retryable {
			m.workitemDetail.hitl.Visible = false
		} else {
			m.workitemDetail.hitl.Error = msg.Err.Error()
			m.workitemDetail.hitl.ErrorRetry = msg.Retryable
		}
		// Handle specific error codes per the spec error table.
		// When Retryable is false, skip retry-triggering errors to avoid cycling.
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
			case api.IsQueueUnavailable(msg.Err) && msg.Retryable:
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
				} else if m.hitlState != nil {
					m.statusMessage = "Queue unavailable — retrying release..."
					return m, func() tea.Msg {
						err := m.hitlState.RetryQueueUnavailable(m.ctx, func(ctx context.Context) error {
							return m.hitlState.ReleaseClaim(ctx)
						})
						if err != nil {
							return HitlErrorMsg{
								WorkitemID: m.selectedWorkitemName(),
								Err:        err,
								Retryable:  false,
							}
						}
						return HitlReleasedMsg{
							WorkitemID: m.selectedWorkitemName(),
						}
					}
				} else {
					m.statusMessage = "Queue unavailable"
					m.workitemDetail.hitl.Visible = true
					m.workitemDetail.hitl.Error = "Queue unavailable"
					m.workitemDetail.hitl.ErrorRetry = true
				}
			case api.IsBadRequest(msg.Err):
				m.workitemDetail.hitl.Visible = true
				// Strip the "BAD_REQUEST: " prefix to show only the server message
				errStr := strings.TrimPrefix(msg.Err.Error(), "BAD_REQUEST: ")
				m.workitemDetail.hitl.Error = fmt.Sprintf("Invalid request: %s", errStr)
				m.workitemDetail.hitl.ErrorRetry = true
			default:
				m.workitemDetail.hitl.Visible = true
				m.workitemDetail.hitl.Error = msg.Err.Error()
				m.workitemDetail.hitl.ErrorRetry = true
			}
		}
		if msg.DebugHint != "" {
			m.debugHint = msg.DebugHint
		}
	}
	return m, nil
}

// handleBanner applies banner/error display state on the detail screen,
// including returning to the list when the viewed workitem is deleted.
func (m *Model) handleBanner(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case ErrorMsg:
		// Show as error banner at top of detail screen
		m.errorBanner = fmt.Sprintf("⚠ %s: %s", msg.Source, msg.Message)
		m.logIfEnabled("ERROR", msg.Source, msg.Message)
		// Clear error banner after 10 seconds
		return m, tea.Tick(10*time.Second, func(t time.Time) tea.Msg {
			return ClearErrorBannerMsg{}
		})

	case WorkitemDeletedMsg:
		if m.screen == ScreenWorkitemDetail && m.workitemDetail.workitemName == msg.Name {
			// The viewed workitem was deleted — clear detail and return to list
			m.workitemDetail = WorkitemDetailModel{
				statusBar: components.NewStatusBar(),
				topology:  components.NewFlowTopology(),
				artefacts: components.NewArtefactTree(),
				hitl:      components.NewHitlPrompt(),
			}
			m.screen = ScreenWorkitemList
			m.banner = fmt.Sprintf("Workitem %s was deleted", msg.Name)
			m.bannerSource = "watch"
			m.logIfEnabled("INFO", "watch", fmt.Sprintf("workitem %s deleted while viewing detail", msg.Name))
		}

	case ClearErrorBannerMsg:
		m.errorBanner = ""

	case BannerMsg:
		m.banner = msg.Message
		m.bannerSource = msg.Source
		m.bannerTimeout = true
		m.logIfEnabled(strings.ToUpper(msg.Level), "banner", msg.Message)
		return m, tea.Tick(10*time.Second, func(t time.Time) tea.Msg {
			return BannerDismissMsg{Source: msg.Source}
		})

	case BannerDismissMsg:
		if m.bannerSource == msg.Source {
			m.banner = ""
			m.bannerSource = ""
			m.bannerTimeout = false
		}
	}
	return m, nil
}

// handleRefreshCreate handles refresh and create-wizard screen transitions.
func (m *Model) handleRefreshCreate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
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
			m.loadWorkitemDetail(workitemID),
			m.loadTopology(),
		}
		if m.archivist != nil {
			cmds = append(cmds, m.RefreshArtefacts())
		} else {
			cmds = append(cmds, m.loadArtefacts(workitemID))
		}
		return m, tea.Batch(cmds...)

	case CreateStartMsg:
		// Open create wizard from detail screen and load data
		m.createWizard = components.NewCreateWizard()
		m.createWizard.Loading = true
		m.screen = ScreenCreateWizard
		m.createHasCRD = false
		m.createHasArtefact = false
		return m, m.loadWizardData()
	}
	return m, nil
}

// ClearErrorBannerMsg clears the error banner.
type ClearErrorBannerMsg struct{}

// updateWorkitemDetailKeys handles key presses for the detail screen.
func (m *Model) updateWorkitemDetailKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Snapshot the pre-toggle expanded state for the cursor artefact before
	// the component update toggles it. The root handler below needs to know
	// whether the row *was* collapsed (to fetch content) or expanded (to just
	// collapse via the component), but by the time it checks, the component
	// has already flipped the flag.
	var wasExpanded bool
	cursor := m.workitemDetail.artefacts.Cursor
	if cursor >= 0 && cursor < len(m.workitemDetail.artefacts.Artefacts) {
		wasExpanded = m.workitemDetail.artefacts.Artefacts[cursor].Expanded
	}

	m.workitemDetail.artefacts, _ = m.workitemDetail.artefacts.Update(msg)

	if m.workitemDetail.hitl.Visible && m.workitemDetail.hitl.ErrorRetry && msg.String() == "r" {
		wasChoicesError := strings.Contains(m.workitemDetail.hitl.Error, "choices")
		m.workitemDetail.hitl.Error = ""
		m.workitemDetail.hitl.ErrorRetry = false
		m.workitemDetail.hitl.Loading = true
		if m.ctx == nil {
			m.ctx = context.Background()
		}
		if m.hitlState != nil && m.k8s != nil && wasChoicesError {
			return m, m.hitlState.Probe(m.ctx, m.k8s.CoreClient, m.namespace,
				m.hitlState.GetNodeName(), m.hitlState.GetWorkitemID(), m.pfm)
		}
		if m.hitlState != nil && m.hitlState.GetPendingChoice() != "" {
			choice := m.hitlState.GetPendingChoice()
			return m, func() tea.Msg {
				if err := m.hitlState.ClaimAndDecide(m.ctx, choice); err != nil {
					return HitlErrorMsg{WorkitemID: m.hitlState.GetWorkitemID(), Err: err, Retryable: true}
				}
				return HitlDecidedMsg{WorkitemID: m.hitlState.GetWorkitemID(), Choice: choice}
			}
		}
		if m.hitlState != nil {
			return m, func() tea.Msg {
				if err := m.hitlState.ReleaseClaim(m.ctx); err != nil {
					return HitlErrorMsg{WorkitemID: m.hitlState.GetWorkitemID(), Err: err, Retryable: true}
				}
				return HitlReleasedMsg{WorkitemID: m.hitlState.GetWorkitemID()}
			}
		}
	}

	// HITL cancel confirmation keys must be handled before the normal action-key
	// guard, because that path is intentionally disabled while confirming.
	if m.hitlState != nil && m.hitlState.Active() && m.workitemDetail.hitl.Visible && m.workitemDetail.hitl.ConfirmingCancel {
		key := msg.String()
		if key == "n" {
			m.workitemDetail.hitl.ConfirmingCancel = false
			m.workitemDetail.hitl.PendingChoice = ""
			return m, nil
		}
		if key == "y" {
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
						Retryable:  true,
					}
				}
				return HitlDecidedMsg{
					WorkitemID: m.hitlState.GetWorkitemID(),
					Choice:     pendingChoice,
				}
			}
		}
		return m, nil
	}

	// HITL action keys — only when HITL is active and not confirming cancel
	if m.hitlState != nil && m.hitlState.Active() && m.workitemDetail.hitl.Visible && !m.workitemDetail.hitl.ConfirmingCancel {
		key := msg.String()

		// Handle 'r' to clear error — only when there's an error to clear;
		// otherwise fall through to dynamic choice matching.
		if key == "r" && m.workitemDetail.hitl.Error != "" {
			m.workitemDetail.hitl.Error = ""
			return m, nil
		}

		// Handle 'R' (shift+r) for release — abandon the claim.
		// Only active when using dynamic choices (not default approve/cancel).
		if key == "R" && !m.workitemDetail.hitl.DefaultChoices {
			m.workitemDetail.hitl.Loading = true
			if m.ctx == nil {
				m.ctx = context.Background()
			}
			return m, func() tea.Msg {
				err := m.hitlState.ReleaseClaim(m.ctx)
				if err != nil {
					return HitlErrorMsg{
						WorkitemID: m.hitlState.GetWorkitemID(),
						Err:        err,
						Retryable:  true,
					}
				}
				return HitlReleasedMsg{
					WorkitemID: m.hitlState.GetWorkitemID(),
				}
			}
		}

		// Match choice keys: first letter of each choice label
		choices := m.workitemDetail.hitl.Choices
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
							Retryable:  true,
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
		if wasExpanded {
			// Was expanded — collapse handled by component; no network call needed
			return m, nil
		}
		// Was collapsed — fetch content and feedback
		workitemID := m.workitemDetail.workitemName
		if workitemID != "" && m.archivist != nil {
			return m, m.fetchArtefactContent(workitemID, art.ArtefactID)
		}
	}

	return m, nil
}

// ─── Detail loading helpers ────────────────────────────────────────────────

// loadWorkitemDetail fetches the Workitem detail and builds topology/artefacts.
func (m *Model) loadWorkitemDetail(name string) tea.Cmd {
	return func() tea.Msg {
		if m.k8s == nil {
			return nil
		}
		detail, err := m.k8s.GetWorkitem(m.ctx, m.namespace, name)
		if err != nil {
			m.logIfEnabled("ERROR", "workitem-detail", fmt.Sprintf("fetch detail for %s: %v", name, err))
			return ErrorMsg{Source: "workitem-detail", Message: fmt.Sprintf("fetch detail: %v", err)}
		}
		return WorkitemDetailLoadedMsg{Detail: detail}
	}
}

// connectArchivist establishes a port-forward to the Archivist pod and creates
// a gRPC client. It is called after namespace resolution so the connection is
// ready when entering Workitem detail. If it fails, m.archivist stays nil and
// loadArtefacts will report the error at entry time.
func (m *Model) connectArchivist() tea.Cmd {
	return func() tea.Msg {
		if m.pfm == nil || m.systemNS == "" {
			return nil // silently skip — archivist won't be available
		}
		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}

		archivistPod, found, err := m.pfm.FindReadyPod(ctx, m.systemNS, "app.kubernetes.io/name=flow-archivist")
		if err != nil {
			m.logIfEnabled("WARN", "archivist", fmt.Sprintf("find archivist pod: %v", err))
			return nil
		}
		if !found {
			m.logIfEnabled("WARN", "archivist", "no Ready archivist pod found in namespace "+m.systemNS)
			return nil
		}
		_, localPort, err := m.pfm.ForwardPod(ctx, m.systemNS, archivistPod, 50054)
		if err != nil {
			m.logIfEnabled("WARN", "archivist", fmt.Sprintf("forward pod: %v", err))
			return nil
		}
		archivist, err := api.NewArchivistClient(fmt.Sprintf("localhost:%d", localPort))
		if err != nil {
			m.logIfEnabled("WARN", "archivist", fmt.Sprintf("connect: %v", err))
			return nil
		}
		// Close previous client if any
		if m.archivist != nil {
			m.archivist.Close()
		}
		m.archivist = archivist
		return nil
	}
}

// loadArtefacts loads artefacts for a Workitem. The Archivist connection should
// already be established by connectArchivist (called after namespace resolution).
// Falls back to connecting synchronously if m.archivist is nil.
func (m *Model) loadArtefacts(workitemID string) tea.Cmd {
	return func() tea.Msg {
		if m.pfm == nil {
			m.logIfEnabled("ERROR", "archivist", "no port-forward manager")
			return ErrorMsg{Source: "archivist-forward", Message: "no port-forward manager"}
		}

		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}

		// Connect lazily if eager connect after namespace resolution did not run or failed
		if m.archivist == nil {
			archivistPod, found, err := m.pfm.FindReadyPod(ctx, m.systemNS, "app.kubernetes.io/name=flow-archivist")
			if err != nil {
				m.logIfEnabled("ERROR", "archivist", fmt.Sprintf("find archivist pod: %v", err))
				return ErrorMsg{Source: "archivist-forward", Message: fmt.Sprintf("find archivist pod: %v", err)}
			}
			if !found {
				m.logIfEnabled("ERROR", "archivist", fmt.Sprintf("no Ready archivist pod found in namespace %s", m.systemNS))
				return ErrorMsg{Source: "archivist-forward", Message: "no Ready archivist pod found in namespace " + m.systemNS}
			}
			_, localPort, err := m.pfm.ForwardPod(ctx, m.systemNS, archivistPod, 50054)
			if err != nil {
				m.logIfEnabled("ERROR", "archivist", fmt.Sprintf("port-forward: %v", err))
				return ErrorMsg{Source: "archivist-forward", Message: fmt.Sprintf("port-forward: %v", err)}
			}
			archivist, err := api.NewArchivistClient(fmt.Sprintf("localhost:%d", localPort))
			if err != nil {
				m.logIfEnabled("ERROR", "archivist", fmt.Sprintf("connect: %v", err))
				return ErrorMsg{Source: "archivist-forward", Message: fmt.Sprintf("connect: %v", err)}
			}
			if m.archivist != nil {
				m.archivist.Close()
			}
			m.archivist = archivist
		}

		// List artefacts via the (now established) client
		artefacts, err := m.archivist.ListArtefacts(ctx, m.namespace, workitemID)
		if err != nil {
			m.logIfEnabled("ERROR", "archivist", fmt.Sprintf("list artefacts for %s: %v", workitemID, err))
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
			m.logIfEnabled("ERROR", "topology", fmt.Sprintf("ListFoundryNodes: %v", err))
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
			// Non-fatal: show content with error feedback item
			feedback = []api.FeedbackItem{
				{
					State:      api.FeedbackStateNew,
					SourceNode: "archivist",
					Message:    fmt.Sprintf("Feedback unavailable: %v", err),
					CreatedAt:  time.Now(),
				},
			}
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

// fetchArtefactFeedback fetches feedback for a specific artefact.
func (m *Model) fetchArtefactFeedback(workitemID, artefactID string) tea.Cmd {
	return func() tea.Msg {
		if m.archivist == nil {
			return ArtefactFeedbackLoadedMsg{
				WorkitemID:    workitemID,
				ArtefactID:    artefactID,
				FeedbackItems: nil,
			}
		}

		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}

		feedback, err := m.archivist.GetFeedback(ctx, m.namespace, workitemID, artefactID)
		if err != nil {
			return ArtefactLoadErrorMsg{
				WorkitemID: workitemID,
				ArtefactID: artefactID,
				Error:      err,
			}
		}

		return ArtefactFeedbackLoadedMsg{
			WorkitemID:    workitemID,
			ArtefactID:    artefactID,
			FeedbackItems: feedback,
		}
	}
}
