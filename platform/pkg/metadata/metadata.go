// Package metadata defines the gRPC wire metadata keys shared by every Flow
// service. These keys are contract-defining: the Sidecar injects them, the
// Cartographer verifies and enforces them, the Operator proxy routes on them,
// and flowctl forwards them. They are defined once here and imported by every
// consuming module — never re-declared as bare literals in sibling modules.
package metadata

// gRPC metadata keys for the Sidecar-injected flow identity, capability
// attestation, and operator proxy routing headers.
const (
	// MetadataKeyNamespace is the gRPC metadata key for the namespace
	// (flow identity boundary).
	MetadataKeyNamespace = "x-flow-namespace"

	// MetadataKeyWorkitemID is the gRPC metadata key for the workitem identity.
	MetadataKeyWorkitemID = "x-flow-workitem-id"

	// MetadataKeyNodeID is the gRPC metadata key for the node identity.
	MetadataKeyNodeID = "x-flow-node-id"

	// MetadataKeyGraphName is the gRPC metadata key carrying the FoundryGraph
	// resource name routed by the operator proxy (SPEC R1 singleton).
	MetadataKeyGraphName = "x-flow-graph-name"

	// MetadataKeyAuthorization is the gRPC metadata key carrying the caller's
	// bearer token for the operator proxy's TokenReview (SPEC Graph Export
	// Flow).
	MetadataKeyAuthorization = "authorization"

	// MetadataKeyCapabilities is the gRPC metadata key carrying the
	// comma-separated capability grants for the node. Injected by the Sidecar
	// from the FLOW_CAPABILITIES environment variable (set by the Operator
	// during pod construction). Owning services read this header to enforce
	// capability-gated access.
	MetadataKeyCapabilities = "x-flow-capabilities"

	// MetadataKeyCapabilitiesSignature is the gRPC metadata key carrying the
	// base64-encoded Ed25519 signature over
	// "{x-flow-capabilities}|{x-flow-capabilities-signed-at}" (SPEC R3 /
	// Capability Authorisation Chain).
	MetadataKeyCapabilitiesSignature = "x-flow-capabilities-signature"

	// MetadataKeyCapabilitiesSignedBy is the gRPC metadata key selecting the
	// verification key at the Cartographer ("sidecar" or "operator").
	MetadataKeyCapabilitiesSignedBy = "x-flow-capabilities-signed-by"

	// MetadataKeyCapabilitiesSignedAt is the gRPC metadata key carrying the
	// Unix timestamp (seconds) used for the anti-replay staleness check.
	MetadataKeyCapabilitiesSignedAt = "x-flow-capabilities-signed-at"
)
