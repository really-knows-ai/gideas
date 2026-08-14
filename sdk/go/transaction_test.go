package flow

import (
	"context"
	"math"
	"strings"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func newMockTx(mock *mockCartographerClient) *Transaction {
	return newMockTxWithID(mock, embassyTestTxID)
}

// newMockTxWithID returns a Transaction bound to the mock with the given
// transaction ID. An empty ID simulates a write issued without an active
// transaction — the wire carries no transactionId, and the Cartographer
// rejects it (FAILED_PRECONDITION "No active transaction").
func newMockTxWithID(mock *mockCartographerClient, txID string) *Transaction {
	ctx, cancel := context.WithCancel(context.Background())
	sess := &session{
		Cartographer: mock,
		ctx:          ctx,
		cancel:       cancel,
	}
	return &Transaction{
		session:   sess,
		id:        txID,
		idTypeMap: newIDTypeMap(),
	}
}

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

// The SPEC error table ("Embedding contains NaN or infinity") applies the
// NaN/infinity check to CreateEntity and UpdateEntity. These tests pin the
// SDK-side rejection boundary on the Transaction layer, which calls the same
// validateEmbedding guard (transaction.go) as the write paths.
func TestTxEmbeddingNaNInfinityRejection(t *testing.T) {
	bad := []struct {
		name string
		emb  []float32
	}{
		{"nan", []float32{float32(math.NaN())}},
		{"positive-infinity", []float32{float32(math.Inf(1))}},
		{"negative-infinity", []float32{float32(math.Inf(-1))}},
	}
	methods := []struct {
		name string
		fn   func(tx *Transaction, emb []float32) error
	}{
		{"CreateEntity", func(tx *Transaction, emb []float32) error {
			_, err := tx.CreateEntity(componentType, nil, nil, emb)
			return err
		}},
		{"UpdateEntity", func(tx *Transaction, emb []float32) error {
			_, err := tx.UpdateEntity(testUUIDEntity, nil, emb)
			return err
		}},
	}
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			for _, tc := range bad {
				t.Run(tc.name, func(t *testing.T) {
					tx := newMockTx(&mockCartographerClient{})
					if err := m.fn(tx, tc.emb); err == nil {
						t.Errorf("expected error for NaN/infinity embedding on %s", m.name)
					}
				})
			}
		})
	}
}

// TestTransaction_RejectsNonCanonicalUUID pins the SDK's client-side
// canonical RFC4122 §3 UUID v4 validation on the write path (SPEC:162;
// error-table row "Invalid entity or edge ID format"): non-canonical
// spellings that still parse as UUIDs — uppercase hex, 32-char no-hyphen,
// braced {...}, urn:uuid: — plus outright non-UUIDs are rejected before they
// reach the wire, mirroring the validateEmbedding client-side guard. Without
// this guard the Cartographer would persist each spelling verbatim as a
// distinct <id>.json file, creating two entities for one UUID and bypassing
// the CreateEntity ALREADY_EXISTS check. The mock's RPC fields are left nil,
// so a rejection that slips through would panic here rather than pass.
func TestTransaction_RejectsNonCanonicalUUID(t *testing.T) {
	bad := []struct {
		name string
		id   string
	}{
		{"uppercase-hex", "550E8400-E29B-41D4-A716-446655440000"},
		{"no-hyphen", "550e8400e29b41d4a716446655440000"},
		{"braced", "{550e8400-e29b-41d4-a716-446655440000}"},
		{"urn-prefixed", "urn:uuid:550e8400-e29b-41d4-a716-446655440000"},
		{"not-a-uuid", "entity-1"},
	}
	methods := []struct {
		name string
		fn   func(tx *Transaction, id string) error
	}{
		{"CreateEntity", func(tx *Transaction, id string) error {
			_, err := tx.CreateEntity(componentType, &id, nil, nil)
			return err
		}},
		{"UpdateEntity", func(tx *Transaction, id string) error {
			_, err := tx.UpdateEntity(id, nil, nil)
			return err
		}},
		{"DeleteEntity", func(tx *Transaction, id string) error {
			_, err := tx.DeleteEntity(id)
			return err
		}},
		{"CreateEdge-from", func(tx *Transaction, id string) error {
			_, err := tx.CreateEdge("DEPENDS_ON", id, testUUIDTo, nil)
			return err
		}},
		{"CreateEdge-to", func(tx *Transaction, id string) error {
			_, err := tx.CreateEdge("DEPENDS_ON", testUUIDFrom, id, nil)
			return err
		}},
		{"DeleteEdge", func(tx *Transaction, id string) error {
			_, err := tx.DeleteEdge(id)
			return err
		}},
	}
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			for _, tc := range bad {
				t.Run(tc.name, func(t *testing.T) {
					tx := newMockTx(&mockCartographerClient{})
					err := m.fn(tx, tc.id)
					if err == nil {
						t.Fatalf("expected client-side rejection of %q", tc.id)
					}
					if !strings.Contains(err.Error(), "canonical") {
						t.Errorf("expected canonical-form rejection error, got %v", err)
					}
				})
			}
		})
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
		{"ListEntities", func() error { _, err := tx.ListEntities(""); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.fn(); err == nil {
				t.Error("expected error after rollback")
			}
		})
	}
}

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
