package components

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/foundry/flow/tools/flowctl/internal/api"
	"github.com/foundry/flow/tools/flowctl/internal/tui/types"
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
	m.Choices = []types.Choice{} // non-nil but empty — simulates a queue item with no choices
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

// blockingForwarder blocks in ForwardPod until released, forcing the Probe cmd
// goroutine to overlap with the caller goroutine's reads/writes.
type blockingForwarder struct {
	entered   chan struct{}
	release   chan struct{}
	localPort int // the httptest queue-service port
}

func (b *blockingForwarder) FindReadyPod(context.Context, string, string) (string, bool, error) {
	return "queue-service-pod", true, nil
}

func (b *blockingForwarder) ForwardPod(context.Context, string, string, int) (string, int, error) {
	close(b.entered)
	<-b.release
	return "test-ns/queue-service-pod:8081", b.localPort, nil
}

func (b *blockingForwarder) Close(string) error { return nil }

func (b *blockingForwarder) CloseAll() error { return nil }

var _ api.PortForwarder = (*blockingForwarder)(nil)

// TestHitlStateConcurrentProbeAccess runs the Probe cmd goroutine concurrently
// with accessor/reset/close calls from the caller goroutine (the bubbletea
// Update goroutine in production). Under -race this fails if the shared
// HitlState fields are not mutex-guarded.
func TestHitlStateConcurrentProbeAccess(t *testing.T) {
	// The queue-service stub that the Probe cmd's HitlClient reaches through the
	// (fake) port-forward's localPort.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/queues/hitl-approval/wi-001" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"workitem_id":"wi-001","queue_name":"hitl-approval","status":"queued","choices":["approve","cancel"]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	localPort, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}

	h := NewHitlState()
	bf := &blockingForwarder{
		entered:   make(chan struct{}),
		release:   make(chan struct{}),
		localPort: localPort,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// clientset is nil because the production Probe path never consults it
		// (SPEC R-5.1: the queue-service pod is reached via the PortForwarder).
		cmd := h.Probe(context.Background(), nil, "test-ns", "hitl-approval", "wi-001", bf)
		msg := cmd() // returns HitlProbeResultMsg once the forward is released
		if _, ok := msg.(HitlProbeResultMsg); !ok {
			t.Errorf("expected HitlProbeResultMsg, got %T", msg)
		}
	}()

	<-bf.entered // the probe goroutine is now blocked inside ForwardPod

	for i := 0; i < 100; i++ {
		h.Active()
		h.Exhausted()
		h.GetNodeName()
		h.GetWorkitemID()
		h.GetPendingChoice()
		h.ResetForNewWorkitem()
	}
	h.Close(bf)

	close(bf.release)
	<-done
}

// ─── RecordProbeFailure (probeHitl resolution-failure seam) ────────────────

// TestRecordProbeFailure_RetriesThenExhausts pins the RecordProbeFailure
// contract: attempts 1-4 yield HitlProbeRetryMsg with Exhausted() false, the
// 5th (probeMax) yields HitlProbeExhaustedMsg carrying the workitem, the
// queue-service label, and the diagnostic, with Exhausted() true.
func TestRecordProbeFailure_RetriesThenExhausts(t *testing.T) {
	h := NewHitlState()
	for i := 0; i < 4; i++ {
		msg := h.RecordProbeFailure("wi-001", "diag")
		if _, ok := msg.(HitlProbeRetryMsg); !ok {
			t.Fatalf("attempt %d: expected HitlProbeRetryMsg, got %T", i+1, msg)
		}
		if h.Exhausted() {
			t.Fatalf("attempt %d: expected not exhausted yet", i+1)
		}
	}
	msg := h.RecordProbeFailure("wi-001", "diag")
	exh, ok := msg.(HitlProbeExhaustedMsg)
	if !ok {
		t.Fatalf("expected HitlProbeExhaustedMsg, got %T", msg)
	}
	if exh.WorkitemID != "wi-001" {
		t.Errorf("expected WorkitemID wi-001, got %q", exh.WorkitemID)
	}
	if exh.NodeName != QueueServiceLabel {
		t.Errorf("expected NodeName %q, got %q", QueueServiceLabel, exh.NodeName)
	}
	if exh.Diagnostic != "diag" {
		t.Errorf("expected Diagnostic diag, got %q", exh.Diagnostic)
	}
	if !h.Exhausted() {
		t.Error("expected Exhausted() true after probeMax failures")
	}
}

// TestRecordProbeFailure_ResetArmsAgain pins that ResetForNewWorkitem clears
// the attempt counter so a new workitem cycle retries instead of staying
// exhausted.
func TestRecordProbeFailure_ResetArmsAgain(t *testing.T) {
	h := NewHitlState()
	for i := 0; i < 5; i++ {
		h.RecordProbeFailure("wi-001", "diag")
	}
	if !h.Exhausted() {
		t.Fatal("expected exhausted after 5 failures")
	}

	h.ResetForNewWorkitem()
	if h.Exhausted() {
		t.Error("expected not exhausted after reset")
	}

	msg := h.RecordProbeFailure("wi-002", "diag2")
	if _, ok := msg.(HitlProbeRetryMsg); !ok {
		t.Fatalf("expected HitlProbeRetryMsg after reset, got %T", msg)
	}
	if h.Exhausted() {
		t.Error("expected not exhausted after one failure post-reset")
	}
}
