package components

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/gideas/flow/tools/flowctl/internal/tui/styles"
	"github.com/gideas/flow/tools/flowctl/internal/tui/types"
)

// ArtefactTreeModel is the model for the artefact/feedback tree.
type ArtefactTreeModel struct {
	Artefacts []types.ArtefactNode
	Loading   bool
	Error     string
	Cursor    int // which artefact row is selected (exported for parent access)
}

// NewArtefactTree creates an ArtefactTreeModel in loading state.
func NewArtefactTree() ArtefactTreeModel {
	return ArtefactTreeModel{
		Loading: true,
	}
}

// View renders the artefact tree.
func (m ArtefactTreeModel) View() string {
	var b strings.Builder

	b.WriteString(lipgloss.NewStyle().Bold(true).Render("Artefacts"))
	b.WriteString("\n")

	if m.Loading {
		b.WriteString("\n  Loading artefacts...")
		return b.String()
	}

	if m.Error != "" {
		b.WriteString(fmt.Sprintf("\n  Artefacts unavailable: %s", m.Error))
		return b.String()
	}

	// Sort artefacts lexicographically by ArtefactID
	sorted := make([]types.ArtefactNode, len(m.Artefacts))
	copy(sorted, m.Artefacts)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].ArtefactID < sorted[j].ArtefactID
	})

	for _, art := range sorted {
		prefix := "  ▸ "
		if art.Expanded {
			prefix = "  ▾ "
		}

		line := fmt.Sprintf("%s%s (governed: %s)", prefix, art.ArtefactID, art.GovernedBy)
		b.WriteString(line)
		b.WriteString("\n")

		if art.Expanded {
			// Content
			if art.IsBinary {
				b.WriteString(m.renderBinaryContent(art.Content, art.BinarySize))
			} else {
				b.WriteString(fmt.Sprintf("      content: %s", art.Content))
				b.WriteString("\n")
			}

			// Feedback section
			if len(art.Feedback) > 0 {
				b.WriteString("      Feedback\n")

				// Sort feedback: by timestamp ascending, stable secondary by ID
				sortedFeedback := sortFeedback(art.Feedback)

				for _, fb := range sortedFeedback {
					stateDisplay := strings.TrimPrefix(fb.State, "FEEDBACK_STATE_")

					var stateStyle lipgloss.Style
					if fb.State == "RESOLVED" || strings.TrimPrefix(fb.State, "FEEDBACK_STATE_") == "RESOLVED" {
						// Resolved feedback is dimmed
						stateStyle = styles.StyleResolved()
					} else if fb.State == "DEADLOCKED" || strings.TrimPrefix(fb.State, "FEEDBACK_STATE_") == "DEADLOCKED" {
						stateStyle = styles.StyleDeadlocked()
					} else {
						stateStyle = styles.StyleState(fb.State)
					}

					renderedState := stateStyle.Render(stateDisplay)
					msg := fb.Message
					if len(msg) > 120 {
						msg = msg[:120]
					}

					b.WriteString(fmt.Sprintf("        %s  %-14s  %s\n", renderedState, fb.SourceNode, msg))
				}
			}
		}
	}

	return b.String()
}

func (m ArtefactTreeModel) renderBinaryContent(content string, size int) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("      content: (binary, %d bytes)\n", size))

	data := []byte(content)
	// Limit to first 256 bytes
	if len(data) > 256 {
		data = data[:256]
	}

	for offset := 0; offset < len(data); offset += 16 {
		fmt.Fprintf(&b, "      %08x  ", offset)
		for j := 0; j < 16; j++ {
			if offset+j < len(data) {
				fmt.Fprintf(&b, "%02x ", data[offset+j])
			} else {
				b.WriteString("   ")
			}
			if j == 7 {
				b.WriteString(" ")
			}
		}
		b.WriteString(" |")
		for j := 0; j < 16 && offset+j < len(data); j++ {
			ch := data[offset+j]
			if ch >= 32 && ch <= 126 {
				b.WriteByte(ch)
			} else {
				b.WriteByte('.')
			}
		}
		b.WriteString("|\n")
	}

	return b.String()
}

// sortFeedback sorts feedback by timestamp ascending, with stable secondary sort by ID.
func sortFeedback(items []types.FeedbackItem) []types.FeedbackItem {
	sorted := make([]types.FeedbackItem, len(items))
	copy(sorted, items)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Timestamp != sorted[j].Timestamp {
			return sorted[i].Timestamp < sorted[j].Timestamp
		}
		return sorted[i].ID < sorted[j].ID
	})
	return sorted
}

// Update handles messages for the artefact tree.
// Only tea.KeyMsg is handled at the component level; semantic messages
// (ArtefactsLoadedMsg, ArtefactExpandedMsg, etc.) are handled by the
// root screen-level handler which directly sets fields.
func (m ArtefactTreeModel) Update(msg tea.Msg) (ArtefactTreeModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.Cursor > 0 {
				m.Cursor--
			}
		case "down", "j":
			if m.Cursor < len(m.Artefacts)-1 {
				m.Cursor++
			}
		case "enter":
			if m.Cursor >= 0 && m.Cursor < len(m.Artefacts) {
				m.Artefacts[m.Cursor].Expanded = !m.Artefacts[m.Cursor].Expanded
			}
		}
	}
	return m, nil
}
