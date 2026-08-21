package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

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
		m.workitemDetail.hitl.Error = ""
		m.workitemDetail.hitl.ErrorRetry = false
		m.workitemDetail.hitl.Loading = true
		if m.ctx == nil {
			m.ctx = context.Background()
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
