package gitstore

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/memory"
)

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
