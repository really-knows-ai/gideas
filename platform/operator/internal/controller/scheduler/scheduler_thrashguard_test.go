package scheduler

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/operator/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---------------------------------------------------------------------------
// Thrash guard tests
// ---------------------------------------------------------------------------

func TestThrashGuard_WithinBudget(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "step-1", Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image:   "alpine:latest",
			Outputs: []flowv1.Output{{Name: "default", Target: "step-2"}},
		},
	}
	sched := newTestScheduler(node)
	wi := newTestWorkitem(map[string]int32{"step-1": 3, "step-2": 2})
	flow := newTestFlow(10, nil) // maxVisits=10, aggregate=5 — within budget.

	result, err := sched.CalculateNextStep(
		context.Background(),
		"step-1",
		flowv1.RoutingInstruction{Type: "route_to_output", Target: "default"},
		wi, flow,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Phase != phasePending {
		t.Errorf("expected Phase=Pending, got %q", result.Phase)
	}
}

func TestThrashGuard_ExceedsBudget(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "step-1", Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image:   "alpine:latest",
			Outputs: []flowv1.Output{{Name: "default", Target: "step-2"}},
		},
	}
	sched := newTestScheduler(node)
	wi := newTestWorkitem(map[string]int32{"step-1": 5, "step-2": 6})
	flow := newTestFlow(10, nil) // maxVisits=10, aggregate=11 — exceeds budget.

	_, err := sched.CalculateNextStep(
		context.Background(),
		"step-1",
		flowv1.RoutingInstruction{Type: "route_to_output", Target: "default"},
		wi, flow,
	)
	if err == nil {
		t.Fatal("expected THRASH_BUDGET_EXCEEDED error, got nil")
	}
	assertGuardCode(t, err, "THRASH_BUDGET_EXCEEDED")
}

func TestThrashGuard_ExactlyAtBudget(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "step-1", Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image:   "alpine:latest",
			Outputs: []flowv1.Output{{Name: "default", Target: "step-2"}},
		},
	}
	sched := newTestScheduler(node)
	wi := newTestWorkitem(map[string]int32{"step-1": 5, "step-2": 5})
	flow := newTestFlow(10, nil) // maxVisits=10, aggregate=10 — exactly at budget.

	_, err := sched.CalculateNextStep(
		context.Background(),
		"step-1",
		flowv1.RoutingInstruction{Type: "route_to_output", Target: "default"},
		wi, flow,
	)
	if err == nil {
		t.Fatal("expected THRASH_BUDGET_EXCEEDED error when aggregate equals maxVisits, got nil")
	}
	assertGuardCode(t, err, "THRASH_BUDGET_EXCEEDED")
}

func TestThrashGuard_NilWorkitemSkipsCheck(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "step-1", Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image:   "alpine:latest",
			Outputs: []flowv1.Output{{Name: "default", Target: "step-2"}},
		},
	}
	sched := newTestScheduler(node)

	// nil workitem and flow should not panic.
	result, err := sched.CalculateNextStep(
		context.Background(),
		"step-1",
		flowv1.RoutingInstruction{Type: "route_to_output", Target: "default"},
		nil, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Phase != phasePending {
		t.Errorf("expected Phase=Pending, got %q", result.Phase)
	}
}
