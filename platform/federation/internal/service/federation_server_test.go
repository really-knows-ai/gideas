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

const (
	testNamespace        = "test-ns"
	testFlowAlpha        = "flow-alpha"
	testFlowAlphaEmbassy = "flow-alpha-embassy:50059"
)

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
		FlowIdentity:    testFlowAlpha,
		EmbassyEndpoint: testFlowAlphaEmbassy,
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
	key := types.NamespacedName{Namespace: testNamespace, Name: testFlowAlpha}
	if err := srv.k8sClient.Get(context.Background(), key, &member); err != nil {
		t.Fatalf("failed to get FederationMember CR: %v", err)
	}
	if member.Spec.FlowIdentity != testFlowAlpha {
		t.Errorf("member.Spec.FlowIdentity = %q, want %q", member.Spec.FlowIdentity, testFlowAlpha)
	}
	if member.Spec.EmbassyEndpoint != testFlowAlphaEmbassy {
		t.Errorf("member.Spec.EmbassyEndpoint = %q, want %q", member.Spec.EmbassyEndpoint, testFlowAlphaEmbassy)
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
		FlowIdentity:    testFlowAlpha,
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
		FlowIdentity:    testFlowAlpha,
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
			Name:      testFlowAlpha,
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    testFlowAlpha,
			EmbassyEndpoint: testFlowAlphaEmbassy,
		},
	}

	srv := newTestServer(t, existing)

	_, err := srv.JoinFederation(context.Background(), &flowv1.JoinFederationRequest{
		BootstrapToken:  "valid-token",
		FlowIdentity:    testFlowAlpha,
		EmbassyEndpoint: testFlowAlphaEmbassy,
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
		FlowIdentity:    testFlowAlpha,
		EmbassyEndpoint: "",
	})
	if err == nil {
		t.Fatal("expected error for empty embassy_endpoint, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

// --- LeaveFederation Tests ---

func TestLeaveFederation_Success(t *testing.T) {
	// Pre-load an existing FederationMember CR.
	existing := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testFlowAlpha,
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    testFlowAlpha,
			EmbassyEndpoint: testFlowAlphaEmbassy,
		},
	}

	srv := newTestServer(t, existing)

	resp, err := srv.LeaveFederation(context.Background(), &flowv1.LeaveFederationRequest{
		FlowIdentity: testFlowAlpha,
	})
	if err != nil {
		t.Fatalf("LeaveFederation returned error: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Error("expected acknowledged = true")
	}

	// Verify the CR was deleted.
	var member federationv1.FederationMember
	key := types.NamespacedName{Namespace: testNamespace, Name: testFlowAlpha}
	err = srv.k8sClient.Get(context.Background(), key, &member)
	if err == nil {
		t.Fatal("expected FederationMember CR to be deleted, but it still exists")
	}
}

func TestLeaveFederation_NonMember(t *testing.T) {
	// No pre-loaded members.
	srv := newTestServer(t)

	_, err := srv.LeaveFederation(context.Background(), &flowv1.LeaveFederationRequest{
		FlowIdentity: "flow-unknown",
	})
	if err == nil {
		t.Fatal("expected error for non-member, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
}

func TestLeaveFederation_EmptyFlowIdentity(t *testing.T) {
	srv := newTestServer(t)

	_, err := srv.LeaveFederation(context.Background(), &flowv1.LeaveFederationRequest{
		FlowIdentity: "",
	})
	if err == nil {
		t.Fatal("expected error for empty flow_identity, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

// --- GetMembership Tests ---

func TestGetMembership_Success(t *testing.T) {
	// Pre-load a member with states and roles.
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
	member := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testFlowAlpha,
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    testFlowAlpha,
			EmbassyEndpoint: testFlowAlphaEmbassy,
			StateRefs:       []string{"state-qld", "state-nsw"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: "state"},
			},
		},
	}

	srv := newTestServer(t, stateQLD, stateNSW, member)

	resp, err := srv.GetMembership(context.Background(), &flowv1.GetMembershipRequest{
		FlowIdentity: testFlowAlpha,
	})
	if err != nil {
		t.Fatalf("GetMembership returned error: %v", err)
	}

	m := resp.GetMember()
	if m == nil {
		t.Fatal("response member is nil")
	}
	if m.GetFlowIdentity() != testFlowAlpha {
		t.Errorf("flow_identity = %q, want %q", m.GetFlowIdentity(), testFlowAlpha)
	}
	if m.GetEmbassyEndpoint() != testFlowAlphaEmbassy {
		t.Errorf("embassy_endpoint = %q, want %q", m.GetEmbassyEndpoint(), testFlowAlphaEmbassy)
	}

	// States should be resolved from FederationState CRs via stateRefs.
	if len(m.GetStates()) != 2 {
		t.Fatalf("states count = %d, want 2", len(m.GetStates()))
	}
	// Check that state names are resolved (not just IDs).
	statesByID := make(map[string]string)
	for _, s := range m.GetStates() {
		statesByID[s.GetStateId()] = s.GetName()
	}
	if statesByID["state-qld"] != "Queensland" {
		t.Errorf("state-qld name = %q, want %q", statesByID["state-qld"], "Queensland")
	}
	if statesByID["state-nsw"] != "New South Wales" {
		t.Errorf("state-nsw name = %q, want %q", statesByID["state-nsw"], "New South Wales")
	}

	// Publisher roles should be populated.
	if len(m.GetPublisherRoles()) != 1 {
		t.Fatalf("publisher_roles count = %d, want 1", len(m.GetPublisherRoles()))
	}
	role := m.GetPublisherRoles()[0]
	if role.GetScope() != "education" {
		t.Errorf("role scope = %q, want %q", role.GetScope(), "education")
	}
	if role.GetLevel() != "state" {
		t.Errorf("role level = %q, want %q", role.GetLevel(), "state")
	}
}

func TestGetMembership_NonMember(t *testing.T) {
	srv := newTestServer(t)

	_, err := srv.GetMembership(context.Background(), &flowv1.GetMembershipRequest{
		FlowIdentity: "flow-unknown",
	})
	if err == nil {
		t.Fatal("expected error for non-member, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
}

func TestGetMembership_EmptyFlowIdentity(t *testing.T) {
	srv := newTestServer(t)

	_, err := srv.GetMembership(context.Background(), &flowv1.GetMembershipRequest{
		FlowIdentity: "",
	})
	if err == nil {
		t.Fatal("expected error for empty flow_identity, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

// --- DiscoverEndpoints Tests ---

func TestDiscoverEndpoints_NoFilter_ReturnsAllMembers(t *testing.T) {
	// Pre-load two members with different states.
	memberAlpha := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testFlowAlpha,
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    testFlowAlpha,
			EmbassyEndpoint: testFlowAlphaEmbassy,
			StateRefs:       []string{"state-qld"},
		},
	}
	memberBeta := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-beta",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-beta",
			EmbassyEndpoint: "flow-beta-embassy:50059",
			StateRefs:       []string{"state-nsw"},
		},
	}

	srv := newTestServer(t, memberAlpha, memberBeta)

	resp, err := srv.DiscoverEndpoints(context.Background(), &flowv1.DiscoverEndpointsRequest{})
	if err != nil {
		t.Fatalf("DiscoverEndpoints returned error: %v", err)
	}

	if len(resp.GetEndpoints()) != 2 {
		t.Fatalf("endpoints count = %d, want 2", len(resp.GetEndpoints()))
	}

	// Build a map for order-independent assertions.
	byIdentity := make(map[string]*flowv1.FlowEndpoint)
	for _, ep := range resp.GetEndpoints() {
		byIdentity[ep.GetFlowIdentity()] = ep
	}

	alpha := byIdentity[testFlowAlpha]
	if alpha == nil {
		t.Fatal("missing endpoint for flow-alpha")
	}
	if alpha.GetEmbassyAddress() != testFlowAlphaEmbassy {
		t.Errorf("flow-alpha embassy_address = %q, want %q", alpha.GetEmbassyAddress(), testFlowAlphaEmbassy)
	}
	if len(alpha.GetStateIds()) != 1 || alpha.GetStateIds()[0] != "state-qld" {
		t.Errorf("flow-alpha state_ids = %v, want [state-qld]", alpha.GetStateIds())
	}

	beta := byIdentity["flow-beta"]
	if beta == nil {
		t.Fatal("missing endpoint for flow-beta")
	}
	if beta.GetEmbassyAddress() != "flow-beta-embassy:50059" {
		t.Errorf("flow-beta embassy_address = %q, want %q", beta.GetEmbassyAddress(), "flow-beta-embassy:50059")
	}
	if len(beta.GetStateIds()) != 1 || beta.GetStateIds()[0] != "state-nsw" {
		t.Errorf("flow-beta state_ids = %v, want [state-nsw]", beta.GetStateIds())
	}
}

func TestDiscoverEndpoints_WithStateFilter_ReturnsMatchingMembers(t *testing.T) {
	// Pre-load members: alpha in QLD, beta in NSW, gamma in both.
	memberAlpha := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testFlowAlpha,
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    testFlowAlpha,
			EmbassyEndpoint: testFlowAlphaEmbassy,
			StateRefs:       []string{"state-qld"},
		},
	}
	memberBeta := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-beta",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-beta",
			EmbassyEndpoint: "flow-beta-embassy:50059",
			StateRefs:       []string{"state-nsw"},
		},
	}
	memberGamma := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-gamma",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-gamma",
			EmbassyEndpoint: "flow-gamma-embassy:50059",
			StateRefs:       []string{"state-qld", "state-nsw"},
		},
	}

	srv := newTestServer(t, memberAlpha, memberBeta, memberGamma)

	resp, err := srv.DiscoverEndpoints(context.Background(), &flowv1.DiscoverEndpointsRequest{
		StateFilter: "state-qld",
	})
	if err != nil {
		t.Fatalf("DiscoverEndpoints returned error: %v", err)
	}

	// Should return alpha (QLD) and gamma (QLD+NSW), not beta (NSW only).
	if len(resp.GetEndpoints()) != 2 {
		t.Fatalf("endpoints count = %d, want 2", len(resp.GetEndpoints()))
	}

	byIdentity := make(map[string]*flowv1.FlowEndpoint)
	for _, ep := range resp.GetEndpoints() {
		byIdentity[ep.GetFlowIdentity()] = ep
	}

	if byIdentity[testFlowAlpha] == nil {
		t.Error("expected flow-alpha in results (member of state-qld)")
	}
	if byIdentity["flow-gamma"] == nil {
		t.Error("expected flow-gamma in results (member of state-qld and state-nsw)")
	}
	if byIdentity["flow-beta"] != nil {
		t.Error("flow-beta should not be in results (not a member of state-qld)")
	}
}

func TestDiscoverEndpoints_FlowEndpointFields(t *testing.T) {
	// Verify each FlowEndpoint includes flow_identity, embassy_address, state_ids.
	member := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testFlowAlpha,
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    testFlowAlpha,
			EmbassyEndpoint: testFlowAlphaEmbassy,
			StateRefs:       []string{"state-qld", "state-nsw"},
		},
	}

	srv := newTestServer(t, member)

	resp, err := srv.DiscoverEndpoints(context.Background(), &flowv1.DiscoverEndpointsRequest{})
	if err != nil {
		t.Fatalf("DiscoverEndpoints returned error: %v", err)
	}

	if len(resp.GetEndpoints()) != 1 {
		t.Fatalf("endpoints count = %d, want 1", len(resp.GetEndpoints()))
	}

	ep := resp.GetEndpoints()[0]
	if ep.GetFlowIdentity() != testFlowAlpha {
		t.Errorf("flow_identity = %q, want %q", ep.GetFlowIdentity(), testFlowAlpha)
	}
	if ep.GetEmbassyAddress() != testFlowAlphaEmbassy {
		t.Errorf("embassy_address = %q, want %q", ep.GetEmbassyAddress(), testFlowAlphaEmbassy)
	}
	if len(ep.GetStateIds()) != 2 {
		t.Fatalf("state_ids count = %d, want 2", len(ep.GetStateIds()))
	}
	// Check state IDs are present (order from spec.stateRefs).
	stateSet := make(map[string]bool)
	for _, sid := range ep.GetStateIds() {
		stateSet[sid] = true
	}
	if !stateSet["state-qld"] {
		t.Error("missing state-qld in state_ids")
	}
	if !stateSet["state-nsw"] {
		t.Error("missing state-nsw in state_ids")
	}
}

func TestDiscoverEndpoints_EmptyFederation_ReturnsEmptyList(t *testing.T) {
	// No pre-loaded members.
	srv := newTestServer(t)

	resp, err := srv.DiscoverEndpoints(context.Background(), &flowv1.DiscoverEndpointsRequest{})
	if err != nil {
		t.Fatalf("DiscoverEndpoints returned error: %v", err)
	}

	if len(resp.GetEndpoints()) != 0 {
		t.Errorf("endpoints count = %d, want 0", len(resp.GetEndpoints()))
	}
}

// --- GetPetitionTarget Tests ---

func TestGetPetitionTarget_StateLevelScope_ReturnsAuthority(t *testing.T) {
	// Pre-load a member with a state-level publisher role for "education".
	authority := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-authority",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-authority",
			EmbassyEndpoint: "flow-authority-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: "state"},
			},
		},
	}

	srv := newTestServer(t, authority)

	resp, err := srv.GetPetitionTarget(context.Background(), &flowv1.GetPetitionTargetRequest{
		Scope: "education",
	})
	if err != nil {
		t.Fatalf("GetPetitionTarget returned error: %v", err)
	}

	if resp.GetAuthorityFlowIdentity() != "flow-authority" {
		t.Errorf("authority_flow_identity = %q, want %q", resp.GetAuthorityFlowIdentity(), "flow-authority")
	}
	if resp.GetEmbassyEndpoint() != "flow-authority-embassy:50059" {
		t.Errorf("embassy_endpoint = %q, want %q", resp.GetEmbassyEndpoint(), "flow-authority-embassy:50059")
	}
}

func TestGetPetitionTarget_FederationLevelScope_ReturnsAuthority(t *testing.T) {
	// Pre-load a member with a federation-level publisher role for "security".
	authority := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-fed-authority",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-fed-authority",
			EmbassyEndpoint: "flow-fed-authority-embassy:50059",
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "security", Level: "federation"},
			},
		},
	}

	srv := newTestServer(t, authority)

	resp, err := srv.GetPetitionTarget(context.Background(), &flowv1.GetPetitionTargetRequest{
		Scope: "security",
	})
	if err != nil {
		t.Fatalf("GetPetitionTarget returned error: %v", err)
	}

	if resp.GetAuthorityFlowIdentity() != "flow-fed-authority" {
		t.Errorf("authority_flow_identity = %q, want %q", resp.GetAuthorityFlowIdentity(), "flow-fed-authority")
	}
	if resp.GetEmbassyEndpoint() != "flow-fed-authority-embassy:50059" {
		t.Errorf("embassy_endpoint = %q, want %q", resp.GetEmbassyEndpoint(), "flow-fed-authority-embassy:50059")
	}
}

func TestGetPetitionTarget_UnknownScope_NotFound(t *testing.T) {
	// Pre-load a member with a publisher role for "education" -- not "health".
	member := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testFlowAlpha,
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    testFlowAlpha,
			EmbassyEndpoint: testFlowAlphaEmbassy,
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: "state"},
			},
		},
	}

	srv := newTestServer(t, member)

	_, err := srv.GetPetitionTarget(context.Background(), &flowv1.GetPetitionTargetRequest{
		Scope: "health",
	})
	if err == nil {
		t.Fatal("expected error for unknown scope, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
}

func TestGetPetitionTarget_NoMembers_NotFound(t *testing.T) {
	// Empty federation -- no members at all.
	srv := newTestServer(t)

	_, err := srv.GetPetitionTarget(context.Background(), &flowv1.GetPetitionTargetRequest{
		Scope: "education",
	})
	if err == nil {
		t.Fatal("expected error for empty federation, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
}

func TestGetPetitionTarget_EmptyScope_InvalidArgument(t *testing.T) {
	srv := newTestServer(t)

	_, err := srv.GetPetitionTarget(context.Background(), &flowv1.GetPetitionTargetRequest{
		Scope: "",
	})
	if err == nil {
		t.Fatal("expected error for empty scope, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

// --- SubmitPublication Tests ---

func TestSubmitPublication_AuthorisedPublisher_Accepted(t *testing.T) {
	// A member with a state-level publisher role for "education" submits a law
	// with division "education" -> authority check passes, publication proceeds.
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-publisher",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-publisher",
			EmbassyEndpoint: "flow-publisher-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: "state"},
			},
		},
	}

	srv := newTestServer(t, publisher)

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-001",
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-publisher",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}

	// With no conflict detection in this slice, authority-passing publications
	// are accepted.
	if !resp.GetAccepted() {
		t.Errorf("expected accepted = true, got false; rejection = %v", resp.GetRejection())
	}
}

func TestSubmitPublication_NoPublisherRole_Rejected(t *testing.T) {
	// A member with no publisher roles submits a law -> rejected with UNAUTHORISED.
	member := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testFlowAlpha,
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    testFlowAlpha,
			EmbassyEndpoint: testFlowAlphaEmbassy,
			StateRefs:       []string{"state-qld"},
			PublisherRoles:  nil, // No publisher roles.
		},
	}

	srv := newTestServer(t, member)

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-001",
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: testFlowAlpha,
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}

	if resp.GetAccepted() {
		t.Fatal("expected accepted = false for member without publisher role")
	}
	if resp.GetRejection().GetReason() != flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_UNAUTHORISED {
		t.Errorf("rejection reason = %v, want UNAUTHORISED", resp.GetRejection().GetReason())
	}
}

func TestSubmitPublication_WrongScope_Rejected(t *testing.T) {
	// A member with a publisher role for "education" submits a law with
	// division "security" -> rejected with OUT_OF_SCOPE.
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-publisher",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-publisher",
			EmbassyEndpoint: "flow-publisher-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: "state"},
			},
		},
	}

	srv := newTestServer(t, publisher)

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-002",
			Goal:     "Harden security posture",
			Division: "security",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-publisher",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}

	if resp.GetAccepted() {
		t.Fatal("expected accepted = false for mismatched scope")
	}
	if resp.GetRejection().GetReason() != flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_OUT_OF_SCOPE {
		t.Errorf("rejection reason = %v, want OUT_OF_SCOPE", resp.GetRejection().GetReason())
	}
}

func TestSubmitPublication_NonMember_PermissionDenied(t *testing.T) {
	// A non-member attempts to publish -> PermissionDenied gRPC error.
	srv := newTestServer(t)

	_, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-001",
			Goal:     "Some law",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-unknown",
	})
	if err == nil {
		t.Fatal("expected error for non-member, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", err)
	}
}

func TestSubmitPublication_StateLevelPublisher_MustBeInState(t *testing.T) {
	// A member with a state-level publisher role for "education" who is NOT
	// in any state -> rejected with UNAUTHORISED (state-level publishers must
	// be assigned to at least one state).
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-stateless",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-stateless",
			EmbassyEndpoint: "flow-stateless-embassy:50059",
			StateRefs:       nil, // Not in any state.
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: "state"},
			},
		},
	}

	srv := newTestServer(t, publisher)

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-001",
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-stateless",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}

	if resp.GetAccepted() {
		t.Fatal("expected accepted = false for state-level publisher not in any state")
	}
	if resp.GetRejection().GetReason() != flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_UNAUTHORISED {
		t.Errorf("rejection reason = %v, want UNAUTHORISED", resp.GetRejection().GetReason())
	}
}

func TestSubmitPublication_FederationLevelPublisher_Accepted(t *testing.T) {
	// A member with a federation-level publisher role for "security" submits
	// a law with division "security" -> accepted. No state membership required.
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-fed-pub",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-fed-pub",
			EmbassyEndpoint: "flow-fed-pub-embassy:50059",
			StateRefs:       nil, // No states -- OK for federation level.
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "security", Level: "federation"},
			},
		},
	}

	srv := newTestServer(t, publisher)

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-003",
			Goal:     "Harden security posture",
			Division: "security",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-fed-pub",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}

	if !resp.GetAccepted() {
		t.Errorf("expected accepted = true, got false; rejection = %v", resp.GetRejection())
	}
}

func TestSubmitPublication_EmptySourceFlowIdentity_InvalidArgument(t *testing.T) {
	srv := newTestServer(t)

	_, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-001",
			Goal:     "Some law",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "",
	})
	if err == nil {
		t.Fatal("expected error for empty source_flow_identity, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestSubmitPublication_NilLaw_InvalidArgument(t *testing.T) {
	srv := newTestServer(t)

	_, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law:                nil,
		SourceFlowIdentity: "flow-publisher",
	})
	if err == nil {
		t.Fatal("expected error for nil law, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestGetPetitionTarget_MultipleMembers_ReturnsMatchingAuthority(t *testing.T) {
	// Pre-load two members with different scopes: ensure the correct one
	// is returned for each scope.
	eduAuthority := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-edu",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-edu",
			EmbassyEndpoint: "flow-edu-embassy:50059",
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: "state"},
			},
		},
	}
	secAuthority := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-sec",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-sec",
			EmbassyEndpoint: "flow-sec-embassy:50059",
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "security", Level: "federation"},
			},
		},
	}

	srv := newTestServer(t, eduAuthority, secAuthority)

	// Request "education" scope -- should get flow-edu.
	resp, err := srv.GetPetitionTarget(context.Background(), &flowv1.GetPetitionTargetRequest{
		Scope: "education",
	})
	if err != nil {
		t.Fatalf("GetPetitionTarget(education) returned error: %v", err)
	}
	if resp.GetAuthorityFlowIdentity() != "flow-edu" {
		t.Errorf("authority_flow_identity = %q, want %q", resp.GetAuthorityFlowIdentity(), "flow-edu")
	}

	// Request "security" scope -- should get flow-sec.
	resp, err = srv.GetPetitionTarget(context.Background(), &flowv1.GetPetitionTargetRequest{
		Scope: "security",
	})
	if err != nil {
		t.Fatalf("GetPetitionTarget(security) returned error: %v", err)
	}
	if resp.GetAuthorityFlowIdentity() != "flow-sec" {
		t.Errorf("authority_flow_identity = %q, want %q", resp.GetAuthorityFlowIdentity(), "flow-sec")
	}
}
