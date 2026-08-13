package ladybug

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/foundry/flow/cartographer/internal/store"
)

// --------------------------------------------------------------------------
// ExecuteCypher
// --------------------------------------------------------------------------

func (db *ladybugDB) ExecuteCypher(
	ctx context.Context, cypher string, params map[string]any, branch string,
) ([]store.CypherRow, error) {
	if cypher == "" {
		return nil, store.ErrEmptyQuery
	}

	conn, _, unlock, err := db.lockForRead(branch)
	if err != nil {
		return nil, err
	}
	defer unlock()

	// Prepare to parse and check read-only.
	stmt, err := conn.Prepare(cypher)
	if err != nil {
		// A statement that fails to parse is rejected with INVALID_ARGUMENT —
		// this comes from Prepare, unchanged (SPEC R3, SPEC:260; error-table
		// row "Invalid Cypher syntax", SPEC:979). That includes mutation/DDL
		// clauses the LadybugDB v0.17.0 grammar cannot parse (top-level
		// FOREACH, `MATCH ... REMOVE ...`, index/constraint DDL): the grammar
		// gap surfaces INVALID_ARGUMENT (ErrInvalidCypher) — never
		// PERMISSION_DENIED (ErrMutationCypher), per the R7 §5 note on
		// grammar-unparseable clauses (SPEC:493-497). Statements that parse
		// and are classified as mutation/DDL are rejected by the IsReadOnly
		// guard below with PERMISSION_DENIED (row "ExecuteCypher with mutation
		// statement", SPEC:976) — the syntax gate precedes read-only
		// enforcement (check order, SPEC:1015).
		return nil, fmt.Errorf("%w: prepare: %v", store.ErrInvalidCypher, err)
	}
	defer stmt.Close()

	if !stmt.IsReadOnly() {
		return nil, store.ErrMutationCypher
	}

	if params == nil {
		params = map[string]any{}
	}

	result, err := conn.Execute(stmt, params)
	if err != nil {
		return nil, err
	}
	defer result.Close()

	var rows []store.CypherRow
	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			return nil, fmt.Errorf("read row: %w", err)
		}
		// GetAsSlice preserves the column order of the query result (SPEC R2:
		// one flat tuple per row in the order LadybugDB returns them).
		values, err := tuple.GetAsSlice()
		tuple.Close()
		if err != nil {
			return nil, fmt.Errorf("parse row: %w", err)
		}
		rows = append(rows, store.CypherRow{Values: values})
	}
	if rows == nil {
		rows = []store.CypherRow{}
	}
	return rows, nil
}

// ExtractEntityTypes parses and validates a Cypher statement and returns the
// distinct entity-type labels its node patterns reference. It is the
// server-authoritative statement-analysis seam for the ExecuteCypher
// capability check (SPEC R3): the Cartographer derives the referenced types
// from its own parse of the statement it is about to execute, so a client can
// neither omit nor forge the type set.
//
// Error classification is identical to ExecuteCypher's, so the SPEC check
// order "empty query → Cypher syntax → read-only enforcement → capability"
// (SPEC:1015) holds: an empty statement returns ErrEmptyQuery, a statement
// that fails to parse returns ErrInvalidCypher (the syntax gate precedes
// read-only enforcement, SPEC R3 / SPEC:260), and a non-read-only statement
// returns ErrMutationCypher — all before any capability decision.
//
// Extraction itself is best-effort and never an error: a parseable read-only
// statement whose patterns yield no labels returns an empty slice and the
// service falls back to the READ:graph/entity/* wildcard check (SPEC R3).
func (db *ladybugDB) ExtractEntityTypes(ctx context.Context, cypher string) ([]string, error) {
	if cypher == "" {
		return nil, store.ErrEmptyQuery
	}

	conn, _, unlock, err := db.lockForRead("")
	if err != nil {
		return nil, err
	}
	defer unlock()

	// Prepare to parse and check read-only — the same seam ExecuteCypher uses.
	// A statement that fails to parse surfaces ErrInvalidCypher (INVALID_ARGUMENT)
	// unchanged (SPEC R3, SPEC:260; row "Invalid Cypher syntax", SPEC:979),
	// never ErrMutationCypher — the syntax gate precedes read-only enforcement.
	stmt, err := conn.Prepare(cypher)
	if err != nil {
		return nil, fmt.Errorf("%w: prepare: %v", store.ErrInvalidCypher, err)
	}
	defer stmt.Close()

	if !stmt.IsReadOnly() {
		return nil, store.ErrMutationCypher
	}

	return extractEntityTypeLabels(cypher), nil
}

// extractEntityTypeLabels derives the distinct entity-type labels referenced
// by node patterns in a known-parseable, read-only Cypher statement. It
// strips `//` and `/* */` comments and string literals, then finds node
// patterns `(v:Label)`, `(:Label)` (anonymous nodes), and multi-label
// `(v:A:B)`, handling inline property maps and relationship patterns —
// e.g. `MATCH (a:Component)-[:DEPENDS_ON]->(b:Service)` yields
// [Component Service]. Node-pattern-shaped text inside comments, string
// literals, or non-node parenthesised expressions is ignored; node patterns
// outside MATCH clauses (e.g. a `WHERE (a)--(:Service)` pattern expression)
// ARE extracted, since they reference the same labels.
//
// Bare (non-parenthesised) label predicates — `m:Service` in
// `WHERE m:Service` — are also extracted as referenced labels, so a query
// like `MATCH (n:Component)-[r]->(m) WHERE m:Service RETURN m` yields
// [Component Service] and cannot be authorised for Service rows by a caller
// holding only the Component per-type grant (SPEC R3). Relationship-type
// specifiers (`-[r:DEPENDS_ON]->`) and map-literal key/value pairs are not
// entity-type labels and are skipped.
//
// The analyzer is deliberately not a full Cypher parser: the SPEC check order
// runs syntax validation (Prepare) before this point, so it only classifies
// the pattern structure of a statement already known to parse.
//
// Fail-closed on unclassifiable labels: when a node pattern carries a label
// form the analyzer cannot classify — backtick-quoted labels (`(n:`Label With
// Space`)`), parameterised labels (`(n:$label)`), dynamic labels, labels in
// patterns the matcher cannot balance, or a bare predicate whose label form
// is likewise unclassifiable — the extraction is abandoned and returns nil,
// so the caller falls back to the READ:graph/entity/* wildcard check (SPEC:260
// "must never widen access beyond the wildcard fallback"). A partial
// extraction that returned only the classifiable labels would let a caller
// holding that subset execute a query that also touches a missed type,
// widening access relative to the every-referenced-type rule (SPEC R3); the
// wildcard check is strictly stronger than any per-type subset. Upgrade path:
// a server-side cgo binding of a real Cypher parser (e.g. libcypher-parser)
// exposing the statement AST would make extraction exact; per the SPEC
// discussion this is deferred while the regex-state-machine coverage is
// sufficient.
func extractEntityTypeLabels(cypher string) []string {
	s := stripCommentsAndStrings(cypher)
	seen := make(map[string]struct{})
	var labels []string
	addLabel := func(label string) bool {
		if !isCypherIdentifier(label) {
			// Fail closed: an unclassifiable label form (backtick-quoted,
			// parameterised, or dynamic labels) means the statement's
			// referenced-type set cannot be derived exactly. Returning a
			// partial subset would let a caller holding only the extracted
			// types execute a query that also touches a missed type, widening
			// access beyond the every-referenced-type rule (SPEC R3). Returning
			// nil sends the caller to the READ:graph/entity/* wildcard check —
			// the SPEC:260 bound.
			return false
		}
		if _, dup := seen[label]; !dup {
			seen[label] = struct{}{}
			labels = append(labels, label)
		}
		return true
	}
	i := 0
	for i < len(s) {
		switch s[i] {
		case '(':
			close, ok := matchingParen(s, i)
			if !ok {
				break
			}
			inner := s[i+1 : close]
			i = close + 1
			// A node pattern carries labels from its first ':' (after the
			// optional variable) up to any '{' (inline property map) or the
			// closing paren.
			colon := strings.IndexByte(inner, ':')
			if colon < 0 {
				continue // unlabelled node (e.g. "(n)") — no labels
			}
			rest := inner[colon:]
			if brace := strings.IndexByte(rest, '{'); brace >= 0 {
				rest = rest[:brace]
			}
			for part := range strings.SplitSeq(rest, ":") {
				label := strings.TrimSpace(part)
				if label == "" {
					continue // artifact of the ':' split (leading/trailing separator)
				}
				if !addLabel(label) {
					return nil
				}
			}
		case '{':
			// A map literal's key/value pairs are not label predicates.
			close, ok := matchingBrace(s, i)
			if !ok {
				break
			}
			i = close + 1
		case '[':
			// A relationship specifier's edge type — `-[r:TYPE]->` — and list
			// literal contents are not entity-type labels (per-type grants are
			// entity grants, SPEC R3). The `WHERE x:Label` predicate inside a
			// list comprehension is a missed shape with the same fail-closed
			// exposure as the pre-fix bare-WHERE gap; distinguishing it from a
			// relationship specifier needs the parser upgrade path below.
			close, ok := matchingBracket(s, i)
			if !ok {
				break
			}
			i = close + 1
		case ':':
			// A bare (non-parenthesised) label predicate, e.g. `m:Service` in
			// `WHERE m:Service`. Node patterns handle their own labels above;
			// this case catches predicate-position labels the paren scanner
			// never sees. In a parseable statement a colon outside node-pattern
			// parens, map literals, and bracket groups must be a label
			// predicate; an unclassifiable one abandons the extraction.
			label, end, ok := barePredicateLabel(s, i)
			if !ok {
				return nil
			}
			if !addLabel(label) {
				return nil
			}
			i = end
		default:
			i++
		}
	}
	if len(labels) == 0 {
		return nil
	}
	return labels
}

// barePredicateLabel parses the label of a bare `identifier:Label` predicate
// whose colon sits at s[colon] (whitespace after the colon is allowed, e.g.
// `WHERE n: Service`). It returns the label and the index just past it, or
// ok=false when the shape is not a bare label predicate the analyzer can
// classify: a colon not immediately preceded by a bare variable identifier,
// or a backtick-quoted/parameterised/empty label. The caller fails closed on
// ok=false.
func barePredicateLabel(s string, colon int) (string, int, bool) {
	// The variable identifier must immediately precede the colon.
	start := colon
	for start > 0 && isIdentifierChar(s[start-1]) {
		start--
	}
	if !isCypherIdentifier(s[start:colon]) {
		return "", 0, false
	}
	j := colon + 1
	for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
		j++
	}
	end := j
	for end < len(s) && isIdentifierChar(s[end]) {
		end++
	}
	label := s[j:end]
	if !isCypherIdentifier(label) {
		return "", 0, false
	}
	return label, end, true
}

// isIdentifierChar reports whether c is a character that may appear in a bare
// Cypher identifier ([a-zA-Z_][a-zA-Z0-9_]*, see isCypherIdentifier). It does
// not enforce the first-character rule — barePredicateLabel validates the full
// run via isCypherIdentifier.
func isIdentifierChar(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// matchingParen returns the index of the ')' matching the '(' at open, or
// ok=false if no match exists. Inline property maps ({...}) are skipped in
// balanced fashion so a ')' or '(' inside a map value never terminates or
// mis-nests the pattern scan.
func matchingParen(s string, open int) (int, bool) {
	depth := 0
	for j := open; j < len(s); j++ {
		switch s[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return j, true
			}
		case '{':
			bDepth := 0
			for j < len(s) {
				switch s[j] {
				case '{':
					bDepth++
				case '}':
					bDepth--
				}
				if bDepth == 0 {
					// Stop with j on the closing '}' so the outer loop's j++
					// advances to the character after the map (the node's
					// closing ')') instead of skipping it.
					break
				}
				j++
			}
		}
	}
	return 0, false
}

// matchingBrace returns the index of the '}' matching the '{' at open, or
// ok=false if no match exists.
func matchingBrace(s string, open int) (int, bool) {
	depth := 0
	for j := open; j < len(s); j++ {
		switch s[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return j, true
			}
		}
	}
	return 0, false
}

// matchingBracket returns the index of the ']' matching the '[' at open, or
// ok=false if no match exists.
func matchingBracket(s string, open int) (int, bool) {
	depth := 0
	for j := open; j < len(s); j++ {
		switch s[j] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return j, true
			}
		}
	}
	return 0, false
}

// isCypherIdentifier reports whether s is a bare Cypher identifier (letters,
// digits after the first character, and underscores) — the only label form the
// analyzer can classify (see the extractEntityTypeLabels ponytail for the
// unclassified forms).
func isCypherIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

// stripCommentsAndStrings removes `//` line comments, `/* */` block comments,
// and single/double-quoted string literals (honouring backslash escapes) from
// a Cypher statement, so node-pattern-shaped text inside them is never
// misread as a referenced entity-type label. The statement is known to parse,
// so strings and comments are well-formed; the guards simply bound the scan.
func stripCommentsAndStrings(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\'' || c == '"':
			quote := c
			i++
			for i < len(s) {
				if s[i] == '\\' {
					i += 2
					continue
				}
				if s[i] == quote {
					i++
					break
				}
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			i += 2
			for i+1 < len(s) && (s[i] != '*' || s[i+1] != '/') {
				i++
			}
			i += 2
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// --------------------------------------------------------------------------
// SearchNeighbors
// --------------------------------------------------------------------------

func (db *ladybugDB) SearchNeighbors(
	ctx context.Context, embedding []float32, entityType string, topK int, branch string,
) ([]store.NeighborResult, error) {
	// Validate embedding. The SPEC error-table (line ~880) defines an empty
	// embedding as INVALID_ARGUMENT; enforce it at this authoritative store
	// boundary in addition to the service-layer gate so a direct store caller
	// cannot silently receive empty results for an empty embedding.
	if len(embedding) == 0 {
		return nil, store.ErrEmptyEmbedding
	}
	for _, v := range embedding {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, store.ErrNaNOrInfEmbedding
		}
	}
	if topK < 0 {
		return nil, store.ErrInvalidTopK
	}
	if topK == 0 {
		topK = 10
	}

	conn, typeDefs, unlock, err := db.lockForRead(branch)
	if err != nil {
		return nil, err
	}
	defer unlock()

	var typesToSearch []string
	if entityType != "" {
		def, ok := typeDefs.entityTypeDefs[entityType]
		if !ok {
			return nil, fmt.Errorf("%w: %q", store.ErrUnknownEntityType, entityType)
		}
		if !def.EnableVectorIndex {
			return nil, fmt.Errorf("%w: %q", store.ErrNonIndexedType, entityType)
		}
		typesToSearch = append(typesToSearch, entityType)
	} else {
		// Search all vector-indexed types.
		for name, def := range typeDefs.entityTypeDefs {
			if def.EnableVectorIndex {
				typesToSearch = append(typesToSearch, name)
			}
		}
	}

	var results []store.NeighborResult
	// searchIndexedType handles a single entity type's contribution to a
	// multi-type search. Returned matched reports whether the type's
	// established dimension matched the query embedding (data was searched);
	// found carries any results. Extracted to keep SearchNeighbors' cyclomatic
	// complexity under the lint threshold.
	dimensionMatched := false
	indexedExists := false
	var foundForType []store.NeighborResult
	for _, t := range typesToSearch {
		indexed, matched, found, err := db.searchIndexedType(conn, t, embedding, topK, entityType,
			typeDefs.entityTypeDefs[t].EnableVectorIndex)
		if err != nil {
			return nil, err
		}
		if indexed {
			indexedExists = true
		}
		if matched {
			dimensionMatched = true
			foundForType = append(foundForType, found...)
		}
	}
	results = foundForType

	// A wildcard search only fails with ErrEmbeddingDimension when a query
	// embedding's dimension "matches no established index" — i.e. at least one
	// indexed type has an actually bootstrapped index (indexedExists) but none
	// dimension-matched this query. When no vector-indexed type is bootstrapped
	// yet (an empty graph, or only not-yet-ever-written types whose dimension is
	// still 0 and are silently skipped — see searchIndexedType), there is no
	// established index to mismatch against: SPEC R5 requires a type-omitted
	// (non-type-referencing) method to succeed on an empty graph, so the
	// wildcard search returns an empty result set instead of erroring.
	if entityType == "" && indexedExists && !dimensionMatched {
		return nil, store.ErrEmbeddingDimension
	}

	// Sort by distance ascending.
	sort.Slice(results, func(i, j int) bool {
		return results[i].Distance < results[j].Distance
	})
	if len(results) > topK {
		results = results[:topK]
	}
	if results == nil {
		results = []store.NeighborResult{}
	}
	return results, nil
}

// searchIndexedType queries the vector index for a single entity type and
// returns indexed (whether the type has an established bootstrapped index, i.e.
// a dimension > 0), matched (whether that established dimension matched the
// query embedding, meaning data was searched) and its aggregated results
// (found). A skipped type — not bootstrapped (indexed==false), or a dimension
// mismatch in a wildcard search (indexed==true, matched==false) — yields no
// results and no error, letting the caller distinguish "no index yet" from
// "established index but dimension mismatch" and aggregate the other types.
func (db *ladybugDB) searchIndexedType(
	conn *lbug.Connection, t string, embedding []float32, topK int, entityType string, vectorIndexed bool,
) (indexed bool, matched bool, found []store.NeighborResult, err error) {
	// Check if the index is bootstrapped.
	dim, derr := getEmbeddingDimension(conn, t, vectorIndexed)
	if derr != nil {
		return false, false, nil, fmt.Errorf("read embedding dimension for %q: %w", t, derr)
	}
	// ponytail: A type whose vector index is not yet bootstrapped (dim == 0) is
	// silently skipped rather than surfacing an error. The dimension is inferred
	// from the first embedding written for the type (lazy index bootstrap, see
	// R7), so a type with no embeddings legitimately has no index yet and is
	// simply not searchable. The SPEC does not define this as an error condition,
	// so we skip silently while still surfacing real errors (dimension mismatch,
	// read failures, etc.) where they exist.
	if dim == 0 {
		return false, false, nil, nil // no bootstrapped index yet
	}
	// A bootstrapped index exists from here on.
	if len(embedding) != dim {
		if entityType != "" {
			// Single-type search: the queried type's established dimension is
			// authoritative, so a mismatch is an error (SPEC error table row
			// "Embedding dimension mismatch").
			return true, false, nil, fmt.Errorf("%w: for entity type %q, expected dimension %d, got %d",
				store.ErrEmbeddingDimension, t, dim, len(embedding))
		}
		// Wildcard search: skip this type, keep searching the others.
		return true, false, nil, nil
	}
	// Use QUERY_VECTOR_INDEX. Index name matches what CreateEntity creates.
	idxName := t + "_vec"
	q := fmt.Sprintf("CALL QUERY_VECTOR_INDEX('%s', '%s', $emb, %d) RETURN node, distance ORDER BY distance;",
		t, idxName, topK)
	stmt, err := conn.Prepare(q)
	if err != nil {
		// The embedding column exists with a bootstrapped dimension (dim > 0),
		// so the vector index should be present; a Prepare failure here is an
		// operational error for this type, not a transient "index absent"
		// state. Propagate so the caller can distinguish the two instead of
		// silently dropping this type's contribution.
		return true, false, nil, fmt.Errorf("prepare vector index query for %q: %w", t, err)
	}
	// ponytail: The LadybugDB query-vector-index call expects the embedding
	// as a FLOAT[] parameter. We pass it as a flat []any slice.
	embAny := make([]any, len(embedding))
	for i, v := range embedding {
		embAny[i] = v
	}
	result, err := conn.Execute(stmt, map[string]any{"emb": embAny})
	stmt.Close()
	if err != nil {
		// The vector index query prepared successfully, so the index exists;
		// an Execute failure here is operational. Surface it rather than
		// silently dropping this type's contribution.
		return true, false, nil, fmt.Errorf("execute vector index query for %q: %w", t, err)
	}
	defer result.Close()
	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			return true, false, nil, fmt.Errorf("read vector result: %w", err)
		}
		m, err := tuple.GetAsMap()
		tuple.Close()
		if err != nil {
			return true, false, nil, fmt.Errorf("parse vector result: %w", err)
		}
		node, ok := m["node"].(lbug.Node)
		if !ok {
			return true, false, nil, fmt.Errorf("vector result for %q: unexpected node type %T", t, m["node"])
		}
		var distance float64
		switch d := m["distance"].(type) {
		case float64:
			distance = d
		case float32:
			distance = float64(d)
		case nil:
			return true, false, nil, fmt.Errorf("vector result for %q missing distance", t)
		default:
			return true, false, nil, fmt.Errorf("unexpected distance type for %q: got %T", t, m["distance"])
		}
		entity := entityFromNode(node, t, vectorIndexed)
		found = append(found, store.NeighborResult{
			Entity:   *entity,
			Distance: distance,
		})
	}
	return true, true, found, nil
}

// --------------------------------------------------------------------------
// FullTextSearch
// --------------------------------------------------------------------------

func (db *ladybugDB) FullTextSearch(
	ctx context.Context, query, entityType, branch string,
) ([]store.Entity, error) {
	if query == "" {
		return nil, store.ErrEmptyQuery
	}

	conn, typeDefs, unlock, err := db.lockForRead(branch)
	if err != nil {
		return nil, err
	}
	defer unlock()

	var typesToSearch []string
	if entityType != "" {
		if _, ok := typeDefs.entityTypeDefs[entityType]; !ok {
			return nil, fmt.Errorf("%w: %q", store.ErrUnknownEntityType, entityType)
		}
		typesToSearch = append(typesToSearch, entityType)
	} else {
		for name := range typeDefs.entityTypeDefs {
			typesToSearch = append(typesToSearch, name)
		}
	}

	var results []store.Entity
	for _, t := range typesToSearch {
		// Use QUERY_FTS_INDEX if available; fall back to property scan.
		idxName := t + "_fts"
		// No TOP argument: LadybugDB's QUERY_FTS_INDEX TOP is optional and
		// defaults to retrieving every matching document, and SPEC R2 defines
		// FullTextSearch(query, entityType?) with no result limit and no error
		// table cap — a silently capped result set would be an incomplete
		// answer with no client-visible indication. If a topK parameter is
		// added to the SPEC in future, it should be threaded through as TOP.
		q := fmt.Sprintf("CALL QUERY_FTS_INDEX('%s', '%s', $q) RETURN node, score ORDER BY score DESC;",
			t, idxName)
		stmt, err := conn.Prepare(q)
		if err != nil {
			// ponytail: An entity type whose FTS index is absent (never created,
			// or the FTS extension failed to load at table-creation time) is
			// silently skipped here, so a FullTextSearch across multiple types
			// returns an incomplete result set with no error and no indication
			// of which types were omitted. The SPEC does not define this as an
			// error condition and an index-less type is legitimately
			// unsearchable, so we skip silently — but the caller has no way to
			// distinguish a complete result set from one missing types. Upgrade
			// path: surface per-type index absence (log or return a partial-result
			// notice) or fall back to a property scan (MATCH + LIKE) when the FTS
			// index is unavailable. This is the same silent-skip failure mode
			// documented for vector search in SearchNeighbors.
			continue
		}
		result, err := conn.Execute(stmt, map[string]any{"q": query})
		stmt.Close()
		if err != nil {
			// The FTS index query prepared successfully, so the index exists;
			// an Execute failure here is operational. Surface it rather than
			// silently omitting this type from the result set.
			return nil, fmt.Errorf("execute fts index query for %q: %w", t, err)
		}
		for result.HasNext() {
			tuple, err := result.Next()
			if err != nil {
				result.Close()
				return nil, fmt.Errorf("read fts result: %w", err)
			}
			m, err := tuple.GetAsMap()
			tuple.Close()
			if err != nil {
				result.Close()
				return nil, fmt.Errorf("parse fts result: %w", err)
			}
			node, ok := m["node"].(lbug.Node)
			if !ok {
				result.Close()
				return nil, fmt.Errorf("fts result for %q: unexpected node type %T", t, m["node"])
			}
			results = append(results, *entityFromNode(node, t, typeDefs.entityTypeDefs[t].EnableVectorIndex))
		}
		result.Close()
	}

	if results == nil {
		results = []store.Entity{}
	}
	return results, nil
}

// --------------------------------------------------------------------------
// ListEntities
// --------------------------------------------------------------------------

func (db *ladybugDB) ListEntities(
	ctx context.Context, entityType string, pageSize int, pageToken, branch string,
) ([]store.Entity, string, error) {
	// SPEC:960 check order for ListEntities: capability → structural (unknown
	// entity type → pageSize → pageToken). The entity-type existence check
	// requires the type defs from the branch lock, so the lock is acquired
	// before the pageSize validation; when multiple inputs are invalid the
	// earliest check in this order is the error surfaced.
	conn, typeDefs, unlock, err := db.lockForRead(branch)
	if err != nil {
		return nil, "", err
	}
	defer unlock()

	if _, ok := typeDefs.entityTypeDefs[entityType]; !ok {
		return nil, "", fmt.Errorf("%w: %q", store.ErrUnknownEntityType, entityType)
	}

	if pageSize < 0 {
		return nil, "", store.ErrInvalidPageSize
	}
	if pageSize == 0 {
		pageSize = 1000
	}
	if pageSize > 1000 {
		return nil, "", fmt.Errorf("%w: page size %d exceeds maximum of 1000", store.ErrInvalidPageSize, pageSize)
	}

	// Decode page token (offset-based: base64-encoded offset as string).
	// ponytail: Pagination is offset-based (ORDER BY n.id with SKIP $off), which is
	// fragile under concurrent mutations: an insert or delete between pages can
	// shift offsets and cause rows to be skipped or duplicated. A cursor-based
	// (keyset) scheme would be resilient, but that changes the page-token semantics
	// and the SPEC does not document either approach or its fragility. Kept as-is
	// until the SPEC is updated.
	// ponytail: The emitted next-page token is computed as `offset + pageSize` with
	// no overflow guard. A crafted non-negative offset near math.MaxInt64 is accepted
	// (any non-negative int64 token passes ParseInt), so `offset + pageSize` wraps to a
	// negative value; that negative token is then rejected by this same method as
	// ErrInvalidPageToken on the follow-up call. Practically unreachable (it requires
	// the graph to have many billions of rows at a huge SKIP offset — with fewer rows
	// SKIP returns nothing so no next token is emitted), so it is left as-is rather than
	// introducing arbitrary-precision offset arithmetic. Upgrade path: clamp the offset
	// so offset+pageSize cannot overflow, or reject offsets that exceed the row count /
	// a sane cursor bound, or switch to the cursor-based (keyset) scheme above.
	offset := 0
	if pageToken != "" {
		data, err := base64.StdEncoding.DecodeString(pageToken)
		if err != nil {
			return nil, "", fmt.Errorf("%w: malformed page token", store.ErrInvalidPageToken)
		}
		parsed, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		if err != nil {
			// Require an exact integer token: ParseInt rejects trailing garbage
			// (SPEC R2/R3) where the previous fmt.Sscanf partial match silently
			// accepted a malformed token like "12abc" as offset 12.
			return nil, "", fmt.Errorf("%w: malformed page token", store.ErrInvalidPageToken)
		}
		offset = int(parsed)
		if offset < 0 {
			return nil, "", fmt.Errorf("%w: malformed page token (negative offset)", store.ErrInvalidPageToken)
		}
	}

	q := fmt.Sprintf("MATCH (n:%s) RETURN n ORDER BY n.id SKIP $off LIMIT $ps;", quoteID(entityType))
	stmt, err := conn.Prepare(q)
	if err != nil {
		return nil, "", fmt.Errorf("prepare list query: %w", err)
	}
	defer stmt.Close()

	result, err := conn.Execute(stmt, map[string]any{"off": int64(offset), "ps": int64(pageSize + 1)})
	if err != nil {
		return nil, "", err
	}
	defer result.Close()

	var entities []store.Entity
	count := 0
	for result.HasNext() && count < pageSize {
		tuple, err := result.Next()
		if err != nil {
			return nil, "", fmt.Errorf("read entity row: %w", err)
		}
		m, err := tuple.GetAsMap()
		tuple.Close()
		if err != nil {
			return nil, "", fmt.Errorf("parse entity row: %w", err)
		}
		node, ok := m["n"].(lbug.Node)
		if !ok {
			return nil, "", fmt.Errorf("entity row for %q: unexpected node type %T", entityType, m["n"])
		}
		entities = append(entities, *entityFromNode(node, entityType, typeDefs.entityTypeDefs[entityType].EnableVectorIndex))
		count++
	}

	// Check if there is a next page (we fetched pageSize+1 but only returned pageSize).
	hasMore := result.HasNext()

	var nextToken string
	if hasMore {
		nextOffset := offset + pageSize
		nextToken = base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%d", nextOffset))
	}
	if entities == nil {
		entities = []store.Entity{}
	}
	return entities, nextToken, nil
}
