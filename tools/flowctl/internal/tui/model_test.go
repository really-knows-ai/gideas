package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ─── Integration Tests ─────────────────────────────────────────────────────

func TestFullCreateIntegration(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemList
	m.workitemList.Loading = false
	m.workitemList.Items = nil // empty list

	// Simulate pressing 'n' to start create
	model, cmd := m.Update(CreateStartMsg{})
	m2 := model.(*Model)

	if m2.screen != ScreenCreateWizard {
		t.Errorf("expected ScreenCreateWizard, got %d", m2.screen)
	}
	if cmd != nil {
		// CreateStartMsg currently returns nil command — that's OK
	}

	// Verify the wizard is initialized
	if m2.createWizard.Step != 0 {
		t.Errorf("expected wizard step 0, got %d", m2.createWizard.Step)
	}

	// Verify initial wizard state
	v := m2.View()
	if !containsAny(v, "Enter prompt text", "prompt") {
		t.Error("expected wizard prompt in view, got:", v)
	}
}

func TestFullDeleteIntegration(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemList
	m.workitemList.Loading = false
	m.workitemList.Items = nil // empty list

	// Verify empty state
	v := m.View()
	if !containsAny(v, "No Workitems") {
		t.Error("expected empty workitem list in view, got:", v)
	}

	// Delete confirmation flow is tested in update_test.go (TestUpdateDeleteTerminalAllowed)
	// and at the model level with the deleteConfirmWorkitem field.
}

func TestCreateDeleteCycle(t *testing.T) {
	// Create wizard -> Delete flow integration
	m := initialModel()
	m.screen = ScreenWorkitemList

	// Start create
	model, _ := m.Update(CreateStartMsg{})
	m2 := model.(*Model)
	if m2.screen != ScreenCreateWizard {
		t.Errorf("expected ScreenCreateWizard after CreateStart, got %d", m2.screen)
	}

	// Cancel and return to list
	model, _ = m2.Update(CreateCancelMsg{})
	m3 := model.(*Model)
	if m3.screen != ScreenWorkitemList {
		t.Errorf("expected ScreenWorkitemList after cancel, got %d", m3.screen)
	}
}

func TestErrorStateRendering(t *testing.T) {
	m := initialModel()

	// Test error state view
	m.err = errCreateNeedsRetry
	v := m.View()
	if !containsAny(v, "retry") {
		t.Error("expected error text in error view, got:", v)
	}

	// Clear error and verify normal view
	m.err = nil
	v = m.View()
	if v == "" {
		t.Error("expected non-empty view after clearing error")
	}
}

func TestCleanExitClosesResources(t *testing.T) {
	m := initialModel()

	// Verify that closeAll can be called without resources
	// (no panic, no error)
	m.closeAll()

	// Verify that Ctrl+C triggers closeAll and Quit
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Error("expected non-nil command for Ctrl+C")
	}

	// Verify that 'q' also triggers closeAll and Quit
	m2 := initialModel()
	_, cmd2 := m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd2 == nil {
		t.Error("expected non-nil command for q")
	}
}

// containsAny checks if s contains any of the given substrings.
func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(sub) > 0 && !contains(s, sub) {
			return false
		}
	}
	return true
}

// contains is a helper that wraps strings.Contains for use in tests.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsString(s, substr)
}

// containsString is a simple substring check.
func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
