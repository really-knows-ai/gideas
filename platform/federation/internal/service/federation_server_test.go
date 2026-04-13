package service

import (
	"context"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	federationv1 "github.com/gideas/flow/federation/api/v1"
)

const testNamespace = "test-ns"

// newTestServer creates a FederationServer backed by a fake K8s client
// for unit testing. Optional K8s objects can be pre-loaded.
func newTestServer(t *testing.T, objs ...client.Object) *FederationServer {
	t.Helper()

	scheme := federationv1.NewTestScheme()
	builder := fake.NewClientBuilder().WithScheme(scheme)
	if len(objs) > 0 {
		builder = builder.WithObjects(objs...)
	}
	k8sClient := builder.Build()

	return NewFederationServer(k8sClient, testNamespace,
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
	if srv.namespace != testNamespace {
		t.Errorf("namespace = %q, want %q", srv.namespace, testNamespace)
	}
	if srv.config.GetFederationId() != "fed-001" {
		t.Errorf("federation_id = %q, want %q", srv.config.GetFederationId(), "fed-001")
	}
	if srv.bootstrapToken != "valid-token" {
		t.Errorf("bootstrap_token = %q, want %q", srv.bootstrapToken, "valid-token")
	}
}

// --- JoinFederation Tests ---

func TestJoinFederation_Success(t *testing.T) {
	// Pre-load two FederationState CRs that the response should reference.
	stateQLD := &federationv1.FederationState{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "state-qld",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationStateSpec{Name: "Queensland"},
	}
	stateNSW := &federationv1.FederationState{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "state-nsw",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationStateSpec{Name: "New South Wales"},
	}

	srv := newTestServer(t, stateQLD, stateNSW)

	resp, err := srv.JoinFederation(context.Background(), &flowv1.JoinFederationRequest{
		BootstrapToken:  "valid-token",
		FlowIdentity:    "flow-alpha",
		EmbassyEndpoint: "flow-alpha-embassy:50059",
	})
	if err != nil {
		t.Fatalf("JoinFederation returned error: %v", err)
	}

	// Response should include federation config.
	if resp.GetFederationConfig().GetFederationId() != "fed-001" {
		t.Errorf("federation_id = %q, want %q", resp.GetFederationConfig().GetFederationId(), "fed-001")
	}
	if resp.GetFederationConfig().GetFederationName() != "Test Federation" {
		t.Errorf("federation_name = %q, want %q", resp.GetFederationConfig().GetFederationName(), "Test Federation")
	}

	// Response should include intermediate CA.
	if resp.GetIntermediateCaPem() == "" {
		t.Error("intermediate_ca_pem is empty")
	}

	// Response should include states.
	if len(resp.GetStates()) != 2 {
		t.Errorf("states count = %d, want 2", len(resp.GetStates()))
	}

	// Verify that a FederationMember CR was created.
	var member federationv1.FederationMember
	key := types.NamespacedName{Namespace: testNamespace, Name: "flow-alpha"}
	if err := srv.k8sClient.Get(context.Background(), key, &member); err != nil {
		t.Fatalf("failed to get FederationMember CR: %v", err)
	}
	if member.Spec.FlowIdentity != "flow-alpha" {
		t.Errorf("member.Spec.FlowIdentity = %q, want %q", member.Spec.FlowIdentity, "flow-alpha")
	}
	if member.Spec.EmbassyEndpoint != "flow-alpha-embassy:50059" {
		t.Errorf("member.Spec.EmbassyEndpoint = %q, want %q", member.Spec.EmbassyEndpoint, "flow-alpha-embassy:50059")
	}
}

func TestJoinFederation_EmptyFlowIdentity(t *testing.T) {
	srv := newTestServer(t)

	_, err := srv.JoinFederation(context.Background(), &flowv1.JoinFederationRequest{
		BootstrapToken:  "valid-token",
		FlowIdentity:    "",
		EmbassyEndpoint: "flow-embassy:50059",
	})
	if err == nil {
		t.Fatal("expected error for empty flow_identity, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestJoinFederation_EmptyBootstrapToken(t *testing.T) {
	srv := newTestServer(t)

	_, err := srv.JoinFederation(context.Background(), &flowv1.JoinFederationRequest{
		BootstrapToken:  "",
		FlowIdentity:    "flow-alpha",
		EmbassyEndpoint: "flow-embassy:50059",
	})
	if err == nil {
		t.Fatal("expected error for empty bootstrap_token, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestJoinFederation_InvalidBootstrapToken(t *testing.T) {
	srv := newTestServer(t)

	_, err := srv.JoinFederation(context.Background(), &flowv1.JoinFederationRequest{
		BootstrapToken:  "wrong-token",
		FlowIdentity:    "flow-alpha",
		EmbassyEndpoint: "flow-embassy:50059",
	})
	if err == nil {
		t.Fatal("expected error for invalid bootstrap_token, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", err)
	}
}

func TestJoinFederation_AlreadyJoined(t *testing.T) {
	// Pre-load an existing FederationMember CR for flow-alpha.
	existing := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-alpha",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-alpha",
			EmbassyEndpoint: "flow-alpha-embassy:50059",
		},
	}

	srv := newTestServer(t, existing)

	_, err := srv.JoinFederation(context.Background(), &flowv1.JoinFederationRequest{
		BootstrapToken:  "valid-token",
		FlowIdentity:    "flow-alpha",
		EmbassyEndpoint: "flow-alpha-embassy:50059",
	})
	if err == nil {
		t.Fatal("expected error for already-joined member, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.AlreadyExists {
		t.Errorf("expected AlreadyExists, got %v", err)
	}
}

func TestJoinFederation_ResponseIncludesStatesAndRoles(t *testing.T) {
	// Pre-load states.
	stateQLD := &federationv1.FederationState{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "state-qld",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationStateSpec{Name: "Queensland"},
	}

	srv := newTestServer(t, stateQLD)

	resp, err := srv.JoinFederation(context.Background(), &flowv1.JoinFederationRequest{
		BootstrapToken:  "valid-token",
		FlowIdentity:    "flow-beta",
		EmbassyEndpoint: "flow-beta-embassy:50059",
	})
	if err != nil {
		t.Fatalf("JoinFederation returned error: %v", err)
	}

	// States should be populated from FederationState CRs.
	if len(resp.GetStates()) != 1 {
		t.Fatalf("states count = %d, want 1", len(resp.GetStates()))
	}
	state := resp.GetStates()[0]
	if state.GetStateId() != "state-qld" {
		t.Errorf("state_id = %q, want %q", state.GetStateId(), "state-qld")
	}
	if state.GetName() != "Queensland" {
		t.Errorf("state name = %q, want %q", state.GetName(), "Queensland")
	}

	// Publisher roles should initially be empty (no default roles assigned).
	if len(resp.GetPublisherRoles()) != 0 {
		t.Errorf("publisher_roles count = %d, want 0 (no default roles)", len(resp.GetPublisherRoles()))
	}
}

func TestJoinFederation_EmptyEmbassyEndpoint(t *testing.T) {
	srv := newTestServer(t)

	_, err := srv.JoinFederation(context.Background(), &flowv1.JoinFederationRequest{
		BootstrapToken:  "valid-token",
		FlowIdentity:    "flow-alpha",
		EmbassyEndpoint: "",
	})
	if err == nil {
		t.Fatal("expected error for empty embassy_endpoint, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}
