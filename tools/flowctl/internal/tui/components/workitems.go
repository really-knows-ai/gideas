package components

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/gideas/flow/tools/flowctl/internal/api"
	"github.com/gideas/flow/tools/flowctl/internal/tui/styles"
)

// ageString converts time.Duration to a human-readable string like "5m", "2h", "3d".
func ageString(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// WorkitemListModel is the model for the Workitem list screen.
type WorkitemListModel struct {
	Items        []api.WorkitemSummary
	Cursor       int
	Loading      bool
	Watching     bool // true when watch is active
	Disconnected bool // true when watch disconnected, waiting for reconnection
	Namespace    string
	Error        string

	colWidths columnWidths
}

type columnWidths struct {
	name, state, node, children, age int
}

// NewWorkitemList creates a WorkitemListModel in loading state.
func NewWorkitemList() WorkitemListModel {
	return WorkitemListModel{
		Loading: true,
		colWidths: columnWidths{
			name:     20,
			state:    12,
			node:     15,
			children: 8,
			age:      10,
		},
	}
}

// View renders the Workitem list screen.
func (m WorkitemListModel) View() string {
	var b strings.Builder

	if m.Loading {
		b.WriteString("\n  Loading Workitems...")
		return b.String()
	}

	if m.Error != "" {
		b.WriteString(fmt.Sprintf("\n  Error: %s\n\n  Press r to retry", m.Error))
		return b.String()
	}

	if len(m.Items) == 0 {
		b.WriteString(fmt.Sprintf("\n  No Workitems in namespace %s", m.Namespace))
		return b.String()
	}

	// Header
	fmt.Fprintf(&b, "  %-*s  %-*s  %-*s  %-*s  %s\n",
		m.colWidths.name, "NAME",
		m.colWidths.state, "STATE",
		m.colWidths.node, "NODE",
		m.colWidths.children, "CHILDREN",
		"AGE")
	b.WriteString("  " + strings.Repeat("─", m.colWidths.name+m.colWidths.state+m.colWidths.node+m.colWidths.children+m.colWidths.age+10))
	b.WriteString("\n")

	for i, item := range m.Items {
		cursor := "  "
		if i == m.Cursor {
			cursor = "❯ "
		}

		stateStyle := styles.StyleStateColumn(item.State)
		nodeStr := item.Node
		nodeStyle := styles.StyleNodeColumn(nodeStr)

		name := item.Name
		state := stateStyle.Render(item.State)
		node := nodeStyle.Render(nodeStr)
		children := fmt.Sprintf("%d", item.ChildrenCount)
		age := ageString(item.Age)

		line := fmt.Sprintf("%s%-*s  %-*s  %-*s  %-*s  %s",
			cursor,
			m.colWidths.name, name,
			m.colWidths.state, state,
			m.colWidths.node, node,
			m.colWidths.children, children,
			age)

		if i == m.Cursor {
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#00FF00")).Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\n")
	}

	b.WriteString("\n↑/↓ navigate  •  enter select  •  n new  •  d delete  •  r refresh  •  esc back  •  q quit")
	return b.String()
}

// Update handles messages for the Workitem list.
// Only tea.KeyMsg is handled at the component level; semantic messages
// are handled by the root screen-level handler which directly sets fields.
func (m WorkitemListModel) Update(msg tea.Msg) (WorkitemListModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.Cursor > 0 {
				m.Cursor--
			}
		case "down", "j":
			if m.Cursor < len(m.Items)-1 {
				m.Cursor++
			}
		}
	}
	return m, nil
}
