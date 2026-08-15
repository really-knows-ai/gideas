package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowmeta "github.com/foundry/flow/pkg/metadata"
	"github.com/go-git/go-git/v5/plumbing"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
)

// testSidecarPriv is a test-level Ed25519 private key used for signing
// capability metadata in tests. It is lazily initialized by the first
// call to testCtx.
var testSidecarPriv ed25519.PrivateKey

const testMutationEntityID = "11111111-1111-4111-8111-111111111111"

// capabilityStaleMsg is the message carried by errStaleCapability (SPEC error
// table "Stale capability signature (anti-replay)"), asserted by the
// stale/missing/malformed signed-at tests.
const capabilityStaleMsg = "stale capability (anti-replay)"

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
	return capabilityContextAt(caps, priv, signedBy, time.Now().Unix())
}

func capabilityContextAt(caps string, priv ed25519.PrivateKey, signedBy string, signedAt int64) context.Context {
	payload := fmt.Sprintf("%s|%d", caps, signedAt)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(payload)))
	md := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, caps,
		flowmeta.MetadataKeyCapabilitiesSignature, sig,
		flowmeta.MetadataKeyCapabilitiesSignedAt, fmt.Sprintf("%d", signedAt),
		flowmeta.MetadataKeyCapabilitiesSignedBy, signedBy,
	)
	return metadata.NewIncomingContext(context.Background(), md)
}

// fakeClock implements Clock for testing.
type fakeClock struct {
	mu          sync.Mutex
	now         time.Time
	lastTicker  *fakeTicker
	tickerCount int
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t} }
func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}
func (f *fakeClock) NewTicker(d time.Duration) Ticker {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTicker{ch: make(chan time.Time, 1)}
	f.lastTicker = t
	f.tickerCount++
	return t
}
func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// tickers returns how many tickers the clock has handed out. The SyncWorker
// creates its ticker immediately after the startup cycle, so a non-zero count
// is the barrier for "the worker finished its startup cycle and is parked in
// the select loop".
func (f *fakeClock) tickers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tickerCount
}

// FireTicker emits one tick on the most recently created ticker. The ticker
// channel is buffered (size 1) so the send never blocks; a tick delivered
// before the worker parks in select is consumed when it does.
func (f *fakeClock) FireTicker() {
	f.mu.Lock()
	t := f.lastTicker
	f.mu.Unlock()
	if t == nil {
		return
	}
	select {
	case t.ch <- time.Now():
	default:
	}
}

type fakeTicker struct{ ch chan time.Time }

func (f *fakeTicker) C() <-chan time.Time { return f.ch }
func (f *fakeTicker) Stop()               {}

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

type testSchemaProvider struct {
	entityNames []string
	edgeNames   []string
	entities    map[string]*store.EntityTypeDef
	edges       map[string]*store.EdgeTypeDef
}

func (s testSchemaProvider) EntityTypeNames() []string {
	return append([]string(nil), s.entityNames...)
}
func (s testSchemaProvider) EdgeTypeNames() []string { return append([]string(nil), s.edgeNames...) }
func (s testSchemaProvider) EntityType(name string) (*store.EntityTypeDef, bool) {
	def, ok := s.entities[name]
	return def, ok
}
func (s testSchemaProvider) EdgeType(name string) (*store.EdgeTypeDef, bool) {
	def, ok := s.edges[name]
	return def, ok
}

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

// wipeFailingStore fails on every call to WipeSchema, used to test mid-wipe
// error handling in WipeGraph.
type wipeFailingStore struct {
	store.Store
}

func (w *wipeFailingStore) WipeSchema(ctx context.Context) error {
	return fmt.Errorf("simulated WipeSchema failure")
}

// wipeFailingGitStore fails a configurable git operation to exercise the
// git-side mid-wipe error paths of WipeGraph (git rm, wipe commit, clean).
type wipeFailingGitStore struct {
	gitstore.GitStore
	failGitRm  bool
	failCommit bool
	failClean  bool
}

func (g *wipeFailingGitStore) GitRm(ctx context.Context, path string) error {
	if g.failGitRm {
		return fmt.Errorf("simulated GitRm failure")
	}
	return g.GitStore.GitRm(ctx, path)
}

func (g *wipeFailingGitStore) Commit(ctx context.Context, message string) error {
	if g.failCommit {
		return fmt.Errorf("simulated wipe commit failure")
	}
	return g.GitStore.Commit(ctx, message)
}

func (g *wipeFailingGitStore) CleanUntracked(ctx context.Context) error {
	if g.failClean {
		return fmt.Errorf("simulated clean untracked failure")
	}
	return g.GitStore.CleanUntracked(ctx)
}

// failOnCreateBranchDBStore fails on CreateBranchDB, used to test
// RESOURCE_EXHAUSTED in BeginTransaction.
type failOnCreateBranchDBStore struct {
	store.Store
}

func (f *failOnCreateBranchDBStore) CreateBranchDB(context.Context, string) error {
	return fmt.Errorf("simulated CreateBranchDB failure")
}

type beginSetupBlockingStore struct {
	store.Store
	entered chan struct{}
	release chan struct{}
}

func (s *beginSetupBlockingStore) CreateBranchDB(ctx context.Context, txID string) error {
	close(s.entered)
	<-s.release
	return s.Store.CreateBranchDB(ctx, txID)
}

type wipeBlockingStore struct {
	store.Store
	mu            sync.Mutex
	entered       chan struct{}
	release       chan struct{}
	wipeCompleted bool
	branchSetup   chan bool
}

func (s *wipeBlockingStore) WipeSchema(ctx context.Context) error {
	close(s.entered)
	<-s.release
	err := s.Store.WipeSchema(ctx)
	s.mu.Lock()
	s.wipeCompleted = true
	s.mu.Unlock()
	return err
}

func (s *wipeBlockingStore) CreateBranchDB(ctx context.Context, txID string) error {
	if s.branchSetup != nil {
		s.mu.Lock()
		wipeCompleted := s.wipeCompleted
		s.mu.Unlock()
		s.branchSetup <- wipeCompleted
	}
	return s.Store.CreateBranchDB(ctx, txID)
}

// panicStore panics on ListMainEntityTypes to simulate a buffer allocation
// panic inside collectExportData.
type panicStore struct {
	store.Store
}

func (p *panicStore) ListMainEntityTypes() ([]string, error) {
	panic("simulated OOM in export data collection")
}

type mutationBlockingStore struct {
	store.Store
	wrote   chan struct{}
	release chan struct{}
}

type deleteEntityFailingStore struct {
	store.Store
}

func (s *deleteEntityFailingStore) DeleteEntity(context.Context, string, string) (*store.Entity, error) {
	return nil, errors.New("simulated DeleteEntity failure")
}

func (s *mutationBlockingStore) CreateEntity(
	ctx context.Context, entityType, id string, properties map[string]string, embedding []float32, branch string,
) (*store.Entity, error) {
	entity, err := s.Store.CreateEntity(ctx, entityType, id, properties, embedding, branch)
	if branch != "" && err == nil {
		close(s.wrote)
		<-s.release
	}
	return entity, err
}

type hydrationBlockingStore struct {
	store.Store
	calls   int
	blocked chan struct{}
	release chan struct{}
	fail    bool
}

func (s *hydrationBlockingStore) HydrateBranchFromFiles(
	ctx context.Context, txID, entitiesDir, edgesDir string,
) error {
	s.calls++
	err := s.Store.HydrateBranchFromFiles(ctx, txID, entitiesDir, edgesDir)
	if s.calls == 2 {
		close(s.blocked)
		<-s.release
		if s.fail {
			return fmt.Errorf("simulated hydration failure")
		}
	}
	return err
}

type hydrationCountingStore struct {
	store.Store
	fromFiles  int
	fromBranch int
}

func (s *hydrationCountingStore) RehydrateMainFromFiles(context.Context, string, string) error {
	s.fromFiles++
	return nil
}

func (s *hydrationCountingStore) RehydrateFromBranch(context.Context, string) error {
	s.fromBranch++
	return nil
}

type dropFailingStore struct {
	store.Store
	failDrop bool
}

func (s *dropFailingStore) DropBranchDB(ctx context.Context, txID string) error {
	if s.failDrop {
		s.failDrop = false
		return fmt.Errorf("simulated DropBranchDB failure")
	}
	return s.Store.DropBranchDB(ctx, txID)
}

type markerFailingStore struct {
	store.Store
	failMark bool
	failDrop bool
}

type transactionStateFailingStore struct {
	store.Store
	fail   func(store.BranchTransactionState) bool
	failed bool
}

func (s *transactionStateFailingStore) SaveBranchTransactionState(
	ctx context.Context, txID string, state store.BranchTransactionState,
) error {
	if !s.failed && s.fail(state) {
		s.failed = true
		return errors.New("simulated transaction state write failure")
	}
	return s.Store.SaveBranchTransactionState(ctx, txID, state)
}

func (s *markerFailingStore) SaveBranchTransactionState(
	ctx context.Context, txID string, state store.BranchTransactionState,
) error {
	if s.failMark && state.RollbackOnly {
		s.failMark = false
		return errors.New("simulated rollback-only marker failure")
	}
	return s.Store.SaveBranchTransactionState(ctx, txID, state)
}

func (s *markerFailingStore) DropBranchDB(ctx context.Context, txID string) error {
	if s.failDrop {
		s.failDrop = false
		return errors.New("simulated marker cleanup drop failure")
	}
	return s.Store.DropBranchDB(ctx, txID)
}

type mergeFailingGitStore struct {
	gitstore.GitStore
	failMerge bool
}

type commitCountingGitStore struct {
	gitstore.GitStore
	commits int
}

type commitErrorGitStore struct {
	gitstore.GitStore
	failBefore bool
	failAfter  bool
	commits    int
}

func (s *commitErrorGitStore) Commit(ctx context.Context, message string) error {
	s.commits++
	if s.failBefore {
		s.failBefore = false
		return errors.New("simulated commit failure")
	}
	if err := s.GitStore.Commit(ctx, message); err != nil {
		return err
	}
	if s.failAfter {
		s.failAfter = false
		return errors.New("simulated error after commit")
	}
	return nil
}

func (s *commitCountingGitStore) Commit(ctx context.Context, message string) error {
	s.commits++
	return s.GitStore.Commit(ctx, message)
}

type cleanupAfterMergeFailingGitStore struct {
	gitstore.GitStore
	failRestore bool
	commits     int
	merges      int
}

type recoveryFailingGitStore struct {
	gitstore.GitStore
	fail      string
	lockCalls int
}

func (s *recoveryFailingGitStore) failOnce(operation string) error {
	if s.fail == operation {
		s.fail = ""
		return fmt.Errorf("simulated %s failure", operation)
	}
	return nil
}

func (s *recoveryFailingGitStore) WithGitLock(fn func() error) error {
	s.lockCalls++
	if s.fail == "lookup lock" && s.lockCalls == 2 {
		s.fail = ""
		return errors.New("simulated lookup lock failure")
	}
	if err := s.failOnce("lock"); err != nil {
		return err
	}
	return s.GitStore.WithGitLock(fn)
}

func (s *recoveryFailingGitStore) RestoreMain(ctx context.Context) error {
	if err := s.failOnce("restore"); err != nil {
		return err
	}
	return s.GitStore.RestoreMain(ctx)
}

func (s *recoveryFailingGitStore) CleanUntracked(ctx context.Context) error {
	if err := s.failOnce("clean"); err != nil {
		return err
	}
	return s.GitStore.CleanUntracked(ctx)
}

func (s *recoveryFailingGitStore) DeleteBranch(ctx context.Context, txID string) error {
	if err := s.failOnce("delete"); err != nil {
		return err
	}
	return s.GitStore.DeleteBranch(ctx, txID)
}

func (s *recoveryFailingGitStore) ListEntityTypes(ctx context.Context) ([]string, error) {
	if err := s.failOnce("list entities"); err != nil {
		return nil, err
	}
	return s.GitStore.ListEntityTypes(ctx)
}

func (s *recoveryFailingGitStore) ReadAllEntityFiles(
	ctx context.Context, entityType string,
) ([]gitstore.EntityFile, error) {
	if err := s.failOnce("read entities"); err != nil {
		return nil, err
	}
	return s.GitStore.ReadAllEntityFiles(ctx, entityType)
}

func (s *recoveryFailingGitStore) ListEdgeTypes(ctx context.Context) ([]string, error) {
	if err := s.failOnce("list edges"); err != nil {
		return nil, err
	}
	return s.GitStore.ListEdgeTypes(ctx)
}

func (s *recoveryFailingGitStore) ReadAllEdgeFiles(
	ctx context.Context, edgeType string,
) ([]gitstore.EdgeFile, error) {
	if err := s.failOnce("read edges"); err != nil {
		return nil, err
	}
	return s.GitStore.ReadAllEdgeFiles(ctx, edgeType)
}

func (s *cleanupAfterMergeFailingGitStore) Commit(ctx context.Context, message string) error {
	s.commits++
	return s.GitStore.Commit(ctx, message)
}

func (s *cleanupAfterMergeFailingGitStore) FastForwardMerge(ctx context.Context, branch, into string) error {
	s.merges++
	return s.GitStore.FastForwardMerge(ctx, branch, into)
}

func (s *cleanupAfterMergeFailingGitStore) RestoreMain(ctx context.Context) error {
	if s.failRestore {
		s.failRestore = false
		return fmt.Errorf("simulated post-merge restore failure")
	}
	return s.GitStore.RestoreMain(ctx)
}

type rehydrateFailingStore struct {
	store.Store
	fail bool
}

func (s *rehydrateFailingStore) RehydrateMainFromFiles(
	ctx context.Context, entitiesDir, edgesDir string,
) error {
	if s.fail {
		s.fail = false
		return fmt.Errorf("simulated rehydration failure")
	}
	return s.Store.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
}

func (s *mergeFailingGitStore) FastForwardMerge(ctx context.Context, branch, into string) error {
	if s.failMerge {
		s.failMerge = false
		return fmt.Errorf("simulated merge failure")
	}
	return s.GitStore.FastForwardMerge(ctx, branch, into)
}

// mergeDivergedGitStore surfaces ErrMergeDiverged from FastForwardMerge on the
// first call, simulating the post-re-hydration commit-merge divergence path.
type mergeDivergedGitStore struct {
	gitstore.GitStore
	diverged bool
}

func (s *mergeDivergedGitStore) FastForwardMerge(ctx context.Context, branch, into string) error {
	if s.diverged {
		s.diverged = false
		return gitstore.ErrMergeDiverged
	}
	return s.GitStore.FastForwardMerge(ctx, branch, into)
}

type gitAttemptStore struct {
	gitstore.GitStore
	mu        sync.Mutex
	attempted chan struct{}
}

func (s *gitAttemptStore) setAttempted(attempted chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempted = attempted
}

func (s *gitAttemptStore) WithGitLock(fn func() error) error {
	s.mu.Lock()
	attempted := s.attempted
	s.attempted = nil
	s.mu.Unlock()
	if attempted != nil {
		close(attempted)
	}
	return s.GitStore.WithGitLock(fn)
}

// mockExportStream implements grpc.ServerStreamingServer[flowv1.ExportGraphResponse]
// for testing ExportGraph through the stream interceptor.
type mockExportStream struct {
	ctx          context.Context
	sendErr      error
	sendCount    int
	data         []byte
	verifiedCaps *Capabilities
}

func (m *mockExportStream) Send(resp *flowv1.ExportGraphResponse) error {
	return m.SendMsg(resp)
}
func (m *mockExportStream) SendMsg(msg any) error {
	m.sendCount++
	if resp, ok := msg.(*flowv1.ExportGraphResponse); ok {
		m.data = append(m.data, resp.Chunk...)
	}
	return m.sendErr
}
func (m *mockExportStream) Context() context.Context        { return m.ctx }
func (m *mockExportStream) SetTrailer(md metadata.MD)       {}
func (m *mockExportStream) SendHeader(md metadata.MD) error { return nil }
func (m *mockExportStream) SetHeader(md metadata.MD) error  { return nil }
func (m *mockExportStream) RecvMsg(any) error               { return nil }

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

// initTestKey generates the shared test key pair once and returns the public key.
func initTestKey() ed25519.PublicKey {
	if testSidecarPriv != nil {
		return testSidecarPriv.Public().(ed25519.PublicKey)
	}
	pub, priv := generateTestKey()
	testSidecarPriv = priv
	return pub
}

// openTestStore is the service-test constructor for a real store. The exported
// ladybug.OpenInMemory seam was removed as test-only dead production surface
// (LEARNINGS: test-only callers do not justify a production surface), so
// service tests open a temp-dir file-backed store instead.
func openTestStore(t *testing.T) (store.Store, error) {
	return ladybug.Open(t.TempDir())
}

// newTestServer creates a CartographerServer with a temp-dir store and temp gitstore.
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

func TestComputeSchemaHashCompleteAndDeterministic(t *testing.T) {
	base := testSchemaProvider{
		entityNames: []string{"Service", "Component"},
		edgeNames:   []string{"DEPENDS_ON"},
		entities: map[string]*store.EntityTypeDef{
			"Service": {
				Name: "Service", EnableVectorIndex: true,
				Properties: []store.PropertyDef{
					{Name: "version", Type: "string"}, {Name: "name", Type: "string", Required: true},
				},
				Rules: []store.ConnectionRuleDef{{
					CanConnectTo: []string{"Service", "Component"}, Using: []string{"CALLS", "DEPENDS_ON"},
				}},
			},
			"Component": {Name: "Component"},
		},
		edges: map[string]*store.EdgeTypeDef{
			"DEPENDS_ON": {Name: "DEPENDS_ON", Properties: []store.PropertyDef{{Name: "weight", Type: "string"}}},
		},
	}
	reordered := testSchemaProvider{
		entityNames: []string{"Component", "Service"}, edgeNames: []string{"DEPENDS_ON"},
		entities: map[string]*store.EntityTypeDef{
			"Component": {Name: "Component"},
			"Service": {
				Name: "Service", EnableVectorIndex: true,
				Properties: []store.PropertyDef{
					{Name: "name", Type: "string", Required: true}, {Name: "version", Type: "string"},
				},
				Rules: []store.ConnectionRuleDef{{
					CanConnectTo: []string{"Component", "Service"}, Using: []string{"DEPENDS_ON", "CALLS"},
				}},
			},
		},
		edges: base.edges,
	}
	baseline := computeSchemaHash(base)
	if got := computeSchemaHash(reordered); got != baseline {
		t.Fatalf("hash changed with ordering: %q != %q", got, baseline)
	}

	mutations := []struct {
		name   string
		mutate func(testSchemaProvider)
	}{
		{"required", func(s testSchemaProvider) { s.entities["Service"].Properties[0].Required = true }},
		{"vector", func(s testSchemaProvider) { s.entities["Service"].EnableVectorIndex = false }},
		{"rule", func(s testSchemaProvider) { s.entities["Service"].Rules[0].Using = []string{"CALLS"} }},
		{"property", func(s testSchemaProvider) { s.edges["DEPENDS_ON"].Properties[0].Type = "integer" }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			copyProvider := cloneTestSchemaProvider(base)
			mutation.mutate(copyProvider)
			if got := computeSchemaHash(copyProvider); got == baseline {
				t.Fatalf("hash did not change for %s mutation", mutation.name)
			}
		})
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

// =========================================================================
// 1. Capability verification tests
// =========================================================================

func TestCapability_ValidSignature(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
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

// TestCapability_WhitespaceEntriesTrimmed pins the capability-string
// normalization that must match the sibling capability gates (SPEC R3 /
// Capability Authorisation Chain): each comma-separated entry in
// x-flow-capabilities is trimmed and empty entries dropped. The Sidecar proxy
// (nodeCapabilities) and Operator (checkCapability) trim every entry before
// matching, so the Cartographer's authoritative exact-match gates must do the
// same — otherwise a capability entry with leading/trailing whitespace is
// granted by the Sidecar and Operator gates but denied here, a divergent
// authorization between sibling implementations of the same contract.
func TestCapability_WhitespaceEntriesTrimmed(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)

	// Entries padded with whitespace and an all-whitespace trailing entry, as
	// a node operator might write in FoundryNode.spec.capabilities.
	ctx := capabilityContext(" READ:graph/entity/Component , WRITE:graph/entity/* , ", scPriv, "sidecar")
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
		t.Fatalf("whitespace-padded capability must be trimmed before the authoritative exact match, got: %v", err)
	}
	if err := srv.verifier.CheckWildcard(caps, "WRITE"); err != nil {
		t.Fatalf("whitespace-padded wildcard capability must be trimmed before the authoritative exact match, got: %v", err)
	}
}

// TestCapability_ValidOperatorSignature is the positive pin for the
// operator-signer branch of the capability chain: verify() selects
// v.operatorKey when signedBy == "operator" (capability.go:135-137). Every
// other service-layer capability test signs with the sidecar private key
// (TestCapability_ValidSignature, testCtx, narrowCtx), and the only
// operator-signed test (TestCapability_InvalidSignature) deliberately uses a
// wrong key to pin denial — so a regression that broke operator-key selection
// would fail no test. This test signs a capability payload with the OPERATOR
// private key, routes it through the Cartographer's verifier, and asserts it
// is ACCEPTED and stored: if the operator branch stopped selecting
// v.operatorKey (or selected nothing), the Ed25519 verify would fail and this
// test would fail.
func TestCapability_ValidOperatorSignature(t *testing.T) {
	opPub, opPriv := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)

	ctx := capabilityContext("READ:graph/entity/Component", opPriv, "operator")
	ctx, err := srv.verifier.verify(ctx)
	if err != nil {
		t.Fatalf("verify failed for operator-signed capabilities: %v", err)
	}
	caps, err := ExtractCapabilities(ctx)
	if err != nil {
		t.Fatalf("ExtractCapabilities failed: %v", err)
	}
	if caps == nil {
		t.Fatal("expected non-nil capabilities")
	}
	if caps.SignedBy != "operator" {
		t.Fatalf("expected SignedBy operator, got %q", caps.SignedBy)
	}
	if err := srv.verifier.CheckSpecificType(caps, "READ", "Component"); err != nil {
		t.Fatalf("CheckSpecificType failed: %v", err)
	}
}

func TestCapability_InvalidSignature(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	_, wrongPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
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
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
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

// TestCapability_PresentButEmptyCapsFailsClosed pins the boundary between
// "capability metadata absent" (system-to-system Operator pass-through,
// TestCapability_MissingMetadata) and "capability metadata present but
// empty/whitespace-only" (capability.go): a request that carries the
// x-flow-capabilities key with an empty or whitespace-only value claims a
// capability attestation but carries no capability entries, so it must fail
// closed with PERMISSION_DENIED at the ingress interceptor instead of being
// reclassified as a trusted system-to-system Operator call that skips
// signature and staleness verification entirely (interceptor contract:
// "If present but unverifiable ..., the interceptor returns PERMISSION_DENIED
// before the handler runs").
func TestCapability_PresentButEmptyCapsFailsClosed(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)

	for _, tc := range []struct {
		name string
		caps string
	}{
		{"empty value", ""},
		{"whitespace only", "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			md := metadata.Pairs(flowmeta.MetadataKeyCapabilities, tc.caps)
			ctx := metadata.NewIncomingContext(context.Background(), md)
			_, err := srv.verifier.verify(ctx)
			if err == nil {
				t.Fatal("expected error for present-but-empty capabilities, got nil")
			}
			if status.Code(err) != codes.PermissionDenied ||
				status.Convert(err).Message() != "invalid capability signature" {
				t.Fatalf("expected PermissionDenied invalid-capability-signature, got %v", err)
			}
		})
	}
}

// TestCapability_NilCapsFailClosed pins the fail-closed branch of every
// node-facing capability gate (checkEntityCap, checkTxCap,
// checkWildcardEntityCap — cartographer_server.go): when no capability
// metadata is present, the ingress interceptor's system-to-system pass-through
// leaves nil capabilities in the context, and every node-facing gate must deny
// the request with PERMISSION_DENIED (errCapabilityDenied). Only the verifier
// half is pinned elsewhere (TestCapability_MissingMetadata); every RPC test
// injects non-nil capabilities (testCtx, capabilityContext/narrowCtx/noReadCtx),
// so a regression making any of the three nil branches fail open (return nil)
// would currently pass the entire suite. Each subtest drives one gate with a
// bare context.Background().
func TestCapability_NilCapsFailClosed(t *testing.T) {
	srv, _ := newTestServer(t)

	// checkEntityCap — ListEntities checks READ:graph/entity/<type> first.
	t.Run("checkEntityCap", func(t *testing.T) {
		_, err := srv.ListEntities(context.Background(), &flowv1.ListEntitiesRequest{EntityType: "Component"})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("expected PermissionDenied for nil capabilities on checkEntityCap, got %v", status.Code(err))
		}
	})

	// checkTxCap — BeginTransaction checks WRITE:graph/tx first.
	t.Run("checkTxCap", func(t *testing.T) {
		_, err := srv.BeginTransaction(context.Background(), &flowv1.BeginTransactionRequest{})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("expected PermissionDenied for nil capabilities on checkTxCap, got %v", status.Code(err))
		}
	})

	// checkWildcardEntityCap — Sync checks WRITE:graph/entity/* first.
	t.Run("checkWildcardEntityCap", func(t *testing.T) {
		_, err := srv.Sync(context.Background(), &flowv1.SyncRequest{})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("expected PermissionDenied for nil capabilities on checkWildcardEntityCap, got %v", status.Code(err))
		}
	})
}

func TestCapability_UnrecognizedSigner(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)

	sig := base64.StdEncoding.EncodeToString([]byte("fake"))
	md := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, "READ:graph/entity/Component",
		flowmeta.MetadataKeyCapabilitiesSignature, sig,
		flowmeta.MetadataKeyCapabilitiesSignedAt, "1234567890",
		flowmeta.MetadataKeyCapabilitiesSignedBy, "unknown",
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

func TestCapability_MissingSignedBy(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)

	// Valid caps + signature + signed-at present, but the signed-by key is
	// omitted entirely — no verification key can be selected, so verify must
	// return PERMISSION_DENIED (SPEC error table: absent/empty signed-by).
	payload := "READ:graph/entity/Component|1234567890"
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(scPriv, []byte(payload)))
	md := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, "READ:graph/entity/Component",
		flowmeta.MetadataKeyCapabilitiesSignature, sig,
		flowmeta.MetadataKeyCapabilitiesSignedAt, "1234567890",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := srv.verifier.verify(ctx)
	if err == nil {
		t.Fatal("expected error for missing signed-by, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

func TestCapability_StaleCapability_UnaryInterceptorRejectsBeforeHandler(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := capabilityContextAt(
		"WRITE:graph/entity/*", testSidecarPriv, "sidecar", time.Now().Add(-2*time.Minute).Unix(),
	)

	handlerInvoked, _, err := invokeSync(srv, ctx)
	if handlerInvoked {
		t.Fatal("unary handler ran for stale capability")
	}
	if status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != capabilityStaleMsg {
		t.Fatalf("expected stale-capability PermissionDenied, got %v", err)
	}
}

func TestCapability_StaleCapability_StreamInterceptorRejectsBeforeHandler(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := capabilityContextAt(
		"READ:graph/entity/*", testSidecarPriv, "sidecar", time.Now().Add(-2*time.Minute).Unix(),
	)

	handlerInvoked, err := invokeExportGraph(
		srv, &flowv1.ExportGraphRequest{Format: "json"}, &mockExportStream{ctx: ctx},
	)
	if handlerInvoked {
		t.Fatal("stream handler ran for stale capability")
	}
	if status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != capabilityStaleMsg {
		t.Fatalf("expected stale-capability PermissionDenied, got %v", err)
	}
}

// =========================================================================
// 2. Service-facing RPC tests (no capabilities needed)
// =========================================================================

func TestApplySchema_Valid(t *testing.T) {
	srv, st := newTestServer(t)
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
	srv, _ := newTestServer(t)
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
	srv, _ := newTestServer(t)
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

// TestApplySchema_ValidationErrorPaths covers each SPEC error-table schema
// validation path at the service level. Each invalid schema must be rejected
// with INVALID_ARGUMENT via ApplySchema.
func TestApplySchema_ValidationErrorPaths(t *testing.T) {
	tests := []struct {
		name   string
		schema *flowv1.Schema
	}{
		{
			// An ApplySchemaRequest whose schema field is unset (nil) must be
			// rejected with INVALID_ARGUMENT, not panic downstream in the
			// store's catalog diff (which would surface as INTERNAL).
			name:   "nil schema (omitted schema field)",
			schema: nil,
		},
		{
			name: "duplicate property name",
			schema: &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
				{Name: "Component", Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
					{Name: "name", Type: "string"},
				}},
			}},
		},
		{
			name: "name is a reserved word",
			schema: &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
				{Name: "MATCH", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
			}},
		},
		{
			name: "name is the reserved internal placeholder",
			schema: &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
				{Name: "_untyped", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
			}},
		},
		{
			name: "edge type name is the reserved internal placeholder",
			schema: &flowv1.Schema{EdgeTypes: []*flowv1.EdgeType{
				{Name: "_untyped", Properties: []*flowv1.Property{{Name: "weight", Type: "string"}}},
			}},
		},
		{
			name: "name violates cypher identifier regex",
			schema: &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
				{Name: "1bad-name", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
			}},
		},
		{
			name: "property name collides with implicit column",
			schema: &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
				{Name: "Component", Properties: []*flowv1.Property{{Name: "id", Type: "string"}}},
			}},
		},
		{
			name: "property name collides with embedding column on vector-indexed type",
			schema: &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
				{Name: "Component", EnableVectorIndex: true, Properties: []*flowv1.Property{{Name: "embedding", Type: "string"}}},
			}},
		},
		{
			name: "edge property collides with implicit column: from",
			schema: &flowv1.Schema{EdgeTypes: []*flowv1.EdgeType{
				{Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "from", Type: "string"}}},
			}},
		},
		{
			name: "edge property collides with implicit column: to",
			schema: &flowv1.Schema{EdgeTypes: []*flowv1.EdgeType{
				{Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "to", Type: "string"}}},
			}},
		},
		{
			name: "edge property collides with implicit column: type",
			schema: &flowv1.Schema{EdgeTypes: []*flowv1.EdgeType{
				{Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "type", Type: "string"}}},
			}},
		},
		{
			name: "invalid property type in schema",
			schema: &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
				{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "int"}}},
			}},
		},
		{
			name: "empty canConnectTo list",
			schema: &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
				{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
					Rules: []*flowv1.ConnectionRule{{CanConnectTo: []string{}, Using: []string{"DEPENDS_ON"}}}},
			}},
		},
		{
			name: "empty using list",
			schema: &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
				{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
					Rules: []*flowv1.ConnectionRule{{CanConnectTo: []string{"Component"}, Using: []string{}}}},
			}},
		},
		{
			name: "undeclared type reference",
			schema: &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
				{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
					Rules: []*flowv1.ConnectionRule{{CanConnectTo: []string{"Missing"}, Using: []string{"DEPENDS_ON"}}}},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := newTestServer(t)
			ctx := context.Background()
			_, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: tt.schema})
			if err == nil {
				t.Fatal("expected ApplySchema to reject invalid schema, got nil")
			}
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
			}
		})
	}
}

func TestHealthCheck_Healthy(t *testing.T) {
	srv, _ := newTestServer(t)
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
	srv, _ := newTestServer(t)
	ctx := context.Background()

	// Create an active transaction.
	srv.dbReady.Store(true)
	applyTestSchema(ctx, t, srv.store)
	err := srv.store.CreateBranchDB(ctx, "test-tx")
	if err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	_, _ = srv.txManager.Create("test-tx", 5*time.Minute, "head")

	_, err = srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{})
	if err == nil {
		t.Fatal("expected error for WipeGraph with open transactions, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", status.Code(err))
	}
}

// TestWipeGraph_ExpiredTxDoesNotBlock verifies that a transaction whose
// deadline has passed (but which has not yet been garbage-collected) is not
// considered active, so WipeGraph does NOT return FAILED_PRECONDITION
// (SPEC R2 WipeGraph: only transactions that are still open block a wipe).
func TestWipeGraph_ExpiredTxDoesNotBlock(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	srv.dbReady.Store(true)
	applyTestSchema(ctx, t, srv.store)
	err := srv.store.CreateBranchDB(ctx, "test-tx")
	if err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}

	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(7*24*time.Hour, 100000)
	srv.txManager.clock = fc
	_, _ = srv.txManager.Create("test-tx", 1*time.Minute, "head")

	// Advance past the transaction deadline without GC: the transaction is
	// still registered but is no longer active.
	fc.Advance(2 * time.Minute)
	if srv.txManager.HasActive() {
		t.Fatal("expired transaction must not be reported as active")
	}

	_, err = srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{})
	if err != nil {
		t.Fatalf("WipeGraph with only expired transaction should succeed, got %v", err)
	}
}

// TestWipeGraph_TreeOnTxBranchWipeLandsOnMain pins the wipe-on-main invariant
// when the git working tree is on a transaction branch: a failed commit leaves
// the tree checked out on the transaction branch
// (reconcileFailedCommitGitLocked), and an expired-but-not-yet-garbage-collected
// transaction is not considered active by HasActive (SPEC R2 error-table row
// "WipeGraph called while open transactions exist": "A timed-out transaction
// (deadline passed) is not considered open for this guard"). A wipe issued in
// that grace window must restore main before the git rm/commit so the deletion
// lands on main's history — otherwise the next sync-cycle RestoreMain brings
// the pre-wipe files back and stale data silently survives the wipe.
func TestWipeGraph_TreeOnTxBranchWipeLandsOnMain(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	failingGit := &mergeFailingGitStore{GitStore: gs}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, failingGit, opPub, initTestKey(), nil, "", 30*time.Second,
		"test-ns", 1*time.Minute, 100000, WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	// Replace the tx manager with a fake clock so the transaction can be
	// expired deterministically without running the GC loop.
	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(7*24*time.Hour, 100000)
	srv.txManager.clock = fc
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	// Establish a non-empty main: a committed entity whose pre-wipe file must
	// not survive a wipe that lands on the transaction branch.
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "pre-wipe"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("first CommitTransaction: %v", err)
	}
	// A second transaction whose commit fails at the fast-forward merge leaves
	// the working tree checked out on the transaction branch with its commit
	// recorded (reconcileFailedCommitGitLocked).
	failingGit.failMerge = true
	begin2, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "stale"},
		TransactionId: begin2.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin2.TransactionId,
	}); err == nil {
		t.Fatal("expected the simulated merge failure")
	}
	// Expire the transaction without GC: it is still registered (the tree is
	// still on its branch) but no longer blocks the wipe guard.
	fc.Advance(2 * time.Minute)
	if srv.txManager.HasActive() {
		t.Fatal("expired transaction must not be reported as active")
	}
	if _, err = srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{}); err != nil {
		t.Fatalf("WipeGraph: %v", err)
	}
	// The wipe must have landed on main: restore main (as the next sync cycle
	// does) and assert the pre-wipe files do not survive.
	if err := gs.WithGitLock(func() error {
		if err := gs.RestoreMain(ctx); err != nil {
			return err
		}
		if err := gs.CleanUntracked(ctx); err != nil {
			return err
		}
		types, err := gs.ListEntityTypes(ctx)
		if err != nil {
			return err
		}
		if len(types) != 0 {
			return fmt.Errorf("entity files survived the wipe on main: %v", types)
		}
		edgeTypes, err := gs.ListEdgeTypes(ctx)
		if err != nil {
			return err
		}
		if len(edgeTypes) != 0 {
			return fmt.Errorf("edge files survived the wipe on main: %v", edgeTypes)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// =========================================================================
// 3. Read-path tests
// =========================================================================

func TestExecuteCypher_EmptyQuery(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: ""})
	if err == nil {
		t.Fatal("expected error for empty query, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// TestExecuteCypher_ParamsNotAStruct asserts the errCypherParamsNotAStruct
// branch: when req.Params is present but not a *structpb.StructValue (a JSON
// object), ExecuteCypher rejects the request with INVALID_ARGUMENT. Per SPEC
// R2 the params must decode from a JSON object; a list/unset-wrapped value is
// the untested divergence from that contract.
func TestExecuteCypher_ParamsNotAStruct(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()
	// Schema is applied so the statement parses server-side; the params
	// validation then fires (the SPEC check order runs the parse before the
	// capability/params gates, so an unparseable statement would otherwise
	// surface its syntax error first).
	applyTestSchema(ctx, t, srv.store)

	// Wrap a list in Params: GetStructValue() is nil for a list, hitting the
	// not-a-struct branch rather than an absent-Params fast path.
	nonStructParams := structpb.NewListValue(&structpb.ListValue{})
	_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{
		Cypher: "MATCH (n:Component) RETURN n",
		Params: nonStructParams,
	})
	if err == nil {
		t.Fatal("expected error for non-struct params, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
	}
	if got, want := status.Convert(err).Message(), "cypher query parameters must be a JSON object"; got != want {
		t.Fatalf("expected message %q, got %q", want, got)
	}
}

func TestExecuteCypher_ValidQuery(t *testing.T) {
	srv, _ := newTestServer(t)
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

// TestRowsToRowsOneTuplePerRow asserts the SPEC R2 flat-tuple Row contract:
// each Row is one flat tuple of string values in the order the caller supplied
// (LadybugDB return order) — no sorted column schema, no cross-row alignment
// or null-filling, and non-string values stringified. A null column (an absent
// property in a RETURN) becomes the empty string, since the v1 wire carries no
// null marker.
func TestRowsToRowsOneTuplePerRow(t *testing.T) {
	rows := []store.CypherRow{
		{Values: []any{"id-1", "x", int64(2)}},
		{Values: []any{"id-2"}},
	}
	got := rowsToRows(rows)
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(got))
	}
	want := [][]string{
		{"id-1", "x", "2"},
		{"id-2"},
	}
	for i, row := range got {
		if len(row.Values) != len(want[i]) {
			t.Fatalf("row %d: expected %d values, got %d", i, len(want[i]), len(row.Values))
		}
		for j, w := range want[i] {
			if row.Values[j] != w {
				t.Fatalf("row %d value %d: expected %q, got %q", i, j, w, row.Values[j])
			}
		}
	}
}

// TestCypherValueStringNullIsEmpty asserts a null column value stringifies to
// the empty string on the string-only v1 row wire.
func TestCypherValueStringNullIsEmpty(t *testing.T) {
	if got, want := cypherValueString(nil), ""; got != want {
		t.Fatalf("cypherValueString(nil) = %q, want %q", got, want)
	}
	if got, want := cypherValueString("hello"), "hello"; got != want {
		t.Fatalf("cypherValueString(\"hello\") = %q, want %q", got, want)
	}
}

func TestExecuteCypher_MutationRejected(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()
	applyTestSchema(ctx, t, st)

	// Each mutation/DDL clause the SPEC R7 §5 and the error table enumerate
	// (CREATE, SET, DELETE, MERGE, REMOVE, DROP, DDL index/constraint, and
	// FOREACH-as-mutation) must be rejected by ExecuteCypher so no mutation
	// ever executes through the read-only RPC.
	//
	// LadybugDB v0.17.0's parser recognises CREATE/SET/DELETE/MERGE/DROP and
	// classifies each as non-read-only, surfacing ErrMutationCypher which the
	// service maps to PERMISSION_DENIED (row "ExecuteCypher with mutation
	// statement", SPEC:976). Its grammar does not parse top-level FOREACH,
	// `MATCH ... REMOVE ...`, or index/constraint DDL; those fail at the
	// syntax gate and surface INVALID_ARGUMENT ("Invalid Cypher syntax",
	// SPEC:979) — SPEC R3 mandates INVALID_ARGUMENT for every statement that
	// fails to parse (SPEC:260) and the R7 §5 grammar-gap note pins it "never
	// as PERMISSION_DENIED" (SPEC:493-497). The syntax gate precedes read-only
	// enforcement in the ExecuteCypher check order (SPEC:1015).
	cases := []struct {
		name           string
		cypher         string
		wantStatusCode codes.Code
	}{
		{"create", "CREATE (n:Component {id: '11111111-1111-1111-1111-111111111111', name: 'x'})", codes.PermissionDenied},
		{"set", "MATCH (n:Component) SET n.name = 'x'", codes.PermissionDenied},
		{"delete", "MATCH (n:Component) DELETE n", codes.PermissionDenied},
		{"merge", "MERGE (n:Component {id: '11111111-1111-1111-1111-111111111111'})", codes.PermissionDenied},
		{"drop", "DROP TABLE Component", codes.PermissionDenied},
		{"remove", "MATCH (n:Component) REMOVE n.name", codes.InvalidArgument},
		{"ddl-index", "CREATE INDEX Component_name IF NOT EXISTS FOR (n:Component) ON (n.name)", codes.InvalidArgument},
		{"ddl-constraint", "CREATE CONSTRAINT IF NOT EXISTS FOR (n:Component) REQUIRE n.id IS UNIQUE",
			codes.InvalidArgument},
		{"foreach-as-mutation", "FOREACH (x IN ['aaa'] | CREATE (n:Component {id: x}))", codes.InvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: tc.cypher})
			if err == nil {
				t.Fatalf("expected error for mutation %q, got nil", tc.cypher)
			}
			if got := status.Code(err); got != tc.wantStatusCode {
				t.Errorf("mutation %q: expected %v, got %v (%v)", tc.cypher, tc.wantStatusCode, got, err)
			}
		})
	}
}

func TestFullTextSearch_EmptyQuery(t *testing.T) {
	srv, _ := newTestServer(t)
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
	srv, _ := newTestServer(t)
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

// TestFullTextSearch_ReturnsEmbedding pins the Entity wire contract
// (proto/flow/v1/cartographer.proto: Entity.embedding): the FullTextSearch
// handler must populate the embedding field from the store result (the store
// returns embeddings via entityFromNode for vector-indexed types) instead of
// silently dropping it, so SDK callers reading GetEmbedding() receive the
// stored vector.
func TestFullTextSearch_ReturnsEmbedding(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

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
	_, _ = srv.store.CreateEntity(ctx, "VectorType", "", map[string]string{"name": "apple"}, []float32{0.1, 0.2, 0.3}, "")

	resp, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{Query: "apple"})
	if err != nil {
		t.Fatalf("FullTextSearch failed: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected at least one result")
	}
	if len(resp.Results[0].Embedding) != 3 ||
		resp.Results[0].Embedding[0] != 0.1 || resp.Results[0].Embedding[2] != 0.3 {
		t.Fatalf("expected embedding [0.1 0.2 0.3] on result, got %v", resp.Results[0].Embedding)
	}
}

// TestFullTextSearch_SingleTypeSpecificCapabilityPasses pins SPEC R3
// (SPEC:240): a caller holding only READ:graph/entity/<type> is authorised
// for a FullTextSearch scoped to that type (the per-type branch, not the
// wildcard fallback).
func TestFullTextSearch_SingleTypeSpecificCapabilityPasses(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("READ:graph/entity/Component")

	applyTestSchema(ctx, t, srv.store)
	_, _ = srv.store.CreateEntity(testCtx(), "Component", "", map[string]string{"name": "apple", "version": "1"}, nil, "")

	resp, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{
		Query:      "apple",
		EntityType: "Component",
	})
	if err != nil {
		t.Fatalf("FullTextSearch with per-type capability failed: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected at least one result")
	}
}

func TestListEntities_UnknownType(t *testing.T) {
	srv, _ := newTestServer(t)
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
	srv, _ := newTestServer(t)
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

// TestListEntities_ReturnsEmbedding pins the Entity wire contract
// (proto/flow/v1/cartographer.proto: Entity.embedding): the ListEntities
// handler must populate the embedding field from the store result instead of
// silently dropping it, so SDK callers reading GetEmbedding() receive the
// stored vector.
func TestListEntities_ReturnsEmbedding(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

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
	_, _ = srv.store.CreateEntity(ctx, "VectorType", "", map[string]string{"name": "a"}, []float32{0.4, 0.5}, "")

	resp, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{EntityType: "VectorType", PageSize: 10})
	if err != nil {
		t.Fatalf("ListEntities failed: %v", err)
	}
	if len(resp.Entities) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(resp.Entities))
	}
	if len(resp.Entities[0].Embedding) != 2 ||
		resp.Entities[0].Embedding[0] != 0.4 || resp.Entities[0].Embedding[1] != 0.5 {
		t.Fatalf("expected embedding [0.4 0.5] on entity, got %v", resp.Entities[0].Embedding)
	}
}

// TestListEntities_SingleTypeSpecificCapabilityPasses pins SPEC R3
// (SPEC:240): a caller holding only READ:graph/entity/<type> is authorised
// for a ListEntities scoped to that type (the per-type branch, not the
// wildcard fallback).
func TestListEntities_SingleTypeSpecificCapabilityPasses(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("READ:graph/entity/Component")

	applyTestSchema(ctx, t, srv.store)
	_, _ = srv.store.CreateEntity(testCtx(), "Component", "", map[string]string{"name": "a"}, nil, "")
	_, _ = srv.store.CreateEntity(testCtx(), "Component", "", map[string]string{"name": "b"}, nil, "")

	resp, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
		EntityType: "Component",
		PageSize:   10,
	})
	if err != nil {
		t.Fatalf("ListEntities with per-type capability failed: %v", err)
	}
	if len(resp.Entities) != 2 {
		t.Fatalf("expected 2 entities, got %d", len(resp.Entities))
	}
}

func TestSearchNeighbors_NonIndexed(t *testing.T) {
	srv, _ := newTestServer(t)
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
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	resp, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "test", "version": "1.0"},
		TransactionId: txID,
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

// TestCreateEntity_SingleTypeSpecificCapabilityPasses pins SPEC R3
// (SPEC:242): a caller holding only WRITE:graph/entity/<type> (plus
// WRITE:graph/tx to begin the transaction) is authorised for a CreateEntity
// of that type (the per-type branch, not the wildcard fallback).
func TestCreateEntity_SingleTypeSpecificCapabilityPasses(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("WRITE:graph/entity/Component", "WRITE:graph/tx")

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	resp, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "per-type", "version": "1.0"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity with per-type capability failed: %v", err)
	}
	if resp.EntityId == "" {
		t.Fatal("expected non-empty entity ID")
	}
}

// TestUpdateEntity_SingleTypeSpecificCapabilityPasses pins SPEC R3
// (SPEC:242): a caller holding only WRITE:graph/entity/<type> (plus
// WRITE:graph/tx to begin the transaction) is authorised for an UpdateEntity
// of that type through the Cartographer's authoritative per-type check. The
// positive per-type branch is pinned for CreateEntity elsewhere; without this
// test a handler regression that required the wildcard for UpdateEntity would
// go undetected.
func TestUpdateEntity_SingleTypeSpecificCapabilityPasses(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("WRITE:graph/entity/Component", "WRITE:graph/tx")

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	ent, err := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "original"}, nil, txID)
	if err != nil {
		t.Fatalf("seed entity: %v", err)
	}

	resp, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:            ent.Id,
		Properties:    map[string]string{"version": "2"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("UpdateEntity with per-type capability failed: %v", err)
	}
	if resp.Properties["version"] != "2" {
		t.Fatalf("expected version=2, got %q", resp.Properties["version"])
	}
}

// TestDeleteEntity_SingleTypeSpecificCapabilityPasses pins SPEC R3
// (SPEC:242): a caller holding only WRITE:graph/entity/<type> (plus
// WRITE:graph/tx) is authorised for a DeleteEntity of that type through the
// Cartographer's authoritative per-type check.
func TestDeleteEntity_SingleTypeSpecificCapabilityPasses(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("WRITE:graph/entity/Component", "WRITE:graph/tx")

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	ent, err := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "delete-me"}, nil, txID)
	if err != nil {
		t.Fatalf("seed entity: %v", err)
	}

	resp, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: ent.Id, TransactionId: txID})
	if err != nil {
		t.Fatalf("DeleteEntity with per-type capability failed: %v", err)
	}
	if resp.EntityId != ent.Id {
		t.Fatalf("expected deleted entity ID %q, got %q", ent.Id, resp.EntityId)
	}
}

// TestCreateEdge_SingleTypeSpecificCapabilityPasses pins SPEC R3
// (SPEC:249-250): a caller holding only WRITE:graph/entity/<source-type>
// (plus WRITE:graph/tx) is authorised for a CreateEdge whose source entity is
// of that type, through the Cartographer's authoritative per-type check.
func TestCreateEdge_SingleTypeSpecificCapabilityPasses(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("WRITE:graph/entity/Service", "WRITE:graph/tx")

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	svc, err := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, txID)
	if err != nil {
		t.Fatalf("seed source entity: %v", err)
	}
	comp, err := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "core"}, nil, txID)
	if err != nil {
		t.Fatalf("seed target entity: %v", err)
	}

	resp, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  svc.Id,
		ToEntityId:    comp.Id,
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEdge with per-type capability failed: %v", err)
	}
	if resp.EdgeId == "" {
		t.Fatal("expected non-empty edge ID")
	}
}

// TestDeleteEdge_SingleTypeSpecificCapabilityPasses pins SPEC R3
// (SPEC:249-250): a caller holding only WRITE:graph/entity/<source-type>
// (plus WRITE:graph/tx) is authorised for a DeleteEdge whose source entity is
// of that type, through the Cartographer's authoritative per-type check.
func TestDeleteEdge_SingleTypeSpecificCapabilityPasses(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("WRITE:graph/entity/Service", "WRITE:graph/tx")

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	svc, err := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, txID)
	if err != nil {
		t.Fatalf("seed source entity: %v", err)
	}
	comp, err := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "core"}, nil, txID)
	if err != nil {
		t.Fatalf("seed target entity: %v", err)
	}
	edge, err := srv.store.CreateEdge(ctx, "DEPENDS_ON", svc.Id, comp.Id, nil, txID)
	if err != nil {
		t.Fatalf("seed edge: %v", err)
	}

	resp, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: edge.Id, TransactionId: txID})
	if err != nil {
		t.Fatalf("DeleteEdge with per-type capability failed: %v", err)
	}
	if resp.EdgeId != edge.Id {
		t.Fatalf("expected deleted edge ID %q, got %q", edge.Id, resp.EdgeId)
	}
}

func TestCreateEntity_UnknownType(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Unknown",
		Properties:    map[string]string{"name": "x"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for unknown type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEntity_DuplicateID(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	resp, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "first"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("first CreateEntity failed: %v", err)
	}
	// Same ID again.
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Id:            resp.EntityId,
		Properties:    map[string]string{"name": "second"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for duplicate ID, got nil")
	}
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %v", status.Code(err))
	}
}

func TestCreateEntity_MissingRequiredProperty(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{},
		TransactionId: txID,
	})
	if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "missing required property") {
		t.Fatalf("CreateEntity without required property should fail: %v", err)
	}
}

func TestUpdateEntity_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
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

func TestUpdateEntity_Valid(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	ent, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "original"}, nil, txID)

	resp, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:            ent.Id,
		Properties:    map[string]string{"version": "2"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("UpdateEntity failed: %v", err)
	}
	if resp.Properties["version"] != "2" {
		t.Fatalf("expected version=2, got %q", resp.Properties["version"])
	}
}

func TestDeleteEntity_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
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

func TestDeleteEntity_InvalidUUID(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	_, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: "not-a-uuid", TransactionId: txID})
	if err == nil {
		t.Fatal("expected error for invalid UUID, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestDeleteEntity_Valid(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	ent, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "delete-me"}, nil, txID)

	resp, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: ent.Id, TransactionId: txID})
	if err != nil {
		t.Fatalf("DeleteEntity failed: %v", err)
	}
	if resp.EntityId != ent.Id {
		t.Fatalf("expected deleted entity ID %q, got %q", ent.Id, resp.EntityId)
	}
}

// TestDeleteEntity_ReturnsEmbedding pins the DeleteEntityResponse wire
// contract (proto/flow/v1/cartographer.proto: DeleteEntityResponse.embedding):
// the handler must populate the embedding field from the store's
// read-before-delete result instead of silently dropping it, so SDK callers
// reading GetEmbedding() receive the deleted entity's stored vector.
func TestDeleteEntity_ReturnsEmbedding(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

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
	txID := beginTestTx(t, srv, ctx)
	ent, _ := srv.store.CreateEntity(ctx, "VectorType", "", map[string]string{"name": "a"}, []float32{0.1, 0.2, 0.3}, txID)

	resp, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: ent.Id, TransactionId: txID})
	if err != nil {
		t.Fatalf("DeleteEntity failed: %v", err)
	}
	if len(resp.Embedding) != 3 ||
		resp.Embedding[0] != 0.1 || resp.Embedding[2] != 0.3 {
		t.Fatalf("expected embedding [0.1 0.2 0.3] on deleted entity, got %v", resp.Embedding)
	}
}

// TestDeleteEntity_TransactionRecordsCascadeEdgeDeletion verifies SPEC R7 §4
// atomicity is preserved across a commit: deleting an entity inside a
// transaction must also record the cascade-removed edges in the change log so
// commit serialisation removes their git files. Without this, committed main
// retains edges pointing at a deleted entity.
func TestDeleteEntity_TransactionRecordsCascadeEdgeDeletion(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	txID := begin.TransactionId

	// Create the participating entities and edge inside the transaction so they
	// exist on the branch DB (the in-memory branch is not seeded from main).
	svcResp, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Service", Properties: map[string]string{"name": "svc"}, TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity svc: %v", err)
	}
	svc := svcResp.EntityId
	compResp, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "core"}, TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity comp: %v", err)
	}
	comp := compResp.EntityId
	edgeResp, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType: "DEPENDS_ON", FromEntityId: svc, ToEntityId: comp, TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}

	// Delete the service entity inside the transaction; the DEPENDS_ON edge it
	// participates in is cascade-removed by the store.
	_, err = srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: svc, TransactionId: txID})
	if err != nil {
		t.Fatalf("transactional DeleteEntity: %v", err)
	}

	// The deleted edge must be present in the change log as a ChangeDelEdge.
	state, err := srv.txManager.Lookup(txID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if _, ok := state.ChangeLog.DeletedEdges[edgeResp.EdgeId]; !ok {
		t.Fatalf("expected cascade-deleted edge %q in change log, got %+v", edgeResp.EdgeId, state.ChangeLog.DeletedEdges)
	}
	if _, ok := state.ChangeLog.DeletedEntities[svc]; !ok {
		t.Fatalf("expected deleted entity %q in change log", svc)
	}
}

func TestCreateEdge_Valid(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	svc, _ := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, txID)
	comp, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "core"}, nil, txID)

	resp, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  svc.Id,
		ToEntityId:    comp.Id,
		Properties:    map[string]string{"weight": "high"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEdge failed: %v", err)
	}
	if resp.EdgeId == "" {
		t.Fatal("expected non-empty edge ID")
	}
}

func TestCreateEdge_SourceNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
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

func TestCreateEdge_UnknownEdgeType(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	ent, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "x"}, nil, txID)

	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "UNKNOWN_EDGE",
		FromEntityId:  ent.Id,
		ToEntityId:    ent.Id,
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for unknown edge type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// TestCreateEdge_UnknownEdgeTypeWinsOverMissingCapability asserts the SPEC
// validation order (structural before capability): a caller lacking
// WRITE:graph/entity/* still gets INVALID_ARGUMENT (not PERMISSION_DENIED)
// for an unknown edge type. The transaction is begun with full capabilities;
// the mutation call itself carries only READ capabilities.
func TestCreateEdge_UnknownEdgeTypeWinsOverMissingCapability(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)

	// Only READ capabilities — the caller holds no write capability at all.
	noWriteCtx := capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")
	_, err := srv.CreateEdge(noWriteCtx, &flowv1.CreateEdgeRequest{
		EdgeType:      "UNKNOWN_EDGE",
		FromEntityId:  "11111111-1111-4111-8111-111111111111",
		ToEntityId:    "22222222-2222-4222-8222-222222222222",
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for unknown edge type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected structural InvalidArgument to win over capability check, got %v (%v)", status.Code(err), err)
	}
}

func TestDeleteEdge_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
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

func TestDeleteEdge_InvalidUUID(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	_, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: "not-a-uuid", TransactionId: txID})
	if err == nil {
		t.Fatal("expected error for invalid UUID, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// TestCreateEntity_InvalidIDWinsOverMissingCapability asserts the SPEC
// CreateEntity validation order (structural before capability): a caller lacking
// WRITE capabilities but supplying a structurally-invalid explicit `id` gets
// INVALID_ARGUMENT (not PERMISSION_DENIED) — mirroring
// TestCreateEdge_UnknownEdgeTypeWinsOverMissingCapability. The transaction is
// begun with full capabilities; the mutation call itself carries only READ
// capabilities.
func TestCreateEntity_InvalidIDWinsOverMissingCapability(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	// Only READ capabilities — the caller holds no write capability at all.
	noWriteCtx := capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")
	_, err := srv.CreateEntity(noWriteCtx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Id:            "not-a-uuid",
		Properties:    map[string]string{"name": "x"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for invalid entity ID, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected structural InvalidArgument to win over capability check, got %v (%v)", status.Code(err), err)
	}
}

// TestCreateEntity_UnknownPropertyWinsOverMissingCapability asserts the SPEC
// CreateEntity validation order (SPEC:1004: structural validation →
// data-integrity; R7 §1: unknown property → INVALID_ARGUMENT): a caller
// lacking WRITE capabilities but supplying an unknown property gets
// INVALID_ARGUMENT (not PERMISSION_DENIED) — mirroring
// TestCreateEdge_UnknownEdgeTypeWinsOverMissingCapability. The transaction is
// begun with full capabilities; the mutation call itself carries only READ
// capabilities.
func TestCreateEntity_UnknownPropertyWinsOverMissingCapability(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	// Only READ capabilities — the caller holds no write capability at all.
	noWriteCtx := capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")
	_, err := srv.CreateEntity(noWriteCtx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "x", "nonexistent": "value"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for unknown property, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected structural InvalidArgument to win over capability check, got %v (%v)", status.Code(err), err)
	}
	if !strings.Contains(err.Error(), "unknown property") {
		t.Fatalf("expected the unknown-property rejection, got %v", err)
	}
}

// TestCreateEntity_MissingRequiredPropertyWinsOverMissingCapability pins the
// same SPEC check-order guarantee (SPEC:1004, R7 §1: missing required
// property → INVALID_ARGUMENT) for the missing-required-property branch: a
// caller lacking WRITE capabilities but omitting a required property gets
// INVALID_ARGUMENT (not PERMISSION_DENIED), mirroring the unknown-property
// test above.
func TestCreateEntity_MissingRequiredPropertyWinsOverMissingCapability(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	// Only READ capabilities — the caller holds no write capability at all.
	noWriteCtx := capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")
	_, err := srv.CreateEntity(noWriteCtx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for missing required property, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected structural InvalidArgument to win over capability check, got %v (%v)", status.Code(err), err)
	}
	if !strings.Contains(err.Error(), "missing required property") {
		t.Fatalf("expected the missing-required-property rejection, got %v", err)
	}
}

// TestUpdateEntity_EmbeddingRewriteSuccess drives UpdateEntity on an
// established vector-indexed row and asserts the embedding rewrite succeeds:
// the store drops the vector index, writes the new embedding, and recreates the
// index (SPEC R2/R7 — a dimension-matching, NaN-free embedding update is
// accepted; only dimension mismatch and NaN/Inf are rejections). The response
// carries the persisted embedding.
func TestUpdateEntity_EmbeddingRewriteSuccess(t *testing.T) {
	srv, _ := newTestServer(t)
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
	if err := srv.store.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	txID := beginTestTx(t, srv, ctx)
	// Bootstrap a 3-dim vector index via CreateEntity with an embedding.
	ent, err := srv.store.CreateEntity(
		ctx, "VecType", "", map[string]string{"name": "seeded"}, []float32{1.0, 0.0, 0.0}, txID,
	)
	if err != nil {
		t.Fatalf("bootstrap CreateEntity: %v", err)
	}
	resp, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:            ent.Id,
		Embedding:     []float32{0.0, 1.0, 0.0},
		Properties:    map[string]string{"name": "updated"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("matching-dimension embedding update must succeed, got %v", err)
	}
	if !reflect.DeepEqual(resp.Embedding, []float32{0.0, 1.0, 0.0}) {
		t.Fatalf("expected rewritten embedding [0 1 0], got %v", resp.Embedding)
	}
	if resp.Properties["name"] != "updated" {
		t.Fatalf("expected name=updated, got %q", resp.Properties["name"])
	}
}

// =========================================================================
// 5. Transaction lifecycle tests
// =========================================================================

func TestTransaction_BeginCommit(t *testing.T) {
	srv, _ := newTestServer(t)
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
	srv, _ := newTestServer(t)
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

func TestTransaction_ChangeLogAdmissionFailureRollsBackEveryMutationFamily(t *testing.T) {
	type mutationCase struct {
		name  string
		setup func(context.Context, *testing.T, store.Store, string)
		first func(context.Context, *CartographerServer, string) error
		over  func(context.Context, *CartographerServer, string) error
	}
	ids := []string{
		testMutationEntityID,
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		"44444444-4444-4444-8444-444444444444",
	}
	seedEntities := func(ctx context.Context, t *testing.T, st store.Store, branch string) {
		t.Helper()
		for i, id := range ids {
			entityType := "Component"
			if i < 2 {
				entityType = "Service"
			}
			_, err := st.CreateEntity(
				ctx, entityType, id, map[string]string{"name": fmt.Sprintf("entity-%d", i)}, nil, branch,
			)
			if err != nil {
				t.Fatalf("seed entity %s on %q: %v", id, branch, err)
			}
		}
	}
	seedEdges := func(ctx context.Context, t *testing.T, st store.Store, branch string) {
		t.Helper()
		seedEntities(ctx, t, st, branch)
		for i := range 2 {
			_, err := st.CreateEdge(
				ctx, "DEPENDS_ON", ids[i], ids[i+2], map[string]string{"weight": fmt.Sprintf("%d", i)}, branch,
			)
			if err != nil {
				t.Fatalf("seed edge on %q: %v", branch, err)
			}
		}
	}
	createEntity := func(name string) func(context.Context, *CartographerServer, string) error {
		return func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
				EntityType: "Component", Properties: map[string]string{"name": name}, TransactionId: txID,
			})
			return err
		}
	}
	updateEntity := func(id, version string) func(context.Context, *CartographerServer, string) error {
		return func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
				Id: id, Properties: map[string]string{"version": version}, TransactionId: txID,
			})
			return err
		}
	}
	deleteEntity := func(id string) func(context.Context, *CartographerServer, string) error {
		return func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: id, TransactionId: txID})
			return err
		}
	}
	createEdge := func(from, to string) func(context.Context, *CartographerServer, string) error {
		return func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
				EdgeType: "DEPENDS_ON", FromEntityId: from, ToEntityId: to, TransactionId: txID,
			})
			return err
		}
	}

	cases := []mutationCase{
		{name: "CreateEntity", first: createEntity("first"), over: createEntity("overflow")},
		{name: "UpdateEntity", setup: seedEntities,
			first: updateEntity(ids[2], "first"), over: updateEntity(ids[3], "overflow")},
		{name: "DeleteEntity", setup: seedEntities, first: deleteEntity(ids[2]), over: deleteEntity(ids[3])},
		{name: "CreateEdge", setup: seedEntities, first: createEdge(ids[0], ids[2]), over: createEdge(ids[1], ids[3])},
		{name: "DeleteEdge", setup: seedEdges,
			first: func(ctx context.Context, srv *CartographerServer, txID string) error {
				edges, err := srv.store.ListEdgesOfType(ctx, "DEPENDS_ON", txID)
				if err != nil {
					return err
				}
				_, err = srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: edges[0].Id, TransactionId: txID})
				return err
			},
			over: func(ctx context.Context, srv *CartographerServer, txID string) error {
				edges, err := srv.store.ListEdgesOfType(ctx, "DEPENDS_ON", txID)
				if err != nil {
					return err
				}
				_, err = srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: edges[0].Id, TransactionId: txID})
				return err
			}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, base := newTestServer(t)
			srv.txManager.changeLogCap = 1
			ctx := testCtx()
			applyTestSchema(ctx, t, base)
			begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
			if err != nil {
				t.Fatalf("BeginTransaction: %v", err)
			}
			if tc.setup != nil {
				tc.setup(ctx, t, base, "main")
				tc.setup(ctx, t, base, begin.TransactionId)
			}
			mainEntitiesBefore, _, err := base.ListEntities(ctx, "Component", 100, "", "main")
			if err != nil {
				t.Fatalf("snapshot main components: %v", err)
			}
			mainServicesBefore, _, err := base.ListEntities(ctx, "Service", 100, "", "main")
			if err != nil {
				t.Fatalf("snapshot main services: %v", err)
			}
			mainEdgesBefore, err := base.ListEdgesOfType(ctx, "DEPENDS_ON", "main")
			if err != nil {
				t.Fatalf("snapshot main edges: %v", err)
			}

			if err := tc.first(ctx, srv, begin.TransactionId); err != nil {
				t.Fatalf("first mutation: %v", err)
			}
			if err := tc.over(ctx, srv, begin.TransactionId); status.Code(err) != codes.ResourceExhausted {
				t.Fatalf("overflow mutation error = %v, want ResourceExhausted", err)
			}
			if _, err := srv.txManager.Lookup(begin.TransactionId); err == nil {
				t.Fatal("overflow transaction remained active")
			}
			exists, err := srv.gitstore.BranchExists(ctx, begin.TransactionId)
			if err != nil || exists {
				t.Fatalf("git branch cleanup: exists=%v err=%v", exists, err)
			}
			if _, err := base.DumpAllEntities(ctx, begin.TransactionId); !errors.Is(err, store.ErrBranchNotFound) {
				t.Fatalf("branch DB remained: %v", err)
			}
			mainEntitiesAfter, _, err := base.ListEntities(ctx, "Component", 100, "", "main")
			if err != nil {
				t.Fatalf("read main components after rejection: %v", err)
			}
			mainServicesAfter, _, err := base.ListEntities(ctx, "Service", 100, "", "main")
			if err != nil {
				t.Fatalf("read main services after rejection: %v", err)
			}
			mainEdgesAfter, err := base.ListEdgesOfType(ctx, "DEPENDS_ON", "main")
			if err != nil {
				t.Fatalf("read main edges after rejection: %v", err)
			}
			if !reflect.DeepEqual(mainEntitiesAfter, mainEntitiesBefore) ||
				!reflect.DeepEqual(mainServicesAfter, mainServicesBefore) ||
				!reflect.DeepEqual(mainEdgesAfter, mainEdgesBefore) {
				t.Fatal("main changed after rejected transaction mutation")
			}
		})
	}
}

func TestDeleteEntity_StoreFailureDoesNotAddChangeLogEntry(t *testing.T) {
	srv, base := newTestServer(t)
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	id := testMutationEntityID
	if _, err := base.CreateEntity(
		ctx, "Component", id, map[string]string{"name": "kept"}, nil, begin.TransactionId,
	); err != nil {
		t.Fatalf("seed branch entity: %v", err)
	}
	srv.store = &deleteEntityFailingStore{Store: base}
	_, err = srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: id, TransactionId: begin.TransactionId})
	if err == nil || !strings.Contains(err.Error(), "simulated DeleteEntity failure") {
		t.Fatalf("DeleteEntity error = %v", err)
	}
	state, err := srv.txManager.Lookup(begin.TransactionId)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if state.ChangeLog.Len() != 0 {
		t.Fatalf("change log contains failed deletion: %+v", state.ChangeLog.Entries())
	}
	if _, err := base.GetEntity(ctx, id, begin.TransactionId); err != nil {
		t.Fatalf("failed deletion removed entity: %v", err)
	}
}

func TestTransaction_ChangeLogRollbackFailureIsExplicitAndRetryable(t *testing.T) {
	srv, base := newTestServer(t)
	failing := &dropFailingStore{Store: base, failDrop: true}
	srv.store = failing
	srv.txManager.changeLogCap = 1
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	for i := range 2 {
		_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
			EntityType: "Component", Properties: map[string]string{"name": fmt.Sprintf("entity-%d", i)},
			TransactionId: begin.TransactionId,
		})
		if i == 0 && err != nil {
			t.Fatalf("first CreateEntity: %v", err)
		}
	}
	if status.Code(err) != codes.ResourceExhausted || !strings.Contains(err.Error(), "transaction rollback failed") ||
		!strings.Contains(err.Error(), "simulated DropBranchDB failure") {
		t.Fatalf("overflow cleanup error = %v", err)
	}
	state, err := srv.txManager.Lookup(begin.TransactionId)
	if err != nil {
		t.Fatalf("transaction not retained for cleanup retry: %v", err)
	}
	if !state.RollbackOnly {
		t.Fatal("transaction was not marked rollback-only")
	}
	if _, err := base.DumpAllEntities(ctx, begin.TransactionId); err != nil {
		t.Fatalf("branch DB not retained after failed drop: %v", err)
	}
	exists, err := srv.gitstore.BranchExists(ctx, begin.TransactionId)
	if err != nil || !exists {
		t.Fatalf("Git recovery anchor not retained: exists=%v err=%v", exists, err)
	}
	// Subsequent operations on the rolled-back (rollback-only) transaction are
	// rejected with NOT_FOUND — the SPEC error table defines the cap-violation
	// outcome as RESOURCE_EXHAUSTED with the transaction "rolled back", and
	// "Transaction not found" covers "already committed/rolled back". Only
	// RollbackTransaction (below) may still finish the cleanup.
	assertTerminal := func(name string, call func() error) {
		t.Helper()
		if err := call(); status.Code(err) != codes.NotFound {
			t.Fatalf("%s error = %v, want NotFound for rolled-back transaction", name, err)
		}
	}
	assertTerminal("read", func() error {
		_, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
			EntityType: "Component", TransactionId: begin.TransactionId,
		})
		return err
	})
	assertTerminal("mutation", func() error {
		_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
			EntityType: "Component", Properties: map[string]string{"name": "rejected"},
			TransactionId: begin.TransactionId,
		})
		return err
	})
	assertTerminal("diff", func() error {
		_, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: begin.TransactionId})
		return err
	})
	assertTerminal("commit", func() error {
		_, err := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
		return err
	})
	opPub, _ := generateTestKey()
	restarted := NewCartographerServer(
		base, srv.gitstore, opPub, initTestKey(), nil, "", 30*time.Second,
		"test-ns", 30*time.Minute, 1,
	)
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions after failed cleanup: %v", err)
	}
	recovered, err := restarted.txManager.Lookup(begin.TransactionId)
	if err != nil || !recovered.RollbackOnly {
		t.Fatalf("recovered transaction was not rollback-only: state=%+v err=%v", recovered, err)
	}
	_, err = restarted.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("retry RollbackTransaction: %v", err)
	}
	if _, err := restarted.txManager.Lookup(begin.TransactionId); err == nil {
		t.Fatal("transaction remained after cleanup retry")
	}
	if _, err := base.DumpAllEntities(ctx, begin.TransactionId); !errors.Is(err, store.ErrBranchNotFound) {
		t.Fatalf("branch DB remained after cleanup retry: %v", err)
	}
}

func TestRollbackTransaction_RestoresMainAfterFailedMerge(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	failingGit := &mergeFailingGitStore{GitStore: gs, failMerge: true}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, failingGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	created, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "partial"}, TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	if err == nil {
		t.Fatal("expected merge failure")
	} else if !strings.Contains(err.Error(), "simulated merge failure") {
		t.Fatalf("commit failed before merge: %v", err)
	}
	if _, err = base.GetEntity(ctx, created.EntityId, "main"); err != nil {
		t.Fatalf("partial commit did not rehydrate main before merge failure: %v", err)
	}
	state, err := srv.txManager.Lookup(begin.TransactionId)
	if err != nil || !state.MainRehydrated {
		t.Fatalf("partial commit state not recorded: state=%+v error=%v", state, err)
	}
	_, err = srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("RollbackTransaction: %v", err)
	}
	if _, err = base.GetEntity(ctx, created.EntityId, "main"); !errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("transaction entity remained visible after rollback: %v", err)
	}
}

// TestCommitTransaction_MergeDivergedIsInternal asserts the SPEC R2 error-table
// row "Commit merge failed (post-re-hydration) → INTERNAL". When Commit's
// FastForwardMerge surfaces gitstore.ErrMergeDiverged, the handler must map it
// to INTERNAL — not the distinct "Refresh conflict → ABORTED" code.
func TestCommitTransaction_MergeDivergedIsInternal(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	divergingGit := &mergeDivergedGitStore{GitStore: gs, diverged: true}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, divergingGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "item"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected merge-diverged commit to map to INTERNAL, got %v (%v)", status.Code(err), err)
	}
	if !strings.Contains(err.Error(), "merge") {
		t.Fatalf("expected commit-merge error message, got %q", err.Error())
	}
}

// pushTrackingGitStore wraps a gitstore to observe the background sync worker's
// fetch→push cycle (FetchAndMerge → PushRemote) and inject failures into it.
// The cycle runs synchronously inside the worker (SetPushNeeded then
// runSyncCycle, driven directly in tests), so the fetch/push counters are fully
// observable once the cycle returns. Only the sync worker invokes
// FetchAndMerge/PushRemote, so the counters cannot be polluted by the commit
// flow itself.
type pushTrackingGitStore struct {
	gitstore.GitStore
	mu         sync.Mutex
	fetchCalls int
	pushCalls  int
	fetchErr   error
	pushErr    error
}

func (s *pushTrackingGitStore) FetchAndMerge(ctx context.Context, remote, branch string) (plumbing.Hash, error) {
	s.mu.Lock()
	s.fetchCalls++
	err := s.fetchErr
	s.mu.Unlock()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return plumbing.ZeroHash, nil
}

func (s *pushTrackingGitStore) PushRemote(ctx context.Context) error {
	s.mu.Lock()
	s.pushCalls++
	err := s.pushErr
	s.mu.Unlock()
	return err
}

// TestSyncWorkerFetchAndPushCycle exercises the background sync worker's
// fetch→push cycle (the SPEC R10 commit-push surface under the sync-worker
// model): with a remote configured and the push flag set by CommitTransaction,
// one cycle performs FetchAndMerge then PushRemote, clears the flag, and emits
// no push_failed telemetry.
func TestSyncWorkerFetchAndPushCycle(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	pushGit := &pushTrackingGitStore{GitStore: gs}
	mockPub := &mockTelemetryPublisher{}
	sw := NewSyncWorker("https://example.com/repo.git", pushGit, base, RealClock{}, SyncWorkerWithAuditPublisher(mockPub))
	sw.SetPushNeeded()
	sw.runSyncCycle()
	if sw.pushNeeded() {
		t.Fatal("push flag not cleared after successful push")
	}
	pushGit.mu.Lock()
	fetchCalls, pushCalls := pushGit.fetchCalls, pushGit.pushCalls
	pushGit.mu.Unlock()
	if fetchCalls != 1 {
		t.Fatalf("expected 1 FetchAndMerge call, got %d", fetchCalls)
	}
	if pushCalls != 1 {
		t.Fatalf("expected 1 PushRemote call, got %d", pushCalls)
	}
	for _, e := range mockPub.Events() {
		if e.Event != nil && e.Event.EventType == syncFailureEventType {
			t.Fatal("push_failed telemetry emitted on successful push")
		}
	}
}

// rehydrateTrackingStore wraps a store.Store to count RehydrateMainFromFiles
// invocations, pinning the SPEC R10 re-hydration condition ("if new data was
// pulled re-hydrates main.lbug").
type rehydrateTrackingStore struct {
	store.Store
	mu    sync.Mutex
	calls int
}

func (r *rehydrateTrackingStore) RehydrateMainFromFiles(ctx context.Context, entitiesDir, edgesDir string) error {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return r.Store.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
}

func (r *rehydrateTrackingStore) hydrateCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// flakyRehydrateStore wraps a store.Store whose RehydrateMainFromFiles fails
// for the first failAt calls, pinning the next-cycle re-hydration retry
// contract: a failed post-fetch re-hydration must be retried by the next sync
// cycle.
type flakyRehydrateStore struct {
	store.Store
	mu     sync.Mutex
	calls  int
	failAt int // the first failAt calls fail
}

func (f *flakyRehydrateStore) RehydrateMainFromFiles(ctx context.Context, entitiesDir, edgesDir string) error {
	f.mu.Lock()
	f.calls++
	fail := f.calls <= f.failAt
	f.mu.Unlock()
	if fail {
		return errors.New("disk full")
	}
	return f.Store.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
}

func (f *flakyRehydrateStore) rehydrateCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestSyncWorker_RehydrateOnlyWhenNewDataPulled pins the SPEC R10 re-hydration
// condition ("if new data was pulled re-hydrates main.lbug"):
// an up-to-date fetch (unchanged HEAD) must not re-hydrate, while a fetch that
// advances HEAD must.
func TestSyncWorker_RehydrateOnlyWhenNewDataPulled(t *testing.T) {
	t.Run("unchanged HEAD does not re-hydrate", func(t *testing.T) {
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		base, err := openTestStore(t)
		if err != nil {
			t.Fatalf("openTestStore: %v", err)
		}
		t.Cleanup(func() { _ = base.Close() })
		syncGit := &syncMockGitStore{GitStore: gs} // ZeroHash = remote up-to-date
		rt := &rehydrateTrackingStore{Store: base}
		sw := NewSyncWorker("https://example.com/repo.git", syncGit, rt, RealClock{})
		sw.SetPushNeeded()
		sw.runSyncCycle()
		if calls := rt.hydrateCalls(); calls != 0 {
			t.Fatalf("expected no re-hydration on an up-to-date fetch, got %d", calls)
		}
		if syncGit.fetchCalls != 1 {
			t.Fatalf("expected the fetch to run, got %d calls", syncGit.fetchCalls)
		}
	})

	t.Run("changed HEAD re-hydrates", func(t *testing.T) {
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		base, err := openTestStore(t)
		if err != nil {
			t.Fatalf("openTestStore: %v", err)
		}
		t.Cleanup(func() { _ = base.Close() })
		preHead, err := gs.BranchHEAD(context.Background(), "main")
		if err != nil {
			t.Fatalf("BranchHEAD: %v", err)
		}
		// A different valid hash: the new-data signal FetchAndMerge returns
		// when the remote advanced main.
		fetchHash := "1" + preHead[1:]
		if fetchHash == preHead {
			fetchHash = "2" + preHead[1:]
		}
		syncGit := &syncMockGitStore{GitStore: gs, fetchHash: plumbing.NewHash(fetchHash)}
		rt := &rehydrateTrackingStore{Store: base}
		sw := NewSyncWorker("https://example.com/repo.git", syncGit, rt, RealClock{})
		sw.runSyncCycle()
		if calls := rt.hydrateCalls(); calls != 1 {
			t.Fatalf("expected exactly one re-hydration after new data, got %d", calls)
		}
	})
}

// TestSyncWorker_RehydrateRetriesOnNextCycle pins the next-cycle re-hydration
// retry contract: "The next sync cycle retries the re-hydration — the git files
// are already merged, re-hydration is a read from the working tree". A re-hydration
// that fails after a successful fetch (e.g. disk full) is retried by the next
// cycle even though HEAD no longer advances.
func TestSyncWorker_RehydrateRetriesOnNextCycle(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	preHead, err := gs.BranchHEAD(context.Background(), "main")
	if err != nil {
		t.Fatalf("BranchHEAD: %v", err)
	}
	fetchHash := "1" + preHead[1:]
	if fetchHash == preHead {
		fetchHash = "2" + preHead[1:]
	}
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	syncGit := &syncMockGitStore{GitStore: gs, fetchHash: plumbing.NewHash(fetchHash)}
	flaky := &flakyRehydrateStore{Store: base, failAt: 1}
	sw := NewSyncWorker("https://example.com/repo.git", syncGit, flaky, RealClock{})

	// Cycle 1: fetch advances HEAD, re-hydration fails (disk full).
	sw.runSyncCycle()
	if calls := flaky.rehydrateCalls(); calls != 1 {
		t.Fatalf("expected 1 re-hydration attempt in the first cycle, got %d", calls)
	}
	sw.cycleMu.Lock()
	cycleErr := sw.cycleErr
	sw.cycleMu.Unlock()
	if cycleErr == nil {
		t.Fatal("expected the first cycle to surface the re-hydration failure")
	}

	// Cycle 2: the remote is now up-to-date (fetch returns the unchanged HEAD),
	// but the failed re-hydration must still be retried — and succeed once the
	// underlying cause clears.
	syncGit.fetchHash = plumbing.NewHash(preHead) // unchanged local HEAD: new-data signal absent
	sw.runSyncCycle()
	if calls := flaky.rehydrateCalls(); calls != 2 {
		t.Fatalf("expected the next cycle to retry re-hydration, got %d calls", calls)
	}
	sw.cycleMu.Lock()
	cycleErr = sw.cycleErr
	sw.cycleMu.Unlock()
	if cycleErr != nil {
		t.Fatalf("expected the retried re-hydration to succeed, got %v", cycleErr)
	}
}

// TestSyncWorker_RehydrateRestoresMainBeforeReadingTree pins the
// transaction-isolation invariant (SPEC R10 re-hydration: the working tree is
// restored to main before files are read): with
// the working tree checked out on a transaction branch carrying an uncommitted
// entity file, a new-data cycle must restore main (and clean the tree) before
// RehydrateMainFromFiles so the uncommitted file can never be published into
// main.lbug.
func TestSyncWorker_RehydrateRestoresMainBeforeReadingTree(t *testing.T) {
	ctx := context.Background()
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })

	const leakedID = "22222222-2222-4222-8222-222222222222"
	// Simulate an open transaction whose commit is in flight: the working tree
	// is checked out on the transaction branch and WriteEntityFiles has dropped
	// an (uncommitted, unstaged) file there (BeginTransaction →
	// HardResetToBranch; CommitTransaction → Checkout(tx) → WriteEntityFiles).
	if err := gs.WithGitLock(func() error {
		if err := gs.CreateBranch(ctx, testMutationEntityID); err != nil {
			return err
		}
		if err := gs.HardResetToBranch(ctx, testMutationEntityID); err != nil {
			return err
		}
		return gs.WriteEntityFiles(ctx, "Component", []gitstore.Entity{{
			ID: leakedID, Type: "Component", Properties: map[string]string{"name": "uncommitted"},
		}})
	}); err != nil {
		t.Fatalf("set up transaction-branch working tree: %v", err)
	}

	// Sanity: the uncommitted file is present in the working tree on the
	// transaction branch — the leak scenario is real.
	files, err := gs.ReadAllEntityFiles(ctx, "Component")
	if err != nil {
		t.Fatalf("ReadAllEntityFiles: %v", err)
	}
	if len(files) != 1 || files[0].ID != leakedID {
		t.Fatalf("expected the uncommitted entity file on the transaction branch, got %+v", files)
	}

	preHead, err := gs.BranchHEAD(ctx, "main")
	if err != nil {
		t.Fatalf("BranchHEAD: %v", err)
	}
	fetchHash := "1" + preHead[1:]
	if fetchHash == preHead {
		fetchHash = "2" + preHead[1:]
	}
	syncGit := &syncMockGitStore{GitStore: gs, fetchHash: plumbing.NewHash(fetchHash)}
	sw := NewSyncWorker("https://example.com/repo.git", syncGit, base, RealClock{})
	sw.runSyncCycle()

	if _, err := base.GetEntity(ctx, leakedID, ""); err == nil {
		t.Fatal("uncommitted transaction data leaked into main.lbug")
	}
}

// TestSyncWorkerPushFailureLeavesFlagSet exercises the sync worker's push
// failure semantics: when the fetch or the push fails, the push flag stays set
// so the next cycle (or a later commit/Sync/BeginTransaction wake) retries the
// delivery. The failure is logged; the commit itself is not rolled back.
func TestSyncWorkerPushFailureLeavesFlagSet(t *testing.T) {
	cases := []struct {
		name      string
		fetchErr  error
		pushErr   error
		wantFetch int
		wantPush  int
	}{
		{name: "fetch fails", fetchErr: errors.New("simulated fetch failure"), wantFetch: 3, wantPush: 0},
		{name: "push fails", pushErr: errors.New("simulated push rejection"), wantFetch: 1, wantPush: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, err := openTestStore(t)
			if err != nil {
				t.Fatalf("openTestStore: %v", err)
			}
			t.Cleanup(func() { _ = base.Close() })
			gs, err := gitstore.New(t.TempDir())
			if err != nil {
				t.Fatalf("gitstore.New: %v", err)
			}
			pushGit := &pushTrackingGitStore{
				GitStore: gs, fetchErr: tc.fetchErr, pushErr: tc.pushErr,
			}
			sw := NewSyncWorker("https://example.com/repo.git", pushGit, base, RealClock{})
			sw.backoffFn = func(int) time.Duration { return 0 }
			sw.SetPushNeeded()
			sw.runSyncCycle()
			if !sw.pushNeeded() {
				t.Fatal("push flag cleared despite push failure")
			}
			pushGit.mu.Lock()
			fetchCalls, pushCalls := pushGit.fetchCalls, pushGit.pushCalls
			pushGit.mu.Unlock()
			if fetchCalls != tc.wantFetch {
				t.Fatalf("expected %d FetchAndMerge calls, got %d", tc.wantFetch, fetchCalls)
			}
			if pushCalls != tc.wantPush {
				t.Fatalf("expected %d PushRemote calls, got %d", tc.wantPush, pushCalls)
			}
		})
	}
}

// TestSyncWorker_FailureEmitsTelemetry pins the SPEC R10 telemetry
// contract ("log loudly + telemetry"): every permanent sync failure — a
// non-recoverable error or retries exhausted, for fetch or push — emits
// exactly one "cartographer.push_failed" Event Bus event.
func TestSyncWorker_FailureEmitsTelemetry(t *testing.T) {
	cases := []struct {
		name      string
		fetchErr  error
		pushErr   error
		operation string
	}{
		{name: "fetch non-recoverable", fetchErr: gitstore.ErrAuthFailed, operation: "fetch"},
		{name: "fetch retries exhausted", fetchErr: gitstore.ErrRemoteUnreachable, operation: "fetch"},
		{name: "push non-recoverable", pushErr: gitstore.ErrAuthFailed, operation: "push"},
		{name: "push retries exhausted", pushErr: gitstore.ErrRemoteUnreachable, operation: "push"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gs, err := gitstore.New(t.TempDir())
			if err != nil {
				t.Fatalf("gitstore.New: %v", err)
			}
			base, err := openTestStore(t)
			if err != nil {
				t.Fatalf("openTestStore: %v", err)
			}
			t.Cleanup(func() { _ = base.Close() })
			syncGit := &syncMockGitStore{GitStore: gs, fetchErr: tc.fetchErr, pushErr: tc.pushErr}
			mockPub := &mockTelemetryPublisher{}
			sw := NewSyncWorker("https://example.com/repo.git", syncGit, base, RealClock{},
				SyncWorkerWithAuditPublisher(mockPub), SyncWorkerWithPodNamespace("test-ns"))
			sw.backoffFn = func(int) time.Duration { return 0 }
			sw.SetPushNeeded()
			sw.runSyncCycle()

			events := mockPub.Events()
			if len(events) != 1 {
				t.Fatalf("expected exactly 1 telemetry event, got %d", len(events))
			}
			if events[0].Event == nil || events[0].Event.EventType != syncFailureEventType {
				t.Fatalf("expected a %s event, got %+v", syncFailureEventType, events[0])
			}
			if events[0].Event.FlowNamespace != "test-ns" {
				t.Fatalf("expected FlowNamespace %q, got %q", "test-ns", events[0].Event.FlowNamespace)
			}
			if got := events[0].Event.Attributes["operation"]; got != tc.operation {
				t.Fatalf("expected operation %q, got %q", tc.operation, got)
			}
			if events[0].Event.Attributes["error"] == "" {
				t.Fatal("expected the failure error in the telemetry attributes")
			}
		})
	}
}

// TestSyncWorker_NoRemote_ClassifiedNonRecoverable pins the SPEC error-table
// row "Remote not configured" (FAILED_PRECONDITION) at the worker layer: when
// the gitstore has no remote — FetchAndMerge returns ErrNoRemote, the
// production misconfiguration of a non-empty REMOTE_URL whose SetRemote was
// rejected non-fatally at startup with pullOnInit=false (cmd/main.go) — a
// woken or timer cycle must not silently succeed. It returns ErrNoRemote
// classified non-recoverable and emits a telemetry event, so Sync() surfaces
// FAILED_PRECONDITION instead of reporting success without ever running a
// fetch (SPEC R10 "one full cycle" promise, SPEC:992).
func TestSyncWorker_NoRemote_ClassifiedNonRecoverable(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrNoRemote}
	mockPub := &mockTelemetryPublisher{}
	sw := NewSyncWorker("https://example.com/repo.git", syncGit, base, RealClock{},
		SyncWorkerWithAuditPublisher(mockPub), SyncWorkerWithPodNamespace("test-ns"))
	sw.backoffFn = func(int) time.Duration { return 0 }
	sw.SetPushNeeded()

	res := sw.doSyncCycle()
	if res.err == nil {
		t.Fatal("expected the no-remote cycle to fail, not silently succeed")
	}
	if !errors.Is(res.err, gitstore.ErrNoRemote) {
		t.Fatalf("expected ErrNoRemote as the cycle error, got %v", res.err)
	}
	if res.classification != syncNonRecoverable {
		t.Fatalf("expected the no-remote cycle classified non-recoverable, got %v", res.classification)
	}
	events := mockPub.Events()
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 telemetry event, got %d", len(events))
	}
	if events[0].Event == nil || events[0].Event.EventType != syncFailureEventType {
		t.Fatalf("expected a %s event, got %+v", syncFailureEventType, events[0])
	}
	if got := events[0].Event.Attributes["operation"]; got != "fetch" {
		t.Fatalf("expected operation %q, got %q", "fetch", got)
	}
}

// syncMockGitStore drives the SyncWorker deterministically: WithGitLock runs
// fn inline, FetchAndMerge/PushRemote are programmable, and an optional push
// gate parks a push attempt until the test releases it (for blocking/ack
// tests). An operation-order log lets tests assert sync-before-branch
// ordering for BeginTransaction's implicit sync.
type syncMockGitStore struct {
	gitstore.GitStore
	mu          sync.Mutex
	fetchCalls  int
	pushCalls   int
	fetchErr    error
	pushErr     error
	fetchHash   plumbing.Hash // returned on a successful fetch; ZeroHash = up-to-date
	order       []string
	pushEntered chan struct{} // closed when a gated push begins
	pushRelease chan struct{} // closing it unblocks the gated push
}

func (s *syncMockGitStore) WithGitLock(fn func() error) error { return fn() }

func (s *syncMockGitStore) FetchAndMerge(ctx context.Context, remote, branch string) (plumbing.Hash, error) {
	s.mu.Lock()
	s.fetchCalls++
	s.order = append(s.order, "fetch")
	err := s.fetchErr
	hash := s.fetchHash
	s.mu.Unlock()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return hash, nil
}

func (s *syncMockGitStore) RestoreMain(ctx context.Context) error {
	s.mu.Lock()
	s.order = append(s.order, "restore")
	s.mu.Unlock()
	return s.GitStore.RestoreMain(ctx)
}

func (s *syncMockGitStore) PushRemote(ctx context.Context) error {
	s.mu.Lock()
	s.pushCalls++
	err := s.pushErr
	entered, release := s.pushEntered, s.pushRelease
	s.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if release != nil {
		<-release
	}
	return err
}

func (s *syncMockGitStore) CreateBranch(ctx context.Context, txID string) error {
	s.mu.Lock()
	s.order = append(s.order, "branch")
	s.mu.Unlock()
	return s.GitStore.CreateBranch(ctx, txID)
}

// releasePush unblocks a gated push. Idempotent and safe to call after the
// push already consumed (or nil'd) the gate.
func (s *syncMockGitStore) releasePush() {
	s.mu.Lock()
	rel := s.pushRelease
	s.pushRelease = nil
	s.mu.Unlock()
	if rel != nil {
		close(rel)
	}
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

// TestSyncWorker_PushSucceedsOnNextCycle exercises the timer-driven cycle: a
// push flagged after the startup cycle is delivered on the next ticker tick,
// and the flag is cleared once the push succeeds.
func TestSyncWorker_PushSucceedsOnNextCycle(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs}
	fc := newFakeClock(time.Now())
	sw := newSyncWorker(t, syncGit, fc)
	go sw.Run()
	t.Cleanup(sw.Stop)

	// Wait for the startup cycle so the ticker exists and the worker is parked
	// in select.
	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	sw.SetPushNeeded()
	if !sw.pushNeeded() {
		t.Fatal("push flag not set after SetPushNeeded")
	}

	// Fire the ticker -> the next cycle performs fetch + push and clears the
	// flag.
	fc.FireTicker()
	waitFor(t, func() bool {
		syncGit.mu.Lock()
		defer syncGit.mu.Unlock()
		return syncGit.pushCalls >= 1
	}, "push on ticker cycle")

	if sw.pushNeeded() {
		t.Fatal("push flag not cleared after successful push")
	}
	syncGit.mu.Lock()
	fetchCalls, pushCalls := syncGit.fetchCalls, syncGit.pushCalls
	syncGit.mu.Unlock()
	if fetchCalls != 2 {
		t.Fatalf("expected 2 fetch cycles (startup + ticker), got %d", fetchCalls)
	}
	if pushCalls != 1 {
		t.Fatalf("expected 1 push, got %d", pushCalls)
	}
}

// TestSyncWorker_WithAck_BlocksUntilPush exercises the WithAck contract:
// WakeAndWait blocks until the woken cycle completes (a push parked in the
// gitstore gate), returns without error once the push succeeds, and the push
// flag is cleared.
func TestSyncWorker_WithAck_BlocksUntilPush(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{
		GitStore:    gs,
		pushEntered: make(chan struct{}),
		pushRelease: make(chan struct{}),
	}
	fc := newFakeClock(time.Now())
	sw := newSyncWorker(t, syncGit, fc)
	go sw.Run()
	t.Cleanup(sw.Stop)
	// Release any pending push before Stop's final cycle can run.
	t.Cleanup(syncGit.releasePush)

	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	sw.SetPushNeeded()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wakeDone := make(chan error, 1)
	go func() { wakeDone <- sw.WakeAndWait(ctx) }()

	// The woken cycle parks in the push gate: WakeAndWait must still be
	// blocked until the push completes.
	select {
	case <-syncGit.pushEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("push never started")
	}
	select {
	case err := <-wakeDone:
		t.Fatalf("WakeAndWait returned before the push completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	syncGit.releasePush()
	if err := <-wakeDone; err != nil {
		t.Fatalf("WakeAndWait returned error: %v", err)
	}
	if sw.pushNeeded() {
		t.Fatal("push flag not cleared after successful push")
	}
}

// ctxWithTimeout returns a context that times out after 10 seconds; a waiter
// blocked by a bug then surfaces a test failure instead of hanging the suite.
func ctxWithTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestSyncWorker_WithAck_ConcurrentWaitersBothComplete pins per-waiter
// completion-channel ownership (SPEC R10 WithAck): two concurrent
// WakeAndWait callers each own their channel — the second must not overwrite
// the first. The first waiter is satisfied by the cycle that delivers the push;
// the second, registered while that cycle is in flight, is satisfied by the
// follow-up cycle the buffered wakeCh triggers.
func TestSyncWorker_WithAck_ConcurrentWaitersBothComplete(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{
		GitStore:    gs,
		pushEntered: make(chan struct{}),
		pushRelease: make(chan struct{}),
	}
	fc := newFakeClock(time.Now())
	sw := newSyncWorker(t, syncGit, fc)
	go sw.Run()
	t.Cleanup(sw.Stop)
	t.Cleanup(syncGit.releasePush)

	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	sw.SetPushNeeded()
	wakeDone := make([]chan error, 2)
	for i := range wakeDone {
		wakeDone[i] = make(chan error, 1)
	}
	// First waiter wakes the worker; the cycle parks in the push gate.
	go func() { wakeDone[0] <- sw.WakeAndWait(ctxWithTimeout(t)) }()
	select {
	case <-syncGit.pushEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("push never started")
	}
	// Second waiter registers while the first cycle is in flight.
	go func() { wakeDone[1] <- sw.WakeAndWait(ctxWithTimeout(t)) }()
	for i := range wakeDone {
		select {
		case err := <-wakeDone[i]:
			t.Fatalf("waiter %d returned before the push completed: %v", i, err)
		case <-time.After(100 * time.Millisecond):
		}
	}

	syncGit.releasePush()
	for i := range wakeDone {
		if err := <-wakeDone[i]; err != nil {
			t.Fatalf("waiter %d returned error: %v", i, err)
		}
	}
	if sw.pushNeeded() {
		t.Fatal("push flag not cleared after successful push")
	}
}

// TestSyncWorker_WithAck_TimerCycleInFlightDoesNotSatisfyFreshWaiter pins the
// WithAck/timer-race edge case: a WithAck waiter registered while a
// timer-driven cycle is in flight must not be satisfied by that cycle (whose
// waiter snapshot predates the registration). The waiter stays blocked until a
// push is actually delivered; the follow-up cycle then unblocks it.
func TestSyncWorker_WithAck_TimerCycleInFlightDoesNotSatisfyFreshWaiter(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{
		GitStore:    gs,
		pushEntered: make(chan struct{}),
		pushRelease: make(chan struct{}),
	}
	fc := newFakeClock(time.Now())
	sw := newSyncWorker(t, syncGit, fc)
	go sw.Run()
	t.Cleanup(sw.Stop)
	t.Cleanup(syncGit.releasePush)

	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	// Flag the push and fire the timer: the timer-driven cycle parks in the
	// push gate.
	sw.SetPushNeeded()
	fc.FireTicker()
	select {
	case <-syncGit.pushEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("timer cycle never reached the push")
	}

	// A WithAck waiter registered while the timer cycle is in flight.
	wakeDone := make(chan error, 1)
	go func() { wakeDone <- sw.WakeAndWait(ctxWithTimeout(t)) }()

	// The waiter must not be satisfied by the in-flight cycle: it stays
	// blocked while that cycle is still parked at the push gate.
	select {
	case err := <-wakeDone:
		t.Fatalf("WakeAndWait returned while the in-flight cycle was still parked: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	// Deliver the push: the in-flight cycle completes it, and the follow-up
	// cycle (woken by the buffered wakeCh) satisfies the waiter.
	syncGit.releasePush()
	if err := <-wakeDone; err != nil {
		t.Fatalf("WakeAndWait returned error: %v", err)
	}
	if sw.pushNeeded() {
		t.Fatal("push flag not cleared after the push was delivered")
	}
}

// TestSyncWorker_RecoverableError_RetriesAndGivesUp exercises the recoverable
// fetch path: an ErrRemoteUnreachable is retried within the cycle (3 total
// attempts with backoff), and when all attempts fail the error is recorded
// for the next WakeAndWait caller while the push flag stays set.
//
//nolint:dupl // Recoverable/non-recoverable worker-failure tests share structure; the error classes under test differ.
func TestSyncWorker_RecoverableError_RetriesAndGivesUp(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrRemoteUnreachable}
	sw := newSyncWorker(t, syncGit, RealClock{})
	sw.backoffFn = func(int) time.Duration { return 0 }
	sw.SetPushNeeded()

	sw.runSyncCycle()

	syncGit.mu.Lock()
	fetchCalls, pushCalls := syncGit.fetchCalls, syncGit.pushCalls
	syncGit.mu.Unlock()
	if fetchCalls != 3 {
		t.Fatalf("expected 3 fetch attempts (1 + 2 retries), got %d", fetchCalls)
	}
	if pushCalls != 0 {
		t.Fatalf("expected no push attempts after fetch failure, got %d", pushCalls)
	}
	if !sw.pushNeeded() {
		t.Fatal("push flag cleared despite recoverable fetch failure")
	}
	sw.cycleMu.Lock()
	cycleErr := sw.cycleErr
	sw.cycleMu.Unlock()
	if !errors.Is(cycleErr, gitstore.ErrRemoteUnreachable) {
		t.Fatalf("expected propagated ErrRemoteUnreachable, got %v", cycleErr)
	}
}

// TestSyncWorker_NonRecoverableError_LogsAndLeavesFlag exercises the
// non-recoverable path: an ErrAuthFailed fails the cycle immediately (no
// retries), the error is recorded for waiting callers, and the push flag
// stays set for the next cycle.
//
//nolint:dupl // Recoverable/non-recoverable worker-failure tests share structure; the error classes under test differ.
func TestSyncWorker_NonRecoverableError_LogsAndLeavesFlag(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrAuthFailed}
	sw := newSyncWorker(t, syncGit, RealClock{})
	sw.SetPushNeeded()

	sw.runSyncCycle()

	syncGit.mu.Lock()
	fetchCalls, pushCalls := syncGit.fetchCalls, syncGit.pushCalls
	syncGit.mu.Unlock()
	if fetchCalls != 1 {
		t.Fatalf("expected a single fetch attempt (no retries for non-recoverable), got %d", fetchCalls)
	}
	if pushCalls != 0 {
		t.Fatalf("expected no push attempts after fetch failure, got %d", pushCalls)
	}
	if !sw.pushNeeded() {
		t.Fatal("push flag cleared despite non-recoverable failure")
	}
	sw.cycleMu.Lock()
	cycleErr := sw.cycleErr
	sw.cycleMu.Unlock()
	if !errors.Is(cycleErr, gitstore.ErrAuthFailed) {
		t.Fatalf("expected propagated ErrAuthFailed, got %v", cycleErr)
	}
}

// TestSyncWorker_StartupCatchUpPush exercises the startup catch-up contract:
// a push flag left set from a prior run (committed-but-unpushed data) is
// delivered by the worker's initial cycle, without waiting for the first
// ticker tick.
func TestSyncWorker_StartupCatchUpPush(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs}
	sw := newSyncWorker(t, syncGit, newFakeClock(time.Now()))
	sw.SetPushNeeded() // simulate uncommitted push from a prior pod lifetime

	go sw.Run()
	t.Cleanup(sw.Stop)

	waitFor(t, func() bool {
		syncGit.mu.Lock()
		defer syncGit.mu.Unlock()
		return syncGit.pushCalls >= 1
	}, "startup catch-up push")

	if sw.pushNeeded() {
		t.Fatal("push flag not cleared after startup catch-up push")
	}
	syncGit.mu.Lock()
	fetchCalls := syncGit.fetchCalls
	syncGit.mu.Unlock()
	if fetchCalls != 1 {
		t.Fatalf("expected a single startup fetch cycle, got %d", fetchCalls)
	}
}

// TestBeginTransaction_ImplicitSync verifies BeginTransaction's implicit sync
// contract: when a remote is configured, waking the sync worker and waiting
// for its cycle precedes branch creation, so the branch starts from the
// latest remote state.
func TestBeginTransaction_ImplicitSync(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs}
	srv, fc := newSyncServer(t, syncGit)

	// Wait for the startup cycle so the implicit-sync wake is consumed by a
	// fresh cycle that completes before branch creation.
	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	ctx := testCtx()
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if begin.TransactionId == "" {
		t.Fatal("expected a transaction ID")
	}
	if _, unlock, err := srv.txManager.LockActive(begin.TransactionId); err != nil {
		t.Fatalf("transaction not registered: %v", err)
	} else {
		unlock()
	}

	// The implicit-sync fetch must have happened immediately before the branch
	// creation (WakeAndWait completed before branch setup began).
	syncGit.mu.Lock()
	order := append([]string(nil), syncGit.order...)
	syncGit.mu.Unlock()
	if len(order) < 2 || order[len(order)-2] != "fetch" || order[len(order)-1] != "branch" {
		t.Fatalf("expected implicit sync (fetch) immediately before branch creation, got order %v", order)
	}
}

// TestBeginTransaction_ImplicitSyncFailsProceeds verifies that implicit sync
// errors are non-blocking: when the sync cycle fails with a non-recoverable
// error, BeginTransaction still creates the transaction from the current
// local state.
func TestBeginTransaction_ImplicitSyncFailsProceeds(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrAuthFailed}
	srv, fc := newSyncServer(t, syncGit)

	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	ctx := testCtx()
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction must succeed despite implicit-sync failure: %v", err)
	}
	if begin.TransactionId == "" {
		t.Fatal("expected a transaction ID")
	}
	if _, unlock, err := srv.txManager.LockActive(begin.TransactionId); err != nil {
		t.Fatalf("transaction not registered: %v", err)
	} else {
		unlock()
	}
	syncGit.mu.Lock()
	fetchCalls := syncGit.fetchCalls
	syncGit.mu.Unlock()
	if fetchCalls < 2 {
		t.Fatalf("expected the implicit-sync cycle to attempt a fetch, got %d", fetchCalls)
	}
}

// TestBeginTransaction_NoRemote_StillCreatesTransaction pins the SPEC R10
// BeginTransaction implicit-sync contract for the "Remote not configured"
// case: when the gitstore has no remote (a REMOTE_URL whose SetRemote was
// rejected non-fatally at startup with pullOnInit=false, cmd/main.go), the
// implicit-sync cycle fails with ErrNoRemote but the transaction is still
// created from the current local state — sync errors are non-blocking
// (SPEC:624-626 "If the cycle fails, the transaction is still created with the
// current local state").
func TestBeginTransaction_NoRemote_StillCreatesTransaction(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrNoRemote}
	srv, fc := newSyncServer(t, syncGit)

	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	begin, err := srv.BeginTransaction(testCtx(), &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction must succeed despite the no-remote implicit-sync failure: %v", err)
	}
	if begin.TransactionId == "" {
		t.Fatal("expected a transaction ID")
	}
	if _, unlock, err := srv.txManager.LockActive(begin.TransactionId); err != nil {
		t.Fatalf("transaction not registered: %v", err)
	} else {
		unlock()
	}
	syncGit.mu.Lock()
	fetchCalls := syncGit.fetchCalls
	syncGit.mu.Unlock()
	if fetchCalls < 2 {
		t.Fatalf("expected the implicit-sync cycle to attempt a fetch, got %d", fetchCalls)
	}
}

// TestSync_WakesWorkerAndBlocks verifies the Sync RPC contract: it wakes the
// worker and blocks until the cycle completes, and propagates the cycle's
// non-recoverable errors to the caller.
//
//nolint:gocyclo // One subtest per SPEC Sync error-table row; each is a t.Run branch.
func TestSync_WakesWorkerAndBlocks(t *testing.T) {
	t.Run("blocks until cycle completes", func(t *testing.T) {
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		syncGit := &syncMockGitStore{
			GitStore:    gs,
			pushEntered: make(chan struct{}),
			pushRelease: make(chan struct{}),
		}
		srv, fc := newSyncServer(t, syncGit)
		t.Cleanup(syncGit.releasePush)

		waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")
		srv.syncWorker.SetPushNeeded()

		syncDone := make(chan error, 1)
		go func() { _, err := srv.Sync(testCtx(), &flowv1.SyncRequest{}); syncDone <- err }()

		// The woken cycle parks in the push gate: Sync must still be blocked.
		select {
		case <-syncGit.pushEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("sync cycle never reached the push")
		}
		select {
		case err := <-syncDone:
			t.Fatalf("Sync returned before the cycle completed: %v", err)
		case <-time.After(100 * time.Millisecond):
		}

		syncGit.releasePush()
		if err := <-syncDone; err != nil {
			t.Fatalf("Sync returned error: %v", err)
		}
		if srv.syncWorker.pushNeeded() {
			t.Fatal("push flag not cleared after successful sync")
		}
	})

	t.Run("propagates non-recoverable errors", func(t *testing.T) {
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrAuthFailed}
		srv, fc := newSyncServer(t, syncGit)
		waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

		_, err = srv.Sync(testCtx(), &flowv1.SyncRequest{})
		if err == nil {
			t.Fatal("expected Sync to propagate the worker error")
		}
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("expected Unauthenticated for auth failure, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("propagates divergence as FailedPrecondition", func(t *testing.T) {
		// SPEC error-table row "Sync diverged" (SPEC:967): FetchAndMerge
		// detecting divergence surfaces FAILED_PRECONDITION through Sync().
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrPullDiverged}
		srv, fc := newSyncServer(t, syncGit)
		waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

		_, err = srv.Sync(testCtx(), &flowv1.SyncRequest{})
		if err == nil {
			t.Fatal("expected Sync to propagate the divergence error")
		}
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("expected FailedPrecondition for divergence, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("propagates auth-config-missing as FailedPrecondition", func(t *testing.T) {
		// SPEC error-table row "Remote auth config missing (Sync)" (SPEC:975):
		// gitstore.ErrAuthConfigMissing — the pre-flight guard when the remote
		// demands credentials but no auth provider is configured — is classified
		// non-recoverable by the worker and surfaces FAILED_PRECONDITION through
		// Sync() (classifySyncError → mapGitError, errors.go:168).
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrAuthConfigMissing}
		srv, fc := newSyncServer(t, syncGit)
		waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

		_, err = srv.Sync(testCtx(), &flowv1.SyncRequest{})
		if err == nil {
			t.Fatal("expected Sync to propagate the auth-config-missing error")
		}
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("expected FailedPrecondition for missing remote auth config, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("propagates no-remote as FailedPrecondition", func(t *testing.T) {
		// SPEC error-table row "Remote not configured" (SPEC:972) beyond the
		// server gate: a server with remoteURL set but a gitstore with no
		// remote — the production misconfiguration of a REMOTE_URL whose
		// SetRemote was rejected non-fatally at startup (pullOnInit=false,
		// cmd/main.go) — must surface FAILED_PRECONDITION through Sync(), not
		// a silent success. ErrNoRemote is classified non-recoverable by the
		// worker (classifySyncError → mapGitError, errors.go:164), so Sync
		// reports the row instead of claiming a full cycle ran (SPEC R10).
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrNoRemote}
		srv, fc := newSyncServer(t, syncGit)
		waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

		_, err = srv.Sync(testCtx(), &flowv1.SyncRequest{})
		if err == nil {
			t.Fatal("expected Sync to propagate the no-remote error")
		}
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("expected FailedPrecondition for a missing gitstore remote, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("propagates unsupported-URL-scheme as InvalidArgument", func(t *testing.T) {
		// SPEC error-table row "Unsupported remote URL scheme" (SPEC:984): a
		// scheme that is not https:// or ssh:// is a permanent pre-flight
		// config error — "the git operation cannot be attempted at all" — so
		// it is classified non-recoverable and surfaces INVALID_ARGUMENT
		// through Sync() (classifySyncError → mapGitError, errors.go:170).
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrUnsupportedURLScheme}
		srv, fc := newSyncServer(t, syncGit)
		waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

		_, err = srv.Sync(testCtx(), &flowv1.SyncRequest{})
		if err == nil {
			t.Fatal("expected Sync to propagate the unsupported-URL-scheme error")
		}
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument for unsupported remote URL scheme, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("propagates push rejection as FailedPrecondition", func(t *testing.T) {
		// SPEC R10 error classification ("non-fast-forward push rejection" is
		// non-recoverable, SPEC:610): a rejected push is classified
		// non-recoverable by the worker and surfaces FAILED_PRECONDITION
		// through Sync() (classifySyncError → mapGitError, errors.go:174).
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		syncGit := &syncMockGitStore{GitStore: gs, pushErr: gitstore.ErrPushRejected}
		srv, fc := newSyncServer(t, syncGit)
		waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")
		srv.syncWorker.SetPushNeeded()

		_, err = srv.Sync(testCtx(), &flowv1.SyncRequest{})
		if err == nil {
			t.Fatal("expected Sync to propagate the push-rejection error")
		}
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("expected FailedPrecondition for push rejection, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("propagates re-hydration failure as Internal", func(t *testing.T) {
		// SPEC error-table row "Sync re-hydration failed" (SPEC:973): a fetch
		// that advances main whose RehydrateMainFromFiles then fails (e.g. disk
		// full) is a non-recoverable hydrateError in the worker and surfaces
		// INTERNAL through Sync() (sync_worker.go fetchAttempt → mapGitError
		// default branch).
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		preHead, err := gs.BranchHEAD(context.Background(), "main")
		if err != nil {
			t.Fatalf("BranchHEAD: %v", err)
		}
		fetchHash := "1" + preHead[1:]
		if fetchHash == preHead {
			fetchHash = "2" + preHead[1:]
		}
		base, err := openTestStore(t)
		if err != nil {
			t.Fatalf("openTestStore: %v", err)
		}
		t.Cleanup(func() { _ = base.Close() })
		flaky := &flakyRehydrateStore{Store: base, failAt: 100000}
		syncGit := &syncMockGitStore{GitStore: gs, fetchHash: plumbing.NewHash(fetchHash)}
		fc := newFakeClock(time.Now())
		sw := NewSyncWorker("https://example.com/repo.git", syncGit, flaky, fc)
		go sw.Run()
		t.Cleanup(sw.Stop)
		opPub, _ := generateTestKey()
		srv := NewCartographerServer(flaky, syncGit, opPub, initTestKey(), nil, "https://example.com/repo.git",
			30*time.Second, "test-ns", 30*time.Minute, 100000, WithSyncWorker(sw))
		srv.MarkDBReady()
		waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

		_, err = srv.Sync(testCtx(), &flowv1.SyncRequest{})
		if err == nil {
			t.Fatal("expected Sync to propagate the re-hydration failure")
		}
		if status.Code(err) != codes.Internal {
			t.Fatalf("expected Internal for re-hydration failure, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("recoverable-exhausted does not surface", func(t *testing.T) {
		// SPEC R10 Sync: "If the cycle encounters a non-recoverable error,
		// returns the worker's last error" — a recoverable-exhausted cycle
		// (all retries failed) is logged + telemetry in the worker and must
		// not surface as an RPC error.
		gs, err := gitstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("gitstore.New: %v", err)
		}
		syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrRemoteUnreachable}
		base, err := openTestStore(t)
		if err != nil {
			t.Fatalf("openTestStore: %v", err)
		}
		t.Cleanup(func() { _ = base.Close() })
		fc := newFakeClock(time.Now())
		sw := NewSyncWorker("https://example.com/repo.git", syncGit, base, fc)
		sw.backoffFn = func(int) time.Duration { return 0 }
		go sw.Run()
		t.Cleanup(sw.Stop)
		opPub, _ := generateTestKey()
		srv := NewCartographerServer(base, syncGit, opPub, initTestKey(), nil, "https://example.com/repo.git",
			30*time.Second, "test-ns", 30*time.Minute, 100000, WithSyncWorker(sw))
		srv.MarkDBReady()
		waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

		if _, err := srv.Sync(testCtx(), &flowv1.SyncRequest{}); err != nil {
			t.Fatalf("Sync must not surface a recoverable-exhausted cycle error: %v", err)
		}
	})
}

// TestSync_WakesWorkerAndReturnsSuccess verifies the up-to-date Sync path:
// when the remote is up-to-date (fetch succeeds, no push needed), Sync wakes
// the worker, waits for the cycle, and returns nil.
func TestSync_WakesWorkerAndReturnsSuccess(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs}
	srv, fc := newSyncServer(t, syncGit)
	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	_, err = srv.Sync(testCtx(), &flowv1.SyncRequest{})
	if err != nil {
		t.Fatalf("Sync on an up-to-date remote should succeed: %v", err)
	}
	syncGit.mu.Lock()
	fetchCalls, pushCalls := syncGit.fetchCalls, syncGit.pushCalls
	syncGit.mu.Unlock()
	if fetchCalls != 2 {
		t.Fatalf("expected startup cycle + sync cycle fetches, got %d", fetchCalls)
	}
	if pushCalls != 0 {
		t.Fatalf("expected no push without a push flag, got %d", pushCalls)
	}
}

// TestSync_RemoteNotConfigured covers the SPEC error-table row "Remote not
// configured" (SPEC:972): Sync() on a server with no remote configured must
// return FAILED_PRECONDITION. Every other Sync test uses newSyncServer, which
// always configures a remote, so this branch is otherwise uncovered.
func TestSync_RemoteNotConfigured(t *testing.T) {
	srv, _ := newTestServer(t) // remoteURL == ""

	_, err := srv.Sync(testCtx(), &flowv1.SyncRequest{})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v (%v)", status.Code(err), err)
	}
}

// TestSync_MissingWriteCapability covers SPEC R10's Sync() capability
// requirement (WRITE:graph/entity/*): a context lacking it must return
// PERMISSION_DENIED before the sync worker is consulted.
func TestSync_MissingWriteCapability(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.remoteURL = "https://example.com/repo.git"
	ctx := narrowCtx("READ:graph/entity/*", "READ:graph/tx") // no WRITE:graph/entity/*

	_, err := srv.Sync(ctx, &flowv1.SyncRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v (%v)", status.Code(err), err)
	}
}

// TestSync_PerTypeWriteCapabilityDenied pins the SPEC R3 negative branch
// (SPEC:243: only WRITE:graph/entity/* "Authorises ... plus Sync()"): a caller
// holding only a per-type WRITE grant (e.g. WRITE:graph/entity/Component) must
// be denied Sync. TestSync_MissingWriteCapability uses a no-WRITE-at-all
// holder; if the wildcard gate regressed to accept per-type grants, only this
// test fails.
func TestSync_PerTypeWriteCapabilityDenied(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.remoteURL = "https://example.com/repo.git"
	ctx := narrowCtx("WRITE:graph/entity/Component") // per-type only, no wildcard

	_, err := srv.Sync(ctx, &flowv1.SyncRequest{})
	if status.Code(err) != codes.PermissionDenied ||
		status.Convert(err).Message() != "capability denied: WRITE:graph/entity/*" {
		t.Fatalf("expected per-type-only PermissionDenied for Sync, got %v", err)
	}
}

// TestCommitTransaction_WithSyncWorker_AckWaitsForPush pins the service-layer
// CommitTransaction sync-worker branch (SPEC R10 commit/WithAck contract,
// SPEC:615-619): with a SyncWorker wired via WithSyncWorker, an acked commit
// sets the push-needed flag and blocks until the sync cycle delivers the push
// (the ack wait resolves only after the push completes), then returns success
// with the flag cleared. The TestSyncWorker_WithAck_* tests drive
// sw.WakeAndWait directly; this test pins the handler wiring
// (SetPushNeeded → req.GetAck() → WakeAndWait) end to end.
func TestCommitTransaction_WithSyncWorker_AckWaitsForPush(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{
		GitStore:    gs,
		pushEntered: make(chan struct{}),
		pushRelease: make(chan struct{}),
	}
	srv, fc := newSyncServer(t, syncGit)
	t.Cleanup(syncGit.releasePush)
	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	ctx := testCtx()
	applyTestSchema(ctx, t, srv.store)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "acked"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	// The acked commit must block until the sync cycle delivers the push: the
	// woken cycle parks in the push gate, and CommitTransaction stays pending.
	commitDone := make(chan error, 1)
	go func() {
		_, err := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
			TransactionId: begin.TransactionId, Ack: true,
		})
		commitDone <- err
	}()
	select {
	case <-syncGit.pushEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("acked commit never reached the sync worker's push")
	}
	select {
	case err := <-commitDone:
		t.Fatalf("CommitTransaction returned before the push was delivered: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	syncGit.releasePush()
	if err := <-commitDone; err != nil {
		t.Fatalf("CommitTransaction with ack returned error after the push was delivered: %v", err)
	}

	// The commit→push-flag contract fired and the ack cycle delivered exactly
	// one push (SetPushNeeded was observed by the worker).
	syncGit.mu.Lock()
	pushCalls := syncGit.pushCalls
	syncGit.mu.Unlock()
	if pushCalls != 1 {
		t.Fatalf("expected exactly 1 push for the acked commit, got %d", pushCalls)
	}
	if srv.syncWorker.pushNeeded() {
		t.Fatal("push flag not cleared after the acked push")
	}
}

// TestCommitTransaction_WithSyncWorker_AckPushFailureSurfacesMappedError pins
// the ack-error mapping branch of the CommitTransaction sync-worker wiring
// (SPEC R10, SPEC:620-621: "If the cycle ends with the flag still set
// (permanent failure, or retries exhausted), the call returns an error with
// the worker's last push error"): a non-recoverable push rejection surfaces
// through mapGitError as FAILED_PRECONDITION ("push rejected
// (non-fast-forward)"), not a raw INTERNAL error, and the push flag stays set
// for the next cycle.
func TestCommitTransaction_WithSyncWorker_AckPushFailureSurfacesMappedError(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs, pushErr: gitstore.ErrPushRejected}
	srv, fc := newSyncServer(t, syncGit)
	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	ctx := testCtx()
	applyTestSchema(ctx, t, srv.store)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "rejected"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId, Ack: true,
	})
	if err == nil {
		t.Fatal("expected the acked commit to surface the rejected push")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition for a rejected push, got %v (%v)", status.Code(err), err)
	}
	if !srv.syncWorker.pushNeeded() {
		t.Fatal("push flag cleared despite the rejected push")
	}
}

// TestCommitTransaction_WithSyncWorker_AckPushRetriesExhaustedSurfacesUnavailable
// pins the SPEC error-table row "Remote unreachable" (UNAVAILABLE) through the
// ack-error mapping branch of the CommitTransaction sync-worker wiring (SPEC
// R10, SPEC:620-621: "If the cycle ends with the flag still set (permanent
// failure, or retries exhausted), the call returns an error with the worker's
// last push error"): a push whose retries exhaust against an unreachable remote
// (DNS failure, connection refused, or transport timeout — ErrRemoteUnreachable
// is classified recoverable and retried within the cycle) surfaces through
// mapGitError as UNAVAILABLE, not a raw INTERNAL error, and the push flag stays
// set for the next cycle.
func TestCommitTransaction_WithSyncWorker_AckPushRetriesExhaustedSurfacesUnavailable(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs, pushErr: gitstore.ErrRemoteUnreachable}
	srv, fc := newSyncServer(t, syncGit)
	srv.syncWorker.backoffFn = func(int) time.Duration { return 0 }
	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	ctx := testCtx()
	applyTestSchema(ctx, t, srv.store)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "unreachable"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId, Ack: true,
	})
	if err == nil {
		t.Fatal("expected the acked commit to surface the exhausted-push error")
	}
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable for an unreachable remote, got %v (%v)", status.Code(err), err)
	}
	if !srv.syncWorker.pushNeeded() {
		t.Fatal("push flag cleared despite the unreachable remote")
	}
}

// TestCommitTransaction_WithSyncWorker_AckCallerDeadlineSurfacesDeadlineExceeded
// pins the caller-deadline branch of the CommitTransaction sync-worker wiring
// (SPEC R10, SPEC:621-622: "A caller that hits the context deadline receives
// DEADLINE_EXCEEDED and the flag stays set"): a caller deadline expires while
// the acked commit waits on the sync cycle, and the
// commit surfaces DEADLINE_EXCEEDED (mapGitError's context-error mapping, not
// a raw INTERNAL), with the push flag left set for the next cycle.
func TestCommitTransaction_WithSyncWorker_AckCallerDeadlineSurfacesDeadlineExceeded(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{
		GitStore:    gs,
		pushEntered: make(chan struct{}),
		pushRelease: make(chan struct{}),
	}
	srv, fc := newSyncServer(t, syncGit)
	t.Cleanup(syncGit.releasePush)
	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	ctx := testCtx()
	applyTestSchema(ctx, t, srv.store)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "slow"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	// The commit's local git work is fast; the caller deadline is long enough
	// for it to finish but expires while the acked commit is blocked in the
	// sync-cycle wait (the push gate keeps the cycle from completing). Derived
	// from ctx so the capability metadata set by testCtx is preserved.
	ackCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	start := time.Now()
	_, err = srv.CommitTransaction(ackCtx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId, Ack: true,
	})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded for an expired ack wait, got %v (%v)", status.Code(err), err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("acked commit took %v to surface the deadline", elapsed)
	}
	if !srv.syncWorker.pushNeeded() {
		t.Fatal("push flag cleared despite the timed-out ack wait")
	}
}

// TestCommitTransaction_WithSyncWorker_NoAckReturnsWithoutBlocking pins the
// SPEC R10 commit branch "commit() returns immediately and sets the
// push-needed flag" (SPEC:614-615 — the non-WithAck path): with a SyncWorker
// wired via WithSyncWorker, a commit whose Ack is unset must set the push flag
// and return without blocking for the sync cycle. Every other
// CommitTransaction-with-sync-worker test uses Ack: true (the acked tests park
// the woken cycle's push in the gate and assert the handler stays blocked); a
// regression that made the handler wake-and-wait unconditionally would trip
// this test's timeout while the cycle parks in the gate below.
func TestCommitTransaction_WithSyncWorker_NoAckReturnsWithoutBlocking(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{
		GitStore:    gs,
		pushEntered: make(chan struct{}),
		pushRelease: make(chan struct{}),
	}
	srv, fc := newSyncServer(t, syncGit)
	t.Cleanup(syncGit.releasePush)
	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	ctx := testCtx()
	applyTestSchema(ctx, t, srv.store)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "no-ack"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	// The non-acked commit must return immediately — SetPushNeeded fires but no
	// WakeAndWait blocks for the cycle.
	commitDone := make(chan error, 1)
	go func() {
		_, err := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
			TransactionId: begin.TransactionId,
		})
		commitDone <- err
	}()
	select {
	case err := <-commitDone:
		if err != nil {
			t.Fatalf("CommitTransaction (no ack): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("non-acked CommitTransaction blocked on the sync worker")
	}

	// No sync cycle ran during the commit, and the push flag is set for the
	// worker to pick up on its next cycle.
	syncGit.mu.Lock()
	pushCalls := syncGit.pushCalls
	syncGit.mu.Unlock()
	if pushCalls != 0 {
		t.Fatalf("non-acked commit ran the sync cycle (%d push attempts)", pushCalls)
	}
	if !srv.syncWorker.pushNeeded() {
		t.Fatal("push flag not set after the non-acked commit")
	}

	// Deliver the flag on the next timer-driven cycle: the push completes and
	// the flag clears, proving the worker attached to the commit is live.
	syncGit.releasePush()
	fc.FireTicker()
	waitFor(t, func() bool {
		syncGit.mu.Lock()
		defer syncGit.mu.Unlock()
		return syncGit.pushCalls >= 1
	}, "push on the next cycle after the non-acked commit")
	if srv.syncWorker.pushNeeded() {
		t.Fatal("push flag not cleared after the next cycle delivered the push")
	}
}

// TestRollbackTransaction_PartialCommitWithoutLadybugPathIsExplicit pins the
// SPEC error-table row "Commit serialisation or re-hydration failed"
// (INTERNAL) for a rollback that must restore the main store after a partial
// commit in a process whose LADYBUG_DB_PATH is unset: the restoration is
// re-hydration work, so the failure surfaces INTERNAL (never a FAILED_PRECONDITION
// no error-table row assigns to this condition), and the failed rollback leaves
// the transaction registered for retry.
func TestRollbackTransaction_PartialCommitWithoutLadybugPathIsExplicit(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.gitstore = &mergeFailingGitStore{GitStore: srv.gitstore, failMerge: true}
	ctx := testCtx()
	applyTestSchema(ctx, t, srv.store)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "partial"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	if err == nil {
		t.Fatal("expected merge failure")
	}
	_, err = srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{TransactionId: begin.TransactionId})
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal without LADYBUG_DB_PATH, got %v", err)
	}
	if _, unlock, err := srv.txManager.LockActive(begin.TransactionId); err != nil {
		t.Fatalf("transaction was deregistered after explicit restoration failure: %v", err)
	} else {
		unlock()
	}
}

func TestRollbackTransaction_WaitsForUnrelatedGitActivity(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	attempting := &gitAttemptStore{GitStore: srv.gitstore}
	srv.gitstore = attempting
	gitHeld := make(chan struct{})
	releaseGit := make(chan struct{})
	unrelatedDone := make(chan error, 1)
	go func() {
		unrelatedDone <- srv.withGitLock(func() error {
			close(gitHeld)
			<-releaseGit
			return nil
		})
	}()
	<-gitHeld
	attempted := make(chan struct{})
	attempting.setAttempted(attempted)
	rollbackDone := make(chan error, 1)
	go func() {
		_, rollbackErr := srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
			TransactionId: begin.TransactionId,
		})
		rollbackDone <- rollbackErr
	}()
	<-attempted
	close(releaseGit)
	if err := <-unrelatedDone; err != nil {
		t.Fatalf("unrelated Git activity: %v", err)
	}
	if err := <-rollbackDone; err != nil {
		t.Fatalf("RollbackTransaction: %v", err)
	}
}

func TestTransaction_ExtendTimeout(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	extendResp, err := srv.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{
		TransactionId: txID,
		Duration:      durationpb.New(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("ExtendTimeout failed: %v", err)
	}
	if extendResp.GetAppliedTimeout() == nil {
		t.Fatal("expected applied_timeout to be set on ExtendTimeout response")
	}
	if extendResp.GetAppliedTimeout().AsDuration() != 10*time.Minute {
		t.Errorf("expected applied timeout 10m, got %v", extendResp.GetAppliedTimeout().AsDuration())
	}
}

func TestTransaction_TimedOut(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	// Replace with fake clock so we can control time.
	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(7*24*time.Hour, 100000)
	srv.txManager.clock = fc

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
	srv, _ := newTestServer(t)
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

// TestTransaction_NonCanonicalTxIDRejected pins the non-canonical-but-parseable
// branch of the SPEC error-table row "Invalid transaction ID format"
// (SPEC:990): spellings that google/uuid parses but that are not the canonical
// RFC4122 §3 lowercase dashed form — uppercase hex, 32-char no-hyphen, braced
// {...}, and urn:uuid: — must be rejected with INVALID_ARGUMENT because IDs are
// persisted verbatim (SPEC:162; uuidutil.Validate's canonical check). The
// existing tests (TestTransaction_InvalidTxID, TestReadPathTransactionID_Rejected,
// TestMutationTransactionID_Rejected) all send "not-a-uuid" and pin only the
// unparseable branch; this test exercises the shared canonical-check branch
// (validateTxID / LockActive → isValidUUID → uuidutil.Validate) from both the
// read path (GetTransactionDiff) and the mutation path (CreateEntity →
// lockTransactionMutation → LockActive).
func TestTransaction_NonCanonicalTxIDRejected(t *testing.T) {
	nonCanonical := []string{
		"550E8400-E29B-41D4-A716-446655440000",          // uppercase hex
		"550e8400e29b41d4a716446655440000",              // no-hyphen 32-char
		"{550e8400-e29b-41d4-a716-446655440000}",        // braced
		"urn:uuid:550e8400-e29b-41d4-a716-446655440000", // urn prefix
	}

	t.Run("read path GetTransactionDiff", func(t *testing.T) {
		srv, _ := newTestServer(t)
		ctx := testCtx()
		for _, txID := range nonCanonical {
			_, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: txID})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument for %q, got %v", txID, status.Code(err))
			}
		}
	})

	t.Run("mutation path CreateEntity", func(t *testing.T) {
		srv, _ := newTestServer(t)
		ctx := testCtx()
		for _, txID := range nonCanonical {
			_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
				EntityType: "Component", Properties: map[string]string{"name": "x"}, TransactionId: txID,
			})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument for %q, got %v", txID, status.Code(err))
			}
		}
	})
}

func TestTransaction_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	_, err := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: "11111111-1111-4111-8111-111111111111",
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
	srv, _ := newTestServer(t)
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

// TestEmptyTransaction_CommitNoOpCreatesNoGitCommitAndNoPush pins the SPEC R10
// zero-mutation branch ("zero-mutation commits produce no git commit and
// therefore no remote push"; SPEC R9 step 5: "Commit() — if zero mutations,
// no-op"): a CommitTransaction with an empty change log must succeed without
// creating a git commit and without setting the sync-worker push-needed flag.
// TestEmptyTransaction_CommitNoOp only asserts that the RPC succeeds; nothing
// pins the git/push side of the no-op.
func TestEmptyTransaction_CommitNoOpCreatesNoGitCommitAndNoPush(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	countingGit := &commitCountingGitStore{GitStore: gs}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, countingGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
	)
	srv.MarkDBReady()
	// Wire a (non-running) SyncWorker so the push flag is observable. No remote
	// URL is configured, so BeginTransaction's implicit sync is skipped.
	sw := NewSyncWorker("", gs, base, RealClock{})
	srv.syncWorker = sw
	ctx := testCtx()

	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CommitTransaction (no-op) failed: %v", err)
	}
	if countingGit.commits != 0 {
		t.Fatalf("zero-mutation commit created %d git commits, want 0", countingGit.commits)
	}
	if sw.pushNeeded() {
		t.Fatal("zero-mutation commit set the sync-worker push-needed flag")
	}
}

func TestEmptyTransaction_RollbackNoOp(t *testing.T) {
	srv, _ := newTestServer(t)
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

func TestEmptyTransaction_CommitWaitsForMutationCompletion(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	blocking := &mutationBlockingStore{Store: base, wrote: make(chan struct{}), release: make(chan struct{})}
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		blocking, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}

	mutationDone := make(chan error, 1)
	go func() {
		_, mutationErr := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
			EntityType: "Component", Properties: map[string]string{"name": "racing"}, TransactionId: begin.TransactionId,
		})
		mutationDone <- mutationErr
	}()
	<-blocking.wrote

	commitAtLifecycleLock := make(chan struct{})
	srv.txManager.beforeLifecycleLock = func(string) { close(commitAtLifecycleLock) }
	commitDone := make(chan error, 1)
	go func() {
		_, commitErr := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
		commitDone <- commitErr
	}()
	<-commitAtLifecycleLock
	srv.txManager.beforeLifecycleLock = nil
	close(blocking.release)
	if err := <-mutationDone; err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if err := <-commitDone; err != nil {
		t.Fatalf("CommitTransaction: %v", err)
	}
	entities, _, err := base.ListEntities(ctx, "Component", 10, "", "main")
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(entities) != 1 {
		t.Fatalf("expected committed mutation on main, got %d entities", len(entities))
	}
}

func TestEmptyTransaction_CommitCleanupFailureRemainsRetryable(t *testing.T) {
	srv, base := newTestServer(t)
	wrapped := &dropFailingStore{Store: base, failDrop: true}
	srv.store = wrapped
	ctx := testCtx()
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}

	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	if err == nil {
		t.Fatal("expected cleanup failure")
	}
	if _, unlock, err := srv.txManager.LockActive(begin.TransactionId); err != nil {
		t.Fatalf("transaction was not retryable after cleanup failure: %v", err)
	} else {
		unlock()
	}
	if err = srv.gitstore.WithGitLock(func() error {
		exists, branchErr := srv.gitstore.BranchExists(ctx, begin.TransactionId)
		if branchErr != nil {
			return branchErr
		}
		if !exists {
			return fmt.Errorf("transaction Git branch was deleted before branch DB cleanup")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("retry CommitTransaction: %v", err)
	}
}

func TestCommitTransaction_FileRehydrationUsesExactlyOnePath(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	counting := &hydrationCountingStore{Store: base}
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		counting, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(t.TempDir()),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "hydrate"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("CommitTransaction: %v", err)
	}
	if counting.fromFiles != 1 || counting.fromBranch != 0 {
		t.Fatalf(
			"expected one file hydration and no branch hydration, got files=%d branch=%d",
			counting.fromFiles, counting.fromBranch,
		)
	}
}

func TestCommitTransaction_RetryAfterCommitCreatedDoesNotDuplicateCommit(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	countingGit := &commitCountingGitStore{GitStore: gs}
	failingStore := &rehydrateFailingStore{Store: base, fail: true}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		failingStore, countingGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "retry"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	// SPEC error table (SPEC:975): "Commit serialisation or re-hydration failed"
	// maps to INTERNAL — the re-hydration failure surfaces via mapGitError's
	// default branch, so the first commit attempt must fail with codes.Internal.
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected commit re-hydration failure to map to INTERNAL, got %v (%v)", status.Code(err), err)
	}
	state, lookupErr := srv.txManager.Lookup(begin.TransactionId)
	if lookupErr != nil || !state.CommitCreated || state.MergeCompleted {
		t.Fatalf("unexpected retry state: state=%+v error=%v", state, lookupErr)
	}
	// A mutation against a transaction whose commit has started is rejected
	// with NOT_FOUND (SPEC error-table row "Transaction not found": "was
	// already committed/rolled back" — the commit-in-progress handle no
	// longer references a usable active transaction from the write surface).
	// FAILED_PRECONDITION would be an un-justified code. RefreshTransaction,
	// by contrast, remains available for a commit-started transaction whose
	// commit has not merged (the SPEC "Commit not up-to-date with main" row
	// prescribes "Call Refresh() before Commit()") — see
	// TestRefreshTransaction_CommitMergeFailedThenMainAdvancedUnwedges.
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "late"}, TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.NotFound {
		t.Fatalf("expected mutation rejection after commit creation, got %v", err)
	}
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("retry CommitTransaction: %v", err)
	}
	if countingGit.commits != 1 {
		t.Fatalf("expected one transaction commit, got %d", countingGit.commits)
	}
}

// TestRefreshTransaction_CommitMergeFailedThenMainAdvancedUnwedges pins the
// SPEC serialisation-flow retry contract for a transaction whose step-6 git
// commit was created but whose step-10 merge failed, after main advanced: the
// retried commit surfaces step 5's FAILED_PRECONDITION ("Commit not up-to-date
// with main", SPEC error table, whose prescription is "Call Refresh() before
// Commit()"), and the prescribed Refresh() → Commit() path must now succeed
// without losing the transaction's changes. Previously RefreshTransaction
// rejected any CommitStarted transaction with NOT_FOUND, so the baseline could
// never advance again and the transaction was permanently wedged — its changes
// recoverable only by Rollback, i.e. loss. The fix keeps the FAILED_PRECONDITION
// guard intact for genuinely conflicting changes: the wedge here is the
// baseline-advance path, not the conflict detection.
func TestRefreshTransaction_CommitMergeFailedThenMainAdvancedUnwedges(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	failingGit := &mergeFailingGitStore{GitStore: gs, failMerge: true}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, failingGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)

	// Transaction A: the step-6 git commit is created, the step-10 merge fails.
	beginA, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction A: %v", err)
	}
	createdA, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "a"}, TransactionId: beginA.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity A: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: beginA.TransactionId,
	}); err == nil {
		t.Fatal("expected commit A merge failure")
	}
	stateA, lookupErr := srv.txManager.Lookup(beginA.TransactionId)
	if lookupErr != nil || !stateA.CommitStarted || !stateA.CommitCreated || stateA.MergeCompleted {
		t.Fatalf("commit A did not retain the created-commit milestone: state=%+v error=%v", stateA, lookupErr)
	}

	// Main advances: transaction B commits on top.
	beginB, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction B: %v", err)
	}
	createdB, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "b"}, TransactionId: beginB.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity B: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: beginB.TransactionId,
	}); err != nil {
		t.Fatalf("CommitTransaction B: %v", err)
	}

	// The retried commit A now hits step 5's divergence check and surfaces the
	// SPEC-prescribed FAILED_PRECONDITION ("Commit not up-to-date with main").
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: beginA.TransactionId,
	}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected retried commit A to surface FAILED_PRECONDITION, got %v (%v)", status.Code(err), err)
	}

	// The SPEC error-table prescription for that condition: Refresh() then
	// Commit(). The refresh must succeed (it resets the branch, discarding the
	// orphaned commit, and re-applies A's change) and clear the commit-in-flight
	// milestones so the retried commit re-enters the serialisation flow.
	if _, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: beginA.TransactionId,
	}); err != nil {
		t.Fatalf("RefreshTransaction after failed merge + main advance: %v", err)
	}
	refreshedA, lookupErr := srv.txManager.Lookup(beginA.TransactionId)
	if lookupErr != nil || refreshedA.CommitStarted || refreshedA.CommitCreated || refreshedA.CommitHydrated {
		t.Fatalf("refresh did not clear commit-in-flight milestones: state=%+v error=%v", refreshedA, lookupErr)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: beginA.TransactionId,
	}); err != nil {
		t.Fatalf("retried CommitTransaction after refresh: %v", err)
	}

	// No data loss: A's entity is committed to main, and B's survives too.
	if _, err = base.GetEntity(ctx, createdA.EntityId, "main"); err != nil {
		t.Fatalf("transaction A entity lost after un-wedged commit: %v", err)
	}
	if _, err = base.GetEntity(ctx, createdB.EntityId, "main"); err != nil {
		t.Fatalf("transaction B entity lost after A's un-wedged commit: %v", err)
	}
}

func TestCommitTransaction_CommitFailureWithoutCommitAllowsRefreshAndRetry(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	failingGit := &commitErrorGitStore{GitStore: gs, failBefore: true}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, failingGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "retry"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.Internal {
		// SPEC error table (SPEC:975): "Commit serialisation or re-hydration
		// failed" maps to INTERNAL — the git Commit failure surfaces via
		// mapGitError's default branch as codes.Internal.
		t.Fatalf("expected commit serialisation failure to map to INTERNAL, got %v (%v)", status.Code(err), err)
	}
	state, lookupErr := srv.txManager.Lookup(begin.TransactionId)
	if lookupErr != nil || state.CommitStarted || state.CommitCreated {
		t.Fatalf("commit failure remained irreversible: state=%+v error=%v", state, lookupErr)
	}
	commitGitEntity(ctx, t, gs, "22222222-2222-4222-8222-222222222222", "concurrent")
	if _, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RefreshTransaction after failed commit: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("retry CommitTransaction: %v", err)
	}
	if failingGit.commits != 2 {
		t.Fatalf("expected two transaction commit attempts, got %d", failingGit.commits)
	}
}

func TestCommitTransaction_ErrorAfterCommitRetainsResumableState(t *testing.T) {
	ladybugPath := t.TempDir()
	base, err := ladybug.Open(ladybugPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	failingGit := &commitErrorGitStore{GitStore: gs, failAfter: true}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, failingGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "resume"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err == nil {
		t.Fatal("expected error after commit creation")
	}
	if err = base.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := ladybug.Open(ladybugPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	countingGit := &commitCountingGitStore{GitStore: reopenedGit}
	restarted := NewCartographerServer(
		reopened, countingGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	restarted.MarkDBReady()
	if err = restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions: %v", err)
	}
	state, lookupErr := restarted.txManager.Lookup(begin.TransactionId)
	if lookupErr != nil || !state.CommitStarted || !state.CommitCreated {
		t.Fatalf("created commit was not retained: state=%+v error=%v", state, lookupErr)
	}
	// A mutation against the recovered mid-commit transaction is rejected
	// with NOT_FOUND (SPEC error-table row "Transaction not found": the
	// commit-in-progress handle no longer references a usable active
	// transaction from the write surface). RefreshTransaction, by contrast,
	// remains available for a commit-started transaction whose commit has not
	// merged (the SPEC "Commit not up-to-date with main" row prescribes
	// "Call Refresh() before Commit()") — see
	// TestRefreshTransaction_CommitMergeFailedThenMainAdvancedUnwedges.
	if _, err = restarted.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "late"},
		TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.NotFound {
		t.Fatalf("expected mutation rejection after commit creation, got %v", err)
	}
	if _, err = restarted.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("retry CommitTransaction: %v", err)
	}
	if failingGit.commits != 1 || countingGit.commits != 0 {
		t.Fatalf("expected no duplicate transaction commit, before restart=%d after restart=%d",
			failingGit.commits, countingGit.commits)
	}
	if err = reopenedGit.WithGitLock(func() error {
		if err := reopenedGit.RestoreMain(ctx); err != nil {
			return err
		}
		logs, logErr := reopenedGit.GitLogOneline(ctx, "transaction:"+begin.TransactionId)
		if logErr != nil {
			return logErr
		}
		if len(logs) != 1 {
			return fmt.Errorf("expected one transaction commit, got %d", len(logs))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackTransaction_AfterReconciledCommitError(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failBefore bool
		failAfter  bool
	}{
		{name: "no commit created", failBefore: true},
		{name: "commit created", failAfter: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, err := openTestStore(t)
			if err != nil {
				t.Fatalf("openTestStore: %v", err)
			}
			t.Cleanup(func() { _ = base.Close() })
			gs, err := gitstore.New(t.TempDir())
			if err != nil {
				t.Fatalf("gitstore.New: %v", err)
			}
			failingGit := &commitErrorGitStore{
				GitStore: gs, failBefore: tc.failBefore, failAfter: tc.failAfter,
			}
			opPub, _ := generateTestKey()
			srv := NewCartographerServer(
				base, failingGit, opPub, initTestKey(), nil, "", 30*time.Second,
				"test-ns", 30*time.Minute, 100000,
			)
			srv.MarkDBReady()
			ctx := testCtx()
			applyTestSchema(ctx, t, base)
			begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
			if err != nil {
				t.Fatalf("BeginTransaction: %v", err)
			}
			if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
				EntityType: "Component", Properties: map[string]string{"name": "rollback"},
				TransactionId: begin.TransactionId,
			}); err != nil {
				t.Fatalf("CreateEntity: %v", err)
			}
			if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
				TransactionId: begin.TransactionId,
			}); err == nil {
				t.Fatal("expected commit error")
			}
			if _, err = srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
				TransactionId: begin.TransactionId,
			}); err != nil {
				t.Fatalf("RollbackTransaction: %v", err)
			}
			if _, err = srv.txManager.Lookup(begin.TransactionId); err == nil {
				t.Fatal("rolled-back transaction remains registered")
			}
		})
	}
}

func TestCommitTransaction_StateWriteFailureRemainsDiscoverableAndRetryable(t *testing.T) {
	for _, tc := range []struct {
		name          string
		fail          func(store.BranchTransactionState) bool
		commitCreated bool
	}{
		{
			name: "before commit",
			fail: func(state store.BranchTransactionState) bool {
				return state.CommitStarted && !state.CommitCreated
			},
		},
		{
			name: "after commit",
			fail: func(state store.BranchTransactionState) bool {
				return state.CommitCreated
			},
			commitCreated: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, err := openTestStore(t)
			if err != nil {
				t.Fatalf("openTestStore: %v", err)
			}
			t.Cleanup(func() { _ = base.Close() })
			failingStore := &transactionStateFailingStore{Store: base, fail: tc.fail}
			ladybugPath := t.TempDir()
			gs, err := gitstore.New(ladybugPath)
			if err != nil {
				t.Fatalf("gitstore.New: %v", err)
			}
			countingGit := &commitCountingGitStore{GitStore: gs}
			opPub, _ := generateTestKey()
			srv := NewCartographerServer(
				failingStore, countingGit, opPub, initTestKey(), nil, "", 30*time.Second,
				"test-ns", 30*time.Minute, 100000, WithLadybugPath(ladybugPath),
			)
			srv.MarkDBReady()
			ctx := testCtx()
			applyTestSchema(ctx, t, base)
			begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
			if err != nil {
				t.Fatalf("BeginTransaction: %v", err)
			}
			if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
				EntityType: "Component", Properties: map[string]string{"name": "retry"},
				TransactionId: begin.TransactionId,
			}); err != nil {
				t.Fatalf("CreateEntity: %v", err)
			}
			if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
				TransactionId: begin.TransactionId,
			}); err == nil || !strings.Contains(err.Error(), "state write failure") {
				t.Fatalf("CommitTransaction error=%v", err)
			}
			state, err := srv.txManager.Lookup(begin.TransactionId)
			if err != nil || state.CommitCreated != tc.commitCreated {
				t.Fatalf("reconciled state=%+v err=%v", state, err)
			}
			_, refreshErr := srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
				TransactionId: begin.TransactionId,
			})
			// A refresh remains available after both failure points — the SPEC
			// error-table row "Commit not up-to-date with main" prescribes
			// "Call Refresh() before Commit()", and a commit whose state write
			// failed never merged (the SPEC "Commit merge failed
			// (post-re-hydration)" row leaves the merge retryable). The
			// refresh re-opens the transaction, so the retried commit re-enters
			// the serialisation flow: a fresh commit is created for the
			// after-commit case, whose orphaned commit the refresh discarded.
			if refreshErr != nil {
				t.Fatalf("refresh after %s failure: %v", tc.name, refreshErr)
			}
			if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
				TransactionId: begin.TransactionId,
			}); err != nil {
				t.Fatalf("retry CommitTransaction: %v", err)
			}
			expectedCommits := 1
			if tc.commitCreated {
				expectedCommits = 2
			}
			if countingGit.commits != expectedCommits {
				t.Fatalf("expected %d Git commits, got %d", expectedCommits, countingGit.commits)
			}
		})
	}
}

func TestRollbackTransaction_AfterRestartDuringMainRehydrationRestoresMain(t *testing.T) {
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()
	base, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	failingGit := &mergeFailingGitStore{GitStore: gs, failMerge: true}
	srv := NewCartographerServer(
		base, failingGit, opPub, initTestKey(), nil, "", 30*time.Second,
		"test-ns", 30*time.Minute, 100000, WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	created, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "unmerged"},
		TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err == nil {
		t.Fatal("expected merge failure")
	}
	if err = base.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second,
		"test-ns", 30*time.Minute, 100000, WithLadybugPath(dataPath),
	)
	restarted.MarkDBReady()
	if err = restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions: %v", err)
	}
	state, err := restarted.txManager.Lookup(begin.TransactionId)
	if err != nil || !state.MainRehydrated || !state.CommitCreated {
		t.Fatalf("recovered partial commit state=%+v err=%v", state, err)
	}
	if _, err = restarted.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RollbackTransaction: %v", err)
	}
	if _, err = reopened.GetEntity(ctx, created.EntityId, "main"); !errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("unmerged transaction entity survived rollback: %v", err)
	}
}

func TestRecoverOpenTransactionsPreservesDivergenceAndSchemaBaselines(t *testing.T) {
	for _, tc := range []struct {
		name          string
		advanceMain   bool
		advanceSchema bool
		commitOK      bool
		wantMessage   string
	}{
		{name: "main advanced", advanceMain: true, wantMessage: "main has advanced"},
		// A purely additive schema push (new types, new properties, rule
		// additions — SPEC R2/R6 non-destructive) does not make the branch DB
		// incompatible with the current schema, so the transaction commits
		// normally instead of being wedged in FAILED_PRECONDITION.
		{name: "schema advanced", advanceSchema: true, commitOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := testCtx()
			dataPath := t.TempDir()
			opPub, _ := generateTestKey()
			base, err := ladybug.Open(dataPath)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			gs, err := gitstore.New(dataPath)
			if err != nil {
				t.Fatalf("gitstore.New: %v", err)
			}
			srv := NewCartographerServer(
				base, gs, opPub, initTestKey(), nil, "", 30*time.Second,
				"test-ns", 30*time.Minute, 100000, WithLadybugPath(dataPath),
			)
			srv.MarkDBReady()
			applyTestSchema(ctx, t, base)
			begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
			if err != nil {
				t.Fatalf("BeginTransaction: %v", err)
			}
			created, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
				EntityType: "Component", Properties: map[string]string{"name": "pending"},
				TransactionId: begin.TransactionId,
			})
			if err != nil {
				t.Fatalf("CreateEntity: %v", err)
			}
			original, err := srv.txManager.Lookup(begin.TransactionId)
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			originalHead, originalSchema := original.MainHeadAtLastSync, original.SchemaHash
			if tc.advanceMain {
				commitGitEntity(ctx, t, gs, "66666666-6666-4666-8666-666666666666", "advanced")
			}
			if tc.advanceSchema {
				if err = base.ApplySchema(ctx, &flowv1.Schema{
					EntityTypes: []*flowv1.EntityType{
						{Name: "Component", Properties: []*flowv1.Property{
							{Name: "name", Type: "string", Required: true},
							{Name: "version", Type: "string"},
						}},
						{
							Name: "Service", Properties: []*flowv1.Property{
								{Name: "name", Type: "string", Required: true},
							},
							Rules: []*flowv1.ConnectionRule{{
								CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"},
							}},
						},
						{Name: "Added", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
					},
					EdgeTypes: []*flowv1.EdgeType{{
						Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "weight", Type: "string"}},
					}},
				}); err != nil {
					t.Fatalf("advance schema: %v", err)
				}
			}
			if err = base.Close(); err != nil {
				t.Fatalf("close store: %v", err)
			}
			reopened, err := ladybug.Open(dataPath)
			if err != nil {
				t.Fatalf("reopen store: %v", err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			reopenedGit, err := gitstore.New(dataPath)
			if err != nil {
				t.Fatalf("reopen git store: %v", err)
			}
			restarted := NewCartographerServer(
				reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second,
				"test-ns", 30*time.Minute, 100000, WithLadybugPath(dataPath),
			)
			restarted.MarkDBReady()
			if err = restarted.RecoverOpenTransactions(ctx); err != nil {
				t.Fatalf("RecoverOpenTransactions: %v", err)
			}
			recovered, err := restarted.txManager.Lookup(begin.TransactionId)
			if err != nil {
				t.Fatalf("Lookup recovered: %v", err)
			}
			if recovered.MainHeadAtLastSync != originalHead || recovered.SchemaHash != originalSchema {
				t.Fatalf("baselines changed: got head=%q schema=%q want head=%q schema=%q",
					recovered.MainHeadAtLastSync, recovered.SchemaHash, originalHead, originalSchema)
			}
			if tc.commitOK {
				// The additive schema push is compatible with the branch DB
				// (SPEC R2/R6 non-destructive): the recovered transaction
				// commits normally and its data lands on main.
				if _, err = restarted.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
					TransactionId: begin.TransactionId,
				}); err != nil {
					t.Fatalf("CommitTransaction after additive schema push: %v", err)
				}
				if _, err := reopened.GetEntity(ctx, created.EntityId, "main"); err != nil {
					t.Fatalf("committed entity missing from main after additive schema push: %v", err)
				}
				return
			}
			if _, err = restarted.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
				TransactionId: begin.TransactionId,
			}); status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), tc.wantMessage) {
				t.Fatalf("CommitTransaction error=%v, want %q", err, tc.wantMessage)
			}
			if _, err = restarted.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
				TransactionId: begin.TransactionId,
			}); err != nil {
				t.Fatalf("RollbackTransaction: %v", err)
			}
		})
	}
}

func TestCommitTransaction_RetryAfterMergeCompletedOnlyCleansUp(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	failingGit := &cleanupAfterMergeFailingGitStore{GitStore: gs}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, failingGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	created, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "merged"}, TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	failingGit.failRestore = true
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	if err == nil {
		t.Fatal("expected post-merge cleanup failure")
	}
	state, lookupErr := srv.txManager.Lookup(begin.TransactionId)
	if lookupErr != nil || !state.MergeCompleted {
		t.Fatalf("merge completion was not retained: state=%+v error=%v", state, lookupErr)
	}
	// A rollback against an already-committed transaction (the merge landed on
	// main) is rejected with NOT_FOUND (SPEC error-table row "Transaction not
	// found": "was already committed/rolled back"), not FAILED_PRECONDITION.
	_, err = srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{TransactionId: begin.TransactionId})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected rollback rejection after merge, got %v", err)
	}
	if _, err = base.GetEntity(ctx, created.EntityId, "main"); err != nil {
		t.Fatalf("committed entity was removed by rejected rollback: %v", err)
	}
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
	if failingGit.commits != 1 || failingGit.merges != 1 {
		t.Fatalf("retry repeated irreversible work: commits=%d merges=%d", failingGit.commits, failingGit.merges)
	}
	if err := gs.WithGitLock(func() error {
		exists, err := gs.BranchExists(ctx, begin.TransactionId)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("transaction Git branch still exists after cleanup")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestCommitTransaction_MergePersistFailureSetsPushNeededOnRetry pins the SPEC
// R10 "commit() returns immediately and sets the push-needed flag" contract
// across the merge-then-persist-failure retry: when the fast-forward merge
// lands on main but the MergeCompleted state write fails, the first attempt
// returns an error without flagging the push; the retry (MergeCompleted path)
// must flag the locally-merged commit for push even though it only finishes the
// cleanup. Without the flag the locally-merged commit stays un-pushed until an
// unrelated later commit sets it.
func TestCommitTransaction_MergePersistFailureSetsPushNeededOnRetry(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	// Fail the one MergeCompleted state write (the "persist completed merge"
	// step); the earlier commit-created / re-hydration writes pass through.
	failingStore := &transactionStateFailingStore{
		Store: base,
		fail:  func(state store.BranchTransactionState) bool { return state.MergeCompleted },
	}
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		failingStore, gs, opPub, initTestKey(), nil, "", 30*time.Second,
		"test-ns", 30*time.Minute, 100000, WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	// Wire a (non-running) SyncWorker so the push flag is observable. No
	// remote URL is configured, so BeginTransaction's implicit sync is skipped.
	sw := NewSyncWorker("", gs, base, RealClock{})
	srv.syncWorker = sw
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	created, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "merged"},
		TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	// First attempt: the git fast-forward merge lands on main, but persisting
	// MergeCompleted fails, so CommitTransaction returns an error before the
	// normal-path SetPushNeeded call.
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err == nil || !strings.Contains(err.Error(), "state write failure") {
		t.Fatalf("CommitTransaction error=%v", err)
	}
	if sw.pushNeeded() {
		t.Fatal("push flag set on the failed first attempt before any retry")
	}
	// Retry: the MergeCompleted path finishes the cleanup and must flag the
	// locally-merged commit for push.
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("retry CommitTransaction: %v", err)
	}
	if !sw.pushNeeded() {
		t.Fatal("locally-merged commit never flagged for push after the retry")
	}
	if _, err = base.GetEntity(ctx, created.EntityId, "main"); err != nil {
		t.Fatalf("merged entity missing from main: %v", err)
	}
}

// =========================================================================
// 7. Concurrent access tests
// =========================================================================

// =========================================================================
// 8. Export tests
// =========================================================================

func TestExportGraph_UnsupportedFormat(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")

	handlerInvoked, err := invokeExportGraph(
		srv, &flowv1.ExportGraphRequest{Format: "unsupported"}, &mockExportStream{ctx: ctx},
	)
	if !handlerInvoked {
		t.Fatal("stream interceptor did not invoke ExportGraph")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestExportGraph_JSON(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	_, _ = srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "a"}, nil, "")
	_, _ = srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "b"}, nil, "")

	stream := &mockExportStream{ctx: capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")}
	handlerInvoked, err := invokeExportGraph(srv, &flowv1.ExportGraphRequest{Format: "json"}, stream)
	if err != nil {
		t.Fatalf("export JSON failed: %v", err)
	}
	if !handlerInvoked {
		t.Fatal("stream interceptor did not invoke ExportGraph")
	}
	if len(stream.data) == 0 {
		t.Fatal("expected non-empty export data")
	}
}

func TestExportGraph_GraphML(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	ent, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "x"}, nil, "")

	stream := &mockExportStream{ctx: capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")}
	handlerInvoked, err := invokeExportGraph(srv, &flowv1.ExportGraphRequest{Format: "graphml"}, stream)
	if err != nil {
		t.Fatalf("export GraphML failed: %v", err)
	}
	if !handlerInvoked {
		t.Fatal("stream interceptor did not invoke ExportGraph")
	}
	if len(stream.data) == 0 {
		t.Fatal("expected non-empty export data")
	}

	// Validate XML structure.
	type graphmlKey struct {
		ID       string `xml:"id,attr"`
		For      string `xml:"for,attr"`
		AttrName string `xml:"attr.name,attr"`
		AttrType string `xml:"attr.type,attr"`
	}
	type graphmlNode struct {
		ID string `xml:"id,attr"`
	}
	type graphmlEdge struct {
		ID     string `xml:"id,attr"`
		Source string `xml:"source,attr"`
		Target string `xml:"target,attr"`
	}
	type graphmlGraph struct {
		ID          string        `xml:"id,attr"`
		EdgeDefault string        `xml:"edgedefault,attr"`
		Nodes       []graphmlNode `xml:"node"`
		Edges       []graphmlEdge `xml:"edge"`
	}
	type graphmlRoot struct {
		XMLName struct{}     `xml:"graphml"`
		Keys    []graphmlKey `xml:"key"`
		Graph   graphmlGraph `xml:"graph"`
	}

	var root graphmlRoot
	if err := xml.Unmarshal(stream.data, &root); err != nil {
		t.Fatalf("invalid GraphML XML: %v", err)
	}
	if root.Graph.ID != "G" {
		t.Errorf("expected graph id G, got %q", root.Graph.ID)
	}
	if root.Graph.EdgeDefault != "directed" {
		t.Errorf("expected edgedefault directed, got %q", root.Graph.EdgeDefault)
	}
	if len(root.Graph.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(root.Graph.Nodes))
	}
	if root.Graph.Nodes[0].ID != ent.Id {
		t.Errorf("expected node id %q, got %q", ent.Id, root.Graph.Nodes[0].ID)
	}
	foundNameKey := false
	for _, k := range root.Keys {
		if k.ID == "name" && k.For == "node" && k.AttrName == "name" && k.AttrType == "string" {
			foundNameKey = true
		}
	}
	if !foundNameKey {
		t.Error("expected <key> declaration for property 'name' on nodes")
	}
}

// TestSerializeGraph_GraphMLDeterministic pins the serializer determinism
// contract: GraphML <data> elements inside each node/edge are emitted in sorted
// property-key order (the same order as the <key> declarations), so repeated
// serialisation of the same graph is byte-identical. Map-iteration order is
// randomised by the runtime, so this test fails without the sort.
func TestSerializeGraph_GraphMLDeterministic(t *testing.T) {
	entities := []store.Entity{
		{Id: "e1", Type: "Component", Properties: map[string]string{"z": "1", "a": "2", "m": "3"}},
	}
	edges := []store.Edge{
		{
			Id:           "ed1",
			Type:         "DEPENDS_ON",
			FromEntityID: "e1",
			ToEntityID:   "e2",
			Properties:   map[string]string{"w": "5", "k": "x", "q": "y"},
		},
	}
	want := `<?xml version="1.0" encoding="UTF-8"?>
<graphml xmlns="http://graphml.graphdrawing.org/xmlns">
  <key id="a" for="node" attr.name="a" attr.type="string"/>
  <key id="m" for="node" attr.name="m" attr.type="string"/>
  <key id="z" for="node" attr.name="z" attr.type="string"/>
  <key id="k" for="edge" attr.name="k" attr.type="string"/>
  <key id="q" for="edge" attr.name="q" attr.type="string"/>
  <key id="w" for="edge" attr.name="w" attr.type="string"/>
  <graph id="G" edgedefault="directed">
    <node id="e1"><data key="a">2</data><data key="m">3</data><data key="z">1</data></node>
    <edge id="ed1" source="e1" target="e2"><data key="k">x</data><data key="q">y</data><data key="w">5</data></edge>
  </graph>
</graphml>
`
	first, err := serializeGraph(ExportFormatGraphML, entities, edges)
	if err != nil {
		t.Fatalf("serializeGraph: %v", err)
	}
	if got := string(first); got != want {
		t.Fatalf("GraphML output mismatch\nwant: %s\ngot:  %s", want, got)
	}
	for i := range 9 {
		got, err := serializeGraph(ExportFormatGraphML, entities, edges)
		if err != nil {
			t.Fatalf("serializeGraph iteration %d: %v", i, err)
		}
		if string(got) != string(first) {
			t.Fatalf("GraphML serialisation is non-deterministic (iteration %d differs):\nfirst: %s\niter:  %s", i, first, got)
		}
	}
}

// =========================================================================
// 9. Missing error-condition tests
// =========================================================================

func TestSearchNeighbors_Valid(t *testing.T) {
	srv, st := newTestServer(t)
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

// TestSearchNeighbors_SingleTypeSpecificCapabilityPasses pins SPEC R3
// (SPEC:240): a caller holding only READ:graph/entity/<type> is authorised
// for a SearchNeighbors scoped to that type (the per-type branch, not the
// wildcard fallback).
func TestSearchNeighbors_SingleTypeSpecificCapabilityPasses(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := narrowCtx("READ:graph/entity/VectorType")

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
	// Seed data directly via the store (the caller holds no write capability).
	_, _ = srv.store.CreateEntity(testCtx(), "VectorType", "",
		map[string]string{"name": "a"}, []float32{1.0, 0.0, 0.0}, "")
	_, _ = srv.store.CreateEntity(testCtx(), "VectorType", "",
		map[string]string{"name": "b"}, []float32{0.0, 1.0, 0.0}, "")

	resp, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{1.0, 0.0, 0.0},
		EntityType: "VectorType",
		TopK:       5,
	})
	if err != nil {
		t.Fatalf("SearchNeighbors with per-type capability failed: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected at least one neighbor result")
	}
}

// TestReadWildcardOnly_TypeOmittedSearchesSucceed covers the SPEC R3 success
// path for the type-omitted read-search branch: a caller holding ONLY
// READ:graph/entity/* must be able to run a type-omitted (empty entityType)
// FullTextSearch and SearchNeighbors. The empty entityType request takes the
// checkWildcardEntityCap branch (not the per-type branch), so an exclusive
// wildcard holder must not be denied.
func TestReadWildcardOnly_TypeOmittedSearchesSucceed(t *testing.T) {
	srv, st := newTestServer(t)
	// Caller holds ONLY READ:graph/entity/* (no WRITE, no per-type caps).
	ctx := narrowCtx("READ:graph/entity/*")

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
	// Seed data directly via the store (the caller holds no write capability).
	_, _ = st.CreateEntity(ctx, "VectorType", "", map[string]string{"name": "alpha"}, []float32{1.0, 0, 0}, "")
	_, _ = st.CreateEntity(ctx, "VectorType", "", map[string]string{"name": "beta"}, []float32{0.0, 1.0, 0.0}, "")

	// FullTextSearch with EntityType omitted (wildcard branch).
	fts, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{Query: "alpha"})
	if err != nil {
		t.Fatalf("FullTextSearch (type-omitted) denied for wildcard-only holder: %v", err)
	}
	if len(fts.Results) == 0 {
		t.Fatal("expected at least one FullTextSearch result")
	}

	// SearchNeighbors with EntityType omitted (wildcard branch).
	sn, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding: []float32{1.0, 0.0, 0.0},
		TopK:      5,
	})
	if err != nil {
		t.Fatalf("SearchNeighbors (type-omitted) with wildcard-only holder: %v", err)
	}
	if len(sn.Results) == 0 {
		t.Fatal("expected at least one neighbor result")
	}
}

// TestReadTypeSpecificOnly_TypeOmittedSearchesDenied pins SPEC R3 (SPEC:262):
// "a per-type capability cannot authorise an all-types search" — a caller
// holding ONLY READ:graph/entity/<type> is denied a type-omitted (empty
// entityType) FullTextSearch and SearchNeighbors with PERMISSION_DENIED,
// because the type-omitted branch requires READ:graph/entity/*.
func TestReadTypeSpecificOnly_TypeOmittedSearchesDenied(t *testing.T) {
	srv, st := newTestServer(t)
	// Caller holds ONLY the per-type read capability (no wildcard).
	ctx := narrowCtx("READ:graph/entity/Component")

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VectorType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
			{
				Name:       "Component",
				Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	_, _ = st.CreateEntity(ctx, "VectorType", "", map[string]string{"name": "alpha"}, []float32{1.0, 0, 0}, "")
	_, _ = st.CreateEntity(ctx, "Component", "", map[string]string{"name": "beta"}, nil, "")

	// FullTextSearch with EntityType omitted (wildcard branch) — denied.
	_, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{Query: "alpha"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied for per-type-only holder on type-omitted FullTextSearch, got %v", err)
	}

	// SearchNeighbors with EntityType omitted (wildcard branch) — denied.
	_, err = srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding: []float32{1.0, 0.0, 0.0},
		TopK:      5,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied for per-type-only holder on type-omitted SearchNeighbors, got %v", err)
	}
}

// TestReadMethods_BeforeFirstApplySchema pins the SPEC R5 (SPEC:345-346) read
// boundary: before the first ApplySchema, non-type-referencing read methods
// (ExecuteCypher without type labels, type-omitted FullTextSearch and
// SearchNeighbors) succeed on an empty graph, while methods referencing a
// specific type return INVALID_ARGUMENT specifically because no schema has been
// applied.
func TestReadMethods_BeforeFirstApplySchema(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	t.Run("non-type-referencing methods succeed on an empty graph", func(t *testing.T) {
		// ExecuteCypher with no type reference.
		resp, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: "MATCH (n) RETURN n"})
		if err != nil {
			t.Fatalf("ExecuteCypher without a type reference should succeed on an empty graph, got %v", err)
		}
		if len(resp.Rows) != 0 {
			t.Fatalf("expected no rows on an empty graph, got %d", len(resp.Rows))
		}

		// FullTextSearch with entityType omitted (wildcard branch).
		fts, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{Query: "anything"})
		if err != nil {
			t.Fatalf("type-omitted FullTextSearch should succeed on an empty graph, got %v", err)
		}
		if len(fts.Results) != 0 {
			t.Fatalf("expected no FullTextSearch results on an empty graph, got %d", len(fts.Results))
		}

		// SearchNeighbors with entityType omitted (wildcard branch).
		sn, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
			Embedding: []float32{1.0, 2.0, 3.0},
			TopK:      5,
		})
		if err != nil {
			t.Fatalf("type-omitted SearchNeighbors should succeed on an empty graph, got %v", err)
		}
		if len(sn.Results) != 0 {
			t.Fatalf("expected no neighbor results on an empty graph, got %d", len(sn.Results))
		}
	})

	t.Run("type-referencing methods return INVALID_ARGUMENT", func(t *testing.T) {
		// ExecuteCypher referencing a specific type label.
		_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: "MATCH (n:Component) RETURN n"})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument for ExecuteCypher referencing an unapplied type, got %v", err)
		}

		// FullTextSearch / SearchNeighbors / ListEntities with an explicit type.
		_, err = srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{Query: "x", EntityType: "Component"})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument for type-referencing FullTextSearch, got %v", err)
		}
		_, err = srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
			Embedding: []float32{1.0, 2.0, 3.0}, EntityType: "Component", TopK: 5,
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument for type-referencing SearchNeighbors, got %v", err)
		}
		_, err = srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{EntityType: "Component", PageSize: 10})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument for type-referencing ListEntities, got %v", err)
		}
	})
}

func TestRefreshTransaction_NoConflicts(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
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

// TestRefreshTransaction_DoesNotResetTimeoutTimer pins SPEC R9 step 4
// ("Refresh() does not reset the transaction timeout timer — the timeout is an
// absolute lifetime from BeginTransaction, not an idle timeout"): after a
// successful RefreshTransaction, advancing the fake clock past the original
// ExpiresAt must surface DEADLINE_EXCEEDED on the next transaction operation.
// The handler never touches ExpiresAt, but a regression that re-armed the timer
// inside Refresh (ExpiresAt = now + timeout) would keep the transaction alive
// past its original absolute lifetime — this test fails if that happens.
func TestRefreshTransaction_DoesNotResetTimeoutTimer(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	// Replace the tx manager with a fake clock so the absolute lifetime can be
	// advanced deterministically without running the GC loop.
	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(7*24*time.Hour, 100000)
	srv.txManager.clock = fc
	ctx := testCtx()

	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(1 * time.Minute),
	})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}

	// Refresh while the transaction is still within its absolute lifetime.
	if _, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RefreshTransaction: %v", err)
	}

	// Advance past the original ExpiresAt (t0 + 1m). The refresh must not have
	// re-armed the timer, so the next operation reports DEADLINE_EXCEEDED.
	fc.Advance(2 * time.Minute)
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "late"},
		TransactionId: begin.TransactionId,
	})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded after refresh and expiry, got %v (%v)", status.Code(err), err)
	}
}

// TestRefreshTransaction_EmptyRefreshThenMutateAndCommit pins the SPEC R9
// refresh flow for a zero-mutation refresh: the branch must be reset and
// re-hydrated from latest main even when the change log is empty, so a
// subsequent mutate+commit produces the clean refresh-then-commit outcome. The
// previous empty-refresh short-circuit only advanced MainHeadAtLastSync: the
// branch DB stayed on its stale begin-time snapshot, so a mutate after the
// refresh committed against the stale branch, re-hydrated main from files
// missing the interim entity, and the fast-forward merge failed with INTERNAL,
// leaving main LadybugDB and git main divergent.
func TestRefreshTransaction_EmptyRefreshThenMutateAndCommit(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)

	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}

	// Main advances while the (empty) transaction is open.
	mainEntityID := testMutationEntityID
	commitGitEntity(ctx, t, gs, mainEntityID, "main")

	// Empty refresh: must reset-and-re-hydrate the branch from the new main.
	if _, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RefreshTransaction (empty): %v", err)
	}

	// Mutate after the empty refresh, then commit.
	created, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity after empty refresh: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CommitTransaction after empty refresh: %v", err)
	}

	// Both the interim main entity and the transaction's own entity must be
	// present on main, with the git fast-forward merge having succeeded.
	if _, err := base.GetEntity(ctx, mainEntityID, "main"); err != nil {
		t.Fatalf("interim main entity missing after refresh-then-commit: %v", err)
	}
	if _, err := base.GetEntity(ctx, created.EntityId, "main"); err != nil {
		t.Fatalf("transaction entity missing from main after commit: %v", err)
	}
}

func TestRefreshTransaction_ConcurrentCommitCannotUseStaleHead(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	blocking := &hydrationBlockingStore{Store: base, blocked: make(chan struct{}), release: make(chan struct{})}
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	attemptingGit := &gitAttemptStore{GitStore: gs}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		blocking, attemptingGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	created, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	mainEntityID := testMutationEntityID
	commitGitEntity(ctx, t, gs, mainEntityID, "main")

	refreshDone := make(chan error, 1)
	go func() {
		_, refreshErr := srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{TransactionId: begin.TransactionId})
		refreshDone <- refreshErr
	}()
	<-blocking.blocked
	gitAttempted := make(chan struct{})
	attemptingGit.setAttempted(gitAttempted)
	unrelatedDone := make(chan error, 1)
	go func() { unrelatedDone <- srv.withGitLock(func() error { return nil }) }()
	<-gitAttempted
	commitAtLifecycleLock := make(chan struct{})
	srv.txManager.beforeLifecycleLock = func(string) { close(commitAtLifecycleLock) }
	commitDone := make(chan error, 1)
	go func() {
		_, commitErr := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
		commitDone <- commitErr
	}()
	<-commitAtLifecycleLock
	srv.txManager.beforeLifecycleLock = nil
	close(blocking.release)
	if err := <-refreshDone; err != nil {
		t.Fatalf("RefreshTransaction: %v", err)
	}
	if err := <-unrelatedDone; err != nil {
		t.Fatalf("unrelated Git checkout: %v", err)
	}
	if err := <-commitDone; err != nil {
		t.Fatalf("CommitTransaction: %v", err)
	}
	if _, err := base.GetEntity(ctx, created.EntityId, "main"); err != nil {
		t.Fatalf("committed entity missing from main: %v", err)
	}
	if _, err := base.GetEntity(ctx, mainEntityID, "main"); err != nil {
		t.Fatalf("advanced main entity missing after refresh/commit: %v", err)
	}
}

func TestRefreshTransaction_HydrationFailureDoesNotAdvanceSyncHead(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	release := make(chan struct{})
	close(release)
	blocking := &hydrationBlockingStore{
		Store: base, blocked: make(chan struct{}), release: release, fail: true,
	}
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		blocking, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	state, err := srv.txManager.Lookup(begin.TransactionId)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	oldHead := state.MainHeadAtLastSync
	commitGitEntity(ctx, t, gs, "11111111-1111-4111-8111-111111111111", "main")

	_, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{TransactionId: begin.TransactionId})
	if err == nil {
		t.Fatal("expected hydration failure")
	}
	if state.MainHeadAtLastSync != oldHead {
		t.Fatalf("sync head advanced after failed hydration: got %q want %q", state.MainHeadAtLastSync, oldHead)
	}
}

func TestRefreshTransaction_ConflictLeavesCleanRefreshedBranch(t *testing.T) {
	ladybugPath := t.TempDir()
	base, err := ladybug.Open(ladybugPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	first, err := base.CreateEntity(ctx, "Component", "", map[string]string{"name": "one"}, nil, "main")
	if err != nil {
		t.Fatalf("create first main entity: %v", err)
	}
	second, err := base.CreateEntity(ctx, "Component", "", map[string]string{"name": "two"}, nil, "main")
	if err != nil {
		t.Fatalf("create second main entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, first.Id, "one")
	commitGitEntity(ctx, t, gs, second.Id, "two")
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: first.Id, Properties: map[string]string{"name": "tx-one"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("update first transaction entity: %v", err)
	}
	if _, err = srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: second.Id, Properties: map[string]string{"name": "tx-two"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("update second transaction entity: %v", err)
	}
	if _, err = base.UpdateEntity(ctx, second.Id, map[string]string{"name": "main-two"}, nil, "main"); err != nil {
		t.Fatalf("update second main entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, second.Id, "main-two")

	_, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{TransactionId: begin.TransactionId})
	if status.Code(err) != codes.Aborted {
		t.Fatalf("expected refresh conflict, got %v", err)
	}
	firstAfter, err := base.GetEntity(ctx, first.Id, begin.TransactionId)
	if err != nil {
		t.Fatalf("get first refreshed entity: %v", err)
	}
	secondAfter, err := base.GetEntity(ctx, second.Id, begin.TransactionId)
	if err != nil {
		t.Fatalf("get second refreshed entity: %v", err)
	}
	if firstAfter.Properties["name"] != "one" || secondAfter.Properties["name"] != "main-two" {
		t.Fatalf("conflicted refresh partially reapplied changes: first=%+v second=%+v", firstAfter, secondAfter)
	}
}

// TestRefreshTransaction_AbortedRefreshPreservesDiff pins SPEC R9 step 3
// ("Because Diff() reads the change log rather than querying the branch DB, it
// returns the same result regardless of whether the branch DB reflects the
// transaction's changes or has been re-hydrated to a clean state (e.g. after
// an aborted Refresh())"): after an ABORTED refresh leaves the branch DB
// re-hydrated to a clean state, GetTransactionDiff must return exactly the
// pre-refresh diff — the change log is preserved, only the branch DB is
// rebuilt. The existing aborted-refresh tests assert the branch DB contents but
// never call GetTransactionDiff.
func TestRefreshTransaction_AbortedRefreshPreservesDiff(t *testing.T) {
	ladybugPath := t.TempDir()
	base, err := ladybug.Open(ladybugPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	first, err := base.CreateEntity(ctx, "Component", "", map[string]string{"name": "one"}, nil, "main")
	if err != nil {
		t.Fatalf("create first main entity: %v", err)
	}
	second, err := base.CreateEntity(ctx, "Component", "", map[string]string{"name": "two"}, nil, "main")
	if err != nil {
		t.Fatalf("create second main entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, first.Id, "one")
	commitGitEntity(ctx, t, gs, second.Id, "two")
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: first.Id, Properties: map[string]string{"name": "tx-one"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("update first transaction entity: %v", err)
	}
	if _, err = srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: second.Id, Properties: map[string]string{"name": "tx-two"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("update second transaction entity: %v", err)
	}
	before, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("GetTransactionDiff before refresh: %v", err)
	}

	// Main advances on the second entity while the transaction is open,
	// forcing the refresh to abort (UUID-overlap conflict).
	if _, err = base.UpdateEntity(ctx, second.Id, map[string]string{"name": "main-two"}, nil, "main"); err != nil {
		t.Fatalf("update second main entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, second.Id, "main-two")

	if _, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.Aborted {
		t.Fatalf("expected refresh conflict, got %v", err)
	}
	after, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("GetTransactionDiff after aborted refresh: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("aborted refresh changed the transaction diff: before=%+v after=%+v", before, after)
	}
	if len(after.ModifiedEntities) != 2 {
		t.Fatalf("expected the two modified entities preserved in the diff, got %+v", after.ModifiedEntities)
	}
}

// TestRefreshTransaction_EmbeddingDimensionConflict exercises the SPEC R7
// dimension-scope refresh-conflict rule. validateRefresh checks each
// change-log entry carrying an embedding against main's established
// embedding dimension; a mismatch must surface as ABORTED (errRefreshConflict).
// Every pre-existing refresh test only exercises entity/edge-overlap conflicts,
// leaving this dimension path uncovered.
func TestRefreshTransaction_EmbeddingDimensionConflict(t *testing.T) {
	srv, st := newTestServer(t)
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
		t.Fatalf("ApplySchema: %v", err)
	}
	// Main bootstraps a 3-dim embedding column for VecType.
	_, err := st.CreateEntity(ctx, "VecType", "", map[string]string{"name": "main"}, []float32{1.0, 0.0, 0.0}, "main")
	if err != nil {
		t.Fatalf("bootstrap main: %v", err)
	}

	// A transaction whose change log records a 2-dim embedding add.
	txID := testMutationEntityID
	state, err := srv.txManager.Create(txID, time.Minute, "head")
	if err != nil {
		t.Fatalf("Create transaction: %v", err)
	}
	err = state.ChangeLog.Add(gitstore.ChangeLogEntry{
		Kind: gitstore.ChangeAddEntity, ID: "22222222-2222-4222-8222-222222222222", Type: "VecType",
		Entity: &gitstore.EntityEntry{
			ID: "22222222-2222-4222-8222-222222222222", Type: "VecType",
			Embedding: []float32{1.0, 0.0}, // 2 dims vs main's established 3
		},
	})
	if err != nil {
		t.Fatalf("Add change log entry: %v", err)
	}

	// No entity/edge files collide — before and current are empty — so only the
	// dimension check can fire. It must map to an ABORTED refresh conflict.
	err = srv.validateRefresh(ctx, state, gitGraphSnapshot{}, gitGraphSnapshot{})
	if status.Code(err) != codes.Aborted {
		t.Fatalf("expected ABORTED dimension-scope refresh conflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "refresh conflict") {
		t.Fatalf("expected refresh-conflict message, got %q", err.Error())
	}
}

// TestRefreshTransaction_CreatedThenModifiedWithinTransaction pins SPEC R9
// refresh step 3 for a create-then-update of the same entity within one
// transaction. The map-based change log records ChangeAddEntity then
// ChangeModEntity for an ID that has no baseline file on the branch tree (the
// entity was created inside the transaction), and validateRefresh must not
// treat the missing baseline as a UUID-overlap conflict against main when main
// is untouched — the refresh must succeed and re-apply both entries so the
// entity survives with its updated content.
func TestRefreshTransaction_CreatedThenModifiedWithinTransaction(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Id: testMutationEntityID, Properties: map[string]string{"name": "created"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err = srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: testMutationEntityID, Properties: map[string]string{"name": "updated"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("UpdateEntity: %v", err)
	}
	if _, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RefreshTransaction must not conflict on a same-transaction create-then-update: %v", err)
	}
	ent, err := base.GetEntity(ctx, testMutationEntityID, begin.TransactionId)
	if err != nil {
		t.Fatalf("refreshed entity missing: %v", err)
	}
	if ent.Properties["name"] != "updated" {
		t.Fatalf("expected the re-applied update to win, got %+v", ent.Properties)
	}
}

// TestRefreshTransaction_CreatedThenDeletedWithinTransaction pins SPEC R9
// refresh step 3 for a create-then-delete of the same entity within one
// transaction: the ChangeDelEntity entry has no baseline file (the entity was
// created inside the transaction), and with main untouched the refresh must
// succeed and re-apply both entries, leaving the branch without the entity.
func TestRefreshTransaction_CreatedThenDeletedWithinTransaction(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Id: testMutationEntityID, Properties: map[string]string{"name": "created"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err = srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{
		Id: testMutationEntityID, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}
	if _, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RefreshTransaction must not conflict on a same-transaction create-then-delete: %v", err)
	}
	if _, err := base.GetEntity(ctx, testMutationEntityID, begin.TransactionId); !errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("expected the re-applied create-then-delete to leave the entity absent, got %v", err)
	}
}

// TestRefreshTransaction_CreatedThenDeleted_ConflictsWhenMainHasUUID is the
// positive control for the create-then-delete fix: the missing baseline must
// not conflict when main is untouched, but the same change MUST still abort
// when main advances with an entity holding the same UUID (SPEC R9 refresh
// step 3 UUID-overlap rule). The ChangeAddEntity entry is what detects the
// overlap.
func TestRefreshTransaction_CreatedThenDeleted_ConflictsWhenMainHasUUID(t *testing.T) {
	ladybugPath := t.TempDir()
	base, err := ladybug.Open(ladybugPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Id: testMutationEntityID, Properties: map[string]string{"name": "created"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err = srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{
		Id: testMutationEntityID, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}
	// Main advances with an entity holding the same UUID while the transaction
	// is open.
	commitGitEntity(ctx, t, gs, testMutationEntityID, "main-owned")
	if _, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.Aborted {
		t.Fatalf("expected ABORTED refresh conflict when main holds the same UUID, got %v", err)
	}
}

// TestCommitTransaction_DeleteThenRecreateSameID_Survives pins the commit
// path's final-state semantics for delete-then-recreate of the same explicit
// entity ID within one transaction. The map-based change log records both a
// ChangeDelEntity and a ChangeAddEntity for the ID; the commit path writes
// added files before removing deleted ones, so without same-ID resolution the
// recreated <id>.json would be written then removed — the committed tree (and
// main, which is re-hydrated from the tree per SPEC R9 commit step 8) would
// lack the entity while the branch LadybugDB's final state still holds it:
// silent data loss. The recreated entity must survive both the git tree and
// main.
func TestCommitTransaction_DeleteThenRecreateSameID_Survives(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	// Main holds the entity at begin — a delete requires an existing entity.
	commitGitEntity(ctx, t, gs, testMutationEntityID, "original")
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{
		Id: testMutationEntityID, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Id: testMutationEntityID, Properties: map[string]string{"name": "recreated"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("recreate entity: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CommitTransaction: %v", err)
	}
	mainEnt, err := base.GetEntity(ctx, testMutationEntityID, "main")
	if err != nil {
		t.Fatalf("recreated entity lost from main after commit: %v", err)
	}
	if mainEnt.Properties["name"] != "recreated" {
		t.Fatalf("expected the recreated entity content on main, got %+v", mainEnt.Properties)
	}
	files, err := gs.ReadAllEntityFiles(ctx, "Component")
	if err != nil {
		t.Fatalf("ReadAllEntityFiles: %v", err)
	}
	if len(files) != 1 || files[0].ID != testMutationEntityID || files[0].Properties["name"] != "recreated" {
		t.Fatalf("expected the recreated <id>.json to survive in the committed tree, got %+v", files)
	}
}

// TestCommitTransaction_CreateThenDeleteSameID_LeavesNoTrace pins the other
// half of the same-ID ambiguity: create-then-delete within one transaction is
// a net no-op and must commit with no entity on main and no file in the
// committed tree — the same-ID resolution must not resurrect the deleted
// entity.
func TestCommitTransaction_CreateThenDeleteSameID_LeavesNoTrace(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Id: testMutationEntityID, Properties: map[string]string{"name": "created"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err = srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{
		Id: testMutationEntityID, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CommitTransaction: %v", err)
	}
	if _, err := base.GetEntity(ctx, testMutationEntityID, "main"); !errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("expected create-then-delete to leave no entity on main, got %v", err)
	}
	files, err := gs.ReadAllEntityFiles(ctx, "Component")
	if err != nil {
		t.Fatalf("ReadAllEntityFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected no entity files in the committed tree after create-then-delete, got %+v", files)
	}
}

func TestRecoveryDiffPropagatesSuspectedDeletions(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()
	txID := testMutationEntityID
	state, err := srv.txManager.Create(txID, time.Minute, "")
	if err != nil {
		t.Fatalf("Create transaction: %v", err)
	}
	_, err = srv.recoverEntityChanges(state.ChangeLog, nil, map[string]map[string]gitstore.EntityFile{
		"Component": {"entity": {ID: "entity", Type: "Component"}},
	})
	if err != nil {
		t.Fatalf("recoverEntityChanges: %v", err)
	}
	_, err = srv.recoverEdgeChanges(state.ChangeLog, nil, map[string]map[string]gitstore.EdgeFile{
		"DEPENDS_ON": {"edge": {ID: "edge", Type: "DEPENDS_ON"}},
	})
	if err != nil {
		t.Fatalf("recoverEdgeChanges: %v", err)
	}

	diff, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: txID})
	if err != nil {
		t.Fatalf("GetTransactionDiff: %v", err)
	}
	if len(diff.DeletedEntities) != 1 || !diff.DeletedEntities[0].Suspected {
		t.Fatalf("expected suspected recovered entity deletion, got %+v", diff.DeletedEntities)
	}
	if len(diff.DeletedEdges) != 1 || !diff.DeletedEdges[0].Suspected {
		t.Fatalf("expected suspected recovered edge deletion, got %+v", diff.DeletedEdges)
	}
}

// TestRecoveryDiffClassifiesModifiedEdges pins SPEC R9 recovery step 2 ("For
// each edge table in the branch DB, iterate all relationships using the same
// comparison logic" as step 1) and step 3 (Diff returns "lists of added,
// modified, and deleted entities and edges"): an edge present in main with
// different content must be classified as a modification (ChangeModEdge) — not
// collapsed into ChangeAddEdge — and must surface through GetTransactionDiff's
// modified_edges wire field.
func TestRecoveryDiffClassifiesModifiedEdges(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()
	txID := testMutationEntityID
	state, err := srv.txManager.Create(txID, time.Minute, "")
	if err != nil {
		t.Fatalf("Create transaction: %v", err)
	}
	changed, err := srv.recoverEdgeChanges(state.ChangeLog, []store.Edge{
		{
			Id: "edge-mod", Type: "DEPENDS_ON", FromEntityID: "a", ToEntityID: "b",
			Properties: map[string]string{"weight": "high"},
		},
	}, map[string]map[string]gitstore.EdgeFile{
		"DEPENDS_ON": {
			"edge-mod": {
				ID: "edge-mod", Type: "DEPENDS_ON", FromEntityID: "a", ToEntityID: "b",
				Properties: map[string]string{"weight": "low"},
			},
		},
	})
	if err != nil {
		t.Fatalf("recoverEdgeChanges: %v", err)
	}
	if !changed {
		t.Fatal("expected an edge change to be recorded")
	}

	diff, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: txID})
	if err != nil {
		t.Fatalf("GetTransactionDiff: %v", err)
	}
	if len(diff.ModifiedEdges) != 1 || diff.ModifiedEdges[0].Id != "edge-mod" ||
		diff.ModifiedEdges[0].Properties["weight"] != "high" {
		t.Fatalf("expected recovered edge modification in diff, got %+v", diff.ModifiedEdges)
	}
	if len(diff.AddedEdges) != 0 {
		t.Fatalf("expected no added edges for a modified edge, got %+v", diff.AddedEdges)
	}

	// A recovered modification must never re-apply as a deletion/creation or
	// silently pass a refresh: a ChangeModEdge whose branch content differs
	// from main always conflicts at validateRefresh (before/current edge files
	// differ), leaving the branch clean and the change log preserved.
	before := gitGraphSnapshot{edges: map[string]gitstore.EdgeFile{
		"edge-mod": {
			ID: "edge-mod", Type: "DEPENDS_ON", FromEntityID: "a", ToEntityID: "b",
			Properties: map[string]string{"weight": "high"},
		},
	}}
	current := gitGraphSnapshot{edges: map[string]gitstore.EdgeFile{
		"edge-mod": {
			ID: "edge-mod", Type: "DEPENDS_ON", FromEntityID: "a", ToEntityID: "b",
			Properties: map[string]string{"weight": "low"},
		},
	}}
	if err := srv.validateRefresh(ctx, state, before, current); status.Code(err) != codes.Aborted {
		t.Fatalf("expected ABORTED refresh conflict for recovered edge modification, got %v", err)
	}
	if err := srv.reapplyTransactionChanges(ctx, txID, state.ChangeLog); status.Code(err) != codes.Aborted {
		t.Fatalf("expected re-apply of a recovered edge modification to fail loudly with ABORTED, got %v", err)
	}
}

//nolint:gocyclo
func TestRecoverOpenTransactionsAfterStoreRestart(t *testing.T) {
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()

	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, st)
	modified, err := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "before"}, nil, "main")
	if err != nil {
		t.Fatalf("create modified entity: %v", err)
	}
	deleted, err := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "deleted"}, nil, "main")
	if err != nil {
		t.Fatalf("create deleted entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, modified.Id, "before")
	commitGitEntity(ctx, t, gs, deleted.Id, "deleted")

	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	if _, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: modified.Id, Properties: map[string]string{"name": "after"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("update transaction entity: %v", err)
	}
	if _, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{
		Id: deleted.Id, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("delete transaction entity: %v", err)
	}
	branchPath := filepath.Join(dataPath, "branches", begin.TransactionId+".lbug")
	branchMetadataPath := filepath.Join(dataPath, "branches", begin.TransactionId+".schema.json")
	if _, err := os.Stat(branchPath); err != nil {
		t.Fatalf("stat persisted branch: %v", err)
	}
	if _, err := os.Stat(branchMetadataPath); err != nil {
		t.Fatalf("stat persisted branch schema metadata: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	restarted.MarkDBReady()
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("recover transactions: %v", err)
	}
	state, err := restarted.txManager.Lookup(begin.TransactionId)
	if err != nil {
		t.Fatalf("lookup recovered transaction: %v", err)
	}
	if state.MainHeadAtLastSync == "" {
		t.Fatal("recovered transaction has no main HEAD baseline")
	}
	if state.SchemaHash == "" {
		t.Fatal("recovered transaction has no schema baseline")
	}

	diff, err := restarted.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("get recovered diff: %v", err)
	}
	if len(diff.ModifiedEntities) != 1 || diff.ModifiedEntities[0].Id != modified.Id ||
		diff.ModifiedEntities[0].Properties["name"] != "after" {
		t.Fatalf("unexpected recovered modifications: %+v", diff.ModifiedEntities)
	}
	if len(diff.DeletedEntities) != 1 || diff.DeletedEntities[0].Id != deleted.Id ||
		!diff.DeletedEntities[0].Suspected {
		t.Fatalf("unexpected recovered deletions: %+v", diff.DeletedEntities)
	}
	if _, err := os.Stat(branchPath); err != nil {
		t.Fatalf("recovery removed open transaction branch: %v", err)
	}
	if _, err := restarted.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{}, TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "missing required property") {
		t.Fatalf("recovered branch accepted missing required property: %v", err)
	}
	commitGitEntity(ctx, t, reopenedGit, "55555555-5555-4555-8555-555555555555", "advanced")
	if _, err := restarted.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("commit after main advancement should require refresh: %v", err)
	}
	if _, err := restarted.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("refresh recovered transaction: %v", err)
	}
	if _, err := restarted.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "after-recovery"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("mutate recovered transaction after refresh: %v", err)
	}
	if _, err := restarted.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("roll back recovered transaction: %v", err)
	}
	if _, err := os.Stat(branchPath); !os.IsNotExist(err) {
		t.Fatalf("rollback did not remove persisted branch: %v", err)
	}
	if _, err := os.Stat(branchMetadataPath); !os.IsNotExist(err) {
		t.Fatalf("rollback did not remove branch schema metadata: %v", err)
	}
}

func TestRecoverOpenTransactionsPersistsAppliedTimeout(t *testing.T) {
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()

	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, st)

	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(90 * time.Second),
	})
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	if begin.GetAppliedTimeout().AsDuration() != 90*time.Second {
		t.Fatalf("expected applied timeout 90s, got %v", begin.GetAppliedTimeout().AsDuration())
	}
	// A mutation is required so the branch diff is non-empty and recovery does
	// not treat the transaction as already-committed.
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "in-tx"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("create entity in transaction: %v", err)
	}
	// Extend the expiry to 2 minutes, then verify the granted value is
	// persisted (mirroring durableTransactionState) so recovery restores it
	// instead of silently defaulting to the 7-day hard maximum.
	if _, err := srv.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{
		TransactionId: begin.TransactionId,
		Duration:      durationpb.New(2 * time.Minute),
	}); err != nil {
		t.Fatalf("extend timeout: %v", err)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second, "test", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	restarted.MarkDBReady()
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions: %v", err)
	}
	recovered, err := restarted.txManager.Lookup(begin.TransactionId)
	if err != nil {
		t.Fatalf("lookup recovered transaction: %v", err)
	}
	if recovered.AppliedTimeout != 2*time.Minute {
		t.Fatalf("expected recovered applied timeout 2m, got %v", recovered.AppliedTimeout)
	}
	if time.Until(recovered.ExpiresAt) >= 24*time.Hour {
		t.Fatalf("recovered transaction should not default to the 7-day hard max, ExpiresAt=%v", recovered.ExpiresAt)
	}
}

// TestRecoverOpenTransactionsPreservesAbsoluteLifetime pins SPEC R9's
// "absolute lifetime from BeginTransaction, not an idle timeout" across a
// restart: a recovered transaction must retain its original begin instant
// (CreatedAt) and expiry (ExpiresAt) rather than being re-based from the
// restart instant. Before the fix, RecoverOpenTransactions re-based both via
// txManager.Create, so a transaction could live materially longer than its
// granted lifetime — and beyond the 7-day hard maximum measured from the
// original begin — after each crash/restart, and the ExtendTimeout ceiling
// (computed against the rebased CreatedAt) no longer bounded the true total
// lifetime. The test begins a transaction with a 30-minute timeout, lets its
// absolute lifetime elapse while it is open (fake clock), restarts, and
// asserts the recovered transaction keeps the original CreatedAt/ExpiresAt and
// is already expired (DEADLINE_EXCEEDED) rather than re-armed for a fresh
// 30-minute lease of life from the restart instant.
func TestRecoverOpenTransactionsPreservesAbsoluteLifetime(t *testing.T) {
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()

	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	// Drive the lifetime deterministically with a fake clock shared across the
	// simulated restart.
	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(7*24*time.Hour, 100000)
	srv.txManager.clock = fc
	applyTestSchema(ctx, t, st)

	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	beginInstant := fc.Now()
	// A mutation is required so the branch diff is non-empty and recovery does
	// not treat the transaction as already-committed.
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "in-tx"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("create entity in transaction: %v", err)
	}

	// The transaction's absolute lifetime elapses while it is open: the
	// restart happens after the original expiry.
	fc.Advance(40 * time.Minute)

	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	restarted.txManager = NewTransactionManager(7*24*time.Hour, 100000)
	restarted.txManager.clock = fc
	restarted.MarkDBReady()
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions: %v", err)
	}
	recovered, err := restarted.txManager.Lookup(begin.TransactionId)
	if err != nil {
		t.Fatalf("lookup recovered transaction: %v", err)
	}
	// The recovered transaction keeps the original begin instant and expiry:
	// recovery must NOT re-base them from the restart instant (now =
	// begin+40m), which would grant a fresh 30-minute lease of life.
	if !recovered.CreatedAt.Equal(beginInstant) {
		t.Fatalf("recovered CreatedAt = %v, want the original begin %v", recovered.CreatedAt, beginInstant)
	}
	wantExpiry := beginInstant.Add(30 * time.Minute)
	if !recovered.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("recovered ExpiresAt = %v, want the original absolute expiry %v", recovered.ExpiresAt, wantExpiry)
	}
	// The absolute lifetime has already elapsed at restart (now = begin+40m),
	// so the recovered transaction is expired: an operation on it surfaces
	// DEADLINE_EXCEEDED rather than a re-armed fresh lifetime.
	_, err = restarted.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: begin.TransactionId})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded on the expired recovered transaction, got %v", err)
	}
}

func TestRecoverRollbackOnlyTransactionWhenRejectedUpdateDoesNotIncreaseNetDiff(t *testing.T) {
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	failing := &dropFailingStore{Store: st, failDrop: true}
	srv := NewCartographerServer(
		failing, gs, opPub, initTestKey(), nil, "", 30*time.Second,
		"test-ns", 30*time.Minute, 1, WithLadybugPath(dataPath),
	)
	applyTestSchema(ctx, t, st)
	mainEntity, err := st.CreateEntity(
		ctx, "Component", "", map[string]string{"name": "before"}, nil, "main",
	)
	if err != nil {
		t.Fatalf("create main entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, mainEntity.Id, "before")
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	if _, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: mainEntity.Id, Properties: map[string]string{"version": "first"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("first update: %v", err)
	}
	// The rejected mutation is a CreateEntity: under the SPEC change-log
	// admission predicate a same-ID update at cap is admitted (it reuses the
	// element's slot and does not grow the log), so the capacity trigger must
	// be a mutation that adds a new distinct element — a CreateEntity always
	// does.
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "rejected"}, TransactionId: begin.TransactionId,
	})
	if status.Code(err) != codes.ResourceExhausted || !strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("rejected mutation error = %v", err)
	}
	markerPath := filepath.Join(dataPath, "branches", begin.TransactionId+".state.json")
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("rollback-only marker was not persisted: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second,
		"test-ns", 30*time.Minute, 1, WithLadybugPath(dataPath),
	)
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("recover rollback-only transaction: %v", err)
	}
	state, err := restarted.txManager.Lookup(begin.TransactionId)
	if err != nil || !state.RollbackOnly || state.ChangeLog.Len() != 1 {
		t.Fatalf("recovered state = %+v, err=%v", state, err)
	}
	// A rollback-only transaction is already rolled back, so commit and
	// mutations on it surface NOT_FOUND (SPEC "Transaction not found": "was
	// already committed/rolled back") — the cap-violation outcome is defined
	// solely as RESOURCE_EXHAUSTED with the transaction "rolled back".
	if _, err := restarted.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.NotFound {
		t.Fatalf("commit rollback-only transaction: %v", err)
	}
	if _, err := restarted.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: mainEntity.Id, Properties: map[string]string{"version": "late"}, TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.NotFound {
		t.Fatalf("mutate rollback-only transaction: %v", err)
	}
	if _, err := restarted.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("rollback recovered transaction: %v", err)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("rollback-only marker remained after cleanup: %v", err)
	}
}

func TestChangeLogMarkerFailureStillCleansRejectedTransaction(t *testing.T) {
	srv, base := newTestServer(t)
	srv.store = &markerFailingStore{Store: base, failMark: true}
	srv.txManager.changeLogCap = 1
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "first"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("first CreateEntity: %v", err)
	}
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "rejected"}, TransactionId: begin.TransactionId,
	})
	if status.Code(err) != codes.ResourceExhausted ||
		!strings.Contains(err.Error(), "simulated rollback-only marker failure") {
		t.Fatalf("cap rejection error = %v", err)
	}
	if _, err := srv.txManager.Lookup(begin.TransactionId); err == nil {
		t.Fatal("transaction remained after successful cleanup")
	}
	if _, err := base.DumpAllEntities(ctx, begin.TransactionId); !errors.Is(err, store.ErrBranchNotFound) {
		t.Fatalf("branch DB remained after cleanup: %v", err)
	}
	exists, err := srv.gitstore.BranchExists(ctx, begin.TransactionId)
	if err != nil || exists {
		t.Fatalf("Git branch remained after cleanup: exists=%v err=%v", exists, err)
	}
}

func TestChangeLogMarkerAndCleanupFailureCannotRecoverAsActive(t *testing.T) {
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	failing := &markerFailingStore{Store: st, failMark: true, failDrop: true}
	srv := NewCartographerServer(
		failing, gs, opPub, initTestKey(), nil, "", 30*time.Second,
		"test-ns", 30*time.Minute, 1, WithLadybugPath(dataPath),
	)
	applyTestSchema(ctx, t, st)
	mainEntity, err := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "before"}, nil, "main")
	if err != nil {
		t.Fatalf("create main entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, mainEntity.Id, "before")
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: mainEntity.Id, Properties: map[string]string{"version": "first"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("first update: %v", err)
	}
	// The rejected mutation is a CreateEntity: under the SPEC change-log
	// admission predicate a same-ID update at cap is admitted (it reuses the
	// element's slot and does not grow the log), so the capacity trigger must
	// be a mutation that adds a new distinct element — a CreateEntity always
	// does.
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "rejected"}, TransactionId: begin.TransactionId,
	})
	if status.Code(err) != codes.ResourceExhausted ||
		!strings.Contains(err.Error(), "simulated rollback-only marker failure") ||
		!strings.Contains(err.Error(), "simulated marker cleanup drop failure") {
		t.Fatalf("combined failure error = %v", err)
	}
	branchEntity, err := st.GetEntity(ctx, mainEntity.Id, begin.TransactionId)
	if err != nil {
		t.Fatalf("read branch entity: %v", err)
	}
	if branchEntity.Properties["version"] != "first" {
		t.Fatalf("rejected mutation reached branch: %+v", branchEntity.Properties)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second,
		"test-ns", 30*time.Minute, 1, WithLadybugPath(dataPath),
	)
	// The rollback-only marker persist failed, so the store invalidated the
	// state record; on restart the branch has no lifecycle record. Recovery
	// must not wedge startup and must not register the rejected transaction as
	// active — it finishes the aborted rollback instead (SPEC R9 recovery:
	// a branch with no persisted state record cannot be recovered).
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("recovery wedged startup on an invalidated branch: %v", err)
	}
	if _, err := restarted.txManager.Lookup(begin.TransactionId); err == nil {
		t.Fatal("failed-closed transaction was registered as active")
	}
	// The rollback finished the aborted cleanup: the git branch is removed.
	if err := reopenedGit.WithGitLock(func() error {
		exists, err := reopenedGit.BranchExists(ctx, begin.TransactionId)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("invalidated transaction git branch still exists")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestRecoverOpenTransactionsSchemaPushDoesNotBlockCommit pins recovery's
// restoration of the schema baseline together with the SPEC R9 commit flow
// step 1 semantics: a schema push after recovery that is additive (a property
// flag change, a new type) does not make the branch DB incompatible with the
// current schema, so the recovered transaction commits normally instead of
// being wedged in FAILED_PRECONDITION.
func TestRecoverOpenTransactionsSchemaPushDoesNotBlockCommit(t *testing.T) {
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	applyTestSchema(ctx, t, st)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	created, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "persisted"},
		TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("create transaction entity: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("recover transactions: %v", err)
	}
	// Additive push after recovery: an existing property's required flag and a
	// new entity type. Non-destructive per SPEC R2/R6 (the store's diff ignores
	// the Required flag and new types are additive), so the commit must proceed.
	if err := reopened.ApplySchema(ctx, &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{
				{Name: "name", Type: "string", Required: true},
				{Name: "version", Type: "string", Required: true},
			}},
			{
				Name: "Service", Properties: []*flowv1.Property{{Name: "name", Type: "string", Required: true}},
				Rules: []*flowv1.ConnectionRule{{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}}},
			},
			{Name: "Added", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
		},
		EdgeTypes: []*flowv1.EdgeType{{
			Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "weight", Type: "string"}},
		}},
	}); err != nil {
		t.Fatalf("change schema: %v", err)
	}
	if _, err := restarted.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("commit after additive schema push should succeed: %v", err)
	}
	if _, err := reopened.GetEntity(ctx, created.EntityId, "main"); err != nil {
		t.Fatalf("committed entity missing from main after additive schema push: %v", err)
	}
}

// A corrupt branch .lbug is rolled back during recovery, mirroring the
// absent-.lbug case (SPEC R9 recovery point 4): the R8-corruption
// classification (branchLocked → ErrBranchNotFound) turns the transaction into
// a rollback (DropBranchDB removes the corrupt file, the git branch is
// deleted) so startup proceeds instead of wedging on a hard open error until a
// human deletes the file (the pre-fix behavior).
func TestRecoverOpenTransactionsRollsBackCorruptBranch(t *testing.T) {
	ctx := testCtx()
	dataPath := t.TempDir()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	txID := "33333333-3333-4333-8333-333333333333"
	if err := gs.WithGitLock(func() error { return gs.CreateBranch(ctx, txID) }); err != nil {
		t.Fatalf("create git branch: %v", err)
	}
	branchPath := filepath.Join(dataPath, "branches", txID+".lbug")
	if err := os.WriteFile(branchPath, []byte("not a ladybug database"), 0600); err != nil {
		t.Fatalf("write corrupt branch: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	// Startup must not wedge: recovery classifies the corrupt .lbug as a lost
	// branch and rolls it back instead of returning a hard error.
	if err := srv.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("recover transactions with corrupt branch should roll back, got %v", err)
	}
	// The rollback removed the corrupt branch .lbug.
	if _, err := os.Stat(branchPath); !os.IsNotExist(err) {
		t.Fatalf("corrupt branch .lbug was not removed by rollback: %v", err)
	}
	// The rollback deleted the git branch.
	if err := gs.WithGitLock(func() error {
		exists, err := gs.BranchExists(ctx, txID)
		if err != nil {
			return err
		}
		if exists {
			return errors.New("git branch was not removed by rollback")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverOpenTransactionsMissingBranchRestoresMain(t *testing.T) {
	ctx := testCtx()
	dataPath := t.TempDir()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	applyTestSchema(ctx, t, st)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(dataPath, "branches", begin.TransactionId+".lbug")); err != nil {
		t.Fatalf("remove branch DB: %v", err)
	}

	reopened, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("recover transactions: %v", err)
	}
	if _, err := restarted.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{}); err != nil {
		t.Fatalf("begin transaction after missing-branch cleanup: %v", err)
	}
}

func TestRecoverOpenTransactionsMainLookupFailuresAbort(t *testing.T) {
	for _, operation := range []string{
		"lookup lock", "restore", "clean", "list entities", "read entities", "list edges", "read edges",
	} {
		t.Run(operation, func(t *testing.T) {
			ctx := testCtx()
			st, err := openTestStore(t)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			gs, err := gitstore.New(t.TempDir())
			if err != nil {
				t.Fatalf("open git store: %v", err)
			}
			opPub, _ := generateTestKey()
			setup := NewCartographerServer(
				st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
			)
			applyTestSchema(ctx, t, st)
			if err := gs.WithGitLock(func() error {
				if err := gs.WriteEntityFiles(ctx, "Component", []gitstore.Entity{{
					ID: "11111111-1111-4111-8111-111111111111", Type: "Component",
					Properties: map[string]string{"name": "main"},
				}}); err != nil {
					return err
				}
				if err := gs.WriteEdgeFiles(ctx, "DEPENDS_ON", []gitstore.Edge{{
					ID: "22222222-2222-4222-8222-222222222222", Type: "DEPENDS_ON",
					FromEntityID: "33333333-3333-4333-8333-333333333333",
					ToEntityID:   "44444444-4444-4444-8444-444444444444",
				}}); err != nil {
					return err
				}
				if err := gs.AddAll(ctx, "."); err != nil {
					return err
				}
				return gs.Commit(ctx, "recovery fixtures")
			}); err != nil {
				t.Fatalf("write git fixtures: %v", err)
			}
			begin, err := setup.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
			if err != nil {
				t.Fatalf("begin transaction: %v", err)
			}
			failing := &recoveryFailingGitStore{GitStore: gs, fail: operation}
			restarted := NewCartographerServer(
				st, failing, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
			)
			if err := restarted.RecoverOpenTransactions(ctx); err == nil {
				t.Fatalf("expected %s failure", operation)
			}
			if _, unlock, err := restarted.txManager.LockActive(begin.TransactionId); status.Code(err) != codes.NotFound {
				t.Fatalf("recovery registered transaction after lookup failure: %v", err)
			} else if unlock != nil {
				unlock()
			}
		})
	}
}

func TestRecoverOpenTransactionsIdenticalCleanupIsRetryable(t *testing.T) {
	for _, operation := range []string{"restore", "clean", "drop", "delete"} {
		t.Run(operation, func(t *testing.T) {
			ctx := testCtx()
			st, err := openTestStore(t)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			gs, err := gitstore.New(t.TempDir())
			if err != nil {
				t.Fatalf("open git store: %v", err)
			}
			opPub, _ := generateTestKey()
			setup := NewCartographerServer(
				st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
			)
			begin, err := setup.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
			if err != nil {
				t.Fatalf("begin transaction: %v", err)
			}
			failingGit := &recoveryFailingGitStore{GitStore: gs}
			var recoveryStore = st
			if operation == "drop" {
				recoveryStore = &dropFailingStore{Store: st, failDrop: true}
			} else {
				failingGit.fail = operation
			}
			restarted := NewCartographerServer(
				recoveryStore, failingGit, opPub, initTestKey(), nil, "", 30*time.Second,
				"test-ns", 30*time.Minute, 100000,
			)
			if err := restarted.RecoverOpenTransactions(ctx); err == nil {
				t.Fatalf("expected %s cleanup failure", operation)
			}
			if operation == "drop" {
				exists, branchErr := gs.BranchExists(ctx, begin.TransactionId)
				if branchErr != nil || !exists {
					t.Fatalf("drop failure lost recovery anchor: exists=%v err=%v", exists, branchErr)
				}
				restarted = NewCartographerServer(
					st, gs, opPub, initTestKey(), nil, "", 30*time.Second,
					"test-ns", 30*time.Minute, 100000,
				)
			}
			if err := restarted.RecoverOpenTransactions(ctx); err != nil {
				t.Fatalf("retry cleanup after %s failure: %v", operation, err)
			}
			if err := gs.WithGitLock(func() error {
				exists, err := gs.BranchExists(ctx, begin.TransactionId)
				if err != nil {
					return err
				}
				if exists {
					return errors.New("transaction branch still exists")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDeleteEdge_Valid(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	svc, _ := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, txID)
	comp, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "core"}, nil, txID)

	createResp, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  svc.Id,
		ToEntityId:    comp.Id,
		Properties:    map[string]string{"weight": "high"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEdge failed: %v", err)
	}

	deleteResp, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: createResp.EdgeId, TransactionId: txID})
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
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)

	// Capabilities missing READ:graph/tx.
	ctx := capabilityContext("WRITE:graph/entity/*,WRITE:graph/tx", scPriv, "sidecar")

	_, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{
		TransactionId: "11111111-1111-4111-8111-111111111111",
	})
	if err == nil {
		t.Fatal("expected error for wrong capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

func TestWipeGraph_Clean(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	_, err := srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{})
	if err != nil {
		t.Fatalf("WipeGraph on empty graph failed: %v", err)
	}
}

// TestWipeGraph_CommitsDeletionWithMessageWipe pins the SPEC R2 WipeGraph
// contract "commits the deletion with message \"wipe\"" (SPEC:207): the git
// wipe commit must carry the exact "wipe" message. A regression that changed
// or dropped the wipe commit message (or skipped the commit entirely) would
// fail this test.
func TestWipeGraph_CommitsDeletionWithMessageWipe(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()
	ctx := context.Background()
	applyTestSchema(ctx, t, st)
	// Establish a non-empty git main so the wipe has content to delete.
	commitGitEntity(ctx, t, gs, testMutationEntityID, "pre-wipe")

	if _, err := srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{}); err != nil {
		t.Fatalf("WipeGraph: %v", err)
	}
	logs, err := gs.GitLogOneline(ctx, "wipe")
	if err != nil {
		t.Fatalf("GitLogOneline: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected exactly one wipe commit, got %d: %v", len(logs), logs)
	}
	if !strings.Contains(logs[0], "wipe") {
		t.Fatalf("wipe commit does not carry the SPEC \"wipe\" message: %q", logs[0])
	}
}

// TestWipeGraph_SetsPushNeeded pins the SPEC R10 push contract for WipeGraph's
// wipe commit: the wipe is a mutation-making commit on main ("backing up every
// committed change", SPEC R10), so it must set the sync worker's push-needed
// flag. Without the flag the remote backup retains the pre-wipe graph
// indefinitely, and a manual reprovision from the remote (R10 Init clone)
// would resurrect exactly the data the destructive change deleted.
func TestWipeGraph_SetsPushNeeded(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs}
	srv, fc := newSyncServer(t, syncGit)
	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	ctx := context.Background()
	applyTestSchema(ctx, t, srv.store)
	if _, err := srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{}); err != nil {
		t.Fatalf("WipeGraph: %v", err)
	}
	if !srv.syncWorker.pushNeeded() {
		t.Fatal("push flag not set after WipeGraph's wipe commit")
	}
	// The next timer cycle must deliver the push and clear the flag.
	fc.FireTicker()
	waitFor(t, func() bool { return !srv.syncWorker.pushNeeded() }, "push flag cleared after cycle")
	syncGit.mu.Lock()
	pushCalls := syncGit.pushCalls
	syncGit.mu.Unlock()
	if pushCalls != 1 {
		t.Fatalf("expected exactly 1 push after the wipe, got %d", pushCalls)
	}
}

func TestWipeGraph_WaitsForBeginSetupAndSeesRegisteredTransaction(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	blocking := &beginSetupBlockingStore{Store: base, entered: make(chan struct{}), release: make(chan struct{})}
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		blocking, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
	)
	srv.MarkDBReady()
	ctx := testCtx()
	beginDone := make(chan *flowv1.BeginTransactionResponse, 1)
	beginErr := make(chan error, 1)
	go func() {
		response, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
		beginDone <- response
		beginErr <- err
	}()
	<-blocking.entered
	if srv.txAdmission.TryLock() {
		srv.txAdmission.Unlock()
		t.Fatal("WipeGraph admission unexpectedly succeeded during BeginTransaction setup")
	}
	wipeDone := make(chan error, 1)
	wipeStarted := make(chan struct{})
	go func() {
		close(wipeStarted)
		_, err := srv.WipeGraph(context.Background(), &flowv1.WipeGraphRequest{})
		wipeDone <- err
	}()
	<-wipeStarted
	close(blocking.release)
	begin := <-beginDone
	if err := <-beginErr; err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if err := <-wipeDone; status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected WipeGraph to observe registered transaction, got %v", err)
	}
	if _, err := srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RollbackTransaction: %v", err)
	}
}

func TestBeginTransaction_WaitsUntilWipeGraphCompletes(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	blocking := &wipeBlockingStore{
		Store: base, entered: make(chan struct{}), release: make(chan struct{}), branchSetup: make(chan bool, 1),
	}
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		blocking, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
	)
	srv.MarkDBReady()
	wipeDone := make(chan error, 1)
	go func() {
		_, err := srv.WipeGraph(context.Background(), &flowv1.WipeGraphRequest{})
		wipeDone <- err
	}()
	<-blocking.entered
	if srv.txAdmission.TryRLock() {
		srv.txAdmission.RUnlock()
		t.Fatal("BeginTransaction admission unexpectedly succeeded during WipeGraph")
	}
	beginDone := make(chan *flowv1.BeginTransactionResponse, 1)
	beginErr := make(chan error, 1)
	beginStarted := make(chan struct{})
	ctx := testCtx()
	go func() {
		close(beginStarted)
		response, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
		beginDone <- response
		beginErr <- err
	}()
	<-beginStarted
	close(blocking.release)
	if err := <-wipeDone; err != nil {
		t.Fatalf("WipeGraph: %v", err)
	}
	if wipeCompleted := <-blocking.branchSetup; !wipeCompleted {
		t.Fatal("BeginTransaction reached branch setup before WipeGraph completed its store wipe")
	}
	begin := <-beginDone
	if err := <-beginErr; err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RollbackTransaction: %v", err)
	}
}

// =========================================================================
// 12. CommitTransaction error-path tests
// =========================================================================

func TestCommitTransaction_Divergence(t *testing.T) {
	srv, _ := newTestServer(t)
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

// TestCommitTransaction_EmptyBaselineStaleMainFailsPrecondition pins the
// commit divergence check's fail-closed behavior when no baseline is recorded
// (state.MainHeadAtLastSync == ""): a stale-branch commit must surface step
// 5's FAILED_PRECONDITION ("Commit not up-to-date with main", SPEC:980) — not
// the step-10 INTERNAL merge failure — so the empty-baseline corner cannot
// silently skip the serialisation guard.
func TestCommitTransaction_EmptyBaselineStaleMainFailsPrecondition(t *testing.T) {
	srv, _ := newTestServer(t)
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

	// Drop the recorded baseline to exercise the no-baseline corner, then
	// advance main so the commit is genuinely stale.
	state, _ := srv.txManager.Lookup(txID)
	state.MainHeadAtLastSync = ""
	commitGitEntity(ctx, t, srv.gitstore, testMutationEntityID, "main")

	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: txID})
	if err == nil {
		t.Fatal("expected error for stale-branch commit, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition for empty-baseline stale commit, got %v (%v)", status.Code(err), err)
	}
}

// TestCommitTransaction_AdditiveSchemaPushDoesNotBlockCommit pins the SPEC R9
// commit flow step 1 semantics: a schema push that is additive (new types, new
// properties, rule modifications — SPEC R2/R6 non-destructive) does not make the
// branch DB state incompatible with the current schema, so an in-flight
// transaction commits normally. The previous full-schema-hash equality check
// rejected any schema change — and, because RefreshTransaction never refreshed
// the begin-time hash, permanently wedged the transaction in
// FAILED_PRECONDITION, forcing a rollback.
func TestCommitTransaction_AdditiveSchemaPushDoesNotBlockCommit(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	// Begin a transaction.
	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	// Add a change so we're not zero-mutation.
	created, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "test"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	// Push an additive schema between begin and commit: a new property on an
	// existing type, a new entity type, and rule/edge declarations.
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

	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: txID}); err != nil {
		t.Fatalf("commit after additive schema push should succeed: %v", err)
	}
	if _, err := st.GetEntity(ctx, created.EntityId, "main"); err != nil {
		t.Fatalf("committed entity missing from main after additive schema push: %v", err)
	}
}

// incompatibleBranchSchemaStore simulates a branch whose schema is incompatible
// with the current (main) schema. The store's own ApplySchema rejects
// destructive changes (ErrDestructiveSchemaChange), so this state cannot arise
// through public APIs; the wrapper pins the commit-time mapping of the store's
// detection to the SPEC error-table row "Schema changed incompatibly during tx"
// (FAILED_PRECONDITION).
type incompatibleBranchSchemaStore struct {
	store.Store
}

func (s *incompatibleBranchSchemaStore) CheckBranchSchemaCompatibility(context.Context, string) error {
	return fmt.Errorf("%w: simulated incompatible schema", store.ErrDestructiveSchemaChange)
}

func TestCommitTransaction_IncompatibleSchemaBlocksCommit(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "test"},
		TransactionId: txID,
	}); err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	// From here on the store reports an incompatible branch schema, exercising
	// the service's mapping of ErrDestructiveSchemaChange to the SPEC
	// FAILED_PRECONDITION row.
	srv.store = &incompatibleBranchSchemaStore{Store: srv.store}

	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: txID})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "schema changed incompatibly") {
		t.Fatalf("expected FailedPrecondition schema-incompatible error, got %v", err)
	}
}

// =========================================================================
// 13. ExtendTimeout validation tests
// =========================================================================

func TestExtendTimeout_NonPositiveDuration(t *testing.T) {
	srv, _ := newTestServer(t)
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
	srv, _ := newTestServer(t)
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

func TestExtendTimeout_AcceptedAt7DayBoundary(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	// Replace with a fake clock so the boundary is deterministic.
	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(7*24*time.Hour, 100000)
	srv.txManager.clock = fc

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(1 * time.Minute),
	})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	// created at the fake now, so a lifetime of exactly 7 days is
	// totalLifetime == hardMaxTimeout, which strict `>` accepts.
	_, err = srv.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{
		TransactionId: txID,
		Duration:      durationpb.New(7 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("expected 7-day-boundary extend to be accepted, got %v", err)
	}
}

// TestExtendTimeout_PersistFailureRevertsInMemoryState pins the
// revert-on-persist-failure contract for ExtendTimeout: when the durable branch
// state write fails, the RPC reports the failure and the in-memory
// ExpiresAt/AppliedTimeout mutations are reverted, so no silent
// in-memory/durable divergence exists (recovery on restart restores the
// persisted, un-extended timeout — the in-memory state must match it).
func TestExtendTimeout_PersistFailureRevertsInMemoryState(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	failingStore := &transactionStateFailingStore{
		Store: base,
		// Fail only the ExtendTimeout persist (the state carrying the extended
		// 10m AppliedTimeout), not BeginTransaction's own initial persist.
		fail: func(state store.BranchTransactionState) bool {
			return state.AppliedTimeout == 10*time.Minute
		},
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(failingStore, gs, opPub, initTestKey(), nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()
	ctx := testCtx()

	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	txID := begin.TransactionId
	state, err := srv.txManager.Lookup(txID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	oldExpiresAt := state.ExpiresAt
	oldAppliedTimeout := state.AppliedTimeout

	_, err = srv.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{
		TransactionId: txID,
		Duration:      durationpb.New(10 * time.Minute),
	})
	if err == nil {
		t.Fatal("expected persist failure to be reported, got nil")
	}
	if state.ExpiresAt != oldExpiresAt || state.AppliedTimeout != oldAppliedTimeout {
		t.Fatalf("in-memory expiry mutated despite persist failure: expiresAt=%v (want %v) applied=%v (want %v)",
			state.ExpiresAt, oldExpiresAt, state.AppliedTimeout, oldAppliedTimeout)
	}
	// The transaction must still be usable with its original expiry.
	if _, unlock, err := srv.txManager.LockActive(txID); err != nil {
		t.Fatalf("transaction unusable after reverted extend failure: %v", err)
	} else {
		unlock()
	}
}

// =========================================================================
// 14. RollbackTransaction NOT_FOUND test
// =========================================================================

func TestRollbackTransaction_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	_, err := srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
		TransactionId: "11111111-1111-4111-8111-111111111111",
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

// TestBeginTransaction_TimeoutValidation asserts the SPEC R2 (SPEC:207), R9
// (SPEC:557-559), and error-table row 917 contract for BeginTransaction: a
// requested timeout that is non-positive or exceeds the 7-day hard maximum is
// rejected with INVALID_ARGUMENT — no silent capping (the previous behavior)
// and no silent default-substitution — mirroring ExtendTimeout. Exactly 7 days
// is accepted (strict > comparison, matching TestExtendTimeout_AcceptedAt7DayBoundary).
func TestBeginTransaction_TimeoutValidation(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()
	ctx := testCtx()

	// Request a timeout far exceeding the hard max (7 days): rejected, not capped.
	_, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(14 * 24 * time.Hour),
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for over-max timeout, got %v (%v)", status.Code(err), err)
	}

	// Non-positive timeouts are rejected, not silently defaulted.
	for _, d := range []time.Duration{0, -1 * time.Minute} {
		_, err = srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
			Timeout: durationpb.New(d),
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument for timeout %v, got %v (%v)", d, status.Code(err), err)
		}
	}

	// Exactly 7 days is the accepted boundary, and the applied timeout surfaces
	// the granted value verbatim.
	resp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(7 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("expected 7-day-boundary begin to be accepted, got %v", err)
	}
	if resp.AppliedTimeout.AsDuration() != 7*24*time.Hour {
		t.Fatalf("expected applied timeout 7d, got %v", resp.AppliedTimeout.AsDuration())
	}
}

func TestBeginTransaction_ResourceExhausted(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub := initTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
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

// TestBeginTransaction_GitBranchCreationResourceExhausted pins the SPEC
// error-table row "BeginTransaction resource exhausted" → RESOURCE_EXHAUSTED
// ("Out of file handles, memory, or disk space; branch or LadybugDB creation
// failed"): a git-side branch-creation failure (CreateBranch, HardResetToBranch
// — e.g. disk full) must surface RESOURCE_EXHAUSTED, matching the store-side
// branch-DB path, not INTERNAL via mapGitError.
func TestBeginTransaction_GitBranchCreationResourceExhausted(t *testing.T) {
	tests := []struct {
		name   string
		failFn func(*cleanupFailingGitStore)
	}{
		{"CreateBranch", func(g *cleanupFailingGitStore) { g.failCreateBranch = true }},
		{"HardResetToBranch", func(g *cleanupFailingGitStore) { g.failHardReset = true }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opPub, _ := generateTestKey()
			scPub := initTestKey()
			st, _ := openTestStore(t)
			t.Cleanup(func() { _ = st.Close() })
			gs, _ := gitstore.New(t.TempDir())
			srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
				30*time.Second, "test-ns", 30*time.Minute, 100000)
			srv.MarkDBReady()

			failing := &cleanupFailingGitStore{GitStore: gs}
			tt.failFn(failing)
			srv.gitstore = failing

			ctx := testCtx()
			_, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if status.Code(err) != codes.ResourceExhausted {
				t.Fatalf("expected ResourceExhausted for git branch creation failure, got %v", status.Code(err))
			}
			if !strings.Contains(err.Error(), "simulated "+tt.name+" failure") {
				t.Fatalf("error should contain original failure, got: %v", err)
			}
		})
	}
}

// cleanupFailingStore fails on CreateBranchDB and on DropBranchDB to test
// that cleanup failures during BeginTransaction are surfaced.
type cleanupFailingStore struct {
	store.Store
	failDrop bool
}

func (s *cleanupFailingStore) CreateBranchDB(context.Context, string) error {
	return fmt.Errorf("simulated CreateBranchDB failure")
}

func (s *cleanupFailingStore) DropBranchDB(ctx context.Context, txID string) error {
	if s.failDrop {
		return fmt.Errorf("simulated DropBranchDB failure")
	}
	return s.Store.DropBranchDB(ctx, txID)
}

// cleanupFailingGitStore fails on specified git operations to test
// that cleanup failures during BeginTransaction are surfaced.
type cleanupFailingGitStore struct {
	gitstore.GitStore
	failRestore      bool
	failClean        bool
	failDelete       bool
	failCreateBranch bool
	failHardReset    bool
}

func (s *cleanupFailingGitStore) CreateBranch(ctx context.Context, txID string) error {
	if s.failCreateBranch {
		return fmt.Errorf("simulated CreateBranch failure")
	}
	return s.GitStore.CreateBranch(ctx, txID)
}

func (s *cleanupFailingGitStore) HardResetToBranch(ctx context.Context, branch string) error {
	if s.failHardReset {
		return fmt.Errorf("simulated HardResetToBranch failure")
	}
	return s.GitStore.HardResetToBranch(ctx, branch)
}

func (s *cleanupFailingGitStore) RestoreMain(ctx context.Context) error {
	if s.failRestore {
		return fmt.Errorf("simulated RestoreMain failure")
	}
	return s.GitStore.RestoreMain(ctx)
}

func (s *cleanupFailingGitStore) CleanUntracked(ctx context.Context) error {
	if s.failClean {
		return fmt.Errorf("simulated CleanUntracked failure")
	}
	return s.GitStore.CleanUntracked(ctx)
}

func (s *cleanupFailingGitStore) DeleteBranch(ctx context.Context, txID string) error {
	if s.failDelete {
		return fmt.Errorf("simulated DeleteBranch failure")
	}
	return s.GitStore.DeleteBranch(ctx, txID)
}

func TestBeginTransaction_SurfacesDropBranchDBFailure(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub := initTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	srv.store = &cleanupFailingStore{Store: st, failDrop: true}

	ctx := testCtx()
	_, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v", status.Code(err))
	}
	if !strings.Contains(err.Error(), "simulated CreateBranchDB failure") {
		t.Fatalf("error should contain original failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "simulated DropBranchDB failure") {
		t.Fatalf("error should surface DropBranchDB cleanup failure, got: %v", err)
	}
}

func TestBeginTransaction_SurfacesCleanupFailures(t *testing.T) {
	tests := []struct {
		name      string
		failField string // "restore", "clean", "delete"
		wantMsg   string
	}{
		{"RestoreMain", "restore", "simulated RestoreMain failure"},
		{"CleanUntracked", "clean", "simulated CleanUntracked failure"},
		{"DeleteBranch", "delete", "simulated DeleteBranch failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opPub, _ := generateTestKey()
			scPub := initTestKey()
			st, _ := openTestStore(t)
			t.Cleanup(func() { _ = st.Close() })
			gs, _ := gitstore.New(t.TempDir())
			srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
				30*time.Second, "test-ns", 30*time.Minute, 100000)
			srv.MarkDBReady()

			srv.store = &cleanupFailingStore{Store: st}
			srv.gitstore = &cleanupFailingGitStore{GitStore: gs,
				failRestore: tt.failField == "restore",
				failClean:   tt.failField == "clean",
				failDelete:  tt.failField == "delete",
			}

			ctx := testCtx()
			_, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if status.Code(err) != codes.ResourceExhausted {
				t.Fatalf("expected ResourceExhausted, got %v", status.Code(err))
			}
			if !strings.Contains(err.Error(), "simulated CreateBranchDB failure") {
				t.Fatalf("error should contain original failure, got: %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("error should surface %s cleanup failure, got: %v", tt.name, err)
			}
		})
	}
}

func TestBeginTransaction_SurfacesMultipleCleanupFailures(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub := initTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	srv.store = &cleanupFailingStore{Store: st, failDrop: true}
	srv.gitstore = &cleanupFailingGitStore{GitStore: gs, failRestore: true, failClean: true, failDelete: true}

	ctx := testCtx()
	_, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v", status.Code(err))
	}
	if !strings.Contains(err.Error(), "simulated CreateBranchDB failure") {
		t.Fatalf("error should contain original failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "simulated DropBranchDB failure") {
		t.Fatalf("error should surface DropBranchDB cleanup failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "simulated RestoreMain failure") {
		t.Fatalf("error should surface RestoreMain cleanup failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "simulated CleanUntracked failure") {
		t.Fatalf("error should surface CleanUntracked cleanup failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "simulated DeleteBranch failure") {
		t.Fatalf("error should surface DeleteBranch cleanup failure, got: %v", err)
	}
}

func TestBeginTransaction_SurfacesTxManagerCreateCleanupFailures(t *testing.T) {
	// When txManager.Create fails, BeginTransaction attempts to clean up
	// the git branch and branch DB. This test verifies those cleanup
	// failures are surfaced by pre-registering the txID in the manager.
	opPub, _ := generateTestKey()
	scPub := initTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Pre-register a txID so txManager.Create fails with "already exists".
	fixedID := "00000000-0000-4000-8000-000000000001"
	srv.txManager.active[fixedID] = &TransactionState{ID: fixedID}
	srv.newIDFn = func() string { return fixedID }

	srv.gitstore = &cleanupFailingGitStore{GitStore: gs, failDelete: true}

	ctx := testCtx()
	_, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v", status.Code(err))
	}
	if !strings.Contains(err.Error(), "simulated DeleteBranch failure") {
		t.Fatalf("error should surface DeleteBranch cleanup failure, got: %v", err)
	}
}

func TestBeginTransaction_PersistStateFailure_CleanupSuccess(t *testing.T) {
	// Path C1: persistTransactionState fails, cleanupTransaction succeeds.
	// Only the persist error should be returned.
	opPub, _ := generateTestKey()
	scPub := initTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Fail on the first SaveBranchTransactionState call.
	srv.store = &transactionStateFailingStore{
		Store: st,
		fail:  func(store.BranchTransactionState) bool { return true },
	}

	ctx := testCtx()
	_, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v", status.Code(err))
	}
	if !strings.Contains(err.Error(), "persist transaction state") {
		t.Fatalf("error should contain persist failure, got: %v", err)
	}
	if strings.Contains(err.Error(), "cleanup") {
		t.Fatalf("error should NOT contain cleanup failure when cleanup succeeds, got: %v", err)
	}
}

func TestBeginTransaction_PersistStateFailure_CleanupFails(t *testing.T) {
	// Path C2: persistTransactionState fails, cleanupTransaction also fails.
	// Both errors should be aggregated.
	opPub, _ := generateTestKey()
	scPub := initTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Fail on the first SaveBranchTransactionState call.
	failingStore := &transactionStateFailingStore{
		Store: st,
		fail:  func(store.BranchTransactionState) bool { return true },
	}
	// Also make DropBranchDB fail so cleanupTransaction fails.
	srv.store = &dropFailingStore{Store: failingStore, failDrop: true}

	ctx := testCtx()
	_, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v", status.Code(err))
	}
	if !strings.Contains(err.Error(), "persist transaction state") {
		t.Fatalf("error should contain persist failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "cleanup") {
		t.Fatalf("error should contain cleanup failure when cleanup fails, got: %v", err)
	}
}

// lockObservationGitStore tracks when WithGitLock's closure runs so a test can
// assert the schema hash is computed while the git lock is held.
type lockObservationGitStore struct {
	gitstore.GitStore
	lockHeld *atomic.Bool
}

func (g *lockObservationGitStore) WithGitLock(fn func() error) error {
	g.lockHeld.Store(true)
	defer g.lockHeld.Store(false)
	return g.GitStore.WithGitLock(fn)
}

// lockObservationStore records whether the store's schema lookups (which
// computeSchemaHash performs) happen while the git lock is held.
type lockObservationStore struct {
	store.Store
	lockHeld *atomic.Bool
	mu       sync.Mutex
	locked   int
	unlocked int
}

func (s *lockObservationStore) EntityType(name string) (*store.EntityTypeDef, bool) {
	s.record()
	return s.Store.EntityType(name)
}

func (s *lockObservationStore) EdgeType(name string) (*store.EdgeTypeDef, bool) {
	s.record()
	return s.Store.EdgeType(name)
}

func (s *lockObservationStore) record() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lockHeld.Load() {
		s.locked++
	} else {
		s.unlocked++
	}
}

// TestBeginTransaction_SchemaHashCapturedUnderGitLock pins the fix for the
// BeginTransaction schema-hash data race: the persisted SchemaHash must be
// computed while holding the git lock, because re-hydration
// (RehydrateMainFromFiles) promotes vector-enabled flags on the store's shared
// schema defs in place (ensureEmbeddingLoadSchema) under the same git lock.
// Computing the hash outside the lock races that in-place mutation and
// persists a nondeterministic SchemaHash into branch state. The BeginTransaction
// flow's only schema-def reads come from computeSchemaHash, so the observation
// wrapper can assert they all happen under the git lock.
func TestBeginTransaction_SchemaHashCapturedUnderGitLock(t *testing.T) {
	st, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	lockHeld := &atomic.Bool{}
	observedStore := &lockObservationStore{Store: st, lockHeld: lockHeld}
	observedGit := &lockObservationGitStore{GitStore: gs, lockHeld: lockHeld}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(observedStore, observedGit, opPub, initTestKey(), nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, st)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	observedStore.mu.Lock()
	locked, unlocked := observedStore.locked, observedStore.unlocked
	observedStore.mu.Unlock()
	if unlocked != 0 {
		t.Fatalf("schema defs read outside the git lock: locked=%d unlocked=%d", locked, unlocked)
	}
	if locked == 0 {
		t.Fatal("schema defs never read under the git lock")
	}
	// The persisted SchemaHash must equal the hash of the current schema.
	state, lookupErr := srv.txManager.Lookup(begin.TransactionId)
	if lookupErr != nil {
		t.Fatalf("lookup transaction: %v", lookupErr)
	}
	if want := computeSchemaHash(st); state.SchemaHash != want {
		t.Fatalf("persisted SchemaHash %q does not match the current schema hash %q", state.SchemaHash, want)
	}
}

// =========================================================================
// 16. ExecuteCypher invalid syntax test
// =========================================================================

func TestExecuteCypher_InvalidSyntax(t *testing.T) {
	srv, _ := newTestServer(t)
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
	srv, _ := newTestServer(t)
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
	srv, _ := newTestServer(t)
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

// TestSearchNeighbors_TopKZeroDefaultsTo10 pins the SPEC error-table boundary
// "topK is negative (zero is treated as omitted and defaults to 10)" (row
// "Invalid topK in SearchNeighbors"): a zero topK is accepted and behaves like
// the default of 10, not an error. Verified against a graph with more indexed
// entities than the default: all 10 nearest neighbors are returned and no
// more.
func TestSearchNeighbors_TopKZeroDefaultsTo10(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

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
	// 12 indexed entities so the result set exceeds the default topK of 10.
	// Each embedding is distinct; the query embedding matches the first, whose
	// distance 0 is the nearest.
	for i := range 12 {
		vec := make([]float32, 12)
		vec[i] = 1.0
		if _, err := srv.store.CreateEntity(ctx, "VectorType", "",
			map[string]string{"name": fmt.Sprintf("entity-%d", i)}, vec, ""); err != nil {
			t.Fatalf("CreateEntity %d: %v", i, err)
		}
	}
	query := make([]float32, 12)
	query[0] = 1.0

	resp, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  query,
		EntityType: "VectorType",
		TopK:       0,
	})
	if err != nil {
		t.Fatalf("SearchNeighbors with zero topK failed: %v", err)
	}
	if len(resp.Results) != 10 {
		t.Fatalf("expected 10 results with zero topK (default), got %d", len(resp.Results))
	}
}

func TestSearchNeighbors_NaNEmbedding(t *testing.T) {
	srv, st := newTestServer(t)
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

// TestSearchNeighbors_NaNEmbeddingNonIndexed pins SPEC R7: the NaN/Inf
// embedding rejection applies "regardless of indexing status" — the service
// layer rejects a NaN/Inf embedding for a non-indexed entity type before the
// store's non-indexed-type rejection (also INVALID_ARGUMENT) could surface.
func TestSearchNeighbors_NaNEmbeddingNonIndexed(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	// Component is a non-indexed type (enableVectorIndex not set).
	applyTestSchema(ctx, t, srv.store)

	_, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{float32(math.NaN()), 0.0, 0.0},
		EntityType: "Component",
		TopK:       5,
	})
	if err == nil {
		t.Fatal("expected error for NaN embedding on non-indexed type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
	}
	if !strings.Contains(err.Error(), "NaN") {
		t.Fatalf("expected the NaN-embedding rejection, got %v", err)
	}
}

func TestSearchNeighbors_EmbeddingDimensionMismatch(t *testing.T) {
	srv, st := newTestServer(t)
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
	srv, _ := newTestServer(t)
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
	srv, _ := newTestServer(t)
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

// TestListEntities_PageSizeZeroDefaultsTo1000 pins the SPEC error-table
// boundary "pageSize of 0 is treated as omitted and defaults to 1000" (row
// "Invalid pageSize in ListEntities"): a zero pageSize is accepted and behaves
// like the default, not an error. Verified by listing a graph larger than the
// default page size and asserting every entity is returned.
func TestListEntities_PageSizeZeroDefaultsTo1000(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	// 1005 entities exceeds the 1000 default page size.
	for i := range 1005 {
		_, err := srv.store.CreateEntity(ctx, "Component", "",
			map[string]string{"name": fmt.Sprintf("entity-%d", i)}, nil, "")
		if err != nil {
			t.Fatalf("CreateEntity %d: %v", i, err)
		}
	}

	resp, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{EntityType: "Component"})
	if err != nil {
		t.Fatalf("ListEntities with zero pageSize failed: %v", err)
	}
	if len(resp.Entities) != 1000 {
		t.Fatalf("expected 1000 entities with zero pageSize (default), got %d", len(resp.Entities))
	}
	if resp.NextPageToken == "" {
		t.Fatal("expected a next-page token when the graph exceeds the default page size")
	}
}

func TestListEntities_InvalidPageToken(t *testing.T) {
	srv, _ := newTestServer(t)
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
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	svc, _ := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, txID)

	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  svc.Id,
		ToEntityId:    "11111111-1111-4111-8111-111111111111",
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for target not found, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

func TestCreateEdge_RuleViolation(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	// Service has rules permitting connection to Component via DEPENDS_ON,
	// but Component has no rules defined.
	comp, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "comp"}, nil, txID)
	svc, _ := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, txID)

	// Attempt edge FROM Component (no rules) TO Service.
	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  comp.Id,
		ToEntityId:    svc.Id,
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for rule violation, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

// TestCreateEdge_SelfReferencing verifies SPEC R1's self-referencing allowance
// at the service layer: an entity type appearing in its own canConnectTo list
// (Component → Component) must admit an edge, with the membership check treating
// the declaring type the same as any other. Only Service → Component rule tests
// were previously exercised at the service layer.
func TestCreateEdge_SelfReferencing(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

	// Component declares a self-referencing rule via DEPENDS_ON.
	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:       "Component",
				Properties: []*flowv1.Property{{Name: "name", Type: "string", Required: true}},
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "DEPENDS_ON"}},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	txID := beginTestTx(t, srv, ctx)
	from, _ := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "from"}, nil, txID)
	to, _ := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "to"}, nil, txID)

	resp, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType: "DEPENDS_ON", FromEntityId: from.Id, ToEntityId: to.Id, TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("self-referencing Component->Component edge must be allowed: %v", err)
	}
	if resp.EdgeId == "" {
		t.Fatal("expected non-empty edge ID")
	}
}

func TestCreateEdge_MissingRequiredProperty(t *testing.T) {
	srv, st := newTestServer(t)
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

	txID := beginTestTx(t, srv, ctx)
	src, _ := srv.store.CreateEntity(ctx, "Source", "", map[string]string{"name": "src"}, nil, txID)
	tgt, _ := srv.store.CreateEntity(ctx, "Target", "", map[string]string{"name": "tgt"}, nil, txID)

	// Missing required property "label".
	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "LINKED",
		FromEntityId:  tgt.Id,
		ToEntityId:    src.Id,
		Properties:    map[string]string{},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for missing required property, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEdge_UnknownProperty(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	svc, _ := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, txID)
	comp, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "core"}, nil, txID)

	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  svc.Id,
		ToEntityId:    comp.Id,
		Properties:    map[string]string{"unknownprop": "x"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for unknown property, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// TestCreateEdge_StructuralErrorBeforeEntityExistence asserts the SPEC RPC
// check-order (CreateEdge: structural → entity existence): a request carrying
// BOTH a missing source entity AND a structurally invalid edge property surfaces
// INVALID_ARGUMENT (structural), not the NOT_FOUND the entity-existence probe
// would otherwise return.
func TestCreateEdge_StructuralErrorBeforeEntityExistence(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

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
	txID := beginTestTx(t, srv, ctx)
	tgt, _ := srv.store.CreateEntity(ctx, "Target", "", map[string]string{"name": "tgt"}, nil, txID)

	missingSource := "11111111-1111-4111-8111-111111111111"

	// Missing required property + missing source → INVALID_ARGUMENT, not NOT_FOUND.
	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "LINKED",
		FromEntityId:  missingSource,
		ToEntityId:    tgt.Id,
		TransactionId: txID,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for missing required property + missing source, got %v", status.Code(err))
	}

	// Unknown property + missing source → INVALID_ARGUMENT, not NOT_FOUND.
	_, err = srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "LINKED",
		FromEntityId:  missingSource,
		ToEntityId:    tgt.Id,
		Properties:    map[string]string{"label": "x", "bogus": "y"},
		TransactionId: txID,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for unknown property + missing source, got %v", status.Code(err))
	}

	// Structurally valid + missing source → NOT_FOUND (entity existence is the
	// next check in order).
	_, err = srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "LINKED",
		FromEntityId:  missingSource,
		ToEntityId:    tgt.Id,
		Properties:    map[string]string{"label": "x"},
		TransactionId: txID,
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound for structurally-valid missing source, got %v", status.Code(err))
	}
}

func TestCreateEdge_InvalidIDFormat(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)

	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  "not-a-uuid",
		ToEntityId:    "11111111-1111-4111-8111-111111111111",
		TransactionId: txID,
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
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	ent, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "test"}, nil, txID)

	_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:            ent.Id,
		Properties:    map[string]string{"nonexistent": "value"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for unknown property, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestUpdateEntity_InvalidIDFormat(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)

	_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:            "not-a-uuid",
		Properties:    map[string]string{"name": "x"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for invalid ID format, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestUpdateEntity_NaNEmbedding(t *testing.T) {
	srv, st := newTestServer(t)
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
	txID := beginTestTx(t, srv, ctx)
	ent, _ := srv.store.CreateEntity(ctx, "VecType", "", map[string]string{"name": "a"}, []float32{1.0, 0.0, 0.0}, txID)

	_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:            ent.Id,
		Properties:    map[string]string{"name": "b"},
		Embedding:     []float32{float32(math.NaN()), 0.0, 0.0},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for NaN embedding, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// TestUpdateEntity_NaNEmbeddingNonIndexed pins SPEC R7: the NaN/Inf embedding
// rejection applies "regardless of indexing status" — a non-indexed entity
// type must still reject a NaN/Inf embedding at the service layer (the store's
// NaN guard runs unconditionally, before any EnableVectorIndex branching).
func TestUpdateEntity_NaNEmbeddingNonIndexed(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	// Component is a non-indexed type (enableVectorIndex not set).
	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	ent, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "a"}, nil, txID)

	_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:            ent.Id,
		Properties:    map[string]string{"name": "b"},
		Embedding:     []float32{float32(math.NaN()), 0.0, 0.0},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for NaN embedding on non-indexed type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
	}
	if !strings.Contains(err.Error(), "NaN") {
		t.Fatalf("expected the NaN-embedding rejection, got %v", err)
	}
}

func TestUpdateEntity_EmbeddingDimensionMismatch(t *testing.T) {
	srv, st := newTestServer(t)
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
	txID := beginTestTx(t, srv, ctx)
	ent, _ := srv.store.CreateEntity(ctx, "VecType", "", map[string]string{"name": "a"}, []float32{1.0, 0.0, 0.0}, txID)

	_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:            ent.Id,
		Properties:    map[string]string{"name": "b"},
		Embedding:     []float32{1.0, 0.0}, // only 2 dims, expected 3
		TransactionId: txID,
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
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)

	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "test", "nonexistent": "value"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for unknown property, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEntity_InvalidIDFormat(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)

	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Id:            "not-a-uuid",
		Properties:    map[string]string{"name": "test"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for invalid ID format, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEntity_NaNEmbedding(t *testing.T) {
	srv, st := newTestServer(t)
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
	txID := beginTestTx(t, srv, ctx)

	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "VecType",
		Properties:    map[string]string{"name": "bad"},
		Embedding:     []float32{float32(math.NaN()), 0.0, 0.0},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for NaN embedding, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEntity_NaNEmbeddingNonIndexed(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	// Component is a non-indexed type (enableVectorIndex not set).
	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)

	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "bad"},
		Embedding:     []float32{float32(math.NaN()), 0.0, 0.0},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for NaN embedding on non-indexed type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEntity_EmbeddingDimensionMismatch(t *testing.T) {
	srv, st := newTestServer(t)
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
	txID := beginTestTx(t, srv, ctx)
	// Bootstrap with a 3-dim entity in the transaction branch.
	_, _ = srv.store.CreateEntity(ctx, "VecType", "", map[string]string{"name": "a"}, []float32{1.0, 0.0, 0.0}, txID)

	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "VecType",
		Properties:    map[string]string{"name": "b"},
		Embedding:     []float32{1.0, 0.0}, // only 2 dims
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for dimension mismatch, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEntity_VectorBootstrap(t *testing.T) {
	srv, st := newTestServer(t)
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
	txID := beginTestTx(t, srv, ctx)

	// First entity must include an embedding to bootstrap the vector dimension.
	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "VecType",
		Properties:    map[string]string{"name": "no-embedding"},
		TransactionId: txID,
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
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
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

// TestWipeGraph_GitSideMidWipeFailure pins SPEC error-table row 940 ("WipeGraph
// mid-wipe failure → INTERNAL") for the git-side wipe steps: git rm entities,
// the "wipe" commit, and clean untracked. These failures were previously
// returned as raw plain errors, which grpc-go converted to codes.Unknown; only
// the store-side failure produced INTERNAL.
func TestWipeGraph_GitSideMidWipeFailure(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	for _, tc := range []struct {
		name      string
		configure func(*wipeFailingGitStore)
	}{
		{"git rm entities", func(g *wipeFailingGitStore) { g.failGitRm = true }},
		{"wipe commit", func(g *wipeFailingGitStore) { g.failCommit = true }},
		{"clean untracked", func(g *wipeFailingGitStore) { g.failClean = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := openTestStore(t)
			t.Cleanup(func() { _ = st.Close() })
			gs, _ := gitstore.New(t.TempDir())
			failingGit := &wipeFailingGitStore{GitStore: gs}
			tc.configure(failingGit)
			srv := NewCartographerServer(st, failingGit, opPub, scPub, nil, "",
				30*time.Second, "test-ns", 30*time.Minute, 100000)
			srv.MarkDBReady()

			_, err := srv.WipeGraph(context.Background(), &flowv1.WipeGraphRequest{})
			if err == nil {
				t.Fatal("expected error for git-side mid-wipe failure, got nil")
			}
			if status.Code(err) != codes.Internal {
				t.Fatalf("expected Internal, got %v", status.Code(err))
			}
		})
	}
}

// =========================================================================
// 24. ApplySchema error-path tests
// =========================================================================

func TestApplySchema_DestructiveChange(t *testing.T) {
	srv, st := newTestServer(t)
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

	// Re-apply schema with a property removed (destructive).
	destructive := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{
				{Name: "name", Type: "string"},
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
	if !strings.Contains(err.Error(), "destructive") {
		t.Fatalf("expected destructive schema change error, got %v", err)
	}

	// After WipeGraph, destructive change should succeed.
	if _, err := srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{}); err != nil {
		t.Fatalf("WipeGraph failed: %v", err)
	}
	if _, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: destructive}); err != nil {
		t.Fatalf("ApplySchema after WipeGraph failed: %v", err)
	}

	// Verify the new schema is applied.
	if !st.TableExists("Component") {
		t.Fatal("Component table should exist after ApplySchema")
	}
	// Create an entity with the new schema (no toremove property).
	_, err = st.CreateEntity(ctx, "Component", "", map[string]string{"name": "test"}, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity after wipe+apply failed: %v", err)
	}
}

func TestApplySchema_BeforeDBReady(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
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

// TestApplySchema_InvalidSchemaBeforeDBReady pins the SPEC ApplySchema check
// order (SPEC:1021: database readiness → schema validation → table structure
// mismatch) for the readiness gate: a schema that is ALSO structurally invalid
// (duplicate entity type name → INVALID_ARGUMENT if validation ran) must still
// surface FAILED_PRECONDITION ("ApplySchema called before database ready")
// when the database is not ready — the readiness gate precedes schema
// validation. TestApplySchema_BeforeDBReady uses a valid schema, so only a
// combined fault can detect a reorder that surfaced the validation error first.
func TestApplySchema_InvalidSchemaBeforeDBReady(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := ladybug.Open(t.TempDir())
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	// Do NOT call MarkDBReady.
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	ctx := context.Background()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
			{Name: "Component", Properties: []*flowv1.Property{{Name: "version", Type: "string"}}},
		},
	}
	_, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: schema})
	if err == nil {
		t.Fatal("expected error for ApplySchema before DB ready, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition (readiness gate first), got %v", status.Code(err))
	}
}

// TestApplySchema_InvalidSchemaWinsOverDestructive pins the SPEC ApplySchema
// check order (SPEC:1021: database readiness → schema validation → table
// structure mismatch) for the validation gate: a schema that is both
// structurally invalid (duplicate property name → INVALID_ARGUMENT) and
// destructive (removes an applied property → FAILED_PRECONDITION if the
// structure diff ran) must surface INVALID_ARGUMENT — schema validation
// precedes the table-structure mismatch check.
func TestApplySchema_InvalidSchemaWinsOverDestructive(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	initial := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{
				{Name: "name", Type: "string"},
				{Name: "toremove", Type: "string"},
			}},
		},
	}
	if _, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: initial}); err != nil {
		t.Fatalf("initial ApplySchema failed: %v", err)
	}

	// Combined fault: removes `toremove` (destructive → FAILED_PRECONDITION if
	// the catalog diff ran) and declares `version` twice (invalid →
	// INVALID_ARGUMENT). Validation runs before the structure diff, so
	// INVALID_ARGUMENT must win.
	invalidAndDestructive := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "version", Type: "string"},
			}},
		},
	}
	_, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: invalidAndDestructive})
	if err == nil {
		t.Fatal("expected error for invalid destructive schema, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument (validation gate before structure mismatch), got %v", status.Code(err))
	}
}

func TestApplySchema_AdditiveChange(t *testing.T) {
	srv, _ := newTestServer(t)
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
			{Name: "Service", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}},
				},
			},
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
	srv, _ := newTestServer(t)

	// No data in the graph — export should succeed with an empty result.
	stream := &mockExportStream{
		ctx: capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar"),
	}
	handlerInvoked, err := invokeExportGraph(srv, &flowv1.ExportGraphRequest{Format: "json"}, stream)
	if err != nil {
		t.Fatalf("export empty graph failed: %v", err)
	}
	if !handlerInvoked {
		t.Fatal("stream interceptor did not invoke ExportGraph")
	}
}

// TestExportGraph_JSONShape pins the SPEC R11 (SPEC:619) JSON export shape: the
// graph always serialises with top-level "nodes" and "edges" arrays — an empty
// graph is {"nodes":[],"edges":[]}, not {} — and every node/edge entry carries a
// top-level "properties" key, an empty object when the element has no properties.
func TestExportGraph_JSONShape(t *testing.T) {
	t.Run("empty graph serialises with empty arrays", func(t *testing.T) {
		srv, _ := newTestServer(t)
		stream := &mockExportStream{ctx: capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")}
		handlerInvoked, err := invokeExportGraph(srv, &flowv1.ExportGraphRequest{Format: "json"}, stream)
		if err != nil {
			t.Fatalf("export empty graph failed: %v", err)
		}
		if !handlerInvoked {
			t.Fatal("stream interceptor did not invoke ExportGraph")
		}
		if got, want := string(stream.data), `{"nodes":[],"edges":[]}`; got != want {
			t.Fatalf("empty-graph JSON = %s, want %s", got, want)
		}
	})

	t.Run("entries always carry a properties key", func(t *testing.T) {
		srv, st := newTestServer(t)
		ctx := context.Background()
		if err := st.ApplySchema(ctx, &flowv1.Schema{
			EntityTypes: []*flowv1.EntityType{
				{
					Name: "Loose",
					// No declared properties, so a property-less entity has a
					// genuinely empty properties map (a declared-but-unset
					// property would be materialised by the store instead).
					Rules: []*flowv1.ConnectionRule{{
						CanConnectTo: []string{"Loose"}, Using: []string{"DEPENDS_ON"},
					}},
				},
			},
			EdgeTypes: []*flowv1.EdgeType{{Name: "DEPENDS_ON"}},
		}); err != nil {
			t.Fatalf("ApplySchema: %v", err)
		}
		// Create an entity with no properties and an edge with no properties.
		ent, err := st.CreateEntity(ctx, "Loose", "", nil, nil, "")
		if err != nil {
			t.Fatalf("CreateEntity: %v", err)
		}
		other, err := st.CreateEntity(ctx, "Loose", "", nil, nil, "")
		if err != nil {
			t.Fatalf("CreateEntity other: %v", err)
		}
		if _, err := st.CreateEdge(ctx, "DEPENDS_ON", ent.Id, other.Id, nil, ""); err != nil {
			t.Fatalf("CreateEdge: %v", err)
		}

		stream := &mockExportStream{ctx: capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")}
		if _, err := invokeExportGraph(srv, &flowv1.ExportGraphRequest{Format: "json"}, stream); err != nil {
			t.Fatalf("export failed: %v", err)
		}
		var out struct {
			Nodes []map[string]any `json:"nodes"`
			Edges []map[string]any `json:"edges"`
		}
		if err := json.Unmarshal(stream.data, &out); err != nil {
			t.Fatalf("invalid export JSON: %v", err)
		}
		if len(out.Nodes) != 2 {
			t.Fatalf("expected 2 nodes, got %d", len(out.Nodes))
		}
		for _, node := range out.Nodes {
			props, ok := node["properties"].(map[string]any)
			if !ok {
				t.Fatalf("node entry missing object properties key: %+v", node)
			}
			if len(props) != 0 {
				t.Fatalf("expected empty properties object for property-less node, got %v", props)
			}
		}
		if len(out.Edges) != 1 {
			t.Fatalf("expected 1 edge, got %d", len(out.Edges))
		}
		props, ok := out.Edges[0]["properties"].(map[string]any)
		if !ok {
			t.Fatalf("edge entry missing object properties key: %+v", out.Edges[0])
		}
		if len(props) != 0 {
			t.Fatalf("expected empty properties object for property-less edge, got %v", props)
		}
	})
}

func TestExportGraph_MidStreamFailure(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Add some data so export has content.
	applySchemaCtx := context.Background()
	applyTestSchema(applySchemaCtx, t, srv.store)
	_, _ = srv.store.CreateEntity(applySchemaCtx, "Component", "", map[string]string{"name": "a"}, nil, "")

	stream := &mockExportStream{
		ctx:     capabilityContext("READ:graph/entity/*", scPriv, "sidecar"),
		sendErr: fmt.Errorf("stream send failure"),
	}
	handlerInvoked, err := invokeExportGraph(srv, &flowv1.ExportGraphRequest{Format: "json"}, stream)
	if !handlerInvoked {
		t.Fatal("stream interceptor did not invoke ExportGraph")
	}
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
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Wrap store to panic on ListMainEntityTypes, simulating an OOM.
	srv.store = &panicStore{Store: st}

	stream := &mockExportStream{ctx: capabilityContext("READ:graph/entity/*", scPriv, "sidecar")}
	handlerInvoked, err := invokeExportGraph(srv, &flowv1.ExportGraphRequest{Format: "json"}, stream)
	if !handlerInvoked {
		t.Fatal("stream interceptor did not invoke ExportGraph")
	}
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
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Only WRITE capabilities, no READ.
	ctx := capabilityContext("WRITE:graph/entity/*,WRITE:graph/tx", scPriv, "sidecar")

	// Schema is applied so the unlabelled query parses server-side; the
	// no-labels statement then falls back to the READ:graph/entity/* check,
	// which a write-only caller lacks (SPEC R3 server-authoritative path).
	applyTestSchema(ctx, t, srv.store)

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
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()
	ctx := capabilityContext("READ:graph/entity/*,READ:graph/tx", scPriv, "sidecar")

	applyTestSchema(ctx, t, srv.store)

	// The caller holds no WRITE capability. A registered (valid) transaction
	// lets the request pass the active-transaction gate (SPEC RPC check order:
	// active transaction → structural → capability) so the missing-WRITE
	// capability check is reached and PERMISSION_DENIED surfaces. A
	// nonexistent transaction ID would instead surface NOT_FOUND, which is why
	// a real transaction is required to reach this branch.
	state, err := srv.txManager.Create(testMutationEntityID, 30*time.Minute, "")
	if err != nil {
		t.Fatalf("register transaction: %v", err)
	}
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "x"},
		TransactionId: state.ID,
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
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
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

// TestCommitTransaction_MissingTxCapability pins SPEC R3 (SPEC:244): a caller
// without WRITE:graph/tx is denied CommitTransaction with PERMISSION_DENIED
// before any transaction lookup or validation.
func TestCommitTransaction_MissingTxCapability(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Only entity capabilities, no WRITE:graph/tx.
	ctx := capabilityContext("READ:graph/entity/*,WRITE:graph/entity/*", scPriv, "sidecar")

	_, err := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: testMutationEntityID,
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing WRITE:graph/tx capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

// TestRollbackTransaction_MissingTxCapability pins SPEC R3 (SPEC:244): a
// caller without WRITE:graph/tx is denied RollbackTransaction with
// PERMISSION_DENIED before any transaction lookup or validation.
func TestRollbackTransaction_MissingTxCapability(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Only entity capabilities, no WRITE:graph/tx.
	ctx := capabilityContext("READ:graph/entity/*,WRITE:graph/entity/*", scPriv, "sidecar")

	_, err := srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
		TransactionId: testMutationEntityID,
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing WRITE:graph/tx capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

// TestRefreshTransaction_MissingTxCapability pins SPEC R3 (SPEC:244): a
// caller without WRITE:graph/tx is denied RefreshTransaction with
// PERMISSION_DENIED before any transaction lookup or validation.
func TestRefreshTransaction_MissingTxCapability(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Only entity capabilities, no WRITE:graph/tx.
	ctx := capabilityContext("READ:graph/entity/*,WRITE:graph/entity/*", scPriv, "sidecar")

	_, err := srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: testMutationEntityID,
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing WRITE:graph/tx capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

// TestExtendTimeout_MissingTxCapability pins SPEC R3 (SPEC:244): a caller
// without WRITE:graph/tx is denied ExtendTimeout with PERMISSION_DENIED
// before any transaction lookup or validation.
func TestExtendTimeout_MissingTxCapability(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Only entity capabilities, no WRITE:graph/tx.
	ctx := capabilityContext("READ:graph/entity/*,WRITE:graph/entity/*", scPriv, "sidecar")

	_, err := srv.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{
		TransactionId: testMutationEntityID,
		Duration:      durationpb.New(time.Minute),
	})
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
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Only WRITE capabilities, no READ:graph/entity/*.
	ctx := capabilityContext("WRITE:graph/entity/*,WRITE:graph/tx", scPriv, "sidecar")
	stream := &mockExportStream{ctx: ctx}

	handlerInvoked, err := invokeExportGraph(
		srv, &flowv1.ExportGraphRequest{Format: "json"}, stream,
	)
	if !handlerInvoked {
		t.Fatal("stream interceptor did not invoke ExportGraph")
	}
	if stream.verifiedCaps == nil || len(stream.verifiedCaps.Caps) != 2 ||
		stream.verifiedCaps.Caps[0] != "WRITE:graph/entity/*" || stream.verifiedCaps.Caps[1] != "WRITE:graph/tx" {
		t.Fatalf("handler did not receive interceptor-verified WRITE capabilities: %+v", stream.verifiedCaps)
	}
	if status.Code(err) != codes.PermissionDenied ||
		status.Convert(err).Message() != "capability denied: READ:graph/entity/*" {
		t.Fatalf("expected missing-READ PermissionDenied, got %v", err)
	}
}

// TestExportGraph_PerTypeReadCapabilityDenied pins the SPEC R3 negative branch
// (SPEC:241: only READ:graph/entity/* "Authorises the above plus
// ExportGraph(format)"): a caller holding only a per-type READ grant (e.g.
// READ:graph/entity/Component) must be denied ExportGraph.
// TestExportGraph_MissingReadCapability uses a WRITE-only holder; if the
// wildcard gate regressed to accept per-type grants, only this test fails.
func TestExportGraph_PerTypeReadCapabilityDenied(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Only a per-type READ capability, no READ:graph/entity/*.
	ctx := capabilityContext("READ:graph/entity/Component", scPriv, "sidecar")
	stream := &mockExportStream{ctx: ctx}

	handlerInvoked, err := invokeExportGraph(
		srv, &flowv1.ExportGraphRequest{Format: "json"}, stream,
	)
	if !handlerInvoked {
		t.Fatal("stream interceptor did not invoke ExportGraph")
	}
	if status.Code(err) != codes.PermissionDenied ||
		status.Convert(err).Message() != "capability denied: READ:graph/entity/*" {
		t.Fatalf("expected per-type-only PermissionDenied for ExportGraph, got %v", err)
	}
}

// TestCapability_WildcardFallback verifies that Mode 2 wildcard fallback works:
// a capability "READ:graph/entity/*" should allow reading any entity type even
// without a specific "READ:graph/entity/Component" capability.
func TestCapability_WildcardFallback(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
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
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	// 30-second staleness window, like production.
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)

	// Capability signed 25 seconds ago — inside the 30-second window. A margin
	// (25s, not 29s/30s) is required because Unix() truncates to a whole
	// second while the verifier's elapsed (time.Since of that second) carries
	// the sub-second fraction of the current time: signed at -29s, a wall-clock
	// second tick between signing and verifying pushes elapsed past 30s and the
	// in-window capability is wrongly rejected as stale.
	insideWindow := time.Now().Add(-25 * time.Second).Unix()
	payload := fmt.Sprintf("READ:graph/entity/Component|%d", insideWindow)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(scPriv, []byte(payload)))
	md := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, "READ:graph/entity/Component",
		flowmeta.MetadataKeyCapabilitiesSignature, sig,
		flowmeta.MetadataKeyCapabilitiesSignedAt, fmt.Sprintf("%d", insideWindow),
		flowmeta.MetadataKeyCapabilitiesSignedBy, "sidecar",
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
		flowmeta.MetadataKeyCapabilities, "READ:graph/entity/Component",
		flowmeta.MetadataKeyCapabilitiesSignature, sig2,
		flowmeta.MetadataKeyCapabilitiesSignedAt, fmt.Sprintf("%d", past31s),
		flowmeta.MetadataKeyCapabilitiesSignedBy, "sidecar",
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
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	// Negative staleness window disables the check.
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		-1*time.Second, "test-ns", 30*time.Minute, 100000)

	// Very old timestamp — would be stale with a positive window.
	oldTime := time.Now().Add(-24 * time.Hour).Unix()
	payload := fmt.Sprintf("READ:graph/entity/Component|%d", oldTime)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(scPriv, []byte(payload)))
	md := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, "READ:graph/entity/Component",
		flowmeta.MetadataKeyCapabilitiesSignature, sig,
		flowmeta.MetadataKeyCapabilitiesSignedAt, fmt.Sprintf("%d", oldTime),
		flowmeta.MetadataKeyCapabilitiesSignedBy, "sidecar",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := srv.verifier.verify(ctx)
	if err != nil {
		t.Fatalf("expected success with negative staleness window, got: %v", err)
	}
}

// TestCapability_FutureDatedSignedAtRejected pins the anti-replay boundary for
// a future-dated x-flow-capabilities-signed-at (capability.go): the staleness
// check is two-sided — an attestation signed in the future (elapsed < 0) is
// stale just as one past the window (elapsed > window) is — so a captured
// attestation replayed with a forged future timestamp can never outlive the
// anti-replay window (SPEC error table "Stale capability signature
// (anti-replay)": missing, malformed, or expired).
func TestCapability_FutureDatedSignedAtRejected(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)

	// Signed one hour in the future: time.Since(future) is negative.
	future := time.Now().Add(time.Hour).Unix()
	payload := fmt.Sprintf("READ:graph/entity/Component|%d", future)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(scPriv, []byte(payload)))
	md := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, "READ:graph/entity/Component",
		flowmeta.MetadataKeyCapabilitiesSignature, sig,
		flowmeta.MetadataKeyCapabilitiesSignedAt, fmt.Sprintf("%d", future),
		flowmeta.MetadataKeyCapabilitiesSignedBy, "sidecar",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := srv.verifier.verify(ctx)
	if err == nil {
		t.Fatal("expected error for future-dated signed-at, got nil")
	}
	if status.Code(err) != codes.PermissionDenied ||
		status.Convert(err).Message() != capabilityStaleMsg {
		t.Fatalf("expected PermissionDenied stale-capability, got %v", err)
	}
}

// =========================================================================
// 28. Git lock serialization test
// =========================================================================

func TestGitLockSerialization(t *testing.T) {
	srv, _ := newTestServer(t)

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
// 10. Telemetry tests
// =========================================================================

func TestTelemetry_TransactionGC(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())

	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)
	mockPub := &mockTelemetryPublisher{}
	srv.auditor = mockPub
	srv.dbReady.Store(true)

	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(7*24*time.Hour, 100000)
	srv.txManager.clock = fc
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

// TestGC_ExpiredTransaction_Rollback pins the gcTick rollback body (SPEC R9:
// "the branch DB, git branch, and change log are rolled back asynchronously
// within the cleanup grace period"; Design → Versioning Architecture → Garbage
// collection: "expired transactions ... are rolled back automatically. Orphaned
// git branches and branches/<tx-id>.lbug files are deleted."). Unlike
// TestTelemetry_TransactionGC, which only observes the telemetry event, this
// test registers a transaction with real branch resources (git branch + branch
// DB, mirroring BeginTransaction), expires it past the 30-second cleanup grace
// period, runs gcTick, and asserts every rollback observable: the branch DB is
// dropped, the git branch is deleted, and the transaction — whose change log
// is owned by its registration (TransactionState.ChangeLog) — is deregistered.
// Deleting the rollback body of gcTick (cartographer_server.go) would fail
// this test.
func TestGC_ExpiredTransaction_Rollback(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}

	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)
	mockPub := &mockTelemetryPublisher{}
	srv.auditor = mockPub
	srv.dbReady.Store(true)

	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(7*24*time.Hour, 100000)
	srv.txManager.clock = fc

	ctx := context.Background()
	txID := srv.newIDFn()

	// Mirror BeginTransaction's branch-resource creation so gcTick's rollback
	// has real resources to remove: a git branch and a branch DB.
	if err := srv.gitstore.WithGitLock(func() error {
		if err := srv.gitstore.CreateBranch(ctx, txID); err != nil {
			return err
		}
		return srv.gitstore.HardResetToBranch(ctx, txID)
	}); err != nil {
		t.Fatalf("create git branch: %v", err)
	}
	if err := srv.store.CreateBranchDB(ctx, txID); err != nil {
		t.Fatalf("create branch DB: %v", err)
	}
	state, err := srv.txManager.Create(txID, 1*time.Minute, "head")
	if err != nil {
		t.Fatalf("register transaction: %v", err)
	}
	// The change log lives on the registered transaction state; seed it so the
	// rollback has a log to discard along with the registration.
	if err := state.ChangeLog.Add(gitstore.ChangeLogEntry{
		Kind: gitstore.ChangeDelEntity,
		ID:   "22222222-2222-4222-8222-222222222222",
		Type: "Component",
	}); err != nil {
		t.Fatalf("seed change log: %v", err)
	}

	// Sanity: the branch resources exist and the transaction is registered
	// before gcTick runs.
	if exists, err := srv.gitstore.BranchExists(ctx, txID); err != nil || !exists {
		t.Fatalf("git branch should exist before gcTick (exists=%v err=%v)", exists, err)
	}
	if _, err := srv.store.DumpAllEntities(ctx, txID); err != nil {
		t.Fatalf("branch DB should exist before gcTick: %v", err)
	}
	if _, err := srv.txManager.Lookup(txID); err != nil {
		t.Fatalf("transaction should be registered before gcTick: %v", err)
	}

	// Expire the transaction past its timeout plus the 30-second cleanup grace
	// period, then run the GC tick.
	fc.Advance(2 * time.Minute)
	srv.gcTick()

	// Rollback asserts (SPEC R9 + Garbage collection):
	// 1. the branch DB is dropped,
	if _, err := srv.store.DumpAllEntities(ctx, txID); !errors.Is(err, store.ErrBranchNotFound) {
		t.Fatalf("branch DB not rolled back: DumpAllEntities err = %v, want store.ErrBranchNotFound", err)
	}
	// 2. the git branch is deleted,
	if exists, err := srv.gitstore.BranchExists(ctx, txID); err != nil || exists {
		t.Fatalf("git branch not rolled back: BranchExists = %v (err %v), want false", exists, err)
	}
	// 3. the transaction (and its change log) is deregistered.
	if _, err := srv.txManager.Lookup(txID); err == nil {
		t.Fatal("transaction not deregistered after gcTick")
	}
}

// TestGCScan_ConcurrentWithExtendTimeout pins that gcTick's first expiry scan
// reads ExpiresAt under the transaction's lifecycle lock — the same lock
// ExtendTimeout writes it under — not under tm.mu alone. Before the fix, the
// first scan read state.ExpiresAt while holding only tm.mu.RLock, racing the
// lifecycle-locked write; run under -race this test reports that race, and
// without -race it still completes with the unexpired transaction surviving
// (gcTick must not collect a transaction the extension loop keeps alive).
func TestGCScan_ConcurrentWithExtendTimeout(t *testing.T) {
	srv, _ := newTestServer(t)
	txID := srv.newIDFn()
	if _, err := srv.txManager.Create(txID, 10*time.Minute, "head"); err != nil {
		t.Fatalf("register transaction: %v", err)
	}

	done := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 500 {
			if err := srv.txManager.ExtendTimeout(txID, 10*time.Minute, nil); err != nil {
				done <- fmt.Errorf("ExtendTimeout: %w", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 500 {
			srv.gcTick()
		}
	}()
	wg.Wait()
	close(done)
	for err := range done {
		t.Fatal(err)
	}
	if _, err := srv.txManager.Lookup(txID); err != nil {
		t.Fatalf("unexpired transaction must survive the concurrent GC scan: %v", err)
	}
}

// =========================================================================
// 30. Service check-order fix tests (Phase 2)
// =========================================================================

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

// =========================================================================
// 25. SearchNeighbors empty embedding test
// =========================================================================

func TestSearchNeighbors_EmptyEmbedding(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  nil,
		EntityType: "Component",
		TopK:       5,
	})
	if err == nil {
		t.Fatal("expected error for empty embedding, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
	if msg := status.Convert(err).Message(); msg != "embedding is required" {
		t.Fatalf("expected missing-embedding error, got %q", msg)
	}
}

// =========================================================================
// 26. ListEntities pagination test
// =========================================================================

func TestListEntities_Pagination(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	// Create exactly 10 entities (enough for 2 pages of 5).
	const total = 10
	for i := range total {
		_, err := srv.store.CreateEntity(ctx, "Component", "",
			map[string]string{"name": fmt.Sprintf("entity-%d", i)}, nil, "")
		if err != nil {
			t.Fatalf("CreateEntity %d: %v", i, err)
		}
	}

	// First page: PageSize=5.
	resp, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
		EntityType: "Component",
		PageSize:   5,
	})
	if err != nil {
		t.Fatalf("ListEntities page 1 failed: %v", err)
	}
	if len(resp.Entities) != 5 {
		t.Fatalf("page 1 expected 5 entities, got %d", len(resp.Entities))
	}
	if resp.NextPageToken == "" {
		t.Fatal("page 1 expected non-empty NextPageToken")
	}

	// Collect first page IDs for dedup check.
	page1IDs := make(map[string]bool)
	for _, e := range resp.Entities {
		page1IDs[e.EntityId] = true
	}

	// Second page: use the token from the first page.
	resp2, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
		EntityType: "Component",
		PageSize:   5,
		PageToken:  resp.NextPageToken,
	})
	if err != nil {
		t.Fatalf("ListEntities page 2 failed: %v", err)
	}
	if len(resp2.Entities) != 5 {
		t.Fatalf("page 2 expected 5 entities, got %d", len(resp2.Entities))
	}
	if resp2.NextPageToken != "" {
		t.Fatal("page 2 expected empty NextPageToken (no more entities)")
	}

	// Verify no overlap between pages.
	for _, e := range resp2.Entities {
		if page1IDs[e.EntityId] {
			t.Fatalf("entity %q appears in both pages", e.EntityId)
		}
	}
}

// =========================================================================
// 32. Read-path transactionId rejection tests (SPEC R2)
// =========================================================================

func TestReadPathTransactionID_Rejected(t *testing.T) {
	applySchemaAndSeed := func(ctx context.Context, t *testing.T, srv *CartographerServer) {
		t.Helper()
		applyTestSchema(ctx, t, srv.store)
		_, _ = srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "a"}, nil, "")
	}

	t.Run("ExecuteCypher invalid transaction id", func(t *testing.T) {
		srv, _ := newTestServer(t)
		applyTestSchema(testCtx(), t, srv.store)
		_, err := srv.ExecuteCypher(testCtx(), &flowv1.ExecuteCypherRequest{
			Cypher:        "MATCH (n:Component) RETURN n",
			TransactionId: "not-a-uuid",
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
		}
	})

	t.Run("ExecuteCypher unknown transaction id", func(t *testing.T) {
		srv, _ := newTestServer(t)
		applySchemaAndSeed(testCtx(), t, srv)
		_, err := srv.ExecuteCypher(testCtx(), &flowv1.ExecuteCypherRequest{
			Cypher:        "MATCH (n:Component) RETURN n",
			TransactionId: "11111111-1111-4111-8111-111111111111",
		})
		if status.Code(err) != codes.NotFound {
			t.Fatalf("expected NotFound, got %v", status.Code(err))
		}
	})

	t.Run("SearchNeighbors invalid transaction id", func(t *testing.T) {
		srv, _ := newTestServer(t)
		applySchemaAndSeed(testCtx(), t, srv)
		_, err := srv.SearchNeighbors(testCtx(), &flowv1.SearchNeighborsRequest{
			Embedding:     []float32{1.0, 2.0, 3.0},
			EntityType:    "Component",
			TopK:          5,
			TransactionId: "not-a-uuid",
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
		}
	})

	t.Run("SearchNeighbors unknown transaction id", func(t *testing.T) {
		srv, _ := newTestServer(t)
		applySchemaAndSeed(testCtx(), t, srv)
		_, err := srv.SearchNeighbors(testCtx(), &flowv1.SearchNeighborsRequest{
			Embedding:     []float32{1.0, 2.0, 3.0},
			EntityType:    "Component",
			TopK:          5,
			TransactionId: "11111111-1111-4111-8111-111111111111",
		})
		if status.Code(err) != codes.NotFound {
			t.Fatalf("expected NotFound, got %v", status.Code(err))
		}
	})

	t.Run("FullTextSearch invalid transaction id", func(t *testing.T) {
		srv, _ := newTestServer(t)
		applySchemaAndSeed(testCtx(), t, srv)
		_, err := srv.FullTextSearch(testCtx(), &flowv1.FullTextSearchRequest{
			Query:         "apple",
			EntityType:    "Component",
			TransactionId: "not-a-uuid",
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
		}
	})

	t.Run("FullTextSearch unknown transaction id", func(t *testing.T) {
		srv, _ := newTestServer(t)
		applySchemaAndSeed(testCtx(), t, srv)
		_, err := srv.FullTextSearch(testCtx(), &flowv1.FullTextSearchRequest{
			Query:         "apple",
			EntityType:    "Component",
			TransactionId: "11111111-1111-4111-8111-111111111111",
		})
		if status.Code(err) != codes.NotFound {
			t.Fatalf("expected NotFound, got %v", status.Code(err))
		}
	})

	t.Run("ListEntities invalid transaction id", func(t *testing.T) {
		srv, _ := newTestServer(t)
		applySchemaAndSeed(testCtx(), t, srv)
		_, err := srv.ListEntities(testCtx(), &flowv1.ListEntitiesRequest{
			EntityType:    "Component",
			PageSize:      10,
			TransactionId: "not-a-uuid",
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
		}
	})

	t.Run("ListEntities unknown transaction id", func(t *testing.T) {
		srv, _ := newTestServer(t)
		applySchemaAndSeed(testCtx(), t, srv)
		_, err := srv.ListEntities(testCtx(), &flowv1.ListEntitiesRequest{
			EntityType:    "Component",
			PageSize:      10,
			TransactionId: "11111111-1111-4111-8111-111111111111",
		})
		if status.Code(err) != codes.NotFound {
			t.Fatalf("expected NotFound, got %v", status.Code(err))
		}
	})
}

// TestMutationTransactionID_Rejected verifies SPEC R2's error-table rows
// "Invalid transaction ID format" (INVALID_ARGUMENT) and "Transaction not
// found" (NOT_FOUND) cover the write path exactly as the read path (SPEC:167-175
// states both rows "cover both read- and write-path operations"): every
// mutation RPC (CreateEntity, UpdateEntity, DeleteEntity, CreateEdge,
// DeleteEdge) rejects a malformed transactionId with INVALID_ARGUMENT and an
// unknown-but-valid transactionId with NOT_FOUND via lockTransactionMutation.
func TestMutationTransactionID_Rejected(t *testing.T) {
	validID := "11111111-1111-4111-8111-111111111111"
	tests := []struct {
		name string
		call func(context.Context, *CartographerServer, string) error
	}{
		{"CreateEntity", func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
				EntityType: "Component", Properties: map[string]string{"name": "x"}, TransactionId: txID,
			})
			return err
		}},
		{"UpdateEntity", func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
				Id: validID, Properties: map[string]string{"version": "2"}, TransactionId: txID,
			})
			return err
		}},
		{"DeleteEntity", func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: validID, TransactionId: txID})
			return err
		}},
		{"CreateEdge", func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
				EdgeType: "DEPENDS_ON", FromEntityId: validID, ToEntityId: validID, TransactionId: txID,
			})
			return err
		}},
		{"DeleteEdge", func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: validID, TransactionId: txID})
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/invalid transaction id", func(t *testing.T) {
			srv, _ := newTestServer(t)
			applyTestSchema(testCtx(), t, srv.store)
			err := tt.call(testCtx(), srv, "not-a-uuid")
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
			}
		})
		t.Run(tt.name+"/unknown transaction id", func(t *testing.T) {
			srv, _ := newTestServer(t)
			applyTestSchema(testCtx(), t, srv.store)
			err := tt.call(testCtx(), srv, validID)
			if status.Code(err) != codes.NotFound {
				t.Fatalf("expected NotFound, got %v (%v)", status.Code(err), err)
			}
		})
	}
}

// TestMutationTransactionID_EmptyRejected is the empty-transactionId sibling of
// TestMutationTransactionID_Rejected: it pins the SPEC error-table row "Write
// outside a transaction → FAILED_PRECONDITION" (SPEC:954) directly at the
// service layer. Every write RPC (CreateEntity, UpdateEntity, DeleteEntity,
// CreateEdge, DeleteEdge) begins with an active-transaction gate that rejects a
// request carrying no transaction ID — the wire's empty transactionId — with
// FAILED_PRECONDITION. The sibling test covers the malformed and unknown-ID
// fault modes; the empty-ID gate is the first check in each handler and has no
// other service-layer test reaching it with a real (non-fake) handler.
func TestMutationTransactionID_EmptyRejected(t *testing.T) {
	tests := []struct {
		name string
		call func(context.Context, *CartographerServer) error
	}{
		{"CreateEntity", func(ctx context.Context, srv *CartographerServer) error {
			_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
				EntityType: "Component", Properties: map[string]string{"name": "x"},
			})
			return err
		}},
		{"UpdateEntity", func(ctx context.Context, srv *CartographerServer) error {
			_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
				Id: testMutationEntityID, Properties: map[string]string{"version": "2"},
			})
			return err
		}},
		{"DeleteEntity", func(ctx context.Context, srv *CartographerServer) error {
			_, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: testMutationEntityID})
			return err
		}},
		{"CreateEdge", func(ctx context.Context, srv *CartographerServer) error {
			_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
				EdgeType: "DEPENDS_ON", FromEntityId: testMutationEntityID, ToEntityId: testMutationEntityID,
			})
			return err
		}},
		{"DeleteEdge", func(ctx context.Context, srv *CartographerServer) error {
			_, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: testMutationEntityID})
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/empty transaction id", func(t *testing.T) {
			srv, _ := newTestServer(t)
			applyTestSchema(testCtx(), t, srv.store)
			err := tt.call(testCtx(), srv)
			if status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("expected FailedPrecondition, got %v (%v)", status.Code(err), err)
			}
		})
	}
}

// TestWriteCheckOrder_TransactionValidationPrecedesStructural pins the SPEC RPC
// check order for the five write RPCs (SPEC:984-988 — every write RPC begins
// with "active transaction"): a request combining an active-transaction
// validation fault (malformed, unknown, timed-out, or rollback-only transaction
// ID) with a structural fault must surface the transaction error — never the
// structural one. Before this fix the lockTransactionMutation gate ran after
// the structural and capability checks, so a nonexistent transaction combined
// with a structural fault surfaced INVALID_ARGUMENT instead of NOT_FOUND.
func TestWriteCheckOrder_TransactionValidationPrecedesStructural(t *testing.T) {
	// Each write RPC with the structural fault that would surface first if the
	// active-transaction gate ran after structural validation.
	writeCases := []struct {
		name string
		call func(context.Context, *CartographerServer, string) error
	}{
		{"CreateEntity", func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
				EntityType: "NoSuchType", Properties: map[string]string{"name": "x"}, TransactionId: txID,
			})
			return err
		}},
		{"UpdateEntity", func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
				Properties: map[string]string{"version": "2"}, TransactionId: txID,
			})
			return err
		}},
		{"DeleteEntity", func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{TransactionId: txID})
			return err
		}},
		{"CreateEdge", func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
				EdgeType: "", FromEntityId: testMutationEntityID, ToEntityId: testMutationEntityID, TransactionId: txID,
			})
			return err
		}},
		{"DeleteEdge", func(ctx context.Context, srv *CartographerServer, txID string) error {
			_, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{TransactionId: txID})
			return err
		}},
	}
	faultModes := []struct {
		name        string
		prepare     func(t *testing.T, srv *CartographerServer) string
		want        codes.Code
		msgContains string
	}{
		{"malformed transaction id", func(t *testing.T, srv *CartographerServer) string {
			return "not-a-uuid"
		}, codes.InvalidArgument, "transaction"},
		{"unknown transaction id", func(t *testing.T, srv *CartographerServer) string {
			return testMutationEntityID
		}, codes.NotFound, ""},
		{"timed-out transaction", func(t *testing.T, srv *CartographerServer) string {
			fc := newFakeClock(time.Now())
			srv.txManager = NewTransactionManager(7*24*time.Hour, 100000)
			srv.txManager.clock = fc
			txID := beginTestTx(t, srv, testCtx())
			fc.Advance(2 * time.Hour)
			return txID
		}, codes.DeadlineExceeded, ""},
		{"rollback-only transaction", func(t *testing.T, srv *CartographerServer) string {
			txID := beginTestTx(t, srv, testCtx())
			state, err := srv.txManager.Lookup(txID)
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			state.RollbackOnly = true
			return txID
			// A rollback-only transaction is already rolled back (SPEC: the
			// cap-violation outcome is RESOURCE_EXHAUSTED with the transaction
			// "rolled back"), so the transaction gate surfaces NOT_FOUND
			// ("Transaction not found": "was already committed/rolled back").
		}, codes.NotFound, ""},
	}
	for _, wc := range writeCases {
		for _, fm := range faultModes {
			t.Run(wc.name+"/"+fm.name, func(t *testing.T) {
				srv, _ := newTestServer(t)
				txID := fm.prepare(t, srv)
				err := wc.call(testCtx(), srv, txID)
				if status.Code(err) != fm.want {
					t.Fatalf("expected %v, got %v (%v)", fm.want, status.Code(err), err)
				}
				if fm.msgContains != "" && !strings.Contains(err.Error(), fm.msgContains) {
					t.Fatalf("expected error mentioning %q, got %q", fm.msgContains, err.Error())
				}
			})
		}
	}
}

// =========================================================================
// 33. HealthCheck propagates store errors (SPEC R1/R5)
// =========================================================================

type healthFailingStore struct {
	store.Store
}

func (s *healthFailingStore) Health(ctx context.Context) (*store.HealthResult, error) {
	return nil, fmt.Errorf("store health probe failed: pvc unreadable")
}

func TestHealthCheck_PropagatesStoreError(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	failing := &healthFailingStore{Store: base}
	gs, _ := gitstore.New(t.TempDir())
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(failing, gs, opPub, initTestKey(), nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	resp, err := srv.HealthCheck(context.Background(), &flowv1.HealthCheckRequest{})
	if err == nil {
		t.Fatal("expected HealthCheck to propagate store error, got nil")
	}
	// A healthy-probe store must not be reported as a successful (non-nil)
	// response while the probe failed.
	if resp != nil {
		t.Fatalf("expected nil response on error, got %+v", resp)
	}
	if !strings.Contains(err.Error(), "pvc unreadable") {
		t.Fatalf("expected store error message, got %v", err)
	}
}

// =========================================================================
// 34. capability.go missing/malformed signed-at branch
// =========================================================================

func TestCapability_MissingSignedAt(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)

	// Build metadata without the signed-at key (or with an empty value).
	payload := "READ:graph/entity/Component|1234567890"
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(scPriv, []byte(payload)))
	md := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, "READ:graph/entity/Component",
		flowmeta.MetadataKeyCapabilitiesSignature, sig,
		flowmeta.MetadataKeyCapabilitiesSignedBy, "sidecar",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := srv.verifier.verify(ctx)
	if err == nil {
		t.Fatal("expected error for missing signed-at, got nil")
	}
	if status.Code(err) != codes.PermissionDenied ||
		status.Convert(err).Message() != capabilityStaleMsg {
		t.Fatalf("expected PermissionDenied stale-capability, got %v", err)
	}
}

func TestCapability_EmptySignedAt(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	base, _ := openTestStore(t)
	t.Cleanup(func() { _ = base.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(base, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)

	md := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, "READ:graph/entity/Component",
		flowmeta.MetadataKeyCapabilitiesSignature, base64.StdEncoding.EncodeToString([]byte("fake")),
		flowmeta.MetadataKeyCapabilitiesSignedAt, "",
		flowmeta.MetadataKeyCapabilitiesSignedBy, "sidecar",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := srv.verifier.verify(ctx)
	if err == nil {
		t.Fatal("expected error for empty signed-at, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

func TestCapability_MalformedSignedAt(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)

	// signed-at is present but non-numeric ("abc"): the empty/len==0 guard is
	// bypassed, the (assumed valid) signature verifies over the raw payload
	// including "abc", and only then does ParseInt fail. Assert the resulting
	// PERMISSION_DENIED for the malformed signed-at anti-replay branch.
	payload := "READ:graph/entity/Component|abc"
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(scPriv, []byte(payload)))
	md := metadata.Pairs(
		flowmeta.MetadataKeyCapabilities, "READ:graph/entity/Component",
		flowmeta.MetadataKeyCapabilitiesSignature, sig,
		flowmeta.MetadataKeyCapabilitiesSignedBy, "sidecar",
		flowmeta.MetadataKeyCapabilitiesSignedAt, "abc",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := srv.verifier.verify(ctx)
	if err == nil {
		t.Fatal("expected error for malformed signed-at, got nil")
	}
	if status.Code(err) != codes.PermissionDenied ||
		status.Convert(err).Message() != capabilityStaleMsg {
		t.Fatalf("expected PermissionDenied stale-capability, got %v", err)
	}
}

// =========================================================================
// 35. ExecuteCypher read-only caller without entity-types metadata
// =========================================================================

// TestExecuteCypher_NoLabelsFallsBackToWildcard asserts the SPEC R3 wildcard
// fallback on the server-authoritative path: a statement that parses as a
// read-only cross-type read but yields no labels (an unlabelled MATCH) is
// checked against READ:graph/entity/* — which a write-only caller lacks.
func TestExecuteCypher_NoLabelsFallsBackToWildcard(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := noReadCtx() // WRITE-only capabilities, no READ.
	applyTestSchema(ctx, t, srv.store)

	_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: "MATCH (n) RETURN n"})
	if err == nil {
		t.Fatal("expected PermissionDenied for write-only caller, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

// TestExecuteCypher_MultiTypeSubsetRejected asserts SPEC R3 (SPEC:249): the
// Cartographer derives the referenced entity-type labels from its own
// server-side parse of the statement, and a caller holding read capability
// for only a subset of the referenced types is rejected with PERMISSION_DENIED
// — the specific-type check must not fall back to the wildcard.
func TestExecuteCypher_MultiTypeSubsetRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("READ:graph/entity/Component", "READ:graph/tx")
	applyTestSchema(ctx, t, srv.store)

	_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{
		Cypher: "MATCH (a:Component)-[:DEPENDS_ON]->(b:Service) RETURN b",
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for subset capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v (%v)", status.Code(err), err)
	}
}

// TestExecuteCypher_SingleTypeSpecificCapabilityPasses asserts SPEC R3: a
// caller holding READ:graph/entity/Component passes the server-side per-type
// check for a single-type query referencing exactly Component, and the query
// executes successfully (the per-type branch is not a blanket rejection).
func TestExecuteCypher_SingleTypeSpecificCapabilityPasses(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("READ:graph/entity/Component", "READ:graph/tx")
	applyTestSchema(ctx, t, srv.store)
	_, _ = srv.store.CreateEntity(testCtx(), "Component", "", map[string]string{"name": "x"}, nil, "")

	resp, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{
		Cypher: "MATCH (n:Component) RETURN n",
	})
	if err != nil {
		t.Fatalf("ExecuteCypher failed: %v", err)
	}
	if len(resp.Rows) == 0 {
		t.Fatal("expected at least one row")
	}
}

// TestExecuteCypher_WildcardHolderPassesMultiType asserts SPEC R3 (SPEC:249):
// a caller holding READ:graph/entity/* passes regardless of the label set the
// server extracts from its own parse, and the query executes successfully.
func TestExecuteCypher_WildcardHolderPassesMultiType(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("READ:graph/entity/*", "READ:graph/tx")
	applyTestSchema(ctx, t, srv.store)
	comp, _ := srv.store.CreateEntity(testCtx(), "Component", "", map[string]string{"name": "x"}, nil, "")
	svc, _ := srv.store.CreateEntity(testCtx(), "Service", "", map[string]string{"name": "s"}, nil, "")
	_, _ = srv.store.CreateEdge(testCtx(), "DEPENDS_ON", svc.Id, comp.Id, nil, "")

	resp, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{
		Cypher: "MATCH (a:Service)-[:DEPENDS_ON]->(b:Component) RETURN b",
	})
	if err != nil {
		t.Fatalf("ExecuteCypher failed: %v", err)
	}
	if len(resp.Rows) == 0 {
		t.Fatal("expected at least one row")
	}
}

// =========================================================================
// 36. ExportGraph deterministic RESOURCE_EXHAUSTED during enumeration
// =========================================================================

// exhaustedStore fails deterministically during export enumeration with a
// RESOURCE_EXHAUSTED gRPC status, exercising the collectExportData error path.
type exhaustedStore struct {
	store.Store
}

func (e *exhaustedStore) ListMainEntityTypes() ([]string, error) {
	return nil, status.Error(codes.ResourceExhausted, "simulated enumeration capacity exceeded")
}

func TestExportGraph_DeterministicResourceExhausted(t *testing.T) {
	base, _ := openTestStore(t)
	t.Cleanup(func() { _ = base.Close() })
	gs, _ := gitstore.New(t.TempDir())
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(base, gs, opPub, initTestKey(), nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()
	srv.store = &exhaustedStore{Store: base}

	stream := &mockExportStream{ctx: capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")}
	handlerInvoked, err := invokeExportGraph(srv, &flowv1.ExportGraphRequest{Format: "json"}, stream)
	if !handlerInvoked {
		t.Fatal("stream interceptor did not invoke ExportGraph")
	}
	if err == nil {
		t.Fatal("expected error during enumeration, got nil")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v (%v)", status.Code(err), err)
	}
	if len(stream.data) != 0 {
		t.Fatalf("expected no data streamed on failure, got %d bytes", len(stream.data))
	}
}

// =========================================================================
// Crash-window / durability pins (special-fixer review items)
// =========================================================================

// TestCommitTransaction_RecoveredPartialCommitRehydratesAfterStartupRebuild
// pins the CommitTransaction cross-restart retry (SPEC serialisation flow
// retry contract): a crash between main's re-hydration and the fast-forward
// merge, followed by the unconditional startup rebuild
// (rehydrateMainAfterRecovery, cmd/main.go) which re-hydrates main.lbug from
// git main's pre-transaction tree, must not leave the retried commit skipping
// re-hydration. Recovery must clear the durable CommitHydrated flag so the
// retried commit re-hydrates from the transaction branch and main.lbug
// converges with git main.
func TestCommitTransaction_RecoveredPartialCommitRehydratesAfterStartupRebuild(t *testing.T) {
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()

	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	// The first commit's fast-forward merge fails, simulating the crash
	// window between main's re-hydration and the merge.
	failingGit := &mergeFailingGitStore{GitStore: gs, failMerge: true}
	srv := NewCartographerServer(
		st, failingGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, st)
	// Git main is non-empty, so the startup rebuild runs on restart.
	mainEntity, err := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "main"}, nil, "main")
	if err != nil {
		t.Fatalf("create main entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, mainEntity.Id, "main")

	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	created, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	// First commit: the re-hydration completes (CommitHydrated=true persists)
	// but the merge fails — the crash window.
	if _, err := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err == nil {
		t.Fatal("expected merge failure")
	}

	// Simulate restart.
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	// Simulate the unconditional startup rebuild (rehydrateMainAfterRecovery):
	// restore the working tree to main, then re-hydrate main.lbug from git
	// main — which does NOT contain the un-merged transaction commit.
	if err := reopenedGit.WithGitLock(func() error {
		if err := reopenedGit.RestoreMain(ctx); err != nil {
			return err
		}
		return reopenedGit.CleanUntracked(ctx)
	}); err != nil {
		t.Fatalf("restore main before rebuild: %v", err)
	}
	entitiesDir, edgesDir := reopenedGit.HydrationDirs()
	if err := reopened.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		t.Fatalf("simulate startup rebuild: %v", err)
	}
	// The rebuild left main.lbug pre-transaction.
	if _, err := reopened.GetEntity(ctx, created.EntityId, "main"); !errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("main.lbug should serve pre-transaction data after the rebuild, got err=%v", err)
	}

	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	restarted.MarkDBReady()
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions: %v", err)
	}
	state, err := restarted.txManager.Lookup(begin.TransactionId)
	if err != nil || state.CommitHydrated {
		t.Fatalf("recovered partial commit must not carry CommitHydrated: state=%+v err=%v", state, err)
	}
	// Retry the commit: it must re-hydrate main from the transaction branch's
	// files (CommitHydrated was cleared) so main.lbug converges with git main.
	if _, err := restarted.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("retry CommitTransaction: %v", err)
	}
	if _, err := reopened.GetEntity(ctx, created.EntityId, "main"); err != nil {
		t.Fatalf("committed transaction entity missing from main.lbug after restart-retry: %v", err)
	}
}

// TestRefreshTransaction_CrashSafeRebuildPreservesBranchDBOnFailure pins the
// RefreshTransaction branch-DB rebuild's crash-safety property: when the
// refresh's re-hydration fails, the existing branch DB — the only durable
// record of the transaction's mutations (SPEC R9 change-log recovery) — must
// survive, and no temporary files may leak.
func TestRefreshTransaction_CrashSafeRebuildPreservesBranchDBOnFailure(t *testing.T) {
	ctx := testCtx()
	dataPath := t.TempDir()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	// Fail the refresh's branch re-hydration (the 2nd HydrateBranchFromFiles
	// call — the 1st is BeginTransaction's).
	release := make(chan struct{})
	close(release)
	blocking := &hydrationBlockingStore{
		Store: st, blocked: make(chan struct{}), release: release, fail: true,
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		blocking, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, st)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	created, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	commitGitEntity(ctx, t, gs, testMutationEntityID, "main")

	if _, err := srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err == nil {
		t.Fatal("expected refresh hydration failure")
	}
	// The existing branch DB — the only durable record of the transaction's
	// mutations — must survive the failed rebuild.
	if _, err := os.Stat(filepath.Join(dataPath, "branches", begin.TransactionId+".lbug")); err != nil {
		t.Fatalf("transaction branch DB was destroyed by the failed refresh: %v", err)
	}
	ent, err := st.GetEntity(ctx, created.EntityId, begin.TransactionId)
	if err != nil {
		t.Fatalf("transaction mutation lost after failed refresh: %v", err)
	}
	if ent.Properties["name"] != "tx" {
		t.Fatalf("transaction entity content = %+v, want name=tx", ent.Properties)
	}
	// No leftover temporary branch files from the aborted rebuild. The
	// engine's `<key>.lbug.wal` write-ahead-log artifact is deliberately
	// ignored: the store's DropBranchDB removes only the `.lbug`/`.schema.json`/
	// `.state.json` files (a pre-existing store behavior applying to every
	// branch drop), so the assertion pins that no temporary *branch DB*
	// resources leak.
	entries, err := os.ReadDir(filepath.Join(dataPath, "branches"))
	if err != nil {
		t.Fatalf("read branches dir: %v", err)
	}
	expected := map[string]bool{
		begin.TransactionId + ".lbug":        true,
		begin.TransactionId + ".schema.json": true,
		begin.TransactionId + ".state.json":  true,
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".wal") {
			continue
		}
		if !expected[e.Name()] {
			t.Fatalf("leftover branch file from aborted refresh: %q", e.Name())
		}
	}
}

// TestRefreshTransaction_FileBackedCrashSafeSwap exercises the crash-safe
// rebuild+swap on a file-backed store end to end: the refreshed branch reflects
// main's new state plus the transaction's changes, and the subsequent commit
// converges main.lbug with git main.
func TestRefreshTransaction_FileBackedCrashSafeSwap(t *testing.T) {
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, st)
	mainEntity, err := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "main"}, nil, "main")
	if err != nil {
		t.Fatalf("create main entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, mainEntity.Id, "main")

	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	txEntity, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	// Main advances while the transaction is open.
	interim, err := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "interim"}, nil, "main")
	if err != nil {
		t.Fatalf("create interim entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, interim.Id, "interim")

	if _, err := srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RefreshTransaction: %v", err)
	}
	// The refreshed branch reflects main's new state plus the transaction's
	// change (all reads go through the swapped-in branch DB).
	for _, probe := range []struct{ id, want string }{
		{mainEntity.Id, "main"}, {interim.Id, "interim"}, {txEntity.EntityId, "tx"},
	} {
		ent, err := st.GetEntity(ctx, probe.id, begin.TransactionId)
		if err != nil {
			t.Fatalf("entity %s missing from refreshed branch: %v", probe.id, err)
		}
		if ent.Properties["name"] != probe.want {
			t.Fatalf("entity %s on branch has name %q, want %q", probe.id, ent.Properties["name"], probe.want)
		}
	}
	// The commit succeeds and main.lbug converges with git main.
	if _, err := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CommitTransaction after file-backed refresh: %v", err)
	}
	for _, probe := range []struct{ id, want string }{
		{mainEntity.Id, "main"}, {interim.Id, "interim"}, {txEntity.EntityId, "tx"},
	} {
		ent, err := st.GetEntity(ctx, probe.id, "main")
		if err != nil {
			t.Fatalf("entity %s missing from main after commit: %v", probe.id, err)
		}
		if ent.Properties["name"] != probe.want {
			t.Fatalf("entity %s on main has name %q, want %q", probe.id, ent.Properties["name"], probe.want)
		}
	}
}

// swapRecordingStore records the branch-lifecycle store calls the refresh
// branch-DB swap makes so a test can assert their order.
type swapRecordingStore struct {
	store.Store
	mu  sync.Mutex
	ops []string
}

func (s *swapRecordingStore) record(op string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ops = append(s.ops, op)
}

func (s *swapRecordingStore) DropBranchDB(ctx context.Context, txID string) error {
	s.record("drop:" + txID)
	return s.Store.DropBranchDB(ctx, txID)
}

func (s *swapRecordingStore) CloseBranchDB(ctx context.Context, txID string) error {
	s.record("close:" + txID)
	return s.Store.CloseBranchDB(ctx, txID)
}

// TestRefreshTransaction_SwapClosesTempBranchBeforeRename pins the
// RefreshTransaction branch-DB swap's crash-window fix (SPEC R9 refresh): the
// old branch's in-memory handle must be evicted and the replacement branch must
// be closed — its write-ahead log checkpointed into its .lbug file — before
// its files are renamed onto the transaction's canonical names. The old swap
// dropped the old branch (removing its files before the rename) and renamed
// <temp>.lbug → <txID>.lbug while the temp connection was still open; a crash
// in those windows left the durable branch DB absent or missing rows still held
// in the orphaned <temp>.lbug.wal, and RecoverOpenTransactions rolled the
// transaction back or classified the absent entities as suspected deletions
// that the recovered commit re-applied to main's committed data. The swap must
// therefore close the old branch (evicting the handle while keeping its files
// until the atomic rename overwrites them), close the temp replacement, then
// release the temp key after the rename.
func TestRefreshTransaction_SwapClosesTempBranchBeforeRename(t *testing.T) {
	ctx := testCtx()
	dataPath := t.TempDir()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	recording := &swapRecordingStore{Store: st}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		recording, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, st)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	// Main advances while the transaction is open so the refresh re-hydrates.
	interim, err := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "interim"}, nil, "main")
	if err != nil {
		t.Fatalf("create interim entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, interim.Id, "interim")

	if _, err := srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RefreshTransaction: %v", err)
	}

	recording.mu.Lock()
	ops := append([]string(nil), recording.ops...)
	recording.mu.Unlock()
	// The swap must be: close the old tx branch handle, close the temp
	// replacement (checkpointing its WAL), then release the temp key after the
	// rename. A swap that dropped the old branch (deleting its files) before
	// the rename, or that released the temp key without a close, would leave a
	// crash window in which the durable branch DB is absent or the renamed file
	// is missing un-checkpointed rows.
	if len(ops) != 3 {
		t.Fatalf("expected swap ops [close:%s close:<temp> drop:<temp>], got %v", begin.TransactionId, ops)
	}
	if ops[0] != "close:"+begin.TransactionId {
		t.Fatalf("swap must close (evict) the old branch handle first, got %q", ops[0])
	}
	if !strings.HasPrefix(ops[1], "close:") {
		t.Fatalf("swap must close the temp branch before the rename, got %q", ops[1])
	}
	if ops[2] != "drop:"+strings.TrimPrefix(ops[1], "close:") {
		t.Fatalf("swap must release the temp key after the close, got %q (close %q)", ops[2], ops[1])
	}
}

// TestStoreCloseBranchDB_CheckpointsDataBeforeFileRename pins the store
// primitive RefreshTransaction's swap relies on: closing a file-backed branch
// must checkpoint its write-ahead log into the .lbug file (so a renamed .lbug
// is complete) and must not delete the persisted branch files. After the
// close, moving the .lbug to a new name without its WAL companion — exactly
// what a crash between the refresh swap's rename and the (old) close did —
// must still yield every row from the file alone.
func TestStoreCloseBranchDB_CheckpointsDataBeforeFileRename(t *testing.T) {
	dataPath := t.TempDir()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	applyTestSchema(ctx, t, st)
	const txID = "11111111-1111-4111-8111-111111111111"
	if err := st.CreateBranchDB(ctx, txID); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := st.ReplicateSchemaToBranch(ctx, txID); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}
	ent, err := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "kept"}, nil, txID)
	if err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}
	// Close the branch: this must checkpoint the WAL into the .lbug and must
	// not remove the persisted files.
	if err := st.CloseBranchDB(ctx, txID); err != nil {
		t.Fatalf("CloseBranchDB: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataPath, "branches", txID+".lbug")); err != nil {
		t.Fatalf("CloseBranchDB deleted the branch file: %v", err)
	}
	// Simulate the refresh swap's crash: rename the branch's files onto a new
	// name WITHOUT the .lbug.wal WAL companion — the engine's path-based WAL
	// recovery cannot find the orphaned <old>.wal, so only data checkpointed
	// into the .lbug before the rename survives.
	const moved = "22222222-2222-4222-8222-222222222222"
	for _, suffix := range []string{".lbug", ".schema.json", ".state.json"} {
		src := filepath.Join(dataPath, "branches", txID+suffix)
		if _, err := os.Stat(src); err != nil {
			continue // no such file (e.g. no state record saved yet)
		}
		if err := os.Rename(src, filepath.Join(dataPath, "branches", moved+suffix)); err != nil {
			t.Fatalf("rename branch file %s: %v", suffix, err)
		}
	}
	got, err := st.GetEntity(ctx, ent.Id, moved)
	if err != nil {
		t.Fatalf("entity lost after close+rename (WAL not checkpointed): %v", err)
	}
	if got.Properties["name"] != "kept" {
		t.Fatalf("entity content = %+v, want name=kept", got.Properties)
	}
}

// TestRecoverOpenTransactionsMissingStateRollsBack pins the BeginTransaction
// crash-window recovery (SPEC R9 change-log recovery): a git branch whose
// branch DB exists but whose state record is missing (crash between
// HydrateBranchFromFiles and persistTransactionState) must be rolled back
// instead of hard-failing startup.
func TestRecoverOpenTransactionsMissingStateRollsBack(t *testing.T) {
	ctx := testCtx()
	dataPath := t.TempDir()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	applyTestSchema(ctx, t, st)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	branchDBPath := filepath.Join(dataPath, "branches", begin.TransactionId+".lbug")
	if _, err := os.Stat(branchDBPath); err != nil {
		t.Fatalf("stat persisted branch DB: %v", err)
	}
	// Simulate the BeginTransaction crash window: the branch DB and git branch
	// persist but the state record was never written.
	if err := os.Remove(filepath.Join(dataPath, "branches", begin.TransactionId+".state.json")); err != nil {
		t.Fatalf("remove state record: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	// Recovery must roll the harmless transaction back, not wedge startup.
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions with missing state record: %v", err)
	}
	if _, err := os.Stat(branchDBPath); !os.IsNotExist(err) {
		t.Fatalf("missing-state transaction branch DB was not rolled back: %v", err)
	}
	if err := reopenedGit.WithGitLock(func() error {
		exists, err := reopenedGit.BranchExists(ctx, begin.TransactionId)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("missing-state transaction git branch still exists")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Startup continues: a fresh transaction begins normally.
	if _, err := restarted.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{}); err != nil {
		t.Fatalf("BeginTransaction after missing-state recovery: %v", err)
	}
}

// refreshTailPersistFailingStore simulates a crash at the tail of a
// RefreshTransaction: it fails the third SaveBranchTransactionState for the
// transaction's own key — BeginTransaction's persist, the refresh's pre-swap
// in-progress marker, then the refresh's final persist — leaving the durable
// branch DB swapped in with the re-applied changes and the in-progress marker
// still set. This is exactly the crash state that previously produced silent
// data loss: the swap replaced the durable branch DB (the only durable record
// of the transaction's mutations) with a clean copy of main and the change log
// existed only in memory.
type refreshTailPersistFailingStore struct {
	store.Store
	txID    string
	txSaves int
}

func (s *refreshTailPersistFailingStore) SaveBranchTransactionState(
	ctx context.Context, txID string, state store.BranchTransactionState,
) error {
	if txID == s.txID {
		s.txSaves++
		if s.txSaves == 3 {
			return errors.New("simulated crash at refresh tail")
		}
	}
	return s.Store.SaveBranchTransactionState(ctx, txID, state)
}

// TestRefreshTransaction_MidRefreshCrashPreservesMutations pins the SPEC R9
// change-log-recovery guarantee across the RefreshTransaction branch-DB swap
// (the swap-to-reapply crash window): the swap re-applies the transaction's
// changes onto the replacement branch before the swap, and evicts the old
// branch handle without deleting its files until the atomic rename installs
// the fully re-applied replacement. A crash at any point in the refresh — here
// simulated by failing the final state persist after the swap — leaves the
// durable branch DB carrying the complete change set and the BranchRefreshInProgress
// marker set, so RecoverOpenTransactions reconstructs the FULL change log
// instead of misclassifying the transaction as already committed and deleting
// it (or, in the partial-reapply sub-window, reconstructing a truncated log).
func TestRefreshTransaction_MidRefreshCrashPreservesMutations(t *testing.T) {
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()

	base, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	failing := &refreshTailPersistFailingStore{Store: base}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	srv := NewCartographerServer(
		failing, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	// BeginTransaction already wrote the durable record (the first txID save);
	// the next two txID saves are the refresh's pre-swap in-progress marker and
	// its final persist. Account for the first so the failure fires on the
	// final persist.
	failing.txID = begin.TransactionId
	failing.txSaves = 1
	first, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx-one"}, TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity tx-one: %v", err)
	}
	second, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx-two"}, TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity tx-two: %v", err)
	}
	// Main advances while the transaction is open, forcing a real refresh.
	mainEntity, err := base.CreateEntity(ctx, "Component", "", map[string]string{"name": "main"}, nil, "main")
	if err != nil {
		t.Fatalf("create main entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, mainEntity.Id, "main")

	// The refresh completes its branch-DB swap (re-applying both transaction
	// changes onto the replacement) and then "crashes" at the final state
	// persist: the durable record keeps the in-progress marker and the
	// swapped-in branch DB carries the full change set.
	if _, err := srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err == nil || !strings.Contains(err.Error(), "simulated crash at refresh tail") {
		t.Fatalf("expected refresh tail persist failure, got %v", err)
	}
	// The durable marker distinguishes this mid-refresh crash from a
	// post-merge crash.
	durable, err := base.LoadBranchTransactionState(ctx, begin.TransactionId)
	if err != nil {
		t.Fatalf("load durable state after refresh crash: %v", err)
	}
	if !durable.BranchRefreshInProgress {
		t.Fatal("mid-refresh crash left no BranchRefreshInProgress marker")
	}
	if err := base.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Restart: the transaction's uncommitted mutations must be recovered, not
	// misclassified as already committed and deleted.
	reopened, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	restarted.MarkDBReady()
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions: %v", err)
	}
	state, err := restarted.txManager.Lookup(begin.TransactionId)
	if err != nil {
		t.Fatalf("mid-refresh-crash transaction was not recovered (deleted?): %v", err)
	}
	if !state.BranchRefreshInProgress {
		t.Fatal("recovered mid-refresh transaction lost its in-progress marker")
	}
	diff, err := restarted.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("get recovered diff: %v", err)
	}
	if len(diff.AddedEntities) != 2 {
		t.Fatalf("expected both transaction mutations recovered, got %+v", diff.AddedEntities)
	}
	got := map[string]bool{}
	for _, e := range diff.AddedEntities {
		got[e.Id] = true
	}
	if !got[first.EntityId] || !got[second.EntityId] {
		t.Fatalf("recovered diff missing transaction mutations: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dataPath, "branches", begin.TransactionId+".lbug")); err != nil {
		t.Fatalf("recovered transaction branch DB missing: %v", err)
	}
}

// captureLogHandler collects slog records so a test can assert on recovery
// log output.
type captureLogHandler struct {
	mu       sync.Mutex
	messages []string
}

func (h *captureLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureLogHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = append(h.messages, r.Message)
	return nil
}
func (h *captureLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureLogHandler) WithGroup(string) slog.Handler      { return h }

// TestRecoverOpenTransactionsMidRefreshEmptyDiffRollsBack pins the
// BranchRefreshInProgress guard in RecoverOpenTransactions' empty-diff branch
// (SPEC R9 change-log recovery step 5): a transaction whose durable record
// carries the refresh-in-progress marker AND whose branch DB diff is empty is a
// mid-refresh crash — the branch DB was swapped to a clean copy of main and the
// transaction's changes existed only in the in-memory change log — NOT an
// already-committed transaction. Recovery must roll the branch back loudly
// instead of silently reporting the transaction as committed.
func TestRecoverOpenTransactionsMidRefreshEmptyDiffRollsBack(t *testing.T) {
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()

	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, st)
	mainEntity, err := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "main"}, nil, "main")
	if err != nil {
		t.Fatalf("create main entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, mainEntity.Id, "main")
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "lost"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	// Construct the old swap-to-reapply crash state: the branch DB has been
	// replaced by a clean copy of main (the transaction's change existed only
	// in the in-memory change log, lost at the crash) and the durable record
	// carries the refresh-in-progress marker.
	state, err := st.LoadBranchTransactionState(ctx, begin.TransactionId)
	if err != nil {
		t.Fatalf("load branch state: %v", err)
	}
	state.BranchRefreshInProgress = true
	if err := st.SaveBranchTransactionState(ctx, begin.TransactionId, state); err != nil {
		t.Fatalf("persist marker: %v", err)
	}
	if err := st.CloseBranchDB(ctx, begin.TransactionId); err != nil {
		t.Fatalf("close branch handle: %v", err)
	}
	branchesDir := filepath.Join(dataPath, "branches")
	for _, f := range []string{begin.TransactionId + ".lbug", begin.TransactionId + ".schema.json"} {
		if err := os.Remove(filepath.Join(branchesDir, f)); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove branch file %s: %v", f, err)
		}
	}
	if err := st.CreateBranchDB(ctx, begin.TransactionId); err != nil {
		t.Fatalf("recreate branch DB: %v", err)
	}
	if err := st.ReplicateSchemaToBranch(ctx, begin.TransactionId); err != nil {
		t.Fatalf("replicate schema: %v", err)
	}
	entitiesDir, edgesDir := gs.HydrationDirs()
	if err := st.HydrateBranchFromFiles(ctx, begin.TransactionId, entitiesDir, edgesDir); err != nil {
		t.Fatalf("hydrate clean branch: %v", err)
	}

	oldLogger := slog.Default()
	defer slog.SetDefault(oldLogger)
	captured := &captureLogHandler{}
	slog.SetDefault(slog.New(captured))

	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	restarted.MarkDBReady()
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions: %v", err)
	}
	// The transaction must not be re-registered as active (there is nothing to
	// recover — its changes were never durable) and its branch must be rolled
	// back.
	if _, err := restarted.txManager.Lookup(begin.TransactionId); err == nil {
		t.Fatal("mid-refresh-crash transaction was recovered as active despite an empty branch diff")
	}
	if _, err := os.Stat(filepath.Join(branchesDir, begin.TransactionId+".lbug")); !os.IsNotExist(err) {
		t.Fatalf("mid-refresh-crash branch DB was not rolled back: %v", err)
	}
	// The empty-diff classification must be the loud mid-refresh rollback, not
	// the silent "already committed" claim.
	if !slices.Contains(captured.messages,
		"RecoverOpenTransactions: rolled back transaction interrupted by a mid-refresh crash (never committed)") {
		t.Fatalf("expected loud mid-refresh-crash rollback log, got %v", captured.messages)
	}
}

// TestCommitTransaction_MergeCompletedAckWaitsForPush pins the MergeCompleted
// retry path's WithAck contract (SPEC R10, SPEC:630-634): an acked commit
// retried after the merge landed must wake the sync worker and block until the
// push is delivered — it must not return success while the push flag is still
// set.
func TestCommitTransaction_MergeCompletedAckWaitsForPush(t *testing.T) {
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{
		GitStore:    gs,
		pushEntered: make(chan struct{}),
		pushRelease: make(chan struct{}),
	}
	t.Cleanup(syncGit.releasePush)
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	// Fail only the "persist completed merge" state write so the first commit
	// leaves MergeCompleted=true in memory with the git merge already landed.
	failingStore := &transactionStateFailingStore{
		Store: base,
		fail:  func(state store.BranchTransactionState) bool { return state.MergeCompleted },
	}
	fc := newFakeClock(time.Now())
	sw := NewSyncWorker("https://example.com/repo.git", syncGit, failingStore, fc)
	go sw.Run()
	t.Cleanup(sw.Stop)
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		failingStore, syncGit, opPub, initTestKey(), nil, "https://example.com/repo.git",
		30*time.Second, "test-ns", 30*time.Minute, 100000, WithLadybugPath(ladybugPath), WithSyncWorker(sw),
	)
	srv.MarkDBReady()
	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "merged"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	// First attempt: the fast-forward merge lands on main, but persisting
	// MergeCompleted fails, so CommitTransaction returns before the normal-path
	// push wiring.
	if _, err := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err == nil || !strings.Contains(err.Error(), "state write failure") {
		t.Fatalf("CommitTransaction error=%v", err)
	}
	state, err := srv.txManager.Lookup(begin.TransactionId)
	if err != nil || !state.MergeCompleted {
		t.Fatalf("MergeCompleted not retained: state=%+v err=%v", state, err)
	}
	// Retry with Ack: the MergeCompleted path must wake the worker and block
	// until the sync cycle delivers the push.
	commitDone := make(chan error, 1)
	go func() {
		_, err := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
			TransactionId: begin.TransactionId, Ack: true,
		})
		commitDone <- err
	}()
	select {
	case <-syncGit.pushEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("acked MergeCompleted commit never reached the sync worker's push")
	}
	select {
	case err := <-commitDone:
		t.Fatalf("MergeCompleted commit returned before the push was delivered: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	syncGit.releasePush()
	if err := <-commitDone; err != nil {
		t.Fatalf("MergeCompleted acked commit returned error after the push was delivered: %v", err)
	}
	syncGit.mu.Lock()
	pushCalls := syncGit.pushCalls
	syncGit.mu.Unlock()
	if pushCalls != 1 {
		t.Fatalf("expected exactly 1 push for the acked MergeCompleted commit, got %d", pushCalls)
	}
	if srv.syncWorker.pushNeeded() {
		t.Fatal("push flag not cleared after the acked push")
	}
}

// TestStopGC_ConcurrentCallsDoNotPanic pins the StopGC close-once
// synchronisation: concurrent StopGC calls must not race on the gcStop channel
// close (the select/default idiom is a data race; the fix guards the close with
// a sync.Once).
func TestStopGC_ConcurrentCallsDoNotPanic(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.StartGC()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			srv.StopGC()
		})
	}
	wg.Wait()
	// Sequential idempotency after the concurrent burst.
	srv.StopGC()
}

// TestDeleteEdge_ReturnsFullDeletedEdge pins SPEC R2's "DeleteEdge(id,
// transactionId?) … Returns the deleted edge": the response must carry the
// deleted edge's endpoints and properties (the SDK builds the returned Edge
// from these fields).
func TestDeleteEdge_ReturnsFullDeletedEdge(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	svc, err := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, txID)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	comp, err := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "core"}, nil, txID)
	if err != nil {
		t.Fatalf("create component: %v", err)
	}

	createResp, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  svc.Id,
		ToEntityId:    comp.Id,
		Properties:    map[string]string{"weight": "high"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEdge failed: %v", err)
	}

	deleteResp, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: createResp.EdgeId, TransactionId: txID})
	if err != nil {
		t.Fatalf("DeleteEdge failed: %v", err)
	}
	if deleteResp.EdgeId != createResp.EdgeId {
		t.Fatalf("expected deleted edge ID %q, got %q", createResp.EdgeId, deleteResp.EdgeId)
	}
	if deleteResp.FromEntityId != svc.Id {
		t.Fatalf("deleted edge from-entity = %q, want %q", deleteResp.FromEntityId, svc.Id)
	}
	if deleteResp.ToEntityId != comp.Id {
		t.Fatalf("deleted edge to-entity = %q, want %q", deleteResp.ToEntityId, comp.Id)
	}
	if deleteResp.Properties["weight"] != "high" {
		t.Fatalf("deleted edge properties = %v, want weight=high", deleteResp.Properties)
	}
}

// =========================================================================
// Special-fixer review items (cartographer_server.go)
// =========================================================================

// hydrationDirErrorStore fails the refresh's branch re-hydration with the
// store's ErrInvalidEntityDir sentinel — the error mapStoreError previously
// misclassified as INVALID_ARGUMENT — to pin the SPEC re-hydration INTERNAL
// mapping (SPEC:987).
type hydrationDirErrorStore struct {
	store.Store
	fail bool
}

func (s *hydrationDirErrorStore) HydrateBranchFromFiles(
	ctx context.Context, txID, entitiesDir, edgesDir string,
) error {
	if s.fail {
		return fmt.Errorf("%w: entities directory inconsistent", store.ErrInvalidEntityDir)
	}
	return s.Store.HydrateBranchFromFiles(ctx, txID, entitiesDir, edgesDir)
}

// TestRefreshTransaction_RehydrationFailureIsInternal pins SPEC error-table
// row "Commit serialisation or re-hydration failed" (INTERNAL, SPEC:987) for
// the RefreshTransaction branch re-hydration path: a refresh whose
// HydrateBranchFromFiles fails with the store's ErrInvalidEntityDir sentinel
// must surface INTERNAL, never the INVALID_ARGUMENT the old mapStoreError
// mapping produced. TestRefreshTransaction_HydrationFailureDoesNotAdvanceSyncHead
// covers the plain-error hydration failure; this test covers the sentinel that
// previously hit the removed ErrInvalidEntityDir/ErrInvalidEdgeDir →
// INVALID_ARGUMENT mappings (errors.go).
func TestRefreshTransaction_RehydrationFailureIsInternal(t *testing.T) {
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()
	base, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	dirErr := &hydrationDirErrorStore{Store: base}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	srv := NewCartographerServer(
		dirErr, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	// Main advances while the transaction is open so the refresh re-hydrates.
	interim, err := base.CreateEntity(ctx, "Component", "", map[string]string{"name": "interim"}, nil, "main")
	if err != nil {
		t.Fatalf("create interim entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, interim.Id, "interim")
	// Arm the failure for the refresh's HydrateBranchFromFiles call (the
	// BeginTransaction call has already completed).
	dirErr.fail = true

	_, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{TransactionId: begin.TransactionId})
	if err == nil {
		t.Fatal("expected branch re-hydration failure during refresh")
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal for branch re-hydration failure, got %v (%v)", status.Code(err), err)
	}
}

// TestReadPathTransactionID_ScopesToTransaction pins the SPEC R2 read-path
// positive branch (SPEC:189-194: "When present, the operation is scoped to that
// transaction's isolated LadybugDB instance. When absent, the operation reads
// from main") at the service layer: with a valid active transaction, every
// read-path RPC routed through the handler's transactionId wiring
// (ExecuteCypher, SearchNeighbors, FullTextSearch, ListEntities) must observe
// the transaction's branch data, and the unscoped call must observe main's
// (empty) data. TestReadPathTransactionID_Rejected pins the rejection half
// (invalid/unknown transactionId) and the store-layer TestBranch_*Scoped tests
// pin the store primitive; this test pins the handler pass-through.
func TestReadPathTransactionID_ScopesToTransaction(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string", Required: true}}},
			{Name: "VectorType", EnableVectorIndex: true, Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
		},
	}
	if err := srv.store.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	txID := beginTestTx(t, srv, ctx)
	comp, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "gadget"}, TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	vec, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "VectorType", Properties: map[string]string{"name": "vec"},
		Embedding: []float32{1.0, 0.0, 0.0}, TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity vector: %v", err)
	}

	t.Run("ListEntities scoped to branch", func(t *testing.T) {
		scoped, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{
			EntityType: "Component", PageSize: 100, TransactionId: txID,
		})
		if err != nil {
			t.Fatalf("scoped ListEntities: %v", err)
		}
		if len(scoped.Entities) != 1 || scoped.Entities[0].EntityId != comp.EntityId {
			t.Fatalf("scoped ListEntities = %+v, want the transaction's entity", scoped.Entities)
		}
		unscoped, err := srv.ListEntities(ctx, &flowv1.ListEntitiesRequest{EntityType: "Component", PageSize: 100})
		if err != nil {
			t.Fatalf("unscoped ListEntities: %v", err)
		}
		if len(unscoped.Entities) != 0 {
			t.Fatalf("unscoped ListEntities saw transaction data: %+v", unscoped.Entities)
		}
	})

	t.Run("ExecuteCypher scoped to branch", func(t *testing.T) {
		scoped, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{
			Cypher: "MATCH (n:Component) RETURN n", TransactionId: txID,
		})
		if err != nil {
			t.Fatalf("scoped ExecuteCypher: %v", err)
		}
		if len(scoped.Rows) != 1 {
			t.Fatalf("scoped ExecuteCypher rows = %d, want 1", len(scoped.Rows))
		}
		unscoped, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: "MATCH (n:Component) RETURN n"})
		if err != nil {
			t.Fatalf("unscoped ExecuteCypher: %v", err)
		}
		if len(unscoped.Rows) != 0 {
			t.Fatalf("unscoped ExecuteCypher saw transaction data: %+v", unscoped.Rows)
		}
	})

	t.Run("FullTextSearch scoped to branch", func(t *testing.T) {
		scoped, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{
			Query: "gadget", EntityType: "Component", TransactionId: txID,
		})
		if err != nil {
			t.Fatalf("scoped FullTextSearch: %v", err)
		}
		if len(scoped.Results) != 1 || scoped.Results[0].EntityId != comp.EntityId {
			t.Fatalf("scoped FullTextSearch = %+v, want the transaction's entity", scoped.Results)
		}
		unscoped, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{Query: "gadget", EntityType: "Component"})
		if err != nil {
			t.Fatalf("unscoped FullTextSearch: %v", err)
		}
		if len(unscoped.Results) != 0 {
			t.Fatalf("unscoped FullTextSearch saw transaction data: %+v", unscoped.Results)
		}
	})

	t.Run("SearchNeighbors scoped to branch", func(t *testing.T) {
		scoped, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
			Embedding: []float32{1.0, 0.0, 0.0}, EntityType: "VectorType", TopK: 5, TransactionId: txID,
		})
		if err != nil {
			t.Fatalf("scoped SearchNeighbors: %v", err)
		}
		if len(scoped.Results) != 1 || scoped.Results[0].EntityId != vec.EntityId {
			t.Fatalf("scoped SearchNeighbors = %+v, want the transaction's entity", scoped.Results)
		}
		unscoped, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
			Embedding: []float32{1.0, 0.0, 0.0}, EntityType: "VectorType", TopK: 5,
		})
		if err != nil {
			t.Fatalf("unscoped SearchNeighbors: %v", err)
		}
		if len(unscoped.Results) != 0 {
			t.Fatalf("unscoped SearchNeighbors saw transaction data: %+v", unscoped.Results)
		}
	})
}

// TestSearchNeighbors_MissingCapBeforeTypeCheck pins the SPEC check-order row
// "SearchNeighbors / FullTextSearch: capability → structural" (SPEC:1019) at
// the service layer for SearchNeighbors: a caller lacking READ capability who
// also supplies an unknown entity type must receive PERMISSION_DENIED (the
// capability gate fires first), never the structural INVALID_ARGUMENT from
// errUnknownEntityType. TestSearchNeighbors_NaNBeforeTypeCheck pins the
// NaN-before-type ordering with full capabilities; only this test detects a
// gate reorder that moved capability after the type check.
func TestSearchNeighbors_MissingCapBeforeTypeCheck(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := noReadCtx() // WRITE-only, no READ

	_, err := srv.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{1.0, 2.0, 3.0},
		EntityType: "NonExistentType",
		TopK:       5,
	})
	if err == nil {
		t.Fatal("expected error for missing READ capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied (capability gate first), got %v", status.Code(err))
	}
}

// TestFullTextSearch_MissingCapBeforeTypeCheck pins the same capability →
// structural ordering for FullTextSearch (SPEC:1019).
func TestFullTextSearch_MissingCapBeforeTypeCheck(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := noReadCtx() // WRITE-only, no READ

	_, err := srv.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{
		Query:      "apple",
		EntityType: "NonExistentType",
	})
	if err == nil {
		t.Fatal("expected error for missing READ capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied (capability gate first), got %v", status.Code(err))
	}
}

// TestExecuteCypher_MutationRejectedBeforeCapability pins the SPEC ExecuteCypher
// check order (SPEC:1018: empty query → Cypher syntax → read-only enforcement →
// capability) for a combined fault: a caller lacking READ capability sending a
// mutation statement must receive the read-only enforcement's PERMISSION_DENIED
// ("mutation or DDL Cypher statements are not allowed"), NOT the capability
// gate's denial — the read-only enforcement precedes the capability check.
// Every mutation test uses a full-capability context and every capability test
// uses a read-only statement, so only a combined-fault test can detect a
// reorder that surfaced the capability denial ahead of the read-only rejection.
func TestExecuteCypher_MutationRejectedBeforeCapability(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := noReadCtx() // WRITE-only, no READ
	applyTestSchema(ctx, t, st)

	_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{
		Cypher: "CREATE (n:Component {id: '11111111-1111-1111-1111-111111111111', name: 'x'})",
	})
	if err == nil {
		t.Fatal("expected error for mutation statement from a no-READ caller, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
	if msg := status.Convert(err).Message(); !strings.Contains(msg, "mutation or DDL") {
		t.Fatalf("expected the read-only enforcement's rejection (mutation/DDL), got %q", msg)
	}
}

// TestExecuteCypher_EmptyQueryBeforeCapability pins the SPEC ExecuteCypher
// check order (SPEC:1018: empty query → Cypher syntax → read-only enforcement →
// capability) for the empty-query gate: a caller lacking READ capability
// sending an EMPTY query must receive the empty-query gate's INVALID_ARGUMENT
// ("Empty ExecuteCypher query"), NOT the capability gate's PERMISSION_DENIED —
// the empty-query gate precedes the capability check. The plain
// TestExecuteCypher_EmptyQuery runs with a full-capability context, so only a
// combined fault can detect a reorder that hoisted the capability gate ahead.
func TestExecuteCypher_EmptyQueryBeforeCapability(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := noReadCtx() // WRITE-only, no READ

	_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: ""})
	if err == nil {
		t.Fatal("expected error for empty query from a no-READ caller, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument (empty-query gate first), got %v", status.Code(err))
	}
}

// TestExecuteCypher_InvalidSyntaxBeforeCapability pins the SPEC ExecuteCypher
// check order (SPEC:1018) for the syntax gate: a caller lacking READ capability
// sending a syntactically invalid query must receive the parse gate's
// INVALID_ARGUMENT ("Invalid Cypher syntax"), NOT the capability gate's
// PERMISSION_DENIED — the syntax gate precedes the capability check. The plain
// TestExecuteCypher_InvalidSyntax runs with a full-capability context, so only
// a combined fault can detect a reorder that hoisted the capability gate ahead.
func TestExecuteCypher_InvalidSyntaxBeforeCapability(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := noReadCtx() // WRITE-only, no READ
	applyTestSchema(ctx, t, srv.store)

	_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{
		Cypher: "NOT VALID CYPHER SYNTAX @@@",
	})
	if err == nil {
		t.Fatal("expected error for invalid Cypher syntax from a no-READ caller, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument (syntax gate first), got %v", status.Code(err))
	}
}

// TestGetTransactionDiff_LiveDeletePopulatesDeletionBuckets pins SPEC R2
// GetTransactionDiff's deleted_entities / deleted_edges wire fields for a live
// (non-recovery) transaction: after a DeleteEntity and a DeleteEdge inside an
// open transaction, the RPC response must carry the deleted entities and edges
// in their respective buckets, populated with the payload captured at deletion
// time (properties for the entity, endpoints for the edge). All prior
// assertions on these buckets came from the recovery path (suspected
// deletions), so a regression that stopped populating them for a normal delete
// would previously fail no test.
func TestGetTransactionDiff_LiveDeletePopulatesDeletionBuckets(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()
	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	svc, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Service", Properties: map[string]string{"name": "svc"}, TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity svc: %v", err)
	}
	comp, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "core"}, TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity comp: %v", err)
	}
	edge, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType: "DEPENDS_ON", FromEntityId: svc.EntityId, ToEntityId: comp.EntityId,
		Properties: map[string]string{"weight": "medium"}, TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}
	// A standalone DeleteEdge must populate deleted_edges with its endpoints.
	if _, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: edge.EdgeId, TransactionId: txID}); err != nil {
		t.Fatalf("DeleteEdge: %v", err)
	}
	// Deleting the entity populates deleted_entities with its payload.
	if _, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: svc.EntityId, TransactionId: txID}); err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}

	diff, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: txID})
	if err != nil {
		t.Fatalf("GetTransactionDiff: %v", err)
	}
	if len(diff.DeletedEntities) != 1 || diff.DeletedEntities[0].Id != svc.EntityId {
		t.Fatalf("expected the deleted entity in deleted_entities, got %+v", diff.DeletedEntities)
	}
	if diff.DeletedEntities[0].Suspected {
		t.Fatal("live deletion must not be marked suspected")
	}
	if diff.DeletedEntities[0].Properties["name"] != "svc" {
		t.Fatalf("deleted entity payload dropped: %+v", diff.DeletedEntities[0].Properties)
	}
	if len(diff.DeletedEdges) != 1 || diff.DeletedEdges[0].Id != edge.EdgeId {
		t.Fatalf("expected the deleted edge in deleted_edges, got %+v", diff.DeletedEdges)
	}
	if diff.DeletedEdges[0].Suspected {
		t.Fatal("live deletion must not be marked suspected")
	}
	if diff.DeletedEdges[0].FromEntityId != svc.EntityId || diff.DeletedEdges[0].ToEntityId != comp.EntityId {
		t.Fatalf(
			"deleted edge endpoints dropped: from=%q to=%q",
			diff.DeletedEdges[0].FromEntityId, diff.DeletedEdges[0].ToEntityId,
		)
	}
	if diff.DeletedEdges[0].Properties["weight"] != "medium" {
		t.Fatalf("deleted edge payload dropped: %+v", diff.DeletedEdges[0].Properties)
	}
}

// TestWipeGraph_RemovesUntrackedResidualFiles pins the SPEC R2 WipeGraph
// sentence "performs a git clean -fd on the working tree to remove any
// untracked residual files" (SPEC:207): a wipe must remove a file planted in
// the git working tree so a subsequent re-hydration on restart does not
// encounter stale files from removed types. The gitstore primitive is pinned
// by TestCleanUntracked (gitstore_test.go) and the failure branch by
// TestWipeGraph_GitSideMidWipeFailure; this test seeds an untracked residual
// file and asserts the WipeGraph-level wipe removes it.
func TestWipeGraph_RemovesUntrackedResidualFiles(t *testing.T) {
	ctx := context.Background()
	dataPath := t.TempDir()
	st, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()

	// Plant an untracked residual file inside the tracked entities directory of
	// the git working tree (graph-repo/entities/.gitkeep is tracked, so a new
	// file there is untracked and must be removed by git clean -fd).
	residual := filepath.Join(dataPath, "graph-repo", "entities", "stale-residual.json")
	if err := os.MkdirAll(filepath.Dir(residual), 0o755); err != nil {
		t.Fatalf("mkdir residual dir: %v", err)
	}
	if err := os.WriteFile(residual, []byte(`{"stale":true}`), 0o600); err != nil {
		t.Fatalf("plant residual file: %v", err)
	}

	if _, err := srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{}); err != nil {
		t.Fatalf("WipeGraph: %v", err)
	}
	if _, err := os.Stat(residual); !os.IsNotExist(err) {
		t.Fatalf("untracked residual file survived the wipe: %v", err)
	}
}

// TestSync_MissingCapabilityBeforeRemoteProbe pins the SPEC check-order table
// (SPEC:1023: Sync is "general rule only" — the capability gate) combined with
// SPEC R10's WRITE:graph/entity/* requirement (SPEC:243): the capability gate
// runs before the remote-configuration probe, so a caller holding no WRITE
// capability receives PERMISSION_DENIED regardless of whether a remote is
// configured. Before the fix the remoteURL=="" probe ran first, disclosing
// remote-configuration state to unprivileged callers (FAILED_PRECONDITION when
// no remote is configured). TestSync_MissingWriteCapability pins the
// configured-remote half; this test pins the no-remote half that detects a
// regression moving the capability check after the probe.
func TestSync_MissingCapabilityBeforeRemoteProbe(t *testing.T) {
	srv, _ := newTestServer(t) // remoteURL == "" AND caller lacks WRITE:graph/entity/*
	ctx := narrowCtx("READ:graph/entity/*", "READ:graph/tx")

	_, err := srv.Sync(ctx, &flowv1.SyncRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied (capability gate before remote probe), got %v (%v)", status.Code(err), err)
	}
}

// TestRecoverOpenTransactionsMidSwapMismatchRollsBack pins the branch-DB swap
// crash window (SPEC R9 change-log recovery): resetBranchStoreFromWorkingTree
// swaps the branch DB via a non-atomic rename loop (.lbug → .schema.json →
// .state.json), so a crash between the .lbug rename and the .schema.json rename
// leaves the swapped-in branch DB (built under main's *current* schema) paired
// with the stale pre-refresh .schema.json. When main gained a non-destructive
// R6 schema change during the transaction's lifetime, reopening that branch
// fails hard in restoreBranchSchemaMetadata → validateMetadataAgainstCatalog
// ("database entity type %q is absent from schema metadata"), a hard error the
// old recovery propagated and cmd/main.go treated as fatal (pod crash loop).
// Recovery must roll the mid-swap casualty back loudly instead.
func TestRecoverOpenTransactionsMidSwapMismatchRollsBack(t *testing.T) {
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, st)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	// Main gains a non-destructive R6 schema change during the transaction's
	// lifetime: a new entity type added to the schema.
	altered := &flowv1.Schema{
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
			{Name: "Widget", Properties: []*flowv1.Property{{Name: "label", Type: "string"}}},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "weight", Type: "string"}}},
		},
	}
	if err := st.ApplySchema(ctx, altered); err != nil {
		t.Fatalf("apply additive schema change: %v", err)
	}
	// Build the refresh replacement branch (under main's *current* schema, which
	// now includes Widget) exactly as resetBranchStoreFromWorkingTree does, close
	// it so its WAL is checkpointed, and rename only its .lbug onto the
	// transaction's canonical name — leaving the pre-refresh branches/<txID>.
	// schema.json (which does not declare Widget) in place. This is the on-disk
	// state a crash between the swap's .lbug and .schema.json renames leaves
	// behind.
	const tempID = "99999999-9999-4999-8999-999999999999"
	if err := srv.buildBranchStoreFromWorkingTree(ctx, tempID); err != nil {
		t.Fatalf("build replacement branch: %v", err)
	}
	if err := st.CloseBranchDB(ctx, tempID); err != nil {
		t.Fatalf("close replacement branch: %v", err)
	}
	// The durable state record carries the refresh-in-progress marker across the
	// swap (persisted before the renames begin, cleared only by the refresh's
	// final persist).
	durable, err := st.LoadBranchTransactionState(ctx, begin.TransactionId)
	if err != nil {
		t.Fatalf("load durable state: %v", err)
	}
	durable.BranchRefreshInProgress = true
	if err := st.SaveBranchTransactionState(ctx, begin.TransactionId, durable); err != nil {
		t.Fatalf("persist refresh marker: %v", err)
	}
	// Evict the live in-memory handle for the tx branch so a reopen reloads from
	// the (mismatched) files, then swap in the replacement .lbug.
	if err := st.CloseBranchDB(ctx, begin.TransactionId); err != nil {
		t.Fatalf("close tx branch handle: %v", err)
	}
	branchesDir := filepath.Join(dataPath, "branches")
	if err := os.Rename(
		filepath.Join(branchesDir, tempID+".lbug"),
		filepath.Join(branchesDir, begin.TransactionId+".lbug"),
	); err != nil {
		t.Fatalf("swap replacement .lbug onto tx name: %v", err)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	// Restart: the mismatched branch must be rolled back loudly, not crash-loop
	// startup with a hard error.
	reopened, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	restarted.MarkDBReady()
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions: %v", err)
	}
	if _, err := restarted.txManager.Lookup(begin.TransactionId); err == nil {
		t.Fatalf("expected mid-swap-crash transaction rolled back, got a registered transaction")
	}
	if _, err := os.Stat(filepath.Join(branchesDir, begin.TransactionId+".lbug")); !os.IsNotExist(err) {
		t.Fatalf("rollback did not remove the mismatched branch DB: %v", err)
	}
}

// TestRecoverOpenTransactionsCleansOrphanedRefreshTempBranches pins the
// mid-refresh swap crash-window cleanup (SPEC R9 change-log recovery): the
// refresh branch-DB swap (resetBranchStoreFromWorkingTree) builds the
// replacement branch under a temporary key (tempID := s.newIDFn()) and renames
// branches/<tempID>.{lbug,schema.json,state.json} onto the transaction's
// canonical names, so a crash after some but not all of the swap's renames
// strands the not-yet-renamed temp files under branches/. The temporary key
// never becomes a git branch, so recovery's git-branch enumeration never
// visits it — without the sweep every mid-refresh crash leaks the orphaned
// temp files (plus the engine's write-ahead-log companion) indefinitely. The
// sweep must remove the orphaned temp files while leaving live transaction
// branches (git branch + branch files) untouched.
func TestRecoverOpenTransactionsCleansOrphanedRefreshTempBranches(t *testing.T) {
	ctx := testCtx()
	dataPath := t.TempDir()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, st)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	branchesDir := filepath.Join(dataPath, "branches")

	// Replicate the swap's on-disk state exactly as resetBranchStoreFromWorkingTree
	// builds it, then crash after the first rename: build the replacement under
	// a temp key, mark the refresh in progress, mirror the durable state
	// record, re-apply the transaction's changes, close the replacement (its
	// WAL checkpointed), and rename only the temp .lbug onto the transaction's
	// canonical name — leaving the temp .schema.json and .state.json (and the
	// engine's torn WAL companion) orphaned under the temp key.
	const tempID = "88888888-8888-4888-8888-888888888888"
	if err := srv.buildBranchStoreFromWorkingTree(ctx, tempID); err != nil {
		t.Fatalf("build replacement branch: %v", err)
	}
	state, lookupErr := srv.txManager.Lookup(begin.TransactionId)
	if lookupErr != nil {
		t.Fatalf("look up transaction state: %v", lookupErr)
	}
	state.BranchRefreshInProgress = true
	if err := srv.persistTransactionState(ctx, state); err != nil {
		t.Fatalf("persist refresh marker: %v", err)
	}
	if err := st.SaveBranchTransactionState(ctx, tempID, durableTransactionState(state)); err != nil {
		t.Fatalf("persist temp state record: %v", err)
	}
	if err := srv.reapplyTransactionChanges(ctx, tempID, state.ChangeLog); err != nil {
		t.Fatalf("re-apply transaction changes onto replacement: %v", err)
	}
	if err := st.CloseBranchDB(ctx, tempID); err != nil {
		t.Fatalf("close replacement branch: %v", err)
	}
	// Evict the live in-memory handle for the tx branch (its files stay on
	// disk) so the rename below does not race the engine's handle, mirroring
	// the swap's CloseBranchDB(txID) before its renames.
	if err := st.CloseBranchDB(ctx, begin.TransactionId); err != nil {
		t.Fatalf("close tx branch handle: %v", err)
	}
	if err := os.Rename(
		filepath.Join(branchesDir, tempID+".lbug"),
		filepath.Join(branchesDir, begin.TransactionId+".lbug"),
	); err != nil {
		t.Fatalf("swap replacement .lbug onto tx name: %v", err)
	}
	// The engine's write-ahead-log companion (<key>.lbug.wal) is the artifact a
	// hard crash tears alongside the database file; plant it so the sweep's WAL
	// cleanup is pinned.
	walPath := filepath.Join(branchesDir, tempID+".lbug.wal")
	if err := os.WriteFile(walPath, []byte("torn wal"), 0600); err != nil {
		t.Fatalf("plant torn temp WAL: %v", err)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	// Restart: the orphaned temp files must be swept while the canonical
	// transaction branch survives recovery.
	reopened, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	restarted.MarkDBReady()
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions: %v", err)
	}
	// The orphaned refresh temp files (including the torn WAL) are gone.
	for _, name := range []string{
		tempID + ".lbug", tempID + ".schema.json", tempID + ".state.json", tempID + ".lbug.wal",
	} {
		if _, err := os.Stat(filepath.Join(branchesDir, name)); !os.IsNotExist(err) {
			t.Fatalf("orphaned refresh temp file %q survived recovery: %v", name, err)
		}
	}
	// The canonical transaction branch survives: it is recovered as a live
	// transaction and its branch DB is intact.
	if _, err := restarted.txManager.Lookup(begin.TransactionId); err != nil {
		t.Fatalf("canonical transaction branch was not recovered: %v", err)
	}
	if _, err := os.Stat(filepath.Join(branchesDir, begin.TransactionId+".lbug")); err != nil {
		t.Fatalf("canonical transaction branch DB missing: %v", err)
	}
}
