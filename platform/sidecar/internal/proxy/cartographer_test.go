package proxy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net"
	"sync"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flow "github.com/foundry/flow/sdk/go"
	"github.com/foundry/flow/sidecar/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// entityTypeComponent is the entity type used across the mode-1/mode-2
// capability wire tests (goconst: shared literal).
const entityTypeComponent = "Component"

// methodUpdateEntity, methodDeleteEntity and methodCreateEdge are the
// Cartographer RPC names asserted via captureCartographerServer.lastMethod in
// the metadata-type wire tests (goconst: shared literals).
const (
	methodUpdateEntity = "UpdateEntity"
	methodDeleteEntity = "DeleteEntity"
	methodCreateEdge   = "CreateEdge"
)

// captureCartographerServer is a fake CartographerServiceServer that records
// the node-facing requests and the metadata they arrived with, so the tests
// can assert the SDK → Sidecar → Cartographer wire path end to end (SPEC R5:
// Node → SDK → Sidecar CartographerProxy → Cartographer).
type captureCartographerServer struct {
	flowv1.UnimplementedCartographerServiceServer

	mu         sync.Mutex
	requests   int
	method     string
	capturedMD metadata.MD
	exportReq  *flowv1.ExportGraphRequest

	// createEntityID and createEntityType optionally override the fake
	// Cartographer's CreateEntity response. Tests use them to seed a specific
	// (possibly stale — SPEC R3:252) ID→type mapping into the SDK's cache:
	// the SDK records the response type keyed by the response ID.
	createEntityID   string
	createEntityType string
}

func (s *captureCartographerServer) record(ctx context.Context, method string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	s.method = method
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
}

func (s *captureCartographerServer) ExecuteCypher(
	ctx context.Context, req *flowv1.ExecuteCypherRequest,
) (*flowv1.ExecuteCypherResponse, error) {
	s.record(ctx, "ExecuteCypher")
	return &flowv1.ExecuteCypherResponse{}, nil
}

func (s *captureCartographerServer) BeginTransaction(
	ctx context.Context, req *flowv1.BeginTransactionRequest,
) (*flowv1.BeginTransactionResponse, error) {
	s.record(ctx, "BeginTransaction")
	return &flowv1.BeginTransactionResponse{TransactionId: "tx-wire-test"}, nil
}

func (s *captureCartographerServer) CreateEntity(
	ctx context.Context, req *flowv1.CreateEntityRequest,
) (*flowv1.CreateEntityResponse, error) {
	s.record(ctx, "CreateEntity")
	s.mu.Lock()
	entityID, entityType := "entity-1", req.GetEntityType()
	if s.createEntityID != "" {
		entityID = s.createEntityID
	}
	if s.createEntityType != "" {
		entityType = s.createEntityType
	}
	s.mu.Unlock()
	return &flowv1.CreateEntityResponse{EntityId: entityID, EntityType: entityType}, nil
}

func (s *captureCartographerServer) UpdateEntity(
	ctx context.Context, req *flowv1.UpdateEntityRequest,
) (*flowv1.UpdateEntityResponse, error) {
	s.record(ctx, methodUpdateEntity)
	return &flowv1.UpdateEntityResponse{EntityId: req.GetId()}, nil
}

func (s *captureCartographerServer) DeleteEntity(
	ctx context.Context, req *flowv1.DeleteEntityRequest,
) (*flowv1.DeleteEntityResponse, error) {
	s.record(ctx, methodDeleteEntity)
	return &flowv1.DeleteEntityResponse{EntityId: req.GetId()}, nil
}

func (s *captureCartographerServer) CreateEdge(
	ctx context.Context, req *flowv1.CreateEdgeRequest,
) (*flowv1.CreateEdgeResponse, error) {
	s.record(ctx, methodCreateEdge)
	return &flowv1.CreateEdgeResponse{
		EdgeId:       "edge-1",
		EdgeType:     req.GetEdgeType(),
		FromEntityId: req.GetFromEntityId(),
		ToEntityId:   req.GetToEntityId(),
	}, nil
}

func (s *captureCartographerServer) DeleteEdge(
	ctx context.Context, req *flowv1.DeleteEdgeRequest,
) (*flowv1.DeleteEdgeResponse, error) {
	s.record(ctx, "DeleteEdge")
	return &flowv1.DeleteEdgeResponse{EdgeId: req.GetId(), EdgeType: "DEPENDS_ON"}, nil
}

func (s *captureCartographerServer) SearchNeighbors(
	ctx context.Context, req *flowv1.SearchNeighborsRequest,
) (*flowv1.SearchNeighborsResponse, error) {
	s.record(ctx, "SearchNeighbors")
	return &flowv1.SearchNeighborsResponse{}, nil
}

func (s *captureCartographerServer) ExportGraph(
	req *flowv1.ExportGraphRequest, stream grpc.ServerStreamingServer[flowv1.ExportGraphResponse],
) error {
	s.mu.Lock()
	s.exportReq = req
	if md, ok := metadata.FromIncomingContext(stream.Context()); ok {
		s.capturedMD = md
	}
	s.mu.Unlock()
	return stream.Send(&flowv1.ExportGraphResponse{Chunk: []byte(`{"nodes":[],"edges":[]}`)})
}

func (s *captureCartographerServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func (s *captureCartographerServer) lastMethod() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.method
}

func (s *captureCartographerServer) lastExportReq() *flowv1.ExportGraphRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exportReq
}

func (s *captureCartographerServer) metadata() metadata.MD {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.capturedMD
}

// setupCartographerWire spins up an in-process fake Cartographer, a real
// Sidecar gRPC server with the CartographerProxy + identity interceptors +
// signing key, and a real SDK client dialing the Sidecar. It returns the
// capture server, the SDK client, and the Sidecar's public key for signature
// verification.
func setupCartographerWire(
	t *testing.T, nodeCaps string,
) (*captureCartographerServer, *flow.Client, ed25519.PublicKey) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	// Fake Cartographer on a real listener.
	capture := &captureCartographerServer{}
	cartLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for fake Cartographer: %v", err)
	}
	cartSrv := grpc.NewServer()
	flowv1.RegisterCartographerServiceServer(cartSrv, capture)
	go func() { _ = cartSrv.Serve(cartLis) }()
	t.Cleanup(cartSrv.Stop)

	// Real Sidecar gRPC server with the new proxy + identity interceptors.
	sidecarSrv := service.NewSidecarServer("wire-ns", "wire-node", "")
	cp, err := NewCartographerProxy(cartLis.Addr().String())
	if err != nil {
		t.Fatalf("new cartographer proxy: %v", err)
	}
	t.Cleanup(func() { _ = cp.Close() })

	sidecarLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for Sidecar: %v", err)
	}
	grpcSrv := grpc.NewServer(
		grpc.UnaryInterceptor(service.IdentityInterceptor(sidecarSrv, "wire-ns", "wire-node", nodeCaps, priv)),
		grpc.StreamInterceptor(service.IdentityStreamInterceptor(sidecarSrv, "wire-ns", "wire-node", nodeCaps, priv)),
	)
	flowv1.RegisterCartographerServiceServer(grpcSrv, cp)
	go func() { _ = grpcSrv.Serve(sidecarLis) }()
	t.Cleanup(grpcSrv.Stop)

	// Real SDK client dialing the Sidecar.
	client, err := flow.NewClient(flow.WithSidecarAddress(sidecarLis.Addr().String()), flow.WithWorkitemID("wi-1"))
	if err != nil {
		t.Fatalf("new sdk client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return capture, client, pub
}

// assertSignedCapabilitiesOnMD verifies that the metadata carries the given
// capabilities signed by the sidecar key, reconstructing the payload the same
// way the Cartographer's ingress verifier does (SPEC Capability Authorisation
// Chain).
func assertSignedCapabilitiesOnMD(t *testing.T, pub ed25519.PublicKey, md metadata.MD, caps string) {
	t.Helper()
	if got := md.Get("x-flow-capabilities"); len(got) != 1 || got[0] != caps {
		t.Fatalf("expected x-flow-capabilities=%q, got %v", caps, got)
	}
	if got := md.Get("x-flow-capabilities-signed-by"); len(got) != 1 || got[0] != "sidecar" {
		t.Fatalf("expected signed-by=sidecar, got %v", got)
	}
	sigB64 := md.Get("x-flow-capabilities-signature")
	signedAt := md.Get("x-flow-capabilities-signed-at")
	if len(sigB64) != 1 || len(signedAt) != 1 {
		t.Fatalf("expected signature and signed-at metadata, got sig=%v at=%v", sigB64, signedAt)
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64[0])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if !ed25519.Verify(pub, []byte(caps+"|"+signedAt[0]), sig) {
		t.Fatal("capability signature does not verify against the sidecar public key")
	}
}

// TestCartographerProxy_E2E_ExecuteCypher exercises the full read-path wire
// chain: the SDK's ExecuteCypher (no entity-type metadata — SPEC R3) reaches
// the fake Cartographer through the Sidecar proxy carrying a capability
// signature that verifies against the sidecar public key.
func TestCartographerProxy_E2E_ExecuteCypher_ReachesUpstreamWithSignedCapabilities(t *testing.T) {
	const caps = "READ:graph/entity/*"
	capture, client, pub := setupCartographerWire(t, caps)

	_, err := client.GetGraph().ExecuteCypher("MATCH (c:Component) RETURN c", nil)
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

	graph := client.GetGraph()
	tx, err := graph.BeginTransaction()
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	before := capture.count()
	_, err = tx.CreateEntity(entityTypeComponent, nil, nil, nil)
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

	graph := client.GetGraph()
	tx, err := graph.BeginTransaction()
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	entity, err := tx.CreateEntity(entityTypeComponent, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateEntity with WRITE:graph/entity/* should pass mode-1: %v", err)
	}
	if entity.ID != "entity-1" || entity.Type != entityTypeComponent {
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

	graph := client.GetGraph()
	tx, err := graph.BeginTransaction()
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	edge, err := tx.DeleteEdge("f47ac10b-58cc-4372-a567-0e02b2c3d479")
	if err != nil {
		t.Fatalf("DeleteEdge mode-2 pass-through failed: %v", err)
	}
	if edge.ID != "f47ac10b-58cc-4372-a567-0e02b2c3d479" {
		t.Fatalf("expected forwarded edge id, got %q", edge.ID)
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
	graph := client.GetGraph()
	before := capture.count()

	// Mode-1 specific-type READ: requires READ:graph/entity/Component.
	if _, err := graph.SearchNeighbors([]float32{0.1, 0.2}, entityTypeComponent, 10); err == nil {
		t.Fatal("expected zero-capability node to be denied on mode-1 SearchNeighbors")
	} else if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
	if capture.count() != before {
		t.Fatal("mode-1 block must prevent the request from reaching the Cartographer")
	}

	// Mode-1 wildcard WRITE: Sync requires WRITE:graph/entity/*.
	if err := graph.Sync(); err == nil {
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

	if _, err := client.GetGraph().ExecuteCypher("MATCH (c:Component) RETURN c", nil); err != nil {
		t.Fatalf("zero-capability node should pass mode-2 ExecuteCypher: %v", err)
	}
	if capture.lastMethod() != "ExecuteCypher" {
		t.Fatalf("expected the Cartographer to receive ExecuteCypher, got %q", capture.lastMethod())
	}
}

// TestCartographerProxy_E2E_Mode1_AllTypesSearch_BlocksPerTypeGrantOnly pins the
// omitted-type (all-types) read-search branch (SPEC R3:262): a node holding
// only a per-type READ:graph/entity/Component grant cannot perform a
// type-omitted SearchNeighbors or FullTextSearch — a per-type capability
// cannot authorise an all-types search, so the Sidecar blocks the request with
// PERMISSION_DENIED before it reaches the Cartographer.
func TestCartographerProxy_E2E_Mode1_AllTypesSearch_BlocksPerTypeGrantOnly(t *testing.T) {
	const caps = "READ:graph/entity/Component,WRITE:graph/tx"
	capture, client, _ := setupCartographerWire(t, caps)

	graph := client.GetGraph()
	before := capture.count()

	if _, err := graph.SearchNeighbors([]float32{0.1, 0.2}, "", 10); err == nil {
		t.Fatal("expected an all-types SearchNeighbors without READ:graph/entity/* to be blocked")
	} else if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
	if _, err := graph.FullTextSearch("auth service", ""); err == nil {
		t.Fatal("expected an all-types FullTextSearch without READ:graph/entity/* to be blocked")
	} else if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
	if capture.count() != before {
		t.Fatal("mode-1 block must prevent the requests from reaching the Cartographer")
	}
}

// TestCartographerProxy_E2E_Mode1_AllTypesSearch_PassesWithWildcardGrant pins
// the all-types search success path (SPEC R3:262): a node holding
// READ:graph/entity/* can perform a type-omitted SearchNeighbors and the
// request reaches the Cartographer.
func TestCartographerProxy_E2E_Mode1_AllTypesSearch_PassesWithWildcardGrant(t *testing.T) {
	const caps = "READ:graph/entity/*"
	capture, client, _ := setupCartographerWire(t, caps)

	if _, err := client.GetGraph().SearchNeighbors([]float32{0.1, 0.2}, "", 10); err != nil {
		t.Fatalf("all-types SearchNeighbors with READ:graph/entity/* should pass mode-1: %v", err)
	}
	if capture.lastMethod() != "SearchNeighbors" {
		t.Fatalf("expected the Cartographer to receive SearchNeighbors, got %q", capture.lastMethod())
	}
}

// TestCartographerProxy_E2E_ExportGraph_StreamsWithSignedCapabilities
// exercises the server-streaming path: the SDK's ExportGraph reaches the fake
// Cartographer through the Sidecar's stream interceptor, carrying signed
// capability metadata, and the chunk is relayed back.
func TestCartographerProxy_E2E_ExportGraph_StreamsWithSignedCapabilities(t *testing.T) {
	const caps = "READ:graph/entity/*"
	capture, client, pub := setupCartographerWire(t, caps)

	stream, err := client.GetGraph().ExportGraph("json")
	if err != nil {
		t.Fatalf("ExportGraph: %v", err)
	}
	defer stream.Stop()
	chunk, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if string(chunk.Data) != `{"nodes":[],"edges":[]}` {
		t.Fatalf("unexpected export chunk: %q", string(chunk.Data))
	}
	if capture.lastExportReq() == nil || capture.lastExportReq().GetFormat() != "json" {
		t.Fatal("expected the Cartographer to receive the ExportGraph request")
	}
	assertSignedCapabilitiesOnMD(t, pub, capture.metadata(), caps)
}

// TestCartographerProxy_E2E_MetadataType_PassesToCartographer pins the
// metadata-driven WRITE gate for UpdateEntity/DeleteEntity/CreateEdge (SPEC
// R3:252): the SDK resolves the entity type from its local ID-to-type mapping
// and annotates entity_type metadata. The mapping is best-effort, so the
// Sidecar's check is the mode-2 wildcard best-effort — it never blocks — and
// each request reaches the Cartographer (the authoritative per-type enforcer)
// carrying the resolved type.
func TestCartographerProxy_E2E_MetadataType_PassesToCartographer(t *testing.T) {
	const caps = "WRITE:graph/entity/Component,WRITE:graph/tx"
	const (
		entityID = "550e8400-e29b-41d4-a716-446655440000"
		targetID = "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	)
	capture, client, _ := setupCartographerWire(t, caps)
	// The fake Cartographer must return a canonical UUID v4: the SDK's
	// UpdateEntity/DeleteEntity/CreateEdge validate ID format client-side.
	capture.createEntityID = entityID

	graph := client.GetGraph()
	tx, err := graph.BeginTransaction()
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}

	// CreateEntity seeds the SDK's ID-to-type map: entityID → Component.
	entity, err := tx.CreateEntity(entityTypeComponent, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if entity.ID != entityID || entity.Type != entityTypeComponent {
		t.Fatalf("unexpected created entity: %+v", entity)
	}

	if _, err := tx.UpdateEntity(entityID, map[string]string{"name": "x"}, nil); err != nil {
		t.Fatalf("UpdateEntity with held type should pass the mode-2 metadata check: %v", err)
	}
	if capture.lastMethod() != methodUpdateEntity {
		t.Fatalf("expected UpdateEntity to reach the Cartographer, got %q", capture.lastMethod())
	}

	if _, err := tx.CreateEdge("DEPENDS_ON", entityID, targetID, nil); err != nil {
		t.Fatalf("CreateEdge with held source type should pass the mode-2 metadata check: %v", err)
	}
	if capture.lastMethod() != methodCreateEdge {
		t.Fatalf("expected CreateEdge to reach the Cartographer, got %q", capture.lastMethod())
	}

	if _, err := tx.DeleteEntity(entityID); err != nil {
		t.Fatalf("DeleteEntity with held type should pass the mode-2 metadata check: %v", err)
	}
	if capture.lastMethod() != methodDeleteEntity {
		t.Fatalf("expected DeleteEntity to reach the Cartographer, got %q", capture.lastMethod())
	}

	// The forwarded request carries the entity_type metadata the Sidecar
	// consumed for its mode-2 best-effort check.
	if got := capture.metadata().Get("entity_type"); len(got) != 1 || got[0] != entityTypeComponent {
		t.Fatalf("expected entity_type=Component forwarded to Cartographer, got %v", got)
	}
}

// TestCartographerProxy_E2E_StaleMetadataType_FallsBackToMode2 pins the stale
// ID-to-type mapping fallback (SPEC R3:252): the SDK's best-effort mapping
// "may be stale or miss entities created by other nodes", and the metadata
// carries no staleness marker, so the Sidecar cannot distinguish a stale
// annotation from a current one. Unknown or stale IDs must fall back to the
// WRITE:graph/entity/* wildcard check (Sidecar mode 2) — never a mode-1 block
// on the possibly-stale type — with the Cartographer authoritative on ingress.
// Here the cache records entityID → Service (seeded from the CreateEntity
// response) while the node holds only WRITE:graph/entity/Component: the
// UpdateEntity/DeleteEntity/CreateEdge requests must NOT be denied at the
// Sidecar and must reach the Cartographer carrying entity_type=Service.
func TestCartographerProxy_E2E_StaleMetadataType_FallsBackToMode2(t *testing.T) {
	const caps = "WRITE:graph/entity/Component,WRITE:graph/tx"
	const (
		entityID = "550e8400-e29b-41d4-a716-446655440000"
		targetID = "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	)
	capture, client, _ := setupCartographerWire(t, caps)
	// Plant a stale mapping: the fake Cartographer reports the created entity
	// as type Service, seeding entityID → Service into the SDK's cache while
	// the node only holds WRITE:graph/entity/Component.
	capture.createEntityID = entityID
	capture.createEntityType = "Service"

	graph := client.GetGraph()
	tx, err := graph.BeginTransaction()
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	// The request body type (Component) is held, so CreateEntity passes; the
	// response seeds entityID → Service into the SDK's mapping.
	if _, err := tx.CreateEntity(entityTypeComponent, nil, nil, nil); err != nil {
		t.Fatalf("CreateEntity (request body type Component, held): %v", err)
	}

	assertPasses := func(name string, call func() error) {
		t.Helper()
		if err := call(); err != nil {
			t.Fatalf("%s: stale metadata type must fall back to mode-2 pass-through, got %v", name, err)
		}
	}

	assertPasses(methodUpdateEntity, func() error {
		_, err := tx.UpdateEntity(entityID, nil, nil)
		return err
	})
	if capture.lastMethod() != methodUpdateEntity {
		t.Fatalf("expected UpdateEntity to reach the Cartographer, got %q", capture.lastMethod())
	}
	if got := capture.metadata().Get("entity_type"); len(got) != 1 || got[0] != "Service" {
		t.Fatalf("expected entity_type=Service forwarded to Cartographer, got %v", got)
	}

	assertPasses(methodDeleteEntity, func() error {
		_, err := tx.DeleteEntity(entityID)
		return err
	})
	if capture.lastMethod() != methodDeleteEntity {
		t.Fatalf("expected DeleteEntity to reach the Cartographer, got %q", capture.lastMethod())
	}

	assertPasses(methodCreateEdge, func() error {
		_, err := tx.CreateEdge("DEPENDS_ON", entityID, targetID, nil)
		return err
	})
	if capture.lastMethod() != methodCreateEdge {
		t.Fatalf("expected CreateEdge to reach the Cartographer, got %q", capture.lastMethod())
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

	graph := client.GetGraph()
	tx, err := graph.BeginTransaction()
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}

	// No entity was created, so no ID is in the SDK's mapping: each RPC
	// resolves to entity_type="*" and falls back to the mode-2 wildcard check.
	if _, err := tx.UpdateEntity(entityID, nil, nil); err != nil {
		t.Fatalf("UpdateEntity wildcard fallback should pass through: %v", err)
	}
	if capture.lastMethod() != methodUpdateEntity {
		t.Fatalf("expected UpdateEntity to reach the Cartographer, got %q", capture.lastMethod())
	}
	if got := capture.metadata().Get("entity_type"); len(got) != 1 || got[0] != "*" {
		t.Fatalf("expected entity_type=* forwarded to Cartographer, got %v", got)
	}

	if _, err := tx.DeleteEntity(entityID); err != nil {
		t.Fatalf("DeleteEntity wildcard fallback should pass through: %v", err)
	}
	if capture.lastMethod() != methodDeleteEntity {
		t.Fatalf("expected DeleteEntity to reach the Cartographer, got %q", capture.lastMethod())
	}

	if _, err := tx.CreateEdge("DEPENDS_ON", entityID, targetID, nil); err != nil {
		t.Fatalf("CreateEdge wildcard fallback should pass through: %v", err)
	}
	if capture.lastMethod() != methodCreateEdge {
		t.Fatalf("expected CreateEdge to reach the Cartographer, got %q", capture.lastMethod())
	}
}
