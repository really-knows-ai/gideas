package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

const bufSize = 1024 * 1024

// =========================================================================
// Test utilities
// =========================================================================

func generateTestKey() (ed25519.PublicKey, ed25519.PrivateKey) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(fmt.Sprintf("failed to generate test key: %v", err))
	}
	return pub, priv
}

func signCapabilities(caps string, priv ed25519.PrivateKey) (sig string, unixTimestamp int64) {
	unixTimestamp = time.Now().Unix()
	payload := fmt.Sprintf("%s|%d", caps, unixTimestamp)
	rawSig := ed25519.Sign(priv, []byte(payload))
	sig = base64.StdEncoding.EncodeToString(rawSig)
	return
}

// capabilityContext returns a context with signed capability metadata.
// The signedBy parameter must be "operator" or "sidecar".
func capabilityContext(caps string, priv ed25519.PrivateKey, signedBy string) context.Context {
	sig, signedAt := signCapabilities(caps, priv)
	md := metadata.Pairs(
		MetadataKeyCapabilities, caps,
		MetadataKeyCapabilitiesSignature, sig,
		MetadataKeyCapabilitiesSignedAt, fmt.Sprintf("%d", signedAt),
		MetadataKeyCapabilitiesSignedBy, signedBy,
	)
	return metadata.NewIncomingContext(context.Background(), md)
}

// fakeClock implements Clock for testing.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t} }
func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}
func (f *fakeClock) NewTicker(d time.Duration) Ticker { return &fakeTicker{ch: make(chan time.Time)} }
func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

type fakeTicker struct{ ch chan time.Time }

func (f *fakeTicker) C() <-chan time.Time { return f.ch }
func (f *fakeTicker) Stop()               {}

type mockTelemetryPublisher struct {
	mu     sync.Mutex
	events []*flowv1.PublishRequest
}

func (m *mockTelemetryPublisher) Submit(req *flowv1.PublishRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, req)
}
func (m *mockTelemetryPublisher) Events() []*flowv1.PublishRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := make([]*flowv1.PublishRequest, len(m.events))
	copy(r, m.events)
	return r
}

// newTestServer creates a CartographerServer with in-memory store and temp gitstore.
func newTestServer(t *testing.T) (*CartographerServer, store.Store, gitstore.GitStore) {
	t.Helper()
	st, err := store.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	srv := NewCartographerServer(
		st, gs, make([]byte, 32), make([]byte, 32),
		nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
	)
	srv.MarkDBReady()
	return srv, st, gs
}

// applyTestSchema applies a minimal test schema.
func applyTestSchema(ctx context.Context, t *testing.T, st store.Store) {
	t.Helper()
	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string", Required: true},
					{Name: "version", Type: "string"},
				},
			},
			{
				Name: "Service",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string", Required: true},
				},
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "weight", Type: "string"}}},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
}

// =========================================================================
// 1. Capability verification tests
// =========================================================================

func TestCapability_ValidSignature(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)

	ctx := capabilityContext("READ:graph/entity/Component", scPriv, "sidecar")
	ctx, err := srv.verifier.verify(ctx)
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	caps, err := ExtractCapabilities(ctx)
	if err != nil {
		t.Fatalf("ExtractCapabilities failed: %v", err)
	}
	if caps == nil {
		t.Fatal("expected non-nil capabilities")
	}
	if err := srv.verifier.CheckSpecificType(caps, "READ", "Component"); err != nil {
		t.Fatalf("CheckSpecificType failed: %v", err)
	}
}

func TestCapability_InvalidSignature(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	_, wrongPriv := generateTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)

	ctx := capabilityContext("READ:graph/entity/Component", wrongPriv, "operator")
	_, err := srv.verifier.verify(ctx)
	if err == nil {
		t.Fatal("expected error for invalid signature, got nil")
	}
}

func TestCapability_MissingMetadata(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)

	// No metadata context -> should pass through (system-to-system call).
	ctx := context.Background()
	ctx, err := srv.verifier.verify(ctx)
	if err != nil {
		t.Fatalf("expected no error for missing metadata, got: %v", err)
	}
	caps, err := ExtractCapabilities(ctx)
	if err != nil {
		t.Fatalf("ExtractCapabilities failed: %v", err)
	}
	if caps != nil {
		t.Fatal("expected nil capabilities for system-to-system call")
	}
}

func TestCapability_UnrecognizedSigner(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)

	sig := base64.StdEncoding.EncodeToString([]byte("fake"))
	md := metadata.Pairs(
		MetadataKeyCapabilities, "READ:graph/entity/Component",
		MetadataKeyCapabilitiesSignature, sig,
		MetadataKeyCapabilitiesSignedAt, "1234567890",
		MetadataKeyCapabilitiesSignedBy, "unknown",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := srv.verifier.verify(ctx)
	if err == nil {
		t.Fatal("expected error for unrecognized signer, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

func TestCapability_StaleCapability(t *testing.T) {
	opPub, opPriv := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)

	// Sign with a past timestamp (older than 30s staleness window).
	pastTime := time.Now().Add(-2 * time.Minute).Unix()
	payload := fmt.Sprintf("READ:graph/entity/Component|%d", pastTime)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(opPriv, []byte(payload)))
	md := metadata.Pairs(
		MetadataKeyCapabilities, "READ:graph/entity/Component",
		MetadataKeyCapabilitiesSignature, sig,
		MetadataKeyCapabilitiesSignedAt, fmt.Sprintf("%d", pastTime),
		MetadataKeyCapabilitiesSignedBy, "sidecar",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := srv.verifier.verify(ctx)
	if err == nil {
		t.Fatal("expected error for stale capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

// =========================================================================
// 2. Service-facing RPC tests (no capabilities needed)
// =========================================================================

func TestApplySchema_Valid(t *testing.T) {
	srv, st, _ := newTestServer(t)
	ctx := context.Background()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
		},
	}
	_, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: schema})
	if err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	_ = st
}

func TestApplySchema_Idempotent(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
		},
	}
	if _, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: schema}); err != nil {
		t.Fatalf("first ApplySchema failed: %v", err)
	}
	if _, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: schema}); err != nil {
		t.Fatalf("second ApplySchema failed: %v", err)
	}
}

func TestHealthCheck_Healthy(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.HealthCheck(ctx, &flowv1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("HealthCheck failed: %v", err)
	}
	if !resp.LadybugOk {
		t.Fatal("expected LadybugOk to be true")
	}
}

func TestWipeGraph_WithOpenTx(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	// Create an active transaction.
	srv.dbReady.Store(true)
	applyTestSchema(ctx, t, srv.store)
	err := srv.store.CreateBranchDB("test-tx")
	if err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	srv.txManager.Create("test-tx", 5*time.Minute, "head")

	_, err = srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{})
	if err == nil {
		t.Fatal("expected error for WipeGraph with open transactions, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", status.Code(err))
	}
}

// =========================================================================
// 3. Read-path tests
// =========================================================================

func TestExecuteCypher_EmptyQuery(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: ""})
	if err == nil {
		t.Fatal("expected error for empty query, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestExecuteCypher_ValidQuery(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	_, _ = srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "test"}, nil, "")

	resp, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: "MATCH (n:Component) RETURN n"})
	if err != nil {
		t.Fatalf("ExecuteCypher failed: %v", err)
	}
	if len(resp.Rows) == 0 {
		t.Fatal("expected at least one row")
	}
}

func TestExecuteCypher_MutationRejected(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: "CREATE (n:Test)"})
	if err == nil {
		t.Fatal("expected error for mutation, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

func TestFullTextSearch_EmptyQuery(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	_, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{Query: ""})
	if err == nil {
		t.Fatal("expected error for empty query, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestFullTextSearch_Valid(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	_, _ = srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "apple", "version": "1"}, nil, "")

	resp, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{Query: "apple"})
	if err != nil {
		t.Fatalf("FullTextSearch failed: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected at least one result")
	}
}

func TestListEntities_UnknownType(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	_, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{EntityType: "NonExistent"})
	if err == nil {
		t.Fatal("expected error for unknown type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestListEntities_Valid(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	_, _ = srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "a"}, nil, "")
	_, _ = srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "b"}, nil, "")

	resp, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
		EntityType: "Component",
		PageSize:   10,
	})
	if err != nil {
		t.Fatalf("ListEntities failed: %v", err)
	}
	if len(resp.Entities) != 2 {
		t.Fatalf("expected 2 entities, got %d", len(resp.Entities))
	}
}

func TestSearchNeighbors_NonIndexed(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{1.0, 2.0, 3.0},
		EntityType: "Component",
		TopK:       5,
	})
	if err == nil {
		t.Fatal("expected error for non-indexed type, got nil")
	}
}

// =========================================================================
// 4. Write-path tests
// =========================================================================

func TestCreateEntity_Valid(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	resp, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component",
		Properties: map[string]string{"name": "test", "version": "1.0"},
	})
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}
	if resp.EntityId == "" {
		t.Fatal("expected non-empty entity ID")
	}
	if resp.EntityType != "Component" {
		t.Fatalf("expected Component, got %q", resp.EntityType)
	}
}

func TestCreateEntity_UnknownType(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Unknown",
		Properties: map[string]string{"name": "x"},
	})
	if err == nil {
		t.Fatal("expected error for unknown type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEntity_DuplicateID(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	resp, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component",
		Properties: map[string]string{"name": "first"},
	})
	if err != nil {
		t.Fatalf("first CreateEntity failed: %v", err)
	}
	// Same ID again.
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component",
		Id:         resp.EntityId,
		Properties: map[string]string{"name": "second"},
	})
	if err == nil {
		t.Fatal("expected error for duplicate ID, got nil")
	}
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %v", status.Code(err))
	}
}

func TestCreateEntity_MissingRequiredProperty(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component",
		Properties: map[string]string{},
	})
	if err == nil {
		t.Fatal("expected error for missing required property, got nil")
	}
}

func TestUpdateEntity_NotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:         "00000000-0000-0000-0000-000000000000",
		Properties: map[string]string{"name": "x"},
	})
	if err == nil {
		t.Fatal("expected error for not-found entity, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

func TestUpdateEntity_Valid(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	ent, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "original"}, nil, "")

	resp, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:         ent.Id,
		Properties: map[string]string{"version": "2"},
	})
	if err != nil {
		t.Fatalf("UpdateEntity failed: %v", err)
	}
	if resp.Properties["version"] != "2" {
		t.Fatalf("expected version=2, got %q", resp.Properties["version"])
	}
}

func TestDeleteEntity_NotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	_, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{
		Id: "00000000-0000-0000-0000-000000000000",
	})
	if err == nil {
		t.Fatal("expected error for not-found entity, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

func TestDeleteEntity_Valid(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	ent, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "delete-me"}, nil, "")

	resp, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: ent.Id})
	if err != nil {
		t.Fatalf("DeleteEntity failed: %v", err)
	}
	if resp.EntityId != ent.Id {
		t.Fatalf("expected deleted entity ID %q, got %q", ent.Id, resp.EntityId)
	}
}

func TestCreateEdge_Valid(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	svc, _ := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, "")
	comp, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "core"}, nil, "")

	resp, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:     "DEPENDS_ON",
		FromEntityId: svc.Id,
		ToEntityId:   comp.Id,
		Properties:   map[string]string{"weight": "high"},
	})
	if err != nil {
		t.Fatalf("CreateEdge failed: %v", err)
	}
	if resp.EdgeId == "" {
		t.Fatal("expected non-empty edge ID")
	}
}

func TestCreateEdge_SourceNotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:     "DEPENDS_ON",
		FromEntityId: "00000000-0000-0000-0000-000000000000",
		ToEntityId:   "00000000-0000-0000-0000-000000000001",
	})
	if err == nil {
		t.Fatal("expected error for not-found source, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

func TestCreateEdge_UnknownEdgeType(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	ent, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "x"}, nil, "")

	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:     "UNKNOWN_EDGE",
		FromEntityId: ent.Id,
		ToEntityId:   ent.Id,
	})
	if err == nil {
		t.Fatal("expected error for unknown edge type, got nil")
	}
}

func TestDeleteEdge_NotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	_, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{
		Id: "00000000-0000-0000-0000-000000000000",
	})
	if err == nil {
		t.Fatal("expected error for not-found edge, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

// =========================================================================
// 5. Transaction lifecycle tests
// =========================================================================

func TestTransaction_BeginCommit(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)

	// Begin transaction.
	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId
	if txID == "" {
		t.Fatal("expected non-empty transaction ID")
	}

	// Create entity inside transaction.
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "tx-test"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity in tx failed: %v", err)
	}

	// Get transaction diff.
	diffResp, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: txID})
	if err != nil {
		t.Fatalf("GetTransactionDiff failed: %v", err)
	}
	if len(diffResp.AddedEntities) != 1 {
		t.Fatalf("expected 1 added entity, got %d", len(diffResp.AddedEntities))
	}

	// Commit.
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: txID})
	if err != nil {
		t.Fatalf("CommitTransaction failed: %v", err)
	}

	// Verify entity exists on main.
	ents, _, err := srv.store.ListEntities(ctx, "Component", 100, "", "")
	if err != nil {
		t.Fatalf("ListEntities failed: %v", err)
	}
	if len(ents) != 1 {
		t.Fatalf("expected 1 entity on main after commit, got %d", len(ents))
	}
}

func TestTransaction_BeginRollback(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "rollback-test"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity in tx failed: %v", err)
	}

	_, err = srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{TransactionId: txID})
	if err != nil {
		t.Fatalf("RollbackTransaction failed: %v", err)
	}

	// Verify entity does NOT exist on main.
	ents, _, err := srv.store.ListEntities(ctx, "Component", 100, "", "")
	if err != nil {
		t.Fatalf("ListEntities failed: %v", err)
	}
	if len(ents) != 0 {
		t.Fatalf("expected 0 entities after rollback, got %d", len(ents))
	}
}

func TestTransaction_ExtendTimeout(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	_, err = srv.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{
		TransactionId: txID,
		Duration:      durationpb.New(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("ExtendTimeout failed: %v", err)
	}
}

func TestTransaction_InvalidTxID(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	_, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{
		TransactionId: "not-a-uuid",
	})
	if err == nil {
		t.Fatal("expected error for invalid tx ID, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestTransaction_NotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	_, err := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: "00000000-0000-0000-0000-000000000000",
	})
	if err == nil {
		t.Fatal("expected error for not-found tx, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

// =========================================================================
// 6. Zero-mutation transaction tests
// =========================================================================

func TestEmptyTransaction_CommitNoOp(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}

	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: beginResp.TransactionId,
	})
	if err != nil {
		t.Fatalf("CommitTransaction (no-op) failed: %v", err)
	}
}

func TestEmptyTransaction_RollbackNoOp(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}

	_, err = srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
		TransactionId: beginResp.TransactionId,
	})
	if err != nil {
		t.Fatalf("RollbackTransaction (no-op) failed: %v", err)
	}
}

// =========================================================================
// 7. Concurrent access tests
// =========================================================================

func TestConcurrentNonTxWrites(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
				EntityType: "Component",
				Properties: map[string]string{"name": "concurrent"},
			})
			if err != nil && status.Code(err) != codes.AlreadyExists {
				t.Errorf("concurrent CreateEntity failed: %v", err)
			}
		}()
	}
	wg.Wait()
}

// =========================================================================
// 8. Export tests
// =========================================================================

func TestExportGraph_UnsupportedFormat(t *testing.T) {
	srv, _, _ := newTestServer(t)

	_, err := collectExportData(srv, "unsupported")
	if err == nil {
		t.Fatal("expected error for unsupported format, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestExportGraph_JSON(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	_, _ = srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "a"}, nil, "")
	_, _ = srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "b"}, nil, "")

	data, err := collectExportData(srv, "json")
	if err != nil {
		t.Fatalf("export JSON failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty export data")
	}
}

func TestExportGraph_GraphML(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	_, _ = srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "x"}, nil, "")

	data, err := collectExportData(srv, "graphml")
	if err != nil {
		t.Fatalf("export GraphML failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty export data")
	}
}

// =========================================================================
// 9. Telemetry tests
// =========================================================================

func TestTelemetry_TransactionGC(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())

	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)
	mockPub := &mockTelemetryPublisher{}
	srv.auditor = mockPub
	srv.dbReady.Store(true)

	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(30*time.Minute, 7*24*time.Hour, 100000, WithClock(fc))
	_, _ = srv.txManager.Create("test-tx-id", 1*time.Minute, "head")

	fc.Advance(2 * time.Minute)
	srv.gcTick()

	events := mockPub.Events()
	if len(events) == 0 {
		t.Fatal("expected at least one telemetry event")
	}
	found := false
	for _, e := range events {
		if e.Event != nil && e.Event.EventType == "cartographer.transaction_gc" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected telemetry event 'cartographer.transaction_gc'")
	}
}
