package proxy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

// captureArchivistServer captures the StoreArtefact request to verify
// that the Sidecar computed the content hash.
type captureArchivistServer struct {
	flowv1.UnimplementedArchivistServiceServer
	lastStoreReq *flowv1.StoreArtefactRequest
	lastGetReq   *flowv1.GetArtefactRequest
	capturedMD   metadata.MD
}

func (s *captureArchivistServer) StoreArtefact(ctx context.Context, req *flowv1.StoreArtefactRequest) (*flowv1.StoreArtefactResponse, error) {
	s.lastStoreReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.StoreArtefactResponse{
		VersionHash:  req.GetContentHash(),
		IsNewVersion: true,
	}, nil
}

func (s *captureArchivistServer) GetArtefact(ctx context.Context, req *flowv1.GetArtefactRequest) (*flowv1.GetArtefactResponse, error) {
	s.lastGetReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.GetArtefactResponse{
		Content:     []byte("test-content"),
		VersionHash: "test-hash",
		Kind:        "txt",
	}, nil
}

func setupArchivistProxy(t *testing.T) (*ArchivistProxy, *captureArchivistServer) {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	capture := &captureArchivistServer{}
	flowv1.RegisterArchivistServiceServer(srv, capture)

	go func() {
		if err := srv.Serve(lis); err != nil {
			t.Logf("bufconn server error: %v", err)
		}
	}()
	t.Cleanup(func() {
		srv.Stop()
		lis.Close()
	})

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial bufconn: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

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
		WorkitemId:  "wi-1",
		ArtefactId:  "greeting",
		Kind:        "txt",
		Content:     content,
		ContentHash: "node-supplied-hash-should-be-ignored",
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
		WorkitemId: "wi-2",
		ArtefactId: "report",
		Kind:       "json",
		Content:    content,
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
	if capture.lastStoreReq.GetKind() != "json" {
		t.Fatalf("expected kind=json, got %q", capture.lastStoreReq.GetKind())
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
