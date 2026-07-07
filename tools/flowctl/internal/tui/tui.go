// Package tui provides the bubbletea-based terminal UI for flowctl.
package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gideas/flow/tools/flowctl/internal/api"
	"github.com/gideas/flow/tools/flowctl/internal/config"
)

// New creates and returns a new bubbletea Program with the flowctl TUI.
//
// Deprecated: Use NewModel + tea.NewProgram directly for finer control.
func New() *tea.Program {
	m := initialModel()
	return tea.NewProgram(&m, tea.WithAltScreen())
}

// NewWithClient creates a TUI program with a K8s client and config.
func NewWithClient(k8s *api.K8sClient, cfg *config.Config, ctx context.Context) *tea.Program {
	m := NewModel(k8s, cfg, ctx)
	p := tea.NewProgram(&m, tea.WithAltScreen())
	m.Program = p
	return p
}
