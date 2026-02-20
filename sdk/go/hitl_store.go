package flow

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// sqliteTimeFormat is the format used to store and retrieve timestamps in
// SQLite. Matches the output of datetime('now') and strftime.
const sqliteTimeFormat = "2006-01-02 15:04:05"

// queueStore is the SQLite-backed persistence layer for the HITL queue.
// Each pod has its own queue.db file with items owned by that shard.
type queueStore struct {
	db      *sql.DB
	shardID string
}

// newQueueStore opens (or creates) a SQLite database at the given path and
// initialises the queue schema. Use ":memory:" for testing.
func newQueueStore(dbPath, shardID string) (*queueStore, error) {
	db, err := sql.Open("sqlite3", dbPath)
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

	s := &queueStore{db: db, shardID: shardID}
	if err := s.initSchema(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return s, nil
}

// close closes the underlying database connection.
func (s *queueStore) close() error {
	return s.db.Close()
}

// initSchema creates the queue table and indexes if they do not already exist.
func (s *queueStore) initSchema() error {
	const schema = `
CREATE TABLE IF NOT EXISTS hitl_queue (
    workitem_id TEXT PRIMARY KEY,
    shard_id    TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'waiting',
    enqueued_at DATETIME NOT NULL DEFAULT (datetime('now')),
    claimed_at  DATETIME
);

CREATE INDEX IF NOT EXISTS idx_status ON hitl_queue(status);
CREATE INDEX IF NOT EXISTS idx_shard ON hitl_queue(shard_id);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("exec schema: %w", err)
	}
	return nil
}

// enqueue inserts a new item into the queue with status "waiting".
func (s *queueStore) enqueue(ctx context.Context, workitemID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO hitl_queue (workitem_id, shard_id, status) VALUES (?, ?, 'waiting')`,
		workitemID, s.shardID,
	)
	if err != nil {
		return fmt.Errorf("enqueue: %w", err)
	}
	return nil
}

// getLocal returns queue items from this shard, filtered by the given criteria.
func (s *queueStore) getLocal(ctx context.Context, filter QueueFilter) ([]QueueItem, int, error) {
	// Build the WHERE clause.
	where := "WHERE shard_id = ?"
	args := []any{s.shardID}
	if filter.Status != nil {
		where += " AND status = ?"
		args = append(args, string(*filter.Status))
	}

	// Count total matching rows.
	var total int
	countQuery := "SELECT COUNT(*) FROM hitl_queue " + where
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count queue items: %w", err)
	}

	// Apply pagination defaults.
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	offset := max(filter.Offset, 0)

	query := "SELECT workitem_id, shard_id, status, enqueued_at, claimed_at FROM hitl_queue " +
		where + " ORDER BY enqueued_at ASC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query queue: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []QueueItem
	for rows.Next() {
		item, err := scanQueueItem(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate queue: %w", err)
	}
	return items, total, nil
}

// getByID retrieves a single queue item by Workitem ID.
func (s *queueStore) getByID(ctx context.Context, workitemID string) (*QueueItem, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT workitem_id, shard_id, status, enqueued_at, claimed_at
		 FROM hitl_queue WHERE workitem_id = ?`, workitemID,
	)
	item, err := scanQueueItemRow(row)
	if err == sql.ErrNoRows {
		return nil, ErrQueueItemNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get queue item: %w", err)
	}
	return &item, nil
}

// claim transitions an item from "waiting" to "claimed".
func (s *queueStore) claim(ctx context.Context, workitemID string) (*QueueItem, error) {
	now := time.Now().UTC().Format(sqliteTimeFormat)
	res, err := s.db.ExecContext(ctx,
		`UPDATE hitl_queue SET status = 'claimed', claimed_at = ?
		 WHERE workitem_id = ? AND status = 'waiting'`,
		now, workitemID,
	)
	if err != nil {
		return nil, fmt.Errorf("claim: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("claim rows affected: %w", err)
	}
	if n == 0 {
		return nil, s.diagnoseClaimFailure(ctx, workitemID)
	}

	return s.getByID(ctx, workitemID)
}

// release transitions a "claimed" item back to "waiting".
func (s *queueStore) release(ctx context.Context, workitemID string) (*QueueItem, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE hitl_queue SET status = 'waiting', claimed_at = NULL
		 WHERE workitem_id = ? AND status = 'claimed'`,
		workitemID,
	)
	if err != nil {
		return nil, fmt.Errorf("release: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("release rows affected: %w", err)
	}
	if n == 0 {
		return nil, s.diagnoseStateFailure(ctx, workitemID, "release")
	}

	return s.getByID(ctx, workitemID)
}

// complete deletes a "claimed" item from the queue (decision made).
func (s *queueStore) complete(ctx context.Context, workitemID string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM hitl_queue WHERE workitem_id = ? AND status = 'claimed'`,
		workitemID,
	)
	if err != nil {
		return fmt.Errorf("complete: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("complete rows affected: %w", err)
	}
	if n == 0 {
		return s.diagnoseStateFailure(ctx, workitemID, "complete")
	}
	return nil
}

// countByStatus returns the count of items on this shard, optionally filtered.
func (s *queueStore) countByStatus(ctx context.Context, status *QueueStatus) (int, error) {
	query := "SELECT COUNT(*) FROM hitl_queue WHERE shard_id = ?"
	args := []any{s.shardID}
	if status != nil {
		query += " AND status = ?"
		args = append(args, string(*status))
	}

	var count int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count by status: %w", err)
	}
	return count, nil
}

// diagnoseClaimFailure determines the correct error for a failed claim.
func (s *queueStore) diagnoseClaimFailure(ctx context.Context, workitemID string) error {
	var status string
	err := s.db.QueryRowContext(ctx,
		`SELECT status FROM hitl_queue WHERE workitem_id = ?`, workitemID,
	).Scan(&status)
	if err == sql.ErrNoRows {
		return ErrQueueItemNotFound
	}
	if err != nil {
		return fmt.Errorf("diagnose claim: %w", err)
	}
	// Item exists but is not in "waiting" state.
	return ErrQueueItemAlreadyClaimed
}

// diagnoseStateFailure determines the correct error for a failed release/complete.
func (s *queueStore) diagnoseStateFailure(ctx context.Context, workitemID, op string) error {
	var status string
	err := s.db.QueryRowContext(ctx,
		`SELECT status FROM hitl_queue WHERE workitem_id = ?`, workitemID,
	).Scan(&status)
	if err == sql.ErrNoRows {
		return ErrQueueItemNotFound
	}
	if err != nil {
		return fmt.Errorf("diagnose %s: %w", op, err)
	}
	// Item exists but is not in "claimed" state.
	return ErrQueueItemInvalidState
}

// scanQueueItem scans a QueueItem from a sql.Rows iterator.
func scanQueueItem(rows *sql.Rows) (QueueItem, error) {
	var item QueueItem
	var statusStr, enqueuedStr string
	var claimedStr sql.NullString

	if err := rows.Scan(&item.WorkitemID, &item.ShardID, &statusStr, &enqueuedStr, &claimedStr); err != nil {
		return QueueItem{}, fmt.Errorf("scan queue item: %w", err)
	}

	item.Status = QueueStatus(statusStr)
	item.EnqueuedAt = parseQueueTime(enqueuedStr)
	if claimedStr.Valid {
		t := parseQueueTime(claimedStr.String)
		item.ClaimedAt = &t
	}
	return item, nil
}

// scanQueueItemRow scans a QueueItem from a sql.Row.
func scanQueueItemRow(row *sql.Row) (QueueItem, error) {
	var item QueueItem
	var statusStr, enqueuedStr string
	var claimedStr sql.NullString

	if err := row.Scan(&item.WorkitemID, &item.ShardID, &statusStr, &enqueuedStr, &claimedStr); err != nil {
		return QueueItem{}, err
	}

	item.Status = QueueStatus(statusStr)
	item.EnqueuedAt = parseQueueTime(enqueuedStr)
	if claimedStr.Valid {
		t := parseQueueTime(claimedStr.String)
		item.ClaimedAt = &t
	}
	return item, nil
}

// parseQueueTime parses a SQLite datetime string. Falls back to RFC3339.
func parseQueueTime(s string) time.Time {
	t, err := time.Parse(sqliteTimeFormat, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t
}
