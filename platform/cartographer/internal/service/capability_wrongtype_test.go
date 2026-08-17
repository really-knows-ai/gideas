package service

import (
	"math"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestWriteMethods_WrongEntityTypeCapabilityDenied pins the Cartographer's
// authoritative per-type capability check on ingress (SPEC R3: "the
// Cartographer performing the authoritative type-specific check on ingress as
// the correctness net"; R7 §3: DeleteEdge's WRITE:graph/entity/<source-type>
// validation is "authoritative in Cartographer"). Every earlier denial test
// for the write methods is either the no-WRITE-at-all absence branch
// (TestCreateEntity_MissingWriteCapability,
// TestCreateEdge_UnknownEdgeTypeWinsOverMissingCapability, ...) or a
// NOT_FOUND-before-capability ordering test
// (TestUpdateEntity_NotFound_CapCheckOrder,
// TestDeleteEdge_NotFound_CapCheckOrder, ...) — none pins a caller holding
// WRITE:graph/entity/<wrongType> against an EXISTING entity/edge of another
// resolved type. Each subtest seeds the target row and invokes the mutation
// with a wrong-type per-type capability, so the only gate that can reject is
// the authoritative resolved-type check; if a regression made the per-type
// check pass or skip, these subtests fail.
func TestWriteMethods_WrongEntityTypeCapabilityDenied(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git")
	}
	// seedComponentAndService creates one Component and one Service entity in
	// the transaction branch, returning their IDs.
	seedComponentAndService := func(t *testing.T, srv *CartographerServer, txID string) (componentID, serviceID string) {
		t.Helper()
		ctx := testCtx()
		comp, err := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "core"}, nil, txID)
		if err != nil {
			t.Fatalf("seed Component: %v", err)
		}
		svc, err := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, txID)
		if err != nil {
			t.Fatalf("seed Service: %v", err)
		}
		return comp.Id, svc.Id
	}

	// UpdateEntity resolves the entity's type from the store and checks the
	// caller's WRITE capability against it (SPEC R3): a Component entity
	// reached with only WRITE:graph/entity/Service must be denied.
	t.Run("UpdateEntity", func(t *testing.T) {
		srv, _ := newTestServer(t)
		applyTestSchema(testCtx(), t, srv.store)
		txID := beginTestTx(t, srv, testCtx())
		compID, _ := seedComponentAndService(t, srv, txID)
		_, err := srv.UpdateEntity(narrowCtx("WRITE:graph/entity/Service"), &flowv1.UpdateEntityRequest{
			Id: compID, Properties: map[string]string{"name": "x"}, TransactionId: txID,
		})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("expected PermissionDenied for wrong per-type capability on UpdateEntity, got %v", status.Code(err))
		}
	})

	// TestUpdateEntity_CapabilityWinsOverEmbeddingValidation pins the SPEC
	// UpdateEntity check order (SPEC:1015: active transaction → entity
	// existence → type-specific capability → property/embedding validation):
	// the capability gate runs before the store's embedding validation, so a
	// combined fault (wrong/missing WRITE capability + NaN or
	// dimension-mismatch embedding) must surface PERMISSION_DENIED, never the
	// embedding's INVALID_ARGUMENT. The standalone embedding tests
	// (TestUpdateEntity_NaNEmbedding, TestUpdateEntity_EmbeddingDimensionMismatch)
	// run with full capabilities, so only a combined fault pins the order.
	t.Run("UpdateEntity capability wins over embedding validation", func(t *testing.T) {
		srv, st := newTestServer(t)
		schema := &flowv1.Schema{
			EntityTypes: []*flowv1.EntityType{
				{
					Name:              "VecType",
					EnableVectorIndex: true,
					Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
				},
			},
		}
		if err := st.ApplySchema(testCtx(), schema); err != nil {
			t.Fatalf("ApplySchema: %v", err)
		}
		txID := beginTestTx(t, srv, testCtx())
		ent, err := srv.store.CreateEntity(
			testCtx(), "VecType", "", map[string]string{"name": "a"}, []float32{1.0, 0.0, 0.0}, txID,
		)
		if err != nil {
			t.Fatalf("seed VecType entity: %v", err)
		}
		for _, tc := range []struct {
			name      string
			caps      []string
			embedding []float32
		}{
			{"wrong-type capability + NaN embedding", []string{"WRITE:graph/entity/Component"},
				[]float32{float32(math.NaN()), 0.0, 0.0}},
			{"missing capability + dimension mismatch", []string{"WRITE:graph/tx"},
				[]float32{1.0, 0.0}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := srv.UpdateEntity(narrowCtx(tc.caps...), &flowv1.UpdateEntityRequest{
					Id: ent.Id, Properties: map[string]string{"name": "b"},
					Embedding: tc.embedding, TransactionId: txID,
				})
				if status.Code(err) != codes.PermissionDenied {
					t.Fatalf("expected PermissionDenied (capability gate first), got %v", status.Code(err))
				}
			})
		}
	})

	// DeleteEntity: the same authoritative ingress check runs before the
	// cascade (SPEC R7 §4); a Component entity reached with only
	// WRITE:graph/entity/Service must be denied.
	t.Run("DeleteEntity", func(t *testing.T) {
		srv, _ := newTestServer(t)
		applyTestSchema(testCtx(), t, srv.store)
		txID := beginTestTx(t, srv, testCtx())
		compID, _ := seedComponentAndService(t, srv, txID)
		_, err := srv.DeleteEntity(narrowCtx("WRITE:graph/entity/Service"), &flowv1.DeleteEntityRequest{
			Id: compID, TransactionId: txID,
		})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("expected PermissionDenied for wrong per-type capability on DeleteEntity, got %v", status.Code(err))
		}
	})

	// CreateEdge authorises from the SOURCE entity type (SPEC R3): a caller
	// holding WRITE:graph/entity/Component cannot create an edge whose source
	// is a Service even though the target is a Component.
	t.Run("CreateEdge", func(t *testing.T) {
		srv, _ := newTestServer(t)
		applyTestSchema(testCtx(), t, srv.store)
		txID := beginTestTx(t, srv, testCtx())
		compID, svcID := seedComponentAndService(t, srv, txID)
		_, err := srv.CreateEdge(narrowCtx("WRITE:graph/entity/Component"), &flowv1.CreateEdgeRequest{
			EdgeType: "DEPENDS_ON", FromEntityId: svcID, ToEntityId: compID, TransactionId: txID,
		})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("expected PermissionDenied for wrong source-type capability on CreateEdge, got %v", status.Code(err))
		}
	})

	// DeleteEdge (SPEC R7 §3): the Cartographer loads the edge by ID, reads the
	// source entity ID, looks up the source entity's type, and checks the
	// caller's attested capabilities against that type authoritatively. A
	// caller holding only WRITE:graph/entity/Component cannot delete an edge
	// sourced from a Service.
	t.Run("DeleteEdge", func(t *testing.T) {
		srv, _ := newTestServer(t)
		applyTestSchema(testCtx(), t, srv.store)
		txID := beginTestTx(t, srv, testCtx())
		compID, svcID := seedComponentAndService(t, srv, txID)
		edge, err := srv.store.CreateEdge(testCtx(), "DEPENDS_ON", svcID, compID, nil, txID)
		if err != nil {
			t.Fatalf("seed edge: %v", err)
		}
		_, err = srv.DeleteEdge(narrowCtx("WRITE:graph/entity/Component"), &flowv1.DeleteEdgeRequest{
			Id: edge.Id, TransactionId: txID,
		})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("expected PermissionDenied for wrong source-type capability on DeleteEdge, got %v", status.Code(err))
		}
	})
}
