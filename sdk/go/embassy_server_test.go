package flow

import (
	"context"
	"io"
	"net"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	embassyTestWorkitemID = "wi-1"
	embassyTestTxID       = "tx-1"
)

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
		Manifest: &flowv1.TransferManifest{ImportType: "law-petition", TransferId: embassyTestTxID},
	})
	if err != nil {
		t.Fatalf("PreflightManifest() returned error: %v", err)
	}
	if !preflightResp.GetAccepted() || handler.lastPreflight.GetManifest().GetTransferId() != embassyTestTxID {
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
		testSystemImportTypes(),
		map[string]EmbassyFlowImportTypeSpec{"external-submission": {Node: "intake"}},
	)
	if !ok || !resolved.BuiltIn {
		t.Fatal("expected built-in law-petition import type to resolve")
	}

	custom, ok := ResolveEmbassyImportType(
		"external-submission",
		testSystemImportTypes(),
		map[string]EmbassyFlowImportTypeSpec{"external-submission": {Node: "intake"}},
	)
	if !ok || custom.BuiltIn || custom.Spec == nil || custom.Spec.Node != "intake" {
		t.Fatal("expected flow-defined import type to resolve with spec")
	}
}
