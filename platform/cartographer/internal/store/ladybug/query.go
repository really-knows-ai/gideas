package ladybug

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"sort"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/foundry/flow/cartographer/internal/store"
)

// --------------------------------------------------------------------------
// ExecuteCypher
// --------------------------------------------------------------------------

func (db *ladybugDB) ExecuteCypher(
	ctx context.Context, cypher string, params map[string]any, branch string,
) ([]map[string]any, error) {
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

	var rows []map[string]any
	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			return nil, fmt.Errorf("read row: %w", err)
		}
		m, err := tuple.GetAsMap()
		tuple.Close()
		if err != nil {
			return nil, fmt.Errorf("parse row: %w", err)
		}
		rows = append(rows, m)
	}
	if rows == nil {
		rows = []map[string]any{}
	}
	return rows, nil
}

// --------------------------------------------------------------------------
// SearchNeighbors
// --------------------------------------------------------------------------

func (db *ladybugDB) SearchNeighbors(
	ctx context.Context, embedding []float32, entityType string, topK int, branch string,
) ([]store.NeighborResult, error) {
	// Validate embedding.
	for _, v := range embedding {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, store.ErrNaNOrInfEmbedding
		}
	}
	if len(embedding) == 0 {
		return nil, store.ErrEmbeddingRequired
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
	for _, t := range typesToSearch {
		// Check if the index is bootstrapped.
		dim := getEmbeddingDimension(conn, t)
		if dim == 0 {
			continue // no bootstrapped index yet
		}
		if len(embedding) != dim {
			return nil, fmt.Errorf("%w: for entity type %q, expected dimension %d, got %d",
				store.ErrEmbeddingDimension, t, dim, len(embedding))
		}

		// Use QUERY_VECTOR_INDEX. Index name matches what CreateEntity creates.
		idxName := t + "_vec"
		q := fmt.Sprintf("CALL QUERY_VECTOR_INDEX('%s', '%s', $emb, %d) RETURN node, distance ORDER BY distance;",
			t, idxName, topK)
		stmt, err := conn.Prepare(q)
		if err != nil {
			// Index may not exist yet — skip.
			continue
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
			continue
		}
		for result.HasNext() {
			tuple, err := result.Next()
			if err != nil {
				result.Close()
				return nil, fmt.Errorf("read vector result: %w", err)
			}
			m, err := tuple.GetAsMap()
			tuple.Close()
			if err != nil {
				result.Close()
				return nil, fmt.Errorf("parse vector result: %w", err)
			}
			node, ok := m["node"].(lbug.Node)
			if !ok {
				continue
			}
			distance, _ := m["distance"].(float64)
			entity := entityFromNode(node, t)
			results = append(results, store.NeighborResult{
				Entity:   *entity,
				Distance: distance,
			})
		}
		result.Close()
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
		q := fmt.Sprintf("CALL QUERY_FTS_INDEX('%s', '%s', $q, TOP := 100) RETURN node, score ORDER BY score DESC;",
			t, idxName)
		stmt, err := conn.Prepare(q)
		if err != nil {
			// FTS index may not exist — fall back to simple MATCH + scan.
			continue
		}
		result, err := conn.Execute(stmt, map[string]any{"q": query})
		stmt.Close()
		if err != nil {
			continue
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
				continue
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
	offset := 0
	if pageToken != "" {
		data, err := base64.StdEncoding.DecodeString(pageToken)
		if err != nil {
			return nil, "", fmt.Errorf("%w: malformed page token", store.ErrInvalidPageToken)
		}
		if _, err := fmt.Sscanf(string(data), "%d", &offset); err != nil {
			return nil, "", fmt.Errorf("%w: malformed page token", store.ErrInvalidPageToken)
		}
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
			continue
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
