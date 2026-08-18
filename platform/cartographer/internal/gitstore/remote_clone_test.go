package gitstore

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

func pushGraphUpdate(t *testing.T, tmpDir, remoteDir, content string) plumbing.Hash {
	t.Helper()
	writer, err := git.PlainClone(filepath.Join(tmpDir, "writer"), false,
		&git.CloneOptions{URL: "file://" + remoteDir})
	if err != nil {
		t.Fatalf("clone remote writer: %v", err)
	}
	writerWT, err := writer.Worktree()
	if err != nil {
		t.Fatalf("writer worktree: %v", err)
	}
	if err := writerWT.Filesystem.Remove("data.txt"); err != nil {
		t.Fatalf("remove old payload file: %v", err)
	}
	updatedGraph, err := writerWT.Filesystem.Create("data.txt")
	if err != nil {
		t.Fatalf("create updated payload file: %v", err)
	}
	if _, err := updatedGraph.Write([]byte(content)); err != nil {
		t.Fatalf("write updated payload file: %v", err)
	}
	if err := updatedGraph.Close(); err != nil {
		t.Fatalf("close updated payload file: %v", err)
	}
	if _, err := writerWT.Add("data.txt"); err != nil {
		t.Fatalf("add updated payload file: %v", err)
	}
	updatedHash, err := writerWT.Commit("update data", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	})
	if err != nil {
		t.Fatalf("commit updated graph file: %v", err)
	}
	if err := writer.Push(&git.PushOptions{}); err != nil {
		t.Fatalf("push updated graph: %v", err)
	}
	return updatedHash
}

// configureAnonymousRemote configures the remote on an already-cloned gitStore.
// It is only valid after a successful clone of the remote's main branch: the
// local main already points at the remote's HEAD, so the FetchAndMerge call is a
// no-op (up-to-date). It is not meant to seed an empty local repo.
func configureAnonymousRemote(t *testing.T, gs *gitStore, remoteURL string) {
	t.Helper()
	if err := gs.SetRemote(ctx(), remoteURL, func() (transport.AuthMethod, error) { return nil, nil }); err != nil {
		t.Fatalf("SetRemote: %v", err)
	}
	if _, err := gs.FetchAndMerge(ctx(), "origin", "main"); err != nil {
		t.Fatalf("anonymous FetchAndMerge: %v", err)
	}
}

func TestCloneSingleBranchNoAuth(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")
	source, err := git.PlainInitWithOptions(sourceDir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	if err != nil {
		t.Fatalf("init source: %v", err)
	}
	sourceWT, err := source.Worktree()
	if err != nil {
		t.Fatalf("source worktree: %v", err)
	}
	const graphContent = `{"graph":"controlled"}`
	graphFile, err := sourceWT.Filesystem.Create("data.txt")
	if err != nil {
		t.Fatalf("create payload file: %v", err)
	}
	if _, err := graphFile.Write([]byte(graphContent)); err != nil {
		t.Fatalf("write payload file: %v", err)
	}
	if err := graphFile.Close(); err != nil {
		t.Fatalf("close payload file: %v", err)
	}
	if _, err := sourceWT.Add("data.txt"); err != nil {
		t.Fatalf("add payload file: %v", err)
	}
	sourceHash, err := sourceWT.Commit("main graph", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	})
	if err != nil {
		t.Fatalf("commit graph file: %v", err)
	}

	remoteDir := filepath.Join(tmpDir, "remote.git")
	if _, err := git.PlainClone(remoteDir, true, &git.CloneOptions{URL: "file://" + sourceDir}); err != nil {
		t.Fatalf("create bare remote: %v", err)
	}
	// Serve the remote over go-git's native file:// transport — no external
	// git binary, honoring SPEC R5's "pure Go, no external git binary" policy.
	// CloneSingleBranch's URL validation accepts file:// remotes, and file://
	// demands no credentials (requiresAuth false), so the nil-authFn clone
	// path is exercised end-to-end without the network.
	store, err := New(filepath.Join(tmpDir, "local"))
	if err != nil {
		t.Fatalf("new local store: %v", err)
	}
	gs := store.(*gitStore)
	if gs.authFn != nil {
		t.Fatal("expected nil auth provider")
	}
	err = store.WithGitLock(func() error {
		return gs.CloneSingleBranch(ctx(), "file://"+remoteDir, "main")
	})
	if err != nil {
		t.Fatalf("CloneSingleBranchNoAuth: %v", err)
	}

	mainRef, err := gs.repo.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatalf("main ref: %v", err)
	}
	if mainRef.Hash() != sourceHash {
		t.Fatalf("main ref = %s, want %s", mainRef.Hash(), sourceHash)
	}
	head, err := gs.repo.Head()
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	if head.Name() != plumbing.NewBranchReferenceName("main") || head.Hash() != sourceHash {
		t.Fatalf("HEAD = %s at %s, want main at %s", head.Name(), head.Hash(), sourceHash)
	}
	clonedGraph, err := gs.fs.Open("data.txt")
	if err != nil {
		t.Fatalf("open checked-out payload file: %v", err)
	}
	got, err := io.ReadAll(clonedGraph)
	_ = clonedGraph.Close()
	if err != nil {
		t.Fatalf("read checked-out graph file: %v", err)
	}
	if string(got) != graphContent {
		t.Fatalf("graph content = %q, want %q", got, graphContent)
	}
	status, err := gs.wt.Status()
	if err != nil {
		t.Fatalf("worktree status: %v", err)
	}
	if !status.IsClean() {
		t.Fatalf("expected clean checked-out worktree, got %s", status)
	}

	// A configured resolver returning nil explicitly selects anonymous access.
	configureAnonymousRemote(t, gs, "file://"+remoteDir)

	const updatedGraphContent = `{"graph":"updated"}`
	updatedHash := pushGraphUpdate(t, tmpDir, remoteDir, updatedGraphContent)

	if _, err := gs.FetchAndMerge(ctx(), "origin", "main"); err != nil {
		t.Fatalf("anonymous FetchAndMerge: %v", err)
	}
	mainRef, err = gs.repo.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatalf("updated main ref: %v", err)
	}
	if mainRef.Hash() != updatedHash {
		t.Fatalf("updated main ref = %s, want %s", mainRef.Hash(), updatedHash)
	}
	got, err = os.ReadFile(filepath.Join(tmpDir, "local", "graph-repo", "data.txt"))
	if err != nil {
		t.Fatalf("read updated worktree payload: %v", err)
	}
	if string(got) != updatedGraphContent {
		t.Fatalf("updated payload content = %q, want %q", got, updatedGraphContent)
	}
}

// TestCloneSingleBranchCleansUntracked pins the SPEC R10 clone-on-init
// contract: after the forced checkout the working tree must reflect exactly
// the cloned state (re-hydration reads the cloned tree). A transaction that
// crashed between file-write and git-commit on a prior run strands uncommitted
// files in the working tree that IsEmpty cannot detect (main still points at
// the init commit), so without the post-checkout clean — mirroring
// setLocalRefAndCheckout — they survive the clone and are re-hydrated into
// main.lbug as phantom graph data.
func TestCloneSingleBranchCleansUntracked(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")
	source, err := git.PlainInitWithOptions(sourceDir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	if err != nil {
		t.Fatalf("init source: %v", err)
	}
	sourceWT, err := source.Worktree()
	if err != nil {
		t.Fatalf("source worktree: %v", err)
	}
	const graphContent = `{"graph":"controlled"}`
	graphFile, err := sourceWT.Filesystem.Create("data.txt")
	if err != nil {
		t.Fatalf("create payload file: %v", err)
	}
	if _, err := graphFile.Write([]byte(graphContent)); err != nil {
		t.Fatalf("write payload file: %v", err)
	}
	if err := graphFile.Close(); err != nil {
		t.Fatalf("close payload file: %v", err)
	}
	if _, err := sourceWT.Add("data.txt"); err != nil {
		t.Fatalf("add payload file: %v", err)
	}
	if _, err := sourceWT.Commit("main graph", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("commit graph file: %v", err)
	}

	remoteDir := filepath.Join(tmpDir, "remote.git")
	if _, err := git.PlainClone(remoteDir, true, &git.CloneOptions{URL: "file://" + sourceDir}); err != nil {
		t.Fatalf("create bare remote: %v", err)
	}

	store, err := New(filepath.Join(tmpDir, "local"))
	if err != nil {
		t.Fatalf("new local store: %v", err)
	}
	gs := store.(*gitStore)

	// Simulate a transaction that crashed between file-write and git-commit
	// on a prior run: an uncommitted file stranded in the re-hydration
	// directory. main still points at the init commit, so IsEmpty reports the
	// repo empty and the clone-on-init path runs with this file in the tree.
	stranded := filepath.Join(tmpDir, "local", "graph-repo", "entities", "phantom.json")
	if err := os.WriteFile(stranded, []byte(`{"id":"phantom","type":"Person"}`), 0644); err != nil {
		t.Fatalf("write stranded file: %v", err)
	}

	err = store.WithGitLock(func() error {
		return gs.CloneSingleBranch(ctx(), "file://"+remoteDir, "main")
	})
	if err != nil {
		t.Fatalf("CloneSingleBranchCleansUntracked: %v", err)
	}

	if _, err := os.Stat(stranded); !os.IsNotExist(err) {
		t.Fatalf("stranded untracked file survived the clone: %v", err)
	}
	cloned, err := gs.fs.Open("data.txt")
	if err != nil {
		t.Fatalf("open checked-out payload file: %v", err)
	}
	got, err := io.ReadAll(cloned)
	_ = cloned.Close()
	if err != nil {
		t.Fatalf("read checked-out graph file: %v", err)
	}
	if string(got) != graphContent {
		t.Fatalf("graph content = %q, want %q", got, graphContent)
	}
	status, err := gs.wt.Status()
	if err != nil {
		t.Fatalf("worktree status: %v", err)
	}
	if !status.IsClean() {
		t.Fatalf("expected clean checked-out worktree, got %s", status)
	}
}

// TestCloneSingleBranchNonEmptyRepoRejected pins the SPEC R10 clone-on-init
// precondition: CloneSingleBranch must refuse to clone over a local repo that
// holds data commits — per the low-level-primitive rule the primitive enforces
// its empty-repo precondition rather than deferring it to callers. A clone
// over a non-empty repo would silently overwrite the local main ref and
// discard local commits (data loss), so the call must fail loudly with
// ErrRepoNotEmpty and leave local main untouched.
func TestCloneSingleBranchNonEmptyRepoRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")
	source, err := git.PlainInitWithOptions(sourceDir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	if err != nil {
		t.Fatalf("init source: %v", err)
	}
	sourceWT, err := source.Worktree()
	if err != nil {
		t.Fatalf("source worktree: %v", err)
	}
	const graphContent = `{"graph":"controlled"}`
	graphFile, err := sourceWT.Filesystem.Create("data.txt")
	if err != nil {
		t.Fatalf("create payload file: %v", err)
	}
	if _, err := graphFile.Write([]byte(graphContent)); err != nil {
		t.Fatalf("write payload file: %v", err)
	}
	if err := graphFile.Close(); err != nil {
		t.Fatalf("close payload file: %v", err)
	}
	if _, err := sourceWT.Add("data.txt"); err != nil {
		t.Fatalf("add payload file: %v", err)
	}
	if _, err := sourceWT.Commit("main graph", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("commit graph file: %v", err)
	}

	remoteDir := filepath.Join(tmpDir, "remote.git")
	if _, err := git.PlainClone(remoteDir, true, &git.CloneOptions{URL: "file://" + sourceDir}); err != nil {
		t.Fatalf("create bare remote: %v", err)
	}

	store, err := New(filepath.Join(tmpDir, "local"))
	if err != nil {
		t.Fatalf("new local store: %v", err)
	}
	gs := store.(*gitStore)

	err = store.WithGitLock(func() error {
		// Commit local data so the repo is no longer init-only (IsEmpty false).
		now := time.Now().UTC().Round(time.Millisecond)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: validUUID(t), Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "transaction:local-data"); err != nil {
			return err
		}
		mainRef, err := gs.repo.Reference(plumbing.NewBranchReferenceName("main"), true)
		if err != nil {
			return err
		}
		localHash := mainRef.Hash()

		err = gs.CloneSingleBranch(ctx(), "file://"+remoteDir, "main")
		if !errors.Is(err, ErrRepoNotEmpty) {
			return fmt.Errorf("expected ErrRepoNotEmpty, got %v", err)
		}

		// Local main must be untouched — the rejected clone must not overwrite it.
		mainRef, err = gs.repo.Reference(plumbing.NewBranchReferenceName("main"), true)
		if err != nil {
			return err
		}
		if mainRef.Hash() != localHash {
			return fmt.Errorf("main ref changed after rejected clone: got %s, want %s", mainRef.Hash(), localHash)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestCloneSingleBranchNonEmptyRepoRejected: %v", err)
	}
}
