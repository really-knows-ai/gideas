package rpc

import (
	"strings"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	apiv1 "github.com/foundry/flow/operator/api/v1"
	"google.golang.org/grpc/codes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSubmitResult_CompletionGuard_ChildrenPending(t *testing.T) {
	scheme := newScheme()

	parent := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "parent-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "clerk",
		},
	}

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
			Labels:    map[string]string{"flow.foundry.io/parent": "parent-wi"},
		},
		Status: apiv1.WorkitemStatus{
			Phase:            phasePending,
			ParentWorkitemID: "parent-wi",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(parent, child).
		WithStatusSubresource(parent, child).
		Build()

	srv := NewOperatorServer(k8s)

	_, err := srv.SubmitResult(nsCtx(), &flowv1.SubmitResultRequest{
		WorkitemId: "parent-wi",
		Action:     &flowv1.SubmitResultRequest_Complete{Complete: &flowv1.CompleteAction{}},
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), "CHILDREN_NOT_TERMINAL") {
		t.Fatalf("Expected CHILDREN_NOT_TERMINAL error, got: %v", err)
	}
}

func TestSubmitResult_CompletionGuard_ChildrenRunning(t *testing.T) {
	scheme := newScheme()

	parent := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "parent-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "clerk",
		},
	}

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
			Labels:    map[string]string{"flow.foundry.io/parent": "parent-wi"},
		},
		Status: apiv1.WorkitemStatus{
			Phase:            "Running",
			ParentWorkitemID: "parent-wi",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(parent, child).
		WithStatusSubresource(parent, child).
		Build()

	srv := NewOperatorServer(k8s)

	_, err := srv.SubmitResult(nsCtx(), &flowv1.SubmitResultRequest{
		WorkitemId: "parent-wi",
		Action:     &flowv1.SubmitResultRequest_Complete{Complete: &flowv1.CompleteAction{}},
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), "CHILDREN_NOT_TERMINAL") {
		t.Fatalf("Expected CHILDREN_NOT_TERMINAL error, got: %v", err)
	}
}

func TestSubmitResult_CompletionGuard_AllChildrenCompleted(t *testing.T) {
	scheme := newScheme()

	parent := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "parent-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "clerk",
		},
	}

	child1 := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-1",
			Namespace: "default",
			Labels:    map[string]string{"flow.foundry.io/parent": "parent-wi"},
		},
		Status: apiv1.WorkitemStatus{
			Phase:            "Completed",
			ParentWorkitemID: "parent-wi",
		},
	}

	child2 := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-2",
			Namespace: "default",
			Labels:    map[string]string{"flow.foundry.io/parent": "parent-wi"},
		},
		Status: apiv1.WorkitemStatus{
			Phase:            "Failed",
			ParentWorkitemID: "parent-wi",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(parent, child1, child2).
		WithStatusSubresource(parent, child1, child2).
		Build()

	srv := NewOperatorServer(k8s)

	resp, err := srv.SubmitResult(nsCtx(), &flowv1.SubmitResultRequest{
		WorkitemId: "parent-wi",
		Action:     &flowv1.SubmitResultRequest_Complete{Complete: &flowv1.CompleteAction{}},
	})
	if err != nil {
		t.Fatalf("Expected completion to succeed when all children are terminal, got: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}
}

func TestSubmitResult_CompletionGuard_NoChildren(t *testing.T) {
	scheme := newScheme()

	parent := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "parent-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "clerk",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(parent).
		WithStatusSubresource(parent).
		Build()

	srv := NewOperatorServer(k8s)

	resp, err := srv.SubmitResult(nsCtx(), &flowv1.SubmitResultRequest{
		WorkitemId: "parent-wi",
		Action:     &flowv1.SubmitResultRequest_Complete{Complete: &flowv1.CompleteAction{}},
	})
	if err != nil {
		t.Fatalf("Expected completion to succeed with no children, got: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}
}

func TestSubmitResult_CompletionGuard_NonCompleteSkipsCheck(t *testing.T) {
	scheme := newScheme()

	parent := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "parent-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "clerk",
		},
	}

	// Non-terminal child that would block completion.
	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
			Labels:    map[string]string{"flow.foundry.io/parent": "parent-wi"},
		},
		Status: apiv1.WorkitemStatus{
			Phase:            "Running",
			ParentWorkitemID: "parent-wi",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(parent, child).
		WithStatusSubresource(parent, child).
		Build()

	srv := NewOperatorServer(k8s)

	// route_to_output does NOT trigger the completion guard.
	resp, err := srv.SubmitResult(nsCtx(), &flowv1.SubmitResultRequest{
		WorkitemId: "parent-wi",
		Action:     &flowv1.SubmitResultRequest_Route{Route: &flowv1.RouteAction{Target: "review", Output: true}},
	})
	if err != nil {
		t.Fatalf("Expected route_to_output to succeed despite non-terminal child, got: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}
}
