package proxy

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/sidecar/internal/service"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Phase 8: Cross-Workitem authorization tests
// ---------------------------------------------------------------------------

// --- GetArtefact authorization ---

func TestArchivistProxy_GetArtefact_CrossWorkitem_Allowed(t *testing.T) {
	auth := newSidecarWithSession(t)
	auth.TrackChild(parentWIStr, childWIStr)
	proxy, capture := setupArchivistProxyWithAuth(t, auth)

	md := metadata.Pairs("x-flow-workitem-id", parentWIStr)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := proxy.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId:       parentWIStr,
		ArtefactId:       "doc",
		TargetWorkitemId: childWIStr,
	})
	if err != nil {
		t.Fatalf("expected allowed, got error: %v", err)
	}
	if string(resp.GetContent()) != testContentStr {
		t.Fatalf("expected passthrough content, got %q", string(resp.GetContent()))
	}
	if capture.lastGetReq.GetTargetWorkitemId() != childWIStr {
		t.Fatalf("expected target forwarded, got %q",
			capture.lastGetReq.GetTargetWorkitemId())
	}
}

func TestArchivistProxy_GetArtefact_CrossWorkitem_Denied(t *testing.T) {
	auth := newSidecarWithSession(t)
	auth.TrackChild(parentWIStr, "other-child") // Session has children, but not "rogue-wi"
	proxy, _ := setupArchivistProxyWithAuth(t, auth)

	md := metadata.Pairs("x-flow-workitem-id", parentWIStr)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId:       parentWIStr,
		ArtefactId:       "doc",
		TargetWorkitemId: "rogue-wi",
	})
	if err == nil {
		t.Fatal("expected PermissionDenied, got nil")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestArchivistProxy_GetArtefact_CrossWorkitem_Unknown_PassesThrough(t *testing.T) {
	auth := newSidecarWithSession(t)
	// No children added — session exists but has no children → ChildAccessUnknown.
	proxy, capture := setupArchivistProxyWithAuth(t, auth)

	md := metadata.Pairs("x-flow-workitem-id", parentWIStr)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := proxy.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId:       parentWIStr,
		ArtefactId:       "doc",
		TargetWorkitemId: childWIStr,
	})
	if err != nil {
		t.Fatalf("expected passthrough for unknown, got error: %v", err)
	}
	if string(resp.GetContent()) != testContentStr {
		t.Fatalf("expected passthrough content")
	}
	if capture.lastGetReq.GetTargetWorkitemId() != childWIStr {
		t.Fatalf("expected target forwarded")
	}
}

func TestArchivistProxy_GetArtefact_NoTargetWorkitem_NoAuth(t *testing.T) {
	auth := service.NewSidecarServer("ns", "node", "")
	proxy, _ := setupArchivistProxyWithAuth(t, auth)

	md := metadata.Pairs("x-flow-workitem-id", parentWIStr)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId: parentWIStr,
		ArtefactId: "doc",
	})
	if err != nil {
		t.Fatalf("expected no auth for normal read, got error: %v", err)
	}
}

func TestArchivistProxy_GetArtefact_NilAuth_PassesThrough(t *testing.T) {
	proxy, _ := setupArchivistProxy(t)

	md := metadata.Pairs("x-flow-workitem-id", parentWIStr)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId:       parentWIStr,
		ArtefactId:       "doc",
		TargetWorkitemId: childWIStr,
	})
	if err != nil {
		t.Fatalf("expected passthrough with nil auth, got error: %v", err)
	}
}

// --- ListArtefacts authorization ---

func TestArchivistProxy_ListArtefacts_CrossWorkitem_Allowed(t *testing.T) {
	auth := newSidecarWithSession(t)
	auth.TrackChild(parentWIStr, childWIStr)
	proxy, capture := setupArchivistProxyWithAuth(t, auth)

	md := metadata.Pairs("x-flow-workitem-id", parentWIStr)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := proxy.ListArtefacts(ctx, &flowv1.ListArtefactsRequest{
		WorkitemId:       parentWIStr,
		TargetWorkitemId: childWIStr,
	})
	if err != nil {
		t.Fatalf("expected allowed, got error: %v", err)
	}
	if len(resp.GetArtefactRefs()) != 1 {
		t.Fatalf("expected 1 artefact ref")
	}
	if capture.lastListReq.GetTargetWorkitemId() != childWIStr {
		t.Fatalf("expected target forwarded")
	}
}

func TestArchivistProxy_ListArtefacts_CrossWorkitem_Denied(t *testing.T) {
	auth := newSidecarWithSession(t)
	auth.TrackChild(parentWIStr, "other-child")
	proxy, _ := setupArchivistProxyWithAuth(t, auth)

	md := metadata.Pairs("x-flow-workitem-id", parentWIStr)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.ListArtefacts(ctx, &flowv1.ListArtefactsRequest{
		WorkitemId:       parentWIStr,
		TargetWorkitemId: "rogue-wi",
	})
	if err == nil {
		t.Fatal("expected PermissionDenied")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

// --- StoreArtefact authorization ---

func TestArchivistProxy_StoreArtefact_SameWorkitem_NoAuth(t *testing.T) {
	auth := service.NewSidecarServer("ns", "node", "")
	proxy, _ := setupArchivistProxyWithAuth(t, auth)

	md := metadata.Pairs("x-flow-workitem-id", "wi-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          []byte("data"),
	})
	if err != nil {
		t.Fatalf("expected no auth for same-workitem write, got error: %v", err)
	}
}

func TestArchivistProxy_StoreArtefact_ChildWorkitem_Allowed(t *testing.T) {
	auth := newSidecarWithSession(t)
	auth.TrackChild(parentWIStr, childWIStr)
	proxy, capture := setupArchivistProxyWithAuth(t, auth)

	md := metadata.Pairs("x-flow-workitem-id", parentWIStr)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       childWIStr,
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          []byte("child data"),
	})
	if err != nil {
		t.Fatalf("expected allowed, got error: %v", err)
	}
	if capture.lastStoreReq.GetWorkitemId() != childWIStr {
		t.Fatalf("expected child workitem forwarded")
	}
}

func TestArchivistProxy_StoreArtefact_ChildWorkitem_Denied(t *testing.T) {
	auth := newSidecarWithSession(t)
	auth.TrackChild(parentWIStr, "other-child")
	proxy, _ := setupArchivistProxyWithAuth(t, auth)

	md := metadata.Pairs("x-flow-workitem-id", parentWIStr)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "rogue-wi",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          []byte("rogue data"),
	})
	if err == nil {
		t.Fatal("expected PermissionDenied")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestArchivistProxy_StoreArtefact_ChildWorkitem_Unknown_Denied(t *testing.T) {
	auth := newSidecarWithSession(t)
	// No children — ChildAccessUnknown but StoreArtefact treats Unknown as Denied for writes.
	proxy, _ := setupArchivistProxyWithAuth(t, auth)

	md := metadata.Pairs("x-flow-workitem-id", parentWIStr)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "unknown-wi",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          []byte("data"),
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for unknown cross-Workitem write")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestArchivistProxy_StoreArtefact_NilAuth_PassesThrough(t *testing.T) {
	proxy, _ := setupArchivistProxy(t)

	md := metadata.Pairs("x-flow-workitem-id", parentWIStr)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "other-wi",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          []byte("data"),
	})
	if err != nil {
		t.Fatalf("expected passthrough with nil auth, got error: %v", err)
	}
}
