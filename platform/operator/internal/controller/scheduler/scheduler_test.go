package scheduler

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/operator/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---------------------------------------------------------------------------
// Routing instruction tests (existing, adapted for new signature)
// ---------------------------------------------------------------------------

func TestRouteToOutput_DefaultTarget(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "step-1", Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image:   "alpine:latest",
			Outputs: []flowv1.Output{{Name: "default", Target: "step-2"}},
		},
	}
	sched := newTestScheduler(node)
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, nil)

	result, err := sched.CalculateNextStep(
		context.Background(),
		"step-1",
		flowv1.RoutingInstruction{Type: "route_to_output", Target: ""},
		wi, flow,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NextAssignee != "step-2" {
		t.Errorf("expected NextAssignee=step-2, got %q", result.NextAssignee)
	}
	if result.Phase != phasePending {
		t.Errorf("expected Phase=Pending, got %q", result.Phase)
	}
}

func TestRouteToOutput_NamedTarget(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "review", Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image: "alpine:latest",
			Outputs: []flowv1.Output{
				{Name: "approved", Target: "publish"},
				{Name: "rejected", Target: "revision"},
			},
		},
	}
	sched := newTestScheduler(node)
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, nil)

	result, err := sched.CalculateNextStep(
		context.Background(),
		"review",
		flowv1.RoutingInstruction{Type: "route_to_output", Target: "rejected"},
		wi, flow,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NextAssignee != "revision" {
		t.Errorf("expected NextAssignee=revision, got %q", result.NextAssignee)
	}
	if result.Phase != phasePending {
		t.Errorf("expected Phase=Pending, got %q", result.Phase)
	}
}

func TestRouteToOutput_UnknownOutput(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "step-1", Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image:   "alpine:latest",
			Outputs: []flowv1.Output{{Name: "default", Target: "step-2"}},
		},
	}
	sched := newTestScheduler(node)
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, nil)

	_, err := sched.CalculateNextStep(
		context.Background(),
		"step-1",
		flowv1.RoutingInstruction{Type: "route_to_output", Target: "nonexistent"},
		wi, flow,
	)
	if err == nil {
		t.Fatal("expected error for unknown output, got nil")
	}
	assertGuardCode(t, err, "INVALID_ROUTE")
}

func TestComplete_ExitBound(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "step-2", Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image: "alpine:latest",
			Exit:  "standard-exit",
		},
	}
	sched := newTestScheduler(node)
	wi := newTestWorkitem(nil)
	// Exit contract exists on flow but no Querier set — contract validation skipped.
	flow := newTestFlow(100, map[string]flowv1.Contract{"standard-exit": {}})

	result, err := sched.CalculateNextStep(
		context.Background(),
		"step-2",
		flowv1.RoutingInstruction{Type: "complete"},
		wi, flow,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NextAssignee != "" {
		t.Errorf("expected empty NextAssignee, got %q", result.NextAssignee)
	}
	if result.Phase != phaseCompleted {
		t.Errorf("expected Phase=Completed, got %q", result.Phase)
	}
}

func TestComplete_NotExitBound(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "step-1", Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image:   "alpine:latest",
			Outputs: []flowv1.Output{{Name: "default", Target: "step-2"}},
		},
	}
	sched := newTestScheduler(node)
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, nil)

	_, err := sched.CalculateNextStep(
		context.Background(),
		"step-1",
		flowv1.RoutingInstruction{Type: "complete"},
		wi, flow,
	)
	if err == nil {
		t.Fatal("expected error for complete on non-exit node, got nil")
	}
	assertGuardCode(t, err, "EXIT_NOT_BOUND")
}

func TestRouteTo_Direct(t *testing.T) {
	step1 := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "step-1", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "alpine:latest"},
	}
	step3 := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "step-3", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "alpine:latest"},
	}
	sched := newTestScheduler(step1, step3)
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, nil)

	result, err := sched.CalculateNextStep(
		context.Background(),
		"step-1",
		flowv1.RoutingInstruction{Type: "route_to", Target: "step-3"},
		wi, flow,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NextAssignee != "step-3" {
		t.Errorf("expected NextAssignee=step-3, got %q", result.NextAssignee)
	}
	if result.Phase != phasePending {
		t.Errorf("expected Phase=Pending, got %q", result.Phase)
	}
}

func TestRouteTo_EmptyTarget(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "step-1", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "alpine:latest"},
	}
	sched := newTestScheduler(node)
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, nil)

	_, err := sched.CalculateNextStep(
		context.Background(),
		"step-1",
		flowv1.RoutingInstruction{Type: "route_to", Target: ""},
		wi, flow,
	)
	if err == nil {
		t.Fatal("expected error for route_to with empty target, got nil")
	}
	assertGuardCode(t, err, "INVALID_ROUTE")
}

func TestRouteTo_TargetNodeNotFound(t *testing.T) {
	step1 := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "step-1", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "alpine:latest"},
	}
	// Only step-1 exists, not "nonexistent".
	sched := newTestScheduler(step1)
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, nil)

	_, err := sched.CalculateNextStep(
		context.Background(),
		"step-1",
		flowv1.RoutingInstruction{Type: "route_to", Target: "nonexistent"},
		wi, flow,
	)
	if err == nil {
		t.Fatal("expected error for nonexistent target node, got nil")
	}
	assertGuardCode(t, err, "INVALID_ROUTE")
}

func TestUnknownInstructionType(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "step-1", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "alpine:latest"},
	}
	sched := newTestScheduler(node)
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, nil)

	_, err := sched.CalculateNextStep(
		context.Background(),
		"step-1",
		flowv1.RoutingInstruction{Type: "teleport"},
		wi, flow,
	)
	if err == nil {
		t.Fatal("expected error for unknown instruction type, got nil")
	}
	assertGuardCode(t, err, "INVALID_ROUTE")
}

func TestNodeNotFound(t *testing.T) {
	// No nodes seeded.
	sched := newTestScheduler()

	_, err := sched.CalculateNextStep(
		context.Background(),
		"nonexistent",
		flowv1.RoutingInstruction{Type: "route_to_output", Target: "default"},
		nil, nil,
	)
	if err == nil {
		t.Fatal("expected error for missing node, got nil")
	}
}
