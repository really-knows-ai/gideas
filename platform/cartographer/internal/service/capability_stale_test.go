package service

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowmeta "github.com/foundry/flow/pkg/metadata"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// TestCapability_WildcardFallback verifies that Mode 2 wildcard fallback works:
// a capability "READ:graph/entity/*" should allow reading any entity type even
// without a specific "READ:graph/entity/Component" capability.
func TestCapability_WildcardFallback(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Grant only the wildcard entity read capability.
	mdCtx := capabilityContext("READ:graph/entity/*", scPriv, "sidecar")
	// Run through the verifier to store capabilities in context (simulating interceptor).
	verifiedCtx, err := srv.verifier.verify(mdCtx)
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	applyTestSchema(verifiedCtx, t, srv.store)
	_, _ = srv.store.CreateEntity(verifiedCtx, "Component", "", map[string]string{"name": "x"}, nil, "")

	// ListEntities uses checkEntityCap which falls back to wildcard.
	resp, err := srv.ListEntities(verifiedCtx, &flowv1.ListEntitiesRequest{
		EntityType: "Component",
		PageSize:   10,
	})
	if err != nil {
		t.Fatalf("ListEntities with wildcard fallback failed: %v", err)
	}
	if len(resp.Entities) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(resp.Entities))
	}
}

// =========================================================================
// 27. Staleness window boundary tests
// =========================================================================

func TestCapability_StalenessBoundary_InsideAndPast(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	// 30-second staleness window, like production.
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)

	// Capability signed 25 seconds ago — inside the 30-second window. A margin
	// (25s, not 29s/30s) is required because Unix() truncates to a whole
	// second while the verifier's elapsed (time.Since of that second) carries
	// the sub-second fraction of the current time: signed at -29s, a wall-clock
	// second tick between signing and verifying pushes elapsed past 30s and the
	// in-window capability is wrongly rejected as stale.
	insideWindow := time.Now().Add(-25 * time.Second).Unix()
	payload := fmt.Sprintf("READ:graph/entity/Component|%d", insideWindow)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(scPriv, []byte(payload)))
	md := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, "READ:graph/entity/Component",
		flowmeta.MetadataKeyCapabilitiesSignature, sig,
		flowmeta.MetadataKeyCapabilitiesSignedAt, fmt.Sprintf("%d", insideWindow),
		flowmeta.MetadataKeyCapabilitiesSignedBy, "sidecar",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := srv.verifier.verify(ctx)
	if err != nil {
		t.Fatalf("expected success at staleness boundary (29s), got: %v", err)
	}

	// Capability signed 31 seconds ago — just past the 30-second boundary.
	past31s := time.Now().Add(-31 * time.Second).Unix()
	payload2 := fmt.Sprintf("READ:graph/entity/Component|%d", past31s)
	sig2 := base64.StdEncoding.EncodeToString(ed25519.Sign(scPriv, []byte(payload2)))
	md2 := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, "READ:graph/entity/Component",
		flowmeta.MetadataKeyCapabilitiesSignature, sig2,
		flowmeta.MetadataKeyCapabilitiesSignedAt, fmt.Sprintf("%d", past31s),
		flowmeta.MetadataKeyCapabilitiesSignedBy, "sidecar",
	)
	ctx2 := metadata.NewIncomingContext(context.Background(), md2)
	_, err2 := srv.verifier.verify(ctx2)
	if err2 == nil {
		t.Fatal("expected error for stale capability (31s past), got nil")
	}
	if status.Code(err2) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied for 31s past, got %v", status.Code(err2))
	}
}

func TestCapability_StalenessBoundary_NegativeWindow(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	// Negative staleness window disables the check.
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		-1*time.Second, "test-ns", 30*time.Minute, 100000)

	// Very old timestamp — would be stale with a positive window.
	oldTime := time.Now().Add(-24 * time.Hour).Unix()
	payload := fmt.Sprintf("READ:graph/entity/Component|%d", oldTime)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(scPriv, []byte(payload)))
	md := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, "READ:graph/entity/Component",
		flowmeta.MetadataKeyCapabilitiesSignature, sig,
		flowmeta.MetadataKeyCapabilitiesSignedAt, fmt.Sprintf("%d", oldTime),
		flowmeta.MetadataKeyCapabilitiesSignedBy, "sidecar",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := srv.verifier.verify(ctx)
	if err != nil {
		t.Fatalf("expected success with negative staleness window, got: %v", err)
	}
}

// TestCapability_FutureDatedSignedAtRejected pins the anti-replay boundary for
// a future-dated x-flow-capabilities-signed-at (capability.go): the staleness
// check is two-sided — an attestation signed in the future (elapsed < 0) is
// stale just as one past the window (elapsed > window) is — so a captured
// attestation replayed with a forged future timestamp can never outlive the
// anti-replay window (SPEC error table "Stale capability signature
// (anti-replay)": missing, malformed, or expired).
func TestCapability_FutureDatedSignedAtRejected(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)

	// Signed one hour in the future: time.Since(future) is negative.
	future := time.Now().Add(time.Hour).Unix()
	payload := fmt.Sprintf("READ:graph/entity/Component|%d", future)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(scPriv, []byte(payload)))
	md := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, "READ:graph/entity/Component",
		flowmeta.MetadataKeyCapabilitiesSignature, sig,
		flowmeta.MetadataKeyCapabilitiesSignedAt, fmt.Sprintf("%d", future),
		flowmeta.MetadataKeyCapabilitiesSignedBy, "sidecar",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := srv.verifier.verify(ctx)
	if err == nil {
		t.Fatal("expected error for future-dated signed-at, got nil")
	}
	if status.Code(err) != codes.PermissionDenied ||
		status.Convert(err).Message() != capabilityStaleMsg {
		t.Fatalf("expected PermissionDenied stale-capability, got %v", err)
	}
}

func TestCapability_MissingSignedAt(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)

	// Build metadata without the signed-at key (or with an empty value).
	payload := "READ:graph/entity/Component|1234567890"
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(scPriv, []byte(payload)))
	md := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, "READ:graph/entity/Component",
		flowmeta.MetadataKeyCapabilitiesSignature, sig,
		flowmeta.MetadataKeyCapabilitiesSignedBy, "sidecar",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := srv.verifier.verify(ctx)
	if err == nil {
		t.Fatal("expected error for missing signed-at, got nil")
	}
	if status.Code(err) != codes.PermissionDenied ||
		status.Convert(err).Message() != capabilityStaleMsg {
		t.Fatalf("expected PermissionDenied stale-capability, got %v", err)
	}
}

func TestCapability_EmptySignedAt(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	base, _ := openTestStore(t)
	t.Cleanup(func() { _ = base.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(base, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)

	md := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, "READ:graph/entity/Component",
		flowmeta.MetadataKeyCapabilitiesSignature, base64.StdEncoding.EncodeToString([]byte("fake")),
		flowmeta.MetadataKeyCapabilitiesSignedAt, "",
		flowmeta.MetadataKeyCapabilitiesSignedBy, "sidecar",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := srv.verifier.verify(ctx)
	if err == nil {
		t.Fatal("expected error for empty signed-at, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

func TestCapability_MalformedSignedAt(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)

	// signed-at is present but non-numeric ("abc"): the empty/len==0 guard is
	// bypassed, the (assumed valid) signature verifies over the raw payload
	// including "abc", and only then does ParseInt fail. Assert the resulting
	// PERMISSION_DENIED for the malformed signed-at anti-replay branch.
	payload := "READ:graph/entity/Component|abc"
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(scPriv, []byte(payload)))
	md := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, "READ:graph/entity/Component",
		flowmeta.MetadataKeyCapabilitiesSignature, sig,
		flowmeta.MetadataKeyCapabilitiesSignedBy, "sidecar",
		flowmeta.MetadataKeyCapabilitiesSignedAt, "abc",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := srv.verifier.verify(ctx)
	if err == nil {
		t.Fatal("expected error for malformed signed-at, got nil")
	}
	if status.Code(err) != codes.PermissionDenied ||
		status.Convert(err).Message() != capabilityStaleMsg {
		t.Fatalf("expected PermissionDenied stale-capability, got %v", err)
	}
}
