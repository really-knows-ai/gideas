package gitstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// SetRemote configures the remote URL and auth provider for push/pull/fetch/clone.
// Validates the URL scheme (must be https:// or ssh://) and returns
// ErrUnsupportedURLScheme otherwise. authFn is called on each remote operation
// so credentials can be refreshed on every call.
func (g *gitStore) SetRemote(ctx context.Context, url string, authFn func() (transport.AuthMethod, error)) error {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "ssh://") {
		return ErrUnsupportedURLScheme
	}
	g.remoteURL = url
	g.authFn = authFn

	_, err := g.repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{url}})
	if err != nil && !errors.Is(err, git.ErrRemoteExists) {
		return fmt.Errorf("create remote: %w", err)
	}
	return nil
}

// FetchRemote fetches the main branch from the remote origin.
// Returns ErrNoRemote if no remote is configured, ErrAuthConfigMissing
// if no auth provider is configured, and typed errors for auth and
// network failures.
func (g *gitStore) FetchRemote(ctx context.Context) error {
	if g.remoteURL == "" {
		return ErrNoRemote
	}
	if g.authFn == nil {
		return ErrAuthConfigMissing
	}

	if err := g.ensureRemoteExists(); err != nil {
		return err
	}

	auth, err := g.resolveAuth()
	if err != nil {
		return err
	}

	err = g.repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		Auth:       auth,
		Force:      false,
		RefSpecs:   []config.RefSpec{"+refs/heads/main:refs/heads/main"},
	})
	if err != nil {
		if errors.Is(err, git.NoErrAlreadyUpToDate) {
			return nil
		}
		if errors.Is(err, transport.ErrAuthenticationRequired) ||
			strings.Contains(err.Error(), "authentication") {
			return ErrAuthFailed
		}
		if strings.Contains(err.Error(), "no such host") ||
			strings.Contains(err.Error(), "connection refused") ||
			strings.Contains(err.Error(), "i/o timeout") ||
			strings.Contains(err.Error(), "dial tcp") {
			return ErrRemoteUnreachable
		}
		return fmt.Errorf("fetch: %w", err)
	}
	return nil
}

// PushRemote pushes the main branch to the remote origin.
// Returns ErrNoRemote if no remote is configured, ErrAuthConfigMissing
// if no auth provider is configured, and typed errors for auth, network,
// and rejection failures.
func (g *gitStore) PushRemote(ctx context.Context) error {
	if g.remoteURL == "" {
		return ErrNoRemote
	}
	if g.authFn == nil {
		return ErrAuthConfigMissing
	}

	if err := g.ensureRemoteExists(); err != nil {
		return err
	}

	auth, err := g.resolveAuth()
	if err != nil {
		return err
	}

	err = g.repo.PushContext(ctx, &git.PushOptions{
		RemoteName: "origin",
		Auth:       auth,
		Force:      false,
		RefSpecs:   []config.RefSpec{"+refs/heads/main:refs/heads/main"},
	})
	if err != nil {
		if errors.Is(err, git.NoErrAlreadyUpToDate) {
			return nil
		}
		if errors.Is(err, transport.ErrAuthenticationRequired) ||
			strings.Contains(err.Error(), "authentication") {
			return ErrAuthFailed
		}
		if strings.Contains(err.Error(), "non-fast-forward") ||
			strings.Contains(err.Error(), "[rejected]") {
			return ErrPushRejected
		}
		if strings.Contains(err.Error(), "no such host") ||
			strings.Contains(err.Error(), "connection refused") ||
			strings.Contains(err.Error(), "i/o timeout") ||
			strings.Contains(err.Error(), "dial tcp") {
			return ErrRemoteUnreachable
		}
		return fmt.Errorf("push: %w", err)
	}
	return nil
}

// PullAndFastForward pulls from the remote origin and performs a
// fast-forward merge. Returns ErrNoRemote if no remote is configured,
// ErrAuthConfigMissing if no auth provider is configured, and typed
// errors for auth, network, and divergence failures.
func (g *gitStore) PullAndFastForward(ctx context.Context) error {
	if g.remoteURL == "" {
		return ErrNoRemote
	}
	if g.authFn == nil {
		return ErrAuthConfigMissing
	}

	if err := g.ensureRemoteExists(); err != nil {
		return err
	}

	auth, err := g.resolveAuth()
	if err != nil {
		return err
	}

	err = g.wt.PullContext(ctx, &git.PullOptions{
		RemoteName:   "origin",
		Auth:         auth,
		Force:        false,
		SingleBranch: true,
	})
	if err != nil {
		if errors.Is(err, git.NoErrAlreadyUpToDate) {
			return nil
		}
		if errors.Is(err, transport.ErrAuthenticationRequired) ||
			strings.Contains(err.Error(), "authentication") {
			return ErrAuthFailed
		}
		if errors.Is(err, git.ErrNonFastForwardUpdate) ||
			strings.Contains(err.Error(), "non-fast-forward") {
			return ErrPullDiverged
		}
		if strings.Contains(err.Error(), "no such host") ||
			strings.Contains(err.Error(), "connection refused") ||
			strings.Contains(err.Error(), "i/o timeout") ||
			strings.Contains(err.Error(), "dial tcp") {
			return ErrRemoteUnreachable
		}
		return fmt.Errorf("pull: %w", err)
	}
	return nil
}

// HasRemote returns whether the remote "origin" is configured.
// Returns an error if the check itself fails (I/O error, storage corruption).
func (g *gitStore) HasRemote(ctx context.Context) (bool, error) {
	_, err := g.repo.Remote("origin")
	if err != nil {
		if errors.Is(err, git.ErrRemoteNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("check remote: %w", err)
	}
	return true, nil
}

// CloneSingleBranch fetches a single branch from a remote URL into the
// existing repository. The url parameter is passed explicitly (not from
// g.remoteURL). After fetching, the local main ref is set to the fetched
// commit and the working tree is checked out to main.
func (g *gitStore) CloneSingleBranch(ctx context.Context, url, branch string) error {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "ssh://") {
		return ErrUnsupportedURLScheme
	}

	// Resolve auth: nil authFn or nil return means anonymous access to public
	// remotes.  Unlike FetchRemote/PushRemote/PullAndFastForward, this path
	// explicitly allows anonymous clone for the initial remote bootstrap.
	var auth transport.AuthMethod
	var err error
	if g.authFn != nil {
		auth, err = g.authFn()
		if err != nil {
			return ErrRemoteAuthResolutionFailed
		}
	}
	// nil auth is OK for anonymous public remotes

	// Ensure remote exists
	_, err = g.repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{url}})
	if err != nil && !errors.Is(err, git.ErrRemoteExists) {
		return fmt.Errorf("create remote: %w", err)
	}

	// Fetch from remote
	err = g.repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		Auth:       auth,
		Force:      false,
	})
	if err != nil {
		if errors.Is(err, git.NoErrAlreadyUpToDate) {
			// Already up-to-date is fine; continue to set main ref
		} else {
			if errors.Is(err, transport.ErrAuthenticationRequired) ||
				strings.Contains(err.Error(), "authentication") {
				return ErrAuthFailed
			}
			if strings.Contains(err.Error(), "no such host") ||
				strings.Contains(err.Error(), "connection refused") ||
				strings.Contains(err.Error(), "i/o timeout") ||
				strings.Contains(err.Error(), "dial tcp") {
				return ErrRemoteUnreachable
			}
			return fmt.Errorf("fetch: %w", err)
		}
	}

	// Resolve the remote tracking ref for the branch, set main to point at it
	remoteRef, err := g.repo.Reference(
		plumbing.ReferenceName("refs/remotes/origin/"+branch), true)
	if err != nil {
		return fmt.Errorf("resolve remote ref %s: %w", branch, err)
	}

	mainRef := plumbing.NewHashReference(
		plumbing.ReferenceName("refs/heads/main"),
		remoteRef.Hash(),
	)
	if err := g.backend.SetReference(mainRef); err != nil {
		return fmt.Errorf("set main ref: %w", err)
	}

	// Checkout main
	if err := g.wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.ReferenceName("refs/heads/main"),
		Force:  true,
	}); err != nil {
		return fmt.Errorf("checkout main: %w", err)
	}

	// Re-open repo to refresh worktree and filesystem
	// Use g.basePath to locate the repo directory
	repoPath := g.basePath + "/graph-repo"
	reopened, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("reopen repo: %w", err)
	}

	wt, err := reopened.Worktree()
	if err != nil {
		return fmt.Errorf("reopen worktree: %w", err)
	}

	g.repo = reopened
	g.wt = wt
	g.fs = wt.Filesystem

	return nil
}

// IsEmpty returns true if the repository has no graph-data commits on main.
// A repo with only the init commit (produced by New()) is considered empty.
func (g *gitStore) IsEmpty(ctx context.Context) (bool, error) {
	mainRef, err := g.repo.Reference(
		plumbing.ReferenceName("refs/heads/main"), true)
	if err != nil {
		if err == plumbing.ErrReferenceNotFound {
			return true, nil
		}
		return false, fmt.Errorf("resolve main ref: %w", err)
	}

	log, err := g.repo.Log(&git.LogOptions{From: mainRef.Hash()})
	if err != nil {
		return false, fmt.Errorf("log: %w", err)
	}
	defer log.Close()

	err = log.ForEach(func(commit *object.Commit) error {
		if commit.Message != "init" {
			// Found a non-init commit
			return fmt.Errorf("HAS_DATA")
		}
		return nil
	})
	if err != nil {
		if err.Error() == "HAS_DATA" {
			return false, nil
		}
		return false, fmt.Errorf("iterate log: %w", err)
	}

	// Only init commit(s) found
	return true, nil
}

// ensureRemoteExists creates the "origin" remote if it does not already exist.
func (g *gitStore) ensureRemoteExists() error {
	_, err := g.repo.Remote("origin")
	if err != nil {
		if errors.Is(err, git.ErrRemoteNotFound) {
			_, err = g.repo.CreateRemote(&config.RemoteConfig{
				Name: "origin",
				URLs: []string{g.remoteURL},
			})
			if err != nil && !errors.Is(err, git.ErrRemoteExists) {
				return fmt.Errorf("create remote: %w", err)
			}
			return nil
		}
		return fmt.Errorf("check remote: %w", err)
	}
	return nil
}

// resolveAuth calls the auth provider and returns typed errors.
func (g *gitStore) resolveAuth() (transport.AuthMethod, error) {
	auth, err := g.authFn()
	if err != nil {
		return nil, ErrRemoteAuthResolutionFailed
	}
	if auth == nil {
		return nil, ErrAuthConfigMissing
	}
	return auth, nil
}
