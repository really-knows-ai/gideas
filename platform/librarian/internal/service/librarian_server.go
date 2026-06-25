// Package service implements the LibrarianService gRPC server.
//
// The Librarian manages the Flow's body of law: creation, versioning,
// querying, retirement, and lifecycle actions. It integrates optional
// embedding-based conflict detection for duplicate Findings.
package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/gideas/flow/librarian/internal/embed"
	"github.com/gideas/flow/librarian/internal/store/sqlite"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/pkg/randid"
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

// AuditPublisher provides non-blocking audit event submission to the Event Bus.
// Satisfied by *eventbus.AsyncPublisher. A nil publisher silently disables
// audit publishing.
type AuditPublisher interface {
	Submit(req *flowv1.PublishRequest)
}

// LibrarianServer implements flowv1.LibrarianServiceServer backed by a
// SQLite store and optional embedder for conflict detection.
type LibrarianServer struct {
	flowv1.UnimplementedLibrarianServiceServer
	store               *sqlite.Store
	embedder            embed.Embedder // nil-safe: conflict detection degrades gracefully
	newID               IDGenerator
	similarityThreshold float64
	auditor             AuditPublisher // nil-safe: audit publishing degrades gracefully
	bgWg                sync.WaitGroup // tracks in-flight background goroutines
}

// NewLibrarianServer returns a LibrarianServer backed by the given store.
// The embedder may be nil; embedding operations will degrade gracefully.
// The idGen function produces unique law identifiers.
func NewLibrarianServer(
	store *sqlite.Store, embedder embed.Embedder,
	idGen IDGenerator, similarityThreshold float64,
	opts ...LibrarianOption,
) *LibrarianServer {
	if similarityThreshold <= 0 {
		similarityThreshold = 0.85
	}
	srv := &LibrarianServer{
		store:               store,
		embedder:            embedder,
		newID:               idGen,
		similarityThreshold: similarityThreshold,
	}
	for _, o := range opts {
		o(srv)
	}
	return srv
}

// Wait blocks until all in-flight background goroutines (e.g. conflict
// detection) have completed. Callers should invoke Wait before closing the
// underlying store to avoid accessing a closed database.
func (s *LibrarianServer) Wait() { s.bgWg.Wait() }

// LibrarianOption configures a LibrarianServer.
type LibrarianOption func(*LibrarianServer)

// WithAuditPublisher sets the Event Bus client for audit event publishing.
func WithAuditPublisher(pub AuditPublisher) LibrarianOption {
	return func(s *LibrarianServer) { s.auditor = pub }
}

// publishAudit submits an audit event to the async publisher for non-blocking
// delivery to the Event Bus. If the publisher is nil, audit publishing is
// silently disabled.
func (s *LibrarianServer) publishAudit(_ context.Context, eventType string, attrs map[string]string) {
	if s.auditor == nil {
		return
	}
	s.auditor.Submit(&flowv1.PublishRequest{
		Channel: "audit",
		Event: &flowv1.FlowEvent{
			EventId:    randid.NewRandomID(),
			EventType:  eventType,
			Timestamp:  timestamppb.Now(),
			Attributes: attrs,
		},
	})
}

// ---------------------------------------------------------------------------
// Capability enforcement
// ---------------------------------------------------------------------------

const (
	metadataKeyCapabilities = "x-flow-capabilities"
	metadataKeyNodeID       = "x-flow-node-id"
)

// checkCapability enforces deny-by-default capability gating for
// node-originated requests. System-to-system calls (no x-flow-node-id)
// pass through unconditionally.
//
// Per spec (specs/05-reference/grpc-api.md, API Invariant #3):
// "Capability enforcement is performed by the owning service."
func checkCapability(ctx context.Context, required string) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil // No metadata — system call.
	}
	nodeIDs := md.Get(metadataKeyNodeID)
	if len(nodeIDs) == 0 {
		return nil // No node identity — system call.
	}

	// Node-originated call: capability must be present.
	caps := md.Get(metadataKeyCapabilities)
	for _, c := range caps {
		for cap := range strings.SplitSeq(c, ",") {
			if strings.TrimSpace(cap) == required {
				return nil
			}
		}
	}

	return status.Errorf(codes.PermissionDenied,
		"CAPABILITY_DENIED: missing required capability %q", required)
}

// ---------------------------------------------------------------------------
// Node-Facing RPCs (via Sidecar)
// ---------------------------------------------------------------------------

// QueryLaws returns laws matching the filter.
func (s *LibrarianServer) QueryLaws(
	ctx context.Context, req *flowv1.QueryLawsRequest,
) (*flowv1.QueryLawsResponse, error) {
	// Capability check.
	if err := checkCapability(ctx, "READ:law"); err != nil {
		return nil, err
	}

	filter := sqlite.QueryFilter{}
	if f := req.GetFilter(); f != nil {
		filter.GovernedArtefact = f.GetGovernedArtefact()
		filter.RepresentationType = f.GetRepresentationType()
		filter.Division = f.GetDivision()

		// Validate: if representation_type is set, governed_artefact must also be set.
		if filter.RepresentationType != "" && filter.GovernedArtefact == "" {
			return nil, status.Error(codes.InvalidArgument, "representation_type requires governed_artefact")
		}
	}

	slog.Info("QueryLaws",
		"governed_artefact", filter.GovernedArtefact,
		"representation_type", filter.RepresentationType,
		"division", filter.Division,
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

	s.publishAudit(ctx, "audit.law.created", map[string]string{
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
		Division:        protoLaw.GetDivision(),
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
	s.publishAudit(ctx, "audit.law."+action, map[string]string{
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

	s.publishAudit(ctx, "audit.law.retired", map[string]string{
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
			Division:        protoLaw.GetDivision(),
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

// ---------------------------------------------------------------------------
// Dispute Record RPCs
// ---------------------------------------------------------------------------

// CreateDisputeRecord creates a dispute record linking a petition to the
// laws it cites. Called by law-applicator on the T4-5 path.
func (s *LibrarianServer) CreateDisputeRecord(
	ctx context.Context, req *flowv1.CreateDisputeRecordRequest,
) (*flowv1.CreateDisputeRecordResponse, error) {
	if req.GetPetitionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "petition_id is required")
	}
	if len(req.GetCitedLawIds()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one cited_law_id is required")
	}

	rec, err := s.store.CreateDisputeRecord(ctx, req.GetPetitionId(), req.GetCitedLawIds())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") ||
			strings.Contains(err.Error(), "PRIMARY KEY") {
			return nil, status.Errorf(codes.AlreadyExists,
				"dispute record already exists for petition %q", req.GetPetitionId())
		}
		slog.Error("CreateDisputeRecord failed", "petition_id", req.GetPetitionId(), "error", err)
		return nil, status.Errorf(codes.Internal, "create dispute record: %v", err)
	}

	slog.Info("CreateDisputeRecord",
		"petition_id", rec.PetitionID,
		"cited_law_ids", rec.CitedLawIDs,
	)

	s.publishAudit(ctx, "audit.dispute.created", map[string]string{
		"action":        "created",
		"petition_id":   rec.PetitionID,
		"cited_law_ids": strings.Join(rec.CitedLawIDs, ","),
	})

	return &flowv1.CreateDisputeRecordResponse{
		Record: storeDisputeToProto(rec),
	}, nil
}

// RetireDisputeRecord retires a dispute record when a petition outcome
// is resolved. Called by the petition-outcome-watcher.
func (s *LibrarianServer) RetireDisputeRecord(
	ctx context.Context, req *flowv1.RetireDisputeRecordRequest,
) (*flowv1.RetireDisputeRecordResponse, error) {
	if req.GetPetitionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "petition_id is required")
	}

	err := s.store.RetireDisputeRecord(ctx, req.GetPetitionId())
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound,
				"dispute record %q not found or already retired", req.GetPetitionId())
		}
		slog.Error("RetireDisputeRecord failed", "petition_id", req.GetPetitionId(), "error", err)
		return nil, status.Errorf(codes.Internal, "retire dispute record: %v", err)
	}

	slog.Info("RetireDisputeRecord", "petition_id", req.GetPetitionId())

	s.publishAudit(ctx, "audit.dispute.retired", map[string]string{
		"action":      "retired",
		"petition_id": req.GetPetitionId(),
	})

	return &flowv1.RetireDisputeRecordResponse{Acknowledged: true}, nil
}

// GetActiveDisputes returns active dispute records, optionally filtered by
// a cited law ID. Called by Sort to check for pending disputes.
func (s *LibrarianServer) GetActiveDisputes(
	ctx context.Context, req *flowv1.GetActiveDisputesRequest,
) (*flowv1.GetActiveDisputesResponse, error) {
	records, err := s.store.GetActiveDisputes(ctx, req.GetLawId())
	if err != nil {
		slog.Error("GetActiveDisputes failed", "law_id", req.GetLawId(), "error", err)
		return nil, status.Errorf(codes.Internal, "get active disputes: %v", err)
	}

	protoRecords := make([]*flowv1.DisputeRecord, 0, len(records))
	for _, rec := range records {
		protoRecords = append(protoRecords, storeDisputeToProto(rec))
	}

	return &flowv1.GetActiveDisputesResponse{Records: protoRecords}, nil
}

// SearchSimilarLaws performs a vector similarity search against the law
// embeddings in the Library. It embeds the query text, searches the vec0
// virtual table for nearest neighbours, optionally filters by division
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

		// Scope filter: if set, only include laws matching the division.
		if req.GetScopeFilter() != "" && law.Division != req.GetScopeFilter() {
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
		s.publishAudit(ctx, "audit.law.promoted", map[string]string{
			"action":      "promoted",
			"resource_id": lawID,
		})

	case flowv1.Verdict_VERDICT_RETIRE:
		if err := s.store.RetireLaw(ctx, lawID); err != nil {
			return nil, status.Errorf(codes.Internal, "retire law: %v", err)
		}
		slog.Info("ApplyLifecycleAction: retire", "law_id", lawID)
		s.publishAudit(ctx, "audit.law.retired", map[string]string{
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
		s.publishAudit(ctx, "audit.law.demoted", map[string]string{
			"action":      "demoted",
			"resource_id": lawID,
		})

	default:
		return nil, status.Errorf(codes.InvalidArgument, "unknown verdict: %v", verdict)
	}

	return &flowv1.ApplyLifecycleActionResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Embedding Pipeline
// ---------------------------------------------------------------------------

// embedLawSync computes and stores the embedding synchronously in both
// law_versions and the vec0 table. This is the primary embedding hook for
// WriteLaw, RecordFinding, and ReplicateLaws.
func (s *LibrarianServer) embedLawSync(ctx context.Context, lawID, versionHash string, law sqlite.Law) {
	if s.embedder == nil {
		return
	}

	embedding, err := s.embedder.Embed(ctx, law.Goal)
	if err != nil {
		slog.Warn("Failed to compute embedding", "law_id", lawID, "error", err)
		return
	}

	if err := s.store.SetEmbedding(ctx, lawID, versionHash, embedding); err != nil {
		slog.Warn("Failed to store embedding", "law_id", lawID, "error", err)
	}

	// Store in the vec0 table for similarity search.
	if err := s.store.UpsertVecEmbedding(ctx, lawID, embedding); err != nil {
		slog.Warn("Failed to store vec embedding", "law_id", lawID, "error", err)
	}
}

// deleteVecEmbedding removes the vec embedding for a law. Called on retire.
func (s *LibrarianServer) deleteVecEmbedding(ctx context.Context, lawID string) {
	if err := s.store.DeleteVecEmbedding(ctx, lawID); err != nil {
		slog.Warn("Failed to delete vec embedding", "law_id", lawID, "error", err)
	}
}

// runConflictDetection runs scope-aware conflict detection for a law that
// already has its embedding stored. This is designed to be called as a
// goroutine after embedLawSync has completed.
func (s *LibrarianServer) runConflictDetection(lawID string, law sqlite.Law) {
	ctx := context.Background()

	// Load the embedding that was just stored.
	headLaw, err := s.store.GetLaw(ctx, lawID)
	if err != nil {
		slog.Warn("Failed to load law for conflict detection", "law_id", lawID, "error", err)
		return
	}

	embedding, err := s.store.GetEmbedding(ctx, lawID, headLaw.VersionHash)
	if err != nil {
		slog.Warn("Failed to load embedding for conflict detection", "law_id", lawID, "error", err)
		return
	}
	if embedding == nil {
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

// findConflicts implements scope-aware embedding similarity search.
//
// Algorithm:
//  1. Load all active embeddings.
//  2. Scope filter: skip candidates with no scope overlap unless one is global.
//  3. Similarity filter: keep candidates above the configured threshold.
func (s *LibrarianServer) findConflicts(
	ctx context.Context, lawID string, appliesTo []string, embedding []float32,
) []ConflictCandidate {
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
		Division:        law.Division,
		VersionHash:     law.VersionHash,
		CreatedAt:       timestamppb.New(law.CreatedAt),
		UpdatedAt:       timestamppb.New(law.UpdatedAt),
	}
}

func storeDisputeToProto(rec *sqlite.DisputeRecord) *flowv1.DisputeRecord {
	s := flowv1.DisputeStatus_DISPUTE_STATUS_ACTIVE
	if rec.Status == sqlite.DisputeStatusRetired {
		s = flowv1.DisputeStatus_DISPUTE_STATUS_RETIRED
	}
	return &flowv1.DisputeRecord{
		PetitionId:  rec.PetitionID,
		CitedLawIds: rec.CitedLawIDs,
		CreatedAt:   timestamppb.New(rec.CreatedAt),
		Status:      s,
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
