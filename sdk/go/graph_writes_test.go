package flow

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Transaction write-method tests
// ---------------------------------------------------------------------------
//
// The write-path tests construct a Transaction handle directly via newMockTx:
// mutation methods exist only on the Transaction surface (SPEC R4 — the Graph
// exposes read and administrative methods only), so each test exercises the
// Transaction handle in isolation from the Graph that produced it.

func TestCreateEntity(t *testing.T) {
	mock := &mockCartographerClient{
		createEntity: func(ctx context.Context, req *flowv1.CreateEntityRequest) (*flowv1.CreateEntityResponse, error) {
			return &flowv1.CreateEntityResponse{
				EntityId:   "test-id",
				EntityType: componentType,
				Properties: req.GetProperties(),
			}, nil
		},
	}
	tx := newMockTx(mock)
	props := map[string]string{"name": "test"}
	entity, err := tx.CreateEntity(componentType, nil, props, nil)
	if err != nil {
		t.Fatalf("CreateEntity returned error: %v", err)
	}
	if entity.ID != "test-id" {
		t.Errorf("expected entity ID test-id, got %s", entity.ID)
	}
	if entity.Type != componentType {
		t.Errorf("expected entity type Component, got %s", entity.Type)
	}
	if entity.Properties["name"] != "test" {
		t.Errorf("expected name=test, got %v", entity.Properties)
	}
}

func TestCreateEntity_NilIDSendsEmpty(t *testing.T) {
	var capturedID string
	mock := &mockCartographerClient{
		createEntity: func(ctx context.Context, req *flowv1.CreateEntityRequest) (*flowv1.CreateEntityResponse, error) {
			capturedID = req.GetId()
			return &flowv1.CreateEntityResponse{
				EntityId:   "generated-id",
				EntityType: "Component",
			}, nil
		},
	}
	tx := newMockTx(mock)
	entity, err := tx.CreateEntity("Component", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateEntity returned error: %v", err)
	}
	if capturedID != "" {
		t.Errorf("expected empty id in request, got %q", capturedID)
	}
	if entity.ID != "generated-id" {
		t.Errorf("expected generated ID, got %s", entity.ID)
	}
}

// TestCreateEntity_PopulatesMap pins SPEC R3's ID-to-type cache population
// from creation responses on the Transaction layer: the created entity's
// response type is recorded keyed by entity ID.
func TestCreateEntity_PopulatesMap(t *testing.T) {
	mock := &mockCartographerClient{
		createEntity: func(ctx context.Context, req *flowv1.CreateEntityRequest) (*flowv1.CreateEntityResponse, error) {
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
	typ, ok := tx.idTypeMap.resolve("entity-1")
	if !ok || typ != componentType {
		t.Errorf("expected Component type for entity-1, got %q (ok=%v)", typ, ok)
	}
}

func TestUpdateEntity(t *testing.T) {
	mock := &mockCartographerClient{
		updateEntity: func(ctx context.Context, req *flowv1.UpdateEntityRequest) (*flowv1.UpdateEntityResponse, error) {
			return &flowv1.UpdateEntityResponse{
				EntityId:   req.GetId(),
				EntityType: "Component",
				Properties: req.GetProperties(),
			}, nil
		},
	}
	tx := newMockTx(mock)
	tx.idTypeMap.store(testUUIDEntity, "Component")
	entity, err := tx.UpdateEntity(testUUIDEntity, map[string]string{"name": "updated"}, nil)
	if err != nil {
		t.Fatalf("UpdateEntity returned error: %v", err)
	}
	if entity.ID != testUUIDEntity {
		t.Errorf("expected entity ID %s, got %s", testUUIDEntity, entity.ID)
	}
}

func TestDeleteEntity(t *testing.T) {
	mock := &mockCartographerClient{
		deleteEntity: func(ctx context.Context, req *flowv1.DeleteEntityRequest) (*flowv1.DeleteEntityResponse, error) {
			return &flowv1.DeleteEntityResponse{
				EntityId:   req.GetId(),
				EntityType: "Component",
			}, nil
		},
	}
	tx := newMockTx(mock)
	tx.idTypeMap.store(testUUIDEntity, "Component")
	entity, err := tx.DeleteEntity(testUUIDEntity)
	if err != nil {
		t.Fatalf("DeleteEntity returned error: %v", err)
	}
	if entity.ID != testUUIDEntity {
		t.Errorf("expected entity ID %s, got %s", testUUIDEntity, entity.ID)
	}
	// Verify removed from map
	_, ok := tx.idTypeMap.resolve(testUUIDEntity)
	if ok {
		t.Error("expected entity to be removed from map after delete")
	}
}

func TestDeleteEntity_ReturnsEmbedding(t *testing.T) {
	mock := &mockCartographerClient{
		deleteEntity: func(ctx context.Context, req *flowv1.DeleteEntityRequest) (*flowv1.DeleteEntityResponse, error) {
			return &flowv1.DeleteEntityResponse{
				EntityId:   req.GetId(),
				EntityType: "Component",
				Embedding:  []float32{0.1, 0.2, 0.3},
			}, nil
		},
	}
	tx := newMockTx(mock)
	entity, err := tx.DeleteEntity(testUUIDEntity)
	if err != nil {
		t.Fatalf("DeleteEntity returned error: %v", err)
	}
	if len(entity.Embedding) != 3 || entity.Embedding[0] != 0.1 || entity.Embedding[2] != 0.3 {
		t.Errorf("expected embedding [0.1 0.2 0.3], got %v", entity.Embedding)
	}
}

func TestCreateEdge(t *testing.T) {
	mock := &mockCartographerClient{
		createEdge: func(ctx context.Context, req *flowv1.CreateEdgeRequest) (*flowv1.CreateEdgeResponse, error) {
			return &flowv1.CreateEdgeResponse{
				EdgeId:       testUUIDEdge,
				EdgeType:     "DEPENDS_ON",
				FromEntityId: req.GetFromEntityId(),
				ToEntityId:   req.GetToEntityId(),
			}, nil
		},
	}
	tx := newMockTx(mock)
	tx.idTypeMap.store(testUUIDFrom, "Component")
	edge, err := tx.CreateEdge("DEPENDS_ON", testUUIDFrom, testUUIDTo, nil)
	if err != nil {
		t.Fatalf("CreateEdge returned error: %v", err)
	}
	if edge.ID != testUUIDEdge {
		t.Errorf("expected edge ID %s, got %s", testUUIDEdge, edge.ID)
	}
	if edge.FromEntityID != testUUIDFrom {
		t.Errorf("expected from entity %s, got %s", testUUIDFrom, edge.FromEntityID)
	}
}

func TestDeleteEdge(t *testing.T) {
	mock := &mockCartographerClient{
		deleteEdge: func(ctx context.Context, req *flowv1.DeleteEdgeRequest) (*flowv1.DeleteEdgeResponse, error) {
			return &flowv1.DeleteEdgeResponse{
				EdgeId:       req.GetId(),
				EdgeType:     "DEPENDS_ON",
				FromEntityId: "from-1",
				ToEntityId:   "to-1",
			}, nil
		},
	}
	tx := newMockTx(mock)
	edge, err := tx.DeleteEdge(testUUIDEdge)
	if err != nil {
		t.Fatalf("DeleteEdge returned error: %v", err)
	}
	if edge.ID != testUUIDEdge {
		t.Errorf("expected edge ID %s, got %s", testUUIDEdge, edge.ID)
	}
}
