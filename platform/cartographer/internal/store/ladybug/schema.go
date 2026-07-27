package ladybug

import (
	"context"
	"fmt"
	"sort"
	"strings"

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
		// ponytail: show_indexes may not be supported in all modes; treat empty.
		return idxMap, nil
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
		if tableName != "" && indexType == "HNSW" {
			idxMap[tableName] = true
		}
	}

	return idxMap, nil
}

// getTableProperties queries table_info for the given table and returns its
// column definitions (excluding hidden/system columns).
func (db *ladybugDB) getTableProperties(tableName string) ([]store.PropertyDef, error) {
	q := fmt.Sprintf("CALL table_info('%s') RETURN *;", tableName)
	result, err := db.conn.Query(q)
	if err != nil {
		return nil, err
	}
	defer result.Close()

	// Skip implicit/self-managed columns that we don't expose as user properties.
	skip := map[string]bool{
		"id":          true,
		"_properties": true,
		"embedding":   true,
		"from":        true,
		"to":          true,
		"type":        true,
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
		if len(vals) < 2 {
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
// applies it to the database.
func (db *ladybugDB) ApplySchema(ctx context.Context, s *flowv1.Schema) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.closed {
		return store.ErrDatabaseNotReady
	}

	// Validate first.
	if err := schema.Validate(s); err != nil {
		return err
	}

	// Collect FROM/TO pairs for each edge type from entity-type rules.
	edgePairs := collectFromToPairs(s)
	db.edgePairs = edgePairs

	// Apply entity types.
	for _, et := range s.EntityTypes {
		if err := db.createNodeTable(et); err != nil {
			return fmt.Errorf("create node table %q: %w", et.Name, err)
		}
	}

	// Build rule index from entity types.
	db.ruleIndex = make(map[string][]*flowv1.ConnectionRule)
	vectorEnabled := make(map[string]bool)             // track which types have vector index enabled
	entityRequired := make(map[string]map[string]bool) // entityType -> propName -> required
	edgeRequired := make(map[string]map[string]bool)   // edgeType -> propName -> required
	for _, et := range s.EntityTypes {
		db.ruleIndex[et.Name] = et.Rules
		if et.EnableVectorIndex {
			vectorEnabled[et.Name] = true
		}
		for _, p := range et.Properties {
			if p.Required {
				if entityRequired[et.Name] == nil {
					entityRequired[et.Name] = make(map[string]bool)
				}
				entityRequired[et.Name][p.Name] = true
			}
		}
	}
	for _, et := range s.EdgeTypes {
		for _, p := range et.Properties {
			if p.Required {
				if edgeRequired[et.Name] == nil {
					edgeRequired[et.Name] = make(map[string]bool)
				}
				edgeRequired[et.Name][p.Name] = true
			}
		}
	}

	// Apply edge types.
	for _, et := range s.EdgeTypes {
		if err := db.createRelTable(et, edgePairs[et.Name]); err != nil {
			return fmt.Errorf("create rel table %q: %w", et.Name, err)
		}
	}

	// Rebuild cache (the cache gets EnableVectorIndex from the catalog, which
	// is false since we defer index creation to first entity bootstrap).
	// Patch it with the proto schema's EnableVectorIndex and Required values.
	if err := db.rebuildSchemaCacheLocked(); err != nil {
		return err
	}
	for name := range vectorEnabled {
		if def, ok := db.entityTypeDefs[name]; ok {
			def.EnableVectorIndex = true
		}
	}
	// Patch Required flags from the proto schema (not captured by catalog).
	for typeName, reqMap := range entityRequired {
		if def, ok := db.entityTypeDefs[typeName]; ok {
			for i := range def.Properties {
				if reqMap[def.Properties[i].Name] {
					def.Properties[i].Required = true
				}
			}
		}
	}
	for typeName, reqMap := range edgeRequired {
		if def, ok := db.edgeTypeDefs[typeName]; ok {
			for i := range def.Properties {
				if reqMap[def.Properties[i].Name] {
					def.Properties[i].Required = true
				}
			}
		}
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

		props, err := db.getTableProperties(tableName)
		if err != nil {
			return err
		}

		switch strings.ToUpper(tableType) {
		case "NODE":
			newEntity[tableName] = &store.EntityTypeDef{
				Name:              tableName,
				Properties:        props,
				EnableVectorIndex: hasVectorIdx[tableName],
			}
		case "REL":
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

// createNodeTable generates and executes CREATE NODE TABLE IF NOT EXISTS DDL.
// If the entity type has EnableVectorIndex, the embedding column is NOT created
// here — it is bootstrapped lazily on the first CreateEntity with an embedding.
// An FTS index is created on all string properties for full-text search.
func (db *ladybugDB) createNodeTable(et *flowv1.EntityType) error {
	cols := make([]string, 0, 1+len(et.Properties)+1)
	cols = append(cols, "id STRING PRIMARY KEY")
	var stringProps []string
	for _, p := range et.Properties {
		colType := ladybugType(p.Type)
		cols = append(cols, quoteID(p.Name)+" "+colType)
		if colType == colTypeString || colType == "STRING[]" {
			stringProps = append(stringProps, p.Name)
		}
	}
	cols = append(cols, "_properties STRING")

	ddl := fmt.Sprintf("CREATE NODE TABLE IF NOT EXISTS %s (%s);",
		quoteID(et.Name), strings.Join(cols, ", "))

	if _, err := db.conn.Query(ddl); err != nil {
		return err
	}

	// Create FTS index on all string properties.
	if len(stringProps) > 0 {
		propsList := "'" + strings.Join(stringProps, "', '") + "'"
		ftsDDL := fmt.Sprintf("CALL CREATE_FTS_INDEX('%s', '%s_fts', [%s], stemmer := 'porter');",
			et.Name, et.Name, propsList)
		_, _ = db.conn.Query(ftsDDL) // non-fatal; may fail if FTS ext not loaded
	}

	return nil
}

// createRelTable generates and executes CREATE REL TABLE IF NOT EXISTS DDL.
func (db *ladybugDB) createRelTable(et *flowv1.EdgeType, pairs []fromToPair) error {
	var ddl strings.Builder
	ddl.WriteString("CREATE REL TABLE IF NOT EXISTS ")
	ddl.WriteString(quoteID(et.Name))

	// Add FROM/TO pairs.
	var clauses []string
	for _, p := range pairs {
		clauses = append(clauses, fmt.Sprintf("FROM %s TO %s",
			quoteID(p.From), quoteID(p.To)))
	}
	// If no rules defined, use a placeholder.
	if len(clauses) == 0 {
		clauses = append(clauses, "FROM _untyped TO _untyped")
	}
	ddl.WriteString(" (")
	ddl.WriteString(strings.Join(clauses, ", "))

	// Add id and edge properties.
	ddl.WriteString(", id STRING")
	for _, p := range et.Properties {
		ddl.WriteString(", ")
		ddl.WriteString(quoteID(p.Name))
		ddl.WriteString(" ")
		ddl.WriteString(ladybugType(p.Type))
	}
	ddl.WriteString(", _properties STRING")

	ddl.WriteString(");")

	_, err := db.conn.Query(ddl.String())
	return err
}

// fromToPair describes a single FROM → TO clause for a rel table.
type fromToPair struct {
	From, To string
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

const colTypeString = "STRING"

// ladybugType maps the proto property type string to a LadybugDB column type.
// ponytail: Currently all user properties are "string"; if richer types are
// added to the proto, this mapping must grow.
func ladybugType(protoType string) string {
	switch strings.ToUpper(protoType) {
	case colTypeString, "":
		return colTypeString
	case "INT64":
		return "INT64"
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
	if !ok {
		return nil, false
	}
	return def, true
}

func (db *ladybugDB) EdgeType(name string) (*store.EdgeTypeDef, bool) {
	db.mu.Lock()
	defer db.mu.Unlock()

	def, ok := db.edgeTypeDefs[name]
	if !ok {
		return nil, false
	}
	return def, true
}

func (db *ladybugDB) TableExists(entityType string) bool {
	_, ok := db.EntityType(entityType)
	return ok
}

func (db *ladybugDB) ListMainEntityTypes() ([]string, error) {
	return db.EntityTypeNames(), nil
}

func (db *ladybugDB) ValidateSchema(_ context.Context, s *flowv1.Schema) error {
	return schema.Validate(s)
}

func (db *ladybugDB) Health(_ context.Context) (*store.HealthResult, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	result := &store.HealthResult{
		LadybugOK:     !db.closed,
		SchemaApplied: len(db.entityTypeDefs) > 0 || len(db.edgeTypeDefs) > 0,
		PVCWritable:   true, // ponytail: PVC writable is not checked at this layer
	}

	return result, nil
}
