package flow

import (
	"context"
	"io"
	"net"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const embassyTestWorkitemID = "wi-1"

type embassyHandlerSpy struct {
	lastPreflight *flowv1.PreflightManifestRequest
	lastStream    []*flowv1.PackageChunk
	lastExport    *flowv1.ExportPackageRequest
}

func (s *embassyHandlerSpy) PreflightManifest(
	_ context.Context, req *flowv1.PreflightManifestRequest,
) (*flowv1.PreflightManifestResponse, error) {
	s.lastPreflight = req
	return &flowv1.PreflightManifestResponse{Accepted: true, TransferId: "accepted-1"}, nil
}

func (s *embassyHandlerSpy) StreamPackage(
	_ context.Context, chunks []*flowv1.PackageChunk,
) (*flowv1.StreamPackageResponse, error) {
	s.lastStream = chunks
	return &flowv1.StreamPackageResponse{WorkitemId: "imported-42"}, nil
}

func (s *embassyHandlerSpy) ExportPackage(
	_ context.Context, req *flowv1.ExportPackageRequest,
) ([]*flowv1.PackageChunk, error) {
	s.lastExport = req
	return []*flowv1.PackageChunk{
		{Chunk: &flowv1.PackageChunk_Manifest{Manifest: &flowv1.TransferManifest{TransferId: "tx-export"}}},
		{Chunk: &flowv1.PackageChunk_Trailer{Trailer: &flowv1.PackageTrailer{PackageDigest: "sha256:abc"}}},
	}, nil
}

func setupEmbassyServerClient(t *testing.T, handler EmbassyServiceHandler) flowv1.EmbassyServiceClient {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := grpc.NewServer()
	flowv1.RegisterEmbassyServiceServer(srv, NewEmbassyServer(handler))
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	t.Cleanup(func() {
		_ = conn.Close()
		srv.GracefulStop()
	})

	return flowv1.NewEmbassyServiceClient(conn)
}

func TestEmbassyServerDelegatesToHandler(t *testing.T) {
	t.Parallel()

	handler := &embassyHandlerSpy{}
	client := setupEmbassyServerClient(t, handler)

	preflightResp, err := client.PreflightManifest(context.Background(), &flowv1.PreflightManifestRequest{
		Manifest: &flowv1.TransferManifest{ImportType: "law-petition", TransferId: "tx-1"},
	})
	if err != nil {
		t.Fatalf("PreflightManifest() returned error: %v", err)
	}
	if !preflightResp.GetAccepted() || handler.lastPreflight.GetManifest().GetTransferId() != "tx-1" {
		t.Fatal("expected preflight call to be delegated to handler")
	}

	stream, err := client.StreamPackage(context.Background())
	if err != nil {
		t.Fatalf("StreamPackage() open returned error: %v", err)
	}
	if err := stream.Send(&flowv1.PackageChunk{
		Chunk: &flowv1.PackageChunk_Content{Content: []byte("hello")},
	}); err != nil {
		t.Fatalf("Send() returned error: %v", err)
	}
	streamResp, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatalf("CloseAndRecv() returned error: %v", err)
	}
	if streamResp.GetWorkitemId() != "imported-42" || len(handler.lastStream) != 1 {
		t.Fatal("expected stream package call to be delegated to handler")
	}

	exportStream, err := client.ExportPackage(context.Background(), &flowv1.ExportPackageRequest{
		WorkitemId: embassyTestWorkitemID,
		ImportType: "law-petition",
	})
	if err != nil {
		t.Fatalf("ExportPackage() returned error: %v", err)
	}
	chunk, err := exportStream.Recv()
	if err != nil {
		t.Fatalf("Recv() returned error: %v", err)
	}
	if handler.lastExport.GetWorkitemId() != embassyTestWorkitemID ||
		chunk.GetManifest().GetTransferId() != "tx-export" {
		t.Fatal("expected export package call to be delegated to handler")
	}
	_, err = exportStream.Recv()
	if err != nil {
		t.Fatalf("Recv() second returned error: %v", err)
	}
	_, err = exportStream.Recv()
	if err != io.EOF {
		t.Fatalf("expected EOF after export chunks, got %v", err)
	}
}

func TestResolveEmbassyImportTypeMergesBuiltInAndFlowDefined(t *testing.T) {
	t.Parallel()

	resolved, ok := ResolveEmbassyImportType(
		"law-petition",
		DefaultSystemImportTypes(),
		map[string]EmbassyFlowImportTypeSpec{"external-submission": {Node: "intake"}},
	)
	if !ok || !resolved.BuiltIn {
		t.Fatal("expected built-in law-petition import type to resolve")
	}

	custom, ok := ResolveEmbassyImportType(
		"external-submission",
		DefaultSystemImportTypes(),
		map[string]EmbassyFlowImportTypeSpec{"external-submission": {Node: "intake"}},
	)
	if !ok || custom.BuiltIn || custom.Spec == nil || custom.Spec.Node != "intake" {
		t.Fatal("expected flow-defined import type to resolve with spec")
	}
}

type stagingSpy struct {
	manifest *flowv1.TransferManifest
	chunks   []*flowv1.PackageChunk
}

func (s *stagingSpy) StageManifest(_ context.Context, manifest *flowv1.TransferManifest) error {
	s.manifest = manifest
	return nil
}

func (s *stagingSpy) StageChunk(_ context.Context, chunk *flowv1.PackageChunk) error {
	s.chunks = append(s.chunks, chunk)
	return nil
}

func (s *stagingSpy) Complete(_ context.Context) (*EmbassyStagedPackage, error) {
	return &EmbassyStagedPackage{Manifest: s.manifest, Chunks: s.chunks}, nil
}

type materializerSpy struct {
	importType EmbassyResolvedImportType
	pkg        *EmbassyStagedPackage
}

func (m *materializerSpy) MaterializeImport(
	_ context.Context, importType EmbassyResolvedImportType, pkg *EmbassyStagedPackage,
) (*flowv1.StreamPackageResponse, error) {
	m.importType = importType
	m.pkg = pkg
	return &flowv1.StreamPackageResponse{WorkitemId: "materialized-1"}, nil
}

func TestMaterializeStreamedPackageUsesStagerAndMaterializer(t *testing.T) {
	t.Parallel()

	stager := &stagingSpy{}
	materializer := &materializerSpy{}
	importType := EmbassyResolvedImportType{Name: "law-petition", BuiltIn: true}

	resp, err := MaterializeStreamedPackage(context.Background(), stager, materializer, importType, []*flowv1.PackageChunk{
		{Chunk: &flowv1.PackageChunk_Manifest{Manifest: &flowv1.TransferManifest{TransferId: "tx-stage"}}},
		{Chunk: &flowv1.PackageChunk_Content{Content: []byte("payload")}},
	})
	if err != nil {
		t.Fatalf("MaterializeStreamedPackage() returned error: %v", err)
	}
	if resp.GetWorkitemId() != "materialized-1" {
		t.Fatalf("expected materialized workitem id, got %q", resp.GetWorkitemId())
	}
	if stager.manifest.GetTransferId() != "tx-stage" || len(materializer.pkg.Chunks) != 2 {
		t.Fatal("expected staged package to include manifest and chunks")
	}
	if !materializer.importType.BuiltIn {
		t.Fatal("expected built-in import type to be passed to materializer")
	}
}
