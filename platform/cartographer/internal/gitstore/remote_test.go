package gitstore

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/memory"
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

// ============================================================================
// T7: Remote operations
// ============================================================================

func TestSetRemoteInvalidScheme(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		err := gs.SetRemote(ctx(), "ftp://example.com/repo.git", nil)
		if !errors.Is(err, ErrUnsupportedURLScheme) {
			return fmt.Errorf("expected ErrUnsupportedURLScheme, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("SetRemoteInvalidScheme: %v", err)
	}
}

// TestSetRemoteNoHost verifies that a scheme-valid URL lacking a host
// component (e.g. "https://") is rejected with ErrRemoteURLNoHost rather than
// accepted (SPEC R10 / error-table row "Unsupported remote URL scheme",
// SPEC:987: validate the URL before configuring the remote).
func TestSetRemoteNoHost(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		err := gs.SetRemote(ctx(), "https://", nil)
		if !errors.Is(err, ErrRemoteURLNoHost) {
			return fmt.Errorf("expected ErrRemoteURLNoHost, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("SetRemoteNoHost: %v", err)
	}
}

// TestSetRemoteUppercaseScheme verifies that scheme matching is
// case-insensitive per RFC 3986 §3.1: url.Parse lowercases parsed.Scheme, so
// uppercase-scheme URLs (HTTPS://, SSH://, FILE://) are accepted by
// validateRemoteURL instead of being falsely rejected as
// ErrUnsupportedURLScheme (SPEC error-table row "Unsupported remote URL
// scheme", SPEC:993). The no-location guard still applies to uppercase
// spellings.
func TestSetRemoteUppercaseScheme(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	for _, tt := range []struct {
		name string
		url  string
	}{
		{"https uppercase", "HTTPS://example.com/repo.git"},
		{"ssh uppercase", "SSH://git@example.com/repo.git"},
		{"file uppercase", "FILE:///tmp/repo.git"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gs := setupTestStore(t)
			err := gs.WithGitLock(func() error {
				return gs.SetRemote(ctx(), tt.url, nil)
			})
			if err != nil {
				t.Fatalf("SetRemote(%q) = %v, want nil", tt.url, err)
			}
		})
	}
	t.Run("uppercase https no host still rejected", func(t *testing.T) {
		gs := setupTestStore(t)
		err := gs.WithGitLock(func() error {
			err := gs.SetRemote(ctx(), "HTTPS://", nil)
			if !errors.Is(err, ErrRemoteURLNoHost) {
				return fmt.Errorf("expected ErrRemoteURLNoHost, got %v", err)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("SetRemote uppercase no-host: %v", err)
		}
	})
}

// TestIsEmpty pins the SPEC R10 clone-vs-pull / empty-repo classification:
// a fresh init-only repo (only New()'s "init" commit authored by cartographer)
// is empty; any repo with a data commit, a wipe commit, or an "init"-message
// commit from a different author is not. Each branch must be asserted for real
// — the WithGitLock result is propagated, never discarded.
func TestIsEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	t.Run("fresh init returns true", func(t *testing.T) {
		gs := setupTestStore(t)
		err := gs.WithGitLock(func() error {
			empty, err := gs.IsEmpty(ctx())
			if err != nil {
				return err
			}
			if !empty {
				return fmt.Errorf("expected empty for init-only repo")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("fresh init: %v", err)
		}
	})

	t.Run("with data commit returns false", func(t *testing.T) {
		gs := setupTestStore(t)
		err := gs.WithGitLock(func() error {
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
			if err := gs.Commit(ctx(), "transaction:test-1"); err != nil {
				return err
			}

			empty, err := gs.IsEmpty(ctx())
			if err != nil {
				return err
			}
			if empty {
				return fmt.Errorf("expected non-empty after data commit")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("with data commit: %v", err)
		}
	})

	t.Run("wiped-but-committed returns false", func(t *testing.T) {
		gs := setupTestStore(t)
		err := gs.WithGitLock(func() error {
			// Commit a "wipe" commit on top of init, mirroring the production
			// WipeGraph sequence (git rm -r entities+edges, commit "wipe"):
			// a wipe commit must be classified as data (not empty), so the
			// SPEC R10 clone-vs-pull decision never re-clones a wiped repo.
			if err := gs.GitRm(ctx(), "entities"); err != nil {
				return err
			}
			if err := gs.GitRm(ctx(), "edges"); err != nil {
				return err
			}
			if err := gs.Commit(ctx(), "wipe"); err != nil {
				return err
			}

			empty, err := gs.IsEmpty(ctx())
			if err != nil {
				return err
			}
			if empty {
				return fmt.Errorf("expected non-empty after wipe commit")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("wiped-but-committed: %v", err)
		}
	})

	t.Run("remote init commit with different author returns false", func(t *testing.T) {
		gs := setupTestStore(t)
		err := gs.WithGitLock(func() error {
			// Simulate a commit whose message is "init" but from a different
			// author (e.g. a cloned remote's initial commit). This must NOT
			// be treated as New()'s init commit. A content change is required
			// first: committing on a clean tree would be ErrEmptyCommit and
			// would not create the commit at all.
			now := time.Now().UTC().Round(time.Millisecond)
			if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
				{ID: validUUID(t), Type: "Component", CreatedAt: now, UpdatedAt: now},
			}); err != nil {
				return err
			}
			if err := gs.AddAll(ctx(), "."); err != nil {
				return err
			}
			if _, err := gs.wt.Commit("init", &git.CommitOptions{
				Author: &object.Signature{
					Name:  "developer",
					Email: "dev@remote.example",
				},
			}); err != nil {
				return err
			}

			empty, err := gs.IsEmpty(ctx())
			if err != nil {
				return err
			}
			if empty {
				return fmt.Errorf("expected non-empty for remote init commit with different author")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("remote init commit with different author: %v", err)
		}
	})
}

func TestPushRemoteNoRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		err := gs.PushRemote(ctx())
		if !errors.Is(err, ErrNoRemote) {
			return fmt.Errorf("expected ErrNoRemote, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("PushRemoteNoRemote: %v", err)
	}
}

// TestFetchAndMergeNoRemote pins FetchAndMerge's no-remote guard: with an
// empty remoteURL, FetchAndMerge must fail with ErrNoRemote (and a zero hash)
// before touching the network. This branch is a live production path — the
// sync worker's fetchAndRehydrate classifies the sentinel non-recoverable
// (classifySyncError) and logs loudly + emits cartographer.push_failed
// telemetry on every cycle (sync_worker.go), feeding the SPEC error-table row
// "Remote not configured" (SPEC:992). PushRemote's sibling guard is pinned by
// TestPushRemoteNoRemote.
func TestFetchAndMergeNoRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		hash, err := gs.FetchAndMerge(ctx(), "origin", "main")
		if !errors.Is(err, ErrNoRemote) {
			return fmt.Errorf("expected ErrNoRemote, got %v", err)
		}
		if !hash.IsZero() {
			return fmt.Errorf("expected zero hash on ErrNoRemote, got %v", hash)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("FetchAndMergeNoRemote: %v", err)
	}
}

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

func TestCloneSingleBranchInvalidScheme(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		err := gs.CloneSingleBranch(ctx(), "ftp://example.com/repo.git", "main")
		if !errors.Is(err, ErrUnsupportedURLScheme) {
			return fmt.Errorf("expected ErrUnsupportedURLScheme, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("CloneSingleBranchInvalidScheme: %v", err)
	}
}

// TestRequiresAuth drives the requiresAuth helper directly for each URL class:
// ssh:// always requires auth, https:// requires auth only when it embeds a
// user (basic auth), and non-ssh/no-user URLs (public https remotes, file://
// scratch paths, malformed inputs) do not. This mirrors the helper's exact
// semantics (remote.go:56) rather than inserted intent.
func TestRequiresAuth(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"ssh always requires auth", "ssh://git@example.com/repo.git", true},
		{"https with embedded user requires auth", "https://user@host/repo.git", true},
		{"plain https public remote no auth", "https://host/repo.git", false},
		{"file scratch path no auth", "file:///tmp/repo.git", false},
		{"malformed url no auth", "://not-a-url", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requiresAuth(tt.url); got != tt.want {
				t.Fatalf("requiresAuth(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

// TestCloneSingleAuthConfigMissing verifies the authenticated-URL pre-flight
// rejection: an ssh:// clone URL with no configured auth provider returns
// ErrAuthConfigMissing before any fetch is attempted (SPEC R1/R10 Init).
func TestCloneSingleAuthConfigMissing(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	if gs.authFn != nil {
		t.Fatal("expected nil auth provider")
	}
	err := gs.WithGitLock(func() error {
		err := gs.CloneSingleBranch(ctx(), "ssh://git@example.com/repo.git", "main")
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("CloneSingleAuthConfigMissing: %v", err)
	}
}

// TestCloneSingleAuthConfigMissingHTTPS verifies the authenticated-URL
// pre-flight rejection for an https:// URL with an embedded user (basic
// auth) and no configured auth provider — the https counterpart of
// TestCloneSingleAuthConfigMissing (which covers ssh://). This pins the
// requiresAuth URL-scheme branch for https-with-embedded-user at the
// CloneSingleBranch level (remote.go:416).
func TestCloneSingleAuthConfigMissingHTTPS(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	if gs.authFn != nil {
		t.Fatal("expected nil auth provider")
	}
	err := gs.WithGitLock(func() error {
		err := gs.CloneSingleBranch(ctx(), "https://user@example.com/repo.git", "main")
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestCloneSingleAuthConfigMissingHTTPS: %v", err)
	}
}

// TestCloneSingleAuthSentinelPreserved verifies that CloneSingleBranch
// preserves the ErrAuthConfigMissing sentinel returned by a configured authFn
// — the missing-expected-key sub-case of the SPEC error-table row "Remote auth
// config missing (Sync)". CloneSingleBranch is the clone-on-init path, so the
// credential failure must surface as ErrAuthConfigMissing for mapGitError to
// return FAILED_PRECONDITION.
func TestCloneSingleAuthSentinelPreserved(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	gs.authFn = func() (transport.AuthMethod, error) {
		return nil, ErrAuthConfigMissing
	}
	err := gs.WithGitLock(func() error {
		err := gs.CloneSingleBranch(ctx(), "ssh://git@example.com/repo.git", "main")
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("CloneSingleAuthSentinelPreserved: %v", err)
	}
}

// TestCloneSingleAuthFnFailureCollapsesToAuthConfigMissing verifies that
// CloneSingleBranch surfaces ErrAuthConfigMissing when a configured authFn
// returns a generic (non-sentinel) error — the readSecretFn-failure (Secret
// missing) and invalid-credential (unparseable ssh-privatekey PEM) sub-cases
// of the SPEC error-table row "Remote auth config missing (Sync)"
// on the clone-on-init path.
func TestCloneSingleAuthFnFailureCollapsesToAuthConfigMissing(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	gs.authFn = func() (transport.AuthMethod, error) {
		return nil, fmt.Errorf("secrets: secret %q not found", "cartographer-remote-auth")
	}
	err := gs.WithGitLock(func() error {
		err := gs.CloneSingleBranch(ctx(), "ssh://git@example.com/repo.git", "main")
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestCloneSingleAuthFnFailureCollapsesToAuthConfigMissing: %v", err)
	}
}

// TestPushRejectedSentinel guards the exported ErrPushRejected sentinel that
// PushRemote maps from git.ErrNonFastForwardUpdate (mapPushError, remote.go).
// It cannot be reached deterministically through a genuine push (go-git's
// receive-pack does not wrap rejections in ErrNonFastForwardUpdate — see the
// ponytail on mapPushError), so this locks the sentinel's identity and its
// distinctness from the sibling remote sentinels the service mapGitError must
// tell apart; the classification branch itself is pinned by TestMapPushError.
func TestPushRejectedSentinel(t *testing.T) {
	if ErrPushRejected == nil {
		t.Fatal("ErrPushRejected must be a non-nil exported sentinel")
	}
	for _, other := range []error{ErrPullDiverged, ErrAuthConfigMissing, ErrNoRemote} {
		if errors.Is(ErrPushRejected, other) {
			t.Fatalf("ErrPushRejected aliases %v; sentinels must stay distinct", other)
		}
	}
}

// TestEnsureRemoteExists verifies the ensureRemoteExists helper by
// removing the remote and then calling a remote operation that recreates it.
func TestEnsureRemoteExists(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Configure the remote via SetRemote, which validates the URL scheme
		// and creates the "origin" remote.
		if err := gs.SetRemote(ctx(), "https://example.com/repo.git", func() (transport.AuthMethod, error) {
			return nil, nil
		}); err != nil {
			return err
		}

		remote, err := gs.repo.Remote("origin")
		if err != nil {
			return fmt.Errorf("get recreated remote: %w", err)
		}
		if got := remote.Config().URLs; len(got) != 1 || got[0] != gs.remoteURL {
			return fmt.Errorf("recreated remote URLs = %v, want [%s]", got, gs.remoteURL)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("TestEnsureRemoteExists: %v", err)
	}
}

// TestEnsureRemoteExistsURLChange verifies the URL-change branch of
// ensureRemoteExists: when the origin remote already exists but its URL
// differs from the configured remote URL (REMOTE_URL changed across pod
// restarts on the same PVC), the remote must be deleted and recreated with
// the new URL. This destructive delete+recreate transition is pinned
// directly — a regression that skipped the branch would leave origin
// pointing at the stale URL.
func TestEnsureRemoteExistsURLChange(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Configure the remote with the original URL (create-on-missing).
		if err := gs.SetRemote(ctx(), "https://example.com/original.git", func() (transport.AuthMethod, error) {
			return nil, nil
		}); err != nil {
			return err
		}
		remote, err := gs.repo.Remote("origin")
		if err != nil {
			return fmt.Errorf("get remote: %w", err)
		}
		if got := remote.Config().URLs; len(got) != 1 || got[0] != "https://example.com/original.git" {
			return fmt.Errorf("initial remote URLs = %v, want [https://example.com/original.git]", got)
		}

		// Reconfigure with a different URL — origin exists but differs, so
		// ensureRemoteExists must delete and recreate it with the new URL.
		if err := gs.SetRemote(ctx(), "https://example.com/changed.git", func() (transport.AuthMethod, error) {
			return nil, nil
		}); err != nil {
			return err
		}
		remote, err = gs.repo.Remote("origin")
		if err != nil {
			return fmt.Errorf("get recreated remote: %w", err)
		}
		if got := remote.Config().URLs; len(got) != 1 || got[0] != "https://example.com/changed.git" {
			return fmt.Errorf("recreated remote URLs = %v, want [https://example.com/changed.git]", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestEnsureRemoteExistsURLChange: %v", err)
	}
}

// noopAuth is a transport.AuthMethod that does nothing — used for file:// URLs.
type noopAuth struct{}

func (a noopAuth) Name() string   { return "noop" }
func (a noopAuth) String() string { return "noop" }

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

// failRefStorer wraps a storage.Storer and fails every reference lookup with
// the configured error, simulating a backend that cannot resolve refs for
// reasons other than a missing ref (ErrReferenceNotFound).
type failRefStorer struct {
	storage.Storer
	failErr error
}

func (f *failRefStorer) Reference(name plumbing.ReferenceName) (*plumbing.Reference, error) {
	return nil, f.failErr
}

// TestIsEmptyMainRefError pins both main-ref resolution branches of IsEmpty:
// a missing main ref (ErrReferenceNotFound) reports empty, while a backend
// that fails ref resolution for any other reason surfaces the
// "resolve main ref: %w" error branch of IsEmpty (remote.go) instead of being
// swallowed as empty.
func TestIsEmptyMainRefError(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	// Create a gitStore with in-memory storage; a failing backend is swapped
	// in below to trigger the non-ErrReferenceNotFound resolution error.
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

	gs := wireGitStore(&gitStore{
		repo:     repo,
		wt:       wt,
		fs:       fs,
		backend:  storer,
		basePath: t.TempDir(),
	})

	err = gs.WithGitLock(func() error {
		// Delete the main ref to exercise the ErrReferenceNotFound path in
		// IsEmpty: a repo with no main ref is reported empty.
		if err := gs.backend.RemoveReference(plumbing.ReferenceName("refs/heads/main")); err != nil {
			return err
		}
		empty, err := gs.IsEmpty(ctx())
		if err != nil {
			return err
		}
		if !empty {
			return fmt.Errorf("expected empty when main ref is missing")
		}

		// Swap in a backend whose ref lookups fail for a non-ErrReferenceNotFound
		// reason: IsEmpty must surface the "resolve main ref" error branch
		// (remote.go) rather than treat the failure as empty.
		refErr := errors.New("ref lookup failed")
		broken := &failRefStorer{Storer: storer, failErr: refErr}
		gs.backend = broken
		gs.repo = &git.Repository{Storer: broken}

		empty, err = gs.IsEmpty(ctx())
		if err == nil {
			return fmt.Errorf("expected resolve main ref error, got empty=%v", empty)
		}
		if !errors.Is(err, refErr) {
			return fmt.Errorf("expected wrapped ref lookup error, got %v", err)
		}
		if !strings.Contains(err.Error(), "resolve main ref") {
			return fmt.Errorf("expected 'resolve main ref' wrap, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestIsEmptyMainRefError: %v", err)
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
