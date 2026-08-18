package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTransaction_BeginCommit(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	// Begin transaction.
	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId
	if txID == "" {
		t.Fatal("expected non-empty transaction ID")
	}

	// Create entity inside transaction.
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "tx-test"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity in tx failed: %v", err)
	}

	// Get transaction diff.
	diffResp, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: txID})
	if err != nil {
		t.Fatalf("GetTransactionDiff failed: %v", err)
	}
	if len(diffResp.AddedEntities) != 1 {
		t.Fatalf("expected 1 added entity, got %d", len(diffResp.AddedEntities))
	}

	// Commit.
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: txID})
	if err != nil {
		t.Fatalf("CommitTransaction failed: %v", err)
	}

	// Verify entity exists on main.
	ents, _, err := srv.store.ListEntities(ctx, "Component", 100, "", "")
	if err != nil {
		t.Fatalf("ListEntities failed: %v", err)
	}
	if len(ents) != 1 {
		t.Fatalf("expected 1 entity on main after commit, got %d", len(ents))
	}
}

func TestTransaction_BeginRollback(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "rollback-test"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity in tx failed: %v", err)
	}

	_, err = srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{TransactionId: txID})
	if err != nil {
		t.Fatalf("RollbackTransaction failed: %v", err)
	}

	// Verify entity does NOT exist on main.
	ents, _, err := srv.store.ListEntities(ctx, "Component", 100, "", "")
	if err != nil {
		t.Fatalf("ListEntities failed: %v", err)
	}
	if len(ents) != 0 {
		t.Fatalf("expected 0 entities after rollback, got %d", len(ents))
	}
}

func TestTransaction_ChangeLogAdmissionFailureRollsBackEveryMutationFamily(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git + recovery")
	}
	type mutationCase struct {
		name  string
		setup func(context.Context, *testing.T, store.Store, string)
		first func(context.Context, *CartographerServer, string) error
		over  func(context.Context, *CartographerServer, string) error
	}
	ids := []string{
		testMutationEntityID,
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		"44444444-4444-4444-8444-444444444444",
	}
	seedEntities := func(ctx context.Context, t *testing.T, st store.Store, branch string) {
		t.Helper()
		for i, id := range ids {
			entityType := "Component"
			if i < 2 {
				entityType = "Service"
			}
			_, err := st.CreateEntity(
				ctx, entityType, id, map[string]string{"name": fmt.Sprintf("entity-%d", i)}, nil, branch,
			)
			if err != nil {
				t.Fatalf("seed entity %s on %q: %v", id, branch, err)
			}
		}
	}
	seedEdges := func(ctx context.Context, t *testing.T, st store.Store, branch string) {
		t.Helper()
		seedEntities(ctx, t, st, branch)
		for i := range 2 {
			_, err := st.CreateEdge(
				ctx, "DEPENDS_ON", ids[i], ids[i+2], map[string]string{"weight": fmt.Sprintf("%d", i)}, branch,
			)
			if err != nil {
				t.Fatalf("seed edge on %q: %v", branch, err)
			}
		}
	}
	createEntity := func(name string) func(context.Context, *CartographerServer, string) error {
		return func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
				EntityType: "Component", Properties: map[string]string{"name": name}, TransactionId: txID,
			})
			return err
		}
	}
	updateEntity := func(id, version string) func(context.Context, *CartographerServer, string) error {
		return func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
				Id: id, Properties: map[string]string{"version": version}, TransactionId: txID,
			})
			return err
		}
	}
	deleteEntity := func(id string) func(context.Context, *CartographerServer, string) error {
		return func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: id, TransactionId: txID})
			return err
		}
	}
	createEdge := func(from, to string) func(context.Context, *CartographerServer, string) error {
		return func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
				EdgeType: "DEPENDS_ON", FromEntityId: from, ToEntityId: to, TransactionId: txID,
			})
			return err
		}
	}

	cases := []mutationCase{
		{name: "CreateEntity", first: createEntity("first"), over: createEntity("overflow")},
		{name: "UpdateEntity", setup: seedEntities,
			first: updateEntity(ids[2], "first"), over: updateEntity(ids[3], "overflow")},
		{name: "DeleteEntity", setup: seedEntities, first: deleteEntity(ids[2]), over: deleteEntity(ids[3])},
		{name: "CreateEdge", setup: seedEntities, first: createEdge(ids[0], ids[2]), over: createEdge(ids[1], ids[3])},
		{name: "DeleteEdge", setup: seedEdges,
			first: func(ctx context.Context, srv *CartographerServer, txID string) error {
				edges, err := srv.store.ListEdgesOfType(ctx, "DEPENDS_ON", txID)
				if err != nil {
					return err
				}
				_, err = srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: edges[0].Id, TransactionId: txID})
				return err
			},
			over: func(ctx context.Context, srv *CartographerServer, txID string) error {
				edges, err := srv.store.ListEdgesOfType(ctx, "DEPENDS_ON", txID)
				if err != nil {
					return err
				}
				_, err = srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: edges[0].Id, TransactionId: txID})
				return err
			}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, base := newTestServer(t)
			srv.txManager.changeLogCap = 1
			ctx := testCtx()
			applyTestSchema(ctx, t, base)
			begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
			if err != nil {
				t.Fatalf("BeginTransaction: %v", err)
			}
			if tc.setup != nil {
				tc.setup(ctx, t, base, "main")
				tc.setup(ctx, t, base, begin.TransactionId)
			}
			mainEntitiesBefore, _, err := base.ListEntities(ctx, "Component", 100, "", "main")
			if err != nil {
				t.Fatalf("snapshot main components: %v", err)
			}
			mainServicesBefore, _, err := base.ListEntities(ctx, "Service", 100, "", "main")
			if err != nil {
				t.Fatalf("snapshot main services: %v", err)
			}
			mainEdgesBefore, err := base.ListEdgesOfType(ctx, "DEPENDS_ON", "main")
			if err != nil {
				t.Fatalf("snapshot main edges: %v", err)
			}

			if err := tc.first(ctx, srv, begin.TransactionId); err != nil {
				t.Fatalf("first mutation: %v", err)
			}
			if err := tc.over(ctx, srv, begin.TransactionId); status.Code(err) != codes.ResourceExhausted {
				t.Fatalf("overflow mutation error = %v, want ResourceExhausted", err)
			}
			if _, err := srv.txManager.Lookup(begin.TransactionId); err == nil {
				t.Fatal("overflow transaction remained active")
			}
			exists, err := srv.gitstore.BranchExists(ctx, begin.TransactionId)
			if err != nil || exists {
				t.Fatalf("git branch cleanup: exists=%v err=%v", exists, err)
			}
			if _, err := base.DumpAllEntities(ctx, begin.TransactionId); !errors.Is(err, store.ErrBranchNotFound) {
				t.Fatalf("branch DB remained: %v", err)
			}
			mainEntitiesAfter, _, err := base.ListEntities(ctx, "Component", 100, "", "main")
			if err != nil {
				t.Fatalf("read main components after rejection: %v", err)
			}
			mainServicesAfter, _, err := base.ListEntities(ctx, "Service", 100, "", "main")
			if err != nil {
				t.Fatalf("read main services after rejection: %v", err)
			}
			mainEdgesAfter, err := base.ListEdgesOfType(ctx, "DEPENDS_ON", "main")
			if err != nil {
				t.Fatalf("read main edges after rejection: %v", err)
			}
			if !reflect.DeepEqual(mainEntitiesAfter, mainEntitiesBefore) ||
				!reflect.DeepEqual(mainServicesAfter, mainServicesBefore) ||
				!reflect.DeepEqual(mainEdgesAfter, mainEdgesBefore) {
				t.Fatal("main changed after rejected transaction mutation")
			}
		})
	}
}

func TestDeleteEntity_StoreFailureDoesNotAddChangeLogEntry(t *testing.T) {
	srv, base := newTestServer(t)
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	id := testMutationEntityID
	if _, err := base.CreateEntity(
		ctx, "Component", id, map[string]string{"name": "kept"}, nil, begin.TransactionId,
	); err != nil {
		t.Fatalf("seed branch entity: %v", err)
	}
	srv.store = &deleteEntityFailingStore{Store: base}
	_, err = srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: id, TransactionId: begin.TransactionId})
	if err == nil || !strings.Contains(err.Error(), "simulated DeleteEntity failure") {
		t.Fatalf("DeleteEntity error = %v", err)
	}
	state, err := srv.txManager.Lookup(begin.TransactionId)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if state.ChangeLog.Len() != 0 {
		t.Fatalf("change log contains failed deletion: %+v", state.ChangeLog.Entries())
	}
	if _, err := base.GetEntity(ctx, id, begin.TransactionId); err != nil {
		t.Fatalf("failed deletion removed entity: %v", err)
	}
}

func TestTransaction_ChangeLogRollbackFailureIsExplicitAndRetryable(t *testing.T) {
	srv, base := newTestServer(t)
	failing := &dropFailingStore{Store: base, failDrop: true}
	srv.store = failing
	srv.txManager.changeLogCap = 1
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	for i := range 2 {
		_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
			EntityType: "Component", Properties: map[string]string{"name": fmt.Sprintf("entity-%d", i)},
			TransactionId: begin.TransactionId,
		})
		if i == 0 && err != nil {
			t.Fatalf("first CreateEntity: %v", err)
		}
	}
	if status.Code(err) != codes.ResourceExhausted || !strings.Contains(err.Error(), "transaction rollback failed") ||
		!strings.Contains(err.Error(), "simulated DropBranchDB failure") {
		t.Fatalf("overflow cleanup error = %v", err)
	}
	state, err := srv.txManager.Lookup(begin.TransactionId)
	if err != nil {
		t.Fatalf("transaction not retained for cleanup retry: %v", err)
	}
	if !state.RollbackOnly {
		t.Fatal("transaction was not marked rollback-only")
	}
	if _, err := base.DumpAllEntities(ctx, begin.TransactionId); err != nil {
		t.Fatalf("branch DB not retained after failed drop: %v", err)
	}
	exists, err := srv.gitstore.BranchExists(ctx, begin.TransactionId)
	if err != nil || !exists {
		t.Fatalf("Git recovery anchor not retained: exists=%v err=%v", exists, err)
	}
	// Subsequent operations on the rolled-back (rollback-only) transaction are
	// rejected with NOT_FOUND — the SPEC error table defines the cap-violation
	// outcome as RESOURCE_EXHAUSTED with the transaction "rolled back", and
	// "Transaction not found" covers "already committed/rolled back". Only
	// RollbackTransaction (below) may still finish the cleanup.
	assertTerminal := func(name string, call func() error) {
		t.Helper()
		if err := call(); status.Code(err) != codes.NotFound {
			t.Fatalf("%s error = %v, want NotFound for rolled-back transaction", name, err)
		}
	}
	assertTerminal("read", func() error {
		_, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
			EntityType: "Component", TransactionId: begin.TransactionId,
		})
		return err
	})
	assertTerminal("mutation", func() error {
		_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
			EntityType: "Component", Properties: map[string]string{"name": "rejected"},
			TransactionId: begin.TransactionId,
		})
		return err
	})
	assertTerminal("diff", func() error {
		_, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: begin.TransactionId})
		return err
	})
	assertTerminal("commit", func() error {
		_, err := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
		return err
	})
	opPub, _ := generateTestKey()
	restarted := NewCartographerServer(
		base, srv.gitstore, opPub, initTestKey(), nil, "", 30*time.Second,
		"test-ns", 30*time.Minute, 1,
	)
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions after failed cleanup: %v", err)
	}
	recovered, err := restarted.txManager.Lookup(begin.TransactionId)
	if err != nil || !recovered.RollbackOnly {
		t.Fatalf("recovered transaction was not rollback-only: state=%+v err=%v", recovered, err)
	}
	_, err = restarted.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("retry RollbackTransaction: %v", err)
	}
	if _, err := restarted.txManager.Lookup(begin.TransactionId); err == nil {
		t.Fatal("transaction remained after cleanup retry")
	}
	if _, err := base.DumpAllEntities(ctx, begin.TransactionId); !errors.Is(err, store.ErrBranchNotFound) {
		t.Fatalf("branch DB remained after cleanup retry: %v", err)
	}
}
