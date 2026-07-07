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

// renderDetail renders the Workitem detail screen.
// Phase 03: placeholder — shows only the workitem name.
// Phase 04 replaces this with full status bar, topology, artefacts, and HITL.
func (m *Model) renderDetail() string {
	return lipgloss.JoinVertical(lipgloss.Top,
		m.workitemDetail.statusBar.View(),
		fmt.Sprintf("\n  Detail for %s\n\n  Full detail rendering coming in Phase 04.", m.workitemDetail.workitemName),
	)
}
