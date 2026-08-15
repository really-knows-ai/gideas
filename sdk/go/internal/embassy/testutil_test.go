package embassy

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// setupStandaloneGRPCTestConn creates a standalone TCP-based gRPC server,
// registers services on it, and returns the client connection. This is used
// by standalone clients (e.g. FederationClient, EmbassyClient) that are not
// part of the main Client struct and don't need the workitem interceptor.
func setupStandaloneGRPCTestConn(
	t *testing.T, registerServices func(srv *grpc.Server),
) *grpc.ClientConn {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	srv := grpc.NewServer()
	registerServices(srv)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet-standalone",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", lis.Addr().String())
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial standalone server: %v", err)
	}

	t.Cleanup(func() {
		_ = conn.Close()
		srv.GracefulStop()
	})

	return conn
}
