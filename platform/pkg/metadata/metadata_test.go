package metadata

import "testing"

// TestNormalizeCapabilities pins the capability-string normalization shared by
// every capability gate in the chain (SPEC R3 / Capability Authorisation
// Chain): the Cartographer's ingress verifier, the Sidecar's nodeCapabilities,
// and the SDK's CheckCapability all import NormalizeCapabilities so the
// x-flow-capabilities wire value is parsed identically across sibling modules
// — split on ",", trim each entry, drop empty entries. A divergence here fails
// this test before it can silently split authorization decisions.
func TestNormalizeCapabilities(t *testing.T) {
	tests := []struct {
		name string
		caps string
		want []string
	}{
		{"single entry", "READ:flow", []string{"READ:flow"}},
		{"multiple entries", "READ:flow,WRITE:artefact", []string{"READ:flow", "WRITE:artefact"}},
		{"whitespace padded entries", " READ:flow , WRITE:artefact ,", []string{"READ:flow", "WRITE:artefact"}},
		{"empty entries dropped", "READ:flow,,,WRITE:artefact", []string{"READ:flow", "WRITE:artefact"}},
		{"empty string", "", nil},
		{"whitespace only", "   ", nil},
		{"commas only", ", ,", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeCapabilities(tt.caps)
			if tt.want == nil {
				if len(got) != 0 {
					t.Fatalf("NormalizeCapabilities(%q) = %v, want no entries", tt.caps, got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("NormalizeCapabilities(%q) = %v, want %v", tt.caps, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("NormalizeCapabilities(%q) = %v, want %v", tt.caps, got, tt.want)
				}
			}
		})
	}
}
