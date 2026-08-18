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

// TestFetchAndMerge_DivergencePersistsAcrossCycles pins the persistent-
// divergence contract of FetchAndMerge: after the first divergent cycle
// advances the tracking ref to the remote tip while local main is left
// behind (ErrPullDiverged, local ref unchanged), the next fetch is a no-op
// (git.NoErrAlreadyUpToDate) but the local branch is STILL diverged. The
// up-to-date branch must re-run the ancestry classification and continue to
// surface ErrPullDiverged — never silently report the divergence as
// up-to-date — so the sync worker and Sync() keep delivering the SPEC R10
// "Sync diverged" failure (FAILED_PRECONDITION, SPEC error-table row 977)
// and its telemetry on every cycle, and BeginTransaction's implicit sync
// never sees a clean cycle over stale local state (SPEC R10 BeginTransaction
// implicit sync).
func TestFetchAndMerge_DivergencePersistsAcrossCycles(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	setupBareRemote(t, tmpDir, bareDir)

	// Clone the bare remote to create a "writer" that will push to remote.
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

	// Make a local commit (diverging from remote).
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

	// Make a different commit on the remote (push from writer).
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

		// Cycle 1: the fetch advances the tracking ref to the remote tip and
		// the ancestry classification surfaces the divergence.
		_, err = gs.FetchAndMerge(ctx(), "origin", "main")
		if !errors.Is(err, ErrPullDiverged) {
			if err == nil {
				return fmt.Errorf("cycle 1: expected ErrPullDiverged, got nil")
			}
			return fmt.Errorf("cycle 1: expected ErrPullDiverged, got %v", err)
		}

		// Cycle 2: the fetch is a no-op (tracking ref already at the remote
		// tip → git.NoErrAlreadyUpToDate) but local main is STILL diverged.
		// The up-to-date branch must re-classify, not silently succeed.
		_, err = gs.FetchAndMerge(ctx(), "origin", "main")
		if !errors.Is(err, ErrPullDiverged) {
			if err == nil {
				return fmt.Errorf("cycle 2: expected ErrPullDiverged, got nil")
			}
			return fmt.Errorf("cycle 2: expected ErrPullDiverged, got %v", err)
		}

		// Local main must remain unchanged across both divergent cycles.
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
		t.Fatalf("TestFetchAndMerge_DivergencePersistsAcrossCycles: %v", err)
	}
}

// TestFetchAndMerge_LocalAhead pins the local-ahead (remote strictly behind)
// classification of FetchAndMerge: local main has advanced past the remote
// (e.g. a fire-and-forget push that failed transiently — the SPEC R10 Commit
// model defers delivery to the worker's next push), so there
// is nothing to pull and the call must succeed as up-to-date, never fail with
// ErrPullDiverged. The remote-behind state is reached with distinct
// local/remote tips by dropping the remote-tracking ref first (simulating a
// remote (re)configuration on a repo that has not fetched since —
// ensureRemoteExists deletes/recreates origin on URL change), forcing the
// fetch to re-create the tracking ref from the remote's behind-local tip.
func TestFetchAndMerge_LocalAhead(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	setupBareRemote(t, tmpDir, bareDir)
	gs := cloneFromBare(t, tmpDir, bareDir)

	// Local commits a change the remote has never seen — local main is now
	// strictly ahead of the remote.
	localFile, err := gs.wt.Filesystem.Create("local.txt")
	if err != nil {
		t.Fatalf("create local file: %v", err)
	}
	_, _ = localFile.Write([]byte("local content"))
	_ = localFile.Close()
	if _, err := gs.wt.Add("local.txt"); err != nil {
		t.Fatalf("add local: %v", err)
	}
	localCommitHash, err := gs.wt.Commit("local ahead", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	})
	if err != nil {
		t.Fatalf("local commit: %v", err)
	}

	err = gs.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		// Drop the tracking ref so the fetch re-creates it from the remote's
		// (behind-local) tip and the ancestry classification runs on distinct
		// local/remote tips rather than short-circuiting as
		// NoErrAlreadyUpToDate.
		if err := gs.backend.RemoveReference(plumbing.ReferenceName("refs/remotes/origin/main")); err != nil {
			return fmt.Errorf("remove tracking ref: %w", err)
		}

		newHash, err := gs.FetchAndMerge(ctx(), "origin", "main")
		if err != nil {
			return fmt.Errorf("FetchAndMerge local-ahead: %w", err)
		}
		if newHash != localCommitHash {
			return fmt.Errorf("expected local hash %s, got %s", localCommitHash, newHash)
		}

		// Local main must be left unchanged — there is nothing to pull.
		localRef, refErr := gs.repo.Reference(plumbing.ReferenceName("refs/heads/main"), true)
		if refErr != nil {
			return fmt.Errorf("resolve local ref: %w", refErr)
		}
		if localRef.Hash() != localCommitHash {
			return fmt.Errorf("local main ref changed on local-ahead: got %s, want %s", localRef.Hash(), localCommitHash)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestFetchAndMerge_LocalAhead: %v", err)
	}
}

// TestFetchAndMerge_NonMainBranch asserts that FetchAndMerge honors its branch
// parameter: it fetches the given branch's refspec and reads the matching
// tracking ref, so a non-"main" branch is pulled correctly. Previously the
// fetch refspec was hardwired to "refs/heads/main", so for a non-main branch
// the tracking-ref read at "refs/remotes/<remote>/<branch>" was stale/absent.
func TestFetchAndMerge_NonMainBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	setupBareRemote(t, tmpDir, bareDir)

	// Clone the bare remote to get a local repo that only knows main.
	gs := cloneFromBare(t, tmpDir, bareDir)

	// Clone the bare remote again as a "writer", commit a new commit, and push
	// it as a non-main "feature" branch so it appears on the remote AFTER the
	// local repo was created (the local repo must not already have the feature
	// tracking ref, otherwise the pull would be a no-op).
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
	featureFile, err := writerWT.Filesystem.Create("feature.txt")
	if err != nil {
		t.Fatalf("create feature file: %v", err)
	}
	_, _ = featureFile.Write([]byte("feature content"))
	_ = featureFile.Close()
	if _, err := writerWT.Add("feature.txt"); err != nil {
		t.Fatalf("add feature: %v", err)
	}
	if _, err := writerWT.Commit("feature commit", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("feature commit: %v", err)
	}
	if err := writer.Push(&git.PushOptions{
		RefSpecs: []config.RefSpec{config.RefSpec("refs/heads/main:refs/heads/feature")},
	}); err != nil {
		t.Fatalf("push feature branch: %v", err)
	}

	// The feature branch's tip hash on the remote.
	remoteRepo, err := git.PlainOpen(bareDir)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	featureRef, err := remoteRepo.Reference(plumbing.ReferenceName("refs/heads/feature"), true)
	if err != nil {
		t.Fatalf("remote feature ref: %v", err)
	}
	featureHash := featureRef.Hash()

	// Local repo starts on main; FetchAndMerge the non-main "feature" branch.
	err = gs.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		newHash, err := gs.FetchAndMerge(ctx(), "origin", "feature")
		if err != nil {
			return fmt.Errorf("FetchAndMerge(feature): %w", err)
		}
		if newHash != featureHash {
			return fmt.Errorf("expected feature hash %s, got %s", featureHash, newHash)
		}

		// The local branch must point at the feature tip.
		localRef, refErr := gs.repo.Reference(plumbing.ReferenceName("refs/heads/feature"), true)
		if refErr != nil {
			return fmt.Errorf("resolve local feature ref: %w", refErr)
		}
		if localRef.Hash() != featureHash {
			return fmt.Errorf("local feature ref: got %s, want %s", localRef.Hash(), featureHash)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestFetchAndMerge_NonMainBranch: %v", err)
	}
}

// TestFetchAndMerge_PullAfterWipe exercises the pull-after-wipe fast-forward
// path: the remote has a "wipe" commit that removed data, the local was
// cloned before the wipe and has stale untracked files from a pre-wipe type.
// After FetchAndMerge fast-forwards local main to the remote's wipe commit,
// the Force:true checkout replaces tracked files and the explicit
// wt.Clean removes stale untracked files — the working tree must exactly
// match the remote state.
func TestFetchAndMerge_PullAfterWipe(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	// Create a seed repo with a data commit.
	seedDir := filepath.Join(tmpDir, "seed")
	seedRepo, err := git.PlainInitWithOptions(seedDir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.ReferenceName("refs/heads/main"),
		},
	})
	if err != nil {
		t.Fatalf("init seed: %v", err)
	}
	seedWT, err := seedRepo.Worktree()
	if err != nil {
		t.Fatalf("seed worktree: %v", err)
	}
	sf, err := seedWT.Filesystem.Create("data.txt")
	if err != nil {
		t.Fatalf("create data: %v", err)
	}
	_, _ = sf.Write([]byte("initial data"))
	_ = sf.Close()
	if _, err := seedWT.Add("data.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := seedWT.Commit("data commit", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("commit data: %v", err)
	}

	// Clone as bare remote.
	_, err = git.PlainClone(bareDir, true, &git.CloneOptions{
		URL: "file://" + seedDir,
	})
	if err != nil {
		t.Fatalf("clone bare: %v", err)
	}

	// Clone local from the bare remote (local now has data.txt).
	localDir := filepath.Join(tmpDir, "local")
	localRepo, err := git.PlainClone(localDir, false, &git.CloneOptions{
		URL: "file://" + bareDir,
	})
	if err != nil {
		t.Fatalf("clone local: %v", err)
	}
	localWT, err := localRepo.Worktree()
	if err != nil {
		t.Fatalf("local worktree: %v", err)
	}

	// Create a stale untracked file in the local working tree — simulates
	// a previously wiped type's leftover files.
	if err := localWT.Filesystem.MkdirAll("entities/OldType", 0755); err != nil {
		t.Fatalf("mkdir stale dir: %v", err)
	}
	stale, err := localWT.Filesystem.Create("entities/OldType/stale.json")
	if err != nil {
		t.Fatalf("create stale file: %v", err)
	}
	_ = stale.Close()

	gs := wireGitStore(&gitStore{
		repo:     localRepo,
		wt:       localWT,
		fs:       localWT.Filesystem,
		backend:  localRepo.Storer,
		basePath: t.TempDir(),
	})

	// Push a "wipe" commit to the remote that removes data.txt.
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
	if _, err := workWT.Remove("data.txt"); err != nil {
		t.Fatalf("remove data: %v", err)
	}
	if _, err := workWT.Commit("wipe", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test"},
	}); err != nil {
		t.Fatalf("commit wipe: %v", err)
	}
	wipeHash, err := workRepo.Head()
	if err != nil {
		t.Fatalf("work head: %v", err)
	}
	if err := workRepo.Push(&git.PushOptions{}); err != nil {
		t.Fatalf("push wipe: %v", err)
	}

	err = gs.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		// Fast-forward from data to wipe
		newHash, pullErr := gs.FetchAndMerge(ctx(), "origin", "main")
		if pullErr != nil {
			return fmt.Errorf("FetchAndMerge: %w", pullErr)
		}
		if newHash != wipeHash.Hash() {
			return fmt.Errorf("expected wipe hash %s, got %s", wipeHash.Hash(), newHash)
		}

		// data.txt must be gone (tracked file removed by wipe commit)
		if _, statErr := gs.fs.Stat("data.txt"); statErr == nil {
			return fmt.Errorf("data.txt should be removed after wipe fast-forward")
		}

		// Stale untracked file must be cleaned by the post-checkout wt.Clean
		if _, statErr := gs.fs.Stat("entities/OldType/stale.json"); statErr == nil {
			return fmt.Errorf("stale untracked file should be cleaned after pull-after-wipe fast-forward")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("TestFetchAndMerge_PullAfterWipe: %v", err)
	}
}
