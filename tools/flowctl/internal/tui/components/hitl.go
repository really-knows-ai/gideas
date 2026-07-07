package components

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gideas/flow/tools/flowctl/internal/tui/types"
)

// HitlPromptModel is the model for the HITL action prompt.
type HitlPromptModel struct {
	Visible    bool   // true when a queue item exists
	QueueItemID string
	Choices    []types.Choice // populated from /choices or defaults
	Loading    bool   // true while probing or deciding
	Error      string
	ErrorRetry bool   // true if retry is possible

	// Cancel confirmation
	ConfirmingCancel bool   // true when user pressed a cancel-type choice
	PendingChoice    string // the cancel choice value pending confirmation
}

// Default choices (hardcoded fallback).
var defaultChoices = []types.Choice{
	{Value: "approve", Label: "Approve", Type: "route"},
	{Value: "cancel", Label: "Cancel", Type: "cancel"},
}

// NewHitlPrompt creates a HitlPromptModel in hidden state.
func NewHitlPrompt() HitlPromptModel {
	return HitlPromptModel{}
}

// View renders the HITL prompt.
func (m HitlPromptModel) View() string {
	if !m.Visible {
		return ""
	}

	var b strings.Builder

	if m.Loading {
		b.WriteString("\nChecking HITL queue...")
		return b.String()
	}

	// Error states
	if m.Error != "" {
		switch {
		case strings.Contains(m.Error, "QUEUE_ITEM_ALREADY_CLAIMED"):
			b.WriteString("HITL error: Already claimed by another client")
			b.WriteString("\n[r]etry")
		case strings.Contains(m.Error, "QUEUE_ITEM_INVALID_STATE"):
			b.WriteString("HITL error: Item in unexpected state")
			b.WriteString("\n[r]etry")
		case strings.Contains(m.Error, "QUEUE_ITEM_NOT_FOUND"):
			b.WriteString("Queue item no longer exists — refreshing...")
		case strings.Contains(m.Error, "QUEUE_UNAVAILABLE"):
			b.WriteString("HITL error: queue unavailable")
			b.WriteString("\n[r]etry")
		case strings.Contains(m.Error, "choices"):
			b.WriteString("Unable to load choices")
			if m.ErrorRetry {
				b.WriteString("  [r]etry")
			}
		default:
			b.WriteString(fmt.Sprintf("HITL error: %s", m.Error))
			if m.ErrorRetry {
				b.WriteString("  [r]etry")
			}
		}
		return b.String()
	}

	// Cancel confirmation
	if m.ConfirmingCancel {
		b.WriteString("Cancel this workitem? This cannot be undone.  [y]es  [n]o")
		return b.String()
	}

	// Ready: show choices
	choices := m.Choices
	if len(choices) == 0 {
		choices = defaultChoices
	}

	b.WriteString("Workitem awaiting decision  ")
	for i, ch := range choices {
		if i > 0 {
			b.WriteString("  ")
		}
		if len(ch.Label) > 0 {
			key := string(ch.Label[0])
			b.WriteString(fmt.Sprintf("[%s]%s", strings.ToLower(key), ch.Label[1:]))
		}
	}

	return b.String()
}

// Update handles messages for the HITL prompt.
func (m HitlPromptModel) Update(msg tea.Msg) (HitlPromptModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if !m.Visible {
			return m, nil
		}

		switch msg.String() {
		case "y":
			if m.ConfirmingCancel {
				// Confirm cancel — handled by root update
				m.ConfirmingCancel = false
			}
		case "n":
			if m.ConfirmingCancel {
				m.ConfirmingCancel = false
				m.PendingChoice = ""
			}
		}
	}
	return m, nil
}
