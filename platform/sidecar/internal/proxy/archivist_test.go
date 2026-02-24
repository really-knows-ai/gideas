package proxy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/sidecar/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	testContentStr = "test-content"
	childWIStr     = "child-wi"
	parentWIStr    = "parent-wi"
)

// captureArchivistServer captures the StoreArtefact request to verify
// that the Sidecar computed the content hash.
type captureArchivistServer struct {
	flowv1.UnimplementedArchivistServiceServer
	lastStoreReq *flowv1.StoreArtefactRequest
	lastGetReq   *flowv1.GetArtefactRequest
	lastListReq  *flowv1.ListArtefactsRequest
	capturedMD   metadata.MD
}

func (s *captureArchivistServer) StoreArtefact(
	ctx context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.lastStoreReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.StoreArtefactResponse{
		VersionHash:  req.GetContentHash(),
		IsNewVersion: true,
	}, nil
}

func (s *captureArchivistServer) GetArtefact(
	ctx context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	s.lastGetReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.GetArtefactResponse{
		Content:          []byte(testContentStr),
		VersionHash:      "test-hash",
		GovernedArtefact: "txt",
	}, nil
}

func (s *captureArchivistServer) LinkRuling(
	ctx context.Context, req *flowv1.LinkRulingRequest,
) (*flowv1.LinkRulingResponse, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.LinkRulingResponse{
		UpdatedItem: &flowv1.FeedbackItem{
			Id:           req.GetFeedbackId(),
			LinkedRuling: req.GetLawId(),
			State:        flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
		},
	}, nil
}

func (s *captureArchivistServer) ListArtefacts(
	ctx context.Context, req *flowv1.ListArtefactsRequest,
) (*flowv1.ListArtefactsResponse, error) {
	s.lastListReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.ListArtefactsResponse{
		ArtefactRefs: []*flowv1.ArtefactRef{
			{Id: "doc1", GovernedArtefact: "txt"},
		},
	}, nil
}

func setupArchivistProxy(t *testing.T) (*ArchivistProxy, *captureArchivistServer) {
	t.Helper()

	capture := &captureArchivistServer{}
	conn := dialBufconn(t, func(srv *grpc.Server) {
		flowv1.RegisterArchivistServiceServer(srv, capture)
	})

	proxy := &ArchivistProxy{
		client: flowv1.NewArchivistServiceClient(conn),
		conn:   conn,
	}

	return proxy, capture
}

// fakeChildAuthorizer implements service.ChildAuthorizer for testing.
type fakeChildAuthorizer struct {
	decisions map[string]service.ChildAccessDecision // key: "parent:child"
}

func newFakeChildAuthorizer() *fakeChildAuthorizer {
	return &fakeChildAuthorizer{
		decisions: make(map[string]service.ChildAccessDecision),
	}
}

func (f *fakeChildAuthorizer) allow(parent, child string) {
	f.decisions[parent+":"+child] = service.ChildAccessAllowed
}

func (f *fakeChildAuthorizer) deny(parent, child string) {
	f.decisions[parent+":"+child] = service.ChildAccessDenied
}

func (f *fakeChildAuthorizer) unknown(parent, child string) {
	f.decisions[parent+":"+child] = service.ChildAccessUnknown
}

func (f *fakeChildAuthorizer) AuthorizeChildAccess(
	parentWorkitemID, targetWorkitemID string,
) service.ChildAccessDecision {
	key := parentWorkitemID + ":" + targetWorkitemID
	if d, ok := f.decisions[key]; ok {
		return d
	}
	return service.ChildAccessUnknown
}

func setupArchivistProxyWithAuth(
	t *testing.T, auth service.ChildAuthorizer,
) (*ArchivistProxy, *captureArchivistServer) {
	t.Helper()

	capture := &captureArchivistServer{}
	conn := dialBufconn(t, func(srv *grpc.Server) {
		flowv1.RegisterArchivistServiceServer(srv, capture)
	})

	proxy := &ArchivistProxy{
		client:    flowv1.NewArchivistServiceClient(conn),
		conn:      conn,
		childAuth: auth,
	}

	return proxy, capture
}

func TestArchivistProxy_StoreArtefact_ComputesHash(t *testing.T) {
	proxy, capture := setupArchivistProxy(t)

	content := []byte("Hello from Step 1")
	expectedHash := fmt.Sprintf("%x", sha256.Sum256(content))

	resp, err := proxy.StoreArtefact(context.Background(), &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "greeting",
		GovernedArtefact: "txt",
		Content:          content,
		ContentHash:      "node-supplied-hash-should-be-ignored",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the proxy computed the hash and forwarded it, ignoring node's hash.
	if capture.lastStoreReq.GetContentHash() != expectedHash {
		t.Fatalf("expected Sidecar-computed hash %q, got %q",
			expectedHash, capture.lastStoreReq.GetContentHash())
	}

	if resp.GetVersionHash() != expectedHash {
		t.Fatalf("expected version_hash=%q, got %q", expectedHash, resp.GetVersionHash())
	}
}

func TestArchivistProxy_StoreArtefact_ForwardsFields(t *testing.T) {
	proxy, capture := setupArchivistProxy(t)

	content := []byte("data")
	_, err := proxy.StoreArtefact(context.Background(), &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-2",
		ArtefactId:       "report",
		GovernedArtefact: "json",
		Content:          content,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capture.lastStoreReq.GetWorkitemId() != "wi-2" {
		t.Fatalf("expected workitem_id=wi-2, got %q", capture.lastStoreReq.GetWorkitemId())
	}
	if capture.lastStoreReq.GetArtefactId() != "report" {
		t.Fatalf("expected artefact_id=report, got %q", capture.lastStoreReq.GetArtefactId())
	}
	if capture.lastStoreReq.GetGovernedArtefact() != "json" {
		t.Fatalf("expected governed_artefact=json, got %q", capture.lastStoreReq.GetGovernedArtefact())
	}
	if string(capture.lastStoreReq.GetContent()) != "data" {
		t.Fatalf("expected content=data, got %q", string(capture.lastStoreReq.GetContent()))
	}
}

func TestArchivistProxy_GetArtefact_Passthrough(t *testing.T) {
	proxy, capture := setupArchivistProxy(t)

	resp, err := proxy.GetArtefact(context.Background(), &flowv1.GetArtefactRequest{
		WorkitemId: "wi-1",
		ArtefactId: "greeting",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capture.lastGetReq.GetWorkitemId() != "wi-1" {
		t.Fatal("expected workitem_id to be forwarded")
	}
	if string(resp.GetContent()) != testContentStr {
		t.Fatalf("expected passthrough content, got %q", string(resp.GetContent()))
	}
}

func TestArchivistProxy_PropagatesMetadata(t *testing.T) {
	proxy, capture := setupArchivistProxy(t)

	md := metadata.Pairs("x-flow-workitem-id", "wi-meta-test")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId: "wi-1",
		ArtefactId: "greeting",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vals := capture.capturedMD.Get("x-flow-workitem-id")
	if len(vals) != 1 || vals[0] != "wi-meta-test" {
		t.Fatalf("expected metadata propagation, got %v", vals)
	}
}

func TestArchivistProxy_LinkRuling_Passthrough(t *testing.T) {
	proxy, _ := setupArchivistProxy(t)

	resp, err := proxy.LinkRuling(context.Background(), &flowv1.LinkRulingRequest{
		WorkitemId: "wi-1",
		FeedbackId: "fb-1",
		LawId:      "law-001",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	item := resp.GetUpdatedItem()
	if item.GetId() != "fb-1" {
		t.Fatalf("expected id=fb-1 in response, got %q", item.GetId())
	}
	if item.GetLinkedRuling() != "law-001" {
		t.Fatalf("expected linked_ruling=law-001, got %q", item.GetLinkedRuling())
	}
}

func TestArchivistProxy_LinkRuling_PropagatesMetadata(t *testing.T) {
	proxy, capture := setupArchivistProxy(t)

	md := metadata.Pairs("x-flow-workitem-id", "wi-ruling-test")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := proxy.LinkRuling(ctx, &flowv1.LinkRulingRequest{
		WorkitemId: "wi-1",
		FeedbackId: "fb-1",
		LawId:      "law-001",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vals := capture.capturedMD.Get("x-flow-workitem-id")
	if len(vals) != 1 || vals[0] != "wi-ruling-test" {
		t.Fatalf("expected metadata propagation, got %v", vals)
	}
}

// ---------------------------------------------------------------------------
// Cross-Workitem forwarding tests (Phase 7D)
// ---------------------------------------------------------------------------

func TestArchivistProxy_GetArtefact_ForwardsTargetWorkitemID(t *testing.T) {
	proxy, capture := setupArchivistProxy(t)

	resp, err := proxy.GetArtefact(context.Background(), &flowv1.GetArtefactRequest{
		WorkitemId:       parentWIStr,
		ArtefactId:       "doc",
		TargetWorkitemId: childWIStr,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the target_workitem_id was forwarded to the backend.
	if capture.lastGetReq.GetTargetWorkitemId() != childWIStr {
		t.Fatalf("expected target_workitem_id=%s, got %q",
			childWIStr, capture.lastGetReq.GetTargetWorkitemId())
	}
	if string(resp.GetContent()) != testContentStr {
		t.Fatalf("expected passthrough content, got %q", string(resp.GetContent()))
	}
}

func TestArchivistProxy_ListArtefacts_ForwardsTargetWorkitemID(t *testing.T) {
	proxy, capture := setupArchivistProxy(t)

	resp, err := proxy.ListArtefacts(context.Background(), &flowv1.ListArtefactsRequest{
		WorkitemId:       parentWIStr,
		TargetWorkitemId: childWIStr,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the target_workitem_id was forwarded.
	if capture.lastListReq.GetTargetWorkitemId() != childWIStr {
		t.Fatalf("expected target_workitem_id=%s, got %q",
			childWIStr, capture.lastListReq.GetTargetWorkitemId())
	}
	if len(resp.GetArtefactRefs()) != 1 {
		t.Fatalf("expected 1 artefact ref, got %d", len(resp.GetArtefactRefs()))
	}
}

// ---------------------------------------------------------------------------
// Phase 8: Cross-Workitem authorization tests
// ---------------------------------------------------------------------------

// --- GetArtefact authorization ---

func TestArchivistProxy_GetArtefact_CrossWorkitem_Allowed(t *testing.T) {
	auth := newFakeChildAuthorizer()
	auth.allow(parentWIStr, childWIStr)
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
	auth := newFakeChildAuthorizer()
	auth.deny(parentWIStr, "rogue-wi")
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
	auth := newFakeChildAuthorizer()
	auth.unknown(parentWIStr, childWIStr)
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
	auth := newFakeChildAuthorizer()
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
	auth := newFakeChildAuthorizer()
	auth.allow(parentWIStr, childWIStr)
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
	auth := newFakeChildAuthorizer()
	auth.deny(parentWIStr, "rogue-wi")
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
	auth := newFakeChildAuthorizer()
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
	auth := newFakeChildAuthorizer()
	auth.allow(parentWIStr, childWIStr)
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
	auth := newFakeChildAuthorizer()
	auth.deny(parentWIStr, "rogue-wi")
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
	auth := newFakeChildAuthorizer()
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
