package rpc

import (
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	apiv1 "github.com/foundry/flow/operator/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestListSuspendedWorkitems_ReturnsSuspendedWithMatchingCondition(t *testing.T) {
	scheme := newScheme()

	wi1 := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{Name: "wi-held-001", Namespace: "default"},
		Status: apiv1.WorkitemStatus{
			Phase:           "Suspended",
			ResumeCondition: `dispute_retired("pet-123")`,
		},
	}
	wi2 := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{Name: "wi-held-002", Namespace: "default"},
		Status: apiv1.WorkitemStatus{
			Phase:           "Suspended",
			ResumeCondition: `dispute_retired("pet-123")`,
		},
	}
	// This one should NOT match — different petition_id.
	wi3 := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{Name: "wi-other", Namespace: "default"},
		Status: apiv1.WorkitemStatus{
			Phase:           "Suspended",
			ResumeCondition: `dispute_retired("pet-999")`,
		},
	}
	// This one should NOT match — not Suspended.
	wi4 := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{Name: "wi-running", Namespace: "default"},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			ResumeCondition: `dispute_retired("pet-123")`,
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(wi1, wi2, wi3, wi4).
		Build()

	srv := NewOperatorServer(k8s)

	resp, err := srv.ListSuspendedWorkitems(nsCtx(), &flowv1.ListSuspendedWorkitemsRequest{
		ConditionContains: "pet-123",
	})
	if err != nil {
		t.Fatalf("ListSuspendedWorkitems() returned error: %v", err)
	}

	if len(resp.GetWorkitems()) != 2 {
		t.Fatalf("expected 2 workitems, got %d", len(resp.GetWorkitems()))
	}

	ids := make(map[string]bool)
	for _, wi := range resp.GetWorkitems() {
		ids[wi.GetWorkitemId()] = true
	}
	if !ids["wi-held-001"] || !ids["wi-held-002"] {
		t.Fatalf("expected wi-held-001 and wi-held-002, got %v", ids)
	}
}

func TestListSuspendedWorkitems_EmptyFilter_ReturnsError(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	_, err := srv.ListSuspendedWorkitems(nsCtx(), &flowv1.ListSuspendedWorkitemsRequest{
		ConditionContains: "",
	})
	if err == nil {
		t.Fatal("expected error for empty condition_contains, got nil")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestListSuspendedWorkitems_NoMatches_ReturnsEmptyList(t *testing.T) {
	scheme := newScheme()

	wi1 := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{Name: "wi-suspended", Namespace: "default"},
		Status: apiv1.WorkitemStatus{
			Phase:           "Suspended",
			ResumeCondition: `dispute_retired("pet-999")`,
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(wi1).
		Build()

	srv := NewOperatorServer(k8s)

	resp, err := srv.ListSuspendedWorkitems(nsCtx(), &flowv1.ListSuspendedWorkitemsRequest{
		ConditionContains: "pet-123",
	})
	if err != nil {
		t.Fatalf("ListSuspendedWorkitems() returned error: %v", err)
	}
	if len(resp.GetWorkitems()) != 0 {
		t.Fatalf("expected 0 workitems, got %d", len(resp.GetWorkitems()))
	}
}
