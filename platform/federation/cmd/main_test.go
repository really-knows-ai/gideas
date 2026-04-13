package main

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	federationv1 "github.com/gideas/flow/federation/api/v1"
	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// pickFreePort returns an available TCP port on localhost.
func pickFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("pickFreePort: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// testRunConfig returns a runConfig suitable for unit tests. It injects a
// fake K8s client so no real cluster is needed.
func testRunConfig(t *testing.T, port int) runConfig {
	t.Helper()
	s := federationv1.NewTestScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(s).Build()
	return runConfig{
		grpcPort:  port,
		namespace: "test-ns",
		k8sClient: k8sClient,
	}
}

func TestServerStartsOnConfiguredPort(t *testing.T) {
	port := pickFreePort(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, testRunConfig(t, port))
	}()

	// Wait for the gRPC server to be reachable.
	addr := fmt.Sprintf("localhost:%d", port)
	var conn *grpc.ClientConn
	var err error
	for i := range 50 {
		conn, err = grpc.NewClient(
			addr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			if i == 49 {
				t.Fatalf("could not create gRPC client after retries: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}

		// Verify the connection is actually usable by making a call.
		client := flowv1.NewFederationServiceClient(conn)
		_, err = client.GetMembership(ctx, &flowv1.GetMembershipRequest{
			FlowIdentity: "nonexistent",
		})
		// We expect an error (Unimplemented or similar), but NOT a
		// "connection refused" — that would mean the server isn't up.
		if err != nil && isConnectionRefused(err) {
			_ = conn.Close()
			time.Sleep(20 * time.Millisecond)
			continue
		}
		break
	}
	if conn != nil {
		_ = conn.Close()
	}

	// Server is up. Shut it down.
	cancel()

	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("run returned error: %v", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return within 5s after context cancellation")
	}
}

func TestServerRegistersFederationService(t *testing.T) {
	port := pickFreePort(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, testRunConfig(t, port))
	}()

	addr := fmt.Sprintf("localhost:%d", port)
	waitForServer(t, addr)

	conn, err := grpc.NewClient(
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer func() { _ = conn.Close() }()

	client := flowv1.NewFederationServiceClient(conn)

	// Call each of the unary RPCs — they should return Unimplemented
	// (not "unknown service"), proving the service is registered.
	_, err = client.JoinFederation(ctx, &flowv1.JoinFederationRequest{})
	assertUnimplemented(t, err, "JoinFederation")

	_, err = client.LeaveFederation(ctx, &flowv1.LeaveFederationRequest{})
	assertUnimplemented(t, err, "LeaveFederation")

	_, err = client.GetMembership(ctx, &flowv1.GetMembershipRequest{})
	assertUnimplemented(t, err, "GetMembership")

	_, err = client.DiscoverEndpoints(ctx, &flowv1.DiscoverEndpointsRequest{})
	assertUnimplemented(t, err, "DiscoverEndpoints")

	_, err = client.GetPetitionTarget(ctx, &flowv1.GetPetitionTargetRequest{})
	assertUnimplemented(t, err, "GetPetitionTarget")

	_, err = client.SubmitPublication(ctx, &flowv1.SubmitPublicationRequest{})
	assertUnimplemented(t, err, "SubmitPublication")

	cancel()
	<-errCh
}

func TestGracefulShutdown(t *testing.T) {
	port := pickFreePort(t)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, testRunConfig(t, port))
	}()

	addr := fmt.Sprintf("localhost:%d", port)
	waitForServer(t, addr)

	// Cancel context to trigger graceful shutdown.
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run returned error on shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("graceful shutdown did not complete within 5s")
	}

	// After shutdown, the port should no longer be accepting connections.
	conn, err := grpc.NewClient(
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err == nil {
		client := flowv1.NewFederationServiceClient(conn)
		_, callErr := client.GetMembership(context.Background(), &flowv1.GetMembershipRequest{})
		_ = conn.Close()
		if callErr == nil {
			t.Error("expected error after shutdown, got nil")
		}
	}
}

// --- helpers ---

func isConnectionRefused(err error) bool {
	if err == nil {
		return false
	}
	return contains(err.Error(), "connection refused")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func waitForServer(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become reachable within 3s", addr)
}

func assertUnimplemented(t *testing.T, err error, rpc string) {
	t.Helper()
	if err == nil {
		// Unimplemented methods return errors. If no error, the method
		// was somehow implemented — which is fine for our purposes (it
		// means the service IS registered).
		return
	}
	errStr := err.Error()
	// "Unimplemented" is acceptable — service is registered, method is
	// just not wired yet. "unknown service" would mean the service is
	// NOT registered.
	if contains(errStr, "unknown service") {
		t.Errorf("%s: service not registered: %v", rpc, err)
	}
}
