package schema

// reservedWords contains the set of Cypher and LadybugDB reserved words that
// cannot be used as type names or property names.
//
// ponytail: this set is hand-maintained (not sourced from the engine's
// compiled parser — the set cannot be extracted from the parser at runtime),
// so a LadybugDB version bump may introduce new reserved words absent here.
// Consequences of the ceiling: a type or property named with such a word
// passes ApplySchema validation (INVALID_ARGUMENT) and only fails later at
// table-creation time (FAILED_PRECONDITION) — the divergence is silent until
// the engine rejects the DDL, and every version bump must re-audit this set
// against the engine's keyword list to stay accurate. Upgrade path: query the
// engine for its reserved-word list on version upgrade, or add a CI check
// pinning this list against the vendored LadybugDB parser, so validation and
// the engine's reserved words can never diverge.
//
// Basis / coverage: the listed keywords are asserted to be covered by
// LadybugDB v0.17.0 (SPEC R1 "Reserved words — LadybugDB reserved words
// (Cypher keywords) are rejected as names; the Cartographer validates this
// at schema application time"), enforced at reservedWords[strings.ToUpper(name)]
// in validate.go (validateEntityType / validateEdgeType), not sourced from the
// parser.
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
