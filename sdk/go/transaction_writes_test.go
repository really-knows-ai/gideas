package flow

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// noActiveTransactionMsg is the server's transaction-only enforcement
// rejection message (SPEC Phase 1): every write RPC without an active
// transaction is refused with FAILED_PRECONDITION carrying this message.
const noActiveTransactionMsg = "No active transaction"

// ---------------------------------------------------------------------------
// Transaction-only write model: server rejection propagation (Phase 1)
// ---------------------------------------------------------------------------
//
// The Cartographer rejects every write RPC carrying an empty transactionId
// with FAILED_PRECONDITION "No active transaction" (SPEC Phase 1
// transaction-only enforcement; the Graph object no longer exposes mutation
// methods). Each test below pins both halves of the contract on the SDK
// layer: a Transaction handle injects its transaction ID, so the write
// succeeds; a write issued without a transaction (empty transactionId) is
// rejected by the server, and the SDK propagates that rejection verbatim to
// the caller.

func TestCreateEntity_NoTransaction_Rejected(t *testing.T) {
	mock := &mockCartographerClient{
		createEntity: func(ctx context.Context, req *flowv1.CreateEntityRequest) (*flowv1.CreateEntityResponse, error) {
			if req.GetTransactionId() == "" {
				return nil, status.Error(codes.FailedPrecondition, noActiveTransactionMsg)
			}
			return &flowv1.CreateEntityResponse{EntityId: "entity-1", EntityType: componentType}, nil
		},
	}

	// A Transaction handle injects the transaction ID -> the write succeeds.
	tx := newMockTx(mock)
	if _, err := tx.CreateEntity(componentType, nil, nil, nil); err != nil {
		t.Fatalf("CreateEntity through a Transaction handle should succeed: %v", err)
	}

	// Issued without a transaction, the server rejects the write and the SDK
	// surfaces the rejection to the caller.
	noTx := newMockTxWithID(mock, "")
	_, err := noTx.CreateEntity(componentType, nil, nil, nil)
	if err == nil {
		t.Fatal("expected rejection for CreateEntity without an active transaction")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v (%v)", status.Code(err), err)
	}
	if got, want := status.Convert(err).Message(), noActiveTransactionMsg; got != want {
		t.Fatalf("expected server rejection message %q, got %q", want, got)
	}
}

func TestUpdateEntity_NoTransaction_Rejected(t *testing.T) {
	mock := &mockCartographerClient{
		updateEntity: func(ctx context.Context, req *flowv1.UpdateEntityRequest) (*flowv1.UpdateEntityResponse, error) {
			if req.GetTransactionId() == "" {
				return nil, status.Error(codes.FailedPrecondition, noActiveTransactionMsg)
			}
			return &flowv1.UpdateEntityResponse{EntityId: req.GetId()}, nil
		},
	}

	tx := newMockTx(mock)
	if _, err := tx.UpdateEntity(testUUIDEntity, nil, nil); err != nil {
		t.Fatalf("UpdateEntity through a Transaction handle should succeed: %v", err)
	}

	noTx := newMockTxWithID(mock, "")
	_, err := noTx.UpdateEntity(testUUIDEntity, nil, nil)
	if err == nil {
		t.Fatal("expected rejection for UpdateEntity without an active transaction")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v (%v)", status.Code(err), err)
	}
	if got, want := status.Convert(err).Message(), noActiveTransactionMsg; got != want {
		t.Fatalf("expected server rejection message %q, got %q", want, got)
	}
}

//nolint:dupl // DeleteEntity/DeleteEdge rejection tests share the same mock-rejection shape.
func TestDeleteEntity_NoTransaction_Rejected(t *testing.T) {
	mock := &mockCartographerClient{
		deleteEntity: func(ctx context.Context, req *flowv1.DeleteEntityRequest) (*flowv1.DeleteEntityResponse, error) {
			if req.GetTransactionId() == "" {
				return nil, status.Error(codes.FailedPrecondition, noActiveTransactionMsg)
			}
			return &flowv1.DeleteEntityResponse{EntityId: req.GetId()}, nil
		},
	}

	tx := newMockTx(mock)
	if _, err := tx.DeleteEntity(testUUIDEntity); err != nil {
		t.Fatalf("DeleteEntity through a Transaction handle should succeed: %v", err)
	}

	noTx := newMockTxWithID(mock, "")
	_, err := noTx.DeleteEntity(testUUIDEntity)
	if err == nil {
		t.Fatal("expected rejection for DeleteEntity without an active transaction")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v (%v)", status.Code(err), err)
	}
	if got, want := status.Convert(err).Message(), noActiveTransactionMsg; got != want {
		t.Fatalf("expected server rejection message %q, got %q", want, got)
	}
}

func TestCreateEdge_NoTransaction_Rejected(t *testing.T) {
	mock := &mockCartographerClient{
		createEdge: func(ctx context.Context, req *flowv1.CreateEdgeRequest) (*flowv1.CreateEdgeResponse, error) {
			if req.GetTransactionId() == "" {
				return nil, status.Error(codes.FailedPrecondition, noActiveTransactionMsg)
			}
			return &flowv1.CreateEdgeResponse{
				EdgeId: "edge-1", FromEntityId: req.GetFromEntityId(), ToEntityId: req.GetToEntityId(),
			}, nil
		},
	}

	tx := newMockTx(mock)
	if _, err := tx.CreateEdge("DEPENDS_ON", testUUIDFrom, testUUIDTo, nil); err != nil {
		t.Fatalf("CreateEdge through a Transaction handle should succeed: %v", err)
	}

	noTx := newMockTxWithID(mock, "")
	_, err := noTx.CreateEdge("DEPENDS_ON", testUUIDFrom, testUUIDTo, nil)
	if err == nil {
		t.Fatal("expected rejection for CreateEdge without an active transaction")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v (%v)", status.Code(err), err)
	}
	if got, want := status.Convert(err).Message(), noActiveTransactionMsg; got != want {
		t.Fatalf("expected server rejection message %q, got %q", want, got)
	}
}

//nolint:dupl // DeleteEntity/DeleteEdge rejection tests share the same mock-rejection shape.
func TestDeleteEdge_NoTransaction_Rejected(t *testing.T) {
	mock := &mockCartographerClient{
		deleteEdge: func(ctx context.Context, req *flowv1.DeleteEdgeRequest) (*flowv1.DeleteEdgeResponse, error) {
			if req.GetTransactionId() == "" {
				return nil, status.Error(codes.FailedPrecondition, noActiveTransactionMsg)
			}
			return &flowv1.DeleteEdgeResponse{EdgeId: req.GetId()}, nil
		},
	}

	tx := newMockTx(mock)
	if _, err := tx.DeleteEdge(testUUIDEdge); err != nil {
		t.Fatalf("DeleteEdge through a Transaction handle should succeed: %v", err)
	}

	noTx := newMockTxWithID(mock, "")
	_, err := noTx.DeleteEdge(testUUIDEdge)
	if err == nil {
		t.Fatal("expected rejection for DeleteEdge without an active transaction")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v (%v)", status.Code(err), err)
	}
	if got, want := status.Convert(err).Message(), noActiveTransactionMsg; got != want {
		t.Fatalf("expected server rejection message %q, got %q", want, got)
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
	tx.idTypeMap.store(testUUIDEntity, "Component")
	_, err := tx.UpdateEntity(testUUIDEntity, nil, nil)
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
	_, err := tx.DeleteEntity(testUUIDEntity)
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
	_, err := tx.CreateEdge("DEPENDS_ON", testUUIDFrom, testUUIDTo, nil)
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
	_, err := tx.DeleteEdge(testUUIDEdge)
	if err != nil {
		t.Fatalf("DeleteEdge returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected transaction ID tx-1, got %q", capturedTxID)
	}
}
