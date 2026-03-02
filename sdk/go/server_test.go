package flow

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ---------------------------------------------------------------------------
// Tests — SDK Server (flow.Start / NodeService)
// ---------------------------------------------------------------------------

func TestNodeServiceServer_Process_Success(t *testing.T) {
	// Start a nodeServiceServer directly (not via Start, to avoid signal handling).
	handlerCalled := make(chan *flowv1.WorkitemContext, 1)
	srv := grpc.NewServer()
	nodeServer := &nodeServiceServer{
		handler: func(ctx context.Context, wctx *flowv1.WorkitemContext) error {
			handlerCalled <- wctx
			return nil
		},
	}
	flowv1.RegisterNodeServiceServer(srv, nodeServer)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	// Dial the server.
	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := flowv1.NewNodeServiceClient(conn)

	// Call Process.
	ack, err := client.Process(context.Background(), &flowv1.AssignWorkRequest{
		Context: &flowv1.WorkitemContext{
			FlowNamespace: "flow-1",
			WorkitemId:    "wi-1",
			NodeId:        "node-1",
		},
	})
	if err != nil {
		t.Fatalf("Process() returned error: %v", err)
	}
	if !ack.GetAccepted() {
		t.Fatalf("expected accepted=true, got false: %s", ack.GetMessage())
	}

	// Verify handler was called with correct context.
	select {
	case wctx := <-handlerCalled:
		if wctx.GetFlowNamespace() != "flow-1" {
			t.Errorf("expected flow_namespace=flow-1, got %s", wctx.GetFlowNamespace())
		}
		if wctx.GetWorkitemId() != "wi-1" {
			t.Errorf("expected workitem_id=wi-1, got %s", wctx.GetWorkitemId())
		}
		if wctx.GetNodeId() != "node-1" {
			t.Errorf("expected node_id=node-1, got %s", wctx.GetNodeId())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not called within timeout")
	}
}

func TestNodeServiceServer_Process_HandlerError(t *testing.T) {
	srv := grpc.NewServer()
	nodeServer := &nodeServiceServer{
		handler: func(ctx context.Context, wctx *flowv1.WorkitemContext) error {
			return fmt.Errorf("simulated failure")
		},
	}
	flowv1.RegisterNodeServiceServer(srv, nodeServer)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := flowv1.NewNodeServiceClient(conn)

	ack, err := client.Process(context.Background(), &flowv1.AssignWorkRequest{
		Context: &flowv1.WorkitemContext{
			FlowNamespace: "flow-1",
			WorkitemId:    "wi-err",
			NodeId:        "node-1",
		},
	})
	if err != nil {
		t.Fatalf("Process() returned gRPC error: %v (expected Ack with accepted=false)", err)
	}
	if ack.GetAccepted() {
		t.Fatal("expected accepted=false when handler returns error")
	}
	if ack.GetMessage() != "simulated failure" {
		t.Fatalf("expected error message 'simulated failure', got %q", ack.GetMessage())
	}
}

func TestStartConfig_Defaults(t *testing.T) {
	cfg := &startConfig{port: DefaultNodePort}

	if cfg.port != "50053" {
		t.Fatalf("expected default port 50053, got %s", cfg.port)
	}
}

func TestStartConfig_WithNodePort(t *testing.T) {
	cfg := &startConfig{port: DefaultNodePort}
	WithNodePort("9999")(cfg)

	if cfg.port != "9999" {
		t.Fatalf("expected port 9999, got %s", cfg.port)
	}
}
