package flow

import (
	"context"
	"net"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// setupGRPCTestEnv creates a bufconn-backed gRPC server, dials it with the
// workitem interceptor, and returns a connected Client plus the server.
// The caller provides a registerServices callback to register whichever
// service implementations it needs (spy servers, etc.).
// Cleanup (connection close + graceful stop) is registered via t.Cleanup.
func setupGRPCTestEnv(
	t *testing.T, workitemID string, registerServices func(srv *grpc.Server),
) (*Client, *grpc.Server) {
	t.Helper()

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	registerServices(srv)

	go func() {
		_ = srv.Serve(lis) // Server stopped — expected during cleanup.
	}()

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
		conn:           conn,
		workitemID:     workitemID,
		Sidecar:        flowv1.NewSidecarServiceClient(conn),
		Operator:       flowv1.NewOperatorServiceClient(conn),
		Archivist:      flowv1.NewArchivistServiceClient(conn),
		Librarian:      flowv1.NewLibrarianServiceClient(conn),
		FrictionLedger: flowv1.NewFrictionLedgerServiceClient(conn),
	}

	t.Cleanup(func() {
		_ = client.Close()
		srv.GracefulStop()
	})

	return client, srv
}

// setupGRPCTestEnvWithEventBus is like setupGRPCTestEnv but also creates a
// second bufconn listener for the Event Bus service and wires the Client's
// EventBus field to it.
func setupGRPCTestEnvWithEventBus(
	t *testing.T,
	workitemID string,
	registerServices func(srv *grpc.Server),
	registerEventBus func(srv *grpc.Server),
) *Client {
	t.Helper()

	client, _ := setupGRPCTestEnv(t, workitemID, registerServices)

	// Set up a separate bufconn for the Event Bus.
	ebLis := bufconn.Listen(bufSize)
	ebSrv := grpc.NewServer()
	registerEventBus(ebSrv)

	go func() {
		_ = ebSrv.Serve(ebLis)
	}()

	ebConn, err := grpc.NewClient(
		"passthrough:///bufnet-eb",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return ebLis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial eventbus bufconn: %v", err)
	}

	client.eventBusConn = ebConn
	client.EventBus = flowv1.NewFlowEventBusServiceClient(ebConn)

	t.Cleanup(func() {
		_ = ebConn.Close()
		ebSrv.GracefulStop()
	})

	return client
}
