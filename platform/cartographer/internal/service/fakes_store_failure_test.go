package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/foundry/flow/cartographer/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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

type healthFailingStore struct {
	store.Store
}

func (s *healthFailingStore) Health(ctx context.Context) (*store.HealthResult, error) {
	return nil, fmt.Errorf("store health probe failed: pvc unreadable")
}

// exhaustedStore fails deterministically during export enumeration with a
// RESOURCE_EXHAUSTED gRPC status, exercising the collectExportData error path.
type exhaustedStore struct {
	store.Store
}

func (e *exhaustedStore) ListMainEntityTypes() ([]string, error) {
	return nil, status.Error(codes.ResourceExhausted, "simulated enumeration capacity exceeded")
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
