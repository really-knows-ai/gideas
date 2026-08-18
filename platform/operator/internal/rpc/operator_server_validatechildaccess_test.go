package rpc

import (
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	apiv1 "github.com/foundry/flow/operator/api/v1"
	"google.golang.org/grpc/codes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestValidateChildAccess_Valid(t *testing.T) {
	scheme := newScheme()

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
			Labels:    map[string]string{"flow.foundry.io/parent": "parent-wi"},
		},
		Status: apiv1.WorkitemStatus{
			Phase:            phaseCompleted,
			ParentWorkitemID: "parent-wi",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(child).
		WithStatusSubresource(child).
		Build()

	srv := NewOperatorServer(k8s)

	resp, err := srv.ValidateChildAccess(nsCtx(), &flowv1.ValidateChildAccessRequest{
		ParentWorkitemId: "parent-wi",
		ChildWorkitemId:  "child-wi",
	})
	if err != nil {
		t.Fatalf("ValidateChildAccess() returned error: %v", err)
	}
	if !resp.GetValid() {
		t.Fatal("Expected valid=true")
	}
	if resp.GetPhase() != phaseCompleted {
		t.Fatalf("Expected phase Completed, got %s", resp.GetPhase())
	}
}

func TestValidateChildAccess_WrongParent(t *testing.T) {
	scheme := newScheme()

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:            phaseCompleted,
			ParentWorkitemID: "actual-parent",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(child).
		WithStatusSubresource(child).
		Build()

	srv := NewOperatorServer(k8s)

	resp, err := srv.ValidateChildAccess(nsCtx(), &flowv1.ValidateChildAccessRequest{
		ParentWorkitemId: "wrong-parent",
		ChildWorkitemId:  "child-wi",
	})
	if err != nil {
		t.Fatalf("ValidateChildAccess() returned error: %v", err)
	}
	if resp.GetValid() {
		t.Fatal("Expected valid=false for wrong parent")
	}
	if resp.GetPhase() != phaseCompleted {
		t.Fatalf("Expected phase Completed, got %s", resp.GetPhase())
	}
}

func TestValidateChildAccess_ChildNotCompleted(t *testing.T) {
	phases := []string{"Pending", "Running", "Failed"}

	for _, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			scheme := newScheme()

			child := &apiv1.Workitem{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "child-wi",
					Namespace: "default",
				},
				Status: apiv1.WorkitemStatus{
					Phase:            phase,
					ParentWorkitemID: "parent-wi",
				},
			}

			k8s := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(child).
				WithStatusSubresource(child).
				Build()

			srv := NewOperatorServer(k8s)

			resp, err := srv.ValidateChildAccess(nsCtx(), &flowv1.ValidateChildAccessRequest{
				ParentWorkitemId: "parent-wi",
				ChildWorkitemId:  "child-wi",
			})
			if err != nil {
				t.Fatalf("ValidateChildAccess() returned error: %v", err)
			}
			if resp.GetValid() {
				t.Fatalf("Expected valid=false for phase %s", phase)
			}
			if resp.GetPhase() != phase {
				t.Fatalf("Expected phase %s, got %s", phase, resp.GetPhase())
			}
		})
	}
}

func TestValidateChildAccess_ChildNotFound(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	_, err := srv.ValidateChildAccess(nsCtx(), &flowv1.ValidateChildAccessRequest{
		ParentWorkitemId: "parent-wi",
		ChildWorkitemId:  "nonexistent-child",
	})
	assertGRPCCode(t, err, codes.NotFound)
}

func TestValidateChildAccess_MissingParentID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	_, err := srv.ValidateChildAccess(nsCtx(), &flowv1.ValidateChildAccessRequest{
		ChildWorkitemId: "child-wi",
	})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestValidateChildAccess_MissingChildID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	_, err := srv.ValidateChildAccess(nsCtx(), &flowv1.ValidateChildAccessRequest{
		ParentWorkitemId: "parent-wi",
	})
	assertGRPCCode(t, err, codes.InvalidArgument)
}
