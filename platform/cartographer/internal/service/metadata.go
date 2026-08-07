// Package service implements the Cartographer gRPC service with capability
// verification, transaction management, and error mapping.
package service

// Capability attestation metadata keys carried in gRPC metadata.
// Each consuming module (sidecar proxy, operator proxy) duplicates these
// string constants to avoid cross-module import coupling.
//
// ponytail: If the set grows or the wire protocol stabilises, extract
// to pkg/metadata/ and import from all consuming modules.
const (
	MetadataKeyCapabilities          = "x-flow-capabilities"
	MetadataKeyCapabilitiesSignature = "x-flow-capabilities-signature"
	MetadataKeyCapabilitiesSignedAt  = "x-flow-capabilities-signed-at"
	MetadataKeyCapabilitiesSignedBy  = "x-flow-capabilities-signed-by"
	// MetadataKeyEntityTypes carries entity type labels extracted from
	// Cypher queries by the SDK. Passed as gRPC metadata values keyed by
	// "entity_types" (SPEC R3). The Cartographer uses this to perform
	// authoritative type-specific capability checking.
	MetadataKeyEntityTypes = "entity_types"
)
