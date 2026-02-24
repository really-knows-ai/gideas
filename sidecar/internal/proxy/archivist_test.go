package proxy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
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
		Content:          []byte("test-content"),
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
	if string(resp.GetContent()) != "test-content" {
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
		WorkitemId:       "parent-wi",
		ArtefactId:       "doc",
		TargetWorkitemId: "child-wi",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the target_workitem_id was forwarded to the backend.
	if capture.lastGetReq.GetTargetWorkitemId() != "child-wi" {
		t.Fatalf("expected target_workitem_id=child-wi, got %q",
			capture.lastGetReq.GetTargetWorkitemId())
	}
	if string(resp.GetContent()) != "test-content" {
		t.Fatalf("expected passthrough content, got %q", string(resp.GetContent()))
	}
}

func TestArchivistProxy_ListArtefacts_ForwardsTargetWorkitemID(t *testing.T) {
	proxy, capture := setupArchivistProxy(t)

	resp, err := proxy.ListArtefacts(context.Background(), &flowv1.ListArtefactsRequest{
		WorkitemId:       "parent-wi",
		TargetWorkitemId: "child-wi",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the target_workitem_id was forwarded.
	if capture.lastListReq.GetTargetWorkitemId() != "child-wi" {
		t.Fatalf("expected target_workitem_id=child-wi, got %q",
			capture.lastListReq.GetTargetWorkitemId())
	}
	if len(resp.GetArtefactRefs()) != 1 {
		t.Fatalf("expected 1 artefact ref, got %d", len(resp.GetArtefactRefs()))
	}
}
