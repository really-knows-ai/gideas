package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

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
