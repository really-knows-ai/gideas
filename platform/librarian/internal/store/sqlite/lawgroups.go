// LawGroup CRUD operations for the Librarian SQLite store.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// UpsertLawGroup inserts or updates a law group. On conflict, mode, passes,
// and synced_at are updated.
func (s *Store) UpsertLawGroup(ctx context.Context, name, mode string, passes int) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO law_groups (name, mode, passes, synced_at)
		 VALUES (?, ?, ?, datetime('now'))
		 ON CONFLICT(name) DO UPDATE SET
		   mode = excluded.mode,
		   passes = excluded.passes,
		   synced_at = datetime('now')`,
		name, mode, passes,
	)
	if err != nil {
		return fmt.Errorf("upsert law group: %w", err)
	}
	return nil
}

// DeleteLawGroup removes a law group by name. Returns an error if the group
// does not exist.
func (s *Store) DeleteLawGroup(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM law_groups WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete law group: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("law group %q not found", name)
	}
	return nil
}

// GetLawGroup returns a law group by name. Returns an error if the group
// does not exist.
func (s *Store) GetLawGroup(ctx context.Context, name string) (*LawGroup, error) {
	var (
		mode     string
		passes   int
		syncedAt string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT name, mode, passes, synced_at FROM law_groups WHERE name = ?`, name,
	).Scan(&name, &mode, &passes, &syncedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("law group %q not found", name)
	}
	if err != nil {
		return nil, fmt.Errorf("get law group: %w", err)
	}
	st, err := parseTime(syncedAt)
	if err != nil {
		return nil, fmt.Errorf("parse synced_at: %w", err)
	}
	return &LawGroup{
		Name:     name,
		Mode:     mode,
		Passes:   passes,
		SyncedAt: st,
	}, nil
}

// ListLawGroups returns all stored law groups, ordered by name.
// Returns an empty slice (not nil) if no groups are stored.
func (s *Store) ListLawGroups(ctx context.Context) ([]*LawGroup, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, mode, passes, synced_at FROM law_groups ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list law groups: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Collect partial results first, then close the cursor before parsing times
	// (in-memory SQLite limitation).
	type partial struct {
		name     string
		mode     string
		passes   int
		syncedAt string
	}
	var partials []partial
	for rows.Next() {
		var p partial
		if err := rows.Scan(&p.name, &p.mode, &p.passes, &p.syncedAt); err != nil {
			return nil, fmt.Errorf("scan law group: %w", err)
		}
		partials = append(partials, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()

	groups := make([]*LawGroup, 0, len(partials))
	for _, p := range partials {
		st, err := parseTime(p.syncedAt)
		if err != nil {
			return nil, fmt.Errorf("parse synced_at: %w", err)
		}
		groups = append(groups, &LawGroup{
			Name:     p.name,
			Mode:     p.mode,
			Passes:   p.passes,
			SyncedAt: st,
		})
	}
	return groups, nil
}
