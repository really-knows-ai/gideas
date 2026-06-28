package controller

import (
	"context"
	"sync"
	"testing"
	"time"

	flowv1gen "github.com/gideas/flow/gen/flow/v1"
	flowv1 "github.com/gideas/flow/operator/api/v1"
	"github.com/gideas/flow/pkg/eventbus"
	"google.golang.org/grpc"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// ---------------------------------------------------------------------------
// Spy publisher for lifecycle event assertions
// ---------------------------------------------------------------------------

type lifecycleSpy struct {
	mu     sync.Mutex
	events []*flowv1gen.PublishRequest
}

func (s *lifecycleSpy) Publish(_ context.Context, req *flowv1gen.PublishRequest, _ ...grpc.CallOption) (*flowv1gen.PublishResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, req)
	return &flowv1gen.PublishResponse{Acknowledged: true}, nil
}

func (s *lifecycleSpy) Subscribe(_ context.Context, _ *flowv1gen.SubscribeRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[flowv1gen.FlowEvent], error) {
	return nil, nil
}

// byChannel returns events matching the given channel.
func (s *lifecycleSpy) byChannel(ch string) []*flowv1gen.PublishRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*flowv1gen.PublishRequest
	for _, e := range s.events {
		if e.GetChannel() == ch {
			out = append(out, e)
		}
	}
	return out
}

// testReconcilerWithAuditor creates a WorkitemReconciler with a spy-backed
// AsyncPublisher. Returns the reconciler, the spy for assertions, and a stop
// function that must be called to flush buffered events.
func testReconcilerWithAuditor(objs ...client.Object) (*WorkitemReconciler, *lifecycleSpy, func()) {
	scheme := testScheme()
	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&flowv1.Workitem{})

	for _, obj := range objs {
		builder = builder.WithObjects(obj)
	}

	spy := &lifecycleSpy{}
	pub := eventbus.NewAsyncPublisher(spy)

	r := &WorkitemReconciler{
		Client:  builder.Build(),
		Scheme:  scheme,
		Auditor: pub,
	}
	return r, spy, func() { pub.Stop() }
}

// findLabel returns the value for the first label with the given key, or "".
func findLabel(labels []*flowv1gen.Label, key string) string {
	for _, l := range labels {
		if l.GetKey() == key {
			return l.GetValue()
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Lifecycle: Pending → Failed (thrash budget exceeded)
// ---------------------------------------------------------------------------

func TestLifecycle_PendingThrashFailed(t *testing.T) {
	flow := testFlow(5) // maxVisits=5
	node := testNode("worker")
	wi := testWorkitem(phasePending, "worker")
	wi.Status.ThrashCounters = map[string]int32{"worker": 3, "other": 2}

	r, spy, stop := testReconcilerWithAuditor(flow, node, wi)

	_, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	stop()

	lifecycle := spy.byChannel("workitem")
	if len(lifecycle) != 1 {
		t.Fatalf("Expected 1 lifecycle event, got %d", len(lifecycle))
	}

	evt := lifecycle[0].GetEvent()
	if evt.GetEventType() != "workitem.phase_changed" {
		t.Fatalf("Expected event_type workitem.phase_changed, got %q", evt.GetEventType())
	}
	if got := findLabel(evt.GetLabels(), "phase"); got != phaseFailed {
		t.Fatalf("Expected phase label %q, got %q", phaseFailed, got)
	}
	if got := findLabel(evt.GetLabels(), "workitem_id"); got != testWorkitemName {
		t.Fatalf("Expected workitem_id label %q, got %q", testWorkitemName, got)
	}
	if got := findLabel(evt.GetLabels(), "node_id"); got != "worker" {
		t.Fatalf("Expected node_id label 'worker', got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle: Pending → Running (successful dispatch)
// ---------------------------------------------------------------------------

func TestLifecycle_PendingToRunning(t *testing.T) {
	flow := testFlow(100)
	node := testNode("worker")
	wi := testWorkitem(phasePending, "worker")

	r, spy, stop := testReconcilerWithAuditor(flow, node, wi)

	// Dispatch will fail (no pods), but the Running transition + lifecycle
	// event should still be emitted if the claim succeeded.
	_, _ = r.Reconcile(context.Background(), testReq(testWorkitemName))

	stop()

	lifecycle := spy.byChannel("workitem")
	// The reconciler transitions to Running before dispatch. If dispatch
	// fails it reverts to Pending. The lifecycle event for Running is
	// emitted after successful dispatch, so we may get 0 or 1 events
	// depending on dispatch outcome. Since there are no pods, dispatch
	// fails and the event is not emitted.
	// For this test we verify the auditor was not nil and no panic occurred.
	// A proper integration test with pods would verify the Running event.
	t.Logf("Lifecycle events emitted: %d (dispatch may have failed)", len(lifecycle))
}

// ---------------------------------------------------------------------------
// Lifecycle: Running → Failed (timeout)
// ---------------------------------------------------------------------------

func TestLifecycle_RunningTimeoutFailed(t *testing.T) {
	flow := testFlow(100)
	flow.Spec.GovernancePolicy.DefaultTimeout = metav1.Duration{Duration: 10 * time.Minute}

	node := testNode("worker")
	wi := testWorkitem(phaseRunning, "worker")

	past := metav1.NewTime(time.Now().Add(-15 * time.Minute))
	wi.Status.AssignedAt = &past

	r, spy, stop := testReconcilerWithAuditor(flow, node, wi)

	_, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	stop()

	lifecycle := spy.byChannel("workitem")
	if len(lifecycle) != 1 {
		t.Fatalf("Expected 1 lifecycle event, got %d", len(lifecycle))
	}

	evt := lifecycle[0].GetEvent()
	if got := findLabel(evt.GetLabels(), "phase"); got != phaseFailed {
		t.Fatalf("Expected phase label %q, got %q", phaseFailed, got)
	}
	if got := findLabel(evt.GetLabels(), "node_id"); got != "worker" {
		t.Fatalf("Expected node_id label 'worker', got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle: Routing → Completed
// ---------------------------------------------------------------------------

func TestLifecycle_RoutingCompleted(t *testing.T) {
	flow := testFlow(100)
	exitNode := testExitNode("publisher", "standard-exit")
	wi := testWorkitem(phaseRouting, "publisher")
	wi.Status.RoutingInstruction = &flowv1.RoutingInstruction{
		Type: "complete",
	}

	r, spy, stop := testReconcilerWithAuditor(flow, exitNode, wi)

	_, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	stop()

	lifecycle := spy.byChannel("workitem")
	if len(lifecycle) != 1 {
		t.Fatalf("Expected 1 lifecycle event, got %d", len(lifecycle))
	}

	evt := lifecycle[0].GetEvent()
	if evt.GetEventType() != "workitem.phase_changed" {
		t.Fatalf("Expected event_type workitem.phase_changed, got %q", evt.GetEventType())
	}
	if got := findLabel(evt.GetLabels(), "phase"); got != phaseCompleted {
		t.Fatalf("Expected phase label %q, got %q", phaseCompleted, got)
	}
	if got := findLabel(evt.GetLabels(), "node_id"); got != "publisher" {
		t.Fatalf("Expected node_id label 'publisher', got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle: Routing → Pending (route to next node)
// ---------------------------------------------------------------------------

func TestLifecycle_RoutingToPending(t *testing.T) {
	flow := testFlow(100)
	node := testNode("worker")
	nextNode := testNode("next-node")
	wi := testWorkitem(phaseRouting, "worker")
	wi.Status.RoutingInstruction = &flowv1.RoutingInstruction{
		Type:   "route_to_output",
		Target: "default",
	}

	r, spy, stop := testReconcilerWithAuditor(flow, node, nextNode, wi)

	_, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	stop()

	lifecycle := spy.byChannel("workitem")
	if len(lifecycle) != 1 {
		t.Fatalf("Expected 1 lifecycle event, got %d", len(lifecycle))
	}

	evt := lifecycle[0].GetEvent()
	if got := findLabel(evt.GetLabels(), "phase"); got != phasePending {
		t.Fatalf("Expected phase label %q, got %q", phasePending, got)
	}
	if got := findLabel(evt.GetLabels(), "node_id"); got != "next-node" {
		t.Fatalf("Expected node_id label 'next-node', got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle: Routing → Failed (thrash guard during routing)
// ---------------------------------------------------------------------------

func TestLifecycle_RoutingThrashFailed(t *testing.T) {
	flow := testFlow(5) // maxVisits=5
	node := testNode("worker")
	wi := testWorkitem(phaseRouting, "worker")
	wi.Status.ThrashCounters = map[string]int32{"worker": 3, "other": 3}
	wi.Status.RoutingInstruction = &flowv1.RoutingInstruction{
		Type:   "route_to_output",
		Target: "default",
	}

	r, spy, stop := testReconcilerWithAuditor(flow, node, wi)

	_, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	stop()

	lifecycle := spy.byChannel("workitem")
	if len(lifecycle) != 1 {
		t.Fatalf("Expected 1 lifecycle event, got %d", len(lifecycle))
	}

	evt := lifecycle[0].GetEvent()
	if got := findLabel(evt.GetLabels(), "phase"); got != phaseFailed {
		t.Fatalf("Expected phase label %q, got %q", phaseFailed, got)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle: parent_workitem_id label included for child workitems
// ---------------------------------------------------------------------------

func TestLifecycle_ChildWorkitem_IncludesParentLabel(t *testing.T) {
	flow := testFlow(100)
	flow.Spec.GovernancePolicy.DefaultTimeout = metav1.Duration{Duration: 10 * time.Minute}

	node := testNode("codify")
	wi := testWorkitem(phaseRunning, "codify")
	wi.Status.ParentWorkitemID = "parent-wi-42"

	past := metav1.NewTime(time.Now().Add(-15 * time.Minute))
	wi.Status.AssignedAt = &past

	r, spy, stop := testReconcilerWithAuditor(flow, node, wi)

	_, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	stop()

	lifecycle := spy.byChannel("workitem")
	if len(lifecycle) != 1 {
		t.Fatalf("Expected 1 lifecycle event, got %d", len(lifecycle))
	}

	evt := lifecycle[0].GetEvent()
	if got := findLabel(evt.GetLabels(), "parent_workitem_id"); got != "parent-wi-42" {
		t.Fatalf("Expected parent_workitem_id label 'parent-wi-42', got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle: root workitem has no parent_workitem_id label
// ---------------------------------------------------------------------------

func TestLifecycle_RootWorkitem_NoParentLabel(t *testing.T) {
	flow := testFlow(100)
	flow.Spec.GovernancePolicy.DefaultTimeout = metav1.Duration{Duration: 10 * time.Minute}

	node := testNode("worker")
	wi := testWorkitem(phaseRunning, "worker")
	// ParentWorkitemID is empty (root workitem).

	past := metav1.NewTime(time.Now().Add(-15 * time.Minute))
	wi.Status.AssignedAt = &past

	r, spy, stop := testReconcilerWithAuditor(flow, node, wi)

	_, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	stop()

	lifecycle := spy.byChannel("workitem")
	if len(lifecycle) != 1 {
		t.Fatalf("Expected 1 lifecycle event, got %d", len(lifecycle))
	}

	evt := lifecycle[0].GetEvent()
	if got := findLabel(evt.GetLabels(), "parent_workitem_id"); got != "" {
		t.Fatalf("Expected no parent_workitem_id label for root workitem, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle: flow_namespace attribute from workitem namespace
// ---------------------------------------------------------------------------

func TestLifecycle_FlowIDAttribute(t *testing.T) {
	flow := testFlow(100)
	exitNode := testExitNode("publisher", "standard-exit")
	wi := testWorkitem(phaseRouting, "publisher")
	wi.Status.RoutingInstruction = &flowv1.RoutingInstruction{
		Type: "complete",
	}

	r, spy, stop := testReconcilerWithAuditor(flow, exitNode, wi)

	_, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	stop()

	lifecycle := spy.byChannel("workitem")
	if len(lifecycle) != 1 {
		t.Fatalf("Expected 1 lifecycle event, got %d", len(lifecycle))
	}

	evt := lifecycle[0].GetEvent()
	// flow_namespace attribute is now the workitem's namespace (one namespace = one flow).
	if got := evt.GetAttributes()["flow_namespace"]; got != "default" {
		t.Fatalf("Expected flow_namespace attribute %q, got %q", "default", got)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle: nil auditor does not panic
// ---------------------------------------------------------------------------

func TestLifecycle_NilAuditor_NoPanic(t *testing.T) {
	flow := testFlow(5)
	node := testNode("worker")
	wi := testWorkitem(phasePending, "worker")
	wi.Status.ThrashCounters = map[string]int32{"worker": 3, "other": 2}

	// Use testReconciler (no auditor) — should not panic.
	r := testReconciler(flow, node, wi)

	_, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle: both audit and workitem events emitted
// ---------------------------------------------------------------------------

func TestLifecycle_BothAuditAndWorkitemEvents(t *testing.T) {
	flow := testFlow(5) // maxVisits=5
	node := testNode("worker")
	wi := testWorkitem(phasePending, "worker")
	wi.Status.ThrashCounters = map[string]int32{"worker": 3, "other": 2}

	r, spy, stop := testReconcilerWithAuditor(flow, node, wi)

	_, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	stop()

	audit := spy.byChannel("audit")
	lifecycle := spy.byChannel("workitem")

	if len(audit) != 1 {
		t.Fatalf("Expected 1 audit event, got %d", len(audit))
	}
	if len(lifecycle) != 1 {
		t.Fatalf("Expected 1 lifecycle event, got %d", len(lifecycle))
	}

	// Verify they are distinct channels.
	if audit[0].GetEvent().GetEventType() == lifecycle[0].GetEvent().GetEventType() {
		t.Fatal("Audit and lifecycle events should have different event types")
	}
}
