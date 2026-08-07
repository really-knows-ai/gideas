package ladybug

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"regexp"
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
		// Statements the LadybugDB v0.17.0 grammar cannot classify (e.g.
		// `MATCH ... REMOVE ...`, index/constraint DDL, top-level FOREACH) fail
		// at Prepare before the IsReadOnly guard runs. They are still
		// mutation/DDL statements per SPEC error-table row "ExecuteCypher with
		// mutation statement" and R7 §5 (FOREACH is treated as mutation per
		// SPEC:469-470), so they must surface as PERMISSION_DENIED
		// (ErrMutationCypher) rather than INVALID_ARGUMENT (ErrInvalidCypher).
		// Genuinely-invalid read-only syntax keeps INVALID_ARGUMENT.
		if isMutationCypher(cypher) {
			return nil, store.ErrMutationCypher
		}
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
		indexed, matched, found, err := db.searchIndexedType(conn, t, embedding, topK, entityType)
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
	conn *lbug.Connection, t string, embedding []float32, topK int, entityType string,
) (indexed bool, matched bool, found []store.NeighborResult, err error) {
	// Check if the index is bootstrapped.
	dim, derr := getEmbeddingDimension(conn, t)
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
		entity := entityFromNode(node, t)
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
		// ponytail: TOP is hard-coded to 100 because the SPEC R2 defines
		// FullTextSearch(query, entityType?) with no topK parameter, so there is no
		// caller-supplied limit to thread through. If a topK parameter is added to
		// the SPEC in future, this constant should be replaced with it.
		q := fmt.Sprintf("CALL QUERY_FTS_INDEX('%s', '%s', $q, TOP := 100) RETURN node, score ORDER BY score DESC;",
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
			results = append(results, *entityFromNode(node, t))
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
	if pageSize < 0 {
		return nil, "", store.ErrInvalidPageSize
	}
	if pageSize == 0 {
		pageSize = 1000
	}
	if pageSize > 1000 {
		return nil, "", fmt.Errorf("%w: page size %d exceeds maximum of 1000", store.ErrInvalidPageSize, pageSize)
	}

	conn, typeDefs, unlock, err := db.lockForRead(branch)
	if err != nil {
		return nil, "", err
	}
	defer unlock()

	if _, ok := typeDefs.entityTypeDefs[entityType]; !ok {
		return nil, "", fmt.Errorf("%w: %q", store.ErrUnknownEntityType, entityType)
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
		entities = append(entities, *entityFromNode(node, entityType))
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

// --------------------------------------------------------------------------
// ExecuteCypher mutation/DDL classification helpers
// --------------------------------------------------------------------------

// mutationCypherPattern matches the mutation/DDL clause keywords the SPEC
// error-table row "ExecuteCypher with mutation statement" (SPEC:913) and R7 §5
// enumerate: CREATE, SET, DELETE, MERGE, REMOVE, DROP, and FOREACH (SPEC:469-470
// mandates treating FOREACH as mutation). DDL clause forms — index and
// constraint operations — begin with CREATE, so they are covered by the CREATE
// keyword.
var mutationCypherPattern = regexp.MustCompile(`(?i)\b(create|set|delete|merge|remove|drop|foreach)\b`)

// isMutationCypher reports whether a statement that the grammar could not
// prepare contains a mutation/DDL clause keyword.
// ponytail: keyword containment is a heuristic fallback for statements the
// LadybugDB v0.17.0 grammar cannot parse — it cannot distinguish a mutation
// keyword inside a string literal (e.g. WHERE n.name = 'SET') from a real
// clause, so a read-only query quoting a mutation keyword fails closed with
// PERMISSION_DENIED. This is acceptable: the ceiling is a defensive rejection
// of a read-only query, never an execution of a mutation, and the SPEC mandates
// PERMISSION_DENIED for the whole SPEC-enumerated mutation set. Upgrade path: a
// parser accepting the full Neo4j clause grammar (or a lexer that skips string
// literals) would classify by AST/clause position instead of keyword containment.
func isMutationCypher(cypher string) bool {
	return mutationCypherPattern.MatchString(cypher)
}
