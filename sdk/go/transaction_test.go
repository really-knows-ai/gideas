package flow

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
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

func TestTxExecuteCypher(t *testing.T) {
	var capturedTxID string
	mock := &mockCartographerClient{
		executeCypher: func(ctx context.Context, req *flowv1.ExecuteCypherRequest) (*flowv1.ExecuteCypherResponse, error) {
			capturedTxID = req.GetTransactionId()
			// SPEC R2: each Row is one flat tuple of string values in the
			// order LadybugDB returned them; the SDK exposes them
			// positionally as col_<N>.
			return &flowv1.ExecuteCypherResponse{
				Rows: []*flowv1.Row{
					{Values: []string{"id-1", "x"}},
					{Values: []string{"id-2", "y"}},
				},
			}, nil
		},
	}
	tx := newMockTx(mock)
	rows, err := tx.ExecuteCypher("MATCH (c:Component) RETURN c", nil)
	if err != nil {
		t.Fatalf("ExecuteCypher returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected transaction ID tx-1, got %q", capturedTxID)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0]["col_0"] != "id-1" || rows[0]["col_1"] != "x" {
		t.Errorf("row 0 = %v, want map[col_0:id-1 col_1:x]", rows[0])
	}
	if rows[1]["col_0"] != "id-2" || rows[1]["col_1"] != "y" {
		t.Errorf("row 1 = %v, want map[col_0:id-2 col_1:y]", rows[1])
	}
}

// TestTxExecuteCypher_ParamsConvertedToStructpb pins SPEC R2's optional
// params parameter on the Transaction layer: when params are present, the SDK
// converts them to a structpb.Value and the request carries them on the wire.
//
//nolint:dupl // Transaction and Graph params-conversion tests share structure.
func TestTxExecuteCypher_ParamsConvertedToStructpb(t *testing.T) {
	var capturedParams *structpb.Value
	mock := &mockCartographerClient{
		executeCypher: func(ctx context.Context, req *flowv1.ExecuteCypherRequest) (*flowv1.ExecuteCypherResponse, error) {
			capturedParams = req.GetParams()
			return &flowv1.ExecuteCypherResponse{}, nil
		},
	}
	tx := newMockTx(mock)
	_, err := tx.ExecuteCypher("MATCH (c:"+componentType+") RETURN c", map[string]any{"limit": int64(5), "name": "x"})
	if err != nil {
		t.Fatalf("ExecuteCypher returned error: %v", err)
	}
	if capturedParams == nil {
		t.Fatal("expected params to be set on request")
	}
	s := capturedParams.GetStructValue()
	if s == nil {
		t.Fatalf("expected struct params, got %v", capturedParams)
	}
	if s.Fields["limit"].GetNumberValue() != 5 {
		t.Errorf("expected limit=5 in params, got %v", s.Fields["limit"])
	}
	if s.Fields["name"].GetStringValue() != "x" {
		t.Errorf("expected name=x in params, got %v", s.Fields["name"])
	}
}

// TestTxExecuteCypher_InvalidParams pins the Transaction-layer "invalid
// params" error branch: a params value that cannot be converted to structpb
// must surface an error instead of reaching the wire.
func TestTxExecuteCypher_InvalidParams(t *testing.T) {
	called := false
	mock := &mockCartographerClient{
		executeCypher: func(ctx context.Context, req *flowv1.ExecuteCypherRequest) (*flowv1.ExecuteCypherResponse, error) {
			called = true
			return &flowv1.ExecuteCypherResponse{}, nil
		},
	}
	tx := newMockTx(mock)
	_, err := tx.ExecuteCypher("MATCH (c:"+componentType+") RETURN c", map[string]any{"bad": make(chan int)})
	if err == nil {
		t.Fatal("expected error for params that cannot convert to structpb")
	}
	if called {
		t.Error("expected ExecuteCypher RPC not to be called for invalid params")
	}
}

// TestTxExecuteCypher_NoEntityTypeMetadata pins SPEC R3's amended contract
// for ExecuteCypher on the Transaction layer (SPEC.md:247, :651 — "statement
// only — no entity-type metadata"): the SDK attaches no entity-type
// capability metadata, so the outgoing gRPC context carries neither
// entity_type nor entity_types metadata keys.
//
//nolint:dupl // Transaction and Graph no-annotation metadata tests share structure.
func TestTxExecuteCypher_NoEntityTypeMetadata(t *testing.T) {
	mock := &mockCartographerClient{
		executeCypher: func(ctx context.Context, req *flowv1.ExecuteCypherRequest) (*flowv1.ExecuteCypherResponse, error) {
			md, _ := metadata.FromOutgoingContext(ctx)
			if vals := md.Get(metadataEntityTypeKey); len(vals) > 0 {
				t.Errorf("expected no %s metadata on ExecuteCypher outgoing context, got %v", metadataEntityTypeKey, vals)
			}
			if vals := md.Get(metadataEntityTypesKey); len(vals) > 0 {
				t.Errorf("expected no %s metadata on ExecuteCypher outgoing context, got %v", metadataEntityTypesKey, vals)
			}
			return &flowv1.ExecuteCypherResponse{}, nil
		},
	}
	tx := newMockTx(mock)
	_, err := tx.ExecuteCypher("MATCH (c:"+componentType+") RETURN c", nil)
	if err != nil {
		t.Fatalf("ExecuteCypher returned error: %v", err)
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

func TestTxSearchNeighbors_LosslessIdentityLikeProperties(t *testing.T) {
	mock := &mockCartographerClient{
		searchNeighbors: func(ctx context.Context,
			req *flowv1.SearchNeighborsRequest,
		) (*flowv1.SearchNeighborsResponse, error) {
			return &flowv1.SearchNeighborsResponse{
				Results: []*flowv1.SearchNeighborResult{
					{
						EntityId:   "tn1",
						EntityType: componentType,
						Properties: map[string]string{
							"entity_id":   clobberedIDProp,
							"entity_type": clobberedTypeProp,
						},
						Distance: 0.7,
					},
				},
			}, nil
		},
	}
	tx := newMockTx(mock)
	results, err := tx.SearchNeighbors([]float32{0.1}, "Component", 10)
	if err != nil {
		t.Fatalf("SearchNeighbors returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.ID != "tn1" {
		t.Errorf("expected identity ID tn1, got %q", r.ID)
	}
	if r.Type != componentType {
		t.Errorf("expected identity type Component, got %q", r.Type)
	}
	if r.Properties["entity_id"] != clobberedIDProp {
		t.Errorf("expected identity-like property entity_id preserved, got %q", r.Properties["entity_id"])
	}
	if r.Properties["entity_type"] != clobberedTypeProp {
		t.Errorf("expected identity-like property entity_type preserved, got %q", r.Properties["entity_type"])
	}
	if r.Distance != 0.7 {
		t.Errorf("expected distance 0.7, got %v", r.Distance)
	}
}

// The SPEC error table ("Embedding contains NaN or infinity") applies the
// NaN/infinity check to CreateEntity, UpdateEntity, and SearchNeighbors.
// These tests pin the SDK-side rejection boundary on the Transaction layer,
// which calls the same validateEmbedding guard (transaction.go) as the
// Graph layer — the Graph-layer tests alone do not cover this boundary.
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
		{"SearchNeighbors", func(tx *Transaction, emb []float32) error {
			_, err := tx.SearchNeighbors(emb, componentType, 10)
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

func TestTxFullTextSearch_LosslessIdentityLikeProperties(t *testing.T) {
	mock := &mockCartographerClient{
		fullTextSearch: func(ctx context.Context, req *flowv1.FullTextSearchRequest) (*flowv1.FullTextSearchResponse, error) {
			return &flowv1.FullTextSearchResponse{
				Results: []*flowv1.Entity{
					{
						EntityId:   "tf1",
						EntityType: componentType,
						Properties: map[string]string{
							"entity_id":   "flattened-id",
							"entity_type": "FlattenedType",
						},
					},
				},
			}, nil
		},
	}
	tx := newMockTx(mock)
	results, err := tx.FullTextSearch("query", "Component")
	if err != nil {
		t.Fatalf("FullTextSearch returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	e := results[0]
	if e.ID != "tf1" {
		t.Errorf("expected identity ID tf1, got %q", e.ID)
	}
	if e.Type != componentType {
		t.Errorf("expected identity type Component, got %q", e.Type)
	}
	if e.Properties["entity_id"] != "flattened-id" {
		t.Errorf("expected identity-like property entity_id preserved, got %q", e.Properties["entity_id"])
	}
	if e.Properties["entity_type"] != "FlattenedType" {
		t.Errorf("expected identity-like property entity_type preserved, got %q", e.Properties["entity_type"])
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

// TestTxSearchNeighbors_PopulatesMap pins SPEC R3's ID-to-type cache
// population from query results on the Transaction layer: SearchNeighbors
// results carry the entity type, which the SDK records keyed by entity ID.
func TestTxSearchNeighbors_PopulatesMap(t *testing.T) {
	mock := &mockCartographerClient{
		searchNeighbors: func(ctx context.Context,
			req *flowv1.SearchNeighborsRequest,
		) (*flowv1.SearchNeighborsResponse, error) {
			return &flowv1.SearchNeighborsResponse{
				Results: []*flowv1.SearchNeighborResult{
					{EntityId: "tn1", EntityType: "Component", Distance: 0.9},
					{EntityId: "tn2", EntityType: "Service", Distance: 0.8},
				},
			}, nil
		},
	}
	tx := newMockTx(mock)
	_, err := tx.SearchNeighbors([]float32{0.1}, "Component", 10)
	if err != nil {
		t.Fatalf("SearchNeighbors returned error: %v", err)
	}
	typ, ok := tx.idTypeMap.resolve("tn1")
	if !ok || typ != componentType {
		t.Errorf("expected Component for tn1, got %q (ok=%v)", typ, ok)
	}
	typ, ok = tx.idTypeMap.resolve("tn2")
	if !ok || typ != serviceType {
		t.Errorf("expected Service for tn2, got %q (ok=%v)", typ, ok)
	}
}

// TestTxFullTextSearch_PopulatesMap pins SPEC R3's ID-to-type cache
// population from query results on the Transaction layer: FullTextSearch
// results carry the entity type, which the SDK records keyed by entity ID.
func TestTxFullTextSearch_PopulatesMap(t *testing.T) {
	mock := &mockCartographerClient{
		fullTextSearch: func(ctx context.Context, req *flowv1.FullTextSearchRequest) (*flowv1.FullTextSearchResponse, error) {
			return &flowv1.FullTextSearchResponse{
				Results: []*flowv1.Entity{
					{EntityId: "tf1", EntityType: "Component"},
					{EntityId: "tf2", EntityType: "Service"},
				},
			}, nil
		},
	}
	tx := newMockTx(mock)
	_, err := tx.FullTextSearch("query", "Component")
	if err != nil {
		t.Fatalf("FullTextSearch returned error: %v", err)
	}
	typ, ok := tx.idTypeMap.resolve("tf1")
	if !ok || typ != componentType {
		t.Errorf("expected Component for tf1, got %q (ok=%v)", typ, ok)
	}
	typ, ok = tx.idTypeMap.resolve("tf2")
	if !ok || typ != serviceType {
		t.Errorf("expected Service for tf2, got %q (ok=%v)", typ, ok)
	}
}

// TestTxFullTextSearch_ReturnsEmbedding pins the Transaction-layer
// FullTextSearch conversion surfacing the proto Entity message's embedding
// field, matching the write-path conversions.
func TestTxFullTextSearch_ReturnsEmbedding(t *testing.T) {
	mock := &mockCartographerClient{
		fullTextSearch: func(ctx context.Context, req *flowv1.FullTextSearchRequest) (*flowv1.FullTextSearchResponse, error) {
			return &flowv1.FullTextSearchResponse{
				Results: []*flowv1.Entity{
					{
						EntityId:   "tf1",
						EntityType: componentType,
						Embedding:  []float32{0.1, 0.2, 0.3},
					},
				},
			}, nil
		},
	}
	tx := newMockTx(mock)
	results, err := tx.FullTextSearch("query", "Component")
	if err != nil {
		t.Fatalf("FullTextSearch returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if len(results[0].Embedding) != 3 || results[0].Embedding[0] != 0.1 || results[0].Embedding[2] != 0.3 {
		t.Errorf("expected embedding [0.1 0.2 0.3], got %v", results[0].Embedding)
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

// The type-omitted read-search tests below exercise the SPEC type-omitted
// wildcard branch (SPEC line 247: the Sidecar validates request entityType,
// or against READ:graph/entity/* "when omitted") on the Transaction read path
// at the SDK metadata-wiring level, on a fresh/non-bootstrapped graph (no
// entities yet, which still succeeds with an empty result). The SDK wire path
// is identical for a concrete type and the omitted type (both skip capability
// annotation), so each asserts the request carries the empty entityType the
// Sidecar interprets as the wildcard, plus the empty-graph success path.

func TestTxSearchNeighbors_WildcardOmittedOnEmptyGraph(t *testing.T) {
	var capturedType string
	mock := &mockCartographerClient{
		searchNeighbors: func(ctx context.Context,
			req *flowv1.SearchNeighborsRequest,
		) (*flowv1.SearchNeighborsResponse, error) {
			capturedType = req.GetEntityType()
			return &flowv1.SearchNeighborsResponse{}, nil
		},
	}
	g := newMockTx(mock)
	results, err := g.SearchNeighbors([]float32{0.1}, "", 10)
	if err != nil {
		t.Fatalf("SearchNeighbors returned error: %v", err)
	}
	if capturedType != "" {
		t.Errorf("expected omitted entityType (wildcard branch) on request, got %q", capturedType)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results on fresh graph, got %d", len(results))
	}
}

func TestTxFullTextSearch_WildcardOmittedOnEmptyGraph(t *testing.T) {
	var capturedType string
	mock := &mockCartographerClient{
		fullTextSearch: func(ctx context.Context, req *flowv1.FullTextSearchRequest) (*flowv1.FullTextSearchResponse, error) {
			capturedType = req.GetEntityType()
			return &flowv1.FullTextSearchResponse{}, nil
		},
	}
	tx := newMockTx(mock)
	results, err := tx.FullTextSearch("query", "")
	if err != nil {
		t.Fatalf("FullTextSearch returned error: %v", err)
	}
	if capturedType != "" {
		t.Errorf("expected omitted entityType (wildcard branch) on request, got %q", capturedType)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results on fresh graph, got %d", len(results))
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

func TestDiff(t *testing.T) {
	var capturedTxID string
	mock := &mockCartographerClient{
		getTxDiff: func(ctx context.Context,
			req *flowv1.GetTransactionDiffRequest,
		) (*flowv1.GetTransactionDiffResponse, error) {
			capturedTxID = req.GetTransactionId()
			return &flowv1.GetTransactionDiffResponse{
				AddedEntities: []*flowv1.DiffEntry{
					{
						Id:         "e1",
						Type:       "Component",
						Properties: map[string]string{"name": "add"},
						Embedding:  []float32{0.1, 0.2},
						Suspected:  true,
					},
				},
				ModifiedEntities: []*flowv1.DiffEntry{
					{Id: "e2", Type: "Service", Properties: map[string]string{"name": "mod"}},
				},
				DeletedEntities: []*flowv1.DiffEntry{
					{Id: "e3", Type: "Component"},
				},
				AddedEdges: []*flowv1.DiffEntry{
					{
						Id: "edge1", Type: "DEPENDS_ON",
						FromEntityId: "e1", ToEntityId: "e2",
						Properties: map[string]string{"k": "v"},
						Suspected:  true,
					},
				},
				ModifiedEdges: []*flowv1.DiffEntry{
					{Id: "edge2", Type: "DEPENDS_ON", FromEntityId: "e2", ToEntityId: "e3"},
				},
				DeletedEdges: []*flowv1.DiffEntry{
					{Id: "edge3", Type: "DEPENDS_ON", FromEntityId: "e3", ToEntityId: "e1"},
				},
			}, nil
		},
	}
	tx := newMockTx(mock)
	diff, err := tx.Diff()
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected transaction ID tx-1, got %q", capturedTxID)
	}

	if len(diff.AddedEntities) != 1 || len(diff.ModifiedEntities) != 1 || len(diff.DeletedEntities) != 1 {
		t.Fatalf("expected 1 added/modified/deleted entity each, got %d/%d/%d",
			len(diff.AddedEntities), len(diff.ModifiedEntities), len(diff.DeletedEntities))
	}
	add := diff.AddedEntities[0]
	if add.ID != "e1" || add.Type != "Component" || add.Properties["name"] != "add" {
		t.Errorf("added entity conversion wrong: %+v", add)
	}
	if len(add.Embedding) != 2 || add.Embedding[0] != 0.1 || add.Embedding[1] != 0.2 {
		t.Errorf("expected embedding [0.1 0.2] on entity, got %v", add.Embedding)
	}
	if !add.Suspected {
		t.Error("expected Suspected flag propagated on entity")
	}
	if diff.ModifiedEntities[0].ID != "e2" || diff.DeletedEntities[0].ID != "e3" {
		t.Errorf("unexpected modified/deleted entity conversion:\n %+v / %+v",
			diff.ModifiedEntities[0], diff.DeletedEntities[0])
	}

	if len(diff.AddedEdges) != 1 || len(diff.ModifiedEdges) != 1 || len(diff.DeletedEdges) != 1 {
		t.Fatalf("expected 1 added/modified/deleted edge each, got %d/%d/%d",
			len(diff.AddedEdges), len(diff.ModifiedEdges), len(diff.DeletedEdges))
	}
	addEdge := diff.AddedEdges[0]
	if addEdge.ID != "edge1" || addEdge.Type != "DEPENDS_ON" ||
		addEdge.FromEntityID != "e1" || addEdge.ToEntityID != "e2" || addEdge.Properties["k"] != "v" {
		t.Errorf("edge conversion wrong: %+v", addEdge)
	}
	if !addEdge.Suspected {
		t.Errorf("expected Suspected flag propagated on edge")
	}
	if diff.ModifiedEdges[0].ID != "edge2" || diff.DeletedEdges[0].ID != "edge3" {
		t.Errorf("unexpected modified/deleted edge conversion wrong: %+v/%+v", diff.ModifiedEdges[0], diff.DeletedEdges[0])
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

// TestRefresh_AbortedSurfaces pins the SPEC R4 refresh-conflict boundary
// (error-table row "Refresh conflict") on the SDK layer: when the server
// rejects Refresh with ABORTED — the same entity/edge modified on main since
// the transaction was last refreshed (or began) — the SDK surfaces that
// status to the caller, matching the R4 example's "start over" branch on
// tx.Refresh()'s error.
func TestRefresh_AbortedSurfaces(t *testing.T) {
	mock := &mockCartographerClient{
		refreshTx: func(ctx context.Context,
			req *flowv1.RefreshTransactionRequest,
		) (*flowv1.RefreshTransactionResponse, error) {
			return nil, status.Error(codes.Aborted, "refresh conflict: same entity modified on main")
		},
	}
	tx := newMockTx(mock)

	err := tx.Refresh()
	if err == nil {
		t.Fatal("expected ABORTED from Refresh on conflict")
	}
	if status.Code(err) != codes.Aborted {
		t.Errorf("expected ABORTED, got %v", status.Code(err))
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

// TestCommit_WithAck pins the SPEC R10 commit(WithAck()) surface on the SDK
// layer: WithAck() carries ack=true on the CommitTransactionRequest wire
// (synchronous push delivery — the worker wakes immediately and the call
// blocks until the sync cycle completes), while a plain Commit() carries
// ack=false (deferred push on the worker's next timer cycle). Each variant
// uses a fresh transaction handle: a successful Commit marks the handle
// terminal, so the second Commit on the same handle is rejected locally.
func TestCommit_WithAck(t *testing.T) {
	var capturedAck bool
	mock := &mockCartographerClient{
		commitTx: func(ctx context.Context, req *flowv1.CommitTransactionRequest) (*flowv1.CommitTransactionResponse, error) {
			capturedAck = req.GetAck()
			return &flowv1.CommitTransactionResponse{}, nil
		},
	}

	tx := newMockTx(mock)
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit returned error: %v", err)
	}
	if capturedAck {
		t.Error("expected ack=false on plain Commit, got true")
	}

	tx = newMockTx(mock)
	if err := tx.Commit(WithAck()); err != nil {
		t.Fatalf("Commit(WithAck()) returned error: %v", err)
	}
	if !capturedAck {
		t.Error("expected ack=true on Commit(WithAck()), got false")
	}
}

// TestCommit_MarksTerminal pins that a successful Commit marks the handle
// terminal (SPEC R4's example defers `tx.Rollback()` around a flow that ends
// in `tx.Commit()`): a post-commit mutation and a second Commit are rejected
// locally with ErrTransactionCommitted and never reach the wire with the
// committed transaction ID — which the server would reject with NOT_FOUND
// (SPEC error-table row "Transaction not found": "already committed/rolled
// back") — mirroring Rollback's local rolledBack guard.
func TestCommit_MarksTerminal(t *testing.T) {
	wireCalls := 0
	mock := &mockCartographerClient{
		createEntity: func(ctx context.Context, req *flowv1.CreateEntityRequest) (*flowv1.CreateEntityResponse, error) {
			wireCalls++
			return &flowv1.CreateEntityResponse{EntityId: "entity-1", EntityType: componentType}, nil
		},
		commitTx: func(ctx context.Context, req *flowv1.CommitTransactionRequest) (*flowv1.CommitTransactionResponse, error) {
			return &flowv1.CommitTransactionResponse{}, nil
		},
	}
	tx := newMockTx(mock)

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit returned error: %v", err)
	}
	if !tx.committed {
		t.Fatal("expected committed flag to be set after successful Commit")
	}

	// A post-commit mutation must be rejected locally, not sent to the wire
	// with the committed transaction ID.
	if _, err := tx.CreateEntity(componentType, nil, nil, nil); !errors.Is(err, ErrTransactionCommitted) {
		t.Fatalf("expected ErrTransactionCommitted after commit, got %v", err)
	}
	if wireCalls != 0 {
		t.Errorf("expected no wire calls after commit, got %d", wireCalls)
	}

	// A second Commit is likewise a local rejection.
	if err := tx.Commit(); !errors.Is(err, ErrTransactionCommitted) {
		t.Fatalf("expected ErrTransactionCommitted on second Commit, got %v", err)
	}
}

// TestRollback_AfterCommit_Noop pins the R4 example's deferred
// `tx.Rollback()` after a successful `tx.Commit()`: the rollback is an
// idempotent no-op that never reaches the wire with the committed
// transaction ID (which the server would reject with NOT_FOUND), matching
// the Rollback-after-Rollback idempotency.
func TestRollback_AfterCommit_Noop(t *testing.T) {
	rollbackCalls := 0
	mock := &mockCartographerClient{
		commitTx: func(ctx context.Context, req *flowv1.CommitTransactionRequest) (*flowv1.CommitTransactionResponse, error) {
			return &flowv1.CommitTransactionResponse{}, nil
		},
		rollbackTx: func(
			ctx context.Context,
			req *flowv1.RollbackTransactionRequest,
		) (*flowv1.RollbackTransactionResponse, error) {
			rollbackCalls++
			return &flowv1.RollbackTransactionResponse{}, nil
		},
	}
	tx := newMockTx(mock)

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit returned error: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback after Commit returned error: %v", err)
	}
	if rollbackCalls != 0 {
		t.Errorf("expected no RollbackTransaction wire call after Commit, got %d", rollbackCalls)
	}
}

// TestCommit_FailedLeavesOpen pins that the committed flag is set only on
// success: a Commit that fails on the wire leaves the transaction open and
// retryable (SPEC error-table row "Commit serialisation or re-hydration
// failed": "transaction remains open for retry").
func TestCommit_FailedLeavesOpen(t *testing.T) {
	attempts := 0
	mock := &mockCartographerClient{
		commitTx: func(ctx context.Context, req *flowv1.CommitTransactionRequest) (*flowv1.CommitTransactionResponse, error) {
			attempts++
			if attempts == 1 {
				return nil, status.Error(codes.Internal, "serialisation failed")
			}
			return &flowv1.CommitTransactionResponse{}, nil
		},
	}
	tx := newMockTx(mock)

	if err := tx.Commit(); err == nil {
		t.Fatal("expected first Commit to fail on the wire")
	}
	if tx.committed {
		t.Fatal("expected committed flag NOT to be set after a failed Commit")
	}

	// The transaction remains open: a retry succeeds.
	if err := tx.Commit(); err != nil {
		t.Fatalf("expected retry Commit to succeed, got %v", err)
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

func TestExtendTimeout(t *testing.T) {
	var capturedTxID string
	var capturedDuration *durationpb.Duration
	mock := &mockCartographerClient{
		extendTimeout: func(ctx context.Context, req *flowv1.ExtendTimeoutRequest) (*flowv1.ExtendTimeoutResponse, error) {
			capturedTxID = req.GetTransactionId()
			capturedDuration = req.GetDuration()
			// The server applies the requested duration verbatim and returns
			// it as applied_timeout (mirroring BeginTransaction).
			return &flowv1.ExtendTimeoutResponse{
				AppliedTimeout: durationpb.New(24 * time.Hour),
			}, nil
		},
	}
	tx := newMockTx(mock)

	got, err := tx.ExtendTimeout(24 * time.Hour)
	if err != nil {
		t.Fatalf("ExtendTimeout returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected transaction ID tx-1, got %q", capturedTxID)
	}
	if capturedDuration == nil {
		t.Fatal("expected duration to be set on request")
	}
	if capturedDuration.AsDuration() != 24*time.Hour {
		t.Errorf("expected requested duration 24h on RPC, got %v", capturedDuration.AsDuration())
	}
	if got != 24*time.Hour {
		t.Errorf("expected returned value 24h, got %v", got)
	}
	if tx.timeout != got {
		t.Errorf("expected tx.timeout set to returned value %v, got %v", got, tx.timeout)
	}
}

// TestExtendTimeout_SurfacesAppliedTimeout verifies the SDK returns the
// server-granted applied_timeout from the response (mirroring BeginTransaction
// applied_timeout), not a locally assumed requested value.
func TestExtendTimeout_SurfacesAppliedTimeout(t *testing.T) {
	mock := &mockCartographerClient{
		extendTimeout: func(ctx context.Context, req *flowv1.ExtendTimeoutRequest) (*flowv1.ExtendTimeoutResponse, error) {
			return &flowv1.ExtendTimeoutResponse{
				AppliedTimeout: durationpb.New(48 * time.Hour),
			}, nil
		},
	}
	tx := newMockTx(mock)

	got, err := tx.ExtendTimeout(24 * time.Hour)
	if err != nil {
		t.Fatalf("ExtendTimeout returned error: %v", err)
	}
	if got != 48*time.Hour {
		t.Errorf("expected surfaced applied timeout 48h, got %v", got)
	}
	if tx.timeout != 48*time.Hour {
		t.Errorf("expected tx.timeout set to applied 48h, got %v", tx.timeout)
	}
}

// TestExtendTimeout_ServerRejectsOverCap pins the SPEC error-table row
// "Invalid transaction timeout duration" on the SDK layer: an extension whose
// total lifetime would exceed the 7-day hard maximum is rejected by the
// server with INVALID_ARGUMENT — never silently capped — and the SDK surfaces
// that rejection to the caller (mirroring TestBeginTransaction_WithTimeout's
// pin of the symmetric BeginTransaction rejection).
func TestExtendTimeout_ServerRejectsOverCap(t *testing.T) {
	var capturedDuration *durationpb.Duration
	mock := &mockCartographerClient{
		extendTimeout: func(ctx context.Context, req *flowv1.ExtendTimeoutRequest) (*flowv1.ExtendTimeoutResponse, error) {
			capturedDuration = req.GetDuration()
			return nil, status.Error(codes.InvalidArgument, "invalid transaction timeout duration")
		},
	}
	tx := newMockTx(mock)

	got, err := tx.ExtendTimeout(10 * 24 * time.Hour)
	if err == nil {
		t.Fatal("expected INVALID_ARGUMENT rejection for over-cap extension")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected INVALID_ARGUMENT, got %v", status.Code(err))
	}
	if got != 0 {
		t.Errorf("expected zero duration on rejection, got %v", got)
	}
	if capturedDuration == nil {
		t.Fatal("expected duration to be set on request")
	}
	if capturedDuration.AsDuration() != 10*24*time.Hour {
		t.Errorf("expected requested duration 10d sent verbatim, got %v", capturedDuration.AsDuration())
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

// TestDiff_AppliesPerCallDeadline verifies that the per-call deadline
// configured via session timeout is applied to transaction lifecycle RPCs.
// A mock that parks until its context deadline fires proves the operation is
// cut by the deadline (via session.call) rather than hanging on the raw
// session ctx, which carries no deadline.
func TestDiff_AppliesPerCallDeadline(t *testing.T) {
	mock := &mockCartographerClient{
		getTxDiff: func(ctx context.Context, _ *flowv1.GetTransactionDiffRequest,
		) (*flowv1.GetTransactionDiffResponse, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(2 * time.Second):
				// If the per-call deadline is not applied, the call would hang
				// instead of being cut. Fail loudly rather than hang the suite.
				return nil, errors.New("per-call deadline was not applied; call was not cut")
			}
		},
	}
	tx := newMockTx(mock)
	tx.session.timeout = 100 * time.Millisecond

	_, err := tx.Diff()
	if err == nil {
		t.Fatal("expected Diff to be cut by the per-call deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded to cut the call, got %v", err)
	}
}
