package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
)

// FeedbackRecord represents a feedback item on an artefact.
type FeedbackRecord struct {
	ID           string
	WorkitemID   string
	ArtefactID   string
	Source       string
	CanWontFix   bool  // if true, refiner may refuse this feedback
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

// AddFeedback creates a new feedback item in NEW state and appends the
// initial "created" event. Returns the generated feedback ID.
func (s *Store) AddFeedback(
	ctx context.Context,
	workitemID, artefactID, source string,
	canWontFix bool, message, versionHash string,
) (string, error) {
	feedbackID := uuid.New().String()
	const stateNew int32 = 1 // flowv1.FeedbackState_FEEDBACK_STATE_NEW

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	canWontFixInt := 0
	if canWontFix {
		canWontFixInt = 1
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO feedback_items (id, workitem_id, artefact_id, source, can_wont_fix, state, message, version_hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		feedbackID, workitemID, artefactID, source, canWontFixInt, stateNew, message, versionHash,
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
		`SELECT id, workitem_id, artefact_id, source, can_wont_fix, state, message, version_hash, linked_ruling, created_at
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
		var canWontFixInt int
		if err := rows.Scan(
			&f.ID, &f.WorkitemID, &f.ArtefactID, &f.Source,
			&canWontFixInt, &f.State, &f.Message, &f.VersionHash,
			&f.LinkedRuling, &createdStr,
		); err != nil {
			return nil, fmt.Errorf("scan feedback: %w", err)
		}
		f.CanWontFix = canWontFixInt != 0
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
	var canWontFixInt int
	err = tx.QueryRowContext(ctx,
		`SELECT id, workitem_id, artefact_id, source, can_wont_fix, state,
		        message, version_hash, linked_ruling, created_at
		 FROM feedback_items WHERE id = ?`, feedbackID,
	).Scan(
		&f.ID, &f.WorkitemID, &f.ArtefactID, &f.Source,
		&canWontFixInt, &f.State, &f.Message, &f.VersionHash,
		&f.LinkedRuling, &createdStr,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("feedback %q not found", feedbackID)
	}
	if err != nil {
		return nil, fmt.Errorf("read feedback: %w", err)
	}
	f.CanWontFix = canWontFixInt != 0
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
	var canWontFixInt int
	err = tx.QueryRowContext(ctx,
		`SELECT id, workitem_id, artefact_id, source, can_wont_fix, state,
		        message, version_hash, linked_ruling, created_at
		 FROM feedback_items WHERE id = ?`, feedbackID,
	).Scan(
		&f.ID, &f.WorkitemID, &f.ArtefactID, &f.Source,
		&canWontFixInt, &f.State, &f.Message, &f.VersionHash,
		&f.LinkedRuling, &createdStr,
	)
	f.CanWontFix = canWontFixInt != 0
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

// ResolveStaleFeedback atomically resolves all feedback items for a given
// (workitemID, artefactID) that are tied to an older version (version_hash !=
// newVersionHash) and have can_wont_fix = 0 (only auto-resolvable feedback).
// Items already in RESOLVED or DEADLOCKED state, or with a linked ruling, are
// skipped. Returns the number of items resolved.
func (s *Store) ResolveStaleFeedback(
	ctx context.Context, workitemID, artefactID, newVersionHash string,
) (int64, error) {
	const stateResolved int32 = 6   // FEEDBACK_STATE_RESOLVED
	const stateDeadlocked int32 = 5 // FEEDBACK_STATE_DEADLOCKED

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Select eligible feedback items: old version, can_wont_fix=0,
	// not in terminal states, no linked ruling.
	res, err := tx.ExecContext(ctx,
		`UPDATE feedback_items
		 SET state = ?
		 WHERE workitem_id = ?
		   AND artefact_id = ?
		   AND version_hash != ?
		   AND can_wont_fix = 0
		   AND state NOT IN (?, ?)
		   AND linked_ruling = ''`,
		stateResolved,
		workitemID, artefactID, newVersionHash,
		stateResolved, stateDeadlocked,
	)
	if err != nil {
		return 0, fmt.Errorf("resolve stale feedback: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return 0, tx.Commit() // nothing to do, commit empty tx
	}

	// Append a "resolved" event for each affected item.
	//
	// ponytail: We use a subquery-based INSERT to avoid a separate
	// SELECT-then-loop. SQLite does not support INSERT ... SELECT with
	// RETURNING in older versions, so we rely on RowsAffected for the
	// count and insert events in bulk.
	_, err = tx.ExecContext(ctx,
		`INSERT INTO feedback_events (feedback_id, actor, action, message)
		 SELECT id, 'archivist', 'resolved', ?
		 FROM feedback_items
		 WHERE workitem_id = ?
		   AND artefact_id = ?
		   AND version_hash != ?
		   AND can_wont_fix = 0
		   AND state = ?
		   AND linked_ruling = ''`,
		fmt.Sprintf("superseded by version %s", newVersionHash),
		workitemID, artefactID, newVersionHash,
		stateResolved,
	)
	if err != nil {
		return 0, fmt.Errorf("insert resolve events: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}

	return n, nil
}
