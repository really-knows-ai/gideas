// Package tui provides the bubbletea-based terminal UI for flowctl.
package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// New creates and returns a new bubbletea Program with the flowctl TUI.
//
// Deprecated: Use NewModel + tea.NewProgram directly for finer control.
func New() *tea.Program {
	m := initialModel()
	return tea.NewProgram(&m, tea.WithAltScreen())
}
