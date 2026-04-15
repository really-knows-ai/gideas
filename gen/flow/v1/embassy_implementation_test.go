package flowv1_test

import (
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// TestEmbassyServiceDescriptorSurface is an architectural guard that ensures
// the embassy.proto RPC surface has not regressed. Every required RPC must
// appear in the generated service descriptor -- if a method is removed from the
// proto the test fails at compile+run time, catching accidental regressions.
func TestEmbassyServiceDescriptorSurface(t *testing.T) {
	t.Parallel()

	desc := flowv1.EmbassyService_ServiceDesc
	methods := make(map[string]bool)
	for _, m := range desc.Methods {
		methods[m.MethodName] = true
	}
	for _, s := range desc.Streams {
		methods[s.StreamName] = true
	}

	required := []string{
		"PreflightManifest",
		"StreamPackage",
		"ExportPackage",
	}
	for _, name := range required {
		if !methods[name] {
			t.Errorf("EmbassyService_ServiceDesc missing required RPC %q", name)
		}
	}
}

// TestEmbassyServiceCompileTimeAssertions is a compile-time guard ensuring the
// generated Embassy interfaces and their Unimplemented stubs remain consistent.
func TestEmbassyServiceCompileTimeAssertions(t *testing.T) {
	t.Parallel()

	// Verify Unimplemented stub satisfies the server interface.
	var _ flowv1.EmbassyServiceServer = flowv1.UnimplementedEmbassyServiceServer{}

	// Verify the client interface symbol exists (nil zero-value).
	var _ flowv1.EmbassyServiceClient = (flowv1.EmbassyServiceClient)(nil)
}
