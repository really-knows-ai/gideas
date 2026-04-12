package flow

import (
	"context"
	"io"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
)

type embassySpyServer struct {
	flowv1.UnimplementedEmbassyServiceServer

	lastPreflight *flowv1.PreflightManifestRequest
	preflightResp *flowv1.PreflightManifestResponse

	lastExport   *flowv1.ExportPackageRequest
	exportChunks []*flowv1.PackageChunk

	receivedChunks []*flowv1.PackageChunk
	streamResp     *flowv1.StreamPackageResponse
}

func (s *embassySpyServer) PreflightManifest(
	_ context.Context, req *flowv1.PreflightManifestRequest,
) (*flowv1.PreflightManifestResponse, error) {
	s.lastPreflight = req
	if s.preflightResp != nil {
		return s.preflightResp, nil
	}
	return &flowv1.PreflightManifestResponse{Accepted: true, TransferId: "transfer-123"}, nil
}

func (s *embassySpyServer) StreamPackage(
	stream grpc.ClientStreamingServer[flowv1.PackageChunk, flowv1.StreamPackageResponse],
) error {
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		s.receivedChunks = append(s.receivedChunks, chunk)
	}
	resp := s.streamResp
	if resp == nil {
		resp = &flowv1.StreamPackageResponse{WorkitemId: "imported-001"}
	}
	return stream.SendAndClose(resp)
}

func (s *embassySpyServer) ExportPackage(
	req *flowv1.ExportPackageRequest, stream grpc.ServerStreamingServer[flowv1.PackageChunk],
) error {
	s.lastExport = req
	for _, chunk := range s.exportChunks {
		if err := stream.Send(chunk); err != nil {
			return err
		}
	}
	return nil
}

func setupEmbassyTestClient(t *testing.T, spy *embassySpyServer) *EmbassyClient {
	t.Helper()

	conn := setupStandaloneGRPCTestConn(t, func(srv *grpc.Server) {
		flowv1.RegisterEmbassyServiceServer(srv, spy)
	})

	return &EmbassyClient{
		conn:    conn,
		embassy: flowv1.NewEmbassyServiceClient(conn),
	}
}

func TestEmbassyClient_PreflightManifest_Success(t *testing.T) {
	spy := &embassySpyServer{}
	client := setupEmbassyTestClient(t, spy)

	resp, err := client.PreflightManifest(context.Background(), &flowv1.TransferManifest{
		ImportType: "law-petition",
		TransferId: "tx-1",
	}, "remote-treaty")
	if err != nil {
		t.Fatalf("PreflightManifest() returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("expected manifest to be accepted")
	}
	if spy.lastPreflight.GetTreatyName() != "remote-treaty" {
		t.Fatalf("expected treaty name remote-treaty, got %q", spy.lastPreflight.GetTreatyName())
	}
	if spy.lastPreflight.GetManifest().GetImportType() != "law-petition" {
		t.Fatalf("expected import type law-petition, got %q", spy.lastPreflight.GetManifest().GetImportType())
	}
}

func TestEmbassyClient_StreamPackage_SendsChunks(t *testing.T) {
	spy := &embassySpyServer{}
	client := setupEmbassyTestClient(t, spy)

	resp, err := client.StreamPackage(context.Background(), []*flowv1.PackageChunk{
		{Chunk: &flowv1.PackageChunk_Manifest{Manifest: &flowv1.TransferManifest{TransferId: "tx-2"}}},
		{Chunk: &flowv1.PackageChunk_Content{Content: []byte("payload")}},
	})
	if err != nil {
		t.Fatalf("StreamPackage() returned error: %v", err)
	}
	if resp.GetWorkitemId() != "imported-001" {
		t.Fatalf("expected workitem_id imported-001, got %q", resp.GetWorkitemId())
	}
	if len(spy.receivedChunks) != 2 {
		t.Fatalf("expected 2 streamed chunks, got %d", len(spy.receivedChunks))
	}
	if spy.receivedChunks[0].GetManifest().GetTransferId() != "tx-2" {
		t.Fatalf("expected manifest transfer id tx-2, got %q", spy.receivedChunks[0].GetManifest().GetTransferId())
	}
	if string(spy.receivedChunks[1].GetContent()) != "payload" {
		t.Fatalf("expected payload chunk, got %q", string(spy.receivedChunks[1].GetContent()))
	}
}

func TestEmbassyClient_ExportPackage_ReceivesChunks(t *testing.T) {
	spy := &embassySpyServer{
		exportChunks: []*flowv1.PackageChunk{
			{Chunk: &flowv1.PackageChunk_Manifest{Manifest: &flowv1.TransferManifest{TransferId: "tx-3"}}},
			{Chunk: &flowv1.PackageChunk_Trailer{Trailer: &flowv1.PackageTrailer{PackageDigest: "sha256:abc"}}},
		},
	}
	client := setupEmbassyTestClient(t, spy)

	stream, err := client.ExportPackage(context.Background(), "wi-123", "law-petition")
	if err != nil {
		t.Fatalf("ExportPackage() returned error: %v", err)
	}

	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv() first chunk returned error: %v", err)
	}
	second, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv() second chunk returned error: %v", err)
	}
	_, err = stream.Recv()
	if err != io.EOF {
		t.Fatalf("expected EOF after final chunk, got %v", err)
	}

	if spy.lastExport.GetWorkitemId() != "wi-123" {
		t.Fatalf("expected export workitem wi-123, got %q", spy.lastExport.GetWorkitemId())
	}
	if spy.lastExport.GetImportType() != "law-petition" {
		t.Fatalf("expected export import type law-petition, got %q", spy.lastExport.GetImportType())
	}
	if first.GetManifest().GetTransferId() != "tx-3" {
		t.Fatalf("expected manifest transfer id tx-3, got %q", first.GetManifest().GetTransferId())
	}
	if second.GetTrailer().GetPackageDigest() != "sha256:abc" {
		t.Fatalf("expected package digest sha256:abc, got %q", second.GetTrailer().GetPackageDigest())
	}
}

func TestEmbassyClient_PreflightManifest_NoConnection(t *testing.T) {
	client := &EmbassyClient{}
	_, err := client.PreflightManifest(context.Background(), &flowv1.TransferManifest{}, "")
	if err == nil {
		t.Fatal("expected error when embassy connection is missing")
	}
}
