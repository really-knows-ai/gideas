// Package sqlite implements the SQLite-backed storage layer for the Federation
// service.
//
// It manages membership, states, and publisher role data. All writes are
// transactional. The store can be initialised with ":memory:" for testing or
// a file path for persistent operation.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

// Member represents a federation member in the store.
type Member struct {
	FlowIdentity    string
	EmbassyEndpoint string
	StateIDs        []string
	PublisherRoles  []PublisherRole
}

// PublisherRole defines a Flow's authority to publish laws within a scope.
type PublisherRole struct {
	Scope string
	Level string // "state" or "federation"
}

// Store is the SQLite-backed repository for the Federation service.
type Store struct {
	db *sql.DB
}

// New opens (or creates) a SQLite database at the given path and initialises
// the schema. Use ":memory:" for an ephemeral in-memory store suitable for
// testing.
func New(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	s := &Store{db: db}
	if err := s.initSchema(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return s, nil
}

// Close releases the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// initSchema creates the required tables if they do not already exist.
func (s *Store) initSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS members (
			flow_identity    TEXT PRIMARY KEY,
			embassy_endpoint TEXT NOT NULL,
			joined_at        TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS member_states (
			flow_identity TEXT NOT NULL REFERENCES members(flow_identity) ON DELETE CASCADE,
			state_id      TEXT NOT NULL,
			PRIMARY KEY (flow_identity, state_id)
		)`,
		`CREATE TABLE IF NOT EXISTS member_publisher_roles (
			flow_identity TEXT NOT NULL REFERENCES members(flow_identity) ON DELETE CASCADE,
			scope         TEXT NOT NULL,
			level         TEXT NOT NULL,
			PRIMARY KEY (flow_identity, scope, level)
		)`,
		`CREATE TABLE IF NOT EXISTS states (
			state_id TEXT PRIMARY KEY,
			name     TEXT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("exec schema: %w", err)
		}
	}
	return nil
}

// AddMember persists a new federation member with its state assignments and
// publisher roles. Returns an error if the flow identity already exists.
func (s *Store) AddMember(flowIdentity, embassyEndpoint string, stateIDs []string, roles []PublisherRole) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO members (flow_identity, embassy_endpoint) VALUES (?, ?)`,
		flowIdentity, embassyEndpoint)
	if err != nil {
		return fmt.Errorf("insert member: %w", err)
	}

	for _, sid := range stateIDs {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO member_states (flow_identity, state_id) VALUES (?, ?)`,
			flowIdentity, sid)
		if err != nil {
			return fmt.Errorf("insert member state: %w", err)
		}
	}

	for _, r := range roles {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO member_publisher_roles (flow_identity, scope, level) VALUES (?, ?, ?)`,
			flowIdentity, r.Scope, r.Level)
		if err != nil {
			return fmt.Errorf("insert publisher role: %w", err)
		}
	}

	return tx.Commit()
}

// RemoveMember removes a federation member by flow identity. Returns an error
// if the member does not exist.
func (s *Store) RemoveMember(flowIdentity string) error {
	res, err := s.db.Exec(`DELETE FROM members WHERE flow_identity = ?`, flowIdentity)
	if err != nil {
		return fmt.Errorf("delete member: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("member %q not found", flowIdentity)
	}
	return nil
}

// GetMember returns a member by flow identity, including state IDs and
// publisher roles. Returns an error if the member does not exist.
func (s *Store) GetMember(flowIdentity string) (*Member, error) {
	var m Member
	err := s.db.QueryRow(
		`SELECT flow_identity, embassy_endpoint FROM members WHERE flow_identity = ?`,
		flowIdentity).Scan(&m.FlowIdentity, &m.EmbassyEndpoint)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("member %q not found", flowIdentity)
	}
	if err != nil {
		return nil, fmt.Errorf("get member: %w", err)
	}

	stateIDs, err := s.memberStateIDs(flowIdentity)
	if err != nil {
		return nil, err
	}
	m.StateIDs = stateIDs

	roles, err := s.memberPublisherRoles(flowIdentity)
	if err != nil {
		return nil, err
	}
	m.PublisherRoles = roles

	return &m, nil
}

// ListMembers returns all members. If stateFilter is non-empty, only members
// belonging to that state are returned.
func (s *Store) ListMembers(stateFilter string) ([]*Member, error) {
	var rows *sql.Rows
	var err error

	if stateFilter == "" {
		rows, err = s.db.Query(`SELECT flow_identity, embassy_endpoint FROM members ORDER BY flow_identity`)
	} else {
		rows, err = s.db.Query(`
			SELECT m.flow_identity, m.embassy_endpoint
			FROM members m
			JOIN member_states ms ON m.flow_identity = ms.flow_identity
			WHERE ms.state_id = ?
			ORDER BY m.flow_identity`, stateFilter)
	}
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Collect all member rows first before making sub-queries to avoid
	// holding the rows cursor open while querying related tables.
	type partial struct {
		flowIdentity    string
		embassyEndpoint string
	}
	var partials []partial
	for rows.Next() {
		var p partial
		if err := rows.Scan(&p.flowIdentity, &p.embassyEndpoint); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		partials = append(partials, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate members: %w", err)
	}

	members := make([]*Member, 0, len(partials))
	for _, p := range partials {
		m := &Member{
			FlowIdentity:    p.flowIdentity,
			EmbassyEndpoint: p.embassyEndpoint,
		}

		stateIDs, err := s.memberStateIDs(m.FlowIdentity)
		if err != nil {
			return nil, err
		}
		m.StateIDs = stateIDs

		roles, err := s.memberPublisherRoles(m.FlowIdentity)
		if err != nil {
			return nil, err
		}
		m.PublisherRoles = roles

		members = append(members, m)
	}
	return members, nil
}

// memberStateIDs returns the state IDs for a member.
func (s *Store) memberStateIDs(flowIdentity string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT state_id FROM member_states WHERE flow_identity = ? ORDER BY state_id`,
		flowIdentity)
	if err != nil {
		return nil, fmt.Errorf("query member states: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan state id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// memberPublisherRoles returns the publisher roles for a member.
func (s *Store) memberPublisherRoles(flowIdentity string) ([]PublisherRole, error) {
	rows, err := s.db.Query(
		`SELECT scope, level FROM member_publisher_roles WHERE flow_identity = ? ORDER BY scope, level`,
		flowIdentity)
	if err != nil {
		return nil, fmt.Errorf("query publisher roles: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var roles []PublisherRole
	for rows.Next() {
		var r PublisherRole
		if err := rows.Scan(&r.Scope, &r.Level); err != nil {
			return nil, fmt.Errorf("scan publisher role: %w", err)
		}
		roles = append(roles, r)
	}
	return roles, rows.Err()
}

// State represents a federation-defined organisational group.
type State struct {
	ID   string
	Name string
}

// CreateState persists a new state. Returns an error if the state ID already
// exists.
func (s *Store) CreateState(stateID, name string) error {
	_, err := s.db.Exec(`INSERT INTO states (state_id, name) VALUES (?, ?)`, stateID, name)
	if err != nil {
		return fmt.Errorf("insert state: %w", err)
	}
	return nil
}

// GetState returns a state by ID. Returns an error if the state does not
// exist.
func (s *Store) GetState(stateID string) (*State, error) {
	var st State
	err := s.db.QueryRow(`SELECT state_id, name FROM states WHERE state_id = ?`, stateID).
		Scan(&st.ID, &st.Name)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("state %q not found", stateID)
	}
	if err != nil {
		return nil, fmt.Errorf("get state: %w", err)
	}
	return &st, nil
}

// ListStates returns all states ordered by state ID.
func (s *Store) ListStates() ([]*State, error) {
	rows, err := s.db.Query(`SELECT state_id, name FROM states ORDER BY state_id`)
	if err != nil {
		return nil, fmt.Errorf("list states: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var states []*State
	for rows.Next() {
		var st State
		if err := rows.Scan(&st.ID, &st.Name); err != nil {
			return nil, fmt.Errorf("scan state: %w", err)
		}
		states = append(states, &st)
	}
	return states, rows.Err()
}
