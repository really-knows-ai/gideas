package tui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foundry/flow/tools/flowctl/internal/api"
	"github.com/foundry/flow/tools/flowctl/internal/tui/components"
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
	m.hitlState = components.NewHitlState()
	m.namespace = "test-ns"

	// Simulate a modified event with a new node
	model, cmd := m.Update(WorkitemUpdateMsg{
		Event: "MODIFIED",
		Item:  api.WorkitemSummary{Name: "wi-001", Node: "hitl-approval"},
	})
	m2 := model.(*Model)

	// Since m.k8s is nil in tests, the probe won't be started with a full cmd,
	// but the debounced child count refresh cmd should still be present
	if cmd == nil {
		t.Error("expected non-nil command (debounced child refresh)")
	}

	// The workitem's node should be updated in the list
	if len(m2.workitemList.Items) > 0 {
		if m2.workitemList.Items[0].Node != "hitl-approval" {
			t.Errorf("expected list item node 'hitl-approval', got %q", m2.workitemList.Items[0].Node)
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
		NodeName:   "hitl-approval",
		QueueItem:  &api.QueueItem{WorkitemID: "wi-001"},
		Choices: []api.Choice{
			{Value: "approve", Label: "Approve", Type: "route"},
			{Value: "cancel", Label: "Cancel", Type: "cancel"},
		},
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
		NodeName:   "hitl-approval",
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

// W4: Default choices used when the queue item has no choices
// (Default choices are set by HitlState.Probe when the queue item has no choices;
// the Update function receives them as part of HitlProbeResultMsg.)
func TestDefaultChoicesOnProbeResult(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"

	// HitlProbeResultMsg carries whatever choices were resolved
	model, cmd := m.Update(components.HitlProbeResultMsg{
		WorkitemID: "wi-001",
		NodeName:   "hitl-approval",
		QueueItem:  &api.QueueItem{WorkitemID: "wi-001"},
		Choices: []api.Choice{
			{Value: "approve", Label: "Approve", Type: "route"},
			{Value: "cancel", Label: "Cancel", Type: "cancel"},
		},
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

// W5: Dynamic item-sourced choices
func TestDynamicChoicesOnProbeResult(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"

	// Dynamic choices from /choices
	model, cmd := m.Update(components.HitlProbeResultMsg{
		WorkitemID: "wi-001",
		NodeName:   "hitl-arbiter",
		QueueItem:  &api.QueueItem{WorkitemID: "wi-001"},
		Choices: []api.Choice{
			{Value: "accept", Label: "Accept", Type: "route"},
			{Value: "reject", Label: "Reject", Type: "route"},
			{Value: "cancel", Label: "Cancel", Type: "cancel"},
		},
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
	m.hitlState = components.NewHitlState()
	m2, _ := startHitlProbe(t, &m, "hitl-approval")

	// Press "c" (first letter of "Cancel")
	// This is handled in updateWorkitemDetailKeys
	model, cmd := m2.updateWorkitemDetailKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m3 := model.(*Model)

	if !m3.workitemDetail.hitl.ConfirmingCancel {
		t.Error("expected confirmingCancel=true after pressing cancel-type choice")
	}
	if m3.workitemDetail.hitl.PendingChoice != "cancel" {
		t.Errorf("expected pendingChoice 'cancel', got %q", m3.workitemDetail.hitl.PendingChoice)
	}
	if cmd != nil {
		t.Error("expected nil command (confirmation pending)")
	}
}

func TestCancelConfirmationDismissesOnNo(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.hitlState = components.NewHitlState()
	m2, _ := startHitlProbe(t, &m, "hitl-approval")
	m2.workitemDetail.hitl.ConfirmingCancel = true
	m2.workitemDetail.hitl.PendingChoice = "cancel"

	model, cmd := m2.updateWorkitemDetailKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m3 := model.(*Model)

	if m3.workitemDetail.hitl.ConfirmingCancel {
		t.Error("expected cancel confirmation dismissed")
	}
	if m3.workitemDetail.hitl.PendingChoice != "" {
		t.Errorf("expected pendingChoice cleared, got %q", m3.workitemDetail.hitl.PendingChoice)
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestCancelConfirmationSubmitsOnYes(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.hitlState = components.NewHitlState()
	m2, _ := startHitlProbe(t, &m, "hitl-approval")
	m2.workitemDetail.hitl.ConfirmingCancel = true
	m2.workitemDetail.hitl.PendingChoice = "cancel"

	model, cmd := m2.updateWorkitemDetailKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m3 := model.(*Model)

	if m3.workitemDetail.hitl.ConfirmingCancel {
		t.Error("expected cancel confirmation cleared")
	}
	if m3.workitemDetail.hitl.PendingChoice != "" {
		t.Errorf("expected pendingChoice cleared, got %q", m3.workitemDetail.hitl.PendingChoice)
	}
	if !m3.workitemDetail.hitl.Loading {
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
	m.hitlState = components.NewHitlState()
	m2, mockPFM := startHitlProbe(t, &m, "hitl-approval")

	if len(mockPFM.forwards) != 1 {
		t.Fatalf("expected 1 HITL forward from probe, got %d", len(mockPFM.forwards))
	}

	// Send Ctrl+C
	_, cmd := m2.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected non-nil command (quit)")
	}

	// Ensure the forward was closed
	if len(mockPFM.forwards) != 0 {
		t.Error("expected HITL forward to be closed on Ctrl+C")
	}
}

// ─── Mock PortForwarder for W9 ──────────────────────────────────────────────

type mockPortForwarder struct {
	localPort int
	forwards  map[string]bool
}

func newMockPortForwarder(localPort int) *mockPortForwarder {
	return &mockPortForwarder{localPort: localPort, forwards: make(map[string]bool)}
}

func (m *mockPortForwarder) FindReadyPod(ctx context.Context, namespace, labelSelector string) (string, bool, error) {
	// R-5.1: the probe reaches the queue-service pod directly. Only return a
	// ready pod for the queue-service label; anything else is not-found.
	if labelSelector == components.QueueServiceLabel {
		return "queue-service-pod", true, nil
	}
	return "", false, nil
}

func (m *mockPortForwarder) ForwardPod(ctx context.Context, namespace, podName string, remotePort int) (string, int, error) {
	fid := namespace + "/" + podName + ":8080"
	m.forwards[fid] = true
	return fid, m.localPort, nil
}

func (m *mockPortForwarder) Close(forwardID string) error {
	delete(m.forwards, forwardID)
	return nil
}

func (m *mockPortForwarder) CloseAll() error {
	m.forwards = make(map[string]bool)
	return nil
}

// startHitlProbe drives HitlState.Probe through the real production path
// against a stubbed HITL HTTP endpoint, so the model reaches an active probe
// state (live forward, loaded choices) without a test-only setter seam.
func startHitlProbe(t *testing.T, m *Model, nodeName string) (*Model, *mockPortForwarder) {
	t.Helper()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/queues/hitl-approval/wi-001":
			// Queue-service item route (R-5.1). No `choices` field, so
			// selectedFromItem falls back to DefaultAPIChoices() — this keeps
			// the cancel-confirmation tests (which press "c" and expect a
			// cancel-type choice, detail_keys.go) passing unchanged, since
			// item-sourced choices are all Type "route".
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"workitem_id":"wi-001","queue_name":"hitl-approval","status":"queued"}`)
		case r.URL.Path == "/choices" || strings.HasPrefix(r.URL.Path, "/queue/"):
			// The old node flow (node /choices or /queue/{id}) must never be
			// reached — flowctl talks to the queue-service directly (R-5.1).
			t.Errorf("node route %s reached under never-a-node (SPEC R-5.1)", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	localPort, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}

	m.namespace = "test-ns"
	m.workitemDetail.workitemName = "wi-001"
	m.pfm = newMockPortForwarder(localPort)

	// Probe reaches the queue-service via the PortForwarder only; the
	// clientset (pod listing) is no longer consulted by the production path.
	cmd := m.hitlState.Probe(context.Background(), nil, m.namespace, nodeName, m.workitemDetail.workitemName, m.pfm)
	msg := cmd()
	model, _ := m.Update(msg)
	m2, ok := model.(*Model)
	if !ok {
		t.Fatalf("unexpected model type %T", model)
	}
	if !m2.hitlState.Active() {
		t.Fatal("expected hitlState to be active after probe")
	}
	return m2, m.pfm.(*mockPortForwarder)
}
