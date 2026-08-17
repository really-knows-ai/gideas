// Law CRUD operations for the Librarian SQLite store.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CreateLaw inserts a new active law with its scope and initial version.
// Returns the generated version hash.
func (s *Store) CreateLaw(ctx context.Context, id string, law Law) (string, error) {
	return s.createLaw(ctx, id, law, true)
}

// CreateLawInactive inserts a new law with active=0 (hearing-created, pending
// activation). Returns the generated version hash.
func (s *Store) CreateLawInactive(ctx context.Context, id string, law Law) (string, error) {
	return s.createLaw(ctx, id, law, false)
}

func (s *Store) createLaw(ctx context.Context, id string, law Law, active bool) (string, error) {
	versionHash := ComputeContentHash(law.Goal, law.Tier, law.AppliesTo, law.Representations, law.Group)

	repsJSON, err := marshalRepresentations(law.Representations)
	if err != nil {
		return "", err
	}
	atJSON, err := marshalAppliesTo(law.AppliesTo)
	if err != nil {
		return "", err
	}

	activeInt := 0
	if active {
		activeInt = 1
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	now := formatTime(time.Now().UTC())

	// Insert into laws.
	_, err = tx.ExecContext(ctx,
		`INSERT INTO laws
		 (id, goal, tier, law_group, source_flow, petition_id, active, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, law.Goal, law.Tier, law.Group, law.SourceFlow, law.PetitionID,
		activeInt, now, now,
	)
	if err != nil {
		return "", fmt.Errorf("insert law: %w", err)
	}

	// Insert scope entries.
	if len(law.AppliesTo) > 0 {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO law_applies_to (law_id, artefact_kind) VALUES (?, ?)`)
		if err != nil {
			return "", fmt.Errorf("prepare applies_to insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()

		for _, kind := range law.AppliesTo {
			if _, err := stmt.ExecContext(ctx, id, kind); err != nil {
				return "", fmt.Errorf("insert applies_to %q: %w", kind, err)
			}
		}
	}

	// Insert initial version.
	_, err = tx.ExecContext(ctx,
		`INSERT INTO law_versions
		 (law_id, version_hash, goal, tier, law_group, representations_json, applies_to_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, versionHash, law.Goal, law.Tier, law.Group, repsJSON, atJSON, now,
	)
	if err != nil {
		return "", fmt.Errorf("insert law_version: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	return versionHash, nil
}

// GetLaw returns the full law from the head (latest) version.
// Returns an error if the law is missing or retired.
func (s *Store) GetLaw(ctx context.Context, id string) (Law, error) {
	// Read from the laws table first.
	var (
		goal       string
		tier       int
		activeInt  int
		groupVal   string
		sourceFlow string
		petitionID string
		createdAt  string
		updatedAt  string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT goal, tier, active, law_group, source_flow, petition_id, created_at, updated_at FROM laws WHERE id = ?`, id,
	).Scan(&goal, &tier, &activeInt, &groupVal, &sourceFlow, &petitionID, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return Law{}, fmt.Errorf("law %q not found", id)
	}
	if err != nil {
		return Law{}, fmt.Errorf("get law: %w", err)
	}

	// Get head version (latest by created_at).
	var (
		versionHash string
		repsJSON    string
		atJSON      string
	)
	err = s.db.QueryRowContext(ctx,
		`SELECT version_hash, representations_json, applies_to_json
		 FROM law_versions WHERE law_id = ? ORDER BY rowid DESC LIMIT 1`, id,
	).Scan(&versionHash, &repsJSON, &atJSON)
	if err != nil {
		return Law{}, fmt.Errorf("get head version: %w", err)
	}

	reps, err := unmarshalRepresentations(repsJSON)
	if err != nil {
		return Law{}, err
	}
	appliesTo, err := unmarshalAppliesTo(atJSON)
	if err != nil {
		return Law{}, err
	}

	ct, err := parseTime(createdAt)
	if err != nil {
		return Law{}, fmt.Errorf("parse created_at: %w", err)
	}
	ut, err := parseTime(updatedAt)
	if err != nil {
		return Law{}, fmt.Errorf("parse updated_at: %w", err)
	}

	return Law{
		ID:              id,
		Goal:            goal,
		Tier:            tier,
		Active:          activeInt == 1,
		AppliesTo:       appliesTo,
		Representations: reps,
		Group:           groupVal,
		VersionHash:     versionHash,
		CreatedAt:       ct,
		UpdatedAt:       ut,
		SourceFlow:      sourceFlow,
		PetitionID:      petitionID,
	}, nil
}

// UpdateLaw appends a new version and updates the laws table. Returns
// the new version hash.
func (s *Store) UpdateLaw(ctx context.Context, id string, law Law) (string, error) {
	versionHash := ComputeContentHash(law.Goal, law.Tier, law.AppliesTo, law.Representations, law.Group)

	repsJSON, err := marshalRepresentations(law.Representations)
	if err != nil {
		return "", err
	}
	atJSON, err := marshalAppliesTo(law.AppliesTo)
	if err != nil {
		return "", err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	now := formatTime(time.Now().UTC())

	// Update the laws table.
	_, err = tx.ExecContext(ctx,
		`UPDATE laws SET goal = ?, tier = ?, law_group = ?, source_flow = ?, petition_id = ?, updated_at = ? WHERE id = ?`,
		law.Goal, law.Tier, law.Group, law.SourceFlow, law.PetitionID, now, id,
	)
	if err != nil {
		return "", fmt.Errorf("update law: %w", err)
	}

	// Replace scope entries.
	_, err = tx.ExecContext(ctx, `DELETE FROM law_applies_to WHERE law_id = ?`, id)
	if err != nil {
		return "", fmt.Errorf("delete old applies_to: %w", err)
	}
	if len(law.AppliesTo) > 0 {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO law_applies_to (law_id, artefact_kind) VALUES (?, ?)`)
		if err != nil {
			return "", fmt.Errorf("prepare applies_to insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()

		for _, kind := range law.AppliesTo {
			if _, err := stmt.ExecContext(ctx, id, kind); err != nil {
				return "", fmt.Errorf("insert applies_to %q: %w", kind, err)
			}
		}
	}

	// Append new version. OR IGNORE handles idempotent re-inserts when
	// content cycles back to a previously-seen hash.
	_, err = tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO law_versions
		 (law_id, version_hash, goal, tier, law_group, representations_json, applies_to_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, versionHash, law.Goal, law.Tier, law.Group, repsJSON, atJSON, now,
	)
	if err != nil {
		return "", fmt.Errorf("insert law_version: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	return versionHash, nil
}

// ReplicateLaw stores a law received from a remote Flow via Federation
// distribution. It is an upsert: if the law does not exist it is created
// as active; if it already exists its content and provenance are updated.
// Provenance fields (SourceFlow, PetitionID) are preserved through the
// write. Returns the version hash.
func (s *Store) ReplicateLaw(ctx context.Context, id string, law Law) (string, error) {
	// Check whether the law already exists.
	_, err := s.GetLaw(ctx, id)
	if err != nil {
		// Law does not exist — create as active.
		return s.CreateLaw(ctx, id, law)
	}
	// Law exists — update content and provenance.
	return s.UpdateLaw(ctx, id, law)
}

// RetireLaw deletes the law from the active registry and scope table.
// Versions remain in law_versions for audit trail.
func (s *Store) RetireLaw(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Delete scope entries first (foreign key).
	_, err = tx.ExecContext(ctx, `DELETE FROM law_applies_to WHERE law_id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete applies_to: %w", err)
	}

	// Delete the law.
	res, err := tx.ExecContext(ctx, `DELETE FROM laws WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete law: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("law %q not found", id)
	}

	return tx.Commit()
}

// ActivateLaw sets active=1 on a law. Used by ApplyLifecycleAction after
// hearing.
func (s *Store) ActivateLaw(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE laws SET active = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("activate law: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("law %q not found", id)
	}
	return nil
}

// SetTier updates the tier on a law and creates a new version.
func (s *Store) SetTier(ctx context.Context, id string, tier int) error {
	// Read current law state.
	law, err := s.GetLaw(ctx, id)
	if err != nil {
		return err
	}

	law.Tier = tier
	_, err = s.UpdateLaw(ctx, id, law)
	return err
}

// QueryLaws returns laws matching the given filter. Three base modes:
//  1. Empty filter: all active laws.
//  2. ArtefactKind set: scoped + global active laws.
//  3. ArtefactKind + RepresentationType: further filtered by MIME type in
//     representations, without stripping representations from the result.
//
// All modes support an optional Group filter: when set, only laws with
// that exact group value are returned.
func (s *Store) QueryLaws(ctx context.Context, filter QueryFilter) ([]Law, error) {
	var lawIDs []string

	if filter.GovernedArtefact == "" {
		// Mode 1: all active laws, optionally filtered by group.
		query := `SELECT id FROM laws WHERE active = 1`
		args := []any{}
		if filter.Group != "" {
			query += ` AND law_group = ?`
			args = append(args, filter.Group)
		}
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("query active laws: %w", err)
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, fmt.Errorf("scan law id: %w", err)
			}
			lawIDs = append(lawIDs, id)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	} else {
		// Mode 2/3: scoped + global active laws, optionally filtered by group.
		// A law is "global" if it has no entries in law_applies_to.
		query := `
			SELECT DISTINCT l.id FROM laws l
			LEFT JOIN law_applies_to la ON l.id = la.law_id
			WHERE l.active = 1
			AND (la.artefact_kind = ? OR la.law_id IS NULL)`
		args := []any{filter.GovernedArtefact}
		if filter.Group != "" {
			query += ` AND l.law_group = ?`
			args = append(args, filter.Group)
		}
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("query scoped laws: %w", err)
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, fmt.Errorf("scan law id: %w", err)
			}
			lawIDs = append(lawIDs, id)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	// Build full Law objects.
	var laws []Law
	for _, id := range lawIDs {
		law, err := s.GetLaw(ctx, id)
		if err != nil {
			return nil, err
		}
		laws = append(laws, law)
	}

	// Mode 3: further filter by representation type. Include laws that have
	// at least one representation matching the type. Do NOT strip
	// representations.
	if filter.RepresentationType != "" {
		var filtered []Law
		for _, law := range laws {
			for _, rep := range law.Representations {
				if rep.Type == filter.RepresentationType {
					filtered = append(filtered, law)
					break
				}
			}
		}
		laws = filtered
	}

	return laws, nil
}
