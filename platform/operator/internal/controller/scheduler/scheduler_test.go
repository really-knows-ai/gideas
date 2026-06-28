package scheduler

import (
	"context"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/operator/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	phasePending   = "Pending"
	phaseCompleted = "Completed"
	phaseFailed    = "Failed"
	phaseSuspended = "Suspended"
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

// newTestWorkitem creates a minimal Workitem for testing.
func newTestWorkitem(counters map[string]int32) *flowv1.Workitem {
	return &flowv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{Name: "wi-1", Namespace: "default"},
		Status: flowv1.WorkitemStatus{
			ThrashCounters: counters,
		},
	}
}

// newTestFlow creates a minimal FoundryFlow for testing.
func newTestFlow(maxVisits int32, exitContracts map[string]flowv1.Contract) *flowv1.FoundryFlow {
	return &flowv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: flowv1.FoundryFlowSpec{
			EntryContracts: map[string]flowv1.Contract{"main": {}},
			ExitContracts:  exitContracts,
			GovernancePolicy: flowv1.GovernancePolicy{
				MaxVisits: maxVisits,
			},
		},
	}
}

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

// ---------------------------------------------------------------------------
// Exit contract validation tests
// ---------------------------------------------------------------------------

func TestExitContract_Satisfied(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "exit-node", Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image: "alpine:latest",
			Exit:  "standard-exit",
		},
	}
	sched := newTestScheduler(node)
	sched.Querier = func(_ context.Context, _ string, _ []string) ([]ArtefactState, error) {
		return []ArtefactState{
			{ArtefactID: "art-1", GovernedArtefact: "haiku", StampNames: []string{"linter", "review", "approval"}},
		}, nil
	}
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, map[string]flowv1.Contract{
		"standard-exit": {"haiku": {"linter", "review", "approval"}},
	})

	result, err := sched.CalculateNextStep(
		context.Background(),
		"exit-node",
		flowv1.RoutingInstruction{Type: "complete"},
		wi, flow,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Phase != phaseCompleted {
		t.Errorf("expected Phase=Completed, got %q", result.Phase)
	}
}

func TestExitContract_MissingStamp(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "exit-node", Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image: "alpine:latest",
			Exit:  "standard-exit",
		},
	}
	sched := newTestScheduler(node)
	sched.Querier = func(_ context.Context, _ string, _ []string) ([]ArtefactState, error) {
		return []ArtefactState{
			{ArtefactID: "art-1", GovernedArtefact: "haiku", StampNames: []string{"linter"}},
		}, nil
	}
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, map[string]flowv1.Contract{
		"standard-exit": {"haiku": {"linter", "review", "approval"}},
	})

	_, err := sched.CalculateNextStep(
		context.Background(),
		"exit-node",
		flowv1.RoutingInstruction{Type: "complete"},
		wi, flow,
	)
	if err == nil {
		t.Fatal("expected CONTRACT_VIOLATION error, got nil")
	}
	assertGuardCode(t, err, "CONTRACT_VIOLATION")
}

func TestExitContract_MissingArtefact(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "exit-node", Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image: "alpine:latest",
			Exit:  "standard-exit",
		},
	}
	sched := newTestScheduler(node)
	sched.Querier = func(_ context.Context, _ string, _ []string) ([]ArtefactState, error) {
		return []ArtefactState{}, nil // No artefacts returned.
	}
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, map[string]flowv1.Contract{
		"standard-exit": {"haiku": {"linter"}},
	})

	_, err := sched.CalculateNextStep(
		context.Background(),
		"exit-node",
		flowv1.RoutingInstruction{Type: "complete"},
		wi, flow,
	)
	if err == nil {
		t.Fatal("expected CONTRACT_VIOLATION error, got nil")
	}
	assertGuardCode(t, err, "CONTRACT_VIOLATION")
}

func TestExitContract_MultipleArtefacts_AllMustSatisfy(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "exit-node", Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image: "alpine:latest",
			Exit:  "standard-exit",
		},
	}
	sched := newTestScheduler(node)
	sched.Querier = func(_ context.Context, _ string, _ []string) ([]ArtefactState, error) {
		return []ArtefactState{
			{ArtefactID: "art-1", GovernedArtefact: "haiku", StampNames: []string{"linter", "review"}},
			{ArtefactID: "art-2", GovernedArtefact: "haiku", StampNames: []string{"linter"}}, // Missing "review".
		}, nil
	}
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, map[string]flowv1.Contract{
		"standard-exit": {"haiku": {"linter", "review"}},
	})

	_, err := sched.CalculateNextStep(
		context.Background(),
		"exit-node",
		flowv1.RoutingInstruction{Type: "complete"},
		wi, flow,
	)
	if err == nil {
		t.Fatal("expected CONTRACT_VIOLATION error for second artefact, got nil")
	}
	assertGuardCode(t, err, "CONTRACT_VIOLATION")
}

func TestExitContract_EmptyContractPasses(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "exit-node", Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image: "alpine:latest",
			Exit:  "simple-exit",
		},
	}
	sched := newTestScheduler(node)
	sched.Querier = func(_ context.Context, _ string, _ []string) ([]ArtefactState, error) {
		return nil, nil
	}
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, map[string]flowv1.Contract{
		"simple-exit": {}, // Empty contract — no requirements.
	})

	result, err := sched.CalculateNextStep(
		context.Background(),
		"exit-node",
		flowv1.RoutingInstruction{Type: "complete"},
		wi, flow,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Phase != phaseCompleted {
		t.Errorf("expected Phase=Completed, got %q", result.Phase)
	}
}

func TestExitContract_ContractNotFoundOnFlow(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "exit-node", Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image: "alpine:latest",
			Exit:  "missing-contract",
		},
	}
	sched := newTestScheduler(node)
	sched.Querier = func(_ context.Context, _ string, _ []string) ([]ArtefactState, error) {
		return nil, nil
	}
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, map[string]flowv1.Contract{
		"standard-exit": {"haiku": {"linter"}}, // "missing-contract" not present.
	})

	_, err := sched.CalculateNextStep(
		context.Background(),
		"exit-node",
		flowv1.RoutingInstruction{Type: "complete"},
		wi, flow,
	)
	if err == nil {
		t.Fatal("expected CONTRACT_VIOLATION error, got nil")
	}
	assertGuardCode(t, err, "CONTRACT_VIOLATION")
}

// ---------------------------------------------------------------------------
// Suspend instruction tests
// ---------------------------------------------------------------------------

func TestSuspend_ExplicitTimeout(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "alpine:latest"},
	}
	sched := newTestScheduler(node)

	flow := newTestFlow(100, nil)
	maxTimeout := metav1.Duration{Duration: 1 * time.Hour}
	flow.Spec.Suspension = &flowv1.SuspensionConfig{
		MaxSuspendTimeout: &maxTimeout,
	}

	wi := newTestWorkitem(nil)

	result, err := sched.CalculateNextStep(
		context.Background(),
		"worker",
		flowv1.RoutingInstruction{Type: "suspend", SuspendTimeout: "30m"},
		wi, flow,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Phase != phaseSuspended {
		t.Errorf("expected Phase=Suspended, got %q", result.Phase)
	}
	if result.NextAssignee != "" {
		t.Errorf("expected empty NextAssignee, got %q", result.NextAssignee)
	}
	if result.SuspendTimeout != "30m" {
		t.Errorf("expected SuspendTimeout=30m, got %q", result.SuspendTimeout)
	}
}

func TestSuspend_FlowDefaultTimeout(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "alpine:latest"},
	}
	sched := newTestScheduler(node)

	flow := newTestFlow(100, nil)
	defaultTimeout := metav1.Duration{Duration: 15 * time.Minute}
	maxTimeout := metav1.Duration{Duration: 1 * time.Hour}
	flow.Spec.Suspension = &flowv1.SuspensionConfig{
		DefaultSuspendTimeout: &defaultTimeout,
		MaxSuspendTimeout:     &maxTimeout,
	}

	wi := newTestWorkitem(nil)

	result, err := sched.CalculateNextStep(
		context.Background(),
		"worker",
		flowv1.RoutingInstruction{Type: "suspend"},
		wi, flow,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Phase != phaseSuspended {
		t.Errorf("expected Phase=Suspended, got %q", result.Phase)
	}
	// Should use the default timeout from flow config.
	if result.SuspendTimeout != (15 * time.Minute).String() {
		t.Errorf("expected SuspendTimeout=%s, got %q", (15 * time.Minute).String(), result.SuspendTimeout)
	}
}

func TestSuspend_FallbackToMaxWhenNoDefault(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "alpine:latest"},
	}
	sched := newTestScheduler(node)

	flow := newTestFlow(100, nil)
	maxTimeout := metav1.Duration{Duration: 45 * time.Minute}
	flow.Spec.Suspension = &flowv1.SuspensionConfig{
		MaxSuspendTimeout: &maxTimeout,
		// No DefaultSuspendTimeout — should fall back to max.
	}

	wi := newTestWorkitem(nil)

	result, err := sched.CalculateNextStep(
		context.Background(),
		"worker",
		flowv1.RoutingInstruction{Type: "suspend"},
		wi, flow,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuspendTimeout != (45 * time.Minute).String() {
		t.Errorf("expected SuspendTimeout=%s (fallback to max), got %q",
			(45 * time.Minute).String(), result.SuspendTimeout)
	}
}

func TestSuspend_TimeoutExceedsMax(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "alpine:latest"},
	}
	sched := newTestScheduler(node)

	flow := newTestFlow(100, nil)
	maxTimeout := metav1.Duration{Duration: 10 * time.Minute}
	flow.Spec.Suspension = &flowv1.SuspensionConfig{
		MaxSuspendTimeout: &maxTimeout,
	}

	wi := newTestWorkitem(nil)

	_, err := sched.CalculateNextStep(
		context.Background(),
		"worker",
		flowv1.RoutingInstruction{Type: "suspend", SuspendTimeout: "1h"},
		wi, flow,
	)
	if err == nil {
		t.Fatal("expected SUSPEND_TIMEOUT_EXCEEDED error, got nil")
	}
	assertGuardCode(t, err, "SUSPEND_TIMEOUT_EXCEEDED")
}

func TestSuspend_InvalidTimeoutString(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "alpine:latest"},
	}
	sched := newTestScheduler(node)

	flow := newTestFlow(100, nil)
	maxTimeout := metav1.Duration{Duration: 1 * time.Hour}
	flow.Spec.Suspension = &flowv1.SuspensionConfig{
		MaxSuspendTimeout: &maxTimeout,
	}

	wi := newTestWorkitem(nil)

	_, err := sched.CalculateNextStep(
		context.Background(),
		"worker",
		flowv1.RoutingInstruction{Type: "suspend", SuspendTimeout: "not-a-duration"},
		wi, flow,
	)
	if err == nil {
		t.Fatal("expected INVALID_SUSPEND error, got nil")
	}
	assertGuardCode(t, err, "INVALID_SUSPEND")
}

func TestSuspend_ConditionPassthrough(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "alpine:latest"},
	}
	sched := newTestScheduler(node)
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, nil)

	condition := `children.all(c, c.phase == "Completed")`

	result, err := sched.CalculateNextStep(
		context.Background(),
		"worker",
		flowv1.RoutingInstruction{Type: "suspend", SuspendCondition: condition},
		wi, flow,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuspendCondition != condition {
		t.Errorf("expected condition passthrough, got %q", result.SuspendCondition)
	}
	if result.Phase != phaseSuspended {
		t.Errorf("expected Phase=Suspended, got %q", result.Phase)
	}
	// No suspension config — timeout should be empty.
	if result.SuspendTimeout != "" {
		t.Errorf("expected empty timeout (no SuspensionConfig), got %q", result.SuspendTimeout)
	}
}

func TestSuspend_NoSuspensionConfig_NoTimeout(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "alpine:latest"},
	}
	sched := newTestScheduler(node)
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, nil)
	// flow.Spec.Suspension is nil — no timeout applied.

	result, err := sched.CalculateNextStep(
		context.Background(),
		"worker",
		flowv1.RoutingInstruction{Type: "suspend", SuspendTimeout: "10m"},
		wi, flow,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Phase != phaseSuspended {
		t.Errorf("expected Phase=Suspended, got %q", result.Phase)
	}
	// Timeout is passed through even without SuspensionConfig (no validation).
	if result.SuspendTimeout != "10m" {
		t.Errorf("expected SuspendTimeout=10m, got %q", result.SuspendTimeout)
	}
}

func TestSuspend_NilFlowAndWorkitem(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "alpine:latest"},
	}
	sched := newTestScheduler(node)

	// nil flow and workitem should not panic (backward compat).
	result, err := sched.CalculateNextStep(
		context.Background(),
		"worker",
		flowv1.RoutingInstruction{Type: "suspend", SuspendTimeout: "5m"},
		nil, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Phase != phaseSuspended {
		t.Errorf("expected Phase=Suspended, got %q", result.Phase)
	}
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

// assertGuardCode checks that the error is a GuardError with the expected code.
func assertGuardCode(t *testing.T, err error, expectedCode string) {
	t.Helper()
	ge, ok := err.(*GuardError)
	if !ok {
		t.Fatalf("expected *GuardError, got %T: %v", err, err)
	}
	if ge.Code != expectedCode {
		t.Fatalf("expected guard code %q, got %q: %s", expectedCode, ge.Code, ge.Message)
	}
}
