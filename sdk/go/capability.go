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
