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

func (db *ladybugDB) mainMetadataPath() string {
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
func (db *ladybugDB) persistMainVectorMetadataLocked() error {
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

func (db *ladybugDB) branchMetadataPath(txID string) string {
	return filepath.Join(db.path, "branches", txID+".schema.json")
}

func (db *ladybugDB) restoreMainSchemaMetadataLocked() error {
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
	if len(db.entityTypeDefs) == 0 && len(db.edgeTypeDefs) == 0 {
		for _, name := range sortedKeys(entities) {
			if err := createNodeTableOnConn(db.conn, name, entities[name].Properties); err != nil {
				return fmt.Errorf("restore node table %q from metadata: %w", name, err)
			}
			if dimension := metadata.VectorDimensions[name]; dimension > 0 {
				if _, err := db.conn.Query(fmt.Sprintf(
					"ALTER TABLE %s ADD embedding FLOAT[%d];", quoteID(name), dimension,
				)); err != nil {
					return fmt.Errorf("restore embedding column %q from metadata: %w", name, err)
				}
				if metadata.VectorIndexes[name] {
					if _, err := db.conn.Query(fmt.Sprintf(
						"CALL CREATE_VECTOR_INDEX('%s', '%s_vec', 'embedding', metric := 'cosine');", name, name,
					)); err != nil {
						return fmt.Errorf("restore vector index %q from metadata: %w", name, err)
					}
				}
			}
		}
		for _, name := range sortedKeys(edges) {
			if err := createRelTableOnConn(db.conn, name, edges[name].Properties, pairs[name]); err != nil {
				return fmt.Errorf("restore edge table %q from metadata: %w", name, err)
			}
		}
		if err := db.rebuildSchemaCacheLocked(); err != nil {
			return fmt.Errorf("rebuild restored schema catalog: %w", err)
		}
	}
	if err := validateMetadataAgainstCatalog(
		db.conn, metadata, entities, edges, db.entityTypeDefs, db.edgeTypeDefs,
	); err != nil {
		return err
	}
	db.entityTypeDefs, db.edgeTypeDefs = entities, edges
	db.ruleIndex, db.edgePairs = rules, pairs
	db.schemaApplied = true
	return nil
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
	if err := validateMetadataAgainstCatalog(conn, metadata, entities, edges, catalogEntities, catalogEdges); err != nil {
		return nil, nil, err
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
