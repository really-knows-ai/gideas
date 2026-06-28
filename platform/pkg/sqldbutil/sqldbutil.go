// Package sqldbutil provides shared SQLite utilities for Foundry Flow stores.
package sqldbutil

import (
	"database/sql"
	"fmt"
	"time"
)

// TimeFormat is the SQLite-compatible timestamp format used across stores.
const TimeFormat = "2006-01-02 15:04:05"

// FormatTime formats a time as a SQLite-compatible UTC string.
func FormatTime(t time.Time) string {
	return t.UTC().Format(TimeFormat)
}

// ParseTime parses a SQLite timestamp string. Falls back to RFC3339 for
// compatibility with timestamps from other systems.
func ParseTime(s string) (time.Time, error) {
	t, err := time.Parse(TimeFormat, s)
	if err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

// SetPragmas configures standard SQLite pragmas on a connection.
func SetPragmas(db *sql.DB) error {
	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(p); err != nil {
			return fmt.Errorf("pragma %q: %w", p, err)
		}
	}
	return nil
}
