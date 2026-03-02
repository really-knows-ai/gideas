package service

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// ---------------------------------------------------------------------------
// Fake SessionResolver for testing
// ---------------------------------------------------------------------------

type fakeResolver struct {
	sessions map[string]*SessionIdentity
}

func (f *fakeResolver) LookupSession(workitemID string) *SessionIdentity {
	return f.sessions[workitemID]
}

// ---------------------------------------------------------------------------
// IdentityInterceptor Tests — Session Mode
// ---------------------------------------------------------------------------

func TestIdentityInterceptor_InjectsSessionIdentity(t *testing.T) {
	resolver := &fakeResolver{
		sessions: map[string]*SessionIdentity{
			"wi-42": {WorkitemID: "wi-42", NodeID: "node-X"},
		},
	}
	interceptor := IdentityInterceptor(resolver, "ns-A", "node-X", "READ:artefact,WRITE:artefact")

	// Build incoming context with SDK-supplied workitem ID.
	md := metadata.Pairs(MetadataKeyWorkitemID, "wi-42")
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

	assertMDValue(t, enrichedMD, MetadataKeyNamespace, "ns-A")
	assertMDValue(t, enrichedMD, MetadataKeyWorkitemID, "wi-42")
	assertMDValue(t, enrichedMD, MetadataKeyNodeID, "node-X")
	assertMDValue(t, enrichedMD, MetadataKeyCapabilities, "READ:artefact,WRITE:artefact")
}

func TestIdentityInterceptor_OverwritesNodeSuppliedValues(t *testing.T) {
	resolver := &fakeResolver{
		sessions: map[string]*SessionIdentity{
			"wi-1": {WorkitemID: "wi-1", NodeID: "real-node"},
		},
	}
	interceptor := IdentityInterceptor(resolver, "real-ns", "real-node", "READ:law")

	// Node attempts to spoof namespace, node_id, and capabilities.
	md := metadata.Pairs(
		MetadataKeyWorkitemID, "wi-1",
		MetadataKeyNamespace, "spoofed-ns",
		MetadataKeyNodeID, "spoofed-node",
		MetadataKeyCapabilities, "WRITE:artefact,STAMP:artefact/x/y",
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
	assertMDValue(t, enrichedMD, MetadataKeyNamespace, "real-ns")
	assertMDValue(t, enrichedMD, MetadataKeyNodeID, "real-node")
	assertMDValue(t, enrichedMD, MetadataKeyCapabilities, "READ:law")
}

// ---------------------------------------------------------------------------
// IdentityInterceptor Tests — Entry-Bound Fallback
// ---------------------------------------------------------------------------

func TestIdentityInterceptor_EntryBoundFallback(t *testing.T) {
	resolver := &fakeResolver{sessions: map[string]*SessionIdentity{}}
	interceptor := IdentityInterceptor(resolver, "entry-ns", "entry-node", "READ:artefact")

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
	assertMDValue(t, enrichedMD, MetadataKeyNamespace, "entry-ns")
	assertMDValue(t, enrichedMD, MetadataKeyNodeID, "entry-node")
	assertMDValue(t, enrichedMD, MetadataKeyCapabilities, "READ:artefact")

	// workitem_id should NOT be present.
	if vals := enrichedMD.Get(MetadataKeyWorkitemID); len(vals) > 0 {
		t.Fatalf("workitem_id should not be present in entry-bound fallback, got %v", vals)
	}

	// Custom metadata should be preserved.
	assertMDValue(t, enrichedMD, "x-other-key", "value")
}

func TestIdentityInterceptor_EntryBoundFallback_UnknownWorkitem(t *testing.T) {
	resolver := &fakeResolver{sessions: map[string]*SessionIdentity{}}
	interceptor := IdentityInterceptor(resolver, "entry-ns", "entry-node", "")

	// Workitem ID present but no matching session — falls through to entry-bound.
	md := metadata.Pairs(MetadataKeyWorkitemID, "unknown-wi")
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
	assertMDValue(t, enrichedMD, MetadataKeyNamespace, "entry-ns")
	assertMDValue(t, enrichedMD, MetadataKeyNodeID, "entry-node")
}

// ---------------------------------------------------------------------------
// IdentityInterceptor Tests — Pass-through (no namespace/nodeID)
// ---------------------------------------------------------------------------

func TestIdentityInterceptor_NoMetadata_NoNamespace_PassesThrough(t *testing.T) {
	resolver := &fakeResolver{sessions: map[string]*SessionIdentity{}}
	interceptor := IdentityInterceptor(resolver, "", "", "")

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
	resolver := &fakeResolver{
		sessions: map[string]*SessionIdentity{
			"wi-1": {WorkitemID: "wi-1", NodeID: "n"},
		},
	}
	interceptor := IdentityInterceptor(resolver, "ns-f", "n", "READ:artefact")

	md := metadata.Pairs(
		MetadataKeyWorkitemID, "wi-1",
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
	assertMDValue(t, enrichedMD, MetadataKeyNamespace, "ns-f")
	assertMDValue(t, enrichedMD, MetadataKeyNodeID, "n")
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
