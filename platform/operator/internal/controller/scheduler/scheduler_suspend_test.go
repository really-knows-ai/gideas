package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/operator/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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

func TestComplete_ArchivistUnavailable(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "exit-node", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "alpine:latest", Exit: "standard-exit"},
	}
	sched := newTestScheduler(node)
	sched.Querier = func(_ context.Context, _ string, _ []string) ([]ArtefactState, error) {
		return nil, errors.New("archivist unreachable")
	}
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, map[string]flowv1.Contract{"standard-exit": {"haiku": {"appraisal"}}})

	_, err := sched.CalculateNextStep(context.Background(), "exit-node", flowv1.RoutingInstruction{Type: "complete"}, wi, flow)
	if err == nil {
		t.Fatal("expected error for Archivist unavailability, got nil")
	}
}
