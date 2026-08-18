package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRecoverOpenTransactionsPreservesDivergenceAndSchemaBaselines(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	for _, tc := range []struct {
		name          string
		advanceMain   bool
		advanceSchema bool
		commitOK      bool
		wantMessage   string
	}{
		{name: "main advanced", advanceMain: true, wantMessage: "main has advanced"},
		// A purely additive schema push (new types, new properties, rule
		// additions — SPEC R2/R6 non-destructive) does not make the branch DB
		// incompatible with the current schema, so the transaction commits
		// normally instead of being wedged in FAILED_PRECONDITION.
		{name: "schema advanced", advanceSchema: true, commitOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := testCtx()
			dataPath := t.TempDir()
			opPub, _ := generateTestKey()
			base, err := ladybug.Open(dataPath)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			gs, err := gitstore.New(dataPath)
			if err != nil {
				t.Fatalf("gitstore.New: %v", err)
			}
			srv := NewCartographerServer(
				base, gs, opPub, initTestKey(), nil, "", 30*time.Second,
				"test-ns", 30*time.Minute, 100000, WithLadybugPath(dataPath),
			)
			srv.MarkDBReady()
			applyTestSchema(ctx, t, base)
			begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
			if err != nil {
				t.Fatalf("BeginTransaction: %v", err)
			}
			created, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
				EntityType: "Component", Properties: map[string]string{"name": "pending"},
				TransactionId: begin.TransactionId,
			})
			if err != nil {
				t.Fatalf("CreateEntity: %v", err)
			}
			original, err := srv.txManager.Lookup(begin.TransactionId)
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			originalHead, originalSchema := original.MainHeadAtLastSync, original.SchemaHash
			if tc.advanceMain {
				commitGitEntity(ctx, t, gs, "66666666-6666-4666-8666-666666666666", "advanced")
			}
			if tc.advanceSchema {
				if err = base.ApplySchema(ctx, &flowv1.Schema{
					EntityTypes: []*flowv1.EntityType{
						{Name: "Component", Properties: []*flowv1.Property{
							{Name: "name", Type: "string", Required: true},
							{Name: "version", Type: "string"},
						}},
						{
							Name: "Service", Properties: []*flowv1.Property{
								{Name: "name", Type: "string", Required: true},
							},
							Rules: []*flowv1.ConnectionRule{{
								CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"},
							}},
						},
						{Name: "Added", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
					},
					EdgeTypes: []*flowv1.EdgeType{{
						Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "weight", Type: "string"}},
					}},
				}); err != nil {
					t.Fatalf("advance schema: %v", err)
				}
			}
			if err = base.Close(); err != nil {
				t.Fatalf("close store: %v", err)
			}
			reopened, err := ladybug.Open(dataPath)
			if err != nil {
				t.Fatalf("reopen store: %v", err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			reopenedGit, err := gitstore.New(dataPath)
			if err != nil {
				t.Fatalf("reopen git store: %v", err)
			}
			restarted := NewCartographerServer(
				reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second,
				"test-ns", 30*time.Minute, 100000, WithLadybugPath(dataPath),
			)
			restarted.MarkDBReady()
			if err = restarted.RecoverOpenTransactions(ctx); err != nil {
				t.Fatalf("RecoverOpenTransactions: %v", err)
			}
			recovered, err := restarted.txManager.Lookup(begin.TransactionId)
			if err != nil {
				t.Fatalf("Lookup recovered: %v", err)
			}
			if recovered.MainHeadAtLastSync != originalHead || recovered.SchemaHash != originalSchema {
				t.Fatalf("baselines changed: got head=%q schema=%q want head=%q schema=%q",
					recovered.MainHeadAtLastSync, recovered.SchemaHash, originalHead, originalSchema)
			}
			if tc.commitOK {
				// The additive schema push is compatible with the branch DB
				// (SPEC R2/R6 non-destructive): the recovered transaction
				// commits normally and its data lands on main.
				if _, err = restarted.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
					TransactionId: begin.TransactionId,
				}); err != nil {
					t.Fatalf("CommitTransaction after additive schema push: %v", err)
				}
				if _, err := reopened.GetEntity(ctx, created.EntityId, "main"); err != nil {
					t.Fatalf("committed entity missing from main after additive schema push: %v", err)
				}
				return
			}
			if _, err = restarted.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
				TransactionId: begin.TransactionId,
			}); status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), tc.wantMessage) {
				t.Fatalf("CommitTransaction error=%v, want %q", err, tc.wantMessage)
			}
			if _, err = restarted.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
				TransactionId: begin.TransactionId,
			}); err != nil {
				t.Fatalf("RollbackTransaction: %v", err)
			}
		})
	}
}

func TestRecoveryDiffPropagatesSuspectedDeletions(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()
	txID := testMutationEntityID
	state, err := srv.txManager.Create(txID, time.Minute, "")
	if err != nil {
		t.Fatalf("Create transaction: %v", err)
	}
	_, err = srv.recoverEntityChanges(state.ChangeLog, nil, map[string]map[string]gitstore.EntityFile{
		"Component": {"entity": {ID: "entity", Type: "Component"}},
	})
	if err != nil {
		t.Fatalf("recoverEntityChanges: %v", err)
	}
	_, err = srv.recoverEdgeChanges(state.ChangeLog, nil, map[string]map[string]gitstore.EdgeFile{
		"DEPENDS_ON": {"edge": {ID: "edge", Type: "DEPENDS_ON"}},
	})
	if err != nil {
		t.Fatalf("recoverEdgeChanges: %v", err)
	}

	diff, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: txID})
	if err != nil {
		t.Fatalf("GetTransactionDiff: %v", err)
	}
	if len(diff.DeletedEntities) != 1 || !diff.DeletedEntities[0].Suspected {
		t.Fatalf("expected suspected recovered entity deletion, got %+v", diff.DeletedEntities)
	}
	if len(diff.DeletedEdges) != 1 || !diff.DeletedEdges[0].Suspected {
		t.Fatalf("expected suspected recovered edge deletion, got %+v", diff.DeletedEdges)
	}
}

// TestRecoveryDiffClassifiesModifiedEdges pins SPEC R9 recovery step 2 ("For
// each edge table in the branch DB, iterate all relationships using the same
// comparison logic" as step 1) and step 3 (Diff returns "lists of added,
// modified, and deleted entities and edges"): an edge present in main with
// different content must be classified as a modification (ChangeModEdge) — not
// collapsed into ChangeAddEdge — and must surface through GetTransactionDiff's
// modified_edges wire field.
func TestRecoveryDiffClassifiesModifiedEdges(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()
	txID := testMutationEntityID
	state, err := srv.txManager.Create(txID, time.Minute, "")
	if err != nil {
		t.Fatalf("Create transaction: %v", err)
	}
	changed, err := srv.recoverEdgeChanges(state.ChangeLog, []store.Edge{
		{
			Id: "edge-mod", Type: "DEPENDS_ON", FromEntityID: "a", ToEntityID: "b",
			Properties: map[string]string{"weight": "high"},
		},
	}, map[string]map[string]gitstore.EdgeFile{
		"DEPENDS_ON": {
			"edge-mod": {
				ID: "edge-mod", Type: "DEPENDS_ON", FromEntityID: "a", ToEntityID: "b",
				Properties: map[string]string{"weight": "low"},
			},
		},
	})
	if err != nil {
		t.Fatalf("recoverEdgeChanges: %v", err)
	}
	if !changed {
		t.Fatal("expected an edge change to be recorded")
	}

	diff, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: txID})
	if err != nil {
		t.Fatalf("GetTransactionDiff: %v", err)
	}
	if len(diff.ModifiedEdges) != 1 || diff.ModifiedEdges[0].Id != "edge-mod" ||
		diff.ModifiedEdges[0].Properties["weight"] != "high" {
		t.Fatalf("expected recovered edge modification in diff, got %+v", diff.ModifiedEdges)
	}
	if len(diff.AddedEdges) != 0 {
		t.Fatalf("expected no added edges for a modified edge, got %+v", diff.AddedEdges)
	}

	// A recovered modification must never re-apply as a deletion/creation or
	// silently pass a refresh: a ChangeModEdge whose branch content differs
	// from main always conflicts at validateRefresh (before/current edge files
	// differ), leaving the branch clean and the change log preserved.
	before := gitGraphSnapshot{edges: map[string]gitstore.EdgeFile{
		"edge-mod": {
			ID: "edge-mod", Type: "DEPENDS_ON", FromEntityID: "a", ToEntityID: "b",
			Properties: map[string]string{"weight": "high"},
		},
	}}
	current := gitGraphSnapshot{edges: map[string]gitstore.EdgeFile{
		"edge-mod": {
			ID: "edge-mod", Type: "DEPENDS_ON", FromEntityID: "a", ToEntityID: "b",
			Properties: map[string]string{"weight": "low"},
		},
	}}
	if err := srv.validateRefresh(ctx, state, before, current); status.Code(err) != codes.Aborted {
		t.Fatalf("expected ABORTED refresh conflict for recovered edge modification, got %v", err)
	}
	if err := srv.reapplyTransactionChanges(ctx, txID, state.ChangeLog); status.Code(err) != codes.Aborted {
		t.Fatalf("expected re-apply of a recovered edge modification to fail loudly with ABORTED, got %v", err)
	}
}

//nolint:gocyclo
func TestRecoverOpenTransactionsAfterStoreRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()

	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, st)
	modified, err := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "before"}, nil, "main")
	if err != nil {
		t.Fatalf("create modified entity: %v", err)
	}
	deleted, err := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "deleted"}, nil, "main")
	if err != nil {
		t.Fatalf("create deleted entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, modified.Id, "before")
	commitGitEntity(ctx, t, gs, deleted.Id, "deleted")

	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	if _, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: modified.Id, Properties: map[string]string{"name": "after"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("update transaction entity: %v", err)
	}
	if _, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{
		Id: deleted.Id, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("delete transaction entity: %v", err)
	}
	branchPath := filepath.Join(dataPath, "branches", begin.TransactionId+".lbug")
	branchMetadataPath := filepath.Join(dataPath, "branches", begin.TransactionId+".schema.json")
	if _, err := os.Stat(branchPath); err != nil {
		t.Fatalf("stat persisted branch: %v", err)
	}
	if _, err := os.Stat(branchMetadataPath); err != nil {
		t.Fatalf("stat persisted branch schema metadata: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	restarted.MarkDBReady()
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("recover transactions: %v", err)
	}
	state, err := restarted.txManager.Lookup(begin.TransactionId)
	if err != nil {
		t.Fatalf("lookup recovered transaction: %v", err)
	}
	if state.MainHeadAtLastSync == "" {
		t.Fatal("recovered transaction has no main HEAD baseline")
	}
	if state.SchemaHash == "" {
		t.Fatal("recovered transaction has no schema baseline")
	}

	diff, err := restarted.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("get recovered diff: %v", err)
	}
	if len(diff.ModifiedEntities) != 1 || diff.ModifiedEntities[0].Id != modified.Id ||
		diff.ModifiedEntities[0].Properties["name"] != "after" {
		t.Fatalf("unexpected recovered modifications: %+v", diff.ModifiedEntities)
	}
	if len(diff.DeletedEntities) != 1 || diff.DeletedEntities[0].Id != deleted.Id ||
		!diff.DeletedEntities[0].Suspected {
		t.Fatalf("unexpected recovered deletions: %+v", diff.DeletedEntities)
	}
	if _, err := os.Stat(branchPath); err != nil {
		t.Fatalf("recovery removed open transaction branch: %v", err)
	}
	if _, err := restarted.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{}, TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "missing required property") {
		t.Fatalf("recovered branch accepted missing required property: %v", err)
	}
	commitGitEntity(ctx, t, reopenedGit, "55555555-5555-4555-8555-555555555555", "advanced")
	if _, err := restarted.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("commit after main advancement should require refresh: %v", err)
	}
	if _, err := restarted.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("refresh recovered transaction: %v", err)
	}
	if _, err := restarted.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "after-recovery"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("mutate recovered transaction after refresh: %v", err)
	}
	if _, err := restarted.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("roll back recovered transaction: %v", err)
	}
	if _, err := os.Stat(branchPath); !os.IsNotExist(err) {
		t.Fatalf("rollback did not remove persisted branch: %v", err)
	}
	if _, err := os.Stat(branchMetadataPath); !os.IsNotExist(err) {
		t.Fatalf("rollback did not remove branch schema metadata: %v", err)
	}
}
