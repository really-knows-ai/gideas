package service

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// ReplicateLaws
// ---------------------------------------------------------------------------

func TestReplicateLaws_Basic(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.ReplicateLaws(ctx, &flowv1.ReplicateLawsRequest{
		Laws: []*flowv1.Law{
			{
				Id:   "rep-law-1",
				Goal: "Replicated law",
				Tier: flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION,
				Representations: []*flowv1.Representation{
					{Type: "text/plain", Content: "replicated content"},
				},
			},
		},
		SourceFlowNamespace: "authority-ns",
	})
	if err != nil {
		t.Fatalf("ReplicateLaws: %v", err)
	}
	if len(resp.GetIntegrationResults()) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.GetIntegrationResults()))
	}
	if !resp.GetIntegrationResults()[0].GetAccepted() {
		t.Fatalf("expected accepted, got: %s", resp.GetIntegrationResults()[0].GetConflictReason())
	}

	// Verify the law was stored.
	getLawResp, err := srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: "rep-law-1"})
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	if getLawResp.GetLaw().GetGoal() != "Replicated law" {
		t.Fatalf("expected goal %q, got %q", "Replicated law", getLawResp.GetLaw().GetGoal())
	}
	if getLawResp.GetLaw().GetTier() != flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION {
		t.Fatalf("expected tier 4, got %v", getLawResp.GetLaw().GetTier())
	}
}

func TestReplicateLaws_EmptyLaws(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.ReplicateLaws(ctx, &flowv1.ReplicateLawsRequest{})
	if err != nil {
		t.Fatalf("ReplicateLaws: %v", err)
	}
	if len(resp.GetIntegrationResults()) != 0 {
		t.Fatalf("expected 0 results, got %d", len(resp.GetIntegrationResults()))
	}
}

func TestReplicateLaws_MissingID(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.ReplicateLaws(ctx, &flowv1.ReplicateLawsRequest{
		Laws: []*flowv1.Law{
			{
				Goal: "No ID",
				Tier: flowv1.LawTier_LAW_TIER_RULING,
				Representations: []*flowv1.Representation{
					{Type: "text/plain", Content: "x"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ReplicateLaws: %v", err)
	}
	if len(resp.GetIntegrationResults()) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.GetIntegrationResults()))
	}
	if resp.GetIntegrationResults()[0].GetAccepted() {
		t.Fatal("expected rejected for missing ID")
	}
}

func TestReplicateLaws_RetainsProvenance(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.ReplicateLaws(ctx, &flowv1.ReplicateLawsRequest{
		Laws: []*flowv1.Law{
			{
				Id:   "prov-law-1",
				Goal: "Provenance test law",
				Tier: flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION,
				Representations: []*flowv1.Representation{
					{Type: "text/plain", Content: "provenance content"},
				},
				Group: "security",
			},
		},
		SourceFlowNamespace: "authority-flow-ns",
		PetitionId:          "petition-xyz",
	})
	if err != nil {
		t.Fatalf("ReplicateLaws: %v", err)
	}
	if !resp.GetIntegrationResults()[0].GetAccepted() {
		t.Fatalf("expected accepted, got: %s", resp.GetIntegrationResults()[0].GetConflictReason())
	}

	// Verify provenance is stored by reading directly from the store.
	law, err := srv.store.GetLaw(ctx, "prov-law-1")
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	if law.SourceFlow != "authority-flow-ns" {
		t.Fatalf("expected SourceFlow %q, got %q", "authority-flow-ns", law.SourceFlow)
	}
	if law.PetitionID != "petition-xyz" {
		t.Fatalf("expected PetitionID %q, got %q", "petition-xyz", law.PetitionID)
	}
}

func TestReplicateLaws_DuplicateLawID(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// First replication.
	resp, err := srv.ReplicateLaws(ctx, &flowv1.ReplicateLawsRequest{
		Laws: []*flowv1.Law{
			{
				Id:   "dup-law-1",
				Goal: "Original goal",
				Tier: flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION,
				Representations: []*flowv1.Representation{
					{Type: "text/plain", Content: "original content"},
				},
			},
		},
		SourceFlowNamespace: "flow-a",
	})
	if err != nil {
		t.Fatalf("ReplicateLaws (first): %v", err)
	}
	if !resp.GetIntegrationResults()[0].GetAccepted() {
		t.Fatalf("expected accepted (first), got: %s", resp.GetIntegrationResults()[0].GetConflictReason())
	}

	// Second replication with same ID but updated content.
	resp, err = srv.ReplicateLaws(ctx, &flowv1.ReplicateLawsRequest{
		Laws: []*flowv1.Law{
			{
				Id:   "dup-law-1",
				Goal: "Updated goal",
				Tier: flowv1.LawTier_LAW_TIER_FEDERAL_ACCORD,
				Representations: []*flowv1.Representation{
					{Type: "text/plain", Content: "updated content"},
				},
			},
		},
		SourceFlowNamespace: "flow-b",
		PetitionId:          "petition-update",
	})
	if err != nil {
		t.Fatalf("ReplicateLaws (second): %v", err)
	}
	if !resp.GetIntegrationResults()[0].GetAccepted() {
		t.Fatalf("expected accepted (update), got: %s", resp.GetIntegrationResults()[0].GetConflictReason())
	}

	// Verify the law was updated.
	law, err := srv.store.GetLaw(ctx, "dup-law-1")
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	if law.Goal != "Updated goal" {
		t.Fatalf("expected goal %q, got %q", "Updated goal", law.Goal)
	}
	if law.Tier != 5 {
		t.Fatalf("expected tier 5, got %d", law.Tier)
	}
	// Provenance should reflect the latest replication.
	if law.SourceFlow != "flow-b" {
		t.Fatalf("expected SourceFlow %q, got %q", "flow-b", law.SourceFlow)
	}
	if law.PetitionID != "petition-update" {
		t.Fatalf("expected PetitionID %q, got %q", "petition-update", law.PetitionID)
	}
}

func TestReplicateLaws_MultipleResults(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.ReplicateLaws(ctx, &flowv1.ReplicateLawsRequest{
		Laws: []*flowv1.Law{
			{
				Id:   "multi-1",
				Goal: "First law",
				Tier: flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION,
			},
			{
				// Missing ID — should fail.
				Goal: "No ID law",
				Tier: flowv1.LawTier_LAW_TIER_RULING,
			},
			{
				Id:   "multi-2",
				Goal: "Third law",
				Tier: flowv1.LawTier_LAW_TIER_FEDERAL_ACCORD,
			},
		},
		SourceFlowNamespace: "multi-ns",
	})
	if err != nil {
		t.Fatalf("ReplicateLaws: %v", err)
	}
	if len(resp.GetIntegrationResults()) != 3 {
		t.Fatalf("expected 3 results, got %d", len(resp.GetIntegrationResults()))
	}
	// First: accepted.
	if !resp.GetIntegrationResults()[0].GetAccepted() {
		t.Fatalf("expected result[0] accepted, got: %s", resp.GetIntegrationResults()[0].GetConflictReason())
	}
	// Second: rejected (missing ID).
	if resp.GetIntegrationResults()[1].GetAccepted() {
		t.Fatal("expected result[1] rejected for missing ID")
	}
	if resp.GetIntegrationResults()[1].GetConflictReason() == "" {
		t.Fatal("expected conflict_reason for missing ID")
	}
	// Third: accepted.
	if !resp.GetIntegrationResults()[2].GetAccepted() {
		t.Fatalf("expected result[2] accepted, got: %s", resp.GetIntegrationResults()[2].GetConflictReason())
	}
}

func TestReplicateLaws_StoresVecEmbedding(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	resp, err := srv.ReplicateLaws(ctx, &flowv1.ReplicateLawsRequest{
		Laws: []*flowv1.Law{
			{
				Id:   "replicated-law-1",
				Goal: "Replicated law from authority",
				Tier: flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION,
				Representations: []*flowv1.Representation{
					{Type: "text/plain", Content: "state law content"},
				},
				AppliesTo: []string{"source-code"},
			},
		},
		SourceFlowNamespace: "authority-flow-ns",
	})
	if err != nil {
		t.Fatalf("ReplicateLaws: %v", err)
	}

	// Check the result was successful.
	if len(resp.GetIntegrationResults()) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.GetIntegrationResults()))
	}
	if !resp.GetIntegrationResults()[0].GetAccepted() {
		t.Fatalf("expected accepted, got conflict: %s", resp.GetIntegrationResults()[0].GetConflictReason())
	}

	// Verify vec embedding was stored.
	if !vecEmbeddingStored(t, srv, "replicated-law-1", "Replicated law from authority") {
		t.Fatal("expected vec embedding to be stored after ReplicateLaws")
	}
}
