package tui

import (
	"fmt"
	"sort"

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

	// Delete confirmation overlay (shown on any screen that supports deletion)
	if m.deleteConfirmWorkitem != "" {
		return lipgloss.JoinVertical(lipgloss.Top,
			fmt.Sprintf("Delete Workitem %s and all its children? [y/N]", m.deleteConfirmWorkitem),
			"",
			"Any key other than y cancels the deletion.",
		)
	}

	// Banner at top of screen (before screen content)
	var content string
	switch m.screen {
	case ScreenNamespaceSelect:
		content = m.namespaceSelector.View()
	case ScreenWorkitemList:
		content = m.workitemList.View()
	case ScreenWorkitemDetail:
		content = m.renderDetail()
	case ScreenCreateWizard:
		content = m.createWizard.View()
	default:
		content = "Unknown screen"
	}

	// Prepend banner if active
	if m.banner != "" {
		content = errorBannerStyle.Render(m.banner) + "\n" + content
	}

	return content
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
		infoLine = fmt.Sprintf("  State: %s  |  Node: %s  |  Age: %s",
			detail.detail.State,
			detail.detail.Node,
			detail.detail.Age,
		)
		if detail.detail.FailureReason != "" {
			infoLine += fmt.Sprintf("  |  Failure: %s", detail.detail.FailureReason)
		}
		// Visit counters
		if len(detail.detail.ThrashCounters) > 0 {
			// Sort keys for deterministic output.
			keys := make([]string, 0, len(detail.detail.ThrashCounters))
			for k := range detail.detail.ThrashCounters {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			counters := ""
			for _, name := range keys {
				if counters != "" {
					counters += " "
				}
				counters += fmt.Sprintf("%s:%d", name, detail.detail.ThrashCounters[name])
			}
			infoLine += fmt.Sprintf("\n  Visits: %s", counters)
		}

		// Children
		if len(detail.detail.ChildWorkitems) > 0 {
			children := ""
			for _, child := range detail.detail.ChildWorkitems {
				if children != "" {
					children += ", "
				}
				children += fmt.Sprintf("%s (%s)", child.Name, child.State)
			}
			infoLine += fmt.Sprintf("\n  Children: %s", children)
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
