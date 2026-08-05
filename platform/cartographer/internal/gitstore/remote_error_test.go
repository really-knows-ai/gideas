package gitstore

import (
	"errors"
	"net"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

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
