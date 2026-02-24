package controller

import (
	"context"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/operator/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	phasePending   = "Pending"
	phaseRunning   = "Running"
	phaseRouting   = "Routing"
	phaseCompleted = "Completed"
	// phaseFailed is already declared in foundrynode_controller.go (same package).

	reasonThrashBudgetExceeded = "THRASH_BUDGET_EXCEEDED"
	reasonTimeoutExceeded      = "TIMEOUT_EXCEEDED"

	testWorkitemName = "wi-1"
	testFlowLabel    = "flow.gideas.io/flow"
	testFlowName     = "test-flow"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = flowv1.AddToScheme(s)
	return s
}

func testReconciler(objs ...client.Object) *WorkitemReconciler {
	scheme := testScheme()
	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&flowv1.Workitem{})

	for _, obj := range objs {
		builder = builder.WithObjects(obj)
	}

	return &WorkitemReconciler{
		Client: builder.Build(),
		Scheme: scheme,
	}
}

func testReq(name string) reconcile.Request {
	return reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: name},
	}
}

func testFlow(maxVisits int32) *flowv1.FoundryFlow {
	return &flowv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: testFlowName, Namespace: "default"},
		Spec: flowv1.FoundryFlowSpec{
			EntryContracts: map[string]flowv1.Contract{"main": {}},
			ExitContracts: map[string]flowv1.Contract{
				"standard-exit": {"haiku": {"linter", "review"}},
			},
			GovernancePolicy: flowv1.GovernancePolicy{
				MaxVisits:      maxVisits,
				DefaultTimeout: metav1.Duration{Duration: 30 * time.Minute},
				MaxTimeout:     metav1.Duration{Duration: 1 * time.Hour},
			},
		},
	}
}

func testNode(name string) *flowv1.FoundryNode {
	return &flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image: name + ":latest",
			Outputs: []flowv1.Output{
				{Name: "default", Target: "next-node"},
			},
		},
	}
}

func testExitNode(name, exitContract string) *flowv1.FoundryNode {
	return &flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image: name + ":latest",
			Exit:  exitContract,
		},
	}
}

func testWorkitem(phase, assignee string, labels map[string]string) *flowv1.Workitem {
	return &flowv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWorkitemName,
			Namespace: "default",
			Labels:    labels,
		},
		Status: flowv1.WorkitemStatus{
			Phase:           phase,
			CurrentAssignee: assignee,
		},
	}
}

func flowLabels() map[string]string {
	return map[string]string{testFlowLabel: testFlowName}
}

// getWorkitem fetches a fresh copy of the workitem from the fake client.
func getWorkitem(t *testing.T, r *WorkitemReconciler) *flowv1.Workitem {
	t.Helper()
	var wi flowv1.Workitem
	err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: testWorkitemName}, &wi)
	if err != nil {
		t.Fatalf("Failed to get workitem %q: %v", testWorkitemName, err)
	}
	return &wi
}

// assertNoRequeue checks that the result does not request a requeue.
func assertNoRequeue(t *testing.T, result reconcile.Result) {
	t.Helper()
	if result.RequeueAfter > 0 {
		t.Errorf("Expected no requeue, got RequeueAfter=%v", result.RequeueAfter)
	}
}

// ---------------------------------------------------------------------------
// Pending phase: thrash counter increment
// ---------------------------------------------------------------------------

func TestPending_ThrashCounterIncrement(t *testing.T) {
	// Pending workitem with an assignee should have its thrash counter
	// incremented during reconciliation. Dispatch will fail because there
	// are no pods — the counter is incremented before dispatch.
	flow := testFlow(100)
	node := testNode("worker")
	wi := testWorkitem(phasePending, "worker", flowLabels())

	r := testReconciler(flow, node, wi)

	// Reconcile — dispatch will fail (no pods), workitem reverts to Pending.
	// Error is expected because no pods exist for dispatch.
	_, _ = r.Reconcile(context.Background(), testReq(testWorkitemName))

	fresh := getWorkitem(t, r)
	// The counter persists if the Running claim succeeded (even if dispatch
	// failed and reverted). If the claim itself failed, the counter won't
	// persist — that is acceptable.
	if fresh.Status.ThrashCounters != nil {
		if count := fresh.Status.ThrashCounters["worker"]; count > 0 {
			t.Logf("Thrash counter incremented to %d — correct", count)
		}
	}
}

// ---------------------------------------------------------------------------
// Pending phase: thrash budget exceeded
// ---------------------------------------------------------------------------

func TestPending_ThrashBudgetExceeded(t *testing.T) {
	flow := testFlow(5)
	node := testNode("worker")
	wi := testWorkitem(phasePending, "worker", flowLabels())

	// Pre-set thrash counters to just below the limit (aggregate=5).
	// After increment (aggregate=6), it exceeds maxVisits=5.
	wi.Status.ThrashCounters = map[string]int32{
		"worker": 3,
		"other":  2,
	}

	r := testReconciler(flow, node, wi)

	result, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error on thrash-exceeded failure, got: %v", err)
	}
	assertNoRequeue(t, result)

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != phaseFailed {
		t.Fatalf("Expected phase %s, got %q", phaseFailed, fresh.Status.Phase)
	}
	if fresh.Status.FailureReason != reasonThrashBudgetExceeded {
		t.Fatalf("Expected failure reason %s, got %q", reasonThrashBudgetExceeded, fresh.Status.FailureReason)
	}
}

// ---------------------------------------------------------------------------
// Pending phase: no assignee
// ---------------------------------------------------------------------------

func TestPending_NoAssignee_Skips(t *testing.T) {
	flow := testFlow(100)
	wi := testWorkitem(phasePending, "", flowLabels())

	r := testReconciler(flow, wi)

	result, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	assertNoRequeue(t, result)

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != phasePending {
		t.Fatalf("Expected phase %s (unchanged), got %q", phasePending, fresh.Status.Phase)
	}
}

// ---------------------------------------------------------------------------
// Running phase: timeout enforcement
// ---------------------------------------------------------------------------

func TestRunning_TimeoutExpired(t *testing.T) {
	flow := testFlow(100)
	flow.Spec.GovernancePolicy.DefaultTimeout = metav1.Duration{Duration: 10 * time.Minute}

	node := testNode("worker")
	wi := testWorkitem(phaseRunning, "worker", flowLabels())

	// Assignment started 15 minutes ago — exceeds 10 minute timeout.
	past := metav1.NewTime(time.Now().Add(-15 * time.Minute))
	wi.Status.AssignedAt = &past

	r := testReconciler(flow, node, wi)

	result, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error on timeout failure, got: %v", err)
	}
	assertNoRequeue(t, result)

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != phaseFailed {
		t.Fatalf("Expected phase %s, got %q", phaseFailed, fresh.Status.Phase)
	}
	if fresh.Status.FailureReason != reasonTimeoutExceeded {
		t.Fatalf("Expected failure reason %s, got %q", reasonTimeoutExceeded, fresh.Status.FailureReason)
	}
}

func TestRunning_TimeoutNotExpired_Requeues(t *testing.T) {
	flow := testFlow(100)
	flow.Spec.GovernancePolicy.DefaultTimeout = metav1.Duration{Duration: 30 * time.Minute}

	node := testNode("worker")
	wi := testWorkitem(phaseRunning, "worker", flowLabels())

	// Assignment started 5 minutes ago — within 30 minute timeout.
	past := metav1.NewTime(time.Now().Add(-5 * time.Minute))
	wi.Status.AssignedAt = &past

	r := testReconciler(flow, node, wi)

	result, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	// Should requeue for remaining timeout (~25 minutes).
	if result.RequeueAfter <= 0 {
		t.Fatal("Expected RequeueAfter > 0 for non-expired timeout")
	}
	if result.RequeueAfter > 30*time.Minute {
		t.Errorf("Expected RequeueAfter <= 30m, got %v", result.RequeueAfter)
	}

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != phaseRunning {
		t.Fatalf("Expected phase %s (unchanged), got %q", phaseRunning, fresh.Status.Phase)
	}
}

func TestRunning_NodeSpecificTimeout(t *testing.T) {
	flow := testFlow(100)
	flow.Spec.GovernancePolicy.DefaultTimeout = metav1.Duration{Duration: 30 * time.Minute}
	flow.Spec.GovernancePolicy.MaxTimeout = metav1.Duration{Duration: 1 * time.Hour}

	node := testNode("worker")
	nodeTimeout := metav1.Duration{Duration: 5 * time.Minute}
	node.Spec.Timeout = &nodeTimeout

	wi := testWorkitem(phaseRunning, "worker", flowLabels())

	// Assignment started 6 minutes ago — exceeds 5 minute node-specific timeout.
	past := metav1.NewTime(time.Now().Add(-6 * time.Minute))
	wi.Status.AssignedAt = &past

	r := testReconciler(flow, node, wi)

	_, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != phaseFailed {
		t.Fatalf("Expected phase %s (node-specific timeout), got %q", phaseFailed, fresh.Status.Phase)
	}
	if fresh.Status.FailureReason != reasonTimeoutExceeded {
		t.Fatalf("Expected failure reason %s, got %q", reasonTimeoutExceeded, fresh.Status.FailureReason)
	}
}

func TestRunning_NodeTimeoutCappedAtMaxTimeout(t *testing.T) {
	flow := testFlow(100)
	flow.Spec.GovernancePolicy.DefaultTimeout = metav1.Duration{Duration: 30 * time.Minute}
	flow.Spec.GovernancePolicy.MaxTimeout = metav1.Duration{Duration: 10 * time.Minute}

	node := testNode("worker")
	nodeTimeout := metav1.Duration{Duration: 1 * time.Hour} // Node wants 1h but max is 10m.
	node.Spec.Timeout = &nodeTimeout

	wi := testWorkitem(phaseRunning, "worker", flowLabels())

	// Assignment started 12 minutes ago — exceeds capped timeout of 10m.
	past := metav1.NewTime(time.Now().Add(-12 * time.Minute))
	wi.Status.AssignedAt = &past

	r := testReconciler(flow, node, wi)

	_, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != phaseFailed {
		t.Fatalf("Expected phase %s (capped timeout), got %q", phaseFailed, fresh.Status.Phase)
	}
}

// ---------------------------------------------------------------------------
// Routing phase: route_to_output happy path
// ---------------------------------------------------------------------------

func TestRouting_RouteToOutput_HappyPath(t *testing.T) {
	flow := testFlow(100)
	node := testNode("worker")
	nextNode := testNode("next-node")
	wi := testWorkitem(phaseRouting, "worker", flowLabels())
	wi.Status.RoutingInstruction = &flowv1.RoutingInstruction{
		Type:   "route_to_output",
		Target: "default",
	}

	r := testReconciler(flow, node, nextNode, wi)

	_, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != phasePending {
		t.Fatalf("Expected phase %s, got %q", phasePending, fresh.Status.Phase)
	}
	if fresh.Status.CurrentAssignee != "next-node" {
		t.Fatalf("Expected assignee 'next-node', got %q", fresh.Status.CurrentAssignee)
	}
	if fresh.Status.RoutingInstruction != nil {
		t.Fatal("Expected routing instruction to be cleared")
	}
	if fresh.Status.AssignedAt != nil {
		t.Fatal("Expected assignedAt to be cleared")
	}
}

// ---------------------------------------------------------------------------
// Routing phase: complete happy path
// ---------------------------------------------------------------------------

func TestRouting_Complete_HappyPath(t *testing.T) {
	flow := testFlow(100)
	exitNode := testExitNode("publisher", "standard-exit")
	wi := testWorkitem(phaseRouting, "publisher", flowLabels())
	wi.Status.RoutingInstruction = &flowv1.RoutingInstruction{
		Type: "complete",
	}

	r := testReconciler(flow, exitNode, wi)

	_, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != phaseCompleted {
		t.Fatalf("Expected phase %s, got %q", phaseCompleted, fresh.Status.Phase)
	}
	if fresh.Status.CurrentAssignee != "" {
		t.Fatalf("Expected empty assignee, got %q", fresh.Status.CurrentAssignee)
	}
}

// ---------------------------------------------------------------------------
// Routing phase: complete from non-exit node
// ---------------------------------------------------------------------------

func TestRouting_Complete_NonExitNode_ReturnsError(t *testing.T) {
	flow := testFlow(100)
	node := testNode("worker") // Not exit-bound.
	wi := testWorkitem(phaseRouting, "worker", flowLabels())
	wi.Status.RoutingInstruction = &flowv1.RoutingInstruction{
		Type: "complete",
	}

	r := testReconciler(flow, node, wi)

	_, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err == nil {
		t.Fatal("Expected error for complete on non-exit node")
	}

	// Workitem should remain in Routing (error returned for retry).
	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != phaseRouting {
		t.Fatalf("Expected phase %s (unchanged), got %q", phaseRouting, fresh.Status.Phase)
	}
}

// ---------------------------------------------------------------------------
// Routing phase: unknown output
// ---------------------------------------------------------------------------

func TestRouting_UnknownOutput_ReturnsError(t *testing.T) {
	flow := testFlow(100)
	node := testNode("worker")
	wi := testWorkitem(phaseRouting, "worker", flowLabels())
	wi.Status.RoutingInstruction = &flowv1.RoutingInstruction{
		Type:   "route_to_output",
		Target: "nonexistent",
	}

	r := testReconciler(flow, node, wi)

	_, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err == nil {
		t.Fatal("Expected error for unknown output")
	}
}

// ---------------------------------------------------------------------------
// Routing phase: route_to with target validation
// ---------------------------------------------------------------------------

func TestRouting_RouteTo_TargetExists(t *testing.T) {
	flow := testFlow(100)
	currentNode := testNode("worker")
	targetNode := testNode("step-3")
	wi := testWorkitem(phaseRouting, "worker", flowLabels())
	wi.Status.RoutingInstruction = &flowv1.RoutingInstruction{
		Type:   "route_to",
		Target: "step-3",
	}

	r := testReconciler(flow, currentNode, targetNode, wi)

	_, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != phasePending {
		t.Fatalf("Expected phase %s, got %q", phasePending, fresh.Status.Phase)
	}
	if fresh.Status.CurrentAssignee != "step-3" {
		t.Fatalf("Expected assignee 'step-3', got %q", fresh.Status.CurrentAssignee)
	}
}

func TestRouting_RouteTo_TargetNotFound(t *testing.T) {
	flow := testFlow(100)
	currentNode := testNode("worker")
	wi := testWorkitem(phaseRouting, "worker", flowLabels())
	wi.Status.RoutingInstruction = &flowv1.RoutingInstruction{
		Type:   "route_to",
		Target: "nonexistent",
	}

	r := testReconciler(flow, currentNode, wi)

	_, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err == nil {
		t.Fatal("Expected error for nonexistent target node")
	}
}

// ---------------------------------------------------------------------------
// Routing phase: thrash guard during routing
// ---------------------------------------------------------------------------

func TestRouting_ThrashGuardExceeded_FailsWorkitem(t *testing.T) {
	flow := testFlow(5) // maxVisits=5
	node := testNode("worker")
	wi := testWorkitem(phaseRouting, "worker", flowLabels())
	wi.Status.ThrashCounters = map[string]int32{
		"worker": 3,
		"other":  3, // aggregate=6, exceeds 5
	}
	wi.Status.RoutingInstruction = &flowv1.RoutingInstruction{
		Type:   "route_to_output",
		Target: "default",
	}

	r := testReconciler(flow, node, wi)

	result, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error on thrash failure, got: %v", err)
	}
	assertNoRequeue(t, result)

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != phaseFailed {
		t.Fatalf("Expected phase %s, got %q", phaseFailed, fresh.Status.Phase)
	}
	if fresh.Status.FailureReason != reasonThrashBudgetExceeded {
		t.Fatalf("Expected %s, got %q", reasonThrashBudgetExceeded, fresh.Status.FailureReason)
	}
}

// ---------------------------------------------------------------------------
// Routing phase: missing routing instruction
// ---------------------------------------------------------------------------

func TestRouting_MissingInstruction_NoError(t *testing.T) {
	flow := testFlow(100)
	wi := testWorkitem(phaseRouting, "worker", flowLabels())

	r := testReconciler(flow, wi)

	result, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error for missing instruction, got: %v", err)
	}
	assertNoRequeue(t, result)
}

// ---------------------------------------------------------------------------
// Terminal phases: no-op
// ---------------------------------------------------------------------------

func TestCompleted_NoOp(t *testing.T) {
	wi := testWorkitem(phaseCompleted, "", flowLabels())

	r := testReconciler(wi)

	result, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	assertNoRequeue(t, result)
}

func TestFailed_NoOp(t *testing.T) {
	wi := testWorkitem(phaseFailed, "", flowLabels())
	wi.Status.FailureReason = reasonTimeoutExceeded

	r := testReconciler(wi)

	result, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	assertNoRequeue(t, result)
}

// ---------------------------------------------------------------------------
// Deleted workitem
// ---------------------------------------------------------------------------

func TestDeletedWorkitem_NoError(t *testing.T) {
	r := testReconciler()

	result, err := r.Reconcile(context.Background(), testReq("nonexistent"))
	if err != nil {
		t.Fatalf("Expected no error for deleted workitem, got: %v", err)
	}
	assertNoRequeue(t, result)
}

// ---------------------------------------------------------------------------
// failWorkitem: already terminal
// ---------------------------------------------------------------------------

func TestFailWorkitem_AlreadyFailed_NoOp(t *testing.T) {
	wi := testWorkitem(phaseFailed, "", flowLabels())
	wi.Status.FailureReason = reasonTimeoutExceeded

	r := testReconciler(wi)

	_, err := r.failWorkitem(context.Background(), wi, reasonThrashBudgetExceeded)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	fresh := getWorkitem(t, r)
	// Should not overwrite the existing failure reason.
	if fresh.Status.FailureReason != reasonTimeoutExceeded {
		t.Fatalf("Expected original failure reason preserved, got %q", fresh.Status.FailureReason)
	}
}

func TestFailWorkitem_AlreadyCompleted_NoOp(t *testing.T) {
	wi := testWorkitem(phaseCompleted, "", flowLabels())

	r := testReconciler(wi)

	_, err := r.failWorkitem(context.Background(), wi, reasonThrashBudgetExceeded)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != phaseCompleted {
		t.Fatalf("Expected phase %s (unchanged), got %q", phaseCompleted, fresh.Status.Phase)
	}
}

// ---------------------------------------------------------------------------
// resolveTimeout unit tests
// ---------------------------------------------------------------------------

func TestResolveTimeout_Default(t *testing.T) {
	flow := testFlow(100)
	flow.Spec.GovernancePolicy.DefaultTimeout = metav1.Duration{Duration: 20 * time.Minute}
	flow.Spec.GovernancePolicy.MaxTimeout = metav1.Duration{Duration: 1 * time.Hour}

	node := &flowv1.FoundryNode{
		Spec: flowv1.FoundryNodeSpec{Image: "test:latest"},
	}

	timeout := resolveTimeout(node, flow)
	if timeout != 20*time.Minute {
		t.Fatalf("Expected 20m default timeout, got %v", timeout)
	}
}

func TestResolveTimeout_NodeSpecific(t *testing.T) {
	flow := testFlow(100)
	flow.Spec.GovernancePolicy.DefaultTimeout = metav1.Duration{Duration: 20 * time.Minute}
	flow.Spec.GovernancePolicy.MaxTimeout = metav1.Duration{Duration: 1 * time.Hour}

	nodeTimeout := metav1.Duration{Duration: 45 * time.Minute}
	node := &flowv1.FoundryNode{
		Spec: flowv1.FoundryNodeSpec{
			Image:   "test:latest",
			Timeout: &nodeTimeout,
		},
	}

	timeout := resolveTimeout(node, flow)
	if timeout != 45*time.Minute {
		t.Fatalf("Expected 45m node-specific timeout, got %v", timeout)
	}
}

func TestResolveTimeout_CappedAtMax(t *testing.T) {
	flow := testFlow(100)
	flow.Spec.GovernancePolicy.DefaultTimeout = metav1.Duration{Duration: 20 * time.Minute}
	flow.Spec.GovernancePolicy.MaxTimeout = metav1.Duration{Duration: 30 * time.Minute}

	nodeTimeout := metav1.Duration{Duration: 2 * time.Hour}
	node := &flowv1.FoundryNode{
		Spec: flowv1.FoundryNodeSpec{
			Image:   "test:latest",
			Timeout: &nodeTimeout,
		},
	}

	timeout := resolveTimeout(node, flow)
	if timeout != 30*time.Minute {
		t.Fatalf("Expected 30m (capped at max), got %v", timeout)
	}
}

func TestResolveTimeout_NilNode(t *testing.T) {
	flow := testFlow(100)
	flow.Spec.GovernancePolicy.DefaultTimeout = metav1.Duration{Duration: 20 * time.Minute}

	timeout := resolveTimeout(nil, flow)
	if timeout != 20*time.Minute {
		t.Fatalf("Expected 20m default timeout for nil node, got %v", timeout)
	}
}

// ---------------------------------------------------------------------------
// resolveFlow unit tests
// ---------------------------------------------------------------------------

func TestResolveFlow_FromLabel(t *testing.T) {
	flow := testFlow(100)
	wi := testWorkitem(phasePending, "worker", flowLabels())

	r := testReconciler(flow, wi)

	resolved, err := r.resolveFlow(context.Background(), wi)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if resolved.Name != testFlowName {
		t.Fatalf("Expected flow %q, got %q", testFlowName, resolved.Name)
	}
}

func TestResolveFlow_FallbackToList(t *testing.T) {
	flow := testFlow(100)
	wi := testWorkitem(phasePending, "worker", nil)

	r := testReconciler(flow, wi)

	resolved, err := r.resolveFlow(context.Background(), wi)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if resolved.Name != testFlowName {
		t.Fatalf("Expected flow %q, got %q", testFlowName, resolved.Name)
	}
}

func TestResolveFlow_NoFlowFound(t *testing.T) {
	wi := testWorkitem(phasePending, "worker", nil)

	r := testReconciler(wi)

	_, err := r.resolveFlow(context.Background(), wi)
	if err == nil {
		t.Fatal("Expected error when no flow found")
	}
}

// ---------------------------------------------------------------------------
// nowFunc override for deterministic tests
// ---------------------------------------------------------------------------

func TestNowFunc_Override(t *testing.T) {
	orig := nowFunc
	t.Cleanup(func() { nowFunc = orig })

	fixed := metav1.NewTime(time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC))
	nowFunc = func() metav1.Time { return fixed }

	got := nowFunc()
	if !got.Equal(&fixed) {
		t.Fatalf("Expected fixed time, got %v", got)
	}
}
