// Package service implements the FederationService gRPC server.
//
// The Federation service is the control-plane authority for Flow federations.
// It manages membership, endpoint discovery, authority publisher roles,
// published law distribution, and petition-outcome events.
//
// All persistent state lives in Kubernetes CRDs (FederationMember,
// FederationState) backed by etcd -- no SQLite.
package service

import (
	"context"
	"fmt"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	federationv1 "github.com/gideas/flow/federation/api/v1"
)

// FederationServer implements flowv1.FederationServiceServer backed by
// Kubernetes CRDs via a controller-runtime client.
type FederationServer struct {
	flowv1.UnimplementedFederationServiceServer
	k8sClient      client.Client
	namespace      string
	config         *flowv1.FederationConfig
	bootstrapToken string
}

// FederationOption configures a FederationServer.
type FederationOption func(*FederationServer)

// WithFederationConfig sets the federation-wide config returned to joining members.
func WithFederationConfig(cfg *flowv1.FederationConfig) FederationOption {
	return func(s *FederationServer) { s.config = cfg }
}

// WithBootstrapToken sets the expected bootstrap token for authentication.
func WithBootstrapToken(token string) FederationOption {
	return func(s *FederationServer) { s.bootstrapToken = token }
}

// NewFederationServer returns a FederationServer backed by the given
// Kubernetes client.
func NewFederationServer(k8sClient client.Client, namespace string, opts ...FederationOption) *FederationServer {
	srv := &FederationServer{
		k8sClient: k8sClient,
		namespace: namespace,
	}
	for _, o := range opts {
		o(srv)
	}
	return srv
}

// JoinFederation creates a FederationMember CR for the joining Flow and
// returns the federation config, CA, available states, and assigned roles.
func (s *FederationServer) JoinFederation(
	ctx context.Context,
	req *flowv1.JoinFederationRequest,
) (*flowv1.JoinFederationResponse, error) {
	// Validate inputs.
	if req.GetBootstrapToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "bootstrap_token is required")
	}
	if req.GetFlowIdentity() == "" {
		return nil, status.Error(codes.InvalidArgument, "flow_identity is required")
	}
	if req.GetEmbassyEndpoint() == "" {
		return nil, status.Error(codes.InvalidArgument, "embassy_endpoint is required")
	}

	// Authenticate the bootstrap token.
	if req.GetBootstrapToken() != s.bootstrapToken {
		return nil, status.Error(codes.PermissionDenied, "invalid bootstrap token")
	}

	// Build the FederationMember CR. Name is derived from the flow identity
	// using a K8s-safe transformation (lowercase, replace non-alphanumeric
	// with hyphens).
	memberName := toK8sName(req.GetFlowIdentity())
	member := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      memberName,
			Namespace: s.namespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    req.GetFlowIdentity(),
			EmbassyEndpoint: req.GetEmbassyEndpoint(),
		},
	}

	// Create the CR. If it already exists, return AlreadyExists.
	if err := s.k8sClient.Create(ctx, member); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil, status.Errorf(codes.AlreadyExists, "flow %q is already a federation member", req.GetFlowIdentity())
		}
		return nil, status.Errorf(codes.Internal, "failed to create FederationMember: %v", err)
	}

	// Read all FederationState CRs to populate the response.
	states, err := s.listStates(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list federation states: %v", err)
	}

	// Build publisher roles from the member spec (initially empty for new
	// members -- roles are assigned by the federation admin via CR update).
	roles := toProtoPublisherRoles(member.Spec.PublisherRoles)

	return &flowv1.JoinFederationResponse{
		IntermediateCaPem: s.config.GetRootCaPem(),
		FederationConfig:  s.config,
		States:            states,
		PublisherRoles:    roles,
	}, nil
}

// listStates retrieves all FederationState CRs in the namespace and
// converts them to proto State messages.
func (s *FederationServer) listStates(ctx context.Context) ([]*flowv1.State, error) {
	var stateList federationv1.FederationStateList
	if err := s.k8sClient.List(ctx, &stateList, client.InNamespace(s.namespace)); err != nil {
		return nil, fmt.Errorf("list FederationStates: %w", err)
	}

	result := make([]*flowv1.State, 0, len(stateList.Items))
	for i := range stateList.Items {
		st := &stateList.Items[i]
		result = append(result, &flowv1.State{
			StateId: st.Name,
			Name:    st.Spec.Name,
		})
	}
	return result, nil
}

// toProtoPublisherRoles converts CRD publisher role specs to proto messages.
func toProtoPublisherRoles(specs []federationv1.PublisherRoleSpec) []*flowv1.PublisherRole {
	if len(specs) == 0 {
		return nil
	}
	result := make([]*flowv1.PublisherRole, len(specs))
	for i, spec := range specs {
		result[i] = &flowv1.PublisherRole{
			Scope: spec.Scope,
			Level: spec.Level,
		}
	}
	return result
}

// toK8sName converts a flow identity string to a valid K8s resource name.
// It lowercases the string and replaces non-alphanumeric characters (except
// hyphens and dots) with hyphens.
func toK8sName(identity string) string {
	s := strings.ToLower(identity)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}
