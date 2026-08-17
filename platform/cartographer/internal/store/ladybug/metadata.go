package ladybug

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"syscall"

	lbug "github.com/LadybugDB/go-ladybug"
	schemavalidator "github.com/foundry/flow/cartographer/internal/schema"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

const schemaMetadataVersion = 2

type schemaMetadata struct {
	Version          int                   `json:"version"`
	EntityTypes      []store.EntityTypeDef `json:"entity_types"`
	EdgeTypes        []store.EdgeTypeDef   `json:"edge_types"`
	VectorIndexes    map[string]bool       `json:"vector_indexes"`
	VectorDimensions map[string]int        `json:"vector_dimensions"`
	// EdgePairs records each edge type's FROM/TO endpoint pairs. It is
	// authoritative for edge types inferred from the directory structure
	// (SPEC R8), which carry no connection rules for applySchemaMetadata's
	// rule derivation to recover on reopen. A legacy schema.json (written
	// before this field existed) unmarshals it to nil and falls back to rule
	// derivation, which is equivalent for rule-derived types.
	EdgePairs map[string][]fromToPair `json:"edge_pairs,omitempty"`
}

func metadataFromSchema(s *flowv1.Schema) schemaMetadata {
	metadata := schemaMetadata{
		Version: schemaMetadataVersion, VectorIndexes: make(map[string]bool), VectorDimensions: make(map[string]int),
		EdgePairs: collectFromToPairs(s),
	}
	for _, entityType := range s.EntityTypes {
		def := store.EntityTypeDef{Name: entityType.Name, EnableVectorIndex: entityType.EnableVectorIndex}
		for _, property := range entityType.Properties {
			def.Properties = append(def.Properties, store.PropertyDef{
				Name: property.Name, Type: property.Type, Required: property.Required,
			})
		}
		for _, rule := range entityType.Rules {
			def.Rules = append(def.Rules, store.ConnectionRuleDef{
				CanConnectTo: append([]string(nil), rule.CanConnectTo...),
				Using:        append([]string(nil), rule.Using...),
			})
		}
		metadata.EntityTypes = append(metadata.EntityTypes, def)
	}
	for _, edgeType := range s.EdgeTypes {
		def := store.EdgeTypeDef{Name: edgeType.Name}
		for _, property := range edgeType.Properties {
			def.Properties = append(def.Properties, store.PropertyDef{
				Name: property.Name, Type: property.Type, Required: property.Required,
			})
		}
		metadata.EdgeTypes = append(metadata.EdgeTypes, def)
	}
	return metadata
}

// metadataFromDefinitions rebuilds schema metadata from the in-memory type
// definitions, persisting the given FROM/TO endpoint pairs so a reopen's
// applySchemaMetadata can recover pairs for rule-less (inferred, SPEC R8) edge
// types. Every caller must supply the authoritative pair map (db.edgePairs or
// the catalog-derived set) — a nil map would round-trip a lossy schema.json.
func metadataFromDefinitions(
	entities map[string]*store.EntityTypeDef, edges map[string]*store.EdgeTypeDef,
	pairs map[string][]fromToPair,
) schemaMetadata {
	metadata := schemaMetadata{
		Version: schemaMetadataVersion, VectorIndexes: make(map[string]bool), VectorDimensions: make(map[string]int),
		EdgePairs: pairs,
	}
	for _, name := range sortedKeys(entities) {
		metadata.EntityTypes = append(metadata.EntityTypes, cloneEntityTypeDef(entities[name]))
	}
	for _, name := range sortedKeys(edges) {
		metadata.EdgeTypes = append(metadata.EdgeTypes, cloneEdgeTypeDef(edges[name]))
	}
	return metadata
}

func cloneEntityTypeDef(def *store.EntityTypeDef) store.EntityTypeDef {
	clone := store.EntityTypeDef{
		Name: def.Name, EnableVectorIndex: def.EnableVectorIndex,
		Properties: append([]store.PropertyDef(nil), def.Properties...),
	}
	for _, rule := range def.Rules {
		clone.Rules = append(clone.Rules, store.ConnectionRuleDef{
			CanConnectTo: append([]string(nil), rule.CanConnectTo...),
			Using:        append([]string(nil), rule.Using...),
		})
	}
	return clone
}

func cloneEdgeTypeDef(def *store.EdgeTypeDef) store.EdgeTypeDef {
	return store.EdgeTypeDef{Name: def.Name, Properties: append([]store.PropertyDef(nil), def.Properties...)}
}

func writeSchemaMetadata(path string, metadata schemaMetadata) error {
	temporaryPath, err := stageSchemaMetadata(path, metadata)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporaryPath) }()
	return publishSchemaMetadata(temporaryPath, path)
}

func stageSchemaMetadata(path string, metadata schemaMetadata) (string, error) {
	sort.Slice(metadata.EntityTypes, func(i, j int) bool {
		return metadata.EntityTypes[i].Name < metadata.EntityTypes[j].Name
	})
	sort.Slice(metadata.EdgeTypes, func(i, j int) bool { return metadata.EdgeTypes[i].Name < metadata.EdgeTypes[j].Name })
	data, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("marshal schema metadata: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".schema-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create schema metadata temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("chmod schema metadata: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("write schema metadata: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("sync schema metadata: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close schema metadata: %w", err)
	}
	ok = true
	return temporaryPath, nil
}

func publishSchemaMetadata(temporaryPath, path string) error {
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace schema metadata: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync schema metadata directory: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		return err
	}
	return nil
}

func readSchemaMetadata(path string, required bool) (schemaMetadata, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && !required {
			return schemaMetadata{}, false, nil
		}
		return schemaMetadata{}, false, fmt.Errorf("read schema metadata: %w", err)
	}
	var metadata schemaMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return schemaMetadata{}, false, fmt.Errorf("parse schema metadata: %w", err)
	}
	if metadata.Version != schemaMetadataVersion {
		return schemaMetadata{}, false, fmt.Errorf("unsupported schema metadata version %d", metadata.Version)
	}
	if err := validateSchemaMetadata(metadata); err != nil {
		return schemaMetadata{}, false, err
	}
	return metadata, true, nil
}

func validateSchemaMetadata(metadata schemaMetadata) error {
	if metadata.VectorIndexes == nil || metadata.VectorDimensions == nil {
		return fmt.Errorf("schema metadata is missing vector state")
	}
	entities := make(map[string]bool, len(metadata.EntityTypes))
	edges := make(map[string]bool, len(metadata.EdgeTypes))
	protoSchema := &flowv1.Schema{}
	for _, def := range metadata.EntityTypes {
		if def.Name == "" || entities[def.Name] {
			return fmt.Errorf("invalid duplicate or empty entity type in schema metadata: %q", def.Name)
		}
		entities[def.Name] = true
		if _, ok := metadata.VectorIndexes[def.Name]; !ok {
			return fmt.Errorf("schema metadata is missing vector index state for %q", def.Name)
		}
		if _, ok := metadata.VectorDimensions[def.Name]; !ok {
			return fmt.Errorf("schema metadata is missing vector dimension state for %q", def.Name)
		}
		if metadata.VectorIndexes[def.Name] && metadata.VectorDimensions[def.Name] <= 0 {
			return fmt.Errorf("schema metadata has vector index without dimension for %q", def.Name)
		}
		if !def.EnableVectorIndex &&
			(metadata.VectorIndexes[def.Name] || metadata.VectorDimensions[def.Name] > 0) {
			return fmt.Errorf("schema metadata has vector state for disabled entity type %q", def.Name)
		}
		entityType := &flowv1.EntityType{Name: def.Name, EnableVectorIndex: def.EnableVectorIndex}
		for _, property := range def.Properties {
			entityType.Properties = append(entityType.Properties, &flowv1.Property{
				Name: property.Name, Type: property.Type, Required: property.Required,
			})
		}
		for _, rule := range def.Rules {
			entityType.Rules = append(entityType.Rules, &flowv1.ConnectionRule{
				CanConnectTo: append([]string(nil), rule.CanConnectTo...),
				Using:        append([]string(nil), rule.Using...),
			})
		}
		protoSchema.EntityTypes = append(protoSchema.EntityTypes, entityType)
	}
	if len(metadata.VectorIndexes) != len(entities) || len(metadata.VectorDimensions) != len(entities) {
		return fmt.Errorf("schema metadata contains vector state for unknown entity types")
	}
	for _, def := range metadata.EdgeTypes {
		if def.Name == "" || edges[def.Name] {
			return fmt.Errorf("invalid duplicate or empty edge type in schema metadata: %q", def.Name)
		}
		edges[def.Name] = true
		edgeType := &flowv1.EdgeType{Name: def.Name}
		for _, property := range def.Properties {
			edgeType.Properties = append(edgeType.Properties, &flowv1.Property{
				Name: property.Name, Type: property.Type, Required: property.Required,
			})
		}
		protoSchema.EdgeTypes = append(protoSchema.EdgeTypes, edgeType)
	}
	if err := schemavalidator.Validate(protoSchema); err != nil {
		return fmt.Errorf("invalid schema metadata: %w", err)
	}
	return nil
}

func applySchemaMetadata(
	metadata schemaMetadata,
) (
	map[string]*store.EntityTypeDef,
	map[string]*store.EdgeTypeDef,
	map[string][]*flowv1.ConnectionRule,
	map[string][]fromToPair,
) {
	entities := make(map[string]*store.EntityTypeDef, len(metadata.EntityTypes))
	edges := make(map[string]*store.EdgeTypeDef, len(metadata.EdgeTypes))
	rules := make(map[string][]*flowv1.ConnectionRule, len(metadata.EntityTypes))
	pairs := make(map[string][]fromToPair)
	seen := make(map[string]map[string]bool) // edgeType -> "from|to" set
	for i := range metadata.EntityTypes {
		def := cloneEntityTypeDef(&metadata.EntityTypes[i])
		entities[def.Name] = &def
		for _, rule := range def.Rules {
			protoRule := &flowv1.ConnectionRule{
				CanConnectTo: append([]string(nil), rule.CanConnectTo...),
				Using:        append([]string(nil), rule.Using...),
			}
			rules[def.Name] = append(rules[def.Name], protoRule)
			for _, edgeType := range rule.Using {
				for _, targetType := range rule.CanConnectTo {
					key := def.Name + "\x00" + targetType
					// Dedup FROM/TO pairs exactly as collectFromToPairs does, so
					// the pair set derived from metadata matches the rel table's
					// actual endpoints. SPEC R1 merges the membership lists of
					// multiple rules (OR semantics), so overlapping rules can
					// legitimately produce the same pair twice; without dedup the
					// reopened-store comparison (equalFromToPairs against the
					// catalog, which stores each pair once) would fail closed.
					se := seen[edgeType]
					if se == nil {
						se = make(map[string]bool)
						seen[edgeType] = se
					}
					if se[key] {
						continue
					}
					se[key] = true
					pairs[edgeType] = append(pairs[edgeType], fromToPair{From: def.Name, To: targetType})
				}
			}
		}
	}
	for i := range metadata.EdgeTypes {
		def := cloneEdgeTypeDef(&metadata.EdgeTypes[i])
		edges[def.Name] = &def
	}
	// Persisted endpoint pairs complete the rule-derived set for the edge types
	// the rules do not cover. Re-hydration paths infer edge types from the
	// directory structure (SPEC R8) that carry no connection rules, so nothing
	// but the persisted pairs can recover them on reopen; without them the
	// catalog comparison in validateMetadataAgainstCatalog fails closed against
	// the `_untyped` placeholder ("relationship endpoints do not match schema
	// metadata"), bricking the next file-backed Open. Rule-derived pairs stay
	// authoritative for rule-covered types so a hand-edit that changes a rule's
	// endpoints still fails the strict reopen comparison. Legacy schema.json
	// files (written before EdgePairs existed) leave the field nil and fall back
	// to rule derivation alone.
	if metadata.EdgePairs != nil {
		for edgeType, persisted := range metadata.EdgePairs {
			if _, covered := pairs[edgeType]; covered {
				continue
			}
			pairs[edgeType] = append([]fromToPair(nil), persisted...)
		}
	}
	return entities, edges, rules, pairs
}

func (sm *schemaManager) mainMetadataPath() string {
	db := sm.db
	return filepath.Join(db.path, "schema.json")
}

// persistMainVectorMetadataLocked rewrites main's schema metadata from the
// current in-memory type definitions, capturing the live vector state from the
// catalog, and persists it to the main metadata file when file-backed. It is
// used by re-hydration paths (RehydrateFromBranch, RehydrateMainFromFiles) that
// may promote an embedding column/vector index to main outside ApplySchema: a
// branch-driven or file-driven bootstrap leaves the catalog carrying a vector
// index and dimension that main's previously-written schema.json does not
// record (VectorIndexes=false/VectorDimensions=0), which would otherwise cause
// validateMetadataAgainstCatalog to brick the next Open. Callers must hold
// db.mu and must already have merged the promoted defs into db.entityTypeDefs.
func (sm *schemaManager) persistMainVectorMetadataLocked() error {
	db := sm.db
	if db.path == "" {
		return nil
	}
	metadata := metadataFromDefinitions(db.entityTypeDefs, db.edgeTypeDefs, db.edgePairs)
	metadata, err := captureVectorState(db.conn, metadata)
	if err != nil {
		return fmt.Errorf("capture main vector schema metadata: %w", err)
	}
	if err := db.writeMetadata(db.mainMetadataPath(), metadata); err != nil {
		return fmt.Errorf("persist main schema metadata: %w", err)
	}
	return nil
}

func (sm *schemaManager) branchMetadataPath(txID string) string {
	db := sm.db
	return filepath.Join(db.path, "branches", txID+".schema.json")
}

// restoreMainSchemaMetadataLocked restores main's schema on Open: when the
// catalog is empty the persisted metadata rebuilds every table (SPEC R8
// recovery); when the catalog is partial or missing tables/columns the
// metadata drives a convergent repair (an ApplySchema interrupted between the
// schema.json publish and the DDL completion). The restore also reconciles the
// reverse direction — a vector bootstrap interrupted between its DDL and its
// metadata publish leaves the catalog carrying vector state the metadata does
// not record, which reconcileVectorStateFromCatalog adopts back into the
// metadata before validation. A non-empty catalog with NO metadata at all
// fails loudly — the schema intent cannot be reconstructed from the catalog
// (vector enablement for un-bootstrapped types, connection rules), so it is a
// genuine state-loss event, not a repair. Caller must hold db.mu.
func (sm *schemaManager) restoreMainSchemaMetadataLocked() error {
	db := sm.db
	if db.path == "" {
		return nil
	}
	metadata, found, err := readSchemaMetadata(db.mainMetadataPath(), false)
	if err != nil {
		return err
	}
	if !found {
		if len(db.entityTypeDefs) != 0 || len(db.edgeTypeDefs) != 0 {
			return fmt.Errorf("schema metadata is missing for non-empty database")
		}
		return nil
	}
	entities, edges, rules, pairs := applySchemaMetadata(metadata)
	// Adopt the catalog's actual vector state into the metadata before the
	// convergent repair: a vector bootstrap interrupted between its DDL and
	// its metadata publish (the CreateEntity/UpdateEntity/re-hydration
	// bootstrap paths) leaves the catalog carrying an embedding column (with
	// or without its vector index) that schema.json does not record, and
	// validateMetadataAgainstCatalog would otherwise fail closed, bricking
	// every subsequent Open. The reverse direction (metadata ahead of the
	// catalog) is converged by repairCatalogAgainstMetadataLocked below, so
	// the two passes together make every mid-bootstrap crash state
	// recoverable.
	reconciled, reconcileDDL, err := reconcileVectorStateFromCatalog(db.conn, &metadata, entities)
	if err != nil {
		return err
	}
	// Converge the catalog onto the persisted metadata. The metadata file is
	// published before the DDL loop in ApplySchema (write-ahead), so an
	// interrupted apply can leave the catalog missing tables or columns the
	// metadata already declares; the repair creates them. It is idempotent —
	// on a catalog that already matches the metadata it issues no DDL — so it
	// also serves the empty-catalog recovery (a wiped/recreated main.lbug with
	// surviving metadata) that used to be a separate branch.
	changed, err := db.repairCatalogAgainstMetadataLocked(metadata, entities, edges, pairs)
	if err != nil {
		return err
	}
	if reconcileDDL || changed {
		if err := db.rebuildSchemaCacheLocked(); err != nil {
			return fmt.Errorf("rebuild repaired schema catalog: %w", err)
		}
	}
	if err := validateMetadataAgainstCatalog(
		db.conn, metadata, entities, edges, db.entityTypeDefs, db.edgeTypeDefs,
	); err != nil {
		return err
	}
	if reconciled {
		// Persist the healed metadata so the adopted vector state is durable
		// and the next Open validates without re-reconciling.
		if err := db.writeMetadata(db.mainMetadataPath(), metadata); err != nil {
			return fmt.Errorf("persist reconciled schema metadata: %w", err)
		}
	}
	db.entityTypeDefs, db.edgeTypeDefs = entities, edges
	db.ruleIndex, db.edgePairs = rules, pairs
	db.schemaApplied = true
	return nil
}

// reconcileVectorStateFromCatalog adopts the catalog's actual vector state
// into the schema metadata when the catalog is AHEAD of the persisted
// metadata. A vector bootstrap interrupted between its DDL and its metadata
// publish (the CreateEntity/UpdateEntity/re-hydration bootstrap paths) leaves
// a FLOAT[n] embedding column — with or without its vector index — in the
// catalog that schema.json does not record, and validateMetadataAgainstCatalog
// would otherwise fail closed ("vector index does not match schema metadata"),
// bricking every subsequent Open (the store fails startup). Adoption is safe
// because the only creators of a FLOAT[n] embedding column and HNSW vector
// index are the bootstrap paths themselves (legitimate dimension-lock intent,
// SPEC R7), so the catalog is authoritative for the vector state. The reverse
// direction (metadata ahead of the catalog) is converged by
// repairCatalogAgainstMetadataLocked, so the two passes make every
// mid-bootstrap crash state recoverable.
//
// An interrupted index creation (the crash caught between ALTER TABLE ADD
// embedding and CREATE_VECTOR_INDEX) is completed here so the adopted state
// validates; a type whose schema def was not declared vector-enabled is
// promoted to vector-enabled in both the metadata and the derived defs
// (mirroring ensureEmbeddingLoadSchema's promotion on the re-hydration paths),
// because validateSchemaMetadata rejects vector state on a disabled type.
//
// Returns whether the metadata was changed (the caller re-persists it) and
// whether any DDL was issued (the caller refreshes the schema cache before
// validation).
func reconcileVectorStateFromCatalog(
	conn *lbug.Connection,
	metadata *schemaMetadata,
	entities map[string]*store.EntityTypeDef,
) (changed, ddlIssued bool, err error) {
	if metadata.VectorIndexes == nil {
		metadata.VectorIndexes = make(map[string]bool)
	}
	if metadata.VectorDimensions == nil {
		metadata.VectorDimensions = make(map[string]int)
	}
	for name := range entities {
		// vectorIndexed=false keeps a non-vector type's legitimate STRING
		// `embedding` property from being misread as a bootstrap: only a
		// FLOAT[n] column parses as a dimension (getEmbeddingDimension), so a
		// STRING column reports "not bootstrapped".
		catalogDim, derr := getEmbeddingDimension(conn, name, false)
		if derr != nil {
			return changed, ddlIssued, fmt.Errorf("read catalog vector dimension for %q: %w", name, derr)
		}
		if catalogDim <= 0 {
			continue
		}
		if metadata.VectorIndexes[name] && metadata.VectorDimensions[name] == catalogDim {
			// An embedding rewrite (UpdateEntity, crud.go) drops the vector
			// index before writing the row and recreates it after; a crash
			// caught between the two leaves the catalog carrying the FLOAT[n]
			// column without its index while the metadata records it indexed.
			// The metadata and catalog agree on the dimension, so the fast path
			// cannot just trust the metadata — complete the missing index
			// (below) instead of skipping, mirroring the interrupted-bootstrap
			// completion.
			if ok, ierr := vectorIndexExists(conn, name); ierr != nil {
				return changed, ddlIssued, fmt.Errorf("check vector index for %q: %w", name, ierr)
			} else if ok {
				continue
			}
		}
		metadata.VectorIndexes[name] = true
		metadata.VectorDimensions[name] = catalogDim
		for i := range metadata.EntityTypes {
			def := &metadata.EntityTypes[i]
			if def.Name != name {
				continue
			}
			if !def.EnableVectorIndex {
				def.EnableVectorIndex = true
				entities[name].EnableVectorIndex = true
			}
			break
		}
		changed = true
		// Complete an interrupted bootstrap: a FLOAT[n] column whose vector
		// index never got created (the crash caught between the two DDL
		// statements) is adopted as indexed, so the index must exist for the
		// adopted state to validate against the catalog.
		if ok, ierr := vectorIndexExists(conn, name); ierr != nil {
			return changed, ddlIssued, fmt.Errorf("check vector index for %q: %w", name, ierr)
		} else if !ok {
			if cerr := createVectorIndexOnConn(conn, name); cerr != nil {
				return changed, ddlIssued, fmt.Errorf("complete vector index for %q: %w", name, cerr)
			}
			ddlIssued = true
		}
	}
	return changed, ddlIssued, nil
}

// repairCatalogAgainstMetadataLocked converges the physical catalog onto the
// persisted schema metadata after an ApplySchema interrupted between the
// schema.json publish and the DDL completion (the crash-atomicity window):
// tables the DDL never reached are created (including any embedding column and
// vector index the metadata records), columns an interrupted ALTER never added
// are added, and node tables whose string-property set the repair extended get
// their _fts index rebuilt over the full set — or created if the crash caught
// the window between CREATE NODE TABLE and its CREATE_FTS_INDEX. Idempotent:
// a catalog that already matches the metadata triggers no DDL. Returns whether
// any DDL was issued, so the caller can refresh the schema cache before
// validation. Caller must hold db.mu and pass the defs applySchemaMetadata
// derived from the metadata.
func (sm *schemaManager) repairCatalogAgainstMetadataLocked(
	metadata schemaMetadata,
	entities map[string]*store.EntityTypeDef,
	edges map[string]*store.EdgeTypeDef,
	pairs map[string][]fromToPair,
) (bool, error) {
	db := sm.db
	changed := false
	for _, name := range sortedKeys(entities) {
		def := entities[name]
		existing, ok := db.entityTypeDefs[name]
		if !ok {
			if err := createNodeTableOnConn(db.conn, name, def.Properties); err != nil {
				return changed, fmt.Errorf("restore node table %q from metadata: %w", name, err)
			}
			if dimension := metadata.VectorDimensions[name]; dimension > 0 {
				r, err := db.conn.Query(fmt.Sprintf(
					"ALTER TABLE %s ADD embedding FLOAT[%d];", quoteID(name), dimension,
				))
				if err != nil {
					return changed, fmt.Errorf("restore embedding column %q from metadata: %w", name, err)
				}
				r.Close()
				if metadata.VectorIndexes[name] {
					r, err := db.conn.Query(fmt.Sprintf(
						"CALL CREATE_VECTOR_INDEX('%s', '%s_vec', 'embedding', metric := 'cosine');", name, name,
					))
					if err != nil {
						return changed, fmt.Errorf("restore vector index %q from metadata: %w", name, err)
					}
					r.Close()
				}
			}
			changed = true
			continue
		}
		newStrings, err := db.addMissingColumnsLocked(name, existing.Properties, def.Properties)
		if err != nil {
			return changed, fmt.Errorf("repair node table %q from metadata: %w", name, err)
		}
		if len(newStrings) > 0 {
			var allStringProps []string
			for _, p := range existing.Properties {
				if ladybugType(p.Type) == colTypeString {
					allStringProps = append(allStringProps, p.Name)
				}
			}
			allStringProps = append(allStringProps, newStrings...)
			if err := rebuildFTSIndexForTable(db.conn, name, allStringProps); err != nil {
				return changed, fmt.Errorf("rebuild FTS index for repaired node table %q: %w", name, err)
			}
			changed = true
		} else if created, err := ensureFTSIndexOnConn(db.conn, name, def.Properties); err != nil {
			return changed, fmt.Errorf("ensure FTS index for node table %q: %w", name, err)
		} else if created {
			changed = true
		}
	}
	for _, name := range sortedKeys(edges) {
		def := edges[name]
		existing, ok := db.edgeTypeDefs[name]
		if !ok {
			if err := createRelTableOnConn(db.conn, name, def.Properties, pairs[name]); err != nil {
				return changed, fmt.Errorf("restore edge table %q from metadata: %w", name, err)
			}
			changed = true
			continue
		}
		newStrings, err := db.addMissingColumnsLocked(name, existing.Properties, def.Properties)
		if err != nil {
			return changed, fmt.Errorf("repair edge table %q from metadata: %w", name, err)
		}
		if len(newStrings) > 0 {
			changed = true
		}
	}
	return changed, nil
}

// addMissingColumnsLocked issues ALTER TABLE ADD for metadata properties the
// catalog table lacks, returning the names of any string columns added (for
// the caller's FTS rebuild). Idempotent and DDL-free when the catalog already
// matches the metadata.
func (sm *schemaManager) addMissingColumnsLocked(table string, existing, wanted []store.PropertyDef) ([]string, error) {
	db := sm.db
	existingByName := make(map[string]bool, len(existing))
	for _, p := range existing {
		existingByName[p.Name] = true
	}
	var newStrings []string
	for _, p := range wanted {
		if existingByName[p.Name] {
			continue
		}
		ddl := fmt.Sprintf("ALTER TABLE %s ADD %s %s;", quoteID(table), quoteID(p.Name), ladybugType(p.Type))
		r, err := db.conn.Query(ddl)
		if err != nil {
			return nil, fmt.Errorf("add column %q: %w", p.Name, err)
		}
		r.Close()
		if ladybugType(p.Type) == colTypeString {
			newStrings = append(newStrings, p.Name)
		}
	}
	return newStrings, nil
}

func restoreBranchSchemaMetadata(
	conn *lbug.Connection, path string,
	catalogEntities map[string]*store.EntityTypeDef, catalogEdges map[string]*store.EdgeTypeDef,
) (map[string]*store.EntityTypeDef, map[string]*store.EdgeTypeDef, error) {
	metadata, _, err := readSchemaMetadata(path, true)
	if err != nil {
		return nil, nil, err
	}
	entities, edges, _, _ := applySchemaMetadata(metadata)
	// Adopt the branch catalog's vector state into the branch metadata,
	// mirroring restoreMainSchemaMetadataLocked: a branch whose embedding
	// column/index bootstrap DDL landed before its metadata write (branch
	// CreateEntity/UpdateEntity, HydrateBranchFromFiles) would otherwise fail
	// validation on every reopen, and RecoverOpenTransactions treats that as
	// a hard startup failure instead of a recoverable branch state.
	reconciled, ddlIssued, err := reconcileVectorStateFromCatalog(conn, &metadata, entities)
	if err != nil {
		return nil, nil, err
	}
	if ddlIssued {
		// The completed index changed the branch catalog, so refresh the
		// catalog defs the validation compares against.
		refreshedEntities, refreshedEdges, rerr := rebuildBranchSchemaCache(conn)
		if rerr != nil {
			return nil, nil, rerr
		}
		catalogEntities, catalogEdges = refreshedEntities, refreshedEdges
	}
	if err := validateMetadataAgainstCatalog(conn, metadata, entities, edges, catalogEntities, catalogEdges); err != nil {
		return nil, nil, err
	}
	if reconciled {
		if err := writeSchemaMetadata(path, metadata); err != nil {
			return nil, nil, fmt.Errorf("persist reconciled branch schema metadata: %w", err)
		}
	}
	return entities, edges, nil
}

func validateMetadataAgainstCatalog(
	conn *lbug.Connection,
	metadata schemaMetadata,
	metadataEntities map[string]*store.EntityTypeDef, metadataEdges map[string]*store.EdgeTypeDef,
	catalogEntities map[string]*store.EntityTypeDef, catalogEdges map[string]*store.EdgeTypeDef,
) error {
	for name := range metadataEntities {
		if _, ok := catalogEntities[name]; !ok {
			return fmt.Errorf("schema metadata entity type %q is absent from database", name)
		}
	}
	for name := range metadataEdges {
		if _, ok := catalogEdges[name]; !ok {
			return fmt.Errorf("schema metadata edge type %q is absent from database", name)
		}
	}
	for name := range catalogEntities {
		// The `_untyped` placeholder NODE table (createRelTableOnConn) is an
		// internal table for edgeless rel types, legitimately absent from
		// schema metadata, so it is skipped here. The skip is safe and can
		// never mask a user type: schema.Validate rejects `_untyped` as a user
		// entity/edge type name (ErrReservedWord), so the metadata side cannot
		// legitimately carry it, and readSchemaMetadata's own Validate fails
		// loudly on a metadata file that declares it.
		if name == untypedTableName {
			continue
		}
		if _, ok := metadataEntities[name]; !ok {
			return fmt.Errorf("database entity type %q is absent from schema metadata", name)
		}
	}
	for name := range catalogEdges {
		if _, ok := metadataEdges[name]; !ok {
			return fmt.Errorf("database edge type %q is absent from schema metadata", name)
		}
	}
	for name, expected := range metadataEntities {
		actual := catalogEntities[name]
		// Required is application-only metadata. The catalog comparison below
		// deliberately verifies only each property's persisted name and DB type.
		if err := validateCatalogProperties(name, expected.Properties, actual.Properties); err != nil {
			return err
		}
		if actual.EnableVectorIndex != metadata.VectorIndexes[name] {
			return fmt.Errorf("entity type %q vector index does not match schema metadata", name)
		}
		actualDim, derr := getEmbeddingDimension(conn, name, expected.EnableVectorIndex)
		if derr != nil {
			return fmt.Errorf("entity type %q read vector dimension: %w", name, derr)
		}
		if actualDim != metadata.VectorDimensions[name] {
			return fmt.Errorf("entity type %q vector dimension does not match schema metadata", name)
		}
	}
	for name, expected := range metadataEdges {
		if err := validateCatalogProperties(name, expected.Properties, catalogEdges[name].Properties); err != nil {
			return err
		}
	}
	if conn != nil {
		// Rule grouping is also metadata-only; Ladybug exposes only the resulting
		// relationship FROM/TO pairs, so verify that catalog-verifiable projection.
		_, _, _, expectedPairs := applySchemaMetadata(metadata)
		for name := range metadataEdges {
			actualPairs, err := connectionPairsOnConn(conn, name)
			if err != nil {
				return fmt.Errorf("read relationship endpoints for %q: %w", name, err)
			}
			expected := expectedPairs[name]
			if len(expected) == 0 {
				expected = []fromToPair{{From: untypedTableName, To: untypedTableName}}
			}
			if !equalFromToPairs(actualPairs, expected) {
				return fmt.Errorf("relationship %q endpoints do not match schema metadata", name)
			}
		}
	}
	return nil
}

func validateCatalogProperties(table string, expected, actual []store.PropertyDef) error {
	expectedTypes := make(map[string]string, len(expected))
	for _, property := range expected {
		expectedTypes[property.Name] = ladybugType(property.Type)
	}
	actualTypes := make(map[string]string, len(actual))
	for _, property := range actual {
		actualTypes[property.Name] = property.Type
	}
	if len(expectedTypes) != len(actualTypes) {
		return fmt.Errorf("table %q properties do not match schema metadata", table)
	}
	for name, expectedType := range expectedTypes {
		if actualTypes[name] != expectedType {
			return fmt.Errorf("table %q property %q type does not match schema metadata", table, name)
		}
	}
	return nil
}

func connectionPairsOnConn(conn *lbug.Connection, table string) ([]fromToPair, error) {
	result, err := conn.Query(fmt.Sprintf("CALL show_connection('%s') RETURN *;", table))
	if err != nil {
		return nil, err
	}
	defer result.Close()
	var pairs []fromToPair
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
		if len(values) >= 2 {
			pairs = append(pairs, fromToPair{From: fmt.Sprint(values[0]), To: fmt.Sprint(values[1])})
		}
	}
	return pairs, nil
}

// connectionEdgePairs reads every edge type's FROM/TO endpoint pairs from its
// rel table in the catalog on an arbitrary connection. An edgeless edge type's
// `_untyped` placeholder pair is normalized away (the key stays absent) so the
// schema-metadata round-trip stays consistent with applySchemaMetadata's rule
// derivation and validateMetadataAgainstCatalog's `_untyped` fallback.
func connectionEdgePairs(conn *lbug.Connection, edges map[string]*store.EdgeTypeDef) (map[string][]fromToPair, error) {
	pairs := make(map[string][]fromToPair)
	untyped := []fromToPair{{From: untypedTableName, To: untypedTableName}}
	for _, name := range sortedKeys(edges) {
		actual, err := connectionPairsOnConn(conn, name)
		if err != nil {
			return nil, fmt.Errorf("read relationship endpoints for %q: %w", name, err)
		}
		if equalFromToPairs(actual, untyped) {
			continue
		}
		pairs[name] = actual
	}
	return pairs, nil
}

func equalFromToPairs(left, right []fromToPair) bool {
	keys := func(pairs []fromToPair) []string {
		result := make([]string, len(pairs))
		for i, pair := range pairs {
			result[i] = pair.From + "\x00" + pair.To
		}
		sort.Strings(result)
		return result
	}
	return slices.Equal(keys(left), keys(right))
}

func captureVectorState(conn *lbug.Connection, metadata schemaMetadata) (schemaMetadata, error) {
	indexes, err := vectorIndexesOnConn(conn)
	if err != nil {
		return schemaMetadata{}, err
	}
	if metadata.VectorIndexes == nil {
		metadata.VectorIndexes = make(map[string]bool)
	}
	if metadata.VectorDimensions == nil {
		metadata.VectorDimensions = make(map[string]int)
	}
	for _, entity := range metadata.EntityTypes {
		metadata.VectorIndexes[entity.Name] = indexes[entity.Name]
		dim, derr := getEmbeddingDimension(conn, entity.Name, entity.EnableVectorIndex)
		if derr != nil {
			return schemaMetadata{}, fmt.Errorf("read vector dimension for %q: %w", entity.Name, derr)
		}
		metadata.VectorDimensions[entity.Name] = dim
		if !entity.EnableVectorIndex &&
			(metadata.VectorIndexes[entity.Name] || metadata.VectorDimensions[entity.Name] > 0) {
			return schemaMetadata{}, fmt.Errorf("entity type %q cannot disable an existing vector index", entity.Name)
		}
	}
	return metadata, nil
}
