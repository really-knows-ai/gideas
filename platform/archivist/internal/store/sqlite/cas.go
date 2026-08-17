package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

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
