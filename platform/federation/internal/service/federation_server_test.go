package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
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
	testLevelState       = "state"
	testLawID            = "law-001"
	testFlowPubA         = "flow-pub-a"
	testFlowPubAEmbassy  = "flow-pub-a-embassy:50059"
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
				{Scope: "education", Level: testLevelState},
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
	if role.GetLevel() != testLevelState {
		t.Errorf("role level = %q, want %q", role.GetLevel(), testLevelState)
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
				{Scope: "education", Level: testLevelState},
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
				{Scope: "education", Level: testLevelState},
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
				{Scope: "education", Level: testLevelState},
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
				{Scope: "education", Level: testLevelState},
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
				{Scope: "education", Level: testLevelState},
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
				{Scope: "education", Level: testLevelState},
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

// =============================================================================
// Spy LibrarianDialer and related test infrastructure for 13.8.2
// =============================================================================

// spyLibrarianClient is a minimal LibrarianServiceClient that records calls
// to SearchSimilarLaws and returns a pre-configured response. All other
// methods of the interface are implemented as panicking stubs -- they must
// not be called during these tests.
type spyLibrarianClient struct {
	mu       sync.Mutex
	calls    []*flowv1.SearchSimilarLawsRequest
	response *flowv1.SearchSimilarLawsResponse
	err      error
	flowID   string // identity of the Flow whose Librarian this represents
}

func (s *spyLibrarianClient) SearchSimilarLaws(
	_ context.Context,
	req *flowv1.SearchSimilarLawsRequest,
	_ ...grpc.CallOption,
) (*flowv1.SearchSimilarLawsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, req)
	if s.err != nil {
		return nil, s.err
	}
	if s.response != nil {
		return s.response, nil
	}
	return &flowv1.SearchSimilarLawsResponse{}, nil
}

func (s *spyLibrarianClient) getCalls() []*flowv1.SearchSimilarLawsRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]*flowv1.SearchSimilarLawsRequest, len(s.calls))
	copy(cp, s.calls)
	return cp
}

// Stub implementations for the rest of LibrarianServiceClient. These should
// never be called in this test suite.

func (s *spyLibrarianClient) QueryLaws(
	_ context.Context, _ *flowv1.QueryLawsRequest, _ ...grpc.CallOption,
) (*flowv1.QueryLawsResponse, error) {
	panic("unexpected call to QueryLaws")
}

func (s *spyLibrarianClient) Cite(
	_ context.Context, _ *flowv1.CiteRequest, _ ...grpc.CallOption,
) (*flowv1.CiteResponse, error) {
	panic("unexpected call to Cite")
}

func (s *spyLibrarianClient) RecordFinding(
	_ context.Context, _ *flowv1.RecordFindingRequest, _ ...grpc.CallOption,
) (*flowv1.RecordFindingResponse, error) {
	panic("unexpected call to RecordFinding")
}

func (s *spyLibrarianClient) GetLaw(
	_ context.Context, _ *flowv1.GetLawRequest, _ ...grpc.CallOption,
) (*flowv1.GetLawResponse, error) {
	panic("unexpected call to GetLaw")
}

func (s *spyLibrarianClient) WriteLaw(
	_ context.Context, _ *flowv1.WriteLawRequest, _ ...grpc.CallOption,
) (*flowv1.WriteLawResponse, error) {
	panic("unexpected call to WriteLaw")
}

func (s *spyLibrarianClient) RetireLaw(
	_ context.Context, _ *flowv1.RetireLawRequest, _ ...grpc.CallOption,
) (*flowv1.RetireLawResponse, error) {
	panic("unexpected call to RetireLaw")
}

func (s *spyLibrarianClient) ReplicateLaws(
	_ context.Context, _ *flowv1.ReplicateLawsRequest, _ ...grpc.CallOption,
) (*flowv1.ReplicateLawsResponse, error) {
	panic("unexpected call to ReplicateLaws")
}

func (s *spyLibrarianClient) ApplyLifecycleAction(
	_ context.Context, _ *flowv1.ApplyLifecycleActionRequest, _ ...grpc.CallOption,
) (*flowv1.ApplyLifecycleActionResponse, error) {
	panic("unexpected call to ApplyLifecycleAction")
}

func (s *spyLibrarianClient) CreateDisputeRecord(
	_ context.Context, _ *flowv1.CreateDisputeRecordRequest, _ ...grpc.CallOption,
) (*flowv1.CreateDisputeRecordResponse, error) {
	panic("unexpected call to CreateDisputeRecord")
}

func (s *spyLibrarianClient) RetireDisputeRecord(
	_ context.Context, _ *flowv1.RetireDisputeRecordRequest, _ ...grpc.CallOption,
) (*flowv1.RetireDisputeRecordResponse, error) {
	panic("unexpected call to RetireDisputeRecord")
}

func (s *spyLibrarianClient) GetActiveDisputes(
	_ context.Context, _ *flowv1.GetActiveDisputesRequest, _ ...grpc.CallOption,
) (*flowv1.GetActiveDisputesResponse, error) {
	panic("unexpected call to GetActiveDisputes")
}

func (s *spyLibrarianClient) GetLawGroup(
	_ context.Context, _ *flowv1.GetLawGroupRequest, _ ...grpc.CallOption,
) (*flowv1.GetLawGroupResponse, error) {
	panic("unexpected call to GetLawGroup")
}

func (s *spyLibrarianClient) ListLawGroups(
	_ context.Context, _ *flowv1.ListLawGroupsRequest, _ ...grpc.CallOption,
) (*flowv1.ListLawGroupsResponse, error) {
	panic("unexpected call to ListLawGroups")
}

func (s *spyLibrarianClient) SyncLawGroup(
	_ context.Context, _ *flowv1.SyncLawGroupRequest, _ ...grpc.CallOption,
) (*flowv1.SyncLawGroupResponse, error) {
	panic("unexpected call to SyncLawGroup")
}

func (s *spyLibrarianClient) DeleteLawGroup(
	_ context.Context, _ *flowv1.DeleteLawGroupRequest, _ ...grpc.CallOption,
) (*flowv1.DeleteLawGroupResponse, error) {
	panic("unexpected call to DeleteLawGroup")
}

// spyLibrarianDialer maps embassy endpoint addresses to spy clients.
type spyLibrarianDialer struct {
	mu      sync.Mutex
	clients map[string]*spyLibrarianClient
	dialed  []string // ordered list of addresses dialed
}

func newSpyDialer() *spyLibrarianDialer {
	return &spyLibrarianDialer{
		clients: make(map[string]*spyLibrarianClient),
	}
}

// addClient registers a spy Librarian at the given address.
func (d *spyLibrarianDialer) addClient(address string, c *spyLibrarianClient) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.clients[address] = c
}

func (d *spyLibrarianDialer) DialLibrarian(
	_ context.Context, address string,
) (flowv1.LibrarianServiceClient, func() error, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dialed = append(d.dialed, address)
	c, ok := d.clients[address]
	if !ok {
		return nil, nil, fmt.Errorf("spy dialer: no client registered for %s", address)
	}
	return c, func() error { return nil }, nil
}

func (d *spyLibrarianDialer) getDialed() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	cp := make([]string, len(d.dialed))
	copy(cp, d.dialed)
	return cp
}

// newTestServerWithDialer creates a FederationServer with a spy LibrarianDialer.
func newTestServerWithDialer(t *testing.T, dialer LibrarianDialer, objs ...client.Object) *FederationServer {
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
		WithLibrarianDialer(dialer),
	)
}

// newTestServerWithDialerAndAnalyser creates a FederationServer with both
// a spy LibrarianDialer and a ConflictAnalyser.
func newTestServerWithDialerAndAnalyser(
	t *testing.T,
	dialer LibrarianDialer,
	analyser ConflictAnalyser,
	objs ...client.Object,
) *FederationServer {
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
		WithLibrarianDialer(dialer),
		WithConflictAnalyser(analyser),
	)
}

// --- 13.8.2 SubmitPublication Distributed Conflict Detection Tests ---

func TestSubmitPublication_AuthorityPass_QueriesPublisherLibrarians(t *testing.T) {
	// Two publisher Flows in the same state. One submits a publication.
	// The federation service should query all publisher Flows' Librarians
	// via SearchSimilarLaws.
	publisherA := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}
	publisherB := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-b",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-b",
			EmbassyEndpoint: "flow-pub-b-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	spyA := &spyLibrarianClient{flowID: "flow-pub-a"}
	spyB := &spyLibrarianClient{flowID: "flow-pub-b"}

	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyA)
	dialer.addClient("flow-pub-b-embassy:50059", spyB)

	srv := newTestServerWithDialer(t, dialer, publisherA, publisherB)

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-001",
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Errorf("expected accepted = true, got false; rejection = %v", resp.GetRejection())
	}

	// Both publisher Librarians should have been queried.
	dialedAddrs := dialer.getDialed()
	if len(dialedAddrs) != 2 {
		t.Fatalf("expected 2 librarian dials, got %d: %v", len(dialedAddrs), dialedAddrs)
	}

	// Verify SearchSimilarLaws was called on both.
	callsA := spyA.getCalls()
	callsB := spyB.getCalls()
	if len(callsA) != 1 {
		t.Errorf("spyA: expected 1 SearchSimilarLaws call, got %d", len(callsA))
	}
	if len(callsB) != 1 {
		t.Errorf("spyB: expected 1 SearchSimilarLaws call, got %d", len(callsB))
	}

	// Verify the query text matches the submitted law's goal.
	if len(callsA) > 0 && callsA[0].GetQueryText() != "Ensure quality education" {
		t.Errorf("spyA query_text = %q, want %q", callsA[0].GetQueryText(), "Ensure quality education")
	}
}

func TestSubmitPublication_StateLevelPublication_QueriesOnlySameStateLibrarians(t *testing.T) {
	// Publisher A is in state-qld, Publisher B is in state-nsw, Publisher C in state-qld.
	// A state-level publication from A should only query Librarians of publishers
	// in the same state(s) as A (state-qld).
	publisherA := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}
	publisherB := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-b",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-b",
			EmbassyEndpoint: "flow-pub-b-embassy:50059",
			StateRefs:       []string{"state-nsw"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}
	publisherC := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-c",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-c",
			EmbassyEndpoint: "flow-pub-c-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	spyA := &spyLibrarianClient{flowID: "flow-pub-a"}
	spyB := &spyLibrarianClient{flowID: "flow-pub-b"}
	spyC := &spyLibrarianClient{flowID: "flow-pub-c"}

	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyA)
	dialer.addClient("flow-pub-b-embassy:50059", spyB)
	dialer.addClient("flow-pub-c-embassy:50059", spyC)

	srv := newTestServerWithDialer(t, dialer, publisherA, publisherB, publisherC)

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-001",
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Errorf("expected accepted = true, got false; rejection = %v", resp.GetRejection())
	}

	// Only publishers in state-qld should be queried (A and C), not B (state-nsw).
	callsA := spyA.getCalls()
	callsB := spyB.getCalls()
	callsC := spyC.getCalls()

	if len(callsA) != 1 {
		t.Errorf("spyA (state-qld): expected 1 call, got %d", len(callsA))
	}
	if len(callsB) != 0 {
		t.Errorf("spyB (state-nsw): expected 0 calls, got %d", len(callsB))
	}
	if len(callsC) != 1 {
		t.Errorf("spyC (state-qld): expected 1 call, got %d", len(callsC))
	}
}

func TestSubmitPublication_FederationLevelPublication_QueriesAllPublisherLibrarians(t *testing.T) {
	// For federation-level publication, ALL publisher Librarians should be queried
	// regardless of state membership.
	publisherA := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "security", Level: "federation"},
			},
		},
	}
	publisherB := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-b",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-b",
			EmbassyEndpoint: "flow-pub-b-embassy:50059",
			StateRefs:       []string{"state-nsw"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "security", Level: "federation"},
			},
		},
	}

	spyA := &spyLibrarianClient{flowID: "flow-pub-a"}
	spyB := &spyLibrarianClient{flowID: "flow-pub-b"}

	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyA)
	dialer.addClient("flow-pub-b-embassy:50059", spyB)

	srv := newTestServerWithDialer(t, dialer, publisherA, publisherB)

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-001",
			Goal:     "Harden security posture",
			Division: "security",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Errorf("expected accepted = true, got false; rejection = %v", resp.GetRejection())
	}

	// Both publishers should be queried (federation-level -> all publishers).
	callsA := spyA.getCalls()
	callsB := spyB.getCalls()
	if len(callsA) != 1 {
		t.Errorf("spyA: expected 1 call, got %d", len(callsA))
	}
	if len(callsB) != 1 {
		t.Errorf("spyB: expected 1 call, got %d", len(callsB))
	}
}

func TestSubmitPublication_ResultsConsolidatedFromMultipleLibrarians(t *testing.T) {
	// Two Librarians return overlapping results (same law ID). The
	// consolidated list should be deduplicated by law ID.
	publisherA := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}
	publisherB := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-b",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-b",
			EmbassyEndpoint: "flow-pub-b-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	// Both Librarians return the same law (duplicate).
	sharedLaw := &flowv1.SimilarLaw{
		Law:             &flowv1.Law{Id: "law-existing-001", Goal: "Existing education law"},
		SimilarityScore: 0.95,
	}
	spyA := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: []*flowv1.SimilarLaw{sharedLaw}},
	}
	uniqueLaw := &flowv1.SimilarLaw{
		Law:             &flowv1.Law{Id: "law-existing-002", Goal: "Another education law"},
		SimilarityScore: 0.80,
	}
	spyB := &spyLibrarianClient{
		flowID: "flow-pub-b",
		response: &flowv1.SearchSimilarLawsResponse{Results: []*flowv1.SimilarLaw{
			sharedLaw,
			uniqueLaw,
		}},
	}

	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyA)
	dialer.addClient("flow-pub-b-embassy:50059", spyB)

	srv := newTestServerWithDialer(t, dialer, publisherA, publisherB)

	// Submit: both Librarians have results. Currently no LLM analysis
	// (13.8.3), so with no conflict analyser the publication should still
	// be accepted (conflict detection produces results but analysis is
	// wired in 13.8.3). The key assertion here is that the search was
	// performed and results consolidated.
	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-new-001",
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}

	// Both spies should have been called.
	if len(spyA.getCalls()) != 1 {
		t.Errorf("spyA: expected 1 call, got %d", len(spyA.getCalls()))
	}
	if len(spyB.getCalls()) != 1 {
		t.Errorf("spyB: expected 1 call, got %d", len(spyB.getCalls()))
	}

	// With no conflict analyser wired (13.8.3), no conflicts means accepted.
	if !resp.GetAccepted() {
		t.Errorf("expected accepted = true (no conflict analyser), got false; rejection = %v", resp.GetRejection())
	}
}

func TestSubmitPublication_LibrarianConnectionError_LoggedAndSkipped(t *testing.T) {
	// One publisher's Librarian is unreachable. The Federation service
	// should log the error and skip it (best-effort, non-blocking).
	publisherA := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}
	publisherB := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-b",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-b",
			EmbassyEndpoint: "flow-pub-b-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	spyA := &spyLibrarianClient{flowID: "flow-pub-a"}

	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyA)
	// Do NOT register a client for flow-pub-b -- DialLibrarian will return error.

	srv := newTestServerWithDialer(t, dialer, publisherA, publisherB)

	// Should succeed despite B being unreachable.
	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-001",
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Errorf("expected accepted = true (connection error is best-effort), got false; rejection = %v", resp.GetRejection())
	}

	// Only spyA should have been successfully queried.
	callsA := spyA.getCalls()
	if len(callsA) != 1 {
		t.Errorf("spyA: expected 1 call, got %d", len(callsA))
	}
}

func TestSubmitPublication_SearchSimilarLawsError_LoggedAndSkipped(t *testing.T) {
	// One publisher's Librarian is reachable but SearchSimilarLaws returns error.
	// Should be logged and skipped (best-effort, non-blocking).
	publisherA := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	spyA := &spyLibrarianClient{
		flowID: "flow-pub-a",
		err:    fmt.Errorf("embedding model unavailable"),
	}

	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyA)

	srv := newTestServerWithDialer(t, dialer, publisherA)

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-001",
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Errorf("expected accepted = true (search error is best-effort), got false; rejection = %v", resp.GetRejection())
	}
}

func TestSubmitPublication_EmptySearchResults_NoConflicts(t *testing.T) {
	// All Librarians return empty results -> no conflicts detected.
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	spyA := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: nil},
	}

	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyA)

	srv := newTestServerWithDialer(t, dialer, publisher)

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-001",
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Errorf("expected accepted = true (empty results = no conflicts), got false; rejection = %v", resp.GetRejection())
	}
}

// =============================================================================
// Stub ConflictAnalyser for 13.8.3 tests
// =============================================================================

// stubConflictAnalyser is a deterministic test double for the ConflictAnalyser
// interface. It returns a pre-configured report and records its calls.
type stubConflictAnalyser struct {
	mu     sync.Mutex
	calls  []stubAnalyserCall
	report *ConflictReport
	err    error
}

type stubAnalyserCall struct {
	candidateLaw *flowv1.Law
	similarLaws  []*flowv1.SimilarLaw
}

func (s *stubConflictAnalyser) AnalyseConflicts(
	_ context.Context,
	candidateLaw *flowv1.Law,
	similarLaws []*flowv1.SimilarLaw,
) (*ConflictReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, stubAnalyserCall{
		candidateLaw: candidateLaw,
		similarLaws:  similarLaws,
	})
	if s.err != nil {
		return nil, s.err
	}
	return s.report, nil
}

func (s *stubConflictAnalyser) getCalls() []stubAnalyserCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]stubAnalyserCall, len(s.calls))
	copy(cp, s.calls)
	return cp
}

// --- 13.8.3 SubmitPublication LLM Conflict Analysis Tests ---

func TestSubmitPublication_NoSimilarLaws_AnalyserNotCalled_Accepted(t *testing.T) {
	// No similar laws from search -> AnalyseConflicts is not called,
	// publication is accepted.
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	spyLib := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: nil}, // Empty results.
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyLib)

	analyser := &stubConflictAnalyser{
		report: &ConflictReport{HasConflicts: true}, // Would reject if called.
	}

	srv := newTestServerWithDialerAndAnalyser(t, dialer, analyser, publisher)

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-001",
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Errorf("expected accepted = true (no similar laws -> no analysis), got false; rejection = %v", resp.GetRejection())
	}

	// The analyser must NOT have been called.
	if len(analyser.getCalls()) != 0 {
		t.Errorf("expected 0 analyser calls (no similar laws), got %d", len(analyser.getCalls()))
	}
}

func TestSubmitPublication_SimilarLaws_NoRealConflicts_Accepted(t *testing.T) {
	// Similar laws found but LLM determines no real conflicts -> accepted.
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	similarLaw := &flowv1.SimilarLaw{
		Law:             &flowv1.Law{Id: "law-existing-001", Goal: "Existing education law"},
		SimilarityScore: 0.85,
	}
	spyLib := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: []*flowv1.SimilarLaw{similarLaw}},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyLib)

	analyser := &stubConflictAnalyser{
		report: &ConflictReport{HasConflicts: false}, // No real conflicts.
	}

	srv := newTestServerWithDialerAndAnalyser(t, dialer, analyser, publisher)

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-new-001",
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Errorf("expected accepted = true (no real conflicts), got false; rejection = %v", resp.GetRejection())
	}

	// Analyser should have been called once.
	calls := analyser.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 analyser call, got %d", len(calls))
	}
	if calls[0].candidateLaw.GetId() != "law-new-001" {
		t.Errorf("analyser candidate law ID = %q, want %q", calls[0].candidateLaw.GetId(), "law-new-001")
	}
	if len(calls[0].similarLaws) != 1 {
		t.Errorf("analyser similar laws count = %d, want 1", len(calls[0].similarLaws))
	}
}

func TestSubmitPublication_SimilarLaws_RealConflicts_Rejected(t *testing.T) {
	// Similar laws found and LLM determines real conflicts -> rejected
	// with CONFLICT reason, conflicting_law_ids, and remediation_text.
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	conflictingLaw := &flowv1.SimilarLaw{
		Law:             &flowv1.Law{Id: "law-existing-001", Goal: "Existing education law"},
		SimilarityScore: 0.95,
	}
	spyLib := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: []*flowv1.SimilarLaw{conflictingLaw}},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyLib)

	analyser := &stubConflictAnalyser{
		report: &ConflictReport{
			HasConflicts:      true,
			ConflictingLawIDs: []string{"law-existing-001"},
			RemediationText:   "The proposed law directly contradicts law-existing-001. Consider revising the scope.",
		},
	}

	srv := newTestServerWithDialerAndAnalyser(t, dialer, analyser, publisher)

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-new-001",
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}

	if resp.GetAccepted() {
		t.Fatal("expected accepted = false (real conflicts detected)")
	}
	rejection := resp.GetRejection()
	if rejection == nil {
		t.Fatal("expected rejection to be non-nil")
	}
	if rejection.GetReason() != flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_CONFLICT {
		t.Errorf("rejection reason = %v, want CONFLICT", rejection.GetReason())
	}
	if len(rejection.GetConflictingLawIds()) != 1 || rejection.GetConflictingLawIds()[0] != "law-existing-001" {
		t.Errorf("conflicting_law_ids = %v, want [law-existing-001]", rejection.GetConflictingLawIds())
	}
	if rejection.GetRemediationText() == "" {
		t.Error("expected remediation_text to be non-empty")
	}
}

func TestSubmitPublication_ConflictRejection_IncludesLawIDsAndRemediation(t *testing.T) {
	// Verify that rejection includes conflicting_law_ids and remediation_text
	// from the LLM analysis report.
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	laws := []*flowv1.SimilarLaw{
		{Law: &flowv1.Law{Id: "law-001", Goal: "Law A"}, SimilarityScore: 0.92},
		{Law: &flowv1.Law{Id: "law-002", Goal: "Law B"}, SimilarityScore: 0.88},
	}
	spyLib := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: laws},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyLib)

	analyser := &stubConflictAnalyser{
		report: &ConflictReport{
			HasConflicts:      true,
			ConflictingLawIDs: []string{"law-001", "law-002"},
			RemediationText:   "Both law-001 and law-002 conflict. Narrow scope or retire them first.",
		},
	}

	srv := newTestServerWithDialerAndAnalyser(t, dialer, analyser, publisher)

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-new-001",
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}

	if resp.GetAccepted() {
		t.Fatal("expected accepted = false")
	}
	rejection := resp.GetRejection()
	if len(rejection.GetConflictingLawIds()) != 2 {
		t.Fatalf("conflicting_law_ids count = %d, want 2", len(rejection.GetConflictingLawIds()))
	}
	wantIDs := map[string]bool{"law-001": true, "law-002": true}
	for _, id := range rejection.GetConflictingLawIds() {
		if !wantIDs[id] {
			t.Errorf("unexpected conflicting_law_id: %q", id)
		}
	}
	if rejection.GetRemediationText() != "Both law-001 and law-002 conflict. Narrow scope or retire them first." {
		t.Errorf("remediation_text = %q, want specific text", rejection.GetRemediationText())
	}
}

// =============================================================================
// Spy EventDispatcher for 13.8.4 tests
// =============================================================================

// spyEventDispatcher records dispatched events for test assertions.
type spyEventDispatcher struct {
	mu               sync.Mutex
	lawEvents        []*flowv1.PublishedLawEvent
	petitionOutcomes []*flowv1.PetitionOutcomeEvent
}

func (s *spyEventDispatcher) DispatchLawEvent(_ context.Context, event *flowv1.PublishedLawEvent, _ []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lawEvents = append(s.lawEvents, event)
}

func (s *spyEventDispatcher) DispatchPetitionOutcomeEvent(_ context.Context, event *flowv1.PetitionOutcomeEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.petitionOutcomes = append(s.petitionOutcomes, event)
}

func (s *spyEventDispatcher) getLawEvents() []*flowv1.PublishedLawEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]*flowv1.PublishedLawEvent, len(s.lawEvents))
	copy(cp, s.lawEvents)
	return cp
}

func (s *spyEventDispatcher) getPetitionOutcomes() []*flowv1.PetitionOutcomeEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]*flowv1.PetitionOutcomeEvent, len(s.petitionOutcomes))
	copy(cp, s.petitionOutcomes)
	return cp
}

// newTestServerFull creates a FederationServer with all injectable
// dependencies for comprehensive testing.
func newTestServerFull(
	t *testing.T,
	dialer LibrarianDialer,
	analyser ConflictAnalyser,
	dispatcher EventDispatcher,
	objs ...client.Object,
) *FederationServer {
	t.Helper()

	scheme := federationv1.NewTestScheme()
	builder := fake.NewClientBuilder().WithScheme(scheme)
	if len(objs) > 0 {
		builder = builder.WithObjects(objs...)
	}
	k8sClient := builder.Build()

	opts := []FederationOption{
		WithFederationConfig(&flowv1.FederationConfig{
			FederationId:   "fed-001",
			FederationName: "Test Federation",
			RootCaPem:      "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		}),
		WithBootstrapToken("valid-token"),
	}
	if dialer != nil {
		opts = append(opts, WithLibrarianDialer(dialer))
	}
	if analyser != nil {
		opts = append(opts, WithConflictAnalyser(analyser))
	}
	if dispatcher != nil {
		opts = append(opts, WithEventDispatcher(dispatcher))
	}

	return NewFederationServer(k8sClient, testNamespace, opts...)
}

func TestSubmitPublication_AnalyserError_FailSafeReject(t *testing.T) {
	// LLM error -> publication rejected with INTERNAL-style error (fail-safe:
	// do not publish on uncertainty).
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	similarLaw := &flowv1.SimilarLaw{
		Law:             &flowv1.Law{Id: "law-existing-001", Goal: "Existing education law"},
		SimilarityScore: 0.85,
	}
	spyLib := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: []*flowv1.SimilarLaw{similarLaw}},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyLib)

	analyser := &stubConflictAnalyser{
		err: fmt.Errorf("ollama connection refused"),
	}

	srv := newTestServerWithDialerAndAnalyser(t, dialer, analyser, publisher)

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-new-001",
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}

	// Fail-safe: rejected on analyser error.
	if resp.GetAccepted() {
		t.Fatal("expected accepted = false (fail-safe on analyser error)")
	}
	rejection := resp.GetRejection()
	if rejection == nil {
		t.Fatal("expected rejection to be non-nil")
	}
	// The rejection reason should indicate an internal/conflict analysis failure.
	if rejection.GetReason() != flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_CONFLICT {
		t.Errorf("rejection reason = %v, want CONFLICT (fail-safe)", rejection.GetReason())
	}
	if rejection.GetRemediationText() == "" {
		t.Error("expected remediation_text to contain error context")
	}
}

// --- 13.8.4 SubmitPublication Acceptance and Distribution Trigger Tests ---

func TestSubmitPublication_NoConflicts_AcceptedResponseTrue(t *testing.T) {
	// When authority validation and conflict analysis both pass,
	// SubmitPublicationResponse.accepted must be true.
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	spyLib := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: nil},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyLib)

	analyser := &stubConflictAnalyser{
		report: &ConflictReport{HasConflicts: false},
	}
	dispatcher := &spyEventDispatcher{}

	srv := newTestServerFull(t, dialer, analyser, dispatcher, publisher)

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       testLawID,
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Errorf("expected accepted = true, got false; rejection = %v", resp.GetRejection())
	}
}

func TestSubmitPublication_NoConflicts_DispatchesPublishedLawEvent(t *testing.T) {
	// When publication is accepted, a PublishedLawEvent must be dispatched
	// to the event dispatcher (verified via spy subscriber).
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	spyLib := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: nil},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyLib)

	analyser := &stubConflictAnalyser{
		report: &ConflictReport{HasConflicts: false},
	}
	dispatcher := &spyEventDispatcher{}

	srv := newTestServerFull(t, dialer, analyser, dispatcher, publisher)

	submittedLaw := &flowv1.Law{
		Id:       testLawID,
		Goal:     "Ensure quality education",
		Division: "education",
		Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
	}

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law:                submittedLaw,
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected accepted = true, got false; rejection = %v", resp.GetRejection())
	}

	// Verify a PublishedLawEvent was dispatched.
	events := dispatcher.getLawEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 PublishedLawEvent dispatched, got %d", len(events))
	}

	event := events[0]
	if event.GetLaw().GetId() != testLawID {
		t.Errorf("event law ID = %q, want %q", event.GetLaw().GetId(), testLawID)
	}
	if event.GetPublisherFlowIdentity() != testFlowPubA {
		t.Errorf("event publisher_flow_identity = %q, want %q", event.GetPublisherFlowIdentity(), testFlowPubA)
	}
	if event.GetPublishedAt() == nil {
		t.Error("event published_at is nil")
	}
}

func TestSubmitPublication_StateLevelPublisher_MaterialisationTier4(t *testing.T) {
	// State-level publisher -> materialisation tier is Tier 4 (STATE_CONSTITUTION).
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	spyLib := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: nil},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyLib)
	dispatcher := &spyEventDispatcher{}

	srv := newTestServerFull(t, dialer, nil, dispatcher, publisher)

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       testLawID,
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected accepted = true, got false; rejection = %v", resp.GetRejection())
	}

	events := dispatcher.getLawEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 PublishedLawEvent, got %d", len(events))
	}
	if events[0].GetMaterialisationTier() != flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION {
		t.Errorf("materialisation_tier = %v, want LAW_TIER_STATE_CONSTITUTION (Tier 4)",
			events[0].GetMaterialisationTier())
	}
}

func TestSubmitPublication_FederationLevelPublisher_MaterialisationTier5(t *testing.T) {
	// Federation-level publisher -> materialisation tier is Tier 5 (FEDERAL_ACCORD).
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-fed-pub",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-fed-pub",
			EmbassyEndpoint: "flow-fed-pub-embassy:50059",
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "security", Level: "federation"},
			},
		},
	}

	spyLib := &spyLibrarianClient{
		flowID:   "flow-fed-pub",
		response: &flowv1.SearchSimilarLawsResponse{Results: nil},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-fed-pub-embassy:50059", spyLib)
	dispatcher := &spyEventDispatcher{}

	srv := newTestServerFull(t, dialer, nil, dispatcher, publisher)

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
		t.Fatalf("expected accepted = true, got false; rejection = %v", resp.GetRejection())
	}

	events := dispatcher.getLawEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 PublishedLawEvent, got %d", len(events))
	}
	if events[0].GetMaterialisationTier() != flowv1.LawTier_LAW_TIER_FEDERAL_ACCORD {
		t.Errorf("materialisation_tier = %v, want LAW_TIER_FEDERAL_ACCORD (Tier 5)",
			events[0].GetMaterialisationTier())
	}
}

func TestSubmitPublication_PublishedLawEvent_IncludesAllFields(t *testing.T) {
	// Verify the PublishedLawEvent includes law, materialisation_tier,
	// petition_id (from law provenance), publisher_flow_identity, published_at.
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	spyLib := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: nil},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyLib)
	dispatcher := &spyEventDispatcher{}

	srv := newTestServerFull(t, dialer, nil, dispatcher, publisher)

	submittedLaw := &flowv1.Law{
		Id:       testLawID,
		Goal:     "Ensure quality education",
		Division: "education",
		Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
	}

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law:                submittedLaw,
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected accepted = true, got false; rejection = %v", resp.GetRejection())
	}

	events := dispatcher.getLawEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 PublishedLawEvent, got %d", len(events))
	}

	event := events[0]

	// Law must be included.
	if event.GetLaw() == nil {
		t.Fatal("event law is nil")
	}
	if event.GetLaw().GetId() != testLawID {
		t.Errorf("event law ID = %q, want %q", event.GetLaw().GetId(), testLawID)
	}
	if event.GetLaw().GetGoal() != "Ensure quality education" {
		t.Errorf("event law goal = %q, want %q", event.GetLaw().GetGoal(), "Ensure quality education")
	}

	// Materialisation tier (state-level publisher -> Tier 4).
	if event.GetMaterialisationTier() != flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION {
		t.Errorf("materialisation_tier = %v, want LAW_TIER_STATE_CONSTITUTION",
			event.GetMaterialisationTier())
	}

	// Petition ID: empty for laws without petition provenance (Law proto
	// does not carry provenance yet; this is structurally correct).
	// This test verifies the field is present and populated from available data.
	// (petition_id is empty here because the Law proto has no provenance field.)

	// Publisher flow identity.
	if event.GetPublisherFlowIdentity() != testFlowPubA {
		t.Errorf("publisher_flow_identity = %q, want %q",
			event.GetPublisherFlowIdentity(), testFlowPubA)
	}

	// Published timestamp must be set.
	if event.GetPublishedAt() == nil {
		t.Error("published_at is nil, expected a timestamp")
	}
}

func TestSubmitPublication_ConflictRejection_NoPetitionOutcomeWithoutPetitionID(t *testing.T) {
	// When a publication is rejected and the law does NOT have petition_id
	// provenance, no PetitionOutcomeEvent should be dispatched.
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	conflictingLaw := &flowv1.SimilarLaw{
		Law:             &flowv1.Law{Id: "law-existing-001", Goal: "Existing education law"},
		SimilarityScore: 0.95,
	}
	spyLib := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: []*flowv1.SimilarLaw{conflictingLaw}},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyLib)

	analyser := &stubConflictAnalyser{
		report: &ConflictReport{
			HasConflicts:      true,
			ConflictingLawIDs: []string{"law-existing-001"},
			RemediationText:   "Conflict detected.",
		},
	}
	dispatcher := &spyEventDispatcher{}

	srv := newTestServerFull(t, dialer, analyser, dispatcher, publisher)

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-new-001",
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("expected accepted = false")
	}

	// No petition_id on the law -> no PetitionOutcomeEvent.
	outcomes := dispatcher.getPetitionOutcomes()
	if len(outcomes) != 0 {
		t.Errorf("expected 0 PetitionOutcomeEvents (no petition_id), got %d", len(outcomes))
	}

	// No law event should be dispatched on rejection.
	lawEvents := dispatcher.getLawEvents()
	if len(lawEvents) != 0 {
		t.Errorf("expected 0 PublishedLawEvents (rejected), got %d", len(lawEvents))
	}
}

func TestSubmitPublication_ConflictRejection_DispatchesPetitionOutcomeEvent(t *testing.T) {
	// When a publication is rejected and the law HAS petition_id provenance,
	// a PetitionOutcomeEvent with REJECTED outcome must be dispatched.
	// Since the Law proto does not carry petition_id, we use a convention:
	// the SubmitPublicationRequest can carry a petition_id field. For now,
	// we test via the request's petition_id field.
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	conflictingLaw := &flowv1.SimilarLaw{
		Law:             &flowv1.Law{Id: "law-existing-001", Goal: "Existing education law"},
		SimilarityScore: 0.95,
	}
	spyLib := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: []*flowv1.SimilarLaw{conflictingLaw}},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyLib)

	analyser := &stubConflictAnalyser{
		report: &ConflictReport{
			HasConflicts:      true,
			ConflictingLawIDs: []string{"law-existing-001"},
			RemediationText:   "The proposed law contradicts law-existing-001.",
		},
	}
	dispatcher := &spyEventDispatcher{}

	srv := newTestServerFull(t, dialer, analyser, dispatcher, publisher)

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-new-001",
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
		PetitionId:         "petition-abc-123",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("expected accepted = false")
	}

	// PetitionOutcomeEvent must be dispatched with REJECTED.
	outcomes := dispatcher.getPetitionOutcomes()
	if len(outcomes) != 1 {
		t.Fatalf("expected 1 PetitionOutcomeEvent, got %d", len(outcomes))
	}

	outcome := outcomes[0]
	if outcome.GetPetitionId() != "petition-abc-123" {
		t.Errorf("petition_id = %q, want %q", outcome.GetPetitionId(), "petition-abc-123")
	}
	if outcome.GetOutcome() != flowv1.PetitionOutcome_PETITION_OUTCOME_REJECTED {
		t.Errorf("outcome = %v, want PETITION_OUTCOME_REJECTED", outcome.GetOutcome())
	}
	if outcome.GetRejection() == nil {
		t.Fatal("expected rejection details on PetitionOutcomeEvent")
	}
	if outcome.GetRejection().GetReason() != flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_CONFLICT {
		t.Errorf("rejection reason = %v, want CONFLICT", outcome.GetRejection().GetReason())
	}
	if outcome.GetResolvedAt() == nil {
		t.Error("resolved_at is nil")
	}
}

// =============================================================================
// Mock gRPC server streams for 13.9.1 subscriber registry tests
// =============================================================================

// mockLawUpdateStream implements grpc.ServerStreamingServer[PublishedLawEvent]
// for testing SubscribeLawUpdates.
type mockLawUpdateStream struct {
	grpc.ServerStream
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	events []*flowv1.PublishedLawEvent
}

func newMockLawUpdateStream(ctx context.Context) *mockLawUpdateStream {
	ctx, cancel := context.WithCancel(ctx)
	return &mockLawUpdateStream{ctx: ctx, cancel: cancel}
}

func (m *mockLawUpdateStream) Send(event *flowv1.PublishedLawEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Check if context has been cancelled (simulates client disconnect).
	select {
	case <-m.ctx.Done():
		return m.ctx.Err()
	default:
	}
	m.events = append(m.events, event)
	return nil
}

func (m *mockLawUpdateStream) Context() context.Context {
	return m.ctx
}

func (m *mockLawUpdateStream) getEvents() []*flowv1.PublishedLawEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]*flowv1.PublishedLawEvent, len(m.events))
	copy(cp, m.events)
	return cp
}

// --- 13.9.1 SubscribeLawUpdates and Subscriber Registry Tests ---

func TestSubscribeLawUpdates_RegistersSubscriberAndReceivesEvents(t *testing.T) {
	// A subscriber calls SubscribeLawUpdates and receives a PublishedLawEvent
	// dispatched via SubmitPublication acceptance.
	subscriberMember := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-sub",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-sub",
			EmbassyEndpoint: "flow-sub-embassy:50059",
			StateRefs:       []string{"state-qld"},
		},
	}
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	spyLib := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: nil},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyLib)

	srv := newTestServerWithDialer(t, dialer, subscriberMember, publisher)

	// Start subscriber in a goroutine (SubscribeLawUpdates blocks).
	stream := newMockLawUpdateStream(context.Background())
	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- srv.SubscribeLawUpdates(
			&flowv1.SubscribeLawUpdatesRequest{SubscriberFlowIdentity: "flow-sub"},
			stream,
		)
	}()

	// Give the subscriber time to register.
	waitForLawSubscriber(t, srv, "flow-sub")

	// Publish a law (accepted).
	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-001",
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected accepted = true, got false; rejection = %v", resp.GetRejection())
	}

	// Disconnect the subscriber.
	stream.cancel()
	<-subErrCh

	// The subscriber should have received the event.
	events := stream.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 event on subscriber stream, got %d", len(events))
	}
	if events[0].GetLaw().GetId() != testLawID {
		t.Errorf("event law ID = %q, want %q", events[0].GetLaw().GetId(), testLawID)
	}
	if events[0].GetPublisherFlowIdentity() != testFlowPubA {
		t.Errorf("event publisher_flow_identity = %q, want %q", events[0].GetPublisherFlowIdentity(), testFlowPubA)
	}
	if events[0].GetPublishedAt() == nil {
		t.Error("event published_at is nil")
	}
}

func TestSubscribeLawUpdates_StateLevelPublication_OnlySameStateSubscribersReceive(t *testing.T) {
	// State-level publication: only subscribers in the same state(s) as the
	// publisher receive the event. Subscriber in a different state does not.
	subQLD := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-sub-qld",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-sub-qld",
			EmbassyEndpoint: "flow-sub-qld-embassy:50059",
			StateRefs:       []string{"state-qld"},
		},
	}
	subNSW := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-sub-nsw",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-sub-nsw",
			EmbassyEndpoint: "flow-sub-nsw-embassy:50059",
			StateRefs:       []string{"state-nsw"},
		},
	}
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	spyLib := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: nil},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyLib)

	srv := newTestServerWithDialer(t, dialer, subQLD, subNSW, publisher)

	// Start two subscribers.
	streamQLD := newMockLawUpdateStream(context.Background())
	streamNSW := newMockLawUpdateStream(context.Background())
	qldErrCh := make(chan error, 1)
	nswErrCh := make(chan error, 1)
	go func() {
		qldErrCh <- srv.SubscribeLawUpdates(
			&flowv1.SubscribeLawUpdatesRequest{SubscriberFlowIdentity: "flow-sub-qld"},
			streamQLD,
		)
	}()
	go func() {
		nswErrCh <- srv.SubscribeLawUpdates(
			&flowv1.SubscribeLawUpdatesRequest{SubscriberFlowIdentity: "flow-sub-nsw"},
			streamNSW,
		)
	}()

	waitForLawSubscriber(t, srv, "flow-sub-qld")
	waitForLawSubscriber(t, srv, "flow-sub-nsw")

	// Publish a state-level law (QLD scope).
	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-001",
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected accepted = true, got false; rejection = %v", resp.GetRejection())
	}

	// Disconnect both subscribers.
	streamQLD.cancel()
	streamNSW.cancel()
	<-qldErrCh
	<-nswErrCh

	// QLD subscriber should receive the event.
	qldEvents := streamQLD.getEvents()
	if len(qldEvents) != 1 {
		t.Errorf("QLD subscriber: expected 1 event, got %d", len(qldEvents))
	}

	// NSW subscriber should NOT receive the event (different state).
	nswEvents := streamNSW.getEvents()
	if len(nswEvents) != 0 {
		t.Errorf("NSW subscriber: expected 0 events (different state), got %d", len(nswEvents))
	}
}

func TestSubscribeLawUpdates_FederationLevelPublication_AllSubscribersReceive(t *testing.T) {
	// Federation-level publication: all subscribers receive the event
	// regardless of state membership.
	subQLD := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-sub-qld",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-sub-qld",
			EmbassyEndpoint: "flow-sub-qld-embassy:50059",
			StateRefs:       []string{"state-qld"},
		},
	}
	subNSW := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-sub-nsw",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-sub-nsw",
			EmbassyEndpoint: "flow-sub-nsw-embassy:50059",
			StateRefs:       []string{"state-nsw"},
		},
	}
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-fed-pub",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-fed-pub",
			EmbassyEndpoint: "flow-fed-pub-embassy:50059",
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "security", Level: "federation"},
			},
		},
	}

	spyLib := &spyLibrarianClient{
		flowID:   "flow-fed-pub",
		response: &flowv1.SearchSimilarLawsResponse{Results: nil},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-fed-pub-embassy:50059", spyLib)

	srv := newTestServerWithDialer(t, dialer, subQLD, subNSW, publisher)

	// Start two subscribers.
	streamQLD := newMockLawUpdateStream(context.Background())
	streamNSW := newMockLawUpdateStream(context.Background())
	qldErrCh := make(chan error, 1)
	nswErrCh := make(chan error, 1)
	go func() {
		qldErrCh <- srv.SubscribeLawUpdates(
			&flowv1.SubscribeLawUpdatesRequest{SubscriberFlowIdentity: "flow-sub-qld"},
			streamQLD,
		)
	}()
	go func() {
		nswErrCh <- srv.SubscribeLawUpdates(
			&flowv1.SubscribeLawUpdatesRequest{SubscriberFlowIdentity: "flow-sub-nsw"},
			streamNSW,
		)
	}()

	waitForLawSubscriber(t, srv, "flow-sub-qld")
	waitForLawSubscriber(t, srv, "flow-sub-nsw")

	// Publish a federation-level law.
	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-sec-001",
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
		t.Fatalf("expected accepted = true, got false; rejection = %v", resp.GetRejection())
	}

	// Disconnect both subscribers.
	streamQLD.cancel()
	streamNSW.cancel()
	<-qldErrCh
	<-nswErrCh

	// Both subscribers should receive the event (federation-level).
	qldEvents := streamQLD.getEvents()
	if len(qldEvents) != 1 {
		t.Errorf("QLD subscriber: expected 1 event, got %d", len(qldEvents))
	}
	nswEvents := streamNSW.getEvents()
	if len(nswEvents) != 1 {
		t.Errorf("NSW subscriber: expected 1 event, got %d", len(nswEvents))
	}
}

func TestSubscribeLawUpdates_PublishedLawEvent_IncludesAllFields(t *testing.T) {
	// Verify the event received by the subscriber includes all required fields:
	// law, materialisation_tier, petition_id, publisher_flow_identity, published_at.
	subscriber := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-sub",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-sub",
			EmbassyEndpoint: "flow-sub-embassy:50059",
			StateRefs:       []string{"state-qld"},
		},
	}
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	spyLib := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: nil},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyLib)

	srv := newTestServerWithDialer(t, dialer, subscriber, publisher)

	stream := newMockLawUpdateStream(context.Background())
	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- srv.SubscribeLawUpdates(
			&flowv1.SubscribeLawUpdatesRequest{SubscriberFlowIdentity: "flow-sub"},
			stream,
		)
	}()

	waitForLawSubscriber(t, srv, "flow-sub")

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       testLawID,
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
		PetitionId:         "petition-001",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected accepted = true, got false; rejection = %v", resp.GetRejection())
	}

	stream.cancel()
	<-subErrCh

	events := stream.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	event := events[0]

	// Law.
	if event.GetLaw() == nil {
		t.Fatal("event law is nil")
	}
	if event.GetLaw().GetId() != testLawID {
		t.Errorf("law ID = %q, want %q", event.GetLaw().GetId(), testLawID)
	}
	// Materialisation tier (state-level -> Tier 4).
	if event.GetMaterialisationTier() != flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION {
		t.Errorf("materialisation_tier = %v, want LAW_TIER_STATE_CONSTITUTION",
			event.GetMaterialisationTier())
	}
	// Petition ID.
	if event.GetPetitionId() != "petition-001" {
		t.Errorf("petition_id = %q, want %q", event.GetPetitionId(), "petition-001")
	}
	// Publisher flow identity.
	if event.GetPublisherFlowIdentity() != testFlowPubA {
		t.Errorf("publisher_flow_identity = %q, want %q",
			event.GetPublisherFlowIdentity(), testFlowPubA)
	}
	// Published timestamp.
	if event.GetPublishedAt() == nil {
		t.Error("published_at is nil")
	}
}

func TestSubscribeLawUpdates_DisconnectedSubscriberIsRemoved(t *testing.T) {
	// When a subscriber disconnects (context cancelled), it is removed from
	// the registry. Subsequent events are not sent to it and do not error.
	subscriber := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-sub",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-sub",
			EmbassyEndpoint: "flow-sub-embassy:50059",
			StateRefs:       []string{"state-qld"},
		},
	}
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	spyLib := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: nil},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyLib)

	srv := newTestServerWithDialer(t, dialer, subscriber, publisher)

	// Subscribe and then disconnect.
	stream := newMockLawUpdateStream(context.Background())
	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- srv.SubscribeLawUpdates(
			&flowv1.SubscribeLawUpdatesRequest{SubscriberFlowIdentity: "flow-sub"},
			stream,
		)
	}()

	waitForLawSubscriber(t, srv, "flow-sub")

	// Disconnect the subscriber.
	stream.cancel()
	<-subErrCh

	// Now publish a law. This should not panic or error -- the subscriber
	// has been removed from the registry.
	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-001",
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected accepted = true, got false; rejection = %v", resp.GetRejection())
	}

	// Subscriber should not have received the event (disconnected before publish).
	events := stream.getEvents()
	if len(events) != 0 {
		t.Errorf("expected 0 events (disconnected subscriber), got %d", len(events))
	}
}

func TestSubscribeLawUpdates_MultipleSubscribersReceiveSameEvent(t *testing.T) {
	// Multiple subscribers in the same state both receive the same event.
	sub1 := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-sub-1",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-sub-1",
			EmbassyEndpoint: "flow-sub-1-embassy:50059",
			StateRefs:       []string{"state-qld"},
		},
	}
	sub2 := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-sub-2",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-sub-2",
			EmbassyEndpoint: "flow-sub-2-embassy:50059",
			StateRefs:       []string{"state-qld"},
		},
	}
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	spyLib := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: nil},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyLib)

	srv := newTestServerWithDialer(t, dialer, sub1, sub2, publisher)

	stream1 := newMockLawUpdateStream(context.Background())
	stream2 := newMockLawUpdateStream(context.Background())
	errCh1 := make(chan error, 1)
	errCh2 := make(chan error, 1)
	go func() {
		errCh1 <- srv.SubscribeLawUpdates(
			&flowv1.SubscribeLawUpdatesRequest{SubscriberFlowIdentity: "flow-sub-1"},
			stream1,
		)
	}()
	go func() {
		errCh2 <- srv.SubscribeLawUpdates(
			&flowv1.SubscribeLawUpdatesRequest{SubscriberFlowIdentity: "flow-sub-2"},
			stream2,
		)
	}()

	waitForLawSubscriber(t, srv, "flow-sub-1")
	waitForLawSubscriber(t, srv, "flow-sub-2")

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       "law-001",
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected accepted = true, got false; rejection = %v", resp.GetRejection())
	}

	stream1.cancel()
	stream2.cancel()
	<-errCh1
	<-errCh2

	events1 := stream1.getEvents()
	events2 := stream2.getEvents()
	if len(events1) != 1 {
		t.Errorf("subscriber 1: expected 1 event, got %d", len(events1))
	}
	if len(events2) != 1 {
		t.Errorf("subscriber 2: expected 1 event, got %d", len(events2))
	}
	// Both should receive the same law ID.
	if len(events1) > 0 && events1[0].GetLaw().GetId() != testLawID {
		t.Errorf("subscriber 1 event law ID = %q, want %q", events1[0].GetLaw().GetId(), testLawID)
	}
	if len(events2) > 0 && events2[0].GetLaw().GetId() != testLawID {
		t.Errorf("subscriber 2 event law ID = %q, want %q", events2[0].GetLaw().GetId(), testLawID)
	}
}

// waitForLawSubscriber polls the server's subscriber registry until the
// subscriber is registered, or fails the test after a timeout.
func waitForLawSubscriber(t *testing.T, srv *FederationServer, flowIdentity string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.HasLawSubscriber(flowIdentity) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for law subscriber %q to register", flowIdentity)
}

func TestSubmitPublication_Acceptance_DispatchesPetitionOutcomeEvent(t *testing.T) {
	// When a publication is accepted and has petition_id provenance,
	// a PetitionOutcomeEvent with ACCEPTED outcome must be dispatched.
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	spyLib := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: nil},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyLib)
	dispatcher := &spyEventDispatcher{}

	srv := newTestServerFull(t, dialer, nil, dispatcher, publisher)

	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       testLawID,
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
		PetitionId:         "petition-xyz-789",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected accepted = true, got false; rejection = %v", resp.GetRejection())
	}

	// PublishedLawEvent should have petition_id populated.
	lawEvents := dispatcher.getLawEvents()
	if len(lawEvents) != 1 {
		t.Fatalf("expected 1 PublishedLawEvent, got %d", len(lawEvents))
	}
	if lawEvents[0].GetPetitionId() != "petition-xyz-789" {
		t.Errorf("event petition_id = %q, want %q",
			lawEvents[0].GetPetitionId(), "petition-xyz-789")
	}

	// PetitionOutcomeEvent should also be dispatched with ACCEPTED.
	outcomes := dispatcher.getPetitionOutcomes()
	if len(outcomes) != 1 {
		t.Fatalf("expected 1 PetitionOutcomeEvent, got %d", len(outcomes))
	}
	if outcomes[0].GetPetitionId() != "petition-xyz-789" {
		t.Errorf("outcome petition_id = %q, want %q",
			outcomes[0].GetPetitionId(), "petition-xyz-789")
	}
	if outcomes[0].GetOutcome() != flowv1.PetitionOutcome_PETITION_OUTCOME_ACCEPTED {
		t.Errorf("outcome = %v, want PETITION_OUTCOME_ACCEPTED",
			outcomes[0].GetOutcome())
	}
	if outcomes[0].GetPublishedLawId() != testLawID {
		t.Errorf("published_law_id = %q, want %q",
			outcomes[0].GetPublishedLawId(), testLawID)
	}
}

// =============================================================================
// Mock gRPC server streams for 13.9.2 SubscribePetitionOutcomes tests
// =============================================================================

// mockPetitionOutcomeStream implements grpc.ServerStreamingServer[PetitionOutcomeEvent]
// for testing SubscribePetitionOutcomes.
type mockPetitionOutcomeStream struct {
	grpc.ServerStream
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	events []*flowv1.PetitionOutcomeEvent
}

func newMockPetitionOutcomeStream(ctx context.Context) *mockPetitionOutcomeStream {
	ctx, cancel := context.WithCancel(ctx)
	return &mockPetitionOutcomeStream{ctx: ctx, cancel: cancel}
}

func (m *mockPetitionOutcomeStream) Send(event *flowv1.PetitionOutcomeEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-m.ctx.Done():
		return m.ctx.Err()
	default:
	}
	m.events = append(m.events, event)
	return nil
}

func (m *mockPetitionOutcomeStream) Context() context.Context {
	return m.ctx
}

func (m *mockPetitionOutcomeStream) getEvents() []*flowv1.PetitionOutcomeEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]*flowv1.PetitionOutcomeEvent, len(m.events))
	copy(cp, m.events)
	return cp
}

// waitForPetitionSubscriber polls the server's subscriber registry until the
// petition-outcome subscriber is registered, or fails the test after a timeout.
func waitForPetitionSubscriber(t *testing.T, srv *FederationServer, flowIdentity string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.HasPetitionSubscriber(flowIdentity) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for petition subscriber %q to register", flowIdentity)
}

// --- 13.9.2 SubscribePetitionOutcomes Tests ---

func TestSubscribePetitionOutcomes_RegistersAndReceivesAcceptedEvent(t *testing.T) {
	// A subscriber calls SubscribePetitionOutcomes and receives an ACCEPTED
	// PetitionOutcomeEvent when a publication with petition_id is accepted.
	subscriber := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-sub",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-sub",
			EmbassyEndpoint: "flow-sub-embassy:50059",
			StateRefs:       []string{"state-qld"},
		},
	}
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	spyLib := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: nil},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyLib)

	srv := newTestServerWithDialer(t, dialer, subscriber, publisher)

	// Start petition-outcome subscriber in a goroutine.
	stream := newMockPetitionOutcomeStream(context.Background())
	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- srv.SubscribePetitionOutcomes(
			&flowv1.SubscribePetitionOutcomesRequest{SubscriberFlowIdentity: "flow-sub"},
			stream,
		)
	}()

	waitForPetitionSubscriber(t, srv, "flow-sub")

	// Publish a law with petition_id (accepted, no conflicts).
	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       testLawID,
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
		PetitionId:         "petition-abc-001",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected accepted = true, got false; rejection = %v", resp.GetRejection())
	}

	// Disconnect the subscriber.
	stream.cancel()
	<-subErrCh

	// Verify the subscriber received the ACCEPTED petition outcome event.
	events := stream.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 petition outcome event, got %d", len(events))
	}
	ev := events[0]
	if ev.GetPetitionId() != "petition-abc-001" {
		t.Errorf("petition_id = %q, want %q", ev.GetPetitionId(), "petition-abc-001")
	}
	if ev.GetOutcome() != flowv1.PetitionOutcome_PETITION_OUTCOME_ACCEPTED {
		t.Errorf("outcome = %v, want PETITION_OUTCOME_ACCEPTED", ev.GetOutcome())
	}
	if ev.GetPublishedLawId() != testLawID {
		t.Errorf("published_law_id = %q, want %q", ev.GetPublishedLawId(), testLawID)
	}
	if ev.GetRejection() != nil {
		t.Error("expected no rejection on ACCEPTED event")
	}
	if ev.GetResolvedAt() == nil {
		t.Error("resolved_at is nil")
	}
}

func TestSubscribePetitionOutcomes_ReceivesRejectedEventWithReport(t *testing.T) {
	// When a publication with petition_id is rejected (conflict), the
	// petition subscriber receives a REJECTED event with rejection details.
	subscriber := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-sub",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-sub",
			EmbassyEndpoint: "flow-sub-embassy:50059",
			StateRefs:       []string{"state-qld"},
		},
	}
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	// Return similar laws so conflict analysis triggers.
	spyLib := &spyLibrarianClient{
		flowID: "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{
			Results: []*flowv1.SimilarLaw{
				{
					Law:             &flowv1.Law{Id: "existing-law-99", Goal: "Old education standard", Division: "education"},
					SimilarityScore: 0.95,
				},
			},
		},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyLib)

	// Conflict analyser that reports a real conflict.
	analyser := &stubConflictAnalyser{
		report: &ConflictReport{
			HasConflicts:      true,
			ConflictingLawIDs: []string{"existing-law-99"},
			RemediationText:   "Revise to avoid overlap with existing-law-99",
		},
	}

	// Build server WITHOUT an explicit spy dispatcher so the real
	// SubscriberRegistry is used (petition subscriber receives events).
	scheme := federationv1.NewTestScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(subscriber, publisher).Build()
	srv := NewFederationServer(k8sClient, testNamespace,
		WithFederationConfig(&flowv1.FederationConfig{
			FederationId:   "fed-001",
			FederationName: "Test Federation",
			RootCaPem:      "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		}),
		WithBootstrapToken("valid-token"),
		WithLibrarianDialer(dialer),
		WithConflictAnalyser(analyser),
	)

	// Start petition-outcome subscriber.
	stream := newMockPetitionOutcomeStream(context.Background())
	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- srv.SubscribePetitionOutcomes(
			&flowv1.SubscribePetitionOutcomesRequest{SubscriberFlowIdentity: "flow-sub"},
			stream,
		)
	}()

	waitForPetitionSubscriber(t, srv, "flow-sub")

	// Publish a law with petition_id (will be rejected due to conflict).
	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       testLawID,
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
		PetitionId:         "petition-rej-001",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("expected rejected publication, got accepted")
	}

	// Disconnect the subscriber.
	stream.cancel()
	<-subErrCh

	// Verify the subscriber received the REJECTED petition outcome event.
	events := stream.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 petition outcome event, got %d", len(events))
	}
	ev := events[0]
	if ev.GetPetitionId() != "petition-rej-001" {
		t.Errorf("petition_id = %q, want %q", ev.GetPetitionId(), "petition-rej-001")
	}
	if ev.GetOutcome() != flowv1.PetitionOutcome_PETITION_OUTCOME_REJECTED {
		t.Errorf("outcome = %v, want PETITION_OUTCOME_REJECTED", ev.GetOutcome())
	}
	if ev.GetRejection() == nil {
		t.Fatal("expected rejection details on REJECTED event")
	}
	if ev.GetRejection().GetReason() != flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_CONFLICT {
		t.Errorf("rejection reason = %v, want CONFLICT", ev.GetRejection().GetReason())
	}
	if len(ev.GetRejection().GetConflictingLawIds()) == 0 {
		t.Error("expected conflicting_law_ids in rejection")
	}
	if ev.GetRejection().GetRemediationText() == "" {
		t.Error("expected remediation_text in rejection")
	}
	if ev.GetPublishedLawId() != "" {
		t.Errorf("published_law_id should be empty on rejection, got %q", ev.GetPublishedLawId())
	}
	if ev.GetResolvedAt() == nil {
		t.Error("resolved_at is nil")
	}
}

func TestSubscribePetitionOutcomes_NoPetitionID_NoEventDispatched(t *testing.T) {
	// When a publication is accepted but has no petition_id, no
	// PetitionOutcomeEvent should be dispatched to the subscriber.
	subscriber := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-sub",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-sub",
			EmbassyEndpoint: "flow-sub-embassy:50059",
			StateRefs:       []string{"state-qld"},
		},
	}
	publisher := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flow-pub-a",
			Namespace: testNamespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    "flow-pub-a",
			EmbassyEndpoint: "flow-pub-a-embassy:50059",
			StateRefs:       []string{"state-qld"},
			PublisherRoles: []federationv1.PublisherRoleSpec{
				{Scope: "education", Level: testLevelState},
			},
		},
	}

	spyLib := &spyLibrarianClient{
		flowID:   "flow-pub-a",
		response: &flowv1.SearchSimilarLawsResponse{Results: nil},
	}
	dialer := newSpyDialer()
	dialer.addClient("flow-pub-a-embassy:50059", spyLib)

	srv := newTestServerWithDialer(t, dialer, subscriber, publisher)

	// Subscribe to petition outcomes.
	stream := newMockPetitionOutcomeStream(context.Background())
	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- srv.SubscribePetitionOutcomes(
			&flowv1.SubscribePetitionOutcomesRequest{SubscriberFlowIdentity: "flow-sub"},
			stream,
		)
	}()

	waitForPetitionSubscriber(t, srv, "flow-sub")

	// Publish a law WITHOUT petition_id.
	resp, err := srv.SubmitPublication(context.Background(), &flowv1.SubmitPublicationRequest{
		Law: &flowv1.Law{
			Id:       testLawID,
			Goal:     "Ensure quality education",
			Division: "education",
			Tier:     flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
		},
		SourceFlowIdentity: "flow-pub-a",
	})
	if err != nil {
		t.Fatalf("SubmitPublication returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected accepted = true, got false; rejection = %v", resp.GetRejection())
	}

	// Disconnect.
	stream.cancel()
	<-subErrCh

	// No petition outcome event should have been dispatched.
	events := stream.getEvents()
	if len(events) != 0 {
		t.Errorf("expected 0 petition outcome events (no petition_id), got %d", len(events))
	}
}

func TestSubscribePetitionOutcomes_EmptyFlowIdentity_InvalidArgument(t *testing.T) {
	srv := newTestServer(t)

	stream := newMockPetitionOutcomeStream(context.Background())
	err := srv.SubscribePetitionOutcomes(
		&flowv1.SubscribePetitionOutcomesRequest{SubscriberFlowIdentity: ""},
		stream,
	)
	if err == nil {
		t.Fatal("expected error for empty subscriber_flow_identity")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
	stream.cancel()
}

func TestSubscribePetitionOutcomes_NonMember_NotFound(t *testing.T) {
	srv := newTestServer(t)

	stream := newMockPetitionOutcomeStream(context.Background())
	err := srv.SubscribePetitionOutcomes(
		&flowv1.SubscribePetitionOutcomesRequest{SubscriberFlowIdentity: "unknown-flow"},
		stream,
	)
	if err == nil {
		t.Fatal("expected error for non-member subscriber")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
	stream.cancel()
}
