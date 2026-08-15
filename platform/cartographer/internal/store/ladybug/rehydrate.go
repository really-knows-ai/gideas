package ladybug

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/foundry/flow/cartographer/internal/store"
)

// --------------------------------------------------------------------------
// Re-hydration source pre-flight
// --------------------------------------------------------------------------

// checkEdgesDirCompleteness is the shared file-tree completeness guard: a
// working tree where entities/ exists but edges/ was removed (SPEC R2 WipeGraph
// mid-wipe failure → INTERNAL) must fail loudly instead of loading entities and
// silently skipping every edge. Shared by RehydrateMainFromFiles (before the
// wipe, inside validateRehydrateSource, and after the entity load) and
// HydrateBranchFromFiles so the two hydration paths cannot diverge.
func checkEdgesDirCompleteness(entitiesDir, edgesDir string) error {
	if _, entErr := os.Stat(entitiesDir); entErr == nil {
		if _, edgeErr := os.Stat(edgesDir); os.IsNotExist(edgeErr) {
			return fmt.Errorf("%w: edges directory does not exist but entities directory exists", store.ErrInvalidEdgeDir)
		}
	}
	return nil
}

// validateRehydrateSource dry-runs the file-tree load against a throwaway
// in-memory database so RehydrateMainFromFiles can prove the source is fully
// loadable before wiping main (see RehydrateMainFromFiles). It runs the same
// loaders (parse → schema inference/DDL → insert) on isolated schema-definition
// copies, so a corrupt source fails with no side effect on main. The defs are
// deep-cloned because the loaders mutate them (registering inferred types,
// promoting vector flags) and the real load must run from a pristine snapshot.
//
// ponytail: the pre-flight re-loads the whole tree (parse + insert into an
// in-memory database) before the real load, doubling the CPU cost of every
// re-hydration — paid on each remote pull and each startup rebuild. The cost
// buys all-or-nothing re-hydration: without it, a load failure after the
// DETACH DELETE leaves main partially wiped and the SPEC R8 "automatic recovery
// on next startup" escape hatch serves a destroyed graph. Upgrade path: load
// once into the staging database and swap it onto main on success, eliminating
// the second pass.
func (db *ladybugDB) validateRehydrateSource(entitiesDir, edgesDir string) error {
	staging, err := lbug.OpenInMemoryDatabase(lbug.DefaultSystemConfig())
	if err != nil {
		return fmt.Errorf("open re-hydration staging database: %w", err)
	}
	defer staging.Close()
	conn, err := lbug.OpenConnection(staging)
	if err != nil {
		return fmt.Errorf("open re-hydration staging connection: %w", err)
	}
	defer conn.Close()
	if err := loadExtensionsOnConn(conn, "on re-hydration staging"); err != nil {
		return err
	}
	entDefs := make(map[string]*store.EntityTypeDef, len(db.entityTypeDefs))
	for name, def := range db.entityTypeDefs {
		cloned := cloneEntityTypeDef(def)
		entDefs[name] = &cloned
	}
	edgeDefs := make(map[string]*store.EdgeTypeDef, len(db.edgeTypeDefs))
	for name, def := range db.edgeTypeDefs {
		cloned := cloneEdgeTypeDef(def)
		edgeDefs[name] = &cloned
	}
	// Mirror main's schema onto the staging connection before the load: a fresh
	// staging DB has none of main's tables, and the loaders' DB-dependent checks
	// (the cross-type ID probe in insertEntityOnConn, the edge endpoint
	// resolution, connectionPairsOnConn) query every known type's table — a
	// missing table fails with a Binder exception and the pre-flight would
	// reject a tree the real load (on main, whose tables exist) accepts. The
	// embedding column/vector index and inferred types are handled by the
	// loaders themselves, exactly as on the real load.
	for name, def := range entDefs {
		if err := createNodeTableOnConn(conn, name, def.Properties); err != nil {
			return fmt.Errorf("replicate entity table %q on re-hydration staging: %w", name, err)
		}
	}
	for name, def := range edgeDefs {
		if err := createRelTableOnConn(conn, name, def.Properties, db.edgePairs[name]); err != nil {
			return fmt.Errorf("replicate edge table %q on re-hydration staging: %w", name, err)
		}
	}
	// Mirror the real load's sequence exactly (entities → completeness guard →
	// edges) so the pre-flight fails on precisely the same conditions the real
	// load would — before main is touched.
	if err := db.loadEntitiesFromDirOnConn(conn, entitiesDir, entDefs); err != nil {
		return err
	}
	if err := checkEdgesDirCompleteness(entitiesDir, edgesDir); err != nil {
		return err
	}
	if err := db.loadEdgesFromDirOnConn(conn, edgesDir, edgeDefs); err != nil {
		return err
	}
	return nil
}

// rebuildEdgePairsLocked re-wires db.edgePairs from the rel tables' actual
// FROM/TO endpoint pairs in the catalog. It runs on the re-hydration path
// (RehydrateMainFromFiles), which reassigns db.edgeTypeDefs and therefore must
// re-wire every cached structural handle that other paths read. The main load
// paths (ApplySchema, restoreMainSchemaMetadataLocked) derive edgePairs from
// connection rules, but types inferred from the directory structure (SPEC R8)
// carry no rules, so the catalog is the only authoritative source. An edgeless
// edge type's `_untyped` placeholder pair is normalized away (the key stays
// absent) so ReplicateSchemaToBranch's createRelTableOnConn takes its
// placeholder branch, which creates the `_untyped` NODE table the rel table's
// endpoint clause references. Callers must hold db.mu.
func (db *ladybugDB) rebuildEdgePairsLocked() error {
	pairs, err := connectionEdgePairs(db.conn, db.edgeTypeDefs)
	if err != nil {
		return err
	}
	db.edgePairs = pairs
	return nil
}

// --------------------------------------------------------------------------
// Internal helpers — file loading
// --------------------------------------------------------------------------

// ensureEntityLoadSchema makes the entity table exist before entities are
// loaded from files. When the type is absent from the applied schema (including
// an empty applied schema), the table is inferred on demand from the directory
// structure (SPEC R8). It also (re)creates the FTS index on the type's string
// properties so re-hydration restores the full search state (SPEC R8).
func (db *ladybugDB) ensureEntityLoadSchema(
	conn *lbug.Connection, typeName, typeDir string, entDefs map[string]*store.EntityTypeDef,
) error {
	def, known := entDefs[typeName]
	var props []store.PropertyDef
	if known {
		props = def.Properties
	} else {
		// Infer the schema from the directory structure (SPEC R8): when the
		// type is not in the applied schema, scan its JSON files to collect
		// the union of all property names so the created table has a real
		// column for every property a file may set. Without this, a
		// property-bearing file builds `CREATE (n:Type {col: $v})` against a
		// non-existent column and re-hydration of that type cannot succeed.
		// ponytail: inferred property types are always "string" because the
		// file-per-element representation stores property values as strings.
		// If a future representation carries non-string values, the column
		// type inference here would need corresponding handling.
		names := make(map[string]bool)
		// ponytail: this inference scan deliberately discards readDir /
		// os.ReadFile / json.Unmarshal errors. Propagating them would defeat
		// SPEC R8 inference from a partially-corrupt tree — one unparseable
		// file in a type directory would abort that type's schema inference
		// and take down re-hydration. The tolerance is self-correcting: the
		// strict load path (loadEntitiesFromDirOnConn) re-reads the same files
		// and fails loudly on any corrupt row, so a skipped file never results
		// in a silently dropped entity. Ceiling: a skipped file's properties
		// are absent from the inferred table, so a later row that references
		// them fails loudly at insert time — never silently.
		if files, err := db.readDir(typeDir); err == nil {
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(typeDir, f.Name()))
				if err != nil {
					continue
				}
				var je struct {
					Properties map[string]string `json:"properties"`
				}
				if err := json.Unmarshal(data, &je); err != nil {
					continue
				}
				for k := range je.Properties {
					names[k] = true
				}
			}
		}
		for name := range names {
			// Store the proto type ("string"), not the catalog type (colTypeString):
			// PropertyDef.Type is the proto type everywhere else, and schema.json
			// persists it verbatim — validateSchemaMetadata reconstructs a proto
			// schema from it on reopen, and schema.Validate rejects any type other
			// than "string" (ErrInvalidPropertyType), which would brick the next
			// file-backed Open (SPEC R8 corruption-recovery flow). createNodeTableOnConn
			// maps it to the catalog type via ladybugType, so DDL is unaffected.
			props = append(props, store.PropertyDef{Name: name, Type: "string"})
		}
		slices.SortFunc(props, func(a, b store.PropertyDef) int {
			return strings.Compare(a.Name, b.Name)
		})
	}
	if err := createNodeTableOnConn(conn, typeName, props); err != nil {
		return fmt.Errorf("create entity table %q for load: %w", typeName, err)
	}
	if !known {
		// Infer schema from the directory structure: register the type so
		// subsequent files of the same type are treated as known.
		entDefs[typeName] = &store.EntityTypeDef{Name: typeName, Properties: props}
	}
	return nil
}

// ensureEdgeLoadSchema makes the rel table exist before edges are loaded from
// files. When the type is absent from the applied schema (including an empty
// applied schema), the table is inferred on demand from the directory structure
// (SPEC R8), mirroring
// ensureEntityLoadSchema: the union of property names across the type's JSON
// files becomes real columns so a property-bearing file's
// `CREATE (a)-[:T {col: $v}]->(b)` does not target a non-existent column. The
// FROM/TO endpoint pairs are inferred by resolving each edge file's from/to
// entity IDs to their node labels — entities are loaded before edges on both
// the main and branch paths, so the endpoint tables already exist. This is
// required, not cosmetic: a rel table whose FROM/TO clauses do not name the
// endpoint node types silently drops the CREATE (no error, no edge), so the
// _untyped placeholder fallback would lose every inferred edge.
// ponytail: inferred property types are always "string" (same rationale as
// ensureEntityLoadSchema). If a future file representation carries non-string
// property values, inference here would need corresponding handling.
func (db *ladybugDB) ensureEdgeLoadSchema(
	conn *lbug.Connection, typeName, typeDir string, edgeDefs map[string]*store.EdgeTypeDef,
) error {
	if _, known := edgeDefs[typeName]; known {
		// The rel table already exists (created by ApplySchema or restored from
		// schema metadata); only types absent from the applied schema need
		// inference.
		return nil
	}
	names := make(map[string]bool)
	pairs := make(map[string]fromToPair) // "from|to" -> pair
	// ponytail: same deliberate error-tolerance as ensureEntityLoadSchema's
	// inference scan — readDir / os.ReadFile / json.Unmarshal errors are
	// skipped so SPEC R8 inference survives a partially-corrupt tree, while
	// the strict load path (loadEdgesFromDirOnConn) re-reads the same files
	// and fails loudly, so no row is silently dropped. Ceiling: a skipped
	// file's properties/pairs are absent from the inferred rel table, so a
	// later row that targets them fails loudly (or drops the CREATE) at
	// strict-load time rather than being silently inferred.
	if files, err := db.readDir(typeDir); err == nil {
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(typeDir, f.Name()))
			if err != nil {
				continue
			}
			var je struct {
				From       string            `json:"from"`
				To         string            `json:"to"`
				Properties map[string]string `json:"properties"`
			}
			if err := json.Unmarshal(data, &je); err != nil {
				continue
			}
			for k := range je.Properties {
				names[k] = true
			}
			if je.From == "" || je.To == "" {
				continue
			}
			fromType, err := nodeLabelOnConn(conn, je.From)
			if err != nil {
				return fmt.Errorf("resolve edge %q from endpoint %q: %w", typeName, je.From, err)
			}
			toType, err := nodeLabelOnConn(conn, je.To)
			if err != nil {
				return fmt.Errorf("resolve edge %q to endpoint %q: %w", typeName, je.To, err)
			}
			pairs[fromType+"|"+toType] = fromToPair{From: fromType, To: toType}
		}
	}
	var props []store.PropertyDef
	for name := range names {
		// Store the proto type ("string"), not the catalog type (colTypeString):
		// PropertyDef.Type is the proto type everywhere else, and schema.json
		// persists it verbatim (see ensureEntityLoadSchema for the full
		// rationale). createRelTableOnConn maps it to the catalog type via
		// ladybugType, so DDL is unaffected.
		props = append(props, store.PropertyDef{Name: name, Type: "string"})
	}
	slices.SortFunc(props, func(a, b store.PropertyDef) int {
		return strings.Compare(a.Name, b.Name)
	})
	var pairList []fromToPair
	for _, p := range pairs {
		pairList = append(pairList, p)
	}
	slices.SortFunc(pairList, func(a, b fromToPair) int {
		if c := strings.Compare(a.From, b.From); c != 0 {
			return c
		}
		return strings.Compare(a.To, b.To)
	})
	if err := createRelTableOnConn(conn, typeName, props, pairList); err != nil {
		return fmt.Errorf("create edge table %q for load: %w", typeName, err)
	}
	// Infer schema from the directory structure: register the type so
	// subsequent files of the same type are treated as known.
	edgeDefs[typeName] = &store.EdgeTypeDef{Name: typeName, Properties: props}
	return nil
}

// ensureEmbeddingLoadSchema adds the embedding column (and vector index) to the
// entity table when an entity carrying an embedding is loaded. The dimension is
// taken from the first embedding seen for the type. It also marks the type's
// definition as vector-enabled in defs so the in-memory model stays in parity
// with the column/index it creates during re-hydration.
func (db *ladybugDB) ensureEmbeddingLoadSchema(
	conn *lbug.Connection, typeName string, embedding []float32,
	defs map[string]*store.EntityTypeDef,
) error {
	vectorIndexed := false
	if def, ok := defs[typeName]; ok {
		vectorIndexed = def.EnableVectorIndex
	}
	if dim, derr := getEmbeddingDimension(conn, typeName, vectorIndexed); derr != nil {
		return fmt.Errorf("read embedding dimension for %q: %w", typeName, derr)
	} else if dim > 0 {
		return nil
	}
	altDDL := fmt.Sprintf("ALTER TABLE %s ADD embedding FLOAT[%d];", quoteID(typeName), len(embedding))
	r, err := conn.Query(altDDL)
	if err != nil {
		return fmt.Errorf("ensure embedding column %q: %w", typeName, err)
	}
	r.Close()
	if err := db.createVectorIndex(conn, typeName); err != nil {
		return fmt.Errorf("ensure vector index %q: %w", typeName, err)
	}
	// Keep the definition in parity with the column/index just created: a type
	// whose schema def stays EnableVectorIndex=false while the table now carries
	// an embedding column and vector index would diverge from the metadata model
	// (captureVectorState/ValidateMetadataAgainstCatalog require VectorIndexes to
	// match def.EnableVectorIndex) and from the query path (SearchNeighbors reads
	// EnableVectorIndex to decide which types are searchable). Re-hydration is the
	// only path that can create a vector index without a type first declared with
	// EnableVectorIndex=true (inferred directory schema, SPEC R8), so we promote
	// the flag here on the file-load path to keep the def consistent with the
	// database.
	if def, ok := defs[typeName]; ok {
		def.EnableVectorIndex = true
	}
	return nil
}

// loadEntitiesFromDirOnConn loads entities from JSON files onto an arbitrary
// connection. It is the shared loader for both main re-hydration
// (RehydrateMainFromFiles passes db.conn) and branch hydration
// (HydrateBranchFromFiles passes br.conn); the former per-connection
// duplicates of this function were deleted.
func (db *ladybugDB) loadEntitiesFromDirOnConn(conn *lbug.Connection, dir string,
	entDefs map[string]*store.EntityTypeDef) error {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %q is not a directory", store.ErrInvalidEntityDir, dir)
	}
	entries, err := db.readDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		typeName := entry.Name()
		// No schema-absent skip: a type directory present in the git
		// file-per-element representation but absent from the applied schema is
		// inferred from the directory structure by ensureEntityLoadSchema so
		// re-hydration recovers the full graph state (SPEC R8). Silently
		// skipping committed files would drop rows on the read path.
		typeDir := filepath.Join(dir, typeName)
		if err := db.ensureEntityLoadSchema(conn, typeName, typeDir, entDefs); err != nil {
			return err
		}
		files, err := db.readDir(typeDir)
		if err != nil {
			return fmt.Errorf("read entities dir %q: %w", typeDir, err)
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(typeDir, f.Name()))
			if err != nil {
				return fmt.Errorf("read entity file %q: %w", filepath.Join(typeDir, f.Name()), err)
			}
			var je struct {
				ID         string            `json:"id"`
				Type       string            `json:"type"`
				Properties map[string]string `json:"properties"`
				Embedding  []float32         `json:"embedding"`
				CreatedAt  time.Time         `json:"created_at"`
				UpdatedAt  time.Time         `json:"updated_at"`
			}
			if err := json.Unmarshal(data, &je); err != nil {
				return fmt.Errorf("%w: unparseable entity file %q: %v",
					store.ErrInvalidEntityDir, filepath.Join(typeDir, f.Name()), err)
			}
			if je.Type == "" {
				return fmt.Errorf("%w: entity file %q is missing required key 'type'",
					store.ErrInvalidEntityDir, filepath.Join(typeDir, f.Name()))
			}
			if je.ID == "" {
				return fmt.Errorf("%w: entity file %q is missing required key 'id'",
					store.ErrInvalidEntityDir, filepath.Join(typeDir, f.Name()))
			}
			// The raw filename base must equal the embedded id — the sibling
			// gitstore read path's invariant (ReadAllEntityFiles rejects a file
			// whose embedded id conflicts with its filename). A well-formed
			// file is <id>.json whose embedded id equals the filename base
			// (writeEntityFile); a corrupt/hand-edited file such as
			// wrongname.json containing a canonical id would otherwise load
			// silently under an id never written to that path — resurrecting a
			// previously deleted element on re-hydration. Fail loudly instead.
			if base := strings.TrimSuffix(f.Name(), ".json"); base != je.ID {
				return fmt.Errorf("%w: entity file %s embedded id %s conflicts with filename",
					store.ErrInvalidEntityDir, f.Name(), je.ID)
			}
			// The embedded id must be a canonical RFC4122 §3 UUID v4 string —
			// the same gate the write path applies (validateUUID →
			// store.ErrInvalidIDFormat, crud.go) and the sibling gitstore read
			// path enforces (ReadAllEntityFiles rejects a non-canonical embedded
			// id with ErrInvalidUUID). A corrupt/hand-edited file whose id is a
			// non-canonical spelling of a valid UUID (uppercase hex, no-hyphen,
			// braced, urn:uuid:) would otherwise load silently under that
			// spelling during re-hydration (SPEC R8) — a second row for one
			// UUID the write path would never produce. Fail loudly instead.
			if err := validateUUID(je.ID); err != nil {
				return fmt.Errorf("%w: entity file %s embedded id %s is not a valid UUID v4",
					err, filepath.Join(typeDir, f.Name()), je.ID)
			}
			// The directory-mismatch guard runs BEFORE the embedding-bootstrap
			// DDL: a corrupt/hand-edited file declaring a type that differs from
			// its directory must be rejected before ensureEmbeddingLoadSchema
			// locks a vector dimension on the directory-named table (SPEC R8
			// fail-loudly — never mutate schema state for a file that is about
			// to be rejected).
			if je.Type != typeName {
				return fmt.Errorf("%w: entity file %q declares type %q but is stored under directory %q",
					store.ErrInvalidEntityDir, filepath.Join(typeDir, f.Name()), je.Type, typeName)
			}
			if len(je.Embedding) > 0 {
				if err := db.ensureEmbeddingLoadSchema(conn, typeName, je.Embedding, entDefs); err != nil {
					return err
				}
			}
			props := je.Properties
			if props == nil {
				props = make(map[string]string)
			}
			entity := &store.Entity{
				Id: je.ID, Type: je.Type, Properties: props,
				Embedding: je.Embedding,
				CreatedAt: je.CreatedAt, UpdatedAt: je.UpdatedAt,
			}
			if err := insertEntityOnConn(conn, typeName, entity, entDefs); err != nil {
				return fmt.Errorf("insert entity %q: %w", je.ID, err)
			}
		}
	}
	return nil
}

// loadEdgesFromDirOnConn loads edges from JSON files onto an arbitrary
// connection. It is the shared loader for both main re-hydration
// (RehydrateMainFromFiles passes db.conn) and branch hydration
// (HydrateBranchFromFiles passes br.conn); the former per-connection
// duplicates of this function were deleted.
func (db *ladybugDB) loadEdgesFromDirOnConn(conn *lbug.Connection, dir string,
	edgeDefs map[string]*store.EdgeTypeDef) error {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %q is not a directory", store.ErrInvalidEdgeDir, dir)
	}
	entries, err := db.readDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		typeName := entry.Name()
		// No schema-absent skip: a type directory present in the git
		// file-per-element representation but absent from the applied schema is
		// inferred from the directory structure by ensureEdgeLoadSchema so
		// re-hydration recovers the full graph state (SPEC R8). Silently
		// skipping committed files would drop rows on the read path.
		typeDir := filepath.Join(dir, typeName)
		if err := db.ensureEdgeLoadSchema(conn, typeName, typeDir, edgeDefs); err != nil {
			return err
		}
		// The rel table's endpoint clauses (fixed at CREATE time, SPEC R2) are
		// the labels insertEdgeOnConn's endpoint probe must accept.
		pairs, err := connectionPairsOnConn(conn, typeName)
		if err != nil {
			return fmt.Errorf("read relationship endpoints for %q: %w", typeName, err)
		}
		files, err := db.readDir(typeDir)
		if err != nil {
			return fmt.Errorf("read edges dir %q: %w", typeDir, err)
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(typeDir, f.Name()))
			if err != nil {
				return fmt.Errorf("read edge file %q: %w", filepath.Join(typeDir, f.Name()), err)
			}
			var je struct {
				ID         string            `json:"id"`
				Type       string            `json:"type"`
				From       string            `json:"from"`
				To         string            `json:"to"`
				Properties map[string]string `json:"properties"`
				CreatedAt  time.Time         `json:"created_at"`
				UpdatedAt  time.Time         `json:"updated_at"`
			}
			if err := json.Unmarshal(data, &je); err != nil {
				return fmt.Errorf("%w: unparseable edge file %q: %v",
					store.ErrInvalidEdgeDir, filepath.Join(typeDir, f.Name()), err)
			}
			if je.Type == "" || je.From == "" || je.To == "" {
				return fmt.Errorf("%w: edge file %q is missing required keys (type, from, to)",
					store.ErrInvalidEdgeDir, filepath.Join(typeDir, f.Name()))
			}
			if je.ID == "" {
				return fmt.Errorf("%w: edge file %q is missing required key 'id'",
					store.ErrInvalidEdgeDir, filepath.Join(typeDir, f.Name()))
			}
			// The raw filename base must equal the embedded id — the sibling
			// gitstore read path's invariant (ReadAllEdgeFiles rejects a file
			// whose embedded id conflicts with its filename). A well-formed
			// file is <id>.json whose embedded id equals the filename base
			// (writeEdgeFile); a corrupt/hand-edited file such as wrongname.json
			// containing a canonical id would otherwise load silently under an
			// id never written to that path — resurrecting a previously deleted
			// element on re-hydration. Fail loudly instead.
			if base := strings.TrimSuffix(f.Name(), ".json"); base != je.ID {
				return fmt.Errorf("%w: edge file %s embedded id %s conflicts with filename",
					store.ErrInvalidEdgeDir, f.Name(), je.ID)
			}
			// The embedded id must be a canonical RFC4122 §3 UUID v4 string —
			// the same gate the write path applies (validateUUID →
			// store.ErrInvalidIDFormat, crud.go) and the sibling gitstore read
			// path enforces (ReadAllEdgeFiles rejects a non-canonical embedded
			// id with ErrInvalidUUID). A corrupt/hand-edited file whose id is a
			// non-canonical spelling of a valid UUID (uppercase hex, no-hyphen,
			// braced, urn:uuid:) would otherwise load silently under that
			// spelling during re-hydration (SPEC R8) — a second row for one
			// UUID the write path would never produce. Fail loudly instead.
			if err := validateUUID(je.ID); err != nil {
				return fmt.Errorf("%w: edge file %s embedded id %s is not a valid UUID v4",
					err, filepath.Join(typeDir, f.Name()), je.ID)
			}
			if je.Type != typeName {
				return fmt.Errorf("%w: edge file %q declares type %q but is stored under directory %q",
					store.ErrInvalidEdgeDir, filepath.Join(typeDir, f.Name()), je.Type, typeName)
			}
			props := je.Properties
			if props == nil {
				props = make(map[string]string)
			}
			edge := &store.Edge{
				Id: je.ID, Type: je.Type,
				FromEntityID: je.From, ToEntityID: je.To,
				Properties: props,
				CreatedAt:  je.CreatedAt, UpdatedAt: je.UpdatedAt,
			}
			if err := insertEdgeOnConn(conn, typeName, pairs, edge); err != nil {
				return fmt.Errorf("insert edge %q: %w", je.ID, err)
			}
		}
	}
	return nil
}
