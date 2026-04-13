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
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
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
	Division        string // Optional specialisation division (e.g. "security"). Empty means unset.
	VersionHash     string
	CreatedAt       time.Time
	UpdatedAt       time.Time
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
	Division           string // Filter by division. Empty means all divisions (no filtering).
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

// sqliteTimeFormat is the format used to store and retrieve timestamps in
// SQLite. It matches the output of datetime('now') and strftime.
const sqliteTimeFormat = "2006-01-02 15:04:05"

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

	// Enable WAL mode for better concurrent read performance.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	// Enable foreign keys.
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
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
		division    TEXT NOT NULL DEFAULT '',
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
		division             TEXT NOT NULL DEFAULT '',
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
	CREATE INDEX IF NOT EXISTS idx_laws_division       ON laws(division);
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
// canonical content: goal, tier, division, sorted appliesTo, and sorted
// representations (by type then content).
func ComputeContentHash(
	goal string, tier int, appliesTo []string,
	representations []Representation, division string,
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

	h.Write([]byte(division))

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
	return t.UTC().Format(sqliteTimeFormat)
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(sqliteTimeFormat, s)
	if err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
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

// ---------------------------------------------------------------------------
// CRUD Operations
// ---------------------------------------------------------------------------

// CreateLaw inserts a new active law with its scope and initial version.
// Returns the generated version hash.
func (s *Store) CreateLaw(ctx context.Context, id string, law Law) (string, error) {
	return s.createLaw(ctx, id, law, true)
}

// CreateLawInactive inserts a new law with active=0 (hearing-created, pending
// activation). Returns the generated version hash.
func (s *Store) CreateLawInactive(ctx context.Context, id string, law Law) (string, error) {
	return s.createLaw(ctx, id, law, false)
}

func (s *Store) createLaw(ctx context.Context, id string, law Law, active bool) (string, error) {
	versionHash := ComputeContentHash(law.Goal, law.Tier, law.AppliesTo, law.Representations, law.Division)

	repsJSON, err := marshalRepresentations(law.Representations)
	if err != nil {
		return "", err
	}
	atJSON, err := marshalAppliesTo(law.AppliesTo)
	if err != nil {
		return "", err
	}

	activeInt := 0
	if active {
		activeInt = 1
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	now := formatTime(time.Now().UTC())

	// Insert into laws.
	_, err = tx.ExecContext(ctx,
		`INSERT INTO laws (id, goal, tier, division, active, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, law.Goal, law.Tier, law.Division, activeInt, now, now,
	)
	if err != nil {
		return "", fmt.Errorf("insert law: %w", err)
	}

	// Insert scope entries.
	if len(law.AppliesTo) > 0 {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO law_applies_to (law_id, artefact_kind) VALUES (?, ?)`)
		if err != nil {
			return "", fmt.Errorf("prepare applies_to insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()

		for _, kind := range law.AppliesTo {
			if _, err := stmt.ExecContext(ctx, id, kind); err != nil {
				return "", fmt.Errorf("insert applies_to %q: %w", kind, err)
			}
		}
	}

	// Insert initial version.
	_, err = tx.ExecContext(ctx,
		`INSERT INTO law_versions
		 (law_id, version_hash, goal, tier, division, representations_json, applies_to_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, versionHash, law.Goal, law.Tier, law.Division, repsJSON, atJSON, now,
	)
	if err != nil {
		return "", fmt.Errorf("insert law_version: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	return versionHash, nil
}

// GetLaw returns the full law from the head (latest) version.
// Returns an error if the law is missing or retired.
func (s *Store) GetLaw(ctx context.Context, id string) (Law, error) {
	// Read from the laws table first.
	var (
		goal      string
		tier      int
		activeInt int
		division  string
		createdAt string
		updatedAt string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT goal, tier, active, division, created_at, updated_at FROM laws WHERE id = ?`, id,
	).Scan(&goal, &tier, &activeInt, &division, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return Law{}, fmt.Errorf("law %q not found", id)
	}
	if err != nil {
		return Law{}, fmt.Errorf("get law: %w", err)
	}

	// Get head version (latest by created_at).
	var (
		versionHash string
		repsJSON    string
		atJSON      string
	)
	err = s.db.QueryRowContext(ctx,
		`SELECT version_hash, representations_json, applies_to_json
		 FROM law_versions WHERE law_id = ? ORDER BY rowid DESC LIMIT 1`, id,
	).Scan(&versionHash, &repsJSON, &atJSON)
	if err != nil {
		return Law{}, fmt.Errorf("get head version: %w", err)
	}

	reps, err := unmarshalRepresentations(repsJSON)
	if err != nil {
		return Law{}, err
	}
	appliesTo, err := unmarshalAppliesTo(atJSON)
	if err != nil {
		return Law{}, err
	}

	ct, err := parseTime(createdAt)
	if err != nil {
		return Law{}, fmt.Errorf("parse created_at: %w", err)
	}
	ut, err := parseTime(updatedAt)
	if err != nil {
		return Law{}, fmt.Errorf("parse updated_at: %w", err)
	}

	return Law{
		ID:              id,
		Goal:            goal,
		Tier:            tier,
		Active:          activeInt == 1,
		AppliesTo:       appliesTo,
		Representations: reps,
		Division:        division,
		VersionHash:     versionHash,
		CreatedAt:       ct,
		UpdatedAt:       ut,
	}, nil
}

// UpdateLaw appends a new version and updates the laws table. Returns
// the new version hash.
func (s *Store) UpdateLaw(ctx context.Context, id string, law Law) (string, error) {
	versionHash := ComputeContentHash(law.Goal, law.Tier, law.AppliesTo, law.Representations, law.Division)

	repsJSON, err := marshalRepresentations(law.Representations)
	if err != nil {
		return "", err
	}
	atJSON, err := marshalAppliesTo(law.AppliesTo)
	if err != nil {
		return "", err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	now := formatTime(time.Now().UTC())

	// Update the laws table.
	_, err = tx.ExecContext(ctx,
		`UPDATE laws SET goal = ?, tier = ?, division = ?, updated_at = ? WHERE id = ?`,
		law.Goal, law.Tier, law.Division, now, id,
	)
	if err != nil {
		return "", fmt.Errorf("update law: %w", err)
	}

	// Replace scope entries.
	_, err = tx.ExecContext(ctx, `DELETE FROM law_applies_to WHERE law_id = ?`, id)
	if err != nil {
		return "", fmt.Errorf("delete old applies_to: %w", err)
	}
	if len(law.AppliesTo) > 0 {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO law_applies_to (law_id, artefact_kind) VALUES (?, ?)`)
		if err != nil {
			return "", fmt.Errorf("prepare applies_to insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()

		for _, kind := range law.AppliesTo {
			if _, err := stmt.ExecContext(ctx, id, kind); err != nil {
				return "", fmt.Errorf("insert applies_to %q: %w", kind, err)
			}
		}
	}

	// Append new version. OR IGNORE handles idempotent re-inserts when
	// content cycles back to a previously-seen hash.
	_, err = tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO law_versions
		 (law_id, version_hash, goal, tier, division, representations_json, applies_to_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, versionHash, law.Goal, law.Tier, law.Division, repsJSON, atJSON, now,
	)
	if err != nil {
		return "", fmt.Errorf("insert law_version: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	return versionHash, nil
}

// RetireLaw deletes the law from the active registry and scope table.
// Versions remain in law_versions for audit trail.
func (s *Store) RetireLaw(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Delete scope entries first (foreign key).
	_, err = tx.ExecContext(ctx, `DELETE FROM law_applies_to WHERE law_id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete applies_to: %w", err)
	}

	// Delete the law.
	res, err := tx.ExecContext(ctx, `DELETE FROM laws WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete law: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("law %q not found", id)
	}

	return tx.Commit()
}

// ActivateLaw sets active=1 on a law. Used by ApplyLifecycleAction after
// hearing.
func (s *Store) ActivateLaw(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE laws SET active = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("activate law: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("law %q not found", id)
	}
	return nil
}

// SetTier updates the tier on a law and creates a new version.
func (s *Store) SetTier(ctx context.Context, id string, tier int) error {
	// Read current law state.
	law, err := s.GetLaw(ctx, id)
	if err != nil {
		return err
	}

	law.Tier = tier
	_, err = s.UpdateLaw(ctx, id, law)
	return err
}

// QueryLaws returns laws matching the given filter. Three base modes:
//  1. Empty filter: all active laws.
//  2. ArtefactKind set: scoped + global active laws.
//  3. ArtefactKind + RepresentationType: further filtered by MIME type in
//     representations, without stripping representations from the result.
//
// All modes support an optional Division filter: when set, only laws with
// that exact division value are returned.
func (s *Store) QueryLaws(ctx context.Context, filter QueryFilter) ([]Law, error) {
	var lawIDs []string

	if filter.GovernedArtefact == "" {
		// Mode 1: all active laws, optionally filtered by division.
		query := `SELECT id FROM laws WHERE active = 1`
		args := []any{}
		if filter.Division != "" {
			query += ` AND division = ?`
			args = append(args, filter.Division)
		}
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("query active laws: %w", err)
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, fmt.Errorf("scan law id: %w", err)
			}
			lawIDs = append(lawIDs, id)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	} else {
		// Mode 2/3: scoped + global active laws, optionally filtered by division.
		// A law is "global" if it has no entries in law_applies_to.
		query := `
			SELECT DISTINCT l.id FROM laws l
			LEFT JOIN law_applies_to la ON l.id = la.law_id
			WHERE l.active = 1
			AND (la.artefact_kind = ? OR la.law_id IS NULL)`
		args := []any{filter.GovernedArtefact}
		if filter.Division != "" {
			query += ` AND l.division = ?`
			args = append(args, filter.Division)
		}
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("query scoped laws: %w", err)
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, fmt.Errorf("scan law id: %w", err)
			}
			lawIDs = append(lawIDs, id)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	// Build full Law objects.
	var laws []Law
	for _, id := range lawIDs {
		law, err := s.GetLaw(ctx, id)
		if err != nil {
			return nil, err
		}
		laws = append(laws, law)
	}

	// Mode 3: further filter by representation type. Include laws that have
	// at least one representation matching the type. Do NOT strip
	// representations.
	if filter.RepresentationType != "" {
		var filtered []Law
		for _, law := range laws {
			for _, rep := range law.Representations {
				if rep.Type == filter.RepresentationType {
					filtered = append(filtered, law)
					break
				}
			}
		}
		laws = filtered
	}

	return laws, nil
}

// GetLawsByScope returns laws whose scope overlaps the given kinds, plus
// all global laws. Used for conflict detection.
func (s *Store) GetLawsByScope(ctx context.Context, appliesTo []string) ([]Law, error) {
	if len(appliesTo) == 0 {
		// If the incoming scope is empty (global), return all active laws.
		return s.QueryLaws(ctx, QueryFilter{})
	}

	// Build placeholders.
	placeholders := make([]string, len(appliesTo))
	args := make([]any, len(appliesTo))
	for i, kind := range appliesTo {
		placeholders[i] = "?"
		args[i] = kind
	}

	query := fmt.Sprintf(`
		SELECT DISTINCT l.id FROM laws l
		LEFT JOIN law_applies_to la ON l.id = la.law_id
		WHERE l.active = 1
		AND (la.artefact_kind IN (%s) OR la.law_id IS NULL)
	`, strings.Join(placeholders, ","))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query scoped laws: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var lawIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan law id: %w", err)
		}
		lawIDs = append(lawIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var laws []Law
	for _, id := range lawIDs {
		law, err := s.GetLaw(ctx, id)
		if err != nil {
			return nil, err
		}
		laws = append(laws, law)
	}
	return laws, rows.Err()
}

// ---------------------------------------------------------------------------
// Embedding Operations
// ---------------------------------------------------------------------------

// GetEmbedding reads the embedding BLOB for a specific version.
func (s *Store) GetEmbedding(ctx context.Context, lawID, versionHash string) ([]float32, error) {
	var blob []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT embedding FROM law_versions WHERE law_id = ? AND version_hash = ?`,
		lawID, versionHash,
	).Scan(&blob)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("version %s/%s not found", lawID, versionHash)
	}
	if err != nil {
		return nil, fmt.Errorf("get embedding: %w", err)
	}
	return decodeEmbedding(blob), nil
}

// SetEmbedding writes an embedding BLOB for a specific version.
func (s *Store) SetEmbedding(ctx context.Context, lawID, versionHash string, embedding []float32) error {
	blob := encodeEmbedding(embedding)
	res, err := s.db.ExecContext(ctx,
		`UPDATE law_versions SET embedding = ? WHERE law_id = ? AND version_hash = ?`,
		blob, lawID, versionHash,
	)
	if err != nil {
		return fmt.Errorf("set embedding: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("version %s/%s not found", lawID, versionHash)
	}
	return nil
}

// GetAllActiveEmbeddings returns all (lawID, versionHash, appliesTo, embedding)
// pairs for active laws that have embeddings. Used by conflict detection.
func (s *Store) GetAllActiveEmbeddings(ctx context.Context) ([]LawEmbedding, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT lv.law_id, lv.version_hash, lv.applies_to_json, lv.embedding
		FROM law_versions lv
		INNER JOIN laws l ON lv.law_id = l.id
		WHERE l.active = 1 AND lv.embedding IS NOT NULL
		AND lv.version_hash = (
			SELECT lv2.version_hash FROM law_versions lv2
			WHERE lv2.law_id = lv.law_id
			ORDER BY lv2.rowid DESC LIMIT 1
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("query active embeddings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []LawEmbedding
	for rows.Next() {
		var (
			le     LawEmbedding
			atJSON string
			blob   []byte
		)
		if err := rows.Scan(&le.LawID, &le.VersionHash, &atJSON, &blob); err != nil {
			return nil, fmt.Errorf("scan embedding: %w", err)
		}
		at, err := unmarshalAppliesTo(atJSON)
		if err != nil {
			return nil, err
		}
		le.AppliesTo = at
		le.Embedding = decodeEmbedding(blob)
		results = append(results, le)
	}
	return results, rows.Err()
}

// ---------------------------------------------------------------------------
// Vec Embedding Operations (sqlite-vec)
// ---------------------------------------------------------------------------

// UpsertVecEmbedding inserts or replaces the embedding for a law in the
// law_embeddings vec0 virtual table. The mapping between law_id (text) and
// the integer rowid required by vec0 is maintained in law_embedding_map.
func (s *Store) UpsertVecEmbedding(ctx context.Context, lawID string, embedding []float32) error {
	if len(embedding) != s.embeddingDims {
		return fmt.Errorf("embedding dimension mismatch: got %d, want %d", len(embedding), s.embeddingDims)
	}

	blob, err := sqlite_vec.SerializeFloat32(embedding)
	if err != nil {
		return fmt.Errorf("serialize embedding: %w", err)
	}

	// Check if a mapping already exists.
	var existingRowID int64
	err = s.db.QueryRowContext(ctx,
		`SELECT rowid_ref FROM law_embedding_map WHERE law_id = ?`, lawID,
	).Scan(&existingRowID)

	if err == nil {
		// Update existing: delete old vec row and insert new one with the same rowid.
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM law_embeddings WHERE rowid = ?`, existingRowID,
		); err != nil {
			return fmt.Errorf("delete old vec embedding: %w", err)
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO law_embeddings(rowid, embedding) VALUES (?, ?)`,
			existingRowID, blob,
		); err != nil {
			return fmt.Errorf("update vec embedding: %w", err)
		}
		return nil
	}

	if err != sql.ErrNoRows {
		return fmt.Errorf("check existing embedding map: %w", err)
	}

	// Insert new: let vec0 assign a rowid, then record the mapping.
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO law_embeddings(embedding) VALUES (?)`, blob,
	)
	if err != nil {
		return fmt.Errorf("insert vec embedding: %w", err)
	}
	newRowID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("get last insert id: %w", err)
	}

	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO law_embedding_map(law_id, rowid_ref) VALUES (?, ?)`,
		lawID, newRowID,
	); err != nil {
		return fmt.Errorf("insert embedding map: %w", err)
	}

	return nil
}

// DeleteVecEmbedding removes the embedding for a law from the vec0 virtual
// table and the mapping table. No error is returned if no embedding exists
// for the law.
func (s *Store) DeleteVecEmbedding(ctx context.Context, lawID string) error {
	var rowIDRef int64
	err := s.db.QueryRowContext(ctx,
		`SELECT rowid_ref FROM law_embedding_map WHERE law_id = ?`, lawID,
	).Scan(&rowIDRef)
	if err == sql.ErrNoRows {
		return nil // No embedding to delete — not an error.
	}
	if err != nil {
		return fmt.Errorf("lookup embedding map: %w", err)
	}

	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM law_embeddings WHERE rowid = ?`, rowIDRef,
	); err != nil {
		return fmt.Errorf("delete vec embedding: %w", err)
	}

	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM law_embedding_map WHERE law_id = ?`, lawID,
	); err != nil {
		return fmt.Errorf("delete embedding map: %w", err)
	}

	return nil
}

// HasVecEmbedding reports whether a vec embedding exists for the given law.
func (s *Store) HasVecEmbedding(ctx context.Context, lawID string) (bool, error) {
	var rowIDRef int64
	err := s.db.QueryRowContext(ctx,
		`SELECT rowid_ref FROM law_embedding_map WHERE law_id = ?`, lawID,
	).Scan(&rowIDRef)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check embedding map: %w", err)
	}
	return true, nil
}

// EmbeddingDimension returns the configured vector dimension.
func (s *Store) EmbeddingDimension() int {
	return s.embeddingDims
}

// VecSearchResult represents a single result from a vector similarity search.
type VecSearchResult struct {
	LawID    string
	Distance float64 // L2 distance from sqlite-vec (lower = more similar)
}

// SearchVecSimilar performs a k-nearest-neighbour search against the
// law_embeddings vec0 virtual table using the provided query embedding.
// Results are returned ordered by ascending distance (most similar first).
// The limit parameter controls the maximum number of results.
func (s *Store) SearchVecSimilar(ctx context.Context, queryEmbedding []float32, limit int) ([]VecSearchResult, error) {
	if len(queryEmbedding) != s.embeddingDims {
		return nil, fmt.Errorf("embedding dimension mismatch: got %d, want %d", len(queryEmbedding), s.embeddingDims)
	}
	if limit <= 0 {
		limit = 10
	}

	blob, err := sqlite_vec.SerializeFloat32(queryEmbedding)
	if err != nil {
		return nil, fmt.Errorf("serialize query embedding: %w", err)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT le.rowid, le.distance
		 FROM law_embeddings le
		 WHERE le.embedding MATCH ?
		 ORDER BY le.distance
		 LIMIT ?`,
		blob, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("vec search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Collect rowid+distance pairs first, then close the cursor before
	// issuing follow-up queries (SQLite in-memory requires this).
	type rowResult struct {
		rowID    int64
		distance float64
	}
	var rawResults []rowResult
	for rows.Next() {
		var r rowResult
		if err := rows.Scan(&r.rowID, &r.distance); err != nil {
			return nil, fmt.Errorf("scan vec result: %w", err)
		}
		rawResults = append(rawResults, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()

	// Resolve rowid -> law_id via the mapping table.
	var results []VecSearchResult
	for _, r := range rawResults {
		var lawID string
		err := s.db.QueryRowContext(ctx,
			`SELECT law_id FROM law_embedding_map WHERE rowid_ref = ?`, r.rowID,
		).Scan(&lawID)
		if err != nil {
			continue // Orphaned row — skip.
		}
		results = append(results, VecSearchResult{
			LawID:    lawID,
			Distance: r.distance,
		})
	}

	return results, nil
}

// ---------------------------------------------------------------------------
// Dispute Record Operations
// ---------------------------------------------------------------------------

// CreateDisputeRecord inserts a new active dispute record linking a petition
// to the laws it cites. Returns an error if a record with the same
// petition_id already exists.
func (s *Store) CreateDisputeRecord(
	ctx context.Context, petitionID string, citedLawIDs []string,
) (*DisputeRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	now := formatTime(time.Now().UTC())

	_, err = tx.ExecContext(ctx,
		`INSERT INTO dispute_records (petition_id, status, created_at) VALUES (?, 'active', ?)`,
		petitionID, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert dispute record: %w", err)
	}

	if len(citedLawIDs) > 0 {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO dispute_record_laws (petition_id, law_id) VALUES (?, ?)`)
		if err != nil {
			return nil, fmt.Errorf("prepare dispute_record_laws insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()

		for _, lawID := range citedLawIDs {
			if _, err := stmt.ExecContext(ctx, petitionID, lawID); err != nil {
				return nil, fmt.Errorf("insert dispute_record_law %q: %w", lawID, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	ct, err := parseTime(now)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}

	return &DisputeRecord{
		PetitionID:  petitionID,
		CitedLawIDs: citedLawIDs,
		CreatedAt:   ct,
		Status:      DisputeStatusActive,
	}, nil
}

// RetireDisputeRecord sets the status of an active dispute record to
// retired. Returns an error if no active record with the given petition_id
// exists.
func (s *Store) RetireDisputeRecord(ctx context.Context, petitionID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE dispute_records SET status = 'retired' WHERE petition_id = ? AND status = 'active'`,
		petitionID,
	)
	if err != nil {
		return fmt.Errorf("retire dispute record: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("dispute record %q not found or already retired", petitionID)
	}
	return nil
}

// GetActiveDisputes returns all active dispute records. If lawIDFilter is
// non-empty, only records citing that specific law are returned.
func (s *Store) GetActiveDisputes(ctx context.Context, lawIDFilter string) ([]*DisputeRecord, error) {
	var (
		rows *sql.Rows
		err  error
	)

	if lawIDFilter == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT petition_id, status, created_at FROM dispute_records WHERE status = 'active'`)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT DISTINCT dr.petition_id, dr.status, dr.created_at
			 FROM dispute_records dr
			 INNER JOIN dispute_record_laws drl ON dr.petition_id = drl.petition_id
			 WHERE dr.status = 'active' AND drl.law_id = ?`, lawIDFilter)
	}
	if err != nil {
		return nil, fmt.Errorf("query active disputes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Collect partial records first, then close the rows cursor before
	// issuing follow-up queries. SQLite's in-memory mode does not support
	// concurrent statement execution on a single connection.
	type partialRecord struct {
		petitionID string
		status     string
		createdAt  string
	}
	var partials []partialRecord
	for rows.Next() {
		var p partialRecord
		if err := rows.Scan(&p.petitionID, &p.status, &p.createdAt); err != nil {
			return nil, fmt.Errorf("scan dispute record: %w", err)
		}
		partials = append(partials, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Explicitly close before follow-up queries.
	_ = rows.Close()

	var records []*DisputeRecord
	for _, p := range partials {
		ct, err := parseTime(p.createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}

		// Load cited law IDs for this record.
		lawRows, err := s.db.QueryContext(ctx,
			`SELECT law_id FROM dispute_record_laws WHERE petition_id = ?`, p.petitionID)
		if err != nil {
			return nil, fmt.Errorf("query dispute record laws: %w", err)
		}

		var lawIDs []string
		for lawRows.Next() {
			var lawID string
			if err := lawRows.Scan(&lawID); err != nil {
				_ = lawRows.Close()
				return nil, fmt.Errorf("scan law_id: %w", err)
			}
			lawIDs = append(lawIDs, lawID)
		}
		_ = lawRows.Close()
		if err := lawRows.Err(); err != nil {
			return nil, err
		}

		records = append(records, &DisputeRecord{
			PetitionID:  p.petitionID,
			CitedLawIDs: lawIDs,
			CreatedAt:   ct,
			Status:      DisputeStatus(p.status),
		})
	}
	return records, nil
}
