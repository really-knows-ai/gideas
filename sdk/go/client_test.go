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

// spyServer implements the gRPC services and records the metadata it
// receives. This lets us assert that the SDK's interceptor injects the
// correct workitem_id header.
type spyServer struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFlowMonitorServiceServer

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

func (s *spyServer) QueryLaws(ctx context.Context, req *flowv1.QueryLawsRequest) (*flowv1.QueryLawsResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.QueryLawsResponse{Laws: []*flowv1.Law{{Id: "law-1"}}}, nil
}

func (s *spyServer) Cite(ctx context.Context, req *flowv1.CiteRequest) (*flowv1.CiteResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.CiteResponse{Acknowledged: true}, nil
}

func (s *spyServer) RecordFinding(ctx context.Context, req *flowv1.RecordFindingRequest) (*flowv1.RecordFindingResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.RecordFindingResponse{LawId: "finding-001"}, nil
}

func (s *spyServer) RecordTelemetry(ctx context.Context, req *flowv1.RecordTelemetryRequest) (*flowv1.RecordTelemetryResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
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
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFlowMonitorServiceServer(srv, spy)

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
		Librarian:  flowv1.NewLibrarianServiceClient(conn),
		Monitor:    flowv1.NewFlowMonitorServiceClient(conn),
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

// ---------------------------------------------------------------------------
// Tests — Librarian Convenience Methods
// ---------------------------------------------------------------------------

func TestQueryLaws_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-laws-001"
	env := setupTestEnv(t, wantID)

	laws, err := env.client.QueryLaws(context.Background(), "", "")
	if err != nil {
		t.Fatalf("QueryLaws() returned error: %v", err)
	}
	if len(laws) != 1 {
		t.Fatalf("expected 1 law, got %d", len(laws))
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

func TestCite_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-cite-001"
	env := setupTestEnv(t, wantID)

	err := env.client.Cite(context.Background(), "law-1", "law-2")
	if err != nil {
		t.Fatalf("Cite() returned error: %v", err)
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

func TestRecordFinding_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-finding-001"
	env := setupTestEnv(t, wantID)

	lawID, err := env.client.RecordFinding(context.Background(), "test goal", []string{"docs"}, []*flowv1.Representation{
		{Type: "text/plain", Content: "test"},
	})
	if err != nil {
		t.Fatalf("RecordFinding() returned error: %v", err)
	}
	if lawID != "finding-001" {
		t.Fatalf("expected law_id=finding-001, got %q", lawID)
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

// ---------------------------------------------------------------------------
// Tests — Monitor Convenience Methods
// ---------------------------------------------------------------------------

func TestRecordTelemetry_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-telemetry-001"
	env := setupTestEnv(t, wantID)

	err := env.client.RecordTelemetry(context.Background(), "foundry.cost.llm", []byte(`{"model":"gpt-4"}`))
	if err != nil {
		t.Fatalf("RecordTelemetry() returned error: %v", err)
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}
