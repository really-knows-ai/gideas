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
