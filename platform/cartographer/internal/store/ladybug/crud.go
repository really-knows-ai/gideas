package ladybug

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/foundry/flow/cartographer/internal/store"
	"github.com/google/uuid"
)

// --------------------------------------------------------------------------
// Entity CRUD
// --------------------------------------------------------------------------

func (db *ladybugDB) CreateEntity(
	ctx context.Context, entityType, id string,
	properties map[string]string, embedding []float32, branch string,
) (*store.Entity, error) {
	conn, typeDefs, unlock, err := db.lockForWrite(branch)
	if err != nil {
		return nil, err
	}
	defer unlock()

	def, ok := typeDefs.entityTypeDefs[entityType]
	if !ok {
		return nil, fmt.Errorf("%w: %q", store.ErrUnknownEntityType, entityType)
	}

	// Validate / generate ID.
	if id == "" {
		id = uuid.New().String()
	} else if err := validateUUID(id); err != nil {
		return nil, err
	}

	// Validate properties against schema.
	propDefs := make(map[string]bool)
	for _, p := range def.Properties {
		propDefs[p.Name] = true
	}
	for key := range properties {
		if !propDefs[key] {
			return nil, fmt.Errorf("%w: %q for entity type %q", store.ErrUnknownProperty, key, entityType)
		}
	}

	// Validate embedding (dimension check requires bootstrapped column).
	if err := validateEmbeddingForCreate(embedding, def); err != nil {
		return nil, err
	}

	// Bootstrap embedding column and vector index on first entity with embedding.
	if def.EnableVectorIndex && len(embedding) > 0 {
		dim := getEmbeddingDimension(conn, entityType)
		if dim == 0 {
			// No embedding column yet — bootstrap it.
			dim = len(embedding)
			altDDL := fmt.Sprintf("ALTER TABLE %s ADD embedding FLOAT[%d];", quoteID(entityType), dim)
			if _, err := conn.Query(altDDL); err != nil {
				return nil, fmt.Errorf("bootstrap embedding column: %w", err)
			}
			idxDDL := fmt.Sprintf("CALL CREATE_VECTOR_INDEX('%s', '%s_vec', 'embedding', metric := 'cosine');",
				entityType, entityType)
			if _, err := conn.Query(idxDDL); err != nil {
				return nil, fmt.Errorf("bootstrap vector index: %w", err)
			}
			// Rebuild schema cache so subsequent SearchNeighbors sees the vector index.
			// This is safe only when we're on main (conn == db.conn), where db.mu is held.
			if conn == db.conn {
				_ = db.rebuildSchemaCacheLocked()
			}
		} else if len(embedding) != dim {
			return nil, fmt.Errorf("%w: expected dimension %d, got %d", store.ErrEmbeddingDimension, dim, len(embedding))
		}
	}

	// Build CREATE query with params.
	assigns := []string{"id: $id"}
	params := map[string]any{"id": id}
	for k, v := range properties {
		pk := "p_" + k
		assigns = append(assigns, quoteID(k)+": $"+pk)
		params[pk] = v
	}
	if def.EnableVectorIndex && len(embedding) > 0 {
		assigns = append(assigns, "embedding: $embedding")
		params["embedding"] = embedding
	}

	q := fmt.Sprintf("CREATE (n:%s {%s});", quoteID(entityType), strings.Join(assigns, ", "))
	stmt, pErr := conn.Prepare(q)
	if pErr != nil {
		return nil, pErr
	}
	_, eErr := conn.Execute(stmt, params)
	stmt.Close()
	if eErr != nil {
		errStr := eErr.Error()
		if strings.Contains(errStr, "duplicate key") || strings.Contains(errStr, "already exists") ||
			strings.Contains(errStr, "primary key") {
			return nil, fmt.Errorf("%w: entity with id %q already exists", store.ErrEntityAlreadyExists, id)
		}
		return nil, eErr
	}

	now := time.Now().UTC()
	props := make(map[string]string, len(properties))
	maps.Copy(props, properties)
	return &store.Entity{
		Id: id, Type: entityType, Properties: props,
		Embedding: embedding, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (db *ladybugDB) UpdateEntity(
	ctx context.Context, id string,
	properties map[string]string, embedding []float32, branch string,
) (*store.Entity, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}

	conn, typeDefs, unlock, err := db.lockForWrite(branch)
	if err != nil {
		return nil, err
	}
	defer unlock()

	// Find the entity's type by probing.
	entity, err := findEntityByID(conn, typeDefs.entityTypeDefs, id)
	if err != nil {
		return nil, err
	}

	def, ok := typeDefs.entityTypeDefs[entity.Type]
	if !ok {
		return nil, fmt.Errorf("%w: %q", store.ErrUnknownEntityType, entity.Type)
	}

	// Validate properties.
	propDefs := make(map[string]bool)
	for _, p := range def.Properties {
		propDefs[p.Name] = true
	}
	for key := range properties {
		if !propDefs[key] {
			return nil, fmt.Errorf("%w: %q for entity type %q", store.ErrUnknownProperty, key, entity.Type)
		}
	}

	// Validate embedding.
	hasNewEmb := len(embedding) > 0
	for _, v := range embedding {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, store.ErrNaNOrInfEmbedding
		}
	}
	if def.EnableVectorIndex && hasNewEmb {
		dim := getEmbeddingDimension(conn, entity.Type)
		if dim > 0 && len(embedding) != dim {
			return nil, fmt.Errorf("%w: expected dimension %d, got %d", store.ErrEmbeddingDimension, dim, len(embedding))
		}
	}

	// Build SET clause.
	var sets []string
	params := map[string]any{"id": id}
	for k, v := range properties {
		pk := "p_" + k
		sets = append(sets, "n."+quoteID(k)+" = $"+pk)
		params[pk] = v
	}
	if def.EnableVectorIndex && hasNewEmb {
		sets = append(sets, "n.embedding = $embedding")
		params["embedding"] = embedding
	}
	if len(sets) == 0 {
		// No-op update — return the entity as-is.
		return entity, nil
	}

	q := fmt.Sprintf("MATCH (n:%s {id: $id}) SET %s;", quoteID(entity.Type), strings.Join(sets, ", "))
	stmt, pErr := conn.Prepare(q)
	if pErr != nil {
		return nil, pErr
	}
	_, eErr := conn.Execute(stmt, params)
	stmt.Close()
	if eErr != nil {
		return nil, eErr
	}

	// Merge properties and return.
	maps.Copy(entity.Properties, properties)
	if def.EnableVectorIndex && hasNewEmb {
		entity.Embedding = embedding
	}
	entity.UpdatedAt = time.Now().UTC()
	return entity, nil
}

func (db *ladybugDB) DeleteEntity(
	ctx context.Context, id, branch string,
) (*store.Entity, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}

	conn, typeDefs, unlock, err := db.lockForWrite(branch)
	if err != nil {
		return nil, err
	}
	defer unlock()

	// Read before delete to return.
	entity, err := findEntityByID(conn, typeDefs.entityTypeDefs, id)
	if err != nil {
		return nil, err
	}

	q := fmt.Sprintf("MATCH (n:%s {id: $id}) DETACH DELETE n;", quoteID(entity.Type))
	stmt, pErr := conn.Prepare(q)
	if pErr != nil {
		return nil, pErr
	}
	_, eErr := conn.Execute(stmt, map[string]any{"id": id})
	stmt.Close()
	if eErr != nil {
		return nil, eErr
	}
	return entity, nil
}

func (db *ladybugDB) GetEntity(
	ctx context.Context, id, branch string,
) (*store.Entity, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}

	conn, typeDefs, unlock, err := db.lockForRead(branch)
	if err != nil {
		return nil, err
	}
	defer unlock()

	return findEntityByID(conn, typeDefs.entityTypeDefs, id)
}

// --------------------------------------------------------------------------
// Edge CRUD
// --------------------------------------------------------------------------

func (db *ladybugDB) CreateEdge(
	ctx context.Context, edgeType, fromID, toID string,
	properties map[string]string, branch string,
) (*store.Edge, error) {
	if err := validateUUID(fromID); err != nil {
		return nil, err
	}
	if err := validateUUID(toID); err != nil {
		return nil, err
	}

	conn, typeDefs, unlock, err := db.lockForWrite(branch)
	if err != nil {
		return nil, err
	}
	defer unlock()

	// Validate edge type exists.
	edef, ok := typeDefs.edgeTypeDefs[edgeType]
	if !ok {
		return nil, fmt.Errorf("%w: %q", store.ErrUnknownEdgeType, edgeType)
	}

	// Verify source/target entities exist and get their types.
	src, err := findEntityByID(conn, typeDefs.entityTypeDefs, fromID)
	if err != nil {
		return nil, fmt.Errorf("%w: source entity %q not found", store.ErrSourceOrTargetNotFound, fromID)
	}
	tgt, err := findEntityByID(conn, typeDefs.entityTypeDefs, toID)
	if err != nil {
		return nil, fmt.Errorf("%w: target entity %q not found", store.ErrSourceOrTargetNotFound, toID)
	}

	// Validate edge properties (including required).
	propDefs := make(map[string]bool)
	for _, p := range edef.Properties {
		propDefs[p.Name] = true
		if p.Required {
			if _, ok := properties[p.Name]; !ok {
				return nil, fmt.Errorf("%w: %q for edge type %q", store.ErrMissingRequiredProperty, p.Name, edgeType)
			}
		}
	}
	for key := range properties {
		if !propDefs[key] {
			return nil, fmt.Errorf("%w: %q for edge type %q", store.ErrUnknownProperty, key, edgeType)
		}
	}

	// Validate edge rules.
	if err := db.validateEdgeRulesFor(typeDefs, src.Type, tgt.Type, edgeType); err != nil {
		return nil, err
	}

	edgeID := uuid.New().String()

	// Build CREATE query.
	var relProps []string
	params := map[string]any{"from": fromID, "to": toID, "id": edgeID}
	for k, v := range properties {
		pk := "p_" + k
		relProps = append(relProps, quoteID(k)+": $"+pk)
		params[pk] = v
	}
	relBody := "id: $id"
	if len(relProps) > 0 {
		relBody += ", " + strings.Join(relProps, ", ")
	}

	q := fmt.Sprintf("MATCH (a {id: $from}), (b {id: $to}) CREATE (a)-[:%s {%s}]->(b);",
		quoteID(edgeType), relBody)
	stmt, pErr := conn.Prepare(q)
	if pErr != nil {
		return nil, pErr
	}
	_, eErr := conn.Execute(stmt, params)
	stmt.Close()
	if eErr != nil {
		return nil, eErr
	}

	now := time.Now().UTC()
	props := make(map[string]string, len(properties))
	maps.Copy(props, properties)
	return &store.Edge{
		Id: edgeID, Type: edgeType,
		FromEntityID: fromID, ToEntityID: toID,
		Properties: props, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (db *ladybugDB) DeleteEdge(
	ctx context.Context, id, branch string,
) (*store.Edge, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}

	conn, typeDefs, unlock, err := db.lockForWrite(branch)
	if err != nil {
		return nil, err
	}
	defer unlock()

	// Find which edge type contains this edge by probing.
	edge, err := findEdgeByID(conn, typeDefs.edgeTypeDefs, id)
	if err != nil {
		return nil, err
	}

	q := fmt.Sprintf("MATCH ()-[r:%s {id: $id}]->() DELETE r;", quoteID(edge.Type))
	stmt, pErr := conn.Prepare(q)
	if pErr != nil {
		return nil, pErr
	}
	_, eErr := conn.Execute(stmt, map[string]any{"id": id})
	stmt.Close()
	if eErr != nil {
		return nil, eErr
	}
	return edge, nil
}

func (db *ladybugDB) GetEdge(
	ctx context.Context, id, branch string,
) (*store.Edge, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}

	conn, typeDefs, unlock, err := db.lockForRead(branch)
	if err != nil {
		return nil, err
	}
	defer unlock()

	return findEdgeByID(conn, typeDefs.edgeTypeDefs, id)
}

func (db *ladybugDB) ListEdgesOfType(
	ctx context.Context, edgeType, branch string,
) ([]store.Edge, error) {
	conn, typeDefs, unlock, err := db.lockForRead(branch)
	if err != nil {
		return nil, err
	}
	defer unlock()

	if _, ok := typeDefs.edgeTypeDefs[edgeType]; !ok {
		return nil, fmt.Errorf("%w: %q", store.ErrUnknownEdgeType, edgeType)
	}

	q := fmt.Sprintf("MATCH (a)-[r:%s]->(b) RETURN a.id, r, b.id;", quoteID(edgeType))
	result, err := conn.Query(q)
	if err != nil {
		return nil, err
	}
	defer result.Close()

	var edges []store.Edge
	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			return nil, fmt.Errorf("read edge row: %w", err)
		}
		m, err := tuple.GetAsMap()
		tuple.Close()
		if err != nil {
			return nil, fmt.Errorf("parse edge row: %w", err)
		}

		fromID := fmt.Sprintf("%v", m["a.id"])
		rel, ok := m["r"].(lbug.Relationship)
		if !ok {
			continue
		}
		toID := fmt.Sprintf("%v", m["b.id"])
		edges = append(edges, *edgeFromRel(rel, edgeType, fromID, toID))
	}
	if edges == nil {
		edges = []store.Edge{}
	}
	return edges, nil
}

// --------------------------------------------------------------------------
// Rules
// --------------------------------------------------------------------------

func (db *ladybugDB) ValidateEdgeRules(sourceType, targetType, edgeType string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.validateEdgeRulesFor(&branchDBCache{db.entityTypeDefs, db.edgeTypeDefs},
		sourceType, targetType, edgeType)
}

func (db *ladybugDB) ResolveEntityType(ctx context.Context, entityID, branch string) (string, error) {
	if err := validateUUID(entityID); err != nil {
		return "", err
	}

	conn, typeDefs, unlock, err := db.lockForRead(branch)
	if err != nil {
		return "", err
	}
	defer unlock()

	entity, err := findEntityByID(conn, typeDefs.entityTypeDefs, entityID)
	if err != nil {
		return "", err
	}
	return entity.Type, nil
}

// --------------------------------------------------------------------------
// Internal helpers
// --------------------------------------------------------------------------

// lockForRead returns the connection and type defs for a branch (or main),
// holding the appropriate read lock. Callers must call the returned unlock func.
func (db *ladybugDB) lockForRead(branch string) (*lbug.Connection, *branchDBCache, func(), error) {
	if branch == "" || branch == "main" {
		db.mu.Lock()
		if db.closed {
			db.mu.Unlock()
			return nil, nil, nil, store.ErrDatabaseNotReady
		}
		return db.conn, &branchDBCache{db.entityTypeDefs, db.edgeTypeDefs}, db.mu.Unlock, nil
	}
	db.mu.Lock()
	br, ok := db.branches[branch]
	if !ok {
		db.mu.Unlock()
		return nil, nil, nil, fmt.Errorf("branch %q not found", branch)
	}
	br.mu.Lock()
	db.mu.Unlock()
	return br.conn, &branchDBCache{br.entityTypeDefs, br.edgeTypeDefs}, br.mu.Unlock, nil
}

// lockForWrite returns the connection and type defs for write operations.
// Same pattern as lockForRead but both paths are identical for the cache layer.
func (db *ladybugDB) lockForWrite(branch string) (*lbug.Connection, *branchDBCache, func(), error) {
	// ponytail: Write path uses the same lock pattern as read; for the LadybugDB
	// C library, concurrent writes within one connection are serialized below the
	// Go layer. If contention becomes a bottleneck, promote to a write-preferring
	// RWMutex or a dedicated write connection.
	return db.lockForRead(branch)
}

// branchDBCache is a lightweight view of the type definitions for a branch or main DB.
type branchDBCache struct {
	entityTypeDefs map[string]*store.EntityTypeDef
	edgeTypeDefs   map[string]*store.EdgeTypeDef
}

// findEntityByID probes each entity type table looking for the given ID.
// ponytail: O(#entity_types) scan. Upgrade path: maintain a global ID→type index.
func findEntityByID(conn *lbug.Connection, typeDefs map[string]*store.EntityTypeDef, id string) (*store.Entity, error) {
	for typeName := range typeDefs {
		q := fmt.Sprintf("MATCH (n:%s {id: $id}) RETURN n;", quoteID(typeName))
		stmt, err := conn.Prepare(q)
		if err != nil {
			continue
		}
		result, err := conn.Execute(stmt, map[string]any{"id": id})
		stmt.Close()
		if err != nil {
			continue
		}
		if result.HasNext() {
			tuple, err := result.Next()
			if err != nil {
				result.Close()
				continue
			}
			m, err := tuple.GetAsMap()
			tuple.Close()
			result.Close()
			if err != nil {
				continue
			}
			node, ok := m["n"].(lbug.Node)
			if !ok {
				continue
			}
			return entityFromNode(node, typeName), nil
		}
		result.Close()
	}
	return nil, fmt.Errorf("%w: entity with id %q", store.ErrEntityNotFound, id)
}

// findEdgeByID probes each edge type table for the given edge ID.
// ponytail: O(#edge_types) scan. Upgrade path: maintain a global ID→edge type index.
func findEdgeByID(conn *lbug.Connection, typeDefs map[string]*store.EdgeTypeDef, id string) (*store.Edge, error) {
	for typeName := range typeDefs {
		q := fmt.Sprintf("MATCH (s)-[r:%s {id: $id}]->(t) RETURN s.id, t.id, r;", quoteID(typeName))
		stmt, err := conn.Prepare(q)
		if err != nil {
			continue
		}
		result, err := conn.Execute(stmt, map[string]any{"id": id})
		stmt.Close()
		if err != nil {
			continue
		}
		if result.HasNext() {
			tuple, err := result.Next()
			if err != nil {
				result.Close()
				continue
			}
			m, err := tuple.GetAsMap()
			tuple.Close()
			result.Close()
			if err != nil {
				continue
			}
			fromID := fmt.Sprintf("%v", m["s.id"])
			toID := fmt.Sprintf("%v", m["t.id"])
			rel, ok := m["r"].(lbug.Relationship)
			if !ok {
				continue
			}
			return edgeFromRel(rel, typeName, fromID, toID), nil
		}
		result.Close()
	}
	return nil, fmt.Errorf("%w: edge with id %q", store.ErrEdgeNotFound, id)
}

// entityFromNode converts a LadybugDB Node to a store.Entity.
func entityFromNode(node lbug.Node, entityType string) *store.Entity {
	e := &store.Entity{
		Id:         fmt.Sprintf("%v", node.Properties["id"]),
		Type:       entityType,
		Properties: make(map[string]string),
	}
	for k, v := range node.Properties {
		if k == "id" || k == "_properties" || k == "embedding" {
			continue
		}
		e.Properties[k] = fmt.Sprintf("%v", v)
	}
	// Extract embedding.
	if raw, ok := node.Properties["embedding"]; ok {
		if list, ok := raw.([]any); ok {
			emb := make([]float32, len(list))
			for i, v := range list {
				switch val := v.(type) {
				case float32:
					emb[i] = val
				case float64:
					emb[i] = float32(val)
				}
			}
			e.Embedding = emb
		}
	}
	return e
}

// edgeFromRel converts a LadybugDB Relationship to a store.Edge.
func edgeFromRel(rel lbug.Relationship, edgeType, fromID, toID string) *store.Edge {
	e := &store.Edge{
		Id:           fmt.Sprintf("%v", rel.Properties["id"]),
		Type:         edgeType,
		FromEntityID: fromID,
		ToEntityID:   toID,
		Properties:   make(map[string]string),
	}
	for k, v := range rel.Properties {
		if k == "id" || k == "_properties" {
			continue
		}
		e.Properties[k] = fmt.Sprintf("%v", v)
	}
	return e
}

// validateUUID checks that the given string is a valid UUID v4.
func validateUUID(id string) error {
	u, err := uuid.Parse(id)
	if err != nil || u.Version() != 4 {
		return fmt.Errorf("%w: %q", store.ErrInvalidIDFormat, id)
	}
	return nil
}

// validateEmbeddingForCreate validates embedding for a CreateEntity call.
func validateEmbeddingForCreate(embedding []float32, def *store.EntityTypeDef) error {
	for _, v := range embedding {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return store.ErrNaNOrInfEmbedding
		}
	}
	if !def.EnableVectorIndex {
		return nil
	}
	if len(embedding) == 0 {
		return fmt.Errorf("%w: entity type %q has vector index enabled but no embedding provided",
			store.ErrVectorBootstrap, def.Name)
	}
	return nil
}

// getEmbeddingDimension queries the FLOAT[n] column type to determine dimension.
func getEmbeddingDimension(conn *lbug.Connection, entityType string) int {
	q := fmt.Sprintf("CALL table_info('%s') RETURN *;", entityType)
	result, err := conn.Query(q)
	if err != nil {
		return 0
	}
	defer result.Close()

	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			return 0
		}
		vals, err := tuple.GetAsSlice()
		tuple.Close()
		if err != nil || len(vals) < 3 {
			continue
		}
		colName := fmt.Sprintf("%v", vals[1])
		if colName != "embedding" {
			continue
		}
		colType := fmt.Sprintf("%v", vals[2])
		// Parse FLOAT[n]
		if strings.HasPrefix(colType, "FLOAT[") && strings.HasSuffix(colType, "]") {
			var dim int
			if _, err := fmt.Sscanf(colType, "FLOAT[%d]", &dim); err == nil {
				return dim
			}
		}
	}
	return 0
}

// validateEdgeRulesFor validates that sourceType->targetType via edgeType is allowed.
// It checks the sourceType's ConnectionRules (stored in ruleIndex) to see if the
// targetType and edgeType are permitted. If the source type has no rules, all
// connections are denied; if it has rules, at least one rule must match.
func (db *ladybugDB) validateEdgeRulesFor(typeDefs *branchDBCache,
	sourceType, targetType, edgeType string) error {
	if _, ok := typeDefs.entityTypeDefs[sourceType]; !ok {
		return fmt.Errorf("%w: %q", store.ErrUnknownEntityType, sourceType)
	}
	if _, ok := typeDefs.entityTypeDefs[targetType]; !ok {
		return fmt.Errorf("%w: %q", store.ErrUnknownEntityType, targetType)
	}
	if _, ok := typeDefs.edgeTypeDefs[edgeType]; !ok {
		return fmt.Errorf("%w: %q", store.ErrUnknownEdgeType, edgeType)
	}

	rules := db.ruleIndex[sourceType]
	if len(rules) == 0 {
		// No rules means no connections are permitted — return rule violation.
		return fmt.Errorf("%w: entity type %q has no connection rules, cannot connect to %q via %q",
			store.ErrEdgeRuleViolation, sourceType, targetType, edgeType)
	}

	// Check if any rule permits this connection.
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		usesEdge := slices.Contains(rule.Using, edgeType)
		if !usesEdge {
			continue
		}
		if slices.Contains(rule.CanConnectTo, targetType) {
			return nil // permitted
		}
	}

	return fmt.Errorf("%w: entity type %q cannot connect to %q via %q",
		store.ErrEdgeRuleViolation, sourceType, targetType, edgeType)
}
