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

	"github.com/foundry/flow/tools/flowctl/internal/api"
	"github.com/foundry/flow/tools/flowctl/internal/tui/components"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// ─── probeHitl queue-service resolution tests ───────────────────────────────

// startQueueServiceStub runs an in-memory queue-service stub (SPEC R-5.1):
// GET /queues returns the bare queue-name array, and the hitl-approval/wi-001
// item route returns itemStatus/itemBody. Returns the stub port and a stop
// func. The mock PortForwarder hands this port to the HitlClient, so no real
// port-forward is involved.
func startQueueServiceStub(t *testing.T, queues string, itemStatus int, itemBody string) (int, func()) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/queues":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, queues)
		case r.URL.Path == "/queues/hitl-approval/wi-001":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(itemStatus)
			fmt.Fprint(w, itemBody)
		default:
			http.NotFound(w, r)
		}
	}))
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}
	return port, ts.Close
}

// wireProbeHitlModel returns a Model wired for probeHitl: a real HitlState
// over a fake clientset (never consulted by the R-5.1 queue-service path),
// namespace test-ns, a background context, and the given PortForwarder bound
// to the queue-service stub port.
func wireProbeHitlModel(localPort int, pfm api.PortForwarder) Model {
	m := initialModel()
	m.hitlState = components.NewHitlState()
	m.namespace = "test-ns"
	m.ctx = context.Background()
	m.k8s = &api.K8sClient{CoreClient: k8sfake.NewSimpleClientset()}
	m.pfm = pfm
	return m
}

// labelRecordingForwarder wraps mockPortForwarder and records every
// FindReadyPod label selector, so tests can pin the probe to the
// queue-service label only (SPEC R-5.1).
type labelRecordingForwarder struct {
	mockPortForwarder
	labels []string
}

func (m *labelRecordingForwarder) FindReadyPod(ctx context.Context, namespace, labelSelector string) (string, bool, error) {
	m.labels = append(m.labels, labelSelector)
	if labelSelector != components.QueueServiceLabel {
		return "", false, nil
	}
	return "queue-service-pod", true, nil
}

// TestProbeHitl_ResolutionFailureClosesForward: a resolution miss (queue
// listed, item 404) records attempt 1 as HitlProbeRetryMsg, closes the
// resolution port-forward inside the cmd, and leaves the cycle unexhausted.
func TestProbeHitl_ResolutionFailureClosesForward(t *testing.T) {
	port, stop := startQueueServiceStub(t, `["hitl-approval"]`, http.StatusNotFound, "")
	defer stop()

	m := wireProbeHitlModel(port, newMockPortForwarder(port))
	pfm := m.pfm.(*mockPortForwarder)

	cmd := m.probeHitl("wi-001")
	if cmd == nil {
		t.Fatal("expected non-nil probe cmd")
	}
	msg := cmd()
	if _, ok := msg.(components.HitlProbeRetryMsg); !ok {
		t.Fatalf("expected HitlProbeRetryMsg, got %T", msg)
	}
	if len(pfm.forwards) != 0 {
		t.Errorf("expected resolution forward closed after failed resolve, got %d forwards", len(pfm.forwards))
	}
	if m.hitlState.Exhausted() {
		t.Error("expected not exhausted after first failure")
	}
}

// TestProbeHitl_ExhaustsAfterProbeMax: probeHitl+run 5x against a queue that
// never enqueues the item — attempts 1-4 retry, attempt 5 exhausts with the
// "not enqueued" diagnostic, and every resolution forward is closed.
func TestProbeHitl_ExhaustsAfterProbeMax(t *testing.T) {
	port, stop := startQueueServiceStub(t, `["hitl-approval"]`, http.StatusNotFound, "")
	defer stop()

	m := wireProbeHitlModel(port, newMockPortForwarder(port))
	pfm := m.pfm.(*mockPortForwarder)

	for i := 0; i < 4; i++ {
		cmd := m.probeHitl("wi-001")
		if cmd == nil {
			t.Fatalf("attempt %d: expected non-nil probe cmd", i+1)
		}
		msg := cmd()
		if _, ok := msg.(components.HitlProbeRetryMsg); !ok {
			t.Fatalf("attempt %d: expected HitlProbeRetryMsg, got %T", i+1, msg)
		}
	}

	cmd := m.probeHitl("wi-001")
	if cmd == nil {
		t.Fatal("expected non-nil probe cmd")
	}
	msg := cmd()
	exh, ok := msg.(components.HitlProbeExhaustedMsg)
	if !ok {
		t.Fatalf("expected HitlProbeExhaustedMsg, got %T", msg)
	}
	if exh.WorkitemID != "wi-001" {
		t.Errorf("expected WorkitemID wi-001, got %q", exh.WorkitemID)
	}
	if !strings.Contains(exh.Diagnostic, "not enqueued") {
		t.Errorf("expected diagnostic containing 'not enqueued', got %q", exh.Diagnostic)
	}
	if !m.hitlState.Exhausted() {
		t.Error("expected Exhausted() true after probeMax resolution failures")
	}
	if len(pfm.forwards) != 0 {
		t.Errorf("expected no forwards left after exhausted resolution failures, got %d", len(pfm.forwards))
	}
}

// TestProbeHitl_FindReadyPodOnlyQueueServiceLabel: every FindReadyPod call
// made by the probe must target components.QueueServiceLabel — never a node
// label (SPEC R-5.1).
func TestProbeHitl_FindReadyPodOnlyQueueServiceLabel(t *testing.T) {
	port, stop := startQueueServiceStub(t, `["hitl-approval"]`, http.StatusNotFound, "")
	defer stop()

	pfm := &labelRecordingForwarder{mockPortForwarder: *newMockPortForwarder(port)}
	m := wireProbeHitlModel(port, pfm)

	cmd := m.probeHitl("wi-001")
	if cmd == nil {
		t.Fatal("expected non-nil probe cmd")
	}
	msg := cmd()
	if _, ok := msg.(components.HitlProbeRetryMsg); !ok {
		t.Fatalf("expected HitlProbeRetryMsg, got %T", msg)
	}
	if len(pfm.labels) == 0 {
		t.Fatal("expected FindReadyPod to have been called")
	}
	for i, label := range pfm.labels {
		if label != components.QueueServiceLabel {
			t.Errorf("FindReadyPod call %d used label %q, want %q", i, label, components.QueueServiceLabel)
		}
	}
}

// TestProbeHitl_NoFindReadyPodReturnsNil: the nil-pfm guard returns nil
// instead of panicking, so the detail screen simply skips probing.
func TestProbeHitl_NoFindReadyPodReturnsNil(t *testing.T) {
	m := initialModel()
	m.hitlState = components.NewHitlState()
	m.namespace = "test-ns"
	m.ctx = context.Background()
	m.k8s = &api.K8sClient{CoreClient: k8sfake.NewSimpleClientset()}
	// pfm left nil on purpose.

	if cmd := m.probeHitl("wi-001"); cmd != nil {
		t.Fatalf("expected nil cmd when pfm is nil, got %T", cmd)
	}
}

// TestProbeHitl_SuccessDelegatesToProbe: once the queue-service resolves the
// item, probeHitl delegates to HitlState.Probe — the result message carries
// the item-sourced choices and the session becomes active.
func TestProbeHitl_SuccessDelegatesToProbe(t *testing.T) {
	port, stop := startQueueServiceStub(t, `["hitl-approval"]`, http.StatusOK,
		`{"workitem_id":"wi-001","queue_name":"hitl-approval","status":"queued","choices":["approve","cancel"]}`)
	defer stop()

	m := wireProbeHitlModel(port, newMockPortForwarder(port))

	cmd := m.probeHitl("wi-001")
	if cmd == nil {
		t.Fatal("expected non-nil probe cmd")
	}
	msg := cmd()
	result, ok := msg.(components.HitlProbeResultMsg)
	if !ok {
		t.Fatalf("expected HitlProbeResultMsg, got %T", msg)
	}
	if result.WorkitemID != "wi-001" {
		t.Errorf("expected WorkitemID wi-001, got %q", result.WorkitemID)
	}
	if result.NodeName != "hitl-approval" {
		t.Errorf("expected NodeName hitl-approval, got %q", result.NodeName)
	}
	if !m.hitlState.Active() {
		t.Error("expected hitlState active after successful probe")
	}
	if len(result.Choices) != 2 {
		t.Fatalf("expected 2 choices, got %d", len(result.Choices))
	}
	if result.Choices[0].Value != "approve" || result.Choices[1].Value != "cancel" {
		t.Errorf("expected approve/cancel choices, got %+v", result.Choices)
	}
}
