// Package sqlite implements the SQLite-backed storage layer for the Friction
// Ledger service.
//
// It manages three tables:
//   - friction_events: individual friction event records
//   - friction_event_laws: many-to-many junction between events and law IDs
//   - subscriber_checkpoint: persists Event Bus replay positions per channel
//
// All writes are transactional. The store can be initialised with ":memory:"
// for testing or a file path for persistent operation.
//
// The magnitude column uses REAL (double) to align with the spec's float64
// friction magnitude definition.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// FrictionEvent represents a single friction event row.
type FrictionEvent struct {
	ID string
	// FlowID holds the Kubernetes namespace that owns the flow
	// (one namespace = one FoundryFlow). The field name and SQLite column
	// name are kept as "flow_id" for storage compatibility.
	FlowID     string
	WorkitemID string
	NodeID     string
	Magnitude  float64
	Timestamp  time.Time
}

// FrictionAggregate represents a summed friction result across a grouping axis.
type FrictionAggregate struct {
	LawID          string
	NodeID         string
	WorkitemID     string
	Tier           int32
	TotalMagnitude float64
	EventCount     int32
	Earliest       time.Time
	Latest         time.Time
}

// FrictionFilter specifies optional axes for filtering friction queries.
type FrictionFilter struct {
	LawID      string
	NodeID     string
	WorkitemID string
	Tier       int32
	StartTime  *time.Time
	EndTime    *time.Time
}

// Store is the SQLite-backed repository for the Friction Ledger.
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

	// For in-memory databases, restrict to a single connection to prevent
	// each connection from getting a separate empty database.
	if dsn == ":memory:" {
		db.SetMaxOpenConns(1)
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

func (s *Store) initSchema() error {
	const schema = `
	CREATE TABLE IF NOT EXISTS friction_events (
		id          TEXT PRIMARY KEY,
		flow_id     TEXT NOT NULL,
		workitem_id TEXT NOT NULL,
		node_id     TEXT NOT NULL DEFAULT '',
		magnitude   REAL NOT NULL CHECK(magnitude >= 0),
		timestamp   DATETIME NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS friction_event_laws (
		event_id TEXT NOT NULL REFERENCES friction_events(id),
		law_id   TEXT NOT NULL,
		PRIMARY KEY (event_id, law_id)
	);

	CREATE TABLE IF NOT EXISTS subscriber_checkpoint (
		channel       TEXT PRIMARY KEY,
		last_sequence INTEGER NOT NULL DEFAULT 0
	);

	CREATE INDEX IF NOT EXISTS idx_friction_flow ON friction_events(flow_id);
	CREATE INDEX IF NOT EXISTS idx_friction_node ON friction_events(node_id);
	CREATE INDEX IF NOT EXISTS idx_friction_workitem ON friction_events(workitem_id);
	CREATE INDEX IF NOT EXISTS idx_friction_timestamp ON friction_events(timestamp);
	CREATE INDEX IF NOT EXISTS idx_friction_laws_law ON friction_event_laws(law_id);
	`
	_, err := s.db.Exec(schema)
	return err
}

// AddFriction atomically inserts a friction event and its associated law IDs.
func (s *Store) AddFriction(ctx context.Context, id string, event FrictionEvent, lawIDs []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.ExecContext(ctx,
		`INSERT INTO friction_events (id, flow_id, workitem_id, node_id, magnitude, timestamp)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, event.FlowID, event.WorkitemID, event.NodeID, event.Magnitude, formatTime(event.Timestamp),
	)
	if err != nil {
		return fmt.Errorf("insert friction_event: %w", err)
	}

	if len(lawIDs) > 0 {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO friction_event_laws (event_id, law_id) VALUES (?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare law insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()

		for _, lawID := range lawIDs {
			if _, err := stmt.ExecContext(ctx, id, lawID); err != nil {
				return fmt.Errorf("insert law %q: %w", lawID, err)
			}
		}
	}

	return tx.Commit()
}

// sqliteTimeFormat is the format used to store and retrieve timestamps in
// SQLite. It matches the output of datetime('now') and strftime.
const sqliteTimeFormat = "2006-01-02 15:04:05"

// formatTime serialises a time.Time into the SQLite text format.
func formatTime(t time.Time) string {
	return t.UTC().Format(sqliteTimeFormat)
}

// parseTime deserialises a SQLite text timestamp into a time.Time.
func parseTime(s string) (time.Time, error) {
	return time.Parse(sqliteTimeFormat, s)
}

// QueryFriction returns aggregated friction data matching the given filter.
// Results are grouped by (law_id, node_id, workitem_id) and include
// SUM(magnitude), COUNT(*), MIN(timestamp), and MAX(timestamp).
func (s *Store) QueryFriction(ctx context.Context, filter FrictionFilter) ([]FrictionAggregate, error) {
	// Build dynamic query. Always join with friction_event_laws to support
	// per-law aggregation; events with no laws are included via LEFT JOIN.
	var (
		clauses []string
		args    []any
	)

	if filter.LawID != "" {
		clauses = append(clauses, "fel.law_id = ?")
		args = append(args, filter.LawID)
	}
	if filter.NodeID != "" {
		clauses = append(clauses, "fe.node_id = ?")
		args = append(args, filter.NodeID)
	}
	if filter.WorkitemID != "" {
		clauses = append(clauses, "fe.workitem_id = ?")
		args = append(args, filter.WorkitemID)
	}
	if filter.StartTime != nil {
		clauses = append(clauses, "fe.timestamp >= ?")
		args = append(args, formatTime(*filter.StartTime))
	}
	if filter.EndTime != nil {
		clauses = append(clauses, "fe.timestamp <= ?")
		args = append(args, formatTime(*filter.EndTime))
	}

	query := `
		SELECT
			COALESCE(fel.law_id, '')  AS law_id,
			fe.node_id,
			fe.workitem_id,
			SUM(fe.magnitude)         AS total_magnitude,
			COUNT(*)                  AS event_count,
			MIN(fe.timestamp)         AS earliest,
			MAX(fe.timestamp)         AS latest
		FROM friction_events fe
		LEFT JOIN friction_event_laws fel ON fe.id = fel.event_id
	`

	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}

	query += ` GROUP BY COALESCE(fel.law_id, ''), fe.node_id, fe.workitem_id`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query friction: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []FrictionAggregate
	for rows.Next() {
		var (
			agg         FrictionAggregate
			earliestStr string
			latestStr   string
		)
		if err := rows.Scan(
			&agg.LawID,
			&agg.NodeID,
			&agg.WorkitemID,
			&agg.TotalMagnitude,
			&agg.EventCount,
			&earliestStr,
			&latestStr,
		); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		agg.Earliest, err = parseTime(earliestStr)
		if err != nil {
			return nil, fmt.Errorf("parse earliest: %w", err)
		}
		agg.Latest, err = parseTime(latestStr)
		if err != nil {
			return nil, fmt.Errorf("parse latest: %w", err)
		}
		results = append(results, agg)
	}
	return results, rows.Err()
}

// GetCheckpoint returns the last-processed Event Bus sequence for the given
// channel name. Returns 0 if no checkpoint exists.
func (s *Store) GetCheckpoint(ctx context.Context, channel string) (uint64, error) {
	var seq uint64
	err := s.db.QueryRowContext(ctx,
		`SELECT last_sequence FROM subscriber_checkpoint WHERE channel = ?`, channel,
	).Scan(&seq)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get checkpoint: %w", err)
	}
	return seq, nil
}

// SetCheckpoint upserts the last-processed Event Bus sequence for the given
// channel name.
func (s *Store) SetCheckpoint(ctx context.Context, channel string, sequence uint64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO subscriber_checkpoint (channel, last_sequence) VALUES (?, ?)
		 ON CONFLICT(channel) DO UPDATE SET last_sequence = excluded.last_sequence`,
		channel, sequence,
	)
	if err != nil {
		return fmt.Errorf("set checkpoint: %w", err)
	}
	return nil
}

// QueryFrictionByLaw returns the total accumulated friction for a specific
// law. This is used by the threshold evaluator to determine if a law's
// friction has crossed a tier threshold.
func (s *Store) QueryFrictionByLaw(ctx context.Context, lawID string) (float64, error) {
	var total sql.NullFloat64
	err := s.db.QueryRowContext(ctx,
		`SELECT SUM(fe.magnitude)
		 FROM friction_events fe
		 JOIN friction_event_laws fel ON fe.id = fel.event_id
		 WHERE fel.law_id = ?`, lawID,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("query friction by law: %w", err)
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Float64, nil
}
