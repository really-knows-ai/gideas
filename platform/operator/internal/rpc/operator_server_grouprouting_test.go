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

func TestSubmitResult_GroupRoutingDenied_RouteToInternalGroupNode(t *testing.T) {
	scheme := newScheme()

	// Flow with a NodeGroup containing an internal (non-entry-bound) node.
	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {}},
			ExitContracts:    map[string]apiv1.Contract{},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
			NodeGroups: map[string]apiv1.NodeGroup{
				"codification": {
					Nodes: []string{"codify-internal"},
				},
			},
		},
	}

	// The workitem is on an external node (outside the group).
	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wi-external",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "external-node",
		},
	}

	// The target node is inside the group but not entry-bound.
	internalNode := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "codify-internal", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image: "codify:latest",
			// No Entry binding — not entry-bound.
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, workitem, internalNode).
		WithStatusSubresource(workitem).
		Build()

	srv := NewOperatorServer(k8s)

	// Create context with source node identity.
	md := metadata.Pairs(
		"x-flow-node-id", "external-node",
		"x-flow-namespace", "default",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.SubmitResult(ctx, &flowv1.SubmitResultRequest{
		WorkitemId: "wi-external",
		Action:     &flowv1.SubmitResultRequest_Route{Route: &flowv1.RouteAction{Target: "codify-internal"}},
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), "GROUP_ROUTING_DENIED") {
		t.Fatalf("Expected GROUP_ROUTING_DENIED error, got: %v", err)
	}
}

func TestSubmitResult_GroupRoutingAllowed_RouteToEntryBoundGroupNode(t *testing.T) {
	scheme := newScheme()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {}},
			ExitContracts:    map[string]apiv1.Contract{},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
			NodeGroups: map[string]apiv1.NodeGroup{
				"codification": {
					Nodes: []string{"codify-entry"},
				},
			},
		},
	}

	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wi-external",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "external-node",
		},
	}

	// The target is inside the group and IS entry-bound.
	entryNode := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "codify-entry", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image: "codify:latest",
			Entry: "main",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, workitem, entryNode).
		WithStatusSubresource(workitem).
		Build()

	srv := NewOperatorServer(k8s)

	md := metadata.Pairs("x-flow-node-id", "external-node", "x-flow-namespace", "default")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := srv.SubmitResult(ctx, &flowv1.SubmitResultRequest{
		WorkitemId: "wi-external",
		Action:     &flowv1.SubmitResultRequest_Route{Route: &flowv1.RouteAction{Target: "codify-entry"}},
	})
	if err != nil {
		t.Fatalf("Expected route to entry-bound group node to succeed, got: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}
}

func TestSubmitResult_GroupRoutingAllowed_IntraGroupRouting(t *testing.T) {
	scheme := newScheme()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {}},
			ExitContracts:    map[string]apiv1.Contract{},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
			NodeGroups: map[string]apiv1.NodeGroup{
				"codification": {
					Nodes: []string{"codify-a", "codify-b"},
				},
			},
		},
	}

	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wi-internal",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "codify-a",
		},
	}

	nodeA := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "codify-a", Namespace: "default"},
		Spec:       apiv1.FoundryNodeSpec{Image: "codify-a:latest"},
	}

	nodeB := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "codify-b", Namespace: "default"},
		Spec:       apiv1.FoundryNodeSpec{Image: "codify-b:latest"},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, workitem, nodeA, nodeB).
		WithStatusSubresource(workitem).
		Build()

	srv := NewOperatorServer(k8s)

	// Source node is codify-a, target is codify-b — same group.
	md := metadata.Pairs("x-flow-node-id", "codify-a", "x-flow-namespace", "default")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := srv.SubmitResult(ctx, &flowv1.SubmitResultRequest{
		WorkitemId: "wi-internal",
		Action:     &flowv1.SubmitResultRequest_Route{Route: &flowv1.RouteAction{Target: "codify-b"}},
	})
	if err != nil {
		t.Fatalf("Expected intra-group routing to succeed, got: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}
}

func TestSubmitResult_GroupRoutingAllowed_NoGroups(t *testing.T) {
	scheme := newScheme()

	// Flow without any NodeGroups — routing should be unrestricted.
	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {}},
			ExitContracts:    map[string]apiv1.Contract{},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
		},
	}

	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wi-free",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "node-a",
		},
	}

	nodeB := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "node-b", Namespace: "default"},
		Spec:       apiv1.FoundryNodeSpec{Image: "node-b:latest"},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, workitem, nodeB).
		WithStatusSubresource(workitem).
		Build()

	srv := NewOperatorServer(k8s)

	md := metadata.Pairs("x-flow-node-id", "node-a", "x-flow-namespace", "default")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := srv.SubmitResult(ctx, &flowv1.SubmitResultRequest{
		WorkitemId: "wi-free",
		Action:     &flowv1.SubmitResultRequest_Route{Route: &flowv1.RouteAction{Target: "node-b"}},
	})
	if err != nil {
		t.Fatalf("Expected routing without groups to succeed, got: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}
}

func TestRouteChild_GroupRoutingDenied(t *testing.T) {
	scheme := newScheme()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {}},
			ExitContracts:    map[string]apiv1.Contract{},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
			NodeGroups: map[string]apiv1.NodeGroup{
				"codification": {
					Nodes: []string{"codify-internal"},
				},
			},
		},
	}

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

	// Internal node is not entry-bound.
	internalNode := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "codify-internal", Namespace: "default"},
		Spec:       apiv1.FoundryNodeSpec{Image: "codify:latest"},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, child, internalNode).
		WithStatusSubresource(child).
		Build()

	srv := NewOperatorServer(k8s)

	// Source is external-clerk which is NOT in the codification group.
	md := metadata.Pairs(
		"x-flow-workitem-id", "parent-wi",
		"x-flow-node-id", "external-clerk",
		"x-flow-namespace", "default",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: "child-wi",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO,
			Target: "codify-internal",
		},
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), "GROUP_ROUTING_DENIED") {
		t.Fatalf("Expected GROUP_ROUTING_DENIED error, got: %v", err)
	}
}
