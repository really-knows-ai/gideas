package sqlite

import (
	"context"
	"testing"
)

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
		Group:      testGroupSecurity,
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
	if got.Group != testGroupSecurity {
		t.Fatalf("expected group %q, got %q", testGroupSecurity, got.Group)
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
