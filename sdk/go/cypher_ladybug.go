//go:build ladybug

package flow

import (
	"regexp"
	"strings"

	"github.com/LadybugDB/go-ladybug"
)

// extractEntityTypes parses a Cypher statement and returns the set of
// entity-type labels referenced in MATCH patterns.
// Returns nil if parsing fails (caller falls back to wildcard).
//
// This implementation uses the LadybugDB client library for Cypher statement
// validation via an in-memory database instance. Label extraction still uses
// regex pattern matching because the LadybugDB Go API does not expose AST
// node labels directly. The LadybugDB validation step ensures the statement
// is syntactically valid Cypher before extraction proceeds — if Prepare
// fails (invalid syntax), the function returns nil for wildcard fallback,
// which is stricter than the regex-only fallback.
//
// ponytail: Requires the liblbug C library. When liblbug is not available,
// build without the `ladybug` tag to use the pure-Go regex fallback in
// cypher.go instead. The in-memory database is ephemeral and incurs a
// one-time allocation per call; for hot paths, the database could be
// reused across calls.
func extractEntityTypes(cypher string) []string {
	if cypher == "" {
		return nil
	}

	// Step 1: Validate the Cypher statement using LadybugDB's parser.
	// An in-memory database is used because we only need Prepare, not
	// actual data operations.
	db, err := lbug.OpenInMemoryDatabase(lbug.DefaultSystemConfig())
	if err != nil {
		// Fall through to regex below.
		_ = err
	} else {
		conn, connErr := lbug.OpenConnection(db)
		if connErr == nil {
			stmt, prepErr := conn.Prepare(cypher)
			if prepErr != nil {
				// Invalid Cypher — return nil for wildcard fallback.
				conn.Close()
				db.Close()
				return nil
			}
			// Check that the statement is read-only.
			if !stmt.IsReadOnly() {
				// ponytail: Non-read-only statements are rejected at the
				// Cartographer server level (R7). We also reject them
				// client-side here as a defence-in-depth measure.
				stmt.Close()
				conn.Close()
				db.Close()
				return nil
			}
			stmt.Close()
			conn.Close()
		}
		db.Close()
	}

	// Step 2: Extract labels from MATCH patterns using regex.
	// LadybugDB's Go API does not expose parsed node labels, so this
	// step is shared with the regex-only fallback in cypher.go.
	if !strings.Contains(strings.ToUpper(cypher), "MATCH") {
		return nil
	}

	nodeRe := regexp.MustCompile(`\(([a-zA-Z_][a-zA-Z0-9_]*)((?::[a-zA-Z_][a-zA-Z0-9_]*)*)\)`)
	matches := nodeRe.FindAllStringSubmatch(cypher, -1)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[string]struct{})
	var result []string
	for _, m := range matches {
		labelsStr := m[2]
		if labelsStr == "" {
			continue
		}
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
