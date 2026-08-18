package proxy

import (
	"context"
	"errors"
	"io"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// mockExportClientStream is a flowv1.CartographerService_ExportGraphClient that
// yields a single chunk and then io.EOF when failErr is nil, or returns failErr
// from the first Recv — pinning the sidecar relay's mid-stream upstream branch.
type mockExportClientStream struct {
	failErr error
	calls   int
}

func (m *mockExportClientStream) Recv() (*flowv1.ExportGraphResponse, error) {
	m.calls++
	if m.failErr != nil {
		return nil, m.failErr
	}
	if m.calls == 1 {
		return &flowv1.ExportGraphResponse{Chunk: []byte("chunk")}, nil
	}
	return nil, io.EOF
}

func (mockExportClientStream) Context() context.Context { return context.Background() }
func (mockExportClientStream) Header() (metadata.MD, error) {
	return nil, nil
}
func (mockExportClientStream) Trailer() metadata.MD { return nil }
func (mockExportClientStream) CloseSend() error     { return nil }
func (mockExportClientStream) SendMsg(any) error    { return nil }
func (mockExportClientStream) RecvMsg(any) error    { return nil }

// mockExportClientChunkThenErr is a flowv1.CartographerService_ExportGraphClient
// that yields a single chunk and then returns the configured error from the
// next Recv — pinning the relay's mid-stream upstream branch (at least one
// response already forwarded) when the upstream breaks after streaming started.
type mockExportClientChunkThenErr struct {
	err   error
	calls int
}

func (m *mockExportClientChunkThenErr) Recv() (*flowv1.ExportGraphResponse, error) {
	m.calls++
	if m.calls == 1 {
		return &flowv1.ExportGraphResponse{Chunk: []byte("chunk")}, nil
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

// mockCartographerClientExport is a flowv1.CartographerServiceClient that
// answers ExportGraph with a fake client stream. err is returned from the
// first Recv (a pre-stream failure when set); chunkErr is returned from the
// second Recv, after one chunk (a mid-stream failure when set). The embedded
// interface satisfies the remaining client methods, which the relay never calls.
type mockCartographerClientExport struct {
	flowv1.CartographerServiceClient
	err      error
	chunkErr error
}

func (m *mockCartographerClientExport) ExportGraph(
	ctx context.Context, in *flowv1.ExportGraphRequest, opts ...grpc.CallOption,
) (flowv1.CartographerService_ExportGraphClient, error) {
	if m.chunkErr != nil {
		return &mockExportClientChunkThenErr{err: m.chunkErr}, nil
	}
	return &mockExportClientStream{failErr: m.err}, nil
}

// mockExportServerStream is a grpc.ServerStreamingServer[flowv1.ExportGraphResponse]
// whose Send records the number of chunks and returns err (nil → success),
// pinning the relay's downstream failure branch.
type mockExportServerStream struct {
	ctx  context.Context
	err  error
	sent int
}

func (m *mockExportServerStream) Send(*flowv1.ExportGraphResponse) error {
	m.sent++
	return m.err
}

func (m *mockExportServerStream) SetHeader(metadata.MD) error  { return nil }
func (m *mockExportServerStream) SendHeader(metadata.MD) error { return nil }
func (m *mockExportServerStream) SetTrailer(metadata.MD)       {}
func (m *mockExportServerStream) Context() context.Context {
	if m.ctx == nil {
		return context.Background()
	}
	return m.ctx
}
func (m *mockExportServerStream) SendMsg(any) error { return nil }
func (m *mockExportServerStream) RecvMsg(any) error { return nil }

// TestCartographerProxy_ExportGraph_MidStreamFailureIsInternal pins the SPEC
// error-table row "ExportGraph mid-stream failure → INTERNAL" (SPEC R11) at the
// Sidecar relay, matching the operator proxy (foundrygraph_proxyserver.go) and
// the Cartographer service handler (errExportGraphMidStream): an upstream Recv
// break AFTER the stream has started — a transport-level Unavailable or a raw
// error — and a downstream Send failure must each surface as INTERNAL, never the
// raw (non-INTERNAL) status. A pre-chunk (stream-establishment) Unavailable is
// the sibling UNAVAILABLE case, pinned separately by
// TestCartographerProxy_ExportGraph_StreamEstablishmentUnavailable.
func TestCartographerProxy_ExportGraph_MidStreamFailureIsInternal(t *testing.T) {
	// A raw (non-status) Recv error — a non-conforming upstream — is a genuine
	// mid-stream failure even at the first Recv: surface INTERNAL.
	upstreamBreaks := []struct {
		name string
		err  error
	}{
		{"raw", errors.New("malformed stream chunk")},
	}
	for _, tc := range upstreamBreaks {
		t.Run("recv_"+tc.name, func(t *testing.T) {
			p := &CartographerProxy{client: &mockCartographerClientExport{err: tc.err}}
			err := p.ExportGraph(&flowv1.ExportGraphRequest{Format: "json"}, &mockExportServerStream{})
			if st := status.Code(err); st != codes.Internal {
				t.Fatalf("expected INTERNAL for mid-stream Recv failure, got %v", err)
			}
		})
	}

	t.Run("recv_midstream_unavailable", func(t *testing.T) {
		// A transport-level break AFTER at least one chunk has been forwarded
		// is a genuine mid-stream failure (partial data may already have been
		// sent) → INTERNAL, distinct from the pre-chunk stream-establishment
		// Unavailable.
		p := &CartographerProxy{client: &mockCartographerClientExport{
			chunkErr: status.Error(codes.Unavailable, "connection reset mid-stream"),
		}}
		err := p.ExportGraph(&flowv1.ExportGraphRequest{Format: "json"}, &mockExportServerStream{})
		if st := status.Code(err); st != codes.Internal {
			t.Fatalf("expected INTERNAL for a mid-stream Unavailable, got %v", err)
		}
	})

	t.Run("send", func(t *testing.T) {
		p := &CartographerProxy{client: &mockCartographerClientExport{}}
		stream := &mockExportServerStream{err: errors.New("client stream write failed")}
		err := p.ExportGraph(&flowv1.ExportGraphRequest{Format: "json"}, stream)
		if st := status.Code(err); st != codes.Internal {
			t.Fatalf("expected INTERNAL for downstream Send failure, got %v", err)
		}
		if stream.sent == 0 {
			t.Fatal("expected Send to have been exercised before failing")
		}
	})
}

// TestCartographerProxy_ExportGraph_StreamEstablishmentUnavailable pins the
// stream-establishment transport failure at the Sidecar relay, matching the
// operator proxy's "cannot start export stream" → UNAVAILABLE mapping
// (foundrygraph_proxyserver.go / TestExportGraphCannotStartStreamIsUnavailable):
// an upstream Unavailable received at the first Recv with no chunk forwarded —
// the Cartographer could not be reached — must surface as UNAVAILABLE, not
// INTERNAL (which is reserved for a genuine mid-stream failure after data has
// been sent). The Sidecar's lazy grpc.NewClient dial delivers connection
// failures on the first Recv rather than on the stream call itself, so this is
// the Sidecar's equivalent of the operator's client-call Unavailable.
func TestCartographerProxy_ExportGraph_StreamEstablishmentUnavailable(t *testing.T) {
	p := &CartographerProxy{client: &mockCartographerClientExport{
		err: status.Error(codes.Unavailable, "connection refused"),
	}}
	stream := &mockExportServerStream{}
	err := p.ExportGraph(&flowv1.ExportGraphRequest{Format: "json"}, stream)
	if st := status.Code(err); st != codes.Unavailable {
		t.Fatalf("expected UNAVAILABLE for a stream-establishment transport failure, got %v (%v)", st, err)
	}
	if stream.sent != 0 {
		t.Error("expected no chunks forwarded for a stream-establishment failure")
	}
}

// TestCartographerProxy_ExportGraph_PreStreamRejectionPassesThrough pins the
// SPEC error-table rows "Unsupported export format" → INVALID_ARGUMENT and
// "ExportGraph buffer allocation failure" → RESOURCE_EXHAUSTED (both "no data
// sent") at the Sidecar relay, matching the operator proxy
// (foundrygraph_proxyserver.go / TestExportGraphPreStreamRejectionPassesThrough):
// a status the Cartographer returns BEFORE sending any chunk must surface
// through the relay verbatim — preserving the upstream status code and message —
// rather than being flattened to INTERNAL, so an SDK caller
// (graph.ExportGraph("bogus"), which performs no local format validation)
// receives the documented error code. The capability rejections the
// Cartographer's ingress verifier returns before any chunk — "Invalid
// capability metadata signature" and "Stale capability signature (anti-replay)"
// → PERMISSION_DENIED (SPEC error-table rows applying to ExportGraph) — are
// the same pre-stream "no data sent" case and must also surface verbatim as
// PERMISSION_DENIED, not INTERNAL. Once at least one chunk has been forwarded,
// the same upstream status is a genuine mid-stream failure (partial data may
// already have been sent) and maps to INTERNAL per the SPEC error table row
// "ExportGraph mid-stream failure".
func TestCartographerProxy_ExportGraph_PreStreamRejectionPassesThrough(t *testing.T) {
	for _, tc := range []struct {
		name string
		code codes.Code
	}{
		{"unsupported format", codes.InvalidArgument},
		{"buffer allocation failure", codes.ResourceExhausted},
		{"stale capability signature", codes.PermissionDenied},
	} {
		t.Run("prestream_"+tc.name, func(t *testing.T) {
			p := &CartographerProxy{client: &mockCartographerClientExport{
				err: status.Error(tc.code, "rejected before any chunk was sent"),
			}}
			stream := &mockExportServerStream{}
			err := p.ExportGraph(&flowv1.ExportGraphRequest{Format: "bogus"}, stream)
			if err == nil {
				t.Fatal("expected an error on a pre-stream rejection")
			}
			if st := status.Code(err); st != tc.code {
				t.Fatalf("expected %v to pass through verbatim, got %v (%v)", tc.code, st, err)
			}
			if stream.sent != 0 {
				t.Error("expected no chunks forwarded for a pre-stream rejection")
			}
		})
	}

	t.Run("midstream_after_chunk_is_internal", func(t *testing.T) {
		// A chunk was already forwarded when the upstream breaks with a
		// pre-stream rejection code: the sentAny guard flips it to the SPEC's
		// mid-stream INTERNAL, not a verbatim pass-through.
		p := &CartographerProxy{client: &mockCartographerClientExport{
			chunkErr: status.Error(codes.InvalidArgument, "rejected after one chunk"),
		}}
		stream := &mockExportServerStream{}
		err := p.ExportGraph(&flowv1.ExportGraphRequest{Format: "json"}, stream)
		if st := status.Code(err); st != codes.Internal {
			t.Fatalf("expected INTERNAL for a mid-stream failure, got %v (%v)", st, err)
		}
		if stream.sent == 0 {
			t.Fatal("expected at least one chunk forwarded before the mid-stream failure")
		}
	})

	t.Run("midstream_permission_denied_is_internal", func(t *testing.T) {
		// A PERMISSION_DENIED that arrives AFTER at least one chunk has been
		// forwarded is a genuine mid-stream failure (partial data may already
		// have been sent) → INTERNAL per the SPEC error-table row "ExportGraph
		// mid-stream failure"; only the pre-stream (no-data-sent) capability
		// rejection passes through verbatim.
		p := &CartographerProxy{client: &mockCartographerClientExport{
			chunkErr: status.Error(codes.PermissionDenied, "stale capability signature after one chunk"),
		}}
		stream := &mockExportServerStream{}
		err := p.ExportGraph(&flowv1.ExportGraphRequest{Format: "json"}, stream)
		if st := status.Code(err); st != codes.Internal {
			t.Fatalf("expected INTERNAL for a mid-stream PERMISSION_DENIED, got %v (%v)", st, err)
		}
		if stream.sent == 0 {
			t.Fatal("expected at least one chunk forwarded before the mid-stream failure")
		}
	})
}
