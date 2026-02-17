// Package sqlite implements the SQLite-backed storage layer for the Flow
// Monitor service.
//
// It manages three tables:
//   - friction_events: individual friction event records
//   - friction_event_laws: many-to-many junction between events and law IDs
//   - telemetry_events: custom telemetry event records
//
// All writes are transactional. The store can be initialised with ":memory:"
// for testing or a file path for persistent operation.
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
	ID         string
	FlowID     string
	WorkitemID string
	NodeID     string
	Magnitude  int32
	Timestamp  time.Time
}

// TelemetryEvent represents a single telemetry event row.
type TelemetryEvent struct {
	ID         string
	FlowID     string
	NodeID     string
	WorkitemID string
	EventType  string
	Payload    []byte
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

// Store is the SQLite-backed repository for the Flow Monitor.
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

func (s *Store) initSchema() error {
	const schema = `
	CREATE TABLE IF NOT EXISTS friction_events (
		id          TEXT PRIMARY KEY,
		flow_id     TEXT NOT NULL,
		workitem_id TEXT NOT NULL,
		node_id     TEXT NOT NULL DEFAULT '',
		magnitude   INTEGER NOT NULL CHECK(magnitude >= 0),
		timestamp   DATETIME NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS friction_event_laws (
		event_id TEXT NOT NULL REFERENCES friction_events(id),
		law_id   TEXT NOT NULL,
		PRIMARY KEY (event_id, law_id)
	);

	CREATE TABLE IF NOT EXISTS telemetry_events (
		id          TEXT PRIMARY KEY,
		flow_id     TEXT NOT NULL,
		node_id     TEXT NOT NULL DEFAULT '',
		workitem_id TEXT NOT NULL DEFAULT '',
		event_type  TEXT NOT NULL,
		payload     BLOB CHECK(length(payload) <= 65536),
		timestamp   DATETIME NOT NULL DEFAULT (datetime('now'))
	);

	CREATE INDEX IF NOT EXISTS idx_friction_flow ON friction_events(flow_id);
	CREATE INDEX IF NOT EXISTS idx_friction_node ON friction_events(node_id);
	CREATE INDEX IF NOT EXISTS idx_friction_workitem ON friction_events(workitem_id);
	CREATE INDEX IF NOT EXISTS idx_friction_timestamp ON friction_events(timestamp);
	CREATE INDEX IF NOT EXISTS idx_friction_laws_law ON friction_event_laws(law_id);
	CREATE INDEX IF NOT EXISTS idx_telemetry_flow ON telemetry_events(flow_id);
	CREATE INDEX IF NOT EXISTS idx_telemetry_node ON telemetry_events(node_id);
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

// RecordTelemetry inserts a telemetry event.
func (s *Store) RecordTelemetry(ctx context.Context, event TelemetryEvent) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO telemetry_events (id, flow_id, node_id, workitem_id, event_type, payload, timestamp)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		event.ID, event.FlowID, event.NodeID, event.WorkitemID, event.EventType, event.Payload, formatTime(event.Timestamp),
	)
	if err != nil {
		return fmt.Errorf("insert telemetry_event: %w", err)
	}
	return nil
}

// QueryFriction returns aggregated friction data matching the given filter.
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
