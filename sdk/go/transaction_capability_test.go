package flow

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/metadata"
)

// ---------------------------------------------------------------------------
// Capability annotation tests (Transaction write methods with unknown entity IDs)
// ---------------------------------------------------------------------------

//nolint:dupl // Transaction and Graph wildcard metadata tests share structure.
func TestTxUpdateEntity_UnknownIDSendsWildcard(t *testing.T) {
	var capturedKey, capturedValue string
	mock := &mockCartographerClient{
		updateEntity: func(ctx context.Context, req *flowv1.UpdateEntityRequest) (*flowv1.UpdateEntityResponse, error) {
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatal("no outgoing metadata")
			}
			vals := md.Get(metadataEntityTypeKey)
			if len(vals) == 0 {
				t.Fatal("no entity_type metadata")
			}
			capturedKey = metadataEntityTypeKey
			capturedValue = vals[0]
			return &flowv1.UpdateEntityResponse{EntityId: req.GetId(), EntityType: "Component"}, nil
		},
	}
	tx := newMockTx(mock)
	// testUUIDEntity is NOT in the tx map -> should produce wildcard
	_, err := tx.UpdateEntity(testUUIDEntity, nil, nil)
	if err != nil {
		t.Fatalf("UpdateEntity returned error: %v", err)
	}
	if capturedKey != metadataEntityTypeKey {
		t.Errorf("expected metadata key entity_type, got %q", capturedKey)
	}
	if capturedValue != "*" {
		t.Errorf("expected wildcard *, got %q", capturedValue)
	}
}

// TestTxUpdateEntity_ResolvedTypeAnnotation proves SPEC R3's mode-1
// resolution on the Transaction path: when the entity ID IS in the local
// ID-to-type map, the capability annotation carries the resolved concrete
// <type>, not the wildcard, enabling the Sidecar to block on a specific
// type mismatch.
func TestTxUpdateEntity_ResolvedTypeAnnotation(t *testing.T) {
	var capturedKey, capturedValue string
	mock := &mockCartographerClient{
		updateEntity: func(ctx context.Context, req *flowv1.UpdateEntityRequest) (*flowv1.UpdateEntityResponse, error) {
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatal("no outgoing metadata")
			}
			vals := md.Get(metadataEntityTypeKey)
			if len(vals) == 0 {
				t.Fatal("no entity_type metadata")
			}
			capturedKey = metadataEntityTypeKey
			capturedValue = vals[0]
			return &flowv1.UpdateEntityResponse{EntityId: req.GetId(), EntityType: componentType}, nil
		},
	}
	tx := newMockTx(mock)
	// testUUIDEntity IS in the tx map -> annotation must carry the resolved type.
	tx.idTypeMap.store(testUUIDEntity, componentType)
	_, err := tx.UpdateEntity(testUUIDEntity, nil, nil)
	if err != nil {
		t.Fatalf("UpdateEntity returned error: %v", err)
	}
	if capturedKey != metadataEntityTypeKey {
		t.Errorf("expected metadata key entity_type, got %q", capturedKey)
	}
	if capturedValue != componentType {
		t.Errorf("expected resolved type %q in annotation, got %q", componentType, capturedValue)
	}
}

func TestTxDeleteEntity_UnknownIDSendsWildcard(t *testing.T) {
	var capturedKey, capturedValue string
	mock := &mockCartographerClient{
		deleteEntity: func(ctx context.Context, req *flowv1.DeleteEntityRequest) (*flowv1.DeleteEntityResponse, error) {
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatal("no outgoing metadata")
			}
			vals := md.Get(metadataEntityTypeKey)
			if len(vals) == 0 {
				t.Fatal("no entity_type metadata")
			}
			capturedKey = metadataEntityTypeKey
			capturedValue = vals[0]
			return &flowv1.DeleteEntityResponse{EntityId: req.GetId()}, nil
		},
	}
	tx := newMockTx(mock)
	// testUUIDEntity is NOT in the tx map -> should produce wildcard
	_, err := tx.DeleteEntity(testUUIDEntity)
	if err != nil {
		t.Fatalf("DeleteEntity returned error: %v", err)
	}
	if capturedKey != metadataEntityTypeKey {
		t.Errorf("expected metadata key entity_type, got %q", capturedKey)
	}
	if capturedValue != "*" {
		t.Errorf("expected wildcard *, got %q", capturedValue)
	}
}

func TestTxCreateEdge_UnknownFromIDSendsWildcard(t *testing.T) {
	var capturedKey, capturedValue string
	mock := &mockCartographerClient{
		createEdge: func(ctx context.Context, req *flowv1.CreateEdgeRequest) (*flowv1.CreateEdgeResponse, error) {
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatal("no outgoing metadata")
			}
			vals := md.Get(metadataEntityTypeKey)
			if len(vals) == 0 {
				t.Fatal("no entity_type metadata")
			}
			capturedKey = metadataEntityTypeKey
			capturedValue = vals[0]
			return &flowv1.CreateEdgeResponse{
				EdgeId: "edge-1", FromEntityId: req.GetFromEntityId(), ToEntityId: req.GetToEntityId(),
			}, nil
		},
	}
	tx := newMockTx(mock)
	// testUUIDFrom is NOT in the tx map -> should produce wildcard
	_, err := tx.CreateEdge("DEPENDS_ON", testUUIDFrom, testUUIDTo, nil)
	if err != nil {
		t.Fatalf("CreateEdge returned error: %v", err)
	}
	if capturedKey != metadataEntityTypeKey {
		t.Errorf("expected metadata key entity_type, got %q", capturedKey)
	}
	if capturedValue != "*" {
		t.Errorf("expected wildcard *, got %q", capturedValue)
	}
}

// TestTxDeleteEntity_ResolvedTypeAnnotation proves SPEC R3's mode-1
// resolution on the Transaction write path: when the entity ID IS in the
// local ID-to-type map, DeleteEntity's capability annotation carries the
// resolved concrete <type>, not the wildcard, enabling the Sidecar to block
// on a specific <type> mismatch.
//
//nolint:dupl // Transaction and Graph resolved-type metadata tests share structure.
func TestTxDeleteEntity_ResolvedTypeAnnotation(t *testing.T) {
	var capturedKey, capturedValue string
	mock := &mockCartographerClient{
		deleteEntity: func(ctx context.Context, req *flowv1.DeleteEntityRequest) (*flowv1.DeleteEntityResponse, error) {
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatal("no outgoing metadata")
			}
			vals := md.Get(metadataEntityTypeKey)
			if len(vals) == 0 {
				t.Fatal("no entity_type metadata")
			}
			capturedKey = metadataEntityTypeKey
			capturedValue = vals[0]
			return &flowv1.DeleteEntityResponse{EntityId: req.GetId()}, nil
		},
	}
	tx := newMockTx(mock)
	// testUUIDEntity IS in the tx map -> annotation must carry the resolved type.
	tx.idTypeMap.store(testUUIDEntity, componentType)
	_, err := tx.DeleteEntity(testUUIDEntity)
	if err != nil {
		t.Fatalf("DeleteEntity returned error: %v", err)
	}
	if capturedKey != metadataEntityTypeKey {
		t.Errorf("expected metadata key entity_type, got %q", capturedKey)
	}
	if capturedValue != componentType {
		t.Errorf("expected resolved type %q in annotation, got %q", componentType, capturedValue)
	}
}

// TestTxCreateEdge_ResolvedTypeAnnotation proves SPEC R3's mode-1 resolution
// on the Transaction write path: CreateEdge resolves the SOURCE entity type
// from the local ID-to-type map, so when the from-entity ID IS known the
// annotation carries the resolved concrete <type>, not the wildcard.
//
//nolint:dupl // Transaction and Graph resolved-type metadata tests share structure.
func TestTxCreateEdge_ResolvedTypeAnnotation(t *testing.T) {
	var capturedKey, capturedValue string
	mock := &mockCartographerClient{
		createEdge: func(ctx context.Context, req *flowv1.CreateEdgeRequest) (*flowv1.CreateEdgeResponse, error) {
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatal("no outgoing metadata")
			}
			vals := md.Get(metadataEntityTypeKey)
			if len(vals) == 0 {
				t.Fatal("no entity_type metadata")
			}
			capturedKey = metadataEntityTypeKey
			capturedValue = vals[0]
			return &flowv1.CreateEdgeResponse{
				EdgeId: "edge-1", FromEntityId: req.GetFromEntityId(), ToEntityId: req.GetToEntityId(),
			}, nil
		},
	}
	tx := newMockTx(mock)
	// testUUIDFrom IS in the tx map -> annotation must carry the resolved type.
	tx.idTypeMap.store(testUUIDFrom, componentType)
	_, err := tx.CreateEdge("DEPENDS_ON", testUUIDFrom, testUUIDTo, nil)
	if err != nil {
		t.Fatalf("CreateEdge returned error: %v", err)
	}
	if capturedKey != metadataEntityTypeKey {
		t.Errorf("expected metadata key entity_type, got %q", capturedKey)
	}
	if capturedValue != componentType {
		t.Errorf("expected resolved type %q in annotation, got %q", componentType, capturedValue)
	}
}

func TestTxDeleteEdge_SendsKeyAndWildcard(t *testing.T) {
	var capturedKey, capturedValue string
	mock := &mockCartographerClient{
		deleteEdge: func(ctx context.Context, req *flowv1.DeleteEdgeRequest) (*flowv1.DeleteEdgeResponse, error) {
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatal("no outgoing metadata")
			}
			vals := md.Get(metadataEntityTypeKey)
			if len(vals) == 0 {
				t.Fatal("no entity_type metadata")
			}
			capturedKey = metadataEntityTypeKey
			capturedValue = vals[0]
			return &flowv1.DeleteEdgeResponse{
				EdgeId: req.GetId(), EdgeType: "DEPENDS_ON",
			}, nil
		},
	}
	tx := newMockTx(mock)
	_, err := tx.DeleteEdge(testUUIDEdge)
	if err != nil {
		t.Fatalf("DeleteEdge returned error: %v", err)
	}
	if capturedKey != metadataEntityTypeKey {
		t.Errorf("expected metadata key entity_type, got %q", capturedKey)
	}
	if capturedValue != "*" {
		t.Errorf("expected wildcard *, got %q", capturedValue)
	}
}

// TestTxUpdateEntity_ResponseTypeSeedsMap pins SPEC R3's opportunistic
// ID-to-type mapping population from update responses (SPEC R3:252 — "entity
// creation/update responses, query results, and list results all carry the
// entity type, which the SDK records keyed by entity ID"): an UpdateEntity
// whose response carries a non-empty EntityType records that ID→type mapping
// WITHOUT any explicit seeding, so a subsequent call on the same ID resolves
// the concrete type (mode-1 annotation) instead of the wildcard.
func TestTxUpdateEntity_ResponseTypeSeedsMap(t *testing.T) {
	var capturedValue string
	mock := &mockCartographerClient{
		updateEntity: func(ctx context.Context, req *flowv1.UpdateEntityRequest) (*flowv1.UpdateEntityResponse, error) {
			return &flowv1.UpdateEntityResponse{EntityId: req.GetId(), EntityType: componentType}, nil
		},
		deleteEntity: func(ctx context.Context, req *flowv1.DeleteEntityRequest) (*flowv1.DeleteEntityResponse, error) {
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatal("no outgoing metadata")
			}
			vals := md.Get(metadataEntityTypeKey)
			if len(vals) == 0 {
				t.Fatal("no entity_type metadata")
			}
			capturedValue = vals[0]
			return &flowv1.DeleteEntityResponse{EntityId: req.GetId()}, nil
		},
	}
	tx := newMockTx(mock)

	// No seeding: the ID is unknown to the map. The UpdateEntity response
	// (EntityType=Component) must record the mapping from the SDK's own
	// traffic.
	if _, err := tx.UpdateEntity(testUUIDEntity, nil, nil); err != nil {
		t.Fatalf("UpdateEntity returned error: %v", err)
	}

	// A subsequent call on the same ID must resolve Component (mode-1),
	// not fall back to the "*" wildcard.
	if _, err := tx.DeleteEntity(testUUIDEntity); err != nil {
		t.Fatalf("DeleteEntity returned error: %v", err)
	}
	if capturedValue != componentType {
		t.Errorf("expected subsequent call to resolve %q from the update response, got %q", componentType, capturedValue)
	}
}

// TestTxUpdateEntity_EmptyResponseTypeDoesNotSeedMap pins the complementary
// half of SPEC R3's update-response mapping: an UpdateEntity response with an
// EMPTY EntityType records nothing, so a subsequent call on that ID still
// resolves to the "*" wildcard.
func TestTxUpdateEntity_EmptyResponseTypeDoesNotSeedMap(t *testing.T) {
	var capturedValue string
	mock := &mockCartographerClient{
		updateEntity: func(ctx context.Context, req *flowv1.UpdateEntityRequest) (*flowv1.UpdateEntityResponse, error) {
			return &flowv1.UpdateEntityResponse{EntityId: req.GetId()}, nil
		},
		deleteEntity: func(ctx context.Context, req *flowv1.DeleteEntityRequest) (*flowv1.DeleteEntityResponse, error) {
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatal("no outgoing metadata")
			}
			vals := md.Get(metadataEntityTypeKey)
			if len(vals) == 0 {
				t.Fatal("no entity_type metadata")
			}
			capturedValue = vals[0]
			return &flowv1.DeleteEntityResponse{EntityId: req.GetId()}, nil
		},
	}
	tx := newMockTx(mock)

	if _, err := tx.UpdateEntity(testUUIDEntity, nil, nil); err != nil {
		t.Fatalf("UpdateEntity returned error: %v", err)
	}
	if _, err := tx.DeleteEntity(testUUIDEntity); err != nil {
		t.Fatalf("DeleteEntity returned error: %v", err)
	}
	if capturedValue != "*" {
		t.Errorf("expected subsequent call to fall back to the wildcard for an empty response type, got %q", capturedValue)
	}
}
