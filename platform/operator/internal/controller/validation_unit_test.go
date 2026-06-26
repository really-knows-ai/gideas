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

// ---------------------------------------------------------------------------
// Federation config validation
// ---------------------------------------------------------------------------

func TestValidateFederationConfigIdentityRequired(t *testing.T) {
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
				Federation: &flowv1.FederationConfig{
					Identity:           "",
					States:             []string{"california"},
					FederationEndpoint: "federation.example.com:50061",
				},
			},
		},
	}

	reconciler := &FoundryFlowReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(flow).Build(),
		Scheme: scheme,
	}

	err := reconciler.validateFederationConfig(flow)
	if err == nil {
		t.Fatal("expected validateFederationConfig to reject empty identity")
	}
	if !strings.Contains(err.Error(), "identity") {
		t.Fatalf("expected identity error, got %v", err)
	}
}

func TestValidateFederationConfigPublisherRoleLevelMustBeValid(t *testing.T) {
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
				Federation: &flowv1.FederationConfig{
					Identity:           "flow-alpha",
					States:             []string{"california"},
					FederationEndpoint: "federation.example.com:50061",
					PublisherRoles: []flowv1.FederationPublisherRole{
						{Scope: "security", Level: "galaxy"},
					},
				},
			},
		},
	}

	reconciler := &FoundryFlowReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(flow).Build(),
		Scheme: scheme,
	}

	err := reconciler.validateFederationConfig(flow)
	if err == nil {
		t.Fatal("expected validateFederationConfig to reject invalid publisher role level")
	}
	if !strings.Contains(err.Error(), "level") {
		t.Fatalf("expected level error, got %v", err)
	}
}

func TestValidateFederationConfigPublisherRoleScopeMustBeNonEmpty(t *testing.T) {
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
				Federation: &flowv1.FederationConfig{
					Identity:           "flow-alpha",
					States:             []string{"california"},
					FederationEndpoint: "federation.example.com:50061",
					PublisherRoles: []flowv1.FederationPublisherRole{
						{Scope: "", Level: "state"},
					},
				},
			},
		},
	}

	reconciler := &FoundryFlowReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(flow).Build(),
		Scheme: scheme,
	}

	err := reconciler.validateFederationConfig(flow)
	if err == nil {
		t.Fatal("expected validateFederationConfig to reject empty publisher role scope")
	}
	if !strings.Contains(err.Error(), "scope") {
		t.Fatalf("expected scope error, got %v", err)
	}
}

func TestValidateFederationConfigStatesMustBeNonEmpty(t *testing.T) {
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
				Federation: &flowv1.FederationConfig{
					Identity:           "flow-alpha",
					States:             []string{},
					FederationEndpoint: "federation.example.com:50061",
				},
			},
		},
	}

	reconciler := &FoundryFlowReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(flow).Build(),
		Scheme: scheme,
	}

	err := reconciler.validateFederationConfig(flow)
	if err == nil {
		t.Fatal("expected validateFederationConfig to reject empty states list")
	}
	if !strings.Contains(err.Error(), "states") {
		t.Fatalf("expected states error, got %v", err)
	}
}

func TestValidateFederationConfigEndpointMustBeNonEmpty(t *testing.T) {
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
				Federation: &flowv1.FederationConfig{
					Identity:           "flow-alpha",
					States:             []string{"california"},
					FederationEndpoint: "",
				},
			},
		},
	}

	reconciler := &FoundryFlowReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(flow).Build(),
		Scheme: scheme,
	}

	err := reconciler.validateFederationConfig(flow)
	if err == nil {
		t.Fatal("expected validateFederationConfig to reject empty federation endpoint")
	}
	if !strings.Contains(err.Error(), "federationEndpoint") {
		t.Fatalf("expected federationEndpoint error, got %v", err)
	}
}

func TestValidateFederationConfigPassesWhenValid(t *testing.T) {
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
				Federation: &flowv1.FederationConfig{
					Identity:           "flow-alpha",
					States:             []string{"california", "nevada"},
					FederationEndpoint: "federation.example.com:50061",
					PublisherRoles: []flowv1.FederationPublisherRole{
						{Scope: "security", Level: "state"},
						{Scope: "compliance", Level: "federation"},
					},
				},
			},
		},
	}

	reconciler := &FoundryFlowReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(flow).Build(),
		Scheme: scheme,
	}

	if err := reconciler.validateFederationConfig(flow); err != nil {
		t.Fatalf("expected valid federation config to pass, got %v", err)
	}
}

func TestValidateFederationConfigSkipsWhenNil(t *testing.T) {
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

	reconciler := &FoundryFlowReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(flow).Build(),
		Scheme: scheme,
	}

	if err := reconciler.validateFederationConfig(flow); err != nil {
		t.Fatalf("expected nil federation config to pass, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Capability validation regex tests (Phase 08)
// ---------------------------------------------------------------------------

func TestCapabilityPattern_AcceptsWildcard(t *testing.T) {
	t.Parallel()

	valid := []string{
		"STAMP:artefact/*/appraise-*",
		"STAMP:artefact/haiku/appraise-*",
		"STAMP:artefact/haiku/appraise-security",
		"STAMP:artefact/haiku/appraise-security-L001",
		"STAMP:artefact/*/approval",
		"STAMP:artefact/*/appraise-",
		"READ:artefact/*",
		"WRITE:artefact/haiku",
		"WRITE:artefact/*",
		"CREATE:workitem/child",
		"CREATE:workitem",
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
