package proxy

import (
	"context"
	"net"
	"testing"

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
