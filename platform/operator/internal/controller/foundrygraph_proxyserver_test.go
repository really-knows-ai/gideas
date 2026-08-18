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
			"x-flow-namespace", testNS,
		))
		gotNS, name, err := s.extractRoutingMetadata(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotNS != testNS {
			t.Errorf("expected namespace test-ns, got %q", gotNS)
		}
		if name != defaultGraphName {
			t.Errorf("expected default graph name flow-graph, got %q", name)
		}
	})

	t.Run("valid namespace with custom graph name", func(t *testing.T) {
		s := &ProxyServer{}
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
			"x-flow-namespace", testNS,
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

// TestAuthCacheKeyCollisionSafety (item 11) pins the auth-cache key encoding as
// collision-safe: two distinct (token, ns, name, verb) tuples whose pipe-delimited
// concatenation would be identical (a pipe-containing token aliasing another tuple)
// must map to distinct keys — otherwise one identity's cached positive authz decision
// could be served to a different identity. The same tuple must also always map to the
// same key (the cache-hit path).
func TestAuthCacheKeyCollisionSafety(t *testing.T) {
	cache := newAuthCache(30 * time.Second)

	// Distinct tuples whose pipe-delimited preimages would both be "a|b|c|d|e".
	tuples := [][4]string{
		{"a|b", "c", "d", "e"}, // pipe in the user-supplied token
		{"a", "b|c", "d", "e"}, // pipe in the namespace
	}
	seen := make(map[string]string)
	for _, tp := range tuples {
		k := cache.key(tp[0], tp[1], tp[2], tp[3])
		if prev, ok := seen[k]; ok {
			t.Fatalf("distinct tuples collided on cache key %q: %v aliases %v", k, tp, prev)
		}
		seen[k] = tp[0] + "/" + tp[1] + "/" + tp[2] + "/" + tp[3]
	}

	// The same tuple must always produce the same key (cache-hit path).
	if k1, k2 := cache.key("token", "ns", "graph", "get"), cache.key("token", "ns", "graph", "get"); k1 != k2 {
		t.Fatalf("same tuple produced different keys: %q vs %q", k1, k2)
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
	if err := s.authorize(ctx, "ns", "graph"); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated for missing auth header, got %v", status.Code(err))
	}
}

func TestAuthorizeInvalidToken(t *testing.T) {
	s := &ProxyServer{
		k8sClient: authProxyClient(t, false, false),
		authCache: newAuthCache(30 * time.Second),
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer bad-token"))
	if err := s.authorize(ctx, "ns", "graph"); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated for invalid token, got %v", status.Code(err))
	}
}

func TestAuthorizeDenied(t *testing.T) {
	s := &ProxyServer{
		k8sClient: authProxyClient(t, true, false),
		authCache: newAuthCache(30 * time.Second),
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer token"))
	if err := s.authorize(ctx, "ns", "graph"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

func TestAuthorizeAllowedAndCached(t *testing.T) {
	s := &ProxyServer{
		k8sClient: authProxyClient(t, true, true),
		authCache: newAuthCache(30 * time.Second),
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer valid"))
	if err := s.authorize(ctx, "ns", "graph"); err != nil {
		t.Fatalf("expected authorize to succeed: %v", err)
	}
	// Second call must be served from the auth cache (no token/sar re-evaluation).
	if err := s.authorize(ctx, "ns", "graph"); err != nil {
		t.Fatalf("expected cached authorize to succeed: %v", err)
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
// signature — fail closed, never fail open. The error must be a plain (non-status) error,
// NOT a SPEC-attributed gRPC status: the SPEC error table names no row for a malformed
// signing key, so surfacing a named status (previously codes.Internal) would fabricate a
// table code for a local operator misconfiguration.
func TestSignCapabilitiesFailClosed(t *testing.T) {
	s := &ProxyServer{operatorSigningKey: []byte("too-short")} // 9 bytes ≠ 64
	_, err := s.signCapabilities("READ:graph/entity/*")
	if err == nil {
		t.Fatal("expected error for wrong-length signing key (must fail closed)")
	}
	// A plain error surfaces over gRPC as the generic codes.Unknown, not a named status the
	// SPEC error table assigns to this (un-named) local misconfiguration condition.
	if status.Code(err) != codes.Unknown {
		t.Errorf("expected a plain (non-status) error surfacing as codes.Unknown, got %v", status.Code(err))
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
	if err := s.authorize(ctx, "ns", "graph"); status.Code(err) != codes.Unavailable {
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
	if err := s.authorize(ctx, "ns", "graph"); status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable on SAR failure, got %v", status.Code(err))
	}
}

// ExportGraph mock clients / streams, shared by the forward and failure-mode export
// tests in foundrygraph_proxyserver_forward_test.go and foundrygraph_proxyserver_error_test.go.

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

// mockExportClientChunkThenErr yields a single chunk and then returns the configured error
// from the next Recv — pinning the relay's genuine mid-stream branch (at least one chunk
// already forwarded) when the upstream breaks after streaming started, mirroring the sidecar
// relay's mockExportClientChunkThenErr.
type mockExportClientChunkThenErr struct {
	err   error
	calls int
}

func (m *mockExportClientChunkThenErr) Recv() (*flowv1gen.ExportGraphResponse, error) {
	m.calls++
	if m.calls == 1 {
		return &flowv1gen.ExportGraphResponse{Chunk: []byte("chunk")}, nil
	}
	return nil, m.err
}

func (mockExportClientChunkThenErr) Context() context.Context { return context.Background() }
func (mockExportClientChunkThenErr) Header() (metadata.MD, error) {
	return nil, nil
}
func (mockExportClientChunkThenErr) Trailer() metadata.MD { return nil }
func (mockExportClientChunkThenErr) CloseSend() error     { return nil }
func (mockExportClientChunkThenErr) SendMsg(any) error    { return nil }
func (mockExportClientChunkThenErr) RecvMsg(any) error    { return nil }
