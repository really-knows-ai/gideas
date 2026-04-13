// Package service implements the FederationService gRPC server.
//
// The Federation service is the control-plane authority for Flow federations.
// It manages membership, endpoint discovery, authority publisher roles,
// published law distribution, and petition-outcome events.
package service

import (
	"context"
	"log/slog"
	"strings"

	"github.com/gideas/flow/federation/internal/store/sqlite"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// FederationServer implements flowv1.FederationServiceServer backed by a
// SQLite store.
type FederationServer struct {
	flowv1.UnimplementedFederationServiceServer
	store             *sqlite.Store
	config            *flowv1.FederationConfig
	intermediateCAPem string
	bootstrapToken    string
	defaultStates     []string
	defaultRoles      []sqlite.PublisherRole
}

// FederationOption configures a FederationServer.
type FederationOption func(*FederationServer)

// WithFederationConfig sets the federation-wide config returned to joining members.
func WithFederationConfig(cfg *flowv1.FederationConfig) FederationOption {
	return func(s *FederationServer) { s.config = cfg }
}

// WithIntermediateCAPem sets the intermediate CA PEM returned on join.
func WithIntermediateCAPem(pem string) FederationOption {
	return func(s *FederationServer) { s.intermediateCAPem = pem }
}

// WithBootstrapToken sets the expected bootstrap token for authentication.
func WithBootstrapToken(token string) FederationOption {
	return func(s *FederationServer) { s.bootstrapToken = token }
}

// WithDefaultStates sets the state IDs assigned to new members by default.
func WithDefaultStates(stateIDs []string) FederationOption {
	return func(s *FederationServer) { s.defaultStates = stateIDs }
}

// WithDefaultPublisherRoles sets the publisher roles assigned to new members.
func WithDefaultPublisherRoles(roles []sqlite.PublisherRole) FederationOption {
	return func(s *FederationServer) { s.defaultRoles = roles }
}

// NewFederationServer returns a FederationServer backed by the given store.
func NewFederationServer(store *sqlite.Store, opts ...FederationOption) *FederationServer {
	srv := &FederationServer{
		store: store,
	}
	for _, o := range opts {
		o(srv)
	}
	return srv
}

// JoinFederation adds a Flow to the federation. Validates the bootstrap token
// and flow identity, persists membership, and returns federation config, CA,
// state assignments, and publisher roles.
func (s *FederationServer) JoinFederation(
	ctx context.Context, req *flowv1.JoinFederationRequest,
) (*flowv1.JoinFederationResponse, error) {
	if strings.TrimSpace(req.GetFlowIdentity()) == "" {
		return nil, status.Error(codes.InvalidArgument, "flow_identity is required")
	}
	if strings.TrimSpace(req.GetBootstrapToken()) == "" {
		return nil, status.Error(codes.InvalidArgument, "bootstrap_token is required")
	}

	// Simple token validation. A production implementation would use a
	// more sophisticated authentication scheme.
	if s.bootstrapToken != "" && req.GetBootstrapToken() != s.bootstrapToken {
		return nil, status.Error(codes.PermissionDenied, "invalid bootstrap token")
	}

	err := s.store.AddMember(req.GetFlowIdentity(), req.GetEmbassyEndpoint(), s.defaultStates, s.defaultRoles)
	if err != nil {
		// Check for duplicate member.
		if strings.Contains(err.Error(), "UNIQUE constraint") || strings.Contains(err.Error(), "insert member") {
			return nil, status.Errorf(codes.AlreadyExists, "flow %q already joined", req.GetFlowIdentity())
		}
		return nil, status.Errorf(codes.Internal, "add member: %v", err)
	}

	slog.Info("Flow joined federation", "flow_identity", req.GetFlowIdentity())

	// Resolve state details for the response.
	var states []*flowv1.State
	for _, sid := range s.defaultStates {
		st, err := s.store.GetState(sid)
		if err != nil {
			slog.Warn("State not found in store", "state_id", sid, "error", err)
			continue
		}
		states = append(states, &flowv1.State{StateId: st.ID, Name: st.Name})
	}

	var roles []*flowv1.PublisherRole
	for _, r := range s.defaultRoles {
		roles = append(roles, &flowv1.PublisherRole{Scope: r.Scope, Level: r.Level})
	}

	return &flowv1.JoinFederationResponse{
		IntermediateCaPem: s.intermediateCAPem,
		FederationConfig:  s.config,
		States:            states,
		PublisherRoles:    roles,
	}, nil
}

// LeaveFederation removes a Flow from the federation.
func (s *FederationServer) LeaveFederation(
	ctx context.Context, req *flowv1.LeaveFederationRequest,
) (*flowv1.LeaveFederationResponse, error) {
	if strings.TrimSpace(req.GetFlowIdentity()) == "" {
		return nil, status.Error(codes.InvalidArgument, "flow_identity is required")
	}

	err := s.store.RemoveMember(req.GetFlowIdentity())
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "member %q not found", req.GetFlowIdentity())
		}
		return nil, status.Errorf(codes.Internal, "remove member: %v", err)
	}

	slog.Info("Flow left federation", "flow_identity", req.GetFlowIdentity())
	return &flowv1.LeaveFederationResponse{Acknowledged: true}, nil
}

// GetMembership returns the current membership snapshot for a Flow.
func (s *FederationServer) GetMembership(
	ctx context.Context, req *flowv1.GetMembershipRequest,
) (*flowv1.GetMembershipResponse, error) {
	if strings.TrimSpace(req.GetFlowIdentity()) == "" {
		return nil, status.Error(codes.InvalidArgument, "flow_identity is required")
	}

	m, err := s.store.GetMember(req.GetFlowIdentity())
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "member %q not found", req.GetFlowIdentity())
		}
		return nil, status.Errorf(codes.Internal, "get member: %v", err)
	}

	member := s.storeMemberToProto(m)
	return &flowv1.GetMembershipResponse{Member: member}, nil
}

// storeMemberToProto converts a store Member to a proto FederationMember.
func (s *FederationServer) storeMemberToProto(m *sqlite.Member) *flowv1.FederationMember {
	var states []*flowv1.State
	for _, sid := range m.StateIDs {
		st, err := s.store.GetState(sid)
		if err != nil {
			// State not found in store, include ID only.
			states = append(states, &flowv1.State{StateId: sid})
			continue
		}
		states = append(states, &flowv1.State{StateId: st.ID, Name: st.Name})
	}

	roles := make([]*flowv1.PublisherRole, 0, len(m.PublisherRoles))
	for _, r := range m.PublisherRoles {
		roles = append(roles, &flowv1.PublisherRole{Scope: r.Scope, Level: r.Level})
	}

	return &flowv1.FederationMember{
		FlowIdentity:    m.FlowIdentity,
		EmbassyEndpoint: m.EmbassyEndpoint,
		States:          states,
		PublisherRoles:  roles,
	}
}
