package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"testing"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	gogithttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gogitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// Test constants for the git remote auth URL-scheme resolver tests. They are
// hoisted to constants to satisfy the goconst linter.
const (
	tSSHUser        = "git"
	tSecretUsername = "secret-user"
	tSecretPassword = "secret-pass"
)

type initPullGitStore struct {
	gitstore.GitStore
	isEmpty      bool
	cloneCalls   int
	cloneErr     error
	pushCalls    int
	pushErr      error
	initStateErr error
}

func (g *initPullGitStore) IsEmpty(context.Context) (bool, error) { return g.isEmpty, g.initStateErr }
func (g *initPullGitStore) WithGitLock(fn func() error) error     { return fn() }
func (g *initPullGitStore) CloneSingleBranch(context.Context, string, string) error {
	g.cloneCalls++
	return g.cloneErr
}
func (g *initPullGitStore) PushRemote(context.Context) error {
	g.pushCalls++
	return g.pushErr
}

// scenarioGitStore tracks the sequence of remote operations performed under a
// given init scenario so tests can assert the exact call order.
type scenarioGitStore struct {
	initPullGitStore
	ops []string
}

func (g *scenarioGitStore) CloneSingleBranch(context.Context, string, string) error {
	g.cloneCalls++
	g.ops = append(g.ops, "clone")
	return g.cloneErr
}
func (g *scenarioGitStore) PushRemote(context.Context) error {
	g.pushCalls++
	g.ops = append(g.ops, "push")
	return g.pushErr
}

// ed25519PEM returns a PEM-encoded (PKCS8) ed25519 private key, matching the
// unencrypted PEM format SPEC R1 requires for the ssh-privatekey Secret key.
func ed25519PEM(t *testing.T) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	_ = pub
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal ed25519 private key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestParseBoolEnv(t *testing.T) {
	cases := []struct {
		val  string
		def  bool
		want bool
	}{
		{"true", false, true},
		{"TRUE", false, true},
		{"1", false, true},
		{" false ", true, false},
		{"0", true, false},
		{"t", false, true},
		{"F", true, false},
		// empty and unparseable values fall back to the default, never panic.
		{"", true, true},
		{"bogus", true, true},
	}
	for _, tc := range cases {
		t.Setenv("REMOTE_PULL_ON_INIT", tc.val)
		if got := parseBoolEnv("REMOTE_PULL_ON_INIT", tc.def); got != tc.want {
			t.Errorf("parseBoolEnv(%q, %v) = %v, want %v", tc.val, tc.def, got, tc.want)
		}
	}
	// Unset env falls back to default.
	t.Setenv("REMOTE_PULL_ON_INIT", "")
	if got := parseBoolEnv("REMOTE_PULL_ON_INIT", false); got {
		t.Error("parseBoolEnv on unset var = true, want false")
	}
}

func TestTryRemotePullOnInitAnonymous(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true}
	if err := tryRemotePullOnInit(gs, "https://public.example/repo.git", "", nil, nil, nil); err != nil {
		t.Fatalf("tryRemotePullOnInit: %v", err)
	}
	if gs.cloneCalls != 1 {
		t.Fatalf("anonymous clone calls = %d, want 1", gs.cloneCalls)
	}
}

func TestTryRemotePullOnInitConfiguredSecretFailure(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true}
	secretErr := errors.New("secret unavailable")
	err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth",
		func(context.Context, string) (map[string]string, error) { return nil, secretErr }, nil, nil)
	if !errors.Is(err, secretErr) {
		t.Fatalf("tryRemotePullOnInit error = %v, want wrapped secret error", err)
	}
	if gs.cloneCalls != 0 {
		t.Fatalf("clone calls after secret failure = %d, want 0", gs.cloneCalls)
	}
}

func TestTryRemotePullOnInitPrivateRemoteAuthFailureIsNonBlocking(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true, cloneErr: gitstore.ErrAuthFailed}
	err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth",
		func(context.Context, string) (map[string]string, error) {
			return map[string]string{"password": "expired"}, nil
		}, nil, nil)
	if err != nil {
		t.Fatalf("runtime clone failure blocked startup: %v", err)
	}
	if gs.cloneCalls != 1 {
		t.Fatalf("private clone calls = %d, want 1", gs.cloneCalls)
	}
}

// TestTryRemotePullOnInitCloneRehydrates verifies SPEC R10 Init: after a
// successful clone-on-init, the working tree is re-hydrated into main.
func TestTryRemotePullOnInitCloneRehydrates(t *testing.T) {
	gs := &scenarioGitStore{initPullGitStore: initPullGitStore{isEmpty: true}}
	rehydrated := false
	err := tryRemotePullOnInit(gs, "https://public.example/repo.git", "", nil, nil,
		func() error { rehydrated = true; return nil })
	if err != nil {
		t.Fatalf("tryRemotePullOnInit: %v", err)
	}
	if gs.cloneCalls != 1 {
		t.Fatalf("clone calls = %d, want 1", gs.cloneCalls)
	}
	if !rehydrated {
		t.Fatal("expected rehydrate to run after successful clone-on-init, but it was not called")
	}
}

// TestTryRemotePullOnInitRehydrateFailureIsNonBlocking verifies that a failed
// re-hydration after a successful clone logs and continues (does not block startup).
func TestTryRemotePullOnInitRehydrateFailureIsNonBlocking(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true}
	err := tryRemotePullOnInit(gs, "https://public.example/repo.git", "", nil, nil,
		func() error { return errors.New("rehydrate boom") })
	if err != nil {
		t.Fatalf("rehydrate failure blocked startup: %v", err)
	}
}

// TestTryRemotePullOnInitCatchUpPush verifies SPEC R10 Init: when the local
// repo already has commits (not empty), a catch-up push is performed.
func TestTryRemotePullOnInitCatchUpPush(t *testing.T) {
	gs := &scenarioGitStore{initPullGitStore: initPullGitStore{isEmpty: false}}
	if err := tryRemotePullOnInit(gs, "https://public.example/repo.git", "", nil, nil, nil); err != nil {
		t.Fatalf("tryRemotePullOnInit: %v", err)
	}
	if gs.pushCalls != 1 {
		t.Fatalf("catch-up push calls = %d, want 1", gs.pushCalls)
	}
	if gs.cloneCalls != 0 {
		t.Fatalf("clone calls on non-empty repo = %d, want 0", gs.cloneCalls)
	}
	if len(gs.ops) != 1 || gs.ops[0] != "push" {
		t.Fatalf("expected only a push on init, got ops=%v", gs.ops)
	}
}

// TestTryRemotePullOnInitCatchUpPushFailureNonBlocking verifies a failed
// catch-up push is logged and deferred, not fatal to startup.
func TestTryRemotePullOnInitCatchUpPushFailureNonBlocking(t *testing.T) {
	gs := &initPullGitStore{isEmpty: false, pushErr: errors.New("push failed")}
	if err := tryRemotePullOnInit(gs, "https://public.example/repo.git", "", nil, nil, nil); err != nil {
		t.Fatalf("catch-up push failure blocked startup: %v", err)
	}
	if gs.pushCalls != 1 {
		t.Fatalf("push calls = %d, want 1", gs.pushCalls)
	}
}

// TestTryRemotePullOnInitStateCheckFailureNonBlocking verifies SPEC R10 Init:
// a repository-state (IsEmpty) check failure on init is logged and non-fatal —
// no clone is attempted, no error blocks startup, and it does not call os.Exit.
func TestTryRemotePullOnInitStateCheckFailureNonBlocking(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true, initStateErr: errors.New("state check boom")}
	if err := tryRemotePullOnInit(gs, "https://public.example/repo.git", "", nil, nil, nil); err != nil {
		t.Fatalf("IsEmpty() failure blocked startup: %v", err)
	}
	if gs.cloneCalls != 0 {
		t.Fatalf("clone calls after state-check failure = %d, want 0", gs.cloneCalls)
	}
	if gs.pushCalls != 0 {
		t.Fatalf("push calls after state-check failure = %d, want 0", gs.pushCalls)
	}
}

// ---------------------------------------------------------------------------
// loadVerificationKey / parseVerificationKey tests
// ---------------------------------------------------------------------------

func TestParseVerificationKeyMissingEnv(t *testing.T) {
	t.Setenv("OPERATOR_VERIFICATION_KEY", "")
	got, err := parseVerificationKey("OPERATOR_VERIFICATION_KEY")
	if err == nil {
		t.Fatal("expected error for missing verification key env, got nil")
	}
	if got != nil {
		t.Fatalf("expected nil key on error, got %v", got)
	}
}

func TestParseVerificationKeyInvalidLength(t *testing.T) {
	t.Setenv("OPERATOR_VERIFICATION_KEY", "too-short")
	got, err := parseVerificationKey("OPERATOR_VERIFICATION_KEY")
	if err == nil {
		t.Fatal("expected error for malformed verification key, got nil")
	}
	if got != nil {
		t.Fatalf("expected nil key on malformed input, got %v", got)
	}
}

func TestParseVerificationKeyValid(t *testing.T) {
	// A raw 32-byte key with no NUL bytes (env vars cannot hold NUL on POSIX).
	key := bytes.Repeat([]byte{'a'}, ed25519.PublicKeySize)
	t.Setenv("OPERATOR_VERIFICATION_KEY", string(key))
	got, err := parseVerificationKey("OPERATOR_VERIFICATION_KEY")
	if err != nil {
		t.Fatalf("parseVerificationKey: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil key, got nil")
	}
	if len(got) != ed25519.PublicKeySize {
		t.Fatalf("key length = %d, want %d", len(got), ed25519.PublicKeySize)
	}
	if !bytes.Equal(got, ed25519.PublicKey(key)) {
		t.Fatal("parsed key does not match the raw env bytes")
	}
}

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

// TestBuildResolveAuthFnUnsupportedScheme verifies that a remote URL with an
// unsupported scheme returns ErrUnsupportedURLScheme.
func TestBuildResolveAuthFnUnsupportedScheme(t *testing.T) {
	readSecretFn := func(ctx context.Context, name string) (map[string]string, error) {
		return map[string]string{"username": "user", "password": "pass"}, nil
	}
	for _, scheme := range []string{"ftp", "git", "file"} {
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
}
