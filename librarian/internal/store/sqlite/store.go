// Package sqlite implements the SQLite-backed storage layer for the Librarian
// service.
//
// It manages four tables:
//   - laws: the active law registry
//   - law_applies_to: scoping junction (artefact kinds a law governs)
//   - law_versions: immutable version log with content hash and embeddings
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
	ArtefactKind       string
	RepresentationType string
}

// sqliteTimeFormat is the format used to store and retrieve timestamps in
// SQLite. It matches the output of datetime('now') and strftime.
const sqliteTimeFormat = "2006-01-02 15:04:05"

// Store is the SQLite-backed repository for the Librarian.
type Store struct {
	db *sql.DB
}

// New opens (or creates) a SQLite database at the given path and initialises
// the schema. Use ":memory:" for an ephemeral in-memory store suitable for
// testing.
func New(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Enable WAL mode for better concurrent read performance.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	// Enable foreign keys.
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	s := &Store{db: db}
	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return s, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) initSchema() error {
	const schema = `
	CREATE TABLE IF NOT EXISTS laws (
		id          TEXT PRIMARY KEY,
		goal        TEXT NOT NULL,
		tier        INTEGER NOT NULL CHECK(tier BETWEEN 1 AND 5),
		active      INTEGER NOT NULL DEFAULT 1,
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
	`
	_, err := s.db.Exec(schema)
	return err
}

// ---------------------------------------------------------------------------
// Content Hash
// ---------------------------------------------------------------------------

// ComputeContentHash computes a deterministic SHA-256 hash of a law's
// canonical content: goal, tier, sorted appliesTo, and sorted
// representations (by type then content).
func ComputeContentHash(goal string, tier int, appliesTo []string, representations []Representation) string {
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
	versionHash := ComputeContentHash(law.Goal, law.Tier, law.AppliesTo, law.Representations)

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
		`INSERT INTO laws (id, goal, tier, active, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, law.Goal, law.Tier, activeInt, now, now,
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
		defer stmt.Close()

		for _, kind := range law.AppliesTo {
			if _, err := stmt.ExecContext(ctx, id, kind); err != nil {
				return "", fmt.Errorf("insert applies_to %q: %w", kind, err)
			}
		}
	}

	// Insert initial version.
	_, err = tx.ExecContext(ctx,
		`INSERT INTO law_versions (law_id, version_hash, goal, tier, representations_json, applies_to_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, versionHash, law.Goal, law.Tier, repsJSON, atJSON, now,
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
		createdAt string
		updatedAt string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT goal, tier, active, created_at, updated_at FROM laws WHERE id = ?`, id,
	).Scan(&goal, &tier, &activeInt, &createdAt, &updatedAt)
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
		VersionHash:     versionHash,
		CreatedAt:       ct,
		UpdatedAt:       ut,
	}, nil
}

// UpdateLaw appends a new version and updates the laws table. Returns
// the new version hash.
func (s *Store) UpdateLaw(ctx context.Context, id string, law Law) (string, error) {
	versionHash := ComputeContentHash(law.Goal, law.Tier, law.AppliesTo, law.Representations)

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
		`UPDATE laws SET goal = ?, tier = ?, updated_at = ? WHERE id = ?`,
		law.Goal, law.Tier, now, id,
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
		defer stmt.Close()

		for _, kind := range law.AppliesTo {
			if _, err := stmt.ExecContext(ctx, id, kind); err != nil {
				return "", fmt.Errorf("insert applies_to %q: %w", kind, err)
			}
		}
	}

	// Append new version. OR IGNORE handles idempotent re-inserts when
	// content cycles back to a previously-seen hash.
	_, err = tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO law_versions (law_id, version_hash, goal, tier, representations_json, applies_to_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, versionHash, law.Goal, law.Tier, repsJSON, atJSON, now,
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

// QueryLaws returns laws matching the given filter. Three modes:
//  1. Empty filter: all active laws.
//  2. ArtefactKind set: scoped + global active laws.
//  3. ArtefactKind + RepresentationType: further filtered by MIME type in
//     representations, without stripping representations from the result.
func (s *Store) QueryLaws(ctx context.Context, filter QueryFilter) ([]Law, error) {
	var lawIDs []string

	if filter.ArtefactKind == "" {
		// Mode 1: all active laws.
		rows, err := s.db.QueryContext(ctx, `SELECT id FROM laws WHERE active = 1`)
		if err != nil {
			return nil, fmt.Errorf("query active laws: %w", err)
		}
		defer rows.Close()

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
		// Mode 2/3: scoped + global active laws.
		// A law is "global" if it has no entries in law_applies_to.
		rows, err := s.db.QueryContext(ctx, `
			SELECT DISTINCT l.id FROM laws l
			LEFT JOIN law_applies_to la ON l.id = la.law_id
			WHERE l.active = 1
			AND (la.artefact_kind = ? OR la.law_id IS NULL)
		`, filter.ArtefactKind)
		if err != nil {
			return nil, fmt.Errorf("query scoped laws: %w", err)
		}
		defer rows.Close()

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
	defer rows.Close()

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
	defer rows.Close()

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
