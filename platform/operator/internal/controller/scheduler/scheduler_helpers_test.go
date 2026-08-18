package scheduler

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/operator/api/v1"
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

// mockLawQuerier implements LawQuerier for testing.
type mockLawQuerier struct {
	laws   map[string][]LawInfo // governedArtefact → laws
	groups []LawGroupInfo
	err    error // if set, all queries return this error
}

func (m *mockLawQuerier) QueryLaws(_ context.Context, governedArtefact string) ([]LawInfo, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.laws[governedArtefact], nil
}

func (m *mockLawQuerier) ListLawGroups(_ context.Context) ([]LawGroupInfo, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.groups, nil
}

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
