// Package sqlite implements durable event storage for the Flow Event Bus
// using SQLite with WAL mode. Events are persisted with per-channel
// monotonic sequence numbers and support replay, filtering, and
// retention-based eviction.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3" // SQLite driver.
)

const timeFormat = "2006-01-02 15:04:05"

// Label is a key-value pair stored alongside an event for server-side
// filtering. Multiple labels with the same key are permitted.
type Label struct {
	Key   string
	Value string
}

// Event represents a stored event in the Event Bus.
type Event struct {
	ID         string
	Sequence   uint64
	Channel    string
	EventType  string
	FlowID     string
	NodeID     string
	WorkitemID string
	Timestamp  time.Time
	TraceID    string
	Attributes map[string]string
	Payload    []byte
	Labels     []Label
}

// Store provides durable event storage backed by SQLite.
type Store struct {
	db  *sql.DB
	mu  sync.Mutex
	seq map[string]uint64 // per-channel sequence counters
}

// New opens (or creates) a SQLite database at dsn and initialises the
// schema. Pass ":memory:" for in-memory testing.
func New(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	s := &Store{db: db, seq: make(map[string]uint64)}

	if err := s.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := s.loadSequences(); err != nil {
		_ = db.Close()
		return nil, err
	}

	return s, nil
}

// Close releases the database connection.
func (s *Store) Close() error { return s.db.Close() }

// Insert persists an event and assigns it the next per-channel sequence
// number. The assigned sequence is written into evt.Sequence and returned.
// Labels are stored in a separate table within the same transaction.
func (s *Store) Insert(ctx context.Context, evt *Event) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq[evt.Channel]++
	evt.Sequence = s.seq[evt.Channel]

	attrJSON, err := json.Marshal(evt.Attributes)
	if err != nil {
		return 0, fmt.Errorf("marshal attributes: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.seq[evt.Channel]--
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after commit

	_, err = tx.ExecContext(ctx,
		`INSERT INTO events (id, sequence, channel, event_type, flow_id, node_id, workitem_id,
		                     timestamp, trace_id, attributes, payload)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		evt.ID, evt.Sequence, evt.Channel, evt.EventType,
		evt.FlowID, evt.NodeID, evt.WorkitemID,
		formatTime(evt.Timestamp), evt.TraceID,
		attrJSON, evt.Payload,
	)
	if err != nil {
		s.seq[evt.Channel]--
		return 0, fmt.Errorf("insert event: %w", err)
	}

	// Insert labels into the separate table.
	if len(evt.Labels) > 0 {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO event_labels (channel, sequence, key, value) VALUES (?, ?, ?, ?)`)
		if err != nil {
			s.seq[evt.Channel]--
			return 0, fmt.Errorf("prepare label insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()

		for _, lbl := range evt.Labels {
			if _, err := stmt.ExecContext(ctx, evt.Channel, evt.Sequence, lbl.Key, lbl.Value); err != nil {
				s.seq[evt.Channel]--
				return 0, fmt.Errorf("insert label: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		s.seq[evt.Channel]--
		return 0, fmt.Errorf("commit: %w", err)
	}

	return evt.Sequence, nil
}

// GetSince returns up to limit events for channel whose sequence is
// strictly greater than lastSequence, ordered by sequence ascending.
// Labels are loaded for each returned event.
func (s *Store) GetSince(ctx context.Context, channel string, lastSequence uint64, limit int) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, sequence, channel, event_type, flow_id, node_id, workitem_id,
		        timestamp, trace_id, attributes, payload
		 FROM events
		 WHERE channel = ? AND sequence > ?
		 ORDER BY sequence ASC
		 LIMIT ?`,
		channel, lastSequence, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	events, err := scanEvents(rows)
	if err != nil {
		return nil, err
	}

	// Load labels for the returned events.
	if err := s.loadLabels(ctx, events); err != nil {
		return nil, err
	}

	return events, nil
}

// MinSequence returns the smallest sequence number stored for the given
// channel, or 0 if no events exist.
func (s *Store) MinSequence(ctx context.Context, channel string) (uint64, error) {
	var seq sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT MIN(sequence) FROM events WHERE channel = ?`, channel,
	).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("min sequence: %w", err)
	}
	if !seq.Valid {
		return 0, nil
	}
	return uint64(seq.Int64), nil
}

// Evict removes events that exceed the retention policy for a channel.
// It deletes events older than the duration window and/or oldest events
// until the channel's total payload is under the size limit. Labels are
// cascade-deleted via foreign key.
func (s *Store) Evict(ctx context.Context, channel string, maxAge time.Duration, maxBytes int64) (int64, error) {
	var totalDeleted int64

	// Age-based eviction.
	if maxAge > 0 {
		cutoff := time.Now().Add(-maxAge)
		res, err := s.db.ExecContext(ctx,
			`DELETE FROM events WHERE channel = ? AND timestamp < ?`,
			channel, formatTime(cutoff),
		)
		if err != nil {
			return 0, fmt.Errorf("evict by age: %w", err)
		}
		n, _ := res.RowsAffected()
		totalDeleted += n
	}

	// Size-based eviction.
	if maxBytes > 0 {
		for {
			var totalSize int64
			err := s.db.QueryRowContext(ctx,
				`SELECT COALESCE(SUM(LENGTH(payload)), 0) FROM events WHERE channel = ?`,
				channel,
			).Scan(&totalSize)
			if err != nil {
				return totalDeleted, fmt.Errorf("query channel size: %w", err)
			}
			if totalSize <= maxBytes {
				break
			}
			// Delete the oldest batch.
			res, err := s.db.ExecContext(ctx,
				`DELETE FROM events WHERE channel = ? AND sequence IN (
				   SELECT sequence FROM events WHERE channel = ?
				   ORDER BY sequence ASC LIMIT 100
				 )`,
				channel, channel,
			)
			if err != nil {
				return totalDeleted, fmt.Errorf("evict by size: %w", err)
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				break
			}
			totalDeleted += n
		}
	}

	return totalDeleted, nil
}

// initSchema creates the events and event_labels tables if they do not
// exist.
func (s *Store) initSchema() error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	}
	for _, p := range pragmas {
		if _, err := s.db.Exec(p); err != nil {
			return fmt.Errorf("pragma %q: %w", p, err)
		}
	}

	schema := `
	CREATE TABLE IF NOT EXISTS events (
		id         TEXT    NOT NULL,
		sequence   INTEGER NOT NULL,
		channel    TEXT    NOT NULL,
		event_type TEXT    NOT NULL,
		flow_id    TEXT    NOT NULL DEFAULT '',
		node_id    TEXT    NOT NULL DEFAULT '',
		workitem_id TEXT   NOT NULL DEFAULT '',
		timestamp  TEXT    NOT NULL,
		trace_id   TEXT    NOT NULL DEFAULT '',
		attributes BLOB,
		payload    BLOB,
		PRIMARY KEY (channel, sequence)
	);
	CREATE INDEX IF NOT EXISTS idx_events_channel_seq
		ON events (channel, sequence);
	CREATE INDEX IF NOT EXISTS idx_events_channel_ts
		ON events (channel, timestamp);

	CREATE TABLE IF NOT EXISTS event_labels (
		channel  TEXT    NOT NULL,
		sequence INTEGER NOT NULL,
		key      TEXT    NOT NULL,
		value    TEXT    NOT NULL,
		FOREIGN KEY (channel, sequence) REFERENCES events (channel, sequence) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_labels_kv
		ON event_labels (key, value, channel, sequence);
	`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}

// loadSequences reads the current max sequence per channel from the
// database so that new inserts continue from the correct position.
func (s *Store) loadSequences() error {
	rows, err := s.db.Query(`SELECT channel, MAX(sequence) FROM events GROUP BY channel`)
	if err != nil {
		return fmt.Errorf("load sequences: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var ch string
		var maxSeq uint64
		if err := rows.Scan(&ch, &maxSeq); err != nil {
			return fmt.Errorf("scan sequence: %w", err)
		}
		s.seq[ch] = maxSeq
	}
	return rows.Err()
}

// loadLabels populates the Labels field on each event by querying the
// event_labels table. Events must all belong to the same channel and be
// ordered by sequence ascending.
func (s *Store) loadLabels(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}

	ch := events[0].Channel
	minSeq := events[0].Sequence
	maxSeq := events[len(events)-1].Sequence

	rows, err := s.db.QueryContext(ctx,
		`SELECT sequence, key, value FROM event_labels
		 WHERE channel = ? AND sequence >= ? AND sequence <= ?
		 ORDER BY sequence ASC`,
		ch, minSeq, maxSeq,
	)
	if err != nil {
		return fmt.Errorf("query labels: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Build a map from sequence -> index in events slice for O(1) lookup.
	seqIdx := make(map[uint64]int, len(events))
	for i, evt := range events {
		seqIdx[evt.Sequence] = i
	}

	for rows.Next() {
		var seq uint64
		var key, value string
		if err := rows.Scan(&seq, &key, &value); err != nil {
			return fmt.Errorf("scan label: %w", err)
		}
		if idx, ok := seqIdx[seq]; ok {
			events[idx].Labels = append(events[idx].Labels, Label{Key: key, Value: value})
		}
	}
	return rows.Err()
}

// scanEvents reads Event rows from the result set.
func scanEvents(rows *sql.Rows) ([]Event, error) {
	var events []Event
	for rows.Next() {
		var (
			evt     Event
			ts      string
			attrRaw []byte
		)
		if err := rows.Scan(
			&evt.ID, &evt.Sequence, &evt.Channel, &evt.EventType,
			&evt.FlowID, &evt.NodeID, &evt.WorkitemID,
			&ts, &evt.TraceID, &attrRaw, &evt.Payload,
		); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		evt.Timestamp = parseTime(ts)
		if attrRaw != nil {
			if err := json.Unmarshal(attrRaw, &evt.Attributes); err != nil {
				return nil, fmt.Errorf("unmarshal attributes: %w", err)
			}
		}
		events = append(events, evt)
	}
	return events, rows.Err()
}

func formatTime(t time.Time) string { return t.UTC().Format(timeFormat) }

func parseTime(s string) time.Time {
	t, _ := time.Parse(timeFormat, s)
	return t.UTC()
}
