/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	flowv1gen "github.com/foundry/flow/gen/flow/v1"
)

// TestExportGraphMidStreamUnavailableIsInternal asserts that a transport-level break
// (Unavailable) AFTER at least one chunk has been forwarded — a genuine mid-stream failure
// (SPEC error table row "ExportGraph mid-stream failure → INTERNAL", partial data may
// already have been sent) — surfaces as INTERNAL, not a stream-establishment Unavailable.
func TestExportGraphMidStreamUnavailableIsInternal(t *testing.T) {
	rt := NewProxyRoutingTable()
	rt.Register("ns", "graph", "cartographer-graph.ns.svc.cluster.local:50051")

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	s := &ProxyServer{
		routingTable: rt,
		k8sClient:    authProxyClient(t, true, true),
		authCache:    newAuthCache(30 * time.Second),
		dialer: func(ctx context.Context, endpoint string) (CartographerClient, error) {
			return &mockCartographerClient{
				exportGraphFn: func(ctx context.Context, in *flowv1gen.ExportGraphRequest) (flowv1gen.CartographerService_ExportGraphClient, error) {
					return &mockExportClientChunkThenErr{err: status.Error(codes.Unavailable, "connection reset mid-stream")}, nil
				},
			}, nil
		},
		operatorSigningKey: priv,
	}

	stream := &mockExportStream{}
	md := metadata.Pairs(
		"x-flow-namespace", "ns",
		"x-flow-graph-name", "graph",
		"authorization", "Bearer valid",
	)
	stream.ctx = metadata.NewIncomingContext(context.Background(), md)

	err = s.ExportGraph(&flowv1gen.ExportGraphRequest{Format: "json"}, stream)
	if err == nil {
		t.Fatal("expected an error on mid-stream upstream break")
	}
	// SPEC error table: a mid-stream break after a chunk has been forwarded is INTERNAL,
	// not the stream-establishment Unavailable.
	if status.Code(err) != codes.Internal {
		t.Errorf("expected INTERNAL for mid-stream break, got %v", status.Code(err))
	}
	if len(stream.sends) == 0 {
		t.Error("expected a chunk to have been forwarded before the mid-stream break")
	}
}

// TestExportGraphNonConformingUpstreamStatusIsInternal asserts that a mid-stream Recv error
// carrying a non-Unavailable, non-Internal gRPC status (a misbehaving upstream) is surfaced
// as INTERNAL per the SPEC error table ("ExportGraph mid-stream failure → INTERNAL"), not
// propagated verbatim to the caller.
func TestExportGraphNonConformingUpstreamStatusIsInternal(t *testing.T) {
	rt := NewProxyRoutingTable()
	rt.Register("ns", "graph", "cartographer-graph.ns.svc.cluster.local:50051")

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	s := &ProxyServer{
		routingTable: rt,
		k8sClient:    authProxyClient(t, true, true),
		authCache:    newAuthCache(30 * time.Second),
		dialer: func(ctx context.Context, endpoint string) (CartographerClient, error) {
			return &mockCartographerClient{
				exportGraphFn: func(ctx context.Context, in *flowv1gen.ExportGraphRequest) (flowv1gen.CartographerService_ExportGraphClient, error) {
					return &mockExportClientErr{err: status.Error(codes.DataLoss, "upstream serialisation broke mid-stream")}, nil
				},
			}, nil
		},
		operatorSigningKey: priv,
	}

	stream := &mockExportStream{}
	md := metadata.Pairs(
		"x-flow-namespace", "ns",
		"x-flow-graph-name", "graph",
		"authorization", "Bearer valid",
	)
	stream.ctx = metadata.NewIncomingContext(context.Background(), md)

	err = s.ExportGraph(&flowv1gen.ExportGraphRequest{Format: "json"}, stream)
	if err == nil {
		t.Fatal("expected an error on mid-stream upstream failure")
	}
	// SPEC error table: any mid-stream export failure is INTERNAL — a non-Unavailable
	// upstream status must not be propagated verbatim.
	if status.Code(err) != codes.Internal {
		t.Errorf("expected INTERNAL for a non-conforming upstream status, got %v", status.Code(err))
	}
}

// TestExportGraphPreStreamRejectionPassesThrough asserts that a pre-stream rejection — a
// status the Cartographer returns BEFORE sending any chunk (SPEC error table rows
// "Unsupported export format" → INVALID_ARGUMENT, "ExportGraph buffer allocation
// failure" → RESOURCE_EXHAUSTED, and the ExportGraph capability rows "Invalid capability
// metadata signature" / "Stale capability signature (anti-replay)" → PERMISSION_DENIED,
// all "no data sent") — surfaces through the proxy verbatim rather than being flattened to
// INTERNAL. These statuses arrive at the proxy's first Recv with no chunk forwarded, so
// the documented CLI error codes must reach the caller (the sidecar relay preserves
// upstream statuses identically). The PERMISSION_DENIED row pins the stale-capability /
// rotated-key case: a capability signed by a rotated operator key (or outside the
// staleness window) is rejected by the Cartographer's ingress verifier, and that
// rejection must surface as PERMISSION_DENIED — never as the INTERNAL reserved for a
// mid-stream failure.
func TestExportGraphPreStreamRejectionPassesThrough(t *testing.T) {
	for _, tc := range []struct {
		name string
		code codes.Code
	}{
		{"unsupported format", codes.InvalidArgument},
		{"buffer allocation failure", codes.ResourceExhausted},
		{"stale operator-signed capability (rotated key)", codes.PermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := NewProxyRoutingTable()
			rt.Register("ns", "graph", "cartographer-graph.ns.svc.cluster.local:50051")

			_, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatalf("generate signing key: %v", err)
			}

			upstreamErr := status.Error(tc.code, "rejected before any chunk was sent")
			s := &ProxyServer{
				routingTable: rt,
				k8sClient:    authProxyClient(t, true, true),
				authCache:    newAuthCache(30 * time.Second),
				dialer: func(ctx context.Context, endpoint string) (CartographerClient, error) {
					return &mockCartographerClient{
						exportGraphFn: func(ctx context.Context, in *flowv1gen.ExportGraphRequest) (flowv1gen.CartographerService_ExportGraphClient, error) {
							return &mockExportClientErr{err: upstreamErr}, nil
						},
					}, nil
				},
				operatorSigningKey: priv,
			}

			stream := &mockExportStream{}
			md := metadata.Pairs(
				"x-flow-namespace", "ns",
				"x-flow-graph-name", "graph",
				"authorization", "Bearer valid",
			)
			stream.ctx = metadata.NewIncomingContext(context.Background(), md)

			err = s.ExportGraph(&flowv1gen.ExportGraphRequest{Format: "bogus"}, stream)
			if err == nil {
				t.Fatal("expected an error on a pre-stream rejection")
			}
			// The pre-stream rejection code must pass through verbatim, not be flattened
			// to INTERNAL (SPEC error table: no data was sent before the rejection).
			if status.Code(err) != tc.code {
				t.Errorf("expected %v to pass through verbatim, got %v", tc.code, status.Code(err))
			}
			if len(stream.sends) != 0 {
				t.Error("expected no chunks forwarded for a pre-stream rejection")
			}
		})
	}
}

// TestExportGraphSignsAndInjectsCapabilityMetadata (item 9) verifies the capability-signed
// metadata is injected into the outgoing request forwarded to the Cartographer: the
// x-flow-capabilities-* metadata carries a valid Ed25519 signature over {cap}|{ts} that
// verifies with the operator public key.
func TestExportGraphSignsAndInjectsCapabilityMetadata(t *testing.T) {
	rt := NewProxyRoutingTable()
	rt.Register("ns", "graph", "cartographer-graph.ns.svc.cluster.local:50051")

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	var capturedMD metadata.MD
	s := &ProxyServer{
		routingTable: rt,
		k8sClient:    authProxyClient(t, true, true),
		authCache:    newAuthCache(30 * time.Second),
		dialer: func(ctx context.Context, endpoint string) (CartographerClient, error) {
			return &mockCartographerClient{
				exportGraphFn: func(ctx context.Context, in *flowv1gen.ExportGraphRequest) (flowv1gen.CartographerService_ExportGraphClient, error) {
					// The outgoing context passed to this call is the capability-injected one.
					md, _ := metadata.FromOutgoingContext(ctx)
					capturedMD = md
					return &mockExportClientEOF{}, nil
				},
			}, nil
		},
		operatorSigningKey: priv,
	}

	stream := &mockExportStream{}
	md := metadata.Pairs(
		"x-flow-namespace", "ns",
		"x-flow-graph-name", "graph",
		"authorization", "Bearer valid",
	)
	stream.ctx = metadata.NewIncomingContext(context.Background(), md)

	if err := s.ExportGraph(&flowv1gen.ExportGraphRequest{Format: "json"}, stream); err != nil {
		t.Fatalf("ExportGraph returned error: %v", err)
	}

	caps := capturedMD.Get("x-flow-capabilities")
	signedAt := capturedMD.Get("x-flow-capabilities-signed-at")
	sig := capturedMD.Get("x-flow-capabilities-signature")
	signedBy := capturedMD.Get("x-flow-capabilities-signed-by")
	if len(caps) == 0 || caps[0] != "READ:graph/entity/*" {
		t.Fatalf("expected capability metadata injected, got %v", caps)
	}
	if len(signedAt) == 0 {
		t.Fatal("expected signed-at metadata")
	}
	if len(signedBy) == 0 || signedBy[0] != "operator" {
		t.Fatalf("expected signed-by=operator, got %v", signedBy)
	}
	if len(sig) == 0 {
		t.Fatal("expected a signature to be injected (must not forward unsigned)")
	}
	raw, err := base64.StdEncoding.DecodeString(sig[0])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	payload := caps[0] + "|" + signedAt[0]
	if !ed25519.Verify(pub, []byte(payload), raw) {
		t.Error("injected capability signature did not verify over {cap}|{ts} with the operator public key")
	}
}

// TestExportGraphRouteNotRegistered (item 11) verifies the "route not registered" path
// returns codes.Unavailable and never forwards. Authorization runs first (SPEC Graph Export
// Flow step 3 precedes step 4), so an authorized caller who asks for an unregistered graph
// reaches the Lookup branch and gets Unavailable.
func TestExportGraphRouteNotRegistered(t *testing.T) {
	s := &ProxyServer{
		routingTable: NewProxyRoutingTable(), // empty → Lookup fails
		k8sClient:    authProxyClient(t, true, true),
		authCache:    newAuthCache(30 * time.Second),
	}
	stream := &mockExportStream{}
	md := metadata.Pairs(
		"x-flow-namespace", "ns",
		"x-flow-graph-name", "graph",
		"authorization", "Bearer valid",
	)
	stream.ctx = metadata.NewIncomingContext(context.Background(), md)

	err := s.ExportGraph(&flowv1gen.ExportGraphRequest{Format: "json"}, stream)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable for unregistered route, got %v", status.Code(err))
	}
	if len(stream.sends) != 0 {
		t.Error("expected no chunks forwarded for unregistered route")
	}
}

// TestExportGraphAuthorizesBeforeRouteLookup (item 11) pins the SPEC Graph Export Flow ordering
// (step 3 TokenReview+SAR precedes step 4 routing-table forward): an unauthorized caller hitting a
// *registered* route must get the auth error (PermissionDenied / Unauthenticated), never an
// Unavailable that would reveal the graph is registered, and must never reach the forward path.
func TestExportGraphAuthorizesBeforeRouteLookup(t *testing.T) {
	rt := NewProxyRoutingTable()
	rt.Register("ns", "graph", "cartographer-graph.ns.svc.cluster.local:50051") // registered

	var dialCalled bool
	s := &ProxyServer{
		routingTable: rt,
		k8sClient:    authProxyClient(t, true, false), // authenticated but NOT authorized
		authCache:    newAuthCache(30 * time.Second),
		dialer: func(ctx context.Context, endpoint string) (CartographerClient, error) {
			dialCalled = true
			return nil, errors.New("must not dial")
		},
	}

	stream := &mockExportStream{}
	md := metadata.Pairs(
		"x-flow-namespace", "ns",
		"x-flow-graph-name", "graph",
		"authorization", "Bearer unprivileged",
	)
	stream.ctx = metadata.NewIncomingContext(context.Background(), md)

	err := s.ExportGraph(&flowv1gen.ExportGraphRequest{Format: "json"}, stream)
	if err == nil {
		t.Fatal("expected an auth error for an unprivileged caller")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied (auth-first), got %v", status.Code(err))
	}
	if dialCalled {
		t.Error("unprivileged caller must not reach the forward/dial path")
	}
	if len(stream.sends) != 0 {
		t.Error("expected no chunks forwarded for an unprivileged caller")
	}

	// Same shape with an invalid token: the auth-first order surfaces Unauthenticated
	// rather than an existence-revealing Unavailable.
	s2 := &ProxyServer{
		routingTable: rt,
		k8sClient:    authProxyClient(t, false, false), // token not authenticated
		authCache:    newAuthCache(30 * time.Second),
	}
	stream2 := &mockExportStream{}
	md2 := metadata.Pairs(
		"x-flow-namespace", "ns",
		"x-flow-graph-name", "graph",
		"authorization", "Bearer bad-token",
	)
	stream2.ctx = metadata.NewIncomingContext(context.Background(), md2)
	err2 := s2.ExportGraph(&flowv1gen.ExportGraphRequest{Format: "json"}, stream2)
	if status.Code(err2) != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated (auth-first), got %v", status.Code(err2))
	}
}
