package service

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
	observedGit := &fakeGitStore{GitStore: gs, onWithGitLock: func(fn func() error) error {
		lockHeld.Store(true)
		defer lockHeld.Store(false)
		return gs.WithGitLock(fn)
	}}
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
