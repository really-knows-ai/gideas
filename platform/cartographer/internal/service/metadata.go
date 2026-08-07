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
)
