package gitstore

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// TestPullAlreadyUpToDate2 tests that pulling when already up-to-date
// returns no error.
func TestPullAlreadyUpToDate2(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	// Create a bare remote with a commit
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

	// Clone from bare to create local repo
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

	gs := wireGitStore(&gitStore{
		repo:     clonedRepo,
		wt:       clonedWT,
		fs:       clonedWT.Filesystem,
		backend:  clonedRepo.Storer,
		basePath: t.TempDir(),
	})

	err = gs.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		// First pull — should be no-op (already up-to-date)
		if _, err := gs.FetchAndMerge(ctx(), "origin", "main"); err != nil {
			return fmt.Errorf("pull: %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("TestPullAlreadyUpToDate2: %v", err)
	}
}
