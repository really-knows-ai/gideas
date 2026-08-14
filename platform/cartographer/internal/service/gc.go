package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/foundry/flow/cartographer/internal/store"
)

// startGC runs the GC loop.
// ponytail: The 30-second fixed tick interval is a coarse heuristic suitable
// for typical transaction lifetimes (minutes to hours). On a cluster with many
// short-lived transactions, the GC backlog may accumulate between ticks,
// temporarily occupying git branch and branch-DB resources. On a cluster with
// very long-lived transactions, the frequent tick wastes CPU. Upgrade path:
// make the interval configurable via a server option or env var, or use an
// adaptive interval based on the current transaction count.
func (s *CartographerServer) startGC() {
	ticker := s.txManager.clock.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C():
			s.gcTick()
		case <-s.gcStop:
			return
		}
	}
}

func (s *CartographerServer) gcTick() {
	ctx := context.Background()
	now := s.txManager.clock.Now()
	// The first scan only snapshots the registered states under tm.mu; each
	// state's ExpiresAt is then read under its own lifecycle lock, because
	// ExtendTimeout mutates ExpiresAt under lifecycle. tm.mu is released
	// before lifecycle is acquired, preserving the documented lock order (no
	// tm.mu-while-waiting-on-lifecycle inversion); the lifecycle re-check
	// below re-validates each candidate after admission.
	s.txManager.mu.RLock()
	states := make([]*TransactionState, 0, len(s.txManager.active))
	for _, state := range s.txManager.active {
		states = append(states, state)
	}
	s.txManager.mu.RUnlock()
	var expiredTxIDs []string
	for _, state := range states {
		state.lifecycle.Lock()
		expired := now.After(state.ExpiresAt.Add(30 * time.Second))
		state.lifecycle.Unlock()
		if expired {
			expiredTxIDs = append(expiredTxIDs, state.ID)
		}
	}
	if len(expiredTxIDs) == 0 {
		return
	}
	for _, txID := range expiredTxIDs {
		state, unlockTx, ok := s.txManager.lockRegistered(txID)
		if !ok {
			continue
		}
		if !now.After(state.ExpiresAt.Add(30 * time.Second)) {
			unlockTx()
			continue
		}
		var merged bool
		// ponytail: the git working-tree lock (WithGitLock) is held across the
		// store-layer RehydrateMainFromFiles DB I/O below, so a slow, PVC-backed
		// LadybugDB re-hydration serializes every other git operation in the
		// process (commit/rollback/begin/cleanup/recovery, and this GC loop
		// itself) behind it. This keeps the re-hydration's file reads consistent
		// with the main working tree, but at the cost of blocking git work for
		// the full DB I/O duration. Upgrade path: narrow the lock to cover only
		// the git operations (RestoreMain/CleanUntracked/GitLogOneline) and run
		// RehydrateMainFromFiles outside it (e.g. after releasing the git lock,
		// mirroring the narrowed lock scope used in RefreshTransaction), or
		// re-order so re-hydration happens outside the git lock while still
		// holding the writeLock for main-store exclusivity.
		if err := s.withGitLock(func() error {
			if err := s.gitstore.RestoreMain(ctx); err != nil {
				return err
			}
			if err := s.gitstore.CleanUntracked(ctx); err != nil {
				return err
			}
			logs, err := s.gitstore.GitLogOneline(ctx, "transaction:"+txID)
			if err != nil {
				return err
			}
			merged = len(logs) > 0
			if !merged {
				// Re-hydrate main unconditionally (SPEC R10/serialisation
				// re-hydration), even in in-memory mode where ladybugPath is
				// unset, so main stays consistent with the git working tree.
				s.lockMainStore()
				entitiesDir, edgesDir := s.gitstore.HydrationDirs()
				err := s.store.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
				s.writeLock.Unlock()
				if err != nil {
					return err
				}
				state.MainRehydrated = false
			}
			return s.cleanupTransactionGitLocked(ctx, state)
		}); err != nil {
			unlockTx()
			continue
		}
		if err := s.finishTransactionCleanup(ctx, state); err != nil {
			unlockTx()
			continue
		}
		unlockTx()
		s.publishTelemetry("cartographer.transaction_gc", map[string]string{"tx_id": txID, "reason": "timeout"})
	}
}

// computeSchemaHash hashes all compatibility-relevant application schema data.
func computeSchemaHash(schema store.SchemaProvider) string {
	type canonicalRule struct {
		CanConnectTo []string
		Using        []string
	}
	type canonicalEntity struct {
		Name              string
		Properties        []store.PropertyDef
		EnableVectorIndex bool
		Rules             []canonicalRule
	}
	type canonicalEdge struct {
		Name       string
		Properties []store.PropertyDef
	}
	canonical := struct {
		Entities []canonicalEntity
		Edges    []canonicalEdge
	}{}
	for _, name := range sortedCopy(schema.EntityTypeNames()) {
		def, ok := schema.EntityType(name)
		if !ok {
			continue
		}
		entity := canonicalEntity{
			Name: name, Properties: sortedProperties(def.Properties), EnableVectorIndex: def.EnableVectorIndex,
		}
		for _, rule := range def.Rules {
			entity.Rules = append(entity.Rules, canonicalRule{
				CanConnectTo: sortedCopy(rule.CanConnectTo), Using: sortedCopy(rule.Using),
			})
		}
		sort.Slice(entity.Rules, func(i, j int) bool {
			left, _ := json.Marshal(entity.Rules[i])
			right, _ := json.Marshal(entity.Rules[j])
			return string(left) < string(right)
		})
		canonical.Entities = append(canonical.Entities, entity)
	}
	for _, name := range sortedCopy(schema.EdgeTypeNames()) {
		def, ok := schema.EdgeType(name)
		if !ok {
			continue
		}
		canonical.Edges = append(canonical.Edges, canonicalEdge{Name: name, Properties: sortedProperties(def.Properties)})
	}
	encoded, _ := json.Marshal(canonical)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func sortedCopy(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func sortedProperties(properties []store.PropertyDef) []store.PropertyDef {
	result := append([]store.PropertyDef(nil), properties...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		if result[i].Type != result[j].Type {
			return result[i].Type < result[j].Type
		}
		return !result[i].Required && result[j].Required
	})
	return result
}
