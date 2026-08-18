package embassy

import (
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// --- Slice 12.3.1 tests: membership + discovery ---

func TestFederationClient_GetPetitionTarget_Success(t *testing.T) {
	spy := &federationSpyServer{}
	client := setupFederationTestClient(t, spy)

	target, err := client.GetPetitionTarget(testScopeSecurity)
	if err != nil {
		t.Fatalf("GetPetitionTarget() returned error: %v", err)
	}
	if target.AuthorityFlowIdentity != "authority-flow-1" {
		t.Fatalf("expected authority identity authority-flow-1, got %q", target.AuthorityFlowIdentity)
	}
	if target.EmbassyEndpoint != "authority-flow-1.embassy:50059" {
		t.Fatalf("expected embassy endpoint authority-flow-1.embassy:50059, got %q", target.EmbassyEndpoint)
	}
	if spy.lastGetPetitionTarget.GetScope() != testScopeSecurity {
		t.Fatalf("expected scope %s, got %q", testScopeSecurity, spy.lastGetPetitionTarget.GetScope())
	}
}

func TestFederationClient_DiscoverEndpoints_NoFilter(t *testing.T) {
	spy := &federationSpyServer{}
	client := setupFederationTestClient(t, spy)

	endpoints, err := client.DiscoverEndpoints("")
	if err != nil {
		t.Fatalf("DiscoverEndpoints() returned error: %v", err)
	}
	if len(endpoints) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(endpoints))
	}
	if endpoints[0].FlowIdentity != "flow-a" {
		t.Fatalf("expected flow identity flow-a, got %q", endpoints[0].FlowIdentity)
	}
	if endpoints[0].EmbassyAddress != "flow-a.embassy:50059" {
		t.Fatalf("expected embassy address flow-a.embassy:50059, got %q", endpoints[0].EmbassyAddress)
	}
	if spy.lastDiscoverEndpoints.GetStateFilter() != "" {
		t.Fatalf("expected empty state filter, got %q", spy.lastDiscoverEndpoints.GetStateFilter())
	}
}

func TestFederationClient_DiscoverEndpoints_WithFilter(t *testing.T) {
	spy := &federationSpyServer{
		discoverEndpointsResp: &flowv1.DiscoverEndpointsResponse{
			Endpoints: []*flowv1.FlowEndpoint{
				{
					FlowIdentity:   "flow-b",
					EmbassyAddress: "flow-b.embassy:50059",
					StateIds:       []string{"state-2"},
				},
				{
					FlowIdentity:   "flow-c",
					EmbassyAddress: "flow-c.embassy:50059",
					StateIds:       []string{"state-2"},
				},
			},
		},
	}
	client := setupFederationTestClient(t, spy)

	endpoints, err := client.DiscoverEndpoints("state-2")
	if err != nil {
		t.Fatalf("DiscoverEndpoints() returned error: %v", err)
	}
	if len(endpoints) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(endpoints))
	}
	if spy.lastDiscoverEndpoints.GetStateFilter() != "state-2" {
		t.Fatalf("expected state filter state-2, got %q", spy.lastDiscoverEndpoints.GetStateFilter())
	}
}

func TestFederationClient_ConnectsToConfigurableAddress(t *testing.T) {
	spy := &federationSpyServer{}
	client := setupFederationTestClient(t, spy)

	// Verify the client was successfully created and can make calls.
	_, err := client.GetPetitionTarget("test")
	if err != nil {
		t.Fatalf("expected successful call on configured address, got error: %v", err)
	}
}

func TestFederationClient_GetPetitionTarget_NoConnection(t *testing.T) {
	client := &FederationClient{}
	_, err := client.GetPetitionTarget("test")
	if err == nil {
		t.Fatal("expected error when federation connection is missing")
	}
}

func TestFederationClient_DiscoverEndpoints_NoConnection(t *testing.T) {
	client := &FederationClient{}
	_, err := client.DiscoverEndpoints("")
	if err == nil {
		t.Fatal("expected error when federation connection is missing")
	}
}
