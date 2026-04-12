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
		Signature: &flowv1.ManifestSignature{
			Algorithm:           "sha256-rsa",
			Signature:           []byte("sig"),
			Subject:             "spiffe://source-flow/embassy",
			CertificateChainPem: []string{"cert-a", "cert-b"},
		},
	}

	req := &flowv1.PreflightManifestRequest{Manifest: manifest}
	if req.GetManifest().GetImportType() != "law-petition" {
		t.Fatalf("expected import type law-petition, got %q", req.GetManifest().GetImportType())
	}
	if req.GetManifest().GetSignature().GetAlgorithm() != "sha256-rsa" {
		t.Fatalf("expected manifest signature algorithm sha256-rsa, got %q", req.GetManifest().GetSignature().GetAlgorithm())
	}
	if len(req.GetManifest().GetSignature().GetCertificateChainPem()) != 2 {
		t.Fatalf("expected 2 certificate chain entries, got %d", len(req.GetManifest().GetSignature().GetCertificateChainPem()))
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
