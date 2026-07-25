package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

const bufSize = 1024 * 1024

// testSidecarPriv is a test-level Ed25519 private key used for signing
// capability metadata in tests. It is lazily initialized by the first
// call to testCtx.
var testSidecarPriv ed25519.PrivateKey

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

// =========================================================================
// Helper wrappers for testing error paths
// =========================================================================

// wipeFailingStore fails on every call to WipeAll, used to test mid-wipe
// error handling in WipeGraph.
type wipeFailingStore struct {
	store.Store
}

func (w *wipeFailingStore) WipeAll(ctx context.Context) error {
	return fmt.Errorf("simulated WipeAll failure")
}

// failOnCreateBranchDBStore fails on CreateBranchDB, used to test
// RESOURCE_EXHAUSTED in BeginTransaction.
type failOnCreateBranchDBStore struct {
	store.Store
}

func (f *failOnCreateBranchDBStore) CreateBranchDB(txID string) error {
	return fmt.Errorf("simulated CreateBranchDB failure")
}

// panicStore panics on ListMainEntityTypes to simulate a buffer allocation
// panic inside collectExportData.
type panicStore struct {
	store.Store
}

func (p *panicStore) ListMainEntityTypes() ([]string, error) {
	panic("simulated OOM in export data collection")
}

// mockExportStream implements grpc.ServerStreamingServer[flowv1.ExportGraphResponse]
// for testing ExportGraph error paths.
type mockExportStream struct {
	ctx       context.Context
	sendErr   error
	sendCount int
}

func (m *mockExportStream) Send(resp *flowv1.ExportGraphResponse) error {
	m.sendCount++
	return m.sendErr
}
func (m *mockExportStream) Context() context.Context        { return m.ctx }
func (m *mockExportStream) SetTrailer(md metadata.MD)       {}
func (m *mockExportStream) SendHeader(md metadata.MD) error { return nil }
func (m *mockExportStream) SetHeader(md metadata.MD) error  { return nil }
func (m *mockExportStream) RecvMsg(any) error               { return nil }
func (m *mockExportStream) SendMsg(any) error               { return nil }

// initTestKey generates the shared test key pair once and returns the public key.
func initTestKey() ed25519.PublicKey {
	if testSidecarPriv != nil {
		return testSidecarPriv.Public().(ed25519.PublicKey)
	}
	pub, priv := generateTestKey()
	testSidecarPriv = priv
	return pub
}

// newTestServer creates a CartographerServer with in-memory store and temp gitstore.
func newTestServer(t *testing.T) (*CartographerServer, store.Store, gitstore.GitStore) {
	t.Helper()
	scPub := initTestKey()
	st, err := store.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		st, gs, opPub, scPub,
		nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
	)
	srv.MarkDBReady()
	return srv, st, gs
}

// testCtx returns a context with full wildcard capabilities (READ:graph/entity/*,
// WRITE:graph/entity/*, READ:graph/tx, WRITE:graph/tx) verified and stored in
// context (simulating the interceptor path).
func testCtx() context.Context {
	initTestKey()
	caps := "READ:graph/entity/*,WRITE:graph/entity/*,READ:graph/tx,WRITE:graph/tx"
	sig, signedAt := signCapabilities(caps, testSidecarPriv)
	md := metadata.Pairs(
		MetadataKeyCapabilities, caps,
		MetadataKeyCapabilitiesSignature, sig,
		MetadataKeyCapabilitiesSignedAt, fmt.Sprintf("%d", signedAt),
		MetadataKeyCapabilitiesSignedBy, "sidecar",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	// Store capabilities directly (simulates what VerifyInterceptor would do).
	c := &Capabilities{
		Caps:     []string{"READ:graph/entity/*", "WRITE:graph/entity/*", "READ:graph/tx", "WRITE:graph/tx"},
		SignedBy: "sidecar",
	}
	return StoreCapabilitiesInContext(ctx, c)
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
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
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
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)

	// Sign with a past timestamp (older than 30s staleness window).
	pastTime := time.Now().Add(-2 * time.Minute).Unix()
	payload := fmt.Sprintf("READ:graph/entity/Component|%d", pastTime)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(scPriv, []byte(payload)))
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

func TestApplySchema_InvalidSchema(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	// Duplicate entity type name is an invalid schema.
	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
			{Name: "Component", Properties: []*flowv1.Property{{Name: "name2", Type: "string"}}},
		},
	}
	_, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: schema})
	if err == nil {
		t.Fatal("expected error for duplicate entity type name, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
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
	if resp.SchemaApplied {
		t.Fatal("expected SchemaApplied to be false (no schema applied)")
	}
	if !resp.PvcWritable {
		t.Fatal("expected PvcWritable to be true")
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
	ctx := testCtx()

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
	ctx := testCtx()

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
	ctx := testCtx()

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
	ctx := testCtx()

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
	ctx := testCtx()

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
	ctx := testCtx()

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
	ctx := testCtx()

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
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{1.0, 2.0, 3.0},
		EntityType: "Component",
		TopK:       5,
	})
	if err == nil {
		t.Fatal("expected error for non-indexed type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// =========================================================================
// 4. Write-path tests
// =========================================================================

func TestCreateEntity_Valid(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

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
	ctx := testCtx()

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
	ctx := testCtx()

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
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component",
		Properties: map[string]string{},
	})
	if err == nil {
		t.Fatal("expected error for missing required property, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestUpdateEntity_NotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

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
	ctx := testCtx()

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
	ctx := testCtx()

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
	ctx := testCtx()

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
	ctx := testCtx()

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
	ctx := testCtx()

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
	ctx := testCtx()

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
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestDeleteEdge_NotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

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
	ctx := testCtx()

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
	ctx := testCtx()

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
	ctx := testCtx()

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

func TestTransaction_TimedOut(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	// Replace with fake clock so we can control time.
	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(30*time.Minute, 7*24*time.Hour, 100000, WithClock(fc))

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(1 * time.Minute),
	})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	// Advance clock past the 1-minute timeout.
	fc.Advance(2 * time.Minute)

	// Any operation with this txID should now return DeadlineExceeded.
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "test"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected DeadlineExceeded for timed-out transaction, got nil")
	}
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded, got %v", status.Code(err))
	}
}

func TestTransaction_InvalidTxID(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

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
	ctx := testCtx()

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
	ctx := testCtx()

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
	ctx := testCtx()

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
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	var wg sync.WaitGroup
	errCh := make(chan error, 10)
	for range 10 {
		wg.Go(func() {
			_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
				EntityType: "Component",
				Properties: map[string]string{"name": "concurrent"},
			})
			if err != nil && status.Code(err) != codes.AlreadyExists {
				errCh <- fmt.Errorf("concurrent CreateEntity failed: %v", err)
			}
		})
	}
	wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		t.Fatalf("concurrent writes produced %d errors: %v", len(errs), errs[0])
	}
}

// =========================================================================
// 8. Export tests
// =========================================================================

func TestExportGraph_UnsupportedFormat(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	_, err := collectExportData(srv, ctx, "unsupported")
	if err == nil {
		t.Fatal("expected error for unsupported format, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestExportGraph_JSON(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	_, _ = srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "a"}, nil, "")
	_, _ = srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "b"}, nil, "")

	data, err := collectExportData(srv, ctx, "json")
	if err != nil {
		t.Fatalf("export JSON failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty export data")
	}
}

func TestExportGraph_GraphML(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	_, _ = srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "x"}, nil, "")

	data, err := collectExportData(srv, ctx, "graphml")
	if err != nil {
		t.Fatalf("export GraphML failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty export data")
	}
}

// =========================================================================
// 9. Missing error-condition tests
// =========================================================================

func TestSearchNeighbors_Valid(t *testing.T) {
	srv, st, _ := newTestServer(t)
	ctx := testCtx()

	// Apply a schema with a vector-indexed type.
	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VectorType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Bootstrap with first entity (establishes dimension).
	_, _ = srv.store.CreateEntity(ctx, "VectorType", "", map[string]string{"name": "a"}, []float32{1.0, 0.0, 0.0}, "")
	_, _ = srv.store.CreateEntity(ctx, "VectorType", "", map[string]string{"name": "b"}, []float32{0.0, 1.0, 0.0}, "")

	resp, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{1.0, 0.0, 0.0},
		EntityType: "VectorType",
		TopK:       5,
	})
	if err != nil {
		t.Fatalf("SearchNeighbors failed: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected at least one neighbor result")
	}
}

func TestRefreshTransaction_NoConflicts(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}

	_, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: beginResp.TransactionId,
	})
	if err != nil {
		t.Fatalf("RefreshTransaction failed: %v", err)
	}
}

func TestDeleteEdge_Valid(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	svc, _ := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, "")
	comp, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "core"}, nil, "")

	createResp, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:     "DEPENDS_ON",
		FromEntityId: svc.Id,
		ToEntityId:   comp.Id,
		Properties:   map[string]string{"weight": "high"},
	})
	if err != nil {
		t.Fatalf("CreateEdge failed: %v", err)
	}

	deleteResp, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: createResp.EdgeId})
	if err != nil {
		t.Fatalf("DeleteEdge failed: %v", err)
	}
	if deleteResp.EdgeId != createResp.EdgeId {
		t.Fatalf("expected deleted edge ID %q, got %q", createResp.EdgeId, deleteResp.EdgeId)
	}
}

func TestGetTransactionDiff_WrongCapability(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)

	// Capabilities missing READ:graph/tx.
	ctx := capabilityContext("WRITE:graph/entity/*,WRITE:graph/tx", scPriv, "sidecar")

	_, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{
		TransactionId: "00000000-0000-0000-0000-000000000000",
	})
	if err == nil {
		t.Fatal("expected error for wrong capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

func TestWipeGraph_Clean(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	_, err := srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{})
	if err != nil {
		t.Fatalf("WipeGraph on empty graph failed: %v", err)
	}
}

// =========================================================================
// 11. PullFromRemote error-path tests
// =========================================================================

func TestPullFromRemote_RemoteNotConfigured(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	_, err := srv.PullFromRemote(ctx, &flowv1.PullFromRemoteRequest{})
	if err == nil {
		t.Fatal("expected error for no remote configured, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", status.Code(err))
	}
}

func TestPullFromRemote_AuthConfigMissing(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub := initTestKey() // matches testSidecarPriv used by testCtx()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	// remoteURL is set but gitstore has no remote configured and default
	// authFn returns ErrAuthConfigMissing.
	srv := NewCartographerServer(st, gs, opPub, scPub, nil,
		"https://example.com/repo.git", 30*time.Second, "test-ns",
		30*time.Minute, 100000)
	ctx := testCtx()

	_, err := srv.PullFromRemote(ctx, &flowv1.PullFromRemoteRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", status.Code(err))
	}
}

// =========================================================================
// 12. CommitTransaction error-path tests
// =========================================================================

func TestCommitTransaction_Divergence(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	// Begin a transaction.
	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	// Add a change so we're not zero-mutation.
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "test"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	// Corrupt MainHeadAtLastSync to simulate main having advanced.
	state, _ := srv.txManager.Lookup(txID)
	state.MainHeadAtLastSync = "0000000000000000000000000000000000000000"

	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: txID})
	if err == nil {
		t.Fatal("expected error for divergence, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", status.Code(err))
	}
}

func TestCommitTransaction_SchemaIncompatible(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	// Begin a transaction.
	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	// Add a change so we're not zero-mutation.
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "test"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	// Change schema between begin and commit by adding a new entity type.
	// This changes the schema hash (computed from type names, not properties).
	alteredSchema := &flowv1.Schema{
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
			{Name: "NewType", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "weight", Type: "string"}}},
		},
	}
	if err := srv.store.ApplySchema(ctx, alteredSchema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: txID})
	if err == nil {
		t.Fatal("expected error for schema changed incompatibly, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", status.Code(err))
	}
}

// =========================================================================
// 13. ExtendTimeout validation tests
// =========================================================================

func TestExtendTimeout_NonPositiveDuration(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	_, err = srv.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{
		TransactionId: txID,
		Duration:      durationpb.New(0),
	})
	if err == nil {
		t.Fatal("expected error for zero duration, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}

	_, err = srv.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{
		TransactionId: txID,
		Duration:      durationpb.New(-1 * time.Second),
	})
	if err == nil {
		t.Fatal("expected error for negative duration, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestExtendTimeout_ExceedsMaxTotalLifetime(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(1 * time.Minute),
	})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	// Extending to 8 days would exceed the 7-day hard max (created at now,
	// 8 days from now > 7 days).
	_, err = srv.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{
		TransactionId: txID,
		Duration:      durationpb.New(8 * 24 * time.Hour),
	})
	if err == nil {
		t.Fatal("expected error for exceeding max total lifetime, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// =========================================================================
// 14. RollbackTransaction NOT_FOUND test
// =========================================================================

func TestRollbackTransaction_NotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	_, err := srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
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
// 15. BeginTransaction error-path tests
// =========================================================================

func TestBeginTransaction_TimeoutCapping(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	// Use a short default timeout so capping at hard max (7 days) is visible.
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	ctx := testCtx()

	// Request a timeout far exceeding the hard max (7 days).
	resp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(14 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	// The applied timeout must not exceed 7 days.
	if resp.AppliedTimeout.AsDuration() > 7*24*time.Hour {
		t.Fatalf("expected applied timeout capped at 7 days, got %v", resp.AppliedTimeout.AsDuration())
	}
}

func TestBeginTransaction_ResourceExhausted(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub := initTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Replace the store with one that fails on CreateBranchDB.
	srv.store = &failOnCreateBranchDBStore{Store: st}

	ctx := testCtx()
	_, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err == nil {
		t.Fatal("expected error for resource exhausted, got nil")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v", status.Code(err))
	}
}

// =========================================================================
// 16. ExecuteCypher invalid syntax test
// =========================================================================

func TestExecuteCypher_InvalidSyntax(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{
		Cypher: "NOT VALID CYPHER SYNTAX @@@",
	})
	if err == nil {
		t.Fatal("expected error for invalid Cypher syntax, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// =========================================================================
// 17. SearchNeighbors error-path tests
// =========================================================================

func TestSearchNeighbors_UnknownEntityType(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{1.0, 2.0, 3.0},
		EntityType: "NonExistent",
		TopK:       5,
	})
	if err == nil {
		t.Fatal("expected error for unknown entity type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestSearchNeighbors_InvalidTopK(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	_, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{1.0, 2.0, 3.0},
		EntityType: "Component",
		TopK:       -1,
	})
	if err == nil {
		t.Fatal("expected error for negative topK, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestSearchNeighbors_NaNEmbedding(t *testing.T) {
	srv, st, _ := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	// Bootstrap with a valid entity.
	_, _ = srv.store.CreateEntity(ctx, "VecType", "", map[string]string{"name": "a"}, []float32{1.0, 0.0, 0.0}, "")

	_, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{float32(math.NaN()), 0.0, 0.0},
		EntityType: "VecType",
		TopK:       5,
	})
	if err == nil {
		t.Fatal("expected error for NaN embedding, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestSearchNeighbors_EmbeddingDimensionMismatch(t *testing.T) {
	srv, st, _ := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	// Bootstrap with a 3-dim vector.
	_, _ = srv.store.CreateEntity(ctx, "VecType", "", map[string]string{"name": "a"}, []float32{1.0, 0.0, 0.0}, "")

	_, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{1.0, 0.0}, // only 2 dims
		EntityType: "VecType",
		TopK:       5,
	})
	if err == nil {
		t.Fatal("expected error for dimension mismatch, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// =========================================================================
// 18. FullTextSearch unknown entity type test
// =========================================================================

func TestFullTextSearch_UnknownEntityType(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{
		Query:      "anything",
		EntityType: "NonExistent",
	})
	if err == nil {
		t.Fatal("expected error for unknown entity type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// =========================================================================
// 19. ListEntities error-path tests
// =========================================================================

func TestListEntities_InvalidPageSize(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	// Negative page size.
	_, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
		EntityType: "Component",
		PageSize:   -1,
	})
	if err == nil {
		t.Fatal("expected error for negative page size, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}

	// Page size exceeding maximum.
	_, err = srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
		EntityType: "Component",
		PageSize:   1001,
	})
	if err == nil {
		t.Fatal("expected error for page size > 1000, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestListEntities_InvalidPageToken(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
		EntityType: "Component",
		PageSize:   10,
		PageToken:  "invalid-token",
	})
	if err == nil {
		t.Fatal("expected error for invalid page token, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// =========================================================================
// 20. CreateEdge error-path tests
// =========================================================================

func TestCreateEdge_TargetNotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	svc, _ := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, "")

	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:     "DEPENDS_ON",
		FromEntityId: svc.Id,
		ToEntityId:   "00000000-0000-0000-0000-000000000000",
	})
	if err == nil {
		t.Fatal("expected error for target not found, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

func TestCreateEdge_RuleViolation(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	// Service has rules permitting connection to Component via DEPENDS_ON,
	// but Component has no rules defined.
	comp, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "comp"}, nil, "")
	svc, _ := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, "")

	// Attempt edge FROM Component (no rules) TO Service.
	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:     "DEPENDS_ON",
		FromEntityId: comp.Id,
		ToEntityId:   svc.Id,
	})
	if err == nil {
		t.Fatal("expected error for rule violation, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

func TestCreateEdge_MissingRequiredProperty(t *testing.T) {
	srv, st, _ := newTestServer(t)
	ctx := testCtx()

	// Apply schema with a required edge property.
	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:       "Source",
				Properties: []*flowv1.Property{{Name: "name", Type: "string", Required: true}},
			},
			{
				Name:       "Target",
				Properties: []*flowv1.Property{{Name: "name", Type: "string", Required: true}},
				Rules:      []*flowv1.ConnectionRule{{CanConnectTo: []string{"Source"}, Using: []string{"LINKED"}}},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "LINKED", Properties: []*flowv1.Property{{Name: "label", Type: "string", Required: true}}},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	src, _ := srv.store.CreateEntity(ctx, "Source", "", map[string]string{"name": "src"}, nil, "")
	tgt, _ := srv.store.CreateEntity(ctx, "Target", "", map[string]string{"name": "tgt"}, nil, "")

	// Missing required property "label".
	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:     "LINKED",
		FromEntityId: tgt.Id,
		ToEntityId:   src.Id,
		Properties:   map[string]string{},
	})
	if err == nil {
		t.Fatal("expected error for missing required property, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEdge_UnknownProperty(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	svc, _ := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, "")
	comp, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "core"}, nil, "")

	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:     "DEPENDS_ON",
		FromEntityId: svc.Id,
		ToEntityId:   comp.Id,
		Properties:   map[string]string{"unknownprop": "x"},
	})
	if err == nil {
		t.Fatal("expected error for unknown property, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEdge_InvalidIDFormat(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:     "DEPENDS_ON",
		FromEntityId: "not-a-uuid",
		ToEntityId:   "00000000-0000-0000-0000-000000000000",
	})
	if err == nil {
		t.Fatal("expected error for invalid ID format, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// =========================================================================
// 21. UpdateEntity error-path tests
// =========================================================================

func TestUpdateEntity_UnknownProperty(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	ent, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "test"}, nil, "")

	_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:         ent.Id,
		Properties: map[string]string{"nonexistent": "value"},
	})
	if err == nil {
		t.Fatal("expected error for unknown property, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestUpdateEntity_InvalidIDFormat(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:         "not-a-uuid",
		Properties: map[string]string{"name": "x"},
	})
	if err == nil {
		t.Fatal("expected error for invalid ID format, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestUpdateEntity_NaNEmbedding(t *testing.T) {
	srv, st, _ := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	ent, _ := srv.store.CreateEntity(ctx, "VecType", "", map[string]string{"name": "a"}, []float32{1.0, 0.0, 0.0}, "")

	_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:         ent.Id,
		Properties: map[string]string{"name": "b"},
		Embedding:  []float32{float32(math.NaN()), 0.0, 0.0},
	})
	if err == nil {
		t.Fatal("expected error for NaN embedding, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestUpdateEntity_EmbeddingDimensionMismatch(t *testing.T) {
	srv, st, _ := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	ent, _ := srv.store.CreateEntity(ctx, "VecType", "", map[string]string{"name": "a"}, []float32{1.0, 0.0, 0.0}, "")

	_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:         ent.Id,
		Properties: map[string]string{"name": "b"},
		Embedding:  []float32{1.0, 0.0}, // only 2 dims, expected 3
	})
	if err == nil {
		t.Fatal("expected error for dimension mismatch, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// =========================================================================
// 22. CreateEntity error-path tests
// =========================================================================

func TestCreateEntity_UnknownProperty(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component",
		Properties: map[string]string{"name": "test", "nonexistent": "value"},
	})
	if err == nil {
		t.Fatal("expected error for unknown property, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEntity_InvalidIDFormat(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component",
		Id:         "not-a-uuid",
		Properties: map[string]string{"name": "test"},
	})
	if err == nil {
		t.Fatal("expected error for invalid ID format, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEntity_NaNEmbedding(t *testing.T) {
	srv, st, _ := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "VecType",
		Properties: map[string]string{"name": "bad"},
		Embedding:  []float32{float32(math.NaN()), 0.0, 0.0},
	})
	if err == nil {
		t.Fatal("expected error for NaN embedding, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEntity_NaNEmbeddingNonIndexed(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	// Component is a non-indexed type (enableVectorIndex not set).
	applyTestSchema(ctx, t, srv.store)

	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component",
		Properties: map[string]string{"name": "bad"},
		Embedding:  []float32{float32(math.NaN()), 0.0, 0.0},
	})
	if err == nil {
		t.Fatal("expected error for NaN embedding on non-indexed type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEntity_EmbeddingDimensionMismatch(t *testing.T) {
	srv, st, _ := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	// Bootstrap with a 3-dim entity.
	_, _ = srv.store.CreateEntity(ctx, "VecType", "", map[string]string{"name": "a"}, []float32{1.0, 0.0, 0.0}, "")

	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "VecType",
		Properties: map[string]string{"name": "b"},
		Embedding:  []float32{1.0, 0.0}, // only 2 dims
	})
	if err == nil {
		t.Fatal("expected error for dimension mismatch, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEntity_VectorBootstrap(t *testing.T) {
	srv, st, _ := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// First entity must include an embedding to bootstrap the vector dimension.
	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "VecType",
		Properties: map[string]string{"name": "no-embedding"},
		// No Embedding field.
	})
	if err == nil {
		t.Fatal("expected error for missing bootstrap embedding, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", status.Code(err))
	}
}

// =========================================================================
// 23. WipeGraph mid-wipe failure test
// =========================================================================

func TestWipeGraph_MidWipeFailure(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()
	ctx := context.Background()

	// Replace the store with one that fails on WipeAll.
	srv.store = &wipeFailingStore{Store: st}

	// The git operations (withGitLock) will succeed, but WipeAll will fail,
	// triggering the mid-wipe error.
	_, err := srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{})
	if err == nil {
		t.Fatal("expected error for mid-wipe failure, got nil")
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", status.Code(err))
	}
}

// =========================================================================
// 24. ApplySchema error-path tests
// =========================================================================

func TestApplySchema_DestructiveChange(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	// Apply initial schema.
	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{
				{Name: "name", Type: "string"},
				{Name: "toremove", Type: "string"},
			}},
		},
	}
	if _, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: schema}); err != nil {
		t.Fatalf("first ApplySchema failed: %v", err)
	}

	// Try to re-apply schema with a property removed (destructive).
	destructive := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{
				{Name: "name", Type: "string"},
				// "toremove" is missing.
			}},
		},
	}
	_, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: destructive})
	if err == nil {
		t.Fatal("expected error for destructive schema change, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", status.Code(err))
	}
}

func TestApplySchema_BeforeDBReady(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	// Do NOT call MarkDBReady.
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	ctx := context.Background()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
		},
	}
	_, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: schema})
	if err == nil {
		t.Fatal("expected error for ApplySchema before DB ready, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", status.Code(err))
	}
}

func TestApplySchema_AdditiveChange(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	// Apply initial schema.
	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
		},
	}
	if _, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: schema}); err != nil {
		t.Fatalf("first ApplySchema failed: %v", err)
	}

	// Additive change: add a new property and a new entity type.
	additive := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
			}},
			{Name: "Service", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "weight", Type: "string"}}},
		},
	}
	_, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: additive})
	if err != nil {
		t.Fatalf("additive ApplySchema failed: %v", err)
	}
}

// =========================================================================
// 25. ExportGraph error-path tests
// =========================================================================

func TestExportGraph_EmptyGraph(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ctx := testCtx()

	// No data in the graph — export should succeed with an empty result.
	data, err := collectExportData(srv, ctx, "json")
	if err != nil {
		t.Fatalf("export empty graph failed: %v", err)
	}
	if data == nil {
		t.Fatal("expected non-nil export data for empty graph")
	}
}

func TestExportGraph_MidStreamFailure(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Add some data so export has content.
	applySchemaCtx := context.Background()
	applyTestSchema(applySchemaCtx, t, srv.store)
	_, _ = srv.store.CreateEntity(applySchemaCtx, "Component", "", map[string]string{"name": "a"}, nil, "")

	// Must provide a context with proper capabilities so CheckCapability passes.
	capCtx := capabilityContext("READ:graph/entity/*", scPriv, "sidecar")
	stream := &mockExportStream{ctx: capCtx, sendErr: fmt.Errorf("stream send failure")}
	err := srv.ExportGraph(&flowv1.ExportGraphRequest{Format: "json"}, stream)
	if err == nil {
		t.Fatal("expected error for mid-stream failure, got nil")
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", status.Code(err))
	}
}

func TestExportGraph_BufferAllocationFailure(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Wrap store to panic on ListMainEntityTypes, simulating an OOM.
	srv.store = &panicStore{Store: st}

	capCtx := capabilityContext("READ:graph/entity/*", scPriv, "sidecar")
	stream := &mockExportStream{ctx: capCtx}
	err := srv.ExportGraph(&flowv1.ExportGraphRequest{Format: "json"}, stream)
	if err == nil {
		t.Fatal("expected error for buffer allocation failure, got nil")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v", status.Code(err))
	}
}

// =========================================================================
// 26. Capability enforcement tests
// =========================================================================

func TestExecuteCypher_MissingReadCapability(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Only WRITE capabilities, no READ.
	ctx := capabilityContext("WRITE:graph/entity/*,WRITE:graph/tx", scPriv, "sidecar")

	_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: "MATCH (n) RETURN n"})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing READ capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

func TestCreateEntity_MissingWriteCapability(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()
	ctx := capabilityContext("READ:graph/entity/*,READ:graph/tx", scPriv, "sidecar")

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component",
		Properties: map[string]string{"name": "x"},
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing WRITE capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

func TestBeginTransaction_MissingTxCapability(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Only entity capabilities, no WRITE:graph/tx.
	ctx := capabilityContext("READ:graph/entity/*,WRITE:graph/entity/*", scPriv, "sidecar")

	_, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing WRITE:graph/tx capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

func TestExportGraph_MissingReadCapability(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Only WRITE capabilities, no READ:graph/entity/*.
	ctx := capabilityContext("WRITE:graph/entity/*,WRITE:graph/tx", scPriv, "sidecar")

	err := srv.ExportGraph(&flowv1.ExportGraphRequest{Format: "json"}, &mockExportStream{ctx: ctx})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing READ capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

// TestCapability_WildcardFallback verifies that Mode 2 wildcard fallback works:
// a capability "READ:graph/entity/*" should allow reading any entity type even
// without a specific "READ:graph/entity/Component" capability.
func TestCapability_WildcardFallback(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Grant only the wildcard entity read capability.
	mdCtx := capabilityContext("READ:graph/entity/*", scPriv, "sidecar")
	// Run through the verifier to store capabilities in context (simulating interceptor).
	verifiedCtx, err := srv.verifier.verify(mdCtx)
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	applyTestSchema(verifiedCtx, t, srv.store)
	_, _ = srv.store.CreateEntity(verifiedCtx, "Component", "", map[string]string{"name": "x"}, nil, "")

	// ListEntities uses checkEntityCap which falls back to wildcard.
	resp, err := srv.ListEntities(verifiedCtx, &flowv1.ListEntitiesRequest{
		EntityType: "Component",
		PageSize:   10,
	})
	if err != nil {
		t.Fatalf("ListEntities with wildcard fallback failed: %v", err)
	}
	if len(resp.Entities) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(resp.Entities))
	}
}

// =========================================================================
// 27. Staleness window boundary tests
// =========================================================================

func TestCapability_StalenessBoundary_InsideAndPast(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	// 30-second staleness window, like production.
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)

	// Capability signed 29 seconds ago — inside the 30-second window
	// (use 29s instead of 30s to avoid timing flakiness).
	insideWindow := time.Now().Add(-29 * time.Second).Unix()
	payload := fmt.Sprintf("READ:graph/entity/Component|%d", insideWindow)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(scPriv, []byte(payload)))
	md := metadata.Pairs(
		MetadataKeyCapabilities, "READ:graph/entity/Component",
		MetadataKeyCapabilitiesSignature, sig,
		MetadataKeyCapabilitiesSignedAt, fmt.Sprintf("%d", insideWindow),
		MetadataKeyCapabilitiesSignedBy, "sidecar",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := srv.verifier.verify(ctx)
	if err != nil {
		t.Fatalf("expected success at staleness boundary (29s), got: %v", err)
	}

	// Capability signed 31 seconds ago — just past the 30-second boundary.
	past31s := time.Now().Add(-31 * time.Second).Unix()
	payload2 := fmt.Sprintf("READ:graph/entity/Component|%d", past31s)
	sig2 := base64.StdEncoding.EncodeToString(ed25519.Sign(scPriv, []byte(payload2)))
	md2 := metadata.Pairs(
		MetadataKeyCapabilities, "READ:graph/entity/Component",
		MetadataKeyCapabilitiesSignature, sig2,
		MetadataKeyCapabilitiesSignedAt, fmt.Sprintf("%d", past31s),
		MetadataKeyCapabilitiesSignedBy, "sidecar",
	)
	ctx2 := metadata.NewIncomingContext(context.Background(), md2)
	_, err2 := srv.verifier.verify(ctx2)
	if err2 == nil {
		t.Fatal("expected error for stale capability (31s past), got nil")
	}
	if status.Code(err2) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied for 31s past, got %v", status.Code(err2))
	}
}

func TestCapability_StalenessBoundary_NegativeWindow(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := store.OpenInMemory()
	gs, _ := gitstore.New(t.TempDir())
	// Negative staleness window disables the check.
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		-1*time.Second, "test-ns", 30*time.Minute, 100000)

	// Very old timestamp — would be stale with a positive window.
	oldTime := time.Now().Add(-24 * time.Hour).Unix()
	payload := fmt.Sprintf("READ:graph/entity/Component|%d", oldTime)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(scPriv, []byte(payload)))
	md := metadata.Pairs(
		MetadataKeyCapabilities, "READ:graph/entity/Component",
		MetadataKeyCapabilitiesSignature, sig,
		MetadataKeyCapabilitiesSignedAt, fmt.Sprintf("%d", oldTime),
		MetadataKeyCapabilitiesSignedBy, "sidecar",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := srv.verifier.verify(ctx)
	if err != nil {
		t.Fatalf("expected success with negative staleness window, got: %v", err)
	}
}

// =========================================================================
// 28. Git lock serialization test
// =========================================================================

func TestGitLockSerialization(t *testing.T) {
	srv, _, _ := newTestServer(t)

	var wg sync.WaitGroup
	concurrent := 0
	var mu sync.Mutex

	for range 5 {
		wg.Go(func() {
			err := srv.withGitLock(func() error {
				mu.Lock()
				concurrent++
				if concurrent > 1 {
					mu.Unlock()
					return fmt.Errorf("detected concurrent git lock holders")
				}
				mu.Unlock()

				// Simulate some work.
				time.Sleep(10 * time.Millisecond)

				mu.Lock()
				concurrent--
				mu.Unlock()
				return nil
			})
			if err != nil {
				t.Errorf("withGitLock failed: %v", err)
			}
		})
	}
	wg.Wait()
}

// =========================================================================
// 29. Fixed concurrent non-transactional writes test
// =========================================================================
// 10. Telemetry tests
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
