package gitstore

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

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
