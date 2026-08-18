package service

import (
	"math"
	"strings"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestCreateEdge_SourceNotFound_CapCheckOrder verifies that CreateEdge returns
// NOT_FOUND when the source entity does not exist, even when the caller lacks
// wildcard WRITE capability (which would have caused PERMISSION_DENIED in the
// old code where capability was checked before entity existence).
func TestCreateEdge_SourceNotFound_CapCheckOrder(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("WRITE:graph/entity/Service")

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, testCtx())

	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  "11111111-1111-4111-8111-111111111111",
		ToEntityId:    "22222222-2222-4222-8222-222222222222",
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for not-found source, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

// TestCreateEdge_TargetNotFound_CapCheckOrder verifies the SPEC RPC check-order
// (CreateEdge: structural → entity existence → type-specific capability →
// edge-rule auth) and error-table row "Source or target entity not found on
// CreateEdge → NOT_FOUND" for the TARGET endpoint: a request with a missing
// target from a caller lacking WRITE:graph/entity/<source-type> must return
// NOT_FOUND, not PERMISSION_DENIED. The target's existence is verified before
// the capability gate so the SPEC error code wins regardless of the caller's
// capability.
func TestCreateEdge_TargetNotFound_CapCheckOrder(t *testing.T) {
	srv, _ := newTestServer(t)
	// Caller holds write capability for neither Service nor Component — only
	// the (irrelevant) tx capability, so any capability-absence rejection would
	// surface as PERMISSION_DENIED.
	ctx := narrowCtx("WRITE:graph/tx")

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, testCtx())
	// Only the source exists; the target ID is a valid UUID referencing nothing.
	svc, err := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, txID)
	if err != nil {
		t.Fatalf("seed source entity: %v", err)
	}

	_, err = srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  svc.Id,
		ToEntityId:    "22222222-2222-4222-8222-222222222222",
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for not-found target, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound for missing target despite missing capability, got %v", status.Code(err))
	}
}

// TestDeleteEdge_NotFound_CapCheckOrder verifies that DeleteEdge returns
// NOT_FOUND when the edge does not exist, even when the caller lacks wildcard
// WRITE capability.
func TestDeleteEdge_NotFound_CapCheckOrder(t *testing.T) {
	srv, _ := newTestServer(t)
	txID := beginTestTx(t, srv, testCtx())
	ctx := narrowCtx("WRITE:graph/entity/Service")

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{
		Id:            "11111111-1111-4111-8111-111111111111",
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for not-found edge, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

// TestUpdateEntity_NotFound_CapCheckOrder verifies that UpdateEntity returns
// NOT_FOUND when the entity does not exist, even when the caller lacks wildcard
// WRITE capability.
func TestUpdateEntity_NotFound_CapCheckOrder(t *testing.T) {
	srv, _ := newTestServer(t)
	txID := beginTestTx(t, srv, testCtx())
	ctx := narrowCtx("WRITE:graph/entity/Component")

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:            "11111111-1111-4111-8111-111111111111",
		Properties:    map[string]string{"name": "x"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for not-found entity, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

// TestDeleteEntity_NotFound_CapCheckOrder verifies that DeleteEntity returns
// NOT_FOUND when the entity does not exist, even when the caller lacks wildcard
// WRITE capability.
func TestDeleteEntity_NotFound_CapCheckOrder(t *testing.T) {
	srv, _ := newTestServer(t)
	txID := beginTestTx(t, srv, testCtx())
	ctx := narrowCtx("WRITE:graph/entity/Component")

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{
		Id:            "11111111-1111-4111-8111-111111111111",
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for not-found entity, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

// TestListEntities_MissingCapBeforeTypeCheck verifies that ListEntities returns
// PERMISSION_DENIED when the caller lacks READ capability, even when the entity
// type does not exist (proving capability check happens before TableExists).
func TestListEntities_MissingCapBeforeTypeCheck(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := noReadCtx()

	_, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
		EntityType: "NonExistentType",
		PageSize:   10,
	})
	if err == nil {
		t.Fatal("expected error for missing READ capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

// TestListEntities_EmptyTypeDoesNotFabricateCapabilityString pins that an
// empty/omitted entityType can never fabricate the undefined capability
// "READ:graph/entity/" (SPEC R3 names only READ:graph/entity/<type> and
// READ:graph/entity/*). The omitted-type gate must check the wildcard, so a
// caller with no READ capability is denied with a message naming
// READ:graph/entity/*. A regression that builds "READ:graph/entity/"
// (e.g. checkEntityCap(ctx, "READ", "")) is caught because that bare string
// does not contain "READ:graph/entity/*". A wildcard holder instead passes the
// capability gate and reaches the structural unknown-entity-type check
// (INVALID_ARGUMENT), per the SPEC ListEntities check-order "capability →
// structural".
func TestListEntities_EmptyTypeDoesNotFabricateCapabilityString(t *testing.T) {
	srv, _ := newTestServer(t)

	t.Run("no READ capability denies naming wildcard", func(t *testing.T) {
		_, err := srv.ListEntities(noReadCtx(), &flowv1.ListEntitiesRequest{})
		if err == nil {
			t.Fatal("expected error for empty entityType without READ capability, got nil")
		}
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
		}
		if !strings.Contains(err.Error(), "READ:graph/entity/*") {
			t.Fatalf("denial must name the wildcard READ:graph/entity/*, got: %v", err)
		}
	})

	t.Run("wildcard holder reaches structural unknown-type", func(t *testing.T) {
		_, err := srv.ListEntities(testCtx(), &flowv1.ListEntitiesRequest{})
		if err == nil {
			t.Fatal("expected error for empty entityType without an applied table, got nil")
		}
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument (unknown entity type), got %v", status.Code(err))
		}
		if strings.Contains(err.Error(), "READ:graph/entity/") {
			t.Fatalf("error must not fabricate a capability string, got: %v", err)
		}
	})
}

// TestSearchNeighbors_NaNBeforeTypeCheck verifies that SearchNeighbors returns
// INVALID_ARGUMENT for NaN embedding before checking TableExists, so a NaN
// embedding with an unknown entity type returns "NaN" error not "unknown type".
func TestSearchNeighbors_NaNBeforeTypeCheck(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	_, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{float32(math.NaN()), 0.0, 0.0},
		EntityType: "NonExistentType",
		TopK:       5,
	})
	if err == nil {
		t.Fatal("expected error for NaN embedding, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
	// Verify the error message mentions NaN/Inf, not "unknown entity type".
	if msg := err.Error(); strings.Contains(msg, "unknown entity type") {
		t.Fatalf("expected error about NaN/Inf, got unknown-entity-type message: %q", msg)
	}
}
