package proxy

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/metadata"
)

// ---------------------------------------------------------------------------
// ResumeWorkitem proxy tests
// ---------------------------------------------------------------------------

func TestOperatorProxy_ResumeWorkitem_ForwardsAndReturns(t *testing.T) {
	proxy, capture, _ := setupOperatorProxyWithTracker(t)

	md := metadata.Pairs("x-flow-workitem-id", "wi-suspended")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := proxy.ResumeWorkitem(ctx, &flowv1.ResumeWorkitemRequest{
		WorkitemId: "wi-suspended",
	})
	if err != nil {
		t.Fatalf("ResumeWorkitem: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("expected accepted=true")
	}

	// Verify the request was forwarded to the backend.
	if capture.lastResumeReq == nil {
		t.Fatal("ResumeWorkitem was not forwarded to Operator backend")
	}
	if capture.lastResumeReq.GetWorkitemId() != "wi-suspended" {
		t.Fatalf("expected forwarded workitem_id=wi-suspended, got %q",
			capture.lastResumeReq.GetWorkitemId())
	}
}

func TestOperatorProxy_ResumeWorkitem_PropagatesMetadata(t *testing.T) {
	proxy, capture, _ := setupOperatorProxyWithTracker(t)

	md := metadata.Pairs(
		"x-flow-namespace", "ns-A",
		"x-flow-workitem-id", "wi-suspended",
		"x-flow-node-id", "node-X",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.ResumeWorkitem(ctx, &flowv1.ResumeWorkitemRequest{
		WorkitemId: "wi-suspended",
	})
	if err != nil {
		t.Fatalf("ResumeWorkitem: %v", err)
	}

	assertCapturedMD := func(key, expected string) {
		t.Helper()
		vals := capture.capturedMD.Get(key)
		if len(vals) != 1 || vals[0] != expected {
			t.Fatalf("expected %s=%s, got %v", key, expected, vals)
		}
	}
	assertCapturedMD("x-flow-namespace", "ns-A")
	assertCapturedMD("x-flow-workitem-id", "wi-suspended")
	assertCapturedMD("x-flow-node-id", "node-X")
}

// ---------------------------------------------------------------------------
// ListSuspendedWorkitems proxy tests
// ---------------------------------------------------------------------------

func TestOperatorProxy_ListSuspendedWorkitems_ForwardsAndReturns(t *testing.T) {
	proxy, capture, _ := setupOperatorProxyWithTracker(t)

	// Pre-configure the backend to return some suspended workitems.
	capture.listSuspendedResp = &flowv1.ListSuspendedWorkitemsResponse{
		Workitems: []*flowv1.SuspendedWorkitemInfo{
			{WorkitemId: "wi-held-1", ResumeCondition: `dispute_retired("pet-42")`},
		},
	}

	md := metadata.Pairs("x-flow-namespace", "ns-test")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := proxy.ListSuspendedWorkitems(ctx, &flowv1.ListSuspendedWorkitemsRequest{
		ConditionContains: "pet-42",
	})
	if err != nil {
		t.Fatalf("ListSuspendedWorkitems: %v", err)
	}
	if len(resp.GetWorkitems()) != 1 {
		t.Fatalf("expected 1 workitem, got %d", len(resp.GetWorkitems()))
	}
	if resp.GetWorkitems()[0].GetWorkitemId() != "wi-held-1" {
		t.Fatalf("expected workitem_id=wi-held-1, got %q",
			resp.GetWorkitems()[0].GetWorkitemId())
	}

	// Verify the request was forwarded to the backend.
	if capture.lastListSuspendedReq == nil {
		t.Fatal("ListSuspendedWorkitems was not forwarded to Operator backend")
	}
	if capture.lastListSuspendedReq.GetConditionContains() != "pet-42" {
		t.Fatalf("expected condition_contains=pet-42, got %q",
			capture.lastListSuspendedReq.GetConditionContains())
	}
}

func TestOperatorProxy_ListSuspendedWorkitems_PropagatesMetadata(t *testing.T) {
	proxy, capture, _ := setupOperatorProxyWithTracker(t)

	md := metadata.Pairs(
		"x-flow-namespace", "ns-B",
		"x-flow-node-id", "watcher-node",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.ListSuspendedWorkitems(ctx, &flowv1.ListSuspendedWorkitemsRequest{
		ConditionContains: "pet-1",
	})
	if err != nil {
		t.Fatalf("ListSuspendedWorkitems: %v", err)
	}

	assertCapturedMD := func(key, expected string) {
		t.Helper()
		vals := capture.capturedMD.Get(key)
		if len(vals) != 1 || vals[0] != expected {
			t.Fatalf("expected %s=%s, got %v", key, expected, vals)
		}
	}
	assertCapturedMD("x-flow-namespace", "ns-B")
	assertCapturedMD("x-flow-node-id", "watcher-node")
}
