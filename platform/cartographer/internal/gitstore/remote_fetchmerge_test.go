package gitstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// setupBareRemote creates a bare repo at bareDir with an initial commit
// containing init.txt.
func setupBareRemote(t *testing.T, tmpDir, bareDir string) {
	t.Helper()
	workDir := filepath.Join(tmpDir, "work")
	workRepo, err := git.PlainInitWithOptions(workDir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.ReferenceName("refs/heads/main"),
		},
	})
	if err != nil {
		t.Fatalf("init work: %v", err)
	}
	workWT, err := workRepo.Worktree()
	if err != nil {
		t.Fatalf("work worktree: %v", err)
	}
	initFile, _ := workWT.Filesystem.Create("init.txt")
	_ = initFile.Close()
	if _, err := workWT.Add("init.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := workWT.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	_, err = git.PlainClone(bareDir, true, &git.CloneOptions{
		URL: "file://" + workDir,
	})
	if err != nil {
		t.Fatalf("clone bare: %v", err)
	}
}

// cloneFromBare clones a bare repo into a local non-bare repo and returns
// a *gitStore wrapping it.
func cloneFromBare(t *testing.T, tmpDir, bareDir string) *gitStore {
	t.Helper()
	localDir := filepath.Join(tmpDir, "local")
	clonedRepo, err := git.PlainClone(localDir, false, &git.CloneOptions{
		URL: "file://" + bareDir,
	})
	if err != nil {
		t.Fatalf("clone local: %v", err)
	}
	clonedWT, err := clonedRepo.Worktree()
	if err != nil {
		t.Fatalf("get worktree: %v", err)
	}
	return wireGitStore(&gitStore{
		repo:     clonedRepo,
		wt:       clonedWT,
		fs:       clonedWT.Filesystem,
		backend:  clonedRepo.Storer,
		basePath: t.TempDir(),
	})
}

// remoteHEAD returns the HEAD hash of a bare repository.
func remoteHEAD(t *testing.T, bareDir string) plumbing.Hash {
	t.Helper()
	remoteRepo, err := git.PlainOpen(bareDir)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	remoteRef, err := remoteRepo.Reference(plumbing.ReferenceName("refs/heads/main"), true)
	if err != nil {
		t.Fatalf("remote ref: %v", err)
	}
	return remoteRef.Hash()
}

// copyDir recursively copies the directory tree at src into dst, preserving
// file permissions. Used to derive a repo-local copy from a seed repo so the
// copied repository is byte-identical to the seed (see
// TestFetchAndMerge_BootstrapFromInitOnly).
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	}); err != nil {
		t.Fatalf("copy %s to %s: %v", src, dst, err)
	}
}

// TestFetchAndMerge_BootstrapFromInitOnly exercises the SPEC R10
// clone-vs-pull bootstrap path: an init-only local repo (created by New()
// over a copy of the seed's repo, so its init commit is the seed's init
// commit) pulls from a remote that shares the same init commit and has
// additional history. FetchAndMerge must fast-forward local main from the
// init commit to the remote's HEAD without returning ErrPullDiverged.
func TestFetchAndMerge_BootstrapFromInitOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	// Create the "seed" repo whose init commit will be shared between local
	// and remote. This is the same init commit that New() produces: a single
	// commit with message "init" containing entities/ + edges/ dirs.
	seedDir := filepath.Join(tmpDir, "seed")
	if _, err := New(seedDir); err != nil {
		t.Fatalf("New seed: %v", err)
	}

	// Clone the seed as a bare remote — the bare remote now has the same
	// "init" commit as the local will have.
	_, err := git.PlainClone(bareDir, true, &git.CloneOptions{
		URL: "file://" + filepath.Join(seedDir, "graph-repo"),
	})
	if err != nil {
		t.Fatalf("clone bare: %v", err)
	}

	// Advance the remote by one commit (simulating a remote with history).
	workDir := filepath.Join(tmpDir, "work")
	workRepo, err := git.PlainClone(workDir, false, &git.CloneOptions{
		URL: "file://" + bareDir,
	})
	if err != nil {
		t.Fatalf("clone work: %v", err)
	}
	workWT, err := workRepo.Worktree()
	if err != nil {
		t.Fatalf("work worktree: %v", err)
	}
	f, err := workWT.Filesystem.Create("remote-data.txt")
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	_, _ = f.Write([]byte("remote content"))
	_ = f.Close()
	if _, err := workWT.Add("remote-data.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	remoteHash, err := workWT.Commit("remote data commit", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := workRepo.Push(&git.PushOptions{}); err != nil {
		t.Fatalf("push: %v", err)
	}

	// Derive the local repo from the seed rather than minting a fresh init
	// commit via New(): New()'s init commit embeds the current time (go-git
	// encodes whole seconds), so a second-boundary crossing between the seed
	// and local creation would produce a different init commit hash, breaking
	// the "local main IS an ancestor of remote main" premise with
	// ErrPullDiverged. Copying the seed's graph-repo makes local main the
	// seed's init commit by construction (New opens an existing repo when
	// .git already exists, so no new init commit is created).
	localDir := filepath.Join(tmpDir, "local")
	copyDir(t, filepath.Join(seedDir, "graph-repo"), filepath.Join(localDir, "graph-repo"))
	localStore, err := New(localDir)
	if err != nil {
		t.Fatalf("New local: %v", err)
	}

	gs := localStore.(*gitStore)

	err = localStore.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		// Verify local is empty (init-only) before pull
		empty, emptyErr := gs.IsEmpty(ctx())
		if emptyErr != nil {
			return fmt.Errorf("IsEmpty: %w", emptyErr)
		}
		if !empty {
			return fmt.Errorf("expected empty init-only repo")
		}

		// FetchAndMerge must fast-forward from init to remote HEAD
		newHash, pullErr := gs.FetchAndMerge(ctx(), "origin", "main")
		if pullErr != nil {
			return fmt.Errorf("FetchAndMerge bootstrap: %w", pullErr)
		}
		if newHash != remoteHash {
			return fmt.Errorf("expected hash %s, got %s", remoteHash, newHash)
		}

		// Local main must now point at remote HEAD
		mainRef, refErr := gs.repo.Reference(plumbing.NewBranchReferenceName("main"), true)
		if refErr != nil {
			return fmt.Errorf("main ref: %w", refErr)
		}
		if mainRef.Hash() != remoteHash {
			return fmt.Errorf("main ref = %s, want %s", mainRef.Hash(), remoteHash)
		}

		// Remote data must be visible in the working tree
		if _, statErr := gs.fs.Stat("remote-data.txt"); statErr != nil {
			return fmt.Errorf("remote data file missing after bootstrap: %w", statErr)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("TestFetchAndMerge_BootstrapFromInitOnly: %v", err)
	}
}

// TestFetchAndMerge_AlreadyUpToDate tests that FetchAndMerge when both
// sides are identical returns no error and the HEAD hash is unchanged.
func TestFetchAndMerge_AlreadyUpToDate(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	setupBareRemote(t, tmpDir, bareDir)
	gs := cloneFromBare(t, tmpDir, bareDir)
	initialHash := remoteHEAD(t, bareDir)

	err := gs.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		newHash, err := gs.FetchAndMerge(ctx(), "origin", "main")
		if err != nil {
			return fmt.Errorf("FetchAndMerge: %w", err)
		}
		if newHash != initialHash {
			return fmt.Errorf("expected hash %s, got %s", initialHash, newHash)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestFetchAndMerge_AlreadyUpToDate: %v", err)
	}
}

// TestFetchAndMerge_FastForward tests that FetchAndMerge advances the local
// HEAD when the remote has new commits (fast-forward). The local repo is
// cloned before the remote is advanced, so at fetch time local main trails
// the remote tip and FetchAndMerge must take the isAncestor ->
// setLocalRefAndCheckout fast-forward path rather than the early
// up-to-date return.
func TestFetchAndMerge_FastForward(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	setupBareRemote(t, tmpDir, bareDir)

	// Clone the local repo first, before the remote is advanced, so local
	// main matches the pre-advance remote tip.
	gs := cloneFromBare(t, tmpDir, bareDir)
	originalHash := remoteHEAD(t, bareDir)

	// Advance the remote by one commit pushed from a separate "writer" clone.
	writerDir := filepath.Join(tmpDir, "writer")
	writer, err := git.PlainClone(writerDir, false, &git.CloneOptions{
		URL: "file://" + bareDir,
	})
	if err != nil {
		t.Fatalf("clone writer: %v", err)
	}
	writerWT, err := writer.Worktree()
	if err != nil {
		t.Fatalf("writer worktree: %v", err)
	}
	remoteFile, err := writerWT.Filesystem.Create("remote-data.txt")
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	_, _ = remoteFile.Write([]byte("remote content"))
	_ = remoteFile.Close()
	if _, err := writerWT.Add("remote-data.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := writerWT.Commit("remote data commit", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := writer.Push(&git.PushOptions{}); err != nil {
		t.Fatalf("push: %v", err)
	}

	remoteHash := remoteHEAD(t, bareDir)
	if remoteHash == originalHash {
		t.Fatalf("expected remote to advance past %s", originalHash)
	}

	err = gs.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		newHash, err := gs.FetchAndMerge(ctx(), "origin", "main")
		if err != nil {
			return fmt.Errorf("FetchAndMerge: %w", err)
		}
		if newHash != remoteHash {
			return fmt.Errorf("expected hash %s, got %s", remoteHash, newHash)
		}

		// The local main ref must have moved off the pre-advance head.
		mainRef, refErr := gs.repo.Reference(plumbing.NewBranchReferenceName("main"), true)
		if refErr != nil {
			return fmt.Errorf("main ref: %w", refErr)
		}
		if mainRef.Hash() != remoteHash {
			return fmt.Errorf("main ref = %s, want %s", mainRef.Hash(), remoteHash)
		}

		// Remote data must be visible in the working tree after the
		// fast-forward checkout.
		if _, statErr := gs.fs.Stat("remote-data.txt"); statErr != nil {
			return fmt.Errorf("remote data file missing after fast-forward: %w", statErr)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("TestFetchAndMerge_FastForward: %v", err)
	}
}

// TestFetchAndMerge_Diverged asserts that FetchAndMerge fails with
// ErrPullDiverged when local main and the remote have diverged (neither side
// is an ancestor of the other) and leaves the local main ref unchanged. This
// is the delivered divergence behavior of the sync pull path: service mapGitError
// maps ErrPullDiverged to FAILED_PRECONDITION, matching SPEC R10 / error-table
// row "Sync diverged" (SPEC:980).
func TestFetchAndMerge_Diverged(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	setupBareRemote(t, tmpDir, bareDir)

	// Clone the bare remote to create a "writer" that will push to remote
	writerDir := filepath.Join(tmpDir, "writer")
	writer, err := git.PlainClone(writerDir, false, &git.CloneOptions{
		URL: "file://" + bareDir,
	})
	if err != nil {
		t.Fatalf("clone writer: %v", err)
	}
	writerWT, err := writer.Worktree()
	if err != nil {
		t.Fatalf("writer worktree: %v", err)
	}

	gs := cloneFromBare(t, tmpDir, bareDir)

	// Make a local commit (diverging from remote)
	localFile, err := gs.wt.Filesystem.Create("local.txt")
	if err != nil {
		t.Fatalf("create local file: %v", err)
	}
	_, _ = localFile.Write([]byte("local content"))
	_ = localFile.Close()
	if _, err := gs.wt.Add("local.txt"); err != nil {
		t.Fatalf("add local: %v", err)
	}
	localCommitHash, err := gs.wt.Commit("local commit", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	})
	if err != nil {
		t.Fatalf("local commit: %v", err)
	}

	// Make a different commit on the remote (push from writer)
	remoteFile, err := writerWT.Filesystem.Create("remote.txt")
	if err != nil {
		t.Fatalf("create remote file: %v", err)
	}
	_, _ = remoteFile.Write([]byte("remote content"))
	_ = remoteFile.Close()
	if _, err := writerWT.Add("remote.txt"); err != nil {
		t.Fatalf("add remote: %v", err)
	}
	if _, err := writerWT.Commit("remote commit", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("remote commit: %v", err)
	}
	if err := writer.Push(&git.PushOptions{}); err != nil {
		t.Fatalf("push remote: %v", err)
	}

	err = gs.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		_, err = gs.FetchAndMerge(ctx(), "origin", "main")
		if !errors.Is(err, ErrPullDiverged) {
			if err == nil {
				return fmt.Errorf("expected ErrPullDiverged, got nil")
			}
			return fmt.Errorf("expected ErrPullDiverged, got %v", err)
		}

		// The local main ref must be left unchanged on divergence.
		localRef, refErr := gs.repo.Reference(plumbing.ReferenceName("refs/heads/main"), true)
		if refErr != nil {
			return fmt.Errorf("resolve local ref: %w", refErr)
		}
		if localRef.Hash() != localCommitHash {
			return fmt.Errorf("local main ref changed on divergence: got %s, want %s", localRef.Hash(), localCommitHash)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestFetchAndMerge_Diverged: %v", err)
	}
}
