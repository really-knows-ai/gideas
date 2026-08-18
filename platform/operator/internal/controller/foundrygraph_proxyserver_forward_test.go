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
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	flowv1gen "github.com/foundry/flow/gen/flow/v1"
)

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

// TestExportGraphSendErrorIsInternal asserts the proxy's stream.Send error branch: when the
// client's stream write fails mid-export (e.g. the client has disconnected), the proxy must
// return the SPEC's mid-stream-failure code (error table: "ExportGraph mid-stream failure →
// INTERNAL") rather than silently succeeding or surfacing a different code.
func TestExportGraphSendErrorIsInternal(t *testing.T) {
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
	if status.Code(err) != codes.Internal {
		t.Errorf("expected codes.Internal when client stream Send fails (SPEC: ExportGraph mid-stream failure → INTERNAL), got %v", status.Code(err))
	}
	if stream.sendCalls == 0 {
		t.Fatal("expected Send to have been exercised")
	}
}

// TestExportGraphPropagatesUpstreamStatus asserts an upstream INTERNAL status on a mid-stream
// Recv error surfaces as INTERNAL to the caller (SPEC error table: "ExportGraph mid-stream
// failure → INTERNAL") — the upstream error must not be recast as a dial-style Unavailable.
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
