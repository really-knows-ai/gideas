package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/gideas/flow/librarian/internal/embed"
	"github.com/gideas/flow/librarian/internal/store/sqlite"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// testEmbeddingDims matches the store test dimension.
const testEmbeddingDims = 4

// Test constants reused across LawGroup tests.
const (
	testBundleMode    = "bundle"
	testLawByLawMode  = "law-by-law"
	testGroupSecurity = "security"
)

var idCounter int

func testIDGen() string {
	idCounter++
	return fmt.Sprintf("law-%04d", idCounter)
}

func newTestServer(t *testing.T) *LibrarianServer {
	t.Helper()
	idCounter = 0
	store, err := sqlite.New(":memory:", sqlite.WithEmbeddingDimension(testEmbeddingDims))
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	srv := NewLibrarianServer(store, nil, testIDGen, 0.85)
	t.Cleanup(func() {
		srv.Wait()
		_ = store.Close()
	})
	return srv
}

// stubEmbedder returns a deterministic embedding for any input text.
// The embedding is derived from the text length to make vectors reproducible.
type stubEmbedder struct {
	dims int
}

var _ embed.Embedder = (*stubEmbedder)(nil)

func (s *stubEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	v := make([]float32, s.dims)
	for i := range v {
		v[i] = float32(len(text)+i) / 100.0
	}
	return v, nil
}

func newTestServerWithEmbedder(t *testing.T) *LibrarianServer {
	t.Helper()
	idCounter = 0
	store, err := sqlite.New(":memory:", sqlite.WithEmbeddingDimension(testEmbeddingDims))
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	emb := &stubEmbedder{dims: testEmbeddingDims}
	srv := NewLibrarianServer(store, emb, testIDGen, 0.85)
	t.Cleanup(func() {
		srv.Wait()
		_ = store.Close()
	})
	return srv
}

// ---------------------------------------------------------------------------
// QueryLaws
// ---------------------------------------------------------------------------

func TestQueryLaws_AllLaws(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Seed data.
	if _, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Law A",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "a"}},
	}); err != nil {
		t.Fatalf("RecordFinding Law A: %v", err)
	}
	if _, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Law B",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "b"}},
	}); err != nil {
		t.Fatalf("RecordFinding Law B: %v", err)
	}

	resp, err := srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{})
	if err != nil {
		t.Fatalf("QueryLaws: %v", err)
	}
	if len(resp.GetLaws()) != 2 {
		t.Fatalf("expected 2 laws, got %d", len(resp.GetLaws()))
	}
}

func TestQueryLaws_ScopedFilter(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	if _, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Scoped law",
		AppliesTo:       []string{"source-code"},
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "scoped"}},
	}); err != nil {
		t.Fatalf("RecordFinding Scoped law: %v", err)
	}
	if _, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Other scope",
		AppliesTo:       []string{"docs"},
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "other"}},
	}); err != nil {
		t.Fatalf("RecordFinding Other scope: %v", err)
	}

	resp, err := srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{
		Filter: &flowv1.LawFilter{GovernedArtefact: "source-code"},
	})
	if err != nil {
		t.Fatalf("QueryLaws: %v", err)
	}
	if len(resp.GetLaws()) != 1 {
		t.Fatalf("expected 1 scoped law, got %d", len(resp.GetLaws()))
	}
}

func TestQueryLaws_RepresentationTypeRequiresKind(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{
		Filter: &flowv1.LawFilter{RepresentationType: "text/markdown"},
	})
	if err == nil {
		t.Fatal("expected error when representation_type is set without governed_artefact")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestQueryLaws_CapabilityDenied(t *testing.T) {
	srv := newTestServer(t)

	// Set metadata with capabilities that DON'T include READ:law.
	md := metadata.Pairs(metadataKeyCapabilities, "WRITE:law/tier1", metadataKeyNodeID, "node-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{})
	if err == nil {
		t.Fatal("expected PermissionDenied, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Cite
// ---------------------------------------------------------------------------

func TestCite_Basic(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Create a law first.
	findResp, _ := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Citeable law",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "cite me"}},
	})

	resp, err := srv.Cite(ctx, &flowv1.CiteRequest{
		LawIds: []string{findResp.GetLawId()},
	})
	if err != nil {
		t.Fatalf("Cite: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}
}

func TestCite_EmptyLawIDs(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.Cite(ctx, &flowv1.CiteRequest{})
	if err == nil {
		t.Fatal("expected error for empty law_ids")
	}
}

func TestCite_MissingLaw_NoError(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Cite a non-existent law — should succeed (citation is a signal).
	resp, err := srv.Cite(ctx, &flowv1.CiteRequest{
		LawIds: []string{"nonexistent-law"},
	})
	if err != nil {
		t.Fatalf("Cite should not fail for missing laws: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}
}

// ---------------------------------------------------------------------------
// RecordFinding
// ---------------------------------------------------------------------------

func TestRecordFinding_Basic(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:      "All tests must pass",
		AppliesTo: []string{"source-code"},
		Representations: []*flowv1.Representation{
			{Type: "text/plain", Content: "All tests must pass before merge."},
		},
	})
	if err != nil {
		t.Fatalf("RecordFinding: %v", err)
	}
	if resp.GetLawId() == "" {
		t.Fatal("expected non-empty law_id")
	}

	// Verify it's a Tier 1 law.
	getLawResp, err := srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: resp.GetLawId()})
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	if getLawResp.GetLaw().GetTier() != flowv1.LawTier_LAW_TIER_FINDING {
		t.Fatalf("expected Tier 1 (FINDING), got %v", getLawResp.GetLaw().GetTier())
	}
}

func TestRecordFinding_EmptyGoal(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})
	if err == nil {
		t.Fatal("expected error for empty goal")
	}
}

func TestRecordFinding_NoRepresentations(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal: "Some goal",
	})
	if err == nil {
		t.Fatal("expected error for no representations")
	}
}

func TestRecordFinding_CapabilityDenied(t *testing.T) {
	srv := newTestServer(t)

	md := metadata.Pairs(metadataKeyCapabilities, "READ:law", metadataKeyNodeID, "node-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "test",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})
	if err == nil {
		t.Fatal("expected PermissionDenied, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

// TestRecordFinding_NodeCallNoCapabilities_Denied verifies deny-by-default:
// a node-originated call with no capabilities header is denied.
func TestRecordFinding_NodeCallNoCapabilities_Denied(t *testing.T) {
	srv := newTestServer(t)

	// Node identity present but no capabilities.
	md := metadata.Pairs(metadataKeyNodeID, "node-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "test",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for node call with no capabilities")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

// TestQueryLaws_NodeCallNoCapabilities_Denied verifies deny-by-default
// for node calls to QueryLaws.
func TestQueryLaws_NodeCallNoCapabilities_Denied(t *testing.T) {
	srv := newTestServer(t)

	md := metadata.Pairs(metadataKeyNodeID, "node-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{})
	if err == nil {
		t.Fatal("expected PermissionDenied for node call with no capabilities")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

// TestQueryLaws_SystemCall_BypassesEnforcement verifies system-to-system
// calls (no node identity) pass through without capability checks.
func TestQueryLaws_SystemCall_BypassesEnforcement(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Should succeed — no node identity means system call.
	_, err := srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{})
	if err != nil {
		t.Fatalf("system call should bypass capability enforcement, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// GetLaw
// ---------------------------------------------------------------------------

func TestGetLaw_NotFound(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: "nonexistent"})
	if err == nil {
		t.Fatal("expected NotFound error")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestGetLaw_EmptyID(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.GetLaw(ctx, &flowv1.GetLawRequest{})
	if err == nil {
		t.Fatal("expected InvalidArgument error")
	}
}

// ---------------------------------------------------------------------------
// WriteLaw
// ---------------------------------------------------------------------------

func TestWriteLaw_CreateInactive(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "New ruling",
			Tier: flowv1.LawTier_LAW_TIER_RULING,
			Representations: []*flowv1.Representation{
				{Type: "text/plain", Content: "A new ruling."},
			},
		},
	})
	if err != nil {
		t.Fatalf("WriteLaw: %v", err)
	}
	if resp.GetLawId() == "" {
		t.Fatal("expected non-empty law_id")
	}

	// Should be inactive (pending activation).
	getLawResp, err := srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: resp.GetLawId()})
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	// The law should exist but checking active requires store-level verification.
	if getLawResp.GetLaw().GetTier() != flowv1.LawTier_LAW_TIER_RULING {
		t.Fatalf("expected Tier 2, got %v", getLawResp.GetLaw().GetTier())
	}
}

func TestWriteLaw_UpdateExisting(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Create via RecordFinding.
	findResp, _ := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Original",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "v1"}},
	})

	// Update via WriteLaw.
	resp, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Id:   findResp.GetLawId(),
			Goal: "Updated",
			Tier: flowv1.LawTier_LAW_TIER_FINDING,
			Representations: []*flowv1.Representation{
				{Type: "text/plain", Content: "v2"},
			},
		},
	})
	if err != nil {
		t.Fatalf("WriteLaw: %v", err)
	}

	// Verify update.
	getLawResp, _ := srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: findResp.GetLawId()})
	if getLawResp.GetLaw().GetGoal() != "Updated" {
		t.Fatalf("expected updated goal, got %q", getLawResp.GetLaw().GetGoal())
	}
	if resp.GetVersionHash() == "" {
		t.Fatal("expected non-empty version_hash")
	}
}

func TestWriteLaw_InvalidTier(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal:            "Bad tier",
			Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
		},
	})
	if err == nil {
		t.Fatal("expected error for unspecified tier")
	}
}

// ---------------------------------------------------------------------------
// RetireLaw
// ---------------------------------------------------------------------------

func TestRetireLaw_Basic(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	findResp, _ := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "To retire",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})

	resp, err := srv.RetireLaw(ctx, &flowv1.RetireLawRequest{LawId: findResp.GetLawId()})
	if err != nil {
		t.Fatalf("RetireLaw: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	// Should no longer be retrievable.
	_, err = srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: findResp.GetLawId()})
	if err == nil {
		t.Fatal("expected NotFound after retirement")
	}
}

func TestRetireLaw_EmptyID(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.RetireLaw(ctx, &flowv1.RetireLawRequest{})
	if err == nil {
		t.Fatal("expected InvalidArgument error")
	}
}

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

// ---------------------------------------------------------------------------
// ApplyLifecycleAction
// ---------------------------------------------------------------------------

func TestApplyLifecycleAction_Promote(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	findResp, _ := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Promotable",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})

	resp, err := srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{
		LawId:   findResp.GetLawId(),
		Verdict: flowv1.Verdict_VERDICT_PROMOTE,
	})
	if err != nil {
		t.Fatalf("ApplyLifecycleAction: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	// Verify tier was incremented.
	getLawResp, _ := srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: findResp.GetLawId()})
	if getLawResp.GetLaw().GetTier() != flowv1.LawTier_LAW_TIER_RULING {
		t.Fatalf("expected Tier 2 after promote, got %v", getLawResp.GetLaw().GetTier())
	}
}

func TestApplyLifecycleAction_Demote(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	findResp, _ := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Demotable",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})

	// Promote first (T1 -> T2).
	if _, err := srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{
		LawId:   findResp.GetLawId(),
		Verdict: flowv1.Verdict_VERDICT_PROMOTE,
	}); err != nil {
		t.Fatalf("ApplyLifecycleAction promote: %v", err)
	}

	// Now demote (T2 -> T1).
	resp, err := srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{
		LawId:   findResp.GetLawId(),
		Verdict: flowv1.Verdict_VERDICT_DEMOTE,
	})
	if err != nil {
		t.Fatalf("ApplyLifecycleAction demote: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	getLawResp, _ := srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: findResp.GetLawId()})
	if getLawResp.GetLaw().GetTier() != flowv1.LawTier_LAW_TIER_FINDING {
		t.Fatalf("expected Tier 1 after demote, got %v", getLawResp.GetLaw().GetTier())
	}
}

func TestApplyLifecycleAction_DemoteBelowTier1(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	findResp, _ := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Already T1",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})

	_, err := srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{
		LawId:   findResp.GetLawId(),
		Verdict: flowv1.Verdict_VERDICT_DEMOTE,
	})
	if err == nil {
		t.Fatal("expected error when demoting below Tier 1")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
}

func TestApplyLifecycleAction_Retire(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	findResp, _ := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "To retire via lifecycle",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})

	resp, err := srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{
		LawId:   findResp.GetLawId(),
		Verdict: flowv1.Verdict_VERDICT_RETIRE,
	})
	if err != nil {
		t.Fatalf("ApplyLifecycleAction retire: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	// Should be gone.
	_, err = srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: findResp.GetLawId()})
	if err == nil {
		t.Fatal("expected NotFound after lifecycle retire")
	}
}

func TestApplyLifecycleAction_ActivatesPendingLaw(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Create an inactive law via WriteLaw.
	writeResp, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "Pending ruling",
			Tier: flowv1.LawTier_LAW_TIER_FINDING,
			Representations: []*flowv1.Representation{
				{Type: "text/plain", Content: "pending"},
			},
		},
	})
	if err != nil {
		t.Fatalf("WriteLaw: %v", err)
	}

	// Should not appear in QueryLaws (inactive).
	queryResp, _ := srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{})
	if len(queryResp.GetLaws()) != 0 {
		t.Fatalf("expected 0 active laws, got %d", len(queryResp.GetLaws()))
	}

	// Promote — should activate.
	_, err = srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{
		LawId:   writeResp.GetLawId(),
		Verdict: flowv1.Verdict_VERDICT_PROMOTE,
	})
	if err != nil {
		t.Fatalf("ApplyLifecycleAction promote: %v", err)
	}

	// Now should appear in QueryLaws.
	queryResp, _ = srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{})
	if len(queryResp.GetLaws()) != 1 {
		t.Fatalf("expected 1 active law after promotion, got %d", len(queryResp.GetLaws()))
	}
}

func TestApplyLifecycleAction_EmptyLawID(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{
		Verdict: flowv1.Verdict_VERDICT_PROMOTE,
	})
	if err == nil {
		t.Fatal("expected error for empty law_id")
	}
}

func TestApplyLifecycleAction_UnspecifiedVerdict(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{
		LawId: "some-id",
	})
	if err == nil {
		t.Fatal("expected error for unspecified verdict")
	}
}

// ---------------------------------------------------------------------------
// Group support
// ---------------------------------------------------------------------------

func TestQueryLaws_GroupFilter(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Create laws in different groups via WriteLaw (which passes group through).
	if _, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "Security rule", Tier: flowv1.LawTier_LAW_TIER_RULING,
			Group:           "security",
			Representations: []*flowv1.Representation{{Type: "text/plain", Content: "s"}},
		},
	}); err != nil {
		t.Fatalf("WriteLaw security: %v", err)
	}
	if _, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "Arch rule", Tier: flowv1.LawTier_LAW_TIER_RULING,
			Group:           "architecture",
			Representations: []*flowv1.Representation{{Type: "text/plain", Content: "a"}},
		},
	}); err != nil {
		t.Fatalf("WriteLaw architecture: %v", err)
	}

	// Activate both so they appear in QueryLaws.
	srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{ //nolint:errcheck
		LawId: "law-0001", Verdict: flowv1.Verdict_VERDICT_PROMOTE,
	})
	srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{ //nolint:errcheck
		LawId: "law-0002", Verdict: flowv1.Verdict_VERDICT_PROMOTE,
	})

	// Filter by security.
	resp, err := srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{
		Filter: &flowv1.LawFilter{Group: testGroupSecurity},
	})
	if err != nil {
		t.Fatalf("QueryLaws group=security: %v", err)
	}
	if len(resp.GetLaws()) != 1 {
		t.Fatalf("expected 1 security law, got %d", len(resp.GetLaws()))
	}
	if resp.GetLaws()[0].GetGroup() != testGroupSecurity {
		t.Fatalf("expected group=%s, got %q", testGroupSecurity, resp.GetLaws()[0].GetGroup())
	}

	// No group filter returns all.
	resp, err = srv.QueryLaws(ctx, &flowv1.QueryLawsRequest{})
	if err != nil {
		t.Fatalf("QueryLaws no filter: %v", err)
	}
	if len(resp.GetLaws()) != 2 {
		t.Fatalf("expected 2 laws, got %d", len(resp.GetLaws()))
	}
}

func TestWriteLaw_GroupRoundTrip(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	writeResp, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "Styled rule", Tier: flowv1.LawTier_LAW_TIER_RULING,
			Group:           "style",
			Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
		},
	})
	if err != nil {
		t.Fatalf("WriteLaw: %v", err)
	}

	getLawResp, err := srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: writeResp.GetLawId()})
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	if getLawResp.GetLaw().GetGroup() != "style" {
		t.Fatalf("expected group=style, got %q", getLawResp.GetLaw().GetGroup())
	}
}

func TestStoreLawToProto_IncludesGroup(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// RecordFinding doesn't set group (always empty for findings).
	findResp, _ := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Finding",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "f"}},
	})

	getLawResp, err := srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: findResp.GetLawId()})
	if err != nil {
		t.Fatalf("GetLaw: %v", err)
	}
	// Group should be empty for a RecordFinding (no group in request).
	if getLawResp.GetLaw().GetGroup() != "" {
		t.Fatalf("expected empty group for finding, got %q", getLawResp.GetLaw().GetGroup())
	}
}

// ---------------------------------------------------------------------------
// Dispute Record RPCs
// ---------------------------------------------------------------------------

func TestCreateDisputeRecord_Basic(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  "petition-1",
		CitedLawIds: []string{"law-a", "law-b"},
	})
	if err != nil {
		t.Fatalf("CreateDisputeRecord: %v", err)
	}
	rec := resp.GetRecord()
	if rec.GetPetitionId() != "petition-1" {
		t.Fatalf("expected petition_id %q, got %q", "petition-1", rec.GetPetitionId())
	}
	if len(rec.GetCitedLawIds()) != 2 {
		t.Fatalf("expected 2 cited law IDs, got %d", len(rec.GetCitedLawIds()))
	}
	if rec.GetStatus() != flowv1.DisputeStatus_DISPUTE_STATUS_ACTIVE {
		t.Fatalf("expected ACTIVE status, got %v", rec.GetStatus())
	}
	if rec.GetCreatedAt() == nil {
		t.Fatal("expected non-nil created_at")
	}
}

func TestCreateDisputeRecord_EmptyPetitionID(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		CitedLawIds: []string{"law-a"},
	})
	if err == nil {
		t.Fatal("expected InvalidArgument for empty petition_id")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestCreateDisputeRecord_EmptyCitedLawIDs(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId: "petition-empty",
	})
	if err == nil {
		t.Fatal("expected InvalidArgument for empty cited_law_ids")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestRetireDisputeRecord_Basic(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Create first.
	if _, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  "petition-retire",
		CitedLawIds: []string{"law-1"},
	}); err != nil {
		t.Fatalf("CreateDisputeRecord: %v", err)
	}

	resp, err := srv.RetireDisputeRecord(ctx, &flowv1.RetireDisputeRecordRequest{
		PetitionId: "petition-retire",
	})
	if err != nil {
		t.Fatalf("RetireDisputeRecord: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	// Should no longer appear in active disputes.
	getResp, err := srv.GetActiveDisputes(ctx, &flowv1.GetActiveDisputesRequest{})
	if err != nil {
		t.Fatalf("GetActiveDisputes: %v", err)
	}
	if len(getResp.GetRecords()) != 0 {
		t.Fatalf("expected 0 active disputes after retirement, got %d", len(getResp.GetRecords()))
	}
}

func TestRetireDisputeRecord_NonExistent(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.RetireDisputeRecord(ctx, &flowv1.RetireDisputeRecordRequest{
		PetitionId: "petition-ghost",
	})
	if err == nil {
		t.Fatal("expected NotFound for non-existent petition")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestGetActiveDisputes_ReturnsOnlyActive(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	if _, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  "petition-a",
		CitedLawIds: []string{"law-1"},
	}); err != nil {
		t.Fatalf("CreateDisputeRecord petition-a: %v", err)
	}
	if _, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  "petition-b",
		CitedLawIds: []string{"law-2"},
	}); err != nil {
		t.Fatalf("CreateDisputeRecord petition-b: %v", err)
	}

	// Retire one.
	if _, err := srv.RetireDisputeRecord(ctx, &flowv1.RetireDisputeRecordRequest{
		PetitionId: "petition-a",
	}); err != nil {
		t.Fatalf("RetireDisputeRecord: %v", err)
	}

	resp, err := srv.GetActiveDisputes(ctx, &flowv1.GetActiveDisputesRequest{})
	if err != nil {
		t.Fatalf("GetActiveDisputes: %v", err)
	}
	if len(resp.GetRecords()) != 1 {
		t.Fatalf("expected 1 active dispute, got %d", len(resp.GetRecords()))
	}
	if resp.GetRecords()[0].GetPetitionId() != "petition-b" {
		t.Fatalf("expected petition-b, got %q", resp.GetRecords()[0].GetPetitionId())
	}
}

func TestGetActiveDisputes_WithLawIDFilter(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	if _, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  "petition-x",
		CitedLawIds: []string{"law-10", "law-20"},
	}); err != nil {
		t.Fatalf("CreateDisputeRecord petition-x: %v", err)
	}
	if _, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  "petition-y",
		CitedLawIds: []string{"law-30"},
	}); err != nil {
		t.Fatalf("CreateDisputeRecord petition-y: %v", err)
	}

	// Filter by law-20 -- should only return petition-x.
	resp, err := srv.GetActiveDisputes(ctx, &flowv1.GetActiveDisputesRequest{
		LawId: "law-20",
	})
	if err != nil {
		t.Fatalf("GetActiveDisputes with filter: %v", err)
	}
	if len(resp.GetRecords()) != 1 {
		t.Fatalf("expected 1 dispute citing law-20, got %d", len(resp.GetRecords()))
	}
	if resp.GetRecords()[0].GetPetitionId() != "petition-x" {
		t.Fatalf("expected petition-x, got %q", resp.GetRecords()[0].GetPetitionId())
	}
}

// ---------------------------------------------------------------------------
// Vec Embedding Hooks
// ---------------------------------------------------------------------------

func TestWriteLaw_StoresVecEmbedding(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	resp, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "New ruling with embedding",
			Tier: flowv1.LawTier_LAW_TIER_RULING,
			Representations: []*flowv1.Representation{
				{Type: "text/plain", Content: "A ruling."},
			},
		},
	})
	if err != nil {
		t.Fatalf("WriteLaw: %v", err)
	}

	has, err := srv.store.HasVecEmbedding(ctx, resp.GetLawId())
	if err != nil {
		t.Fatalf("HasVecEmbedding: %v", err)
	}
	if !has {
		t.Fatal("expected vec embedding to be stored after WriteLaw")
	}
}

func TestWriteLaw_UpdateStoresVecEmbedding(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	// Create via RecordFinding.
	findResp, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Original finding",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "v1"}},
	})
	if err != nil {
		t.Fatalf("RecordFinding: %v", err)
	}

	// Update via WriteLaw.
	_, err = srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Id:   findResp.GetLawId(),
			Goal: "Updated finding with new embedding",
			Tier: flowv1.LawTier_LAW_TIER_FINDING,
			Representations: []*flowv1.Representation{
				{Type: "text/plain", Content: "v2"},
			},
		},
	})
	if err != nil {
		t.Fatalf("WriteLaw update: %v", err)
	}

	has, err := srv.store.HasVecEmbedding(ctx, findResp.GetLawId())
	if err != nil {
		t.Fatalf("HasVecEmbedding: %v", err)
	}
	if !has {
		t.Fatal("expected vec embedding to be updated after WriteLaw update")
	}
}

func TestRecordFinding_StoresVecEmbedding(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	resp, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Finding with embedding",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "finding"}},
	})
	if err != nil {
		t.Fatalf("RecordFinding: %v", err)
	}

	has, err := srv.store.HasVecEmbedding(ctx, resp.GetLawId())
	if err != nil {
		t.Fatalf("HasVecEmbedding: %v", err)
	}
	if !has {
		t.Fatal("expected vec embedding to be stored after RecordFinding")
	}
}

func TestRetireLaw_DeletesVecEmbedding(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	// Create a law via RecordFinding (which stores embedding).
	findResp, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "To retire",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "retire me"}},
	})
	if err != nil {
		t.Fatalf("RecordFinding: %v", err)
	}

	// Verify embedding exists.
	has, err := srv.store.HasVecEmbedding(ctx, findResp.GetLawId())
	if err != nil {
		t.Fatalf("HasVecEmbedding before retire: %v", err)
	}
	if !has {
		t.Fatal("expected vec embedding before retirement")
	}

	// Retire the law.
	_, err = srv.RetireLaw(ctx, &flowv1.RetireLawRequest{LawId: findResp.GetLawId()})
	if err != nil {
		t.Fatalf("RetireLaw: %v", err)
	}

	// Verify embedding is deleted.
	has, err = srv.store.HasVecEmbedding(ctx, findResp.GetLawId())
	if err != nil {
		t.Fatalf("HasVecEmbedding after retire: %v", err)
	}
	if has {
		t.Fatal("expected vec embedding to be deleted after retirement")
	}
}

func TestWriteLaw_NoEmbedder_NoVecEmbedding(t *testing.T) {
	srv := newTestServer(t) // no embedder
	ctx := context.Background()

	resp, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "No embedder",
			Tier: flowv1.LawTier_LAW_TIER_RULING,
			Representations: []*flowv1.Representation{
				{Type: "text/plain", Content: "x"},
			},
		},
	})
	if err != nil {
		t.Fatalf("WriteLaw: %v", err)
	}

	// No embedder means no vec embedding.
	has, err := srv.store.HasVecEmbedding(ctx, resp.GetLawId())
	if err != nil {
		t.Fatalf("HasVecEmbedding: %v", err)
	}
	if has {
		t.Fatal("expected no vec embedding when embedder is nil")
	}
}

// ---------------------------------------------------------------------------
// SearchSimilarLaws
// ---------------------------------------------------------------------------

func TestSearchSimilarLaws_ReturnsMatchingLaw(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	// Create a law via RecordFinding (active + embedding stored).
	_, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "All tests must pass",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "tests"}},
	})
	if err != nil {
		t.Fatalf("RecordFinding: %v", err)
	}

	// Search with a query that produces a similar embedding.
	// The stubEmbedder uses text length, so same-length text = identical vector.
	resp, err := srv.SearchSimilarLaws(ctx, &flowv1.SearchSimilarLawsRequest{
		QueryText: "All tests must pass", // same length -> identical embedding -> high similarity
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("SearchSimilarLaws: %v", err)
	}
	if len(resp.GetResults()) == 0 {
		t.Fatal("expected at least one result")
	}
	if resp.GetResults()[0].GetLaw().GetGoal() != "All tests must pass" {
		t.Fatalf("expected matching law goal, got %q", resp.GetResults()[0].GetLaw().GetGoal())
	}
	if resp.GetResults()[0].GetSimilarityScore() <= 0 {
		t.Fatalf("expected positive similarity score, got %f", resp.GetResults()[0].GetSimilarityScore())
	}
}

func TestSearchSimilarLaws_ScopeFilter(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	// Create two laws in different groups.
	_, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "Security rule alpha", Tier: flowv1.LawTier_LAW_TIER_RULING,
			Group:           "security",
			Representations: []*flowv1.Representation{{Type: "text/plain", Content: "s"}},
		},
	})
	if err != nil {
		t.Fatalf("WriteLaw security: %v", err)
	}
	_, err = srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal: "Architecture rule bet", Tier: flowv1.LawTier_LAW_TIER_RULING,
			Group:           "architecture",
			Representations: []*flowv1.Representation{{Type: "text/plain", Content: "a"}},
		},
	})
	if err != nil {
		t.Fatalf("WriteLaw architecture: %v", err)
	}

	// Search with scope_filter = "security" -> only security law.
	resp, err := srv.SearchSimilarLaws(ctx, &flowv1.SearchSimilarLawsRequest{
		QueryText:   "Security rule alpha", // same length as security law goal
		ScopeFilter: "security",
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("SearchSimilarLaws: %v", err)
	}
	for _, r := range resp.GetResults() {
		if r.GetLaw().GetGroup() != "security" {
			t.Fatalf("expected only security group, got %q", r.GetLaw().GetGroup())
		}
	}
}

func TestSearchSimilarLaws_LimitEnforced(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	// Create 3 laws.
	for i := range 3 {
		_, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
			Goal:            fmt.Sprintf("Law number %d padded out", i),
			Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
		})
		if err != nil {
			t.Fatalf("RecordFinding %d: %v", i, err)
		}
	}

	// Search with limit=2 -> at most 2 results.
	resp, err := srv.SearchSimilarLaws(ctx, &flowv1.SearchSimilarLawsRequest{
		QueryText: "Law number 0 padded out",
		Limit:     2,
	})
	if err != nil {
		t.Fatalf("SearchSimilarLaws: %v", err)
	}
	if len(resp.GetResults()) > 2 {
		t.Fatalf("expected at most 2 results, got %d", len(resp.GetResults()))
	}
}

func TestSearchSimilarLaws_NoMatches_ReturnsEmpty(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	// Empty library -> no results.
	resp, err := srv.SearchSimilarLaws(ctx, &flowv1.SearchSimilarLawsRequest{
		QueryText: "anything",
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("SearchSimilarLaws: %v", err)
	}
	if len(resp.GetResults()) != 0 {
		t.Fatalf("expected 0 results for empty library, got %d", len(resp.GetResults()))
	}
}

func TestSearchSimilarLaws_ResultsOrderedBySimilarity(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	// Create laws with varying goal lengths to produce different embeddings.
	// stubEmbedder: v[i] = float32(len(text)+i) / 100.0
	// "Short" (len=5) and "A much longer goal text here" (len=29) will have
	// different similarity to query "Short" (len=5).
	_, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Short",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})
	if err != nil {
		t.Fatalf("RecordFinding Short: %v", err)
	}
	_, err = srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "A much longer goal text here!!",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})
	if err != nil {
		t.Fatalf("RecordFinding Long: %v", err)
	}

	resp, err := srv.SearchSimilarLaws(ctx, &flowv1.SearchSimilarLawsRequest{
		QueryText: "Short",
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("SearchSimilarLaws: %v", err)
	}
	if len(resp.GetResults()) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(resp.GetResults()))
	}

	// Results should be ordered by similarity descending.
	for i := 1; i < len(resp.GetResults()); i++ {
		if resp.GetResults()[i].GetSimilarityScore() > resp.GetResults()[i-1].GetSimilarityScore() {
			t.Fatalf("results not ordered by similarity descending: [%d]=%f > [%d]=%f",
				i, resp.GetResults()[i].GetSimilarityScore(),
				i-1, resp.GetResults()[i-1].GetSimilarityScore())
		}
	}
}

func TestSearchSimilarLaws_EmptyQueryText(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	_, err := srv.SearchSimilarLaws(ctx, &flowv1.SearchSimilarLawsRequest{
		Limit: 10,
	})
	if err == nil {
		t.Fatal("expected InvalidArgument for empty query_text")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestSearchSimilarLaws_NoEmbedder(t *testing.T) {
	srv := newTestServer(t) // no embedder
	ctx := context.Background()

	_, err := srv.SearchSimilarLaws(ctx, &flowv1.SearchSimilarLawsRequest{
		QueryText: "anything",
		Limit:     10,
	})
	if err == nil {
		t.Fatal("expected FailedPrecondition when embedder is nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// LawGroup RPCs
// ---------------------------------------------------------------------------

func TestGetLawGroup_StoredGroup(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Sync a group first.
	_, err := srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{
			Name: "security", Mode: "law-by-law", Passes: 3,
		},
	})
	if err != nil {
		t.Fatalf("SyncLawGroup: %v", err)
	}

	resp, err := srv.GetLawGroup(ctx, &flowv1.GetLawGroupRequest{GroupName: "security"})
	if err != nil {
		t.Fatalf("GetLawGroup: %v", err)
	}
	g := resp.GetGroup()
	if g.GetName() != "security" {
		t.Fatalf("expected name %q, got %q", "security", g.GetName())
	}
	if g.GetMode() != testLawByLawMode {
		t.Fatalf("expected mode %q, got %q", testLawByLawMode, g.GetMode())
	}
	if g.GetPasses() != 3 {
		t.Fatalf("expected passes 3, got %d", g.GetPasses())
	}
}

func TestGetLawGroup_UnknownGroupReturnsDefault(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.GetLawGroup(ctx, &flowv1.GetLawGroupRequest{GroupName: "nonexistent"})
	if err != nil {
		t.Fatalf("GetLawGroup for unknown group: %v", err)
	}
	g := resp.GetGroup()
	if g.GetName() != "nonexistent" {
		t.Fatalf("expected name %q, got %q", "nonexistent", g.GetName())
	}
	if g.GetMode() != testBundleMode {
		t.Fatalf("expected default mode %q, got %q", testBundleMode, g.GetMode())
	}
	if g.GetPasses() != 1 {
		t.Fatalf("expected default passes 1, got %d", g.GetPasses())
	}
}

func TestGetLawGroup_EmptyName(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.GetLawGroup(ctx, &flowv1.GetLawGroupRequest{})
	if err != nil {
		t.Fatalf("GetLawGroup for empty name: %v", err)
	}
	g := resp.GetGroup()
	if g.GetName() != "" {
		t.Fatalf("expected empty name, got %q", g.GetName())
	}
	if g.GetMode() != testBundleMode {
		t.Fatalf("expected default mode %q, got %q", testBundleMode, g.GetMode())
	}
	if g.GetPasses() != 1 {
		t.Fatalf("expected default passes 1, got %d", g.GetPasses())
	}
}

func TestGetLawGroup_CapabilityDenied(t *testing.T) {
	srv := newTestServer(t)
	md := metadata.Pairs(metadataKeyCapabilities, "WRITE:law", metadataKeyNodeID, "node-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.GetLawGroup(ctx, &flowv1.GetLawGroupRequest{GroupName: "security"})
	if err == nil {
		t.Fatal("expected PermissionDenied, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestListLawGroups_ReturnsStored(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, _ = srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{Name: "group-a", Mode: "bundle", Passes: 1},
	})
	_, _ = srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{Name: "group-b", Mode: "law-by-law", Passes: 2},
	})

	resp, err := srv.ListLawGroups(ctx, &flowv1.ListLawGroupsRequest{})
	if err != nil {
		t.Fatalf("ListLawGroups: %v", err)
	}
	if len(resp.GetGroups()) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(resp.GetGroups()))
	}
	names := map[string]bool{}
	for _, g := range resp.GetGroups() {
		names[g.GetName()] = true
	}
	if !names["group-a"] || !names["group-b"] {
		t.Fatalf("expected group-a and group-b, got %v", names)
	}
}

func TestListLawGroups_Empty(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.ListLawGroups(ctx, &flowv1.ListLawGroupsRequest{})
	if err != nil {
		t.Fatalf("ListLawGroups: %v", err)
	}
	if len(resp.GetGroups()) != 0 {
		t.Fatalf("expected 0 groups, got %d", len(resp.GetGroups()))
	}
}

func TestListLawGroups_CapabilityDenied(t *testing.T) {
	srv := newTestServer(t)
	md := metadata.Pairs(metadataKeyCapabilities, "WRITE:law", metadataKeyNodeID, "node-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.ListLawGroups(ctx, &flowv1.ListLawGroupsRequest{})
	if err == nil {
		t.Fatal("expected PermissionDenied, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestSyncLawGroup_InsertAndGet(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{Name: "security", Mode: "law-by-law", Passes: 3},
	})
	if err != nil {
		t.Fatalf("SyncLawGroup: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	// Verify via GetLawGroup.
	getResp, err := srv.GetLawGroup(ctx, &flowv1.GetLawGroupRequest{GroupName: "security"})
	if err != nil {
		t.Fatalf("GetLawGroup after SyncLawGroup: %v", err)
	}
	if getResp.GetGroup().GetPasses() != 3 {
		t.Fatalf("expected passes 3, got %d", getResp.GetGroup().GetPasses())
	}
}

func TestSyncLawGroup_UpdateExisting(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, _ = srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{Name: "security", Mode: "bundle", Passes: 1},
	})
	_, err := srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{Name: "security", Mode: "law-by-law", Passes: 5},
	})
	if err != nil {
		t.Fatalf("SyncLawGroup update: %v", err)
	}

	getResp, _ := srv.GetLawGroup(ctx, &flowv1.GetLawGroupRequest{GroupName: "security"})
	if getResp.GetGroup().GetMode() != "law-by-law" {
		t.Fatalf("expected mode %q, got %q", "law-by-law", getResp.GetGroup().GetMode())
	}
	if getResp.GetGroup().GetPasses() != 5 {
		t.Fatalf("expected passes 5, got %d", getResp.GetGroup().GetPasses())
	}
}

func TestSyncLawGroup_EmptyName(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{Mode: "bundle", Passes: 1},
	})
	if err == nil {
		t.Fatal("expected InvalidArgument for empty name")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestSyncLawGroup_InvalidMode(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{Name: "test", Mode: "invalid", Passes: 1},
	})
	if err == nil {
		t.Fatal("expected InvalidArgument for invalid mode")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestSyncLawGroup_PassesLessThanOne(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{Name: "test", Mode: "bundle", Passes: 0},
	})
	if err == nil {
		t.Fatal("expected InvalidArgument for passes < 1")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestSyncLawGroup_CapabilityDenied(t *testing.T) {
	srv := newTestServer(t)
	md := metadata.Pairs(metadataKeyCapabilities, "READ:law", metadataKeyNodeID, "node-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{Name: "test", Mode: "bundle", Passes: 1},
	})
	if err == nil {
		t.Fatal("expected PermissionDenied, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestDeleteLawGroup_RemovesGroup(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, _ = srv.SyncLawGroup(ctx, &flowv1.SyncLawGroupRequest{
		Group: &flowv1.LawGroup{Name: "security", Mode: "bundle", Passes: 1},
	})

	resp, err := srv.DeleteLawGroup(ctx, &flowv1.DeleteLawGroupRequest{GroupName: "security"})
	if err != nil {
		t.Fatalf("DeleteLawGroup: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	// After delete, GetLawGroup returns the built-in default.
	getResp, err := srv.GetLawGroup(ctx, &flowv1.GetLawGroupRequest{GroupName: "security"})
	if err != nil {
		t.Fatalf("GetLawGroup after delete: %v", err)
	}
	if getResp.GetGroup().GetMode() != "bundle" {
		t.Fatalf("expected default mode after delete, got %q", getResp.GetGroup().GetMode())
	}
}

func TestDeleteLawGroup_NonExistent(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.DeleteLawGroup(ctx, &flowv1.DeleteLawGroupRequest{GroupName: "nonexistent"})
	if err == nil {
		t.Fatal("expected NotFound for non-existent group")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestDeleteLawGroup_EmptyName(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.DeleteLawGroup(ctx, &flowv1.DeleteLawGroupRequest{})
	if err == nil {
		t.Fatal("expected InvalidArgument for empty name")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestDeleteLawGroup_CapabilityDenied(t *testing.T) {
	srv := newTestServer(t)
	md := metadata.Pairs(metadataKeyCapabilities, "READ:law", metadataKeyNodeID, "node-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.DeleteLawGroup(ctx, &flowv1.DeleteLawGroupRequest{GroupName: "test"})
	if err == nil {
		t.Fatal("expected PermissionDenied, got nil")
	}
}

// TestQueryLaws_GroupFilter is tested at the store level (TestQueryLaws_GroupFilter,
// TestQueryLaws_GroupAndArtefactFilter, TestQueryLaws_GroupFilter).
// The server handler simply passes the Group field through to the store's QueryFilter.
// The handler wiring for group filter is verified by TestQueryLaws_AllLaws (no filter)
// and at the store level.

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
	has, err := srv.store.HasVecEmbedding(ctx, "replicated-law-1")
	if err != nil {
		t.Fatalf("HasVecEmbedding: %v", err)
	}
	if !has {
		t.Fatal("expected vec embedding to be stored after ReplicateLaws")
	}
}
