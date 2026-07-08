package components

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/gideas/flow/tools/flowctl/internal/tui/styles"
)

// ConnectionStatus represents the health of a service connection.
type ConnectionStatus int

const (
	StatusOff  ConnectionStatus = iota // HITL: no probe active
	StatusOK                            // K8s/Archivist: connected
	StatusWarn                          // K8s: disconnected; Archivist: forward failed
	StatusErr                           // K8s/Archivist: unreachable
)

// StatusBarModel is the model for the status bar header.
type StatusBarModel struct {
	ScreenName   string // "Namespace Selection", "Workitem Browser", etc.
	Namespace    string
	WorkitemName string // empty if at list or namespace screen
	State        string // Workitem state summary
	Warning      string // non-empty shows yellow warning banner
	Connected    bool   // green indicator dot
	Disconnected bool   // yellow indicator with "Disconnected..."

	// Phase 06: connection status indicators
	K8sStatus      ConnectionStatus // OK, WARN, ERR
	ArchivistStatus ConnectionStatus // OK, WARN, ERR
	HitlStatus     ConnectionStatus // OK, OFF
}

// NewStatusBar creates a StatusBarModel in initial state.
func NewStatusBar() StatusBarModel {
	return StatusBarModel{
		ScreenName:       "Flowctl",
		K8sStatus:        StatusOff,
		ArchivistStatus:  StatusOff,
		HitlStatus:       StatusOff,
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

	// Phase 06: connection status indicators on the right
	var statusIndicators []string
	statusIndicators = append(statusIndicators, connStatus("K8s", m.K8sStatus))
	statusIndicators = append(statusIndicators, connStatus("ARC", m.ArchivistStatus))
	statusIndicators = append(statusIndicators, connStatus("HITL", m.HitlStatus))
	statusLine := strings.Join(parts, "  │  ")
	statusRight := strings.Join(statusIndicators, " ")
	// Pad the status line to push indicators to the right
	if len(statusLine)+len(statusRight)+4 < 80 {
		padding := 80 - len(statusLine) - len(statusRight) - 4
		statusLine = statusLine + strings.Repeat(" ", padding) + "  │  " + statusRight
	} else {
		statusLine = statusLine + "  │  " + statusRight
	}

	b.WriteString(statusLine)
	b.WriteString("\n")

	// Warning banner
	if m.Warning != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(styles.WarningColor()).Render(fmt.Sprintf("⚠  %s", m.Warning)))
		b.WriteString("\n")
	}

	// Disconnected banner
	if m.Disconnected {
		b.WriteString(lipgloss.NewStyle().Foreground(styles.WarningColor()).Render("⚠ Disconnected — waiting for reconnection..."))
		b.WriteString("\n")
	}

	return b.String()
}

// connStatus renders a connection status indicator string.
func connStatus(label string, s ConnectionStatus) string {
	switch s {
	case StatusOff:
		return fmt.Sprintf("%s:OFF", label)
	case StatusOK:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#00FF00")).Render(fmt.Sprintf("%s:OK", label))
	case StatusWarn:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFF00")).Render(fmt.Sprintf("%s:WARN", label))
	case StatusErr:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#FF0000")).Render(fmt.Sprintf("%s:ERR", label))
	default:
		return fmt.Sprintf("%s:?", label)
	}
}

// Update handles messages for the status bar.
func (m StatusBarModel) Update(msg tea.Msg) (StatusBarModel, tea.Cmd) {
	return m, nil
}
