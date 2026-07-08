package tui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// errorBannerStyle is used for error banners at the top of the screen.
var errorBannerStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("#FFFF00")).
	Foreground(lipgloss.Color("#000000"))

// mutedStyle is used for status messages and secondary text.
var mutedStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("#888888"))

// View renders the root TUI view based on the current screen.
func (m *Model) View() string {
	if m.err != nil {
		return lipgloss.JoinVertical(lipgloss.Top,
			fmt.Sprintf("Error: %s", m.err.Error()),
			"\nPress q to quit",
		)
	}

	switch m.screen {
	case ScreenNamespaceSelect:
		return m.namespaceSelector.View()
	case ScreenWorkitemList:
		return m.workitemList.View()
	case ScreenWorkitemDetail:
		return m.renderDetail()
	case ScreenCreateWizard:
		return m.createWizard.View()
	}

	return "Unknown screen"
}

// renderDetail renders the Workitem detail screen with error banner,
// status bar, topology, artefacts, and HITL prompt.
func (m *Model) renderDetail() string {
	detail := m.workitemDetail

	// Top area: error banner + status bar
	top := detail.statusBar.View()

	// Error banner (if set)
	if m.errorBanner != "" {
		top = errorBannerStyle.Render(m.errorBanner) + "\n" + top
	}

	// Workitem info line
	infoLine := ""
	if detail.detail != nil {
		infoLine = fmt.Sprintf("  State: %s  |  Node: %s",
			detail.detail.State,
			detail.detail.Node,
		)
		if detail.detail.FailureReason != "" {
			infoLine += fmt.Sprintf("  |  Failure: %s", detail.detail.FailureReason)
		}
		// Visit counters
		if len(detail.detail.ThrashCounters) > 0 {
			counters := ""
			for name, count := range detail.detail.ThrashCounters {
				if counters != "" {
					counters += " "
				}
				counters += fmt.Sprintf("%s:%d", name, count)
			}
			infoLine += fmt.Sprintf("\n  Visits: %s", counters)
		}
	} else if detail.loading {
		infoLine = "  Loading Workitem detail..."
	}

	// Topology section
	topologySection := detail.topology.View()

	// Artefacts section
	artefactsSection := detail.artefacts.View()

	// HITL prompt
	hitlSection := detail.hitl.View()

	// Status message at bottom (transient HITL messages, diagnostics, etc.)
	statusSection := ""
	if m.statusMessage != "" {
		statusSection = "\n" + mutedStyle.Render(m.statusMessage)
	}

	// Combine everything
	content := lipgloss.JoinVertical(lipgloss.Top,
		top,
		infoLine,
		"",
		topologySection,
		"",
		artefactsSection,
	)

	if hitlSection != "" {
		content = lipgloss.JoinVertical(lipgloss.Top,
			content,
			"",
			hitlSection,
		)
	}

	if statusSection != "" {
		content += statusSection
	}

	return content
}
