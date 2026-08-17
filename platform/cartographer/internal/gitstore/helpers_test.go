package gitstore

import (
	"context"
	"errors"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/uuid"
)

// bg is the shared background context for all test operations.
var bg = context.Background()

// errStop is a typed sentinel used to terminate commit-log iteration once the
// target commit is found, instead of matching on an error message string.
var errStop = errors.New("stop")

// setupTestStore creates a gitStore with in-memory storage and memfs,
// initialised with a main branch and entities/ + edges/ directories.
// The SPEC R8 filesystem-error paths (disk full, permission denied, I/O
// failures) are covered by the disk-backed ladybug store tests
// (ladybug_test.go TestRehydrateFiles_IOErrorFailsLoudly).
// wireGitStore links a directly-constructed *gitStore (tests bypass New() to
// hand-build a store over an existing go-git repo, setting the shared git state
// inline) to its four domain sub-structs, mirroring New()'s wireDomains call.
// Without this, promoted method calls dispatch through nil sub-struct receivers
// and panic.
func wireGitStore(g *gitStore) *gitStore {
	g.branchOps = &branchOps{g}
	g.remoteOps = &remoteOps{g}
	g.commitOps = &commitOps{g}
	g.entityEdgeOps = &entityEdgeOps{g}
	return g
}

func setupTestStore(t *testing.T) *gitStore {
	t.Helper()
	fs := memfs.New()
	storer := memory.NewStorage()
	repo, err := git.InitWithOptions(storer, fs, git.InitOptions{
		DefaultBranch: plumbing.ReferenceName("refs/heads/main"),
	})
	if err != nil {
		t.Fatalf("InitWithOptions: %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}

	if err := initDir(wt, fs, "entities"); err != nil {
		t.Fatalf("create entities dir: %v", err)
	}
	if err := initDir(wt, fs, "edges"); err != nil {
		t.Fatalf("create edges dir: %v", err)
	}

	// Commit init, mirroring New()'s init commit (gitstore.go): isInitCommit
	// (remote.go) requires the "cartographer" author, so a "test"-authored
	// init would make IsEmpty report non-empty for an init-only repo, breaking
	// the SPEC R10 clone-vs-pull decision.
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
		t.Fatalf("Commit init: %v", err)
	}

	gs := wireGitStore(&gitStore{
		repo:     repo,
		wt:       wt,
		fs:       fs,
		backend:  storer,
		basePath: t.TempDir(),
	})
	return gs
}

// validUUID returns a valid UUID v4 string, failing the test if generation
// fails (rather than panicking).
func validUUID(t *testing.T) string {
	t.Helper()
	id, err := uuid.NewRandom()
	if err != nil {
		t.Fatalf("generate uuid: %v", err)
	}
	return id.String()
}

func ctx() context.Context {
	return bg
}

// committedTreeFile returns nil when path is present in HEAD's committed
// tree, or object.ErrFileNotFound when the path is absent (also when an
// intermediate directory is missing — tree.File wraps both). Asserting
// against the committed tree rather than only the working tree pins the
// durable-deletion contract: RemoveEntityFiles/RemoveEdgeFiles remove from
// the working tree, and only AddAll + Commit record the deletion in
// committed history.
func committedTreeFile(gs *gitStore, path string) error {
	head, err := gs.repo.Head()
	if err != nil {
		return err
	}
	commit, err := gs.repo.CommitObject(head.Hash())
	if err != nil {
		return err
	}
	tree, err := commit.Tree()
	if err != nil {
		return err
	}
	_, err = tree.File(path)
	return err
}
