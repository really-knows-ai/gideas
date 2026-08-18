package rpc

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	apiv1 "github.com/foundry/flow/operator/api/v1"
	"google.golang.org/grpc/codes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetChildren_HappyPath(t *testing.T) {
	scheme := newScheme()

	child1 := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-1",
			Namespace: "default",
			Labels:    map[string]string{"flow.foundry.io/parent": "parent-wi"},
		},
		Status: apiv1.WorkitemStatus{
			Phase:            "Running",
			CurrentAssignee:  "codify-smt",
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
			Phase:            "Completed",
			ParentWorkitemID: "parent-wi",
			CompletionReason: "COMPLETION_REASON_CANCELLED",
		},
	}

	// Unrelated workitem (different parent).
	unrelated := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unrelated-wi",
			Namespace: "default",
			Labels:    map[string]string{"flow.foundry.io/parent": "other-parent"},
		},
		Status: apiv1.WorkitemStatus{
			Phase:            "Running",
			ParentWorkitemID: "other-parent",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(child1, child2, unrelated).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := workitemCtx("parent-wi")

	resp, err := srv.GetChildren(ctx, &flowv1.GetChildrenRequest{})
	if err != nil {
		t.Fatalf("GetChildren() returned error: %v", err)
	}

	if len(resp.GetChildren()) != 2 {
		t.Fatalf("Expected 2 children, got %d", len(resp.GetChildren()))
	}

	// Verify child data (order may vary with fake client).
	childMap := make(map[string]*flowv1.ChildWorkitemStatus)
	for _, c := range resp.GetChildren() {
		childMap[c.GetWorkitemId()] = c
	}

	c1, ok := childMap["child-1"]
	if !ok {
		t.Fatal("Expected child-1 in response")
	}
	if c1.GetPhase() != "Running" {
		t.Fatalf("Expected child-1 phase Running, got %s", c1.GetPhase())
	}
	if c1.GetCurrentAssignee() != "codify-smt" {
		t.Fatalf("Expected child-1 assignee 'codify-smt', got %s", c1.GetCurrentAssignee())
	}
	if c1.GetCompletionReason() != flowv1.CompletionReason_COMPLETION_REASON_UNSPECIFIED {
		t.Fatalf("Expected child-1 completion reason UNSPECIFIED, got %s", c1.GetCompletionReason())
	}

	c2, ok := childMap["child-2"]
	if !ok {
		t.Fatal("Expected child-2 in response")
	}
	if c2.GetPhase() != "Completed" {
		t.Fatalf("Expected child-2 phase Completed, got %s", c2.GetPhase())
	}
	if c2.GetCompletionReason() != flowv1.CompletionReason_COMPLETION_REASON_CANCELLED {
		t.Fatalf("Expected child-2 completion reason CANCELLED, got %s", c2.GetCompletionReason())
	}
}

func TestGetChildren_NoChildren(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	ctx := workitemCtx("parent-wi")

	resp, err := srv.GetChildren(ctx, &flowv1.GetChildrenRequest{})
	if err != nil {
		t.Fatalf("GetChildren() returned error: %v", err)
	}

	if len(resp.GetChildren()) != 0 {
		t.Fatalf("Expected 0 children, got %d", len(resp.GetChildren()))
	}
}

func TestGetChildren_MissingWorkitemID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	_, err := srv.GetChildren(context.Background(), &flowv1.GetChildrenRequest{})
	assertGRPCCode(t, err, codes.InvalidArgument)
}
