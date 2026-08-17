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
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/foundry/flow/pkg/sqldbutil"
	_ "github.com/mattn/go-sqlite3"
)

// Sentinel errors for LinkRuling validation failures. These allow callers
// to distinguish error conditions using errors.Is and map to appropriate
// gRPC status codes.
var (
	// ErrFeedbackNotFound is returned when the requested feedback item
	// does not exist.
	ErrFeedbackNotFound = errors.New("feedback not found")
	// ErrFeedbackNotDeadlocked is returned when the feedback item is not
	// in the DEADLOCKED state required for linking a ruling.
	ErrFeedbackNotDeadlocked = errors.New("feedback not in DEADLOCKED state")
	// ErrContemptGuard is returned when the feedback item already has a
	// linked ruling, preventing a second ruling from being attached.
	ErrContemptGuard = errors.New("ruling already linked (contempt guard)")
)

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

	// Enable standard SQLite pragmas.
	if err := sqldbutil.SetPragmas(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	s := &Store{db: db}
	if err := s.initSchema(); err != nil {
		_ = db.Close()
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

CREATE TABLE IF NOT EXISTS stamps (
    rowid          INTEGER PRIMARY KEY AUTOINCREMENT,
    workitem_id    TEXT NOT NULL,
    artefact_id    TEXT NOT NULL,
    version_hash   TEXT NOT NULL,
    stamp_name     TEXT NOT NULL,
    applying_node  TEXT NOT NULL,
    signature      BLOB,
    cert_chain     BLOB,
    created_at     DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(workitem_id, artefact_id, version_hash, stamp_name)
);

CREATE INDEX IF NOT EXISTS idx_stamps_artefact
    ON stamps(workitem_id, artefact_id, version_hash);

CREATE TABLE IF NOT EXISTS feedback_items (
    id             TEXT PRIMARY KEY,
    workitem_id    TEXT NOT NULL,
    artefact_id    TEXT NOT NULL,
    source         TEXT NOT NULL DEFAULT '',
    can_wont_fix   INTEGER NOT NULL DEFAULT 0,
    state          INTEGER NOT NULL DEFAULT 1,
    message        TEXT NOT NULL DEFAULT '',
    version_hash   TEXT NOT NULL DEFAULT '',
    linked_ruling  TEXT NOT NULL DEFAULT '',
    created_at     DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_feedback_workitem_artefact
    ON feedback_items(workitem_id, artefact_id);

CREATE TABLE IF NOT EXISTS feedback_events (
    rowid       INTEGER PRIMARY KEY AUTOINCREMENT,
    feedback_id TEXT NOT NULL REFERENCES feedback_items(id),
    actor       TEXT NOT NULL DEFAULT '',
    action      TEXT NOT NULL,
    message     TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_feedback_events_feedback
    ON feedback_events(feedback_id);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("exec schema: %w", err)
	}
	return nil
}

// parseTime parses a SQLite datetime string. Falls back to RFC3339 if the
// default format does not match.
func parseTime(s string) time.Time {
	t, _ := sqldbutil.ParseTime(s)
	return t
}
