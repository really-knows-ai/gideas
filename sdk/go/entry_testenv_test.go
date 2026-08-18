package flow

import (
	"context"
	"net"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ---------------------------------------------------------------------------
// Spy servers for EntryClient tests
// ---------------------------------------------------------------------------

// entrySpyOperator captures CreateWorkitem, ResumeWorkitem, and
// ListSuspendedWorkitems calls.
type entrySpyOperator struct {
	flowv1.UnimplementedOperatorServiceServer

	lastMetadata    map[string]string
	returnID        string
	returnErr       error
	resumeWorkitems []string // captured workitem IDs from ResumeWorkitem
	resumeErr       error

	// ListSuspendedWorkitems tracking.
	listSuspendedResp []*flowv1.SuspendedWorkitemInfo
	listSuspendedErr  error
	lastCondFilter    string
}

func (s *entrySpyOperator) CreateWorkitem(
	_ context.Context, req *flowv1.CreateWorkitemRequest,
) (*flowv1.CreateWorkitemResponse, error) {
	s.lastMetadata = req.GetMetadata()
	if s.returnErr != nil {
		return nil, s.returnErr
	}
	return &flowv1.CreateWorkitemResponse{WorkitemId: s.returnID}, nil
}

func (s *entrySpyOperator) ResumeWorkitem(
	_ context.Context, req *flowv1.ResumeWorkitemRequest,
) (*flowv1.ResumeWorkitemResponse, error) {
	s.resumeWorkitems = append(s.resumeWorkitems, req.GetWorkitemId())
	if s.resumeErr != nil {
		return nil, s.resumeErr
	}
	return &flowv1.ResumeWorkitemResponse{Accepted: true}, nil
}

func (s *entrySpyOperator) ListSuspendedWorkitems(
	_ context.Context, req *flowv1.ListSuspendedWorkitemsRequest,
) (*flowv1.ListSuspendedWorkitemsResponse, error) {
	s.lastCondFilter = req.GetConditionContains()
	if s.listSuspendedErr != nil {
		return nil, s.listSuspendedErr
	}
	return &flowv1.ListSuspendedWorkitemsResponse{
		Workitems: s.listSuspendedResp,
	}, nil
}

// entrySpyEventBus captures Subscribe calls and sends events.
type entrySpyEventBus struct {
	flowv1.UnimplementedFlowEventBusServiceServer

	events    []*flowv1.FlowEvent
	lastReq   *flowv1.SubscribeRequest
	returnErr error
}

func (s *entrySpyEventBus) Subscribe(
	req *flowv1.SubscribeRequest, stream grpc.ServerStreamingServer[flowv1.FlowEvent],
) error {
	s.lastReq = req
	if s.returnErr != nil {
		return s.returnErr
	}
	for _, evt := range s.events {
		if err := stream.Send(evt); err != nil {
			return err
		}
	}
	return nil
}

// entrySpyLibrarian captures QueryLaws and RetireDisputeRecord calls.
type entrySpyLibrarian struct {
	flowv1.UnimplementedLibrarianServiceServer

	lastFilter         *flowv1.LawFilter
	returnLaws         []*flowv1.Law
	returnErr          error
	retiredPetitionIDs []string // captured petition IDs from RetireDisputeRecord
	retireErr          error
}

func (s *entrySpyLibrarian) QueryLaws(
	_ context.Context, req *flowv1.QueryLawsRequest,
) (*flowv1.QueryLawsResponse, error) {
	s.lastFilter = req.GetFilter()
	if s.returnErr != nil {
		return nil, s.returnErr
	}
	return &flowv1.QueryLawsResponse{Laws: s.returnLaws}, nil
}

func (s *entrySpyLibrarian) RetireDisputeRecord(
	_ context.Context, req *flowv1.RetireDisputeRecordRequest,
) (*flowv1.RetireDisputeRecordResponse, error) {
	s.retiredPetitionIDs = append(s.retiredPetitionIDs, req.GetPetitionId())
	if s.retireErr != nil {
		return nil, s.retireErr
	}
	return &flowv1.RetireDisputeRecordResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// setupEntryTestEnv creates bufconn-backed gRPC servers for the Sidecar
// (Operator + Librarian) and Event Bus, then returns an EntryClient wired to
// them.
func setupEntryTestEnv(
	t *testing.T,
	operatorSpy *entrySpyOperator,
	eventBusSpy *entrySpyEventBus,
	librarianSpies ...*entrySpyLibrarian,
) *EntryClient {
	t.Helper()

	ec := &EntryClient{}

	if operatorSpy != nil {
		lis := newBufconnListener(t)
		srv := grpc.NewServer()
		flowv1.RegisterOperatorServiceServer(srv, operatorSpy)
		if len(librarianSpies) > 0 && librarianSpies[0] != nil {
			flowv1.RegisterLibrarianServiceServer(srv, librarianSpies[0])
		}
		go func() { _ = srv.Serve(lis) }()

		conn, err := grpc.NewClient(
			"passthrough:///bufnet-entry-op",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			t.Fatalf("failed to dial operator bufconn: %v", err)
		}
		ec.sidecarConn = conn
		ec.operator = flowv1.NewOperatorServiceClient(conn)
		ec.librarian = flowv1.NewLibrarianServiceClient(conn)

		t.Cleanup(func() {
			_ = conn.Close()
			srv.GracefulStop()
		})
	}

	if eventBusSpy != nil {
		lis := newBufconnListener(t)
		srv := grpc.NewServer()
		flowv1.RegisterFlowEventBusServiceServer(srv, eventBusSpy)
		go func() { _ = srv.Serve(lis) }()

		conn, err := grpc.NewClient(
			"passthrough:///bufnet-entry-eb",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			t.Fatalf("failed to dial event bus bufconn: %v", err)
		}
		ec.eventBusConn = conn
		ec.eventBus = flowv1.NewFlowEventBusServiceClient(conn)

		t.Cleanup(func() {
			_ = conn.Close()
			srv.GracefulStop()
		})
	}

	t.Cleanup(func() { _ = ec.Close() })

	return ec
}

// newBufconnListener creates a new bufconn listener for tests.
func newBufconnListener(t *testing.T) *bufconnListener {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	return &bufconnListener{Listener: lis}
}

// bufconnListener wraps a net.Listener to provide DialContext.
type bufconnListener struct {
	net.Listener
}

func (l *bufconnListener) DialContext(ctx context.Context) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", l.Addr().String())
}

const testLaw42 = "law-42"
