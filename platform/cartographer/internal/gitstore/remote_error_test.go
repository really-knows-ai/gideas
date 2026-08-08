package gitstore

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

const testRemoteURL = "https://example.com/repo.git"

// TestClassifyRemoteError locks the type-based remote error classification:
// outcomes are assigned by typed sentinels (go-git's transit sentinels and the
// standard library's net error types), never by matching library error text.
func TestClassifyRemoteError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "auth required (typed)",
			err:  transport.ErrAuthenticationRequired,
			want: ErrAuthFailed,
		},
		{
			name: "authorization failed (typed)",
			err:  transport.ErrAuthorizationFailed,
			want: ErrAuthFailed,
		},
		{
			name: "invalid auth method (typed)",
			err:  transport.ErrInvalidAuthMethod,
			want: ErrAuthFailed,
		},
		{
			name: "dns resolution failure",
			err:  &net.DNSError{Err: "no such host", Name: "example.com"},
			want: ErrRemoteUnreachable,
		},
		{
			name: "connection refused (op error)",
			err:  &net.OpError{Op: "dial", Err: errors.New("connection refused")},
			want: ErrRemoteUnreachable,
		},
		{
			name: "transport timeout via net.Error",
			err:  &fakeNetTimeout{err: errors.New("i/o timeout")},
			want: ErrRemoteUnreachable,
		},
		{
			name: "unrelated error is not classified",
			err:  errors.New("some other failure"),
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyRemoteError(tt.err)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("classifyRemoteError(%T) = %v, want nil", tt.err, got)
				}
				return
			}
			if !errors.Is(got, tt.want) {
				t.Fatalf("classifyRemoteError(%T) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestMapFetchErrorPreservesPublicSentinels returns mapFetchError keeps the
// existing public mapping: up-to-date -> nil, auth -> ErrAuthFailed,
// unreachable -> ErrRemoteUnreachable, and anything else -> wrapped error.
func TestMapFetchError(t *testing.T) {
	if got := mapFetchError(git.NoErrAlreadyUpToDate); got != nil {
		t.Fatalf("mapFetchError(up-to-date) = %v, want nil", got)
	}
	if got := mapFetchError(transport.ErrAuthenticationRequired); !errors.Is(got, ErrAuthFailed) {
		t.Fatalf("mapFetchError(auth) = %v, want ErrAuthFailed", got)
	}
	if got := mapFetchError(&net.DNSError{Err: "no such host", Name: "x"}); !errors.Is(got, ErrRemoteUnreachable) {
		t.Fatalf("mapFetchError(unreachable) = %v, want ErrRemoteUnreachable", got)
	}
	boom := errors.New("boom")
	got := mapFetchError(boom)
	if got == nil || errors.Is(got, ErrAuthFailed) || errors.Is(got, ErrRemoteUnreachable) {
		t.Fatalf("mapFetchError(other) = %v, want a generic fetch-wrapped error", got)
	}
	if !errors.Is(got, boom) {
		t.Fatalf("mapFetchError(other) = %v, want the original error to be reachable via Unwrap", got)
	}
}

// TestFetchAndMergeAuthConfigMissing pins FetchAndMerge's auth contract: the
// SPEC error-table row "Remote auth config missing (Sync)" applies only when
// the operation cannot be attempted. A URL that demands credentials with a nil
// authFn returns ErrAuthConfigMissing via the requiresAuth pre-flight; a
// public remote (no credentials demanded) is pulled anonymously — a nil
// authFn must never produce ErrAuthConfigMissing. The divergent path is
// covered by TestFetchAndMerge_Diverged.
func TestFetchAndMergeAuthConfigMissing(t *testing.T) {
	t.Run("credentials-requiring URL with nil authFn", func(t *testing.T) {
		gs := setupTestStore(t)
		err := gs.WithGitLock(func() error {
			// URL embeds a user (demands credentials) but no authFn is
			// configured → pre-flight ErrAuthConfigMissing, no network touched.
			gs.remoteURL = "https://user@example.com/repo.git"
			_, err := gs.FetchAndMerge(ctx(), "origin", "main")
			if !errors.Is(err, ErrAuthConfigMissing) {
				return fmt.Errorf("expected ErrAuthConfigMissing, got %v", err)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("TestFetchAndMergeAuthConfigMissing[credentials-requiring]: %v", err)
		}
	})

	t.Run("plain public https URL with nil authFn proceeds", func(t *testing.T) {
		gs := setupTestStore(t)
		err := gs.WithGitLock(func() error {
			// No credentials demanded (no user in the https URL): the fetch is
			// attempted with nil auth, so the failure is a transport error
			// (port 1 on loopback is connection-refused — no network), never
			// the mislabelled auth-config sentinel.
			gs.remoteURL = "https://127.0.0.1:1/repo.git"
			_, err := gs.FetchAndMerge(ctx(), "origin", "main")
			if err == nil {
				return fmt.Errorf("expected fetch failure against unreachable host")
			}
			if errors.Is(err, ErrAuthConfigMissing) {
				return fmt.Errorf("expected fetch attempt, got ErrAuthConfigMissing")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("TestFetchAndMergeAuthConfigMissing[plain-https]: %v", err)
		}
	})

	t.Run("public remote with nil authFn pulls anonymously", func(t *testing.T) {
		tmpDir := t.TempDir()
		bareDir := filepath.Join(tmpDir, "remote.git")
		setupBareRemote(t, tmpDir, bareDir)
		gs := cloneFromBare(t, tmpDir, bareDir)

		err := gs.WithGitLock(func() error {
			gs.remoteURL = "file://" + bareDir
			gs.authFn = nil
			newHash, err := gs.FetchAndMerge(ctx(), "origin", "main")
			if err != nil {
				return fmt.Errorf("anonymous FetchAndMerge: %w", err)
			}
			if want := remoteHEAD(t, bareDir); newHash != want {
				return fmt.Errorf("FetchAndMerge returned %s, want remote HEAD %s", newHash, want)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("TestFetchAndMergeAuthConfigMissing[anonymous-pull]: %v", err)
		}
	})
}

// TestFetchAndMergeAuthFnFailureCollapsesToAuthConfigMissing verifies that
// FetchAndMerge surfaces ErrAuthConfigMissing when the configured authFn
// returns a generic (non-sentinel) error — the readSecretFn-failure (Secret
// missing) and invalid-credential (unparseable ssh-privatekey PEM) sub-cases
// of the SPEC error-table row "Remote auth config missing (Sync)",
// which mandates FAILED_PRECONDITION for "Secret missing, invalid, or missing
// expected key for the URL scheme". This is distinct from ErrAuthConfigMissing
// (nil authFn), ErrAuthFailed (typed transport sentinel from the server), and
// the missing-expected-key sub-case (pinned by
// TestFetchAndMergeAuthConfigMissingFromFn).
func TestFetchAndMergeAuthFnFailureCollapsesToAuthConfigMissing(t *testing.T) {
	gs := setupTestStore(t)
	for _, authErr := range []error{
		fmt.Errorf("secrets: secret %q not found", "cartographer-remote-auth"),
		fmt.Errorf("ssh: parse private key: asn1: structure error"),
	} {
		gs.authFn = func() (transport.AuthMethod, error) {
			return nil, authErr
		}
		err := gs.WithGitLock(func() error {
			gs.remoteURL = testRemoteURL
			_, err := gs.FetchAndMerge(ctx(), "origin", "main")
			if !errors.Is(err, ErrAuthConfigMissing) {
				return fmt.Errorf("expected ErrAuthConfigMissing, got %v", err)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("TestFetchAndMergeAuthFnFailureCollapsesToAuthConfigMissing: %v", err)
		}
	}
}

// TestFetchAndMergeAuthConfigMissingFromFn verifies that FetchAndMerge preserves
// the ErrAuthConfigMissing sentinel returned by the authFn itself (resolveAuth)
// — the missing-expected-key sub-case of the SPEC error-table row "Remote auth
// config missing (Sync)".
func TestFetchAndMergeAuthConfigMissingFromFn(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		gs.remoteURL = testRemoteURL
		gs.authFn = func() (transport.AuthMethod, error) {
			return nil, ErrAuthConfigMissing
		}
		_, err := gs.FetchAndMerge(ctx(), "origin", "main")
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestFetchAndMergeAuthConfigMissingFromFn: %v", err)
	}
}

// TestResolveAuthBranches pins the full construction of resolveAuth's branches
// (per the git-remote-auth resolver learning): every authFn failure — a
// readSecretFn error (Secret missing), an invalid credential (unparseable
// ssh-privatekey PEM), or the ErrAuthConfigMissing sentinel (missing expected
// key) — collapses to ErrAuthConfigMissing so mapGitError returns
// FAILED_PRECONDITION (SPEC error-table row "Remote auth config missing
// (Sync)"). The success branches return the resolved auth unchanged, nil for
// a nil authFn (no auth configured), and nil for an explicit anonymous
// selection — the caller's requiresAuth pre-flight decides whether the remote
// can be accessed anonymously.
func TestResolveAuthBranches(t *testing.T) {
	gs := setupTestStore(t)

	// Error branches: all authFn failures collapse to ErrAuthConfigMissing.
	for _, authErr := range []error{
		fmt.Errorf("secrets: secret not found"),
		fmt.Errorf("ssh: parse private key: asn1: structure error"),
		ErrAuthConfigMissing,
	} {
		gs.authFn = func() (transport.AuthMethod, error) {
			return nil, authErr
		}
		auth, err := gs.resolveAuth()
		if !errors.Is(err, ErrAuthConfigMissing) {
			t.Fatalf("resolveAuth(authFn err %v) = %v, want ErrAuthConfigMissing", authErr, err)
		}
		if auth != nil {
			t.Fatalf("resolveAuth(authFn err %v) returned auth %v, want nil", authErr, auth)
		}
	}

	// Success branch: a valid auth is returned unchanged.
	want := stubAuth{}
	gs.authFn = func() (transport.AuthMethod, error) {
		return want, nil
	}
	auth, err := gs.resolveAuth()
	if err != nil {
		t.Fatalf("resolveAuth(valid auth) error = %v, want nil", err)
	}
	if auth != want {
		t.Fatalf("resolveAuth(valid auth) = %v, want %v", auth, want)
	}

	// Success branch: an explicit nil auth is anonymous access.
	gs.authFn = func() (transport.AuthMethod, error) {
		return nil, nil
	}
	auth, err = gs.resolveAuth()
	if err != nil || auth != nil {
		t.Fatalf("resolveAuth(nil auth) = (%v, %v), want (nil, nil)", auth, err)
	}

	// Success branch: a nil authFn (auth not configured) selects anonymous
	// access; the requiresAuth pre-flight guards at the call site.
	gs.authFn = nil
	auth, err = gs.resolveAuth()
	if err != nil || auth != nil {
		t.Fatalf("resolveAuth(nil authFn) = (%v, %v), want (nil, nil)", auth, err)
	}
}

// stubAuth is a minimal transport.AuthMethod used to drive resolveAuth's
// success branch.
type stubAuth struct{}

func (stubAuth) Name() string   { return "stub" }
func (stubAuth) String() string { return "stub-auth" }

// fakeNetTimeout implements net.Error with Timeout()==true to drive the
// typed timeout branch of isRemoteUnreachable.
type fakeNetTimeout struct {
	err error
}

func (e *fakeNetTimeout) Error() string {
	return e.err.Error()
}

func (e *fakeNetTimeout) Unwrap() error {
	return e.err
}

func (e *fakeNetTimeout) Timeout() bool {
	return true
}

func (e *fakeNetTimeout) Temporary() bool {
	return false
}
