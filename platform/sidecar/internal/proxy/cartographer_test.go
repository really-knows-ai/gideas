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
	return &flowv1.CreateEntityResponse{EntityId: "entity-1", EntityType: req.GetEntityType()}, nil
}

func (s *captureCartographerServer) DeleteEdge(
	ctx context.Context, req *flowv1.DeleteEdgeRequest,
) (*flowv1.DeleteEdgeResponse, error) {
	s.record(ctx, "DeleteEdge")
	return &flowv1.DeleteEdgeResponse{EdgeId: req.GetId(), EdgeType: "DEPENDS_ON"}, nil
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
	_, err = tx.CreateEntity("Component", nil, nil, nil)
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
	entity, err := tx.CreateEntity("Component", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateEntity with WRITE:graph/entity/* should pass mode-1: %v", err)
	}
	if entity.ID != "entity-1" || entity.Type != "Component" {
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
	edge, err := tx.DeleteEdge("edge-1")
	if err != nil {
		t.Fatalf("DeleteEdge mode-2 pass-through failed: %v", err)
	}
	if edge.ID != "edge-1" {
		t.Fatalf("expected forwarded edge id edge-1, got %q", edge.ID)
	}
	if capture.lastMethod() != "DeleteEdge" {
		t.Fatalf("expected the Cartographer to receive DeleteEdge, got %q", capture.lastMethod())
	}
	assertSignedCapabilitiesOnMD(t, pub, capture.metadata(), caps)
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
