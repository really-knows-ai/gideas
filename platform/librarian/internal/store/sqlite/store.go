// Package sqlite implements the SQLite-backed storage layer for the Librarian
// service.
//
// It manages four tables plus a vec0 virtual table:
//   - laws: the active law registry
//   - law_applies_to: scoping junction (artefact kinds a law governs)
//   - law_versions: immutable version log with content hash and embeddings
//   - dispute_records + dispute_record_laws: cross-flow petition dispute tracking
//   - law_embeddings: sqlite-vec virtual table for vector similarity search
//
// All writes are transactional. The store can be initialised with ":memory:"
// for testing or a file path for persistent operation.
package sqlite

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/foundry/flow/pkg/sqldbutil"
	_ "github.com/mattn/go-sqlite3"
)

// Law represents a law in the Librarian's store.
type Law struct {
	ID              string
	Goal            string
	Tier            int
	Active          bool
	AppliesTo       []string
	Representations []Representation
	Group           string // Law group name (e.g. "security"). Empty means unset.
	VersionHash     string
	CreatedAt       time.Time
	UpdatedAt       time.Time

	// Provenance fields for replicated (cross-flow) laws.
	SourceFlow string // Originating Flow namespace. Empty for locally-created laws.
	PetitionID string // Petition ID when law originated from a cross-flow petition.
}

// Representation is a typed representation of a law's goal.
type Representation struct {
	Type    string // MIME type
	Content string
}

// LawEmbedding associates a law version with its embedding vector and scope.
type LawEmbedding struct {
	LawID       string
	VersionHash string
	AppliesTo   []string
	Embedding   []float32
}

// QueryFilter specifies optional axes for filtering law queries.
type QueryFilter struct {
	GovernedArtefact   string
	RepresentationType string
	Group              string // Filter by group. Empty means no group filter (return all).
}

// LawGroup represents a named group of laws with an evaluation contract.
type LawGroup struct {
	Name     string
	Mode     string // "bundle" or "law-by-law"
	Passes   int
	SyncedAt time.Time
}

// DisputeStatus represents the lifecycle state of a dispute record.
type DisputeStatus string

const (
	// DisputeStatusActive indicates an unresolved dispute.
	DisputeStatusActive DisputeStatus = "active"
	// DisputeStatusRetired indicates a resolved dispute.
	DisputeStatusRetired DisputeStatus = "retired"
)

// DisputeRecord links a cross-flow petition to the laws it cites.
// It is a Library entity distinct from laws.
type DisputeRecord struct {
	PetitionID  string
	CitedLawIDs []string
	CreatedAt   time.Time
	Status      DisputeStatus
}

// DefaultEmbeddingDimension is the default vector dimension for the
// law_embeddings vec0 virtual table. This matches the output dimension of
// the default Ollama embedding model (qwen3-embedding:4b).
const DefaultEmbeddingDimension = 2048

// Store is the SQLite-backed repository for the Librarian.
type Store struct {
	db            *sql.DB
	embeddingDims int // vector dimension for the law_embeddings vec0 table
}

func init() {
	sqlite_vec.Auto()
}

// New opens (or creates) a SQLite database at the given path and initialises
// the schema. Use ":memory:" for an ephemeral in-memory store suitable for
// testing.
func New(dsn string, opts ...StoreOption) (*Store, error) {
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// In-memory databases are per-connection in SQLite: every new connection
	// opened by the pool gets its own blank database.  Limit the pool to a
	// single connection so all queries hit the same in-memory instance.
	if dsn == ":memory:" {
		db.SetMaxOpenConns(1)
	}

	// Enable standard SQLite pragmas.
	if err := sqldbutil.SetPragmas(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	s := &Store{db: db, embeddingDims: DefaultEmbeddingDimension}
	for _, o := range opts {
		o(s)
	}
	if err := s.initSchema(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return s, nil
}

// StoreOption configures a Store.
type StoreOption func(*Store)

// WithEmbeddingDimension sets the vector dimension for the law_embeddings
// vec0 virtual table. Must be called before the store is opened.
func WithEmbeddingDimension(dims int) StoreOption {
	return func(s *Store) { s.embeddingDims = dims }
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) initSchema() error {
	const lawSchema = `
	CREATE TABLE IF NOT EXISTS laws (
		id          TEXT PRIMARY KEY,
		goal        TEXT NOT NULL,
		tier        INTEGER NOT NULL CHECK(tier BETWEEN 1 AND 5),
		active      INTEGER NOT NULL DEFAULT 1,
		law_group   TEXT NOT NULL DEFAULT '',
		source_flow TEXT NOT NULL DEFAULT '',
		petition_id TEXT NOT NULL DEFAULT '',
		created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
		updated_at  DATETIME NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS law_applies_to (
		law_id        TEXT NOT NULL REFERENCES laws(id),
		artefact_kind TEXT NOT NULL,
		PRIMARY KEY (law_id, artefact_kind)
	);

	CREATE TABLE IF NOT EXISTS law_versions (
		law_id               TEXT NOT NULL,
		version_hash         TEXT NOT NULL,
		goal                 TEXT NOT NULL,
		tier                 INTEGER NOT NULL,
		law_group            TEXT NOT NULL DEFAULT '',
		representations_json TEXT NOT NULL,
		applies_to_json      TEXT NOT NULL,
		embedding            BLOB,
		created_at           DATETIME NOT NULL DEFAULT (datetime('now')),
		PRIMARY KEY (law_id, version_hash)
	);

	CREATE INDEX IF NOT EXISTS idx_law_applies_to_kind ON law_applies_to(artefact_kind);
	CREATE INDEX IF NOT EXISTS idx_law_versions_law    ON law_versions(law_id);
	CREATE INDEX IF NOT EXISTS idx_laws_active         ON laws(active);
	CREATE INDEX IF NOT EXISTS idx_laws_tier           ON laws(tier);
	CREATE INDEX IF NOT EXISTS idx_laws_group         ON laws(law_group);
	`
	if _, err := s.db.Exec(lawSchema); err != nil {
		return err
	}

	// Dispute record tables are created individually because go-sqlite3
	// silently stops executing after the first statement in a multi-
	// statement Exec call when the DSN lacks ?_multi=true.
	ddl := []string{
		`CREATE TABLE IF NOT EXISTS dispute_records (
			petition_id TEXT PRIMARY KEY,
			status      TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active', 'retired')),
			created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS dispute_record_laws (
			petition_id TEXT NOT NULL REFERENCES dispute_records(petition_id),
			law_id      TEXT NOT NULL,
			PRIMARY KEY (petition_id, law_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_dispute_records_status ON dispute_records(status)`,
		`CREATE INDEX IF NOT EXISTS idx_dispute_record_laws_law ON dispute_record_laws(law_id)`,
	}
	for _, stmt := range ddl {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}

	// Law groups table.
	lgDDL := []string{
		`CREATE TABLE IF NOT EXISTS law_groups (
			name      TEXT PRIMARY KEY,
			mode      TEXT NOT NULL DEFAULT 'bundle' CHECK(mode IN ('bundle', 'law-by-law')),
			passes    INTEGER NOT NULL DEFAULT 1 CHECK(passes >= 1),
			synced_at DATETIME NOT NULL DEFAULT (datetime('now'))
		)`,
	}
	for _, stmt := range lgDDL {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}

	// ALTER TABLE migration: add law_group column to law_versions (if not present)
	// and drop the old division column from both tables.
	// go-sqlite3 does not support IF NOT EXISTS for these operations, so we attempt
	// the migration and ignore the error if the column already exists or is missing.
	// ponytail: naive try-and-ignore; a migration tracking table would be needed
	// for more complex migrations.
	_, _ = s.db.Exec(`ALTER TABLE law_versions ADD COLUMN law_group TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE laws DROP COLUMN division`)
	_, _ = s.db.Exec(`ALTER TABLE law_versions DROP COLUMN division`)

	// Create the sqlite-vec virtual table for vector similarity search.
	// The law_embeddings table stores one embedding per active law, keyed
	// by a numeric rowid that maps 1:1 with the law_id via the
	// law_embedding_map table.
	vecDDL := []string{
		fmt.Sprintf(
			`CREATE VIRTUAL TABLE IF NOT EXISTS law_embeddings USING vec0(embedding float[%d])`,
			s.embeddingDims,
		),
		`CREATE TABLE IF NOT EXISTS law_embedding_map (
			law_id TEXT PRIMARY KEY,
			rowid_ref INTEGER NOT NULL UNIQUE
		)`,
	}
	for _, stmt := range vecDDL {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("init vec schema: %w", err)
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Content Hash
// ---------------------------------------------------------------------------

// ComputeContentHash computes a deterministic SHA-256 hash of a law's
// canonical content: goal, tier, law_group, sorted appliesTo, and sorted
// representations (by type then content).
func ComputeContentHash(
	goal string, tier int, appliesTo []string,
	representations []Representation, group string,
) string {
	// Sort appliesTo.
	sortedAppliesTo := make([]string, len(appliesTo))
	copy(sortedAppliesTo, appliesTo)
	sort.Strings(sortedAppliesTo)

	// Sort representations by type, then by content.
	sortedReps := make([]Representation, len(representations))
	copy(sortedReps, representations)
	sort.Slice(sortedReps, func(i, j int) bool {
		if sortedReps[i].Type != sortedReps[j].Type {
			return sortedReps[i].Type < sortedReps[j].Type
		}
		return sortedReps[i].Content < sortedReps[j].Content
	})

	h := sha256.New()
	h.Write([]byte(goal))

	tierBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(tierBytes, uint32(tier))
	h.Write(tierBytes)

	h.Write([]byte(group))

	for _, at := range sortedAppliesTo {
		h.Write([]byte(at))
	}
	for _, r := range sortedReps {
		h.Write([]byte(r.Type))
		h.Write([]byte(r.Content))
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}

// ---------------------------------------------------------------------------
// Time helpers
// ---------------------------------------------------------------------------

func formatTime(t time.Time) string {
	return sqldbutil.FormatTime(t)
}

func parseTime(s string) (time.Time, error) {
	return sqldbutil.ParseTime(s)
}

// ---------------------------------------------------------------------------
// JSON helpers for law_versions
// ---------------------------------------------------------------------------

func marshalRepresentations(reps []Representation) (string, error) {
	data, err := json.Marshal(reps)
	if err != nil {
		return "", fmt.Errorf("marshal representations: %w", err)
	}
	return string(data), nil
}

func unmarshalRepresentations(s string) ([]Representation, error) {
	var reps []Representation
	if err := json.Unmarshal([]byte(s), &reps); err != nil {
		return nil, fmt.Errorf("unmarshal representations: %w", err)
	}
	return reps, nil
}

func marshalAppliesTo(appliesTo []string) (string, error) {
	data, err := json.Marshal(appliesTo)
	if err != nil {
		return "", fmt.Errorf("marshal applies_to: %w", err)
	}
	return string(data), nil
}

func unmarshalAppliesTo(s string) ([]string, error) {
	var at []string
	if err := json.Unmarshal([]byte(s), &at); err != nil {
		return nil, fmt.Errorf("unmarshal applies_to: %w", err)
	}
	return at, nil
}

// ---------------------------------------------------------------------------
// Embedding serialization
// ---------------------------------------------------------------------------

func encodeEmbedding(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

func decodeEmbedding(b []byte) []float32 {
	if len(b) == 0 {
		return nil
	}
	n := len(b) / 4
	v := make([]float32, n)
	for i := range n {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
