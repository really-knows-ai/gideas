package queue

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// sqliteTimeFormat is the format used to store and retrieve timestamps in
// SQLite. Matches the output of datetime('now') and strftime.
const sqliteTimeFormat = "2006-01-02 15:04:05"

// choicesSep separates the elements of a QueueItem's Choices slice when they
// are joined into a single TEXT column.
//
//	ponytail: choices are stored as a single TEXT column joined by a control
//	character (0x1F, the ASCII unit separator) rather than a child table. This
//	is the simplest round-trip for the mirror's read-mostly workload; the wire
//	QueueItem carries choices as a short []string that never contains 0x1F, so
//	the encoding is lossless. A child table would be needed only if choices
//	were queried by value, which the SPEC never requires.
const choicesSep = "\x1f"

// Store is the sidecar mirror's in-memory queue store. It is a passive "dumb"
// mirror of the items the queue-service broadcasts: writes are applied
// generation-guarded (R-3.3), reads serve ALL rows regardless of owner
// shard_id, and there is no owner/backup split (single-backup model deleted).
type Store interface {
	// ApplyItem is the generation-guarded broadcast write (R-3.3): apply if the
	// carried item.Generation is >= the stored generation, else no-op. A new
	// workitem_id is always inserted (as the carried status, default waiting).
	ApplyItem(ctx context.Context, item QueueItem) error
	// DropItem deletes a row only when its stored generation matches; a
	// mismatch or absent row returns ErrQueueItemNotFound.
	DropItem(ctx context.Context, workitemID, generation string) error
	// Claim transitions waiting->claimed (CAS); second claim on same item is
	// ErrQueueItemAlreadyClaimed; absent is ErrQueueItemNotFound.
	Claim(ctx context.Context, workitemID string) (*QueueItem, error)
	// Release transitions claimed->waiting; wrong state -> ErrQueueItemInvalidState; absent -> ErrQueueItemNotFound.
	Release(ctx context.Context, workitemID string) (*QueueItem, error)
	// Decide deletes a claimed row; not claimed -> ErrQueueItemInvalidState; absent -> ErrQueueItemNotFound.
	Decide(ctx context.Context, workitemID string, choice string) error
	// GetLocalQueue serves ALL stored rows regardless of owner shard_id (the
	// dumb mirror). Returns (items, total, error).
	GetLocalQueue(ctx context.Context, filter QueueFilter) ([]QueueItem, int, error)
	// Close releases the store's underlying database connection.
	Close() error
}

// queueStore is the SQLite (":memory:") implementation of Store. It mirrors
// the moved SDK queueStore with the backup machinery DROPPED (no backup_shard
// column/index, and listBackups/insertBackup/listBackupsForOwner/promoteBackup/
// setBackupShard are all GONE). It pins the pool to one connection and enables
// WAL, matching the SDK approach.
type queueStore struct {
	db        *sql.DB
	shardID   string
	queueName string
}

// NewInMemoryStore constructs a Store backed by an in-memory SQLite database.
func NewInMemoryStore(shardID, queueName string) (Store, error) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Pin the pool to one connection: with plain ":memory:" every new
	// connection would get its own empty database, so a second pooled
	// connection silently loses the schema and any written rows (read/write
	// then race across disjoint DBs).
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

// initSchema creates the queue_items table and indexes if they do not exist.
// The backup_shard column/index from the SDK is deliberately absent: the
// sidecar mirror is a dumb mirror with no owner/backup split.
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
    choices     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_status ON queue_items(status);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("exec schema: %w", err)
	}
	return nil
}

// ApplyItem applies a broadcast write (R-3.3). A new workitem_id inserts a
// row carrying the item's fields (status defaults to "waiting", enqueued_at
// defaults to now when zero). An existing row is overwritten only when the
// carried generation is >= the stored one; an older generation is a no-op and
// never downgrades the newer copy. Equal generation (re-delivery) is applied
// idempotently.
func (s *queueStore) ApplyItem(ctx context.Context, item QueueItem) error {
	stored, err := s.getByID(ctx, item.WorkitemID)
	if err != nil {
		// Absent -> insert.
		return s.insert(ctx, item)
	}
	// Present -> generation guard. Fixed-width hex generations compare
	// lexicographically (== creation order).
	if item.Generation < stored.Generation {
		// Older generation: no-op, never downgrade.
		return nil
	}
	return s.update(ctx, item)
}

// insert inserts a new row carrying the item's fields. status/enqueued_at
// fall back to defaults when empty/zero.
func (s *queueStore) insert(ctx context.Context, item QueueItem) error {
	status := item.Status
	if status == "" {
		status = QueueStatusWaiting
	}
	enqueued := item.EnqueuedAt
	if enqueued.IsZero() {
		enqueued = time.Now().UTC()
	}
	queueName := item.QueueName
	if queueName == "" {
		queueName = s.queueName
	}
	var claimedAt any
	if item.ClaimedAt != nil {
		claimedAt = item.ClaimedAt.Format(sqliteTimeFormat)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO queue_items (workitem_id, shard_id, queue_name, status, enqueued_at, claimed_at, generation, choices)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		item.WorkitemID, item.ShardID, queueName, string(status),
		enqueued.Format(sqliteTimeFormat), claimedAt, item.Generation, encodeChoices(item.Choices),
	)
	if err != nil {
		return fmt.Errorf("apply item (insert): %w", err)
	}
	return nil
}

// update overwrites an existing row with the item's carried fields. Caller
// has already verified the generation guard (item.Generation >= stored).
func (s *queueStore) update(ctx context.Context, item QueueItem) error {
	status := item.Status
	if status == "" {
		status = QueueStatusWaiting
	}
	var claimedAt any
	if item.ClaimedAt != nil {
		claimedAt = item.ClaimedAt.Format(sqliteTimeFormat)
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE queue_items SET shard_id = ?, queue_name = ?, status = ?, claimed_at = ?, generation = ?, choices = ?
		 WHERE workitem_id = ?`,
		item.ShardID, item.QueueName, string(status), claimedAt, item.Generation, encodeChoices(item.Choices),
		item.WorkitemID,
	)
	if err != nil {
		return fmt.Errorf("apply item (update): %w", err)
	}
	return nil
}

// DropItem deletes a row only when its stored generation matches. A mismatch
// or absent row returns ErrQueueItemNotFound.
func (s *queueStore) DropItem(ctx context.Context, workitemID, generation string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM queue_items WHERE workitem_id = ? AND generation = ?`,
		workitemID, generation,
	)
	if err != nil {
		return fmt.Errorf("drop item: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("drop item rows affected: %w", err)
	} else if n == 0 {
		return ErrQueueItemNotFound
	}
	return nil
}

// Claim transitions waiting->claimed (CAS).
func (s *queueStore) Claim(ctx context.Context, workitemID string) (*QueueItem, error) {
	now := time.Now().UTC().Format(sqliteTimeFormat)
	res, err := s.db.ExecContext(ctx,
		`UPDATE queue_items SET status = ?, claimed_at = ?
		 WHERE workitem_id = ? AND status = ?`,
		QueueStatusClaimed, now, workitemID, QueueStatusWaiting,
	)
	if err != nil {
		return nil, fmt.Errorf("claim: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, fmt.Errorf("claim rows affected: %w", err)
	} else if n == 0 {
		return nil, s.diagnoseClaimFailure(ctx, workitemID)
	}
	return s.getByID(ctx, workitemID)
}

// Release transitions claimed->waiting (CAS).
func (s *queueStore) Release(ctx context.Context, workitemID string) (*QueueItem, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE queue_items SET status = ?, claimed_at = NULL
		 WHERE workitem_id = ? AND status = ?`,
		QueueStatusWaiting, workitemID, QueueStatusClaimed,
	)
	if err != nil {
		return nil, fmt.Errorf("release: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, fmt.Errorf("release rows affected: %w", err)
	} else if n == 0 {
		return nil, s.diagnoseStateFailure(ctx, workitemID)
	}
	return s.getByID(ctx, workitemID)
}

// Decide deletes a claimed row. The choice is carried by the caller and is not
// persisted by the store; the store only confirms the claimed state before
// deleting.
func (s *queueStore) Decide(ctx context.Context, workitemID, choice string) error {
	item, err := s.getByID(ctx, workitemID)
	if err != nil {
		return err
	}
	if item.Status != QueueStatusClaimed {
		return ErrQueueItemInvalidState
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM queue_items WHERE workitem_id = ? AND status = ?`,
		workitemID, QueueStatusClaimed,
	); err != nil {
		return fmt.Errorf("decide: %w", err)
	}
	return nil
}

// GetLocalQueue serves ALL stored rows regardless of owner shard_id (the dumb
// mirror — no owner filter). An optional Status filter is applied, then
// pagination with a default limit of 100. total is the full count matching the
// filters (before pagination).
func (s *queueStore) GetLocalQueue(ctx context.Context, filter QueueFilter) ([]QueueItem, int, error) {
	where := "WHERE 1=1"
	args := []any{}
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
	var total int
	countQuery := "SELECT COUNT(*) FROM queue_items " + where
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count queue items: %w", err)
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	offset := max(filter.Offset, 0)

	query := "SELECT workitem_id, shard_id, queue_name, status, enqueued_at, claimed_at, generation, choices" +
		" FROM queue_items " + where + " ORDER BY enqueued_at ASC LIMIT ? OFFSET ?"
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
		`SELECT workitem_id, shard_id, queue_name, status, enqueued_at, claimed_at, generation, choices
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

// diagnoseStateFailure determines the correct error for a failed release.
func (s *queueStore) diagnoseStateFailure(ctx context.Context, workitemID string) error {
	var status string
	err := s.db.QueryRowContext(ctx,
		`SELECT status FROM queue_items WHERE workitem_id = ?`, workitemID,
	).Scan(&status)
	if err == sql.ErrNoRows {
		return ErrQueueItemNotFound
	}
	if err != nil {
		return fmt.Errorf("diagnose state: %w", err)
	}
	// Item exists but is not in "claimed" state.
	return ErrQueueItemInvalidState
}

// scanQueueItem scans a QueueItem from a sql.Rows iterator.
func scanQueueItem(rows *sql.Rows) (QueueItem, error) {
	var item QueueItem
	var statusStr, enqueuedStr, choicesStr string
	var claimedStr sql.NullString

	if err := rows.Scan(
		&item.WorkitemID, &item.ShardID, &item.QueueName,
		&statusStr, &enqueuedStr, &claimedStr,
		&item.Generation, &choicesStr,
	); err != nil {
		return QueueItem{}, fmt.Errorf("scan queue item: %w", err)
	}

	item.Status = QueueStatus(statusStr)
	item.EnqueuedAt = parseQueueTime(enqueuedStr)
	if claimedStr.Valid {
		t := parseQueueTime(claimedStr.String)
		item.ClaimedAt = &t
	}
	item.Choices = decodeChoices(choicesStr)
	return item, nil
}

// scanQueueItemRow scans a QueueItem from a sql.Row.
func scanQueueItemRow(row *sql.Row) (QueueItem, error) {
	var item QueueItem
	var statusStr, enqueuedStr, choicesStr string
	var claimedStr sql.NullString

	if err := row.Scan(
		&item.WorkitemID, &item.ShardID, &item.QueueName,
		&statusStr, &enqueuedStr, &claimedStr,
		&item.Generation, &choicesStr,
	); err != nil {
		return QueueItem{}, err
	}

	item.Status = QueueStatus(statusStr)
	item.EnqueuedAt = parseQueueTime(enqueuedStr)
	if claimedStr.Valid {
		t := parseQueueTime(claimedStr.String)
		item.ClaimedAt = &t
	}
	item.Choices = decodeChoices(choicesStr)
	return item, nil
}

// encodeChoices joins a []string into the single TEXT column encoding.
func encodeChoices(choices []string) string {
	return strings.Join(choices, choicesSep)
}

// decodeChoices splits the single TEXT column encoding back into []string.
// An empty string decodes to an empty (nil) slice, matching an inserted item
// with no choices.
func decodeChoices(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, choicesSep)
}

// parseQueueTime parses a SQLite datetime string. Falls back to RFC3339.
func parseQueueTime(s string) time.Time {
	t, err := time.Parse(sqliteTimeFormat, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t
}

// Close releases the underlying database connection.
func (s *queueStore) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}
