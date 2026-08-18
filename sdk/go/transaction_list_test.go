package flow

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

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

func TestTxListEntities_LosslessIdentityLikeProperties(t *testing.T) {
	mock := &mockCartographerClient{
		listEntities: func(ctx context.Context, req *flowv1.ListEntitiesRequest) (*flowv1.ListEntitiesResponse, error) {
			return &flowv1.ListEntitiesResponse{
				Entities: []*flowv1.Entity{
					{
						EntityId:   "te1",
						EntityType: componentType,
						Properties: map[string]string{
							"entity_id":   clobberedIDProp,
							"entity_type": clobberedTypeProp,
							"name":        "real",
						},
					},
				},
			}, nil
		},
	}
	tx := newMockTx(mock)
	page, err := tx.ListEntities("Component")
	if err != nil {
		t.Fatalf("ListEntities returned error: %v", err)
	}
	if len(page.Entities) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(page.Entities))
	}
	e := page.Entities[0]
	if e.ID != "te1" {
		t.Errorf("expected identity ID te1, got %q", e.ID)
	}
	if e.Type != componentType {
		t.Errorf("expected identity type Component, got %q", e.Type)
	}
	if e.Properties["entity_id"] != clobberedIDProp {
		t.Errorf("expected identity-like property entity_id preserved, got %q", e.Properties["entity_id"])
	}
	if e.Properties["entity_type"] != clobberedTypeProp {
		t.Errorf("expected identity-like property entity_type preserved, got %q", e.Properties["entity_type"])
	}
}

// TestTxListEntities_ReturnsEmbedding pins the Transaction-layer
// ListEntities conversion surfacing the proto Entity message's embedding
// field, matching the write-path conversions.
func TestTxListEntities_ReturnsEmbedding(t *testing.T) {
	mock := &mockCartographerClient{
		listEntities: func(ctx context.Context, req *flowv1.ListEntitiesRequest) (*flowv1.ListEntitiesResponse, error) {
			return &flowv1.ListEntitiesResponse{
				Entities: []*flowv1.Entity{
					{
						EntityId:   "te1",
						EntityType: componentType,
						Embedding:  []float32{0.4, 0.5},
					},
				},
			}, nil
		},
	}
	tx := newMockTx(mock)
	page, err := tx.ListEntities("Component")
	if err != nil {
		t.Fatalf("ListEntities returned error: %v", err)
	}
	if len(page.Entities) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(page.Entities))
	}
	if len(page.Entities[0].Embedding) != 2 ||
		page.Entities[0].Embedding[0] != 0.4 || page.Entities[0].Embedding[1] != 0.5 {
		t.Errorf("expected embedding [0.4 0.5], got %v", page.Entities[0].Embedding)
	}
}

func TestTxListEntities_WildcardOmittedOnEmptyGraph(t *testing.T) {
	var capturedType string
	mock := &mockCartographerClient{
		listEntities: func(ctx context.Context, req *flowv1.ListEntitiesRequest) (*flowv1.ListEntitiesResponse, error) {
			capturedType = req.GetEntityType()
			return &flowv1.ListEntitiesResponse{}, nil
		},
	}
	tx := newMockTx(mock)
	page, err := tx.ListEntities("")
	if err != nil {
		t.Fatalf("ListEntities returned error: %v", err)
	}
	if capturedType != "" {
		t.Errorf("expected omitted entityType (wildcard branch) on request, got %q", capturedType)
	}
	if len(page.Entities) != 0 {
		t.Errorf("expected empty entities on fresh graph, got %d", len(page.Entities))
	}
}
