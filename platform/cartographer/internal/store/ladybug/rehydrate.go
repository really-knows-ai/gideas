package ladybug

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/foundry/flow/cartographer/internal/store"
)

// rehydrator owns the re-hydration method group of ladybugDB: the file-tree
// pre-flight, the shared file loaders, and the main/branch re-hydration entry
// points (RehydrateMainFromFiles, RehydrateFromBranch, HydrateBranchFromFiles)
// plus the branch dump/scan paths. The shared store state lives on ladybugDB;
// db is the owner pointer back to it.
type rehydrator struct {
	db *ladybugDB
}

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
func (rh *rehydrator) validateRehydrateSource(entitiesDir, edgesDir string) error {
	db := rh.db
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
func (rh *rehydrator) rebuildEdgePairsLocked() error {
	db := rh.db
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

// jsonFileRow is the union of the entity and edge file-per-element JSON shapes
// parsed on the re-hydration load paths. The shared loaders parse one shape;
// each variant reads only the keys it needs.
type jsonFileRow struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	From       string            `json:"from"`
	To         string            `json:"to"`
	Properties map[string]string `json:"properties"`
	Embedding  []float32         `json:"embedding"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

// inferPropertyNamesFromDir scans typeDir's *.json files and collects the
// union of property names, so a schema-absent type's created table has a real
// column for every property a file may set (SPEC R8).
// ponytail: this inference scan deliberately discards readDir / os.ReadFile /
// json.Unmarshal errors. Propagating them would defeat SPEC R8 inference from
// a partially-corrupt tree — one unparseable file in a type directory would
// abort that type's schema inference and take down re-hydration. The tolerance
// is self-correcting: the strict load path (loadEntitiesFromDirOnConn /
// loadEdgesFromDirOnConn) re-reads the same files and fails loudly on any
// corrupt row, so a skipped file never results in a silently dropped element.
// Ceiling: a skipped file's properties are absent from the inferred table, so
// a later row that references them fails loudly at insert time (or its CREATE
// is dropped) — never silently.
// perFile, when non-nil, runs after each successful parse so callers can also
// collect non-property structure (the edge variant resolves FROM/TO endpoint
// pairs here); a perFile error propagates, since the edge variant's endpoint
// resolution failure is a hard error.
func (rh *rehydrator) inferPropertyNamesFromDir(
	typeDir string, perFile func(je jsonFileRow) error,
) (map[string]bool, error) {
	db := rh.db
	names := make(map[string]bool)
	if files, err := db.readDir(typeDir); err == nil {
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(typeDir, f.Name()))
			if err != nil {
				continue
			}
			var je jsonFileRow
			if err := json.Unmarshal(data, &je); err != nil {
				continue
			}
			for k := range je.Properties {
				names[k] = true
			}
			if perFile != nil {
				if err := perFile(je); err != nil {
					return nil, err
				}
			}
		}
	}
	return names, nil
}

// ensureEntityLoadSchema makes the entity table exist before entities are
// loaded from files. When the type is absent from the applied schema (including
// an empty applied schema), the table is inferred on demand from the directory
// structure (SPEC R8). It also (re)creates the FTS index on the type's string
// properties so re-hydration restores the full search state (SPEC R8).
func (rh *rehydrator) ensureEntityLoadSchema(
	conn *lbug.Connection, typeName, typeDir string, entDefs map[string]*store.EntityTypeDef,
) error {
	db := rh.db
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
		names, err := db.inferPropertyNamesFromDir(typeDir, nil)
		if err != nil {
			return err
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
// (SPEC R8), sharing the property-union inference scan with
// ensureEntityLoadSchema via inferPropertyNamesFromDir: the union of property
// names across the type's JSON files becomes real columns so a
// property-bearing file's
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
func (rh *rehydrator) ensureEdgeLoadSchema(
	conn *lbug.Connection, typeName, typeDir string, edgeDefs map[string]*store.EdgeTypeDef,
) error {
	db := rh.db
	if _, known := edgeDefs[typeName]; known {
		// The rel table already exists (created by ApplySchema or restored from
		// schema metadata); only types absent from the applied schema need
		// inference.
		return nil
	}
	pairs := make(map[string]fromToPair) // "from|to" -> pair
	names, err := db.inferPropertyNamesFromDir(typeDir, func(je jsonFileRow) error {
		if je.From == "" || je.To == "" {
			return nil
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
		return nil
	})
	if err != nil {
		return err
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
func (rh *rehydrator) ensureEmbeddingLoadSchema(
	conn *lbug.Connection, typeName string, embedding []float32,
	defs map[string]*store.EntityTypeDef,
) error {
	db := rh.db
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

// fileLoader is the entity-vs-edge difference for loadDirFilesOnConn: the
// error sentinel and message noun, the ensure-schema and endpoint-pair hooks,
// the required-key guard, and the per-file insert (which for entities also
// bootstraps the embedding column).
type fileLoader struct {
	noun     string
	dirNoun  string
	errDir   error
	ensure   func(conn *lbug.Connection, typeName, typeDir string) error
	pairs    func(conn *lbug.Connection, typeName string) ([]fromToPair, error)
	required func(je *jsonFileRow, path string) error
	insert   func(conn *lbug.Connection, typeName string, pairs []fromToPair, je *jsonFileRow) error
}

// loadDirFilesOnConn loads JSON files from a directory tree onto an arbitrary
// connection, one type subdirectory at a time. It is the shared skeleton of
// the entity and edge loaders (loadEntitiesFromDirOnConn /
// loadEdgesFromDirOnConn), which differ only in the ensure-schema step, the
// required-key guard, and the per-file insert; every shared guard runs here so
// the two paths cannot diverge. Both loaders serve main re-hydration
// (RehydrateMainFromFiles passes db.conn) and branch hydration
// (HydrateBranchFromFiles passes br.conn).
func (rh *rehydrator) loadDirFilesOnConn(
	conn *lbug.Connection, dir string, l fileLoader,
) error {
	db := rh.db
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %q is not a directory", l.errDir, dir)
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
		// inferred from the directory structure by the ensure-schema step so
		// re-hydration recovers the full graph state (SPEC R8). Silently
		// skipping committed files would drop rows on the read path.
		typeDir := filepath.Join(dir, typeName)
		if err := l.ensure(conn, typeName, typeDir); err != nil {
			return err
		}
		// The rel table's endpoint clauses (fixed at CREATE time, SPEC R2) are
		// the labels insertEdgeOnConn's endpoint probe must accept. Entities
		// carry no endpoint set.
		var pairs []fromToPair
		if l.pairs != nil {
			pairs, err = l.pairs(conn, typeName)
			if err != nil {
				return err
			}
		}
		files, err := db.readDir(typeDir)
		if err != nil {
			return fmt.Errorf("read %s dir %q: %w", l.dirNoun, typeDir, err)
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(typeDir, f.Name()))
			if err != nil {
				return fmt.Errorf("read %s file %q: %w", l.noun, filepath.Join(typeDir, f.Name()), err)
			}
			var je jsonFileRow
			if err := json.Unmarshal(data, &je); err != nil {
				return fmt.Errorf("%w: unparseable %s file %q: %v",
					l.errDir, l.noun, filepath.Join(typeDir, f.Name()), err)
			}
			if err := l.required(&je, filepath.Join(typeDir, f.Name())); err != nil {
				return err
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
				return fmt.Errorf("%w: %s file %s embedded id %s conflicts with filename",
					l.errDir, l.noun, f.Name(), je.ID)
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
				return fmt.Errorf("%w: %s file %s embedded id %s is not a valid UUID v4",
					err, l.noun, filepath.Join(typeDir, f.Name()), je.ID)
			}
			// The directory-mismatch guard runs BEFORE the per-file insert, so
			// for entities it fires before the embedding-bootstrap DDL: a
			// corrupt/hand-edited file declaring a type that differs from its
			// directory must be rejected before ensureEmbeddingLoadSchema locks
			// a vector dimension on the directory-named table (SPEC R8
			// fail-loudly — never mutate schema state for a file that is about
			// to be rejected).
			if je.Type != typeName {
				return fmt.Errorf("%w: %s file %q declares type %q but is stored under directory %q",
					l.errDir, l.noun, filepath.Join(typeDir, f.Name()), je.Type, typeName)
			}
			if err := l.insert(conn, typeName, pairs, &je); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadEntitiesFromDirOnConn loads entities from JSON files onto an arbitrary
// connection. It is the shared loader for both main re-hydration
// (RehydrateMainFromFiles passes db.conn) and branch hydration
// (HydrateBranchFromFiles passes br.conn); the former per-connection
// duplicates of this function were deleted.
func (rh *rehydrator) loadEntitiesFromDirOnConn(conn *lbug.Connection, dir string,
	entDefs map[string]*store.EntityTypeDef) error {
	return rh.loadDirFilesOnConn(conn, dir, fileLoader{
		noun:    "entity",
		dirNoun: "entities",
		errDir:  store.ErrInvalidEntityDir,
		ensure: func(conn *lbug.Connection, typeName, typeDir string) error {
			return rh.ensureEntityLoadSchema(conn, typeName, typeDir, entDefs)
		},
		required: func(je *jsonFileRow, path string) error {
			if je.Type == "" {
				return fmt.Errorf("%w: entity file %q is missing required key 'type'",
					store.ErrInvalidEntityDir, path)
			}
			if je.ID == "" {
				return fmt.Errorf("%w: entity file %q is missing required key 'id'",
					store.ErrInvalidEntityDir, path)
			}
			return nil
		},
		insert: func(conn *lbug.Connection, typeName string, _ []fromToPair, je *jsonFileRow) error {
			if len(je.Embedding) > 0 {
				if err := rh.ensureEmbeddingLoadSchema(conn, typeName, je.Embedding, entDefs); err != nil {
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
			return nil
		},
	})
}

// loadEdgesFromDirOnConn loads edges from JSON files onto an arbitrary
// connection. It is the shared loader for both main re-hydration
// (RehydrateMainFromFiles passes db.conn) and branch hydration
// (HydrateBranchFromFiles passes br.conn); the former per-connection
// duplicates of this function were deleted.
func (rh *rehydrator) loadEdgesFromDirOnConn(conn *lbug.Connection, dir string,
	edgeDefs map[string]*store.EdgeTypeDef) error {
	return rh.loadDirFilesOnConn(conn, dir, fileLoader{
		noun:    "edge",
		dirNoun: "edges",
		errDir:  store.ErrInvalidEdgeDir,
		ensure: func(conn *lbug.Connection, typeName, typeDir string) error {
			return rh.ensureEdgeLoadSchema(conn, typeName, typeDir, edgeDefs)
		},
		pairs: func(conn *lbug.Connection, typeName string) ([]fromToPair, error) {
			pairs, err := connectionPairsOnConn(conn, typeName)
			if err != nil {
				return nil, fmt.Errorf("read relationship endpoints for %q: %w", typeName, err)
			}
			return pairs, nil
		},
		required: func(je *jsonFileRow, path string) error {
			if je.Type == "" || je.From == "" || je.To == "" {
				return fmt.Errorf("%w: edge file %q is missing required keys (type, from, to)",
					store.ErrInvalidEdgeDir, path)
			}
			if je.ID == "" {
				return fmt.Errorf("%w: edge file %q is missing required key 'id'",
					store.ErrInvalidEdgeDir, path)
			}
			return nil
		},
		insert: func(conn *lbug.Connection, typeName string, pairs []fromToPair, je *jsonFileRow) error {
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
			return nil
		},
	})
}

// RehydrateFromBranch replaces main DB data with the branch data.
// For in-memory mode we wipe main and bulk-insert from branch queries.
func (rh *rehydrator) RehydrateFromBranch(ctx context.Context, txID string) error {
	db := rh.db
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
	// Snapshot entity/edge defs before releasing lock for branch work.
	entDefs := make(map[string]*store.EntityTypeDef)
	maps.Copy(entDefs, br.entityTypeDefs)
	edgeDefs := make(map[string]*store.EdgeTypeDef)
	maps.Copy(edgeDefs, br.edgeTypeDefs)
	// Wipe all data from main.
	result, err := db.conn.Query("MATCH (n) DETACH DELETE n;")
	if err != nil {
		return fmt.Errorf("wipe main: %w", err)
	}
	result.Close()

	// Copy all entities from branch to main.
	for _, name := range sortedKeys(entDefs) {
		stmt, err := br.conn.Prepare(fmt.Sprintf("MATCH (n:%s) RETURN n;", quoteID(name)))
		if err != nil {
			return fmt.Errorf("query branch entities for %q: %w", name, err)
		}
		result, err := br.conn.Execute(stmt, map[string]any{})
		stmt.Close()
		if err != nil {
			return fmt.Errorf("execute branch query for %q: %w", name, err)
		}
		for result.HasNext() {
			tuple, err := result.Next()
			if err != nil {
				result.Close()
				return fmt.Errorf("read branch entity: %w", err)
			}
			m, err := tuple.GetAsMap()
			tuple.Close()
			if err != nil {
				result.Close()
				return fmt.Errorf("parse branch entity: %w", err)
			}
			node, ok := m["n"].(lbug.Node)
			if !ok {
				result.Close()
				return fmt.Errorf("branch entity of type %q: unexpected node type %T", name, m["n"])
			}
			entity := entityFromNode(node, name, entDefs[name].EnableVectorIndex)
			// Ensure main's embedding column / vector index exists before
			// inserting an entity that carries an embedding. The branch may have
			// bootstrapped the dimension (SPEC R7 lazy bootstrap on the first
			// embedding write), so main's table need not have the embedding
			// column yet — the copy path leads with an entity that targets it.
			if len(entity.Embedding) > 0 {
				if err := db.ensureEmbeddingLoadSchema(db.conn, name, entity.Embedding, db.entityTypeDefs); err != nil {
					result.Close()
					return fmt.Errorf("promote embedding schema to main for %q: %w", name, err)
				}
			}
			if err := insertEntityOnConn(db.conn, name, entity, entDefs); err != nil {
				result.Close()
				return fmt.Errorf("insert entity into main: %w", err)
			}
		}
		result.Close()
	}

	// Copy all edges from branch to main.
	for _, name := range sortedKeys(edgeDefs) {
		// The CREATE below targets main's rel table, so main's FROM/TO endpoint
		// clauses (SPEC R2, fixed at CREATE time) are the labels the endpoint
		// probe must accept.
		pairs, err := connectionPairsOnConn(db.conn, name)
		if err != nil {
			return fmt.Errorf("read relationship endpoints for %q: %w", name, err)
		}
		edges, err := listEdgesOnConn(br.conn, name)
		if err != nil {
			return fmt.Errorf("query branch edges for %q: %w", name, err)
		}
		for _, edge := range edges {
			if err := insertEdgeOnConn(db.conn, name, pairs, &edge); err != nil {
				return fmt.Errorf("insert edge into main: %w", err)
			}
		}
	}

	// Persist main's schema metadata (capturing the vector index/dimension the
	// copy path promoted to main above) so a reopen's
	// validateMetadataAgainstCatalog does not fail closed. A branch that
	// bootstrapped the embedding column/vector index on its first write (SPEC
	// R7 lazy bootstrap) promoted that vector state to main's catalog here, but
	// main's schema.json (written at the original ApplySchema) still records
	// VectorIndexes=false/VectorDimensions=0 for the type; without rewriting
	// it, restart bricks startup with a catalog/metadata mismatch.
	if err := db.persistMainVectorMetadataLocked(); err != nil {
		return err
	}

	return nil
}

// RehydrateMainFromFiles loads entities/edges from JSON files into main.
// It holds db.mu for the entire wipe-and-load cycle so that concurrent reads
// never observe partially reconstructed state. The load is atomic with respect
// to the source: the entire file tree is validated into a throwaway database
// before the DETACH DELETE below, so a corrupt source (e.g. a corrupt merged
// JSON from the remote) fails with main untouched instead of wiping main first
// and leaving it partially loaded.
func (rh *rehydrator) RehydrateMainFromFiles(ctx context.Context, entitiesDir, edgesDir string) error {
	db := rh.db
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.closed || db.failed {
		return store.ErrDatabaseNotReady
	}

	entDefs := make(map[string]*store.EntityTypeDef)
	maps.Copy(entDefs, db.entityTypeDefs)
	edgeDefs := make(map[string]*store.EdgeTypeDef)
	maps.Copy(edgeDefs, db.edgeTypeDefs)

	// Pre-flight: prove the entire file tree loads cleanly before main is
	// touched. The wipe below precedes the loads, so without this a failure
	// during the loads — the persistent case being a corrupt merged JSON pulled
	// by the sync worker — leaves main.lbug partially wiped, and the caller's
	// cycle returns with main serving a silently-incomplete graph (SPEC
	// error-table row "Sync re-hydration failed" → INTERNAL; its R8
	// "automatic recovery on next startup" escape hatch presupposes a
	// consistent graph to serve, which a wiped main is not). The pre-flight
	// runs the shared loaders against a throwaway in-memory database, so a
	// corrupt source fails here with main untouched and the caller's recovery
	// path (the worker's next-cycle retry, or R8 on the next startup) has the
	// pre-existing graph to keep serving.
	if err := db.validateRehydrateSource(entitiesDir, edgesDir); err != nil {
		return err
	}

	// Wipe everything — use db.conn directly since we hold db.mu.
	result, err := db.conn.Query("MATCH (n) DETACH DELETE n;")
	if err != nil {
		return fmt.Errorf("delete graph data: %w", err)
	}
	result.Close()

	// Read entities from JSON files.
	if err := db.loadEntitiesFromDirOnConn(db.conn, entitiesDir, entDefs); err != nil {
		return err
	}
	// Fail if entities dir exists but edges dir does not (partial wipe).
	if err := checkEdgesDirCompleteness(entitiesDir, edgesDir); err != nil {
		return err
	}
	// Read edges from JSON files.
	if err := db.loadEdgesFromDirOnConn(db.conn, edgesDir, edgeDefs); err != nil {
		return err
	}
	// Promote the schema cache so any types inferred from the directory
	// structure (SPEC R8) are visible to subsequent reads and writes via
	// db.entityTypeDefs / db.edgeTypeDefs. The caches were loaded from a copy
	// above, so this merge also carries the pre-existing compiled defs back.
	db.entityTypeDefs = entDefs
	db.edgeTypeDefs = edgeDefs
	// Re-wire every cached structural handle (stale-structural-pointer rule,
	// LEARNINGS): db.edgePairs is consumed by ReplicateSchemaToBranch to
	// recreate branch rel tables with the same FROM/TO endpoints main's rel
	// tables carry. Types inferred from the directory structure (SPEC R8) have
	// no connection rules, so nothing but the catalog can rebuild this map —
	// without it the next BeginTransaction replicates a `_untyped`-placeholder
	// rel table to the branch (pairs := db.edgePairs[name] is nil) and every
	// branch edge is silently dropped against the mismatched endpoints.
	if err := db.rebuildEdgePairsLocked(); err != nil {
		return err
	}
	// A re-hydrated tree that carries type definitions (applied, or inferred
	// from the directory structure, SPEC R8) leaves main serving a complete
	// schema: the vector state the file-load path promoted above is persisted (a
	// reopen's validateMetadataAgainstCatalog must not fail closed with a
	// catalog/metadata vector mismatch) and the schemaApplied flag is set. The
	// flag set converges the both-lost recovery corner — corrupt main.lbug AND
	// absent schema.json while the git repo has commits — where Open recovers a
	// fresh database and restoreMainSchemaMetadataLocked finds no metadata to
	// restore, leaving schemaApplied false; without it the store serves the
	// recovered graph while Health() reports SchemaApplied=false indefinitely
	// (only ApplySchema and restoreMainSchemaMetadataLocked set the flag
	// elsewhere).
	//
	// An empty-tree re-hydration is the converse: after WipeGraph (SPEC R2),
	// WipeSchema dropped every table, removed schema.json, and cleared the
	// caches, so the startup rebuild of the wiped (empty) tree must neither
	// persist a schema that no longer exists nor flip the flag — doing either
	// reports the HealthCheck "schema applied" dimension (SPEC R2) true against
	// a store with no schema and no tables until the operator's next
	// ApplySchema.
	if len(db.entityTypeDefs) > 0 || len(db.edgeTypeDefs) > 0 {
		if err := db.persistMainVectorMetadataLocked(); err != nil {
			return err
		}
		db.schemaApplied = true
	}
	return nil
}

// HydrateBranchFromFiles loads entities/edges from JSON files into a branch.
func (rh *rehydrator) HydrateBranchFromFiles(ctx context.Context, txID, entitiesDir, edgesDir string) error {
	db := rh.db
	db.mu.Lock()
	br, err := db.branchLocked(txID)
	if err != nil {
		db.mu.Unlock()
		return err
	}
	br.mu.Lock()
	db.mu.Unlock()
	defer br.mu.Unlock()
	if br.failed {
		return store.ErrDatabaseNotReady
	}

	// Load from files into branch.
	if err := db.loadEntitiesFromDirOnConn(br.conn, entitiesDir, br.entityTypeDefs); err != nil {
		return err
	}
	// Fail if entities dir exists but edges dir does not (partial wipe) —
	// mirrors RehydrateMainFromFiles' completeness guard. On a working tree
	// where entities/ survived but edges/ was removed (SPEC R2 WipeGraph
	// mid-wipe failure → INTERNAL), silently loading entities and skipping
	// every edge would hydrate an incomplete graph with no signal.
	if err := checkEdgesDirCompleteness(entitiesDir, edgesDir); err != nil {
		return err
	}
	if err := db.loadEdgesFromDirOnConn(br.conn, edgesDir, br.edgeTypeDefs); err != nil {
		return err
	}
	// Persist the branch schema metadata AFTER hydration. ReplicateSchemaToBranch
	// writes branches/<txID>.schema.json before hydration runs, so any types and
	// FROM/TO pairs inferred from the directory structure (SPEC R8) here are absent
	// from that record. Without this rewrite, a crash + restart reopens the branch
	// via branchLocked → restoreBranchSchemaMetadata, whose
	// validateMetadataAgainstCatalog fails hard ("database entity type X is absent
	// from schema metadata") on the inferred types, and RecoverOpenTransactions
	// treats that non-ErrBranchNotFound error as a hard startup failure instead of
	// rolling back the one affected branch. The pairs come from the branch rel
	// tables' actual endpoints (mirroring RehydrateMainFromFiles'
	// rebuildEdgePairsLocked), so rule-less inferred edge types round-trip the
	// catalog comparison on reopen. The block runs only after every load succeeds:
	// a failed hydration leaves the persisted record untouched.
	if db.path != "" {
		pairs, perr := connectionEdgePairs(br.conn, br.edgeTypeDefs)
		if perr != nil {
			br.failed = true
			return fmt.Errorf("capture relationship endpoints: %w", perr)
		}
		metadata := metadataFromDefinitions(br.entityTypeDefs, br.edgeTypeDefs, pairs)
		metadata, err = captureVectorState(br.conn, metadata)
		if err != nil {
			br.failed = true
			return fmt.Errorf("capture branch vector state: %w", err)
		}
		if err := db.writeMetadata(db.branchMetadataPath(txID), metadata); err != nil {
			br.failed = true
			return fmt.Errorf("persist branch schema metadata: %w", err)
		}
	}
	return nil
}
