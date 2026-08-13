package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"

	flowmeta "github.com/foundry/flow/pkg/metadata"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// ---------------------------------------------------------------------------
// IdentityInterceptor Tests — Session Mode
// ---------------------------------------------------------------------------

func TestIdentityInterceptor_InjectsSessionIdentity(t *testing.T) {
	srv := NewSidecarServer("ns-A", "node-X", "")
	sess, _ := newSession(context.Background(), "wi-42", "node-X", DefaultTimeout)
	defer sess.stop()
	srv.mu.Lock()
	srv.sessions["wi-42"] = sess
	srv.mu.Unlock()

	interceptor := IdentityInterceptor(srv, "ns-A", "node-X", "READ:artefact,WRITE:artefact", nil)

	// Build incoming context with SDK-supplied workitem ID.
	md := metadata.Pairs(flowmeta.MetadataKeyWorkitemID, "wi-42")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	var capturedCtx context.Context
	handler := func(ctx context.Context, req any) (any, error) {
		capturedCtx = ctx
		return "ok", nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/flow.v1.OperatorService/SubmitResult"}
	resp, err := interceptor(ctx, nil, info, handler)
	if err != nil {
		t.Fatalf("interceptor error: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected resp=ok, got %v", resp)
	}

	// Verify the handler received enriched incoming metadata.
	enrichedMD, ok := metadata.FromIncomingContext(capturedCtx)
	if !ok {
		t.Fatal("expected incoming metadata in handler context")
	}

	assertMDValue(t, enrichedMD, flowmeta.MetadataKeyNamespace, "ns-A")
	assertMDValue(t, enrichedMD, flowmeta.MetadataKeyWorkitemID, "wi-42")
	assertMDValue(t, enrichedMD, flowmeta.MetadataKeyNodeID, "node-X")
	assertMDValue(t, enrichedMD, flowmeta.MetadataKeyCapabilities, "READ:artefact,WRITE:artefact")
}

func TestIdentityInterceptor_OverwritesNodeSuppliedValues(t *testing.T) {
	srv := NewSidecarServer("real-ns", "real-node", "")
	sess, _ := newSession(context.Background(), "wi-1", "real-node", DefaultTimeout)
	defer sess.stop()
	srv.mu.Lock()
	srv.sessions["wi-1"] = sess
	srv.mu.Unlock()

	interceptor := IdentityInterceptor(srv, "real-ns", "real-node", "READ:law", nil)

	// Node attempts to spoof namespace, node_id, and capabilities.
	md := metadata.Pairs(
		flowmeta.MetadataKeyWorkitemID, "wi-1",
		flowmeta.MetadataKeyNamespace, "spoofed-ns",
		flowmeta.MetadataKeyNodeID, "spoofed-node",
		flowmeta.MetadataKeyCapabilities, "WRITE:artefact,STAMP:artefact/x/y",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	var capturedCtx context.Context
	handler := func(ctx context.Context, req any) (any, error) {
		capturedCtx = ctx
		return nil, nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/test"}
	_, err := interceptor(ctx, nil, info, handler)
	if err != nil {
		t.Fatalf("interceptor error: %v", err)
	}

	enrichedMD, ok := metadata.FromIncomingContext(capturedCtx)
	if !ok {
		t.Fatal("expected incoming metadata")
	}

	// Spoofed values must be overwritten with authoritative session values.
	assertMDValue(t, enrichedMD, flowmeta.MetadataKeyNamespace, "real-ns")
	assertMDValue(t, enrichedMD, flowmeta.MetadataKeyNodeID, "real-node")
	assertMDValue(t, enrichedMD, flowmeta.MetadataKeyCapabilities, "READ:law")
}

// ---------------------------------------------------------------------------
// IdentityInterceptor Tests — Entry-Bound Fallback
// ---------------------------------------------------------------------------

func TestIdentityInterceptor_EntryBoundFallback(t *testing.T) {
	srv := NewSidecarServer("entry-ns", "entry-node", "")
	interceptor := IdentityInterceptor(srv, "entry-ns", "entry-node", "READ:artefact", nil)

	// No workitem ID in metadata — entry-bound call.
	md := metadata.Pairs("x-other-key", "value")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	var capturedCtx context.Context
	handler := func(ctx context.Context, req any) (any, error) {
		capturedCtx = ctx
		return nil, nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/flow.v1.OperatorService/CreateWorkitem"}
	_, err := interceptor(ctx, nil, info, handler)
	if err != nil {
		t.Fatalf("interceptor error: %v", err)
	}

	enrichedMD, ok := metadata.FromIncomingContext(capturedCtx)
	if !ok {
		t.Fatal("expected incoming metadata")
	}

	// Entry-bound fallback: namespace and node_id injected.
	assertMDValue(t, enrichedMD, flowmeta.MetadataKeyNamespace, "entry-ns")
	assertMDValue(t, enrichedMD, flowmeta.MetadataKeyNodeID, "entry-node")
	assertMDValue(t, enrichedMD, flowmeta.MetadataKeyCapabilities, "READ:artefact")

	// workitem_id should NOT be present.
	if vals := enrichedMD.Get(flowmeta.MetadataKeyWorkitemID); len(vals) > 0 {
		t.Fatalf("workitem_id should not be present in entry-bound fallback, got %v", vals)
	}

	// Custom metadata should be preserved.
	assertMDValue(t, enrichedMD, "x-other-key", "value")
}

func TestIdentityInterceptor_EntryBoundFallback_UnknownWorkitem(t *testing.T) {
	srv := NewSidecarServer("entry-ns", "entry-node", "")
	interceptor := IdentityInterceptor(srv, "entry-ns", "entry-node", "", nil)

	// Workitem ID present but no matching session — falls through to entry-bound.
	md := metadata.Pairs(flowmeta.MetadataKeyWorkitemID, "unknown-wi")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	var capturedCtx context.Context
	handler := func(ctx context.Context, req any) (any, error) {
		capturedCtx = ctx
		return nil, nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/test"}
	_, err := interceptor(ctx, nil, info, handler)
	if err != nil {
		t.Fatalf("interceptor error: %v", err)
	}

	enrichedMD, ok := metadata.FromIncomingContext(capturedCtx)
	if !ok {
		t.Fatal("expected incoming metadata")
	}

	// Entry-bound fallback should kick in.
	assertMDValue(t, enrichedMD, flowmeta.MetadataKeyNamespace, "entry-ns")
	assertMDValue(t, enrichedMD, flowmeta.MetadataKeyNodeID, "entry-node")
}

// ---------------------------------------------------------------------------
// IdentityInterceptor Tests — Pass-through (no namespace/nodeID)
// ---------------------------------------------------------------------------

func TestIdentityInterceptor_NoMetadata_NoNamespace_PassesThrough(t *testing.T) {
	srv := NewSidecarServer("", "", "")
	interceptor := IdentityInterceptor(srv, "", "", "", nil)

	ctx := context.Background()
	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return nil, nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/test"}
	_, err := interceptor(ctx, nil, info, handler)
	if err != nil {
		t.Fatalf("interceptor error: %v", err)
	}
	if !called {
		t.Fatal("handler should have been called")
	}
}

func TestIdentityInterceptor_PreservesOtherMetadata(t *testing.T) {
	srv := NewSidecarServer("ns-f", "n", "")
	sess, _ := newSession(context.Background(), "wi-1", "n", DefaultTimeout)
	defer sess.stop()
	srv.mu.Lock()
	srv.sessions["wi-1"] = sess
	srv.mu.Unlock()

	interceptor := IdentityInterceptor(srv, "ns-f", "n", "READ:artefact", nil)

	md := metadata.Pairs(
		flowmeta.MetadataKeyWorkitemID, "wi-1",
		"x-custom-header", "custom-value",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	var capturedCtx context.Context
	handler := func(ctx context.Context, req any) (any, error) {
		capturedCtx = ctx
		return nil, nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/test"}
	_, err := interceptor(ctx, nil, info, handler)
	if err != nil {
		t.Fatalf("interceptor error: %v", err)
	}

	enrichedMD, _ := metadata.FromIncomingContext(capturedCtx)

	// Custom metadata should be preserved.
	assertMDValue(t, enrichedMD, "x-custom-header", "custom-value")

	// Identity fields should be injected.
	assertMDValue(t, enrichedMD, flowmeta.MetadataKeyNamespace, "ns-f")
	assertMDValue(t, enrichedMD, flowmeta.MetadataKeyNodeID, "n")
}

// ---------------------------------------------------------------------------
// LookupSession Tests
// ---------------------------------------------------------------------------

func TestSidecarServer_LookupSession_Found(t *testing.T) {
	srv := NewSidecarServer("ns-A", "node-1", "")

	// Manually add a session.
	sess, _ := newSession(context.Background(), "wi-1", "node-1", DefaultTimeout)
	defer sess.stop()
	srv.mu.Lock()
	srv.sessions["wi-1"] = sess
	srv.mu.Unlock()

	identity := srv.LookupSession("wi-1")
	if identity == nil {
		t.Fatal("expected non-nil identity")
	}
	if identity.WorkitemID != "wi-1" {
		t.Fatalf("expected WorkitemID=wi-1, got %s", identity.WorkitemID)
	}
	if identity.NodeID != "node-1" {
		t.Fatalf("expected NodeID=node-1, got %s", identity.NodeID)
	}
}

func TestSidecarServer_LookupSession_NotFound(t *testing.T) {
	srv := NewSidecarServer("ns-A", "node-1", "")

	identity := srv.LookupSession("nonexistent")
	if identity != nil {
		t.Fatalf("expected nil identity for nonexistent session, got %+v", identity)
	}
}

// ---------------------------------------------------------------------------
// Capability signing tests (SPEC R3 / Capability Authorisation Chain)
// ---------------------------------------------------------------------------

func TestIdentityInterceptor_SignsCapabilities(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	srv := NewSidecarServer("ns", "node", "")
	interceptor := IdentityInterceptor(srv, "ns", "node", "READ:graph/entity/*", priv)

	// Entry-bound fallback path (no workitem session).
	var capturedCtx context.Context
	handler := func(ctx context.Context, req any) (any, error) {
		capturedCtx = ctx
		return "ok", nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/flow.v1.CartographerService/ExecuteCypher"}
	_, err = interceptor(context.Background(), nil, info, handler)
	if err != nil {
		t.Fatalf("interceptor error: %v", err)
	}

	enrichedMD, ok := metadata.FromIncomingContext(capturedCtx)
	if !ok {
		t.Fatal("expected incoming metadata")
	}

	caps := "READ:graph/entity/*"
	assertMDValue(t, enrichedMD, flowmeta.MetadataKeyCapabilities, caps)
	assertMDValue(t, enrichedMD, flowmeta.MetadataKeyCapabilitiesSignedBy, "sidecar")
	assertSignedCapabilities(t, pub, enrichedMD, caps)
}

func TestIdentityInterceptor_NilKey_NoSignature(t *testing.T) {
	srv := NewSidecarServer("ns", "node", "")
	interceptor := IdentityInterceptor(srv, "ns", "node", "READ:artefact", nil)

	var capturedCtx context.Context
	handler := func(ctx context.Context, req any) (any, error) {
		capturedCtx = ctx
		return nil, nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/test"}
	_, err := interceptor(context.Background(), nil, info, handler)
	if err != nil {
		t.Fatalf("interceptor error: %v", err)
	}

	enrichedMD, _ := metadata.FromIncomingContext(capturedCtx)
	assertMDValue(t, enrichedMD, flowmeta.MetadataKeyCapabilities, "READ:artefact")
	if vals := enrichedMD.Get(flowmeta.MetadataKeyCapabilitiesSignature); len(vals) != 0 {
		t.Fatalf("no signature expected without a signing key, got %v", vals)
	}
	if vals := enrichedMD.Get(flowmeta.MetadataKeyCapabilitiesSignedAt); len(vals) != 0 {
		t.Fatalf("no signed-at expected without a signing key, got %v", vals)
	}
}

func TestIdentityInterceptor_WrongLengthKey_FailsClosed(t *testing.T) {
	srv := NewSidecarServer("ns", "node", "")
	interceptor := IdentityInterceptor(srv, "ns", "node", "READ:artefact", []byte("too-short"))

	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return nil, nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/test"}
	_, err := interceptor(context.Background(), nil, info, handler)
	if err == nil {
		t.Fatal("expected error for a malformed signing key")
	}
	if called {
		t.Fatal("handler must not be called when the signing key is malformed")
	}
}

func TestIdentityStreamInterceptor_SignsCapabilities(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	srv := NewSidecarServer("ns", "node", "")
	interceptor := IdentityStreamInterceptor(srv, "ns", "node", "READ:graph/entity/*", priv)

	var capturedStream grpc.ServerStream
	handler := func(srv any, ss grpc.ServerStream) error {
		capturedStream = ss
		return nil
	}

	info := &grpc.StreamServerInfo{FullMethod: "/flow.v1.CartographerService/ExportGraph"}
	err = interceptor(nil, &identityTestStream{ctx: context.Background()}, info, handler)
	if err != nil {
		t.Fatalf("stream interceptor error: %v", err)
	}

	enrichedMD, ok := metadata.FromIncomingContext(capturedStream.Context())
	if !ok {
		t.Fatal("expected incoming metadata on the wrapped stream")
	}

	caps := "READ:graph/entity/*"
	assertMDValue(t, enrichedMD, flowmeta.MetadataKeyCapabilities, caps)
	assertMDValue(t, enrichedMD, flowmeta.MetadataKeyCapabilitiesSignedBy, "sidecar")
	assertSignedCapabilities(t, pub, enrichedMD, caps)
}

// identityTestStream is a minimal grpc.ServerStream for interceptor tests.
type identityTestStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *identityTestStream) Context() context.Context { return s.ctx }

// assertSignedCapabilities verifies the x-flow-capabilities-signature over
// "{caps}|{signed-at}" against the given public key.
func assertSignedCapabilities(t *testing.T, pub ed25519.PublicKey, md metadata.MD, caps string) {
	t.Helper()
	sigB64 := md.Get(flowmeta.MetadataKeyCapabilitiesSignature)
	signedAt := md.Get(flowmeta.MetadataKeyCapabilitiesSignedAt)
	if len(sigB64) != 1 || len(signedAt) != 1 {
		t.Fatalf("expected signature and signed-at metadata, got sig=%v at=%v", sigB64, signedAt)
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64[0])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if !ed25519.Verify(pub, []byte(caps+"|"+signedAt[0]), sig) {
		t.Fatal("signature does not verify against the sidecar public key")
	}
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func assertMDValue(t *testing.T, md metadata.MD, key, expected string) {
	t.Helper()
	vals := md.Get(key)
	if len(vals) != 1 {
		t.Fatalf("expected exactly 1 value for %s, got %v", key, vals)
	}
	if vals[0] != expected {
		t.Fatalf("expected %s=%s, got %s", key, expected, vals[0])
	}
}
