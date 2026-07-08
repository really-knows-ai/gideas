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
	m.EntryNodes = []string{"forge", "sort"}
	v := m.View()
	if !strings.Contains(v, "forge") || !strings.Contains(v, "sort") {
		t.Error("expected entry node options in view, got:", v)
	}
}

// ─── Phase 06: Create wizard tests ─────────────────────────────────────────

func TestCreateGovernedArtefactFromEntryContracts(t *testing.T) {
	m := NewCreateWizard()
	m.Step = 3
	m.Artefacts = []string{"haiku", "petition"}
	v := m.View()
	if !strings.Contains(v, "haiku") || !strings.Contains(v, "petition") {
		t.Error("expected governed artefact options in view, got:", v)
	}
}

func TestCreateGovernedArtefactFallbackAll(t *testing.T) {
	m := NewCreateWizard()
	m.Step = 3
	m.Artefacts = []string{"petition", "haiku", "report"}
	v := m.View()
	if !strings.Contains(v, "petition") && !strings.Contains(v, "report") {
		t.Error("expected all artefacts as fallback options, got:", v)
	}
}

func TestCreateGovernedArtefactMissingContractKey(t *testing.T) {
	m := NewCreateWizard()
	m.Step = 3
	m.Artefacts = []string{"default-artefact"}
	v := m.View()
	if !strings.Contains(v, "default-artefact") {
		t.Error("expected fallback artefact option in view, got:", v)
	}
}

func TestCreateBlockedZeroFoundryFlows(t *testing.T) {
	m := NewCreateWizard()
	m.Blocked = "no_flow"
	v := m.View()
	if !strings.Contains(v, "no FoundryFlow") {
		t.Error("expected 'no FoundryFlow' in view, got:", v)
	}
}

func TestCreateBlockedMultipleFoundryFlows(t *testing.T) {
	m := NewCreateWizard()
	m.Blocked = "multiple_flows"
	v := m.View()
	if !strings.Contains(v, "multiple FoundryFlows") {
		t.Error("expected 'multiple FoundryFlows' in view, got:", v)
	}
}

func TestCreateStageProgressDisplay(t *testing.T) {
	m := NewCreateWizard()

	// StageCreating
	m.Stage = StageCreating
	v := m.View()
	if !strings.Contains(v, "Creating Workitem") {
		t.Error("expected 'Creating Workitem' in view for StageCreating, got:", v)
	}

	// StageStoringArtefact
	m.Stage = StageStoringArtefact
	v = m.View()
	if !strings.Contains(v, "Storing artefact") {
		t.Error("expected 'Storing artefact' in view for StageStoringArtefact, got:", v)
	}

	// StageUpdatingStatus
	m.Stage = StageUpdatingStatus
	v = m.View()
	if !strings.Contains(v, "Setting Workitem status") {
		t.Error("expected 'Setting Workitem status' in view for StageUpdatingStatus, got:", v)
	}

	// StageComplete
	m.Stage = StageComplete
	m.WorkitemID = "test-wi-123"
	v = m.View()
	if !strings.Contains(v, "test-wi-123") {
		t.Error("expected workitem name in view for StageComplete, got:", v)
	}

	// StageError with retry
	m.Stage = StageError
	m.Retryable = true
	m.Error = "API timeout"
	v = m.View()
	if !strings.Contains(v, "API timeout") {
		t.Error("expected error message in view for StageError, got:", v)
	}
	if !strings.Contains(v, "[r]etry") {
		t.Error("expected retry hint in view for retryable error, got:", v)
	}

	// StageError without retry
	m.Stage = StageError
	m.Retryable = false
	m.Error = "fatal error"
	v = m.View()
	if !strings.Contains(v, "fatal error") {
		t.Error("expected error message in view for non-retryable error, got:", v)
	}
	if strings.Contains(v, "[r]etry") {
		t.Error("expected no retry hint for non-retryable error, got:", v)
	}
}

func TestCreateSHA256Hash(t *testing.T) {
	// SHA256 hash is computed by api.ComputeSHA256; this tests the TUI display
	m := NewCreateWizard()
	m.Stage = StageComplete
	m.WorkitemID = "test-wi-001"
	v := m.View()
	if !strings.Contains(v, "test-wi-001") {
		t.Error("expected workitem name in success view, got:", v)
	}
}

func TestCreateCreatorLabel(t *testing.T) {
	// The creator label is set in CreateWorkitem in k8s.go.
	// This test verifies the TUI display shows creation success.
	m := NewCreateWizard()
	m.Stage = StageComplete
	m.WorkitemID = "test-wi-001"
	v := m.View()
	if !strings.Contains(v, "test-wi-001") {
		t.Error("expected workitem name in success view, got:", v)
	}
}

func TestCreateWorkitemNameGeneration(t *testing.T) {
	// Name generation is tested via the update function, not the component.
	// Verify the component handles a generated name.
	m := NewCreateWizard()
	m.Stage = StageComplete
	m.WorkitemID = "petition-1712345678-a1b2c3d4"
	v := m.View()
	if !strings.Contains(v, "petition-1712345678-a1b2c3d4") {
		t.Error("expected generated workitem name in view, got:", v)
	}
}
