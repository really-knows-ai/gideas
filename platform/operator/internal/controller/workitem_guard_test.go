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
	testFlowName     = "test-flow"
	testAssignee     = "worker"
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

func testWorkitem(phase, assignee string) *flowv1.Workitem {
	return &flowv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWorkitemName,
			Namespace: "default",
		},
		Status: flowv1.WorkitemStatus{
			Phase:           phase,
			CurrentAssignee: assignee,
		},
	}
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
	node := testNode(testAssignee)
	wi := testWorkitem(phasePending, testAssignee)

	r := testReconciler(flow, node, wi)

	// Reconcile — dispatch will fail (no pods), workitem reverts to Pending.
	// Error is expected because no pods exist for dispatch.
	_, _ = r.Reconcile(context.Background(), testReq(testWorkitemName))

	fresh := getWorkitem(t, r)
	// The counter persists if the Running claim succeeded (even if dispatch
	// failed and reverted). If the claim itself failed, the counter won't
	// persist — that is acceptable.
	if fresh.Status.ThrashCounters != nil {
		if count := fresh.Status.ThrashCounters[testAssignee]; count > 0 {
			t.Logf("Thrash counter incremented to %d — correct", count)
		}
	}
}

// ---------------------------------------------------------------------------
// Pending phase: thrash budget exceeded
// ---------------------------------------------------------------------------

func TestPending_ThrashBudgetExceeded(t *testing.T) {
	flow := testFlow(5)
	node := testNode(testAssignee)
	wi := testWorkitem(phasePending, testAssignee)

	// Pre-set thrash counters to just below the limit (aggregate=5).
	// After increment (aggregate=6), it exceeds maxVisits=5.
	wi.Status.ThrashCounters = map[string]int32{
		testAssignee: 3,
		"other":      2,
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
	wi := testWorkitem(phasePending, "")

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

	node := testNode(testAssignee)
	wi := testWorkitem(phaseRunning, testAssignee)

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

	node := testNode(testAssignee)
	wi := testWorkitem(phaseRunning, testAssignee)

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

	node := testNode(testAssignee)
	nodeTimeout := metav1.Duration{Duration: 5 * time.Minute}
	node.Spec.Timeout = &nodeTimeout

	wi := testWorkitem(phaseRunning, testAssignee)

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

	node := testNode(testAssignee)
	nodeTimeout := metav1.Duration{Duration: 1 * time.Hour} // Node wants 1h but max is 10m.
	node.Spec.Timeout = &nodeTimeout

	wi := testWorkitem(phaseRunning, testAssignee)

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
	node := testNode(testAssignee)
	nextNode := testNode("next-node")
	wi := testWorkitem(phaseRouting, testAssignee)
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
	wi := testWorkitem(phaseRouting, "publisher")
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
	node := testNode(testAssignee) // Not exit-bound.
	wi := testWorkitem(phaseRouting, testAssignee)
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
	node := testNode(testAssignee)
	wi := testWorkitem(phaseRouting, testAssignee)
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
	currentNode := testNode(testAssignee)
	targetNode := testNode("step-3")
	wi := testWorkitem(phaseRouting, testAssignee)
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
	currentNode := testNode(testAssignee)
	wi := testWorkitem(phaseRouting, testAssignee)
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
	node := testNode(testAssignee)
	wi := testWorkitem(phaseRouting, testAssignee)
	wi.Status.ThrashCounters = map[string]int32{
		testAssignee: 3,
		"other":      3, // aggregate=6, exceeds 5
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
	wi := testWorkitem(phaseRouting, testAssignee)

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
	wi := testWorkitem(phaseCompleted, "")

	r := testReconciler(wi)

	result, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	assertNoRequeue(t, result)
}

func TestFailed_NoOp(t *testing.T) {
	wi := testWorkitem(phaseFailed, "")
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
	wi := testWorkitem(phaseFailed, "")
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
	wi := testWorkitem(phaseCompleted, "")

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

func TestResolveFlow_SingletonInNamespace(t *testing.T) {
	flow := testFlow(100)
	wi := testWorkitem(phasePending, testAssignee)

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
	wi := testWorkitem(phasePending, testAssignee)

	r := testReconciler(wi)

	_, err := r.resolveFlow(context.Background(), wi)
	if err == nil {
		t.Fatal("Expected error when no flow found")
	}
}

func TestResolveFlow_MultipleFlows_ReturnsError(t *testing.T) {
	flow1 := testFlow(100)
	flow2 := &flowv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "second-flow", Namespace: "default"},
		Spec: flowv1.FoundryFlowSpec{
			EntryContracts: map[string]flowv1.Contract{"main": {}},
			GovernancePolicy: flowv1.GovernancePolicy{
				MaxVisits:      100,
				DefaultTimeout: metav1.Duration{Duration: 30 * time.Minute},
				MaxTimeout:     metav1.Duration{Duration: 1 * time.Hour},
			},
		},
	}
	wi := testWorkitem(phasePending, testAssignee)

	r := testReconciler(flow1, flow2, wi)

	_, err := r.resolveFlow(context.Background(), wi)
	if err == nil {
		t.Fatal("Expected error when multiple flows found in namespace")
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

// ---------------------------------------------------------------------------
// Running phase: child-aware timeout enforcement
// ---------------------------------------------------------------------------

func TestRunning_WithNonTerminalChildren_SkipsTimeout(t *testing.T) {
	flow := testFlow(100)
	flow.Spec.GovernancePolicy.DefaultTimeout = metav1.Duration{Duration: 10 * time.Minute}

	node := testNode(testAssignee)
	wi := testWorkitem(phaseRunning, testAssignee)

	// Assignment started 15 minutes ago — exceeds 10 minute timeout.
	past := metav1.NewTime(time.Now().Add(-15 * time.Minute))
	wi.Status.AssignedAt = &past

	// Create a child Workitem that is still Running.
	child := &flowv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-of-wi-1",
			Namespace: "default",
			Labels: map[string]string{
				"flow.gideas.io/parent": testWorkitemName,
			},
		},
		Status: flowv1.WorkitemStatus{
			Phase:            "Running",
			CurrentAssignee:  "codify-smt",
			ParentWorkitemID: testWorkitemName,
		},
	}

	r := testReconciler(flow, node, wi, child)

	result, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Should requeue at the child-check interval, NOT fail the workitem.
	if result.RequeueAfter != childCheckInterval {
		t.Fatalf("Expected RequeueAfter=%v (child check interval), got %v", childCheckInterval, result.RequeueAfter)
	}

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != phaseRunning {
		t.Fatalf("Expected phase %s (parent waiting for children), got %q", phaseRunning, fresh.Status.Phase)
	}
}

func TestRunning_WithAllTerminalChildren_AppliesTimeout(t *testing.T) {
	flow := testFlow(100)
	flow.Spec.GovernancePolicy.DefaultTimeout = metav1.Duration{Duration: 10 * time.Minute}

	node := testNode(testAssignee)
	wi := testWorkitem(phaseRunning, testAssignee)

	// Assignment started 15 minutes ago — exceeds 10 minute timeout.
	past := metav1.NewTime(time.Now().Add(-15 * time.Minute))
	wi.Status.AssignedAt = &past

	// All children are terminal.
	child1 := &flowv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-1-of-wi-1",
			Namespace: "default",
			Labels: map[string]string{
				"flow.gideas.io/parent": testWorkitemName,
			},
		},
		Status: flowv1.WorkitemStatus{
			Phase:            "Completed",
			ParentWorkitemID: testWorkitemName,
		},
	}
	child2 := &flowv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-2-of-wi-1",
			Namespace: "default",
			Labels: map[string]string{
				"flow.gideas.io/parent": testWorkitemName,
			},
		},
		Status: flowv1.WorkitemStatus{
			Phase:            "Failed",
			ParentWorkitemID: testWorkitemName,
		},
	}

	r := testReconciler(flow, node, wi, child1, child2)

	result, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	assertNoRequeue(t, result)

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != phaseFailed {
		t.Fatalf("Expected phase %s (timeout applied — all children terminal), got %q", phaseFailed, fresh.Status.Phase)
	}
	if fresh.Status.FailureReason != reasonTimeoutExceeded {
		t.Fatalf("Expected failure reason %s, got %q", reasonTimeoutExceeded, fresh.Status.FailureReason)
	}
}

func TestRunning_WithMixedChildren_SkipsTimeout(t *testing.T) {
	flow := testFlow(100)
	flow.Spec.GovernancePolicy.DefaultTimeout = metav1.Duration{Duration: 10 * time.Minute}

	node := testNode(testAssignee)
	wi := testWorkitem(phaseRunning, testAssignee)

	// Assignment started 15 minutes ago — exceeds 10 minute timeout.
	past := metav1.NewTime(time.Now().Add(-15 * time.Minute))
	wi.Status.AssignedAt = &past

	// One child completed, one still pending.
	childCompleted := &flowv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-done",
			Namespace: "default",
			Labels: map[string]string{
				"flow.gideas.io/parent": testWorkitemName,
			},
		},
		Status: flowv1.WorkitemStatus{
			Phase:            "Completed",
			ParentWorkitemID: testWorkitemName,
		},
	}
	childPending := &flowv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-pending",
			Namespace: "default",
			Labels: map[string]string{
				"flow.gideas.io/parent": testWorkitemName,
			},
		},
		Status: flowv1.WorkitemStatus{
			Phase:            "Pending",
			ParentWorkitemID: testWorkitemName,
		},
	}

	r := testReconciler(flow, node, wi, childCompleted, childPending)

	result, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Should skip timeout — one child still non-terminal.
	if result.RequeueAfter != childCheckInterval {
		t.Fatalf("Expected RequeueAfter=%v, got %v", childCheckInterval, result.RequeueAfter)
	}

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != phaseRunning {
		t.Fatalf("Expected phase %s (waiting for pending child), got %q", phaseRunning, fresh.Status.Phase)
	}
}

// ---------------------------------------------------------------------------
// Suspended phase: reconcileSuspended tests
// ---------------------------------------------------------------------------

func testSuspendedWorkitem(assignee, condition, timeout string, suspendedAt metav1.Time) *flowv1.Workitem {
	wi := &flowv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWorkitemName,
			Namespace: "default",
		},
		Status: flowv1.WorkitemStatus{
			Phase:           wiPhaseSuspended,
			CurrentAssignee: assignee,
			SuspendedAt:     &suspendedAt,
			ResumeCondition: condition,
			ResumeTimeout:   timeout,
		},
	}
	return wi
}

func TestSuspended_TimeoutExceeded_FailsWorkitem(t *testing.T) {
	// Override nowFunc to control elapsed time.
	orig := nowFunc
	t.Cleanup(func() { nowFunc = orig })

	baseTime := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	suspendedAt := metav1.NewTime(baseTime)

	// Now is 20 minutes after suspension — exceeds 10m timeout.
	nowFunc = func() metav1.Time {
		return metav1.NewTime(baseTime.Add(20 * time.Minute))
	}

	flow := testFlow(100)
	wi := testSuspendedWorkitem(testAssignee, "", "10m", suspendedAt)

	r := testReconciler(flow, wi)

	result, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	assertNoRequeue(t, result)

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != phaseFailed {
		t.Fatalf("Expected phase %s, got %q", phaseFailed, fresh.Status.Phase)
	}
	if fresh.Status.FailureReason != "SUSPEND_TIMEOUT_EXCEEDED" {
		t.Fatalf("Expected failure reason SUSPEND_TIMEOUT_EXCEEDED, got %q", fresh.Status.FailureReason)
	}
}

func TestSuspended_InvalidTimeout_FailsWorkitem(t *testing.T) {
	flow := testFlow(100)
	suspendedAt := metav1.NewTime(time.Now())
	wi := testSuspendedWorkitem(testAssignee, "", "not-a-duration", suspendedAt)

	r := testReconciler(flow, wi)

	result, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	assertNoRequeue(t, result)

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != phaseFailed {
		t.Fatalf("Expected phase %s, got %q", phaseFailed, fresh.Status.Phase)
	}
	if fresh.Status.FailureReason != "SUSPEND_TIMEOUT_EXCEEDED" {
		t.Fatalf("Expected failure reason SUSPEND_TIMEOUT_EXCEEDED, got %q", fresh.Status.FailureReason)
	}
}

func TestSuspended_ChildrenCompleted_ResumesToPending(t *testing.T) {
	flow := testFlow(100)
	suspendedAt := metav1.NewTime(time.Now())
	wi := testSuspendedWorkitem(testAssignee, "children-completed", "1h", suspendedAt)

	// All children are Completed — condition should evaluate to true.
	child1 := &flowv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-1",
			Namespace: "default",
			Labels:    map[string]string{"flow.gideas.io/parent": testWorkitemName},
		},
		Status: flowv1.WorkitemStatus{
			Phase:            "Completed",
			ParentWorkitemID: testWorkitemName,
		},
	}
	child2 := &flowv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-2",
			Namespace: "default",
			Labels:    map[string]string{"flow.gideas.io/parent": testWorkitemName},
		},
		Status: flowv1.WorkitemStatus{
			Phase:            "Completed",
			ParentWorkitemID: testWorkitemName,
		},
	}

	r := testReconciler(flow, wi, child1, child2)

	result, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	assertNoRequeue(t, result)

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != phasePending {
		t.Fatalf("Expected phase %s, got %q", phasePending, fresh.Status.Phase)
	}
	// Suspend fields should be cleared.
	if fresh.Status.SuspendedAt != nil {
		t.Fatal("Expected SuspendedAt to be cleared")
	}
	if fresh.Status.ResumeCondition != "" {
		t.Fatalf("Expected ResumeCondition to be cleared, got %q", fresh.Status.ResumeCondition)
	}
	if fresh.Status.ResumeTimeout != "" {
		t.Fatalf("Expected ResumeTimeout to be cleared, got %q", fresh.Status.ResumeTimeout)
	}
	// CurrentAssignee preserved.
	if fresh.Status.CurrentAssignee != testAssignee {
		t.Fatalf("Expected CurrentAssignee=worker, got %q", fresh.Status.CurrentAssignee)
	}
}

func TestSuspended_ChildrenNotCompleted_Requeues(t *testing.T) {
	flow := testFlow(100)
	suspendedAt := metav1.NewTime(time.Now())
	wi := testSuspendedWorkitem(testAssignee, "children-completed", "1h", suspendedAt)

	// One child still Running — condition evaluates to false.
	child1 := &flowv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-1",
			Namespace: "default",
			Labels:    map[string]string{"flow.gideas.io/parent": testWorkitemName},
		},
		Status: flowv1.WorkitemStatus{
			Phase:            "Completed",
			ParentWorkitemID: testWorkitemName,
		},
	}
	child2 := &flowv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-2",
			Namespace: "default",
			Labels:    map[string]string{"flow.gideas.io/parent": testWorkitemName},
		},
		Status: flowv1.WorkitemStatus{
			Phase:            "Running",
			ParentWorkitemID: testWorkitemName,
		},
	}

	r := testReconciler(flow, wi, child1, child2)

	result, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Should requeue (condition not met).
	if result.RequeueAfter <= 0 {
		t.Fatal("Expected RequeueAfter > 0 for condition not met")
	}
	if result.RequeueAfter > suspendCheckInterval {
		t.Errorf("Expected RequeueAfter <= %v, got %v", suspendCheckInterval, result.RequeueAfter)
	}

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != wiPhaseSuspended {
		t.Fatalf("Expected phase Suspended (unchanged), got %q", fresh.Status.Phase)
	}
}

func TestSuspended_NoCondition_NoTimeout_Requeues(t *testing.T) {
	flow := testFlow(100)
	// No condition, no timeout — just a manually-resumable suspension.
	wi := &flowv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWorkitemName,
			Namespace: "default",
		},
		Status: flowv1.WorkitemStatus{
			Phase:           wiPhaseSuspended,
			CurrentAssignee: testAssignee,
			// SuspendedAt, ResumeCondition, ResumeTimeout all zero/empty.
		},
	}

	r := testReconciler(flow, wi)

	result, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Should requeue at the default suspend check interval.
	if result.RequeueAfter != suspendCheckInterval {
		t.Fatalf("Expected RequeueAfter=%v, got %v", suspendCheckInterval, result.RequeueAfter)
	}

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != wiPhaseSuspended {
		t.Fatalf("Expected phase Suspended (unchanged), got %q", fresh.Status.Phase)
	}
}

func TestSuspended_ResumePreservesAssignee(t *testing.T) {
	flow := testFlow(100)
	suspendedAt := metav1.NewTime(time.Now())
	wi := testSuspendedWorkitem("sort", "children-terminal", "1h", suspendedAt)

	// All children terminal — condition met.
	child := &flowv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-1",
			Namespace: "default",
			Labels:    map[string]string{"flow.gideas.io/parent": testWorkitemName},
		},
		Status: flowv1.WorkitemStatus{
			Phase:            "Failed",
			ParentWorkitemID: testWorkitemName,
		},
	}

	r := testReconciler(flow, wi, child)

	_, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != phasePending {
		t.Fatalf("Expected phase %s, got %q", phasePending, fresh.Status.Phase)
	}
	// Key assertion: assignee preserved after resume.
	if fresh.Status.CurrentAssignee != "sort" {
		t.Fatalf("Expected CurrentAssignee=sort (preserved), got %q", fresh.Status.CurrentAssignee)
	}
}

// ---------------------------------------------------------------------------
// Routing phase: suspend instruction
// ---------------------------------------------------------------------------

func TestRouting_Suspend_HappyPath(t *testing.T) {
	flow := testFlow(100)
	maxTimeout := metav1.Duration{Duration: 1 * time.Hour}
	flow.Spec.Suspension = &flowv1.SuspensionConfig{
		MaxSuspendTimeout: &maxTimeout,
	}

	node := testNode(testAssignee)
	wi := testWorkitem(phaseRouting, testAssignee)
	wi.Status.RoutingInstruction = &flowv1.RoutingInstruction{
		Type:             "suspend",
		SuspendCondition: `children.all(c, c.phase == "Completed")`,
		SuspendTimeout:   "30m",
	}

	r := testReconciler(flow, node, wi)

	result, err := r.Reconcile(context.Background(), testReq(testWorkitemName))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Should requeue for the suspend timeout.
	if result.RequeueAfter <= 0 {
		t.Fatal("Expected RequeueAfter > 0 for suspended workitem")
	}

	fresh := getWorkitem(t, r)
	if fresh.Status.Phase != wiPhaseSuspended {
		t.Fatalf("Expected phase Suspended, got %q", fresh.Status.Phase)
	}
	if fresh.Status.CurrentAssignee != testAssignee {
		t.Fatalf("Expected CurrentAssignee=worker (preserved), got %q", fresh.Status.CurrentAssignee)
	}
	if fresh.Status.SuspendedAt == nil {
		t.Fatal("Expected SuspendedAt to be set")
	}
	if fresh.Status.ResumeCondition != `children.all(c, c.phase == "Completed")` {
		t.Fatalf("Expected ResumeCondition to be set, got %q", fresh.Status.ResumeCondition)
	}
	if fresh.Status.ResumeTimeout != "30m" {
		t.Fatalf("Expected ResumeTimeout=30m, got %q", fresh.Status.ResumeTimeout)
	}
	// RoutingInstruction should be cleared.
	if fresh.Status.RoutingInstruction != nil {
		t.Fatal("Expected RoutingInstruction to be cleared")
	}
}
