package rpc

import (
	"context"
	"strings"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	apiv1 "github.com/foundry/flow/operator/api/v1"
	"google.golang.org/grpc/codes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRouteChild_HappyPath(t *testing.T) {
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
			Labels: map[string]string{
				"flow.foundry.io/parent": "parent-wi",
			},
		},
		Status: apiv1.WorkitemStatus{
			Phase:            phasePending,
			ParentWorkitemID: "parent-wi",
		},
	}

	targetNode := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "codify-smt", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image: "codify-smt:latest",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(parent, child, targetNode).
		WithStatusSubresource(parent, child).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := workitemCtx("parent-wi")

	resp, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: "child-wi",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO,
			Target: "codify-smt",
		},
	})
	if err != nil {
		t.Fatalf("RouteChild() returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}

	// Verify the child was updated.
	var updated apiv1.Workitem
	err = k8s.Get(context.Background(), nsName("child-wi"), &updated)
	if err != nil {
		t.Fatalf("Failed to get updated child: %v", err)
	}
	if updated.Status.Phase != phaseRouting {
		t.Fatalf("Expected phase Routing, got %s", updated.Status.Phase)
	}
	if updated.Status.RoutingInstruction == nil {
		t.Fatal("Expected routing instruction to be set")
	}
	if updated.Status.RoutingInstruction.Type != riRouteTo {
		t.Fatalf("Expected routing type 'route_to', got %s", updated.Status.RoutingInstruction.Type)
	}
	if updated.Status.RoutingInstruction.Target != "codify-smt" {
		t.Fatalf("Expected target 'codify-smt', got %s", updated.Status.RoutingInstruction.Target)
	}
}

func TestRouteChild_ChildNotOwned(t *testing.T) {
	scheme := newScheme()

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:            phasePending,
			ParentWorkitemID: "other-parent",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(child).
		WithStatusSubresource(child).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := workitemCtx("my-parent")

	_, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: "child-wi",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO,
			Target: "some-node",
		},
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	if !strings.Contains(err.Error(), "CHILD_NOT_OWNED") {
		t.Fatalf("Expected CHILD_NOT_OWNED error, got: %v", err)
	}
}

func TestRouteChild_ChildAlreadyRouted(t *testing.T) {
	scheme := newScheme()

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:            "Running",
			ParentWorkitemID: "parent-wi",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(child).
		WithStatusSubresource(child).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := workitemCtx("parent-wi")

	_, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: "child-wi",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO,
			Target: "some-node",
		},
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), "CHILD_ALREADY_ROUTED") {
		t.Fatalf("Expected CHILD_ALREADY_ROUTED error, got: %v", err)
	}
}

func TestRouteChild_ChildNotFound(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	ctx := workitemCtx("parent-wi")

	_, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: "nonexistent-child",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO,
			Target: "some-node",
		},
	})
	assertGRPCCode(t, err, codes.NotFound)
}

func TestRouteChild_MissingChildID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	ctx := workitemCtx("parent-wi")

	_, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO,
			Target: "some-node",
		},
	})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestRouteChild_MissingParentID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	_, err := srv.RouteChild(context.Background(), &flowv1.RouteChildRequest{
		ChildWorkitemId: "child-wi",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO,
			Target: "some-node",
		},
	})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestRouteChild_MissingInstruction(t *testing.T) {
	scheme := newScheme()

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:            phasePending,
			ParentWorkitemID: "parent-wi",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(child).
		WithStatusSubresource(child).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := workitemCtx("parent-wi")

	_, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: "child-wi",
	})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestRouteChild_TargetNodeNotFound(t *testing.T) {
	scheme := newScheme()

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:            phasePending,
			ParentWorkitemID: "parent-wi",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(child).
		WithStatusSubresource(child).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := workitemCtx("parent-wi")

	_, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: "child-wi",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO,
			Target: "nonexistent-node",
		},
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), "INVALID_ROUTE") {
		t.Fatalf("Expected INVALID_ROUTE error, got: %v", err)
	}
}

func TestRouteChild_RouteToOutput_NoTargetValidation(t *testing.T) {
	// route_to_output does not validate target node existence (that's the
	// reconciler's job), but it does require a target.
	scheme := newScheme()

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:            phasePending,
			ParentWorkitemID: "parent-wi",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(child).
		WithStatusSubresource(child).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := workitemCtx("parent-wi")

	resp, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: "child-wi",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO_OUTPUT,
			Target: "review",
		},
	})
	if err != nil {
		t.Fatalf("RouteChild(route_to_output) returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}
}

func TestRouteChild_Complete(t *testing.T) {
	scheme := newScheme()

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:            phasePending,
			ParentWorkitemID: "parent-wi",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(child).
		WithStatusSubresource(child).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := workitemCtx("parent-wi")

	resp, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: "child-wi",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type: flowv1.RoutingType_ROUTING_TYPE_COMPLETE,
		},
	})
	if err != nil {
		t.Fatalf("RouteChild(complete) returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}

	// Verify the child was transitioned to Routing with complete instruction.
	var updated apiv1.Workitem
	err = k8s.Get(context.Background(), nsName("child-wi"), &updated)
	if err != nil {
		t.Fatalf("Failed to get updated child: %v", err)
	}
	if updated.Status.RoutingInstruction.Type != riComplete {
		t.Fatalf("Expected routing type 'complete', got %s", updated.Status.RoutingInstruction.Type)
	}
}
