package schema

// reservedWords contains the set of Cypher and LadybugDB reserved words that
// cannot be used as type names or property names.
//
// ponytail: this set is hand-maintained (not sourced from the engine's
// compiled parser — the set cannot be extracted from the parser at runtime),
// so a LadybugDB version bump may introduce new reserved words absent here.
// Consequences of the ceiling: a type or property named with such a word
// passes ApplySchema validation (no INVALID_ARGUMENT is returned) and is
// applied without error — the store emits every table name, label, and column
// through
// quoteID backticks (createNodeTableOnConn/createRelTableOnConn in branch.go,
// ALTER DDL in schema.go), and the engine accepts backtick-quoted reserved
// words as identifiers (verified against v0.17.0: CREATE NODE TABLE `MATCH`
// and ALTER TABLE `MATCH` ADD `LIMIT` both succeed). There is therefore no
// table-creation-time rejection (no FAILED_PRECONDITION); the divergence is
// silent — the type is stored and served through the store's own quoted
// queries, and only surfaces when a user references the name in raw unquoted
// Cypher (parser error) or after a version bump that changes the engine's
// keyword set. Every version bump must re-audit this set against the engine's
// keyword list to stay accurate. Upgrade path: query the engine for its
// reserved-word list on version upgrade, or add a CI check pinning this list
// against the vendored LadybugDB parser, so validation and the engine's
// reserved words can never diverge.
//
// Basis / coverage: verified against LadybugDB v0.17.0 by probing every
// candidate openCypher keyword (the reserved and future-reserved sets, plus
// every keyword token in the engine's parser error set) as an unquoted DDL
// identifier — every word the parser rejects is in this list (FOR was found
// missing in this review and added). Soft keywords the engine accepts as
// identifiers in some positions are retained: they are openCypher reserved
// words that still collide with Cypher syntax in unquoted raw queries, which
// is what SPEC R1 ("Reserved words — LadybugDB reserved words (Cypher
// keywords) are rejected as names; the Cartographer validates this at schema
// application time") guards against. Enforced at reservedWords[strings.ToUpper(name)]
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
	"FOR":        true,
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
