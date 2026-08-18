package proxy

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowmeta "github.com/foundry/flow/pkg/metadata"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// TestCartographerProxy_E2E_MetadataType_PassesToCartographer pins the
// metadata-driven WRITE gate for UpdateEntity/DeleteEntity/CreateEdge (SPEC
// R3:252 / Capability Authorisation Chain): the SDK resolves the entity type
// from its local ID-to-type mapping and annotates entity_type metadata. A
// specific resolved type is the mode-1 check — the Sidecar validates the
// caller against WRITE:graph/entity/<type> and forwards when the type is held.
// Here the node holds WRITE:graph/entity/Component and the resolved type is
// Component, so each request reaches the Cartographer carrying the resolved
// type.
func TestCartographerProxy_E2E_MetadataType_PassesToCartographer(t *testing.T) {
	const caps = "WRITE:graph/entity/Component,WRITE:graph/tx"
	const (
		entityID = "550e8400-e29b-41d4-a716-446655440000"
		targetID = "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	)
	capture, client, _ := setupCartographerWire(t, caps)
	// The fake Cartographer returns the canonical UUID v4 used below.
	capture.createEntityID = entityID
	ctx := context.Background()

	begin, err := client.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	txID := begin.GetTransactionId()

	// CreateEntity (request body type Component, held) reaches the
	// Cartographer; the fake reports the created entity as Component — the
	// same mapping the SDK would record from the response.
	entity, err := client.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    entityTypeComponent,
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if entity.GetEntityId() != entityID || entity.GetEntityType() != entityTypeComponent {
		t.Fatalf("unexpected created entity: %+v", entity)
	}

	// The SDK annotates entity_type=<resolved type> from its local ID-to-type
	// mapping; emulate that wire annotation explicitly.
	annotated := metadata.AppendToOutgoingContext(ctx, flowmeta.MetadataKeyEntityType, entityTypeComponent)

	if _, err := client.UpdateEntity(annotated, &flowv1.UpdateEntityRequest{
		Id:            entityID,
		Properties:    map[string]string{"name": "x"},
		TransactionId: txID,
	}); err != nil {
		t.Fatalf("UpdateEntity with held type should pass the mode-1 metadata check: %v", err)
	}
	if capture.lastMethod() != methodUpdateEntity {
		t.Fatalf("expected UpdateEntity to reach the Cartographer, got %q", capture.lastMethod())
	}

	if _, err := client.CreateEdge(annotated, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  entityID,
		ToEntityId:    targetID,
		TransactionId: txID,
	}); err != nil {
		t.Fatalf("CreateEdge with held source type should pass the mode-1 metadata check: %v", err)
	}
	if capture.lastMethod() != methodCreateEdge {
		t.Fatalf("expected CreateEdge to reach the Cartographer, got %q", capture.lastMethod())
	}

	if _, err := client.DeleteEntity(annotated, &flowv1.DeleteEntityRequest{
		Id:            entityID,
		TransactionId: txID,
	}); err != nil {
		t.Fatalf("DeleteEntity with held type should pass the mode-1 metadata check: %v", err)
	}
	if capture.lastMethod() != methodDeleteEntity {
		t.Fatalf("expected DeleteEntity to reach the Cartographer, got %q", capture.lastMethod())
	}

	// The forwarded request carries the entity_type metadata the Sidecar
	// consumed for its mode-1 specific-type check.
	if got := capture.metadata().Get(flowmeta.MetadataKeyEntityType); len(got) != 1 || got[0] != entityTypeComponent {
		t.Fatalf("expected entity_type=Component forwarded to Cartographer, got %v", got)
	}
}

// TestCartographerProxy_E2E_Mode1_MetadataType_BlocksUnheldResolvedType pins
// the mode-1 block for a metadata-carried specific type (SPEC Capability
// Authorisation Chain:728-733 / R3:252): when the SDK resolves the specific
// entity type (the entity was created or fetched through the same SDK client)
// and annotates entity_type metadata, the Sidecar validates the caller's
// capability against WRITE:graph/entity/<type> and blocks with
// PERMISSION_DENIED when it is lacking. Here the cache records entityID →
// Service (seeded from the CreateEntity response) while the node holds only
// WRITE:graph/entity/Component: the UpdateEntity/DeleteEntity/CreateEdge
// requests must be denied at the Sidecar and never reach the Cartographer.
// (The mode-2 fallback in SPEC R3:252 applies only to unknown or TTL-stale IDs,
// which the SDK annotates "*" — pinned separately by
// TestCartographerProxy_E2E_Mode2_UnresolvableType_PassesThrough.)
func TestCartographerProxy_E2E_Mode1_MetadataType_BlocksUnheldResolvedType(t *testing.T) {
	const caps = "WRITE:graph/entity/Component,WRITE:graph/tx"
	const (
		entityID = "550e8400-e29b-41d4-a716-446655440000"
		targetID = "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	)
	capture, client, _ := setupCartographerWire(t, caps)
	// The fake Cartographer reports the created entity as type Service, which
	// the SDK would record as entityID → Service in its TTL-fresh mapping
	// while the node only holds WRITE:graph/entity/Component.
	capture.createEntityID = entityID
	capture.createEntityType = "Service"
	ctx := context.Background()

	begin, err := client.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	txID := begin.GetTransactionId()
	// The request body type (Component) is held, so CreateEntity passes.
	if _, err := client.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    entityTypeComponent,
		TransactionId: txID,
	}); err != nil {
		t.Fatalf("CreateEntity (request body type Component, held): %v", err)
	}
	before := capture.count()

	// The SDK resolves entityID → Service from the CreateEntity response;
	// emulate that wire annotation explicitly.
	annotated := metadata.AppendToOutgoingContext(ctx, flowmeta.MetadataKeyEntityType, "Service")

	assertBlocked := func(name string, call func() error) {
		t.Helper()
		if err := call(); err == nil {
			t.Fatalf("%s: expected PERMISSION_DENIED for a resolved type the node lacks, got nil error", name)
		} else if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
			t.Fatalf("%s: expected PermissionDenied, got %v", name, err)
		}
	}

	assertBlocked(methodUpdateEntity, func() error {
		_, err := client.UpdateEntity(annotated, &flowv1.UpdateEntityRequest{Id: entityID, TransactionId: txID})
		return err
	})
	assertBlocked(methodDeleteEntity, func() error {
		_, err := client.DeleteEntity(annotated, &flowv1.DeleteEntityRequest{Id: entityID, TransactionId: txID})
		return err
	})
	assertBlocked(methodCreateEdge, func() error {
		_, err := client.CreateEdge(annotated, &flowv1.CreateEdgeRequest{
			EdgeType:      "DEPENDS_ON",
			FromEntityId:  entityID,
			ToEntityId:    targetID,
			TransactionId: txID,
		})
		return err
	})

	// Every mode-1 block must prevent its request from reaching the
	// Cartographer: the request count is unchanged since CreateEntity.
	if capture.count() != before {
		t.Fatalf("mode-1 metadata blocks must prevent the requests from reaching the Cartographer, got %d requests (want %d)",
			capture.count(), before)
	}
}

// TestCartographerProxy_E2E_Mode2_UnresolvableType_PassesThrough pins the
// mode-2 wildcard fallback for UpdateEntity/DeleteEntity/CreateEdge (SPEC
// R3:252): when the SDK cannot resolve the entity type it annotates
// entity_type="*", and the Sidecar's wildcard check is best-effort — it never
// blocks, even when the node holds no WRITE:graph/entity/* grant. The
// Cartographer is the authoritative per-type enforcer, so the request reaches
// it carrying entity_type="*".
func TestCartographerProxy_E2E_Mode2_UnresolvableType_PassesThrough(t *testing.T) {
	const caps = "WRITE:graph/entity/Component,WRITE:graph/tx"
	const (
		entityID = "550e8400-e29b-41d4-a716-446655440000"
		targetID = "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	)
	capture, client, _ := setupCartographerWire(t, caps)
	ctx := context.Background()

	begin, err := client.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	txID := begin.GetTransactionId()

	// No entity was created, so no ID is in the SDK's mapping: each RPC
	// resolves to entity_type="*" and falls back to the mode-2 wildcard check.
	// Emulate the SDK's "*" annotation explicitly.
	annotated := metadata.AppendToOutgoingContext(ctx, flowmeta.MetadataKeyEntityType, "*")

	if _, err := client.UpdateEntity(annotated, &flowv1.UpdateEntityRequest{
		Id: entityID, TransactionId: txID,
	}); err != nil {
		t.Fatalf("UpdateEntity wildcard fallback should pass through: %v", err)
	}
	if capture.lastMethod() != methodUpdateEntity {
		t.Fatalf("expected UpdateEntity to reach the Cartographer, got %q", capture.lastMethod())
	}
	if got := capture.metadata().Get(flowmeta.MetadataKeyEntityType); len(got) != 1 || got[0] != "*" {
		t.Fatalf("expected entity_type=* forwarded to Cartographer, got %v", got)
	}

	if _, err := client.DeleteEntity(annotated, &flowv1.DeleteEntityRequest{
		Id: entityID, TransactionId: txID,
	}); err != nil {
		t.Fatalf("DeleteEntity wildcard fallback should pass through: %v", err)
	}
	if capture.lastMethod() != methodDeleteEntity {
		t.Fatalf("expected DeleteEntity to reach the Cartographer, got %q", capture.lastMethod())
	}

	if _, err := client.CreateEdge(annotated, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  entityID,
		ToEntityId:    targetID,
		TransactionId: txID,
	}); err != nil {
		t.Fatalf("CreateEdge wildcard fallback should pass through: %v", err)
	}
	if capture.lastMethod() != methodCreateEdge {
		t.Fatalf("expected CreateEdge to reach the Cartographer, got %q", capture.lastMethod())
	}
}

// TestCartographerProxy_E2E_MetadataType_AbsentMetadata_FallsBackToMode2 pins
// the absent-metadata branch of the metadata WRITE gate (SPEC R3:252 /
// Capability Authorisation Chain:734-738): when the SDK attaches no
// entity_type metadata the Sidecar cannot resolve a type, so the check falls
// back to the mode-2 wildcard best-effort — it never blocks — and the request
// reaches the Cartographer even when the node holds no WRITE:graph/entity
// grant. The raw client issues UpdateEntity without any entity_type metadata,
// exercising the "" resolution path directly at the proxy layer.
func TestCartographerProxy_E2E_MetadataType_AbsentMetadata_FallsBackToMode2(t *testing.T) {
	const caps = "WRITE:graph/tx"
	w := setupCartographerWireFull(t, caps)
	ctx := context.Background()

	if _, err := w.rawClient.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: "550e8400-e29b-41d4-a716-446655440000",
	}); err != nil {
		t.Fatalf("UpdateEntity with no entity_type metadata should fall back to mode-2 pass-through: %v", err)
	}
	if w.capture.lastMethod() != methodUpdateEntity {
		t.Fatalf("expected UpdateEntity to reach the Cartographer, got %q", w.capture.lastMethod())
	}
}
