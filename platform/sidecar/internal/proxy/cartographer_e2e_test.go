package proxy

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestCartographerProxy_E2E_ExecuteCypher exercises the full read-path wire
// chain: the SDK's ExecuteCypher (no entity-type metadata — SPEC R3) reaches
// the fake Cartographer through the Sidecar proxy carrying a capability
// signature that verifies against the sidecar public key.
func TestCartographerProxy_E2E_ExecuteCypher_ReachesUpstreamWithSignedCapabilities(t *testing.T) {
	const caps = "READ:graph/entity/*"
	capture, client, pub := setupCartographerWire(t, caps)
	ctx := context.Background()

	_, err := client.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: "MATCH (c:Component) RETURN c"})
	if err != nil {
		t.Fatalf("ExecuteCypher via SDK→Sidecar→Cartographer failed: %v", err)
	}
	if capture.count() == 0 {
		t.Fatal("expected the fake Cartographer to receive the ExecuteCypher request")
	}
	assertSignedCapabilitiesOnMD(t, pub, capture.metadata(), caps)
}

// TestCartographerProxy_E2E_Mode1_BlocksWrongType verifies the mode-1 block:
// a node without WRITE:graph/entity/Component is denied at the Sidecar before
// the request reaches the Cartographer.
func TestCartographerProxy_E2E_Mode1_BlocksWrongType(t *testing.T) {
	// The node holds no WRITE:graph/entity/Component grant (only the tx grant).
	const caps = "READ:graph/entity/*,WRITE:graph/tx"
	capture, client, _ := setupCartographerWire(t, caps)
	ctx := context.Background()

	begin, err := client.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	before := capture.count()
	_, err = client.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    entityTypeComponent,
		TransactionId: begin.GetTransactionId(),
	})
	if err == nil {
		t.Fatal("expected CreateEntity to be blocked by the mode-1 capability check")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
	if capture.count() != before {
		t.Fatal("mode-1 block must prevent the request from reaching the Cartographer")
	}
}

// TestCartographerProxy_E2E_Mode1_PassesWithWildcardGrant verifies that a
// WRITE:graph/entity/* grant satisfies the mode-1 specific-type requirement.
func TestCartographerProxy_E2E_Mode1_PassesWithWildcardGrant(t *testing.T) {
	const caps = "READ:graph/entity/*,WRITE:graph/entity/*,WRITE:graph/tx"
	capture, client, _ := setupCartographerWire(t, caps)
	ctx := context.Background()

	begin, err := client.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	entity, err := client.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    entityTypeComponent,
		TransactionId: begin.GetTransactionId(),
	})
	if err != nil {
		t.Fatalf("CreateEntity with WRITE:graph/entity/* should pass mode-1: %v", err)
	}
	if entity.GetEntityId() != "entity-1" || entity.GetEntityType() != entityTypeComponent {
		t.Fatalf("unexpected forwarded entity: %+v", entity)
	}
	if capture.lastMethod() != "CreateEntity" {
		t.Fatalf("expected the Cartographer to receive CreateEntity, got %q", capture.lastMethod())
	}
}

// TestCartographerProxy_E2E_Mode2_PassesThrough verifies the mode-2
// pass-through: DeleteEdge (always wildcard) reaches the Cartographer even
// when the node holds no WRITE:graph/entity grant — the authoritative check is
// deferred to the Cartographer.
func TestCartographerProxy_E2E_Mode2_PassesThrough(t *testing.T) {
	const caps = "READ:graph/entity/*,WRITE:graph/tx"
	capture, client, pub := setupCartographerWire(t, caps)
	ctx := context.Background()

	edge, err := client.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: "f47ac10b-58cc-4372-a567-0e02b2c3d479"})
	if err != nil {
		t.Fatalf("DeleteEdge mode-2 pass-through failed: %v", err)
	}
	if edge.GetEdgeId() != "f47ac10b-58cc-4372-a567-0e02b2c3d479" {
		t.Fatalf("expected forwarded edge id, got %q", edge.GetEdgeId())
	}
	if capture.lastMethod() != "DeleteEdge" {
		t.Fatalf("expected the Cartographer to receive DeleteEdge, got %q", capture.lastMethod())
	}
	assertSignedCapabilitiesOnMD(t, pub, capture.metadata(), caps)
}

// TestCartographerProxy_E2E_ZeroCapabilityNode_BlockedOnMode1 pins the
// zero-capability node gate: a node-originated request carrying no grants is
// NOT a system-to-system call, so it must be denied by mode-1 checks with
// PERMISSION_DENIED before reaching the Cartographer. Both the specific-type
// READ gate (SearchNeighbors) and the wildcard WRITE gate (Sync) block.
func TestCartographerProxy_E2E_ZeroCapabilityNode_BlockedOnMode1(t *testing.T) {
	capture, client, _ := setupCartographerWire(t, "") // node with no grants
	ctx := context.Background()
	before := capture.count()

	// Mode-1 specific-type READ: requires READ:graph/entity/Component.
	if _, err := client.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{0.1, 0.2},
		EntityType: entityTypeComponent,
		TopK:       10,
	}); err == nil {
		t.Fatal("expected zero-capability node to be denied on mode-1 SearchNeighbors")
	} else if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
	if capture.count() != before {
		t.Fatal("mode-1 block must prevent the request from reaching the Cartographer")
	}

	// Mode-1 wildcard WRITE: Sync requires WRITE:graph/entity/*.
	if _, err := client.Sync(ctx, &flowv1.SyncRequest{}); err == nil {
		t.Fatal("expected zero-capability node to be denied on mode-1 Sync")
	} else if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
	if capture.count() != before {
		t.Fatal("mode-1 block must prevent the request from reaching the Cartographer")
	}
}

// TestCartographerProxy_E2E_ZeroCapabilityNode_PassesMode2 pins the inverse of
// the zero-capability gate: mode-2 checks are best-effort and never block, so
// a node with no grants still passes an always-mode-2 RPC (ExecuteCypher) and
// the request reaches the Cartographer (which enforces authoritatively).
func TestCartographerProxy_E2E_ZeroCapabilityNode_PassesMode2(t *testing.T) {
	capture, client, _ := setupCartographerWire(t, "") // node with no grants
	ctx := context.Background()

	if _, err := client.ExecuteCypher(
		ctx, &flowv1.ExecuteCypherRequest{Cypher: "MATCH (c:Component) RETURN c"},
	); err != nil {
		t.Fatalf("zero-capability node should pass mode-2 ExecuteCypher: %v", err)
	}
	if capture.lastMethod() != "ExecuteCypher" {
		t.Fatalf("expected the Cartographer to receive ExecuteCypher, got %q", capture.lastMethod())
	}
}

// TestCartographerProxy_E2E_Mode1_AllTypesSearch_BlocksPerTypeGrantOnly pins the
// omitted-type (all-types) read-search branch (SPEC R3:262): a node holding
// only a per-type READ:graph/entity/Component grant cannot perform a
// type-omitted SearchNeighbors, FullTextSearch, or ListEntities — a per-type
// capability cannot authorise an all-types search, so the Sidecar blocks the
// request with PERMISSION_DENIED before it reaches the Cartographer.
func TestCartographerProxy_E2E_Mode1_AllTypesSearch_BlocksPerTypeGrantOnly(t *testing.T) {
	const caps = "READ:graph/entity/Component,WRITE:graph/tx"
	capture, client, _ := setupCartographerWire(t, caps)
	ctx := context.Background()
	before := capture.count()

	if _, err := client.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{Embedding: []float32{0.1, 0.2}}); err == nil {
		t.Fatal("expected an all-types SearchNeighbors without READ:graph/entity/* to be blocked")
	} else if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
	if _, err := client.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{Query: "auth service"}); err == nil {
		t.Fatal("expected an all-types FullTextSearch without READ:graph/entity/* to be blocked")
	} else if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
	if _, err := client.ListEntities(ctx, &flowv1.ListEntitiesRequest{}); err == nil {
		t.Fatal("expected an all-types ListEntities without READ:graph/entity/* to be blocked")
	} else if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
	if capture.count() != before {
		t.Fatal("mode-1 block must prevent the requests from reaching the Cartographer")
	}
}

// TestCartographerProxy_E2E_Mode1_AllTypesSearch_PassesWithWildcardGrant pins
// the all-types search success path (SPEC R3:262): a node holding
// READ:graph/entity/* can perform a type-omitted SearchNeighbors,
// FullTextSearch, and ListEntities and the requests reach the Cartographer.
func TestCartographerProxy_E2E_Mode1_AllTypesSearch_PassesWithWildcardGrant(t *testing.T) {
	const caps = "READ:graph/entity/*"
	capture, client, _ := setupCartographerWire(t, caps)
	ctx := context.Background()

	if _, err := client.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{Embedding: []float32{0.1, 0.2}}); err != nil {
		t.Fatalf("all-types SearchNeighbors with READ:graph/entity/* should pass mode-1: %v", err)
	}
	if capture.lastMethod() != methodSearchNeighbors {
		t.Fatalf("expected the Cartographer to receive SearchNeighbors, got %q", capture.lastMethod())
	}

	if _, err := client.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{Query: "auth service"}); err != nil {
		t.Fatalf("all-types FullTextSearch with READ:graph/entity/* should pass mode-1: %v", err)
	}
	if capture.lastMethod() != methodFullTextSearch {
		t.Fatalf("expected the Cartographer to receive FullTextSearch, got %q", capture.lastMethod())
	}

	if _, err := client.ListEntities(ctx, &flowv1.ListEntitiesRequest{}); err != nil {
		t.Fatalf("all-types ListEntities with READ:graph/entity/* should pass mode-1: %v", err)
	}
	if capture.lastMethod() != methodListEntities {
		t.Fatalf("expected the Cartographer to receive ListEntities, got %q", capture.lastMethod())
	}
}

// TestCartographerProxy_E2E_ExportGraph_StreamsWithSignedCapabilities
// exercises the server-streaming path: an ExportGraph call reaches the fake
// Cartographer through the Sidecar's stream interceptor, carrying signed
// capability metadata, and the chunk is relayed back.
func TestCartographerProxy_E2E_ExportGraph_StreamsWithSignedCapabilities(t *testing.T) {
	const caps = "READ:graph/entity/*"
	capture, client, pub := setupCartographerWire(t, caps)
	ctx := context.Background()

	stream, err := client.ExportGraph(ctx, &flowv1.ExportGraphRequest{Format: "json"})
	if err != nil {
		t.Fatalf("ExportGraph: %v", err)
	}
	chunk, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if string(chunk.GetChunk()) != `{"nodes":[],"edges":[]}` {
		t.Fatalf("unexpected export chunk: %q", string(chunk.GetChunk()))
	}
	if capture.lastExportReq() == nil || capture.lastExportReq().GetFormat() != "json" {
		t.Fatal("expected the Cartographer to receive the ExportGraph request")
	}
	assertSignedCapabilitiesOnMD(t, pub, capture.metadata(), caps)
}
