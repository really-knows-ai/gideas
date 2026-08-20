package main

import (
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
)

// TestRegisterCartographerProxy pins SPEC R5 (Sidecar discovery of the
// Cartographer): when CARTOGRAPHER_ADDRESS is unset or empty, the
// CartographerProxy is not created and Cartographer-related RPCs are
// unavailable from that node — the CartographerServiceServer is not registered
// on the Sidecar's gRPC server, so every Cartographer RPC fails with
// Unimplemented. The configured case is pinned too so the branch cannot
// silently invert.
func TestRegisterCartographerProxy(t *testing.T) {
	// os.Getenv returns "" for both an unset and an empty CARTOGRAPHER_ADDRESS,
	// so the two SPEC R5 forms of "unset or empty" both reach the seam as the
	// same empty value main() passes in.
	for _, tc := range []struct {
		name    string
		addr    string
		enabled bool
	}{
		{"unset", "", false},
		{"empty", "", false},
		{"configured", "127.0.0.1:1", true}, // grpc.NewClient dials lazily; no Cartographer needed
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := grpc.NewServer()
			closer := registerCartographerProxy(srv, tc.addr)
			t.Cleanup(func() { _ = closer() })

			_, registered := srv.GetServiceInfo()[flowv1.CartographerService_ServiceDesc.ServiceName]
			if registered != tc.enabled {
				t.Errorf("CartographerService registered on the Sidecar gRPC server = %v, want %v", registered, tc.enabled)
			}
		})
	}
}

// TestRegisterQueuePeerDisabled pins the queue-shard role's disabled branch
// (SPEC: when FLOW_QUEUE_SERVICE_ADDR is unset or empty, nothing queue-related
// is registered and a no-op closer is returned). Under the ROUND 0 stub the
// branch already returns a no-op closer + nil error, so both the closer and the
// no-registration assertions must hold: the queue peer service must NOT appear
// on the Sidecar gRPC server.
func TestRegisterQueuePeerDisabled(t *testing.T) {
	srv := grpc.NewServer()

	closer, err := registerQueuePeer(srv, "", "node-a", "host", "50051")
	if err != nil {
		t.Fatalf("registerQueuePeer(disabled) returned error = %v, want nil", err)
	}
	if closer == nil {
		t.Fatal("registerQueuePeer(disabled) returned nil closer, want non-nil no-op closer")
	}
	if err := closer(); err != nil {
		t.Errorf("closer() returned error = %v, want nil", err)
	}

	_, registered := srv.GetServiceInfo()[flowv1.QueuePeerService_ServiceDesc.ServiceName]
	if registered {
		t.Error("QueuePeerService registered on the Sidecar gRPC server when the " +
			"queue-shard role is disabled, want not registered")
	}
}

// TestRegisterQueuePeerEnabled pins the queue-shard role's enabled branch:
// when FLOW_QUEUE_SERVICE_ADDR is non-empty the QueuePeerService IS registered
// on the Sidecar gRPC server, registerQueuePeer returns a non-nil closer + nil
// error, and the closer stops the heartbeat loop / closes the store + registry
// conn without error. Under the ROUND 0 stub the service is NOT registered, so
// this test FAILS (red) until the implementer wires the real body next round.
//
// The registry client uses grpc.NewClient which is lazy (does not connect until
// the first RPC) and the heartbeat loop logs failures non-fatally, so no real
// network I/O occurs in this test and the address is never dialed.
func TestRegisterQueuePeerEnabled(t *testing.T) {
	srv := grpc.NewServer()

	closer, err := registerQueuePeer(srv, "queue-svc:50053", "node-a", "host", "50051")
	if err != nil {
		t.Fatalf("registerQueuePeer(enabled) returned error = %v, want nil", err)
	}
	if closer == nil {
		t.Fatal("registerQueuePeer(enabled) returned nil closer, want non-nil closer")
	}
	// The closer must stop the heartbeat loop and close the store + registry
	// conn without error before the test ends (so no goroutine leaks).
	t.Cleanup(func() {
		if err := closer(); err != nil {
			t.Errorf("closer() returned error = %v, want nil", err)
		}
	})

	_, registered := srv.GetServiceInfo()[flowv1.QueuePeerService_ServiceDesc.ServiceName]
	if !registered {
		t.Error("QueuePeerService NOT registered on the Sidecar gRPC server when the " +
			"queue-shard role is enabled, want registered")
	}
}
