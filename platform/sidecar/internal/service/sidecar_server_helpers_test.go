package service

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
)

// ---------------------------------------------------------------------------
// Fake NodeService server for testing
// ---------------------------------------------------------------------------

type fakeNodeServer struct {
	flowv1.UnimplementedNodeServiceServer
	mu        sync.Mutex
	lastReq   *flowv1.AssignWorkRequest
	returnOK  bool
	returnErr error

	// blockCh blocks Process until closed, allowing timer tests.
	blockCh chan struct{}
}

func (f *fakeNodeServer) Process(ctx context.Context, req *flowv1.AssignWorkRequest) (*flowv1.Ack, error) {
	f.mu.Lock()
	f.lastReq = req
	returnErr := f.returnErr
	returnOK := f.returnOK
	blockCh := f.blockCh
	f.mu.Unlock()

	if returnErr != nil {
		return nil, returnErr
	}

	// If blockCh is set, block until it's closed or context is done.
	if blockCh != nil {
		select {
		case <-blockCh:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return &flowv1.Ack{Accepted: returnOK, Message: "fake"}, nil
}

// ---------------------------------------------------------------------------
// Helper: start a fake node server and return a configured SidecarServer
// ---------------------------------------------------------------------------

func newTestSidecar(t *testing.T, fake *fakeNodeServer) *SidecarServer {
	t.Helper()
	nodeSrv := grpc.NewServer()
	flowv1.RegisterNodeServiceServer(nodeSrv, fake)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	go func() { _ = nodeSrv.Serve(lis) }()
	t.Cleanup(func() { nodeSrv.GracefulStop() })

	sidecar := NewSidecarServer("test-ns", "test-node", lis.Addr().String())
	t.Cleanup(func() { _ = sidecar.Close() })
	return sidecar
}

func testContext() *flowv1.WorkitemContext {
	return &flowv1.WorkitemContext{
		FlowNamespace: "flow-1",
		WorkitemId:    "wi-1",
		NodeId:        "node-1",
	}
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func waitForSession(t *testing.T, s *SidecarServer) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if s.getSession("wi-1") != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for session")
}
