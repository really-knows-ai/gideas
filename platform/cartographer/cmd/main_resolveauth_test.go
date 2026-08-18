package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/service"
	gogithttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gogitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	gossh "golang.org/x/crypto/ssh"
)

func TestBuildResolveAuthFnMissingSSHKey(t *testing.T) {
	readSecretFn := func(ctx context.Context, name string) (map[string]string, error) {
		return map[string]string{"some-key": "some-value"}, nil
	}
	fn := buildResolveAuthFn("remote-auth", readSecretFn, "ssh://git@github.com/org/repo.git")
	auth, err := fn()
	if auth != nil {
		t.Fatalf("expected nil auth, got %v", auth)
	}
	if !errors.Is(err, gitstore.ErrAuthConfigMissing) {
		t.Fatalf("expected ErrAuthConfigMissing, got %v", err)
	}
}

func TestBuildResolveAuthFnMissingPassword(t *testing.T) {
	readSecretFn := func(ctx context.Context, name string) (map[string]string, error) {
		return map[string]string{"username": "user"}, nil
	}
	fn := buildResolveAuthFn("remote-auth", readSecretFn, "https://example.com/repo.git")
	auth, err := fn()
	if auth != nil {
		t.Fatalf("expected nil auth, got %v", auth)
	}
	if !errors.Is(err, gitstore.ErrAuthConfigMissing) {
		t.Fatalf("expected ErrAuthConfigMissing, got %v", err)
	}
}

func TestBuildResolveAuthFnAnonymous(t *testing.T) {
	// No secret ref → anonymous access
	fn := buildResolveAuthFn("", nil, "https://example.com/repo.git")
	auth, err := fn()
	if auth != nil || err != nil {
		t.Fatalf("expected (nil, nil) for anonymous, got (auth=%v, err=%v)", auth, err)
	}
}

// TestBuildResolveAuthFnNilReadSecretFailsClosed verifies the
// non-empty-secretRef-with-nil-readSecretFn branch of buildResolveAuthFn: a
// configured Secret ref with no way to read it fails closed with
// gitstore.ErrAuthConfigMissing instead of widening to anonymous access,
// mirroring tryRemotePullOnInit's pre-flight (SPEC error-table row "Remote
// auth config missing (Sync)" → FAILED_PRECONDITION).
func TestBuildResolveAuthFnNilReadSecretFailsClosed(t *testing.T) {
	fn := buildResolveAuthFn("remote-auth", nil, "https://private.example/repo.git")
	auth, err := fn()
	if auth != nil {
		t.Fatalf("expected nil auth, got %v", auth)
	}
	if !errors.Is(err, gitstore.ErrAuthConfigMissing) {
		t.Fatalf("nil-readSecretFn resolver error = %v, want ErrAuthConfigMissing", err)
	}
}

// TestBuildResolveAuthFnReadSecretErrorPropagates verifies the readSecretFn
// error branch of buildResolveAuthFn (main.go:646-649): a Secret read failure
// surfaces the reader's error verbatim (never a sentinel, never anonymous
// access) so the sync worker's per-operation auth resolution fails closed on
// the same error the startup pre-flight check surfaces.
func TestBuildResolveAuthFnReadSecretErrorPropagates(t *testing.T) {
	secretErr := errors.New("secret unavailable")
	readSecretFn := func(ctx context.Context, name string) (map[string]string, error) {
		return nil, secretErr
	}
	fn := buildResolveAuthFn("remote-auth", readSecretFn, "https://private.example/repo.git")
	auth, err := fn()
	if auth != nil {
		t.Fatalf("expected nil auth on secret read failure, got %v", auth)
	}
	if !errors.Is(err, secretErr) {
		t.Fatalf("resolver error = %v, want the readSecretFn error verbatim", err)
	}
}

// TestBuildResolveAuthFnSecretReadBounded pins the resolver's bounded Secret
// read: go-git invokes the resolver without a context (the authFn signature
// carries none), so the resolver must bound its own read with the sync worker's
// per-operation deadline (service.DefaultGitOperationTimeout, SPEC R10 /
// SPEC:981) — a hung k8s API server aborts the read with a context error
// instead of blocking the worker's git operation past its deadline and wedging
// the worker.
func TestBuildResolveAuthFnSecretReadBounded(t *testing.T) {
	var readCtx context.Context
	readSecretFn := func(ctx context.Context, name string) (map[string]string, error) {
		readCtx = ctx
		return map[string]string{"username": tSecretUsername, "password": tSecretPassword}, nil
	}
	fn := buildResolveAuthFn("remote-auth", readSecretFn, "https://private.example/repo.git")
	auth, err := fn()
	if err != nil {
		t.Fatalf("auth resolution failed: %v", err)
	}
	if auth == nil {
		t.Fatal("expected non-nil auth, got nil")
	}
	if readCtx == nil {
		t.Fatal("expected the resolver's Secret read to receive a context, got nil")
	}
	deadline, ok := readCtx.Deadline()
	if !ok {
		t.Fatal("expected the resolver's Secret read to carry a deadline (bounded read), got none")
	}
	wantDeadline := time.Now().Add(service.DefaultGitOperationTimeout)
	if deadline.Before(time.Now()) {
		t.Fatalf("resolver read deadline %v is already in the past", deadline)
	}
	if delta := wantDeadline.Sub(deadline); delta > 5*time.Second {
		t.Fatalf("resolver read deadline %v deviates %v from %v (wrong timeout?)", deadline, delta, wantDeadline)
	}
}

// TestBuildResolveAuthFnSSHSigner verifies SPEC R1: an ssh:// URL with a valid
// ssh-privatekey produces a public-key SSH signer (with insecure host-key
// verification when no known_hosts is supplied).
func TestBuildResolveAuthFnSSHSigner(t *testing.T) {
	keyPEM := ed25519PEM(t)
	readSecretFn := func(ctx context.Context, name string) (map[string]string, error) {
		return map[string]string{"ssh-privatekey": keyPEM}, nil
	}
	fn := buildResolveAuthFn("remote-auth", readSecretFn, "ssh://git@example.com/org/repo.git")
	auth, err := fn()
	if err != nil {
		t.Fatalf("ssh signer construction failed: %v", err)
	}
	if auth == nil {
		t.Fatal("expected non-nil SSH auth, got nil")
	}
	signer, ok := auth.(*gogitssh.PublicKeys)
	if !ok {
		t.Fatalf("expected *gogitssh.PublicKeys, got %T", auth)
	}
	if signer.User != tSSHUser {
		t.Fatalf("expected ssh user to default to 'git', got %q", signer.User)
	}
	// Without known_hosts, host-key verification falls back to
	// InsecureIgnoreHostKey — the callback accepts an unknown host (fail-open).
	if signer.HostKeyCallback == nil {
		t.Fatal("expected non-nil HostKeyCallback (InsecureIgnoreHostKey), got nil")
	}
	if err := signer.HostKeyCallback("unknown-host", &net.TCPAddr{}, nil); err != nil {
		t.Fatalf("expected InsecureIgnoreHostKey callback to accept unknown host, got: %v", err)
	}
}

// TestBuildResolveAuthFnSSHMalformedPEMFails verifies the signer-construction
// error branch of buildResolveAuthFn (main.go:662-665): an ssh-privatekey that
// is present and non-empty but not valid PEM makes gogitssh.NewPublicKeys fail
// (ssh.ParsePrivateKey cannot find a key in it), and that error must propagate
// — a malformed key must never degrade to anonymous access or a fail-open
// signer.
func TestBuildResolveAuthFnSSHMalformedPEMFails(t *testing.T) {
	readSecretFn := func(ctx context.Context, name string) (map[string]string, error) {
		return map[string]string{"ssh-privatekey": "not-a-valid-pem-key"}, nil
	}
	fn := buildResolveAuthFn("remote-auth", readSecretFn, "ssh://git@example.com/org/repo.git")
	auth, err := fn()
	if auth != nil {
		t.Fatalf("expected nil auth on malformed PEM, got %v", auth)
	}
	if err == nil {
		t.Fatal("expected a signer-construction error for malformed PEM, got nil")
	}
}

// TestBuildResolveAuthFnSSHKnownHostsFailClosed verifies that supplying a
// known_hosts entry enables the SSH host-key callback (fail-closed): the
// callback rejects a host not present in the known_hosts file.
func TestBuildResolveAuthFnSSHKnownHostsFailClosed(t *testing.T) {
	keyPEM := ed25519PEM(t)
	// Build a real known_hosts line from a generated host key so the callback
	// can actually parse it.
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	sshPub, err := gossh.NewPublicKey(hostPriv.Public())
	if err != nil {
		t.Fatalf("marshal host public key: %v", err)
	}
	kh := "example.com " + string(gossh.MarshalAuthorizedKey(sshPub))
	readSecretFn := func(ctx context.Context, name string) (map[string]string, error) {
		return map[string]string{"ssh-privatekey": keyPEM, "known_hosts": kh}, nil
	}
	fn := buildResolveAuthFn("remote-auth", readSecretFn, "ssh://git@example.com/org/repo.git")
	auth, err := fn()
	if err != nil {
		t.Fatalf("ssh auth construction with known_hosts failed: %v", err)
	}
	signer, ok := auth.(*gogitssh.PublicKeys)
	if !ok {
		t.Fatalf("expected *gogitssh.PublicKeys, got %T", auth)
	}
	if signer.HostKeyCallback == nil {
		t.Fatal("expected non-nil HostKeyCallback when known_hosts is present (fail-closed), got nil")
	}
	// The configured callback must reject a host that is not in known_hosts.
	if err := signer.HostKeyCallback("unknown-host", &net.TCPAddr{}, sshPub); err == nil {
		t.Fatal("expected unknown-host rejection from fail-closed HostKeyCallback, got nil")
	}
}

// TestBuildResolveAuthFnSSHKnownHostsEmptyFailsClosed verifies SPEC R1's
// present-key rule: a present-but-empty known_hosts Secret value fails closed
// instead of degrading to InsecureIgnoreHostKey. An empty known_hosts means
// there are no known hosts, so every host is unknown and the callback must
// reject it — mirroring the missing-expected-key rule applied to
// ssh-privatekey (a present-but-empty data key is equivalent to an absent one).
func TestBuildResolveAuthFnSSHKnownHostsEmptyFailsClosed(t *testing.T) {
	keyPEM := ed25519PEM(t)
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	sshPub, err := gossh.NewPublicKey(hostPriv.Public())
	if err != nil {
		t.Fatalf("marshal host public key: %v", err)
	}
	readSecretFn := func(ctx context.Context, name string) (map[string]string, error) {
		return map[string]string{"ssh-privatekey": keyPEM, "known_hosts": ""}, nil
	}
	fn := buildResolveAuthFn("remote-auth", readSecretFn, "ssh://git@example.com/org/repo.git")
	auth, err := fn()
	if err != nil {
		t.Fatalf("ssh auth construction with empty known_hosts failed: %v", err)
	}
	signer, ok := auth.(*gogitssh.PublicKeys)
	if !ok {
		t.Fatalf("expected *gogitssh.PublicKeys, got %T", auth)
	}
	if signer.HostKeyCallback == nil {
		t.Fatal("expected non-nil HostKeyCallback when known_hosts key is present (fail-closed), got nil")
	}
	// A present-but-empty known_hosts must NOT degrade to InsecureIgnoreHostKey:
	// the callback must reject an unknown host.
	if err := signer.HostKeyCallback("unknown-host", &net.TCPAddr{}, sshPub); err == nil {
		t.Fatal("expected unknown-host rejection from fail-closed HostKeyCallback for empty known_hosts, got nil")
	}
}

// TestBuildResolveAuthFnSSHKnownHostsParseFailure verifies the known_hosts
// parse-error branch of buildResolveAuthFn (main.go:685-689): a known_hosts
// value that is not a valid known_hosts line makes knownhosts.New fail, and
// that error must propagate — an unparseable known_hosts file must never
// degrade to InsecureIgnoreHostKey. The fixture is a single word, which the
// parser rejects ("knownhosts: missing host pattern": every line needs at
// least "hostname keytype key"); the resolver writes and removes its own temp
// file, so the fixture needs no cleanup.
func TestBuildResolveAuthFnSSHKnownHostsParseFailure(t *testing.T) {
	keyPEM := ed25519PEM(t)
	readSecretFn := func(ctx context.Context, name string) (map[string]string, error) {
		return map[string]string{"ssh-privatekey": keyPEM, "known_hosts": "not-a-valid-known-hosts-entry"}, nil
	}
	fn := buildResolveAuthFn("remote-auth", readSecretFn, "ssh://git@example.com/org/repo.git")
	auth, err := fn()
	if auth != nil {
		t.Fatalf("expected nil auth on known_hosts parse failure, got %v", auth)
	}
	if err == nil {
		t.Fatal("expected a known_hosts parse error, got nil")
	}
	// Pin the branch: the signer constructed fine (valid PEM above), so the
	// surfaced error must be the known_hosts file parse failure.
	if !strings.Contains(err.Error(), "knownhosts:") {
		t.Fatalf("expected a knownhosts.New parse error, got %v", err)
	}
}

// TestBuildResolveAuthFnHTTPSBasicAuth verifies the https SUCCESS path (SPEC
// R1): a valid https Secret (username + password) yields a usable
// *http.BasicAuth with the expected Username and Password.
func TestBuildResolveAuthFnHTTPSBasicAuth(t *testing.T) {
	readSecretFn := func(ctx context.Context, name string) (map[string]string, error) {
		return map[string]string{"username": tSecretUsername, "password": tSecretPassword}, nil
	}
	fn := buildResolveAuthFn("remote-auth", readSecretFn, "https://example.com/repo.git")
	auth, err := fn()
	if err != nil {
		t.Fatalf("https auth construction failed: %v", err)
	}
	basic, ok := auth.(*gogithttp.BasicAuth)
	if !ok {
		t.Fatalf("expected *http.BasicAuth, got %T", auth)
	}
	if basic.Username != tSecretUsername {
		t.Fatalf("BasicAuth.Username = %q, want %q", basic.Username, tSecretUsername)
	}
	if basic.Password != tSecretPassword {
		t.Fatalf("BasicAuth.Password = %q, want %q", basic.Password, tSecretPassword)
	}
	// The BasicAuth must be usable to set an outgoing request header.
	req, err := http.NewRequest(http.MethodGet, "https://example.com/repo.git", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	basic.SetAuth(req)
	user, pass, ok := req.BasicAuth()
	if !ok || user != tSecretUsername || pass != tSecretPassword {
		t.Fatalf("SetAuth produced header user=%q pass=%q ok=%v, want user=%q pass=%q ok=true",
			user, pass, ok, tSecretUsername, tSecretPassword)
	}
}

// TestBuildResolveAuthFnURLUserOverridesSecret verifies SPEC R1 precedence: a
// username embedded in the https URL wins over the Secret's username key.
func TestBuildResolveAuthFnURLUserOverridesSecret(t *testing.T) {
	readSecretFn := func(ctx context.Context, name string) (map[string]string, error) {
		return map[string]string{"username": tSecretUsername, "password": tSecretPassword}, nil
	}
	fn := buildResolveAuthFn("remote-auth", readSecretFn, "https://url-user@example.com/repo.git")
	auth, err := fn()
	if err != nil {
		t.Fatalf("https auth construction failed: %v", err)
	}
	basic, ok := auth.(*gogithttp.BasicAuth)
	if !ok {
		t.Fatalf("expected *http.BasicAuth, got %T", auth)
	}
	if basic.Username != "url-user" {
		t.Fatalf("BasicAuth.Username = %q, want URL-embedded %q to win over Secret", basic.Username, "url-user")
	}
}

// TestBuildResolveAuthFnURLUserFallsBackToSecret verifies that an https URL
// without an embedded username falls back to the Secret's username key.
func TestBuildResolveAuthFnURLUserFallsBackToSecret(t *testing.T) {
	readSecretFn := func(ctx context.Context, name string) (map[string]string, error) {
		return map[string]string{"username": tSecretUsername, "password": tSecretPassword}, nil
	}
	fn := buildResolveAuthFn("remote-auth", readSecretFn, "https://example.com/repo.git")
	auth, err := fn()
	if err != nil {
		t.Fatalf("https auth construction failed: %v", err)
	}
	basic, ok := auth.(*gogithttp.BasicAuth)
	if !ok {
		t.Fatalf("expected *http.BasicAuth, got %T", auth)
	}
	if basic.Username != tSecretUsername {
		t.Fatalf("BasicAuth.Username = %q, want Secret fallback %q", basic.Username, tSecretUsername)
	}
}

// TestBuildResolveAuthFnSSHDefaultUser verifies SPEC R1: an ssh:// URL with no
// embedded user defaults the auth user to "git".
func TestBuildResolveAuthFnSSHUserDefaultsToGit(t *testing.T) {
	keyPEM := ed25519PEM(t)
	readSecretFn := func(ctx context.Context, name string) (map[string]string, error) {
		return map[string]string{"ssh-privatekey": keyPEM}, nil
	}
	// No user in the URL — the resolver must fall back to "git".
	fn := buildResolveAuthFn("remote-auth", readSecretFn, "ssh://example.com/org/repo.git")
	auth, err := fn()
	if err != nil {
		t.Fatalf("ssh auth construction failed: %v", err)
	}
	signer, ok := auth.(*gogitssh.PublicKeys)
	if !ok {
		t.Fatalf("expected *gogitssh.PublicKeys, got %T", auth)
	}
	if signer.User != tSSHUser {
		t.Fatalf("ssh user default = %q, want %q", signer.User, tSSHUser)
	}
}

// TestBuildResolveAuthFnSSHURLUserOverridesDefault verifies SPEC R1: a
// distinct username embedded in the ssh URL wins over the "git" default,
// mirroring the https URL-user-precedence branch
// (TestBuildResolveAuthFnURLUserOverridesSecret). Without this test the
// embedded-user branch (main.go:732) is only exercised with the user
// "git", indistinguishable from the default.
func TestBuildResolveAuthFnSSHURLUserOverridesDefault(t *testing.T) {
	keyPEM := ed25519PEM(t)
	readSecretFn := func(ctx context.Context, name string) (map[string]string, error) {
		return map[string]string{"ssh-privatekey": keyPEM}, nil
	}
	fn := buildResolveAuthFn("remote-auth", readSecretFn, "ssh://deploy-user@example.com/org/repo.git")
	auth, err := fn()
	if err != nil {
		t.Fatalf("ssh auth construction failed: %v", err)
	}
	signer, ok := auth.(*gogitssh.PublicKeys)
	if !ok {
		t.Fatalf("expected *gogitssh.PublicKeys, got %T", auth)
	}
	if signer.User != "deploy-user" {
		t.Fatalf("ssh user = %q, want URL-embedded %q to win over the %q default", signer.User, "deploy-user", tSSHUser)
	}
}

// TestBuildResolveAuthFnParseURLFailure verifies the url.Parse error branch of
// buildResolveAuthFn (main.go:461-463): a remote URL the parser rejects must
// surface the parse error (never a sentinel, never a nil auth) and stay in the
// error branch, so a malformed remote URL cannot silently fall through to
// anonymous/unauthenticated access.
func TestBuildResolveAuthFnURLParseFailure(t *testing.T) {
	readSecretFn := func(ctx context.Context, name string) (map[string]string, error) {
		return map[string]string{"username": tSecretUsername, "password": tSecretPassword}, nil
	}
	// malformedURL contains an invalid percent-escape, which url.Parse rejects
	// ("net/url: invalid URL escape \"%zz\"").
	malformedURL := "https://host/%zz"
	// Guard the fixture: this must actually be a URL url.Parse rejects, or the
	// test is asserting the wrong branch.
	//nolint:staticcheck // the fixture is intentionally an invalid URL so the
	// parse-error branch of buildResolveAuthFn is exercised; the guard asserts
	// that url.Parse genuinely rejects it (see the test comment above).
	if _, err := url.Parse(malformedURL); err == nil {
		t.Fatalf("test fixture %q unexpectedly parses; pick a URL url.Parse rejects", malformedURL)
	}
	fn := buildResolveAuthFn("remote-auth", readSecretFn, malformedURL)
	auth, err := fn()
	if auth != nil {
		t.Fatalf("expected nil auth for malformed URL, got %v", auth)
	}
	if err == nil {
		t.Fatal("expected a url.Parse error for malformed URL, got nil")
	}
	var parseErr *url.Error
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected a *url.Error from the parse branch, got %T: %v", err, err)
	}
}

// TestBuildResolveAuthFnUnsupportedScheme verifies that a remote URL with an
// unsupported scheme returns ErrUnsupportedURLScheme, and that file:// — a
// supported scheme (SPEC error-table row 987) with no auth keys (SPEC.md:91-100)
// — resolves to anonymous access even when a secretRef is configured.
func TestBuildResolveAuthFnUnsupportedScheme(t *testing.T) {
	readSecretFn := func(ctx context.Context, name string) (map[string]string, error) {
		return map[string]string{"username": "user", "password": "pass"}, nil
	}
	for _, scheme := range []string{"ftp", "git"} {
		t.Run(scheme, func(t *testing.T) {
			fn := buildResolveAuthFn("remote-auth", readSecretFn, scheme+"://example.com/repo.git")
			auth, err := fn()
			if auth != nil {
				t.Fatalf("expected nil auth, got %v", auth)
			}
			if !errors.Is(err, gitstore.ErrUnsupportedURLScheme) {
				t.Fatalf("expected ErrUnsupportedURLScheme, got %v", err)
			}
		})
	}
	t.Run("file", func(t *testing.T) {
		fn := buildResolveAuthFn("remote-auth", readSecretFn, "file:///tmp/repo.git")
		auth, err := fn()
		if err != nil {
			t.Fatalf("expected nil error for file:// anonymous auth, got %v", err)
		}
		if auth != nil {
			t.Fatalf("expected nil (anonymous) auth for file://, got %v", auth)
		}
		// The file:// short-circuit precedes the Secret read: even a failing
		// reader must not block a file:// remote (SPEC.md:91-100 defines auth
		// keys only for ssh:// and https://).
		failingFn := buildResolveAuthFn("remote-auth",
			func(ctx context.Context, name string) (map[string]string, error) {
				return nil, errors.New("secret unavailable")
			}, "file:///tmp/repo.git")
		auth, err = failingFn()
		if err != nil {
			t.Fatalf("expected nil error for file:// with failing reader, got %v", err)
		}
		if auth != nil {
			t.Fatalf("expected nil (anonymous) auth for file:// with failing reader, got %v", auth)
		}
	})
}
