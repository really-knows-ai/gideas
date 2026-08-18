package proxy

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/metadata"
)

// ---------------------------------------------------------------------------
// RouteChild proxy tests
// ---------------------------------------------------------------------------

func TestOperatorProxy_RouteChild_ForwardsAndReturns(t *testing.T) {
	proxy, capture, _ := setupOperatorProxyWithTracker(t)

	md := metadata.Pairs("x-flow-workitem-id", "parent-wi")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := proxy.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: childWI42,
		RoutingInstruction: &flowv1.RoutingInstruction{
			Target: "forge",
		},
	})
	if err != nil {
		t.Fatalf("RouteChild: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("expected accepted=true")
	}

	if capture.lastRouteReq.GetChildWorkitemId() != childWI42 {
		t.Fatalf("expected child_workitem_id=child-42 forwarded, got %q",
			capture.lastRouteReq.GetChildWorkitemId())
	}
}

func TestOperatorProxy_RouteChild_PropagatesMetadata(t *testing.T) {
	proxy, capture, _ := setupOperatorProxyWithTracker(t)

	md := metadata.Pairs(
		"x-flow-namespace", "ns-A",
		"x-flow-workitem-id", "parent-wi",
		"x-flow-node-id", "node-X",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: childWI42,
	})
	if err != nil {
		t.Fatalf("RouteChild: %v", err)
	}

	vals := capture.capturedMD.Get("x-flow-workitem-id")
	if len(vals) != 1 || vals[0] != "parent-wi" {
		t.Fatalf("expected x-flow-workitem-id=parent-wi, got %v", vals)
	}
}

// ---------------------------------------------------------------------------
// GetChildren proxy tests
// ---------------------------------------------------------------------------

func TestOperatorProxy_GetChildren_ForwardsAndReturns(t *testing.T) {
	proxy, _, _ := setupOperatorProxyWithTracker(t)

	md := metadata.Pairs("x-flow-workitem-id", "parent-wi")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := proxy.GetChildren(ctx, &flowv1.GetChildrenRequest{})
	if err != nil {
		t.Fatalf("GetChildren: %v", err)
	}

	if len(resp.GetChildren()) != 1 {
		t.Fatalf("expected 1 child, got %d", len(resp.GetChildren()))
	}
	child := resp.GetChildren()[0]
	if child.GetWorkitemId() != childWI42 {
		t.Fatalf("expected child workitem_id=child-42, got %q", child.GetWorkitemId())
	}
	if child.GetPhase() != "Running" {
		t.Fatalf("expected phase=Running, got %q", child.GetPhase())
	}
}

func TestOperatorProxy_GetChildren_PropagatesMetadata(t *testing.T) {
	proxy, capture, _ := setupOperatorProxyWithTracker(t)

	md := metadata.Pairs(
		"x-flow-namespace", "ns-A",
		"x-flow-workitem-id", "parent-wi",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.GetChildren(ctx, &flowv1.GetChildrenRequest{})
	if err != nil {
		t.Fatalf("GetChildren: %v", err)
	}

	vals := capture.capturedMD.Get("x-flow-workitem-id")
	if len(vals) != 1 || vals[0] != "parent-wi" {
		t.Fatalf("expected x-flow-workitem-id=parent-wi, got %v", vals)
	}
}
