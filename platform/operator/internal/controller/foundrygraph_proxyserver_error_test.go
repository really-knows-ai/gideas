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
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	flowv1gen "github.com/foundry/flow/gen/flow/v1"
)

// TestExportGraphDialFailureIsUnavailable exercises the dial-failure branch
// (foundrygraph_proxyserver.go:276-279): a dialer error must surface as codes.Unavailable
// ("cannot connect to cartographer"), distinguishing the SPEC R11 dial-timeout Unavailable
// from the mid-stream INTERNAL case.
func TestExportGraphDialFailureIsUnavailable(t *testing.T) {
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
			return nil, errors.New("connect failed: timeout")
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
		t.Fatal("expected an error on dial failure")
	}
	// SPEC R11: a dial-timeout/connect failure is Unavailable, NOT the mid-stream INTERNAL.
	if status.Code(err) != codes.Unavailable {
		t.Errorf("expected Unavailable on dial failure, got %v", status.Code(err))
	}
}

// TestExportGraphCannotStartStreamIsUnavailable exercises the lazy-dial / stream-establishment
// branch (foundrygraph_proxyserver.go:286-289). grpc.NewClient connects lazily, so the real
// connect happens inside client.ExportGraph(req, ctx) — an unreachable/blackholed upstream
// surfaces an error from that call, which the proxy must map to codes.Unavailable ("cannot
// start export stream"), distinct from the mid-stream INTERNAL failure. This directly covers
// the branch the dial-failure test (mock dialer returning an error) cannot reach.
func TestExportGraphCannotStartStreamIsUnavailable(t *testing.T) {
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
					// The lazy connect surfaces here, on the caller ctx, when the upstream is
					// unreachable/blackholed: status.Unavailable (e.g. "connection refused").
					return nil, status.Error(codes.Unavailable, "connection refused")
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
		t.Fatal("expected an error when the upstream stream cannot be established")
	}
	// SPEC R11: failing to establish the export stream (lazy connect) is Unavailable, NOT
	// INTERNAL (which is reserved for a stream that broke after it started).
	if status.Code(err) != codes.Unavailable {
		t.Errorf("expected Unavailable for failed stream establishment, got %v", status.Code(err))
	}
}

// TestExportGraphStreamEstablishmentCallerContextStatusPassesThrough asserts that a
// caller-context deadline/cancellation surfacing at stream establishment (the
// cc.ExportGraph(ctx, req) call) is passed through verbatim as DEADLINE_EXCEEDED /
// CANCELED rather than flattened to UNAVAILABLE — matching the Sidecar relay
// (cartographer.go, which returns the establishment error verbatim). The SPEC error-table
// row "ExportGraph stream-establishment failure" → UNAVAILABLE covers a "Cartographer could
// not be reached" transport failure, not the caller's own timeout/cancel; flattening the
// latter would make the two relays of the same row behave differently on the wire.
func TestExportGraphStreamEstablishmentCallerContextStatusPassesThrough(t *testing.T) {
	for _, tc := range []struct {
		name string
		code codes.Code
	}{
		{"deadline exceeded", codes.DeadlineExceeded},
		{"canceled", codes.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
							// The caller's context deadline/cancellation surfaces here, at
							// establishment, as the caller-context status.
							return nil, status.Error(tc.code, "caller context deadline/cancel during establishment")
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
				t.Fatal("expected an error when establishment fails with a caller-context status")
			}
			// The caller-context status must pass through verbatim, not be flattened to
			// UNAVAILABLE (the Sidecar relay surfaces the establishment error verbatim).
			if status.Code(err) != tc.code {
				t.Errorf("expected %v to pass through verbatim, got %v", tc.code, status.Code(err))
			}
			if len(stream.sends) != 0 {
				t.Error("expected no chunks forwarded for an establishment failure")
			}
		})
	}
}

// TestExportGraphRawRecvErrorIsInternal exercises the raw (non-status) mid-stream Recv error
// mapping (foundrygraph_proxyserver.go:296-303): a plain error from Recv — not a gRPC status —
// falls through to the "export stream failed" INTERNAL branch. The existing tests only inject
// gRPC status errors (Internal/Unavailable) via mockExportClientErr, so this raw-error branch
// had no coverage.
func TestExportGraphRawRecvErrorIsInternal(t *testing.T) {
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
					// mockExportClientErr returns the wrapped err from Recv; a plain (non-status)
					// error exercises the raw-error Internal fallthrough.
					return &mockExportClientErr{err: errors.New("malformed stream chunk")}, nil
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
		t.Fatal("expected an error on a raw Recv error")
	}
	// SPEC R11: any mid-stream export failure after establishment is INTERNAL.
	if status.Code(err) != codes.Internal {
		t.Errorf("expected Internal for a raw Recv error, got %v", status.Code(err))
	}
}

// TestExportGraphStreamEstablishmentUnavailableIsUnavailable asserts that an upstream
// transport Unavailable at the first Recv with no chunk forwarded — a stream-establishment
// failure, the Cartographer could not be reached before any data was sent (SPEC error table
// row "ExportGraph stream-establishment failure → UNAVAILABLE") — surfaces as UNAVAILABLE,
// not INTERNAL. The lazy grpc.NewClient dial delivers the connect failure on the first Recv
// rather than on the stream call itself, so this is the operator's first-Recv equivalent of
// the dial-timeout Unavailable, matching the sidecar relay
// (TestCartographerProxy_ExportGraph_StreamEstablishmentUnavailable).
func TestExportGraphStreamEstablishmentUnavailableIsUnavailable(t *testing.T) {
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
					return &mockExportClientErr{err: status.Error(codes.Unavailable, "connection refused")}, nil
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
		t.Fatal("expected an error on stream-establishment failure")
	}
	// SPEC error table: a no-data-sent transport failure at stream establishment is
	// UNAVAILABLE, not the INTERNAL reserved for a genuine mid-stream break.
	if status.Code(err) != codes.Unavailable {
		t.Errorf("expected UNAVAILABLE for stream-establishment failure, got %v", status.Code(err))
	}
	if len(stream.sends) != 0 {
		t.Error("expected no chunks forwarded for a stream-establishment failure")
	}
}
