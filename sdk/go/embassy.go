package flow

import (
	flowv1 "github.com/foundry/flow/gen/flow/v1"

	"github.com/foundry/flow/sdk/go/internal/embassy"
)

// Re-exported Embassy/Federation surface. The embassy transfer-protocol and
// federation client helpers plus the server-side Embassy scaffold live in the
// internal/embassy package; these aliases keep the public names stable on the
// flow package for the node binaries (embassy, petition-watcher) that consume
// them.
type (
	// EmbassyOption configures the EmbassyClient.
	EmbassyOption = embassy.EmbassyOption

	// EmbassyClient provides SDK helpers for the Embassy transfer protocol.
	EmbassyClient = embassy.EmbassyClient

	// EmbassyExportStream wraps the Embassy export stream.
	EmbassyExportStream = embassy.EmbassyExportStream

	// EmbassyFlowImportTypeSpec mirrors the flow-authored import type inputs.
	EmbassyFlowImportTypeSpec = embassy.EmbassyFlowImportTypeSpec

	// EmbassyResolvedImportType describes one effective import type.
	EmbassyResolvedImportType = embassy.EmbassyResolvedImportType

	// EmbassyServiceHandler is the server-side Embassy scaffold interface.
	EmbassyServiceHandler = embassy.EmbassyServiceHandler

	// EmbassyPackageStager stages streamed package chunks before materialisation.
	EmbassyPackageStager = embassy.EmbassyPackageStager

	// EmbassyMaterializer materialises a staged import package into a local result.
	EmbassyMaterializer = embassy.EmbassyMaterializer

	// EmbassyStagedPackage holds a manifest and its staged transfer chunks.
	EmbassyStagedPackage = embassy.EmbassyStagedPackage

	// EmbassyTrustSource identifies which trust topology governs a transfer.
	EmbassyTrustSource = embassy.EmbassyTrustSource

	// EmbassyTrustPolicy captures the parity checks shared by federation and treaty exchange.
	EmbassyTrustPolicy = embassy.EmbassyTrustPolicy

	// EmbassyImportRequest is the trust-relevant subset of an inbound transfer.
	EmbassyImportRequest = embassy.EmbassyImportRequest

	// FederationOption configures the FederationClient.
	FederationOption = embassy.FederationOption

	// FederationClient provides SDK helpers for the Federation service RPCs.
	FederationClient = embassy.FederationClient

	// PetitionTarget holds the authority Flow identity and Embassy endpoint.
	PetitionTarget = embassy.PetitionTarget

	// FlowEndpoint represents a discovered federation endpoint.
	FlowEndpoint = embassy.FlowEndpoint

	// LawUpdateWatcher wraps the server-streaming subscription for law updates.
	LawUpdateWatcher = embassy.LawUpdateWatcher

	// PetitionOutcomeWatcher wraps the server-streaming subscription for petition outcomes.
	PetitionOutcomeWatcher = embassy.PetitionOutcomeWatcher
)

const (
	// DefaultEmbassyAddress is the default gRPC endpoint for the Embassy service.
	DefaultEmbassyAddress = embassy.DefaultEmbassyAddress

	// EnvEmbassyAddress overrides the default Embassy gRPC address.
	EnvEmbassyAddress = embassy.EnvEmbassyAddress

	// DefaultFederationAddress is the default gRPC endpoint for the Federation service.
	DefaultFederationAddress = embassy.DefaultFederationAddress

	// EnvFederationAddress overrides the default Federation gRPC address.
	EnvFederationAddress = embassy.EnvFederationAddress

	// EmbassyTrustSourceFederation identifies federation-governed transfers.
	EmbassyTrustSourceFederation = embassy.EmbassyTrustSourceFederation

	// EmbassyTrustSourceTreaty identifies treaty-governed transfers.
	EmbassyTrustSourceTreaty = embassy.EmbassyTrustSourceTreaty
)

// NewEmbassyClient connects to the Embassy service.
func NewEmbassyClient(opts ...EmbassyOption) (*EmbassyClient, error) {
	return embassy.NewEmbassyClient(opts...)
}

// NewEmbassyClientForTest creates an EmbassyClient connected to the given
// address. Named to make misuse obvious — this is a cross-module test seam
// used by the embassy node's export tests against a spy server.
func NewEmbassyClientForTest(address string) (*EmbassyClient, error) {
	return embassy.NewEmbassyClientForTest(address)
}

// WithEmbassyAddress overrides the default Embassy gRPC address.
func WithEmbassyAddress(addr string) EmbassyOption {
	return embassy.WithEmbassyAddress(addr)
}

// NewEmbassyServer adapts an EmbassyServiceHandler to the generated gRPC server.
func NewEmbassyServer(handler EmbassyServiceHandler) flowv1.EmbassyServiceServer {
	return embassy.NewEmbassyServer(handler)
}

// ResolveEmbassyImportType resolves an import type against the merged effective
// namespace of built-in system import types and flow-authored import types.
func ResolveEmbassyImportType(
	name string,
	system map[string]EmbassyResolvedImportType,
	flowImportTypes map[string]EmbassyFlowImportTypeSpec,
) (EmbassyResolvedImportType, bool) {
	return embassy.ResolveEmbassyImportType(name, system, flowImportTypes)
}

// ResolveEmbassyTrustSource chooses the active trust topology.
func ResolveEmbassyTrustSource(hasTreaty bool) EmbassyTrustSource {
	return embassy.ResolveEmbassyTrustSource(hasTreaty)
}

// ValidateEmbassyTrustPolicy enforces import-type, subject, and bundle-size
// parity checks across federation and treaty exchange.
func ValidateEmbassyTrustPolicy(
	policy EmbassyTrustPolicy,
	req EmbassyImportRequest,
	system map[string]EmbassyResolvedImportType,
	flowImportTypes map[string]EmbassyFlowImportTypeSpec,
) error {
	return embassy.ValidateEmbassyTrustPolicy(policy, req, system, flowImportTypes)
}

// NewFederationClient connects to the Federation service.
func NewFederationClient(opts ...FederationOption) (*FederationClient, error) {
	return embassy.NewFederationClient(opts...)
}

// NewFederationClientForTest creates a FederationClient connected to the
// given address. Named to make misuse obvious — this is a cross-module test
// seam used by node packages (petition-watcher) to test against spy servers.
func NewFederationClientForTest(address string) (*FederationClient, error) {
	return embassy.NewFederationClientForTest(address)
}
