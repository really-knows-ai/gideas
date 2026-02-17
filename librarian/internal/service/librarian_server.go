// Package service implements the LibrarianService gRPC server.
//
// The Librarian manages the Flow's body of law: creation, versioning,
// querying, retirement, and lifecycle actions. It integrates optional
// embedding-based conflict detection for duplicate Findings.
package service

import (
	"context"
	"log/slog"
	"strings"

	"github.com/gideas/flow/librarian/internal/embed"
	"github.com/gideas/flow/librarian/internal/store/sqlite"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// IDGenerator produces unique law identifiers.
type IDGenerator func() string

// ConflictCandidate represents a potential duplicate law found by
// embedding similarity search.
type ConflictCandidate struct {
	LawID      string
	Similarity float64
}

// LibrarianServer implements flowv1.LibrarianServiceServer backed by a
// SQLite store and optional embedder for conflict detection.
type LibrarianServer struct {
	flowv1.UnimplementedLibrarianServiceServer
	store               *sqlite.Store
	embedder            embed.Embedder // nil-safe: conflict detection degrades gracefully
	newID               IDGenerator
	similarityThreshold float64
}

// NewLibrarianServer returns a LibrarianServer backed by the given store.
// The embedder may be nil; embedding operations will degrade gracefully.
// The idGen function produces unique law identifiers.
func NewLibrarianServer(store *sqlite.Store, embedder embed.Embedder, idGen IDGenerator, similarityThreshold float64) *LibrarianServer {
	if similarityThreshold <= 0 {
		similarityThreshold = 0.85
	}
	return &LibrarianServer{
		store:               store,
		embedder:            embedder,
		newID:               idGen,
		similarityThreshold: similarityThreshold,
	}
}

// ---------------------------------------------------------------------------
// Capability enforcement
// ---------------------------------------------------------------------------

const (
	metadataKeyCapabilities = "x-flow-capabilities"
)

// checkCapability parses the x-flow-capabilities metadata header and
// verifies that the required capability is present.
func checkCapability(ctx context.Context, required string) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		// No metadata at all — allow for service-facing calls.
		return nil
	}
	caps := md.Get(metadataKeyCapabilities)
	if len(caps) == 0 {
		// No capability header — allow (capabilities not enforced yet).
		return nil
	}

	// Capabilities are comma-separated.
	for _, c := range caps {
		for cap := range strings.SplitSeq(c, ",") {
			if strings.TrimSpace(cap) == required {
				return nil
			}
		}
	}

	return status.Errorf(codes.PermissionDenied, "missing required capability: %s", required)
}

// ---------------------------------------------------------------------------
// Node-Facing RPCs (via Sidecar)
// ---------------------------------------------------------------------------

// QueryLaws returns laws matching the filter.
func (s *LibrarianServer) QueryLaws(ctx context.Context, req *flowv1.QueryLawsRequest) (*flowv1.QueryLawsResponse, error) {
	// Capability check.
	if err := checkCapability(ctx, "READ:law"); err != nil {
		return nil, err
	}

	filter := sqlite.QueryFilter{}
	if f := req.GetFilter(); f != nil {
		filter.ArtefactKind = f.GetArtefactKind()
		filter.RepresentationType = f.GetRepresentationType()

		// Validate: if representation_type is set, artefact_kind must also be set.
		if filter.RepresentationType != "" && filter.ArtefactKind == "" {
			return nil, status.Error(codes.InvalidArgument, "representation_type requires artefact_kind")
		}
	}

	slog.Info("QueryLaws",
		"artefact_kind", filter.ArtefactKind,
		"representation_type", filter.RepresentationType,
	)

	laws, err := s.store.QueryLaws(ctx, filter)
	if err != nil {
		slog.Error("QueryLaws failed", "error", err)
		return nil, status.Errorf(codes.Internal, "query laws: %v", err)
	}

	protoLaws := make([]*flowv1.Law, 0, len(laws))
	for _, law := range laws {
		protoLaws = append(protoLaws, storeLawToProto(law))
	}

	return &flowv1.QueryLawsResponse{Laws: protoLaws}, nil
}

// Cite records law usage. The Sidecar wraps this as an AddFriction call.
func (s *LibrarianServer) Cite(ctx context.Context, req *flowv1.CiteRequest) (*flowv1.CiteResponse, error) {
	if len(req.GetLawIds()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one law_id is required")
	}

	// Verify each law exists (log warning for missing, don't fail).
	for _, lawID := range req.GetLawIds() {
		_, err := s.store.GetLaw(ctx, lawID)
		if err != nil {
			slog.Warn("Cite: law not found", "law_id", lawID, "error", err)
		}
	}

	slog.Info("Cite recorded", "law_ids", req.GetLawIds())

	return &flowv1.CiteResponse{Acknowledged: true}, nil
}

// RecordFinding creates a Tier 1 Finding. Write-availability-first: returns
// immediately with a law identifier.
func (s *LibrarianServer) RecordFinding(ctx context.Context, req *flowv1.RecordFindingRequest) (*flowv1.RecordFindingResponse, error) {
	// Capability check.
	if err := checkCapability(ctx, "WRITE:law/tier1"); err != nil {
		return nil, err
	}

	if req.GetGoal() == "" {
		return nil, status.Error(codes.InvalidArgument, "goal is required")
	}
	if len(req.GetRepresentations()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one representation is required")
	}

	id := s.newID()

	storeReps := make([]sqlite.Representation, 0, len(req.GetRepresentations()))
	for _, r := range req.GetRepresentations() {
		storeReps = append(storeReps, sqlite.Representation{
			Type:    r.GetType(),
			Content: r.GetContent(),
		})
	}

	law := sqlite.Law{
		Goal:            req.GetGoal(),
		Tier:            1, // Tier 1 Finding.
		AppliesTo:       req.GetAppliesTo(),
		Representations: storeReps,
	}

	versionHash, err := s.store.CreateLaw(ctx, id, law)
	if err != nil {
		slog.Error("RecordFinding failed", "error", err)
		return nil, status.Errorf(codes.Internal, "create law: %v", err)
	}

	slog.Info("RecordFinding created",
		"law_id", id,
		"version_hash", versionHash,
	)

	// Compute embedding inline and store it. Run conflict detection.
	if s.embedder != nil {
		go s.embedAndDetectConflicts(id, versionHash, law)
	}

	return &flowv1.RecordFindingResponse{LawId: id}, nil
}

// ---------------------------------------------------------------------------
// Service-Facing RPCs
// ---------------------------------------------------------------------------

// GetLaw returns the full law object by identifier.
func (s *LibrarianServer) GetLaw(ctx context.Context, req *flowv1.GetLawRequest) (*flowv1.GetLawResponse, error) {
	if req.GetLawId() == "" {
		return nil, status.Error(codes.InvalidArgument, "law_id is required")
	}

	law, err := s.store.GetLaw(ctx, req.GetLawId())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "law not found: %v", err)
	}

	return &flowv1.GetLawResponse{Law: storeLawToProto(law)}, nil
}

// WriteLaw persists a law (Tier 2+ Ruling minted by Assay, or higher-tier
// by administrator).
func (s *LibrarianServer) WriteLaw(ctx context.Context, req *flowv1.WriteLawRequest) (*flowv1.WriteLawResponse, error) {
	protoLaw := req.GetLaw()
	if protoLaw == nil {
		return nil, status.Error(codes.InvalidArgument, "law is required")
	}
	if protoLaw.GetGoal() == "" {
		return nil, status.Error(codes.InvalidArgument, "law.goal is required")
	}
	if len(protoLaw.GetRepresentations()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one representation is required")
	}
	tier := int(protoLaw.GetTier())
	if tier < 1 || tier > 5 {
		return nil, status.Error(codes.InvalidArgument, "law.tier must be between 1 and 5")
	}

	storeReps := make([]sqlite.Representation, 0, len(protoLaw.GetRepresentations()))
	for _, r := range protoLaw.GetRepresentations() {
		storeReps = append(storeReps, sqlite.Representation{
			Type:    r.GetType(),
			Content: r.GetContent(),
		})
	}

	storeLaw := sqlite.Law{
		Goal:            protoLaw.GetGoal(),
		Tier:            tier,
		AppliesTo:       protoLaw.GetAppliesTo(),
		Representations: storeReps,
	}

	var (
		lawID       string
		versionHash string
		err         error
	)

	if protoLaw.GetId() != "" {
		// Existing law: update (new version).
		lawID = protoLaw.GetId()
		versionHash, err = s.store.UpdateLaw(ctx, lawID, storeLaw)
		if err != nil {
			slog.Error("WriteLaw update failed", "law_id", lawID, "error", err)
			return nil, status.Errorf(codes.Internal, "update law: %v", err)
		}
	} else {
		// New law: create inactive (hearing-created, pending activation).
		lawID = s.newID()
		versionHash, err = s.store.CreateLawInactive(ctx, lawID, storeLaw)
		if err != nil {
			slog.Error("WriteLaw create failed", "error", err)
			return nil, status.Errorf(codes.Internal, "create law: %v", err)
		}
	}

	slog.Info("WriteLaw completed",
		"law_id", lawID,
		"version_hash", versionHash,
		"is_update", protoLaw.GetId() != "",
	)

	// Compute and store embedding.
	if s.embedder != nil {
		go s.embedLaw(lawID, versionHash, storeLaw)
	}

	return &flowv1.WriteLawResponse{
		LawId:       lawID,
		VersionHash: versionHash,
	}, nil
}

// RetireLaw removes a law from the active Library.
func (s *LibrarianServer) RetireLaw(ctx context.Context, req *flowv1.RetireLawRequest) (*flowv1.RetireLawResponse, error) {
	if req.GetLawId() == "" {
		return nil, status.Error(codes.InvalidArgument, "law_id is required")
	}

	if err := s.store.RetireLaw(ctx, req.GetLawId()); err != nil {
		slog.Error("RetireLaw failed", "law_id", req.GetLawId(), "error", err)
		return nil, status.Errorf(codes.Internal, "retire law: %v", err)
	}

	slog.Info("RetireLaw completed", "law_id", req.GetLawId())

	return &flowv1.RetireLawResponse{Acknowledged: true}, nil
}

// ReplicateLaws is stubbed — cross-flow support is out of scope.
func (s *LibrarianServer) ReplicateLaws(ctx context.Context, req *flowv1.ReplicateLawsRequest) (*flowv1.ReplicateLawsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ReplicateLaws is not implemented (cross-flow support deferred)")
}

// ApplyLifecycleAction applies the outcome of a review hearing.
func (s *LibrarianServer) ApplyLifecycleAction(ctx context.Context, req *flowv1.ApplyLifecycleActionRequest) (*flowv1.ApplyLifecycleActionResponse, error) {
	if req.GetLawId() == "" {
		return nil, status.Error(codes.InvalidArgument, "law_id is required")
	}

	verdict := req.GetVerdict()
	if verdict == flowv1.Verdict_VERDICT_UNSPECIFIED {
		return nil, status.Error(codes.InvalidArgument, "verdict is required")
	}

	lawID := req.GetLawId()

	switch verdict {
	case flowv1.Verdict_VERDICT_PROMOTE:
		// Get current law.
		law, err := s.store.GetLaw(ctx, lawID)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "law not found: %v", err)
		}
		if law.Tier >= 5 {
			return nil, status.Error(codes.FailedPrecondition, "cannot promote beyond Tier 5")
		}
		// Increment tier, activate if inactive.
		if err := s.store.SetTier(ctx, lawID, law.Tier+1); err != nil {
			return nil, status.Errorf(codes.Internal, "set tier: %v", err)
		}
		if !law.Active {
			if err := s.store.ActivateLaw(ctx, lawID); err != nil {
				return nil, status.Errorf(codes.Internal, "activate law: %v", err)
			}
		}
		slog.Info("ApplyLifecycleAction: promote", "law_id", lawID, "new_tier", law.Tier+1)

	case flowv1.Verdict_VERDICT_RETIRE:
		if err := s.store.RetireLaw(ctx, lawID); err != nil {
			return nil, status.Errorf(codes.Internal, "retire law: %v", err)
		}
		slog.Info("ApplyLifecycleAction: retire", "law_id", lawID)

	case flowv1.Verdict_VERDICT_DEMOTE:
		law, err := s.store.GetLaw(ctx, lawID)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "law not found: %v", err)
		}
		if law.Tier <= 1 {
			return nil, status.Error(codes.FailedPrecondition, "cannot demote below Tier 1")
		}
		if err := s.store.SetTier(ctx, lawID, law.Tier-1); err != nil {
			return nil, status.Errorf(codes.Internal, "set tier: %v", err)
		}
		slog.Info("ApplyLifecycleAction: demote", "law_id", lawID, "new_tier", law.Tier-1)

	default:
		return nil, status.Errorf(codes.InvalidArgument, "unknown verdict: %v", verdict)
	}

	return &flowv1.ApplyLifecycleActionResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Conflict Detection (Phase 4)
// ---------------------------------------------------------------------------

// embedAndDetectConflicts computes the embedding for a law, stores it, and
// runs scope-aware conflict detection. Candidates are logged but no automatic
// action is taken.
func (s *LibrarianServer) embedAndDetectConflicts(lawID, versionHash string, law sqlite.Law) {
	ctx := context.Background()

	embedding, err := s.embedder.Embed(ctx, law.Goal)
	if err != nil {
		slog.Warn("Failed to compute embedding", "law_id", lawID, "error", err)
		return
	}

	if err := s.store.SetEmbedding(ctx, lawID, versionHash, embedding); err != nil {
		slog.Warn("Failed to store embedding", "law_id", lawID, "error", err)
		return
	}

	candidates := s.findConflicts(ctx, lawID, law.AppliesTo, embedding)
	if len(candidates) > 0 {
		slog.Info("Conflict candidates detected",
			"law_id", lawID,
			"candidates", candidates,
		)
	}
}

// embedLaw computes and stores the embedding for a law (without conflict
// detection).
func (s *LibrarianServer) embedLaw(lawID, versionHash string, law sqlite.Law) {
	ctx := context.Background()

	embedding, err := s.embedder.Embed(ctx, law.Goal)
	if err != nil {
		slog.Warn("Failed to compute embedding", "law_id", lawID, "error", err)
		return
	}

	if err := s.store.SetEmbedding(ctx, lawID, versionHash, embedding); err != nil {
		slog.Warn("Failed to store embedding", "law_id", lawID, "error", err)
	}
}

// findConflicts implements scope-aware embedding similarity search.
//
// Algorithm:
//  1. Load all active embeddings.
//  2. Scope filter: skip candidates with no scope overlap unless one is global.
//  3. Similarity filter: keep candidates above the configured threshold.
func (s *LibrarianServer) findConflicts(ctx context.Context, lawID string, appliesTo []string, embedding []float32) []ConflictCandidate {
	allEmbeddings, err := s.store.GetAllActiveEmbeddings(ctx)
	if err != nil {
		slog.Warn("Failed to load embeddings for conflict detection", "error", err)
		return nil
	}

	incomingGlobal := len(appliesTo) == 0
	incomingScope := toSet(appliesTo)

	var candidates []ConflictCandidate
	for _, candidate := range allEmbeddings {
		if candidate.LawID == lawID {
			continue // Skip self.
		}

		// Scope filter.
		candidateGlobal := len(candidate.AppliesTo) == 0
		if !incomingGlobal && !candidateGlobal {
			// Both scoped — check overlap.
			if !setsOverlap(incomingScope, toSet(candidate.AppliesTo)) {
				continue
			}
		}

		// Similarity filter.
		sim, err := embed.CosineSimilarity(embedding, candidate.Embedding)
		if err != nil {
			continue
		}
		if sim >= s.similarityThreshold {
			candidates = append(candidates, ConflictCandidate{
				LawID:      candidate.LawID,
				Similarity: sim,
			})
		}
	}

	return candidates
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func storeLawToProto(law sqlite.Law) *flowv1.Law {
	reps := make([]*flowv1.Representation, 0, len(law.Representations))
	for _, r := range law.Representations {
		reps = append(reps, &flowv1.Representation{
			Type:    r.Type,
			Content: r.Content,
		})
	}

	return &flowv1.Law{
		Id:              law.ID,
		Goal:            law.Goal,
		Representations: reps,
		Tier:            flowv1.LawTier(law.Tier),
		AppliesTo:       law.AppliesTo,
		VersionHash:     law.VersionHash,
		CreatedAt:       timestamppb.New(law.CreatedAt),
		UpdatedAt:       timestamppb.New(law.UpdatedAt),
	}
}

func toSet(ss []string) map[string]struct{} {
	m := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		m[s] = struct{}{}
	}
	return m
}

func setsOverlap(a, b map[string]struct{}) bool {
	for k := range a {
		if _, ok := b[k]; ok {
			return true
		}
	}
	return false
}
