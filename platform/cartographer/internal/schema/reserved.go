package schema

// reservedWords contains the set of Cypher and LadybugDB reserved words that
// cannot be used as type names or property names.
//
// This list includes all standard Cypher keywords and known LadybugDB
// v0.17.0 reserved words. It is hand-maintained (not sourced from the engine
// parser) so a LadybugDB version bump may introduce new reserved words that
// are absent here; until the upgrade path below is enacted, every version
// bump must re-audit this set against the engine's keyword list.
//
// Basis / coverage: the listed keywords are asserted to be covered by
// LadybugDB v0.17.0 (SPEC R1 "Reserved words — LadybugDB reserved words
// (Cypher keywords) are rejected as names; the Cartographer validates this
// at schema application time"), enforced at reservedWords[strings.ToUpper(name)]
// in validate.go (validateEntityType / validateEdgeType), not sourced from the
// parser.
//
// Upgrade path: source the reserved-word set from the actual parser's
// keyword/lexer list (github.com/LadybugDB/go-ladybug) rather than a
// hand-written map, so validation and the engine's reserved words can never
// diverge.
var reservedWords = map[string]bool{
	"ALL":        true,
	"AND":        true,
	"AS":         true,
	"ASC":        true,
	"ASCENDING":  true,
	"BY":         true,
	"CALL":       true,
	"CASE":       true,
	"CONSTRAINT": true,
	"CONTAINS":   true,
	"COUNT":      true,
	"CREATE":     true,
	"CYPHER":     true,
	"DELETE":     true,
	"DESC":       true,
	"DESCENDING": true,
	"DETACH":     true,
	"DISTINCT":   true,
	"DROP":       true,
	"ELSE":       true,
	"END":        true,
	"ENDS":       true,
	"EXISTS":     true,
	"EXPLAIN":    true,
	"FALSE":      true,
	"FILTER":     true,
	"FOREACH":    true,
	"FROM":       true,
	"IN":         true,
	"INDEX":      true,
	"IS":         true,
	"LIMIT":      true,
	"LOAD":       true,
	"MATCH":      true,
	"MERGE":      true,
	"NODE":       true,
	"NONE":       true,
	"NOT":        true,
	"NULL":       true,
	"ON":         true,
	"OPTIONAL":   true,
	"OR":         true,
	"ORDER":      true,
	"PROFILE":    true,
	"REDUCE":     true,
	"REMOVE":     true,
	"RETURN":     true,
	"SET":        true,
	"SHOW":       true,
	"SKIP":       true,
	"START":      true,
	"STARTS":     true,
	"THEN":       true,
	"TRUE":       true,
	"UNION":      true,
	"UNIQUE":     true,
	"UNWIND":     true,
	"USING":      true,
	"WHEN":       true,
	"WHERE":      true,
	"WITH":       true,
	"XOR":        true,
	"YIELD":      true,
}
