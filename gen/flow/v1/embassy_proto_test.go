package flowv1_test

import (
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

func TestEmbassyProtoGeneratedTypes(t *testing.T) {
	t.Parallel()

	manifest := &flowv1.TransferManifest{
		ImportType: "law-petition",
		TransferId: "transfer-123",
	}

	req := &flowv1.PreflightManifestRequest{Manifest: manifest}
	if req.GetManifest().GetImportType() != "law-petition" {
		t.Fatalf("expected import type law-petition, got %q", req.GetManifest().GetImportType())
	}

	chunk := &flowv1.PackageChunk{
		Chunk: &flowv1.PackageChunk_Content{
			Content: []byte("payload"),
		},
	}
	if string(chunk.GetContent()) != "payload" {
		t.Fatalf("expected payload chunk, got %q", string(chunk.GetContent()))
	}
}

func TestEmbassyServiceClientInterfaceExists(t *testing.T) {
	t.Parallel()

	var client flowv1.EmbassyServiceClient
	if client != nil {
		t.Fatal("expected nil zero-value EmbassyServiceClient interface")
	}
}
