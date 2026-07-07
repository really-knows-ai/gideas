package tui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

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

// renderDetail renders the Workitem detail screen layout:
// StatusBar (top), then Topology + Artefacts side by side, then HITL prompt.
func (m *Model) renderDetail() string {
	return lipgloss.JoinVertical(lipgloss.Top,
		m.workitemDetail.statusBar.View(),
		lipgloss.JoinHorizontal(lipgloss.Top,
			m.workitemDetail.topology.View(),
			m.workitemDetail.artefacts.View(),
		),
		m.workitemDetail.hitl.View(),
	)
}
