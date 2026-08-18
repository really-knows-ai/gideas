package gitstore

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// noopAuth is a transport.AuthMethod that does nothing — used for file:// URLs.
type noopAuth struct{}

func (a noopAuth) Name() string   { return "noop" }
func (a noopAuth) String() string { return "noop" }

// TestPushRemoteWithAuth verifies PushRemote's auth contract: ErrAuthConfigMissing
// is returned only when the operation cannot be attempted — a URL that demands
// credentials (ssh:// or an https:// URL embedding a user) with no auth provider
// configured, or a configured authFn that errors. A public remote (no credentials
// demanded) is pushed anonymously: a nil authFn and a nil-auth authFn result both
// proceed past the auth guard, so the failure is a transport error, never the
// mislabelled auth-config sentinel. (The push itself is driven against port 1 on
// loopback — connection refused, no network — so the guard-pass is observable as a
// non-auth failure; the full anonymous-push success path is pinned by
// TestPushRemoteAnonymous.)
func TestPushRemoteWithAuth(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// A URL that demands credentials (embedded user) with no auth provider
		// → ErrAuthConfigMissing via the requiresAuth pre-flight.
		if err := gs.SetRemote(ctx(), "https://user@example.com/repo.git", nil); err != nil {
			return err
		}
		err := gs.PushRemote(ctx())
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing for credentials-requiring URL with nil authFn, got %v", err)
		}

		// An ssh URL always demands credentials → ErrAuthConfigMissing with a
		// nil authFn.
		if err := gs.SetRemote(ctx(), "ssh://git@example.com/repo.git", nil); err != nil {
			return err
		}
		err = gs.PushRemote(ctx())
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing for ssh URL with nil authFn, got %v", err)
		}

		// Set authFn that returns an error (readSecretFn failure / invalid
		// credential) → ErrAuthConfigMissing (SPEC error row "Remote auth
		// config missing (Sync)" → FAILED_PRECONDITION)
		if err := gs.SetRemote(ctx(), "https://example.com/repo.git", func() (transport.AuthMethod, error) {
			return nil, fmt.Errorf("auth resolution failure")
		}); err != nil {
			return err
		}
		err = gs.PushRemote(ctx())
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing, got %v", err)
		}

		// A plain public https URL (no credentials demanded) with a nil authFn:
		// the anonymous push is attempted — the failure is a transport error,
		// never ErrAuthConfigMissing.
		if err := gs.SetRemote(ctx(), "https://127.0.0.1:1/repo.git", nil); err != nil {
			return err
		}
		err = gs.PushRemote(ctx())
		if err == nil || errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected transport failure for public remote with nil authFn, got %v", err)
		}

		// Same for a configured authFn returning nil — an explicit anonymous
		// selection (e.g. the production buildResolveAuthFn closure for a public
		// remote with no secretRef).
		if err := gs.SetRemote(ctx(), "https://127.0.0.1:1/repo.git", func() (transport.AuthMethod, error) {
			return nil, nil
		}); err != nil {
			return err
		}
		err = gs.PushRemote(ctx())
		if err == nil || errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected transport failure for anonymous-selecting authFn, got %v", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("PushRemoteWithAuth: %v", err)
	}
}

// TestPushRemoteAnonymous verifies the operator requirement: a remote configured
// without a secretRef must push anonymously (SPEC R10 "remote configured ⇒ pushes
// happen"). Both a nil authFn (auth not configured) and a configured authFn
// returning nil (explicit anonymous selection) must push successfully to a remote
// that does not demand credentials. Uses the local-bare-repo technique from
// TestRemotePushPull: a file:// URL demands no credentials (requiresAuth false,
// exactly like a plain public https URL), so the anonymous push is driven
// end-to-end without the network, and the remote ref must advance.
func TestPushRemoteAnonymous(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	// Create a bare remote repo
	_, err := git.PlainInitWithOptions(bareDir, &git.PlainInitOptions{
		Bare: true,
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.ReferenceName("refs/heads/main"),
		},
	})
	if err != nil {
		t.Fatalf("init bare remote: %v", err)
	}

	localDir := filepath.Join(tmpDir, "local")
	store, err := New(localDir)
	if err != nil {
		t.Fatalf("New local: %v", err)
	}
	gs := store.(*gitStore)

	for _, tc := range []struct {
		name   string
		authFn func() (transport.AuthMethod, error)
	}{
		{name: "nil authFn", authFn: nil},
		{name: "authFn selecting anonymous access", authFn: func() (transport.AuthMethod, error) {
			return nil, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := store.WithGitLock(func() error {
				gs.remoteURL = "file://" + bareDir
				gs.authFn = tc.authFn

				// Create the "origin" remote directly on the go-git repo so the
				// subtest drives remote state by hand (SetRemote's
				// ensureRemoteExists wiring is exercised elsewhere).
				_, err := gs.repo.CreateRemote(&config.RemoteConfig{
					Name: "origin",
					URLs: []string{gs.remoteURL},
				})
				if err != nil && !errors.Is(err, git.ErrRemoteExists) {
					return fmt.Errorf("create remote: %w", err)
				}

				// Make a commit on local
				now := time.Now().UTC().Round(time.Millisecond)
				if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
					{ID: validUUID(t), Type: "Component", CreatedAt: now, UpdatedAt: now},
				}); err != nil {
					return err
				}
				if err := gs.AddAll(ctx(), "."); err != nil {
					return err
				}
				if err := gs.Commit(ctx(), "transaction:anonymous-push"); err != nil {
					return err
				}

				// Push anonymously to the remote
				if err := gs.PushRemote(ctx()); err != nil {
					return fmt.Errorf("anonymous push: %w", err)
				}

				// Verify the remote ref advanced
				remoteRepo, err := git.PlainOpen(bareDir)
				if err != nil {
					return fmt.Errorf("open remote: %w", err)
				}
				ref, err := remoteRepo.Reference(plumbing.ReferenceName("refs/heads/main"), true)
				if err != nil {
					return fmt.Errorf("remote ref: %w", err)
				}
				if ref.Hash().IsZero() {
					return fmt.Errorf("expected non-zero hash on remote after anonymous push")
				}
				return nil
			})
			if err != nil {
				t.Fatalf("TestPushRemoteAnonymous[%s]: %v", tc.name, err)
			}
		})
	}
}

// TestPushMainOnlyScopeBoundary pins the SPEC R10 scope boundary: only main is
// synced; transaction branches are local-only and never pushed. PushRemote
// hardcodes the refspec refs/heads/main:refs/heads/main (remote.go), so a
// transaction branch present locally must never appear in the remote's ref
// list after a push. This test fails if the refspec is widened (e.g. to push
// all heads) and a transaction branch leaks to the remote — TestPushRemoteWithAuth,
// TestPushRemoteAnonymous and TestRemotePushPull only assert that refs/heads/main
// advances, so none of them would catch a leak.
func TestPushMainOnlyScopeBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	// Create a bare remote repo.
	if _, err := git.PlainInitWithOptions(bareDir, &git.PlainInitOptions{
		Bare: true,
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.ReferenceName("refs/heads/main"),
		},
	}); err != nil {
		t.Fatalf("init bare remote: %v", err)
	}

	localDir := filepath.Join(tmpDir, "local")
	store, err := New(localDir)
	if err != nil {
		t.Fatalf("New local: %v", err)
	}
	gs := store.(*gitStore)
	txID := validUUID(t)

	err = store.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		if _, err := gs.repo.CreateRemote(&config.RemoteConfig{
			Name: "origin",
			URLs: []string{gs.remoteURL},
		}); err != nil && !errors.Is(err, git.ErrRemoteExists) {
			return fmt.Errorf("create remote: %w", err)
		}

		// Commit something on main so the push has content to send.
		now := time.Now().UTC().Round(time.Millisecond)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: validUUID(t), Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "transaction:scope-main"); err != nil {
			return err
		}

		// Create a transaction branch from main, advance it with its own commit
		// so it diverges from main and is present locally. Per SPEC R10 the
		// transaction branch is local-only and must never be pushed.
		if err := gs.CreateBranch(ctx(), txID); err != nil {
			return err
		}
		if err := gs.Checkout(ctx(), txID); err != nil {
			return err
		}
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: validUUID(t), Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "transaction:"+txID); err != nil {
			return err
		}

		// Push: only refs/heads/main is synced (the hardcoded refspec).
		if err := gs.PushRemote(ctx()); err != nil {
			return fmt.Errorf("push: %w", err)
		}

		// Enumerate the remote's ref list: the transaction branch must be
		// absent. Listing every branch ref (not just querying main) is the
		// assertion the existing push tests omit — it fails if the refspec is
		// ever widened to carry transaction branches to the remote.
		remoteRepo, err := git.PlainOpen(bareDir)
		if err != nil {
			return fmt.Errorf("open remote: %w", err)
		}
		refIter, err := remoteRepo.References()
		if err != nil {
			return fmt.Errorf("list remote refs: %w", err)
		}
		defer refIter.Close()
		var remoteHeads []string
		if err := refIter.ForEach(func(ref *plumbing.Reference) error {
			if ref.Name().IsBranch() {
				remoteHeads = append(remoteHeads, ref.Name().Short())
			}
			return nil
		}); err != nil {
			return fmt.Errorf("iterate remote refs: %w", err)
		}
		if len(remoteHeads) != 1 || remoteHeads[0] != "main" {
			return fmt.Errorf(
				"remote branches after push = %v, want exactly [main] "+
					"(transaction branch %s must never be pushed)", remoteHeads, txID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestPushMainOnlyScopeBoundary: %v", err)
	}
}

// TestRemotePushPull exercises PushRemote and FetchAndMerge using temp
// directory repos with file:// URLs set directly on the gitStore.
func TestRemotePushPull(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	// Create a bare remote repo
	_, err := git.PlainInitWithOptions(bareDir, &git.PlainInitOptions{
		Bare: true,
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.ReferenceName("refs/heads/main"),
		},
	})
	if err != nil {
		t.Fatalf("init bare remote: %v", err)
	}

	// Create local repo
	localDir := filepath.Join(tmpDir, "local")
	store, err := New(localDir)
	if err != nil {
		t.Fatalf("New local: %v", err)
	}
	gs := store.(*gitStore)

	err = store.WithGitLock(func() error {
		// Configure the remote URL and authFn directly
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		// Create the "origin" remote directly on the go-git repo
		_, err := gs.repo.CreateRemote(&config.RemoteConfig{
			Name: "origin",
			URLs: []string{gs.remoteURL},
		})
		if err != nil {
			return fmt.Errorf("create remote: %w", err)
		}

		// Make a commit on local
		now := time.Now().UTC().Round(time.Millisecond)
		e1ID := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: e1ID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "transaction:push-test"); err != nil {
			return err
		}

		// Push to remote
		if err := gs.PushRemote(ctx()); err != nil {
			return fmt.Errorf("push: %w", err)
		}

		// Verify on remote
		remoteRepo, err := git.PlainOpen(bareDir)
		if err != nil {
			return fmt.Errorf("open remote: %w", err)
		}
		ref, err := remoteRepo.Reference(plumbing.ReferenceName("refs/heads/main"), true)
		if err != nil {
			return fmt.Errorf("remote ref: %w", err)
		}
		if ref.Hash().IsZero() {
			return fmt.Errorf("expected non-zero hash on remote")
		}

		// Fetch back from remote (should be up-to-date)
		if _, err := gs.FetchAndMerge(ctx(), "origin", "main"); err != nil {
			return fmt.Errorf("fetch: %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("RemotePushPull: %v", err)
	}
}

// TestPushAlreadyUpToDate tests that pushing when already up-to-date
// returns no error (NoErrAlreadyUpToDate is handled as success).
func TestPushAlreadyUpToDate(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git repository on disk")
	}
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "remote.git")

	// Create bare remote with initial commit
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
	if _, err := workRepo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{"file://" + bareDir},
	}); err != nil {
		t.Fatalf("create remote: %v", err)
	}

	// Clone work repo as bare
	_, err = git.PlainClone(bareDir, true, &git.CloneOptions{
		URL: "file://" + workDir,
	})
	if err != nil {
		t.Fatalf("clone bare: %v", err)
	}

	// Open a gitStore on the work repo to test push via gitstore
	// The work and bare repos already have the same content.
	gs := wireGitStore(&gitStore{
		repo:     workRepo,
		wt:       workWT,
		fs:       workWT.Filesystem,
		backend:  workRepo.Storer,
		basePath: t.TempDir(),
	})

	err = gs.WithGitLock(func() error {
		gs.remoteURL = "file://" + bareDir
		gs.authFn = func() (transport.AuthMethod, error) {
			return noopAuth{}, nil
		}

		// Push — should be no-op (already up-to-date)
		if err := gs.PushRemote(ctx()); err != nil {
			return fmt.Errorf("push: %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("TestPushAlreadyUpToDate: %v", err)
	}
}
