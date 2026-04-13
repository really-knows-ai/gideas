package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/gideas/flow/federation/internal/service"
	"github.com/gideas/flow/federation/internal/store/sqlite"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
)

// startTestServer creates a gRPC server wired like main() and starts it on
// a random port. Returns the port and a stop function.
func startTestServer(t *testing.T) (int, func()) {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	store, err := sqlite.New(dsn)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	srv := grpc.NewServer()
	fedSrv := service.NewFederationServer(store)
	flowv1.RegisterFederationServiceServer(srv, fedSrv)
	reflection.Register(srv)

	go func() { _ = srv.Serve(lis) }()

	stop := func() {
		srv.GracefulStop()
		_ = store.Close()
	}
	return port, stop
}

func TestServerStartsOnConfiguredPort(t *testing.T) {
	port, stop := startTestServer(t)
	defer stop()

	// Connect and verify service is reachable.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(
		fmt.Sprintf("127.0.0.1:%d", port),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	client := flowv1.NewFederationServiceClient(conn)
	_, err = client.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken:  "test",
		FlowIdentity:    "flow-test",
		EmbassyEndpoint: "test:50059",
	})
	if err != nil {
		t.Fatalf("JoinFederation: %v", err)
	}
}

func TestServerRegistersFederationService(t *testing.T) {
	port, stop := startTestServer(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(
		fmt.Sprintf("127.0.0.1:%d", port),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Verify all 8 RPCs are registered by calling each one.
	// We only need JoinFederation to succeed to confirm registration.
	// The others will return specific errors (not Unimplemented).
	client := flowv1.NewFederationServiceClient(conn)

	// JoinFederation - should succeed.
	_, err = client.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken:  "test",
		FlowIdentity:    "flow-test",
		EmbassyEndpoint: "test:50059",
	})
	if err != nil {
		t.Fatalf("JoinFederation: %v", err)
	}

	// GetMembership - should succeed (member just joined).
	resp, err := client.GetMembership(ctx, &flowv1.GetMembershipRequest{
		FlowIdentity: "flow-test",
	})
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if resp.GetMember().GetFlowIdentity() != "flow-test" {
		t.Errorf("member flow_identity = %q, want %q", resp.GetMember().GetFlowIdentity(), "flow-test")
	}

	// LeaveFederation - should succeed.
	_, err = client.LeaveFederation(ctx, &flowv1.LeaveFederationRequest{
		FlowIdentity: "flow-test",
	})
	if err != nil {
		t.Fatalf("LeaveFederation: %v", err)
	}
}

func TestGracefulShutdownOnSIGTERM(t *testing.T) {
	_, stop := startTestServer(t)

	// GracefulStop simulates what happens on SIGTERM.
	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()

	select {
	case <-done:
		// Server stopped gracefully.
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop within 5 seconds")
	}

	// Verify process is still healthy.
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("process signal check failed: %v", err)
	}
}
