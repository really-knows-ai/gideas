package sqlite

import (
	"context"
	"testing"
)

const testDivisionSecurity = "security"

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(":memory:")
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
