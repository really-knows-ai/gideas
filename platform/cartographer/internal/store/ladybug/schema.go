package ladybug

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/foundry/flow/cartographer/internal/schema"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Catalog cache
// ---------------------------------------------------------------------------

// rebuildSchemaCache queries the LadybugDB catalog to populate
// entityTypeDefs and edgeTypeDefs (acquires lock).
func (db *ladybugDB) rebuildSchemaCache() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.rebuildSchemaCacheLocked()
}

// collectVectorIndexes returns a set of table names that have a vector index.
// LadybugDB's vector extension creates indexes of type HNSW.
func (db *ladybugDB) collectVectorIndexes() (map[string]bool, error) {
	idxMap := make(map[string]bool)

	result, err := db.conn.Query("CALL show_indexes() RETURN *;")
	if err != nil {
		// Propagate, never swallow: rebuildSchemaCacheLocked treats a nil error
		// as authoritative vector state, so a catalog-read failure returning
		// (empty map, nil) would silently strip vector state from every type
		// read on schema-cache rebuild (Open and metadata-table restoration).
		// The caller propagates this error already, so the read path fails
		// loudly instead of silently marking every type non-vector.
		return nil, err
	}
	defer result.Close()

	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			return nil, fmt.Errorf("read index row: %w", err)
		}

		vals, err := tuple.GetAsSlice()
		tuple.Close()
		if err != nil {
			return nil, fmt.Errorf("get index values: %w", err)
		}

		// Columns from show_indexes: table_name, index_name, index_type,
		// property_names, extension_loaded, index_definition
		if len(vals) < 3 {
			continue
		}
		tableName := fmt.Sprintf("%v", vals[0])
		indexType := fmt.Sprintf("%v", vals[2])

		// Only HNSW (vector) indexes count as vector indexes.
		if tableName != "" && strings.EqualFold(indexType, "HNSW") {
			idxMap[tableName] = true
		}
	}

	return idxMap, nil
}

// indexExistsOnConn reports whether the given table has an index with the
// given name, using the LadybugDB show_indexes catalog (columns: table_name,
// index_name, index_type, property_names, ...). The catalog-read error is
// propagated, never swallowed: the callers use this as an existence guard
// before issuing DROP/CREATE index DDL, and a catalog-read failure reporting
// "no index" would let WipeSchema's dropTable proceed past a residual
// vector/FTS index pointing at a vanished table or skip a needed index
// (re)creation. A genuine create/drop error still surfaces downstream, but the
// guard itself must fail loudly, not silently report "index absent".
func indexExistsOnConn(conn *lbug.Connection, table, index string) (bool, error) {
	result, err := conn.Query("CALL show_indexes() RETURN *;")
	if err != nil {
		return false, err
	}
	defer result.Close()
	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			return false, err
		}
		vals, err := tuple.GetAsSlice()
		tuple.Close()
		if err != nil {
			return false, err
		}
		if len(vals) < 2 {
			continue
		}
		if fmt.Sprintf("%v", vals[0]) == table && fmt.Sprintf("%v", vals[1]) == index {
			return true, nil
		}
	}
	return false, nil
}

// ftsIndexExists reports whether the table already carries its _fts full-text
// index, so CREATE_FTS_INDEX is only issued for a table that lacks one.
func ftsIndexExists(conn *lbug.Connection, table string) (bool, error) {
	return indexExistsOnConn(conn, table, table+"_fts")
}

// rebuildFTSIndexForTable drops and recreates the _fts index over the given
// string property set. CREATE_FTS_INDEX is not idempotent (it errors if the
// index already exists), so any existing _fts index is dropped first. Dropping
// is only attempted when the index is known to exist, so a genuine
// create/rebuild error — not the benign "already exists" collision —
// propagates and cannot leave the type silently unsearchable (FTS search in
// query.go silently skips index-less types). Intentionally NOT error-text
// matched; the existence check is the discriminator.
func rebuildFTSIndexForTable(conn *lbug.Connection, table string, allStringProps []string) error {
	propsList := "'" + strings.Join(allStringProps, "', '") + "'"
	if ok, err := ftsIndexExists(conn, table); err != nil {
		return fmt.Errorf("check FTS index for %q: %w", table, err)
	} else if ok {
		r, err := conn.Query(fmt.Sprintf("CALL DROP_FTS_INDEX('%s', '%s_fts');", table, table))
		if err != nil {
			return fmt.Errorf("drop existing FTS index for %q: %w", table, err)
		}
		r.Close()
	}
	r, err := conn.Query(fmt.Sprintf("CALL CREATE_FTS_INDEX('%s', '%s_fts', [%s], stemmer := 'porter');",
		table, table, propsList))
	if err != nil {
		return fmt.Errorf("rebuild FTS index for %q: %w", table, err)
	}
	r.Close()
	return nil
}

// ensureFTSIndexOnConn creates the _fts index for the table when it is absent,
// over the string properties of the given def, and reports whether it created
// it. Used by the crash-repair path to close the window between CREATE NODE
// TABLE and its CREATE_FTS_INDEX inside createNodeTableOnConn.
func ensureFTSIndexOnConn(conn *lbug.Connection, table string, props []store.PropertyDef) (bool, error) {
	var stringProps []string
	for _, p := range props {
		if ladybugType(p.Type) == colTypeString {
			stringProps = append(stringProps, p.Name)
		}
	}
	if len(stringProps) == 0 {
		return false, nil
	}
	ok, err := ftsIndexExists(conn, table)
	if err != nil {
		return false, fmt.Errorf("check FTS index for %q: %w", table, err)
	}
	if ok {
		return false, nil
	}
	propsList := "'" + strings.Join(stringProps, "', '") + "'"
	r, err := conn.Query(fmt.Sprintf("CALL CREATE_FTS_INDEX('%s', '%s_fts', [%s], stemmer := 'porter');",
		table, table, propsList))
	if err != nil {
		return false, fmt.Errorf("create FTS index for %q: %w", table, err)
	}
	r.Close()
	return true, nil
}

// vectorIndexExists reports whether the table already carries its _vec vector
// (HNSW) index, so DROP_VECTOR_INDEX is only issued when one is present.
func vectorIndexExists(conn *lbug.Connection, table string) (bool, error) {
	return indexExistsOnConn(conn, table, table+"_vec")
}

// getTableProperties queries table_info for the given table and returns its
// column definitions (excluding hidden/system columns). Which columns are
// structural (and therefore not user properties) depends on the table kind:
// REL tables carry structural from/to/type endpoint columns, and vector-indexed
// NODE tables carry a structural embedding column. SPEC R1 reserves those names
// only in those positions — an entity property named from/to/type is not a
// reserved word and passes schema.Validate, and a non-vector entity type may
// declare a property named embedding — so they must be retained as real
// properties on NODE tables. vectorIndexed reports whether the NODE table
// carries an HNSW vector index (the embedding column and its index are
// bootstrapped together, so an index implies a structural embedding column).
func (db *ladybugDB) getTableProperties(tableName, tableType string, vectorIndexed bool) ([]store.PropertyDef, error) {
	q := fmt.Sprintf("CALL table_info('%s') RETURN *;", tableName)
	result, err := db.conn.Query(q)
	if err != nil {
		return nil, err
	}
	defer result.Close()

	// Skip implicit/self-managed columns that we don't expose as user properties.
	skip := map[string]bool{"id": true}
	switch strings.ToUpper(tableType) {
	case tableTypeNode:
		if vectorIndexed {
			skip["embedding"] = true
		}
	case tableTypeRel:
		skip["from"] = true
		skip["to"] = true
		skip["type"] = true
	}

	var props []store.PropertyDef
	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			return nil, fmt.Errorf("read column row: %w", err)
		}

		vals, err := tuple.GetAsSlice()
		tuple.Close()
		if err != nil {
			return nil, fmt.Errorf("get column values: %w", err)
		}

		// columns: property id, name, type, default expression, primary key
		if len(vals) < 3 {
			continue
		}
		colName := fmt.Sprintf("%v", vals[1])
		if skip[colName] {
			continue
		}
		colType := fmt.Sprintf("%v", vals[2])

		props = append(props, store.PropertyDef{
			Name: colName,
			Type: colType,
		})
	}

	if props == nil {
		props = []store.PropertyDef{}
	}
	return props, nil
}

// quoteID quotes a Cypher identifier using backticks. LadybugDB/Cypher uses
// backticks for escaped identifiers; double quotes are string literals.
// Names that pass schema validation ([a-zA-Z_][a-zA-Z0-9_]*) do not need
// quoting, but we do it defensively.
func quoteID(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

// ---------------------------------------------------------------------------
// Schema application
// ---------------------------------------------------------------------------

// ApplySchema validates the schema, translates it to LadybugDB DDL, and
// applies it to the database. Additive changes (new types, new properties on
// existing types, and rule modifications that preserve every edge type's
// FROM/TO endpoint set) are applied via ALTER DDL. Destructive changes
// (removed types, removed/changed properties, vector disable, edge-type
// endpoint-set changes, type incompatibility) return ErrDestructiveSchemaChange
// — the caller must WipeGraph first. Every destructive check runs in the
// pre-DDL catalog diff (diffSchemaAgainstCatalog), so a schema mixing additive
// and destructive changes is rejected all-or-nothing before any DDL executes.
//
// The schema metadata (schema.json) is published BEFORE the DDL loop — a
// write-ahead ordering — so a crash or failure anywhere in the DDL leaves the
// metadata describing the full intended schema while the catalog may be
// partial. restoreMainSchemaMetadataLocked converges the catalog onto that
// metadata on the next Open (creating the tables and columns the DDL never
// reached), so no interruption of ApplySchema can leave the store in a state
// that bricks every subsequent Open (SPEC R8 recoverability).
func (db *ladybugDB) ApplySchema(ctx context.Context, s *flowv1.Schema) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.closed || db.failed {
		return store.ErrDatabaseNotReady
	}

	// Validate first.
	if err := schema.Validate(s); err != nil {
		return err
	}

	// Diff against existing catalog to detect destructive changes.
	if err := db.diffSchemaAgainstCatalog(s); err != nil {
		return err
	}

	// Collect FROM/TO pairs for each edge type from entity-type rules.
	edgePairs := collectFromToPairs(s)
	metadata := metadataFromSchema(s)
	var stagedMetadata string
	if db.path != "" {
		var err error
		metadata, err = captureVectorState(db.conn, metadata)
		if err != nil {
			return fmt.Errorf("capture schema vector state: %w", err)
		}
		stagedMetadata, err = db.stageMetadata(db.mainMetadataPath(), metadata)
		if err != nil {
			return err
		}
		defer func() { _ = os.Remove(stagedMetadata) }()
	}

	// Publish the schema metadata before the DDL loop (write-ahead). If the
	// process crashes after this rename, the metadata already describes the
	// full intended schema and the next Open's restoreMainSchemaMetadataLocked
	// converges the (possibly partial) catalog onto it; if the publish itself
	// fails, no DDL has run and the store is left untouched. The reverse order
	// (DDL then publish) left a crash window in which the catalog was advanced
	// past the persisted metadata — a non-empty catalog with no matching
	// schema.json — which Open refused forever with no recovery path.
	if db.path != "" {
		if err := db.publishMetadata(stagedMetadata, db.mainMetadataPath()); err != nil {
			return fmt.Errorf("publish schema metadata: %w", err)
		}
	}

	// Apply entity types — new tables or additive ALTER.
	for _, et := range s.EntityTypes {
		if existing, exists := db.entityTypeDefs[et.Name]; exists {
			if err := db.alterNodeTable(et, existing); err != nil {
				return fmt.Errorf("alter node table %q: %w", et.Name, err)
			}
		} else {
			if err := db.createNodeTable(et); err != nil {
				return fmt.Errorf("create node table %q: %w", et.Name, err)
			}
		}
	}

	// Apply edge types — new tables or additive ALTER.
	for _, et := range s.EdgeTypes {
		if existing, exists := db.edgeTypeDefs[et.Name]; exists {
			if err := db.alterRelTable(et, existing); err != nil {
				return fmt.Errorf("alter rel table %q: %w", et.Name, err)
			}
		} else {
			if err := db.createRelTable(et, edgePairs[et.Name]); err != nil {
				return fmt.Errorf("create rel table %q: %w", et.Name, err)
			}
		}
	}

	db.entityTypeDefs, db.edgeTypeDefs, db.ruleIndex, db.edgePairs = applySchemaMetadata(metadata)
	db.schemaApplied = true
	return nil
}

// diffSchemaAgainstCatalog checks the requested schema against the current
// catalog for destructive changes. Returns ErrDestructiveSchemaChange if any
// destructive change is detected. Every check runs here — before any DDL — so
// a schema mixing additive and destructive changes fails all-or-nothing and
// can never leave the catalog partially advanced (see ApplySchema). The checks
// cover removed entity/edge types, removed/changed properties, vector-index
// disable, and — matching the SPEC R2 destructive class — an edge-type
// rule modification that adds or removes a FROM/TO pair on an already-applied
// edge type.
func (db *ladybugDB) diffSchemaAgainstCatalog(s *flowv1.Schema) error {
	// FROM/TO pairs for each requested edge type, derived from entity rules.
	edgePairs := collectFromToPairs(s)

	// Check for removed entity types.
	requestedEntities := make(map[string]*flowv1.EntityType, len(s.EntityTypes))
	for _, et := range s.EntityTypes {
		requestedEntities[et.Name] = et
	}
	for name := range db.entityTypeDefs {
		if _, ok := requestedEntities[name]; !ok {
			return fmt.Errorf("%w: entity type %q would be removed", store.ErrDestructiveSchemaChange, name)
		}
	}

	// Check for removed edge types.
	requestedEdges := make(map[string]*flowv1.EdgeType, len(s.EdgeTypes))
	for _, et := range s.EdgeTypes {
		requestedEdges[et.Name] = et
	}
	for name := range db.edgeTypeDefs {
		if _, ok := requestedEdges[name]; !ok {
			return fmt.Errorf("%w: edge type %q would be removed", store.ErrDestructiveSchemaChange, name)
		}
	}

	// Check entity type property changes.
	for _, et := range s.EntityTypes {
		existing, exists := db.entityTypeDefs[et.Name]
		if !exists {
			continue
		}
		existingProps := make(map[string]store.PropertyDef, len(existing.Properties))
		for _, p := range existing.Properties {
			existingProps[p.Name] = p
		}
		requestedProps := make(map[string]*flowv1.Property, len(et.Properties))
		for _, p := range et.Properties {
			requestedProps[p.Name] = p
		}
		// Check for removed or changed properties.
		for _, existingProp := range existing.Properties {
			requested, ok := requestedProps[existingProp.Name]
			if !ok {
				return fmt.Errorf("%w: entity type %q property %q would be removed",
					store.ErrDestructiveSchemaChange, et.Name, existingProp.Name)
			}
			// Check type compatibility (compare mapped DB types).
			if ladybugType(requested.Type) != ladybugType(existingProp.Type) {
				return fmt.Errorf("%w: entity type %q property %q type change from %q to %q",
					store.ErrDestructiveSchemaChange, et.Name, existingProp.Name, existingProp.Type, ladybugType(requested.Type))
			}
		}
		// Check vector index disable.
		if existing.EnableVectorIndex && !et.EnableVectorIndex {
			return fmt.Errorf("%w: entity type %q vector index would be disabled",
				store.ErrDestructiveSchemaChange, et.Name)
		}
	}

	// Check edge type property changes.
	for _, et := range s.EdgeTypes {
		existing, exists := db.edgeTypeDefs[et.Name]
		if !exists {
			continue
		}
		existingProps := make(map[string]store.PropertyDef, len(existing.Properties))
		for _, p := range existing.Properties {
			existingProps[p.Name] = p
		}
		requestedProps := make(map[string]*flowv1.Property, len(et.Properties))
		for _, p := range et.Properties {
			requestedProps[p.Name] = p
		}
		for _, existingProp := range existing.Properties {
			requested, ok := requestedProps[existingProp.Name]
			if !ok {
				return fmt.Errorf("%w: edge type %q property %q would be removed",
					store.ErrDestructiveSchemaChange, et.Name, existingProp.Name)
			}
			if ladybugType(requested.Type) != ladybugType(existingProp.Type) {
				return fmt.Errorf("%w: edge type %q property %q type change from %q to %q",
					store.ErrDestructiveSchemaChange, et.Name, existingProp.Name, existingProp.Type, ladybugType(requested.Type))
			}
		}
		// Check the requested FROM/TO endpoint set against the rel table's
		// actual endpoints. LadybugDB fixes a rel table's endpoint clauses at
		// CREATE time and cannot express a change through ALTER, so a rule
		// modification that adds or removes a pair on an already-applied edge
		// type is destructive (SPEC R2). This must run here in the pre-DDL
		// diff — not in alterRelTable after the entity-type DDL loop — so a
		// schema mixing additive entity changes with a destructive endpoint
		// change fails all-or-nothing before any DDL is applied.
		requestedPairs := edgePairs[et.Name]
		if len(requestedPairs) == 0 {
			// An edgeless edge type's rel table carries the reserved `_untyped`
			// placeholder pair (createRelTableOnConn); normalize exactly as
			// validateMetadataAgainstCatalog does so existing edgeless edge
			// types do not false-positive.
			requestedPairs = []fromToPair{{From: untypedTableName, To: untypedTableName}}
		}
		actualPairs, err := connectionPairsOnConn(db.conn, et.Name)
		if err != nil {
			return fmt.Errorf("read relationship endpoints for %q: %w", et.Name, err)
		}
		if !equalFromToPairs(actualPairs, requestedPairs) {
			return fmt.Errorf("%w: edge %q relationship endpoints would change; WipeGraph required before applying",
				store.ErrDestructiveSchemaChange, et.Name)
		}
	}

	return nil
}

// CheckBranchSchemaCompatibility validates the branch DB's schema against the
// current (main) schema — the SPEC R9 Commit flow step 1 check ("validate the
// branch LadybugDB state against the current schema"). The branch's tables were
// created from the schema in effect when the transaction began
// (ReplicateSchemaToBranch) or was last refreshed (resetBranchStoreFromWorkingTree
// in the service layer), so this is a baseline-vs-current comparison. A change
// is incompatible when a type or property the transaction's data lives under
// has been removed or changed, or a vector index has been disabled; those
// conditions return ErrDestructiveSchemaChange. Additive changes (new types,
// new properties) and entity-type rule modifications that preserve every edge
// type's FROM/TO endpoint set are non-destructive (SPEC R2/R6) and never fail
// the check, so a transaction begun under an older schema can still commit
// after a compatible schema push; a refreshed transaction is re-hydrated onto
// the current schema and always passes.
func (db *ladybugDB) CheckBranchSchemaCompatibility(_ context.Context, txID string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	br, err := db.branchLocked(txID)
	if err != nil {
		return err
	}
	br.mu.Lock()
	defer br.mu.Unlock()
	if br.failed {
		return store.ErrDatabaseNotReady
	}
	for name, branchDef := range br.entityTypeDefs {
		mainDef, ok := db.entityTypeDefs[name]
		if !ok {
			return fmt.Errorf("%w: entity type %q no longer exists in the current schema",
				store.ErrDestructiveSchemaChange, name)
		}
		if branchDef.EnableVectorIndex && !mainDef.EnableVectorIndex {
			return fmt.Errorf("%w: entity type %q vector index would be disabled",
				store.ErrDestructiveSchemaChange, name)
		}
		if err := branchPropertiesCompatible(name, "entity type", branchDef.Properties, mainDef.Properties); err != nil {
			return err
		}
	}
	for name, branchDef := range br.edgeTypeDefs {
		mainDef, ok := db.edgeTypeDefs[name]
		if !ok {
			return fmt.Errorf("%w: edge type %q no longer exists in the current schema",
				store.ErrDestructiveSchemaChange, name)
		}
		if err := branchPropertiesCompatible(name, "edge type", branchDef.Properties, mainDef.Properties); err != nil {
			return err
		}
	}
	return nil
}

// branchPropertiesCompatible verifies that every property of a branch type still
// exists in the current schema with a compatible (equal mapped) type, mirroring
// diffSchemaAgainstCatalog's property-level checks in the branch-vs-current
// direction: removed properties and type changes are destructive, added
// properties are not.
func branchPropertiesCompatible(typeName, kind string, branchProps, currentProps []store.PropertyDef) error {
	current := make(map[string]store.PropertyDef, len(currentProps))
	for _, p := range currentProps {
		current[p.Name] = p
	}
	for _, bp := range branchProps {
		cp, ok := current[bp.Name]
		if !ok {
			return fmt.Errorf("%w: %s %q property %q no longer exists in the current schema",
				store.ErrDestructiveSchemaChange, kind, typeName, bp.Name)
		}
		if ladybugType(cp.Type) != ladybugType(bp.Type) {
			return fmt.Errorf("%w: %s %q property %q type change from %q to %q",
				store.ErrDestructiveSchemaChange, kind, typeName, bp.Name, bp.Type, cp.Type)
		}
	}
	return nil
}

// alterNodeTable applies additive ALTER DDL for new properties on an existing
// node table. It does not handle destructive changes (those are rejected by
// diffSchemaAgainstCatalog).
func (db *ladybugDB) alterNodeTable(et *flowv1.EntityType, existing *store.EntityTypeDef) error {
	existingProps := make(map[string]bool, len(existing.Properties))
	for _, p := range existing.Properties {
		existingProps[p.Name] = true
	}
	var newStringProps []string
	for _, p := range et.Properties {
		if existingProps[p.Name] {
			continue
		}
		ddl := fmt.Sprintf("ALTER TABLE %s ADD %s %s;", quoteID(et.Name), quoteID(p.Name), ladybugType(p.Type))
		r, err := db.conn.Query(ddl)
		if err != nil {
			return fmt.Errorf("add column %q: %w", p.Name, err)
		}
		r.Close()
		if ladybugType(p.Type) == colTypeString {
			newStringProps = append(newStringProps, p.Name)
		}
	}
	// Rebuild FTS index with all string properties (existing + new) so that
	// the index covers every string column, not just the newly added one.
	if len(newStringProps) > 0 {
		var allStringProps []string
		for _, p := range existing.Properties {
			if ladybugType(p.Type) == colTypeString {
				allStringProps = append(allStringProps, p.Name)
			}
		}
		allStringProps = append(allStringProps, newStringProps...)
		if err := rebuildFTSIndexForTable(db.conn, et.Name, allStringProps); err != nil {
			return err
		}
	}
	return nil
}

// alterRelTable applies additive ALTER DDL for new properties on an existing
// rel table. The rel table's FROM/TO endpoint clauses are fixed at CREATE time
// and cannot be expressed through ALTER TABLE, so a rule modification that
// changes the edge type's endpoint set is destructive (SPEC R2/R6). That check
// lives in diffSchemaAgainstCatalog, which runs before any DDL and is the
// single enforcement point for ApplySchema — this function is only reachable
// from ApplySchema after the diff has already verified the endpoint set is
// unchanged, so it needs no defensive re-check. The failure mode stays loud:
// the caller sees ErrDestructiveSchemaChange before a single DDL statement
// executes, never a silent partial apply.
func (db *ladybugDB) alterRelTable(et *flowv1.EdgeType, existing *store.EdgeTypeDef) error {
	existingProps := make(map[string]bool, len(existing.Properties))
	for _, p := range existing.Properties {
		existingProps[p.Name] = true
	}
	for _, p := range et.Properties {
		if existingProps[p.Name] {
			continue
		}
		ddl := fmt.Sprintf("ALTER TABLE %s ADD %s %s;", quoteID(et.Name), quoteID(p.Name), ladybugType(p.Type))
		r, err := db.conn.Query(ddl)
		if err != nil {
			return fmt.Errorf("add column %q: %w", p.Name, err)
		}
		r.Close()
	}
	return nil
}

// rebuildSchemaCacheLocked is the inner rebuild that assumes db.mu is held.
func (db *ladybugDB) rebuildSchemaCacheLocked() error {
	// Create a temporary map, swap in-place while holding lock.
	newEntity := make(map[string]*store.EntityTypeDef)
	newEdge := make(map[string]*store.EdgeTypeDef)

	hasVectorIdx, err := db.collectVectorIndexes()
	if err != nil {
		return err
	}

	result, err := db.conn.Query("CALL show_tables() RETURN *;")
	if err != nil {
		return err
	}
	defer result.Close()

	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			return err
		}
		vals, err := tuple.GetAsSlice()
		tuple.Close()
		if err != nil {
			return err
		}
		if len(vals) < 3 {
			continue
		}
		tableName := fmt.Sprintf("%v", vals[1])
		tableType := fmt.Sprintf("%v", vals[2])

		props, err := db.getTableProperties(tableName, tableType, hasVectorIdx[tableName])
		if err != nil {
			return err
		}

		switch strings.ToUpper(tableType) {
		case tableTypeNode:
			newEntity[tableName] = &store.EntityTypeDef{
				Name:              tableName,
				Properties:        props,
				EnableVectorIndex: hasVectorIdx[tableName],
			}
		case tableTypeRel:
			newEdge[tableName] = &store.EdgeTypeDef{
				Name:       tableName,
				Properties: props,
			}
		}
	}

	db.entityTypeDefs = newEntity
	db.edgeTypeDefs = newEdge
	return nil
}

// createNodeTable translates an entity type into a PropertyDef list and runs
// the shared node-table DDL builder (see createNodeTableOnConn).
// SPEC R7: the FLOAT[n] embedding column is not reserved at table creation for
// entity types with enableVectorIndex — the column and its vector index are both
// deferred to the first embedding write (crud.go), because LadybugDB can host a
// vector index only on a dimensioned FLOAT[n] column and the dimension is
// inferred from that first embedding (the CRD exposes no dimension field to size
// the column at apply time). Until the first embedding the type's table carries
// no embedding column; an embedding write is the first mutation that can fail on
// a column/index DDL error. Every SPEC-observable behavior holds: the index
// stays lazy, the dimension locks on the first embedding, ErrVectorBootstrap
// rejects pre-bootstrap no-embedding creates, and post-bootstrap no-embedding
// creates store NULL. An FTS index is created on all string properties for
// full-text search.
func (db *ladybugDB) createNodeTable(et *flowv1.EntityType) error {
	return createNodeTableOnConn(db.conn, et.Name, propsFromEntity(et))
}

// createRelTable translates an edge flow into a PropertyDef list and runs the
// shared rel-table DDL (see createRelTableOnConn).
func (db *ladybugDB) createRelTable(et *flowv1.EdgeType, pairs []fromToPair) error {
	return createRelTableOnConn(db.conn, et.Name, propsFromEdge(et), pairs)
}

// propsFromEntity converts proto EntityType properties into store PropertyDefs.
func propsFromEntity(et *flowv1.EntityType) []store.PropertyDef {
	props := make([]store.PropertyDef, 0, len(et.Properties))
	for _, p := range et.Properties {
		props = append(props, store.PropertyDef{Name: p.Name, Type: p.Type, Required: p.Required})
	}
	return props
}

// propsFromEdge converts proto EdgeType properties into store PropertyDefs.
func propsFromEdge(et *flowv1.EdgeType) []store.PropertyDef {
	props := make([]store.PropertyDef, 0, len(et.Properties))
	for _, p := range et.Properties {
		props = append(props, store.PropertyDef{Name: p.Name, Type: p.Type, Required: p.Required})
	}
	return props
}

// fromToPair describes a single FROM → TO clause for a rel table. The json tags
// keep schema.json's persisted edge_pairs keys snake_case like the rest of the
// metadata file.
type fromToPair struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// collectFromToPairs scans entity-type rules to build edge-type FROM/TO pairs.
func collectFromToPairs(s *flowv1.Schema) map[string][]fromToPair {
	pairs := make(map[string][]fromToPair)
	seen := make(map[string]map[string]bool) // edgeType -> "from|to" set

	for _, et := range s.EntityTypes {
		for _, rule := range et.Rules {
			for _, edgeRef := range rule.Using {
				if seen[edgeRef] == nil {
					seen[edgeRef] = make(map[string]bool)
				}
				for _, target := range rule.CanConnectTo {
					key := et.Name + "|" + target
					if seen[edgeRef][key] {
						continue
					}
					seen[edgeRef][key] = true
					pairs[edgeRef] = append(pairs[edgeRef], fromToPair{
						From: et.Name,
						To:   target,
					})
				}
			}
		}
	}

	return pairs
}

const (
	colTypeString = "STRING"
	colTypeInt64  = "INT64"
	// untypedTableName is the internal placeholder NODE table created for
	// edgeless rel types (createRelTableOnConn). The name is reserved for this
	// purpose: schema.UntypedTableName is rejected as a user entity/edge type
	// name by schema.Validate, so it can never alias a user type. Aliased here
	// to keep a single source of truth for the reserved name.
	untypedTableName = schema.UntypedTableName
	tableTypeNode    = "NODE"
	tableTypeRel     = "REL"
)

// ladybugType maps the proto property type string to a LadybugDB column type.
// ponytail: Currently all user properties are "string"; if richer types are
// added to the proto, this mapping must grow.
func ladybugType(protoType string) string {
	switch strings.ToUpper(protoType) {
	case colTypeString, "":
		return colTypeString
	case colTypeInt64:
		return colTypeInt64
	case "FLOAT", "DOUBLE":
		return "DOUBLE"
	case "BOOL":
		return "BOOLEAN"
	default:
		return colTypeString
	}
}

// ---------------------------------------------------------------------------
// Schema provider methods
// ---------------------------------------------------------------------------

func (db *ladybugDB) EntityTypeNames() []string {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.failed {
		return nil
	}

	names := make([]string, 0, len(db.entityTypeDefs))
	for name := range db.entityTypeDefs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (db *ladybugDB) EdgeTypeNames() []string {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.failed {
		return nil
	}

	names := make([]string, 0, len(db.edgeTypeDefs))
	for name := range db.edgeTypeDefs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (db *ladybugDB) EntityType(name string) (*store.EntityTypeDef, bool) {
	db.mu.Lock()
	defer db.mu.Unlock()

	def, ok := db.entityTypeDefs[name]
	if db.failed || !ok {
		return nil, false
	}
	return def, true
}

func (db *ladybugDB) EdgeType(name string) (*store.EdgeTypeDef, bool) {
	db.mu.Lock()
	defer db.mu.Unlock()

	def, ok := db.edgeTypeDefs[name]
	if db.failed || !ok {
		return nil, false
	}
	return def, true
}

func (db *ladybugDB) TableExists(entityType string) bool {
	_, ok := db.EntityType(entityType)
	return ok
}

func (db *ladybugDB) ListMainEntityTypes() ([]string, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.failed {
		return nil, store.ErrDatabaseNotReady
	}
	return sortedKeys(db.entityTypeDefs), nil
}

func (db *ladybugDB) ValidateSchema(_ context.Context, s *flowv1.Schema) error {
	return schema.Validate(s)
}

func (db *ladybugDB) Health(_ context.Context) (*store.HealthResult, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	// Bounded database probe: run a simple query to verify the connection
	// is alive and usable, not just that the in-memory flags say so.
	ladybugOK := !db.closed && !db.failed
	if ladybugOK && db.conn != nil {
		r, err := db.conn.Query("RETURN 1;")
		if err != nil {
			ladybugOK = false
		} else {
			r.Close()
		}
	}

	// PVC writability probe: atomic temp-file write/sync/remove in the
	// configured data directory. For in-memory databases (path == "") there
	// is no PVC to check, so we report true.
	pvcWritable := true
	if db.path != "" {
		pvcWritable = probePVCWritable(db.path)
	}

	return &store.HealthResult{
		LadybugOK:     ladybugOK,
		SchemaApplied: db.schemaApplied,
		PVCWritable:   pvcWritable,
	}, nil
}

// probePVCWritable tests whether the directory at path is writable by
// creating a temporary file, writing to it, syncing, and removing it.
func probePVCWritable(path string) bool {
	f, err := os.CreateTemp(path, "health-*.tmp")
	if err != nil {
		return false
	}
	// Always remove the temp file, and close any still-open handle on the
	// early-return failure paths so no file descriptor leaks to GC.
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
		_ = os.Remove(f.Name())
	}()

	if _, err := f.Write([]byte("health")); err != nil {
		return false
	}
	if err := f.Sync(); err != nil {
		return false
	}
	if err := f.Close(); err != nil {
		return false
	}
	closed = true
	return true
}
