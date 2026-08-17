package ladybug

import (
	"context"
	"fmt"
	"os"
	"strings"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/foundry/flow/cartographer/internal/schema"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// schemaManager owns the schema-management method group of ladybugDB: catalog
// cache rebuilds, schema application (ApplySchema/DDL/alts), the schema-provider
// accessors, Health, and the schema-metadata/vector-state helpers in
// metadata.go and vector.go. The shared store state lives on ladybugDB; db is
// the owner pointer back to it.
type schemaManager struct {
	db *ladybugDB
}

// ---------------------------------------------------------------------------
// Catalog cache
// ---------------------------------------------------------------------------

// rebuildSchemaCache queries the LadybugDB catalog to populate
// entityTypeDefs and edgeTypeDefs (acquires lock).
func (sm *schemaManager) rebuildSchemaCache() error {
	db := sm.db
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.rebuildSchemaCacheLocked()
}

// collectVectorIndexes returns a set of table names that have a vector index.
// LadybugDB's vector extension creates indexes of type HNSW. It delegates to
// the conn-based vectorIndexesOnConn used by the branch schema rebuild; the
// catalog-read error is propagated, never swallowed (see vectorIndexesOnConn).
func (sm *schemaManager) collectVectorIndexes() (map[string]bool, error) {
	return vectorIndexesOnConn(sm.db.conn)
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
// column definitions (excluding hidden/system columns), delegating to the
// conn-based tablePropertiesOnConn used by the branch schema rebuild. See
// tablePropertiesOnConn for the structural-column skipping rules (which
// columns are structural depends on the table kind: REL tables carry
// structural from/to/type endpoint columns, and vector-indexed NODE tables
// carry a structural embedding column).
func (sm *schemaManager) getTableProperties(
	tableName, tableType string, vectorIndexed bool,
) ([]store.PropertyDef, error) {
	return tablePropertiesOnConn(sm.db.conn, tableName, tableType, vectorIndexed)
}

func vectorIndexesOnConn(conn *lbug.Connection) (map[string]bool, error) {
	indexes := make(map[string]bool)
	result, err := conn.Query("CALL show_indexes() RETURN *;")
	if err != nil {
		// Propagate, never swallow: every caller (rebuildBranchSchemaCache,
		// captureVectorState) treats a nil error as authoritative vector state,
		// so a catalog-read failure returning (empty map, nil) would silently
		// strip vector state from every type read on branch reopen. The
		// callers all propagate this error already, so the read path fails
		// loudly instead of silently marking every type non-vector.
		return nil, err
	}
	defer result.Close()
	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			return nil, err
		}
		values, err := tuple.GetAsSlice()
		tuple.Close()
		if err != nil {
			return nil, err
		}
		if len(values) >= 3 && strings.EqualFold(fmt.Sprint(values[2]), "hnsw") {
			indexes[fmt.Sprint(values[0])] = true
		}
	}
	return indexes, nil
}

// tablePropertiesOnConn reads a table's column definitions via table_info.
// It is the shared helper for both the main schema-cache rebuild
// (getTableProperties delegates here) and the branch schema rebuild
// (rebuildBranchSchemaCache). Structural column skipping is table-kind
// dependent: REL tables skip their from/to/type endpoint columns;
// vector-indexed NODE tables skip their embedding column. SPEC-valid entity
// properties named from/to/type (or embedding on a non-vector entity type)
// are real properties and are retained. vectorIndexed reports whether the
// NODE table carries an HNSW vector index (the embedding column and its index
// are bootstrapped together, so an index implies a structural embedding
// column).
func tablePropertiesOnConn(
	conn *lbug.Connection, table, tableType string, vectorIndexed bool,
) ([]store.PropertyDef, error) {
	result, err := conn.Query(fmt.Sprintf("CALL table_info('%s') RETURN *;", table))
	if err != nil {
		return nil, err
	}
	defer result.Close()
	skip := map[string]bool{"id": true}
	switch strings.ToUpper(tableType) {
	case "NODE":
		if vectorIndexed {
			skip["embedding"] = true
		}
	case "REL":
		skip["from"] = true
		skip["to"] = true
		skip["type"] = true
	}
	properties := []store.PropertyDef{}
	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			return nil, err
		}
		values, err := tuple.GetAsSlice()
		tuple.Close()
		if err != nil {
			return nil, err
		}
		if len(values) >= 3 && !skip[fmt.Sprint(values[1])] {
			properties = append(properties, store.PropertyDef{Name: fmt.Sprint(values[1]), Type: fmt.Sprint(values[2])})
		}
	}
	return properties, nil
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
func (sm *schemaManager) ApplySchema(ctx context.Context, s *flowv1.Schema) error {
	db := sm.db
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
		if _, exists := db.edgeTypeDefs[et.Name]; exists {
			if err := db.alterRelTable(et); err != nil {
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
func (sm *schemaManager) diffSchemaAgainstCatalog(s *flowv1.Schema) error {
	db := sm.db
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
func (sm *schemaManager) CheckBranchSchemaCompatibility(_ context.Context, txID string) error {
	db := sm.db
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
//
// The diff runs against the LIVE catalog columns, not the in-memory def.
// ApplySchema converges db.entityTypeDefs only after the full DDL loop
// succeeds, so a retried ApplySchema after a mid-loop error diffs against a
// stale cache and would re-issue the non-idempotent ALTER TABLE ADD against a
// column the catalog already holds — failing again on every retry until a pod
// restart (the crash case is converged by restoreMainSchemaMetadataLocked; the
// in-process retry was not). Skipping catalog-present columns — mirroring
// createNodeTableOnConn's IF NOT EXISTS guard — makes the retry converge, and
// the FTS rebuild derives its string set from the same live columns so a
// string column a partial run added before it failed is still covered.
func (sm *schemaManager) alterNodeTable(et *flowv1.EntityType, existing *store.EntityTypeDef) error {
	db := sm.db
	props, err := db.getTableProperties(et.Name, tableTypeNode, existing.EnableVectorIndex)
	if err != nil {
		return fmt.Errorf("read catalog columns for %q: %w", et.Name, err)
	}
	// The ADD-column diff runs against the live catalog columns (passed as the
	// existing set), sharing addMissingColumnsLocked with the metadata repair
	// path.
	newStringProps, err := db.addMissingColumnsLocked(et.Name, props, propsFrom(et.Properties))
	if err != nil {
		return err
	}
	// Rebuild the FTS index over the full string-property set whenever the
	// catalog's string columns grew beyond the in-memory def's record — either
	// from this call's ALTERs or from a partial run that added string columns
	// before it failed (whose index rebuild never ran) — so a retried
	// ApplySchema also converges the index.
	known := make(map[string]bool, len(existing.Properties))
	for _, p := range existing.Properties {
		known[p.Name] = true
	}
	staleString := false
	for _, p := range props {
		if ladybugType(p.Type) == colTypeString && !known[p.Name] {
			staleString = true
			break
		}
	}
	if len(newStringProps) > 0 || staleString {
		var allStringProps []string
		for _, p := range props {
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
func (sm *schemaManager) alterRelTable(et *flowv1.EdgeType) error {
	db := sm.db
	// Same live-catalog diff as alterNodeTable: a retried ApplySchema after a
	// mid-loop error would otherwise re-issue the non-idempotent ALTER TABLE
	// ADD against a column the catalog already holds. Rel tables carry no FTS
	// index, so the guard alone converges the retry.
	props, err := db.getTableProperties(et.Name, tableTypeRel, false)
	if err != nil {
		return fmt.Errorf("read catalog columns for %q: %w", et.Name, err)
	}
	if _, err := db.addMissingColumnsLocked(et.Name, props, propsFrom(et.Properties)); err != nil {
		return err
	}
	return nil
}

// rebuildSchemaCacheLocked is the inner rebuild that assumes db.mu is held.
func (sm *schemaManager) rebuildSchemaCacheLocked() error {
	db := sm.db
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
func (sm *schemaManager) createNodeTable(et *flowv1.EntityType) error {
	return createNodeTableOnConn(sm.db.conn, et.Name, propsFrom(et.Properties))
}

// createRelTable translates an edge flow into a PropertyDef list and runs the
// shared rel-table DDL (see createRelTableOnConn).
func (sm *schemaManager) createRelTable(et *flowv1.EdgeType, pairs []fromToPair) error {
	return createRelTableOnConn(sm.db.conn, et.Name, propsFrom(et.Properties), pairs)
}

// propsFrom converts proto property definitions (an entity or edge type's
// Properties list) into store PropertyDefs.
func propsFrom(props []*flowv1.Property) []store.PropertyDef {
	defs := make([]store.PropertyDef, 0, len(props))
	for _, p := range props {
		defs = append(defs, store.PropertyDef{Name: p.Name, Type: p.Type, Required: p.Required})
	}
	return defs
}

// --------------------------------------------------------------------------
// Internal helpers — table DDL on an arbitrary connection
// --------------------------------------------------------------------------

func createNodeTableOnConn(conn *lbug.Connection, name string,
	properties []store.PropertyDef) error {
	cols := make([]string, 0, 1+len(properties)+1)
	cols = append(cols, "id STRING PRIMARY KEY")
	stringProps := make([]string, 0, len(properties))
	for _, p := range properties {
		propertyType := ladybugType(p.Type)
		cols = append(cols, quoteID(p.Name)+" "+propertyType)
		if propertyType == colTypeString {
			stringProps = append(stringProps, p.Name)
		}
	}
	// ponytail: embedding column and vector index are bootstrapped lazily
	// on first CreateEntity with an embedding; no FLOAT[n] column or index
	// is created at table creation time. Consequences until that first
	// bootstrap write: (1) a pre-bootstrap CreateEntity that omits the
	// embedding fails with ErrVectorBootstrap (SPEC R7/error-table row
	// "Vector dimension bootstrap failed") — the first entity for a
	// vector-indexed type must carry an embedding; (2) the type's vector
	// search silently returns empty — searchIndexedType skips any type
	// whose dimension is still 0, so SearchNeighbors over the type (or a
	// wildcard search covering it) reports no results and no error, which
	// can mask "no embeddings written yet" as "no neighbors found"; and
	// (3) the FLOAT[n] dimension is permanently locked at the first
	// bootstrap — a later embedding of a different dimension is rejected
	// with ErrEmbeddingDimension, and re-dimensioning a bootstrapped type
	// is possible only through a destructive schema change (drop the type,
	// wipe, re-apply). Deployment risk: clients that never write embeddings
	// to a vector-enabled type observe the type as permanently empty to
	// search while the bootstrap contract silently degrades their writes
	// to FAILED_PRECONDITION. The laziness is SPEC-mandated (R7/ApplySchema:
	// the dimension is unknowable until the first embedding), so this is a
	// documented ceiling, not a divergence.
	ddl := fmt.Sprintf("CREATE NODE TABLE IF NOT EXISTS %s (%s);", quoteID(name), strings.Join(cols, ", "))
	r, err := conn.Query(ddl)
	if err != nil {
		return err
	}
	r.Close()
	// Create FTS index on all string properties.
	if len(stringProps) > 0 {
		// Whether the table was freshly created or already existed (this builder
		// runs CREATE NODE TABLE IF NOT EXISTS on every table (re)creation at
		// hydration / schema-load), its FTS index — also not idempotent to
		// create — may already exist. Skip only when the index is known present,
		// so a genuine index-creation error propagates and cannot silently skip
		// the type's FullTextSearch coverage (query.go silently skips index-less
		// types), rather than error-matching the library's "already exists" text.
		if ok, err := ftsIndexExists(conn, name); err != nil {
			return fmt.Errorf("check FTS index for %q: %w", name, err)
		} else if !ok {
			propsList := "'" + strings.Join(stringProps, "', '") + "'"
			ftsDDL := fmt.Sprintf("CALL CREATE_FTS_INDEX('%s', '%s_fts', [%s], stemmer := 'porter');",
				name, name, propsList)
			r, err := conn.Query(ftsDDL)
			if err != nil {
				return fmt.Errorf("create FTS index for %q: %w", name, err)
			}
			r.Close()
		}
	}
	return nil
}

func createRelTableOnConn(conn *lbug.Connection, name string,
	properties []store.PropertyDef, pairs []fromToPair) error {
	var clauses []string
	if len(pairs) > 0 {
		for _, p := range pairs {
			clauses = append(clauses, fmt.Sprintf("FROM %s TO %s", quoteID(p.From), quoteID(p.To)))
		}
	} else {
		// Need at least one FROM/TO pair; create a placeholder _untyped node table.
		// The rel table's endpoint clauses reference this table by name, so a
		// failure here would otherwise surface only second-hand (and unreliably)
		// when the rel-table DDL references the missing table. Propagate it now.
		stmt := "CREATE NODE TABLE IF NOT EXISTS " + untypedTableName + " (id STRING PRIMARY KEY);"
		r, err := conn.Query(stmt)
		if err != nil {
			return fmt.Errorf("create placeholder %s node table: %w", untypedTableName, err)
		}
		r.Close()
		clauses = append(clauses, "FROM "+untypedTableName+" TO "+untypedTableName)
	}

	cols := make([]string, 0, 2+len(properties)+1)
	cols = append(cols, strings.Join(clauses, ", "))
	cols = append(cols, "id STRING")
	for _, p := range properties {
		cols = append(cols, quoteID(p.Name)+" "+ladybugType(p.Type))
	}
	ddl := fmt.Sprintf("CREATE REL TABLE IF NOT EXISTS %s (%s);", quoteID(name), strings.Join(cols, ", "))
	r, err := conn.Query(ddl)
	if err != nil {
		return err
	}
	r.Close()
	return nil
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
	// untypedTableName is the internal placeholder NODE table created for
	// edgeless rel types (createRelTableOnConn). The name is reserved for this
	// purpose: schema.UntypedTableName is rejected as a user entity/edge type
	// name by schema.Validate, so it can never alias a user type. Aliased here
	// to keep a single source of truth for the reserved name.
	untypedTableName = schema.UntypedTableName
	tableTypeNode    = "NODE"
	tableTypeRel     = "REL"
)

// ladybugType maps a property type string to a LadybugDB column type. SPEC
// R1/R7 mandate string-typed properties (the wire carries map<string,string>),
// so "string" (proto form), "STRING" (catalog form), and "" map to STRING. Any
// other input — reachable only as a drifted catalog column type — is returned
// unchanged so the ApplySchema catalog diff still rejects a column whose
// persisted type differs from the declared schema (SPEC error-table row "Table
// structure mismatch"). The mapping is the single seam that must grow if a
// richer proto property-type model is ever added.
func ladybugType(protoType string) string {
	switch strings.ToUpper(protoType) {
	case colTypeString, "":
		return colTypeString
	default:
		return protoType
	}
}

// ---------------------------------------------------------------------------
// Schema provider methods
// ---------------------------------------------------------------------------

func (sm *schemaManager) EntityTypeNames() []string {
	db := sm.db
	db.mu.Lock()
	defer db.mu.Unlock()
	return typeNamesLocked(db.entityTypeDefs, db.failed)
}

func (sm *schemaManager) EdgeTypeNames() []string {
	db := sm.db
	db.mu.Lock()
	defer db.mu.Unlock()
	return typeNamesLocked(db.edgeTypeDefs, db.failed)
}

// typeNamesLocked returns the sorted names of a type-def map, or nil when the
// store is failed. Callers must hold db.mu.
func typeNamesLocked[V any](m map[string]V, failed bool) []string {
	if failed {
		return nil
	}
	return sortedKeys(m)
}

func (sm *schemaManager) EntityType(name string) (*store.EntityTypeDef, bool) {
	db := sm.db
	db.mu.Lock()
	defer db.mu.Unlock()
	return lookupTypeDef(db.entityTypeDefs, name, db.failed)
}

func (sm *schemaManager) EdgeType(name string) (*store.EdgeTypeDef, bool) {
	db := sm.db
	db.mu.Lock()
	defer db.mu.Unlock()
	return lookupTypeDef(db.edgeTypeDefs, name, db.failed)
}

// lookupTypeDef returns the type definition for name from the given type-def
// map, reporting (zero, false) when the store is failed or the type is absent.
// Callers must hold db.mu.
func lookupTypeDef[V any](m map[string]V, name string, failed bool) (V, bool) {
	def, ok := m[name]
	if failed || !ok {
		var zero V
		return zero, false
	}
	return def, true
}

func (sm *schemaManager) TableExists(entityType string) bool {
	_, ok := sm.EntityType(entityType)
	return ok
}

func (sm *schemaManager) ListMainEntityTypes() ([]string, error) {
	db := sm.db
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.failed {
		return nil, store.ErrDatabaseNotReady
	}
	return sortedKeys(db.entityTypeDefs), nil
}

func (sm *schemaManager) Health(_ context.Context) (*store.HealthResult, error) {
	db := sm.db
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
