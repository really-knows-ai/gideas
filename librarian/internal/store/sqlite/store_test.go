package sqlite

import (
	"context"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
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
	s.CreateLaw(ctx, "law-a", Law{Goal: "A", Tier: 1, Representations: []Representation{{Type: "text/plain", Content: "a"}}})
	s.CreateLaw(ctx, "law-b", Law{Goal: "B", Tier: 1, Representations: []Representation{{Type: "text/plain", Content: "b"}}})
	s.CreateLawInactive(ctx, "law-c", Law{Goal: "C", Tier: 2, Representations: []Representation{{Type: "text/plain", Content: "c"}}})

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
	s.CreateLaw(ctx, "law-scoped", Law{
		Goal:            "Scoped",
		Tier:            1,
		AppliesTo:       []string{"source-code"},
		Representations: []Representation{{Type: "text/plain", Content: "scoped"}},
	})
	// Global law (no appliesTo).
	s.CreateLaw(ctx, "law-global", Law{
		Goal:            "Global",
		Tier:            1,
		Representations: []Representation{{Type: "text/plain", Content: "global"}},
	})
	// Different scope.
	s.CreateLaw(ctx, "law-other", Law{
		Goal:            "Other",
		Tier:            1,
		AppliesTo:       []string{"docs"},
		Representations: []Representation{{Type: "text/plain", Content: "other"}},
	})

	laws, err := s.QueryLaws(ctx, QueryFilter{ArtefactKind: "source-code"})
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
	s.CreateLaw(ctx, "law-md", Law{
		Goal:      "Markdown law",
		Tier:      1,
		AppliesTo: []string{"docs"},
		Representations: []Representation{
			{Type: "text/markdown", Content: "# Rule"},
			{Type: "text/plain", Content: "Rule in plain text"},
		},
	})
	// Law without markdown.
	s.CreateLaw(ctx, "law-plain", Law{
		Goal:            "Plain law",
		Tier:            1,
		AppliesTo:       []string{"docs"},
		Representations: []Representation{{Type: "text/plain", Content: "Plain only"}},
	})

	laws, err := s.QueryLaws(ctx, QueryFilter{
		ArtefactKind:       "docs",
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

	s.CreateLaw(ctx, "law-retire", Law{
		Goal:            "To be retired",
		Tier:            1,
		Representations: []Representation{{Type: "text/plain", Content: "old"}},
	})

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

	s.CreateLawInactive(ctx, "law-pending", Law{
		Goal:            "Pending",
		Tier:            2,
		Representations: []Representation{{Type: "text/plain", Content: "pending"}},
	})

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
	s.SetEmbedding(ctx, "law-e1", hash1, []float32{1.0, 0.0, 0.0})

	hash2, _ := s.CreateLaw(ctx, "law-e2", Law{
		Goal:            "Law 2",
		Tier:            1,
		Representations: []Representation{{Type: "text/plain", Content: "l2"}},
	})
	s.SetEmbedding(ctx, "law-e2", hash2, []float32{0.0, 1.0, 0.0})

	// Inactive law with embedding — should NOT appear.
	hash3, _ := s.CreateLawInactive(ctx, "law-e3", Law{
		Goal:            "Law 3",
		Tier:            2,
		Representations: []Representation{{Type: "text/plain", Content: "l3"}},
	})
	s.SetEmbedding(ctx, "law-e3", hash3, []float32{0.0, 0.0, 1.0})

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
	})
	h2 := ComputeContentHash("goal", 1, []string{"a", "b"}, []Representation{
		{Type: "application/rego", Content: "rule"},
		{Type: "text/plain", Content: "content"},
	})

	if h1 != h2 {
		t.Fatalf("content hash should be deterministic regardless of field ordering, got %q and %q", h1, h2)
	}
}

func TestContentHash_DifferentContent(t *testing.T) {
	h1 := ComputeContentHash("goal A", 1, nil, []Representation{{Type: "text/plain", Content: "a"}})
	h2 := ComputeContentHash("goal B", 1, nil, []Representation{{Type: "text/plain", Content: "a"}})

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

	s.CreateLaw(ctx, "law-tier", Law{
		Goal:            "Tier test",
		Tier:            1,
		Representations: []Representation{{Type: "text/plain", Content: "tier"}},
	})

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

	s.CreateLaw(ctx, "law-s1", Law{
		Goal:            "Scoped A",
		Tier:            1,
		AppliesTo:       []string{"source-code", "docs"},
		Representations: []Representation{{Type: "text/plain", Content: "a"}},
	})
	s.CreateLaw(ctx, "law-s2", Law{
		Goal:            "Global",
		Tier:            1,
		Representations: []Representation{{Type: "text/plain", Content: "g"}},
	})
	s.CreateLaw(ctx, "law-s3", Law{
		Goal:            "Scoped B",
		Tier:            1,
		AppliesTo:       []string{"images"},
		Representations: []Representation{{Type: "text/plain", Content: "b"}},
	})

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
