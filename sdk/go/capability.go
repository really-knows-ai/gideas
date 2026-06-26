package flow

import "strings"

// StampCapability represents a parsed STAMP:artefact/<kind>/<stamp> capability.
type StampCapability struct {
	// GovernedArtefact is the governed artefact kind (e.g., "haiku").
	GovernedArtefact string
	// StampName is the stamp name (e.g., "review").
	StampName string
}

const stampCapabilityPrefix = "STAMP:artefact/"

// ParseStampCapability parses a single STAMP:artefact/<kind>/<stamp> capability
// string. Returns ok=false if the string does not match the expected pattern.
func ParseStampCapability(cap string) (StampCapability, bool) {
	if !strings.HasPrefix(cap, stampCapabilityPrefix) {
		return StampCapability{}, false
	}
	rest := cap[len(stampCapabilityPrefix):]
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
//	MatchCapability("STAMP:artefact/*/appraise-*", "STAMP:artefact/haiku/appraise-security") → true
//	MatchCapability("STAMP:artefact/haiku/appraise-*", "STAMP:artefact/haiku/appraise-security") → true
//	MatchCapability("STAMP:artefact/haiku/review", "STAMP:artefact/haiku/review") → true  (exact)
//	MatchCapability("STAMP:artefact/*/appraise-*", "STAMP:artefact/haiku/extra/appraise-security") → false
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

// matchSegment returns true if pattern matches segment.
// pattern supports * as a single-segment wildcard (matches any sequence of characters).
// No other glob syntax is supported.
func matchSegment(pattern, segment string) bool {
	if pattern == "*" {
		return true // Matches any segment content.
	}
	if !strings.Contains(pattern, "*") {
		return pattern == segment // Exact match.
	}
	// Prefix/suffix wildcard: pattern starts or ends with *.
	// For simplicity, only support trailing * (e.g., "appraise-*") or leading * or full *.
	// Split on * and check if the non-wildcard parts match as prefix/suffix.
	parts := strings.Split(pattern, "*")
	// If the pattern has multiple * or * in the middle, use simple prefix/suffix check.
	// After splitting, parts are in order: [prefix, suffix] or [prefix] or [""].
	// parts[0] is the prefix before the first *.
	// parts[len(parts)-1] is the suffix after the last *.
	if parts[0] != "" {
		if !strings.HasPrefix(segment, parts[0]) {
			return false
		}
	}
	if parts[len(parts)-1] != "" {
		if !strings.HasSuffix(segment, parts[len(parts)-1]) {
			return false
		}
	}
	return true
}
