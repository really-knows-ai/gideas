package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
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

type gcBlockingStore struct {
	store.Store
	wipeEntered chan struct{}
	releaseWipe chan struct{}
}

func (s *gcBlockingStore) RehydrateMainFromFiles(ctx context.Context, entitiesDir, edgesDir string) error {
	close(s.wipeEntered)
	<-s.releaseWipe
	return s.Store.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
}

func (s *dropFailingStore) DropBranchDB(ctx context.Context, txID string) error {
	if s.failDrop {
		s.failDrop = false
		return fmt.Errorf("simulated DropBranchDB failure")
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

type pullGitStore struct {
	gitAttemptStore
}

func (s *pullGitStore) IsEmpty(context.Context) (bool, error)    { return false, nil }
func (s *pullGitStore) PullAndFastForward(context.Context) error { return nil }
func (s *pullGitStore) FetchAndMerge(context.Context, string, string) (plumbing.Hash, error) {
	return plumbing.ZeroHash, nil
}

type pullHydrationBlockingStore struct {
	store.Store
	entered chan struct{}
	release chan struct{}
}

func (s *pullHydrationBlockingStore) RehydrateMainFromFiles(
	ctx context.Context, entitiesDir, edgesDir string,
) error {
	close(s.entered)
	<-s.release
	return s.Store.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
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

func invokePullFromRemote(
	srv *CartographerServer, ctx context.Context,
) (handlerInvoked bool, verifiedCaps *Capabilities, err error) {
	_, err = srv.verifier.VerifyInterceptor(
		ctx,
		&flowv1.PullFromRemoteRequest{},
		&grpc.UnaryServerInfo{Server: srv, FullMethod: flowv1.CartographerService_PullFromRemote_FullMethodName},
		func(ctx context.Context, req any) (any, error) {
			handlerInvoked = true
			verifiedCaps, _ = ExtractCapabilities(ctx)
			return srv.PullFromRemote(ctx, req.(*flowv1.PullFromRemoteRequest))
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

// newTestServer creates a CartographerServer with in-memory store and temp gitstore.
func newTestServer(t *testing.T) (*CartographerServer, store.Store) {
	t.Helper()
	scPub := initTestKey()
	st, err := ladybug.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
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
	st, _ := ladybug.OpenInMemory()
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

func TestCapability_InvalidSignature(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	_, wrongPriv := generateTestKey()
	st, _ := ladybug.OpenInMemory()
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
	st, _ := ladybug.OpenInMemory()
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

func TestCapability_UnrecognizedSigner(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := ladybug.OpenInMemory()
	t.Cleanup(func() { _ = st.Close() })
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

func TestCapability_MissingSignedBy(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := ladybug.OpenInMemory()
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
		MetadataKeyCapabilities, "READ:graph/entity/Component",
		MetadataKeyCapabilitiesSignature, sig,
		MetadataKeyCapabilitiesSignedAt, "1234567890",
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

	handlerInvoked, _, err := invokePullFromRemote(srv, ctx)
	if handlerInvoked {
		t.Fatal("unary handler ran for stale capability")
	}
	if status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != "stale capability (anti-replay)" {
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
	if status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != "stale capability (anti-replay)" {
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
	srv.txManager = NewTransactionManager(30*time.Minute, 7*24*time.Hour, 100000, WithClock(fc))
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

// TestRowsToTuplesDeterministicColumnAligned asserts R2's deterministic
// "flat tuples" contract: value order is a stable sorted column set, and every
// tuple lists values in that same column order (null-filling absent columns),
// independent of Go's randomised map iteration order.
func TestRowsToTuplesDeterministicColumnAligned(t *testing.T) {
	rows := []map[string]any{
		{"b": 2, "a": "x", "c": 3.5},
		{"b": 4, "a": "y"},
		{"c": false},
	}
	tuples := rowsToTuples(rows)
	if len(tuples) != 3 {
		t.Fatalf("expected 3 tuples, got %d", len(tuples))
	}
	cases := []struct {
		values []any // nil => null column
	}{
		{[]any{"x", float64(2), 3.5}},
		{[]any{"y", float64(4), nil}},
		{[]any{nil, nil, false}},
	}
	wantCols := []string{"a", "b", "c"}
	for i, tc := range cases {
		if len(tuples[i].Values) != 3 {
			t.Fatalf("tuple %d: expected 3 values, got %d", i, len(tuples[i].Values))
		}
		for j, want := range wantCols {
			got := tuples[i].Values[j]
			switch wantVal := tc.values[j].(type) {
			case nil:
				if _, ok := got.Kind.(*structpb.Value_NullValue); !ok {
					t.Fatalf("tuple %d col %q: expected null, got kind %T", i, want, got.Kind)
				}
			case string:
				if got.GetStringValue() != wantVal {
					t.Fatalf("tuple %d col %q: expected string %q, got val %q", i, want, wantVal, got.GetStringValue())
				}
			case bool:
				if got.GetBoolValue() != wantVal {
					t.Fatalf("tuple %d col %q: expected bool %v, got %v", i, want, wantVal, got.GetBoolValue())
				}
			case float64:
				if got.GetNumberValue() != wantVal {
					t.Fatalf("tuple %d col %q: expected number %v, got %v", i, want, wantVal, got.GetNumberValue())
				}
			}
		}
	}
}

func TestExecuteCypher_MutationRejected(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()
	applyTestSchema(ctx, t, st)

	// Each mutation/DDL clause the SPEC R7 §5 and error-table row 910 enumerate
	// (CREATE, SET, DELETE, MERGE, REMOVE, DROP, DDL index/constraint, and
	// FOREACH-as-mutation) must be rejected by ExecuteCypher so no mutation ever
	// executes through the read-only RPC.
	//
	// LadybugDB v0.17.0's parser recognises CREATE/SET/DELETE/MERGE/DROP and
	// classifies each as non-read-only, surfacing ErrMutationCypher which the
	// service maps to PERMISSION_DENIED. Its grammar does not parse top-level
	// FOREACH, `MATCH ... REMOVE ...`, or index/constraint DDL, so those fail at
	// Prepare with ErrInvalidCypher (which the service maps to INVALID_ARGUMENT)
	// before the read-only guard runs — they cannot execute either. The security
	// property (a mutation can never execute through ExecuteCypher) holds for the
	// whole set; the error code varies by whether the grammar can classify the AST
	// root as a mutation.
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
		{"ddl-constraint", "CREATE CONSTRAINT IF NOT EXISTS FOR (n:Component) REQUIRE n.id IS UNIQUE", codes.InvalidArgument},
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
	srv, _ := newTestServer(t)
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
	srv, _ := newTestServer(t)
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
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component",
		Properties: map[string]string{},
	})
	if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "missing required property") {
		t.Fatalf("CreateEntity without required property should fail: %v", err)
	}
}

func TestUpdateEntity_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:         "11111111-1111-4111-8111-111111111111",
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
	srv, _ := newTestServer(t)
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
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	_, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{
		Id: "11111111-1111-4111-8111-111111111111",
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
	_, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: "not-a-uuid"})
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
	ent, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "delete-me"}, nil, "")

	resp, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: ent.Id})
	if err != nil {
		t.Fatalf("DeleteEntity failed: %v", err)
	}
	if resp.EntityId != ent.Id {
		t.Fatalf("expected deleted entity ID %q, got %q", ent.Id, resp.EntityId)
	}
}

// TestDeleteEntity_NonTransactionalCascade verifies SPEC R7 §4 atomicity on the
// main (non-transactional) path: deleting an entity outside a transaction must
// remove all edges connected to it atomically with the entity. Only the
// in-transaction cascade path was previously covered at the service layer.
func TestDeleteEntity_NonTransactionalCascade(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	svc, _ := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, "")
	comp, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "core"}, nil, "")

	edgeResp, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType: "DEPENDS_ON", FromEntityId: svc.Id, ToEntityId: comp.Id,
	})
	if err != nil {
		t.Fatalf("CreateEdge failed: %v", err)
	}

	// Non-transactional DeleteEntity applies directly to main.
	if _, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: svc.Id}); err != nil {
		t.Fatalf("non-transactional DeleteEntity failed: %v", err)
	}

	// The entity must be gone.
	if _, err := srv.store.GetEntity(ctx, svc.Id, ""); err == nil {
		t.Fatal("expected deleted entity to be gone")
	}
	// The connected edge must have been removed atomically with the entity.
	remaining, err := srv.store.ListEdgesOfType(ctx, "DEPENDS_ON", "")
	if err != nil {
		t.Fatalf("ListEdgesOfType failed: %v", err)
	}
	for _, e := range remaining {
		if e.Id == edgeResp.EdgeId {
			t.Fatalf("expected cascade-deleted edge %q to be removed on main", edgeResp.EdgeId)
		}
	}
	if len(remaining) != 0 {
		t.Fatalf("expected no DEPENDS_ON edges to remain, got %d", len(remaining))
	}
	// The other participant is untouched by the cascade.
	if _, err := srv.store.GetEntity(ctx, comp.Id, ""); err != nil {
		t.Fatalf("non-adjacent entity must survive the cascade: %v", err)
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
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:     "DEPENDS_ON",
		FromEntityId: "11111111-1111-4111-8111-111111111111",
		ToEntityId:   "22222222-2222-4222-8222-222222222222",
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

// TestCreateEdge_UnknownEdgeTypeWinsOverMissingCapability asserts the SPEC
// validation order (structural before capability): a caller lacking
// WRITE:graph/entity/* still gets INVALID_ARGUMENT (not PERMISSION_DENIED)
// for an unknown edge type.
func TestCreateEdge_UnknownEdgeTypeWinsOverMissingCapability(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	ent, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "x"}, nil, "")

	// Only READ capabilities — the caller holds no write capability at all.
	noWriteCtx := capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")
	_, err := srv.CreateEdge(noWriteCtx, &flowv1.CreateEdgeRequest{
		EdgeType:     "UNKNOWN_EDGE",
		FromEntityId: ent.Id,
		ToEntityId:   ent.Id,
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
	_, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{
		Id: "11111111-1111-4111-8111-111111111111",
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
	_, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: "not-a-uuid"})
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
// TestCreateEdge_UnknownEdgeTypeWinsOverMissingCapability.
func TestCreateEntity_InvalidIDWinsOverMissingCapability(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	// Only READ capabilities — the caller holds no write capability at all.
	noWriteCtx := capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")
	_, err := srv.CreateEntity(noWriteCtx, &flowv1.CreateEntityRequest{
		EntityType: "Component",
		Id:         "not-a-uuid",
		Properties: map[string]string{"name": "x"},
	})
	if err == nil {
		t.Fatal("expected error for invalid entity ID, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected structural InvalidArgument to win over capability check, got %v (%v)", status.Code(err), err)
	}
}

// TestUpdateEntity_EmbeddingUpdateUnsupported drives UpdateEntity on an
// established vector-indexed row and asserts the resulting gRPC code. The store
// surfaces ErrEmbeddingUpdateUnsupported when the engine cannot rewrite an
// embedding once the vector index exists; the service must map it to a defined
// SPEC code (INVALID_ARGUMENT, not codes.Unimplemented which has no SPEC row).
func TestUpdateEntity_EmbeddingUpdateUnsupported(t *testing.T) {
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
	// Bootstrap a 3-dim vector index via CreateEntity with an embedding.
	ent, err := srv.store.CreateEntity(
		ctx, "VecType", "", map[string]string{"name": "seeded"}, []float32{1.0, 0.0, 0.0}, "",
	)
	if err != nil {
		t.Fatalf("bootstrap CreateEntity: %v", err)
	}
	_, err = srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:         ent.Id,
		Embedding:  []float32{0.0, 1.0, 0.0},
		Properties: map[string]string{"name": "updated"},
	})
	if err == nil {
		t.Fatal("expected embedding update on indexed row to be rejected, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for unsupported embedding update, got %v (%v)", status.Code(err), err)
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
	assertTerminal := func(name string, call func() error) {
		t.Helper()
		if err := call(); status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "rollback-only") {
			t.Fatalf("%s error = %v, want rollback-only FailedPrecondition", name, err)
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
	base, err := ladybug.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
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
	base, err := ladybug.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
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
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition without LADYBUG_DB_PATH, got %v", err)
	}
	if err = srv.txManager.ValidateActive(begin.TransactionId); err != nil {
		t.Fatalf("transaction was deregistered after explicit restoration failure: %v", err)
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
	base, err := ladybug.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
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
	if err = srv.txManager.ValidateActive(begin.TransactionId); err != nil {
		t.Fatalf("transaction was not retryable after cleanup failure: %v", err)
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
	base, err := ladybug.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
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
	base, err := ladybug.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
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
	if err == nil {
		t.Fatal("expected post-commit rehydration failure")
	}
	state, lookupErr := srv.txManager.Lookup(begin.TransactionId)
	if lookupErr != nil || !state.CommitCreated || state.MergeCompleted {
		t.Fatalf("unexpected retry state: state=%+v error=%v", state, lookupErr)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "late"}, TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected mutation rejection after commit creation, got %v", err)
	}
	_, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{TransactionId: begin.TransactionId})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected refresh rejection after commit creation, got %v", err)
	}
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("retry CommitTransaction: %v", err)
	}
	if countingGit.commits != 1 {
		t.Fatalf("expected one transaction commit, got %d", countingGit.commits)
	}
}

func TestCommitTransaction_CommitFailureWithoutCommitAllowsRefreshAndRetry(t *testing.T) {
	base, err := ladybug.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
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
	}); err == nil {
		t.Fatal("expected commit failure")
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
	if _, err = restarted.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "late"},
		TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected mutation rejection after commit creation, got %v", err)
	}
	if _, err = restarted.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected refresh rejection after commit creation, got %v", err)
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
			base, err := ladybug.OpenInMemory()
			if err != nil {
				t.Fatalf("OpenInMemory: %v", err)
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
			base, err := ladybug.OpenInMemory()
			if err != nil {
				t.Fatalf("OpenInMemory: %v", err)
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
			if tc.commitCreated && status.Code(refreshErr) != codes.FailedPrecondition {
				t.Fatalf("refresh after created commit error=%v", refreshErr)
			}
			if !tc.commitCreated && refreshErr != nil {
				t.Fatalf("refresh after pre-commit failure: %v", refreshErr)
			}
			if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
				TransactionId: begin.TransactionId,
			}); err != nil {
				t.Fatalf("retry CommitTransaction: %v", err)
			}
			if countingGit.commits != 1 {
				t.Fatalf("expected one Git commit, got %d", countingGit.commits)
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
		wantMessage   string
	}{
		{name: "main advanced", advanceMain: true, wantMessage: "main has advanced"},
		{name: "schema advanced", advanceSchema: true, wantMessage: "schema changed"},
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
			if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
				EntityType: "Component", Properties: map[string]string{"name": "pending"},
				TransactionId: begin.TransactionId,
			}); err != nil {
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
	base, err := ladybug.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
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
	_, err = srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{TransactionId: begin.TransactionId})
	if status.Code(err) != codes.FailedPrecondition {
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

// =========================================================================
// 7. Concurrent access tests
// =========================================================================

func TestConcurrentNonTxWrites(t *testing.T) {
	srv, _ := newTestServer(t)
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

func TestRefreshTransaction_NoConflicts(t *testing.T) {
	srv, _ := newTestServer(t)
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

func TestRefreshTransaction_ConcurrentCommitCannotUseStaleHead(t *testing.T) {
	base, err := ladybug.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
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
	base, err := ladybug.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
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
	base, err := ladybug.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
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
	txID := "11111111-1111-4111-8111-111111111111"
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
	err = srv.validateRefresh(state, gitGraphSnapshot{}, gitGraphSnapshot{})
	if status.Code(err) != codes.Aborted {
		t.Fatalf("expected ABORTED dimension-scope refresh conflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "refresh conflict") {
		t.Fatalf("expected refresh-conflict message, got %q", err.Error())
	}
}

func TestRecoveryDiffPropagatesSuspectedDeletions(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()
	txID := "11111111-1111-4111-8111-111111111111"
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
	if err := gs.Close(); err != nil {
		t.Fatalf("close git store: %v", err)
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
	t.Cleanup(func() { _ = reopenedGit.Close() })
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
	if err := gs.Close(); err != nil {
		t.Fatalf("close git store: %v", err)
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
	t.Cleanup(func() { _ = reopenedGit.Close() })
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
	for i, version := range []string{"first", "rejected"} {
		_, err = srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
			Id: mainEntity.Id, Properties: map[string]string{"version": version}, TransactionId: begin.TransactionId,
		})
		if i == 0 && err != nil {
			t.Fatalf("first update: %v", err)
		}
	}
	if status.Code(err) != codes.ResourceExhausted || !strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("rejected update error = %v", err)
	}
	markerPath := filepath.Join(dataPath, "branches", begin.TransactionId+".state.json")
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("rollback-only marker was not persisted: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := gs.Close(); err != nil {
		t.Fatalf("close git store: %v", err)
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
	t.Cleanup(func() { _ = reopenedGit.Close() })
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
	if _, err := restarted.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("commit rollback-only transaction: %v", err)
	}
	if _, err := restarted.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: mainEntity.Id, Properties: map[string]string{"version": "late"}, TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.FailedPrecondition {
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
	_, err = srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: mainEntity.Id, Properties: map[string]string{"version": "rejected"}, TransactionId: begin.TransactionId,
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
	if err := gs.Close(); err != nil {
		t.Fatalf("close git store: %v", err)
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
	t.Cleanup(func() { _ = reopenedGit.Close() })
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second,
		"test-ns", 30*time.Minute, 1, WithLadybugPath(dataPath),
	)
	if err := restarted.RecoverOpenTransactions(ctx); err == nil || !strings.Contains(err.Error(), "branch state") {
		t.Fatalf("restart did not fail closed: %v", err)
	}
	if _, err := restarted.txManager.Lookup(begin.TransactionId); err == nil {
		t.Fatal("failed-closed transaction was registered as active")
	}
}

func TestRecoverOpenTransactionsRestoresSchemaBaseline(t *testing.T) {
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
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "persisted"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("create transaction entity: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := gs.Close(); err != nil {
		t.Fatalf("close git store: %v", err)
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
	t.Cleanup(func() { _ = reopenedGit.Close() })
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("recover transactions: %v", err)
	}
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
		},
		EdgeTypes: []*flowv1.EdgeType{{
			Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "weight", Type: "string"}},
		}},
	}); err != nil {
		t.Fatalf("change schema: %v", err)
	}
	if _, err := restarted.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "schema changed") {
		t.Fatalf("commit with changed schema should fail: %v", err)
	}
}

func TestRecoverOpenTransactionsRetainsCorruptBranch(t *testing.T) {
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
	t.Cleanup(func() { _ = gs.Close() })
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
	if err := srv.RecoverOpenTransactions(ctx); err == nil {
		t.Fatal("expected corrupt branch recovery to fail")
	}
	if _, err := os.Stat(branchPath); err != nil {
		t.Fatalf("corrupt branch was removed: %v", err)
	}
	if err := gs.WithGitLock(func() error {
		exists, err := gs.BranchExists(ctx, txID)
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("git branch was removed")
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
	if err := gs.Close(); err != nil {
		t.Fatalf("close git store: %v", err)
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
	t.Cleanup(func() { _ = reopenedGit.Close() })
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
			st, err := ladybug.OpenInMemory()
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			gs, err := gitstore.New(t.TempDir())
			if err != nil {
				t.Fatalf("open git store: %v", err)
			}
			t.Cleanup(func() { _ = gs.Close() })
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
			if err := restarted.txManager.ValidateActive(begin.TransactionId); status.Code(err) != codes.NotFound {
				t.Fatalf("recovery registered transaction after lookup failure: %v", err)
			}
		})
	}
}

func TestRecoverOpenTransactionsIdenticalCleanupIsRetryable(t *testing.T) {
	for _, operation := range []string{"restore", "clean", "drop", "delete"} {
		t.Run(operation, func(t *testing.T) {
			ctx := testCtx()
			st, err := ladybug.OpenInMemory()
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			gs, err := gitstore.New(t.TempDir())
			if err != nil {
				t.Fatalf("open git store: %v", err)
			}
			t.Cleanup(func() { _ = gs.Close() })
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
	st, _ := ladybug.OpenInMemory()
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

func TestWipeGraph_WaitsForBeginSetupAndSeesRegisteredTransaction(t *testing.T) {
	base, err := ladybug.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
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
	base, err := ladybug.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
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
// 11. PullFromRemote error-path tests
// =========================================================================

func TestPullFromRemote_RemoteNotConfigured(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	_, err := srv.PullFromRemote(ctx, &flowv1.PullFromRemoteRequest{})
	if err == nil {
		t.Fatal("expected error for no remote configured, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", status.Code(err))
	}
}

func TestPullFromRemote_HoldsGitLockThroughHydration(t *testing.T) {
	base, err := ladybug.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	blocking := &pullHydrationBlockingStore{Store: base, entered: make(chan struct{}), release: make(chan struct{})}
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	pullGit := &pullGitStore{gitAttemptStore: gitAttemptStore{GitStore: gs}}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		blocking, pullGit, opPub, initTestKey(), nil, "https://example.invalid/repo.git",
		30*time.Second, "test-ns", 30*time.Minute, 100000, WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	pullDone := make(chan error, 1)
	go func() {
		_, pullErr := srv.PullFromRemote(ctx, &flowv1.PullFromRemoteRequest{})
		pullDone <- pullErr
	}()
	<-blocking.entered
	attempted := make(chan struct{})
	pullGit.setAttempted(attempted)
	unrelatedDone := make(chan error, 1)
	go func() { unrelatedDone <- srv.withGitLock(func() error { return nil }) }()
	<-attempted
	close(blocking.release)
	if err := <-pullDone; err != nil {
		t.Fatalf("PullFromRemote: %v", err)
	}
	if err := <-unrelatedDone; err != nil {
		t.Fatalf("unrelated Git checkout: %v", err)
	}
}

// TestPullFromRemote_RehydrationFailure asserts the SPEC error-table row
// "PullFromRemote re-hydration failed -> INTERNAL": the pull succeeds but
// re-hydrating main from the pulled files fails, returning the
// errPullFromRemoteRehydrationFailed INTERNAL status.
func TestPullFromRemote_RehydrationFailure(t *testing.T) {
	base, err := ladybug.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	// remoteErrorGitStore drives the pull success path (FetchAndMerge yields a
	// hash); the store's RehydrateMainFromFiles fails on the first call.
	failingStore := &rehydrateFailingStore{Store: base, fail: true}
	remoteGit := &remoteErrorGitStore{GitStore: gs, empty: false, err: nil}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		failingStore, remoteGit, opPub, initTestKey(), nil, "https://example.com/repo.git",
		30*time.Second, "test-ns", 30*time.Minute, 100000, WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()

	_, pullErr := srv.PullFromRemote(testCtx(), &flowv1.PullFromRemoteRequest{})
	if pullErr == nil {
		t.Fatal("expected re-hydration failure, got nil")
	}
	if status.Code(pullErr) != codes.Internal {
		t.Fatalf("expected INTERNAL, got %v (%v)", status.Code(pullErr), pullErr)
	}
	if !strings.Contains(pullErr.Error(), "pull from remote re-hydration failed") {
		t.Fatalf("expected re-hydration failure message, got %q", pullErr.Error())
	}
}

// TestPullFromRemote_RehydratesWithoutLadybugPath asserts SPEC R10: after a
// successful pull, main is re-hydrated unconditionally — even in in-memory
// mode (ladybugPath unset) — so it does not go stale. The hydration count is
// observed via a counting store wrapper.
func TestPullFromRemote_RehydratesWithoutLadybugPath(t *testing.T) {
	base, err := ladybug.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	counting := &hydrationCountingStore{Store: base}
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	remoteGit := &remoteErrorGitStore{GitStore: gs, empty: false, err: nil}
	opPub, _ := generateTestKey()
	// No WithLadybugPath option — simulates in-memory mode.
	srv := NewCartographerServer(
		counting, remoteGit, opPub, initTestKey(), nil, "https://example.com/repo.git",
		30*time.Second, "test-ns", 30*time.Minute, 100000,
	)
	srv.MarkDBReady()

	_, pullErr := srv.PullFromRemote(testCtx(), &flowv1.PullFromRemoteRequest{})
	if pullErr != nil {
		t.Fatalf("PullFromRemote: %v", pullErr)
	}
	if counting.fromFiles != 1 {
		t.Fatalf("expected main re-hydration from files in in-memory mode, got fromFiles=%d", counting.fromFiles)
	}
}

//nolint:dupl // PullFromRemote negative tests share setup structure.
func TestPullFromRemote_MissingWriteCapability(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := ladybug.OpenInMemory()
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil,
		"https://example.com/repo.git", 30*time.Second, "test-ns",
		30*time.Minute, 100000)
	srv.MarkDBReady()

	// Only READ capabilities, no WRITE:graph/entity/*.
	ctx := capabilityContext("READ:graph/entity/*,READ:graph/tx", scPriv, "sidecar")
	handlerInvoked, verifiedCaps, err := invokePullFromRemote(srv, ctx)
	if !handlerInvoked {
		t.Fatal("unary interceptor did not invoke PullFromRemote")
	}
	if verifiedCaps == nil || len(verifiedCaps.Caps) != 2 ||
		verifiedCaps.Caps[0] != "READ:graph/entity/*" || verifiedCaps.Caps[1] != "READ:graph/tx" {
		t.Fatalf("handler did not receive interceptor-verified READ capabilities: %+v", verifiedCaps)
	}
	if status.Code(err) != codes.PermissionDenied ||
		status.Convert(err).Message() != "capability denied: WRITE:graph/entity/*" {
		t.Fatalf("expected missing-WRITE PermissionDenied, got %v", err)
	}
}

//nolint:dupl // PullFromRemote negative tests share setup structure.
func TestPullFromRemote_NoRemote(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := ladybug.OpenInMemory()
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	// remoteURL is empty — should fail with FailedPrecondition BEFORE
	// checking capabilities.
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	ctx := capabilityContext("WRITE:graph/entity/*,READ:graph/entity/*", scPriv, "sidecar")
	handlerInvoked, _, err := invokePullFromRemote(srv, ctx)
	if !handlerInvoked {
		t.Fatal("unary interceptor did not invoke PullFromRemote")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", status.Code(err))
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

func TestCommitTransaction_SchemaIncompatible(t *testing.T) {
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
	srv.txManager = NewTransactionManager(30*time.Minute, 7*24*time.Hour, 100000, WithClock(fc))

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

func TestBeginTransaction_TimeoutCapping(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := ladybug.OpenInMemory()
	t.Cleanup(func() { _ = st.Close() })
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
	st, _ := ladybug.OpenInMemory()
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
	failRestore bool
	failClean   bool
	failDelete  bool
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
	st, _ := ladybug.OpenInMemory()
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
			st, _ := ladybug.OpenInMemory()
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
	st, _ := ladybug.OpenInMemory()
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
	st, _ := ladybug.OpenInMemory()
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
	st, _ := ladybug.OpenInMemory()
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
	st, _ := ladybug.OpenInMemory()
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
	svc, _ := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, "")

	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:     "DEPENDS_ON",
		FromEntityId: svc.Id,
		ToEntityId:   "11111111-1111-4111-8111-111111111111",
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

	from, _ := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "from"}, nil, "")
	to, _ := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "to"}, nil, "")

	resp, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType: "DEPENDS_ON", FromEntityId: from.Id, ToEntityId: to.Id,
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
	srv, _ := newTestServer(t)
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
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:     "DEPENDS_ON",
		FromEntityId: "not-a-uuid",
		ToEntityId:   "11111111-1111-4111-8111-111111111111",
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
	srv, _ := newTestServer(t)
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
	srv, _ := newTestServer(t)
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
	srv, _ := newTestServer(t)
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
	srv, _ := newTestServer(t)
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
	st, _ := ladybug.OpenInMemory()
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
	st, _ := ladybug.OpenInMemory()
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

func TestExportGraph_MidStreamFailure(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := ladybug.OpenInMemory()
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
	st, _ := ladybug.OpenInMemory()
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
	st, _ := ladybug.OpenInMemory()
	t.Cleanup(func() { _ = st.Close() })
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
	st, _ := ladybug.OpenInMemory()
	t.Cleanup(func() { _ = st.Close() })
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
	st, _ := ladybug.OpenInMemory()
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

func TestExportGraph_MissingReadCapability(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := ladybug.OpenInMemory()
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

// TestCapability_WildcardFallback verifies that Mode 2 wildcard fallback works:
// a capability "READ:graph/entity/*" should allow reading any entity type even
// without a specific "READ:graph/entity/Component" capability.
func TestCapability_WildcardFallback(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := ladybug.OpenInMemory()
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
	st, _ := ladybug.OpenInMemory()
	t.Cleanup(func() { _ = st.Close() })
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
	st, _ := ladybug.OpenInMemory()
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
// 29. Fixed concurrent non-transactional writes test
// =========================================================================
// 10. Telemetry tests
// =========================================================================

func TestTransactionGC_ExcludesConcurrentMainWriteDuringRehydration(t *testing.T) {
	base, err := ladybug.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	blocking := &gcBlockingStore{Store: base, wipeEntered: make(chan struct{}), releaseWipe: make(chan struct{})}
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
	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(30*time.Minute, 7*24*time.Hour, 100000, WithClock(fc))
	_, err = srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{Timeout: durationpb.New(time.Minute)})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	fc.Advance(2 * time.Minute)
	gcDone := make(chan struct{})
	go func() {
		srv.gcTick()
		close(gcDone)
	}()
	<-blocking.wipeEntered
	gitAttempted := make(chan struct{})
	attemptingGit.setAttempted(gitAttempted)
	unrelatedDone := make(chan error, 1)
	go func() { unrelatedDone <- srv.withGitLock(func() error { return nil }) }()
	<-gitAttempted

	mainWriteAtLock := make(chan struct{})
	srv.beforeWriteLock = func() { close(mainWriteAtLock) }
	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
			EntityType: "Component", Properties: map[string]string{"name": "concurrent"},
		})
		writeDone <- writeErr
	}()
	<-mainWriteAtLock
	srv.beforeWriteLock = nil
	close(blocking.releaseWipe)
	<-gcDone
	if err := <-unrelatedDone; err != nil {
		t.Fatalf("unrelated Git checkout: %v", err)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
}

func TestTelemetry_TransactionGC(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := ladybug.OpenInMemory()
	t.Cleanup(func() { _ = st.Close() })
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
		MetadataKeyCapabilities, capsStr,
		MetadataKeyCapabilitiesSignature, sig,
		MetadataKeyCapabilitiesSignedAt, fmt.Sprintf("%d", signedAt),
		MetadataKeyCapabilitiesSignedBy, "sidecar",
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

	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:     "DEPENDS_ON",
		FromEntityId: "11111111-1111-4111-8111-111111111111",
		ToEntityId:   "22222222-2222-4222-8222-222222222222",
	})
	if err == nil {
		t.Fatal("expected error for not-found source, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

// TestDeleteEdge_NotFound_CapCheckOrder verifies that DeleteEdge returns
// NOT_FOUND when the edge does not exist, even when the caller lacks wildcard
// WRITE capability.
func TestDeleteEdge_NotFound_CapCheckOrder(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("WRITE:graph/entity/Service")

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{
		Id: "11111111-1111-4111-8111-111111111111",
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
	ctx := narrowCtx("WRITE:graph/entity/Component")

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:         "11111111-1111-4111-8111-111111111111",
		Properties: map[string]string{"name": "x"},
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
	ctx := narrowCtx("WRITE:graph/entity/Component")

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{
		Id: "11111111-1111-4111-8111-111111111111",
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
// 31. Remote error-path mapping tests (SPEC error table)
// =========================================================================

// remoteErrorGitStore wraps a gitstore to inject any remote operation failure
// as a sentinel. WithGitLock runs fn inline (no real git lock) so handlers can
// be driven deterministically. CloneSingleBranch is used when the repo is empty
// (IsEmpty returns true); FetchAndMerge is used otherwise.
type remoteErrorGitStore struct {
	gitstore.GitStore
	empty bool
	err   error
}

func (s *remoteErrorGitStore) WithGitLock(fn func() error) error { return fn() }
func (s *remoteErrorGitStore) IsEmpty(ctx context.Context) (bool, error) {
	return s.empty, nil
}
func (s *remoteErrorGitStore) CloneSingleBranch(ctx context.Context, url, branch string) error {
	return s.err
}
func (s *remoteErrorGitStore) FetchAndMerge(ctx context.Context, remote, branch string) (plumbing.Hash, error) {
	return plumbing.ZeroHash, s.err
}

// TestPullFromRemote_MapGitErrorRows drives each SPEC error-table row that
// mapGitError maps but no handler test previously exercised. Each case routes
// the git-store sentinel through the PullFromRemote handler and asserts the
// resulting gRPC status code.
func TestPullFromRemote_MapGitErrorRows(t *testing.T) {
	tests := []struct {
		name       string
		empty      bool
		err        error
		wantCode   codes.Code
		wantSubstr string
	}{
		{
			name:       "remote auth config missing",
			empty:      false,
			err:        gitstore.ErrAuthConfigMissing,
			wantCode:   codes.FailedPrecondition,
			wantSubstr: "auth configuration missing",
		},
		{
			name:       "remote credentials rejected",
			empty:      false,
			err:        gitstore.ErrAuthFailed,
			wantCode:   codes.Unauthenticated,
			wantSubstr: "credentials rejected",
		},
		{
			name:       "unsupported remote URL scheme",
			empty:      true,
			err:        gitstore.ErrUnsupportedURLScheme,
			wantCode:   codes.InvalidArgument,
			wantSubstr: "unsupported remote URL scheme",
		},
		{
			name:       "remote pull diverged",
			empty:      false,
			err:        gitstore.ErrPullDiverged,
			wantCode:   codes.FailedPrecondition,
			wantSubstr: "pull would diverge",
		},
		{
			name:       "push rejected non-fast-forward",
			empty:      false,
			err:        gitstore.ErrPushRejected,
			wantCode:   codes.FailedPrecondition,
			wantSubstr: "push rejected",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, err := ladybug.OpenInMemory()
			if err != nil {
				t.Fatalf("OpenInMemory: %v", err)
			}
			t.Cleanup(func() { _ = base.Close() })
			gs, _ := gitstore.New(t.TempDir())
			remoteGit := &remoteErrorGitStore{GitStore: gs, empty: tt.empty, err: tt.err}
			opPub, _ := generateTestKey()
			srv := NewCartographerServer(
				base, remoteGit, opPub, initTestKey(), nil, "https://example.com/repo.git",
				30*time.Second, "test-ns", 30*time.Minute, 100000,
			)
			srv.MarkDBReady()

			_, pullErr := srv.PullFromRemote(testCtx(), &flowv1.PullFromRemoteRequest{})
			if pullErr == nil {
				t.Fatal("expected error, got nil")
			}
			if status.Code(pullErr) != tt.wantCode {
				t.Fatalf("expected %v, got %v (%v)", tt.wantCode, status.Code(pullErr), pullErr)
			}
			if !strings.Contains(pullErr.Error(), tt.wantSubstr) {
				t.Fatalf("expected message containing %q, got %q", tt.wantSubstr, pullErr.Error())
			}
		})
	}
}

// TestErrNoTransportCredentials covers the SPEC R2 error-table row "Request
// without valid transport credentials → UNAUTHENTICATED". The sentinel is a
// forward-looking placeholder for mTLS (currently unused because all internal
// gRPC uses insecure credentials); the test pins the mapped gRPC status code
// so the contract is locked before mTLS lands.
func TestErrNoTransportCredentials(t *testing.T) {
	got := errNoTransportCredentials()
	if got == nil {
		t.Fatal("expected non-nil error, got nil")
	}
	if code := status.Code(got); code != codes.Unauthenticated {
		t.Fatalf("expected code UNAUTHENTICATED, got %v (%v)", code, got)
	}
	if !strings.Contains(got.Error(), "transport credentials") {
		t.Fatalf("expected message mentioning transport credentials, got %q", got.Error())
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
	base, err := ladybug.OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
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
	st, _ := ladybug.OpenInMemory()
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)

	// Build metadata without the signed-at key (or with an empty value).
	payload := "READ:graph/entity/Component|1234567890"
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(scPriv, []byte(payload)))
	md := metadata.Pairs(
		MetadataKeyCapabilities, "READ:graph/entity/Component",
		MetadataKeyCapabilitiesSignature, sig,
		MetadataKeyCapabilitiesSignedBy, "sidecar",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := srv.verifier.verify(ctx)
	if err == nil {
		t.Fatal("expected error for missing signed-at, got nil")
	}
	if status.Code(err) != codes.PermissionDenied ||
		status.Convert(err).Message() != "invalid capability signature" {
		t.Fatalf("expected PermissionDenied invalid-signature, got %v", err)
	}
}

func TestCapability_EmptySignedAt(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	base, _ := ladybug.OpenInMemory()
	t.Cleanup(func() { _ = base.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(base, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)

	md := metadata.Pairs(
		MetadataKeyCapabilities, "READ:graph/entity/Component",
		MetadataKeyCapabilitiesSignature, base64.StdEncoding.EncodeToString([]byte("fake")),
		MetadataKeyCapabilitiesSignedAt, "",
		MetadataKeyCapabilitiesSignedBy, "sidecar",
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
	st, _ := ladybug.OpenInMemory()
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
		MetadataKeyCapabilities, "READ:graph/entity/Component",
		MetadataKeyCapabilitiesSignature, sig,
		MetadataKeyCapabilitiesSignedBy, "sidecar",
		MetadataKeyCapabilitiesSignedAt, "abc",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := srv.verifier.verify(ctx)
	if err == nil {
		t.Fatal("expected error for malformed signed-at, got nil")
	}
	if status.Code(err) != codes.PermissionDenied ||
		status.Convert(err).Message() != "invalid capability signature" {
		t.Fatalf("expected PermissionDenied invalid-signature, got %v", err)
	}
}

// =========================================================================
// 35. ExecuteCypher read-only caller without entity-types metadata
// =========================================================================

func TestExecuteCypher_NoEntityTypesMetadataReadOnlyDenied(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := noReadCtx() // WRITE-only capabilities, no READ.

	// No x-flow-entity-types metadata: ExecuteCypher falls back to the wildcard
	// READ capability check, which a write-only caller lacks.
	_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: "MATCH (n) RETURN n"})
	if err == nil {
		t.Fatal("expected PermissionDenied for write-only caller, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
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
	base, _ := ladybug.OpenInMemory()
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
