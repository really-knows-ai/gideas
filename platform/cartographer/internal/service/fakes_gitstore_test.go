package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/go-git/go-git/v5/plumbing"
)

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

type mergeFailingGitStore struct {
	gitstore.GitStore
	failMerge bool
}

func (s *mergeFailingGitStore) FastForwardMerge(ctx context.Context, branch, into string) error {
	if s.failMerge {
		s.failMerge = false
		return fmt.Errorf("simulated merge failure")
	}
	return s.GitStore.FastForwardMerge(ctx, branch, into)
}

type commitCountingGitStore struct {
	gitstore.GitStore
	commits int
}

func (s *commitCountingGitStore) Commit(ctx context.Context, message string) error {
	s.commits++
	return s.GitStore.Commit(ctx, message)
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

type cleanupAfterMergeFailingGitStore struct {
	gitstore.GitStore
	failRestore bool
	commits     int
	merges      int
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
