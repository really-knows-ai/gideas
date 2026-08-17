package relay

import (
	"errors"
	"io"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// stubUpstream is an ExportGraphClient whose Recv behaviour is scripted: a
// single optional chunk, then one optional error, then io.EOF.
type stubUpstream struct {
	chunk *flowv1.ExportGraphResponse
	err   error
	calls int
}

func (s *stubUpstream) Recv() (*flowv1.ExportGraphResponse, error) {
	s.calls++
	if s.chunk != nil && s.calls == 1 {
		return s.chunk, nil
	}
	if s.err != nil && (s.chunk == nil || s.calls > 1) {
		return nil, s.err
	}
	return nil, io.EOF
}

// stubDownstream is an ExportGraphServer whose Send returns sendErr (nil →
// success) and records the number of chunk forwards.
type stubDownstream struct {
	sendErr error
	sent    int
}

func (s *stubDownstream) Send(*flowv1.ExportGraphResponse) error {
	s.sent++
	return s.sendErr
}

// TestExportGraph_PreStreamRejectionPassesThrough pins the SPEC R11 error-table
// rows that are "no data sent" pre-stream conditions: "Unsupported export
// format" → INVALID_ARGUMENT, "ExportGraph buffer allocation failure" →
// RESOURCE_EXHAUSTED, "ExportGraph stream-establishment failure" → UNAVAILABLE,
// and the capability rejections ("Invalid capability metadata signature" /
// "Stale capability signature (anti-replay)" / "Read operation without READ
// capability") → PERMISSION_DENIED. The upstream returns them BEFORE any chunk,
// so the relay must pass them through verbatim — preserving the upstream status
// code and message — rather than flattening them to INTERNAL. No chunk may have
// been forwarded.
func TestExportGraph_PreStreamRejectionPassesThrough(t *testing.T) {
	for _, tc := range []struct {
		name string
		code codes.Code
	}{
		{"unsupported format", codes.InvalidArgument},
		{"buffer allocation failure", codes.ResourceExhausted},
		{"stream-establishment failure", codes.Unavailable},
		{"capability rejection", codes.PermissionDenied},
	} {
		t.Run("prestream_"+tc.name, func(t *testing.T) {
			upstreamErr := status.Error(tc.code, "rejected before any chunk")
			downstream := &stubDownstream{}
			err := ExportGraph(&stubUpstream{err: upstreamErr}, downstream)
			if err == nil {
				t.Fatal("expected an error on a pre-stream rejection")
			}
			if st := status.Code(err); st != tc.code {
				t.Fatalf("expected %v to pass through verbatim, got %v (%v)", tc.code, st, err)
			}
			if downstream.sent != 0 {
				t.Errorf("expected no chunks forwarded for a pre-stream rejection, got %d", downstream.sent)
			}
		})
	}
}

// TestExportGraph_MidStreamFailureIsInternal pins the SPEC R11 error-table row
// "ExportGraph mid-stream failure" → INTERNAL: once at least one chunk has been
// forwarded, any subsequent upstream Recv failure — a pre-stream rejection code,
// a transport-level Unavailable, or a raw (non-status) error — is a genuine
// mid-stream failure (partial data may already have been sent) and must surface
// as INTERNAL, never the raw code.
func TestExportGraph_MidStreamFailureIsInternal(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"invalid_argument_after_chunk", status.Error(codes.InvalidArgument, "rejected after one chunk")},
		{"resource_exhausted_after_chunk", status.Error(codes.ResourceExhausted, "exhausted after one chunk")},
		{"unavailable_after_chunk", status.Error(codes.Unavailable, "connection reset mid-stream")},
		{"permission_denied_after_chunk", status.Error(codes.PermissionDenied, "stale capability after one chunk")},
		{"raw_error_after_chunk", errors.New("malformed stream chunk")},
	} {
		t.Run("recv_"+tc.name, func(t *testing.T) {
			downstream := &stubDownstream{}
			err := ExportGraph(&stubUpstream{
				chunk: &flowv1.ExportGraphResponse{Chunk: []byte("chunk")},
				err:   tc.err,
			}, downstream)
			if st := status.Code(err); st != codes.Internal {
				t.Fatalf("expected INTERNAL for a mid-stream failure, got %v (%v)", st, err)
			}
			if downstream.sent == 0 {
				t.Fatal("expected at least one chunk forwarded before the mid-stream failure")
			}
		})
	}
}

// TestExportGraph_SendErrorIsInternal pins the downstream Send failure branch of
// the SPEC R11 error-table row "ExportGraph mid-stream failure" → INTERNAL: when
// the downstream stream breaks after a chunk has been forwarded, the relay
// surfaces INTERNAL, not the raw Send error.
func TestExportGraph_SendErrorIsInternal(t *testing.T) {
	downstream := &stubDownstream{sendErr: errors.New("client stream write failed")}
	err := ExportGraph(&stubUpstream{chunk: &flowv1.ExportGraphResponse{Chunk: []byte("chunk")}}, downstream)
	if st := status.Code(err); st != codes.Internal {
		t.Fatalf("expected INTERNAL for a downstream Send failure, got %v (%v)", st, err)
	}
	if downstream.sent == 0 {
		t.Fatal("expected Send to have been exercised before failing")
	}
}

// TestExportGraph_EOFIsNil pins the clean end-of-stream branch: an upstream
// io.EOF with no error returns nil, so a fully drained export succeeds.
func TestExportGraph_EOFIsNil(t *testing.T) {
	if err := ExportGraph(&stubUpstream{}, &stubDownstream{}); err != nil {
		t.Fatalf("expected nil for io.EOF, got %v", err)
	}
}

// TestExportGraph_ForwardsChunks verifies the happy path: chunks are relayed
// downstream in order until the stream ends, and io.EOF yields a nil return.
func TestExportGraph_ForwardsChunks(t *testing.T) {
	downstream := &stubDownstream{}
	err := ExportGraph(&stubUpstream{chunk: &flowv1.ExportGraphResponse{Chunk: []byte("chunk")}}, downstream)
	if err != nil {
		t.Fatalf("expected nil after forwarding chunks, got %v", err)
	}
	if downstream.sent != 1 {
		t.Fatalf("expected 1 chunk forwarded, got %d", downstream.sent)
	}
}
