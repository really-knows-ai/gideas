// Package gitstore provides a Git-backed file-per-element serialisation layer
// for entity and edge data. It uses go-git for pure-Go git operations and
// supports branching, committing, merging, and remote sync.
package gitstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage"
)

// GitStore is the authoritative interface for all git-backed versioning
// operations. Every method must be called with the git lock held (via
// WithGitLock). The context parameter cancels remote git operations (threaded
// into go-git's ctx-aware network calls, below); the remaining filesystem and
// worktree I/O is not cancellable (see the ponytail below).
//
// ponytail: ctx is wired into go-git's ctx-aware network operations —
// FetchAndMerge and CloneSingleBranch thread it into FetchContext, PushRemote
// into PushContext (remote.go) — so caller cancellation and deadlines abort a
// hung remote and surface the context error (the sync worker's per-operation
// git deadline, SPEC R10 / SPEC:981, depends on this). All other store I/O is
// not ctx-aware: go-billy filesystem operations (stat, open, write, Close) and
// the go-git worktree operations (add, commit, checkout, clean, remove, log)
// take no context, so a hung CSI/NFS filesystem (a blocked stat, open, write,
// or Close) still blocks the reconcile or RPC for the duration of the hang
// regardless of caller cancellation.
// Upgrade path: run the remaining I/O on a goroutine and select on ctx.Done(),
// or wrap the filesystem in a deadline-bounding adapter that fails operations
// exceeding a configured budget; once go-billy gains ctx-aware IO, thread ctx
// through directly.
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
	PushRemote(ctx context.Context) error
	FetchAndMerge(ctx context.Context, remoteName, branch string) (plumbing.Hash, error)
	CloneSingleBranch(ctx context.Context, url, branch string) error
	IsEmpty(ctx context.Context) (bool, error)

	// HydrationDirs returns the working-tree directories (entities/ and edges/)
	// under graph-repo from which main's LadybugDB is re-hydrated. Callers
	// (service layer) use these even when LADYBUG_DB_PATH is not set, so an
	// in-memory main can be re-hydrated from the git working tree.
	HydrationDirs() (entitiesDir, edgesDir string)

	// Lock
	WithGitLock(fn func() error) error
}

// mainBranchName is the name of the authoritative single synced branch (SPEC
// R10: only main is synced; transaction branches are local-only). The spelling
// is defined once so every refspec and branch-name comparison agrees.
const mainBranchName = "main"

// gitStore is the concrete implementation of GitStore.
//
// It is a composition of four domain sub-structs (branchOps, remoteOps,
// commitOps, entityEdgeOps), one per file domain, so the store's ~40 methods
// are grouped by cohesive responsibility instead of living as one flat god
// struct. The shared git state (repo, worktree, filesystem, backend, remote
// config) stays as direct fields on gitStore — every domain sub-struct embeds
// a back-reference to the owning gitStore (*gitStore), so its methods read and
// write that single shared state and are promoted onto gitStore, letting
// gitStore satisfy the GitStore interface as a composition.
type gitStore struct {
	mu       sync.Mutex
	repo     *git.Repository
	wt       *git.Worktree
	fs       billy.Filesystem
	basePath string
	backend  storage.Storer

	remoteURL string
	authFn    func() (transport.AuthMethod, error)

	*branchOps
	*remoteOps
	*commitOps
	*entityEdgeOps
}

// branchOps owns the branch-lifecycle methods (branch.go): create/delete/lookup,
// checkout, reset, restore and branch-ref manipulation. It embeds *gitStore so
// it shares the store's single git state and its methods are promoted onto
// gitStore.
type branchOps struct{ *gitStore }

// remoteOps owns the remote-sync methods (remote.go): remote config, push, pull
// (fetch+merge), clone-on-init, and repo-emptiness.
type remoteOps struct{ *gitStore }

// commitOps owns the commit-serialisation methods (commit.go): staging, git rm,
// commit, fast-forward merge, and log scanning.
type commitOps struct{ *gitStore }

// entityEdgeOps owns the file-per-element I/O (entity.go, edge.go): batch
// writes/removes/reads and list-types for entities and edges, plus the shared
// JSON file helpers.
type entityEdgeOps struct{ *gitStore }

// wireDomains links a gitStore to its four domain sub-structs. It must be
// called once on every freshly built gitStore (see New) so that promoted
// methods dispatch to non-nil receivers and the sub-structs share the store's
// state.
func wireDomains(g *gitStore) *gitStore {
	g.branchOps = &branchOps{g}
	g.remoteOps = &remoteOps{g}
	g.commitOps = &commitOps{g}
	g.entityEdgeOps = &entityEdgeOps{g}
	return g
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
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", keep, err)
	}
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
	if basePath == "" {
		return nil, ErrEmptyBasePath
	}
	repoPath := filepath.Join(basePath, "graph-repo")
	gitPath := filepath.Join(repoPath, ".git")

	var repo *git.Repository
	var err error

	info, statErr := os.Stat(gitPath)
	switch {
	case statErr == nil && info.IsDir():
		repo, err = git.PlainOpen(repoPath)
		if err != nil {
			return nil, fmt.Errorf("open existing repo: %w", err)
		}
	case os.IsNotExist(statErr):
		repo, err = git.PlainInitWithOptions(repoPath, &git.PlainInitOptions{
			InitOptions: git.InitOptions{
				DefaultBranch: plumbing.ReferenceName("refs/heads/main"),
			},
		})
		if err != nil {
			return nil, fmt.Errorf("init repo: %w", err)
		}
	case statErr != nil:
		return nil, fmt.Errorf("stat .git: %w", statErr)
	default:
		return nil, fmt.Errorf(".git exists but is not a directory: %s", gitPath)
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

	gs := wireDomains(&gitStore{
		repo:     repo,
		wt:       wt,
		fs:       fs,
		basePath: basePath,
		backend:  repo.Storer,
	})

	// The init commit is gated on the presence of refs/heads/main, not on
	// whether .git was just created (the old isNew flag): a crash between
	// PlainInitWithOptions and the initial commit leaves .git present, so a
	// reopen takes the PlainOpen branch and isNew=false while the repo still
	// has an unborn HEAD and no main ref — stranding the repo with no recovery
	// (IsEmpty cannot find a main ref, BeginTransaction's BranchHEAD/CreateBranch
	// fail INTERNAL forever, and only a pullOnInit=true run would replace the
	// repo). Re-running the init sequence whenever the main ref is absent
	// repairs that crash window idempotently, per the SPEC durability/recovery
	// requirement.
	if _, err := repo.Reference(plumbing.ReferenceName("refs/heads/main"), false); err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
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
		} else {
			return nil, fmt.Errorf("check main ref: %w", err)
		}
	}

	return gs, nil
}

// HydrationDirs returns the working-tree directories under graph-repo/ from
// which a LadybugDB instance is re-hydrated (SPEC R8/R10). They mirror the
// repository layout created by New.
func (g *gitStore) HydrationDirs() (string, string) {
	base := filepath.Join(g.basePath, "graph-repo")
	return filepath.Join(base, "entities"), filepath.Join(base, "edges")
}

// WithGitLock acquires the mutex, calls fn, and releases the mutex.
// All public methods on gitStore assume the lock is held by the caller.
func (g *gitStore) WithGitLock(fn func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return fn()
}
