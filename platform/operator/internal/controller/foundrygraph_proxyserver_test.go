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
	"io"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	flowv1gen "github.com/foundry/flow/gen/flow/v1"
)

func TestProxyServerExtractRoutingMetadata(t *testing.T) {
	t.Run("missing namespace", func(t *testing.T) {
		s := &ProxyServer{}
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs())
		_, _, err := s.extractRoutingMetadata(ctx)
		if err == nil {
			t.Fatal("expected error for missing namespace")
		}
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("expected InvalidArgument, got %v", status.Code(err))
		}
	})

	t.Run("valid namespace with default graph name", func(t *testing.T) {
		s := &ProxyServer{}
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
			"x-flow-namespace", "test-ns",
		))
		gotNS, name, err := s.extractRoutingMetadata(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotNS != "test-ns" {
			t.Errorf("expected namespace test-ns, got %q", gotNS)
		}
		if name != "flow-graph" {
			t.Errorf("expected default graph name flow-graph, got %q", name)
		}
	})

	t.Run("valid namespace with custom graph name", func(t *testing.T) {
		s := &ProxyServer{}
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
			"x-flow-namespace", "test-ns",
			"x-flow-graph-name", "my-graph",
		))
		_, name, err := s.extractRoutingMetadata(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if name != "my-graph" {
			t.Errorf("expected graph name my-graph, got %q", name)
		}
	})
}

func TestAuthCache(t *testing.T) {
	cache := newAuthCache(0) // zero TTL means entries expire immediately

	// Set and immediately get should work for >0 TTL
	cache2 := newAuthCache(0)
	cache2.Set("test-key")
	if cache2.Get("test-key") {
		t.Fatal("expected expired entry to return false")
	}

	// Verify empty cache returns false
	if cache.Get("nonexistent") {
		t.Fatal("expected nonexistent entry to return false")
	}
}

func TestProxyUnimplemented(t *testing.T) {
	s := &ProxyServer{}
	err := s.proxyUnimplemented("TestMethod")
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if status.Code(err) != codes.Unimplemented {
		t.Errorf("expected Unimplemented, got %v", status.Code(err))
	}
}

// authProxyClient builds a fake client whose Create interceptor populates
// TokenReview / SubjectAccessReview statuses — simulating the in-cluster API
// server responses — so the authorize path can be tested without a real cluster.
func authProxyClient(t *testing.T, authenticated, allowed bool) client.Client {
	t.Helper()

	interceptorFuncs := interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			switch o := obj.(type) {
			case *authenticationv1.TokenReview:
				o.Status = authenticationv1.TokenReviewStatus{
					Authenticated: authenticated,
					User:          authenticationv1.UserInfo{Username: "test-user"},
				}
			case *authorizationv1.SubjectAccessReview:
				o.Status = authorizationv1.SubjectAccessReviewStatus{Allowed: allowed}
			}
			return nil
		},
	}

	_ = authenticationv1.AddToScheme(scheme.Scheme)
	_ = authorizationv1.AddToScheme(scheme.Scheme)

	return fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithInterceptorFuncs(interceptorFuncs).
		Build()
}

func TestAuthorizeMissingAuthHeader(t *testing.T) {
	s := &ProxyServer{
		k8sClient: authProxyClient(t, true, true),
		authCache: newAuthCache(30 * time.Second),
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs())
	if err := s.authorize(ctx, "ns", "graph", "get"); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated for missing auth header, got %v", status.Code(err))
	}
}

func TestAuthorizeInvalidToken(t *testing.T) {
	s := &ProxyServer{
		k8sClient: authProxyClient(t, false, false),
		authCache: newAuthCache(30 * time.Second),
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer bad-token"))
	if err := s.authorize(ctx, "ns", "graph", "get"); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated for invalid token, got %v", status.Code(err))
	}
}

func TestAuthorizeDenied(t *testing.T) {
	s := &ProxyServer{
		k8sClient: authProxyClient(t, true, false),
		authCache: newAuthCache(30 * time.Second),
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer token"))
	if err := s.authorize(ctx, "ns", "graph", "get"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

func TestAuthorizeAllowedAndCached(t *testing.T) {
	s := &ProxyServer{
		k8sClient: authProxyClient(t, true, true),
		authCache: newAuthCache(30 * time.Second),
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer valid"))
	if err := s.authorize(ctx, "ns", "graph", "get"); err != nil {
		t.Fatalf("expected authorize to succeed: %v", err)
	}
	// Second call must be served from the auth cache (no token/sar re-evaluation).
	if err := s.authorize(ctx, "ns", "graph", "get"); err != nil {
		t.Fatalf("expected cached authorize to succeed: %v", err)
	}
}

// mockExportClientWithChunks returns one chunk then io.EOF.
type mockExportClientWithChunks struct {
	calls int
}

func (m *mockExportClientWithChunks) Recv() (*flowv1gen.ExportGraphResponse, error) {
	m.calls++
	if m.calls == 1 {
		return &flowv1gen.ExportGraphResponse{Chunk: []byte("hello")}, nil
	}
	return nil, io.EOF
}

func (mockExportClientWithChunks) Context() context.Context { return context.Background() }
func (mockExportClientWithChunks) Header() (metadata.MD, error) {
	return nil, nil
}
func (mockExportClientWithChunks) Trailer() metadata.MD { return nil }
func (mockExportClientWithChunks) CloseSend() error     { return nil }
func (mockExportClientWithChunks) SendMsg(any) error    { return nil }
func (mockExportClientWithChunks) RecvMsg(any) error    { return nil }

// mockExportClientEOF returns io.EOF immediately (empty stream, OK termination).
type mockExportClientEOF struct{}

func (mockExportClientEOF) Recv() (*flowv1gen.ExportGraphResponse, error) {
	return nil, io.EOF
}

func (mockExportClientEOF) Context() context.Context { return context.Background() }
func (mockExportClientEOF) Header() (metadata.MD, error) {
	return nil, nil
}
func (mockExportClientEOF) Trailer() metadata.MD { return nil }
func (mockExportClientEOF) CloseSend() error     { return nil }
func (mockExportClientEOF) SendMsg(any) error    { return nil }
func (mockExportClientEOF) RecvMsg(any) error    { return nil }

// mockExportStream implements the server streaming interface for ExportGraph tests.
type mockExportStream struct {
	ctx   context.Context
	sends []*flowv1gen.ExportGraphResponse
}

func (m *mockExportStream) Send(r *flowv1gen.ExportGraphResponse) error {
	m.sends = append(m.sends, r)
	return nil
}

func (m *mockExportStream) SetHeader(metadata.MD) error  { return nil }
func (m *mockExportStream) SendHeader(metadata.MD) error { return nil }
func (m *mockExportStream) SetTrailer(metadata.MD)       {}
func (m *mockExportStream) Context() context.Context     { return m.ctx }
func (m *mockExportStream) SendMsg(any) error            { return nil }
func (m *mockExportStream) RecvMsg(any) error            { return nil }

func TestExportGraphForwarding(t *testing.T) {
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
					return &mockExportClientWithChunks{}, nil
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

	req := &flowv1gen.ExportGraphRequest{Format: "json"}
	if err := s.ExportGraph(req, stream); err != nil {
		t.Fatalf("ExportGraph returned error: %v", err)
	}
	if len(stream.sends) == 0 {
		t.Fatal("expected at least one forwarded chunk")
	}
}

func TestExportGraphForwardingEmptyStream(t *testing.T) {
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
		t.Fatalf("ExportGraph on empty stream should succeed, got: %v", err)
	}
	if len(stream.sends) != 0 {
		t.Fatalf("expected no chunks forwarded, got %d", len(stream.sends))
	}
}

// mockExportStreamSendErr is a server stream whose Send always returns an error, forcing the
// proxy's stream.Send failure branch (foundrygraph_proxyserver.go:305-307). It yields a single
// upstream chunk so Send is actually reached.
type mockExportStreamSendErr struct {
	ctx       context.Context
	sendCalls int
}

func (m *mockExportStreamSendErr) Send(r *flowv1gen.ExportGraphResponse) error {
	m.sendCalls++
	return errors.New("client stream write failed")
}

func (m *mockExportStreamSendErr) SetHeader(metadata.MD) error  { return nil }
func (m *mockExportStreamSendErr) SendHeader(metadata.MD) error { return nil }
func (m *mockExportStreamSendErr) SetTrailer(metadata.MD)       {}
func (m *mockExportStreamSendErr) Context() context.Context     { return m.ctx }
func (m *mockExportStreamSendErr) SendMsg(any) error            { return nil }
func (m *mockExportStreamSendErr) RecvMsg(any) error            { return nil }

// TestExportGraphSendErrorIsCanceled asserts the proxy's stream.Send error branch: when the
// client's stream write fails (e.g. the client has disconnected), the proxy must return a
// codes.Canceled error to the caller rather than silently succeeding or forking a different
// code.
func TestExportGraphSendErrorIsCanceled(t *testing.T) {
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
					return &mockExportClientWithChunks{}, nil
				},
			}, nil
		},
		operatorSigningKey: priv,
	}

	stream := &mockExportStreamSendErr{}
	md := metadata.Pairs(
		"x-flow-namespace", "ns",
		"x-flow-graph-name", "graph",
		"authorization", "Bearer valid",
	)
	stream.ctx = metadata.NewIncomingContext(context.Background(), md)

	err = s.ExportGraph(&flowv1gen.ExportGraphRequest{Format: "json"}, stream)
	if err == nil {
		t.Fatal("expected an error when Send fails")
	}
	if status.Code(err) != codes.Canceled {
		t.Errorf("expected codes.Canceled when client stream Send fails, got %v", status.Code(err))
	}
	if stream.sendCalls == 0 {
		t.Fatal("expected Send to have been exercised")
	}
}

type mockExportClientErr struct {
	err error
}

func (m *mockExportClientErr) Recv() (*flowv1gen.ExportGraphResponse, error) { return nil, m.err }
func (mockExportClientErr) Context() context.Context                         { return context.Background() }
func (mockExportClientErr) Header() (metadata.MD, error)                     { return nil, nil }
func (mockExportClientErr) Trailer() metadata.MD                             { return nil }
func (mockExportClientErr) CloseSend() error                                 { return nil }
func (mockExportClientErr) SendMsg(any) error                                { return nil }
func (mockExportClientErr) RecvMsg(any) error                                { return nil }

// TestExportGraphPropagatesUpstreamStatus asserts the proxy propagates the upstream gRPC
// status on a mid-stream Recv error (SPEC R11: ExportGraph mid-stream failure → INTERNAL)
// rather than recasting every upstream error as Unavailable.
func TestExportGraphPropagatesUpstreamStatus(t *testing.T) {
	rt := NewProxyRoutingTable()
	rt.Register("ns", "graph", "cartographer-graph.ns.svc.cluster.local:50051")

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	upstreamErr := status.Error(codes.Internal, "stream broke during export")
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

	err = s.ExportGraph(&flowv1gen.ExportGraphRequest{Format: "json"}, stream)
	if err == nil {
		t.Fatal("expected an error on upstream stream failure")
	}
	// The upstream INTERNAL status must be propagated, not masked as Unavailable.
	if status.Code(err) != codes.Internal {
		t.Errorf("expected upstream status code Internal to propagate, got %v", status.Code(err))
	}
}

// TestExportGraphStreamUsesCallerContext verifies the ExportGraph stream is established on the
// caller's context, not the 10s dial deadline. grpc-go binds a client stream's lifetime to the
// context passed to the stream RPC, so passing the dial deadline would cut any export that
// streams past 10s mid-stream (SPEC R11 INTERNAL). Decoupling the stream from the dial window
// proves a stream that outlives the dial window is not cut.
func TestExportGraphStreamUsesCallerContext(t *testing.T) {
	rt := NewProxyRoutingTable()
	rt.Register("ns1", "graph", "cartographer-graph.ns1.svc.cluster.local:50051")

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	var streamCtx context.Context
	s := &ProxyServer{
		routingTable: rt,
		k8sClient:    authProxyClient(t, true, true),
		authCache:    newAuthCache(30 * time.Second),
		dialer: func(ctx context.Context, endpoint string) (CartographerClient, error) {
			return &mockCartographerClient{
				exportGraphFn: func(ctx context.Context, in *flowv1gen.ExportGraphRequest) (flowv1gen.CartographerService_ExportGraphClient, error) {
					streamCtx = ctx
					return &mockExportClientEOF{}, nil
				},
			}, nil
		},
		operatorSigningKey: priv,
	}

	stream := &mockExportStream{}
	md := metadata.Pairs(
		"x-flow-namespace", "ns1",
		"x-flow-graph-name", "graph",
		"authorization", "Bearer valid",
	)
	stream.ctx = metadata.NewIncomingContext(context.Background(), md)

	if err := s.ExportGraph(&flowv1gen.ExportGraphRequest{Format: "json"}, stream); err != nil {
		t.Fatalf("ExportGraph returned error: %v", err)
	}
	if streamCtx == nil {
		t.Fatal("expected the ExportGraph call to receive a context")
	}
	// The caller's context carries no deadline; if the dialCtx (10s) were passed instead, a
	// deadline would be set and the stream would be cut once it fired.
	if deadline, ok := streamCtx.Deadline(); ok {
		t.Errorf("expected the ExportGraph stream context to carry no dial deadline, got deadline %v (stream would be cut mid-export)", deadline)
	}
}

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

// TestExportGraphMidStreamUnavailableIsInternal asserts that a mid-stream transport-level
// break (Unavailable) after the stream has started surfaces as the SPEC's INTERNAL, not a
// dial-timeout Unavailable.
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
					return &mockExportClientErr{err: status.Error(codes.Unavailable, "connection reset mid-stream")}, nil
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
	// SPEC R11: mid-stream export failure is INTERNAL, not the Unavailable used for dial.
	if status.Code(err) != codes.Internal {
		t.Errorf("expected INTERNAL for mid-stream break, got %v", status.Code(err))
	}
}

// TestSignCapabilitiesValidSignature (item 9) verifies signCapabilities produces an
// Ed25519 signature over {cap}|{ts} that verifies against the operator public key.
func TestSignCapabilitiesValidSignature(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	s := &ProxyServer{operatorSigningKey: priv}

	// Accept a fixed "now" via a wrapper: use signCapabilities with capabilities and verify
	// the returned signature against the payload it claims.
	caps, err := s.signCapabilities("READ:graph/entity/*")
	if err != nil {
		t.Fatalf("signCapabilities: %v", err)
	}
	if caps.capabilities != "READ:graph/entity/*" {
		t.Errorf("capabilities mismatch: %q", caps.capabilities)
	}
	if caps.signedBy != "operator" {
		t.Errorf("signed-by mismatch: %q", caps.signedBy)
	}
	if caps.signature == "" {
		t.Fatal("expected a non-empty signature")
	}
	payload := caps.capabilities + "|" + caps.signedAt
	sig, err := base64.StdEncoding.DecodeString(caps.signature)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if !ed25519.Verify(pub, []byte(payload), sig) {
		t.Error("signature did not verify over {cap}|{ts}")
	}
}

// TestSignCapabilitiesFailClosed (item 10) asserts signCapabilities returns an error when
// the operator signing key has the wrong length, rather than forwarding an unsigned/empty
// signature — fail closed, never fail open.
func TestSignCapabilitiesFailClosed(t *testing.T) {
	s := &ProxyServer{operatorSigningKey: []byte("too-short")} // 9 bytes ≠ 64
	_, err := s.signCapabilities("READ:graph/entity/*")
	if err == nil {
		t.Fatal("expected error for wrong-length signing key (must fail closed)")
	}
	if status.Code(err) != codes.Internal {
		t.Errorf("expected codes.Internal on key mismatch, got %v", status.Code(err))
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

// TestAuthorizeTokenReviewFailure verifies the TokenReview Create error → codes.Unavailable
// branch inside authorize (item 11).
func TestAuthorizeTokenReviewFailure(t *testing.T) {
	interceptorFuncs := interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*authenticationv1.TokenReview); ok {
				return errors.New("TokenReview API unreachable")
			}
			return nil
		},
	}
	_ = authenticationv1.AddToScheme(scheme.Scheme)
	_ = authorizationv1.AddToScheme(scheme.Scheme)
	fakeCli := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithInterceptorFuncs(interceptorFuncs).Build()

	s := &ProxyServer{k8sClient: fakeCli, authCache: newAuthCache(30 * time.Second)}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer token"))
	if err := s.authorize(ctx, "ns", "graph", "get"); status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable on TokenReview failure, got %v", status.Code(err))
	}
}

func TestAuthorizeSARFailure(t *testing.T) {
	interceptorFuncs := interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			switch o := obj.(type) {
			case *authenticationv1.TokenReview:
				o.Status = authenticationv1.TokenReviewStatus{Authenticated: true, User: authenticationv1.UserInfo{Username: "user"}}
				return nil
			case *authorizationv1.SubjectAccessReview:
				return errors.New("SAR API unreachable")
			}
			return nil
		},
	}
	_ = authenticationv1.AddToScheme(scheme.Scheme)
	_ = authorizationv1.AddToScheme(scheme.Scheme)
	fakeCli := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithInterceptorFuncs(interceptorFuncs).Build()

	s := &ProxyServer{k8sClient: fakeCli, authCache: newAuthCache(30 * time.Second)}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer token"))
	if err := s.authorize(ctx, "ns", "graph", "get"); status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable on SAR failure, got %v", status.Code(err))
	}
}
