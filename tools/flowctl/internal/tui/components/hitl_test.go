package components

import (
	"strings"
	"testing"

	"github.com/gideas/flow/tools/flowctl/internal/tui/types"
)

func TestHitlHidden(t *testing.T) {
	m := NewHitlPrompt()
	m.Visible = false
	v := m.View()
	if v != "" {
		t.Error("expected empty view when hidden, got:", v)
	}
}

func TestHitlLoadingState(t *testing.T) {
	m := NewHitlPrompt()
	m.Visible = true
	m.Loading = true
	v := m.View()
	if !strings.Contains(v, "Checking HITL queue") {
		t.Error("expected 'Checking HITL queue' in view, got:", v)
	}
}

func TestHitlDefaultChoices(t *testing.T) {
	m := NewHitlPrompt()
	m.Visible = true
	m.Loading = false
	m.Choices = nil // should fall back to defaults
	v := m.View()
	if !strings.Contains(v, "[a]") || !strings.Contains(v, "[c]") {
		t.Error("expected default choice keybindings in view, got:", v)
	}
	if !strings.Contains(v, "pprove") || !strings.Contains(v, "ancel") {
		t.Error("expected default choice labels in view, got:", v)
	}
}

func TestHitlDynamicChoices(t *testing.T) {
	m := NewHitlPrompt()
	m.Visible = true
	m.Loading = false
	m.Choices = []types.Choice{
		{Value: "accept", Label: "Accept", Type: "route"},
		{Value: "reject", Label: "Reject", Type: "route"},
	}
	v := m.View()
	if !strings.Contains(v, "[a]") || !strings.Contains(v, "[r]") {
		t.Error("expected custom choice keybindings in view, got:", v)
	}
	if !strings.Contains(v, "ccept") || !strings.Contains(v, "eject") {
		t.Error("expected custom choice labels in view, got:", v)
	}
}

func TestHitlEmptyChoicesFallback(t *testing.T) {
	m := NewHitlPrompt()
	m.Visible = true
	m.Loading = false
	m.Choices = []types.Choice{} // non-nil but empty — simulates /choices returning {"choices":[]}
	m.ChoicesLoaded = true
	m.DefaultChoices = false
	v := m.View()
	// Should fall back to defaults to avoid a dead end.
	if !strings.Contains(v, "[a]") || !strings.Contains(v, "[c]") {
		t.Error("expected default choice keybindings for empty choices, got:", v)
	}
	if strings.Contains(v, "[R]elease") {
		t.Error("expected no [R]elease when using default fallback, got:", v)
	}
}

func TestHitlCancelConfirmation(t *testing.T) {
	m := NewHitlPrompt()
	m.Visible = true
	m.ConfirmingCancel = true
	v := m.View()
	if !strings.Contains(v, "Cancel this workitem") {
		t.Error("expected cancel confirmation text in view, got:", v)
	}
	if !strings.Contains(v, "[y]es") || !strings.Contains(v, "[n]o") {
		t.Error("expected yes/no options in view, got:", v)
	}
}

func TestHitlErrorState(t *testing.T) {
	m := NewHitlPrompt()
	m.Visible = true
	m.Error = "queue unavailable"
	m.ErrorRetry = true
	v := m.View()
	if !strings.Contains(v, "queue unavailable") {
		t.Error("expected error text in view, got:", v)
	}
	if !strings.Contains(v, "[r]etry") {
		t.Error("expected retry hint in view, got:", v)
	}
}

func TestHitlAlreadyClaimedError(t *testing.T) {
	m := NewHitlPrompt()
	m.Visible = true
	m.Error = "QUEUE_ITEM_ALREADY_CLAIMED"
	m.ErrorRetry = true
	v := m.View()
	if !strings.Contains(v, "Already claimed") {
		t.Error("expected 'already claimed' in view, got:", v)
	}
}

func TestHitlInvalidStateError(t *testing.T) {
	m := NewHitlPrompt()
	m.Visible = true
	m.Error = "QUEUE_ITEM_INVALID_STATE"
	m.ErrorRetry = true
	v := m.View()
	if !strings.Contains(v, "unexpected state") {
		t.Error("expected 'unexpected state' in view, got:", v)
	}
}

func TestHitlNotFoundError(t *testing.T) {
	m := NewHitlPrompt()
	m.Visible = true
	m.Error = "QUEUE_ITEM_NOT_FOUND"
	m.ErrorRetry = false
	v := m.View()
	if !strings.Contains(v, "no longer exists") {
		t.Error("expected 'no longer exists' in view, got:", v)
	}
}

func TestHitlChoicesUnavailableError(t *testing.T) {
	m := NewHitlPrompt()
	m.Visible = true
	m.Error = "unable to load choices"
	m.ErrorRetry = true
	v := m.View()
	if !strings.Contains(v, "Unable to load choices") {
		t.Error("expected 'unable to load choices' in view, got:", v)
	}
}

func TestHitlLoadingAfterProbe(t *testing.T) {
	m := NewHitlPrompt()
	m.Visible = true
	m.Loading = true
	v := m.View()
	if !strings.Contains(v, "Checking HITL queue") {
		t.Error("expected loading indicator in view, got:", v)
	}
}
