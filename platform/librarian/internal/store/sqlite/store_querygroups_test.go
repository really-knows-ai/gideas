package sqlite

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// LawGroup QueryLaws filter tests
// ---------------------------------------------------------------------------

func TestQueryLaws_GroupFilterByGroup(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Security group.
	if _, err := s.CreateLaw(ctx, testLawSecID, Law{
		Goal:            "Security rule",
		Tier:            1,
		Group:           "security",
		Representations: []Representation{{Type: "text/plain", Content: "s"}},
	}); err != nil {
		t.Fatalf("CreateLaw %s: %v", testLawSecID, err)
	}
	// A law in the "default" group (empty Group field).
	if _, err := s.CreateLaw(ctx, "law-def", Law{
		Goal:            "Default rule",
		Tier:            1,
		Representations: []Representation{{Type: "text/plain", Content: "d"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-def: %v", err)
	}

	// Filter by security group.
	laws, err := s.QueryLaws(ctx, QueryFilter{Group: "security"})
	if err != nil {
		t.Fatalf("QueryLaws group=security: %v", err)
	}
	if len(laws) != 1 || laws[0].ID != testLawSecID {
		t.Fatalf("expected [%s], got %v", testLawSecID, lawIDs(laws))
	}

	// Filter by non-existent group returns empty.
	laws, err = s.QueryLaws(ctx, QueryFilter{Group: "nonexistent"})
	if err != nil {
		t.Fatalf("QueryLaws group=nonexistent: %v", err)
	}
	if len(laws) != 0 {
		t.Fatalf("expected 0 laws, got %d", len(laws))
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
		Goal: "Security scoped", Tier: 1, Group: "security", AppliesTo: []string{"source-code"},
		Representations: []Representation{{Type: "text/plain", Content: "ss"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-ss: %v", err)
	}
	if _, err := s.CreateLaw(ctx, "law-sg", Law{
		Goal: "Security global", Tier: 1, Group: "security",
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

func TestQueryLaws_GroupFilterCombined(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateLaw(ctx, "law-sec", Law{
		Goal: "Security rule", Tier: 1, Group: "security",
		Representations: []Representation{{Type: "text/plain", Content: "s"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-sec: %v", err)
	}
	if _, err := s.CreateLaw(ctx, "law-arch", Law{
		Goal: "Arch rule", Tier: 1, Group: "architecture",
		Representations: []Representation{{Type: "text/plain", Content: "a"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-arch: %v", err)
	}

	// Filter by security group — only law-sec should match.
	laws, err := s.QueryLaws(ctx, QueryFilter{Group: "security"})
	if err != nil {
		t.Fatalf("QueryLaws: %v", err)
	}
	if len(laws) != 1 || laws[0].ID != "law-sec" {
		t.Fatalf("expected [law-sec], got %v", lawIDs(laws))
	}

	// Filter by architecture group — only law-arch should match.
	laws, err = s.QueryLaws(ctx, QueryFilter{Group: "architecture"})
	if err != nil {
		t.Fatalf("QueryLaws: %v", err)
	}
	if len(laws) != 1 || laws[0].ID != "law-arch" {
		t.Fatalf("expected [law-arch], got %v", lawIDs(laws))
	}
}

func TestContentHash_Deterministic(t *testing.T) {
	h1 := ComputeContentHash("goal", 1, []string{"b", "a"}, []Representation{
		{Type: "text/plain", Content: "content"},
		{Type: "application/rego", Content: "rule"},
	}, testGroupSecurity)
	h2 := ComputeContentHash("goal", 1, []string{"a", "b"}, []Representation{
		{Type: "application/rego", Content: "rule"},
		{Type: "text/plain", Content: "content"},
	}, testGroupSecurity)

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

func TestGroup_Persistence(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	law := Law{
		Goal:            "Security check",
		Tier:            1,
		Group:           testGroupSecurity,
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
	if got.Group != testGroupSecurity {
		t.Fatalf("expected group %q, got %q", testGroupSecurity, got.Group)
	}
}

func TestGroup_EmptyDefault(t *testing.T) {
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
	if got.Group != "" {
		t.Fatalf("expected empty group, got %q", got.Group)
	}
}

func TestQueryLaws_GroupFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Security group.
	if _, err := s.CreateLaw(ctx, "law-sec", Law{
		Goal: "Security rule", Tier: 1, Group: testGroupSecurity,
		Representations: []Representation{{Type: "text/plain", Content: "s"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-sec: %v", err)
	}
	// Architecture group.
	if _, err := s.CreateLaw(ctx, "law-arch", Law{
		Goal: "Architecture rule", Tier: 1, Group: "architecture",
		Representations: []Representation{{Type: "text/plain", Content: "a"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-arch: %v", err)
	}
	// No group (general).
	if _, err := s.CreateLaw(ctx, "law-gen", Law{
		Goal: "General rule", Tier: 1,
		Representations: []Representation{{Type: "text/plain", Content: "g"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-gen: %v", err)
	}

	// Filter by security group.
	laws, err := s.QueryLaws(ctx, QueryFilter{Group: testGroupSecurity})
	if err != nil {
		t.Fatalf("QueryLaws group=security: %v", err)
	}
	if len(laws) != 1 || laws[0].ID != "law-sec" {
		t.Fatalf("expected [law-sec], got %v", lawIDs(laws))
	}

	// Empty group filter returns all.
	laws, err = s.QueryLaws(ctx, QueryFilter{})
	if err != nil {
		t.Fatalf("QueryLaws no filter: %v", err)
	}
	if len(laws) != 3 {
		t.Fatalf("expected 3 laws, got %d", len(laws))
	}
}

func TestQueryLaws_GroupWithArtefactFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Security + scoped to source-code.
	if _, err := s.CreateLaw(ctx, "law-ss", Law{
		Goal: "Security scoped", Tier: 1, Group: testGroupSecurity, AppliesTo: []string{"source-code"},
		Representations: []Representation{{Type: "text/plain", Content: "ss"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-ss: %v", err)
	}
	// Architecture + scoped to source-code.
	if _, err := s.CreateLaw(ctx, "law-as", Law{
		Goal: "Arch scoped", Tier: 1, Group: "architecture", AppliesTo: []string{"source-code"},
		Representations: []Representation{{Type: "text/plain", Content: "as"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-as: %v", err)
	}
	// Security + global.
	if _, err := s.CreateLaw(ctx, "law-sg", Law{
		Goal: "Security global", Tier: 1, Group: testGroupSecurity,
		Representations: []Representation{{Type: "text/plain", Content: "sg"}},
	}); err != nil {
		t.Fatalf("CreateLaw law-sg: %v", err)
	}

	// Filter: artefact=source-code + group=security.
	laws, err := s.QueryLaws(ctx, QueryFilter{GovernedArtefact: "source-code", Group: testGroupSecurity})
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

func TestContentHash_GroupChangesHash(t *testing.T) {
	h1 := ComputeContentHash("goal", 1, nil, []Representation{{Type: "text/plain", Content: "c"}}, "")
	h2 := ComputeContentHash("goal", 1, nil, []Representation{{Type: "text/plain", Content: "c"}}, testGroupSecurity)

	if h1 == h2 {
		t.Fatal("different groups should produce different hashes")
	}
}

func TestUpdateLaw_GroupChange(t *testing.T) {
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

	// Change only group.
	law.Group = testGroupSecurity
	hash2, err := s.UpdateLaw(ctx, "law-upd", law)
	if err != nil {
		t.Fatalf("UpdateLaw: %v", err)
	}

	if hash1 == hash2 {
		t.Fatal("changing group should produce a new version hash")
	}

	got, err := s.GetLaw(ctx, "law-upd")
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	if got.Group != testGroupSecurity {
		t.Fatalf("expected group %q, got %q", testGroupSecurity, got.Group)
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
