// Package service implements the FederationService gRPC server.
//
// The Federation service is the control-plane authority for Flow federations.
// It manages membership, endpoint discovery, authority publisher roles,
// published law distribution, and petition-outcome events.
//
// This file is a scaffold placeholder. The full CRD-backed implementation
// is wired in slice 13.6.6.
package service

import (
	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
