package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

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
			Labels:    map[string]string{"flow.foundry.io/parent": testWorkitemName},
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
			Labels:    map[string]string{"flow.foundry.io/parent": testWorkitemName},
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
			Labels:    map[string]string{"flow.foundry.io/parent": testWorkitemName},
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
			Labels:    map[string]string{"flow.foundry.io/parent": testWorkitemName},
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
			Labels:    map[string]string{"flow.foundry.io/parent": testWorkitemName},
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
