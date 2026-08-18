package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/foundry/flow/cartographer/internal/store"
)

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

func (s *dropFailingStore) DropBranchDB(ctx context.Context, txID string) error {
	if s.failDrop {
		s.failDrop = false
		return fmt.Errorf("simulated DropBranchDB failure")
	}
	return s.Store.DropBranchDB(ctx, txID)
}
