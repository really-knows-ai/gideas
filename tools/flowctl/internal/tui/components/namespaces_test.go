package components

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestNamespaceLoadingState(t *testing.T) {
	m := NewNamespaceSelector()
	v := m.View()
	if !strings.Contains(v, "Loading namespaces") {
		t.Error("expected 'Loading namespaces' in view, got:", v)
	}
}

func TestNamespaceLoadedState(t *testing.T) {
	m := NewNamespaceSelector()
	m = m.SetNamespaces([]string{"ns-a", "ns-b", "ns-c"}, "")
	v := m.View()
	if !strings.Contains(v, "ns-a") || !strings.Contains(v, "ns-b") || !strings.Contains(v, "ns-c") {
		t.Error("expected namespace names in view, got:", v)
	}
}

func TestNamespaceEmptyState(t *testing.T) {
	m := NewNamespaceSelector()
	m = m.SetNamespaces(nil, "")
	v := m.View()
	if !strings.Contains(v, "No namespaces found") {
		t.Error("expected 'No namespaces found' in view, got:", v)
	}
}

func TestNamespaceErrorState(t *testing.T) {
	m := NewNamespaceSelector()
	errMsg := "permission denied"
	m.Loading = false
	m.Error = errMsg
	v := m.View()
	if !strings.Contains(v, errMsg) {
		t.Error("expected error text in view, got:", v)
	}
}

func TestNamespaceCurrentHighlight(t *testing.T) {
	m := NewNamespaceSelector()
	m = m.SetNamespaces([]string{"ns-a", "ns-b", "ns-c"}, "ns-b")
	v := m.View()
	if !strings.Contains(v, "← current") {
		t.Error("expected '← current' annotation in view, got:", v)
	}
}

func TestNamespaceCursorMovement(t *testing.T) {
	m := NewNamespaceSelector()
	m = m.SetNamespaces([]string{"ns-a", "ns-b", "ns-c"}, "")
	if m.Cursor != 0 {
		t.Fatalf("expected cursor at 0, got %d", m.Cursor)
	}

	// Move down
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.Cursor != 1 {
		t.Errorf("expected cursor at 1 after down, got %d", m.Cursor)
	}

	// Move up
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.Cursor != 0 {
		t.Errorf("expected cursor at 0 after up, got %d", m.Cursor)
	}

	// j key
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if m.Cursor != 1 {
		t.Errorf("expected cursor at 1 after j, got %d", m.Cursor)
	}

	// k key
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if m.Cursor != 0 {
		t.Errorf("expected cursor at 0 after k, got %d", m.Cursor)
	}
}

func TestNamespaceCursorClamped(t *testing.T) {
	m := NewNamespaceSelector()
	m = m.SetNamespaces([]string{"ns-a", "ns-b"}, "")

	// Up from 0 should stay at 0
	m.Cursor = 0
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.Cursor != 0 {
		t.Errorf("expected cursor at 0 (clamped), got %d", m.Cursor)
	}

	// Down from len-1 should stay at len-1
	m.Cursor = 1
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.Cursor != 1 {
		t.Errorf("expected cursor at 1 (clamped), got %d", m.Cursor)
	}
}
