package flow

import (
	"context"
	"net"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

// ---------------------------------------------------------------------------
// Spy server — captures incoming metadata for assertions
// ---------------------------------------------------------------------------

// spyServer implements the three gRPC services and records the metadata it
// receives. This lets us assert that the SDK's interceptor injects the
// correct workitem_id header.
type spyServer struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer

	// lastMD is the metadata captured from the most recent call.
	lastMD metadata.MD
}

func (s *spyServer) Heartbeat(ctx context.Context, req *flowv1.HeartbeatRequest) (*flowv1.HeartbeatResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *spyServer) SubmitResult(ctx context.Context, req *flowv1.SubmitResultRequest) (*flowv1.SubmitResultResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

func (s *spyServer) GetArtefact(ctx context.Context, req *flowv1.GetArtefactRequest) (*flowv1.GetArtefactResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.GetArtefactResponse{
		Content:     []byte("test-content"),
		VersionHash: "test-hash",
		Kind:        "test-kind",
	}, nil
}

// ---------------------------------------------------------------------------
// Test helper — starts a bufconn gRPC server and returns a connected Client
// ---------------------------------------------------------------------------

// testEnv bundles a live Client wired to an in-process gRPC server via
// bufconn so tests never touch the real network.
type testEnv struct {
	client *Client
	spy    *spyServer
	srv    *grpc.Server
}

func setupTestEnv(t *testing.T, workitemID string) *testEnv {
	t.Helper()

	lis := bufconn.Listen(bufSize)
	spy := &spyServer{}

	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)

	go func() {
		if err := srv.Serve(lis); err != nil {
			// Server stopped — expected during cleanup.
		}
	}()

	// Use bufconn dialer to create an in-memory connection.
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(workitemContextInterceptor(workitemID)),
	)
	if err != nil {
		t.Fatalf("failed to dial bufconn: %v", err)
	}

	client := &Client{
		conn:       conn,
		workitemID: workitemID,
		Sidecar:    flowv1.NewSidecarServiceClient(conn),
		Operator:   flowv1.NewOperatorServiceClient(conn),
		Archivist:  flowv1.NewArchivistServiceClient(conn),
	}

	t.Cleanup(func() {
		client.Close()
		srv.GracefulStop()
	})

	return &testEnv{client: client, spy: spy, srv: srv}
}

// ---------------------------------------------------------------------------
// Tests — Configuration
// ---------------------------------------------------------------------------

func TestNewClient_DefaultAddress(t *testing.T) {
	cfg := &clientConfig{sidecarAddr: DefaultSidecarAddress}

	// Apply zero options — the default should remain.
	opts := []ClientOption{}
	for _, o := range opts {
		o(cfg)
	}

	if cfg.sidecarAddr != "localhost:50051" {
		t.Fatalf("expected default address localhost:50051, got %s", cfg.sidecarAddr)
	}
}

func TestNewClient_CustomAddress(t *testing.T) {
	cfg := &clientConfig{sidecarAddr: DefaultSidecarAddress}
	WithSidecarAddress("custom:9090")(cfg)

	if cfg.sidecarAddr != "custom:9090" {
		t.Fatalf("expected custom address custom:9090, got %s", cfg.sidecarAddr)
	}
}

func TestClient_WorkitemID(t *testing.T) {
	env := setupTestEnv(t, "wid-123")
	if env.client.WorkitemID() != "wid-123" {
		t.Fatalf("expected WorkitemID wid-123, got %s", env.client.WorkitemID())
	}
}

// ---------------------------------------------------------------------------
// Tests — Metadata Injection (The Critical Path)
// ---------------------------------------------------------------------------

func TestHeartbeat_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-abc-789"
	env := setupTestEnv(t, wantID)

	ack, err := env.client.Heartbeat(context.Background())
	if err != nil {
		t.Fatalf("Heartbeat() returned error: %v", err)
	}
	if !ack {
		t.Fatal("Heartbeat() was not acknowledged")
	}

	// THE critical assertion: the interceptor must inject the workitem_id
	// into the gRPC metadata that the server sees.
	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 {
		t.Fatal("metadata x-flow-workitem-id was NOT present in the server-side context — interceptor is broken")
	}
	if got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %q, want %q", got[0], wantID)
	}
}

func TestComplete_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-complete-456"
	env := setupTestEnv(t, wantID)

	accepted, err := env.client.Complete(context.Background(), "next-node")
	if err != nil {
		t.Fatalf("Complete() returned error: %v", err)
	}
	if !accepted {
		t.Fatal("Complete() was not accepted")
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 {
		t.Fatal("metadata x-flow-workitem-id was NOT present on SubmitResult call")
	}
	if got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %q, want %q", got[0], wantID)
	}
}

func TestGetArtefact_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-artefact-001"
	env := setupTestEnv(t, wantID)

	resp, err := env.client.GetArtefact(context.Background(), "doc-draft")
	if err != nil {
		t.Fatalf("GetArtefact() returned error: %v", err)
	}
	if string(resp.GetContent()) != "test-content" {
		t.Fatalf("unexpected content: %s", resp.GetContent())
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 {
		t.Fatal("metadata x-flow-workitem-id was NOT present on GetArtefact call")
	}
	if got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %q, want %q", got[0], wantID)
	}
}

func TestHeartbeat_EmptyWorkitemID_NoMetadataInjected(t *testing.T) {
	// When workitem ID is empty, the interceptor should NOT inject the header.
	env := setupTestEnv(t, "")

	ack, err := env.client.Heartbeat(context.Background())
	if err != nil {
		t.Fatalf("Heartbeat() returned error: %v", err)
	}
	if !ack {
		t.Fatal("Heartbeat() was not acknowledged")
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) != 0 {
		t.Fatalf("expected no x-flow-workitem-id metadata when workitem ID is empty, got %v", got)
	}
}
