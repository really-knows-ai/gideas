package sqlite

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// LawGroup Store Tests
// ---------------------------------------------------------------------------

func TestUpsertLawGroup_InsertAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.UpsertLawGroup(ctx, "security", "law-by-law", 3)
	if err != nil {
		t.Fatalf("UpsertLawGroup: %v", err)
	}

	got, err := s.GetLawGroup(ctx, "security")
	if err != nil {
		t.Fatalf("GetLawGroup: %v", err)
	}
	if got.Name != "security" {
		t.Fatalf("expected name %q, got %q", "security", got.Name)
	}
	if got.Mode != "law-by-law" {
		t.Fatalf("expected mode %q, got %q", "law-by-law", got.Mode)
	}
	if got.Passes != 3 {
		t.Fatalf("expected passes 3, got %d", got.Passes)
	}
	if got.SyncedAt.IsZero() {
		t.Fatal("expected non-zero synced_at")
	}
}

func TestUpsertLawGroup_UpdateExisting(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.UpsertLawGroup(ctx, "security", "bundle", 1)
	if err != nil {
		t.Fatalf("UpsertLawGroup (first): %v", err)
	}

	err = s.UpsertLawGroup(ctx, "security", "law-by-law", 5)
	if err != nil {
		t.Fatalf("UpsertLawGroup (update): %v", err)
	}

	got, err := s.GetLawGroup(ctx, "security")
	if err != nil {
		t.Fatalf("GetLawGroup after update: %v", err)
	}
	if got.Mode != "law-by-law" {
		t.Fatalf("expected mode %q, got %q", "law-by-law", got.Mode)
	}
	if got.Passes != 5 {
		t.Fatalf("expected passes 5, got %d", got.Passes)
	}
	// synced_at should be non-zero (updated on upsert).
	if got.SyncedAt.IsZero() {
		t.Fatal("expected non-zero synced_at after update")
	}
}

func TestDeleteLawGroup_RemovesGroup(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.UpsertLawGroup(ctx, "security", "bundle", 1)
	if err != nil {
		t.Fatalf("UpsertLawGroup: %v", err)
	}

	err = s.DeleteLawGroup(ctx, "security")
	if err != nil {
		t.Fatalf("DeleteLawGroup: %v", err)
	}

	_, err = s.GetLawGroup(ctx, "security")
	if err == nil {
		t.Fatal("expected error after delete, got nil")
	}
}

func TestDeleteLawGroup_NonExistent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.DeleteLawGroup(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent group, got nil")
	}
}

func TestGetLawGroup_NonExistent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetLawGroup(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent group, got nil")
	}
}

func TestListLawGroups_ReturnsAll(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_ = s.UpsertLawGroup(ctx, "group-a", "bundle", 1)
	_ = s.UpsertLawGroup(ctx, "group-b", "law-by-law", 2)
	_ = s.UpsertLawGroup(ctx, "group-c", "bundle", 3)

	groups, err := s.ListLawGroups(ctx)
	if err != nil {
		t.Fatalf("ListLawGroups: %v", err)
	}
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}
	names := make(map[string]bool)
	for _, g := range groups {
		names[g.Name] = true
	}
	if !names["group-a"] || !names["group-b"] || !names["group-c"] {
		t.Fatalf("expected all group names, got %v", names)
	}
}

func TestListLawGroups_Empty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	groups, err := s.ListLawGroups(ctx)
	if err != nil {
		t.Fatalf("ListLawGroups: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("expected 0 groups, got %d", len(groups))
	}
}
