package sqlite

import (
	"context"
	"fmt"
	"time"
)

// StampRecord represents a governance stamp applied to an artefact version.
type StampRecord struct {
	Name         string
	ApplyingNode string
	ContentHash  string // the artefact version_hash this stamp is on
	Signature    []byte
	CertChain    []byte
	CreatedAt    time.Time
}

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
