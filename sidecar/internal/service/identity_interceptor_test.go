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
// IdentityInterceptor Tests
// ---------------------------------------------------------------------------

func TestIdentityInterceptor_InjectsSessionIdentity(t *testing.T) {
	resolver := &fakeResolver{
		sessions: map[string]*SessionIdentity{
			"wi-42": {FlowID: "flow-A", WorkitemID: "wi-42", NodeID: "node-X"},
		},
	}
	interceptor := IdentityInterceptor(resolver, "READ:artefact,WRITE:artefact")

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

	assertMDValue(t, enrichedMD, MetadataKeyFlowID, "flow-A")
	assertMDValue(t, enrichedMD, MetadataKeyWorkitemID, "wi-42")
	assertMDValue(t, enrichedMD, MetadataKeyNodeID, "node-X")
	assertMDValue(t, enrichedMD, MetadataKeyCapabilities, "READ:artefact,WRITE:artefact")
}

func TestIdentityInterceptor_OverwritesNodeSuppliedValues(t *testing.T) {
	resolver := &fakeResolver{
		sessions: map[string]*SessionIdentity{
			"wi-1": {FlowID: "real-flow", WorkitemID: "wi-1", NodeID: "real-node"},
		},
	}
	interceptor := IdentityInterceptor(resolver, "READ:law")

	// Node attempts to spoof flow_id, node_id, and capabilities.
	md := metadata.Pairs(
		MetadataKeyWorkitemID, "wi-1",
		MetadataKeyFlowID, "spoofed-flow",
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
	assertMDValue(t, enrichedMD, MetadataKeyFlowID, "real-flow")
	assertMDValue(t, enrichedMD, MetadataKeyNodeID, "real-node")
	assertMDValue(t, enrichedMD, MetadataKeyCapabilities, "READ:law")
}

func TestIdentityInterceptor_NoMetadata_PassesThrough(t *testing.T) {
	resolver := &fakeResolver{sessions: map[string]*SessionIdentity{}}
	interceptor := IdentityInterceptor(resolver, "")

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

func TestIdentityInterceptor_NoWorkitemID_PassesThrough(t *testing.T) {
	resolver := &fakeResolver{sessions: map[string]*SessionIdentity{}}
	interceptor := IdentityInterceptor(resolver, "")

	// Incoming metadata without workitem ID.
	md := metadata.Pairs("x-other-key", "value")
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

	// Metadata should be unchanged (no flow_id or node_id injected).
	inMD, ok := metadata.FromIncomingContext(capturedCtx)
	if !ok {
		t.Fatal("expected incoming metadata")
	}
	if vals := inMD.Get(MetadataKeyFlowID); len(vals) > 0 {
		t.Fatalf("flow_id should not be present, got %v", vals)
	}
	if vals := inMD.Get(MetadataKeyNodeID); len(vals) > 0 {
		t.Fatalf("node_id should not be present, got %v", vals)
	}
}

func TestIdentityInterceptor_NoSession_PassesThrough(t *testing.T) {
	resolver := &fakeResolver{sessions: map[string]*SessionIdentity{}}
	interceptor := IdentityInterceptor(resolver, "")

	// Workitem ID present but no matching session (e.g. stale ID).
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

	// No enrichment should occur for unknown sessions.
	inMD, ok := metadata.FromIncomingContext(capturedCtx)
	if !ok {
		t.Fatal("expected incoming metadata")
	}
	if vals := inMD.Get(MetadataKeyFlowID); len(vals) > 0 {
		t.Fatalf("flow_id should not be present for unknown session, got %v", vals)
	}
}

func TestIdentityInterceptor_PreservesOtherMetadata(t *testing.T) {
	resolver := &fakeResolver{
		sessions: map[string]*SessionIdentity{
			"wi-1": {FlowID: "f", WorkitemID: "wi-1", NodeID: "n"},
		},
	}
	interceptor := IdentityInterceptor(resolver, "READ:artefact")

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
	assertMDValue(t, enrichedMD, MetadataKeyFlowID, "f")
	assertMDValue(t, enrichedMD, MetadataKeyNodeID, "n")
}

// ---------------------------------------------------------------------------
// LookupSession Tests
// ---------------------------------------------------------------------------

func TestSidecarServer_LookupSession_Found(t *testing.T) {
	srv := NewSidecarServer("node-1", "")

	// Manually add a session.
	sess, _ := newSession(context.Background(), "flow-A", "wi-1", "node-1", DefaultTimeout)
	defer sess.stop()
	srv.mu.Lock()
	srv.sessions["wi-1"] = sess
	srv.mu.Unlock()

	identity := srv.LookupSession("wi-1")
	if identity == nil {
		t.Fatal("expected non-nil identity")
	}
	if identity.FlowID != "flow-A" {
		t.Fatalf("expected FlowID=flow-A, got %s", identity.FlowID)
	}
	if identity.WorkitemID != "wi-1" {
		t.Fatalf("expected WorkitemID=wi-1, got %s", identity.WorkitemID)
	}
	if identity.NodeID != "node-1" {
		t.Fatalf("expected NodeID=node-1, got %s", identity.NodeID)
	}
}

func TestSidecarServer_LookupSession_NotFound(t *testing.T) {
	srv := NewSidecarServer("node-1", "")

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
