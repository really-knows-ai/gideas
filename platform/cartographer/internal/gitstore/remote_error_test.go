package gitstore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

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

// TestMapPushError pins every branch of PushRemote's error mapping
// (mapPushError): an already-up-to-date push is a no-op success (nil), a
// non-fast-forward rejection wrapped in go-git's typed sentinel maps to
// ErrPushRejected (SPEC error-table row "Push rejected (non-fast-forward)" →
// FAILED_PRECONDITION via mapGitError), auth and unreachable classes map
// through classifyRemoteError, and an unrelated error is wrapped generically
// with the original reachable via Unwrap. A regression that dropped or
// re-targeted any branch (e.g. mapping the rejection to a different sentinel)
// fails this test even though the branch is not reachable through a genuine
// push (go-git surfaces rejections as untyped text errors — see the ponytail).
func TestMapPushError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "up-to-date is a no-op success",
			err:  git.NoErrAlreadyUpToDate,
			want: nil,
		},
		{
			name: "non-fast-forward rejection maps to ErrPushRejected",
			err:  git.ErrNonFastForwardUpdate,
			want: ErrPushRejected,
		},
		{
			name: "auth failure maps to ErrAuthFailed",
			err:  transport.ErrAuthenticationRequired,
			want: ErrAuthFailed,
		},
		{
			name: "unreachable maps to ErrRemoteUnreachable",
			err:  &net.DNSError{Err: "no such host", Name: "example.com"},
			want: ErrRemoteUnreachable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapPushError(tt.err)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("mapPushError(%T) = %v, want nil", tt.err, got)
				}
				return
			}
			if !errors.Is(got, tt.want) {
				t.Fatalf("mapPushError(%T) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}

	// An unrelated error is neither a sentinel nor nil: it is wrapped in a
	// generic "push:" error that keeps the original reachable via Unwrap.
	boom := errors.New("boom")
	got := mapPushError(boom)
	if got == nil || errors.Is(got, ErrPushRejected) ||
		errors.Is(got, ErrAuthFailed) || errors.Is(got, ErrRemoteUnreachable) {
		t.Fatalf("mapPushError(other) = %v, want a generic push-wrapped error", got)
	}
	if !errors.Is(got, boom) {
		t.Fatalf("mapPushError(other) = %v, want the original error reachable via Unwrap", got)
	}
}

// TestRemoteContextCancellationAborts pins the deadline-abort branches of the
// gitstore's ctx-aware remote operations (remote.go): FetchAndMerge threads
// the caller's context into go-git's FetchContext and PushRemote into
// PushContext, so a cancelled or already-deadlined context must abort the
// operation with the mapped error instead of hanging or proceeding. The
// remote is a local HTTP server that never responds — net/http honours the
// request context deterministically (it returns ctx.Err() before the response
// can arrive), so the aborts are asserted end-to-end. A cancelled context
// surfaces context.Canceled through the generic wrap; an expired deadline
// surfaces as ErrRemoteUnreachable (context.DeadlineExceeded is a net.Error
// timeout). This is the gitstore-side contract behind SPEC R10 / SPEC:978
// ("a hung remote aborts the operation with DEADLINE_EXCEEDED"); the sync
// worker derives the per-operation deadline and maps the surfaced outcome.
func TestRemoteContextCancellationAborts(t *testing.T) {
	// A hung remote: the handler never responds, so only the client context
	// can end the round-trip.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	tests := []struct {
		name string
		ctx  func() context.Context
		want error
	}{
		{
			// A cancelled context surfaces its error through the generic wrap
			// ("fetch:"/"push:"), so the abort is observable end-to-end.
			name: "cancelled context surfaces context.Canceled",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			want: context.Canceled,
		},
		{
			// An expired deadline aborts the operation as a transport timeout:
			// context.DeadlineExceeded implements net.Error with Timeout()==true,
			// so classifyRemoteError maps the aborted operation to
			// ErrRemoteUnreachable at the gitstore layer. The SPEC "Git operation
			// deadline exceeded" DEADLINE_EXCEEDED status is produced by the sync
			// worker, which checks its own ctx.Err() == context.DeadlineExceeded
			// after the aborted operation (pinned by
			// TestSyncWorkerGitOpDeadline_HungPushAbortsWithDeadlineExceeded).
			name: "expired deadline aborts with ErrRemoteUnreachable",
			ctx: func() context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer cancel()
				<-ctx.Done() // deterministic: the expired deadline has fired
				return ctx
			},
			want: ErrRemoteUnreachable,
		},
	}
	for _, tt := range tests {
		t.Run("FetchAndMerge with "+tt.name+" context", func(t *testing.T) {
			gs := setupTestStore(t)
			err := gs.WithGitLock(func() error {
				gs.remoteURL = srv.URL
				_, err := gs.FetchAndMerge(tt.ctx(), "origin", "main")
				if !errors.Is(err, tt.want) {
					return fmt.Errorf("FetchAndMerge = %v, want %v (context must abort the fetch)", err, tt.want)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("TestRemoteContextCancellationAborts[%s fetch]: %v", tt.name, err)
			}
		})
		t.Run("PushRemote with "+tt.name+" context", func(t *testing.T) {
			gs := setupTestStore(t)
			err := gs.WithGitLock(func() error {
				gs.remoteURL = srv.URL
				err := gs.PushRemote(tt.ctx())
				if !errors.Is(err, tt.want) {
					return fmt.Errorf("PushRemote = %v, want %v (context must abort the push)", err, tt.want)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("TestRemoteContextCancellationAborts[%s push]: %v", tt.name, err)
			}
		})
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
