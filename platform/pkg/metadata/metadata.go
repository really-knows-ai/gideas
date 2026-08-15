// Package metadata defines the wire-format contract values shared by every
// Flow service: the gRPC metadata keys the Sidecar injects and the Operator
// proxy routes on, the signer identities carried in the capability
// attestation header, the ExportGraph wire-format identifiers, and the
// conventional singleton FoundryGraph name. These values are
// contract-defining: they are defined once here and imported by every
// consuming module — never re-declared as bare literals in sibling modules.
package metadata

import (
	"context"
	"strings"

	"google.golang.org/grpc/metadata"
)

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

	// MetadataKeyEntityType is the gRPC metadata key the SDK attaches to
	// UpdateEntity/DeleteEntity/CreateEdge/DeleteEdge to carry the entity type
	// it resolved from its local ID-to-type mapping (SPEC R3 / Capability
	// Authorisation Chain). The Sidecar's CartographerProxy reads it to select
	// the mode-1 specific-type capability gate (or the mode-2 wildcard
	// best-effort check when the value is "*" or absent).
	MetadataKeyEntityType = "entity_type"
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

// NormalizeCapabilities splits a comma-separated capability grant string into
// its entries, trimming whitespace around each entry and dropping empty
// entries. It is the single source of truth for capability-string
// normalization (SPEC R3 / Capability Authorisation Chain): the Cartographer's
// ingress verifier, the Sidecar's nodeCapabilities, and the SDK's
// CheckCapability must parse the x-flow-capabilities wire value identically,
// so they import this helper instead of re-implementing the split/trim/drop
// loop per module — duplicated copies of the same contract surface silently
// diverge. Returns nil when no entries remain.
func NormalizeCapabilities(caps string) []string {
	var entries []string
	for cap := range strings.SplitSeq(caps, ",") {
		if cap = strings.TrimSpace(cap); cap != "" {
			entries = append(entries, cap)
		}
	}
	return entries
}

// MetadataValue reads a single value from the incoming gRPC metadata for the
// given key, returning "" when the context carries no metadata or the key is
// absent. It is the single shared lookup for the flow-identity metadata keys
// above (SPEC R3 / flow identity boundary): every service reads the
// Sidecar-injected x-flow-* values identically, so they import this helper
// instead of re-implementing the FromIncomingContext/Get lookup per module —
// duplicated copies of the same contract surface silently diverge.
func MetadataValue(ctx context.Context, key string) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(key)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}
