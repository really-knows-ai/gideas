package rpc

import (
	"context"
	"strings"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	apiv1 "github.com/foundry/flow/operator/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCreateChildWorkitem_HappyPath(t *testing.T) {
	fixedTime(t)
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
		WithStatusSubresource(parent, &apiv1.Workitem{}).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := childCtx("default", "clerk", "parent-wi")

	resp, err := srv.CreateChildWorkitem(ctx, &flowv1.CreateChildWorkitemRequest{})
	if err != nil {
		t.Fatalf("CreateChildWorkitem() returned error: %v", err)
	}

	if resp.GetChildWorkitemId() == "" {
		t.Fatal("Expected non-empty child_workitem_id")
	}
	if !strings.HasPrefix(resp.GetChildWorkitemId(), "child-parent-wi-") {
		t.Fatalf("Expected prefix 'child-parent-wi-', got %s", resp.GetChildWorkitemId())
	}

	// Verify the CRD was created.
	var child apiv1.Workitem
	err = k8s.Get(context.Background(), nsName(resp.GetChildWorkitemId()), &child)
	if err != nil {
		t.Fatalf("Failed to get created child workitem: %v", err)
	}
	if child.Status.Phase != phasePending {
		t.Fatalf("Expected phase Pending, got %s", child.Status.Phase)
	}
	if child.Status.ParentWorkitemID != "parent-wi" {
		t.Fatalf("Expected ParentWorkitemID 'parent-wi', got %s", child.Status.ParentWorkitemID)
	}

	// Verify labels — no flow.foundry.io/flow label.
	if child.Labels["flow.foundry.io/parent"] != "parent-wi" {
		t.Fatalf("Expected parent label 'parent-wi', got %s", child.Labels["flow.foundry.io/parent"])
	}
	if _, hasFlowLabel := child.Labels["flow.foundry.io/flow"]; hasFlowLabel {
		t.Fatal("Expected no flow.foundry.io/flow label on child workitem")
	}
	if child.Labels["flow.foundry.io/creator"] != "clerk" {
		t.Fatalf("Expected creator label 'clerk', got %s", child.Labels["flow.foundry.io/creator"])
	}
}

func TestCreateChildWorkitem_CapabilityDenied(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	// Node call without CREATE:workitem/child capability.
	md := metadata.Pairs(
		"x-flow-namespace", "default",
		"x-flow-node-id", "node-1",
		"x-flow-workitem-id", "wi-1",
		"x-flow-capabilities", "READ:flow,WRITE:artefact",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.CreateChildWorkitem(ctx, &flowv1.CreateChildWorkitemRequest{})
	assertGRPCCode(t, err, codes.PermissionDenied)
}

func TestCreateChildWorkitem_MissingWorkitemID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	// Has capability but no workitem_id.
	md := metadata.Pairs(
		"x-flow-namespace", "default",
		"x-flow-node-id", "node-1",
		"x-flow-capabilities", "CREATE:workitem/child",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.CreateChildWorkitem(ctx, &flowv1.CreateChildWorkitemRequest{})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestCreateChildWorkitem_MissingNamespace(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	md := metadata.Pairs(
		"x-flow-node-id", "node-1",
		"x-flow-workitem-id", "wi-1",
		"x-flow-capabilities", "CREATE:workitem/child",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.CreateChildWorkitem(ctx, &flowv1.CreateChildWorkitemRequest{})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestCreateChildWorkitem_MissingNodeID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	md := metadata.Pairs(
		"x-flow-namespace", "default",
		"x-flow-workitem-id", "wi-1",
		"x-flow-capabilities", "CREATE:workitem/child",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.CreateChildWorkitem(ctx, &flowv1.CreateChildWorkitemRequest{})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestCreateChildWorkitem_ParentNotFound(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	ctx := childCtx("default", "clerk", "nonexistent-parent")

	_, err := srv.CreateChildWorkitem(ctx, &flowv1.CreateChildWorkitemRequest{})
	assertGRPCCode(t, err, codes.NotFound)
}
