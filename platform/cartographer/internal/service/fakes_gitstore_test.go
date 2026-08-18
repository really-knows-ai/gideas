package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/go-git/go-git/v5/plumbing"
)

// fakeGitStore wraps a gitstore.GitStore, overriding selected methods with
// per-method test hooks. A nil hook delegates to the embedded GitStore, so a
// failure-injection seam is a one-field literal:
//
//	fakeGitStore{GitStore: gs, onCommit: func(context.Context, string) error { return errCommit }}
type fakeGitStore struct {
	gitstore.GitStore
	onGitRm              func(context.Context, string) error
	onCommit             func(context.Context, string) error
	onCleanUntracked     func(context.Context) error
	onFastForwardMerge   func(context.Context, string, string) error
	onRestoreMain        func(context.Context) error
	onCreateBranch       func(context.Context, string) error
	onHardResetToBranch  func(context.Context, string) error
	onDeleteBranch       func(context.Context, string) error
	onWithGitLock        func(func() error) error
	onListEntityTypes    func(context.Context) ([]string, error)
	onReadAllEntityFiles func(context.Context, string) ([]gitstore.EntityFile, error)
	onListEdgeTypes      func(context.Context) ([]string, error)
	onReadAllEdgeFiles   func(context.Context, string) ([]gitstore.EdgeFile, error)
}

func (f *fakeGitStore) GitRm(ctx context.Context, path string) error {
	if f.onGitRm != nil {
		return f.onGitRm(ctx, path)
	}
	return f.GitStore.GitRm(ctx, path)
}

func (f *fakeGitStore) Commit(ctx context.Context, message string) error {
	if f.onCommit != nil {
		return f.onCommit(ctx, message)
	}
	return f.GitStore.Commit(ctx, message)
}

func (f *fakeGitStore) CleanUntracked(ctx context.Context) error {
	if f.onCleanUntracked != nil {
		return f.onCleanUntracked(ctx)
	}
	return f.GitStore.CleanUntracked(ctx)
}

func (f *fakeGitStore) FastForwardMerge(ctx context.Context, branch, into string) error {
	if f.onFastForwardMerge != nil {
		return f.onFastForwardMerge(ctx, branch, into)
	}
	return f.GitStore.FastForwardMerge(ctx, branch, into)
}

func (f *fakeGitStore) RestoreMain(ctx context.Context) error {
	if f.onRestoreMain != nil {
		return f.onRestoreMain(ctx)
	}
	return f.GitStore.RestoreMain(ctx)
}

func (f *fakeGitStore) CreateBranch(ctx context.Context, txID string) error {
	if f.onCreateBranch != nil {
		return f.onCreateBranch(ctx, txID)
	}
	return f.GitStore.CreateBranch(ctx, txID)
}

func (f *fakeGitStore) HardResetToBranch(ctx context.Context, branch string) error {
	if f.onHardResetToBranch != nil {
		return f.onHardResetToBranch(ctx, branch)
	}
	return f.GitStore.HardResetToBranch(ctx, branch)
}

func (f *fakeGitStore) DeleteBranch(ctx context.Context, txID string) error {
	if f.onDeleteBranch != nil {
		return f.onDeleteBranch(ctx, txID)
	}
	return f.GitStore.DeleteBranch(ctx, txID)
}

func (f *fakeGitStore) WithGitLock(fn func() error) error {
	if f.onWithGitLock != nil {
		return f.onWithGitLock(fn)
	}
	return f.GitStore.WithGitLock(fn)
}

func (f *fakeGitStore) ListEntityTypes(ctx context.Context) ([]string, error) {
	if f.onListEntityTypes != nil {
		return f.onListEntityTypes(ctx)
	}
	return f.GitStore.ListEntityTypes(ctx)
}

func (f *fakeGitStore) ReadAllEntityFiles(ctx context.Context, entityType string) ([]gitstore.EntityFile, error) {
	if f.onReadAllEntityFiles != nil {
		return f.onReadAllEntityFiles(ctx, entityType)
	}
	return f.GitStore.ReadAllEntityFiles(ctx, entityType)
}

func (f *fakeGitStore) ListEdgeTypes(ctx context.Context) ([]string, error) {
	if f.onListEdgeTypes != nil {
		return f.onListEdgeTypes(ctx)
	}
	return f.GitStore.ListEdgeTypes(ctx)
}

func (f *fakeGitStore) ReadAllEdgeFiles(ctx context.Context, edgeType string) ([]gitstore.EdgeFile, error) {
	if f.onReadAllEdgeFiles != nil {
		return f.onReadAllEdgeFiles(ctx, edgeType)
	}
	return f.GitStore.ReadAllEdgeFiles(ctx, edgeType)
}

// failOnceMerge returns a fakeGitStore whose first FastForwardMerge call fails,
// delegating later calls to gs.
func failOnceMerge(gs gitstore.GitStore) *fakeGitStore {
	var failed bool
	return &fakeGitStore{GitStore: gs, onFastForwardMerge: func(ctx context.Context, branch, into string) error {
		if !failed {
			failed = true
			return fmt.Errorf("simulated merge failure")
		}
		return gs.FastForwardMerge(ctx, branch, into)
	}}
}

// newRecoveryFailingGitStore returns a fakeGitStore that fails the named git
// operation once, delegating every other call to gs. fail=="" arms no failure.
// The "lookup lock" operation fails on the second WithGitLock call, matching
// the recovery flow's lock-then-lookup sequence.
func newRecoveryFailingGitStore(gs gitstore.GitStore, fail string) *fakeGitStore {
	lockCalls := 0
	failOnce := func(op, errMsg string) error {
		if fail == op {
			fail = ""
			return fmt.Errorf("%s", errMsg)
		}
		return nil
	}
	return &fakeGitStore{
		GitStore: gs,
		onWithGitLock: func(fn func() error) error {
			lockCalls++
			if fail == "lookup lock" && lockCalls == 2 {
				fail = ""
				return errors.New("simulated lookup lock failure")
			}
			if err := failOnce("lock", "simulated lock failure"); err != nil {
				return err
			}
			return gs.WithGitLock(fn)
		},
		onRestoreMain: func(ctx context.Context) error {
			if err := failOnce("restore", "simulated restore failure"); err != nil {
				return err
			}
			return gs.RestoreMain(ctx)
		},
		onCleanUntracked: func(ctx context.Context) error {
			if err := failOnce("clean", "simulated clean failure"); err != nil {
				return err
			}
			return gs.CleanUntracked(ctx)
		},
		onDeleteBranch: func(ctx context.Context, txID string) error {
			if err := failOnce("delete", "simulated delete failure"); err != nil {
				return err
			}
			return gs.DeleteBranch(ctx, txID)
		},
		onListEntityTypes: func(ctx context.Context) ([]string, error) {
			if err := failOnce("list entities", "simulated list entities failure"); err != nil {
				return nil, err
			}
			return gs.ListEntityTypes(ctx)
		},
		onReadAllEntityFiles: func(ctx context.Context, entityType string) ([]gitstore.EntityFile, error) {
			if err := failOnce("read entities", "simulated read entities failure"); err != nil {
				return nil, err
			}
			return gs.ReadAllEntityFiles(ctx, entityType)
		},
		onListEdgeTypes: func(ctx context.Context) ([]string, error) {
			if err := failOnce("list edges", "simulated list edges failure"); err != nil {
				return nil, err
			}
			return gs.ListEdgeTypes(ctx)
		},
		onReadAllEdgeFiles: func(ctx context.Context, edgeType string) ([]gitstore.EdgeFile, error) {
			if err := failOnce("read edges", "simulated read edges failure"); err != nil {
				return nil, err
			}
			return gs.ReadAllEdgeFiles(ctx, edgeType)
		},
	}
}

// gitAttemptHook returns a WithGitLock hook that closes the armed attempt
// channel (if any) each time the git lock is entered, then delegates to gs.
// The test arms the channel (*attempted = make(chan struct{})) strictly before
// starting the goroutine that enters the lock, so the shared state is
// race-free; the hook clears the channel after closing so re-arming is safe.
func gitAttemptHook(gs gitstore.GitStore, attempted *chan struct{}) func(func() error) error {
	return func(fn func() error) error {
		if *attempted != nil {
			close(*attempted)
			*attempted = nil
		}
		return gs.WithGitLock(fn)
	}
}

type commitCountingGitStore struct {
	gitstore.GitStore
	commits int
}

func (s *commitCountingGitStore) Commit(ctx context.Context, message string) error {
	s.commits++
	return s.GitStore.Commit(ctx, message)
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
