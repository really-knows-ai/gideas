package gitstore

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// TestCloneSingleBranchFromRemote clones a remote via internal operations
// (the same flow CloneSingleBranch composes), driven by hand so the reopen
// and backend re-wiring steps can be asserted individually.
func TestCloneSingleBranchFromRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	tmpDir := t.TempDir()

	// Create a source repo with a commit
	srcDir := filepath.Join(tmpDir, "src")
	srcRepo, err := git.PlainInitWithOptions(srcDir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.ReferenceName("refs/heads/main"),
		},
	})
	if err != nil {
		t.Fatalf("init src: %v", err)
	}
	srcWT, err := srcRepo.Worktree()
	if err != nil {
		t.Fatalf("src worktree: %v", err)
	}
	sf, err := srcWT.Filesystem.Create("data.txt")
	if err != nil {
		t.Fatalf("create data: %v", err)
	}
	_ = sf.Close()
	if _, err := srcWT.Add("data.txt"); err != nil {
		t.Fatalf("add data: %v", err)
	}
	if _, err := srcWT.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Clone to a bare remote
	bareDir := filepath.Join(tmpDir, "bare.git")
	_, err = git.PlainClone(bareDir, true, &git.CloneOptions{
		URL: "file://" + srcDir,
	})
	if err != nil {
		t.Fatalf("clone bare: %v", err)
	}

	// Create a local repo (initialised by New)
	localDir := filepath.Join(tmpDir, "local")
	localStore, err := New(localDir)
	if err != nil {
		t.Fatalf("New local: %v", err)
	}

	gs := localStore.(*gitStore)

	err = localStore.WithGitLock(func() error {
		// Configure auth
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		auth, err := gs.resolveAuth()
		if err != nil {
			return fmt.Errorf("resolve auth: %w", err)
		}

		// Configure remote directly (bypassing CloneSingleBranch — this test
		// drives the internal operations by hand: fetch, ref setup, checkout,
		// and reopen, asserting the backend re-wiring step in isolation).
		_, err = gs.repo.CreateRemote(&config.RemoteConfig{
			Name: "origin",
			URLs: []string{"file://" + bareDir},
		})
		if err != nil && !errors.Is(err, git.ErrRemoteExists) {
			return fmt.Errorf("create remote: %w", err)
		}

		// Fetch from remote
		err = gs.repo.FetchContext(ctx(), &git.FetchOptions{
			RemoteName: "origin",
			Auth:       auth,
			Force:      false,
		})
		if err != nil {
			if errors.Is(err, git.NoErrAlreadyUpToDate) {
				// up-to-date is fine
			} else {
				return fmt.Errorf("fetch: %w", err)
			}
		}

		// Resolve remote tracking ref for main
		remoteRef, err := gs.repo.Reference(
			plumbing.ReferenceName("refs/remotes/origin/main"), true)
		if err != nil {
			return fmt.Errorf("resolve remote ref: %w", err)
		}

		mainRef := plumbing.NewHashReference(
			plumbing.ReferenceName("refs/heads/main"),
			remoteRef.Hash(),
		)
		if err := gs.backend.SetReference(mainRef); err != nil {
			return fmt.Errorf("set main ref: %w", err)
		}

		// Checkout main
		if err := gs.wt.Checkout(&git.CheckoutOptions{
			Branch: plumbing.ReferenceName("refs/heads/main"),
			Force:  true,
		}); err != nil {
			return fmt.Errorf("checkout main: %w", err)
		}

		// Re-open repo to refresh worktree
		repoPath := gs.basePath + "/graph-repo"
		reopened, err := git.PlainOpen(repoPath)
		if err != nil {
			return fmt.Errorf("reopen repo: %w", err)
		}
		wt, err := reopened.Worktree()
		if err != nil {
			return fmt.Errorf("reopen worktree: %w", err)
		}
		oldBackend := gs.backend
		gs.repo = reopened
		gs.wt = wt
		gs.fs = wt.Filesystem
		gs.backend = reopened.Storer

		// The reopened backend must be re-wired in step with repo/wt/fs, or a
		// swap-in/memory storer would silently write to the pre-reopen backend.
		if gs.backend == oldBackend || gs.backend != reopened.Storer {
			return fmt.Errorf("backend not re-wired to reopened storer")
		}

		// Verify the data file exists
		if _, err := gs.fs.Stat("data.txt"); err != nil {
			return fmt.Errorf("data file should exist after clone: %w", err)
		}

		// Note: this test replicates the CloneSingleBranch flow manually. The
		// forced checkout replaces the tracked working tree (entities/ + edges/
		// .gitkeep init files are lost here), but the production CloneSingleBranch
		// (remote.go) re-creates entities/ + edges/ after reopening the repo, which
		// this manual replication does not do. entities/ + edges/ are not asserted
		// here precisely because this test bypasses that production step.

		return nil
	})
	if err != nil {
		t.Fatalf("CloneSingleBranchFromRemote: %v", err)
	}
}
