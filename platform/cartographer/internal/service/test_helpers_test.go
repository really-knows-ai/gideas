package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowmeta "github.com/foundry/flow/pkg/metadata"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func generateTestKey() (ed25519.PublicKey, ed25519.PrivateKey) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(fmt.Sprintf("failed to generate test key: %v", err))
	}
	return pub, priv
}

// waitFor polls cond until it returns true or the 5-second budget elapses,
// failing the test with msg otherwise.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", msg)
}

func invokeSync(
	srv *CartographerServer, ctx context.Context,
) (handlerInvoked bool, verifiedCaps *Capabilities, err error) {
	_, err = srv.verifier.VerifyInterceptor(
		ctx,
		&flowv1.SyncRequest{},
		&grpc.UnaryServerInfo{Server: srv, FullMethod: flowv1.CartographerService_Sync_FullMethodName},
		func(ctx context.Context, req any) (any, error) {
			handlerInvoked = true
			verifiedCaps, _ = ExtractCapabilities(ctx)
			return srv.Sync(ctx, req.(*flowv1.SyncRequest))
		},
	)
	return handlerInvoked, verifiedCaps, err
}

func invokeExportGraph(
	srv *CartographerServer, req *flowv1.ExportGraphRequest, stream *mockExportStream,
) (handlerInvoked bool, err error) {
	err = srv.verifier.VerifyStreamInterceptor(
		srv,
		stream,
		&grpc.StreamServerInfo{
			FullMethod:     flowv1.CartographerService_ExportGraph_FullMethodName,
			IsServerStream: true,
		},
		func(_ any, intercepted grpc.ServerStream) error {
			handlerInvoked = true
			stream.verifiedCaps, _ = ExtractCapabilities(intercepted.Context())
			return srv.ExportGraph(req, &grpc.GenericServerStream[
				flowv1.ExportGraphRequest, flowv1.ExportGraphResponse,
			]{ServerStream: intercepted})
		},
	)
	return handlerInvoked, err
}

// testSidecarPriv is a test-level Ed25519 private key used for signing
// capability metadata in tests. It is lazily initialized by the first
// call to testCtx.
var testSidecarPriv ed25519.PrivateKey

const testMutationEntityID = "11111111-1111-4111-8111-111111111111"

// capabilityStaleMsg is the message carried by errStaleCapability (SPEC error
// table "Stale capability signature (anti-replay)"), asserted by the
// stale/missing/malformed signed-at tests.
const capabilityStaleMsg = "stale capability (anti-replay)"

// initTestKey generates the shared test key pair once and returns the public key.
func initTestKey() ed25519.PublicKey {
	if testSidecarPriv != nil {
		return testSidecarPriv.Public().(ed25519.PublicKey)
	}
	pub, priv := generateTestKey()
	testSidecarPriv = priv
	return pub
}

// openTestStore is the service-test constructor for a store. It returns an
// in-memory store (ladybug.OpenInMemory): full store.Store semantics without the
// per-test file-backed DDL and disk I/O. Callers only ever use the returned
// handle within the test's process lifetime — they never receive the backing
// path, so they cannot reopen/persist it — meaning the in-memory engine changes
// no observable behaviour while removing the file-backed open cost for every
// test. Tests that must survive a re-open (crash-window recovery, file
// re-hydration) open their own file-backed store via ladybug.Open on an explicit
// path and stay on that path (see the reopen/durability tests).
func openTestStore(t *testing.T) (store.Store, error) {
	t.Helper()
	return ladybug.OpenInMemory()
}

// newTestServer creates a CartographerServer with an in-memory store and a temp
// gitstore. In-memory (ladybug.OpenInMemory via openTestStore) is the unit-test
// default: it gives full store.Store semantics (schema, CRUD, branches,
// rehydration) without the per-test file-backed DDL and disk I/O that made the
// service suite integration-flavoured and slow. Durability is not asserted
// here; tests that must survive a re-open (crash-window recovery, file
// re-hydration) build a file-backed store and gitstore directly (via
// ladybug.Open) and stay on that path.
func newTestServer(t *testing.T) (*CartographerServer, store.Store) {
	t.Helper()
	scPub := initTestKey()
	st, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
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
	return srv, st
}

// testCtx returns a context with full wildcard capabilities (READ:graph/entity/*,
// WRITE:graph/entity/*, READ:graph/tx, WRITE:graph/tx) verified and stored in
// context (simulating the interceptor path).
func testCtx() context.Context {
	initTestKey()
	caps := "READ:graph/entity/*,WRITE:graph/entity/*,READ:graph/tx,WRITE:graph/tx"
	sig, signedAt := signCapabilities(caps, testSidecarPriv)
	md := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, caps,
		flowmeta.MetadataKeyCapabilitiesSignature, sig,
		flowmeta.MetadataKeyCapabilitiesSignedAt, fmt.Sprintf("%d", signedAt),
		flowmeta.MetadataKeyCapabilitiesSignedBy, "sidecar",
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

// beginTestTx begins a transaction on srv using the full-capability testCtx and
// returns its transaction ID. Write-path tests must run inside a transaction
// (transaction-only write model), so this helper is the standard preamble.
func beginTestTx(t *testing.T, srv *CartographerServer, ctx context.Context) string {
	t.Helper()
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	return begin.TransactionId
}

func commitGitEntity(ctx context.Context, t *testing.T, gs gitstore.GitStore, id, name string) {
	t.Helper()
	if err := gs.WithGitLock(func() error {
		if err := gs.RestoreMain(ctx); err != nil {
			return err
		}
		if err := gs.WriteEntityFiles(ctx, "Component", []gitstore.Entity{{
			ID: id, Type: "Component", Properties: map[string]string{"name": name},
		}}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx, "entities"); err != nil {
			return err
		}
		return gs.Commit(ctx, "advance main")
	}); err != nil {
		t.Fatalf("advance main: %v", err)
	}
}

func cloneTestSchemaProvider(source testSchemaProvider) testSchemaProvider {
	clone := testSchemaProvider{
		entityNames: append([]string(nil), source.entityNames...),
		edgeNames:   append([]string(nil), source.edgeNames...),
		entities:    make(map[string]*store.EntityTypeDef, len(source.entities)),
		edges:       make(map[string]*store.EdgeTypeDef, len(source.edges)),
	}
	for name, def := range source.entities {
		copyDef := *def
		copyDef.Properties = append([]store.PropertyDef(nil), def.Properties...)
		copyDef.Rules = make([]store.ConnectionRuleDef, len(def.Rules))
		for i, rule := range def.Rules {
			copyDef.Rules[i] = store.ConnectionRuleDef{
				CanConnectTo: append([]string(nil), rule.CanConnectTo...), Using: append([]string(nil), rule.Using...),
			}
		}
		clone.entities[name] = &copyDef
	}
	for name, def := range source.edges {
		copyDef := *def
		copyDef.Properties = append([]store.PropertyDef(nil), def.Properties...)
		clone.edges[name] = &copyDef
	}
	return clone
}

// newSyncWorker builds a SyncWorker over the shared mock gitstore with an
// in-memory ladybug store (RehydrateMainFromFiles is exercised on pulled
// fetches).
func newSyncWorker(t *testing.T, syncGit *syncMockGitStore, clock Clock) *SyncWorker {
	t.Helper()
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	return NewSyncWorker("https://example.com/repo.git", syncGit, base, clock)
}

// newSyncServer builds a CartographerServer with a remote configured and a
// running SyncWorker over the shared mock gitstore. Callers must wait for the
// startup cycle to finish (fc.tickers() >= 1) before triggering wakes so the
// wake is consumed by a fresh cycle.
func newSyncServer(t *testing.T, syncGit *syncMockGitStore) (*CartographerServer, *fakeClock) {
	t.Helper()
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	fc := newFakeClock(time.Now())
	sw := NewSyncWorker("https://example.com/repo.git", syncGit, base, fc)
	go sw.Run()
	t.Cleanup(sw.Stop)
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(base, syncGit, opPub, initTestKey(), nil, "https://example.com/repo.git",
		30*time.Second, "test-ns", 30*time.Minute, 100000, WithSyncWorker(sw))
	srv.MarkDBReady()
	return srv, fc
}

// ctxWithTimeout returns a context that times out after 10 seconds; a waiter
// blocked by a bug then surfaces a test failure instead of hanging the suite.
func ctxWithTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// narrowCtx returns a context with specific (non-wildcard) capabilities.
func narrowCtx(caps ...string) context.Context {
	initTestKey()
	capsStr := ""
	for i, c := range caps {
		if i > 0 {
			capsStr += ","
		}
		capsStr += c
	}
	sig, signedAt := signCapabilities(capsStr, testSidecarPriv)
	md := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, capsStr,
		flowmeta.MetadataKeyCapabilitiesSignature, sig,
		flowmeta.MetadataKeyCapabilitiesSignedAt, fmt.Sprintf("%d", signedAt),
		flowmeta.MetadataKeyCapabilitiesSignedBy, "sidecar",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	c := &Capabilities{
		Caps:     caps,
		SignedBy: "sidecar",
	}
	return StoreCapabilitiesInContext(ctx, c)
}

// noReadCtx returns a context with WRITE capabilities but no READ capabilities.
func noReadCtx() context.Context {
	return narrowCtx("WRITE:graph/entity/*", "WRITE:graph/tx")
}
