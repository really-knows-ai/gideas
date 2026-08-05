//go:build !ladybug

package flow

import (
	"regexp"
	"strings"
)

// extractEntityTypes parses a Cypher statement and returns the set of
// entity-type labels referenced in MATCH patterns.
// Returns nil if parsing fails (caller falls back to wildcard).
//
// ponytail: This implementation uses regex-based extraction as a pure-Go
// fallback when the LadybugDB C library (liblbug) is not available. The
// regex approach handles common Cypher patterns but may miss edge cases
// (e.g., multi-line MATCH expressions, parameterised labels). When built
// with `-tags ladybug` and liblbug is installed, the LadybugDB-backed
// implementation in cypher_ladybug.go is used instead — it validates syntax
// with the parser BEFORE extracting, so on invalid syntax it returns nil and
// the caller collapses to the READ:graph/entity/* wildcard (SPEC R3). This
// parser-failure fallback can NEVER fire in the default build: the regex
// scan has no validation step, so invalid Cypher that the regex happens to
// pattern-match is forwarded to the Cartographer with its extracted labels
// (or the wildcard when no labels match) rather than collapsing to the
// wildcard on failure as R3 prescribes. Consequence: in the default build
// the R3 "parser fails -> wildcard" contract is degraded to "regex extracts
// -> typed label", deviating from the SPEC's parser-backed contract.
// The Cartographer re-authorizes on ingress, so the correctness risk is
// bounded to capability annotation granularity, not authorization bypass.
// Function signatures and callers are identical regardless of strategy —
// callers handle nil returns with wildcard fallback. Upgrade path: make the
// LadybugDB validation step a mandatory build-time requirement (always build
// with `-tags ladybug`) so the R3 parser-failure fallback actually fires, or
// vendor a pure-Go Cypher grammar/parser.
func extractEntityTypes(cypher string) []string {
	if cypher == "" {
		return nil
	}

	// Step 1: Find all MATCH clauses.
	// Match "MATCH " through the end of the pattern, which may include
	// relationship patterns like -[...]-> or simple commas.
	// We find the MATCH keyword and then extract all node patterns from it.
	if !strings.Contains(strings.ToUpper(cypher), "MATCH") {
		return nil
	}

	// Step 2: Find all individual parenthesized node patterns.
	// A node pattern is (variable:Label...) — excluding relationship
	// patterns like [:REL] which start with colon immediately after '('.
	// Match ( followed by identifier then : then label(s).
	nodeRe := regexp.MustCompile(`\(([a-zA-Z_][a-zA-Z0-9_]*)((?::[a-zA-Z_][a-zA-Z0-9_]*)*)\)`)
	matches := nodeRe.FindAllStringSubmatch(cypher, -1)
	if len(matches) == 0 {
		return nil
	}

	// Step 3: Extract labels from each match.
	seen := make(map[string]struct{})
	var result []string
	for _, m := range matches {
		// m[2] contains ":Label1:Label2" etc.
		labelsStr := m[2]
		if labelsStr == "" {
			continue
		}
		// Split on colon to get individual labels.
		parts := strings.SplitSeq(labelsStr, ":")
		for label := range parts {
			if label == "" {
				continue
			}
			if _, ok := seen[label]; !ok {
				seen[label] = struct{}{}
				result = append(result, label)
			}
		}
	}

	if len(result) == 0 {
		return nil
	}
	return result
}
