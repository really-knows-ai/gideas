package scheduler

import (
	"context"
	"testing"

	flowv1 "github.com/gideas/flow/operator/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	phasePending   = "Pending"
	phaseCompleted = "Completed"
)

// newTestScheduler builds a Scheduler backed by a fake client seeded with the
// given FoundryNode objects.
func newTestScheduler(nodes ...flowv1.FoundryNode) *Scheduler {
	scheme := runtime.NewScheme()
	_ = flowv1.AddToScheme(scheme)

	objs := make([]runtime.Object, len(nodes))
	for i := range nodes {
		objs[i] = &nodes[i]
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		Build()

	return New(cl, "default")
}

func TestRouteToOutput_DefaultTarget(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "step-1", Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image:   "alpine:latest",
			Outputs: []flowv1.Output{{Name: "default", Target: "step-2"}},
		},
	}
	sched := newTestScheduler(node)

	result, err := sched.CalculateNextStep(
		context.Background(),
		"step-1",
		flowv1.RoutingInstruction{Type: "route_to_output", Target: ""},
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

	result, err := sched.CalculateNextStep(
		context.Background(),
		"review",
		flowv1.RoutingInstruction{Type: "route_to_output", Target: "rejected"},
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

	_, err := sched.CalculateNextStep(
		context.Background(),
		"step-1",
		flowv1.RoutingInstruction{Type: "route_to_output", Target: "nonexistent"},
	)
	if err == nil {
		t.Fatal("expected error for unknown output, got nil")
	}
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

	result, err := sched.CalculateNextStep(
		context.Background(),
		"step-2",
		flowv1.RoutingInstruction{Type: "complete"},
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

	_, err := sched.CalculateNextStep(
		context.Background(),
		"step-1",
		flowv1.RoutingInstruction{Type: "complete"},
	)
	if err == nil {
		t.Fatal("expected error for complete on non-exit node, got nil")
	}
}

func TestRouteTo_Direct(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "step-1", Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image: "alpine:latest",
		},
	}
	sched := newTestScheduler(node)

	result, err := sched.CalculateNextStep(
		context.Background(),
		"step-1",
		flowv1.RoutingInstruction{Type: "route_to", Target: "step-3"},
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
		Spec: flowv1.FoundryNodeSpec{
			Image: "alpine:latest",
		},
	}
	sched := newTestScheduler(node)

	_, err := sched.CalculateNextStep(
		context.Background(),
		"step-1",
		flowv1.RoutingInstruction{Type: "route_to", Target: ""},
	)
	if err == nil {
		t.Fatal("expected error for route_to with empty target, got nil")
	}
}

func TestUnknownInstructionType(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "step-1", Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image: "alpine:latest",
		},
	}
	sched := newTestScheduler(node)

	_, err := sched.CalculateNextStep(
		context.Background(),
		"step-1",
		flowv1.RoutingInstruction{Type: "teleport"},
	)
	if err == nil {
		t.Fatal("expected error for unknown instruction type, got nil")
	}
}

func TestNodeNotFound(t *testing.T) {
	// No nodes seeded.
	sched := newTestScheduler()

	_, err := sched.CalculateNextStep(
		context.Background(),
		"nonexistent",
		flowv1.RoutingInstruction{Type: "route_to_output", Target: "default"},
	)
	if err == nil {
		t.Fatal("expected error for missing node, got nil")
	}
}
