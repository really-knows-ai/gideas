package sqlite

import (
	"fmt"
	"testing"
)

// newTestStore returns an in-memory store for testing. It uses a shared-cache
// in-memory database so that all connections in the pool see the same data.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	// Use a unique shared-cache name per test to avoid cross-test interference.
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	s, err := New(dsn)
	if err != nil {
		t.Fatalf("newTestStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// --- Membership CRUD tests ---

func TestAddMember(t *testing.T) {
	s := newTestStore(t)

	roles := []PublisherRole{{Scope: "security", Level: "state"}}
	err := s.AddMember("flow-a", "flow-a-embassy:50059", []string{"state-1"}, roles)
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	m, err := s.GetMember("flow-a")
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if m.FlowIdentity != "flow-a" {
		t.Errorf("FlowIdentity = %q, want %q", m.FlowIdentity, "flow-a")
	}
	if m.EmbassyEndpoint != "flow-a-embassy:50059" {
		t.Errorf("EmbassyEndpoint = %q, want %q", m.EmbassyEndpoint, "flow-a-embassy:50059")
	}
	if len(m.StateIDs) != 1 || m.StateIDs[0] != "state-1" {
		t.Errorf("StateIDs = %v, want [state-1]", m.StateIDs)
	}
	if len(m.PublisherRoles) != 1 || m.PublisherRoles[0].Scope != "security" || m.PublisherRoles[0].Level != "state" {
		t.Errorf("PublisherRoles = %v, want [{security state}]", m.PublisherRoles)
	}
}

func TestRemoveMember(t *testing.T) {
	s := newTestStore(t)

	_ = s.AddMember("flow-a", "flow-a-embassy:50059", nil, nil)

	err := s.RemoveMember("flow-a")
	if err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	_, err = s.GetMember("flow-a")
	if err == nil {
		t.Fatal("GetMember after RemoveMember: expected error, got nil")
	}
}

func TestGetMember(t *testing.T) {
	s := newTestStore(t)

	_, err := s.GetMember("nonexistent")
	if err == nil {
		t.Fatal("GetMember nonexistent: expected error, got nil")
	}
}

func TestListMembers(t *testing.T) {
	s := newTestStore(t)

	_ = s.AddMember("flow-a", "flow-a:50059", []string{"state-1"}, nil)
	_ = s.AddMember("flow-b", "flow-b:50059", []string{"state-2"}, nil)
	_ = s.AddMember("flow-c", "flow-c:50059", []string{"state-1", "state-2"}, nil)

	all, err := s.ListMembers("")
	if err != nil {
		t.Fatalf("ListMembers all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("ListMembers all: got %d, want 3", len(all))
	}
}

func TestListMembersWithStateFilter(t *testing.T) {
	s := newTestStore(t)

	_ = s.AddMember("flow-a", "flow-a:50059", []string{"state-1"}, nil)
	_ = s.AddMember("flow-b", "flow-b:50059", []string{"state-2"}, nil)
	_ = s.AddMember("flow-c", "flow-c:50059", []string{"state-1", "state-2"}, nil)

	filtered, err := s.ListMembers("state-1")
	if err != nil {
		t.Fatalf("ListMembers state-1: %v", err)
	}
	if len(filtered) != 2 {
		t.Errorf("ListMembers state-1: got %d, want 2", len(filtered))
	}
}

func TestAddMemberDuplicate(t *testing.T) {
	s := newTestStore(t)

	_ = s.AddMember("flow-a", "flow-a:50059", nil, nil)

	err := s.AddMember("flow-a", "flow-a:50059", nil, nil)
	if err == nil {
		t.Fatal("AddMember duplicate: expected error, got nil")
	}
}

func TestRemoveMemberNotFound(t *testing.T) {
	s := newTestStore(t)

	err := s.RemoveMember("nonexistent")
	if err == nil {
		t.Fatal("RemoveMember nonexistent: expected error, got nil")
	}
}

// --- State management tests ---

func TestCreateState(t *testing.T) {
	s := newTestStore(t)

	err := s.CreateState("state-1", "Test State")
	if err != nil {
		t.Fatalf("CreateState: %v", err)
	}

	st, err := s.GetState("state-1")
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if st.ID != "state-1" {
		t.Errorf("ID = %q, want %q", st.ID, "state-1")
	}
	if st.Name != "Test State" {
		t.Errorf("Name = %q, want %q", st.Name, "Test State")
	}
}

func TestListStates(t *testing.T) {
	s := newTestStore(t)

	_ = s.CreateState("state-1", "First State")
	_ = s.CreateState("state-2", "Second State")

	states, err := s.ListStates()
	if err != nil {
		t.Fatalf("ListStates: %v", err)
	}
	if len(states) != 2 {
		t.Errorf("ListStates: got %d, want 2", len(states))
	}
}

func TestGetState(t *testing.T) {
	s := newTestStore(t)

	_, err := s.GetState("nonexistent")
	if err == nil {
		t.Fatal("GetState nonexistent: expected error, got nil")
	}
}

func TestCreateStateDuplicate(t *testing.T) {
	s := newTestStore(t)

	_ = s.CreateState("state-1", "First")

	err := s.CreateState("state-1", "Duplicate")
	if err == nil {
		t.Fatal("CreateState duplicate: expected error, got nil")
	}
}

// --- Authority lookup tests ---

func TestGetAuthorityForScope_StateLevel(t *testing.T) {
	s := newTestStore(t)

	// Add a member with a state-level publisher role for "security".
	roles := []PublisherRole{{Scope: "security", Level: "state"}}
	_ = s.AddMember("flow-authority", "flow-authority:50059", []string{"state-1"}, roles)
	// Add another member without authority.
	_ = s.AddMember("flow-regular", "flow-regular:50059", []string{"state-1"}, nil)

	m, err := s.GetAuthorityForScope("security")
	if err != nil {
		t.Fatalf("GetAuthorityForScope: %v", err)
	}
	if m.FlowIdentity != "flow-authority" {
		t.Errorf("FlowIdentity = %q, want %q", m.FlowIdentity, "flow-authority")
	}
	if m.EmbassyEndpoint != "flow-authority:50059" {
		t.Errorf("EmbassyEndpoint = %q, want %q", m.EmbassyEndpoint, "flow-authority:50059")
	}
}

func TestGetAuthorityForScope_FederationLevel(t *testing.T) {
	s := newTestStore(t)

	// Add a member with a federation-level publisher role.
	roles := []PublisherRole{{Scope: "architecture", Level: "federation"}}
	_ = s.AddMember("flow-fed-auth", "flow-fed-auth:50059", nil, roles)

	m, err := s.GetAuthorityForScope("architecture")
	if err != nil {
		t.Fatalf("GetAuthorityForScope: %v", err)
	}
	if m.FlowIdentity != "flow-fed-auth" {
		t.Errorf("FlowIdentity = %q, want %q", m.FlowIdentity, "flow-fed-auth")
	}
}

func TestGetAuthorityForScope_NotFound(t *testing.T) {
	s := newTestStore(t)

	// No members have publisher roles for "unknown-scope".
	_ = s.AddMember("flow-a", "flow-a:50059", nil, nil)

	_, err := s.GetAuthorityForScope("unknown-scope")
	if err == nil {
		t.Fatal("GetAuthorityForScope unknown scope: expected error, got nil")
	}
}

func TestGetAuthorityForScope_MemberPublisherRolesRecorded(t *testing.T) {
	s := newTestStore(t)

	// Add a member with multiple publisher roles.
	roles := []PublisherRole{
		{Scope: "security", Level: "state"},
		{Scope: "architecture", Level: "federation"},
	}
	_ = s.AddMember("flow-multi", "flow-multi:50059", []string{"state-1"}, roles)

	// Verify state-level authority for "security".
	m, err := s.GetAuthorityForScope("security")
	if err != nil {
		t.Fatalf("GetAuthorityForScope security: %v", err)
	}
	if m.FlowIdentity != "flow-multi" {
		t.Errorf("security authority FlowIdentity = %q, want %q", m.FlowIdentity, "flow-multi")
	}

	// Verify federation-level authority for "architecture".
	m, err = s.GetAuthorityForScope("architecture")
	if err != nil {
		t.Fatalf("GetAuthorityForScope architecture: %v", err)
	}
	if m.FlowIdentity != "flow-multi" {
		t.Errorf("architecture authority FlowIdentity = %q, want %q", m.FlowIdentity, "flow-multi")
	}
}

func TestGetAuthorityForScope_ReturnsFullMember(t *testing.T) {
	s := newTestStore(t)

	// The returned Member should include state IDs and publisher roles.
	roles := []PublisherRole{{Scope: "security", Level: "state"}}
	_ = s.AddMember("flow-full", "flow-full:50059", []string{"state-1", "state-2"}, roles)

	m, err := s.GetAuthorityForScope("security")
	if err != nil {
		t.Fatalf("GetAuthorityForScope: %v", err)
	}
	if len(m.StateIDs) != 2 {
		t.Errorf("StateIDs len = %d, want 2", len(m.StateIDs))
	}
	if len(m.PublisherRoles) != 1 || m.PublisherRoles[0].Scope != "security" {
		t.Errorf("PublisherRoles = %v, want [{security state}]", m.PublisherRoles)
	}
}
