package service

import (
	"context"
	"log/slog"

	"github.com/foundry/flow/librarian/internal/embed"
	"github.com/foundry/flow/librarian/internal/store/sqlite"
)

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
