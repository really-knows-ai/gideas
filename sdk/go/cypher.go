package flow

import (
	"regexp"
	"strings"
)

// extractEntityTypes parses a Cypher statement and returns the set of
// entity-type labels referenced in MATCH patterns.
// Returns nil if parsing fails (caller falls back to wildcard).
//
// ponytail: Uses regex-based extraction as a fallback when the LadybugDB
// parser is unavailable. The regex approach handles common Cypher patterns
// but may miss edge cases (e.g., multi-line MATCH expressions, parameterised
// labels). Upgrade path: switch to github.com/LadybugDB/go-ladybug/parser
// when available. The function signature remains unchanged regardless of
// extraction strategy — callers handle nil returns with wildcard fallback.
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
		parts := strings.Split(labelsStr, ":")
		for _, label := range parts {
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
