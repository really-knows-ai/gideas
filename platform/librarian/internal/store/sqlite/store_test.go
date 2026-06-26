package sqlite

import (
	"context"
	"strings"
	"testing"
)

const testDivisionSecurity = "security"
const testLawSecID = "law-sec"

// testEmbeddingDims is a small dimension used for tests so we don't need
// to create 2048-dimensional vectors.
const testEmbeddingDims = 4

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(":memory:", WithEmbeddingDimension(testEmbeddingDims))
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCreateLaw_Basic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	law := Law{
		Goal:      "All code must be reviewed",
		Tier:      1,
		AppliesTo: []string{"source-code"},
		Representations: []Representation{
			{Type: "text/plain", Content: "All source code must be reviewed before merge."},
		},
	}

	hash, err := s.CreateLaw(ctx, "law-1", law)
	if err != nil {
		t.Fatalf("CreateLaw: %v", err)
	}
	if hash == "" {
		t.Fatal("expected non-empty version hash")
	}

	// Retrieve it.
	got, err := s.GetLaw(ctx, "law-1")
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	if got.Goal != "All code must be reviewed" {
		t.Fatalf("expected goal %q, got %q", "All code must be reviewed", got.Goal)
	}
	if got.Tier != 1 {
		t.Fatalf("expected tier 1, got %d", got.Tier)
	}
	if !got.Active {
		t.Fatal("expected law to be active")
	}
	if got.VersionHash != hash {
		t.Fatalf("expected version hash %q, got %q", hash, got.VersionHash)
	}
	if len(got.AppliesTo) != 1 || got.AppliesTo[0] != "source-code" {
		t.Fatalf("expected AppliesTo [source-code], got %v", got.AppliesTo)
	}
	if len(got.Representations) != 1 || got.Representations[0].Type != "text/plain" {
		t.Fatalf("unexpected representations: %v", got.Representations)
	}
}

func TestCreateLawInactive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	law := Law{
		Goal: "Pending law",
		Tier: 2,
		Representations: []Representation{
			{Type: "text/plain", Content: "A pending ruling."},
		},
	}

	_, err := s.CreateLawInactive(ctx, "law-2", law)
	if err != nil {
		t.Fatalf("CreateLawInactive: %v", err)
	}

	got, err := s.GetLaw(ctx, "law-2")
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	if got.Active {
		t.Fatal("expected law to be inactive")
	}
}

func TestVersioning_MutationProducesNewHash(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	law := Law{
		Goal:            "Original goal",
		Tier:            1,
		Representations: []Representation{{Type: "text/plain", Content: "v1"}},
	}

	hash1, err := s.CreateLaw(ctx, "law-v", law)
	if err != nil {
		t.Fatalf("CreateLaw: %v", err)
	}

	// Update the law.
	law.Goal = "Updated goal"
	law.Representations = []Representation{{Type: "text/plain", Content: "v2"}}
	hash2, err := s.UpdateLaw(ctx, "law-v", law)
	if err != nil {
		t.Fatalf("UpdateLaw: %v", err)
	}

	if hash1 == hash2 {
		t.Fatal("expected different version hashes after mutation")
	}

	// GetLaw should return the head version.
	got, err := s.GetLaw(ctx, "law-v")
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	if got.Goal != "Updated goal" {
		t.Fatalf("expected updated goal, got %q", got.Goal)
	}
	if got.VersionHash != hash2 {
		t.Fatalf("expected head version hash %q, got %q", hash2, got.VersionHash)
	}
}

func TestQueryLaws_AllActive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Create 2 active, 1 inactive.
	lawA := Law{Goal: "A", Tier: 1, Representations: []Representation{{Type: "text/plain", Content: "a"}}}
	if _, err := s.CreateLaw(ctx, "law-a", lawA); err != nil {
		t.Fatalf("CreateLaw law-a: %v", err)
	}
	lawB := Law{Goal: "B", Tier: 1, Representations: []Representation{{Type: "text/plain", Content: "b"}}}
	if _, err := s.CreateLaw(ctx, "law-b", lawB); err != nil {
		t.Fatalf("CreateLaw law-b: %v", err)
	}
	lawC := Law{Goal: "C", Tier: 2, Representations: []Representation{{Type: "text/plain", Content: "c"}}}
	if _, err := s.CreateLawInactive(ctx, "law-c", lawC); err != nil {
		t.Fatalf("CreateLawInactive law-c: %v", err)
	}

	laws, err := s.QueryLaws(ctx, QueryFilter{})
	if err != nil {
		t.Fatalf("QueryLaws: %v", err)
	}
	if len(laws) != 2 {
		t.Fatalf("expected 2 active laws, got %d", len(laws))
	}
}

func TestQueryLaws_ScopedPlusGlobal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Scoped law.
	if _, err := s.CreateLaw(ctx, "law-scoped", Law{
		Goal:            "Scoped",
		Tier:            1,
		AppliesTo:       []string{"source-code"},
		Representations: []Representation{{Type: "text/plain", Content: "scoped"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-scoped: %v", err)
	}
	// Global law (no appliesTo).
	if _, err := s.CreateLaw(ctx, "law-global", Law{
		Goal:            "Global",
		Tier:            1,
		Representations: []Representation{{Type: "text/plain", Content: "global"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-global: %v", err)
	}
	// Different scope.
	if _, err := s.CreateLaw(ctx, "law-other", Law{
		Goal:            "Other",
		Tier:            1,
		AppliesTo:       []string{"docs"},
		Representations: []Representation{{Type: "text/plain", Content: "other"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-other: %v", err)
	}

	laws, err := s.QueryLaws(ctx, QueryFilter{GovernedArtefact: "source-code"})
	if err != nil {
		t.Fatalf("QueryLaws: %v", err)
	}
	if len(laws) != 2 {
		t.Fatalf("expected 2 laws (scoped + global), got %d", len(laws))
	}

	// Verify we got the right ones.
	ids := map[string]bool{}
	for _, l := range laws {
		ids[l.ID] = true
	}
	if !ids["law-scoped"] || !ids["law-global"] {
		t.Fatalf("expected law-scoped and law-global, got %v", ids)
	}
}

func TestQueryLaws_RepresentationFiltering(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Law with markdown representation.
	if _, err := s.CreateLaw(ctx, "law-md", Law{
		Goal:      "Markdown law",
		Tier:      1,
		AppliesTo: []string{"docs"},
		Representations: []Representation{
			{Type: "text/markdown", Content: "# Rule"},
			{Type: "text/plain", Content: "Rule in plain text"},
		},
	}); err != nil {
		t.Fatalf("CreateLaw law-md: %v", err)
	}
	// Law without markdown.
	if _, err := s.CreateLaw(ctx, "law-plain", Law{
		Goal:            "Plain law",
		Tier:            1,
		AppliesTo:       []string{"docs"},
		Representations: []Representation{{Type: "text/plain", Content: "Plain only"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-plain: %v", err)
	}

	laws, err := s.QueryLaws(ctx, QueryFilter{
		GovernedArtefact:   "docs",
		RepresentationType: "text/markdown",
	})
	if err != nil {
		t.Fatalf("QueryLaws: %v", err)
	}
	if len(laws) != 1 {
		t.Fatalf("expected 1 law with markdown representation, got %d", len(laws))
	}
	if laws[0].ID != "law-md" {
		t.Fatalf("expected law-md, got %s", laws[0].ID)
	}
	// Representations should NOT be stripped.
	if len(laws[0].Representations) != 2 {
		t.Fatalf("expected 2 representations (not stripped), got %d", len(laws[0].Representations))
	}
}

func TestRetireLaw_PreservesHistory(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateLaw(ctx, "law-retire", Law{
		Goal:            "To be retired",
		Tier:            1,
		Representations: []Representation{{Type: "text/plain", Content: "old"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-retire: %v", err)
	}

	err := s.RetireLaw(ctx, "law-retire")
	if err != nil {
		t.Fatalf("RetireLaw: %v", err)
	}

	// GetLaw should fail.
	_, err = s.GetLaw(ctx, "law-retire")
	if err == nil {
		t.Fatal("expected error after retirement, got nil")
	}

	// But the version should still exist in law_versions.
	var count int
	err = s.db.QueryRow(`SELECT COUNT(*) FROM law_versions WHERE law_id = ?`, "law-retire").Scan(&count)
	if err != nil {
		t.Fatalf("query version count: %v", err)
	}
	if count == 0 {
		t.Fatal("expected version history to be preserved after retirement")
	}
}

func TestActivateLaw(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateLawInactive(ctx, "law-pending", Law{
		Goal:            "Pending",
		Tier:            2,
		Representations: []Representation{{Type: "text/plain", Content: "pending"}},
	}); err != nil {
		t.Fatalf("CreateLawInactive law-pending: %v", err)
	}

	// Should not appear in active query.
	laws, _ := s.QueryLaws(ctx, QueryFilter{})
	if len(laws) != 0 {
		t.Fatalf("expected 0 active laws, got %d", len(laws))
	}

	// Activate.
	err := s.ActivateLaw(ctx, "law-pending")
	if err != nil {
		t.Fatalf("ActivateLaw: %v", err)
	}

	// Now it should appear.
	laws, _ = s.QueryLaws(ctx, QueryFilter{})
	if len(laws) != 1 {
		t.Fatalf("expected 1 active law after activation, got %d", len(laws))
	}
}

func TestEmbedding_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	hash, _ := s.CreateLaw(ctx, "law-embed", Law{
		Goal:            "Embedded law",
		Tier:            1,
		Representations: []Representation{{Type: "text/plain", Content: "embed me"}},
	})

	embedding := []float32{0.1, 0.2, 0.3, 0.4, 0.5}
	err := s.SetEmbedding(ctx, "law-embed", hash, embedding)
	if err != nil {
		t.Fatalf("SetEmbedding: %v", err)
	}

	got, err := s.GetEmbedding(ctx, "law-embed", hash)
	if err != nil {
		t.Fatalf("GetEmbedding: %v", err)
	}
	if len(got) != len(embedding) {
		t.Fatalf("expected %d floats, got %d", len(embedding), len(got))
	}
	for i := range embedding {
		if got[i] != embedding[i] {
			t.Fatalf("embedding[%d] = %f, want %f", i, got[i], embedding[i])
		}
	}
}

func TestGetAllActiveEmbeddings(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	hash1, _ := s.CreateLaw(ctx, "law-e1", Law{
		Goal:            "Law 1",
		Tier:            1,
		AppliesTo:       []string{"source-code"},
		Representations: []Representation{{Type: "text/plain", Content: "l1"}},
	})
	if err := s.SetEmbedding(ctx, "law-e1", hash1, []float32{1.0, 0.0, 0.0}); err != nil {
		t.Fatalf("SetEmbedding law-e1: %v", err)
	}

	hash2, _ := s.CreateLaw(ctx, "law-e2", Law{
		Goal:            "Law 2",
		Tier:            1,
		Representations: []Representation{{Type: "text/plain", Content: "l2"}},
	})
	if err := s.SetEmbedding(ctx, "law-e2", hash2, []float32{0.0, 1.0, 0.0}); err != nil {
		t.Fatalf("SetEmbedding law-e2: %v", err)
	}

	// Inactive law with embedding — should NOT appear.
	hash3, _ := s.CreateLawInactive(ctx, "law-e3", Law{
		Goal:            "Law 3",
		Tier:            2,
		Representations: []Representation{{Type: "text/plain", Content: "l3"}},
	})
	if err := s.SetEmbedding(ctx, "law-e3", hash3, []float32{0.0, 0.0, 1.0}); err != nil {
		t.Fatalf("SetEmbedding law-e3: %v", err)
	}

	embeddings, err := s.GetAllActiveEmbeddings(ctx)
	if err != nil {
		t.Fatalf("GetAllActiveEmbeddings: %v", err)
	}
	if len(embeddings) != 2 {
		t.Fatalf("expected 2 active embeddings, got %d", len(embeddings))
	}
}

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

// ---------------------------------------------------------------------------
// LawGroup QueryLaws filter tests
// ---------------------------------------------------------------------------

func TestQueryLaws_GroupFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// A law in the "security" group.
	if _, err := s.CreateLaw(ctx, testLawSecID, Law{
		Goal:            "Security rule",
		Tier:            1,
		Division:        "security",
		Representations: []Representation{{Type: "text/plain", Content: "s"}},
	}); err != nil {
		t.Fatalf("CreateLaw %s: %v", testLawSecID, err)
	}
	// A law in the "default" group.
	if _, err := s.CreateLaw(ctx, "law-def", Law{
		Goal:            "Default rule",
		Tier:            1,
		Representations: []Representation{{Type: "text/plain", Content: "d"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-def: %v", err)
	}

	// At this point, both laws have empty law_group ('') because the
	// CreateLaw SQL does not set law_group. We need to set it manually
	// for this test.
	_, err := s.db.ExecContext(ctx, `UPDATE laws SET law_group = 'security' WHERE id = '`+testLawSecID+`'`)
	if err != nil {
		t.Fatalf("set law_group security: %v", err)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE laws SET law_group = 'default' WHERE id = 'law-def'`)
	if err != nil {
		t.Fatalf("set law_group default: %v", err)
	}

	// Filter by security group.
	laws, err := s.QueryLaws(ctx, QueryFilter{Group: "security"})
	if err != nil {
		t.Fatalf("QueryLaws group=security: %v", err)
	}
	if len(laws) != 1 || laws[0].ID != testLawSecID {
		t.Fatalf("expected [%s], got %v", testLawSecID, lawIDs(laws))
	}

	// Filter by default group.
	laws, err = s.QueryLaws(ctx, QueryFilter{Group: "default"})
	if err != nil {
		t.Fatalf("QueryLaws group=default: %v", err)
	}
	if len(laws) != 1 || laws[0].ID != "law-def" {
		t.Fatalf("expected [law-def], got %v", lawIDs(laws))
	}

	// Empty group filter returns all.
	laws, err = s.QueryLaws(ctx, QueryFilter{})
	if err != nil {
		t.Fatalf("QueryLaws no filter: %v", err)
	}
	if len(laws) != 2 {
		t.Fatalf("expected 2 laws, got %d", len(laws))
	}
}

func TestQueryLaws_GroupAndArtefactFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateLaw(ctx, "law-ss", Law{
		Goal: "Security scoped", Tier: 1, Division: "security", AppliesTo: []string{"source-code"},
		Representations: []Representation{{Type: "text/plain", Content: "ss"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-ss: %v", err)
	}
	if _, err := s.CreateLaw(ctx, "law-sg", Law{
		Goal: "Security global", Tier: 1, Division: "security",
		Representations: []Representation{{Type: "text/plain", Content: "sg"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-sg: %v", err)
	}

	_, err := s.db.ExecContext(ctx, `UPDATE laws SET law_group = 'security' WHERE id IN ('law-ss', 'law-sg')`)
	if err != nil {
		t.Fatalf("set law_group: %v", err)
	}

	// Filter by artefact + group.
	// Global laws (no appliesTo) matching the group should also be included.
	laws, err := s.QueryLaws(ctx, QueryFilter{GovernedArtefact: "source-code", Group: "security"})
	if err != nil {
		t.Fatalf("QueryLaws: %v", err)
	}
	if len(laws) != 2 {
		t.Fatalf("expected 2 laws (scoped+global), got %d: %v", len(laws), lawIDs(laws))
	}
	ids := map[string]bool{}
	for _, l := range laws {
		ids[l.ID] = true
	}
	if !ids["law-ss"] || !ids["law-sg"] {
		t.Fatalf("expected law-ss and law-sg, got %v", lawIDs(laws))
	}
}

func TestQueryLaws_GroupAndDivisionFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateLaw(ctx, "law-sec", Law{
		Goal: "Security rule", Tier: 1, Division: "security",
		Representations: []Representation{{Type: "text/plain", Content: "s"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-sec: %v", err)
	}
	if _, err := s.CreateLaw(ctx, "law-arch", Law{
		Goal: "Arch rule", Tier: 1, Division: "architecture",
		Representations: []Representation{{Type: "text/plain", Content: "a"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-arch: %v", err)
	}

	_, err := s.db.ExecContext(ctx, `UPDATE laws SET law_group = 'security' WHERE id = 'law-sec'`)
	if err != nil {
		t.Fatalf("set law_group security: %v", err)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE laws SET law_group = 'security' WHERE id = 'law-arch'`)
	if err != nil {
		t.Fatalf("set law_group security for arch: %v", err)
	}

	// Filter by group + division.
	laws, err := s.QueryLaws(ctx, QueryFilter{Group: "security", Division: "security"})
	if err != nil {
		t.Fatalf("QueryLaws: %v", err)
	}
	if len(laws) != 1 || laws[0].ID != "law-sec" {
		t.Fatalf("expected [law-sec], got %v", lawIDs(laws))
	}
}

func TestContentHash_Deterministic(t *testing.T) {
	h1 := ComputeContentHash("goal", 1, []string{"b", "a"}, []Representation{
		{Type: "text/plain", Content: "content"},
		{Type: "application/rego", Content: "rule"},
	}, testDivisionSecurity)
	h2 := ComputeContentHash("goal", 1, []string{"a", "b"}, []Representation{
		{Type: "application/rego", Content: "rule"},
		{Type: "text/plain", Content: "content"},
	}, testDivisionSecurity)

	if h1 != h2 {
		t.Fatalf("content hash should be deterministic regardless of field ordering, got %q and %q", h1, h2)
	}
}

func TestContentHash_DifferentContent(t *testing.T) {
	h1 := ComputeContentHash("goal A", 1, nil, []Representation{{Type: "text/plain", Content: "a"}}, "")
	h2 := ComputeContentHash("goal B", 1, nil, []Representation{{Type: "text/plain", Content: "a"}}, "")

	if h1 == h2 {
		t.Fatal("different goals should produce different hashes")
	}
}

func TestGetLaw_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetLaw(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing law, got nil")
	}
}

func TestRetireLaw_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.RetireLaw(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing law, got nil")
	}
}

func TestSetTier(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateLaw(ctx, "law-tier", Law{
		Goal:            "Tier test",
		Tier:            1,
		Representations: []Representation{{Type: "text/plain", Content: "tier"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-tier: %v", err)
	}

	err := s.SetTier(ctx, "law-tier", 2)
	if err != nil {
		t.Fatalf("SetTier: %v", err)
	}

	got, err := s.GetLaw(ctx, "law-tier")
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	if got.Tier != 2 {
		t.Fatalf("expected tier 2, got %d", got.Tier)
	}
}

func TestGetLawsByScope(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateLaw(ctx, "law-s1", Law{
		Goal:            "Scoped A",
		Tier:            1,
		AppliesTo:       []string{"source-code", "docs"},
		Representations: []Representation{{Type: "text/plain", Content: "a"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-s1: %v", err)
	}
	if _, err := s.CreateLaw(ctx, "law-s2", Law{
		Goal:            "Global",
		Tier:            1,
		Representations: []Representation{{Type: "text/plain", Content: "g"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-s2: %v", err)
	}
	if _, err := s.CreateLaw(ctx, "law-s3", Law{
		Goal:            "Scoped B",
		Tier:            1,
		AppliesTo:       []string{"images"},
		Representations: []Representation{{Type: "text/plain", Content: "b"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-s3: %v", err)
	}

	laws, err := s.GetLawsByScope(ctx, []string{"docs"})
	if err != nil {
		t.Fatalf("GetLawsByScope: %v", err)
	}

	ids := map[string]bool{}
	for _, l := range laws {
		ids[l.ID] = true
	}

	if !ids["law-s1"] || !ids["law-s2"] {
		t.Fatalf("expected law-s1 (overlapping scope) and law-s2 (global), got %v", ids)
	}
	if ids["law-s3"] {
		t.Fatalf("law-s3 should not be included (no scope overlap)")
	}
}

func TestDivision_Persistence(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	law := Law{
		Goal:            "Security check",
		Tier:            1,
		Division:        testDivisionSecurity,
		Representations: []Representation{{Type: "text/plain", Content: "check"}},
	}

	_, err := s.CreateLaw(ctx, "law-div", law)
	if err != nil {
		t.Fatalf("CreateLaw: %v", err)
	}

	got, err := s.GetLaw(ctx, "law-div")
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	if got.Division != testDivisionSecurity {
		t.Fatalf("expected division %q, got %q", testDivisionSecurity, got.Division)
	}
}

func TestDivision_EmptyDefault(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	law := Law{
		Goal:            "General rule",
		Tier:            1,
		Representations: []Representation{{Type: "text/plain", Content: "general"}},
	}

	_, err := s.CreateLaw(ctx, "law-nodiv", law)
	if err != nil {
		t.Fatalf("CreateLaw: %v", err)
	}

	got, err := s.GetLaw(ctx, "law-nodiv")
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	if got.Division != "" {
		t.Fatalf("expected empty division, got %q", got.Division)
	}
}

func TestQueryLaws_DivisionFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Security division.
	if _, err := s.CreateLaw(ctx, "law-sec", Law{
		Goal: "Security rule", Tier: 1, Division: testDivisionSecurity,
		Representations: []Representation{{Type: "text/plain", Content: "s"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-sec: %v", err)
	}
	// Architecture division.
	if _, err := s.CreateLaw(ctx, "law-arch", Law{
		Goal: "Architecture rule", Tier: 1, Division: "architecture",
		Representations: []Representation{{Type: "text/plain", Content: "a"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-arch: %v", err)
	}
	// No division (general).
	if _, err := s.CreateLaw(ctx, "law-gen", Law{
		Goal: "General rule", Tier: 1,
		Representations: []Representation{{Type: "text/plain", Content: "g"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-gen: %v", err)
	}

	// Filter by security division.
	laws, err := s.QueryLaws(ctx, QueryFilter{Division: testDivisionSecurity})
	if err != nil {
		t.Fatalf("QueryLaws division=security: %v", err)
	}
	if len(laws) != 1 || laws[0].ID != "law-sec" {
		t.Fatalf("expected [law-sec], got %v", lawIDs(laws))
	}

	// Empty division filter returns all.
	laws, err = s.QueryLaws(ctx, QueryFilter{})
	if err != nil {
		t.Fatalf("QueryLaws no filter: %v", err)
	}
	if len(laws) != 3 {
		t.Fatalf("expected 3 laws, got %d", len(laws))
	}
}

func TestQueryLaws_DivisionWithArtefactFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Security + scoped to source-code.
	if _, err := s.CreateLaw(ctx, "law-ss", Law{
		Goal: "Security scoped", Tier: 1, Division: testDivisionSecurity, AppliesTo: []string{"source-code"},
		Representations: []Representation{{Type: "text/plain", Content: "ss"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-ss: %v", err)
	}
	// Architecture + scoped to source-code.
	if _, err := s.CreateLaw(ctx, "law-as", Law{
		Goal: "Arch scoped", Tier: 1, Division: "architecture", AppliesTo: []string{"source-code"},
		Representations: []Representation{{Type: "text/plain", Content: "as"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-as: %v", err)
	}
	// Security + global.
	if _, err := s.CreateLaw(ctx, "law-sg", Law{
		Goal: "Security global", Tier: 1, Division: testDivisionSecurity,
		Representations: []Representation{{Type: "text/plain", Content: "sg"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-sg: %v", err)
	}

	// Filter: artefact=source-code + division=security.
	laws, err := s.QueryLaws(ctx, QueryFilter{GovernedArtefact: "source-code", Division: testDivisionSecurity})
	if err != nil {
		t.Fatalf("QueryLaws: %v", err)
	}
	ids := map[string]bool{}
	for _, l := range laws {
		ids[l.ID] = true
	}
	if len(ids) != 2 || !ids["law-ss"] || !ids["law-sg"] {
		t.Fatalf("expected law-ss and law-sg, got %v", ids)
	}
}

func TestContentHash_DivisionChangesHash(t *testing.T) {
	h1 := ComputeContentHash("goal", 1, nil, []Representation{{Type: "text/plain", Content: "c"}}, "")
	h2 := ComputeContentHash("goal", 1, nil, []Representation{{Type: "text/plain", Content: "c"}}, testDivisionSecurity)

	if h1 == h2 {
		t.Fatal("different divisions should produce different hashes")
	}
}

func TestUpdateLaw_DivisionChange(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	law := Law{
		Goal: "A rule", Tier: 1,
		Representations: []Representation{{Type: "text/plain", Content: "r"}},
	}
	hash1, err := s.CreateLaw(ctx, "law-upd", law)
	if err != nil {
		t.Fatalf("CreateLaw: %v", err)
	}

	// Change only division.
	law.Division = testDivisionSecurity
	hash2, err := s.UpdateLaw(ctx, "law-upd", law)
	if err != nil {
		t.Fatalf("UpdateLaw: %v", err)
	}

	if hash1 == hash2 {
		t.Fatal("changing division should produce a new version hash")
	}

	got, err := s.GetLaw(ctx, "law-upd")
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	if got.Division != testDivisionSecurity {
		t.Fatalf("expected division %q, got %q", testDivisionSecurity, got.Division)
	}
	if got.VersionHash != hash2 {
		t.Fatalf("expected head hash %q, got %q", hash2, got.VersionHash)
	}
}

// lawIDs is a test helper that extracts IDs from a slice of laws.
func lawIDs(laws []Law) []string {
	ids := make([]string, len(laws))
	for i, l := range laws {
		ids[i] = l.ID
	}
	return ids
}

// ---------------------------------------------------------------------------
// Dispute Record Tests
// ---------------------------------------------------------------------------

func TestCreateDisputeRecord_Basic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rec, err := s.CreateDisputeRecord(ctx, "petition-1", []string{"law-a", "law-b"})
	if err != nil {
		t.Fatalf("CreateDisputeRecord: %v", err)
	}
	if rec.PetitionID != "petition-1" {
		t.Fatalf("expected petition_id %q, got %q", "petition-1", rec.PetitionID)
	}
	if len(rec.CitedLawIDs) != 2 {
		t.Fatalf("expected 2 cited law IDs, got %d", len(rec.CitedLawIDs))
	}
	if rec.Status != DisputeStatusActive {
		t.Fatalf("expected status active, got %q", rec.Status)
	}
	if rec.CreatedAt.IsZero() {
		t.Fatal("expected non-zero created_at")
	}
}

func TestRetireDisputeRecord_Basic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.CreateDisputeRecord(ctx, "petition-2", []string{"law-c"})
	if err != nil {
		t.Fatalf("CreateDisputeRecord: %v", err)
	}

	err = s.RetireDisputeRecord(ctx, "petition-2")
	if err != nil {
		t.Fatalf("RetireDisputeRecord: %v", err)
	}

	// Should no longer appear in active disputes.
	records, err := s.GetActiveDisputes(ctx, "")
	if err != nil {
		t.Fatalf("GetActiveDisputes: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected 0 active disputes after retirement, got %d", len(records))
	}
}

func TestGetActiveDisputes_ReturnsOnlyActive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateDisputeRecord(ctx, "petition-a", []string{"law-1"}); err != nil {
		t.Fatalf("CreateDisputeRecord petition-a: %v", err)
	}
	if _, err := s.CreateDisputeRecord(ctx, "petition-b", []string{"law-2"}); err != nil {
		t.Fatalf("CreateDisputeRecord petition-b: %v", err)
	}
	// Retire one.
	if err := s.RetireDisputeRecord(ctx, "petition-a"); err != nil {
		t.Fatalf("RetireDisputeRecord petition-a: %v", err)
	}

	records, err := s.GetActiveDisputes(ctx, "")
	if err != nil {
		t.Fatalf("GetActiveDisputes: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 active dispute, got %d", len(records))
	}
	if records[0].PetitionID != "petition-b" {
		t.Fatalf("expected petition-b, got %q", records[0].PetitionID)
	}
}

func TestGetActiveDisputes_LawIDFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateDisputeRecord(ctx, "petition-x", []string{"law-10", "law-20"}); err != nil {
		t.Fatalf("CreateDisputeRecord petition-x: %v", err)
	}
	if _, err := s.CreateDisputeRecord(ctx, "petition-y", []string{"law-30"}); err != nil {
		t.Fatalf("CreateDisputeRecord petition-y: %v", err)
	}

	// Filter by law-20 -- should only return petition-x.
	records, err := s.GetActiveDisputes(ctx, "law-20")
	if err != nil {
		t.Fatalf("GetActiveDisputes with filter: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 dispute citing law-20, got %d", len(records))
	}
	if records[0].PetitionID != "petition-x" {
		t.Fatalf("expected petition-x, got %q", records[0].PetitionID)
	}
}

func TestCreateDisputeRecord_DuplicatePetitionID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateDisputeRecord(ctx, "petition-dup", []string{"law-1"}); err != nil {
		t.Fatalf("first CreateDisputeRecord: %v", err)
	}

	_, err := s.CreateDisputeRecord(ctx, "petition-dup", []string{"law-2"})
	if err == nil {
		t.Fatal("expected error on duplicate petition_id, got nil")
	}
}

func TestRetireDisputeRecord_NonExistent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.RetireDisputeRecord(ctx, "petition-ghost")
	if err == nil {
		t.Fatal("expected error for non-existent petition_id, got nil")
	}
}

// ---------------------------------------------------------------------------
// Vec Embedding Tests (sqlite-vec)
// ---------------------------------------------------------------------------

func TestUpsertVecEmbedding_Basic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	embedding := []float32{0.1, 0.2, 0.3, 0.4}
	err := s.UpsertVecEmbedding(ctx, "law-vec-1", embedding)
	if err != nil {
		t.Fatalf("UpsertVecEmbedding: %v", err)
	}

	has, err := s.HasVecEmbedding(ctx, "law-vec-1")
	if err != nil {
		t.Fatalf("HasVecEmbedding: %v", err)
	}
	if !has {
		t.Fatal("expected vec embedding to exist")
	}
}

func TestUpsertVecEmbedding_Update(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Insert.
	if err := s.UpsertVecEmbedding(ctx, "law-vec-u", []float32{1.0, 0.0, 0.0, 0.0}); err != nil {
		t.Fatalf("first UpsertVecEmbedding: %v", err)
	}

	// Update with a new embedding.
	if err := s.UpsertVecEmbedding(ctx, "law-vec-u", []float32{0.0, 1.0, 0.0, 0.0}); err != nil {
		t.Fatalf("second UpsertVecEmbedding: %v", err)
	}

	// Should still have exactly one entry.
	has, err := s.HasVecEmbedding(ctx, "law-vec-u")
	if err != nil {
		t.Fatalf("HasVecEmbedding: %v", err)
	}
	if !has {
		t.Fatal("expected vec embedding to exist after update")
	}
}

func TestUpsertVecEmbedding_DimensionMismatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Wrong dimension (3 instead of 4).
	err := s.UpsertVecEmbedding(ctx, "law-vec-bad", []float32{0.1, 0.2, 0.3})
	if err == nil {
		t.Fatal("expected error for dimension mismatch")
	}
	if !strings.Contains(err.Error(), "dimension mismatch") {
		t.Fatalf("expected dimension mismatch error, got: %v", err)
	}
}

func TestDeleteVecEmbedding_Basic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.UpsertVecEmbedding(ctx, "law-vec-del", []float32{0.1, 0.2, 0.3, 0.4}); err != nil {
		t.Fatalf("UpsertVecEmbedding: %v", err)
	}

	if err := s.DeleteVecEmbedding(ctx, "law-vec-del"); err != nil {
		t.Fatalf("DeleteVecEmbedding: %v", err)
	}

	has, err := s.HasVecEmbedding(ctx, "law-vec-del")
	if err != nil {
		t.Fatalf("HasVecEmbedding: %v", err)
	}
	if has {
		t.Fatal("expected vec embedding to be deleted")
	}
}

func TestDeleteVecEmbedding_NonExistent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Delete a non-existent embedding — should not error.
	err := s.DeleteVecEmbedding(ctx, "law-vec-ghost")
	if err != nil {
		t.Fatalf("expected no error for non-existent embedding, got: %v", err)
	}
}

func TestEmbeddingDimension(t *testing.T) {
	s := newTestStore(t)
	if s.EmbeddingDimension() != testEmbeddingDims {
		t.Fatalf("expected dimension %d, got %d", testEmbeddingDims, s.EmbeddingDimension())
	}
}

// ---------------------------------------------------------------------------
// ReplicateLaw Tests
// ---------------------------------------------------------------------------

func TestReplicateLaw_StoresWithProvenance(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	law := Law{
		Goal:      "Replicated security rule",
		Tier:      4,
		AppliesTo: []string{"source-code"},
		Representations: []Representation{
			{Type: "text/plain", Content: "All code must pass security review."},
		},
		Division:   testDivisionSecurity,
		SourceFlow: "authority-flow-ns",
		PetitionID: "petition-abc-123",
	}

	hash, err := s.ReplicateLaw(ctx, "law-rep-1", law)
	if err != nil {
		t.Fatalf("ReplicateLaw: %v", err)
	}
	if hash == "" {
		t.Fatal("expected non-empty version hash")
	}

	// Retrieve and verify all fields including provenance.
	got, err := s.GetLaw(ctx, "law-rep-1")
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	if got.Goal != "Replicated security rule" {
		t.Fatalf("expected goal %q, got %q", "Replicated security rule", got.Goal)
	}
	if got.Tier != 4 {
		t.Fatalf("expected tier 4, got %d", got.Tier)
	}
	if !got.Active {
		t.Fatal("expected replicated law to be active")
	}
	if got.Division != testDivisionSecurity {
		t.Fatalf("expected division %q, got %q", testDivisionSecurity, got.Division)
	}
	if got.SourceFlow != "authority-flow-ns" {
		t.Fatalf("expected source_flow %q, got %q", "authority-flow-ns", got.SourceFlow)
	}
	if got.PetitionID != "petition-abc-123" {
		t.Fatalf("expected petition_id %q, got %q", "petition-abc-123", got.PetitionID)
	}
	if len(got.AppliesTo) != 1 || got.AppliesTo[0] != "source-code" {
		t.Fatalf("expected AppliesTo [source-code], got %v", got.AppliesTo)
	}
	if len(got.Representations) != 1 || got.Representations[0].Type != "text/plain" {
		t.Fatalf("unexpected representations: %v", got.Representations)
	}
	if got.VersionHash != hash {
		t.Fatalf("expected version hash %q, got %q", hash, got.VersionHash)
	}
}

func TestReplicateLaw_QueryableViaQueryLaws(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	law := Law{
		Goal:      "Queryable replicated law",
		Tier:      5,
		AppliesTo: []string{"docs"},
		Representations: []Representation{
			{Type: "text/plain", Content: "Document formatting rules."},
		},
		SourceFlow: "fed-authority-ns",
	}

	if _, err := s.ReplicateLaw(ctx, "law-rep-q", law); err != nil {
		t.Fatalf("ReplicateLaw: %v", err)
	}

	// Query all active laws.
	laws, err := s.QueryLaws(ctx, QueryFilter{})
	if err != nil {
		t.Fatalf("QueryLaws: %v", err)
	}
	if len(laws) != 1 {
		t.Fatalf("expected 1 active law, got %d", len(laws))
	}
	if laws[0].ID != "law-rep-q" {
		t.Fatalf("expected law-rep-q, got %s", laws[0].ID)
	}

	// Query by artefact scope.
	laws, err = s.QueryLaws(ctx, QueryFilter{GovernedArtefact: "docs"})
	if err != nil {
		t.Fatalf("QueryLaws with scope: %v", err)
	}
	if len(laws) != 1 || laws[0].ID != "law-rep-q" {
		t.Fatalf("expected [law-rep-q], got %v", lawIDs(laws))
	}
}

func TestReplicateLaw_RetainsPetitionID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	law := Law{
		Goal: "Petition-linked law",
		Tier: 4,
		Representations: []Representation{
			{Type: "text/plain", Content: "A rule from a petition."},
		},
		SourceFlow: "petitioner-ns",
		PetitionID: "petition-xyz-789",
	}

	if _, err := s.ReplicateLaw(ctx, "law-rep-pet", law); err != nil {
		t.Fatalf("ReplicateLaw: %v", err)
	}

	got, err := s.GetLaw(ctx, "law-rep-pet")
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	if got.PetitionID != "petition-xyz-789" {
		t.Fatalf("expected petition_id %q, got %q", "petition-xyz-789", got.PetitionID)
	}
	if got.SourceFlow != "petitioner-ns" {
		t.Fatalf("expected source_flow %q, got %q", "petitioner-ns", got.SourceFlow)
	}
}

func TestReplicateLaw_UpdateExisting(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Initial replication.
	law := Law{
		Goal: "Original replicated goal",
		Tier: 4,
		Representations: []Representation{
			{Type: "text/plain", Content: "Original content."},
		},
		SourceFlow: "source-ns-1",
		PetitionID: "petition-001",
	}

	hash1, err := s.ReplicateLaw(ctx, "law-rep-upd", law)
	if err != nil {
		t.Fatalf("ReplicateLaw (create): %v", err)
	}

	// Update with new content and provenance.
	law.Goal = "Updated replicated goal"
	law.Representations = []Representation{
		{Type: "text/plain", Content: "Updated content."},
	}
	law.SourceFlow = "source-ns-2"
	law.PetitionID = "petition-002"
	law.Tier = 5

	hash2, err := s.ReplicateLaw(ctx, "law-rep-upd", law)
	if err != nil {
		t.Fatalf("ReplicateLaw (update): %v", err)
	}

	if hash1 == hash2 {
		t.Fatal("expected different version hashes after update")
	}

	// Verify updated content and provenance.
	got, err := s.GetLaw(ctx, "law-rep-upd")
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	if got.Goal != "Updated replicated goal" {
		t.Fatalf("expected updated goal, got %q", got.Goal)
	}
	if got.Tier != 5 {
		t.Fatalf("expected tier 5, got %d", got.Tier)
	}
	if got.SourceFlow != "source-ns-2" {
		t.Fatalf("expected source_flow %q, got %q", "source-ns-2", got.SourceFlow)
	}
	if got.PetitionID != "petition-002" {
		t.Fatalf("expected petition_id %q, got %q", "petition-002", got.PetitionID)
	}
	if got.VersionHash != hash2 {
		t.Fatalf("expected head version hash %q, got %q", hash2, got.VersionHash)
	}
}
