package service

import (
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	federationv1 "github.com/gideas/flow/federation/api/v1"
)

// newTestServer creates a FederationServer backed by a fake K8s client
// for unit testing.
func newTestServer(t *testing.T) *FederationServer {
	t.Helper()

	scheme := federationv1.NewTestScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	return NewFederationServer(k8sClient, "test-ns",
		WithFederationConfig(&flowv1.FederationConfig{
			FederationId:   "fed-001",
			FederationName: "Test Federation",
			RootCaPem:      "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		}),
		WithBootstrapToken("valid-token"),
	)
}

func TestNewFederationServer(t *testing.T) {
	srv := newTestServer(t)
	if srv == nil {
		t.Fatal("NewFederationServer returned nil")
	}
	if srv.k8sClient == nil {
		t.Error("k8sClient is nil")
	}
	if srv.namespace != "test-ns" {
		t.Errorf("namespace = %q, want %q", srv.namespace, "test-ns")
	}
	if srv.config.GetFederationId() != "fed-001" {
		t.Errorf("federation_id = %q, want %q", srv.config.GetFederationId(), "fed-001")
	}
	if srv.bootstrapToken != "valid-token" {
		t.Errorf("bootstrap_token = %q, want %q", srv.bootstrapToken, "valid-token")
	}
}
