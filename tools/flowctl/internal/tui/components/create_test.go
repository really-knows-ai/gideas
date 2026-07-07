package components

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCreateInitialStepPrompt(t *testing.T) {
	m := NewCreateWizard()
	v := m.View()
	if !strings.Contains(v, "Enter prompt text") {
		t.Error("expected 'Enter prompt text' in view, got:", v)
	}
}

func TestCreateStepNavigation(t *testing.T) {
	m := NewCreateWizard()
	// Step 0 → Step 1
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.Step != 1 {
		t.Errorf("expected step 1 after tab, got %d", m.Step)
	}
	// Step 1 → Step 0 (shift+tab)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.Step != 0 {
		t.Errorf("expected step 0 after shift+tab, got %d", m.Step)
	}
}

func TestCreateFieldUpdatedMsg(t *testing.T) {
	m := NewCreateWizard()
	// Not handled directly by component Update; root handles this.
	// But we can verify field defaults.
	if m.Fields.ArtefactID != "petition" {
		t.Errorf("expected default artefactID 'petition', got %q", m.Fields.ArtefactID)
	}
}

func TestCreateConfirmationStep(t *testing.T) {
	m := NewCreateWizard()
	m.Step = 4
	m.Fields = CreateFields{
		PromptText:       "Write a haiku",
		EntryNode:        "forge",
		ArtefactID:       "petition",
		GovernedArtefact: "petition",
	}
	v := m.View()
	if !strings.Contains(v, "Write a haiku") || !strings.Contains(v, "forge") {
		t.Error("expected field values in confirmation view, got:", v)
	}
}

func TestCreateBlockedNoFlow(t *testing.T) {
	m := NewCreateWizard()
	m.Blocked = "no_flow"
	v := m.View()
	if !strings.Contains(v, "no FoundryFlow") {
		t.Error("expected 'no FoundryFlow' message in view, got:", v)
	}
}

func TestCreateBlockedMultipleFlows(t *testing.T) {
	m := NewCreateWizard()
	m.Blocked = "multiple_flows"
	v := m.View()
	if !strings.Contains(v, "multiple FoundryFlows") {
		t.Error("expected 'multiple FoundryFlows' message in view, got:", v)
	}
}

func TestCreateSuccessStep(t *testing.T) {
	m := NewCreateWizard()
	m.Step = 5
	m.SuccessName = "wi-abc123"
	v := m.View()
	if !strings.Contains(v, "wi-abc123") {
		t.Error("expected workitem name in success view, got:", v)
	}
}

func TestCreateErrorStep(t *testing.T) {
	m := NewCreateWizard()
	m.Step = 5
	m.Error = "API timeout"
	v := m.View()
	if !strings.Contains(v, "API timeout") {
		t.Error("expected error text in view, got:", v)
	}
	if !strings.Contains(v, "[r]etry") {
		t.Error("expected retry hint in view, got:", v)
	}
}

func TestCreateEntryNodeSelection(t *testing.T) {
	m := NewCreateWizard()
	m.Step = 1
	v := m.View()
	if !strings.Contains(v, "forge") || !strings.Contains(v, "human-entry") {
		t.Error("expected entry node options in view, got:", v)
	}
}
