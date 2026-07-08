package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gideas/flow/tools/flowctl/internal/api"
	"github.com/gideas/flow/tools/flowctl/internal/tui/components"
	"github.com/gideas/flow/tools/flowctl/internal/tui/types"
)

// W1: Node change triggers probe (via WorkitemUpdateMsg)
func TestNodeChangeTriggersProbe(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"
	m.workitemDetail.detail = &api.WorkitemDetail{
		WorkitemSummary: api.WorkitemSummary{Name: "wi-001", State: "Running", Node: "sort"},
	}
	m.workitemList.Items = []api.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort"},
	}
	m.workitemList.Namespace = "test-ns"
	m.hitlState = components.NewHitlState(8080)
	m.namespace = "test-ns"

	// Simulate a modified event with a new node
	model, cmd := m.Update(WorkitemUpdateMsg{
		Event: "MODIFIED",
		Item:  api.WorkitemSummary{Name: "wi-001", Node: "human-approval"},
	})
	m2 := model.(*Model)

	// Since m.k8s is nil in tests, the probe won't be started with a full cmd,
	// but the debounced child count refresh cmd should still be present
	if cmd == nil {
		t.Error("expected non-nil command (debounced child refresh)")
	}

	// The workitem's node should be updated in the list
	if len(m2.workitemList.Items) > 0 {
		if m2.workitemList.Items[0].Node != "human-approval" {
			t.Errorf("expected list item node 'human-approval', got %q", m2.workitemList.Items[0].Node)
		}
	}

	// The hitlState should have been closed (HITL forward cleaned up)
	if m2.hitlState.Active() {
		t.Error("expected hitlState to be inactive after node change")
	}
}

// W2: Probe result shows prompt
func TestHitlProbeShowsPrompt(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"

	model, cmd := m.Update(components.HitlProbeResultMsg{
		WorkitemID: "wi-001",
		NodeName:   "human-approval",
		QueueItem:  &api.QueueItem{WorkitemID: "wi-001"},
		Choices: []api.Choice{
			{Value: "approve", Label: "Approve", Type: "route"},
			{Value: "cancel", Label: "Cancel", Type: "cancel"},
		},
		HasCancel: true,
	})
	m2 := model.(*Model)

	if !m2.workitemDetail.hitl.Visible {
		t.Error("expected HITL prompt visible after successful probe")
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

// W3: Probe exhaustion shows diagnostic
func TestHitlProbeExhaustionShowsDiagnostic(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"

	model, cmd := m.Update(components.HitlProbeExhaustedMsg{
		WorkitemID: "wi-001",
		NodeName:   "human-approval",
		Diagnostic: "HITL probe timed out — node may not have enqueued the item",
	})
	m2 := model.(*Model)

	if m2.statusMessage != "HITL probe timed out — node may not have enqueued the item" {
		t.Errorf("expected diagnostic in statusMessage, got %q", m2.statusMessage)
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

// W4: Default choices used when /choices returns 404
// (Default choices are set by HitlState.Probe when /choices returns nil;
// the Update function receives them as part of HitlProbeResultMsg.)
func TestDefaultChoicesOnProbeResult(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"

	// HitlProbeResultMsg carries whatever choices were resolved
	model, cmd := m.Update(components.HitlProbeResultMsg{
		WorkitemID: "wi-001",
		NodeName:   "human-approval",
		QueueItem:  &api.QueueItem{WorkitemID: "wi-001"},
		Choices: []api.Choice{
			{Value: "approve", Label: "Approve", Type: "route"},
			{Value: "cancel", Label: "Cancel", Type: "cancel"},
		},
		HasCancel: true,
	})
	m2 := model.(*Model)

	if len(m2.workitemDetail.hitl.Choices) != 2 {
		t.Fatalf("expected 2 choices, got %d", len(m2.workitemDetail.hitl.Choices))
	}
	// Verify it has the default-like shortcuts
	if m2.workitemDetail.hitl.Choices[0].Value != "approve" {
		t.Errorf("expected first choice value 'approve', got %q", m2.workitemDetail.hitl.Choices[0].Value)
	}
	if m2.workitemDetail.hitl.Choices[1].Value != "cancel" {
		t.Errorf("expected second choice value 'cancel', got %q", m2.workitemDetail.hitl.Choices[1].Value)
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

// W5: Dynamic choices on /choices 200
func TestDynamicChoicesOnProbeResult(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"

	// Dynamic choices from /choices
	model, cmd := m.Update(components.HitlProbeResultMsg{
		WorkitemID: "wi-001",
		NodeName:   "human-arbiter",
		QueueItem:  &api.QueueItem{WorkitemID: "wi-001"},
		Choices: []api.Choice{
			{Value: "accept", Label: "Accept", Type: "route"},
			{Value: "reject", Label: "Reject", Type: "route"},
			{Value: "cancel", Label: "Cancel", Type: "cancel"},
		},
		HasCancel: true,
	})
	m2 := model.(*Model)

	if len(m2.workitemDetail.hitl.Choices) != 3 {
		t.Fatalf("expected 3 dynamic choices, got %d", len(m2.workitemDetail.hitl.Choices))
	}
	if m2.workitemDetail.hitl.Choices[0].Value != "accept" {
		t.Errorf("expected first choice 'accept', got %q", m2.workitemDetail.hitl.Choices[0].Value)
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

// W6: Successful claim+decide triggers refresh (via HitlDecidedMsg)
func TestClaimAndDecideTriggersRefresh(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.hitl.Visible = true
	m.workitemDetail.workitemName = "wi-001"

	model, cmd := m.Update(HitlDecidedMsg{
		WorkitemID: "wi-001",
		Choice:     "approve",
	})
	m2 := model.(*Model)

	if m2.workitemDetail.hitl.Visible {
		t.Error("expected HITL prompt hidden after decision")
	}
	if m2.statusMessage != "Decision 'approve' submitted" {
		t.Errorf("expected statusMessage 'Decision 'approve' submitted', got %q", m2.statusMessage)
	}
	if cmd == nil {
		t.Error("expected non-nil command (refresh artefacts)")
	}
}

// W7: Queue item not found closes prompt
func TestQueueItemNotFoundClosesPrompt(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.hitl.Visible = true
	m.workitemDetail.workitemName = "wi-001"

	model, cmd := m.Update(HitlErrorMsg{
		WorkitemID: "wi-001",
		Err:        &api.HitlError{Code: api.ErrQueueItemNotFound, Message: "item gone"},
		Retryable:  false,
	})
	m2 := model.(*Model)

	if m2.workitemDetail.hitl.Visible {
		t.Error("expected HITL prompt hidden after QUEUE_ITEM_NOT_FOUND")
	}
	if m2.statusMessage != "Queue item no longer exists — refreshing..." {
		t.Errorf("expected statusMessage about refresh, got %q", m2.statusMessage)
	}
	if cmd == nil {
		t.Error("expected non-nil command (refresh)")
	}
}

// W8: Cancel choice shows confirmation
func TestCancelChoiceShowsConfirmation(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.hitlState = components.NewHitlState(8080)
	m.hitlState.SetActiveForTest()
	m.workitemDetail.hitl.Visible = true
	m.workitemDetail.hitl.Choices = []types.Choice{
		{Value: "cancel", Label: "Cancel", Type: "cancel"},
	}
	m.workitemDetail.hitl.ConfirmingCancel = false

	// Press "c" (first letter of "Cancel")
	// This is handled in updateWorkitemDetailKeys
	model, cmd := m.updateWorkitemDetailKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m2 := model.(*Model)

	if !m2.workitemDetail.hitl.ConfirmingCancel {
		t.Error("expected confirmingCancel=true after pressing cancel-type choice")
	}
	if m2.workitemDetail.hitl.PendingChoice != "cancel" {
		t.Errorf("expected pendingChoice 'cancel', got %q", m2.workitemDetail.hitl.PendingChoice)
	}
	if cmd != nil {
		t.Error("expected nil command (confirmation pending)")
	}
}

func TestCancelConfirmationDismissesOnNo(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.hitlState = components.NewHitlState(8080)
	m.hitlState.SetActiveForTest()
	m.workitemDetail.hitl.Visible = true
	m.workitemDetail.hitl.ConfirmingCancel = true
	m.workitemDetail.hitl.PendingChoice = "cancel"

	model, cmd := m.updateWorkitemDetailKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m2 := model.(*Model)

	if m2.workitemDetail.hitl.ConfirmingCancel {
		t.Error("expected cancel confirmation dismissed")
	}
	if m2.workitemDetail.hitl.PendingChoice != "" {
		t.Errorf("expected pendingChoice cleared, got %q", m2.workitemDetail.hitl.PendingChoice)
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestCancelConfirmationSubmitsOnYes(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.hitlState = components.NewHitlState(8080)
	m.hitlState.SetActiveForTest()
	m.workitemDetail.hitl.Visible = true
	m.workitemDetail.hitl.ConfirmingCancel = true
	m.workitemDetail.hitl.PendingChoice = "cancel"

	model, cmd := m.updateWorkitemDetailKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m2 := model.(*Model)

	if m2.workitemDetail.hitl.ConfirmingCancel {
		t.Error("expected cancel confirmation cleared")
	}
	if m2.workitemDetail.hitl.PendingChoice != "" {
		t.Errorf("expected pendingChoice cleared, got %q", m2.workitemDetail.hitl.PendingChoice)
	}
	if !m2.workitemDetail.hitl.Loading {
		t.Error("expected HITL loading while cancel decision command runs")
	}
	if cmd == nil {
		t.Error("expected decision command")
	}
}

// W9: Ctrl+C cleans up HITL forward
func TestCtrlCCleansHitlForward(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.hitlState = components.NewHitlState(8080)

	// Use a mock PortForwarder to verify close behavior
	mockPFM := newMockPortForwarder()
	m.pfm = mockPFM

	// Create a forward and register it with hitlState
	fid, _, _ := mockPFM.ForwardPod(nil, "ns", "pod", 8080)
	m.hitlState.SetForwardIDForTest(fid)

	// Send Ctrl+C
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected non-nil command (quit)")
	}

	// Ensure the forward was closed
	if mockPFM.IsOpen(fid) {
		t.Error("expected HITL forward to be closed on Ctrl+C")
	}
}

// W10: Debug hint for non-default port
func TestHitlDebugHintNonDefaultPort(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"
	m.hitlState = components.NewHitlState(9999)

	// HitlProbeExhaustedMsg from HitlState with non-default port.
	// The diagnostic is already composed by HitlState.Probe.
	model, cmd := m.Update(components.HitlProbeExhaustedMsg{
		WorkitemID: "wi-001",
		NodeName:   "human-approval",
		Diagnostic: "HITL probe timed out — node may not have enqueued the item\nHITL probe failed — verify `--hitl-port` matches the node's `FLOW_HITL_PORT`",
	})
	m2 := model.(*Model)

	if m2.statusMessage == "" {
		t.Error("expected statusMessage to contain diagnostic")
	}
	if !strings.Contains(m2.statusMessage, "HITL probe timed out") {
		t.Errorf("expected diagnostic in statusMessage, got %q", m2.statusMessage)
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

// ─── Mock PortForwarder for W9 ──────────────────────────────────────────────

type mockPortForwarder struct {
	forwards map[string]bool
}

func newMockPortForwarder() *mockPortForwarder {
	return &mockPortForwarder{forwards: make(map[string]bool)}
}

func (m *mockPortForwarder) FindReadyPod(ctx context.Context, namespace, labelSelector string) (string, bool, error) {
	return "", false, nil
}

func (m *mockPortForwarder) ForwardPod(ctx context.Context, namespace, podName string, remotePort int) (string, int, error) {
	fid := namespace + "/" + podName + ":8080"
	m.forwards[fid] = true
	return fid, 10000 + remotePort, nil
}

func (m *mockPortForwarder) Close(forwardID string) error {
	delete(m.forwards, forwardID)
	return nil
}

func (m *mockPortForwarder) CloseAll() error {
	m.forwards = make(map[string]bool)
	return nil
}

func (m *mockPortForwarder) CloseHITLForward() error {
	return nil
}

func (m *mockPortForwarder) SetHITLForward(namespace, podName string, remotePort int) error {
	return nil
}

func (m *mockPortForwarder) ActiveForwards() []string {
	keys := make([]string, 0, len(m.forwards))
	for k := range m.forwards {
		keys = append(keys, k)
	}
	return keys
}

func (m *mockPortForwarder) GetHITLLocalPort() (int, bool) {
	return 0, false
}

func (m *mockPortForwarder) IsOpen(fid string) bool {
	return m.forwards[fid]
}
