package ladybug

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/foundry/flow/cartographer/internal/store"
	"github.com/foundry/flow/cartographer/internal/uuidutil"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/google/uuid"
)

// --------------------------------------------------------------------------
// Entity CRUD
// --------------------------------------------------------------------------

const mainBranch = "main"

//nolint:gocyclo
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

	// Structural validation runs before data-integrity checks (SPEC RPC
	// check-order: CreateEntity: structural validation → data-integrity). An
	// unknown or missing-required property is therefore reported as
	// INVALID_ARGUMENT even when the explicit id already exists.
	propDefs := make(map[string]bool)
	for _, p := range def.Properties {
		propDefs[p.Name] = true
	}
	for key := range properties {
		if !propDefs[key] {
			return nil, fmt.Errorf("%w: %q for entity type %q", store.ErrUnknownProperty, key, entityType)
		}
	}
	for _, property := range def.Properties {
		if _, ok := properties[property.Name]; property.Required && !ok {
			return nil, fmt.Errorf(
				"%w: %q for entity type %q", store.ErrMissingRequiredProperty, property.Name, entityType,
			)
		}
	}

	// Determine the bootstrapped embedding dimension (0 if the vector column
	// has not been created yet for this entity type).
	dim := 0
	if def.EnableVectorIndex {
		var derr error
		dim, derr = getEmbeddingDimension(conn, entityType, def.EnableVectorIndex)
		if derr != nil {
			return nil, fmt.Errorf("read embedding dimension for %q: %w", entityType, derr)
		}
	}

	// Validate embedding (dimension check requires bootstrapped column).
	if err := validateEmbeddingForCreate(embedding, def, dim); err != nil {
		return nil, err
	}
	// An established-dimension mismatch is structural too (SPEC R7:
	// INVALID_ARGUMENT). It is checked here so the data-integrity probe below
	// never masks it (SPEC:946 CreateEntity check-order: structural validation
	// → data-integrity).
	if def.EnableVectorIndex && len(embedding) > 0 && dim != 0 && len(embedding) != dim {
		return nil, fmt.Errorf("%w: expected dimension %d, got %d", store.ErrEmbeddingDimension, dim, len(embedding))
	}

	// Durable duplicate detection: a duplicate-ID create must return
	// ErrEntityAlreadyExists regardless of the underlying DB's error text.
	// LadybugDB's error message is a message, not a contract, so instead of
	// substring-matching it we probe for an existing entity up-front. This is
	// O(#entity_types) per create; CreateEntity is not fast enough to warrant
	// an ID→type index for this (ponytail: upgrade path is a global ID→type
	// index shared with findEntityByID). The probe is the data-integrity check
	// of the SPEC:946 check-order — every structural check (type, id format,
	// properties, embedding) has already run — and it sits before the
	// write-path DDL so a duplicate-ID create never locks the vector dimension.
	if _, perr := findEntityByID(conn, typeDefs.entityTypeDefs, id); perr == nil {
		return nil, fmt.Errorf("%w: entity with id %q already exists", store.ErrEntityAlreadyExists, id)
	} else if !errors.Is(perr, store.ErrEntityNotFound) {
		// The probe itself failed for a non-"not found" reason — propagate it
		// rather than masking a real read failure behind the INSERT.
		return nil, perr
	}

	// Bootstrap embedding column and vector index on first entity with embedding.
	// SPEC R7: neither the FLOAT[n] embedding column nor the vector index is
	// created at schema-application time — both are deferred to the first
	// embedding write for the entity type (mirrored in UpdateEntity and the
	// file/branch load paths). LadybugDB can host a vector index only on a
	// dimensioned FLOAT[n] column, and the dimension is inferred from this first
	// embedding (the CRD exposes no dimension field to size the column at apply
	// time), so the column and its index are created together here, locking the
	// dimension. Until the first embedding the type's table has no embedding
	// column (table_info shows none); an embedding write is the first mutation
	// that can fail on a column/index DDL error (surfaced loudly, marking the
	// store failed). Every SPEC-observable behavior holds: the index stays lazy,
	// the dimension locks on the first embedding, ErrVectorBootstrap rejects
	// pre-bootstrap no-embedding creates, and post-bootstrap no-embedding
	// creates store NULL.
	if def.EnableVectorIndex && len(embedding) > 0 {
		if dim == 0 {
			// No embedding column yet — bootstrap it.
			dim = len(embedding)
			altDDL := fmt.Sprintf("ALTER TABLE %s ADD embedding FLOAT[%d];", quoteID(entityType), dim)
			r, err := conn.Query(altDDL)
			if err != nil {
				return nil, fmt.Errorf("bootstrap embedding column: %w", err)
			}
			r.Close()
			if err := db.createVectorIndex(conn, entityType); err != nil {
				typeDefs.markFailed()
				return nil, fmt.Errorf("bootstrap vector index: %w", err)
			}
			if db.path != "" {
				pairs, perr := connectionEdgePairs(conn, typeDefs.edgeTypeDefs)
				if perr != nil {
					typeDefs.markFailed()
					return nil, fmt.Errorf("capture relationship endpoints: %w", perr)
				}
				metadata := metadataFromDefinitions(typeDefs.entityTypeDefs, typeDefs.edgeTypeDefs, pairs)
				metadata, err = captureVectorState(conn, metadata)
				if err != nil {
					typeDefs.markFailed()
					return nil, fmt.Errorf("capture vector schema metadata: %w", err)
				}
				path := db.mainMetadataPath()
				if branch != "" && branch != mainBranch {
					path = db.branchMetadataPath(branch)
				}
				if err := db.writeMetadata(path, metadata); err != nil {
					typeDefs.markFailed()
					return nil, fmt.Errorf("persist vector schema metadata: %w", err)
				}
			}
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
	r, eErr := conn.Execute(stmt, params)
	stmt.Close()
	if eErr != nil {
		return nil, eErr
	}
	r.Close()

	now := time.Now().UTC()
	props := make(map[string]string, len(properties))
	maps.Copy(props, properties)
	// Only surface the embedding in the return value when it was actually
	// persisted. Non-indexed types discard the embedding, and a zero-embedding
	// create stores NULL in the vector column (SPEC R7).
	returnedEmbedding := embedding
	if !def.EnableVectorIndex || len(embedding) == 0 {
		returnedEmbedding = nil
	}
	return &store.Entity{
		Id: id, Type: entityType, Properties: props,
		Embedding: returnedEmbedding, CreatedAt: now, UpdatedAt: now,
	}, nil
}

//nolint:gocyclo
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

	// Validate and bootstrap embedding. Mirrors CreateEntity's embedding handling
	// (SPEC R7 parity): if the embedding column has not been bootstrapped yet
	// (dim == 0), the first update supplying an embedding bootstraps it; a
	// subsequent update must match the established dimension. Unlike a create,
	// an update rewrites an existing row's embedding, which LadybugDB refuses
	// while the vector index exists ("Cannot set property ... because it is
	// used in one or more indexes"), so a matching-dimension update drops the
	// index before the write and recreates it after; the bootstrap path defers
	// index creation until after the write for the same reason. The FLOAT[n]
	// column (and its locked dimension) is untouched by either operation, so
	// the SPEC-visible rejection surface for an UpdateEntity embedding stays
	// exactly the two error-table rows: dimension mismatch (R7 §1(b)) and
	// NaN/Inf (R7 §1(c)) — a matching, NaN-free embedding update succeeds.
	hasNewEmb := len(embedding) > 0
	for _, v := range embedding {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, store.ErrNaNOrInfEmbedding
		}
	}
	embeddingWritable := def.EnableVectorIndex && hasNewEmb
	recreateIndex := false
	bootstrappedColumn := false
	if embeddingWritable {
		dim, derr := getEmbeddingDimension(conn, entity.Type, def.EnableVectorIndex)
		if derr != nil {
			return nil, fmt.Errorf("read embedding dimension for %q: %w", entity.Type, derr)
		}
		if dim == 0 {
			// No embedding column yet — bootstrap it (same as CreateEntity).
			// The vector index is created after the write below.
			dim = len(embedding)
			altDDL := fmt.Sprintf("ALTER TABLE %s ADD embedding FLOAT[%d];", quoteID(entity.Type), dim)
			r, err := conn.Query(altDDL)
			if err != nil {
				return nil, fmt.Errorf("bootstrap embedding column: %w", err)
			}
			r.Close()
			bootstrappedColumn = true
			recreateIndex = true
		} else if len(embedding) != dim {
			return nil, fmt.Errorf("%w: expected dimension %d, got %d", store.ErrEmbeddingDimension, dim, len(embedding))
		} else {
			// Established dimension with a matching embedding — drop the vector
			// index so the row's embedding can be rewritten in place.
			recreateIndex = true
			if ok, ierr := vectorIndexExists(conn, entity.Type); ierr != nil {
				return nil, fmt.Errorf("check vector index for %q: %w", entity.Type, ierr)
			} else if ok {
				r, derr := conn.Query(fmt.Sprintf("CALL DROP_VECTOR_INDEX('%s', '%s_vec');", entity.Type, entity.Type))
				if derr != nil {
					return nil, fmt.Errorf("drop vector index for embedding update: %w", derr)
				}
				r.Close()
			}
		}
	}

	// Build SET clause (the embedding is included when this update writes one).
	var sets []string
	params := map[string]any{"id": id}
	for k, v := range properties {
		pk := "p_" + k
		sets = append(sets, "n."+quoteID(k)+" = $"+pk)
		params[pk] = v
	}
	if embeddingWritable {
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
	r, eErr := conn.Execute(stmt, params)
	stmt.Close()
	if eErr != nil {
		// The established-dimension path dropped the index for the rewrite;
		// restore it so the store stays consistent before surfacing the error.
		if recreateIndex && !bootstrappedColumn {
			if cerr := db.createVectorIndex(conn, entity.Type); cerr != nil {
				typeDefs.markFailed()
				return nil, fmt.Errorf("write embedding: %v; restore vector index: %w", eErr, cerr)
			}
		}
		return nil, eErr
	}
	r.Close()

	// Recreate the vector index over the rewritten embedding (dropped before
	// the write, or deferred past it on the bootstrap path). The bootstrap path
	// also persists the schema metadata capturing the new vector state; the
	// established-dimension path leaves the metadata unchanged.
	if recreateIndex {
		if err := db.createVectorIndex(conn, entity.Type); err != nil {
			typeDefs.markFailed()
			return nil, fmt.Errorf("recreate vector index: %w", err)
		}
		if bootstrappedColumn && db.path != "" {
			pairs, perr := connectionEdgePairs(conn, typeDefs.edgeTypeDefs)
			if perr != nil {
				typeDefs.markFailed()
				return nil, fmt.Errorf("capture relationship endpoints: %w", perr)
			}
			metadata := metadataFromDefinitions(typeDefs.entityTypeDefs, typeDefs.edgeTypeDefs, pairs)
			metadata, err := captureVectorState(conn, metadata)
			if err != nil {
				typeDefs.markFailed()
				return nil, fmt.Errorf("capture vector schema metadata: %w", err)
			}
			path := db.mainMetadataPath()
			if branch != "" && branch != mainBranch {
				path = db.branchMetadataPath(branch)
			}
			if err := db.writeMetadata(path, metadata); err != nil {
				typeDefs.markFailed()
				return nil, fmt.Errorf("persist vector schema metadata: %w", err)
			}
		}
	}

	// Merge properties and return. The embedding is only surfaced when this
	// update actually persisted one (SPEC R7: a non-indexed type's embedding is
	// accepted but discarded, and a no-embedding update leaves the stored value
	// unchanged).
	maps.Copy(entity.Properties, properties)
	if embeddingWritable {
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
	r, eErr := conn.Execute(stmt, map[string]any{"id": id})
	stmt.Close()
	if eErr != nil {
		return nil, eErr
	}
	r.Close()
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

	// Structural validation runs before the source/target entity-existence
	// check (SPEC RPC check-order: CreateEdge: structural → entity existence →
	// capability → edge-rule auth). An unknown or missing-required edge
	// property is therefore reported as INVALID_ARGUMENT even when the source
	// or target entity does not exist — the NOT_FOUND must never mask the
	// structural error (SPEC error table: unknown/missing edge property →
	// INVALID_ARGUMENT).
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

	// Verify source/target entities exist and get their types. Only a genuine
	// "entity not found" from the probe maps to ErrSourceOrTargetNotFound (SPEC
	// error table: "Source or target entity not found on CreateEdge" →
	// NOT_FOUND); a real DB failure (Prepare/Execute) must propagate as an
	// operational error instead of being masked as a client-visible NOT_FOUND.
	src, err := findEntityByID(conn, typeDefs.entityTypeDefs, fromID)
	if err != nil {
		if !errors.Is(err, store.ErrEntityNotFound) {
			return nil, fmt.Errorf("resolve source entity %q: %w", fromID, err)
		}
		return nil, fmt.Errorf("%w: source entity %q not found", store.ErrSourceOrTargetNotFound, fromID)
	}
	tgt, err := findEntityByID(conn, typeDefs.entityTypeDefs, toID)
	if err != nil {
		if !errors.Is(err, store.ErrEntityNotFound) {
			return nil, fmt.Errorf("resolve target entity %q: %w", toID, err)
		}
		return nil, fmt.Errorf("%w: target entity %q not found", store.ErrSourceOrTargetNotFound, toID)
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
	r, eErr := conn.Execute(stmt, params)
	stmt.Close()
	if eErr != nil {
		return nil, eErr
	}
	r.Close()

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
	r, eErr := conn.Execute(stmt, map[string]any{"id": id})
	stmt.Close()
	if eErr != nil {
		return nil, eErr
	}
	r.Close()
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

	// Shared conn-level edge listing (same MATCH (a)-[r:T]->(b) RETURN a.id, r,
	// b.id query and GetAsMap/edgeFromRel parse loop used by DumpAllEdges and
	// RehydrateFromBranch).
	return listEdgesOnConn(conn, edgeType)
}

// --------------------------------------------------------------------------
// Rules
// --------------------------------------------------------------------------

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
	if branch == "" || branch == mainBranch {
		db.mu.Lock()
		if db.closed || db.failed {
			db.mu.Unlock()
			return nil, nil, nil, store.ErrDatabaseNotReady
		}
		return db.conn, &branchDBCache{
			entityTypeDefs: db.entityTypeDefs,
			edgeTypeDefs:   db.edgeTypeDefs,
			ruleIndex:      db.ruleIndex,
			markFailed:     func() { db.failed = true },
		}, db.mu.Unlock, nil
	}
	db.mu.Lock()
	br, err := db.branchLocked(branch)
	if err != nil {
		db.mu.Unlock()
		return nil, nil, nil, err
	}
	br.mu.Lock()
	// Snapshot main's ruleIndex while still holding db.mu so the cache below
	// (guarded by br.mu) never races concurrent ApplySchema/WipeSchema writes.
	// The maps themselves are never mutated in place — they are replaced — so a
	// shallow copy of the map header under db.mu is race-safe.
	ruleIndex := maps.Clone(db.ruleIndex)
	db.mu.Unlock()
	if br.failed {
		br.mu.Unlock()
		return nil, nil, nil, store.ErrDatabaseNotReady
	}
	return br.conn, &branchDBCache{
		entityTypeDefs: br.entityTypeDefs,
		edgeTypeDefs:   br.edgeTypeDefs,
		ruleIndex:      ruleIndex,
		markFailed:     func() { br.failed = true },
	}, br.mu.Unlock, nil
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
	ruleIndex      map[string][]*flowv1.ConnectionRule
	markFailed     func()
}

func createVectorIndexOnConn(conn *lbug.Connection, entityType string) error {
	ddl := fmt.Sprintf("CALL CREATE_VECTOR_INDEX('%s', '%s_vec', 'embedding', metric := 'cosine');",
		entityType, entityType)
	r, err := conn.Query(ddl)
	if err != nil {
		return err
	}
	r.Close()
	return nil
}

// findEntityByID probes each entity type table looking for the given ID.
// ponytail: O(#entity_types) scan. Upgrade path: maintain a global ID→type index.
func findEntityByID(conn *lbug.Connection, typeDefs map[string]*store.EntityTypeDef, id string) (*store.Entity, error) {
	for typeName := range typeDefs {
		q := fmt.Sprintf("MATCH (n:%s {id: $id}) RETURN n;", quoteID(typeName))
		stmt, err := conn.Prepare(q)
		if err != nil {
			return nil, fmt.Errorf("prepare entity query for %q: %w", typeName, err)
		}
		result, err := conn.Execute(stmt, map[string]any{"id": id})
		stmt.Close()
		if err != nil {
			return nil, fmt.Errorf("execute entity query for %q: %w", typeName, err)
		}
		if result.HasNext() {
			tuple, err := result.Next()
			if err != nil {
				result.Close()
				return nil, fmt.Errorf("read entity row for %q: %w", typeName, err)
			}
			m, err := tuple.GetAsMap()
			tuple.Close()
			result.Close()
			if err != nil {
				return nil, fmt.Errorf("parse entity row for %q: %w", typeName, err)
			}
			node, ok := m["n"].(lbug.Node)
			if !ok {
				return nil, fmt.Errorf("unexpected type in entity result for %q: got %T, expected Node", typeName, m["n"])
			}
			return entityFromNode(node, typeName, typeDefs[typeName].EnableVectorIndex), nil
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
			return nil, fmt.Errorf("prepare edge query for %q: %w", typeName, err)
		}
		result, err := conn.Execute(stmt, map[string]any{"id": id})
		stmt.Close()
		if err != nil {
			return nil, fmt.Errorf("execute edge query for %q: %w", typeName, err)
		}
		if result.HasNext() {
			tuple, err := result.Next()
			if err != nil {
				result.Close()
				return nil, fmt.Errorf("read edge row for %q: %w", typeName, err)
			}
			m, err := tuple.GetAsMap()
			tuple.Close()
			result.Close()
			if err != nil {
				return nil, fmt.Errorf("parse edge row for %q: %w", typeName, err)
			}
			fromID := fmt.Sprintf("%v", m["s.id"])
			toID := fmt.Sprintf("%v", m["t.id"])
			rel, ok := m["r"].(lbug.Relationship)
			if !ok {
				return nil, fmt.Errorf("unexpected type in edge result for %q: got %T, expected Relationship", typeName, m["r"])
			}
			return edgeFromRel(rel, typeName, fromID, toID), nil
		}
		result.Close()
	}
	return nil, fmt.Errorf("%w: edge with id %q", store.ErrEdgeNotFound, id)
}

// entityFromNode converts a LadybugDB Node to a store.Entity. The embedding key
// is only structural for a vector-enabled type (its FLOAT[] vector column); a
// non-vector entity type may legally declare a property named `embedding`
// (schema.Validate rejects the name only for enableVectorIndex types, and SPEC
// R1 reserves it only in that position), so it is skipped — and only extracted
// as a vector — when vectorIndexed is true. Skipping it unconditionally would
// silently drop a legal property on every read.
func entityFromNode(node lbug.Node, entityType string, vectorIndexed bool) *store.Entity {
	e := &store.Entity{
		Id:         fmt.Sprintf("%v", node.Properties["id"]),
		Type:       entityType,
		Properties: make(map[string]string),
	}
	for k, v := range node.Properties {
		if k == "id" {
			continue
		}
		if vectorIndexed && k == "embedding" {
			continue
		}
		if v == nil {
			// A declared-but-unset column is NULL in the DB. It is not a
			// property value and must not surface as the literal string
			// "<nil>"; the gitstore boundary and SPEC R11 export output both
			// model an unset property as absent.
			continue
		}
		e.Properties[k] = fmt.Sprintf("%v", v)
	}
	// Extract embedding. Only a vector-enabled type's embedding column is a
	// vector: on a non-vector type the embedding key is a real property (kept
	// above) whose value is a string, so the []any assertion fails and nothing
	// is extracted.
	if vectorIndexed {
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
		if k == "id" {
			continue
		}
		if v == nil {
			// Same NULL-column semantics as entityFromNode: an unset declared
			// property is absent, never the string "<nil>".
			continue
		}
		e.Properties[k] = fmt.Sprintf("%v", v)
	}
	return e
}

// validateUUID checks that the given string is a valid UUID v4.
func validateUUID(id string) error {
	if err := uuidutil.Validate(id); err != nil {
		return fmt.Errorf("%w: %q", store.ErrInvalidIDFormat, id)
	}
	return nil
}

// validateEmbeddingForCreate validates embedding for a CreateEntity call.
// bootstrappedDim is the established vector column dimension (0 if not yet
// bootstrapped). A zero-embedding create is only rejected when the dimension
// has not been bootstrapped yet — per SPEC R7, subsequent CreateEntity calls
// may omit the embedding once the dimension is locked in.
func validateEmbeddingForCreate(embedding []float32, def *store.EntityTypeDef, bootstrappedDim int) error {
	for _, v := range embedding {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return store.ErrNaNOrInfEmbedding
		}
	}
	if !def.EnableVectorIndex {
		return nil
	}
	if len(embedding) == 0 && bootstrappedDim == 0 {
		return fmt.Errorf("%w: entity type %q has vector index enabled but no embedding provided",
			store.ErrVectorBootstrap, def.Name)
	}
	return nil
}

// getEmbeddingDimension queries the FLOAT[n] column type to determine the
// dimension. It returns (0, nil) when the embedding column has not been
// bootstrapped for the entity type — including when the node table has not
// been created yet (fresh schema, SPEC R7 lazy bootstrap) — and (dim, nil)
// when it has. It returns a non-nil error when a vector-enabled type's
// embedding column is present but not a parseable FLOAT[n] (anomalous), so
// callers can distinguish "no dimension locked in yet" and "schema read
// failed" from a present-but-anomalous column instead of treating every
// non-empty case as "not bootstrapped". A non-vector entity type may legally
// declare a property named `embedding` (schema.Validate rejects the name only
// for enableVectorIndex types), which creates a plain STRING column — that is
// a real property, not an anomalous vector column, so a non-FLOAT[n] embedding
// column on a non-vector type (vectorIndexed false) reports "not bootstrapped"
// (0, nil). Table existence is checked via CALL show_tables() — a durable
// predicate — rather than by matching LadybugDB's query error text (which is a
// message, not a contract).
func getEmbeddingDimension(conn *lbug.Connection, entityType string, vectorIndexed bool) (int, error) {
	// A node table that does not exist has no embedding column yet; that is
	// the "not bootstrapped" state, not a read failure.
	exists, err := connTableExists(conn, entityType)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}

	qr, err := conn.Query(fmt.Sprintf("CALL table_info('%s') RETURN *;", entityType))
	if err != nil {
		return 0, fmt.Errorf("query table info for %q: %w", entityType, err)
	}
	result := qr
	defer result.Close()

	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			return 0, fmt.Errorf("read table info row for %q: %w", entityType, err)
		}
		vals, err := tuple.GetAsSlice()
		tuple.Close()
		if err != nil {
			return 0, fmt.Errorf("parse table info row for %q: %w", entityType, err)
		}
		if len(vals) < 3 {
			continue
		}
		colName := fmt.Sprintf("%v", vals[1])
		if colName != "embedding" {
			continue
		}
		colType := fmt.Sprintf("%v", vals[2])
		// Parse FLOAT[n]. A present embedding column whose type is not a
		// parseable FLOAT[n] is only anomalous for a vector-enabled type (it
		// should only ever be created here with a FLOAT[n] shape), so surface
		// it rather than treating it as "not bootstrapped" — callers that
		// recreate the index over a malformed column would silently corrupt
		// the vector store. A non-vector type's `embedding` property column is
		// a real property and reports "not bootstrapped" (0).
		if strings.HasPrefix(colType, "FLOAT[") && strings.HasSuffix(colType, "]") {
			var dim int
			if _, err := fmt.Sscanf(colType, "FLOAT[%d]", &dim); err == nil && dim > 0 {
				return dim, nil
			}
		}
		if !vectorIndexed {
			return 0, nil
		}
		return 0, fmt.Errorf("entity type %q has an anomalous embedding column type %q", entityType, colType)
	}
	return 0, nil
}

// connTableExists reports whether a NODE table with the given name exists in
// the catalog, using the durable show_tables() predicate rather than any
// error-text matching.
func connTableExists(conn *lbug.Connection, name string) (bool, error) {
	result, err := conn.Query("CALL show_tables() RETURN *;")
	if err != nil {
		return false, fmt.Errorf("list tables: %w", err)
	}
	defer result.Close()
	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			return false, fmt.Errorf("read table row: %w", err)
		}
		values, err := tuple.GetAsSlice()
		tuple.Close()
		if err != nil {
			return false, fmt.Errorf("parse table row: %w", err)
		}
		if len(values) >= 3 && fmt.Sprintf("%v", values[1]) == name {
			return true, nil
		}
	}
	return false, nil
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

	rules := typeDefs.ruleIndex[sourceType]
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
