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
// so credentials can be refreshed on every call. A configured authFn returning
// nil credentials explicitly selects anonymous access for fetch and pull;
// a nil authFn means auth was not configured.
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
// if no auth provider is configured, and typed errors for auth and network
// failures. A configured provider may return nil for an anonymous public remote.
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

	auth, err := g.resolveAuth(true)
	if err != nil {
		return err
	}

	err = g.repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		Auth:       auth,
		Force:      false,
		RefSpecs:   []config.RefSpec{"refs/heads/main:refs/heads/main"},
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

// FetchAndMerge fetches from the remote and merges the remote tracking branch
// into the local branch. If the local branch does not exist, it is created
// pointing at the remote tracking ref. If fast-forward is possible, the local
// ref is advanced. Otherwise a merge commit is created with two parents.
// Returns the new HEAD hash.
// mapFetchError converts common fetch errors to typed sentinels.
func mapFetchError(err error) error {
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

// createMergeCommit builds a merge commit with the given parents using the
// remote tree, stores it, and updates the local branch ref.
// (ponytail: this is a simplified merge that uses the remote tree as the
// merge result. A full 3-way merge would require resolving conflicts
// between local, remote, and merge-base trees. The upgrade path is to use
// go-git's merge functionality or an external merge driver.)
func (g *gitStore) createMergeCommit(branch string, localHash, remoteHash plumbing.Hash) (plumbing.Hash, error) {
	remoteCommit, err := g.repo.CommitObject(remoteHash)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("get remote commit: %w", err)
	}
	mergeTree, err := remoteCommit.Tree()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("get remote tree: %w", err)
	}

	mergeCommit := &object.Commit{
		Author: object.Signature{
			Name:  "cartographer",
			Email: "cartographer@foundry.flow",
		},
		Committer: object.Signature{
			Name:  "cartographer",
			Email: "cartographer@foundry.flow",
		},
		Message:      "merge: sync from remote " + g.remoteURL,
		TreeHash:     mergeTree.Hash,
		ParentHashes: []plumbing.Hash{localHash, remoteHash},
	}

	obj := g.backend.NewEncodedObject()
	if err := mergeCommit.EncodeWithoutSignature(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("encode merge commit: %w", err)
	}
	mergeHash, err := g.backend.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("store merge commit: %w", err)
	}

	newRef := plumbing.NewHashReference(
		plumbing.ReferenceName("refs/heads/"+branch),
		mergeHash,
	)
	if err := g.backend.SetReference(newRef); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("set merge ref: %w", err)
	}
	if err := g.wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.ReferenceName("refs/heads/" + branch),
		Force:  true,
	}); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("checkout after merge: %w", err)
	}
	return mergeHash, nil
}

// setLocalRefAndCheckout sets the local branch ref to the given hash and
// checks out the branch.
func (g *gitStore) setLocalRefAndCheckout(branch string, hash plumbing.Hash) error {
	newRef := plumbing.NewHashReference(
		plumbing.ReferenceName("refs/heads/"+branch),
		hash,
	)
	if err := g.backend.SetReference(newRef); err != nil {
		return fmt.Errorf("set ref: %w", err)
	}
	return g.wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.ReferenceName("refs/heads/" + branch),
		Force:  true,
	})
}

func (g *gitStore) FetchAndMerge(ctx context.Context, remoteName, branch string) (plumbing.Hash, error) {
	if g.remoteURL == "" {
		return plumbing.ZeroHash, ErrNoRemote
	}
	if g.authFn == nil {
		return plumbing.ZeroHash, ErrAuthConfigMissing
	}

	if err := g.ensureRemoteExists(); err != nil {
		return plumbing.ZeroHash, err
	}

	auth, err := g.resolveAuth(true)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	err = g.repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: remoteName,
		Auth:       auth,
		Force:      false,
		RefSpecs:   []config.RefSpec{config.RefSpec("+refs/heads/main:refs/remotes/" + remoteName + "/main")},
	})
	if err != nil {
		if errors.Is(err, git.NoErrAlreadyUpToDate) {
			ref, refErr := g.repo.Reference(plumbing.ReferenceName("refs/heads/"+branch), true)
			if refErr != nil {
				return plumbing.ZeroHash, fmt.Errorf("resolve local ref: %w", refErr)
			}
			return ref.Hash(), nil
		}
		return plumbing.ZeroHash, mapFetchError(err)
	}

	remoteRef, err := g.repo.Reference(
		plumbing.ReferenceName("refs/remotes/"+remoteName+"/"+branch), true)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("resolve remote ref: %w", err)
	}

	localRef, err := g.repo.Reference(
		plumbing.ReferenceName("refs/heads/"+branch), true)
	if err != nil {
		if err == plumbing.ErrReferenceNotFound {
			if err := g.setLocalRefAndCheckout(branch, remoteRef.Hash()); err != nil {
				return plumbing.ZeroHash, err
			}
			return remoteRef.Hash(), nil
		}
		return plumbing.ZeroHash, fmt.Errorf("resolve local ref: %w", err)
	}

	localHash := localRef.Hash()
	remoteHash := remoteRef.Hash()

	if localHash == remoteHash {
		return localHash, nil
	}

	localCommit, err := g.repo.CommitObject(localHash)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("get local commit: %w", err)
	}
	remoteCommit, err := g.repo.CommitObject(remoteHash)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("get remote commit: %w", err)
	}

	isAncestor, err := localCommit.IsAncestor(remoteCommit)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("check ancestor: %w", err)
	}
	if isAncestor {
		if err := g.setLocalRefAndCheckout(branch, remoteHash); err != nil {
			return plumbing.ZeroHash, err
		}
		return remoteHash, nil
	}

	return g.createMergeCommit(branch, localHash, remoteHash)
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

	auth, err := g.resolveAuth(false)
	if err != nil {
		return err
	}

	err = g.repo.PushContext(ctx, &git.PushOptions{
		RemoteName: "origin",
		Auth:       auth,
		Force:      false,
		RefSpecs:   []config.RefSpec{"refs/heads/main:refs/heads/main"},
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
// ErrAuthConfigMissing if no auth provider is configured, and typed errors for
// auth, network, and divergence failures. A configured provider may return nil
// for an anonymous public remote.
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

	auth, err := g.resolveAuth(true)
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

// resolveAuth calls the configured auth provider and returns typed errors.
// allowAnonymous only permits an explicit nil result, not a missing provider.
func (g *gitStore) resolveAuth(allowAnonymous bool) (transport.AuthMethod, error) {
	auth, err := g.authFn()
	if err != nil {
		if errors.Is(err, ErrAuthConfigMissing) {
			return nil, err
		}
		return nil, ErrRemoteAuthResolutionFailed
	}
	if auth == nil && !allowAnonymous {
		return nil, ErrAuthConfigMissing
	}
	return auth, nil
}
