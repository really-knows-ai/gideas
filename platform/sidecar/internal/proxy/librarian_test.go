package proxy

import (
	"context"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// captureLibrarianServer captures Librarian RPC calls for assertions.
type captureLibrarianServer struct {
	flowv1.UnimplementedLibrarianServiceServer
	lastCiteReq              *flowv1.CiteRequest
	lastQueryReq             *flowv1.QueryLawsRequest
	lastGetLawReq            *flowv1.GetLawRequest
	lastGetActiveDisputesReq *flowv1.GetActiveDisputesRequest
	capturedMD               metadata.MD
}

func (s *captureLibrarianServer) QueryLaws(
	ctx context.Context, req *flowv1.QueryLawsRequest,
) (*flowv1.QueryLawsResponse, error) {
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

func (s *captureLibrarianServer) GetLaw(
	ctx context.Context, req *flowv1.GetLawRequest,
) (*flowv1.GetLawResponse, error) {
	s.lastGetLawReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.GetLawResponse{Law: &flowv1.Law{Id: req.GetLawId()}}, nil
}

func (s *captureLibrarianServer) GetActiveDisputes(
	ctx context.Context, req *flowv1.GetActiveDisputesRequest,
) (*flowv1.GetActiveDisputesResponse, error) {
	s.lastGetActiveDisputesReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return &flowv1.GetActiveDisputesResponse{
		Records: []*flowv1.DisputeRecord{
			{PetitionId: "pet-1", CitedLawIds: []string{"law-a"}},
		},
	}, nil
}

// captureEventBusForLibrarian captures Event Bus Publish calls for assertions.
type captureEventBusForLibrarian struct {
	flowv1.UnimplementedFlowEventBusServiceServer
	lastPublishReq *flowv1.PublishRequest
}

func (s *captureEventBusForLibrarian) Publish(
	_ context.Context, req *flowv1.PublishRequest,
) (*flowv1.PublishResponse, error) {
	s.lastPublishReq = req
	return &flowv1.PublishResponse{Acknowledged: true, Sequence: 1}, nil
}

type librarianTestEnv struct {
	proxy        *LibrarianProxy
	librarianSpy *captureLibrarianServer
	eventBusSpy  *captureEventBusForLibrarian
}

func setupLibrarianProxy(t *testing.T) *librarianTestEnv {
	t.Helper()

	libSpy := &captureLibrarianServer{}
	libConn := dialBufconn(t, func(s *grpc.Server) {
		flowv1.RegisterLibrarianServiceServer(s, libSpy)
	})

	busSpy := &captureEventBusForLibrarian{}
	busConn := dialBufconn(t, func(s *grpc.Server) {
		flowv1.RegisterFlowEventBusServiceServer(s, busSpy)
	})

	ebProxy := NewEventBusProxyFromClient(flowv1.NewFlowEventBusServiceClient(busConn))

	p := &LibrarianProxy{
		client:        flowv1.NewLibrarianServiceClient(libConn),
		eventBusProxy: ebProxy,
		conn:          libConn,
		magnitude:     1,
	}

	return &librarianTestEnv{
		proxy:        p,
		librarianSpy: libSpy,
		eventBusSpy:  busSpy,
	}
}

func TestLibrarianProxy_Cite_ForwardsAndEmitsFriction(t *testing.T) {
	env := setupLibrarianProxy(t)

	md := metadata.Pairs(
		"x-flow-workitem-id", "wi-test",
		"x-flow-namespace", "ns-test",
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

	// Verify friction was published to Event Bus (async — wait briefly).
	time.Sleep(100 * time.Millisecond)
	if env.eventBusSpy.lastPublishReq == nil {
		t.Fatal("Friction was not published to Event Bus")
	}
	evt := env.eventBusSpy.lastPublishReq.GetEvent()
	if evt.GetEventType() != "friction" {
		t.Fatalf("expected event_type=friction, got %q", evt.GetEventType())
	}
	if evt.GetFlowNamespace() != "ns-test" {
		t.Fatalf("expected flow_namespace=ns-test, got %q", evt.GetFlowNamespace())
	}
	if evt.GetWorkitemId() != "wi-test" {
		t.Fatalf("expected workitem_id=wi-test, got %q", evt.GetWorkitemId())
	}
	if evt.GetNodeId() != "node-test" {
		t.Fatalf("expected node_id=node-test, got %q", evt.GetNodeId())
	}
	// law_ids are now in labels, not CSV attributes.
	labels := evt.GetLabels()
	if len(labels) != 2 {
		t.Fatalf("expected 2 law_id labels, got %d", len(labels))
	}
	lawIDs := make(map[string]bool)
	for _, l := range labels {
		if l.GetKey() == "law_id" {
			lawIDs[l.GetValue()] = true
		}
	}
	if !lawIDs["law-1"] || !lawIDs["law-2"] {
		t.Fatalf("expected law_id labels law-1 and law-2, got %v", lawIDs)
	}
	if evt.GetAttributes()["magnitude"] != "1" {
		t.Fatalf("expected magnitude=1, got %q", evt.GetAttributes()["magnitude"])
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

func TestLibrarianProxy_GetActiveDisputes_Passthrough(t *testing.T) {
	env := setupLibrarianProxy(t)

	md := metadata.Pairs("x-flow-workitem-id", "wi-dispute-test")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := env.proxy.GetActiveDisputes(ctx, &flowv1.GetActiveDisputesRequest{
		LawId: "law-a",
	})
	if err != nil {
		t.Fatalf("GetActiveDisputes: %v", err)
	}

	// Verify request was forwarded to Librarian backend.
	if env.librarianSpy.lastGetActiveDisputesReq == nil {
		t.Fatal("GetActiveDisputes was not forwarded to Librarian")
	}
	if env.librarianSpy.lastGetActiveDisputesReq.GetLawId() != "law-a" {
		t.Fatalf("expected law_id=law-a forwarded, got %q",
			env.librarianSpy.lastGetActiveDisputesReq.GetLawId())
	}

	// Verify response is returned unmodified.
	if len(resp.GetRecords()) != 1 {
		t.Fatalf("expected 1 dispute record, got %d", len(resp.GetRecords()))
	}
	if resp.GetRecords()[0].GetPetitionId() != "pet-1" {
		t.Fatalf("expected petition_id=pet-1, got %q", resp.GetRecords()[0].GetPetitionId())
	}

	// Verify metadata propagation.
	vals := env.librarianSpy.capturedMD.Get("x-flow-workitem-id")
	if len(vals) != 1 || vals[0] != "wi-dispute-test" {
		t.Fatalf("expected metadata propagation, got %v", vals)
	}
}
