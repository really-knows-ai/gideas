package flow

import (
	"context"
	"io"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

// fakeExportStream is a minimal grpc.ServerStreamingClient[ExportGraphResponse]
// that serves pre-set byte chunks then io.EOF.
type fakeExportStream struct {
	ctx    context.Context
	chunks [][]byte
}

func (s *fakeExportStream) Recv() (*flowv1.ExportGraphResponse, error) {
	if len(s.chunks) == 0 {
		return nil, io.EOF
	}
	chunk := s.chunks[0]
	s.chunks = s.chunks[1:]
	return &flowv1.ExportGraphResponse{Chunk: chunk}, nil
}
func (s *fakeExportStream) Header() (metadata.MD, error) { return nil, nil }
func (s *fakeExportStream) Trailer() metadata.MD         { return nil }
func (s *fakeExportStream) CloseSend() error             { return nil }
func (s *fakeExportStream) Context() context.Context     { return s.ctx }
func (s *fakeExportStream) SendMsg(any) error            { return nil }
func (s *fakeExportStream) RecvMsg(any) error            { return nil }

// TestGraph_ExportGraph pins graph.ExportGraph's wire mapping and stream
// semantics (SPEC R2): the format reaches the request, the session per-call
// timeout bounds the whole stream lifetime, chunks arrive in order, and the
// stream ends with io.EOF.
func TestGraph_ExportGraph(t *testing.T) {
	var capturedFormat string
	var capturedStreamCtx context.Context
	mock := &mockCartographerClient{
		exportGraph: func(
			ctx context.Context, req *flowv1.ExportGraphRequest,
		) (grpc.ServerStreamingClient[flowv1.ExportGraphResponse], error) {
			capturedFormat = req.GetFormat()
			capturedStreamCtx = ctx
			return &fakeExportStream{ctx: ctx, chunks: [][]byte{[]byte(`{"nodes":[`), []byte(`]}`)}}, nil
		},
	}
	g := newMockGraph(mock)
	g.session.timeout = 5 * time.Second

	stream, err := g.ExportGraph("json")
	if err != nil {
		t.Fatalf("ExportGraph returned error: %v", err)
	}
	if capturedFormat != "json" {
		t.Errorf("expected format json on the request, got %q", capturedFormat)
	}
	// The session timeout must bound the stream: grpc-go pins the deadline
	// context passed to the streaming RPC for its whole lifetime.
	dl, ok := capturedStreamCtx.Deadline()
	if !ok {
		t.Fatal("expected the stream context to carry the session timeout deadline")
	}
	if remaining := time.Until(dl); remaining <= 0 || remaining > 5*time.Second {
		t.Errorf("expected the stream deadline ~5s out, got %v remaining", remaining)
	}

	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("first Recv() returned error: %v", err)
	}
	if string(first.GetChunk()) != `{"nodes":[` {
		t.Errorf("unexpected first chunk %q", first.GetChunk())
	}
	second, err := stream.Recv()
	if err != nil {
		t.Fatalf("second Recv() returned error: %v", err)
	}
	if string(second.GetChunk()) != `]}` {
		t.Errorf("unexpected second chunk %q", second.GetChunk())
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("expected io.EOF at the end of the stream, got %v", err)
	}
}

// TestGraph_BeginTransaction pins graph.BeginTransaction (SPEC R4): the
// requested WithTimeout reaches the wire, and the returned handle is wired to
// the session and the Graph's shared ID-to-type cache so transaction writes
// inject the transaction ID and resolve capability types against the same
// cache the graph reads populate.
func TestGraph_BeginTransaction(t *testing.T) {
	var capturedTimeout time.Duration
	var hadTimeout bool
	mock := &mockCartographerClient{
		beginTx: func(ctx context.Context, req *flowv1.BeginTransactionRequest) (*flowv1.BeginTransactionResponse, error) {
			if req.GetTimeout() != nil {
				hadTimeout = true
				capturedTimeout = req.GetTimeout().AsDuration()
			}
			return &flowv1.BeginTransactionResponse{
				TransactionId:  testUUIDTx,
				AppliedTimeout: durationpb.New(48 * time.Hour),
			}, nil
		},
	}
	g := newMockGraph(mock)

	tx, err := g.BeginTransaction(WithTimeout(48 * time.Hour))
	if err != nil {
		t.Fatalf("BeginTransaction returned error: %v", err)
	}
	if !hadTimeout || capturedTimeout != 48*time.Hour {
		t.Errorf("expected the requested 48h timeout on the wire, got %v (present=%v)", capturedTimeout, hadTimeout)
	}
	if tx.id != testUUIDTx {
		t.Errorf("expected tx handle ID %s, got %q", testUUIDTx, tx.id)
	}
	if tx.session != g.session {
		t.Error("expected the tx handle to share the graph's session")
	}
	if tx.idTypeMap != g.idTypeMap {
		t.Error("expected the tx handle to share the graph's ID-to-type cache")
	}
	// The response's applied_timeout ("the value actually granted", SPEC R2)
	// must be surfaced on the handle rather than dropped.
	if got := tx.AppliedTimeout(); got != 48*time.Hour {
		t.Errorf("expected the granted 48h applied timeout on the handle, got %v", got)
	}
}

// TestGraph_BeginTransaction_NoTimeoutOmitted pins the omitted-timeout
// branch: without WithTimeout the request carries no timeout field and the
// server applies its default transaction timeout.
func TestGraph_BeginTransaction_NoTimeoutOmitted(t *testing.T) {
	var hadTimeout bool
	mock := &mockCartographerClient{
		beginTx: func(ctx context.Context, req *flowv1.BeginTransactionRequest) (*flowv1.BeginTransactionResponse, error) {
			hadTimeout = req.GetTimeout() != nil
			return &flowv1.BeginTransactionResponse{TransactionId: testUUIDTx}, nil
		},
	}
	g := newMockGraph(mock)

	if _, err := g.BeginTransaction(); err != nil {
		t.Fatalf("BeginTransaction returned error: %v", err)
	}
	if hadTimeout {
		t.Error("expected no timeout field on the wire when WithTimeout is omitted (server default)")
	}
}

// TestGraph_BeginTransaction_TimeoutPassedVerbatim pins the R9 WithTimeout
// branch end to end: the requested duration is passed to the wire verbatim
// (no silent capping), and a request exceeding the 7-day hard maximum is
// rejected by the Cartographer with INVALID_ARGUMENT which the SDK surfaces.
func TestGraph_BeginTransaction_TimeoutPassedVerbatim(t *testing.T) {
	var captured time.Duration
	mock := &mockCartographerClient{
		beginTx: func(ctx context.Context, req *flowv1.BeginTransactionRequest) (*flowv1.BeginTransactionResponse, error) {
			captured = req.GetTimeout().AsDuration()
			return nil, status.Error(codes.InvalidArgument, "timeout exceeds the 7-day maximum")
		},
	}
	g := newMockGraph(mock)

	_, err := g.BeginTransaction(WithTimeout(10 * 24 * time.Hour))
	if captured != 10*24*time.Hour {
		t.Fatalf("expected the requested 10d timeout on the wire verbatim (no capping), got %v", captured)
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument surfaced for an oversized timeout, got %v (%v)", status.Code(err), err)
	}
}

// TestGraph_BeginTransaction_NonPositiveTimeoutPassed pins the sibling half of
// the R2/R9 WithTimeout contract on the Begin path: a zero or negative
// WithTimeout is passed to the wire verbatim (no silent default-substitution),
// so the SPEC error-table row "Invalid transaction timeout duration" →
// INVALID_ARGUMENT is reachable on BeginTransaction exactly as on
// ExtendTimeout (TestTx_ExtendTimeout / TestTx_ExtendTimeout_RejectsOversized
// pin the sibling path).
func TestGraph_BeginTransaction_NonPositiveTimeoutPassed(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Hour} {
		t.Run(d.String(), func(t *testing.T) {
			var captured time.Duration
			var hadTimeout bool
			mock := &mockCartographerClient{
				beginTx: func(ctx context.Context, req *flowv1.BeginTransactionRequest) (*flowv1.BeginTransactionResponse, error) {
					hadTimeout = req.GetTimeout() != nil
					if hadTimeout {
						captured = req.GetTimeout().AsDuration()
					}
					return nil, status.Error(codes.InvalidArgument, "duration must be positive")
				},
			}
			g := newMockGraph(mock)

			_, err := g.BeginTransaction(WithTimeout(d))
			if !hadTimeout || captured != d {
				t.Fatalf("expected the requested %v timeout on the wire verbatim, got %v (present=%v)", d, captured, hadTimeout)
			}
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument surfaced for a non-positive timeout, got %v (%v)", status.Code(err), err)
			}
		})
	}
}
