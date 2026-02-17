package service

import (
	"context"
	"net"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Fake NodeService server for testing
// ---------------------------------------------------------------------------

type fakeNodeServer struct {
	flowv1.UnimplementedNodeServiceServer
	lastReq   *flowv1.AssignWorkRequest
	returnOK  bool
	returnErr error
}

func (f *fakeNodeServer) Process(_ context.Context, req *flowv1.AssignWorkRequest) (*flowv1.Ack, error) {
	f.lastReq = req
	if f.returnErr != nil {
		return nil, f.returnErr
	}
	return &flowv1.Ack{Accepted: f.returnOK, Message: "fake"}, nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestSidecarServer_Heartbeat(t *testing.T) {
	srv := NewSidecarServer("test-node", "")

	resp, err := srv.Heartbeat(context.Background(), &flowv1.HeartbeatRequest{
		WorkitemId: "wi-1",
	})
	if err != nil {
		t.Fatalf("Heartbeat() error: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}
}

func TestSidecarServer_AssignWork_MissingContext(t *testing.T) {
	srv := NewSidecarServer("test-node", "")

	_, err := srv.AssignWork(context.Background(), &flowv1.AssignWorkRequest{})
	if err == nil {
		t.Fatal("expected error for missing context")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestSidecarServer_AssignWork_ForwardsToNode(t *testing.T) {
	// Start a fake NodeService server.
	fake := &fakeNodeServer{returnOK: true}
	nodeSrv := grpc.NewServer()
	flowv1.RegisterNodeServiceServer(nodeSrv, fake)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	go func() { _ = nodeSrv.Serve(lis) }()
	t.Cleanup(func() { nodeSrv.GracefulStop() })

	// Create SidecarServer pointing at the fake node.
	sidecar := NewSidecarServer("test-node", lis.Addr().String())
	t.Cleanup(func() { sidecar.Close() })

	ack, err := sidecar.AssignWork(context.Background(), &flowv1.AssignWorkRequest{
		Context: &flowv1.WorkitemContext{
			FlowId:     "flow-1",
			WorkitemId: "wi-1",
			NodeId:     "node-1",
		},
	})
	if err != nil {
		t.Fatalf("AssignWork() error: %v", err)
	}
	if !ack.GetAccepted() {
		t.Fatal("expected accepted=true")
	}

	// Verify the fake node received the correct context.
	if fake.lastReq == nil {
		t.Fatal("fake node server did not receive request")
	}
	if fake.lastReq.GetContext().GetWorkitemId() != "wi-1" {
		t.Fatalf("expected workitem_id=wi-1, got %s", fake.lastReq.GetContext().GetWorkitemId())
	}
}

func TestSidecarServer_AssignWork_NodeFailure(t *testing.T) {
	// Start a fake NodeService that returns an error.
	fake := &fakeNodeServer{
		returnErr: status.Error(codes.Internal, "boom"),
	}
	nodeSrv := grpc.NewServer()
	flowv1.RegisterNodeServiceServer(nodeSrv, fake)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	go func() { _ = nodeSrv.Serve(lis) }()
	t.Cleanup(func() { nodeSrv.GracefulStop() })

	sidecar := NewSidecarServer("test-node", lis.Addr().String())
	t.Cleanup(func() { sidecar.Close() })

	_, err = sidecar.AssignWork(context.Background(), &flowv1.AssignWorkRequest{
		Context: &flowv1.WorkitemContext{
			FlowId:     "flow-1",
			WorkitemId: "wi-fail",
			NodeId:     "node-1",
		},
	})
	if err == nil {
		t.Fatal("expected error when node fails")
	}
}

func TestSidecarServer_AssignWork_UnreachableNode(t *testing.T) {
	// Point at an address where nothing is listening.
	// Use a non-routable IP to ensure connection failure.
	sidecar := NewSidecarServer("test-node", "127.0.0.1:1")

	// Force connection with a real address that's refused.
	// Pre-connect to trigger eager dialing.
	conn, err := grpc.NewClient(
		"127.0.0.1:1",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	sidecar.nodeConn = conn
	sidecar.nodeClient = flowv1.NewNodeServiceClient(conn)
	t.Cleanup(func() { sidecar.Close() })

	_, err = sidecar.AssignWork(context.Background(), &flowv1.AssignWorkRequest{
		Context: &flowv1.WorkitemContext{
			FlowId:     "flow-1",
			WorkitemId: "wi-unreachable",
			NodeId:     "node-1",
		},
	})
	if err == nil {
		t.Fatal("expected error when node is unreachable")
	}
}
