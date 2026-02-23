package proxy

import (
	"context"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// captureOperatorServer captures Operator RPC calls for assertions.
type captureOperatorServer struct {
	flowv1.UnimplementedOperatorServiceServer
	lastTopologyReq *flowv1.GetFlowTopologyRequest
	topologyResp    *flowv1.GetFlowTopologyResponse
	capturedMD      metadata.MD
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
