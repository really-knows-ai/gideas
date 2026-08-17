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
