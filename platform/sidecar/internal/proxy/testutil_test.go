package proxy

import (
	"context"
	"net"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/sidecar/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// dialBufconn creates an in-memory gRPC server, registers services via
// registerFunc, and returns a client connection to that server. The server
// and connection are cleaned up when the test finishes.
func dialBufconn(t *testing.T, registerFunc func(*grpc.Server)) *grpc.ClientConn {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	registerFunc(srv)

	go func() {
		if err := srv.Serve(lis); err != nil {
			t.Logf("bufconn server error: %v", err)
		}
	}()
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(metadataUnaryInterceptor),
		grpc.WithStreamInterceptor(metadataStreamInterceptor),
	)
	if err != nil {
		t.Fatalf("failed to dial bufconn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

// blockingNodeServer is a fake NodeService whose Process blocks until
// release is closed, keeping the assignment session live for the duration
// of the test.
type blockingNodeServer struct {
	flowv1.UnimplementedNodeServiceServer
	release chan struct{}
}

func (f *blockingNodeServer) Process(
	ctx context.Context, _ *flowv1.AssignWorkRequest,
) (*flowv1.Ack, error) {
	select {
	case <-f.release:
		return &flowv1.Ack{Accepted: true, Message: "fake"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// startAssignmentSession drives the real AssignWork RPC on sidecar against a
// blocking fake NodeService to create a live assignment session for
// workitemID/nodeID. The session stays registered until test cleanup, which
// releases the fake node and lets the assignment complete. This is the
// test-side replacement for the deleted InjectSessionForTest production seam.
func startAssignmentSession(t *testing.T, sidecar *service.SidecarServer, workitemID, nodeID string) {
	t.Helper()

	node := &blockingNodeServer{release: make(chan struct{})}
	nodeSrv := grpc.NewServer()
	flowv1.RegisterNodeServiceServer(nodeSrv, node)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for fake node server: %v", err)
	}
	go func() { _ = nodeSrv.Serve(lis) }()

	sidecar.NodeAddress = lis.Addr().String()
	done := make(chan error, 1)
	go func() {
		_, err := sidecar.AssignWork(context.Background(), &flowv1.AssignWorkRequest{
			Context: &flowv1.WorkitemContext{
				FlowNamespace: "ns",
				WorkitemId:    workitemID,
				NodeId:        nodeID,
			},
		})
		done <- err
	}()

	// Wait for the assignment session to be registered.
	deadline := time.Now().Add(2 * time.Second)
	for sidecar.LookupSession(workitemID) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for assignment session %q", workitemID)
		}
		time.Sleep(5 * time.Millisecond)
	}

	t.Cleanup(func() {
		close(node.release)
		<-done
		nodeSrv.Stop()
		_ = sidecar.Close()
	})
}

// newSidecarWithSession creates a SidecarServer with a live assignment
// session for the parent workitem ("parent-wi", node "node"), driven through
// the real AssignWork RPC. This is the session the cross-Workitem
// authorization proxy tests exercise.
func newSidecarWithSession(t *testing.T) *service.SidecarServer {
	t.Helper()
	sidecar := service.NewSidecarServer("ns", "node", "")
	startAssignmentSession(t, sidecar, "parent-wi", "node")
	return sidecar
}
