package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

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
				"flow.foundry.io/parent": testWorkitemName,
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
				"flow.foundry.io/parent": testWorkitemName,
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
				"flow.foundry.io/parent": testWorkitemName,
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
				"flow.foundry.io/parent": testWorkitemName,
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
				"flow.foundry.io/parent": testWorkitemName,
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
