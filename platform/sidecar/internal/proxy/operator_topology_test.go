package proxy

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/metadata"
)

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
		"x-flow-namespace", "ns-test",
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

	assertMD("x-flow-namespace", "ns-test")
	assertMD("x-flow-workitem-id", "wi-test")
	assertMD("x-flow-node-id", "node-test")
}
