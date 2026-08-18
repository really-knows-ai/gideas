package controller

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

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
