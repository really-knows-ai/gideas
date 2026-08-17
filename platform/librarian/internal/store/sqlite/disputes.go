// Dispute record operations for the Librarian SQLite store.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CreateDisputeRecord inserts a new active dispute record linking a petition
// to the laws it cites. Returns an error if a record with the same
// petition_id already exists.
func (s *Store) CreateDisputeRecord(
	ctx context.Context, petitionID string, citedLawIDs []string,
) (*DisputeRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	now := formatTime(time.Now().UTC())

	_, err = tx.ExecContext(ctx,
		`INSERT INTO dispute_records (petition_id, status, created_at) VALUES (?, 'active', ?)`,
		petitionID, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert dispute record: %w", err)
	}

	if len(citedLawIDs) > 0 {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO dispute_record_laws (petition_id, law_id) VALUES (?, ?)`)
		if err != nil {
			return nil, fmt.Errorf("prepare dispute_record_laws insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()

		for _, lawID := range citedLawIDs {
			if _, err := stmt.ExecContext(ctx, petitionID, lawID); err != nil {
				return nil, fmt.Errorf("insert dispute_record_law %q: %w", lawID, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	ct, err := parseTime(now)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}

	return &DisputeRecord{
		PetitionID:  petitionID,
		CitedLawIDs: citedLawIDs,
		CreatedAt:   ct,
		Status:      DisputeStatusActive,
	}, nil
}

// RetireDisputeRecord sets the status of an active dispute record to
// retired. Returns an error if no active record with the given petition_id
// exists.
func (s *Store) RetireDisputeRecord(ctx context.Context, petitionID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE dispute_records SET status = 'retired' WHERE petition_id = ? AND status = 'active'`,
		petitionID,
	)
	if err != nil {
		return fmt.Errorf("retire dispute record: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("dispute record %q not found or already retired", petitionID)
	}
	return nil
}

// GetActiveDisputes returns all active dispute records. If lawIDFilter is
// non-empty, only records citing that specific law are returned.
func (s *Store) GetActiveDisputes(ctx context.Context, lawIDFilter string) ([]*DisputeRecord, error) {
	var (
		rows *sql.Rows
		err  error
	)

	if lawIDFilter == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT petition_id, status, created_at FROM dispute_records WHERE status = 'active'`)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT DISTINCT dr.petition_id, dr.status, dr.created_at
			 FROM dispute_records dr
			 INNER JOIN dispute_record_laws drl ON dr.petition_id = drl.petition_id
			 WHERE dr.status = 'active' AND drl.law_id = ?`, lawIDFilter)
	}
	if err != nil {
		return nil, fmt.Errorf("query active disputes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Collect partial records first, then close the rows cursor before
	// issuing follow-up queries. SQLite's in-memory mode does not support
	// concurrent statement execution on a single connection.
	type partialRecord struct {
		petitionID string
		status     string
		createdAt  string
	}
	var partials []partialRecord
	for rows.Next() {
		var p partialRecord
		if err := rows.Scan(&p.petitionID, &p.status, &p.createdAt); err != nil {
			return nil, fmt.Errorf("scan dispute record: %w", err)
		}
		partials = append(partials, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Explicitly close before follow-up queries.
	_ = rows.Close()

	var records []*DisputeRecord
	for _, p := range partials {
		ct, err := parseTime(p.createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}

		// Load cited law IDs for this record.
		lawRows, err := s.db.QueryContext(ctx,
			`SELECT law_id FROM dispute_record_laws WHERE petition_id = ?`, p.petitionID)
		if err != nil {
			return nil, fmt.Errorf("query dispute record laws: %w", err)
		}

		var lawIDs []string
		for lawRows.Next() {
			var lawID string
			if err := lawRows.Scan(&lawID); err != nil {
				_ = lawRows.Close()
				return nil, fmt.Errorf("scan law_id: %w", err)
			}
			lawIDs = append(lawIDs, lawID)
		}
		_ = lawRows.Close()
		if err := lawRows.Err(); err != nil {
			return nil, err
		}

		records = append(records, &DisputeRecord{
			PetitionID:  p.petitionID,
			CitedLawIDs: lawIDs,
			CreatedAt:   ct,
			Status:      DisputeStatus(p.status),
		})
	}
	return records, nil
}
