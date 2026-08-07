package gitstore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
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
func (g *gitStore) SetRemote(ctx context.Context, rawURL string, authFn func() (transport.AuthMethod, error)) error {
	if err := validateRemoteURL(rawURL); err != nil {
		return err
	}
	g.remoteURL = rawURL
	g.authFn = authFn

	if err := g.ensureRemoteExists(); err != nil {
		return err
	}
	return nil
}

// validateRemoteURL rejects URLs that are not https:// or ssh:// and any
// clearly malformed URL with no host component (e.g. "https://").
func validateRemoteURL(rawURL string) error {
	if !strings.HasPrefix(rawURL, "https://") && !strings.HasPrefix(rawURL, "ssh://") {
		return ErrUnsupportedURLScheme
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse remote URL: %w", err)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%w: %q", ErrRemoteURLNoHost, rawURL)
	}
	return nil
}

// requiresAuth reports whether the given remote URL requires credentials.
// ssh:// always requires an SSH key. https:// requires credentials only when
// it embeds a user (basic auth); a plain https URL may be a public remote.
func requiresAuth(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if parsed.Scheme == "ssh" {
		return true
	}
	return parsed.User != nil
}

// FetchRemote fetches the main branch from the remote origin.
// Returns ErrNoRemote if no remote is configured, ErrAuthConfigMissing
// if no auth provider is configured, and typed errors for auth and network
// failures. A configured provider may return nil for an anonymous public remote.
//
// ponytail: no production caller invokes this method — the service layer pulls
// via FetchAndMerge, never FetchRemote. It is retained only because it is a
// member of the GitStore interface (and is exercised by the interface-level
// tests). If the interface is ever trimmed, this implementation and its tests
// should be removed together.
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
		RefSpecs:   []config.RefSpec{config.RefSpec("+refs/heads/main:refs/remotes/origin/main")},
	})
	if err != nil {
		return mapFetchError(err)
	}
	return nil
}

// FetchAndMerge fetches from the remote and merges the remote tracking branch
// into the local branch. If the local branch does not exist, it is created
// pointing at the remote tracking ref. If fast-forward is possible, the local
// ref is advanced. If the local branch is strictly ahead of the remote, it is
// left unchanged (treated as up-to-date). On true divergence ErrPullDiverged
// is returned and local main is left unchanged — no merge commit is
// fabricated. Returns the new HEAD hash.
// mapFetchError converts common fetch errors to typed package sentinels.
// Classification is based on typed errors (go-git sentinels and the standard
// library's net error types) — never on matching library error message text,
// which is a message, not a contract.
func mapFetchError(err error) error {
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	if err := classifyRemoteError(err); err != nil {
		return err
	}
	return fmt.Errorf("fetch: %w", err)
}

// classifyRemoteError returns a typed package sentinel for the given remote
// operation error, or nil if it is not one of the recognised failure classes.
func classifyRemoteError(err error) error {
	switch {
	// go-git's HTTP/Smart transport classifies 401/403/`auth-required` as
	// typed errors; SSLFailure and invalid auth surfaces as ErrInvalidAuthMethod.
	case errors.Is(err, transport.ErrAuthenticationRequired),
		errors.Is(err, transport.ErrAuthorizationFailed),
		errors.Is(err, transport.ErrInvalidAuthMethod):
		return ErrAuthFailed
	case isRemoteUnreachable(err):
		return ErrRemoteUnreachable
	default:
		return nil
	}
}

// isRemoteUnreachable reports whether err indicates the remote endpoint could
// not be reached. It uses the typed net error types from the standard library
// (DNS resolution failure, connection refused, or a transport timeout) rather
// than matching the underlying library's message text.
func isRemoteUnreachable(err error) bool {
	var dnsErr *net.DNSError
	var opErr *net.OpError
	var netErr net.Error
	return errors.As(err, &dnsErr) ||
		errors.As(err, &opErr) ||
		(errors.As(err, &netErr) && netErr.Timeout())
}

// setLocalRefAndCheckout sets the local branch ref to the given hash and
// checks out the branch. After the forced checkout, untracked files are
// cleaned to ensure the working tree exactly matches the target commit —
// without this, stale untracked files from a previously wiped type would
// persist after a pull-after-wipe fast-forward (matching HardResetToBranch's
// cleanup pattern).
func (g *gitStore) setLocalRefAndCheckout(branch string, hash plumbing.Hash) error {
	newRef := plumbing.NewHashReference(
		plumbing.ReferenceName("refs/heads/"+branch),
		hash,
	)
	if err := g.backend.SetReference(newRef); err != nil {
		return fmt.Errorf("set ref: %w", err)
	}
	if err := g.wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.ReferenceName("refs/heads/" + branch),
		Force:  true,
	}); err != nil {
		return fmt.Errorf("checkout %s: %w", branch, err)
	}
	if err := g.wt.Clean(&git.CleanOptions{Dir: true}); err != nil {
		return fmt.Errorf("clean %s: %w", branch, err)
	}
	return nil
}

// FetchAndMerge is used on two distinct paths, distinguished by which branch
// they pass:
//
//   - Explicit pull (branch "main"): SPEC R10 mandates that when main has
//     diverged from the remote, PullFromRemote fails with FAILED_PRECONDITION
//     ("Remote pull diverged" — error table row "Remote pull diverged", SPEC
//     R10, line 929). On divergence this method returns ErrPullDiverged, which
//     service mapGitError maps to FAILED_PRECONDITION; the local main ref is
//     left unchanged (no merge commit is fabricated), so the SPEC-mandated
//     divergence failure is reachable.
//   - Commit step 14 (pull-before-push, branch "main"): the fire-and-forget
//     push path needs the local branch to fast-forward onto the remote so the
//     subsequent push is fast-forward. It only ever fast-forwards (or is
//     already up-to-date), so in the normal case the fetch+ancestor-check
//     below advances local main and the push succeeds. A local-ahead state
//     (remote strictly behind local) is treated as up-to-date — there is
//     nothing to pull — so a failed fire-and-forget push leaves the retry
//     path open on the next commit (SPEC:788). On true divergence it
//     receives ErrPullDiverged and simply skips that push (logged +
//     telemetry) rather than fabricating a merge commit on local main that
//     mixes a peer's commits into the published timeline. The commit itself is
//     unaffected — the push is fire-and-forget behind an already-returned
//     CommitTransaction success. (ponytail: the fire-and-forget push therefore
//     silently drops a divergent pull; it is only logged/telemetry, matching a
//     rejected push. Upgrade path: retry the push itself rather than a
//     pre-merge when it fails non-fast-forward.)
//
// For any branch, the fetch refspec and the tracking-ref lookup both use the
// given branch, so the parameter is honored for non-main branches too.
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
		RefSpecs:   []config.RefSpec{config.RefSpec("+refs/heads/" + branch + ":refs/remotes/" + remoteName + "/" + branch)},
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

	// Local is ahead of the remote (remote strictly behind): the remote tip is
	// an ancestor of the local commit. There is nothing to pull, so this is
	// treated as up-to-date rather than ErrPullDiverged. This matters for the
	// Commit step-14 pull-before-push: a fire-and-forget push that failed
	// transiently leaves local main ahead of the remote, and the next commit's
	// pull must not fail (which would skip the retried push, defeating SPEC:788
	// "the next commit will retry the push").
	remoteIsAncestor, err := remoteCommit.IsAncestor(localCommit)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("check remote ancestor: %w", err)
	}
	if remoteIsAncestor {
		return localHash, nil
	}

	// Diverged: neither local nor remote is an ancestor of the other. The
	// explicit pull path must fail FAILED_PRECONDITION (SPEC R10, error-table
	// row "Remote pull diverged", line 929) rather than fabricate a merge
	// commit on local main that masks the divergence. mapGitError maps
	// ErrPullDiverged to FAILED_PRECONDITION.
	return plumbing.ZeroHash, ErrPullDiverged
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
		// ponytail: go-git surfaces a server-side non-fast-forward rejection as an
		// untyped text error (from checkFastForwardUpdate / the receive-pack report
		// status), not wrapped in git.ErrNonFastForwardUpdate. The only typed signal
		// this code can detect without grepping library text is that sentinel, so
		// ErrPushRejected is produced only when go-git does wrap it. Consequence: a
		// genuine rejected push may surface as a generic "push:" error instead of
		// ErrPushRejected. This is acceptable in production because the commit
		// pull-before-push path (FetchAndMerge) enforces a fast-forward before
		// pushing, so a rejected push is effectively unreachable via production
		// flows. Upgrade path: wrap the push result in a typed sentinel at the
		// point the pull-before-push checks divergence, or patch go-git to return a
		// typed error for non-fast-forward pushes.
		if errors.Is(err, git.ErrNonFastForwardUpdate) {
			return ErrPushRejected
		}
		if err := classifyRemoteError(err); err != nil {
			return err
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
//
// ponytail: no production caller invokes this method — the service pulls via
// FetchAndMerge, and Commit's pull-before-push also uses FetchAndMerge. The
// divergence sentinel it produces (ErrPullDiverged) is also returned by
// FetchAndMerge on the explicit-pull divergence path, so this method and the
// interface member are retained only for the tests; if the interface is ever
// trimmed it and its tests should be removed together.
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
		if errors.Is(err, git.ErrNonFastForwardUpdate) {
			return ErrPullDiverged
		}
		return mapFetchError(err)
	}
	return nil
}

// HasRemote returns whether the remote "origin" is configured.
// Returns an error if the check itself fails (I/O error, storage corruption).
//
// ponytail: no production caller invokes this method — the service layer
// determines remote presence by comparing s.remoteURL != "" before calling the
// git operations. It is retained only because it is a member of the GitStore
// interface (and is exercised by the interface-level tests). If the interface
// is ever trimmed, this implementation and its tests should be removed
// together.
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
// existing repository. The rawURL parameter is passed explicitly (not from
// g.remoteURL). After fetching, the local main ref is set to the fetched
// commit and the working tree is checked out to main.
func (g *gitStore) CloneSingleBranch(ctx context.Context, rawURL, branch string) error {
	if err := validateRemoteURL(rawURL); err != nil {
		return err
	}

	// Pre-flight: an authenticated URL requires an auth provider. A nil authFn
	// means auth was not configured, so an authenticated remote cannot be
	// cloned — fail before attempting the fetch rather than surfacing a
	// runtime auth error.
	if requiresAuth(rawURL) && g.authFn == nil {
		return ErrAuthConfigMissing
	}

	// Resolve auth: nil authFn or nil return means anonymous access to public
	// remotes.  Unlike FetchRemote/PushRemote/PullAndFastForward, this path
	// explicitly allows anonymous clone for the initial remote bootstrap.
	var auth transport.AuthMethod
	var err error
	if g.authFn != nil {
		auth, err = g.authFn()
		if err != nil {
			// Any authFn failure — a readSecretFn error (the referenced Secret
			// does not exist), an invalid credential (an unparseable
			// ssh-privatekey PEM), or the ErrAuthConfigMissing sentinel
			// (missing expected key) — means the git operation cannot be
			// attempted. CloneSingleBranch is the PullFromRemote empty-repo
			// path, so all of them surface as ErrAuthConfigMissing for
			// mapGitError to return FAILED_PRECONDITION (SPEC error-table row
			// "Remote auth config missing (PullFromRemote)").
			return ErrAuthConfigMissing
		}
	}
	// nil auth is OK for anonymous public remotes

	// Ensure remote exists
	_, err = g.repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{rawURL}})
	if err != nil && !errors.Is(err, git.ErrRemoteExists) {
		return fmt.Errorf("create remote: %w", err)
	}

	// Fetch from remote (single-branch: limit to the requested branch)
	err = g.repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		Auth:       auth,
		Force:      false,
		RefSpecs:   []config.RefSpec{config.RefSpec("+refs/heads/" + branch + ":refs/remotes/origin/" + branch)},
	})
	if err != nil {
		if errors.Is(err, git.NoErrAlreadyUpToDate) {
			// Already up-to-date is fine; continue to set main ref
		} else {
			return mapFetchError(err)
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
	repoPath := filepath.Join(g.basePath, "graph-repo")
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
	g.backend = reopened.Storer

	// Ensure entities/ and edges/ directories exist in the working tree.
	// The remote repository may not contain these directories, but downstream
	// re-hydration (WriteEntityFiles, WriteEdgeFiles) requires them.
	if err := g.fs.MkdirAll("entities", 0755); err != nil {
		return fmt.Errorf("create entities dir: %w", err)
	}
	if err := g.fs.MkdirAll("edges", 0755); err != nil {
		return fmt.Errorf("create edges dir: %w", err)
	}

	return nil
}

// IsEmpty returns true if the repository has no graph-data commits on main.
// A repo with only the init commit (produced by New()) is considered empty.
// The check verifies both the commit message ("init") and the author
// (cartographer) to distinguish New()'s init commit from a cloned remote
// whose initial commit coincidentally uses the same message — without this,
// a repo whose only commit is "init" from another remote would be misread
// as empty, breaking the SPEC R10 clone-vs-pull decision.
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
		if !isInitCommit(commit) {
			// Found a non-init commit
			return errHasData
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errHasData) {
			return false, nil
		}
		return false, fmt.Errorf("iterate log: %w", err)
	}

	// Only init commit(s) found
	return true, nil
}

// isInitCommit reports whether a commit was produced by New()'s initial
// setup: message "init" authored by "cartographer <cartographer@foundry.flow>".
// A commit with message "init" from a different author (e.g. a cloned remote
// whose initial commit coincidentally used the same message) is not
// considered an init commit — the repo has real data and must not be treated
// as empty for the SPEC R10 clone-vs-pull decision.
func isInitCommit(commit *object.Commit) bool {
	return commit.Message == "init" &&
		commit.Author.Name == "cartographer" &&
		commit.Author.Email == "cartographer@foundry.flow"
}

// ensureRemoteExists creates the "origin" remote if it does not already exist,
// or updates its URL if it has changed.
func (g *gitStore) ensureRemoteExists() error {
	remote, err := g.repo.Remote("origin")
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
	// Remote exists; update URL if it has changed
	if len(remote.Config().URLs) == 0 || remote.Config().URLs[0] != g.remoteURL {
		// ponytail: this deletes and recreates the remote because go-git's
		// RemoteConfig is immutable after creation. The upgrade path is to
		// use a lower-level config API if performance becomes a concern.
		if err := g.repo.DeleteRemote("origin"); err != nil {
			return fmt.Errorf("delete remote: %w", err)
		}
		_, err = g.repo.CreateRemote(&config.RemoteConfig{
			Name: "origin",
			URLs: []string{g.remoteURL},
		})
		if err != nil {
			return fmt.Errorf("recreate remote: %w", err)
		}
	}
	return nil
}

// resolveAuth calls the configured auth provider and returns typed errors.
// allowAnonymous only permits an explicit nil result, not a missing provider.
// Any authFn failure — a readSecretFn error (the referenced Secret does not
// exist), an invalid credential (an unparseable ssh-privatekey PEM), or the
// ErrAuthConfigMissing sentinel (missing expected key for the URL scheme) —
// means the git operation cannot be attempted, so all of them surface as
// ErrAuthConfigMissing for mapGitError to return FAILED_PRECONDITION (SPEC
// error-table row "Remote auth config missing (PullFromRemote)").
func (g *gitStore) resolveAuth(allowAnonymous bool) (transport.AuthMethod, error) {
	auth, err := g.authFn()
	if err != nil {
		return nil, ErrAuthConfigMissing
	}
	if auth == nil && !allowAnonymous {
		return nil, ErrAuthConfigMissing
	}
	return auth, nil
}
