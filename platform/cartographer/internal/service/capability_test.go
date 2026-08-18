package service

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowmeta "github.com/foundry/flow/pkg/metadata"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestCapability_ValidSignature(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)

	ctx := capabilityContext("READ:graph/entity/Component", scPriv, "sidecar")
	ctx, err := srv.verifier.verify(ctx)
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	caps, err := ExtractCapabilities(ctx)
	if err != nil {
		t.Fatalf("ExtractCapabilities failed: %v", err)
	}
	if caps == nil {
		t.Fatal("expected non-nil capabilities")
	}
	if err := srv.verifier.CheckSpecificType(caps, "READ", "Component"); err != nil {
		t.Fatalf("CheckSpecificType failed: %v", err)
	}
}

// TestCapability_WhitespaceEntriesTrimmed pins the capability-string
// normalization that must match the sibling capability gates (SPEC R3 /
// Capability Authorisation Chain): each comma-separated entry in
// x-flow-capabilities is trimmed and empty entries dropped. The Sidecar proxy
// (nodeCapabilities) and Operator (checkCapability) trim every entry before
// matching, so the Cartographer's authoritative exact-match gates must do the
// same — otherwise a capability entry with leading/trailing whitespace is
// granted by the Sidecar and Operator gates but denied here, a divergent
// authorization between sibling implementations of the same contract.
func TestCapability_WhitespaceEntriesTrimmed(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)

	// Entries padded with whitespace and an all-whitespace trailing entry, as
	// a node operator might write in FoundryNode.spec.capabilities.
	ctx := capabilityContext(" READ:graph/entity/Component , WRITE:graph/entity/* , ", scPriv, "sidecar")
	ctx, err := srv.verifier.verify(ctx)
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	caps, err := ExtractCapabilities(ctx)
	if err != nil {
		t.Fatalf("ExtractCapabilities failed: %v", err)
	}
	if caps == nil {
		t.Fatal("expected non-nil capabilities")
	}
	if err := srv.verifier.CheckSpecificType(caps, "READ", "Component"); err != nil {
		t.Fatalf("whitespace-padded capability must be trimmed before the authoritative exact match, got: %v", err)
	}
	if err := srv.verifier.CheckWildcard(caps, "WRITE"); err != nil {
		t.Fatalf("whitespace-padded wildcard capability must be trimmed before the authoritative exact match, got: %v", err)
	}
}

// TestCapability_ValidOperatorSignature is the positive pin for the
// operator-signer branch of the capability chain: verify() selects
// v.operatorKey when signedBy == "operator" (capability.go:135-137). Every
// other service-layer capability test signs with the sidecar private key
// (TestCapability_ValidSignature, testCtx, narrowCtx), and the only
// operator-signed test (TestCapability_InvalidSignature) deliberately uses a
// wrong key to pin denial — so a regression that broke operator-key selection
// would fail no test. This test signs a capability payload with the OPERATOR
// private key, routes it through the Cartographer's verifier, and asserts it
// is ACCEPTED and stored: if the operator branch stopped selecting
// v.operatorKey (or selected nothing), the Ed25519 verify would fail and this
// test would fail.
func TestCapability_ValidOperatorSignature(t *testing.T) {
	opPub, opPriv := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)

	ctx := capabilityContext("READ:graph/entity/Component", opPriv, "operator")
	ctx, err := srv.verifier.verify(ctx)
	if err != nil {
		t.Fatalf("verify failed for operator-signed capabilities: %v", err)
	}
	caps, err := ExtractCapabilities(ctx)
	if err != nil {
		t.Fatalf("ExtractCapabilities failed: %v", err)
	}
	if caps == nil {
		t.Fatal("expected non-nil capabilities")
	}
	if caps.SignedBy != "operator" {
		t.Fatalf("expected SignedBy operator, got %q", caps.SignedBy)
	}
	if err := srv.verifier.CheckSpecificType(caps, "READ", "Component"); err != nil {
		t.Fatalf("CheckSpecificType failed: %v", err)
	}
}

func TestCapability_InvalidSignature(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	_, wrongPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)

	ctx := capabilityContext("READ:graph/entity/Component", wrongPriv, "operator")
	_, err := srv.verifier.verify(ctx)
	if err == nil {
		t.Fatal("expected error for invalid signature, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

func TestCapability_MissingMetadata(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)

	// No metadata context -> should pass through (system-to-system call).
	ctx := context.Background()
	ctx, err := srv.verifier.verify(ctx)
	if err != nil {
		t.Fatalf("expected no error for missing metadata, got: %v", err)
	}
	caps, err := ExtractCapabilities(ctx)
	if err != nil {
		t.Fatalf("ExtractCapabilities failed: %v", err)
	}
	if caps != nil {
		t.Fatal("expected nil capabilities for system-to-system call")
	}
}

// TestCapability_PresentButEmptyCapsFailsClosed pins the boundary between
// "capability metadata absent" (system-to-system Operator pass-through,
// TestCapability_MissingMetadata) and "capability metadata present but
// empty/whitespace-only" (capability.go): a request that carries the
// x-flow-capabilities key with an empty or whitespace-only value claims a
// capability attestation but carries no capability entries, so it must fail
// closed with PERMISSION_DENIED at the ingress interceptor instead of being
// reclassified as a trusted system-to-system Operator call that skips
// signature and staleness verification entirely (interceptor contract:
// "If present but unverifiable ..., the interceptor returns PERMISSION_DENIED
// before the handler runs").
func TestCapability_PresentButEmptyCapsFailsClosed(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)

	for _, tc := range []struct {
		name string
		caps string
	}{
		{"empty value", ""},
		{"whitespace only", "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			md := metadata.Pairs(flowmeta.MetadataKeyCapabilities, tc.caps)
			ctx := metadata.NewIncomingContext(context.Background(), md)
			_, err := srv.verifier.verify(ctx)
			if err == nil {
				t.Fatal("expected error for present-but-empty capabilities, got nil")
			}
			if status.Code(err) != codes.PermissionDenied ||
				status.Convert(err).Message() != "invalid capability signature" {
				t.Fatalf("expected PermissionDenied invalid-capability-signature, got %v", err)
			}
		})
	}
}

// TestCapability_NilCapsFailClosed pins the fail-closed branch of every
// node-facing capability gate (checkEntityCap, checkTxCap,
// checkWildcardEntityCap — cartographer_server.go): when no capability
// metadata is present, the ingress interceptor's system-to-system pass-through
// leaves nil capabilities in the context, and every node-facing gate must deny
// the request with PERMISSION_DENIED (errCapabilityDenied). Only the verifier
// half is pinned elsewhere (TestCapability_MissingMetadata); every RPC test
// injects non-nil capabilities (testCtx, capabilityContext/narrowCtx/noReadCtx),
// so a regression making any of the three nil branches fail open (return nil)
// would currently pass the entire suite. Each subtest drives one gate with a
// bare context.Background().
func TestCapability_NilCapsFailClosed(t *testing.T) {
	srv, _ := newTestServer(t)

	// checkEntityCap — ListEntities checks READ:graph/entity/<type> first.
	t.Run("checkEntityCap", func(t *testing.T) {
		_, err := srv.ListEntities(context.Background(), &flowv1.ListEntitiesRequest{EntityType: "Component"})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("expected PermissionDenied for nil capabilities on checkEntityCap, got %v", status.Code(err))
		}
	})

	// checkTxCap — BeginTransaction checks WRITE:graph/tx first.
	t.Run("checkTxCap", func(t *testing.T) {
		_, err := srv.BeginTransaction(context.Background(), &flowv1.BeginTransactionRequest{})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("expected PermissionDenied for nil capabilities on checkTxCap, got %v", status.Code(err))
		}
	})

	// checkWildcardEntityCap — Sync checks WRITE:graph/entity/* first.
	t.Run("checkWildcardEntityCap", func(t *testing.T) {
		_, err := srv.Sync(context.Background(), &flowv1.SyncRequest{})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("expected PermissionDenied for nil capabilities on checkWildcardEntityCap, got %v", status.Code(err))
		}
	})
}

func TestCapability_UnrecognizedSigner(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)

	sig := base64.StdEncoding.EncodeToString([]byte("fake"))
	md := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, "READ:graph/entity/Component",
		flowmeta.MetadataKeyCapabilitiesSignature, sig,
		flowmeta.MetadataKeyCapabilitiesSignedAt, "1234567890",
		flowmeta.MetadataKeyCapabilitiesSignedBy, "unknown",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := srv.verifier.verify(ctx)
	if err == nil {
		t.Fatal("expected error for unrecognized signer, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

func TestCapability_MissingSignedBy(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)

	// Valid caps + signature + signed-at present, but the signed-by key is
	// omitted entirely — no verification key can be selected, so verify must
	// return PERMISSION_DENIED (SPEC error table: absent/empty signed-by).
	payload := "READ:graph/entity/Component|1234567890"
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(scPriv, []byte(payload)))
	md := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, "READ:graph/entity/Component",
		flowmeta.MetadataKeyCapabilitiesSignature, sig,
		flowmeta.MetadataKeyCapabilitiesSignedAt, "1234567890",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := srv.verifier.verify(ctx)
	if err == nil {
		t.Fatal("expected error for missing signed-by, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

func TestCapability_StaleCapability_UnaryInterceptorRejectsBeforeHandler(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := capabilityContextAt(
		"WRITE:graph/entity/*", testSidecarPriv, "sidecar", time.Now().Add(-2*time.Minute).Unix(),
	)

	handlerInvoked, _, err := invokeSync(srv, ctx)
	if handlerInvoked {
		t.Fatal("unary handler ran for stale capability")
	}
	if status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != capabilityStaleMsg {
		t.Fatalf("expected stale-capability PermissionDenied, got %v", err)
	}
}

func TestCapability_StaleCapability_StreamInterceptorRejectsBeforeHandler(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := capabilityContextAt(
		"READ:graph/entity/*", testSidecarPriv, "sidecar", time.Now().Add(-2*time.Minute).Unix(),
	)

	handlerInvoked, err := invokeExportGraph(
		srv, &flowv1.ExportGraphRequest{Format: "json"}, &mockExportStream{ctx: ctx},
	)
	if handlerInvoked {
		t.Fatal("stream handler ran for stale capability")
	}
	if status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != capabilityStaleMsg {
		t.Fatalf("expected stale-capability PermissionDenied, got %v", err)
	}
}
