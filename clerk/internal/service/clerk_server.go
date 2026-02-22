// Package service implements the Clerk gRPC service.
//
// The Clerk is the legislative drafter of the Judiciary. It receives jury
// verdicts and transforms them into law representations (initially
// text/markdown only), then persists or retires them via the Librarian.
package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
)

// Librarian defines the subset of LibrarianServiceClient used by the Clerk.
// This interface enables testing with mock implementations.
type Librarian interface {
	WriteLaw(ctx context.Context, in *flowv1.WriteLawRequest, opts ...grpc.CallOption) (*flowv1.WriteLawResponse, error)
	RetireLaw(ctx context.Context, in *flowv1.RetireLawRequest, opts ...grpc.CallOption) (*flowv1.RetireLawResponse, error)
}

// ClerkServer implements the ClerkService gRPC interface.
type ClerkServer struct {
	flowv1.UnimplementedClerkServiceServer

	librarian Librarian
}

// NewClerkServer creates a ClerkServer backed by the given Librarian client.
func NewClerkServer(librarian Librarian) *ClerkServer {
	return &ClerkServer{librarian: librarian}
}

// DraftLaw implements ClerkServiceServer.DraftLaw.
//
// For normal/promote verdicts: drafts a text/markdown representation from the
// verdict justifications and calls Librarian.WriteLaw to persist.
//
// For retire verdicts: calls Librarian.RetireLaw (no prose drafting needed).
//
// For demote verdicts: drafts prose and calls Librarian.WriteLaw at a lower tier.
func (s *ClerkServer) DraftLaw(
	ctx context.Context,
	req *flowv1.DraftLawRequest,
) (*flowv1.DraftLawResponse, error) {
	// Validate request.
	if req.GetVerdict() == nil {
		return nil, fmt.Errorf("clerk: verdict is required")
	}
	if req.GetGoal() == "" {
		return nil, fmt.Errorf("clerk: goal is required")
	}
	tier := req.GetTier()
	if tier < 1 || tier > 5 {
		return nil, fmt.Errorf("clerk: tier must be between 1 and 5")
	}
	if s.librarian == nil {
		return nil, fmt.Errorf("clerk: librarian client not configured")
	}

	verdict := req.GetVerdict()
	outcome := verdict.GetOutcome()

	slog.Info("clerk: DraftLaw requested",
		"outcome", outcome,
		"goal", req.GetGoal(),
		"tier", tier,
		"applies_to", req.GetAppliesTo(),
		"hung", verdict.GetHung(),
	)

	// Route by verdict outcome.
	switch outcome {
	case "retire":
		return s.handleRetire(ctx, req)
	case "demote":
		return s.handleDemote(ctx, req)
	default:
		// Normal draft (including "promote", "favour_refiner", "favour_reviewer", etc.)
		return s.handleDraft(ctx, req)
	}
}

// handleDraft drafts a text/markdown representation and persists via WriteLaw.
func (s *ClerkServer) handleDraft(
	ctx context.Context,
	req *flowv1.DraftLawRequest,
) (*flowv1.DraftLawResponse, error) {
	prose := draftProse(req.GetGoal(), req.GetVerdict())
	reps := []*flowv1.Representation{
		{Type: "text/markdown", Content: prose},
	}

	writeResp, err := s.librarian.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal:            req.GetGoal(),
			Representations: reps,
			Tier:            flowv1.LawTier(req.GetTier()),
			AppliesTo:       req.GetAppliesTo(),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("clerk: write law: %w", err)
	}

	slog.Info("clerk: law drafted",
		"law_id", writeResp.GetLawId(),
		"version_hash", writeResp.GetVersionHash(),
	)

	return &flowv1.DraftLawResponse{
		LawId:           writeResp.GetLawId(),
		VersionHash:     writeResp.GetVersionHash(),
		Representations: reps,
	}, nil
}

// handleRetire calls Librarian.RetireLaw. No prose drafting is needed.
// The verdict must contain a law_id somewhere in the context — for retire
// verdicts the caller embeds the target law_id in the goal field as a
// convention: "retire:<law_id>".
func (s *ClerkServer) handleRetire(
	ctx context.Context,
	req *flowv1.DraftLawRequest,
) (*flowv1.DraftLawResponse, error) {
	// Extract law_id from goal. Convention: goal starts with the law ID
	// when retiring. The caller (Tribunal/Advocate) sets the goal to the
	// law_id for retire operations.
	lawID := req.GetGoal()

	_, err := s.librarian.RetireLaw(ctx, &flowv1.RetireLawRequest{
		LawId: lawID,
	})
	if err != nil {
		return nil, fmt.Errorf("clerk: retire law: %w", err)
	}

	slog.Info("clerk: law retired", "law_id", lawID)

	return &flowv1.DraftLawResponse{
		LawId: lawID,
	}, nil
}

// handleDemote drafts prose and calls WriteLaw with a decremented tier.
// The goal contains the existing law_id for update. Convention: goal is
// formatted as "law_id:description" for demote operations.
func (s *ClerkServer) handleDemote(
	ctx context.Context,
	req *flowv1.DraftLawRequest,
) (*flowv1.DraftLawResponse, error) {
	prose := draftProse(req.GetGoal(), req.GetVerdict())
	reps := []*flowv1.Representation{
		{Type: "text/markdown", Content: prose},
	}

	newTier := req.GetTier() - 1
	if newTier < 1 {
		return nil, fmt.Errorf("clerk: cannot demote below tier 1")
	}

	writeResp, err := s.librarian.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal:            req.GetGoal(),
			Representations: reps,
			Tier:            flowv1.LawTier(newTier),
			AppliesTo:       req.GetAppliesTo(),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("clerk: write law (demote): %w", err)
	}

	slog.Info("clerk: law demoted",
		"law_id", writeResp.GetLawId(),
		"new_tier", newTier,
	)

	return &flowv1.DraftLawResponse{
		LawId:           writeResp.GetLawId(),
		VersionHash:     writeResp.GetVersionHash(),
		Representations: reps,
	}, nil
}

// ---------------------------------------------------------------------------
// Prose Drafting
// ---------------------------------------------------------------------------

// draftProse formats a jury verdict as text/markdown prose suitable for
// a law representation.
func draftProse(goal string, verdict *flowv1.DeliberateResponse) string {
	var b strings.Builder

	b.WriteString("# Law\n\n")
	b.WriteString("## Goal\n\n")
	b.WriteString(goal)
	b.WriteString("\n\n")

	b.WriteString("## Verdict\n\n")
	b.WriteString(fmt.Sprintf("**Outcome:** %s\n\n", verdict.GetOutcome()))
	b.WriteString(fmt.Sprintf("**Rounds used:** %d\n\n", verdict.GetRoundsUsed()))

	justifications := verdict.GetJustifications()
	if len(justifications) > 0 {
		b.WriteString("## Juror Reasoning\n\n")
		for _, j := range justifications {
			b.WriteString(fmt.Sprintf("### %s (voted: %s)\n\n", j.GetJurorId(), j.GetOutcome()))
			b.WriteString(j.GetReasoning())
			b.WriteString("\n\n")
		}
	}

	return b.String()
}
