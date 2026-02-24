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
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
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

// sqliteTimeFormat is the format used to store and retrieve timestamps in
// SQLite. It matches the output of datetime('now') and strftime.
const sqliteTimeFormat = "2006-01-02 15:04:05"

// ArtefactVersion records a single version in an artefact's history.
type ArtefactVersion struct {
	Hash             string
	GovernedArtefact string
	CreatedAt        time.Time
}

// ArtefactEntry is a summary of a single artefact for listing purposes.
type ArtefactEntry struct {
	ID               string
	GovernedArtefact string
}

// StampRecord represents a governance stamp applied to an artefact version.
type StampRecord struct {
	Name         string
	ApplyingNode string
	ContentHash  string // the artefact version_hash this stamp is on
	Signature    []byte
	CertChain    []byte
	CreatedAt    time.Time
}

// FeedbackRecord represents a feedback item on an artefact.
type FeedbackRecord struct {
	ID           string
	WorkitemID   string
	ArtefactID   string
	Source       string
	Severity     int32 // maps to flowv1.Severity enum
	State        int32 // maps to flowv1.FeedbackState enum
	Message      string
	VersionHash  string // artefact version this feedback was raised against
	LinkedRuling string // law ID of judiciary ruling, empty if none
	CreatedAt    time.Time
}

// FeedbackEventRecord represents a single event in a feedback item's history.
type FeedbackEventRecord struct {
	FeedbackID string
	Actor      string
	Action     string
	Message    string
	CreatedAt  time.Time
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
		_ = db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	// Enable foreign keys.
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
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
    severity       INTEGER NOT NULL DEFAULT 0,
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
	).Scan(&v.Hash, &v.GovernedArtefact, &createdStr)
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
	defer func() { _ = rows.Close() }()

	var history []ArtefactVersion
	for rows.Next() {
		var v ArtefactVersion
		var createdStr string
		if err := rows.Scan(&v.Hash, &v.GovernedArtefact, &createdStr); err != nil {
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
	defer func() { _ = rows.Close() }()

	var entries []ArtefactEntry
	for rows.Next() {
		var e ArtefactEntry
		if err := rows.Scan(&e.ID, &e.GovernedArtefact); err != nil {
			return nil, fmt.Errorf("scan artefact entry: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artefact entries: %w", err)
	}
	return entries, nil
}

// ---------------------------------------------------------------------------
// Stamp Methods
// ---------------------------------------------------------------------------

// StampArtefact applies a named stamp to an artefact version. It is a no-op
// (returns false) if the stamp already exists for that version.
func (s *Store) StampArtefact(
	ctx context.Context,
	workitemID, artefactID, versionHash, stampName, applyingNode string,
	signature, certChain []byte,
) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO stamps
		 (workitem_id, artefact_id, version_hash, stamp_name, applying_node, signature, cert_chain)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		workitemID, artefactID, versionHash, stampName, applyingNode, signature, certChain,
	)
	if err != nil {
		return false, fmt.Errorf("stamp artefact: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	return n > 0, nil
}

// GetStamps returns all stamps on a specific artefact version.
func (s *Store) GetStamps(ctx context.Context, workitemID, artefactID, versionHash string) ([]StampRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT stamp_name, applying_node, version_hash, signature, cert_chain, created_at
		 FROM stamps
		 WHERE workitem_id = ? AND artefact_id = ? AND version_hash = ?
		 ORDER BY rowid ASC`,
		workitemID, artefactID, versionHash,
	)
	if err != nil {
		return nil, fmt.Errorf("get stamps: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var stamps []StampRecord
	for rows.Next() {
		var sr StampRecord
		var createdStr string
		if err := rows.Scan(
			&sr.Name, &sr.ApplyingNode, &sr.ContentHash,
			&sr.Signature, &sr.CertChain, &createdStr,
		); err != nil {
			return nil, fmt.Errorf("scan stamp: %w", err)
		}
		sr.CreatedAt = parseTime(createdStr)
		stamps = append(stamps, sr)
	}
	return stamps, rows.Err()
}

// HasStamp checks whether a named stamp exists on the current (head) version
// of the specified artefact.
func (s *Store) HasStamp(ctx context.Context, workitemID, artefactID, stampName string) (bool, error) {
	// First get the head version hash.
	head, err := s.GetHead(ctx, workitemID, artefactID)
	if err != nil {
		return false, err
	}
	if head == nil {
		return false, nil
	}

	var count int
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM stamps
		 WHERE workitem_id = ? AND artefact_id = ? AND version_hash = ? AND stamp_name = ?`,
		workitemID, artefactID, head.Hash, stampName,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("has stamp: %w", err)
	}
	return count > 0, nil
}

// GetStampNamesForVersion returns the stamp names applied to a specific
// artefact version hash.
func (s *Store) GetStampNamesForVersion(
	ctx context.Context, workitemID, artefactID, versionHash string,
) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT stamp_name FROM stamps
		 WHERE workitem_id = ? AND artefact_id = ? AND version_hash = ?
		 ORDER BY rowid ASC`,
		workitemID, artefactID, versionHash,
	)
	if err != nil {
		return nil, fmt.Errorf("get stamp names: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan stamp name: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// ---------------------------------------------------------------------------
// Feedback Methods
// ---------------------------------------------------------------------------

// AddFeedback creates a new feedback item in NEW state and appends the
// initial "created" event. Returns the generated feedback ID.
func (s *Store) AddFeedback(
	ctx context.Context,
	workitemID, artefactID, source string,
	severity int32, message, versionHash string,
) (string, error) {
	feedbackID := uuid.New().String()
	const stateNew int32 = 1 // flowv1.FeedbackState_FEEDBACK_STATE_NEW

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO feedback_items (id, workitem_id, artefact_id, source, severity, state, message, version_hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		feedbackID, workitemID, artefactID, source, severity, stateNew, message, versionHash,
	)
	if err != nil {
		return "", fmt.Errorf("insert feedback: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO feedback_events (feedback_id, actor, action, message)
		 VALUES (?, ?, ?, ?)`,
		feedbackID, source, "created", message,
	)
	if err != nil {
		return "", fmt.Errorf("insert event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return feedbackID, nil
}

// GetFeedback returns all feedback items for a (workitemID, artefactID) pair.
func (s *Store) GetFeedback(ctx context.Context, workitemID, artefactID string) ([]FeedbackRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, workitem_id, artefact_id, source, severity, state, message, version_hash, linked_ruling, created_at
		 FROM feedback_items
		 WHERE workitem_id = ? AND artefact_id = ?
		 ORDER BY created_at ASC`,
		workitemID, artefactID,
	)
	if err != nil {
		return nil, fmt.Errorf("get feedback: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []FeedbackRecord
	for rows.Next() {
		var f FeedbackRecord
		var createdStr string
		if err := rows.Scan(
			&f.ID, &f.WorkitemID, &f.ArtefactID, &f.Source,
			&f.Severity, &f.State, &f.Message, &f.VersionHash,
			&f.LinkedRuling, &createdStr,
		); err != nil {
			return nil, fmt.Errorf("scan feedback: %w", err)
		}
		f.CreatedAt = parseTime(createdStr)
		items = append(items, f)
	}
	return items, rows.Err()
}

// GetFeedbackEvents returns the event history for a feedback item.
func (s *Store) GetFeedbackEvents(ctx context.Context, feedbackID string) ([]FeedbackEventRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT feedback_id, actor, action, message, created_at
		 FROM feedback_events
		 WHERE feedback_id = ?
		 ORDER BY rowid ASC`,
		feedbackID,
	)
	if err != nil {
		return nil, fmt.Errorf("get feedback events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var events []FeedbackEventRecord
	for rows.Next() {
		var e FeedbackEventRecord
		var createdStr string
		if err := rows.Scan(&e.FeedbackID, &e.Actor, &e.Action, &e.Message, &createdStr); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		e.CreatedAt = parseTime(createdStr)
		events = append(events, e)
	}
	return events, rows.Err()
}

// HasUnresolvedFeedback returns true if there are any feedback items for the
// given (workitemID, artefactID) that are not in RESOLVED state.
func (s *Store) HasUnresolvedFeedback(ctx context.Context, workitemID, artefactID string) (bool, error) {
	const stateResolved int32 = 6 // flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED

	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM feedback_items
		 WHERE workitem_id = ? AND artefact_id = ? AND state != ?`,
		workitemID, artefactID, stateResolved,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("has unresolved feedback: %w", err)
	}
	return count > 0, nil
}

// TransitionFeedback updates a feedback item's state and appends a history
// event. It returns the updated feedback record or an error if the current
// state does not match one of the expected from-states.
func (s *Store) TransitionFeedback(
	ctx context.Context, feedbackID string,
	fromStates []int32, toState int32,
	actor, action, message string,
) (*FeedbackRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Read current state.
	var f FeedbackRecord
	var createdStr string
	err = tx.QueryRowContext(ctx,
		`SELECT id, workitem_id, artefact_id, source, severity, state,
		        message, version_hash, linked_ruling, created_at
		 FROM feedback_items WHERE id = ?`, feedbackID,
	).Scan(
		&f.ID, &f.WorkitemID, &f.ArtefactID, &f.Source,
		&f.Severity, &f.State, &f.Message, &f.VersionHash,
		&f.LinkedRuling, &createdStr,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("feedback %q not found", feedbackID)
	}
	if err != nil {
		return nil, fmt.Errorf("read feedback: %w", err)
	}
	f.CreatedAt = parseTime(createdStr)

	// Validate current state is in allowed from-states.
	allowed := slices.Contains(fromStates, f.State)
	if !allowed {
		return nil, fmt.Errorf("feedback %q in state %d, cannot transition to %d", feedbackID, f.State, toState)
	}

	// Update state.
	_, err = tx.ExecContext(ctx,
		`UPDATE feedback_items SET state = ? WHERE id = ?`,
		toState, feedbackID,
	)
	if err != nil {
		return nil, fmt.Errorf("update state: %w", err)
	}

	// Append event.
	_, err = tx.ExecContext(ctx,
		`INSERT INTO feedback_events (feedback_id, actor, action, message)
		 VALUES (?, ?, ?, ?)`,
		feedbackID, actor, action, message,
	)
	if err != nil {
		return nil, fmt.Errorf("insert event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	f.State = toState
	return &f, nil
}

// GetFeedbackByID returns a single feedback item by its ID.
func (s *Store) GetFeedbackByID(ctx context.Context, feedbackID string) (*FeedbackRecord, error) {
	var f FeedbackRecord
	var createdStr string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, workitem_id, artefact_id, source, severity, state,
		        message, version_hash, linked_ruling, created_at
		 FROM feedback_items WHERE id = ?`, feedbackID,
	).Scan(
		&f.ID, &f.WorkitemID, &f.ArtefactID, &f.Source,
		&f.Severity, &f.State, &f.Message, &f.VersionHash,
		&f.LinkedRuling, &createdStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get feedback by id: %w", err)
	}
	f.CreatedAt = parseTime(createdStr)
	return &f, nil
}

// GetFeedbackDepth returns the number of events in a feedback item's history.
func (s *Store) GetFeedbackDepth(ctx context.Context, feedbackID string) (int32, error) {
	var count int32
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM feedback_events WHERE feedback_id = ?`, feedbackID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("get feedback depth: %w", err)
	}
	return count, nil
}

// LinkRuling atomically sets the linked_ruling field on a feedback item and
// transitions it to the target state. It validates that the feedback is in
// DEADLOCKED state (5), that no ruling is already linked (contempt guard),
// and that the target state is a valid terminal state (WONT_FIX=3 or
// REJECTED=4). A feedback event is appended for audit trail. Returns the
// updated record.
func (s *Store) LinkRuling(ctx context.Context, feedbackID, lawID string, targetState int32) (*FeedbackRecord, error) {
	const stateDeadlocked int32 = 5 // flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED
	const stateWontFix int32 = 3    // flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX
	const stateRejected int32 = 4   // flowv1.FeedbackState_FEEDBACK_STATE_REJECTED

	// Validate target state.
	if targetState != stateWontFix && targetState != stateRejected {
		return nil, fmt.Errorf("invalid target_state %d: must be WONT_FIX (%d) or REJECTED (%d)",
			targetState, stateWontFix, stateRejected)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Read current feedback.
	var f FeedbackRecord
	var createdStr string
	err = tx.QueryRowContext(ctx,
		`SELECT id, workitem_id, artefact_id, source, severity, state,
		        message, version_hash, linked_ruling, created_at
		 FROM feedback_items WHERE id = ?`, feedbackID,
	).Scan(
		&f.ID, &f.WorkitemID, &f.ArtefactID, &f.Source,
		&f.Severity, &f.State, &f.Message, &f.VersionHash,
		&f.LinkedRuling, &createdStr,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("feedback %q: %w", feedbackID, ErrFeedbackNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("read feedback: %w", err)
	}
	f.CreatedAt = parseTime(createdStr)

	// Validate state is DEADLOCKED.
	if f.State != stateDeadlocked {
		return nil, fmt.Errorf(
			"feedback %q in state %d, must be DEADLOCKED (%d): %w",
			feedbackID, f.State, stateDeadlocked, ErrFeedbackNotDeadlocked,
		)
	}

	// Contempt guard: block if linked_ruling already set.
	if f.LinkedRuling != "" {
		return nil, fmt.Errorf("feedback %q already has linked ruling %q: %w",
			feedbackID, f.LinkedRuling, ErrContemptGuard)
	}

	// Atomically set linked_ruling and transition state.
	_, err = tx.ExecContext(ctx,
		`UPDATE feedback_items SET linked_ruling = ?, state = ? WHERE id = ?`,
		lawID, targetState, feedbackID,
	)
	if err != nil {
		return nil, fmt.Errorf("update linked_ruling and state: %w", err)
	}

	// Append feedback event for audit trail.
	_, err = tx.ExecContext(ctx,
		`INSERT INTO feedback_events (feedback_id, actor, action, message)
		 VALUES (?, ?, ?, ?)`,
		feedbackID, "judiciary", "link_ruling",
		fmt.Sprintf("Linked ruling %s", lawID),
	)
	if err != nil {
		return nil, fmt.Errorf("insert link_ruling event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	f.LinkedRuling = lawID
	f.State = targetState
	return &f, nil
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
