package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/foundry/flow/cartographer/internal/store"
)

// fakeStore wraps a store.Store, overriding selected methods with per-method
// test hooks. A nil hook delegates to the embedded Store, so a failure-injection
// seam is a one-field literal:
//
//	fakeStore{Store: s, onWipeSchema: func(context.Context) error { return errWipe }}
type fakeStore struct {
	store.Store
	onWipeSchema                     func(context.Context) error
	onCreateBranchDB                 func(context.Context, string) error
	onDropBranchDB                   func(context.Context, string) error
	onDeleteEntity                   func(context.Context, string, string) (*store.Entity, error)
	onListMainEntityTypes            func() ([]string, error)
	onHealth                         func(context.Context) (*store.HealthResult, error)
	onRehydrateMainFromFiles         func(context.Context, string, string) error
	onHydrateBranchFromFiles         func(context.Context, string, string, string) error
	onSaveBranchTransactionState     func(context.Context, string, store.BranchTransactionState) error
	onCheckBranchSchemaCompatibility func(context.Context, string) error
}

func (f *fakeStore) WipeSchema(ctx context.Context) error {
	if f.onWipeSchema != nil {
		return f.onWipeSchema(ctx)
	}
	return f.Store.WipeSchema(ctx)
}

func (f *fakeStore) CreateBranchDB(ctx context.Context, txID string) error {
	if f.onCreateBranchDB != nil {
		return f.onCreateBranchDB(ctx, txID)
	}
	return f.Store.CreateBranchDB(ctx, txID)
}

func (f *fakeStore) DropBranchDB(ctx context.Context, txID string) error {
	if f.onDropBranchDB != nil {
		return f.onDropBranchDB(ctx, txID)
	}
	return f.Store.DropBranchDB(ctx, txID)
}

func (f *fakeStore) DeleteEntity(ctx context.Context, id, branch string) (*store.Entity, error) {
	if f.onDeleteEntity != nil {
		return f.onDeleteEntity(ctx, id, branch)
	}
	return f.Store.DeleteEntity(ctx, id, branch)
}

func (f *fakeStore) ListMainEntityTypes() ([]string, error) {
	if f.onListMainEntityTypes != nil {
		return f.onListMainEntityTypes()
	}
	return f.Store.ListMainEntityTypes()
}

func (f *fakeStore) Health(ctx context.Context) (*store.HealthResult, error) {
	if f.onHealth != nil {
		return f.onHealth(ctx)
	}
	return f.Store.Health(ctx)
}

func (f *fakeStore) RehydrateMainFromFiles(ctx context.Context, entitiesDir, edgesDir string) error {
	if f.onRehydrateMainFromFiles != nil {
		return f.onRehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
	}
	return f.Store.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
}

func (f *fakeStore) HydrateBranchFromFiles(ctx context.Context, txID, entitiesDir, edgesDir string) error {
	if f.onHydrateBranchFromFiles != nil {
		return f.onHydrateBranchFromFiles(ctx, txID, entitiesDir, edgesDir)
	}
	return f.Store.HydrateBranchFromFiles(ctx, txID, entitiesDir, edgesDir)
}

func (f *fakeStore) SaveBranchTransactionState(
	ctx context.Context, txID string, state store.BranchTransactionState,
) error {
	if f.onSaveBranchTransactionState != nil {
		return f.onSaveBranchTransactionState(ctx, txID, state)
	}
	return f.Store.SaveBranchTransactionState(ctx, txID, state)
}

func (f *fakeStore) CheckBranchSchemaCompatibility(ctx context.Context, txID string) error {
	if f.onCheckBranchSchemaCompatibility != nil {
		return f.onCheckBranchSchemaCompatibility(ctx, txID)
	}
	return f.Store.CheckBranchSchemaCompatibility(ctx, txID)
}

// failOnceDropBranchDB returns a fakeStore whose first DropBranchDB call fails
// and whose later calls delegate to s.
func failOnceDropBranchDB(s store.Store) *fakeStore {
	var failed bool
	return &fakeStore{Store: s, onDropBranchDB: func(ctx context.Context, txID string) error {
		if !failed {
			failed = true
			return fmt.Errorf("simulated DropBranchDB failure")
		}
		return s.DropBranchDB(ctx, txID)
	}}
}

// newTxStateFailingStore returns a fakeStore whose first
// SaveBranchTransactionState call matching fail returns a simulated write
// failure; every other call delegates to s.
func newTxStateFailingStore(s store.Store, fail func(store.BranchTransactionState) bool) *fakeStore {
	var failed bool
	return &fakeStore{Store: s, onSaveBranchTransactionState: func(
		ctx context.Context, txID string, state store.BranchTransactionState,
	) error {
		if !failed && fail(state) {
			failed = true
			return errors.New("simulated transaction state write failure")
		}
		return s.SaveBranchTransactionState(ctx, txID, state)
	}}
}

// newMarkerFailingStore returns a fakeStore that fails the first rollback-only
// marker write and/or the first DropBranchDB call, delegating every other call
// to s.
func newMarkerFailingStore(s store.Store, failMark, failDrop bool) *fakeStore {
	return &fakeStore{Store: s,
		onSaveBranchTransactionState: func(
			ctx context.Context, txID string, state store.BranchTransactionState,
		) error {
			if failMark && state.RollbackOnly {
				failMark = false
				return errors.New("simulated rollback-only marker failure")
			}
			return s.SaveBranchTransactionState(ctx, txID, state)
		},
		onDropBranchDB: func(ctx context.Context, txID string) error {
			if failDrop {
				failDrop = false
				return errors.New("simulated marker cleanup drop failure")
			}
			return s.DropBranchDB(ctx, txID)
		},
	}
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

type mutationBlockingStore struct {
	store.Store
	wrote   chan struct{}
	release chan struct{}
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
