package schema

// reservedWords contains the set of Cypher and LadybugDB reserved words that
// cannot be used as type names or property names.
//
// ponytail: This is a hand-maintained subset of LadybugDB's reserved words, NOT
// the complete set. Several standard Cypher keywords are absent (e.g. CONSTRAINT,
// UNIQUE, SHOW, EXPLAIN, COUNT, REDUCE, FILTER, FROM, NONE, NODE, CYPHER, PROFILE).
// A schema using a word LadybugDB reserves but that is absent here passes validation
// and fails at table-creation time, defeating the reserved-word requirement.
//
// Basis / coverage: the listed subset is asserted to be covered by LadybugDB
// v0.17.0 (SPEC R1 "Reserved words — LadybugDB reserved words (Cypher keywords) are
// rejected as names; the Cartographer validates this at schema application time"),
// but the exact coverage of the full list is NOT evidenced against the actual
// library source; the subset is reviewed against SPEC R1 and enforced at
// reservedWords[strings.ToUpper(name)] in validate.go (validateEntityType /
// validateEdgeType), not sourced from the parser.
//
// Upgrade path: source the reserved-word set from the actual parser's
// keyword/lexer list (github.com/LadybugDB/go-ladybug) rather than a hand-written
// map, so validation and the engine's reserved words can never diverge. Until
// then, every LadybugDB version bump must re-audit this set.
var reservedWords = map[string]bool{
	"ALL":        true,
	"AND":        true,
	"AS":         true,
	"ASC":        true,
	"ASCENDING":  true,
	"BY":         true,
	"CALL":       true,
	"CASE":       true,
	"CONTAINS":   true,
	"CREATE":     true,
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
	"FALSE":      true,
	"FOREACH":    true,
	"IN":         true,
	"INDEX":      true,
	"IS":         true,
	"LIMIT":      true,
	"LOAD":       true,
	"MATCH":      true,
	"MERGE":      true,
	"NOT":        true,
	"NULL":       true,
	"ON":         true,
	"OPTIONAL":   true,
	"OR":         true,
	"ORDER":      true,
	"REMOVE":     true,
	"RETURN":     true,
	"SET":        true,
	"SKIP":       true,
	"START":      true,
	"STARTS":     true,
	"THEN":       true,
	"TRUE":       true,
	"UNION":      true,
	"UNWIND":     true,
	"USING":      true,
	"WHEN":       true,
	"WHERE":      true,
	"WITH":       true,
	"XOR":        true,
	"YIELD":      true,
}
