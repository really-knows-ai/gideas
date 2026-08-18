package controller

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

// ---------------------------------------------------------------------------
// Capability validation regex tests (Phase 08)
// ---------------------------------------------------------------------------

func TestCapabilityPattern_AcceptsWildcard(t *testing.T) {
	t.Parallel()

	valid := []string{
		"STAMP:artefact/*/review",
		"STAMP:artefact/haiku/review",
		"STAMP:artefact/haiku/review-L001",
		"STAMP:artefact/*/approval",
		"STAMP:artefact/*/review",
		"READ:artefact/*",
		"WRITE:artefact/haiku",
		"WRITE:artefact/*",
		"CREATE:workitem/child",
		"CREATE:workitem",
		"ATTEST:artefact/haiku/review",
		"ATTEST:artefact/*/review",
		"ATTEST:artefact/haiku/review-L001",
		"ATTEST:artefact/doc/review",
	}

	for _, cap := range valid {
		if !capabilityPattern.MatchString(cap) {
			t.Errorf("capabilityPattern should accept %q", cap)
		}
	}
}

func TestCapabilityPattern_RejectsInvalid(t *testing.T) {
	t.Parallel()

	invalid := []string{
		"STAMP:artefact/",
		"STAMP:artefact/haiku/",
		"STAMP:artefact//review",
		"STAMP:",
		"",
		"*",
		"STAMP:artefact/*/",
		"STAMP:artefact/*//appraise",
		"STAMP:artefact/ /stamp",
		"ATTEST:artefact/",
		"ATTEST:artefact/haiku/",
		"ATTEST:artefact//review",
		"ATTEST:",
		"ATTEST:artefact/*/",
	}

	for _, cap := range invalid {
		if capabilityPattern.MatchString(cap) {
			t.Errorf("capabilityPattern should reject %q", cap)
		}
	}
}

func newControllerTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := flowv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add flow scheme: %v", err)
	}
	return scheme
}

func validUnitTestCACertPEM(t *testing.T) string {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes}))
}
