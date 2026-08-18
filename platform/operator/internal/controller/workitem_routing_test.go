package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	flowv1 "github.com/foundry/flow/operator/api/v1"
	"github.com/foundry/flow/operator/internal/controller/scheduler"
)

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
	r.ArtefactQuerier = func(_ context.Context, _ string, _ []string) ([]scheduler.ArtefactState, error) {
		return []scheduler.ArtefactState{
			{ArtefactID: "art-1", GovernedArtefact: "haiku", StampNames: []string{"review"}},
		}, nil
	}
	r.Librarian = &noopLibrarianClient{}

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
