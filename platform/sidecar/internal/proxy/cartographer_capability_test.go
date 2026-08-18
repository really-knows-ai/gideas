package proxy

import (
	"context"
	"testing"

	flowmeta "github.com/foundry/flow/pkg/metadata"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// TestCartographerProxy_NodeCapabilities_NilPassThrough pins the
// non-node-originated nil branch of nodeCapabilities (SPEC R3 / Capability
// Authorisation Chain): a request carrying no x-flow-node-id — no incoming
// metadata at all, or metadata without the node identity — yields a nil
// capability set, which makes checkCapability pass the request through
// unchecked (even a blocking fixed gate) and leaves the Cartographer's ingress
// verifier as the security boundary. The wire harness always injects
// x-flow-node-id via the identity interceptor (entry-bound fallback), so these
// branches are unreachable through the E2E path and are pinned directly.
func TestCartographerProxy_NodeCapabilities_NilPassThrough(t *testing.T) {
	if caps := nodeCapabilities(context.Background()); caps != nil {
		t.Fatalf("expected nil capabilities for a context with no metadata, got %v", caps)
	}

	noNodeID := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("x-flow-namespace", "wire-ns"),
	)
	if caps := nodeCapabilities(noNodeID); caps != nil {
		t.Fatalf("expected nil capabilities for metadata without x-flow-node-id, got %v", caps)
	}

	// A nil capability set is treated as a system-to-system call: even the
	// fixed blocking tx gate passes through instead of denying.
	p := &CartographerProxy{}
	if err := p.checkCapability(context.Background(), "WRITE", "graph/tx", true); err != nil {
		t.Fatalf("expected checkCapability to pass through on nil capabilities, got %v", err)
	}
}

// TestCartographerProxy_NodeCapabilities_ParseTrimsAndDropsEmpty pins the
// capability-string normalization of nodeCapabilities (SPEC R3 / Capability
// Authorisation Chain): a node-originated request's comma-separated
// x-flow-capabilities value is split, each entry trimmed, and empty entries
// dropped — via the shared NormalizeCapabilities helper in pkg/metadata — so
// the Sidecar's mode-1 exact gates agree with the Cartographer's authoritative
// checks on the same capability string. It also pins the non-nil contract: a
// node-originated request with an empty capability value yields a non-nil
// empty slice (a zero-grant node), never nil (a system-to-system call).
func TestCartographerProxy_NodeCapabilities_ParseTrimsAndDropsEmpty(t *testing.T) {
	nodeCtx := func(caps string) context.Context {
		return metadata.NewIncomingContext(context.Background(),
			metadata.Pairs(
				flowmeta.MetadataKeyNodeID, "wire-node",
				flowmeta.MetadataKeyCapabilities, caps,
			))
	}

	got := nodeCapabilities(nodeCtx(" WRITE:graph/entity/Component , READ:graph/entity/* ,, "))
	want := []string{"WRITE:graph/entity/Component", "READ:graph/entity/*"}
	if len(got) != len(want) {
		t.Fatalf("nodeCapabilities = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("nodeCapabilities = %v, want %v", got, want)
		}
	}

	// Empty/whitespace-only capability value still yields a non-nil empty
	// slice: a zero-grant node is distinguishable from a system-to-system call.
	empty := nodeCapabilities(nodeCtx("  "))
	if empty == nil {
		t.Fatal("expected non-nil empty capabilities for a zero-grant node")
	}
	if len(empty) != 0 {
		t.Fatalf("expected empty capabilities for a zero-grant node, got %v", empty)
	}
}

// TestCartographerProxy_CheckCapability_ExactOrLiteralWildcard pins the matching
// semantics the Sidecar's mode-1/fixed gates share with the Cartographer's
// authoritative CheckSpecificType/CheckWildcard exact-string gates (SPEC R3 /
// Capability Authorisation Chain): a held grant satisfies the requirement only
// when it is exactly equal, or is the literal full-segment
// "<verb>:graph/entity/*" wildcard satisfying a type-specific requirement.
// Filepath metacharacters — partial-segment "*" ("Comp*"), "?", or "[a-z]" —
// are literal strings, never wildcards: a non-SPEC grant such as
// "WRITE:graph/entity/Comp*" must NOT pass a mode-1 check for Component, so it
// is blocked at the Sidecar exactly as the Cartographer would deny it, instead
// of being forwarded only to be denied with PERMISSION_DENIED at ingress.
func TestCartographerProxy_CheckCapability_ExactOrLiteralWildcard(t *testing.T) {
	nodeCtx := func(caps string) context.Context {
		return metadata.NewIncomingContext(context.Background(),
			metadata.Pairs(
				flowmeta.MetadataKeyNodeID, "wire-node",
				flowmeta.MetadataKeyCapabilities, caps,
			))
	}
	p := &CartographerProxy{}

	tests := []struct {
		name     string
		caps     string
		verb     string
		resource string
		wantDeny bool
	}{
		// Exact grant satisfies the same specific-type requirement.
		{"exact specific type", "WRITE:graph/entity/Component", "WRITE", "graph/entity/Component", false},
		// Literal full-segment wildcard satisfies a type-specific requirement
		// (SPEC R3:241-242: WRITE:graph/entity/* authorises all types).
		{"literal wildcard satisfies specific type", "WRITE:graph/entity/*", "WRITE", "graph/entity/Component", false},
		// Partial-segment / character-class wildcards are literal strings,
		// never wildcards — the Cartographer's exact-string gate denies them.
		{"partial wildcard does not match", "WRITE:graph/entity/Comp*", "WRITE", "graph/entity/Component", true},
		{"question mark does not match", "WRITE:graph/entity/Compon?nt", "WRITE", "graph/entity/Component", true},
		{"char class does not match", "WRITE:graph/entity/Compon[a-z]t", "WRITE", "graph/entity/Component", true},
		// A per-type grant cannot authorise an all-types requirement
		// (SPEC R3:262).
		{"specific type does not satisfy wildcard", "WRITE:graph/entity/Component", "WRITE", "graph/entity/*", true},
		{"literal wildcard satisfies wildcard", "WRITE:graph/entity/*", "WRITE", "graph/entity/*", false},
		// Verb mismatch denies.
		{"wrong verb", "READ:graph/entity/*", "WRITE", "graph/entity/Component", true},
		// Fixed tx gates remain exact.
		{"exact tx grant", "WRITE:graph/tx", "WRITE", "graph/tx", false},
		{"missing tx grant", "READ:graph/tx", "WRITE", "graph/tx", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.checkCapability(nodeCtx(tt.caps), tt.verb, tt.resource, true)
			if tt.wantDeny {
				if st := status.Code(err); st != codes.PermissionDenied {
					t.Fatalf("expected PermissionDenied, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("expected the grant to pass, got %v", err)
			}
		})
	}
}
