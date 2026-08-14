// Package metadata defines the wire-format contract values shared by every
// Flow service: the gRPC metadata keys the Sidecar injects and the Operator
// proxy routes on, the signer identities carried in the capability
// attestation header, the ExportGraph wire-format identifiers, and the
// conventional singleton FoundryGraph name. These values are
// contract-defining: they are defined once here and imported by every
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
	// verification key at the Cartographer (SignerIdentitySidecar or
	// SignerIdentityOperator).
	MetadataKeyCapabilitiesSignedBy = "x-flow-capabilities-signed-by"

	// MetadataKeyCapabilitiesSignedAt is the gRPC metadata key carrying the
	// Unix timestamp (seconds) used for the anti-replay staleness check.
	MetadataKeyCapabilitiesSignedAt = "x-flow-capabilities-signed-at"
)

// Signer identity values carried by the x-flow-capabilities-signed-by
// metadata key (SPEC R3 / Capability Authorisation Chain): the value selects
// the verification key at the Cartographer.
const (
	// SignerIdentityOperator identifies capabilities signed by the Operator's
	// signing key (the operator proxy path).
	SignerIdentityOperator = "operator"

	// SignerIdentitySidecar identifies capabilities signed by the Sidecar's
	// shared signing key.
	SignerIdentitySidecar = "sidecar"
)

// ExportGraph wire-format identifiers (SPEC R2 ExportGraph). The format is a
// plain string on the wire, so both the server (Cartographer) and the CLI
// (flowctl) reference this shared set.
const (
	// ExportFormatJSON selects JSON serialisation of the graph.
	ExportFormatJSON = "json"

	// ExportFormatGraphML selects GraphML serialisation of the graph.
	ExportFormatGraphML = "graphml"
)

// DefaultGraphName is the conventional singleton FoundryGraph resource name
// (SPEC R1: the singleton is conventionally named "flow-graph"; other
// components reference this conventional name).
const DefaultGraphName = "flow-graph"
