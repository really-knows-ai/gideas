package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	"google.golang.org/grpc"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	flowv1gen "github.com/foundry/flow/gen/flow/v1"
	flowv1 "github.com/foundry/flow/operator/api/v1"
)

const (
	phasePending   = "Pending"
	phaseRunning   = "Running"
	phaseRouting   = "Routing"
	phaseCompleted = "Completed"
	// phaseFailed is already declared in foundrynode_controller.go (same package).

	reasonThrashBudgetExceeded = "THRASH_BUDGET_EXCEEDED"
	reasonTimeoutExceeded      = "TIMEOUT_EXCEEDED"

	testWorkitemName = "wi-1"
	testFlowName     = "test-flow"
	testAssignee     = "worker"
)

// noopLibrarianClient returns empty results for law queries. Used by tests
// that exercise the complete path without testing law attestation.
type noopLibrarianClient struct{}

func (n *noopLibrarianClient) QueryLaws(_ context.Context, _ *flowv1gen.QueryLawsRequest, _ ...grpc.CallOption) (*flowv1gen.QueryLawsResponse, error) {
	return &flowv1gen.QueryLawsResponse{}, nil
}

func (n *noopLibrarianClient) ListLawGroups(_ context.Context, _ *flowv1gen.ListLawGroupsRequest, _ ...grpc.CallOption) (*flowv1gen.ListLawGroupsResponse, error) {
	return &flowv1gen.ListLawGroupsResponse{}, nil
}

// Satisfy the rest of the LibrarianServiceClient interface (unused).
func (n *noopLibrarianClient) SyncLawGroup(_ context.Context, _ *flowv1gen.SyncLawGroupRequest, _ ...grpc.CallOption) (*flowv1gen.SyncLawGroupResponse, error) {
	return nil, fmt.Errorf("unexpected call: SyncLawGroup")
}
func (n *noopLibrarianClient) DeleteLawGroup(_ context.Context, _ *flowv1gen.DeleteLawGroupRequest, _ ...grpc.CallOption) (*flowv1gen.DeleteLawGroupResponse, error) {
	return nil, fmt.Errorf("unexpected call: DeleteLawGroup")
}
func (n *noopLibrarianClient) Cite(_ context.Context, _ *flowv1gen.CiteRequest, _ ...grpc.CallOption) (*flowv1gen.CiteResponse, error) {
	return nil, fmt.Errorf("unexpected call: Cite")
}
func (n *noopLibrarianClient) RecordFinding(_ context.Context, _ *flowv1gen.RecordFindingRequest, _ ...grpc.CallOption) (*flowv1gen.RecordFindingResponse, error) {
	return nil, fmt.Errorf("unexpected call: RecordFinding")
}
func (n *noopLibrarianClient) GetLaw(_ context.Context, _ *flowv1gen.GetLawRequest, _ ...grpc.CallOption) (*flowv1gen.GetLawResponse, error) {
	return nil, fmt.Errorf("unexpected call: GetLaw")
}
func (n *noopLibrarianClient) WriteLaw(_ context.Context, _ *flowv1gen.WriteLawRequest, _ ...grpc.CallOption) (*flowv1gen.WriteLawResponse, error) {
	return nil, fmt.Errorf("unexpected call: WriteLaw")
}
func (n *noopLibrarianClient) RetireLaw(_ context.Context, _ *flowv1gen.RetireLawRequest, _ ...grpc.CallOption) (*flowv1gen.RetireLawResponse, error) {
	return nil, fmt.Errorf("unexpected call: RetireLaw")
}
func (n *noopLibrarianClient) ReplicateLaws(_ context.Context, _ *flowv1gen.ReplicateLawsRequest, _ ...grpc.CallOption) (*flowv1gen.ReplicateLawsResponse, error) {
	return nil, fmt.Errorf("unexpected call: ReplicateLaws")
}
func (n *noopLibrarianClient) ApplyLifecycleAction(_ context.Context, _ *flowv1gen.ApplyLifecycleActionRequest, _ ...grpc.CallOption) (*flowv1gen.ApplyLifecycleActionResponse, error) {
	return nil, fmt.Errorf("unexpected call: ApplyLifecycleAction")
}
func (n *noopLibrarianClient) CreateDisputeRecord(_ context.Context, _ *flowv1gen.CreateDisputeRecordRequest, _ ...grpc.CallOption) (*flowv1gen.CreateDisputeRecordResponse, error) {
	return nil, fmt.Errorf("unexpected call: CreateDisputeRecord")
}
func (n *noopLibrarianClient) RetireDisputeRecord(_ context.Context, _ *flowv1gen.RetireDisputeRecordRequest, _ ...grpc.CallOption) (*flowv1gen.RetireDisputeRecordResponse, error) {
	return nil, fmt.Errorf("unexpected call: RetireDisputeRecord")
}
func (n *noopLibrarianClient) GetActiveDisputes(_ context.Context, _ *flowv1gen.GetActiveDisputesRequest, _ ...grpc.CallOption) (*flowv1gen.GetActiveDisputesResponse, error) {
	return nil, fmt.Errorf("unexpected call: GetActiveDisputes")
}
func (n *noopLibrarianClient) SearchSimilarLaws(_ context.Context, _ *flowv1gen.SearchSimilarLawsRequest, _ ...grpc.CallOption) (*flowv1gen.SearchSimilarLawsResponse, error) {
	return nil, fmt.Errorf("unexpected call: SearchSimilarLaws")
}
func (n *noopLibrarianClient) GetLawGroup(_ context.Context, _ *flowv1gen.GetLawGroupRequest, _ ...grpc.CallOption) (*flowv1gen.GetLawGroupResponse, error) {
	return nil, fmt.Errorf("unexpected call: GetLawGroup")
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = flowv1.AddToScheme(s)
	return s
}

func testReconciler(objs ...client.Object) *WorkitemReconciler {
	scheme := testScheme()
	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&flowv1.Workitem{})

	for _, obj := range objs {
		builder = builder.WithObjects(obj)
	}

	return &WorkitemReconciler{
		Client: builder.Build(),
		Scheme: scheme,
	}
}

func testReq(name string) reconcile.Request {
	return reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: name},
	}
}

func testFlow(maxVisits int32) *flowv1.FoundryFlow {
	return &flowv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: testFlowName, Namespace: "default"},
		Spec: flowv1.FoundryFlowSpec{
			EntryContracts: map[string]flowv1.Contract{"main": {}},
			ExitContracts: map[string]flowv1.Contract{
				"standard-exit": {"haiku": {"review"}},
			},
			GovernancePolicy: flowv1.GovernancePolicy{
				MaxVisits:      maxVisits,
				DefaultTimeout: metav1.Duration{Duration: 30 * time.Minute},
				MaxTimeout:     metav1.Duration{Duration: 1 * time.Hour},
			},
		},
	}
}

func testNode(name string) *flowv1.FoundryNode {
	return &flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image: name + ":latest",
			Outputs: []flowv1.Output{
				{Name: "default", Target: "next-node"},
			},
		},
	}
}

func testExitNode(name, exitContract string) *flowv1.FoundryNode {
	return &flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: flowv1.FoundryNodeSpec{
			Image: name + ":latest",
			Exit:  exitContract,
		},
	}
}

func testWorkitem(phase, assignee string) *flowv1.Workitem {
	return &flowv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWorkitemName,
			Namespace: "default",
		},
		Status: flowv1.WorkitemStatus{
			Phase:           phase,
			CurrentAssignee: assignee,
		},
	}
}

// getWorkitem fetches a fresh copy of the workitem from the fake client.
func getWorkitem(t *testing.T, r *WorkitemReconciler) *flowv1.Workitem {
	t.Helper()
	var wi flowv1.Workitem
	err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: testWorkitemName}, &wi)
	if err != nil {
		t.Fatalf("Failed to get workitem %q: %v", testWorkitemName, err)
	}
	return &wi
}

// assertNoRequeue checks that the result does not request a requeue.
func assertNoRequeue(t *testing.T, result reconcile.Result) {
	t.Helper()
	if result.RequeueAfter > 0 {
		t.Errorf("Expected no requeue, got RequeueAfter=%v", result.RequeueAfter)
	}
}
