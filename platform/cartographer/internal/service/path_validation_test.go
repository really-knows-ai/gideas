package service

import (
	"context"
	"strings"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestReadPathTransactionID_Rejected(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git transaction validation")
	}
	applySchemaAndSeed := func(ctx context.Context, t *testing.T, srv *CartographerServer) {
		t.Helper()
		applyTestSchema(ctx, t, srv.store)
		_, _ = srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "a"}, nil, "")
	}

	t.Run("ExecuteCypher invalid transaction id", func(t *testing.T) {
		srv, _ := newTestServer(t)
		applyTestSchema(testCtx(), t, srv.store)
		_, err := srv.ExecuteCypher(testCtx(), &flowv1.ExecuteCypherRequest{
			Cypher:        "MATCH (n:Component) RETURN n",
			TransactionId: "not-a-uuid",
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
		}
	})

	t.Run("ExecuteCypher unknown transaction id", func(t *testing.T) {
		srv, _ := newTestServer(t)
		applySchemaAndSeed(testCtx(), t, srv)
		_, err := srv.ExecuteCypher(testCtx(), &flowv1.ExecuteCypherRequest{
			Cypher:        "MATCH (n:Component) RETURN n",
			TransactionId: "11111111-1111-4111-8111-111111111111",
		})
		if status.Code(err) != codes.NotFound {
			t.Fatalf("expected NotFound, got %v", status.Code(err))
		}
	})

	t.Run("SearchNeighbors invalid transaction id", func(t *testing.T) {
		srv, _ := newTestServer(t)
		applySchemaAndSeed(testCtx(), t, srv)
		_, err := srv.SearchNeighbors(testCtx(), &flowv1.SearchNeighborsRequest{
			Embedding:     []float32{1.0, 2.0, 3.0},
			EntityType:    "Component",
			TopK:          5,
			TransactionId: "not-a-uuid",
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
		}
	})

	t.Run("SearchNeighbors unknown transaction id", func(t *testing.T) {
		srv, _ := newTestServer(t)
		applySchemaAndSeed(testCtx(), t, srv)
		_, err := srv.SearchNeighbors(testCtx(), &flowv1.SearchNeighborsRequest{
			Embedding:     []float32{1.0, 2.0, 3.0},
			EntityType:    "Component",
			TopK:          5,
			TransactionId: "11111111-1111-4111-8111-111111111111",
		})
		if status.Code(err) != codes.NotFound {
			t.Fatalf("expected NotFound, got %v", status.Code(err))
		}
	})

	t.Run("FullTextSearch invalid transaction id", func(t *testing.T) {
		srv, _ := newTestServer(t)
		applySchemaAndSeed(testCtx(), t, srv)
		_, err := srv.FullTextSearch(testCtx(), &flowv1.FullTextSearchRequest{
			Query:         "apple",
			EntityType:    "Component",
			TransactionId: "not-a-uuid",
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
		}
	})

	t.Run("FullTextSearch unknown transaction id", func(t *testing.T) {
		srv, _ := newTestServer(t)
		applySchemaAndSeed(testCtx(), t, srv)
		_, err := srv.FullTextSearch(testCtx(), &flowv1.FullTextSearchRequest{
			Query:         "apple",
			EntityType:    "Component",
			TransactionId: "11111111-1111-4111-8111-111111111111",
		})
		if status.Code(err) != codes.NotFound {
			t.Fatalf("expected NotFound, got %v", status.Code(err))
		}
	})

	t.Run("ListEntities invalid transaction id", func(t *testing.T) {
		srv, _ := newTestServer(t)
		applySchemaAndSeed(testCtx(), t, srv)
		_, err := srv.ListEntities(testCtx(), &flowv1.ListEntitiesRequest{
			EntityType:    "Component",
			PageSize:      10,
			TransactionId: "not-a-uuid",
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
		}
	})

	t.Run("ListEntities unknown transaction id", func(t *testing.T) {
		srv, _ := newTestServer(t)
		applySchemaAndSeed(testCtx(), t, srv)
		_, err := srv.ListEntities(testCtx(), &flowv1.ListEntitiesRequest{
			EntityType:    "Component",
			PageSize:      10,
			TransactionId: "11111111-1111-4111-8111-111111111111",
		})
		if status.Code(err) != codes.NotFound {
			t.Fatalf("expected NotFound, got %v", status.Code(err))
		}
	})
}

// TestMutationTransactionID_Rejected verifies SPEC R2's error-table rows
// "Invalid transaction ID format" (INVALID_ARGUMENT) and "Transaction not
// found" (NOT_FOUND) cover the write path exactly as the read path (SPEC:167-175
// states both rows "cover both read- and write-path operations"): every
// mutation RPC (CreateEntity, UpdateEntity, DeleteEntity, CreateEdge,
// DeleteEdge) rejects a malformed transactionId with INVALID_ARGUMENT and an
// unknown-but-valid transactionId with NOT_FOUND via lockTransactionMutation.
func TestMutationTransactionID_Rejected(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git transaction validation")
	}
	validID := "11111111-1111-4111-8111-111111111111"
	tests := []struct {
		name string
		call func(context.Context, *CartographerServer, string) error
	}{
		{"CreateEntity", func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
				EntityType: "Component", Properties: map[string]string{"name": "x"}, TransactionId: txID,
			})
			return err
		}},
		{"UpdateEntity", func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
				Id: validID, Properties: map[string]string{"version": "2"}, TransactionId: txID,
			})
			return err
		}},
		{"DeleteEntity", func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: validID, TransactionId: txID})
			return err
		}},
		{"CreateEdge", func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
				EdgeType: "DEPENDS_ON", FromEntityId: validID, ToEntityId: validID, TransactionId: txID,
			})
			return err
		}},
		{"DeleteEdge", func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: validID, TransactionId: txID})
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/invalid transaction id", func(t *testing.T) {
			srv, _ := newTestServer(t)
			applyTestSchema(testCtx(), t, srv.store)
			err := tt.call(testCtx(), srv, "not-a-uuid")
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
			}
		})
		t.Run(tt.name+"/unknown transaction id", func(t *testing.T) {
			srv, _ := newTestServer(t)
			applyTestSchema(testCtx(), t, srv.store)
			err := tt.call(testCtx(), srv, validID)
			if status.Code(err) != codes.NotFound {
				t.Fatalf("expected NotFound, got %v (%v)", status.Code(err), err)
			}
		})
	}
}

// TestMutationTransactionID_EmptyRejected is the empty-transactionId sibling of
// TestMutationTransactionID_Rejected: it pins the SPEC error-table row "Write
// outside a transaction → FAILED_PRECONDITION" (SPEC:954) directly at the
// service layer. Every write RPC (CreateEntity, UpdateEntity, DeleteEntity,
// CreateEdge, DeleteEdge) begins with an active-transaction gate that rejects a
// request carrying no transaction ID — the wire's empty transactionId — with
// FAILED_PRECONDITION. The sibling test covers the malformed and unknown-ID
// fault modes; the empty-ID gate is the first check in each handler and has no
// other service-layer test reaching it with a real (non-fake) handler.
func TestMutationTransactionID_EmptyRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git transaction validation")
	}
	tests := []struct {
		name string
		call func(context.Context, *CartographerServer) error
	}{
		{"CreateEntity", func(ctx context.Context, srv *CartographerServer) error {
			_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
				EntityType: "Component", Properties: map[string]string{"name": "x"},
			})
			return err
		}},
		{"UpdateEntity", func(ctx context.Context, srv *CartographerServer) error {
			_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
				Id: testMutationEntityID, Properties: map[string]string{"version": "2"},
			})
			return err
		}},
		{"DeleteEntity", func(ctx context.Context, srv *CartographerServer) error {
			_, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: testMutationEntityID})
			return err
		}},
		{"CreateEdge", func(ctx context.Context, srv *CartographerServer) error {
			_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
				EdgeType: "DEPENDS_ON", FromEntityId: testMutationEntityID, ToEntityId: testMutationEntityID,
			})
			return err
		}},
		{"DeleteEdge", func(ctx context.Context, srv *CartographerServer) error {
			_, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: testMutationEntityID})
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/empty transaction id", func(t *testing.T) {
			srv, _ := newTestServer(t)
			applyTestSchema(testCtx(), t, srv.store)
			err := tt.call(testCtx(), srv)
			if status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("expected FailedPrecondition, got %v (%v)", status.Code(err), err)
			}
		})
	}
}

// TestWriteCheckOrder_TransactionValidationPrecedesStructural pins the SPEC RPC
// check order for the five write RPCs (SPEC:984-988 — every write RPC begins
// with "active transaction"): a request combining an active-transaction
// validation fault (malformed, unknown, timed-out, or rollback-only transaction
// ID) with a structural fault must surface the transaction error — never the
// structural one. Before this fix the lockTransactionMutation gate ran after
// the structural and capability checks, so a nonexistent transaction combined
// with a structural fault surfaced INVALID_ARGUMENT instead of NOT_FOUND.
func TestWriteCheckOrder_TransactionValidationPrecedesStructural(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git transaction validation")
	}
	// Each write RPC with the structural fault that would surface first if the
	// active-transaction gate ran after structural validation.
	writeCases := []struct {
		name string
		call func(context.Context, *CartographerServer, string) error
	}{
		{"CreateEntity", func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
				EntityType: "NoSuchType", Properties: map[string]string{"name": "x"}, TransactionId: txID,
			})
			return err
		}},
		{"UpdateEntity", func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
				Properties: map[string]string{"version": "2"}, TransactionId: txID,
			})
			return err
		}},
		{"DeleteEntity", func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{TransactionId: txID})
			return err
		}},
		{"CreateEdge", func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
				EdgeType: "", FromEntityId: testMutationEntityID, ToEntityId: testMutationEntityID, TransactionId: txID,
			})
			return err
		}},
		{"DeleteEdge", func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{TransactionId: txID})
			return err
		}},
	}
	faultModes := []struct {
		name        string
		prepare     func(t *testing.T, srv *CartographerServer) string
		want        codes.Code
		msgContains string
	}{
		{"malformed transaction id", func(t *testing.T, srv *CartographerServer) string {
			return "not-a-uuid"
		}, codes.InvalidArgument, "transaction"},
		{"unknown transaction id", func(t *testing.T, srv *CartographerServer) string {
			return testMutationEntityID
		}, codes.NotFound, ""},
		{"timed-out transaction", func(t *testing.T, srv *CartographerServer) string {
			fc := newFakeClock(time.Now())
			srv.txManager = NewTransactionManager(7*24*time.Hour, 100000)
			srv.txManager.clock = fc
			txID := beginTestTx(t, srv, testCtx())
			fc.Advance(2 * time.Hour)
			return txID
		}, codes.DeadlineExceeded, ""},
		{"rollback-only transaction", func(t *testing.T, srv *CartographerServer) string {
			txID := beginTestTx(t, srv, testCtx())
			state, err := srv.txManager.Lookup(txID)
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			state.RollbackOnly = true
			return txID
			// A rollback-only transaction is already rolled back (SPEC: the
			// cap-violation outcome is RESOURCE_EXHAUSTED with the transaction
			// "rolled back"), so the transaction gate surfaces NOT_FOUND
			// ("Transaction not found": "was already committed/rolled back").
		}, codes.NotFound, ""},
	}
	for _, wc := range writeCases {
		for _, fm := range faultModes {
			t.Run(wc.name+"/"+fm.name, func(t *testing.T) {
				srv, _ := newTestServer(t)
				txID := fm.prepare(t, srv)
				err := wc.call(testCtx(), srv, txID)
				if status.Code(err) != fm.want {
					t.Fatalf("expected %v, got %v (%v)", fm.want, status.Code(err), err)
				}
				if fm.msgContains != "" && !strings.Contains(err.Error(), fm.msgContains) {
					t.Fatalf("expected error mentioning %q, got %q", fm.msgContains, err.Error())
				}
			})
		}
	}
}

// TestReadPathTransactionID_ScopesToTransaction pins the SPEC R2 read-path
// positive branch (SPEC:189-194: "When present, the operation is scoped to that
// transaction's isolated LadybugDB instance. When absent, the operation reads
// from main") at the service layer: with a valid active transaction, every
// read-path RPC routed through the handler's transactionId wiring
// (ExecuteCypher, SearchNeighbors, FullTextSearch, ListEntities) must observe
// the transaction's branch data, and the unscoped call must observe main's
// (empty) data. TestReadPathTransactionID_Rejected pins the rejection half
// (invalid/unknown transactionId) and the store-layer TestBranch_*Scoped tests
// pin the store primitive; this test pins the handler pass-through.
func TestReadPathTransactionID_ScopesToTransaction(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string", Required: true}}},
			{Name: "VectorType", EnableVectorIndex: true, Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
		},
	}
	if err := srv.store.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	txID := beginTestTx(t, srv, ctx)
	comp, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "gadget"}, TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	vec, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "VectorType", Properties: map[string]string{"name": "vec"},
		Embedding: []float32{1.0, 0.0, 0.0}, TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity vector: %v", err)
	}

	t.Run("ListEntities scoped to branch", func(t *testing.T) {
		scoped, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
			EntityType: "Component", PageSize: 100, TransactionId: txID,
		})
		if err != nil {
			t.Fatalf("scoped ListEntities: %v", err)
		}
		if len(scoped.Entities) != 1 || scoped.Entities[0].EntityId != comp.EntityId {
			t.Fatalf("scoped ListEntities = %+v, want the transaction's entity", scoped.Entities)
		}
		unscoped, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{EntityType: "Component", PageSize: 100})
		if err != nil {
			t.Fatalf("unscoped ListEntities: %v", err)
		}
		if len(unscoped.Entities) != 0 {
			t.Fatalf("unscoped ListEntities saw transaction data: %+v", unscoped.Entities)
		}
	})

	t.Run("ExecuteCypher scoped to branch", func(t *testing.T) {
		scoped, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{
			Cypher: "MATCH (n:Component) RETURN n", TransactionId: txID,
		})
		if err != nil {
			t.Fatalf("scoped ExecuteCypher: %v", err)
		}
		if len(scoped.Rows) != 1 {
			t.Fatalf("scoped ExecuteCypher rows = %d, want 1", len(scoped.Rows))
		}
		unscoped, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: "MATCH (n:Component) RETURN n"})
		if err != nil {
			t.Fatalf("unscoped ExecuteCypher: %v", err)
		}
		if len(unscoped.Rows) != 0 {
			t.Fatalf("unscoped ExecuteCypher saw transaction data: %+v", unscoped.Rows)
		}
	})

	t.Run("FullTextSearch scoped to branch", func(t *testing.T) {
		scoped, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{
			Query: "gadget", EntityType: "Component", TransactionId: txID,
		})
		if err != nil {
			t.Fatalf("scoped FullTextSearch: %v", err)
		}
		if len(scoped.Results) != 1 || scoped.Results[0].EntityId != comp.EntityId {
			t.Fatalf("scoped FullTextSearch = %+v, want the transaction's entity", scoped.Results)
		}
		unscoped, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{Query: "gadget", EntityType: "Component"})
		if err != nil {
			t.Fatalf("unscoped FullTextSearch: %v", err)
		}
		if len(unscoped.Results) != 0 {
			t.Fatalf("unscoped FullTextSearch saw transaction data: %+v", unscoped.Results)
		}
	})

	t.Run("SearchNeighbors scoped to branch", func(t *testing.T) {
		scoped, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
			Embedding: []float32{1.0, 0.0, 0.0}, EntityType: "VectorType", TopK: 5, TransactionId: txID,
		})
		if err != nil {
			t.Fatalf("scoped SearchNeighbors: %v", err)
		}
		if len(scoped.Results) != 1 || scoped.Results[0].EntityId != vec.EntityId {
			t.Fatalf("scoped SearchNeighbors = %+v, want the transaction's entity", scoped.Results)
		}
		unscoped, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
			Embedding: []float32{1.0, 0.0, 0.0}, EntityType: "VectorType", TopK: 5,
		})
		if err != nil {
			t.Fatalf("unscoped SearchNeighbors: %v", err)
		}
		if len(unscoped.Results) != 0 {
			t.Fatalf("unscoped SearchNeighbors saw transaction data: %+v", unscoped.Results)
		}
	})
}
