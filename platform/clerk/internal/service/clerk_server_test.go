package service

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Mock Librarian
// ---------------------------------------------------------------------------

// mockLibrarian implements the Librarian interface for testing.
type mockLibrarian struct {
	// WriteLaw behaviour.
	writeLawID    string
	writeVersionH string
	writeErr      error
	writeCalled   bool
	writeLastLaw  *flowv1.Law

	// RetireLaw behaviour.
	retireAck       bool
	retireErr       error
	retireCalled    bool
	retireLastLawID string
}

func (m *mockLibrarian) WriteLaw(
	_ context.Context, in *flowv1.WriteLawRequest, _ ...grpc.CallOption,
) (*flowv1.WriteLawResponse, error) {
	m.writeCalled = true
	m.writeLastLaw = in.GetLaw()
	if m.writeErr != nil {
		return nil, m.writeErr
	}
	return &flowv1.WriteLawResponse{
		LawId:       m.writeLawID,
		VersionHash: m.writeVersionH,
	}, nil
}

func (m *mockLibrarian) RetireLaw(
	_ context.Context, in *flowv1.RetireLawRequest, _ ...grpc.CallOption,
) (*flowv1.RetireLawResponse, error) {
	m.retireCalled = true
	m.retireLastLawID = in.GetLawId()
	if m.retireErr != nil {
		return nil, m.retireErr
	}
	return &flowv1.RetireLawResponse{Acknowledged: m.retireAck}, nil
}

// ---------------------------------------------------------------------------
// Test Helpers
// ---------------------------------------------------------------------------

func setupTestServer(t *testing.T, lib Librarian) flowv1.ClerkServiceClient {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	srv := grpc.NewServer()
	clerkSrv := NewClerkServer(lib)
	flowv1.RegisterClerkServiceServer(srv, clerkSrv)

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return flowv1.NewClerkServiceClient(conn)
}

func makeVerdict(outcome string, jurors int) *flowv1.DeliberateResponse {
	justifications := make([]*flowv1.JurorJustification, jurors)
	for i := range jurors {
		justifications[i] = &flowv1.JurorJustification{
			JurorId:   fmt.Sprintf("juror-%d", i),
			Outcome:   outcome,
			Reasoning: fmt.Sprintf("reasoning from juror %d for %s", i, outcome),
		}
	}
	return &flowv1.DeliberateResponse{
		Outcome:        outcome,
		Justifications: justifications,
		RoundsUsed:     1,
		Hung:           false,
	}
}

// ---------------------------------------------------------------------------
// DraftLaw: Normal Draft Tests
// ---------------------------------------------------------------------------

func TestDraftLaw_NormalDraft(t *testing.T) {
	lib := &mockLibrarian{
		writeLawID:    "law-001",
		writeVersionH: "abc123",
	}
	client := setupTestServer(t, lib)

	resp, err := client.DraftLaw(context.Background(), &flowv1.DraftLawRequest{
		Verdict:   makeVerdict("favour_refiner", 3),
		Goal:      "Refiners must cite at least one law when refusing feedback",
		Tier:      2,
		AppliesTo: []string{"haiku"},
	})
	if err != nil {
		t.Fatalf("DraftLaw() error: %v", err)
	}

	if resp.GetLawId() != "law-001" {
		t.Errorf("law_id = %q, want %q", resp.GetLawId(), "law-001")
	}
	if resp.GetVersionHash() != "abc123" {
		t.Errorf("version_hash = %q, want %q", resp.GetVersionHash(), "abc123")
	}
	if len(resp.GetRepresentations()) != 1 {
		t.Fatalf("representations count = %d, want 1", len(resp.GetRepresentations()))
	}
	rep := resp.GetRepresentations()[0]
	if rep.GetType() != "text/markdown" {
		t.Errorf("representation type = %q, want %q", rep.GetType(), "text/markdown")
	}
	if !strings.Contains(rep.GetContent(), "favour_refiner") {
		t.Error("representation content should contain the verdict outcome")
	}
	if !strings.Contains(rep.GetContent(), "Refiners must cite") {
		t.Error("representation content should contain the goal")
	}
	if len(resp.GetCodificationDeclines()) != 0 {
		t.Errorf("codification_declines = %v, want empty", resp.GetCodificationDeclines())
	}

	// Verify Librarian was called correctly.
	if !lib.writeCalled {
		t.Error("expected WriteLaw to be called")
	}
	if lib.writeLastLaw.GetGoal() != "Refiners must cite at least one law when refusing feedback" {
		t.Errorf("WriteLaw goal = %q", lib.writeLastLaw.GetGoal())
	}
	if lib.writeLastLaw.GetTier() != flowv1.LawTier_LAW_TIER_RULING {
		t.Errorf("WriteLaw tier = %v, want RULING", lib.writeLastLaw.GetTier())
	}
	if len(lib.writeLastLaw.GetAppliesTo()) != 1 || lib.writeLastLaw.GetAppliesTo()[0] != "haiku" {
		t.Errorf("WriteLaw applies_to = %v, want [haiku]", lib.writeLastLaw.GetAppliesTo())
	}
}

func TestDraftLaw_PromoteOutcome(t *testing.T) {
	lib := &mockLibrarian{
		writeLawID:    "law-002",
		writeVersionH: "def456",
	}
	client := setupTestServer(t, lib)

	resp, err := client.DraftLaw(context.Background(), &flowv1.DraftLawRequest{
		Verdict:   makeVerdict("promote", 5),
		Goal:      "All outputs must include attribution",
		Tier:      3,
		AppliesTo: []string{"haiku", "essay"},
	})
	if err != nil {
		t.Fatalf("DraftLaw() error: %v", err)
	}

	if resp.GetLawId() != "law-002" {
		t.Errorf("law_id = %q, want %q", resp.GetLawId(), "law-002")
	}
	if lib.writeLastLaw.GetTier() != flowv1.LawTier_LAW_TIER_LOCAL_STATUTE {
		t.Errorf("WriteLaw tier = %v, want LOCAL_STATUTE", lib.writeLastLaw.GetTier())
	}
}

// ---------------------------------------------------------------------------
// DraftLaw: Retire Tests
// ---------------------------------------------------------------------------

func TestDraftLaw_Retire(t *testing.T) {
	lib := &mockLibrarian{retireAck: true}
	client := setupTestServer(t, lib)

	resp, err := client.DraftLaw(context.Background(), &flowv1.DraftLawRequest{
		Verdict:   makeVerdict("retire", 5),
		Goal:      "law-existing-001",
		Tier:      2,
		AppliesTo: []string{"haiku"},
	})
	if err != nil {
		t.Fatalf("DraftLaw() error: %v", err)
	}

	if resp.GetLawId() != "law-existing-001" {
		t.Errorf("law_id = %q, want %q", resp.GetLawId(), "law-existing-001")
	}
	if !lib.retireCalled {
		t.Error("expected RetireLaw to be called")
	}
	if lib.retireLastLawID != "law-existing-001" {
		t.Errorf("RetireLaw law_id = %q, want %q", lib.retireLastLawID, "law-existing-001")
	}
	if lib.writeCalled {
		t.Error("WriteLaw should not be called for retire")
	}
}

// ---------------------------------------------------------------------------
// DraftLaw: Demote Tests
// ---------------------------------------------------------------------------

func TestDraftLaw_Demote(t *testing.T) {
	lib := &mockLibrarian{
		writeLawID:    "law-003",
		writeVersionH: "ghi789",
	}
	client := setupTestServer(t, lib)

	resp, err := client.DraftLaw(context.Background(), &flowv1.DraftLawRequest{
		Verdict:   makeVerdict("demote", 5),
		Goal:      "Output length should be bounded",
		Tier:      2,
		AppliesTo: []string{"haiku"},
	})
	if err != nil {
		t.Fatalf("DraftLaw() error: %v", err)
	}

	if resp.GetLawId() != "law-003" {
		t.Errorf("law_id = %q, want %q", resp.GetLawId(), "law-003")
	}
	// Demote from tier 2 -> tier 1.
	if lib.writeLastLaw.GetTier() != flowv1.LawTier_LAW_TIER_FINDING {
		t.Errorf("WriteLaw tier = %v, want FINDING (demoted from 2)", lib.writeLastLaw.GetTier())
	}
	if len(resp.GetRepresentations()) != 1 {
		t.Fatalf("representations count = %d, want 1", len(resp.GetRepresentations()))
	}
	if resp.GetRepresentations()[0].GetType() != "text/markdown" {
		t.Errorf("representation type = %q, want %q", resp.GetRepresentations()[0].GetType(), "text/markdown")
	}
}

func TestDraftLaw_DemoteBelowTier1(t *testing.T) {
	lib := &mockLibrarian{writeLawID: "law-x"}
	client := setupTestServer(t, lib)

	_, err := client.DraftLaw(context.Background(), &flowv1.DraftLawRequest{
		Verdict:   makeVerdict("demote", 3),
		Goal:      "some goal",
		Tier:      1,
		AppliesTo: []string{"haiku"},
	})
	if err == nil {
		t.Fatal("expected error when demoting below tier 1")
	}
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Errorf("status code = %v, want %v", got, codes.InvalidArgument)
	}
}

// ---------------------------------------------------------------------------
// DraftLaw: Validation Error Tests
// ---------------------------------------------------------------------------

func TestDraftLaw_ValidationErrors(t *testing.T) {
	lib := &mockLibrarian{writeLawID: "law-x", writeVersionH: "h"}
	client := setupTestServer(t, lib)

	tests := []struct {
		name string
		req  *flowv1.DraftLawRequest
	}{
		{
			name: "nil verdict",
			req: &flowv1.DraftLawRequest{
				Goal:      "g",
				Tier:      2,
				AppliesTo: []string{"haiku"},
			},
		},
		{
			name: "empty goal",
			req: &flowv1.DraftLawRequest{
				Verdict:   makeVerdict("promote", 3),
				Tier:      2,
				AppliesTo: []string{"haiku"},
			},
		},
		{
			name: "tier too low",
			req: &flowv1.DraftLawRequest{
				Verdict:   makeVerdict("promote", 3),
				Goal:      "g",
				Tier:      0,
				AppliesTo: []string{"haiku"},
			},
		},
		{
			name: "tier too high",
			req: &flowv1.DraftLawRequest{
				Verdict:   makeVerdict("promote", 3),
				Goal:      "g",
				Tier:      6,
				AppliesTo: []string{"haiku"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.DraftLaw(context.Background(), tc.req)
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			if got := status.Code(err); got != codes.InvalidArgument {
				t.Errorf("status code = %v, want %v", got, codes.InvalidArgument)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DraftLaw: Error Handling Tests
// ---------------------------------------------------------------------------

func TestDraftLaw_LibrarianWriteError(t *testing.T) {
	lib := &mockLibrarian{writeErr: fmt.Errorf("librarian unavailable")}
	client := setupTestServer(t, lib)

	_, err := client.DraftLaw(context.Background(), &flowv1.DraftLawRequest{
		Verdict:   makeVerdict("favour_reviewer", 3),
		Goal:      "some goal",
		Tier:      2,
		AppliesTo: []string{"haiku"},
	})
	if err == nil {
		t.Fatal("expected error from Librarian failure")
	}
	if got := status.Code(err); got != codes.Internal {
		t.Errorf("status code = %v, want %v", got, codes.Internal)
	}
	if !strings.Contains(err.Error(), "librarian unavailable") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "librarian unavailable")
	}
}

func TestDraftLaw_LibrarianRetireError(t *testing.T) {
	lib := &mockLibrarian{retireErr: fmt.Errorf("retire failed")}
	client := setupTestServer(t, lib)

	_, err := client.DraftLaw(context.Background(), &flowv1.DraftLawRequest{
		Verdict:   makeVerdict("retire", 5),
		Goal:      "law-to-retire",
		Tier:      1,
		AppliesTo: []string{"haiku"},
	})
	if err == nil {
		t.Fatal("expected error from Librarian retire failure")
	}
	if got := status.Code(err); got != codes.Internal {
		t.Errorf("status code = %v, want %v", got, codes.Internal)
	}
	if !strings.Contains(err.Error(), "retire failed") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "retire failed")
	}
}

func TestDraftLaw_NoLibrarian(t *testing.T) {
	client := setupTestServer(t, nil)

	_, err := client.DraftLaw(context.Background(), &flowv1.DraftLawRequest{
		Verdict:   makeVerdict("promote", 3),
		Goal:      "some goal",
		Tier:      2,
		AppliesTo: []string{"haiku"},
	})
	if err == nil {
		t.Fatal("expected error when librarian is nil")
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Errorf("status code = %v, want %v", got, codes.Unavailable)
	}
}

// ---------------------------------------------------------------------------
// Prose Drafting Tests
// ---------------------------------------------------------------------------

func TestDraftProse_Format(t *testing.T) {
	verdict := &flowv1.DeliberateResponse{
		Outcome: "favour_refiner",
		Justifications: []*flowv1.JurorJustification{
			{
				JurorId:   "textualist",
				Outcome:   "favour_refiner",
				Reasoning: "The refiner cited Law A which clearly applies.",
			},
			{
				JurorId:   "pragmatist",
				Outcome:   "favour_reviewer",
				Reasoning: "The practical impact suggests the reviewer's point has merit.",
			},
		},
		RoundsUsed: 2,
	}

	prose := draftProse("Refiners must cite laws when refusing feedback", verdict)

	// Check structure.
	if !strings.Contains(prose, "# Law") {
		t.Error("prose should contain '# Law' heading")
	}
	if !strings.Contains(prose, "## Goal") {
		t.Error("prose should contain '## Goal' heading")
	}
	if !strings.Contains(prose, "Refiners must cite laws when refusing feedback") {
		t.Error("prose should contain the goal text")
	}
	if !strings.Contains(prose, "## Verdict") {
		t.Error("prose should contain '## Verdict' heading")
	}
	if !strings.Contains(prose, "favour_refiner") {
		t.Error("prose should contain the outcome")
	}
	if !strings.Contains(prose, "Rounds used:** 2") {
		t.Error("prose should contain rounds used")
	}
	if !strings.Contains(prose, "## Juror Reasoning") {
		t.Error("prose should contain '## Juror Reasoning' heading")
	}
	if !strings.Contains(prose, "textualist") {
		t.Error("prose should contain juror IDs")
	}
	if !strings.Contains(prose, "The refiner cited Law A") {
		t.Error("prose should contain juror reasoning")
	}
}

func TestDraftProse_NoJustifications(t *testing.T) {
	verdict := &flowv1.DeliberateResponse{
		Outcome:    "promote",
		RoundsUsed: 1,
	}

	prose := draftProse("simple goal", verdict)

	if !strings.Contains(prose, "# Law") {
		t.Error("prose should contain heading even without justifications")
	}
	if !strings.Contains(prose, "promote") {
		t.Error("prose should contain outcome")
	}
	// Should not contain juror reasoning section.
	if strings.Contains(prose, "## Juror Reasoning") {
		t.Error("prose should not contain juror reasoning when there are no justifications")
	}
}

// ---------------------------------------------------------------------------
// DraftLaw: Prose Content Verification
// ---------------------------------------------------------------------------

func TestDraftLaw_ProseContainsJurorReasoning(t *testing.T) {
	lib := &mockLibrarian{
		writeLawID:    "law-prose",
		writeVersionH: "hash-prose",
	}
	client := setupTestServer(t, lib)

	verdict := &flowv1.DeliberateResponse{
		Outcome: "favour_refiner",
		Justifications: []*flowv1.JurorJustification{
			{JurorId: "textualist", Outcome: "favour_refiner", Reasoning: "Law X is clear"},
			{JurorId: "pragmatist", Outcome: "favour_refiner", Reasoning: "Cost analysis supports"},
		},
		RoundsUsed: 1,
	}

	resp, err := client.DraftLaw(context.Background(), &flowv1.DraftLawRequest{
		Verdict:   verdict,
		Goal:      "Test juror reasoning in prose",
		Tier:      2,
		AppliesTo: []string{"haiku"},
	})
	if err != nil {
		t.Fatalf("DraftLaw() error: %v", err)
	}

	content := resp.GetRepresentations()[0].GetContent()
	if !strings.Contains(content, "Law X is clear") {
		t.Error("prose should contain textualist reasoning")
	}
	if !strings.Contains(content, "Cost analysis supports") {
		t.Error("prose should contain pragmatist reasoning")
	}
}
