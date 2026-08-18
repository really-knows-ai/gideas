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
	"github.com/foundry/flow/sidecar/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
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
// setupCartographerWireFull: the capture server, the Sidecar's public key
// (for signature verification), the Sidecar server (for session injection),
// and a raw Cartographer client dialing the Sidecar (for direct RPC
// invocation without the SDK domain-object layer).
type cartographerWire struct {
	capture    *captureCartographerServer
	sidecarPub ed25519.PublicKey
	sidecarSrv *service.SidecarServer
	rawClient  flowv1.CartographerServiceClient
}

// setupCartographerWireFull spins up an in-process fake Cartographer, a real
// Sidecar gRPC server with the CartographerProxy + identity interceptors +
// signing key, and a raw Cartographer client dialing the Sidecar. It returns
// the capture server, the Sidecar's public key for signature verification,
// the Sidecar server, and the raw client.
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

	// Raw Cartographer client dialing the Sidecar directly.
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
		sidecarPub: pub,
		sidecarSrv: sidecarSrv,
		rawClient:  flowv1.NewCartographerServiceClient(rawConn),
	}
}

// setupCartographerWire spins up the wire harness and returns the capture
// server, a raw Cartographer client dialing the Sidecar, and the Sidecar's
// public key for signature verification. It is the convenience form used by
// the wire tests.
func setupCartographerWire(
	t *testing.T, nodeCaps string,
) (*captureCartographerServer, flowv1.CartographerServiceClient, ed25519.PublicKey) {
	t.Helper()
	w := setupCartographerWireFull(t, nodeCaps)
	return w.capture, w.rawClient, w.sidecarPub
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
