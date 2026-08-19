package queue

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
	db        *sql.DB
	shardID   string
	queueName string
}

// newQueueStore opens (or creates) a SQLite database at the given path and
// initialises the queue schema. Use ":memory:" for testing.
func newQueueStore(dbPath, shardID, queueName string) (*queueStore, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Pin the pool to one connection: with plain ":memory:" every new
	// connection would get its own empty database, so a second pooled
	// connection silently loses the schema and any written rows (read/write
	// then race across disjoint DBs). It also serializes access to the single
	// per-pod queue.db file, avoiding SQLITE_BUSY between pooled connections.
	db.SetMaxOpenConns(1)

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

	s := &queueStore{db: db, shardID: shardID, queueName: queueName}
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
CREATE TABLE IF NOT EXISTS queue_items (
    workitem_id TEXT PRIMARY KEY,
    shard_id    TEXT NOT NULL,
    queue_name  TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'waiting',
    enqueued_at DATETIME NOT NULL DEFAULT (datetime('now')),
    claimed_at  DATETIME,
    generation  TEXT NOT NULL DEFAULT '',
    backup_shard TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_status ON queue_items(status);
CREATE INDEX IF NOT EXISTS idx_shard ON queue_items(shard_id);
CREATE INDEX IF NOT EXISTS idx_backup_shard ON queue_items(backup_shard);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("exec schema: %w", err)
	}
	return nil
}

// enqueue inserts a new owner row into the queue with status "waiting".
// generation is the parking-event ID (R-C2); backupShard is the recorded
// backup identity (” = deferred / no backup yet, R-C1).
func (s *queueStore) enqueue(ctx context.Context, workitemID, generation, backupShard string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO queue_items (workitem_id, shard_id, queue_name, status, generation, backup_shard) `+
			`VALUES (?, ?, ?, 'waiting', ?, ?)`,
		workitemID, s.shardID, s.queueName, generation, backupShard,
	)
	if err != nil {
		return fmt.Errorf("enqueue: %w", err)
	}
	return nil
}

// listOwnerRows returns the rows this shard OWNS (shard_id == s.shardID),
// filtered by the given criteria. Returns the rows AND the total matching
// count (which feeds GetLocalQueue's pagination response Total).
func (s *queueStore) listOwnerRows(ctx context.Context, filter QueueFilter) ([]QueueItem, int, error) {
	where := "WHERE shard_id = ?"
	args := []any{s.shardID}
	if filter.Status != nil {
		where += " AND status = ?"
		args = append(args, string(*filter.Status))
	}
	return s.queryRows(ctx, filter, where, args)
}

// listBackups returns the backup rows this shard HOLDS for foreign owners
// (shard_id != s.shardID), filtered by the given criteria. Same
// count-bearing shape as listOwnerRows so GetLocalQueue's Total = owner
// count + backup count when it serves both kinds.
func (s *queueStore) listBackups(ctx context.Context, filter QueueFilter) ([]QueueItem, int, error) {
	where := "WHERE shard_id <> ?"
	args := []any{s.shardID}
	if filter.Status != nil {
		where += " AND status = ?"
		args = append(args, string(*filter.Status))
	}
	return s.queryRows(ctx, filter, where, args)
}

// queryRows runs a paginated SELECT + COUNT for the given WHERE clause.
func (s *queueStore) queryRows(
	ctx context.Context, filter QueueFilter, where string, args []any,
) ([]QueueItem, int, error) {
	// Count total matching rows.
	var total int
	countQuery := "SELECT COUNT(*) FROM queue_items " + where
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count queue items: %w", err)
	}

	// Apply pagination defaults.
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	offset := max(filter.Offset, 0)

	query := "SELECT workitem_id, shard_id, queue_name, status, enqueued_at, claimed_at, generation, " +
		"backup_shard FROM queue_items " + where + " ORDER BY enqueued_at ASC LIMIT ? OFFSET ?"
	fullArgs := append(append([]any{}, args...), limit, offset)

	rows, err := s.db.QueryContext(ctx, query, fullArgs...)
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
		`SELECT workitem_id, shard_id, queue_name, status, enqueued_at, claimed_at, generation, backup_shard
		 FROM queue_items WHERE workitem_id = ?`, workitemID,
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
		`UPDATE queue_items SET status = 'claimed', claimed_at = ?
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
		`UPDATE queue_items SET status = 'waiting', claimed_at = NULL
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

// decideWithRow deletes a "claimed" item from the queue (decision made) and
// returns the pre-delete row so the caller has its generation + backup_shard
// BEFORE the delete (what drop-propagation uses — nothing is fetched after).
// Order pinned: fetch the row (ErrQueueItemNotFound if absent) -> guard
// status=='claimed' (ErrQueueItemInvalidState) -> delete -> return the
// pre-fetched row. Ownership is NOT decided here — the store cannot
// distinguish an owner row from a backup row it holds; the mesh call sites
// compare the returned row's ShardID against self via localOwnerRow.
func (s *queueStore) decideWithRow(ctx context.Context, workitemID string) (QueueItem, error) {
	item, err := s.getByID(ctx, workitemID)
	if err != nil {
		return QueueItem{}, err
	}
	if item.Status != QueueStatusClaimed {
		return QueueItem{}, ErrQueueItemInvalidState
	}

	res, err := s.db.ExecContext(ctx,
		`DELETE FROM queue_items WHERE workitem_id = ? AND status = 'claimed'`,
		workitemID,
	)
	if err != nil {
		return QueueItem{}, fmt.Errorf("decide: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return QueueItem{}, fmt.Errorf("decide rows affected: %w", err)
	} else if n == 0 {
		// Race: the row was deleted between fetch and delete. Treat as not
		// found (nothing to decide) — same sentinel as a missing item.
		return QueueItem{}, ErrQueueItemNotFound
	}
	return *item, nil
}

// insertBackup stores a backup row on this shard for a foreign owner
// (shard_id = ownerShard, backup_shard = ”). NOT an upsert: workitem_id is
// the table's PRIMARY KEY, so a same-shard duplicate insert for an already
// stored workitem_id fails (this is why stale-copy tests pin every new backup
// to a different shard than the stale copy).
func (s *queueStore) insertBackup(ctx context.Context, workitemID, ownerShard, queueName, generation string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO queue_items (workitem_id, shard_id, queue_name, status, generation, backup_shard) `+
			`VALUES (?, ?, ?, 'waiting', ?, '')`,
		workitemID, ownerShard, queueName, generation,
	)
	if err != nil {
		return fmt.Errorf("insert backup: %w", err)
	}
	return nil
}

// listBackupsForOwner returns the backup rows this shard holds whose owner is
// ownerShard (shard_id = ownerShard). Used by handleShardDead for promotion.
func (s *queueStore) listBackupsForOwner(ctx context.Context, ownerShard string) ([]QueueItem, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT workitem_id, shard_id, queue_name, status, enqueued_at, claimed_at, generation, backup_shard
		 FROM queue_items WHERE shard_id = ?`, ownerShard,
	)
	if err != nil {
		return nil, fmt.Errorf("list backups for owner: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []QueueItem
	for rows.Next() {
		item, err := scanQueueItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate backups for owner: %w", err)
	}
	return items, nil
}

// promoteBackup flips a backup row this shard holds (shard_id = ownerShard)
// to a new owner. Returns ErrQueueItemNotFound when 0 rows affected
// (already-promoted / gone — the idempotency guard for promotion).
func (s *queueStore) promoteBackup(ctx context.Context, workitemID, ownerShard, newOwner string) (QueueItem, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE queue_items SET shard_id = ?, backup_shard = '' WHERE workitem_id = ? AND shard_id = ?`,
		newOwner, workitemID, ownerShard,
	)
	if err != nil {
		return QueueItem{}, fmt.Errorf("promote backup: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return QueueItem{}, fmt.Errorf("promote backup rows affected: %w", err)
	} else if n == 0 {
		return QueueItem{}, ErrQueueItemNotFound
	}
	item, err := s.getByID(ctx, workitemID)
	if err != nil {
		return QueueItem{}, err
	}
	return *item, nil
}

// setBackupShard records the chosen backup identity on the owner row
// (R-C4/R-C5 record-or-clear: ” = deferred / failed replicate).
func (s *queueStore) setBackupShard(ctx context.Context, workitemID, backupShard string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE queue_items SET backup_shard = ? WHERE workitem_id = ?`,
		backupShard, workitemID,
	)
	if err != nil {
		return fmt.Errorf("set backup shard: %w", err)
	}
	return nil
}

// dropByGeneration deletes a backup row generation-guarded (R-C5). Returns
// ErrQueueItemNotFound when 0 rows matched (absent or generation mismatch —
// the stale-drop guard).
func (s *queueStore) dropByGeneration(ctx context.Context, workitemID, generation string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM queue_items WHERE workitem_id = ? AND generation = ?`,
		workitemID, generation,
	)
	if err != nil {
		return fmt.Errorf("drop by generation: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("drop by generation rows affected: %w", err)
	} else if n == 0 {
		return ErrQueueItemNotFound
	}
	return nil
}

// diagnoseClaimFailure determines the correct error for a failed claim.
func (s *queueStore) diagnoseClaimFailure(ctx context.Context, workitemID string) error {
	var status string
	err := s.db.QueryRowContext(ctx,
		`SELECT status FROM queue_items WHERE workitem_id = ?`, workitemID,
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
		`SELECT status FROM queue_items WHERE workitem_id = ?`, workitemID,
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
// IsBackup is computed by shard-aware call sites (item.ShardID != s.shardID);
// the scans only fill Generation and BackupShard (there is no schema column).
func scanQueueItem(rows *sql.Rows) (QueueItem, error) {
	var item QueueItem
	var statusStr, enqueuedStr string
	var claimedStr sql.NullString

	if err := rows.Scan(
		&item.WorkitemID, &item.ShardID, &item.QueueName,
		&statusStr, &enqueuedStr, &claimedStr,
		&item.Generation, &item.BackupShard,
	); err != nil {
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

	if err := row.Scan(
		&item.WorkitemID, &item.ShardID, &item.QueueName,
		&statusStr, &enqueuedStr, &claimedStr,
		&item.Generation, &item.BackupShard,
	); err != nil {
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
