package proxy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowmeta "github.com/foundry/flow/pkg/metadata"
	flow "github.com/foundry/flow/sdk/go"
	"github.com/foundry/flow/sidecar/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// entityTypeComponent is the entity type used across the mode-1/mode-2
// capability wire tests (goconst: shared literal).
const entityTypeComponent = "Component"

// methodUpdateEntity, methodDeleteEntity and methodCreateEdge are the
// Cartographer RPC names asserted via captureCartographerServer.lastMethod in
// the metadata-type wire tests (goconst: shared literals). The remaining
// method* constants name the transaction, admin, and read RPCs pinned by the
// capability-gate wire tests.
const (
	methodUpdateEntity        = "UpdateEntity"
	methodDeleteEntity        = "DeleteEntity"
	methodCreateEdge          = "CreateEdge"
	methodBeginTransaction    = "BeginTransaction"
	methodCommitTransaction   = "CommitTransaction"
	methodRollbackTransaction = "RollbackTransaction"
	methodRefreshTransaction  = "RefreshTransaction"
	methodExtendTimeout       = "ExtendTimeout"
	methodGetTransactionDiff  = "GetTransactionDiff"
	methodSync                = "Sync"
	methodSearchNeighbors     = "SearchNeighbors"
	methodFullTextSearch      = "FullTextSearch"
	methodListEntities        = "ListEntities"
)

// txID is the fake transaction ID used by raw-RPC capability-gate tests
// (goconst: shared literal). Blocked requests never reach the Cartographer,
// so the value is only a placeholder.
const txID = "tx-wire-test"

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
	s.record(ctx, methodBeginTransaction)
	return &flowv1.BeginTransactionResponse{TransactionId: txID}, nil
}

func (s *captureCartographerServer) CommitTransaction(
	ctx context.Context, req *flowv1.CommitTransactionRequest,
) (*flowv1.CommitTransactionResponse, error) {
	s.record(ctx, methodCommitTransaction)
	return &flowv1.CommitTransactionResponse{}, nil
}

func (s *captureCartographerServer) RollbackTransaction(
	ctx context.Context, req *flowv1.RollbackTransactionRequest,
) (*flowv1.RollbackTransactionResponse, error) {
	s.record(ctx, methodRollbackTransaction)
	return &flowv1.RollbackTransactionResponse{}, nil
}

func (s *captureCartographerServer) RefreshTransaction(
	ctx context.Context, req *flowv1.RefreshTransactionRequest,
) (*flowv1.RefreshTransactionResponse, error) {
	s.record(ctx, methodRefreshTransaction)
	return &flowv1.RefreshTransactionResponse{}, nil
}

func (s *captureCartographerServer) ExtendTimeout(
	ctx context.Context, req *flowv1.ExtendTimeoutRequest,
) (*flowv1.ExtendTimeoutResponse, error) {
	s.record(ctx, methodExtendTimeout)
	return &flowv1.ExtendTimeoutResponse{}, nil
}

func (s *captureCartographerServer) GetTransactionDiff(
	ctx context.Context, req *flowv1.GetTransactionDiffRequest,
) (*flowv1.GetTransactionDiffResponse, error) {
	s.record(ctx, methodGetTransactionDiff)
	return &flowv1.GetTransactionDiffResponse{}, nil
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
	s.record(ctx, methodSearchNeighbors)
	return &flowv1.SearchNeighborsResponse{}, nil
}

func (s *captureCartographerServer) FullTextSearch(
	ctx context.Context, req *flowv1.FullTextSearchRequest,
) (*flowv1.FullTextSearchResponse, error) {
	s.record(ctx, methodFullTextSearch)
	return &flowv1.FullTextSearchResponse{}, nil
}

func (s *captureCartographerServer) ListEntities(
	ctx context.Context, req *flowv1.ListEntitiesRequest,
) (*flowv1.ListEntitiesResponse, error) {
	s.record(ctx, methodListEntities)
	return &flowv1.ListEntitiesResponse{}, nil
}

func (s *captureCartographerServer) Sync(
	ctx context.Context, req *flowv1.SyncRequest,
) (*flowv1.SyncResponse, error) {
	s.record(ctx, methodSync)
	return &flowv1.SyncResponse{}, nil
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

// cartographerWire is the full wire-harness result from
// setupCartographerWireFull: the capture server, the SDK client, the Sidecar's
// public key (for signature verification), the Sidecar server (for session
// injection), and a raw Cartographer client dialing the Sidecar (for direct
// RPC invocation without the SDK).
type cartographerWire struct {
	capture    *captureCartographerServer
	client     *flow.Client
	sidecarPub ed25519.PublicKey
	sidecarSrv *service.SidecarServer
	rawClient  flowv1.CartographerServiceClient
}

// setupCartographerWireFull spins up an in-process fake Cartographer, a real
// Sidecar gRPC server with the CartographerProxy + identity interceptors +
// signing key, and a real SDK client dialing the Sidecar, plus a raw
// Cartographer client dialing the Sidecar directly. It returns the capture
// server, the SDK client, and the Sidecar's public key for signature
// verification along with the Sidecar server and raw client.
func setupCartographerWireFull(t *testing.T, nodeCaps string) *cartographerWire {
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

	// Raw Cartographer client dialing the Sidecar directly, for RPCs the SDK
	// cannot issue without a transaction handle (fixed-gate block tests).
	rawConn, err := grpc.NewClient(
		sidecarLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial Sidecar with raw client: %v", err)
	}
	t.Cleanup(func() { _ = rawConn.Close() })

	return &cartographerWire{
		capture:    capture,
		client:     client,
		sidecarPub: pub,
		sidecarSrv: sidecarSrv,
		rawClient:  flowv1.NewCartographerServiceClient(rawConn),
	}
}

// setupCartographerWire spins up the wire harness and returns the capture
// server, the SDK client, and the Sidecar's public key for signature
// verification. It is the convenience form used by the SDK-path wire tests.
func setupCartographerWire(
	t *testing.T, nodeCaps string,
) (*captureCartographerServer, *flow.Client, ed25519.PublicKey) {
	t.Helper()
	w := setupCartographerWireFull(t, nodeCaps)
	return w.capture, w.client, w.sidecarPub
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

// TestCartographerProxy_NodeCapabilities_NilPassThrough pins the
// non-node-originated nil branch of nodeCapabilities (SPEC R3 / Capability
// Authorisation Chain): a request carrying no x-flow-node-id — no incoming
// metadata at all, or metadata without the node identity — yields a nil
// capability set, which makes checkCapability pass the request through
// unchecked (even a blocking fixed gate) and leaves the Cartographer's ingress
// verifier as the security boundary. The wire harness always injects
// x-flow-node-id via the identity interceptor (entry-bound fallback), so these
// branches are unreachable through the E2E path and are pinned directly.
func TestCartographerProxy_NodeCapabilities_NilPassThrough(t *testing.T) {
	if caps := nodeCapabilities(context.Background()); caps != nil {
		t.Fatalf("expected nil capabilities for a context with no metadata, got %v", caps)
	}

	noNodeID := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("x-flow-namespace", "wire-ns"),
	)
	if caps := nodeCapabilities(noNodeID); caps != nil {
		t.Fatalf("expected nil capabilities for metadata without x-flow-node-id, got %v", caps)
	}

	// A nil capability set is treated as a system-to-system call: even the
	// fixed blocking tx gate passes through instead of denying.
	p := &CartographerProxy{}
	if err := p.checkCapability(context.Background(), "WRITE", "graph/tx", true); err != nil {
		t.Fatalf("expected checkCapability to pass through on nil capabilities, got %v", err)
	}
}

// TestCartographerProxy_CheckCapability_ExactOrLiteralWildcard pins the matching
// semantics the Sidecar's mode-1/fixed gates share with the Cartographer's
// authoritative CheckSpecificType/CheckWildcard exact-string gates (SPEC R3 /
// Capability Authorisation Chain): a held grant satisfies the requirement only
// when it is exactly equal, or is the literal full-segment
// "<verb>:graph/entity/*" wildcard satisfying a type-specific requirement.
// Filepath metacharacters — partial-segment "*" ("Comp*"), "?", or "[a-z]" —
// are literal strings, never wildcards: a non-SPEC grant such as
// "WRITE:graph/entity/Comp*" must NOT pass a mode-1 check for Component, so it
// is blocked at the Sidecar exactly as the Cartographer would deny it, instead
// of being forwarded only to be denied with PERMISSION_DENIED at ingress.
func TestCartographerProxy_CheckCapability_ExactOrLiteralWildcard(t *testing.T) {
	nodeCtx := func(caps string) context.Context {
		return metadata.NewIncomingContext(context.Background(),
			metadata.Pairs(
				flowmeta.MetadataKeyNodeID, "wire-node",
				flowmeta.MetadataKeyCapabilities, caps,
			))
	}
	p := &CartographerProxy{}

	tests := []struct {
		name     string
		caps     string
		verb     string
		resource string
		wantDeny bool
	}{
		// Exact grant satisfies the same specific-type requirement.
		{"exact specific type", "WRITE:graph/entity/Component", "WRITE", "graph/entity/Component", false},
		// Literal full-segment wildcard satisfies a type-specific requirement
		// (SPEC R3:241-242: WRITE:graph/entity/* authorises all types).
		{"literal wildcard satisfies specific type", "WRITE:graph/entity/*", "WRITE", "graph/entity/Component", false},
		// Partial-segment / character-class wildcards are literal strings,
		// never wildcards — the Cartographer's exact-string gate denies them.
		{"partial wildcard does not match", "WRITE:graph/entity/Comp*", "WRITE", "graph/entity/Component", true},
		{"question mark does not match", "WRITE:graph/entity/Compon?nt", "WRITE", "graph/entity/Component", true},
		{"char class does not match", "WRITE:graph/entity/Compon[a-z]t", "WRITE", "graph/entity/Component", true},
		// A per-type grant cannot authorise an all-types requirement
		// (SPEC R3:262).
		{"specific type does not satisfy wildcard", "WRITE:graph/entity/Component", "WRITE", "graph/entity/*", true},
		{"literal wildcard satisfies wildcard", "WRITE:graph/entity/*", "WRITE", "graph/entity/*", false},
		// Verb mismatch denies.
		{"wrong verb", "READ:graph/entity/*", "WRITE", "graph/entity/Component", true},
		// Fixed tx gates remain exact.
		{"exact tx grant", "WRITE:graph/tx", "WRITE", "graph/tx", false},
		{"missing tx grant", "READ:graph/tx", "WRITE", "graph/tx", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.checkCapability(nodeCtx(tt.caps), tt.verb, tt.resource, true)
			if tt.wantDeny {
				if st := status.Code(err); st != codes.PermissionDenied {
					t.Fatalf("expected PermissionDenied, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("expected the grant to pass, got %v", err)
			}
		})
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
// type-omitted SearchNeighbors, FullTextSearch, or ListEntities — a per-type
// capability cannot authorise an all-types search, so the Sidecar blocks the
// request with PERMISSION_DENIED before it reaches the Cartographer.
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
	if _, err := graph.ListEntities(""); err == nil {
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

	if _, err := client.GetGraph().SearchNeighbors([]float32{0.1, 0.2}, "", 10); err != nil {
		t.Fatalf("all-types SearchNeighbors with READ:graph/entity/* should pass mode-1: %v", err)
	}
	if capture.lastMethod() != methodSearchNeighbors {
		t.Fatalf("expected the Cartographer to receive SearchNeighbors, got %q", capture.lastMethod())
	}

	if _, err := client.GetGraph().FullTextSearch("auth service", ""); err != nil {
		t.Fatalf("all-types FullTextSearch with READ:graph/entity/* should pass mode-1: %v", err)
	}
	if capture.lastMethod() != methodFullTextSearch {
		t.Fatalf("expected the Cartographer to receive FullTextSearch, got %q", capture.lastMethod())
	}

	if _, err := client.GetGraph().ListEntities(""); err != nil {
		t.Fatalf("all-types ListEntities with READ:graph/entity/* should pass mode-1: %v", err)
	}
	if capture.lastMethod() != methodListEntities {
		t.Fatalf("expected the Cartographer to receive ListEntities, got %q", capture.lastMethod())
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

// mockExportClientStream is a flowv1.CartographerService_ExportGraphClient that
// yields a single chunk and then io.EOF when failErr is nil, or returns failErr
// from the first Recv — pinning the sidecar relay's mid-stream upstream branch.
type mockExportClientStream struct {
	failErr error
	calls   int
}

func (m *mockExportClientStream) Recv() (*flowv1.ExportGraphResponse, error) {
	m.calls++
	if m.failErr != nil {
		return nil, m.failErr
	}
	if m.calls == 1 {
		return &flowv1.ExportGraphResponse{Chunk: []byte("chunk")}, nil
	}
	return nil, io.EOF
}

func (mockExportClientStream) Context() context.Context { return context.Background() }
func (mockExportClientStream) Header() (metadata.MD, error) {
	return nil, nil
}
func (mockExportClientStream) Trailer() metadata.MD { return nil }
func (mockExportClientStream) CloseSend() error     { return nil }
func (mockExportClientStream) SendMsg(any) error    { return nil }
func (mockExportClientStream) RecvMsg(any) error    { return nil }

// mockExportClientChunkThenErr is a flowv1.CartographerService_ExportGraphClient
// that yields a single chunk and then returns the configured error from the
// next Recv — pinning the relay's mid-stream upstream branch (at least one
// response already forwarded) when the upstream breaks after streaming started.
type mockExportClientChunkThenErr struct {
	err   error
	calls int
}

func (m *mockExportClientChunkThenErr) Recv() (*flowv1.ExportGraphResponse, error) {
	m.calls++
	if m.calls == 1 {
		return &flowv1.ExportGraphResponse{Chunk: []byte("chunk")}, nil
	}
	return nil, m.err
}

func (mockExportClientChunkThenErr) Context() context.Context { return context.Background() }
func (mockExportClientChunkThenErr) Header() (metadata.MD, error) {
	return nil, nil
}
func (mockExportClientChunkThenErr) Trailer() metadata.MD { return nil }
func (mockExportClientChunkThenErr) CloseSend() error     { return nil }
func (mockExportClientChunkThenErr) SendMsg(any) error    { return nil }
func (mockExportClientChunkThenErr) RecvMsg(any) error    { return nil }

// mockCartographerClientExport is a flowv1.CartographerServiceClient that
// answers ExportGraph with a fake client stream. err is returned from the
// first Recv (a pre-stream failure when set); chunkErr is returned from the
// second Recv, after one chunk (a mid-stream failure when set). The embedded
// interface satisfies the remaining client methods, which the relay never calls.
type mockCartographerClientExport struct {
	flowv1.CartographerServiceClient
	err      error
	chunkErr error
}

func (m *mockCartographerClientExport) ExportGraph(
	ctx context.Context, in *flowv1.ExportGraphRequest, opts ...grpc.CallOption,
) (flowv1.CartographerService_ExportGraphClient, error) {
	if m.chunkErr != nil {
		return &mockExportClientChunkThenErr{err: m.chunkErr}, nil
	}
	return &mockExportClientStream{failErr: m.err}, nil
}

// mockExportServerStream is a grpc.ServerStreamingServer[flowv1.ExportGraphResponse]
// whose Send records the number of chunks and returns err (nil → success),
// pinning the relay's downstream failure branch.
type mockExportServerStream struct {
	ctx  context.Context
	err  error
	sent int
}

func (m *mockExportServerStream) Send(*flowv1.ExportGraphResponse) error {
	m.sent++
	return m.err
}

func (m *mockExportServerStream) SetHeader(metadata.MD) error  { return nil }
func (m *mockExportServerStream) SendHeader(metadata.MD) error { return nil }
func (m *mockExportServerStream) SetTrailer(metadata.MD)       {}
func (m *mockExportServerStream) Context() context.Context {
	if m.ctx == nil {
		return context.Background()
	}
	return m.ctx
}
func (m *mockExportServerStream) SendMsg(any) error { return nil }
func (m *mockExportServerStream) RecvMsg(any) error { return nil }

// TestCartographerProxy_ExportGraph_MidStreamFailureIsInternal pins the SPEC
// error-table row "ExportGraph mid-stream failure → INTERNAL" (SPEC R11) at the
// Sidecar relay, matching the operator proxy (foundrygraph_proxyserver.go) and
// the Cartographer service handler (errExportGraphMidStream): an upstream Recv
// break AFTER the stream has started — a transport-level Unavailable or a raw
// error — and a downstream Send failure must each surface as INTERNAL, never the
// raw (non-INTERNAL) status. A pre-chunk (stream-establishment) Unavailable is
// the sibling UNAVAILABLE case, pinned separately by
// TestCartographerProxy_ExportGraph_StreamEstablishmentUnavailable.
func TestCartographerProxy_ExportGraph_MidStreamFailureIsInternal(t *testing.T) {
	// A raw (non-status) Recv error — a non-conforming upstream — is a genuine
	// mid-stream failure even at the first Recv: surface INTERNAL.
	upstreamBreaks := []struct {
		name string
		err  error
	}{
		{"raw", errors.New("malformed stream chunk")},
	}
	for _, tc := range upstreamBreaks {
		t.Run("recv_"+tc.name, func(t *testing.T) {
			p := &CartographerProxy{client: &mockCartographerClientExport{err: tc.err}}
			err := p.ExportGraph(&flowv1.ExportGraphRequest{Format: "json"}, &mockExportServerStream{})
			if st := status.Code(err); st != codes.Internal {
				t.Fatalf("expected INTERNAL for mid-stream Recv failure, got %v", err)
			}
		})
	}

	t.Run("recv_midstream_unavailable", func(t *testing.T) {
		// A transport-level break AFTER at least one chunk has been forwarded
		// is a genuine mid-stream failure (partial data may already have been
		// sent) → INTERNAL, distinct from the pre-chunk stream-establishment
		// Unavailable.
		p := &CartographerProxy{client: &mockCartographerClientExport{
			chunkErr: status.Error(codes.Unavailable, "connection reset mid-stream"),
		}}
		err := p.ExportGraph(&flowv1.ExportGraphRequest{Format: "json"}, &mockExportServerStream{})
		if st := status.Code(err); st != codes.Internal {
			t.Fatalf("expected INTERNAL for a mid-stream Unavailable, got %v", err)
		}
	})

	t.Run("send", func(t *testing.T) {
		p := &CartographerProxy{client: &mockCartographerClientExport{}}
		stream := &mockExportServerStream{err: errors.New("client stream write failed")}
		err := p.ExportGraph(&flowv1.ExportGraphRequest{Format: "json"}, stream)
		if st := status.Code(err); st != codes.Internal {
			t.Fatalf("expected INTERNAL for downstream Send failure, got %v", err)
		}
		if stream.sent == 0 {
			t.Fatal("expected Send to have been exercised before failing")
		}
	})
}

// TestCartographerProxy_ExportGraph_StreamEstablishmentUnavailable pins the
// stream-establishment transport failure at the Sidecar relay, matching the
// operator proxy's "cannot start export stream" → UNAVAILABLE mapping
// (foundrygraph_proxyserver.go / TestExportGraphCannotStartStreamIsUnavailable):
// an upstream Unavailable received at the first Recv with no chunk forwarded —
// the Cartographer could not be reached — must surface as UNAVAILABLE, not
// INTERNAL (which is reserved for a genuine mid-stream failure after data has
// been sent). The Sidecar's lazy grpc.NewClient dial delivers connection
// failures on the first Recv rather than on the stream call itself, so this is
// the Sidecar's equivalent of the operator's client-call Unavailable.
func TestCartographerProxy_ExportGraph_StreamEstablishmentUnavailable(t *testing.T) {
	p := &CartographerProxy{client: &mockCartographerClientExport{
		err: status.Error(codes.Unavailable, "connection refused"),
	}}
	stream := &mockExportServerStream{}
	err := p.ExportGraph(&flowv1.ExportGraphRequest{Format: "json"}, stream)
	if st := status.Code(err); st != codes.Unavailable {
		t.Fatalf("expected UNAVAILABLE for a stream-establishment transport failure, got %v (%v)", st, err)
	}
	if stream.sent != 0 {
		t.Error("expected no chunks forwarded for a stream-establishment failure")
	}
}

// TestCartographerProxy_ExportGraph_PreStreamRejectionPassesThrough pins the
// SPEC error-table rows "Unsupported export format" → INVALID_ARGUMENT and
// "ExportGraph buffer allocation failure" → RESOURCE_EXHAUSTED (both "no data
// sent") at the Sidecar relay, matching the operator proxy
// (foundrygraph_proxyserver.go / TestExportGraphPreStreamRejectionPassesThrough):
// a status the Cartographer returns BEFORE sending any chunk must surface
// through the relay verbatim — preserving the upstream status code and message —
// rather than being flattened to INTERNAL, so an SDK caller
// (graph.ExportGraph("bogus"), which performs no local format validation)
// receives the documented error code. Once at least one chunk has been
// forwarded, the same upstream status is a genuine mid-stream failure (partial
// data may already have been sent) and maps to INTERNAL per the SPEC error
// table row "ExportGraph mid-stream failure".
func TestCartographerProxy_ExportGraph_PreStreamRejectionPassesThrough(t *testing.T) {
	for _, tc := range []struct {
		name string
		code codes.Code
	}{
		{"unsupported format", codes.InvalidArgument},
		{"buffer allocation failure", codes.ResourceExhausted},
	} {
		t.Run("prestream_"+tc.name, func(t *testing.T) {
			p := &CartographerProxy{client: &mockCartographerClientExport{
				err: status.Error(tc.code, "rejected before any chunk was sent"),
			}}
			stream := &mockExportServerStream{}
			err := p.ExportGraph(&flowv1.ExportGraphRequest{Format: "bogus"}, stream)
			if err == nil {
				t.Fatal("expected an error on a pre-stream rejection")
			}
			if st := status.Code(err); st != tc.code {
				t.Fatalf("expected %v to pass through verbatim, got %v (%v)", tc.code, st, err)
			}
			if stream.sent != 0 {
				t.Error("expected no chunks forwarded for a pre-stream rejection")
			}
		})
	}

	t.Run("midstream_after_chunk_is_internal", func(t *testing.T) {
		// A chunk was already forwarded when the upstream breaks with a
		// pre-stream rejection code: the sentAny guard flips it to the SPEC's
		// mid-stream INTERNAL, not a verbatim pass-through.
		p := &CartographerProxy{client: &mockCartographerClientExport{
			chunkErr: status.Error(codes.InvalidArgument, "rejected after one chunk"),
		}}
		stream := &mockExportServerStream{}
		err := p.ExportGraph(&flowv1.ExportGraphRequest{Format: "json"}, stream)
		if st := status.Code(err); st != codes.Internal {
			t.Fatalf("expected INTERNAL for a mid-stream failure, got %v (%v)", st, err)
		}
		if stream.sent == 0 {
			t.Fatal("expected at least one chunk forwarded before the mid-stream failure")
		}
	})
}

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
		t.Fatalf("UpdateEntity with held type should pass the mode-1 metadata check: %v", err)
	}
	if capture.lastMethod() != methodUpdateEntity {
		t.Fatalf("expected UpdateEntity to reach the Cartographer, got %q", capture.lastMethod())
	}

	if _, err := tx.CreateEdge("DEPENDS_ON", entityID, targetID, nil); err != nil {
		t.Fatalf("CreateEdge with held source type should pass the mode-1 metadata check: %v", err)
	}
	if capture.lastMethod() != methodCreateEdge {
		t.Fatalf("expected CreateEdge to reach the Cartographer, got %q", capture.lastMethod())
	}

	if _, err := tx.DeleteEntity(entityID); err != nil {
		t.Fatalf("DeleteEntity with held type should pass the mode-1 metadata check: %v", err)
	}
	if capture.lastMethod() != methodDeleteEntity {
		t.Fatalf("expected DeleteEntity to reach the Cartographer, got %q", capture.lastMethod())
	}

	// The forwarded request carries the entity_type metadata the Sidecar
	// consumed for its mode-1 specific-type check.
	if got := capture.metadata().Get("entity_type"); len(got) != 1 || got[0] != entityTypeComponent {
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
	// The fake Cartographer reports the created entity as type Service, seeding
	// entityID → Service into the SDK's TTL-fresh mapping while the node only
	// holds WRITE:graph/entity/Component.
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
	before := capture.count()

	assertBlocked := func(name string, call func() error) {
		t.Helper()
		if err := call(); err == nil {
			t.Fatalf("%s: expected PERMISSION_DENIED for a resolved type the node lacks, got nil error", name)
		} else if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
			t.Fatalf("%s: expected PermissionDenied, got %v", name, err)
		}
	}

	assertBlocked(methodUpdateEntity, func() error {
		_, err := tx.UpdateEntity(entityID, nil, nil)
		return err
	})
	assertBlocked(methodDeleteEntity, func() error {
		_, err := tx.DeleteEntity(entityID)
		return err
	})
	assertBlocked(methodCreateEdge, func() error {
		_, err := tx.CreateEdge("DEPENDS_ON", entityID, targetID, nil)
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

// TestCartographerProxy_E2E_TxLifecycle_PassesWithTxGrants pins the
// transaction-family fixed gates (SPEC R3): a node holding WRITE:graph/tx (and
// READ:graph/tx for GetTransactionDiff) has every transaction RPC forwarded to
// the Cartographer — BeginTransaction, CommitTransaction, RollbackTransaction,
// RefreshTransaction, ExtendTimeout, and GetTransactionDiff.
func TestCartographerProxy_E2E_TxLifecycle_PassesWithTxGrants(t *testing.T) {
	const caps = "WRITE:graph/tx,READ:graph/tx"
	capture, client, _ := setupCartographerWire(t, caps)
	graph := client.GetGraph()

	tx, err := graph.BeginTransaction()
	if err != nil {
		t.Fatalf("BeginTransaction with WRITE:graph/tx: %v", err)
	}
	if capture.lastMethod() != methodBeginTransaction {
		t.Fatalf("expected the Cartographer to receive BeginTransaction, got %q", capture.lastMethod())
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("CommitTransaction with WRITE:graph/tx: %v", err)
	}
	if capture.lastMethod() != methodCommitTransaction {
		t.Fatalf("expected the Cartographer to receive CommitTransaction, got %q", capture.lastMethod())
	}

	tx2, err := graph.BeginTransaction()
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if err := tx2.Rollback(); err != nil {
		t.Fatalf("RollbackTransaction with WRITE:graph/tx: %v", err)
	}
	if capture.lastMethod() != methodRollbackTransaction {
		t.Fatalf("expected the Cartographer to receive RollbackTransaction, got %q", capture.lastMethod())
	}

	tx3, err := graph.BeginTransaction()
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if err := tx3.Refresh(); err != nil {
		t.Fatalf("RefreshTransaction with WRITE:graph/tx: %v", err)
	}
	if capture.lastMethod() != methodRefreshTransaction {
		t.Fatalf("expected the Cartographer to receive RefreshTransaction, got %q", capture.lastMethod())
	}

	tx4, err := graph.BeginTransaction()
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := tx4.ExtendTimeout(time.Hour); err != nil {
		t.Fatalf("ExtendTimeout with WRITE:graph/tx: %v", err)
	}
	if capture.lastMethod() != methodExtendTimeout {
		t.Fatalf("expected the Cartographer to receive ExtendTimeout, got %q", capture.lastMethod())
	}

	tx5, err := graph.BeginTransaction()
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := tx5.Diff(); err != nil {
		t.Fatalf("GetTransactionDiff with READ:graph/tx: %v", err)
	}
	if capture.lastMethod() != methodGetTransactionDiff {
		t.Fatalf("expected the Cartographer to receive GetTransactionDiff, got %q", capture.lastMethod())
	}
}

// TestCartographerProxy_E2E_TxGate_BlocksWithoutTxGrants pins the fixed-gate
// block side (SPEC R3): a node holding no WRITE:graph/tx or READ:graph/tx
// grant is denied at the Sidecar with PERMISSION_DENIED for every transaction
// RPC, and no request reaches the Cartographer. The RPCs are issued with a raw
// client because the SDK cannot obtain a transaction handle without the
// WRITE:graph/tx grant (BeginTransaction is itself gated).
func TestCartographerProxy_E2E_TxGate_BlocksWithoutTxGrants(t *testing.T) {
	const caps = "READ:graph/entity/*"
	w := setupCartographerWireFull(t, caps)
	ctx := context.Background()

	txRPCCalls := []struct {
		name string
		call func() error
	}{
		{methodBeginTransaction, func() error {
			_, err := w.rawClient.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
			return err
		}},
		{methodCommitTransaction, func() error {
			_, err := w.rawClient.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: txID})
			return err
		}},
		{methodRollbackTransaction, func() error {
			_, err := w.rawClient.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{TransactionId: txID})
			return err
		}},
		{methodRefreshTransaction, func() error {
			_, err := w.rawClient.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{TransactionId: txID})
			return err
		}},
		{methodGetTransactionDiff, func() error {
			_, err := w.rawClient.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: txID})
			return err
		}},
		{methodExtendTimeout, func() error {
			_, err := w.rawClient.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{TransactionId: txID})
			return err
		}},
	}
	for _, c := range txRPCCalls {
		if err := c.call(); err == nil {
			t.Fatalf("%s: expected PERMISSION_DENIED without a tx grant, got nil error", c.name)
		} else if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
			t.Fatalf("%s: expected PermissionDenied, got %v", c.name, err)
		}
	}
	if w.capture.count() != 0 {
		t.Fatalf("fixed tx gate must prevent every RPC from reaching the Cartographer, got %d requests", w.capture.count())
	}
}

// TestCartographerProxy_E2E_Sync_PassesWithWildcardWrite pins the WRITE
// admin-gate success path (SPEC R3): Sync requires WRITE:graph/entity/*, and a
// node holding the wildcard grant has the request forwarded to the
// Cartographer.
func TestCartographerProxy_E2E_Sync_PassesWithWildcardWrite(t *testing.T) {
	const caps = "WRITE:graph/entity/*"
	capture, client, _ := setupCartographerWire(t, caps)

	if err := client.GetGraph().Sync(); err != nil {
		t.Fatalf("Sync with WRITE:graph/entity/* should pass the fixed gate: %v", err)
	}
	if capture.lastMethod() != methodSync {
		t.Fatalf("expected the Cartographer to receive Sync, got %q", capture.lastMethod())
	}
}

// TestCartographerProxy_E2E_Sync_BlocksWithoutWildcardWrite pins the WRITE
// admin-gate block side (SPEC R3): a node holding READ capabilities but no
// WRITE:graph/entity/* grant is denied at the Sidecar with PERMISSION_DENIED
// and Sync never reaches the Cartographer.
func TestCartographerProxy_E2E_Sync_BlocksWithoutWildcardWrite(t *testing.T) {
	const caps = "READ:graph/entity/*"
	capture, client, _ := setupCartographerWire(t, caps)
	before := capture.count()

	if err := client.GetGraph().Sync(); err == nil {
		t.Fatal("expected Sync to be blocked without WRITE:graph/entity/*")
	} else if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
	if capture.count() != before {
		t.Fatal("fixed gate block must prevent Sync from reaching the Cartographer")
	}
}

// TestCartographerProxy_E2E_ExportGraph_BlocksWithoutWildcardRead pins the
// READ admin-gate block side (SPEC R3): ExportGraph requires READ:graph/entity/*,
// and a node holding no read grant is denied at the Sidecar with
// PERMISSION_DENIED before the stream is established — the Cartographer never
// receives the request.
func TestCartographerProxy_E2E_ExportGraph_BlocksWithoutWildcardRead(t *testing.T) {
	const caps = "WRITE:graph/entity/*"
	capture, client, _ := setupCartographerWire(t, caps)

	// ExportGraph is server-streaming: the Sidecar's status error is delivered
	// on the stream (Recv), not on the establishment call itself.
	stream, err := client.GetGraph().ExportGraph("json")
	if err == nil {
		defer stream.Stop()
		_, err = stream.Recv()
	}
	if err == nil {
		t.Fatal("expected ExportGraph to be blocked without READ:graph/entity/*")
	} else if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
	if capture.lastExportReq() != nil {
		t.Fatal("fixed gate block must prevent ExportGraph from reaching the Cartographer")
	}
}

// TestCartographerProxy_E2E_Mode1_SpecificTypeRead_PassesWithHeldType pins the
// mode-1 specific-type READ branch (SPEC R3:262): a node holding
// READ:graph/entity/Component has SearchNeighbors, FullTextSearch, and
// ListEntities for that type forwarded to the Cartographer.
func TestCartographerProxy_E2E_Mode1_SpecificTypeRead_PassesWithHeldType(t *testing.T) {
	const caps = "READ:graph/entity/Component"
	capture, client, _ := setupCartographerWire(t, caps)
	graph := client.GetGraph()

	if _, err := graph.SearchNeighbors([]float32{0.1, 0.2}, entityTypeComponent, 10); err != nil {
		t.Fatalf("SearchNeighbors with held type should pass mode-1: %v", err)
	}
	if capture.lastMethod() != methodSearchNeighbors {
		t.Fatalf("expected the Cartographer to receive SearchNeighbors, got %q", capture.lastMethod())
	}

	if _, err := graph.FullTextSearch("auth service", entityTypeComponent); err != nil {
		t.Fatalf("FullTextSearch with held type should pass mode-1: %v", err)
	}
	if capture.lastMethod() != methodFullTextSearch {
		t.Fatalf("expected the Cartographer to receive FullTextSearch, got %q", capture.lastMethod())
	}

	if _, err := graph.ListEntities(entityTypeComponent); err != nil {
		t.Fatalf("ListEntities with held type should pass mode-1: %v", err)
	}
	if capture.lastMethod() != methodListEntities {
		t.Fatalf("expected the Cartographer to receive ListEntities, got %q", capture.lastMethod())
	}
}

// TestCartographerProxy_E2E_Mode1_SpecificTypeRead_BlocksUnheldType pins the
// mode-1 specific-type READ block side (SPEC R3:262): a node holding only
// READ:graph/entity/Service cannot read entities of type Component — each
// request is denied at the Sidecar with PERMISSION_DENIED and never reaches
// the Cartographer.
func TestCartographerProxy_E2E_Mode1_SpecificTypeRead_BlocksUnheldType(t *testing.T) {
	const caps = "READ:graph/entity/Service"
	capture, client, _ := setupCartographerWire(t, caps)
	graph := client.GetGraph()
	before := capture.count()

	readCalls := []struct {
		name string
		call func() error
	}{
		{methodSearchNeighbors, func() error {
			_, err := graph.SearchNeighbors([]float32{0.1, 0.2}, entityTypeComponent, 10)
			return err
		}},
		{methodFullTextSearch, func() error {
			_, err := graph.FullTextSearch("auth service", entityTypeComponent)
			return err
		}},
		{methodListEntities, func() error {
			_, err := graph.ListEntities(entityTypeComponent)
			return err
		}},
	}
	for _, c := range readCalls {
		if err := c.call(); err == nil {
			t.Fatalf("%s: expected PERMISSION_DENIED for an unheld type, got nil error", c.name)
		} else if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
			t.Fatalf("%s: expected PermissionDenied, got %v", c.name, err)
		}
	}
	if capture.count() != before {
		t.Fatal("mode-1 block must prevent the requests from reaching the Cartographer")
	}
}

// TestCartographerProxy_E2E_SessionMode_SignsCapabilitiesWithKey pins the
// identity interceptor's session-mode enrichment branch (SPEC R3 / Capability
// Authorisation Chain): when the SDK's workitem has an active assignment
// session, the interceptor resolves the node identity from the session and
// signs the attested capabilities with the Sidecar's configured key. The
// session's node identity differs from the Sidecar's entry-bound fallback
// identity, so the test proves the session branch (not the fallback) produced
// the signed metadata.
func TestCartographerProxy_E2E_SessionMode_SignsCapabilitiesWithKey(t *testing.T) {
	const caps = "READ:graph/entity/*"
	w := setupCartographerWireFull(t, caps)
	// Register the SDK's workitem as an active assignment whose session node
	// identity differs from the Sidecar's fallback node identity.
	w.sidecarSrv.InjectSessionForTest("wi-1", "session-node")

	if _, err := w.client.GetGraph().ExecuteCypher("MATCH (c:Component) RETURN c", nil); err != nil {
		t.Fatalf("session-mode ExecuteCypher via SDK→Sidecar→Cartographer failed: %v", err)
	}
	if w.capture.count() == 0 {
		t.Fatal("expected the fake Cartographer to receive the ExecuteCypher request")
	}
	assertSignedCapabilitiesOnMD(t, w.sidecarPub, w.capture.metadata(), caps)
	if got := w.capture.metadata().Get(flowmeta.MetadataKeyNodeID); len(got) != 1 || got[0] != "session-node" {
		t.Fatalf("expected the session's node identity to be injected, got %v", got)
	}
}
