package flow

import (
	"context"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

func newMockTx(mock *mockCartographerClient) *Transaction {
	ctx, cancel := context.WithCancel(context.Background())
	sess := &session{
		Cartographer: mock,
		ctx:          ctx,
		cancel:       cancel,
	}
	return &Transaction{
		session:   sess,
		id:        embassyTestTxID,
		idTypeMap: newIDTypeMap(),
	}
}

func TestCreateEntityInTx(t *testing.T) {
	var capturedTxID string
	mock := &mockCartographerClient{
		createEntity: func(ctx context.Context, req *flowv1.CreateEntityRequest) (*flowv1.CreateEntityResponse, error) {
			capturedTxID = req.GetTransactionId()
			return &flowv1.CreateEntityResponse{
				EntityId:   "entity-1",
				EntityType: "Component",
			}, nil
		},
	}
	tx := newMockTx(mock)
	_, err := tx.CreateEntity("Component", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateEntity returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected transaction ID tx-1, got %q", capturedTxID)
	}
}

func TestUpdateEntityInTx(t *testing.T) {
	var capturedTxID string
	mock := &mockCartographerClient{
		updateEntity: func(ctx context.Context, req *flowv1.UpdateEntityRequest) (*flowv1.UpdateEntityResponse, error) {
			capturedTxID = req.GetTransactionId()
			return &flowv1.UpdateEntityResponse{EntityId: req.GetId()}, nil
		},
	}
	tx := newMockTx(mock)
	tx.idTypeMap.store("e1", "Component")
	_, err := tx.UpdateEntity("e1", nil, nil)
	if err != nil {
		t.Fatalf("UpdateEntity returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected transaction ID tx-1, got %q", capturedTxID)
	}
}

func TestDeleteEntityInTx(t *testing.T) {
	var capturedTxID string
	mock := &mockCartographerClient{
		deleteEntity: func(ctx context.Context, req *flowv1.DeleteEntityRequest) (*flowv1.DeleteEntityResponse, error) {
			capturedTxID = req.GetTransactionId()
			return &flowv1.DeleteEntityResponse{EntityId: req.GetId()}, nil
		},
	}
	tx := newMockTx(mock)
	_, err := tx.DeleteEntity("e1")
	if err != nil {
		t.Fatalf("DeleteEntity returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected transaction ID tx-1, got %q", capturedTxID)
	}
}

func TestCreateEdgeInTx(t *testing.T) {
	var capturedTxID string
	mock := &mockCartographerClient{
		createEdge: func(ctx context.Context, req *flowv1.CreateEdgeRequest) (*flowv1.CreateEdgeResponse, error) {
			capturedTxID = req.GetTransactionId()
			return &flowv1.CreateEdgeResponse{
				EdgeId:       "edge-1",
				FromEntityId: req.GetFromEntityId(),
				ToEntityId:   req.GetToEntityId(),
			}, nil
		},
	}
	tx := newMockTx(mock)
	_, err := tx.CreateEdge("DEPENDS_ON", "from-1", "to-1", nil)
	if err != nil {
		t.Fatalf("CreateEdge returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected transaction ID tx-1, got %q", capturedTxID)
	}
}

func TestDeleteEdgeInTx(t *testing.T) {
	var capturedTxID string
	mock := &mockCartographerClient{
		deleteEdge: func(ctx context.Context, req *flowv1.DeleteEdgeRequest) (*flowv1.DeleteEdgeResponse, error) {
			capturedTxID = req.GetTransactionId()
			return &flowv1.DeleteEdgeResponse{EdgeId: req.GetId()}, nil
		},
	}
	tx := newMockTx(mock)
	_, err := tx.DeleteEdge("edge-1")
	if err != nil {
		t.Fatalf("DeleteEdge returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected transaction ID tx-1, got %q", capturedTxID)
	}
}

func TestTxExecuteCypher(t *testing.T) {
	var capturedTxID string
	mock := &mockCartographerClient{
		executeCypher: func(ctx context.Context, req *flowv1.ExecuteCypherRequest) (*flowv1.ExecuteCypherResponse, error) {
			capturedTxID = req.GetTransactionId()
			return &flowv1.ExecuteCypherResponse{}, nil
		},
	}
	tx := newMockTx(mock)
	_, err := tx.ExecuteCypher("MATCH (c:Component) RETURN c", nil)
	if err != nil {
		t.Fatalf("ExecuteCypher returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected transaction ID tx-1, got %q", capturedTxID)
	}
}

func TestTxSearchNeighbors(t *testing.T) {
	var capturedTxID string
	mock := &mockCartographerClient{
		searchNeighbors: func(ctx context.Context,
			req *flowv1.SearchNeighborsRequest,
		) (*flowv1.SearchNeighborsResponse, error) {
			capturedTxID = req.GetTransactionId()
			return &flowv1.SearchNeighborsResponse{}, nil
		},
	}
	tx := newMockTx(mock)
	_, err := tx.SearchNeighbors([]float32{0.1}, "Component", 10)
	if err != nil {
		t.Fatalf("SearchNeighbors returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected transaction ID tx-1, got %q", capturedTxID)
	}
}

func TestTxFullTextSearch(t *testing.T) {
	var capturedTxID string
	mock := &mockCartographerClient{
		fullTextSearch: func(ctx context.Context, req *flowv1.FullTextSearchRequest) (*flowv1.FullTextSearchResponse, error) {
			capturedTxID = req.GetTransactionId()
			return &flowv1.FullTextSearchResponse{}, nil
		},
	}
	tx := newMockTx(mock)
	_, err := tx.FullTextSearch("query", "Component")
	if err != nil {
		t.Fatalf("FullTextSearch returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected transaction ID tx-1, got %q", capturedTxID)
	}
}

func TestTxListEntities(t *testing.T) {
	var capturedTxID string
	mock := &mockCartographerClient{
		listEntities: func(ctx context.Context, req *flowv1.ListEntitiesRequest) (*flowv1.ListEntitiesResponse, error) {
			capturedTxID = req.GetTransactionId()
			return &flowv1.ListEntitiesResponse{}, nil
		},
	}
	tx := newMockTx(mock)
	_, err := tx.ListEntities("Component")
	if err != nil {
		t.Fatalf("ListEntities returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected transaction ID tx-1, got %q", capturedTxID)
	}
}

func TestDiff(t *testing.T) {
	var capturedTxID string
	mock := &mockCartographerClient{
		getTxDiff: func(ctx context.Context,
			req *flowv1.GetTransactionDiffRequest,
		) (*flowv1.GetTransactionDiffResponse, error) {
			capturedTxID = req.GetTransactionId()
			return &flowv1.GetTransactionDiffResponse{}, nil
		},
	}
	tx := newMockTx(mock)
	_, err := tx.Diff()
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected transaction ID tx-1, got %q", capturedTxID)
	}
}

func TestRefresh(t *testing.T) {
	var capturedTxID string
	mock := &mockCartographerClient{
		refreshTx: func(ctx context.Context,
			req *flowv1.RefreshTransactionRequest,
		) (*flowv1.RefreshTransactionResponse, error) {
			capturedTxID = req.GetTransactionId()
			return &flowv1.RefreshTransactionResponse{}, nil
		},
	}
	tx := newMockTx(mock)
	err := tx.Refresh()
	if err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected transaction ID tx-1, got %q", capturedTxID)
	}
}

func TestCommit(t *testing.T) {
	var capturedTxID string
	mock := &mockCartographerClient{
		commitTx: func(ctx context.Context, req *flowv1.CommitTransactionRequest) (*flowv1.CommitTransactionResponse, error) {
			capturedTxID = req.GetTransactionId()
			return &flowv1.CommitTransactionResponse{}, nil
		},
	}
	tx := newMockTx(mock)
	err := tx.Commit()
	if err != nil {
		t.Fatalf("Commit returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected transaction ID tx-1, got %q", capturedTxID)
	}
}

func TestRollback(t *testing.T) {
	var capturedTxID string
	called := false
	mock := &mockCartographerClient{
		rollbackTx: func(
			ctx context.Context,
			req *flowv1.RollbackTransactionRequest,
		) (*flowv1.RollbackTransactionResponse, error) {
			capturedTxID = req.GetTransactionId()
			called = true
			return &flowv1.RollbackTransactionResponse{}, nil
		},
	}
	tx := newMockTx(mock)
	err := tx.Rollback()
	if err != nil {
		t.Fatalf("Rollback returned error: %v", err)
	}
	if !called {
		t.Error("expected RollbackTransaction to be called")
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected transaction ID tx-1, got %q", capturedTxID)
	}
	if !tx.rolledBack {
		t.Error("expected rolledBack flag to be set")
	}
}

func TestRollback_Idempotent(t *testing.T) {
	callCount := 0
	mock := &mockCartographerClient{
		rollbackTx: func(
			ctx context.Context,
			req *flowv1.RollbackTransactionRequest,
		) (*flowv1.RollbackTransactionResponse, error) {
			callCount++
			return &flowv1.RollbackTransactionResponse{}, nil
		},
	}
	tx := newMockTx(mock)
	_ = tx.Rollback()
	err := tx.Rollback() // second call should not error (idempotent)
	if err != nil {
		t.Fatalf("second Rollback returned error: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected RollbackTransaction to be called once, got %d", callCount)
	}
}

func TestExtendTimeout_RejectsNonPositive(t *testing.T) {
	tx := newMockTx(&mockCartographerClient{})
	_, err := tx.ExtendTimeout(0)
	if err == nil {
		t.Error("expected error for zero duration")
	}
	_, err = tx.ExtendTimeout(-1 * time.Second)
	if err == nil {
		t.Error("expected error for negative duration")
	}
}

func TestTxMethodsAfterRollback(t *testing.T) {
	tx := newMockTx(&mockCartographerClient{})
	tx.rolledBack = true

	tests := []struct {
		name string
		fn   func() error
	}{
		{"CreateEntity", func() error { _, err := tx.CreateEntity("", nil, nil, nil); return err }},
		{"UpdateEntity", func() error { _, err := tx.UpdateEntity("", nil, nil); return err }},
		{"DeleteEntity", func() error { _, err := tx.DeleteEntity(""); return err }},
		{"CreateEdge", func() error { _, err := tx.CreateEdge("", "", "", nil); return err }},
		{"DeleteEdge", func() error { _, err := tx.DeleteEdge(""); return err }},
		{"ExecuteCypher", func() error { _, err := tx.ExecuteCypher("", nil); return err }},
		{"SearchNeighbors", func() error { _, err := tx.SearchNeighbors(nil, "", 0); return err }},
		{"FullTextSearch", func() error { _, err := tx.FullTextSearch("", ""); return err }},
		{"ListEntities", func() error { _, err := tx.ListEntities(""); return err }},
		{"Diff", func() error { _, err := tx.Diff(); return err }},
		{"Refresh", func() error { return tx.Refresh() }},
		{"Commit", func() error { return tx.Commit() }},
		{"ExtendTimeout", func() error { _, err := tx.ExtendTimeout(time.Second); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.fn(); err == nil {
				t.Error("expected error after rollback")
			}
		})
	}
}

func TestTransaction_InheritsMapSnapshot(t *testing.T) {
	g := &Graph{idTypeMap: newIDTypeMap()}
	g.idTypeMap.store("e1", "Component")

	mock := &mockCartographerClient{
		beginTx: func(ctx context.Context, req *flowv1.BeginTransactionRequest) (*flowv1.BeginTransactionResponse, error) {
			return &flowv1.BeginTransactionResponse{
				TransactionId: embassyTestTxID,
			}, nil
		},
	}
	g.session = &session{Cartographer: mock}

	tx, err := g.BeginTransaction()
	if err != nil {
		t.Fatalf("BeginTransaction returned error: %v", err)
	}

	// Transaction should inherit the map
	typ, ok := tx.idTypeMap.resolve("e1")
	if !ok || typ != "Component" {
		t.Errorf("expected Component for e1 in tx, got %q (ok=%v)", typ, ok)
	}

	// Adding to graph should not affect tx
	g.idTypeMap.store("e2", "Service")
	_, ok = tx.idTypeMap.resolve("e2")
	if ok {
		t.Error("expected e2 to NOT be in tx map snapshot")
	}
}
