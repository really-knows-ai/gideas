// Package components provides the individual TUI component models, views, and
// update handlers. These are embedded in the root Model and are not standalone
// bubbletea programs.
package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	tea "github.com/charmbracelet/bubbletea"
)

// NamespaceSelectorModel is the model for the namespace selection screen.
type NamespaceSelectorModel struct {
	Namespaces       []string
	Cursor           int
	Loading          bool
	Error            string
	currentNamespace string // highlighted by default if in list
}

// NewNamespaceSelector creates a NamespaceSelectorModel in loading state.
func NewNamespaceSelector() NamespaceSelectorModel {
	return NamespaceSelectorModel{
		Loading: true,
	}
}

// View renders the namespace selector screen.
func (m NamespaceSelectorModel) View() string {
	var b strings.Builder

	b.WriteString(lipgloss.NewStyle().Bold(true).Render("Select Namespace"))
	b.WriteString("\n")
	b.WriteString(strings.Repeat("─", 50))
	b.WriteString("\n")

	if m.Loading {
		b.WriteString("\n  Loading namespaces...")
		return b.String()
	}

	if m.Error != "" {
		b.WriteString(fmt.Sprintf("\n  Error loading namespaces: %s", m.Error))
		b.WriteString("\n  Falling back to current context namespace...")
		return b.String()
	}

	if len(m.Namespaces) == 0 {
		b.WriteString("\n  No namespaces found — using current context namespace")
		return b.String()
	}

	for i, ns := range m.Namespaces {
		cursor := "  "
		if i == m.Cursor {
			cursor = "❯ "
		}
		annotation := ""
		if ns == m.currentNamespace {
			annotation = "  ← current"
		}
		line := fmt.Sprintf("%s%s%s", cursor, ns, annotation)
		if i == m.Cursor {
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#00FF00")).Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\n")
	}

	b.WriteString("\n↑/↓ navigate  •  enter select  •  q quit")
	return b.String()
}

// Update handles messages for the namespace selector.
func (m NamespaceSelectorModel) Update(msg tea.Msg) (NamespaceSelectorModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.Cursor > 0 {
				m.Cursor--
			}
		case "down", "j":
			if m.Cursor < len(m.Namespaces)-1 {
				m.Cursor++
			}
		}
	}
	return m, nil
}

// SetNamespaces populates the namespace list and sets loading to false.
func (m NamespaceSelectorModel) SetNamespaces(namespaces []string, current string) NamespaceSelectorModel {
	m.Namespaces = namespaces
	m.currentNamespace = current
	m.Loading = false
	m.Error = ""

	// Set cursor to current namespace if present
	if current != "" {
		for i, ns := range namespaces {
			if ns == current {
				m.Cursor = i
				break
			}
		}
	}
	return m
}
