package gitstore

import (
	"errors"
	"fmt"
	"net"
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

// TestFetchAndMergeAuthConfigMissing verifies that FetchAndMerge with a nil
// authFn returns ErrAuthConfigMissing — the SPEC error-table row "Remote auth
// config missing (Sync)". The divergent path is covered by
// TestFetchAndMerge_Diverged; this pins the pre-fetch auth-guard branch.
func TestFetchAndMergeAuthConfigMissing(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Configure remote but leave authFn nil
		gs.remoteURL = testRemoteURL
		_, err := gs.FetchAndMerge(ctx(), "origin", "main")
		if !errors.Is(err, ErrAuthConfigMissing) {
			return fmt.Errorf("expected ErrAuthConfigMissing, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestFetchAndMergeAuthConfigMissing: %v", err)
	}
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
// (Sync)"). The success branches return the resolved auth unchanged,
// and nil for an explicit anonymous public remote when allowed.
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
		auth, err := gs.resolveAuth(true)
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
	auth, err := gs.resolveAuth(true)
	if err != nil {
		t.Fatalf("resolveAuth(valid auth) error = %v, want nil", err)
	}
	if auth != want {
		t.Fatalf("resolveAuth(valid auth) = %v, want %v", auth, want)
	}

	// Success branch: an explicit nil auth is anonymous access, permitted only
	// with allowAnonymous=true.
	gs.authFn = func() (transport.AuthMethod, error) {
		return nil, nil
	}
	auth, err = gs.resolveAuth(true)
	if err != nil || auth != nil {
		t.Fatalf("resolveAuth(nil auth, allowAnonymous=true) = (%v, %v), want (nil, nil)", auth, err)
	}
	if _, err := gs.resolveAuth(false); !errors.Is(err, ErrAuthConfigMissing) {
		t.Fatalf("resolveAuth(nil auth, allowAnonymous=false) = %v, want ErrAuthConfigMissing", err)
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
