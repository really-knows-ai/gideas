package flow

import (
	"context"
	"path/filepath"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// StampCapability represents a parsed STAMP:artefact/<kind>/<stamp> or
// ATTEST:artefact/<kind>/<stamp> capability.
type StampCapability struct {
	// GovernedArtefact is the governed artefact kind (e.g., "haiku").
	GovernedArtefact string
	// StampName is the stamp name (e.g., "review").
	StampName string
}

const (
	stampCapabilityPrefix  = "STAMP:artefact/"
	attestCapabilityPrefix = "ATTEST:artefact/"
)

// ParseStampCapability parses a single STAMP:artefact/<kind>/<stamp> or
// ATTEST:artefact/<kind>/<stamp> capability string. Returns ok=false if the
// string does not match the expected pattern.
func ParseStampCapability(cap string) (StampCapability, bool) {
	var rest string
	switch {
	case strings.HasPrefix(cap, stampCapabilityPrefix):
		rest = cap[len(stampCapabilityPrefix):]
	case strings.HasPrefix(cap, attestCapabilityPrefix):
		rest = cap[len(attestCapabilityPrefix):]
	default:
		return StampCapability{}, false
	}
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return StampCapability{}, false
	}
	return StampCapability{
		GovernedArtefact: parts[0],
		StampName:        parts[1],
	}, true
}

// ParseStampCapabilities extracts all STAMP:artefact capabilities from a
// capability list, skipping non-matching entries.
func ParseStampCapabilities(capabilities []string) []StampCapability {
	var stamps []StampCapability
	for _, cap := range capabilities {
		if sc, ok := ParseStampCapability(cap); ok {
			stamps = append(stamps, sc)
		}
	}
	return stamps
}

// MatchCapability returns true if cap matches the required pattern.
// Wildcard rules:
//   - * matches any sequence of characters within a single /-delimited segment.
//   - * does NOT match across / boundaries.
//   - No wildcard means exact string comparison (same as cap == required).
//
// Examples:
//
//	MatchCapability("STAMP:artefact/*/review", "STAMP:artefact/haiku/review") → true
//	MatchCapability("STAMP:artefact/*/review", "STAMP:artefact/code/review") → true
//	MatchCapability("STAMP:artefact/haiku/review", "STAMP:artefact/haiku/review") → true  (exact)
//	MatchCapability("STAMP:artefact/*/review", "STAMP:artefact/haiku/extra/review") → false
//	MatchCapability("STAMP:artefact/haiku/review", "STAMP:artefact/haiku/approval") → false
func MatchCapability(capability, required string) bool {
	// Fast path: no wildcard at all — exact match.
	if !strings.Contains(capability, "*") {
		return capability == required
	}

	// Segment-by-segment matching. Split both into /-delimited segments.
	capSegs := strings.Split(capability, "/")
	reqSegs := strings.Split(required, "/")

	if len(capSegs) != len(reqSegs) {
		return false // Different segment counts — cannot match (wildcard does not cross /).
	}

	for i := range capSegs {
		if !matchSegment(capSegs[i], reqSegs[i]) {
			return false
		}
	}
	return true
}

// matchSegment returns true if pattern matches segment using filepath.Match rules.
// * matches any sequence of characters (but not /, which is guaranteed by the caller).
func matchSegment(pattern, segment string) bool {
	ok, err := filepath.Match(pattern, segment)
	return ok && err == nil
}

// Metadata keys for the Sidecar-injected identity and capability headers.
// These wire strings are contract-defining: they are defined once here and
// imported by the service modules that enforce or read them, never re-declared
// as bare literals in sibling modules.
const (
	// MetadataKeyNodeID is the gRPC metadata key carrying the Sidecar-injected
	// node identity.
	MetadataKeyNodeID = "x-flow-node-id"
	// MetadataKeyCapabilities is the gRPC metadata key carrying the
	// Sidecar-injected, comma-separated capability grants for the node.
	MetadataKeyCapabilities = "x-flow-capabilities"
)

// CheckCapability enforces deny-by-default capability gating for
// node-originated requests. System-to-system calls (no x-flow-node-id)
// pass through unconditionally.
func CheckCapability(ctx context.Context, required string) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil // No metadata — system call.
	}
	nodeIDs := md.Get(MetadataKeyNodeID)
	if len(nodeIDs) == 0 {
		return nil // No node identity — system call.
	}

	caps := md.Get(MetadataKeyCapabilities)
	for _, c := range caps {
		for cap := range strings.SplitSeq(c, ",") {
			if MatchCapability(strings.TrimSpace(cap), required) {
				return nil
			}
		}
	}

	return status.Errorf(codes.PermissionDenied,
		"CAPABILITY_DENIED: missing required capability %q", required)
}
