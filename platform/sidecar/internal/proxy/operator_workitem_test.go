package proxy

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/sidecar/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// CreateChildWorkitem proxy tests
// ---------------------------------------------------------------------------

func setupOperatorProxyWithTracker(t *testing.T) (*OperatorProxy, *captureOperatorServer, *service.SidecarServer) {
	t.Helper()

	capture := &captureOperatorServer{
		createChildResp: &flowv1.CreateChildWorkitemResponse{
			ChildWorkitemId: childWI42,
		},
		routeChildResp: &flowv1.RouteChildResponse{Accepted: true},
		getChildrenResp: &flowv1.GetChildrenResponse{
			Children: []*flowv1.ChildWorkitemStatus{
				{WorkitemId: childWI42, Phase: "Running", CurrentAssignee: "forge"},
			},
		},
		resumeResp: &flowv1.ResumeWorkitemResponse{Accepted: true},
		listSuspendedResp: &flowv1.ListSuspendedWorkitemsResponse{
			Workitems: []*flowv1.SuspendedWorkitemInfo{},
		},
	}

	conn := dialBufconn(t, func(srv *grpc.Server) {
		flowv1.RegisterOperatorServiceServer(srv, capture)
	})

	sidecarSrv := newSidecarWithSession(t)

	proxy := &OperatorProxy{
		client:       flowv1.NewOperatorServiceClient(conn),
		conn:         conn,
		childTracker: sidecarSrv,
	}

	return proxy, capture, sidecarSrv
}

func TestOperatorProxy_CreateChildWorkitem_ForwardsAndTracksChild(t *testing.T) {
	proxy, _, srv := setupOperatorProxyWithTracker(t)

	// Simulate identity-enriched incoming metadata.
	md := metadata.Pairs("x-flow-workitem-id", "parent-wi")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := proxy.CreateChildWorkitem(ctx, &flowv1.CreateChildWorkitemRequest{})
	if err != nil {
		t.Fatalf("CreateChildWorkitem: %v", err)
	}
	if resp.GetChildWorkitemId() != childWI42 {
		t.Fatalf("expected child_workitem_id=%s, got %q",
			childWI42, resp.GetChildWorkitemId())
	}

	// Verify the child was tracked in the session via AuthorizeChildAccess.
	if decision := srv.AuthorizeChildAccess("parent-wi", childWI42); decision != service.ChildAccessAllowed {
		t.Fatalf("expected child_workitem to be authorized, got %v", decision)
	}
}

func TestOperatorProxy_CreateChildWorkitem_PropagatesMetadata(t *testing.T) {
	proxy, capture, _ := setupOperatorProxyWithTracker(t)

	md := metadata.Pairs(
		"x-flow-namespace", "ns-A",
		"x-flow-workitem-id", "parent-wi",
		"x-flow-node-id", "node-X",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.CreateChildWorkitem(ctx, &flowv1.CreateChildWorkitemRequest{})
	if err != nil {
		t.Fatalf("CreateChildWorkitem: %v", err)
	}

	assertCapturedMD := func(key, expected string) {
		t.Helper()
		vals := capture.capturedMD.Get(key)
		if len(vals) != 1 || vals[0] != expected {
			t.Fatalf("expected %s=%s, got %v", key, expected, vals)
		}
	}
	assertCapturedMD("x-flow-namespace", "ns-A")
	assertCapturedMD("x-flow-workitem-id", "parent-wi")
	assertCapturedMD("x-flow-node-id", "node-X")
}

func TestOperatorProxy_CreateChildWorkitem_ErrorNoTracking(t *testing.T) {
	proxy, capture, srv := setupOperatorProxyWithTracker(t)
	capture.createChildErr = status.Error(codes.PermissionDenied, "no capability")

	md := metadata.Pairs("x-flow-workitem-id", "parent-wi")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.CreateChildWorkitem(ctx, &flowv1.CreateChildWorkitemRequest{})
	if err == nil {
		t.Fatal("expected error when Operator rejects")
	}

	// No child should be tracked on error — AuthorizeChildAccess returns Unknown.
	if decision := srv.AuthorizeChildAccess("parent-wi", childWI42); decision != service.ChildAccessUnknown {
		t.Fatalf("expected no children tracked on error, got %v", decision)
	}
}

func TestOperatorProxy_CreateChildWorkitem_NilTracker(t *testing.T) {
	capture := &captureOperatorServer{
		createChildResp: &flowv1.CreateChildWorkitemResponse{
			ChildWorkitemId: "child-99",
		},
	}

	conn := dialBufconn(t, func(srv *grpc.Server) {
		flowv1.RegisterOperatorServiceServer(srv, capture)
	})

	proxy := &OperatorProxy{
		client:       flowv1.NewOperatorServiceClient(conn),
		conn:         conn,
		childTracker: nil, // No tracker.
	}

	md := metadata.Pairs("x-flow-workitem-id", "parent-wi")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := proxy.CreateChildWorkitem(ctx, &flowv1.CreateChildWorkitemRequest{})
	if err != nil {
		t.Fatalf("CreateChildWorkitem with nil tracker: %v", err)
	}
	if resp.GetChildWorkitemId() != "child-99" {
		t.Fatalf("expected child_workitem_id=child-99, got %q", resp.GetChildWorkitemId())
	}
}
