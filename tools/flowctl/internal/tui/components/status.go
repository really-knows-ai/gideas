package components

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/gideas/flow/tools/flowctl/internal/tui/styles"
)

// StatusBarModel is the model for the status bar header.
type StatusBarModel struct {
	ScreenName   string // "Namespace Selection", "Workitem Browser", etc.
	Namespace    string
	WorkitemName string // empty if at list or namespace screen
	State        string // Workitem state summary
	Connected    bool   // green indicator dot
	Disconnected bool   // yellow indicator with "Disconnected..."
}

// NewStatusBar creates a StatusBarModel in initial state.
func NewStatusBar() StatusBarModel {
	return StatusBarModel{
		ScreenName: "Flowctl",
	}
}

// View renders the status bar.
func (m StatusBarModel) View() string {
	var b strings.Builder

	// Connection indicator
	indicator := "○ "
	if m.Connected {
		indicator = lipgloss.NewStyle().Foreground(lipgloss.Color("#00FF00")).Render("● ")
	} else if m.Disconnected {
		indicator = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFF00")).Render("◌ ")
	}

	// Build status line
	parts := []string{indicator + m.ScreenName}

	if m.Namespace != "" {
		parts = append(parts, fmt.Sprintf("Namespace: %s", m.Namespace))
	}
	if m.WorkitemName != "" {
		parts = append(parts, fmt.Sprintf("Workitem: %s", m.WorkitemName))
	}
	if m.State != "" {
		parts = append(parts, m.State)
	}

	b.WriteString(strings.Join(parts, "  │  "))
	b.WriteString("\n")
	// Disconnected banner
	if m.Disconnected {
		b.WriteString(lipgloss.NewStyle().Foreground(styles.WarningColor()).Render("Reconnecting..."))
		b.WriteString("\n")
	}

	return b.String()
}

// Update handles messages for the status bar.
func (m StatusBarModel) Update(msg tea.Msg) (StatusBarModel, tea.Cmd) {
	return m, nil
}
