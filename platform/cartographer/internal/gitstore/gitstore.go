// Package gitstore provides a Git-backed file-per-element serialisation layer
// for entity and edge data. It uses go-git for pure-Go git operations and
// supports branching, committing, merging, and remote sync.
package gitstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage"
)

// GitStore is the authoritative interface for all git-backed versioning
// operations. Every method must be called with the git lock held (via
// WithGitLock). The context parameter is reserved for future cancellation
// support in I/O-bound operations.
type GitStore interface {
	// Branch management
	CreateBranch(ctx context.Context, txID string) error
	DeleteBranch(ctx context.Context, txID string) error
	BranchExists(ctx context.Context, txID string) (bool, error)
	BranchHEAD(ctx context.Context, branch string) (string, error)
	SetBranchRef(ctx context.Context, branch string, hash string) error
	CommitExistsOnBranch(ctx context.Context, txID string) (bool, error)
	ListBranches(ctx context.Context) ([]string, error)

	// File serialisation / deserialisation
	WriteEntityFiles(ctx context.Context, entityType string, entities []Entity) error
	WriteEdgeFiles(ctx context.Context, edgeType string, edges []Edge) error
	RemoveEntityFiles(ctx context.Context, entityType string, ids []string) error
	RemoveEdgeFiles(ctx context.Context, edgeType string, ids []string) error
	ReadAllEntityFiles(ctx context.Context, entityType string) ([]EntityFile, error)
	ReadAllEdgeFiles(ctx context.Context, edgeType string) ([]EdgeFile, error)
	ListEntityTypes(ctx context.Context) ([]string, error)
	ListEdgeTypes(ctx context.Context) ([]string, error)

	// Git operations
	AddAll(ctx context.Context, path string) error
	GitRm(ctx context.Context, path string) error
	Commit(ctx context.Context, message string) error
	FastForwardMerge(ctx context.Context, branch, into string) error
	HardResetToBranch(ctx context.Context, branch string) error
	CleanUntracked(ctx context.Context) error
	Checkout(ctx context.Context, branch string) error
	RestoreMain(ctx context.Context) error
	GitLogOneline(ctx context.Context, prefix string) ([]string, error)

	// Remote operations
	SetRemote(ctx context.Context, url string, authFn func() (transport.AuthMethod, error)) error
	FetchRemote(ctx context.Context) error
	PushRemote(ctx context.Context) error
	PullAndFastForward(ctx context.Context) error
	HasRemote(ctx context.Context) (bool, error)
	CloneSingleBranch(ctx context.Context, url, branch string) error
	IsEmpty(ctx context.Context) (bool, error)

	// Lock
	WithGitLock(fn func() error) error

	// Lifecycle
	Close() error
}

// gitStore is the concrete implementation of GitStore.
type gitStore struct {
	mu       sync.Mutex
	repo     *git.Repository
	wt       *git.Worktree
	fs       billy.Filesystem
	basePath string
	backend  storage.Storer

	remoteURL string
	authFn    func() (transport.AuthMethod, error)

	pushInFlight atomic.Bool
	pushNeeded   atomic.Bool
}

// initDir creates a directory with a .gitkeep file so go-git can stage it.
func initDir(wt *git.Worktree, fs billy.Filesystem, name string) error {
	if err := fs.MkdirAll(name, 0755); err != nil {
		return err
	}
	keep := name + "/.gitkeep"
	f, err := fs.Create(keep)
	if err != nil {
		return err
	}
	f.Close()
	if _, err := wt.Add(keep); err != nil {
		return err
	}
	return nil
}

// compile-time interface check
var _ GitStore = (*gitStore)(nil)

// New opens or initialises a git repository at basePath/graph-repo/.
// If the repository does not exist, it is initialised with the default
// branch set to "main" and an initial "init" commit containing the
// entities/ and edges/ directories.
func New(basePath string) (GitStore, error) {
	repoPath := filepath.Join(basePath, "graph-repo")
	gitPath := filepath.Join(repoPath, ".git")

	var isNew bool
	var repo *git.Repository
	var err error

	if info, statErr := os.Stat(gitPath); statErr == nil && info.IsDir() {
		repo, err = git.PlainOpen(repoPath)
		if err != nil {
			return nil, fmt.Errorf("open existing repo: %w", err)
		}
		isNew = false
	} else if os.IsNotExist(statErr) {
		repo, err = git.PlainInitWithOptions(repoPath, &git.PlainInitOptions{
			InitOptions: git.InitOptions{
				DefaultBranch: plumbing.ReferenceName("refs/heads/main"),
			},
		})
		if err != nil {
			return nil, fmt.Errorf("init repo: %w", err)
		}
		isNew = true
	} else {
		return nil, fmt.Errorf("stat .git: %w", statErr)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("get worktree: %w", err)
	}

	fs := wt.Filesystem

	if err := fs.MkdirAll("entities", 0755); err != nil {
		return nil, fmt.Errorf("create entities dir: %w", err)
	}
	if err := fs.MkdirAll("edges", 0755); err != nil {
		return nil, fmt.Errorf("create edges dir: %w", err)
	}

	gs := &gitStore{
		repo:     repo,
		wt:       wt,
		fs:       fs,
		basePath: basePath,
		backend:  repo.Storer,
	}

	if isNew {
		if err := initDir(wt, fs, "entities"); err != nil {
			return nil, fmt.Errorf("init entities dir: %w", err)
		}
		if err := initDir(wt, fs, "edges"); err != nil {
			return nil, fmt.Errorf("init edges dir: %w", err)
		}
		if _, err := wt.Commit("init", &git.CommitOptions{
			Author: &object.Signature{
				Name:  "cartographer",
				Email: "cartographer@foundry.flow",
			},
			Committer: &object.Signature{
				Name:  "cartographer",
				Email: "cartographer@foundry.flow",
			},
		}); err != nil {
			return nil, fmt.Errorf("initial commit: %w", err)
		}
	}

	return gs, nil
}

// Close is a no-op that returns nil. It exists for interface conformance
// with lifecycle-aware consumers.
func (g *gitStore) Close() error {
	return nil
}

// WithGitLock acquires the mutex, calls fn, and releases the mutex.
// All public methods on gitStore assume the lock is held by the caller.
func (g *gitStore) WithGitLock(fn func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return fn()
}
