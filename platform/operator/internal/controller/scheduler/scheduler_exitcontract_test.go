package scheduler

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/operator/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
			{ArtefactID: "art-1", GovernedArtefact: "haiku", StampNames: []string{"review", "approval"}},
		}, nil
	}
	sched.LawQuerier = &mockLawQuerier{}
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, map[string]flowv1.Contract{
		"standard-exit": {"haiku": {"review", "approval"}},
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
			{ArtefactID: "art-1", GovernedArtefact: "haiku", StampNames: []string{"review"}},
		}, nil
	}
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, map[string]flowv1.Contract{
		"standard-exit": {"haiku": {"review", "approval"}},
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
		"standard-exit": {"haiku": {"review"}},
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
			{ArtefactID: "art-1", GovernedArtefact: "haiku", StampNames: []string{"review"}},
			{ArtefactID: "art-2", GovernedArtefact: "haiku", StampNames: []string{}}, // Missing "review".
		}, nil
	}
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, map[string]flowv1.Contract{
		"standard-exit": {"haiku": {"review"}},
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
		"standard-exit": {"haiku": {"review"}}, // "missing-contract" not present.
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
