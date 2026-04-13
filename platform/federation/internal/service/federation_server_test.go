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
	_ = store.CreateState("state-1", "First State")
	_ = store.CreateState("state-2", "Second State")

	srv := NewFederationServer(store,
		WithFederationConfig(&flowv1.FederationConfig{
			FederationId:   "fed-001",
			FederationName: "Test Federation",
			RootCaPem:      "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		}),
		WithIntermediateCAPem("-----BEGIN CERTIFICATE-----\nintermediate\n-----END CERTIFICATE-----"),
		WithBootstrapToken("valid-token"),
		WithDefaultStates([]string{"state-1"}),
		WithDefaultPublisherRoles([]sqlite.PublisherRole{{Scope: "security", Level: "state"}}),
	)
	return srv
}

// --- JoinFederation ---

func TestJoinFederation_Success(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken:  "valid-token",
		FlowIdentity:    "flow-a",
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
	if len(resp.GetStates()) != 1 || resp.GetStates()[0].GetStateId() != "state-1" {
		t.Errorf("states = %v, want [state-1]", resp.GetStates())
	}
	if len(resp.GetPublisherRoles()) != 1 || resp.GetPublisherRoles()[0].GetScope() != "security" {
		t.Errorf("publisher_roles = %v, want [{security state}]", resp.GetPublisherRoles())
	}
}

func TestJoinFederation_EmptyFlowIdentity(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken: "valid-token",
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
		FlowIdentity:   "flow-a",
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
		BootstrapToken:  "valid-token",
		FlowIdentity:    "flow-a",
		EmbassyEndpoint: "flow-a-embassy:50059",
	})
	if err != nil {
		t.Fatalf("first JoinFederation: %v", err)
	}

	_, err = srv.JoinFederation(ctx, &flowv1.JoinFederationRequest{
		BootstrapToken:  "valid-token",
		FlowIdentity:    "flow-a",
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
		BootstrapToken:  "valid-token",
		FlowIdentity:    "flow-b",
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
		BootstrapToken:  "valid-token",
		FlowIdentity:    "flow-a",
		EmbassyEndpoint: "flow-a:50059",
	})

	resp, err := srv.LeaveFederation(ctx, &flowv1.LeaveFederationRequest{
		FlowIdentity: "flow-a",
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
		BootstrapToken:  "valid-token",
		FlowIdentity:    "flow-a",
		EmbassyEndpoint: "flow-a:50059",
	})

	resp, err := srv.GetMembership(ctx, &flowv1.GetMembershipRequest{
		FlowIdentity: "flow-a",
	})
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}

	m := resp.GetMember()
	if m.GetFlowIdentity() != "flow-a" {
		t.Errorf("flow_identity = %q, want %q", m.GetFlowIdentity(), "flow-a")
	}
	if m.GetEmbassyEndpoint() != "flow-a:50059" {
		t.Errorf("embassy_endpoint = %q, want %q", m.GetEmbassyEndpoint(), "flow-a:50059")
	}
	if len(m.GetStates()) != 1 || m.GetStates()[0].GetStateId() != "state-1" {
		t.Errorf("states = %v, want [state-1]", m.GetStates())
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
