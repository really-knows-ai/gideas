package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/tools/flowctl/manifestfs"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// ─── Test doubles ──────────────────────────────────────────────────────────

type fakeGraphExportStream struct {
	recv func() (*flowv1.ExportGraphResponse, error)
}

func (s *fakeGraphExportStream) Recv() (*flowv1.ExportGraphResponse, error) {
	return s.recv()
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// metadataCapturingServer is a minimal gRPC server that records incoming
// metadata for testing prodGraphExporter.dialOperatorStream outbound keys.
type metadataCapturingServer struct {
	flowv1.UnimplementedCartographerServiceServer
	capturedMD *metadata.MD
}

func (s *metadataCapturingServer) ExportGraph(_ *flowv1.ExportGraphRequest, stream flowv1.CartographerService_ExportGraphServer) error {
	if md, ok := metadata.FromIncomingContext(stream.Context()); ok {
		*s.capturedMD = md
	}
	return stream.Send(&flowv1.ExportGraphResponse{Chunk: []byte("ok")})
}

// fakeGraphExporter lets every interaction be forced to fail.
type fakeGraphExporter struct {
	connectErr error
	lookupErr  error
	ns         string
	nsErr      error
	podName    string
	found      bool
	findErr    error
	localPort  int
	fwdErr     error
	token      string
	tokenErr   error
	streamFn   func() graphExportStream
	streamErr  error
	// dialedFormat records the format passed to dialOperatorStream, so tests
	// can pin the format default that reaches the wire.
	dialedFormat string
	// dialedGraphName records the graph name passed to dialOperatorStream, so
	// tests can pin the graph-name default that reaches the wire.
	dialedGraphName string
}

var _ graphExporter = (*fakeGraphExporter)(nil)

func (f *fakeGraphExporter) checkConnectivity(context.Context) error { return f.connectErr }

func (f *fakeGraphExporter) lookupFoundryGraph(context.Context, string, string) error {
	return f.lookupErr
}

func (f *fakeGraphExporter) resolveOperatorNamespace(context.Context) (string, error) {
	return f.ns, f.nsErr
}

func (f *fakeGraphExporter) findReadyOperatorPod(context.Context, string) (string, bool, error) {
	return f.podName, f.found, f.findErr
}

func (f *fakeGraphExporter) forwardOperatorPod(context.Context, string, string) (int, func(), error) {
	if f.fwdErr != nil {
		return 0, func() {}, f.fwdErr
	}
	return f.localPort, func() {}, nil
}

func (f *fakeGraphExporter) bearerToken() (string, error) { return f.token, f.tokenErr }

func (f *fakeGraphExporter) dialOperatorStream(_ context.Context, _ int, _, _, name, format string) (graphExportStream, func(), error) {
	f.dialedGraphName = name
	f.dialedFormat = format
	if f.streamErr != nil {
		return nil, func() {}, f.streamErr
	}
	if f.streamFn != nil {
		return f.streamFn(), func() {}, nil
	}
	return eofStream(), func() {}, nil
}

func eofStream() graphExportStream {
	return &fakeGraphExportStream{recv: func() (*flowv1.ExportGraphResponse, error) {
		return nil, io.EOF
	}}
}

func chunkStream(chunks ...string) graphExportStream {
	i := 0
	return &fakeGraphExportStream{recv: func() (*flowv1.ExportGraphResponse, error) {
		if i < len(chunks) {
			c := chunks[i]
			i++
			return &flowv1.ExportGraphResponse{Chunk: []byte(c)}, nil
		}
		return nil, io.EOF
	}}
}

func successExporter() *fakeGraphExporter {
	return &fakeGraphExporter{
		ns:        "flow-system",
		podName:   "operator-0",
		found:     true,
		localPort: 50055,
		token:     "test-token",
	}
}

func testParams() graphExportParams {
	return graphExportParams{namespace: "flow-system", graphName: "flow-graph", format: "json"}
}

// runGraphExportCmd executes the graph export command with the given args and
// an injected exporter, exercising the real command wiring (flag defaults,
// namespace resolution, stdout/file output) without a Kubernetes cluster.
func runGraphExportCmd(t *testing.T, exporter graphExporter, args ...string) error {
	t.Helper()
	origFn := newGraphExporterFn
	newGraphExporterFn = func() (graphExporter, error) { return exporter, nil }
	t.Cleanup(func() { newGraphExporterFn = origFn })

	cmd := newGraphExportCmd()
	cmd.SetArgs(args)
	return cmd.Execute()
}

// runGraphExportToFile exercises the file-output path in runGraphExport by
// creating a cobra.Command with the given flags and injecting a fake exporter.
// This allows testing os.Create failure, flush failure, close failure, and
// partial-file cleanup without requiring a real Kubernetes cluster.
func runGraphExportToFile(t *testing.T, exporter graphExporter, outputPath string, params graphExportParams) error {
	return runGraphExportCmd(t, exporter,
		"--format", params.format,
		"--namespace", params.namespace,
		"--graph-name", params.graphName,
		"--output", outputPath,
	)
}

// scriptedFile is an io.WriteCloser wrapping a real output file whose Write and
// Close can be forced to fail, so runGraphExport's flush/close-failure branches
// are driven directly against a real file on disk.
type scriptedFile struct {
	*os.File
	writeErr error
	closeErr error
}

func (s *scriptedFile) Write(p []byte) (int, error) {
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return s.File.Write(p)
}

func (s *scriptedFile) Close() error {
	if s.closeErr != nil {
		_ = s.File.Close()
		return s.closeErr
	}
	return s.File.Close()
}

// captureStdout redirects os.Stdout to a pipe and returns a function that
// closes the write end and returns everything written to stdout so far.
func captureStdout(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	t.Cleanup(func() {
		os.Stdout = orig
		_ = w.Close()
		_ = r.Close()
	})
	return func() string {
		_ = w.Close()
		data, _ := io.ReadAll(r)
		return string(data)
	}
}

// ─── Format pre-validation ──────────────────────────────────────────────────

func TestValidateExportFormat(t *testing.T) {
	for _, ok := range []string{"json", "graphml"} {
		if err := validateExportFormat(ok); err != nil {
			t.Errorf("expected %q to be valid, got: %v", ok, err)
		}
	}
	if err := validateExportFormat("xml"); err == nil {
		t.Error("expected unsupported format to be rejected, got nil")
	}
}

func TestRunGraphExport_UnsupportedFormatRejected(t *testing.T) {
	params := testParams()
	params.format = "xml"
	err := runGraphExportWith(context.Background(), successExporter(), params, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected format rejection error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid export format") {
		t.Errorf("expected error to mention invalid format, got: %v", err)
	}
}

// ─── FoundryGraph validation ───────────────────────────────────────────────

func TestRunGraphExport_FoundryGraphNotFound_FailedPrecondition(t *testing.T) {
	e := successExporter()
	e.lookupErr = apierrors.NewNotFound(
		schema.GroupResource{Group: "flow.foundry.io", Resource: "foundrygraphs"}, "flow-graph",
	)
	err := runGraphExportWith(context.Background(), e, testParams(), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	for _, want := range []string{"FAILED_PRECONDITION", "flow-graph", "flow-system"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to contain %q, got: %v", want, err)
		}
	}
}

func TestRunGraphExport_FoundryGraphLookupError(t *testing.T) {
	e := successExporter()
	e.lookupErr = errors.New("boom")
	err := runGraphExportWith(context.Background(), e, testParams(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "failed to look up FoundryGraph") {
		t.Fatalf("expected lookup error, got: %v", err)
	}
}

// ─── Operator namespace / pod resolution ──────────────────────────────────

func TestRunGraphExport_ResolveOperatorNamespaceError(t *testing.T) {
	e := successExporter()
	e.nsErr = errors.New("permission denied")
	err := runGraphExportWith(context.Background(), e, testParams(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot resolve operator namespace") {
		t.Fatalf("expected namespace resolution error, got: %v", err)
	}
}

func TestRunGraphExport_FindOperatorPodError(t *testing.T) {
	e := successExporter()
	e.findErr = errors.New("api error")
	err := runGraphExportWith(context.Background(), e, testParams(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "finding Ready operator pod") {
		t.Fatalf("expected finding-pod error, got: %v", err)
	}
}

func TestRunGraphExport_NoReadyOperatorPod(t *testing.T) {
	e := successExporter()
	e.found = false
	err := runGraphExportWith(context.Background(), e, testParams(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "no Ready operator pod found in namespace") {
		t.Fatalf("expected no-ready-pod error, got: %v", err)
	}
}

// ─── Port-forward / token ─────────────────────────────────────────────────

func TestRunGraphExport_ForwardOperatorPodError(t *testing.T) {
	e := successExporter()
	e.fwdErr = errors.New("forbidden")
	err := runGraphExportWith(context.Background(), e, testParams(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "port-forward to operator pod failed") {
		t.Fatalf("expected forward error, got: %v", err)
	}
}

func TestRunGraphExport_BearerTokenMissing(t *testing.T) {
	e := successExporter()
	e.tokenErr = errors.New("no bearer token")
	err := runGraphExportWith(context.Background(), e, testParams(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot resolve bearer token") {
		t.Fatalf("expected token error, got: %v", err)
	}
}

// ─── Dial / streaming ───────────────────────────────────────────────────────

func TestRunGraphExport_DialOperatorStreamError(t *testing.T) {
	e := successExporter()
	e.streamErr = errors.New("unavailable")
	err := runGraphExportWith(context.Background(), e, testParams(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "export graph failed") {
		t.Fatalf("expected dial/export error, got: %v", err)
	}
}

func TestRunGraphExport_StreamReceiveError(t *testing.T) {
	e := successExporter()
	e.streamFn = func() graphExportStream {
		return &fakeGraphExportStream{recv: func() (*flowv1.ExportGraphResponse, error) {
			return nil, errors.New("connection reset")
		}}
	}
	err := runGraphExportWith(context.Background(), e, testParams(), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected stream receive error, got nil")
	}
	for _, want := range []string{"stream error", "Possible causes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to contain %q, got: %v", want, err)
		}
	}
}

func TestRunGraphExport_StreamReceiveStatusError(t *testing.T) {
	e := successExporter()
	e.streamFn = func() graphExportStream {
		return &fakeGraphExportStream{recv: func() (*flowv1.ExportGraphResponse, error) {
			return nil, status.Error(codes.Internal, "operator failed mid-stream")
		}}
	}
	err := runGraphExportWith(context.Background(), e, testParams(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "stream error (Internal)") {
		t.Fatalf("expected annotated status error, got: %v", err)
	}
}

func TestRunGraphExport_WriteError(t *testing.T) {
	e := successExporter()
	e.streamFn = func() graphExportStream { return chunkStream("payload") }
	err := runGraphExportWith(context.Background(), e, testParams(), errWriter{})
	if err == nil || !strings.Contains(err.Error(), "write error") {
		t.Fatalf("expected write error, got: %v", err)
	}
}

// ─── Success path ──────────────────────────────────────────────────────────

func TestRunGraphExport_SuccessStreamsChunks(t *testing.T) {
	e := successExporter()
	e.streamFn = func() graphExportStream { return chunkStream("aaa", "bbb") }
	buf := &bytes.Buffer{}
	err := runGraphExportWith(context.Background(), e, testParams(), buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.String() != "aaabbb" {
		t.Errorf("expected output %q, got %q", "aaabbb", buf.String())
	}
}

// ─── Item 21: Outbound metadata on real gRPC dialer ────────────────────────

func TestProdDialOperatorStream_AttachesMetadata(t *testing.T) {
	// Start a real gRPC server that captures incoming metadata.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var capturedMD metadata.MD
	srv := grpc.NewServer()
	flowv1.RegisterCartographerServiceServer(srv, &metadataCapturingServer{capturedMD: &capturedMD})
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	port := lis.Addr().(*net.TCPAddr).Port

	// prodGraphExporter.dialOperatorStream only needs a valid gRPC address;
	// k8s and pfm are unused by this method.
	exporter := &prodGraphExporter{}
	stream, stop, err := exporter.dialOperatorStream(
		context.Background(), port, "tok-abc", "ns-1", "my-graph", "json",
	)
	if err != nil {
		t.Fatalf("dialOperatorStream: %v", err)
	}
	defer stop()

	// Receive the single chunk the server sends.
	_, err = stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}

	// Assert all three outbound metadata keys.
	want := map[string]string{
		"authorization":     "Bearer tok-abc",
		"x-flow-namespace":  "ns-1",
		"x-flow-graph-name": "my-graph",
	}
	for key, wantVal := range want {
		vals := capturedMD.Get(key)
		if len(vals) == 0 {
			t.Errorf("missing metadata key %q", key)
			continue
		}
		if vals[0] != wantVal {
			t.Errorf("metadata %q = %q, want %q", key, vals[0], wantVal)
		}
	}
}

// ─── Item 36: Real-mode dial / stream error surfaces ───────────────────────

// establishmentErrorServer rejects the export stream with a status error before
// sending any data, so the client's first Recv surfaces it.
type establishmentErrorServer struct {
	flowv1.UnimplementedCartographerServiceServer
	code codes.Code
	msg  string
}

func (s *establishmentErrorServer) ExportGraph(_ *flowv1.ExportGraphRequest, _ flowv1.CartographerService_ExportGraphServer) error {
	return status.Error(s.code, s.msg)
}

// midStreamErrorServer sends one chunk and then fails the stream with a status
// error, so the client's second Recv surfaces it mid-export.
type midStreamErrorServer struct {
	flowv1.UnimplementedCartographerServiceServer
	code codes.Code
	msg  string
}

func (s *midStreamErrorServer) ExportGraph(_ *flowv1.ExportGraphRequest, stream flowv1.CartographerService_ExportGraphServer) error {
	if err := stream.Send(&flowv1.ExportGraphResponse{Chunk: []byte("partial")}); err != nil {
		return err
	}
	return status.Error(s.code, s.msg)
}

// startCartographerServer starts a real CartographerService gRPC server on an
// ephemeral port and returns the local port (same setup as
// TestProdDialOperatorStream_AttachesMetadata).
func startCartographerServer(t *testing.T, impl flowv1.CartographerServiceServer) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	flowv1.RegisterCartographerServiceServer(srv, impl)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().(*net.TCPAddr).Port
}

// realDialExporter is a fakeGraphExporter whose dial step runs through the real
// prodGraphExporter.dialOperatorStream, so the CLI's dial/stream error handling
// is exercised against a real gRPC dialer and server rather than the fake.
type realDialExporter struct {
	fakeGraphExporter
	port int
}

func (e *realDialExporter) dialOperatorStream(ctx context.Context, _ int, token, namespace, name, format string) (graphExportStream, func(), error) {
	return (&prodGraphExporter{}).dialOperatorStream(ctx, e.port, token, namespace, name, format)
}

func TestProdDialOperatorStream_DialFailure(t *testing.T) {
	// grpc.NewClient is lazy, so a dial failure surfaces as an Unavailable
	// status error when ExportGraph tries to establish the stream. Reserve a
	// port and close the listener so the address reliably refuses connections.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()

	_, _, err = (&prodGraphExporter{}).dialOperatorStream(
		context.Background(), port, "tok-abc", "ns-1", "my-graph", "json",
	)
	if err == nil {
		t.Fatal("expected dial failure, got nil")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.Unavailable {
		t.Errorf("expected Unavailable status error, got: %v", err)
	}
	// The error must reference the refused address, proving the real dialer
	// attempted to reach it.
	if !strings.Contains(err.Error(), fmt.Sprintf(":%d", port)) {
		t.Errorf("expected error to reference the dialed port, got: %v", err)
	}
}

func TestProdDialOperatorStream_StreamEstablishmentStatusError(t *testing.T) {
	port := startCartographerServer(t, &establishmentErrorServer{
		code: codes.FailedPrecondition,
		msg:  "no Cartographer endpoint registered for this FoundryGraph",
	})

	stream, stop, err := (&prodGraphExporter{}).dialOperatorStream(
		context.Background(), port, "tok-abc", "ns-1", "my-graph", "json",
	)
	if err == nil {
		defer stop()
		// The server's rejection is delivered on the first Recv.
		_, err = stream.Recv()
	}
	if err == nil {
		t.Fatal("expected stream establishment status error, got nil")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.FailedPrecondition {
		t.Errorf("expected FailedPrecondition status error, got: %v", err)
	}
}

func TestProdDialOperatorStream_StreamMidStreamStatusError(t *testing.T) {
	port := startCartographerServer(t, &midStreamErrorServer{
		code: codes.Internal,
		msg:  "operator failed mid-stream",
	})

	stream, stop, err := (&prodGraphExporter{}).dialOperatorStream(
		context.Background(), port, "tok-abc", "ns-1", "my-graph", "json",
	)
	if err != nil {
		t.Fatalf("dialOperatorStream: %v", err)
	}
	defer stop()

	if _, err := stream.Recv(); err != nil {
		t.Fatalf("first Recv: %v", err)
	}
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected mid-stream status error, got nil")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.Internal {
		t.Errorf("expected Internal status error, got: %v", err)
	}
}

func TestRunGraphExport_RealDialErrorSurfaces(t *testing.T) {
	// Point the real dialer at a closed port: the CLI must wrap the resulting
	// Unavailable as an "export graph failed" error.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()

	e := &realDialExporter{fakeGraphExporter: *successExporter(), port: port}
	err = runGraphExportWith(context.Background(), e, testParams(), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected dial error, got nil")
	}
	for _, want := range []string{"export graph failed", "Unavailable"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to contain %q, got: %v", want, err)
		}
	}
}

func TestRunGraphExport_RealStreamStatusErrorSurfaces(t *testing.T) {
	// A real server that sends a chunk then fails with INTERNAL: the CLI's Recv
	// loop must annotate the status code and surface the error.
	port := startCartographerServer(t, &midStreamErrorServer{
		code: codes.Internal,
		msg:  "operator failed mid-stream",
	})

	e := &realDialExporter{fakeGraphExporter: *successExporter(), port: port}
	err := runGraphExportWith(context.Background(), e, testParams(), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected stream error, got nil")
	}
	if !strings.Contains(err.Error(), "stream error (Internal)") {
		t.Errorf("expected annotated stream error, got: %v", err)
	}
}

// ─── Item 22: checkConnectivity failure branch ─────────────────────────────

func TestRunGraphExport_CheckConnectivityFails(t *testing.T) {
	e := successExporter()
	e.connectErr = errors.New("apiserver unreachable")
	err := runGraphExportWith(context.Background(), e, testParams(), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "apiserver unreachable") {
		t.Errorf("expected connectivity error, got: %v", err)
	}
}

// ─── Item 23: Output-file branches ─────────────────────────────────────────

func TestRunGraphExport_StreamFailureCleansUpOutputFile(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "export.json")

	e := successExporter()
	e.streamFn = func() graphExportStream {
		return &fakeGraphExportStream{recv: func() (*flowv1.ExportGraphResponse, error) {
			return nil, errors.New("connection lost")
		}}
	}

	err := runGraphExportToFile(t, e, outPath, testParams())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "stream error") {
		t.Errorf("expected stream error, got: %v", err)
	}
	// The partial file must have been cleaned up.
	if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
		t.Errorf("expected partial output file to be removed, but it still exists")
	}
}

func TestRunGraphExport_OutputCreateFails(t *testing.T) {
	e := successExporter()
	e.streamFn = func() graphExportStream { return eofStream() }

	// Use a path under a non-existent directory so os.Create fails.
	err := runGraphExportToFile(t, e, filepath.Join(t.TempDir(), "no", "such", "dir", "export.json"), testParams())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "create output file") {
		t.Errorf("expected create-output-file error, got: %v", err)
	}
}

func TestRunGraphExport_FlushFailureCleansUpOutputFile(t *testing.T) {
	// Drive runGraphExport's flush-failure branch directly: the stream
	// succeeds, the buffered data fails to flush to the underlying file, and
	// the partial file must be removed.
	dir := t.TempDir()
	outPath := filepath.Join(dir, "flush-fail.json")

	origFileFn := newExportFileFn
	newExportFileFn = func(path string) (io.WriteCloser, error) {
		f, err := os.Create(path)
		if err != nil {
			return nil, err
		}
		return &scriptedFile{File: f, writeErr: errors.New("flush boom")}, nil
	}
	t.Cleanup(func() { newExportFileFn = origFileFn })

	e := successExporter()
	e.streamFn = func() graphExportStream { return chunkStream("partial") }

	err := runGraphExportToFile(t, e, outPath, testParams())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	for _, want := range []string{"flush output file", "flush boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to contain %q, got: %v", want, err)
		}
	}
	if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
		t.Errorf("expected partial output file to be removed after flush failure, but it still exists")
	}
}

func TestRunGraphExport_CloseFailureCleansUpOutputFile(t *testing.T) {
	// Drive runGraphExport's close-failure branch directly: the stream and
	// flush succeed, the file close fails, and the output must be removed so
	// the caller doesn't mistake it for a complete graph.
	dir := t.TempDir()
	outPath := filepath.Join(dir, "close-fail.json")

	origFileFn := newExportFileFn
	newExportFileFn = func(path string) (io.WriteCloser, error) {
		f, err := os.Create(path)
		if err != nil {
			return nil, err
		}
		return &scriptedFile{File: f, closeErr: errors.New("close boom")}, nil
	}
	t.Cleanup(func() { newExportFileFn = origFileFn })

	e := successExporter()
	e.streamFn = func() graphExportStream { return chunkStream("partial") }

	err := runGraphExportToFile(t, e, outPath, testParams())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	for _, want := range []string{"close output file", "close boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to contain %q, got: %v", want, err)
		}
	}
	if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
		t.Errorf("expected output file to be removed after close failure, but it still exists")
	}
}

func TestRunGraphExport_RemoveStreamOutput_NilFile(t *testing.T) {
	// removeStreamOutput is a no-op when outputFile is nil (stdout path).
	removeStreamOutput(nil, "")
	// No assertion needed — just verify it doesn't panic.
}

func TestRunGraphExport_SuccessKeepsOutputFile(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "export.json")

	e := successExporter()
	e.streamFn = func() graphExportStream { return chunkStream("graph-data") }

	err := runGraphExportToFile(t, e, outPath, testParams())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(data) != "graph-data" {
		t.Errorf("expected %q, got %q", "graph-data", string(data))
	}
}

// TestRunGraphExport_StreamFailureCleanupOnFile verifies the full round-trip:
// a file is created, the stream writes partial data, then the stream fails —
// the partial file must be removed so the caller doesn't mistake truncated
// output for a complete graph.
func TestRunGraphExport_StreamFailureCleanupOnFile(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "partial.json")

	var recvCount int
	e := successExporter()
	e.streamFn = func() graphExportStream {
		return &fakeGraphExportStream{recv: func() (*flowv1.ExportGraphResponse, error) {
			recvCount++
			if recvCount == 1 {
				return &flowv1.ExportGraphResponse{Chunk: []byte(`{"nodes":[`)}, nil
			}
			return nil, fmt.Errorf("mid-stream reset")
		}}
	}

	err := runGraphExportToFile(t, e, outPath, testParams())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
		t.Errorf("expected partial output file to be removed after stream failure, but it still exists")
	}
}

// ─── Item: R11 CLI defaults (SPEC: "defaults: json to stdout") ─────────────

func TestGraphExportCmd_DefaultFormatIsJSON(t *testing.T) {
	cmd := newGraphExportCmd()
	format, err := cmd.Flags().GetString("format")
	if err != nil {
		t.Fatalf("get format flag: %v", err)
	}
	if format != formatJSON {
		t.Errorf("expected default --format %q, got %q", formatJSON, format)
	}
	output, err := cmd.Flags().GetString("output")
	if err != nil {
		t.Fatalf("get output flag: %v", err)
	}
	if output != "" {
		t.Errorf("expected default --output to be empty (stdout), got %q", output)
	}
	graphName, err := cmd.Flags().GetString("graph-name")
	if err != nil {
		t.Fatalf("get graph-name flag: %v", err)
	}
	if graphName != defaultGraphName {
		t.Errorf("expected default --graph-name %q, got %q", defaultGraphName, graphName)
	}
}

func TestRunGraphExport_DefaultFormatReachesDial(t *testing.T) {
	// No --format flag: the default "json" must reach the operator stream.
	e := successExporter()
	e.streamFn = func() graphExportStream { return eofStream() }

	if err := runGraphExportCmd(t, e, "--namespace", "flow-system"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.dialedFormat != formatJSON {
		t.Errorf("expected default format %q on the wire, got %q", formatJSON, e.dialedFormat)
	}
}

func TestRunGraphExport_DefaultGraphNameReachesDial(t *testing.T) {
	// No --graph-name flag: the default "flow-graph" must reach the operator
	// stream as the graph name (SPEC Graph Export Flow step 2, per R1).
	e := successExporter()
	e.streamFn = func() graphExportStream { return eofStream() }

	if err := runGraphExportCmd(t, e, "--namespace", "flow-system"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.dialedGraphName != defaultGraphName {
		t.Errorf("expected default graph name %q on the wire, got %q", defaultGraphName, e.dialedGraphName)
	}
}

func TestRunGraphExport_OmittedOutputWritesToStdout(t *testing.T) {
	// No --output flag: the graph stream must be written to stdout.
	e := successExporter()
	e.streamFn = func() graphExportStream { return chunkStream("graph-data") }

	out := captureStdout(t)
	if err := runGraphExportCmd(t, e, "--namespace", "flow-system"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := out(); got != "graph-data" {
		t.Errorf("expected graph data on stdout, got %q", got)
	}
}

func TestRunGraphExport_DashOutputWritesToStdout(t *testing.T) {
	// --output -: an explicit dash means stdout.
	e := successExporter()
	e.streamFn = func() graphExportStream { return chunkStream("graph-data") }

	out := captureStdout(t)
	if err := runGraphExportCmd(t, e, "--namespace", "flow-system", "--output", "-"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := out(); got != "graph-data" {
		t.Errorf("expected graph data on stdout, got %q", got)
	}
}

// ─── Item: resolveOperatorProxyPort branches ───────────────────────────────

func TestResolveOperatorProxyPort_Default(t *testing.T) {
	t.Setenv("OPERATOR_PROXY_PORT", "")
	port, err := resolveOperatorProxyPort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != operatorProxyPort {
		t.Errorf("expected default port %d, got %d", operatorProxyPort, port)
	}
}

func TestResolveOperatorProxyPort_EnvOverride(t *testing.T) {
	t.Setenv("OPERATOR_PROXY_PORT", "50054")
	port, err := resolveOperatorProxyPort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != 50054 {
		t.Errorf("expected env-override port 50054, got %d", port)
	}
}

func TestResolveOperatorProxyPort_ParseError(t *testing.T) {
	t.Setenv("OPERATOR_PROXY_PORT", "not-a-port")
	_, err := resolveOperatorProxyPort()
	if err == nil || !strings.Contains(err.Error(), "OPERATOR_PROXY_PORT parse error") {
		t.Fatalf("expected parse error, got: %v", err)
	}
}

// ─── Item 35: Operator pod selector pinned to the deployed manager.yaml ─────

func TestOperatorLabelSelectorMatchesDeployedManagerManifest(t *testing.T) {
	// The CLI's operator pod selector must match the pod-template label in the
	// deployed operator manager.yaml (the kustomize-deployed manifest that
	// flowctl embeds as operator/deployment.yaml). Pin the embedded copy
	// against that source file rather than against itself, so the embedded
	// copy silently drifting from the source is caught here.
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "platform", "operator", "config", "manager", "manager.yaml"))
	if err != nil {
		t.Fatalf("read source manager.yaml: %v", err)
	}
	// manager.yaml is a multi-document stream (Namespace + Deployment); the
	// Deployment document is the one after the first `---` separator.
	parts := strings.SplitN(string(source), "\n---\n", 2)
	if len(parts) != 2 {
		t.Fatal("source manager.yaml is missing the document separator before the Deployment document")
	}
	var srcDep appsv1.Deployment
	if err := yaml.Unmarshal([]byte(parts[1]), &srcDep); err != nil {
		t.Fatalf("parse source manager.yaml Deployment document: %v", err)
	}
	srcLabel := srcDep.Spec.Template.Labels["app.kubernetes.io/name"]
	if srcLabel == "" {
		t.Fatal("source manager.yaml Deployment pod template is missing the app.kubernetes.io/name label")
	}

	embedded, err := manifestfs.Manifests.ReadFile("operator/deployment.yaml")
	if err != nil {
		t.Fatalf("read embedded operator deployment: %v", err)
	}
	var dep appsv1.Deployment
	if err := yaml.Unmarshal(embedded, &dep); err != nil {
		t.Fatalf("parse embedded operator deployment: %v", err)
	}
	if got := dep.Spec.Template.Labels["app.kubernetes.io/name"]; got != srcLabel {
		t.Errorf("embedded deployment.yaml pod-template label = %q, want %q (from source manager.yaml)", got, srcLabel)
	}
	if want := "app.kubernetes.io/name=" + srcLabel; operatorLabelSelector != want {
		t.Errorf("operatorLabelSelector = %q, want %q (sourced from deployed manager.yaml)", operatorLabelSelector, want)
	}
}
