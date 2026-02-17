package proxy

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

// captureLibrarianServer captures Librarian RPC calls for assertions.
type captureLibrarianServer struct {
	flowv1.UnimplementedLibrarianServiceServer
	lastCiteReq   *flowv1.CiteRequest
	lastQueryReq  *flowv1.QueryLawsRequest
	lastGetLawReq *flowv1.GetLawRequest
	capturedMD    metadata.MD
}

func (s *captureLibrarianServer) QueryLaws(ctx context.Context, req *flowv1.QueryLawsRequest) (*flowv1.QueryLawsResponse, error) {
	s.lastQueryReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.QueryLawsResponse{}, nil
}

func (s *captureLibrarianServer) Cite(ctx context.Context, req *flowv1.CiteRequest) (*flowv1.CiteResponse, error) {
	s.lastCiteReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.CiteResponse{Acknowledged: true}, nil
}

func (s *captureLibrarianServer) GetLaw(ctx context.Context, req *flowv1.GetLawRequest) (*flowv1.GetLawResponse, error) {
	s.lastGetLawReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.GetLawResponse{Law: &flowv1.Law{Id: req.GetLawId()}}, nil
}

// captureMonitorServer captures Monitor RPC calls for assertions.
type captureMonitorServer struct {
	flowv1.UnimplementedFlowMonitorServiceServer
	lastFrictionReq *flowv1.AddFrictionRequest
	capturedMD      metadata.MD
}

func (s *captureMonitorServer) AddFriction(ctx context.Context, req *flowv1.AddFrictionRequest) (*flowv1.AddFrictionResponse, error) {
	s.lastFrictionReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.AddFrictionResponse{Acknowledged: true}, nil
}

type librarianTestEnv struct {
	proxy        *LibrarianProxy
	librarianSpy *captureLibrarianServer
	monitorSpy   *captureMonitorServer
}

func setupLibrarianProxy(t *testing.T) *librarianTestEnv {
	t.Helper()

	// Librarian backend.
	libLis := bufconn.Listen(1024 * 1024)
	libSrv := grpc.NewServer()
	libSpy := &captureLibrarianServer{}
	flowv1.RegisterLibrarianServiceServer(libSrv, libSpy)
	go func() { libSrv.Serve(libLis) }()
	t.Cleanup(func() { libSrv.Stop(); libLis.Close() })

	libConn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return libLis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial librarian bufconn: %v", err)
	}
	t.Cleanup(func() { libConn.Close() })

	// Monitor backend.
	monLis := bufconn.Listen(1024 * 1024)
	monSrv := grpc.NewServer()
	monSpy := &captureMonitorServer{}
	flowv1.RegisterFlowMonitorServiceServer(monSrv, monSpy)
	go func() { monSrv.Serve(monLis) }()
	t.Cleanup(func() { monSrv.Stop(); monLis.Close() })

	monConn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return monLis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial monitor bufconn: %v", err)
	}
	t.Cleanup(func() { monConn.Close() })

	proxy := &LibrarianProxy{
		client:        flowv1.NewLibrarianServiceClient(libConn),
		monitorClient: flowv1.NewFlowMonitorServiceClient(monConn),
		conn:          libConn,
		monitorConn:   monConn,
		magnitude:     1,
	}

	return &librarianTestEnv{
		proxy:        proxy,
		librarianSpy: libSpy,
		monitorSpy:   monSpy,
	}
}

func TestLibrarianProxy_Cite_ForwardsAndEmitsFriction(t *testing.T) {
	env := setupLibrarianProxy(t)

	md := metadata.Pairs(
		"x-flow-workitem-id", "wi-test",
		"x-flow-flow-id", "flow-test",
		"x-flow-node-id", "node-test",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := env.proxy.Cite(ctx, &flowv1.CiteRequest{
		LawIds: []string{"law-1", "law-2"},
	})
	if err != nil {
		t.Fatalf("Cite: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	// Verify Cite was forwarded to Librarian.
	if env.librarianSpy.lastCiteReq == nil {
		t.Fatal("Cite was not forwarded to Librarian")
	}
	if len(env.librarianSpy.lastCiteReq.GetLawIds()) != 2 {
		t.Fatalf("expected 2 law_ids forwarded, got %d", len(env.librarianSpy.lastCiteReq.GetLawIds()))
	}

	// Verify friction was emitted to Monitor.
	if env.monitorSpy.lastFrictionReq == nil {
		t.Fatal("AddFriction was not called on Monitor")
	}
	if env.monitorSpy.lastFrictionReq.GetMagnitude() != 1 {
		t.Fatalf("expected magnitude 1, got %d", env.monitorSpy.lastFrictionReq.GetMagnitude())
	}
	if len(env.monitorSpy.lastFrictionReq.GetLawIds()) != 2 {
		t.Fatalf("expected 2 law_ids in friction, got %d", len(env.monitorSpy.lastFrictionReq.GetLawIds()))
	}
	if env.monitorSpy.lastFrictionReq.GetWorkitemId() != "wi-test" {
		t.Fatalf("expected workitem_id=wi-test, got %q", env.monitorSpy.lastFrictionReq.GetWorkitemId())
	}
	if env.monitorSpy.lastFrictionReq.GetFlowId() != "flow-test" {
		t.Fatalf("expected flow_id=flow-test, got %q", env.monitorSpy.lastFrictionReq.GetFlowId())
	}
	if env.monitorSpy.lastFrictionReq.GetNodeId() != "node-test" {
		t.Fatalf("expected node_id=node-test, got %q", env.monitorSpy.lastFrictionReq.GetNodeId())
	}
}

func TestLibrarianProxy_QueryLaws_PropagatesMetadata(t *testing.T) {
	env := setupLibrarianProxy(t)

	md := metadata.Pairs("x-flow-workitem-id", "wi-meta-test")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := env.proxy.QueryLaws(ctx, &flowv1.QueryLawsRequest{})
	if err != nil {
		t.Fatalf("QueryLaws: %v", err)
	}

	vals := env.librarianSpy.capturedMD.Get("x-flow-workitem-id")
	if len(vals) != 1 || vals[0] != "wi-meta-test" {
		t.Fatalf("expected metadata propagation, got %v", vals)
	}
}

func TestLibrarianProxy_GetLaw_Passthrough(t *testing.T) {
	env := setupLibrarianProxy(t)

	resp, err := env.proxy.GetLaw(context.Background(), &flowv1.GetLawRequest{LawId: "law-123"})
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	if resp.GetLaw().GetId() != "law-123" {
		t.Fatalf("expected law_id=law-123, got %q", resp.GetLaw().GetId())
	}
}
