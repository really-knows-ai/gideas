// Package sqlite implements the SQLite-backed Content-Addressable Storage
// (CAS) backend for the Archivist service.
//
// The architecture separates Content (raw bytes, deduplicated by SHA-256 hash)
// from Provenance (version history keyed by workitem + artefact). This split
// enables deduplication -- identical content stored by different artefacts
// references the same blob entry.
//
// All writes are transactional. The store can be initialised with ":memory:"
// for testing or a file path for persistent operation.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// sqliteTimeFormat is the format used to store and retrieve timestamps in
// SQLite. It matches the output of datetime('now') and strftime.
const sqliteTimeFormat = "2006-01-02 15:04:05"

// ArtefactVersion records a single version in an artefact's history.
type ArtefactVersion struct {
	Hash      string
	Kind      string
	CreatedAt time.Time
}

// ArtefactEntry is a summary of a single artefact for listing purposes.
type ArtefactEntry struct {
	ID   string
	Kind string
}

// Store is the SQLite-backed CAS repository for the Archivist.
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

// initSchema creates tables and indexes if they do not already exist.
func (s *Store) initSchema() error {
	const schema = `
CREATE TABLE IF NOT EXISTS blobs (
    content_hash TEXT PRIMARY KEY,
    content      BLOB NOT NULL,
    created_at   DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS artefact_versions (
    rowid        INTEGER PRIMARY KEY AUTOINCREMENT,
    workitem_id  TEXT NOT NULL,
    artefact_id  TEXT NOT NULL,
    content_hash TEXT NOT NULL REFERENCES blobs(content_hash),
    kind         TEXT NOT NULL,
    created_at   DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_versions_workitem_artefact
    ON artefact_versions(workitem_id, artefact_id);

CREATE INDEX IF NOT EXISTS idx_versions_content_hash
    ON artefact_versions(content_hash);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("exec schema: %w", err)
	}
	return nil
}

// StoreBlob writes raw bytes to the blobs table if not already present.
// Returns true if the blob was newly written, false if it already existed.
func (s *Store) StoreBlob(ctx context.Context, hash string, content []byte) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO blobs (content_hash, content) VALUES (?, ?)`,
		hash, content,
	)
	if err != nil {
		return false, fmt.Errorf("store blob: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	return n > 0, nil
}

// GetBlob retrieves raw bytes by content hash.
// Returns nil, false if the hash is not found.
func (s *Store) GetBlob(ctx context.Context, hash string) ([]byte, bool, error) {
	var content []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT content FROM blobs WHERE content_hash = ?`, hash,
	).Scan(&content)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get blob: %w", err)
	}
	return content, true, nil
}

// AppendVersion adds a new version entry to the provenance history for
// the given (workitemID, artefactID) pair. The caller must ensure the
// referenced blob already exists.
func (s *Store) AppendVersion(ctx context.Context, workitemID, artefactID, hash, kind string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO artefact_versions (workitem_id, artefact_id, content_hash, kind)
		 VALUES (?, ?, ?, ?)`,
		workitemID, artefactID, hash, kind,
	)
	if err != nil {
		return fmt.Errorf("append version: %w", err)
	}
	return nil
}

// GetHead returns the most recent version for (workitemID, artefactID).
// Returns nil, nil if no versions exist.
func (s *Store) GetHead(ctx context.Context, workitemID, artefactID string) (*ArtefactVersion, error) {
	var v ArtefactVersion
	var createdStr string
	err := s.db.QueryRowContext(ctx,
		`SELECT content_hash, kind, created_at
		 FROM artefact_versions
		 WHERE workitem_id = ? AND artefact_id = ?
		 ORDER BY rowid DESC LIMIT 1`,
		workitemID, artefactID,
	).Scan(&v.Hash, &v.Kind, &createdStr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get head: %w", err)
	}
	v.CreatedAt = parseTime(createdStr)
	return &v, nil
}

// GetHistory returns the full version history for (workitemID, artefactID),
// ordered oldest-first. Returns nil, nil if no versions exist.
func (s *Store) GetHistory(ctx context.Context, workitemID, artefactID string) ([]ArtefactVersion, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT content_hash, kind, created_at
		 FROM artefact_versions
		 WHERE workitem_id = ? AND artefact_id = ?
		 ORDER BY rowid ASC`,
		workitemID, artefactID,
	)
	if err != nil {
		return nil, fmt.Errorf("get history: %w", err)
	}
	defer rows.Close()

	var history []ArtefactVersion
	for rows.Next() {
		var v ArtefactVersion
		var createdStr string
		if err := rows.Scan(&v.Hash, &v.Kind, &createdStr); err != nil {
			return nil, fmt.Errorf("scan version: %w", err)
		}
		v.CreatedAt = parseTime(createdStr)
		history = append(history, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate versions: %w", err)
	}
	if len(history) == 0 {
		return nil, nil
	}
	return history, nil
}

// ListArtefacts returns all artefact IDs and their head kinds for a workitem.
// Returns nil, nil if the workitem has no artefacts.
func (s *Store) ListArtefacts(ctx context.Context, workitemID string) ([]ArtefactEntry, error) {
	// Use a subquery to find the max rowid per (workitem_id, artefact_id),
	// then join back to get the kind from that head row.
	rows, err := s.db.QueryContext(ctx,
		`SELECT v.artefact_id, v.kind
		 FROM artefact_versions v
		 INNER JOIN (
		     SELECT MAX(rowid) AS max_rowid
		     FROM artefact_versions
		     WHERE workitem_id = ?
		     GROUP BY artefact_id
		 ) head ON v.rowid = head.max_rowid`,
		workitemID,
	)
	if err != nil {
		return nil, fmt.Errorf("list artefacts: %w", err)
	}
	defer rows.Close()

	var entries []ArtefactEntry
	for rows.Next() {
		var e ArtefactEntry
		if err := rows.Scan(&e.ID, &e.Kind); err != nil {
			return nil, fmt.Errorf("scan artefact entry: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artefact entries: %w", err)
	}
	return entries, nil
}

// parseTime parses a SQLite datetime string. Falls back to RFC3339 if the
// default format does not match.
func parseTime(s string) time.Time {
	t, err := time.Parse(sqliteTimeFormat, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t
}
