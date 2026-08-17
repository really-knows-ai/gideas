package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/foundry/flow/librarian/internal/store/sqlite"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flow "github.com/foundry/flow/sdk/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// QueryLaws returns laws matching the filter.
func (s *LibrarianServer) QueryLaws(
	ctx context.Context, req *flowv1.QueryLawsRequest,
) (*flowv1.QueryLawsResponse, error) {
	// Capability check.
	if err := flow.CheckCapability(ctx, "READ:law"); err != nil {
		return nil, err
	}

	filter := sqlite.QueryFilter{}
	if f := req.GetFilter(); f != nil {
		filter.GovernedArtefact = f.GetGovernedArtefact()
		filter.RepresentationType = f.GetRepresentationType()
		filter.Group = f.GetGroup()

		// Validate: if representation_type is set, governed_artefact must also be set.
		if filter.RepresentationType != "" && filter.GovernedArtefact == "" {
			return nil, status.Error(codes.InvalidArgument, "representation_type requires governed_artefact")
		}
	}

	slog.Info("QueryLaws",
		"governed_artefact", filter.GovernedArtefact,
		"representation_type", filter.RepresentationType,
		"group", filter.Group,
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
func (s *LibrarianServer) RecordFinding(
	ctx context.Context, req *flowv1.RecordFindingRequest,
) (*flowv1.RecordFindingResponse, error) {
	// Capability check.
	if err := flow.CheckCapability(ctx, "WRITE:law/tier1"); err != nil {
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

	s.publishAudit("audit.law.created", map[string]string{
		"action":      "created",
		"resource_id": id,
		"tier":        "1",
	})

	// Compute embedding inline and store it. Run conflict detection.
	if s.embedder != nil {
		s.embedLawSync(ctx, id, versionHash, law)
		s.bgWg.Go(func() {
			s.runConflictDetection(id, law)
		})
	}

	return &flowv1.RecordFindingResponse{LawId: id}, nil
}

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

// WriteLaw persists a law (Tier 2+ Ruling minted by the Clerk, or higher-tier
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
		Group:           protoLaw.GetGroup(),
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

	action := "created"
	if protoLaw.GetId() != "" {
		action = "updated"
	}
	s.publishAudit("audit.law."+action, map[string]string{
		"action":      action,
		"resource_id": lawID,
	})

	// Compute and store embedding synchronously (both law_versions and vec0).
	s.embedLawSync(ctx, lawID, versionHash, storeLaw)

	return &flowv1.WriteLawResponse{
		LawId:       lawID,
		VersionHash: versionHash,
	}, nil
}

// RetireLaw removes a law from the active Library.
func (s *LibrarianServer) RetireLaw(
	ctx context.Context, req *flowv1.RetireLawRequest,
) (*flowv1.RetireLawResponse, error) {
	if req.GetLawId() == "" {
		return nil, status.Error(codes.InvalidArgument, "law_id is required")
	}

	// Delete vec embedding before retiring the law (need law to exist for map lookup).
	s.deleteVecEmbedding(ctx, req.GetLawId())

	if err := s.store.RetireLaw(ctx, req.GetLawId()); err != nil {
		slog.Error("RetireLaw failed", "law_id", req.GetLawId(), "error", err)
		return nil, status.Errorf(codes.Internal, "retire law: %v", err)
	}

	slog.Info("RetireLaw completed", "law_id", req.GetLawId())

	s.publishAudit("audit.law.retired", map[string]string{
		"action":      "retired",
		"resource_id": req.GetLawId(),
	})

	return &flowv1.RetireLawResponse{Acknowledged: true}, nil
}

// ReplicateLaws stores laws received from a remote Flow via Federation
// distribution. Each law is created or updated in the local Library.
// Embeddings are computed and stored for each replicated law.
func (s *LibrarianServer) ReplicateLaws(
	ctx context.Context, req *flowv1.ReplicateLawsRequest,
) (*flowv1.ReplicateLawsResponse, error) {
	results := make([]*flowv1.IntegrationResult, 0, len(req.GetLaws()))

	for _, protoLaw := range req.GetLaws() {
		result := &flowv1.IntegrationResult{LawId: protoLaw.GetId()}

		if protoLaw.GetId() == "" {
			result.ConflictReason = "law.id is required for replication"
			results = append(results, result)
			continue
		}
		if protoLaw.GetGoal() == "" {
			result.ConflictReason = "law.goal is required"
			results = append(results, result)
			continue
		}

		tier := int(protoLaw.GetTier())
		if tier < 1 || tier > 5 {
			result.ConflictReason = "law.tier must be between 1 and 5"
			results = append(results, result)
			continue
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
			Group:           protoLaw.GetGroup(),
			SourceFlow:      req.GetSourceFlowNamespace(),
			PetitionID:      req.GetPetitionId(),
		}

		// Upsert: create if new, update if exists. Provenance is preserved.
		versionHash, err := s.store.ReplicateLaw(ctx, protoLaw.GetId(), storeLaw)
		if err != nil {
			slog.Error("ReplicateLaws store failed",
				"law_id", protoLaw.GetId(), "error", err)
			result.ConflictReason = fmt.Sprintf("store failed: %v", err)
			results = append(results, result)
			continue
		}

		slog.Info("ReplicateLaws: law stored",
			"law_id", protoLaw.GetId(),
			"version_hash", versionHash,
			"source_flow", req.GetSourceFlowNamespace(),
		)

		// Compute and store embedding synchronously.
		s.embedLawSync(ctx, protoLaw.GetId(), versionHash, storeLaw)

		result.Accepted = true
		results = append(results, result)
	}

	return &flowv1.ReplicateLawsResponse{IntegrationResults: results}, nil
}

// SearchSimilarLaws performs a vector similarity search against the law
// embeddings in the Library. It embeds the query text, searches the vec0
// virtual table for nearest neighbours, optionally filters by group
// (scope_filter), and returns full Law objects with similarity scores.
func (s *LibrarianServer) SearchSimilarLaws(
	ctx context.Context, req *flowv1.SearchSimilarLawsRequest,
) (*flowv1.SearchSimilarLawsResponse, error) {
	if req.GetQueryText() == "" {
		return nil, status.Error(codes.InvalidArgument, "query_text is required")
	}
	if s.embedder == nil {
		return nil, status.Error(codes.FailedPrecondition, "embedding provider is not configured")
	}

	// Compute the query embedding.
	queryEmbedding, err := s.embedder.Embed(ctx, req.GetQueryText())
	if err != nil {
		slog.Error("SearchSimilarLaws: embedding failed", "error", err)
		return nil, status.Errorf(codes.Internal, "compute query embedding: %v", err)
	}

	// Determine the search limit. Fetch more than requested if we need to
	// post-filter by scope, so we have enough candidates.
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 10
	}
	fetchLimit := limit
	if req.GetScopeFilter() != "" {
		// Over-fetch to account for scope filtering.
		fetchLimit = max(limit*3, 30)
	}

	// Query the vec0 table for nearest neighbours.
	vecResults, err := s.store.SearchVecSimilar(ctx, queryEmbedding, fetchLimit)
	if err != nil {
		slog.Error("SearchSimilarLaws: vec search failed", "error", err)
		return nil, status.Errorf(codes.Internal, "vector search: %v", err)
	}

	// Resolve each result to a full Law, apply scope filter, and convert
	// distances to similarity scores.
	var results []*flowv1.SimilarLaw
	for _, vr := range vecResults {
		if len(results) >= limit {
			break
		}

		law, err := s.store.GetLaw(ctx, vr.LawID)
		if err != nil {
			// Law may have been retired between the vec search and the
			// lookup — skip silently.
			continue
		}

		// Scope filter: if set, only include laws matching the group.
		if req.GetScopeFilter() != "" && law.Group != req.GetScopeFilter() {
			continue
		}

		// Convert L2 distance to a similarity score in [0, 1].
		// similarity = 1 / (1 + distance)
		similarity := float32(1.0 / (1.0 + vr.Distance))

		results = append(results, &flowv1.SimilarLaw{
			Law:             storeLawToProto(law),
			SimilarityScore: similarity,
		})
	}

	slog.Info("SearchSimilarLaws",
		"query_len", len(req.GetQueryText()),
		"scope_filter", req.GetScopeFilter(),
		"results", len(results),
	)

	return &flowv1.SearchSimilarLawsResponse{Results: results}, nil
}

// ApplyLifecycleAction applies the outcome of a review hearing.
func (s *LibrarianServer) ApplyLifecycleAction(
	ctx context.Context, req *flowv1.ApplyLifecycleActionRequest,
) (*flowv1.ApplyLifecycleActionResponse, error) {
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
		s.publishAudit("audit.law.promoted", map[string]string{
			"action":      "promoted",
			"resource_id": lawID,
		})

	case flowv1.Verdict_VERDICT_RETIRE:
		if err := s.store.RetireLaw(ctx, lawID); err != nil {
			return nil, status.Errorf(codes.Internal, "retire law: %v", err)
		}
		slog.Info("ApplyLifecycleAction: retire", "law_id", lawID)
		s.publishAudit("audit.law.retired", map[string]string{
			"action":      "retired",
			"resource_id": lawID,
		})

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
		s.publishAudit("audit.law.demoted", map[string]string{
			"action":      "demoted",
			"resource_id": lawID,
		})

	default:
		return nil, status.Errorf(codes.InvalidArgument, "unknown verdict: %v", verdict)
	}

	return &flowv1.ApplyLifecycleActionResponse{Acknowledged: true}, nil
}
