package service

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

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
