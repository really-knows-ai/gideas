package flowv1_test

import (
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// TestFederationServiceDescriptorSurface is an architectural guard that ensures
// the federation.proto RPC surface has not regressed. All 8 RPCs (6 unary +
// 2 server-streaming) must appear in the generated service descriptor.
func TestFederationServiceDescriptorSurface(t *testing.T) {
	t.Parallel()

	desc := flowv1.FederationService_ServiceDesc
	methods := make(map[string]bool)
	for _, m := range desc.Methods {
		methods[m.MethodName] = true
	}
	for _, s := range desc.Streams {
		methods[s.StreamName] = true
	}

	required := []string{
		"JoinFederation",
		"LeaveFederation",
		"GetMembership",
		"DiscoverEndpoints",
		"GetPetitionTarget",
		"SubmitPublication",
		"SubscribeLawUpdates",
		"SubscribePetitionOutcomes",
	}
	for _, name := range required {
		if !methods[name] {
			t.Errorf("FederationService_ServiceDesc missing required RPC %q", name)
		}
	}
}

// TestFederationServiceCompileTimeAssertions is a compile-time guard ensuring
// the generated Federation interfaces and their Unimplemented stubs remain
// consistent.
func TestFederationServiceCompileTimeAssertions(t *testing.T) {
	t.Parallel()

	// Verify Unimplemented stub satisfies the server interface.
	var _ flowv1.FederationServiceServer = flowv1.UnimplementedFederationServiceServer{}

	// Verify the client interface symbol exists (nil zero-value).
	var _ = (flowv1.FederationServiceClient)(nil)
}
