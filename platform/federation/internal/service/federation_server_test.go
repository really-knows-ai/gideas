package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/gideas/flow/federation/internal/store/sqlite"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	testFlowA    = "flow-a"
	testFlowB    = "flow-b"
	testState1   = "state-1"
	testState2   = "state-2"
	testToken    = "valid-token"
	testEndpoint = "flow-a:50059"
)

func newTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	s, err := sqlite.New(dsn)
	if err != nil {
		t.Fatalf("newTestStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newTestServer(t *testing.T) *FederationServer {
	t.Helper()
	store := newTestStore(t)

	// Seed federation config and states.
	_ = store.CreateState(testState1, "First State")
	_ = store.CreateState(testState2, "Second State")

	srv := NewFederationServer(store,
		WithFederationConfig(&flowv1.FederationConfig{
			FederationId:   "fed-001",
			FederationName: "Test Federation",
			RootCaPem:      "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		}),
		WithIntermediateCAPem("-----BEGIN CERTIFICATE-----\nintermediate\n-----END CERTIFICATE-----"),
		WithBootstrapToken(testToken),
		WithDefaultStates([]string{testState1}),
		WithDefaultPublisherRoles([]sqlite.PublisherRole{{Scope: "security", Level: "state"}}),
	)
	return srv
}

// --- JoinFederation ---

func TestJoinFederation_Success(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken:  testToken,
		FlowIdentity:    testFlowA,
		EmbassyEndpoint: "flow-a-embassy:50059",
	})
	if err != nil {
		t.Fatalf("JoinFederation: %v", err)
	}

	if resp.GetFederationConfig().GetFederationId() != "fed-001" {
		t.Errorf("federation_id = %q, want %q", resp.GetFederationConfig().GetFederationId(), "fed-001")
	}
	if resp.GetIntermediateCaPem() == "" {
		t.Error("intermediate_ca_pem is empty")
	}
	if len(resp.GetStates()) != 1 || resp.GetStates()[0].GetStateId() != testState1 {
		t.Errorf("states = %v, want [%s]", resp.GetStates(), testState1)
	}
	if len(resp.GetPublisherRoles()) != 1 || resp.GetPublisherRoles()[0].GetScope() != "security" {
		t.Errorf("publisher_roles = %v, want [{security state}]", resp.GetPublisherRoles())
	}
}

func TestJoinFederation_EmptyFlowIdentity(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken: testToken,
		FlowIdentity:   "",
	})
	if err == nil {
		t.Fatal("expected error for empty flow identity")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestJoinFederation_EmptyBootstrapToken(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken: "",
		FlowIdentity:   testFlowA,
	})
	if err == nil {
		t.Fatal("expected error for empty bootstrap token")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestJoinFederation_AlreadyJoined(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken:  testToken,
		FlowIdentity:    testFlowA,
		EmbassyEndpoint: "flow-a-embassy:50059",
	})
	if err != nil {
		t.Fatalf("first JoinFederation: %v", err)
	}

	_, err = srv.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken:  testToken,
		FlowIdentity:    testFlowA,
		EmbassyEndpoint: "flow-a-embassy:50059",
	})
	if err == nil {
		t.Fatal("expected error for duplicate join")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.AlreadyExists {
		t.Errorf("code = %v, want AlreadyExists", status.Code(err))
	}
}

func TestJoinFederation_ResponseIncludesStatesAndRoles(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken:  testToken,
		FlowIdentity:    testFlowB,
		EmbassyEndpoint: "flow-b-embassy:50059",
	})
	if err != nil {
		t.Fatalf("JoinFederation: %v", err)
	}

	// Verify response includes assigned states and publisher roles.
	if len(resp.GetStates()) == 0 {
		t.Error("expected at least one state in response")
	}
	for _, s := range resp.GetStates() {
		if s.GetStateId() == "" {
			t.Error("state has empty state_id")
		}
		if s.GetName() == "" {
			t.Error("state has empty name")
		}
	}
	if len(resp.GetPublisherRoles()) == 0 {
		t.Error("expected at least one publisher role in response")
	}
}

// --- LeaveFederation ---

func TestLeaveFederation_Success(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Join first.
	_, _ = srv.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken:  testToken,
		FlowIdentity:    testFlowA,
		EmbassyEndpoint: testEndpoint,
	})

	resp, err := srv.LeaveFederation(ctx, &flowv1.LeaveFederationRequest{
		FlowIdentity: testFlowA,
	})
	if err != nil {
		t.Fatalf("LeaveFederation: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Error("expected acknowledged = true")
	}
}

func TestLeaveFederation_NonMember(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.LeaveFederation(ctx, &flowv1.LeaveFederationRequest{
		FlowIdentity: "nonexistent",
	})
	if err == nil {
		t.Fatal("expected error for non-member leave")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Errorf("code = %v, want NotFound", status.Code(err))
	}
}

// --- GetMembership ---

func TestGetMembership_Success(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Join first.
	_, _ = srv.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken:  testToken,
		FlowIdentity:    testFlowA,
		EmbassyEndpoint: testEndpoint,
	})

	resp, err := srv.GetMembership(ctx, &flowv1.GetMembershipRequest{
		FlowIdentity: testFlowA,
	})
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}

	m := resp.GetMember()
	if m.GetFlowIdentity() != testFlowA {
		t.Errorf("flow_identity = %q, want %q", m.GetFlowIdentity(), testFlowA)
	}
	if m.GetEmbassyEndpoint() != testEndpoint {
		t.Errorf("embassy_endpoint = %q, want %q", m.GetEmbassyEndpoint(), testEndpoint)
	}
	if len(m.GetStates()) != 1 || m.GetStates()[0].GetStateId() != testState1 {
		t.Errorf("states = %v, want [%s]", m.GetStates(), testState1)
	}
	if len(m.GetPublisherRoles()) != 1 || m.GetPublisherRoles()[0].GetScope() != "security" {
		t.Errorf("publisher_roles = %v, want [{security state}]", m.GetPublisherRoles())
	}
}

func TestGetMembership_NonMember(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.GetMembership(ctx, &flowv1.GetMembershipRequest{
		FlowIdentity: "nonexistent",
	})
	if err == nil {
		t.Fatal("expected error for non-member")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Errorf("code = %v, want NotFound", status.Code(err))
	}
}

// --- DiscoverEndpoints ---

func TestDiscoverEndpoints_NoFilter(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Join two members.
	_, _ = srv.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken:  testToken,
		FlowIdentity:    testFlowA,
		EmbassyEndpoint: testEndpoint,
	})
	_, _ = srv.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken:  testToken,
		FlowIdentity:    testFlowB,
		EmbassyEndpoint: "flow-b:50059",
	})

	resp, err := srv.DiscoverEndpoints(ctx, &flowv1.DiscoverEndpointsRequest{})
	if err != nil {
		t.Fatalf("DiscoverEndpoints: %v", err)
	}
	if len(resp.GetEndpoints()) != 2 {
		t.Errorf("endpoints count = %d, want 2", len(resp.GetEndpoints()))
	}
}

func TestDiscoverEndpoints_StateFilter(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// newTestServer assigns default state "state-1" to all joining members.
	_, _ = srv.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken:  testToken,
		FlowIdentity:    testFlowA,
		EmbassyEndpoint: testEndpoint,
	})

	// Add a second member manually to a different state.
	store := srv.store
	_ = store.AddMember("flow-other", "flow-other:50059", []string{testState2}, nil)

	resp, err := srv.DiscoverEndpoints(ctx, &flowv1.DiscoverEndpointsRequest{
		StateFilter: testState1,
	})
	if err != nil {
		t.Fatalf("DiscoverEndpoints %s: %v", testState1, err)
	}
	if len(resp.GetEndpoints()) != 1 {
		t.Errorf("endpoints count = %d, want 1", len(resp.GetEndpoints()))
	}
	if resp.GetEndpoints()[0].GetFlowIdentity() != testFlowA {
		t.Errorf("flow_identity = %q, want %q", resp.GetEndpoints()[0].GetFlowIdentity(), testFlowA)
	}
}

func TestDiscoverEndpoints_EndpointFields(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, _ = srv.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken:  testToken,
		FlowIdentity:    testFlowA,
		EmbassyEndpoint: testEndpoint,
	})

	resp, err := srv.DiscoverEndpoints(ctx, &flowv1.DiscoverEndpointsRequest{})
	if err != nil {
		t.Fatalf("DiscoverEndpoints: %v", err)
	}
	if len(resp.GetEndpoints()) != 1 {
		t.Fatalf("endpoints count = %d, want 1", len(resp.GetEndpoints()))
	}

	ep := resp.GetEndpoints()[0]
	if ep.GetFlowIdentity() != testFlowA {
		t.Errorf("flow_identity = %q, want %q", ep.GetFlowIdentity(), testFlowA)
	}
	if ep.GetEmbassyAddress() != testEndpoint {
		t.Errorf("embassy_address = %q, want %q", ep.GetEmbassyAddress(), testEndpoint)
	}
	if len(ep.GetStateIds()) != 1 || ep.GetStateIds()[0] != testState1 {
		t.Errorf("state_ids = %v, want [%s]", ep.GetStateIds(), testState1)
	}
}

func TestDiscoverEndpoints_EmptyFederation(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.DiscoverEndpoints(ctx, &flowv1.DiscoverEndpointsRequest{})
	if err != nil {
		t.Fatalf("DiscoverEndpoints: %v", err)
	}
	if len(resp.GetEndpoints()) != 0 {
		t.Errorf("endpoints count = %d, want 0", len(resp.GetEndpoints()))
	}
}

// --- GetPetitionTarget ---

func TestGetPetitionTarget_ValidScope(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Join a member; newTestServer assigns publisher role {Scope: "security", Level: "state"}.
	_, _ = srv.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken:  testToken,
		FlowIdentity:    testFlowA,
		EmbassyEndpoint: testEndpoint,
	})

	resp, err := srv.GetPetitionTarget(ctx, &flowv1.GetPetitionTargetRequest{
		Scope: "security",
	})
	if err != nil {
		t.Fatalf("GetPetitionTarget: %v", err)
	}
	if resp.GetAuthorityFlowIdentity() != testFlowA {
		t.Errorf("authority_flow_identity = %q, want %q", resp.GetAuthorityFlowIdentity(), testFlowA)
	}
	if resp.GetEmbassyEndpoint() != testEndpoint {
		t.Errorf("embassy_endpoint = %q, want %q", resp.GetEmbassyEndpoint(), testEndpoint)
	}
}

func TestGetPetitionTarget_UnknownScope(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Join a member, but it only has "security" scope.
	_, _ = srv.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken:  testToken,
		FlowIdentity:    testFlowA,
		EmbassyEndpoint: testEndpoint,
	})

	_, err := srv.GetPetitionTarget(ctx, &flowv1.GetPetitionTargetRequest{
		Scope: "unknown-scope",
	})
	if err == nil {
		t.Fatal("expected error for unknown scope")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Errorf("code = %v, want NotFound", status.Code(err))
	}
}

func TestGetPetitionTarget_AuthorityLeftFederation(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Join and then leave.
	_, _ = srv.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken:  testToken,
		FlowIdentity:    testFlowA,
		EmbassyEndpoint: testEndpoint,
	})
	_, _ = srv.LeaveFederation(ctx, &flowv1.LeaveFederationRequest{
		FlowIdentity: testFlowA,
	})

	_, err := srv.GetPetitionTarget(ctx, &flowv1.GetPetitionTargetRequest{
		Scope: "security",
	})
	if err == nil {
		t.Fatal("expected error after authority left federation")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Errorf("code = %v, want NotFound", status.Code(err))
	}
}

func TestGetPetitionTarget_StateLevelScope(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Join with state-level role (default from newTestServer: "security" / "state").
	_, _ = srv.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken:  testToken,
		FlowIdentity:    testFlowA,
		EmbassyEndpoint: testEndpoint,
	})

	resp, err := srv.GetPetitionTarget(ctx, &flowv1.GetPetitionTargetRequest{
		Scope: "security",
	})
	if err != nil {
		t.Fatalf("GetPetitionTarget: %v", err)
	}
	if resp.GetAuthorityFlowIdentity() != testFlowA {
		t.Errorf("authority_flow_identity = %q, want %q", resp.GetAuthorityFlowIdentity(), testFlowA)
	}
}

func TestGetPetitionTarget_FederationLevelScope(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Add a member with federation-level role directly to the store.
	_ = srv.store.AddMember("flow-fed-auth", "flow-fed-auth:50059", nil,
		[]sqlite.PublisherRole{{Scope: "architecture", Level: "federation"}})

	resp, err := srv.GetPetitionTarget(ctx, &flowv1.GetPetitionTargetRequest{
		Scope: "architecture",
	})
	if err != nil {
		t.Fatalf("GetPetitionTarget: %v", err)
	}
	if resp.GetAuthorityFlowIdentity() != "flow-fed-auth" {
		t.Errorf("authority_flow_identity = %q, want %q", resp.GetAuthorityFlowIdentity(), "flow-fed-auth")
	}
	if resp.GetEmbassyEndpoint() != "flow-fed-auth:50059" {
		t.Errorf("embassy_endpoint = %q, want %q", resp.GetEmbassyEndpoint(), "flow-fed-auth:50059")
	}
}
