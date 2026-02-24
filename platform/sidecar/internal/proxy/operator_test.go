package proxy

import (
	"context"
	"sync"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// captureOperatorServer captures Operator RPC calls for assertions.
type captureOperatorServer struct {
	flowv1.UnimplementedOperatorServiceServer
	lastTopologyReq *flowv1.GetFlowTopologyRequest
	topologyResp    *flowv1.GetFlowTopologyResponse
	capturedMD      metadata.MD

	// Child Workitem RPC fields.
	createChildResp *flowv1.CreateChildWorkitemResponse
	createChildErr  error
	routeChildResp  *flowv1.RouteChildResponse
	routeChildErr   error
	getChildrenResp *flowv1.GetChildrenResponse
	lastRouteReq    *flowv1.RouteChildRequest
}

func (s *captureOperatorServer) GetFlowTopology(
	ctx context.Context, req *flowv1.GetFlowTopologyRequest,
) (*flowv1.GetFlowTopologyResponse, error) {
	s.lastTopologyReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return s.topologyResp, nil
}

func (s *captureOperatorServer) CreateChildWorkitem(
	ctx context.Context, _ *flowv1.CreateChildWorkitemRequest,
) (*flowv1.CreateChildWorkitemResponse, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	if s.createChildErr != nil {
		return nil, s.createChildErr
	}
	return s.createChildResp, nil
}

func (s *captureOperatorServer) RouteChild(
	ctx context.Context, req *flowv1.RouteChildRequest,
) (*flowv1.RouteChildResponse, error) {
	s.lastRouteReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	if s.routeChildErr != nil {
		return nil, s.routeChildErr
	}
	return s.routeChildResp, nil
}

func (s *captureOperatorServer) GetChildren(
	ctx context.Context, _ *flowv1.GetChildrenRequest,
) (*flowv1.GetChildrenResponse, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return s.getChildrenResp, nil
}

// fakeChildTracker records TrackChild calls for assertions.
type fakeChildTracker struct {
	mu       sync.Mutex
	children map[string][]string // parentID -> []childID
}

func newFakeChildTracker() *fakeChildTracker {
	return &fakeChildTracker{children: make(map[string][]string)}
}

const childWI42 = "child-42"

func (f *fakeChildTracker) TrackChild(parentWorkitemID, childWorkitemID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.children[parentWorkitemID] = append(f.children[parentWorkitemID], childWorkitemID)
}

func (f *fakeChildTracker) getChildren(parentID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.children[parentID]
}

func setupOperatorProxy(t *testing.T) (*OperatorProxy, *captureOperatorServer) {
	t.Helper()

	capture := &captureOperatorServer{
		topologyResp: &flowv1.GetFlowTopologyResponse{
			Self: &flowv1.FlowNode{
				Name:         "sort",
				Capabilities: []string{"READ:flow", "READ:artefact"},
				Outputs: []*flowv1.FlowOutput{
					{Name: "forge", Target: "forge"},
					{Name: "exit", Target: "exit"},
				},
			},
			Nodes: map[string]*flowv1.FlowNode{
				"forge": {
					Name:         "forge",
					Capabilities: []string{"WRITE:artefact"},
				},
			},
			ExitContract: map[string]*flowv1.StampRequirements{
				"txt": {Stamps: []string{"approved"}},
			},
		},
	}

	conn := dialBufconn(t, func(srv *grpc.Server) {
		flowv1.RegisterOperatorServiceServer(srv, capture)
	})

	proxy := &OperatorProxy{
		client: flowv1.NewOperatorServiceClient(conn),
		conn:   conn,
	}

	return proxy, capture
}

func TestPropagateMetadata_WithMetadata(t *testing.T) {
	md := metadata.Pairs("x-flow-workitem-id", "test-123", "x-other", "val")
	inCtx := metadata.NewIncomingContext(context.Background(), md)

	outCtx := propagateMetadata(inCtx)

	outMD, ok := metadata.FromOutgoingContext(outCtx)
	if !ok {
		t.Fatal("Expected outgoing metadata")
	}

	vals := outMD.Get("x-flow-workitem-id")
	if len(vals) != 1 || vals[0] != "test-123" {
		t.Fatalf("Expected x-flow-workitem-id=test-123, got %v", vals)
	}

	otherVals := outMD.Get("x-other")
	if len(otherVals) != 1 || otherVals[0] != "val" {
		t.Fatalf("Expected x-other=val, got %v", otherVals)
	}
}

func TestPropagateMetadata_NoMetadata(t *testing.T) {
	ctx := context.Background()
	outCtx := propagateMetadata(ctx)

	// Should return the same context when no incoming metadata exists.
	_, ok := metadata.FromOutgoingContext(outCtx)
	if ok {
		t.Fatal("Expected no outgoing metadata when no incoming metadata")
	}
}

// TestPropagateMetadata_ForwardsEnrichedIdentity verifies that identity
// metadata injected by the IdentityInterceptor is correctly forwarded
// to outgoing context by propagateMetadata.
func TestPropagateMetadata_ForwardsEnrichedIdentity(t *testing.T) {
	// Simulate what the identity interceptor does: incoming metadata
	// contains all three identity fields after enrichment.
	md := metadata.Pairs(
		"x-flow-flow-id", "flow-A",
		"x-flow-workitem-id", "wi-42",
		"x-flow-node-id", "node-X",
		"x-custom", "preserved",
	)
	inCtx := metadata.NewIncomingContext(context.Background(), md)

	outCtx := propagateMetadata(inCtx)

	outMD, ok := metadata.FromOutgoingContext(outCtx)
	if !ok {
		t.Fatal("Expected outgoing metadata")
	}

	// All identity fields should be forwarded to the upstream service.
	assertOutgoing := func(key, expected string) {
		t.Helper()
		vals := outMD.Get(key)
		if len(vals) != 1 || vals[0] != expected {
			t.Fatalf("expected %s=%s, got %v", key, expected, vals)
		}
	}

	assertOutgoing("x-flow-flow-id", "flow-A")
	assertOutgoing("x-flow-workitem-id", "wi-42")
	assertOutgoing("x-flow-node-id", "node-X")
	assertOutgoing("x-custom", "preserved")
}

// ---------------------------------------------------------------------------
// GetFlowTopology proxy integration tests
// ---------------------------------------------------------------------------

func TestOperatorProxy_GetFlowTopology_ForwardsAndReturnsResponse(t *testing.T) {
	proxy, capture := setupOperatorProxy(t)

	resp, err := proxy.GetFlowTopology(context.Background(), &flowv1.GetFlowTopologyRequest{})
	if err != nil {
		t.Fatalf("GetFlowTopology: %v", err)
	}

	// Verify the request was forwarded to the backend.
	if capture.lastTopologyReq == nil {
		t.Fatal("GetFlowTopology was not forwarded to Operator backend")
	}

	// Verify the response is passed through correctly.
	if resp.GetSelf().GetName() != "sort" {
		t.Fatalf("expected self.name=sort, got %q", resp.GetSelf().GetName())
	}
	if len(resp.GetSelf().GetCapabilities()) != 2 {
		t.Fatalf("expected 2 capabilities on self, got %d", len(resp.GetSelf().GetCapabilities()))
	}
	if len(resp.GetSelf().GetOutputs()) != 2 {
		t.Fatalf("expected 2 outputs on self, got %d", len(resp.GetSelf().GetOutputs()))
	}
	forgeNode, ok := resp.GetNodes()["forge"]
	if !ok {
		t.Fatal("expected nodes map to contain 'forge'")
	}
	if forgeNode.GetName() != "forge" {
		t.Fatalf("expected forge node name=forge, got %q", forgeNode.GetName())
	}
	exitContract := resp.GetExitContract()
	if stamps, ok := exitContract["txt"]; !ok {
		t.Fatal("expected exit_contract to contain 'txt'")
	} else if len(stamps.GetStamps()) != 1 || stamps.GetStamps()[0] != "approved" {
		t.Fatalf("expected exit_contract[txt]=[approved], got %v", stamps.GetStamps())
	}
}

func TestOperatorProxy_GetFlowTopology_PropagatesIdentityMetadata(t *testing.T) {
	proxy, capture := setupOperatorProxy(t)

	md := metadata.Pairs(
		"x-flow-flow-id", "flow-test",
		"x-flow-workitem-id", "wi-test",
		"x-flow-node-id", "node-test",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.GetFlowTopology(ctx, &flowv1.GetFlowTopologyRequest{})
	if err != nil {
		t.Fatalf("GetFlowTopology: %v", err)
	}

	assertMD := func(key, expected string) {
		t.Helper()
		vals := capture.capturedMD.Get(key)
		if len(vals) != 1 || vals[0] != expected {
			t.Fatalf("expected %s=%s in forwarded metadata, got %v", key, expected, vals)
		}
	}

	assertMD("x-flow-flow-id", "flow-test")
	assertMD("x-flow-workitem-id", "wi-test")
	assertMD("x-flow-node-id", "node-test")
}

// ---------------------------------------------------------------------------
// CreateChildWorkitem proxy tests
// ---------------------------------------------------------------------------

func setupOperatorProxyWithTracker(t *testing.T) (*OperatorProxy, *captureOperatorServer, *fakeChildTracker) {
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
	}

	conn := dialBufconn(t, func(srv *grpc.Server) {
		flowv1.RegisterOperatorServiceServer(srv, capture)
	})

	tracker := newFakeChildTracker()

	proxy := &OperatorProxy{
		client:       flowv1.NewOperatorServiceClient(conn),
		conn:         conn,
		childTracker: tracker,
	}

	return proxy, capture, tracker
}

func TestOperatorProxy_CreateChildWorkitem_ForwardsAndTracksChild(t *testing.T) {
	proxy, _, tracker := setupOperatorProxyWithTracker(t)

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

	// Verify the child was tracked in the session.
	children := tracker.getChildren("parent-wi")
	if len(children) != 1 || children[0] != childWI42 {
		t.Fatalf("expected tracked child [child-42], got %v", children)
	}
}

func TestOperatorProxy_CreateChildWorkitem_PropagatesMetadata(t *testing.T) {
	proxy, capture, _ := setupOperatorProxyWithTracker(t)

	md := metadata.Pairs(
		"x-flow-flow-id", "flow-A",
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
	assertCapturedMD("x-flow-flow-id", "flow-A")
	assertCapturedMD("x-flow-workitem-id", "parent-wi")
	assertCapturedMD("x-flow-node-id", "node-X")
}

func TestOperatorProxy_CreateChildWorkitem_ErrorNoTracking(t *testing.T) {
	proxy, capture, tracker := setupOperatorProxyWithTracker(t)
	capture.createChildErr = status.Error(codes.PermissionDenied, "no capability")

	md := metadata.Pairs("x-flow-workitem-id", "parent-wi")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.CreateChildWorkitem(ctx, &flowv1.CreateChildWorkitemRequest{})
	if err == nil {
		t.Fatal("expected error when Operator rejects")
	}

	// No child should be tracked on error.
	children := tracker.getChildren("parent-wi")
	if len(children) != 0 {
		t.Fatalf("expected no tracked children on error, got %v", children)
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
		"x-flow-flow-id", "flow-A",
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
		"x-flow-flow-id", "flow-A",
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
