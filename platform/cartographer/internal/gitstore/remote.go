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
// Validates the URL scheme (must be https://, ssh://, or file://) and returns
// ErrUnsupportedURLScheme otherwise. authFn is called on each remote operation
// so credentials can be refreshed on every call. A configured authFn returning
// nil credentials explicitly selects anonymous access for clone, pull, and
// push; a nil authFn means auth was not configured.
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

// validateRemoteURL rejects URLs that are not https://, ssh://, or file:// and
// any clearly malformed URL with no location component (e.g. "https://" or
// "file://"). file:// URLs carry the repository location in the path rather
// than the host, so the no-location check is scheme-aware.
func validateRemoteURL(rawURL string) error {
	if !strings.HasPrefix(rawURL, "https://") &&
		!strings.HasPrefix(rawURL, "ssh://") &&
		!strings.HasPrefix(rawURL, "file://") {
		return ErrUnsupportedURLScheme
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse remote URL: %w", err)
	}
	if parsed.Scheme == "file" {
		if parsed.Path == "" {
			return fmt.Errorf("%w: %q", ErrRemoteURLNoHost, rawURL)
		}
		return nil
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

// FetchAndMerge is the sync worker's pull: the worker's fetch-merge-re-hydrate
// cycle (SPEC R10) calls it with branch "main" to bring local main up to date
// with the remote before re-hydrating main.lbug and pushing any pending commit.
// Sync() and BeginTransaction wake the worker, so FetchAndMerge has a single
// production caller — the worker's fetch attempt. SPEC mandates that when main
// has diverged from the remote, the pull fails with FAILED_PRECONDITION (SPEC
// R10, error-table row "Sync diverged"). On divergence this method returns
// ErrPullDiverged, which service mapGitError maps to FAILED_PRECONDITION; the
// local main ref is left unchanged (no merge commit is fabricated), so the
// SPEC-mandated divergence failure is reachable.
//
// The ancestry classification below is three-way: equal tips and fast-forward
// (the remote a descendant of local main) leave local main at the remote tip;
// local main strictly ahead of the remote (a committed-but-unpushed state
// awaiting the worker's push) is treated as up-to-date — there is nothing to
// pull; true divergence returns ErrPullDiverged. If the local branch does not
// exist it is created pointing at the remote tracking ref. Returns the
// resulting local HEAD hash.
//
// For any branch, the fetch refspec and the tracking-ref lookup both use the
// given branch, so the parameter is honored for non-main branches too.
func (g *gitStore) FetchAndMerge(ctx context.Context, remoteName, branch string) (plumbing.Hash, error) {
	if g.remoteURL == "" {
		return plumbing.ZeroHash, ErrNoRemote
	}
	// Pre-flight: only a remote that demands credentials (ssh:// or an https://
	// URL embedding a user) with no auth provider configured cannot be pulled.
	// A public remote is pulled anonymously with nil auth — the same policy as
	// CloneSingleBranch and PushRemote.
	if requiresAuth(g.remoteURL) && g.authFn == nil {
		return plumbing.ZeroHash, ErrAuthConfigMissing
	}

	if err := g.ensureRemoteExists(); err != nil {
		return plumbing.ZeroHash, err
	}

	auth, err := g.resolveAuth()
	if err != nil {
		return plumbing.ZeroHash, err
	}

	err = g.repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: remoteName,
		Auth:       auth,
		Force:      false,
		RefSpecs:   []config.RefSpec{config.RefSpec("+refs/heads/" + branch + ":refs/remotes/" + remoteName + "/" + branch)},
	})
	// NoErrAlreadyUpToDate means the tracking ref already matches the remote
	// tip, so the fetch was a no-op. This alone is not sufficient to report
	// success: after a divergent cycle the tracking ref advanced to the remote
	// tip while local main was left behind (ErrPullDiverged leaves the local
	// ref unchanged), so the next fetch is NoErrAlreadyUpToDate but the local
	// branch is still diverged. Returning the local hash here would silently
	// report the persistent divergence as up-to-date, hiding the SPEC-mandated
	// ErrPullDiverged ("Sync diverged", FAILED_PRECONDITION — SPEC R10,
	// error-table row "Sync diverged"). Fall through to the ancestry
	// classification below, which re-detects the divergence (and handles the
	// true up-to-date, fast-forward, and local-ahead cases identically).
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
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
	// sync worker's push-retry contract: a push that failed transiently
	// (retries exhausted, SPEC R10 leaves the flag set for the next cycle)
	// leaves local main ahead of the remote, and the next cycle's pull must
	// not fail — otherwise the retried push is skipped and the pending push
	// wedges the cycle.
	remoteIsAncestor, err := remoteCommit.IsAncestor(localCommit)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("check remote ancestor: %w", err)
	}
	if remoteIsAncestor {
		return localHash, nil
	}

	// Diverged: neither local nor remote is an ancestor of the other. The
	// pull must fail FAILED_PRECONDITION (SPEC R10, error-table row "Sync
	// diverged") rather than fabricate a merge commit on local main that masks
	// the divergence. mapGitError maps ErrPullDiverged to FAILED_PRECONDITION.
	return plumbing.ZeroHash, ErrPullDiverged
}

// PushRemote pushes the main branch to the remote origin.
// Returns ErrNoRemote if no remote is configured, ErrAuthConfigMissing when
// the operation cannot be attempted, and typed errors for auth, network, and
// rejection failures. Anonymous access mirrors CloneSingleBranch: a remote
// that does not demand credentials is pushed with nil auth — a nil authFn
// result from a configured provider is an explicit anonymous selection, and a
// nil authFn means auth was not configured (go-git surfaces
// ErrAuthenticationRequired/ErrAuthorizationFailed from the server, already
// mapped to ErrAuthFailed, if the remote actually demands credentials).
// ErrAuthConfigMissing remains for (a) a URL that demands credentials (ssh://
// or an https:// URL embedding a user) with no authFn configured, and (b) an
// authFn that errors (readSecretFn failure, invalid PEM, missing expected
// key) — in both cases the push cannot be attempted.
func (g *gitStore) PushRemote(ctx context.Context) error {
	if g.remoteURL == "" {
		return ErrNoRemote
	}
	// Pre-flight: only a remote that demands credentials (ssh:// or an https://
	// URL embedding a user) with no auth provider configured cannot be pushed.
	// A public remote is pushed anonymously — mirroring CloneSingleBranch's
	// pre-flight — so a remote configured without a secretRef (the production
	// buildResolveAuthFn closure in cmd/main.go returns nil, nil) pushes just
	// like it clones and pulls.
	if requiresAuth(g.remoteURL) && g.authFn == nil {
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

// CloneSingleBranch fetches a single branch from a remote URL into the
// existing repository. The rawURL parameter is passed explicitly (not from
// g.remoteURL). After fetching, the local main ref is set to the fetched
// commit and the working tree is checked out to main.
//
// Caller-state dependency: an already-existing "origin" remote is tolerated
// (CreateRemote's ErrRemoteExists) and the fetch then goes through the
// pre-existing origin, whose configured URL — not rawURL — determines where
// data is fetched from. A stale origin URL therefore silently wins over
// rawURL. Callers must ensure origin is configured to match rawURL before
// calling (e.g. via SetRemote, which runs ensureRemoteExists). Production
// wiring (cmd/main.go: SetRemote before tryRemotePullOnInit) does this, so
// the stale-origin path is unreachable today; the dependency is documented
// rather than left to silently produce a wrong result.
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
	// remotes — the same policy as FetchAndMerge and PushRemote.
	var auth transport.AuthMethod
	var err error
	if g.authFn != nil {
		auth, err = g.authFn()
		if err != nil {
			// Any authFn failure — a readSecretFn error (the referenced Secret
			// does not exist), an invalid credential (an unparseable
			// ssh-privatekey PEM), or the ErrAuthConfigMissing sentinel
			// (missing expected key) — means the git operation cannot be
			// attempted. CloneSingleBranch is the clone-on-init path, so all of
			// them surface as ErrAuthConfigMissing for mapGitError to return
			// FAILED_PRECONDITION (SPEC error-table row "Remote auth config
			// missing (Sync)").
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
// A nil authFn (no auth configured) and a configured authFn returning a nil
// result (explicit anonymous selection) both select anonymous access; the
// caller's requiresAuth pre-flight decides whether the remote can be accessed
// that way. Any authFn failure — a readSecretFn error (the referenced Secret
// does not exist), an invalid credential (an unparseable ssh-privatekey PEM),
// or the ErrAuthConfigMissing sentinel (missing expected key for the URL
// scheme) — means the git operation cannot be attempted, so all of them
// surface as ErrAuthConfigMissing for mapGitError to return
// FAILED_PRECONDITION (SPEC error-table row "Remote auth config missing").
func (g *gitStore) resolveAuth() (transport.AuthMethod, error) {
	if g.authFn == nil {
		return nil, nil
	}
	auth, err := g.authFn()
	if err != nil {
		return nil, ErrAuthConfigMissing
	}
	return auth, nil
}
