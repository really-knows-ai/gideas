package controller

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	flowv1 "github.com/gideas/flow/operator/api/v1"
)

func TestValidateImportTypesRequiresEntryBoundNode(t *testing.T) {
	t.Parallel()

	scheme := newControllerTestScheme(t)
	flow := &flowv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "flow", Namespace: "default"},
		Spec: flowv1.FoundryFlowSpec{
			EntryContracts: map[string]flowv1.Contract{"default": {}},
			ExitContracts:  map[string]flowv1.Contract{"default": {}},
			GovernancePolicy: flowv1.GovernancePolicy{
				MaxVisits: 10,
			},
			CrossFlow: &flowv1.CrossFlowConfig{
				ImportTypes: map[string]flowv1.ImportTypeSpec{
					"external-submission": {Node: "import-intake"},
				},
			},
		},
	}
	node := &flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "import-intake", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "intake:latest"},
	}

	reconciler := &FoundryFlowReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(flow, node).Build(),
		Scheme: scheme,
	}

	err := reconciler.validateImportTypes(context.Background(), flow)
	if err == nil {
		t.Fatal("expected validateImportTypes to reject non-entry-bound node")
	}
	if !strings.Contains(err.Error(), "entry contract binding") {
		t.Fatalf("expected entry-binding error, got %v", err)
	}
}

func TestValidateImportTypesRejectsBuiltInLawPetitionOverride(t *testing.T) {
	t.Parallel()

	scheme := newControllerTestScheme(t)
	flow := &flowv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "flow", Namespace: "default"},
		Spec: flowv1.FoundryFlowSpec{
			EntryContracts: map[string]flowv1.Contract{"default": {}},
			ExitContracts:  map[string]flowv1.Contract{"default": {}},
			GovernancePolicy: flowv1.GovernancePolicy{
				MaxVisits: 10,
			},
			CrossFlow: &flowv1.CrossFlowConfig{
				ImportTypes: map[string]flowv1.ImportTypeSpec{
					"law-petition": {Node: "clerk-sort"},
				},
			},
		},
	}
	node := &flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "clerk-sort", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "sort:latest", Entry: "default"},
	}

	reconciler := &FoundryFlowReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(flow, node).Build(),
		Scheme: scheme,
	}

	err := reconciler.validateImportTypes(context.Background(), flow)
	if err == nil {
		t.Fatal("expected validateImportTypes to reject built-in law-petition override")
	}
	if !strings.Contains(err.Error(), "built-in system import type") {
		t.Fatalf("expected built-in import type error, got %v", err)
	}
}

func TestValidateImportTypesAllowsCustomImportType(t *testing.T) {
	t.Parallel()

	scheme := newControllerTestScheme(t)
	flow := &flowv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "flow", Namespace: "default"},
		Spec: flowv1.FoundryFlowSpec{
			EntryContracts: map[string]flowv1.Contract{"default": {}},
			ExitContracts:  map[string]flowv1.Contract{"default": {}},
			GovernancePolicy: flowv1.GovernancePolicy{
				MaxVisits: 10,
			},
			CrossFlow: &flowv1.CrossFlowConfig{
				ImportTypes: map[string]flowv1.ImportTypeSpec{
					"external-submission": {Node: "intake-triage"},
				},
			},
		},
	}
	node := &flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "intake-triage", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "triage:latest", Entry: "default"},
	}

	reconciler := &FoundryFlowReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(flow, node).Build(),
		Scheme: scheme,
	}

	if err := reconciler.validateImportTypes(context.Background(), flow); err != nil {
		t.Fatalf("expected custom import type to validate, got %v", err)
	}
}

func TestValidateAllowedImportTypesRejectsUnknownImportType(t *testing.T) {
	t.Parallel()

	scheme := newControllerTestScheme(t)
	flow := &flowv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "flow", Namespace: "default"},
		Spec: flowv1.FoundryFlowSpec{
			EntryContracts: map[string]flowv1.Contract{"default": {}},
			ExitContracts:  map[string]flowv1.Contract{"default": {}},
			GovernancePolicy: flowv1.GovernancePolicy{
				MaxVisits: 10,
			},
			CrossFlow: &flowv1.CrossFlowConfig{
				ImportTypes: map[string]flowv1.ImportTypeSpec{
					"law-petition": {Node: "clerk-sort"},
				},
			},
		},
	}
	treaty := &flowv1.Treaty{
		ObjectMeta: metav1.ObjectMeta{Name: "treaty", Namespace: "default"},
		Spec: flowv1.TreatySpec{
			RemoteName:         "remote-flow",
			Direction:          "import",
			CACert:             validUnitTestCACertPEM(t),
			AllowedImportTypes: []string{"custom-import"},
		},
	}

	reconciler := &TreatyReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(flow, treaty).Build(),
		Scheme: scheme,
	}

	err := reconciler.validateAllowedImportTypes(context.Background(), treaty)
	if err == nil {
		t.Fatal("expected validateAllowedImportTypes to reject unknown import type")
	}
	if !strings.Contains(err.Error(), "custom-import") {
		t.Fatalf("expected unknown import type in error, got %v", err)
	}
}

func TestValidateAllowedImportTypesAllowsBuiltInLawPetition(t *testing.T) {
	t.Parallel()

	scheme := newControllerTestScheme(t)
	flow := &flowv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "flow", Namespace: "default"},
		Spec: flowv1.FoundryFlowSpec{
			EntryContracts: map[string]flowv1.Contract{"default": {}},
			ExitContracts:  map[string]flowv1.Contract{"default": {}},
			GovernancePolicy: flowv1.GovernancePolicy{
				MaxVisits: 10,
			},
		},
	}
	treaty := &flowv1.Treaty{
		ObjectMeta: metav1.ObjectMeta{Name: "treaty", Namespace: "default"},
		Spec: flowv1.TreatySpec{
			RemoteName:         "remote-flow",
			Direction:          "import",
			CACert:             validUnitTestCACertPEM(t),
			AllowedImportTypes: []string{"law-petition"},
		},
	}

	reconciler := &TreatyReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(flow, treaty).Build(),
		Scheme: scheme,
	}

	if err := reconciler.validateAllowedImportTypes(context.Background(), treaty); err != nil {
		t.Fatalf("expected built-in law-petition import type to validate, got %v", err)
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
