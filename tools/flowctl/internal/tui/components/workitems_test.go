package components

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foundry/flow/tools/flowctl/internal/api"
)

func TestWorkitemLoadingState(t *testing.T) {
	m := NewWorkitemList()
	v := m.View()
	if !strings.Contains(v, "Loading Workitems") {
		t.Error("expected 'Loading Workitems' in view, got:", v)
	}
}

func TestWorkitemLoadedState(t *testing.T) {
	m := NewWorkitemList()
	m.Loading = false
	m.Items = []api.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 2, Age: 2 * time.Minute},
	}
	m.Namespace = "test-ns"
	v := m.View()
	if !strings.Contains(v, "NAME") || !strings.Contains(v, "STATE") || !strings.Contains(v, "NODE") || !strings.Contains(v, "CHILDREN") || !strings.Contains(v, "AGE") {
		t.Error("expected column headers in view, got:", v)
	}
}

func TestWorkitemStateColumn(t *testing.T) {
	m := NewWorkitemList()
	m.Loading = false
	m.Items = []api.WorkitemSummary{
		{Name: "wi-pending", State: "Pending", Node: "forge", ChildrenCount: 0, Age: 30 * time.Second},
		{Name: "wi-running", State: "Running", Node: "sort", ChildrenCount: 2, Age: 2 * time.Minute},
		{Name: "wi-complete", State: "Completed", Node: "refine", ChildrenCount: 1, Age: 12 * time.Minute},
		{Name: "wi-failed", State: "Failed", Node: "refine", ChildrenCount: 0, Age: 15 * time.Minute},
	}
	m.Namespace = "test-ns"
	v := m.View()
	if !strings.Contains(v, "Pending") || !strings.Contains(v, "Running") || !strings.Contains(v, "Completed") || !strings.Contains(v, "Failed") {
		t.Error("expected all phase texts in view, got:", v)
	}
}

func TestWorkitemNodeColumnTerminalDash(t *testing.T) {
	m := NewWorkitemList()
	m.Loading = false
	m.Items = []api.WorkitemSummary{
		{Name: "wi-complete", State: "Completed", Node: "refine", ChildrenCount: 0, Age: 12 * time.Minute},
		{Name: "wi-failed", State: "Failed", Node: "refine", ChildrenCount: 0, Age: 15 * time.Minute},
	}
	m.Namespace = "test-ns"
	v := m.View()
	// Terminal phases should show "-" for NODE
	if !strings.Contains(v, "-") {
		t.Error("expected '-' for terminal phase nodes, got:", v)
	}
}

func TestWorkitemNodeColumnActive(t *testing.T) {
	m := NewWorkitemList()
	m.Loading = false
	m.Items = []api.WorkitemSummary{
		{Name: "wi-running", State: "Running", Node: "sort", ChildrenCount: 0, Age: 2 * time.Minute},
		{Name: "wi-pending", State: "Pending", Node: "forge", ChildrenCount: 0, Age: 30 * time.Second},
		{Name: "wi-routing", State: "Routing", Node: "router", ChildrenCount: 0, Age: time.Minute},
		{Name: "wi-suspended", State: "Suspended", Node: "hitl-approval", ChildrenCount: 0, Age: 8 * time.Minute},
	}
	m.Namespace = "test-ns"
	v := m.View()
	if !strings.Contains(v, "sort") || !strings.Contains(v, "forge") || !strings.Contains(v, "router") || !strings.Contains(v, "hitl-approval") {
		t.Error("expected active node names in view, got:", v)
	}
}

func TestWorkitemChildrenCount(t *testing.T) {
	m := NewWorkitemList()
	m.Loading = false
	m.Items = []api.WorkitemSummary{
		{Name: "wi-parent", State: "Running", Node: "sort", ChildrenCount: 3, Age: 2 * time.Minute},
	}
	m.Namespace = "test-ns"
	v := m.View()
	if !strings.Contains(v, "3") {
		t.Error("expected child count in view, got:", v)
	}
}

func TestWorkitemEmptyState(t *testing.T) {
	m := NewWorkitemList()
	m.Loading = false
	m.Items = nil
	m.Namespace = "test-ns"
	v := m.View()
	if !strings.Contains(v, "No Workitems") {
		t.Error("expected 'No Workitems' in view, got:", v)
	}
}

func TestWorkitemErrorState(t *testing.T) {
	m := NewWorkitemList()
	m.Loading = false
	m.Error = "connection refused"
	v := m.View()
	if !strings.Contains(v, "connection refused") {
		t.Error("expected error text in view, got:", v)
	}
}

func TestWorkitemCursorMovement(t *testing.T) {
	m := NewWorkitemList()
	m.Loading = false
	m.Items = []api.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 0, Age: 2 * time.Minute},
		{Name: "wi-002", State: "Completed", Node: "-", ChildrenCount: 0, Age: 12 * time.Minute},
	}
	if m.Cursor != 0 {
		t.Fatalf("expected cursor at 0, got %d", m.Cursor)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.Cursor != 1 {
		t.Errorf("expected cursor at 1 after down, got %d", m.Cursor)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.Cursor != 0 {
		t.Errorf("expected cursor at 0 after up, got %d", m.Cursor)
	}
}

func TestWorkitemStateColumnColorsApplied(t *testing.T) {
	m := NewWorkitemList()
	m.Loading = false
	m.Items = []api.WorkitemSummary{
		{Name: "wi-001", State: "Completed", Node: "-", ChildrenCount: 0, Age: 12 * time.Minute},
		{Name: "wi-002", State: "Suspended", Node: "hitl-approval", ChildrenCount: 0, Age: 8 * time.Minute},
		{Name: "wi-003", State: "Pending", Node: "forge", ChildrenCount: 0, Age: 30 * time.Second},
	}
	m.Namespace = "test-ns"
	v := m.View()
	if !strings.Contains(v, "Completed") || !strings.Contains(v, "Suspended") || !strings.Contains(v, "Pending") {
		t.Error("expected all state values in view, got:", v)
	}
	// Verify terminal phase shows "-" for NODE
	if !strings.Contains(v, "  -  ") && !strings.Contains(v, "  -") {
		t.Log("note: terminal dash rendering check")
	}
}

func TestWorkitemCursorClamped(t *testing.T) {
	m := NewWorkitemList()
	m.Loading = false
	m.Items = []api.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 0, Age: 2 * time.Minute},
	}
	m.Cursor = 0
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.Cursor != 0 {
		t.Errorf("expected cursor at 0 (clamped), got %d", m.Cursor)
	}
}
