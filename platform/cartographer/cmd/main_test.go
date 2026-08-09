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
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/service"
	"github.com/foundry/flow/cartographer/internal/store"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/pkg/eventbus"
	gogithttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gogitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/google/uuid"
	gossh "golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
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

// TestGetEnv verifies the SPEC R5 env-var default fallbacks implemented by
// getEnv (main.go:48-55): each variable's default is returned when the env
// var is unset/empty, and the env value wins when present.
func TestGetEnv(t *testing.T) {
	cases := []struct {
		key     string
		def     string
		wantDef string
	}{
		{"LADYBUG_DB_PATH", "/data", "/data"},
		{"CARTOGRAPHER_PORT", "50051", "50051"},
		{"TRANSACTION_TIMEOUT", "30m", "30m"},
		{"POD_NAMESPACE", "default", "default"},
		{"CAPABILITY_STALENESS_WINDOW", "30s", "30s"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"/unset", func(t *testing.T) {
			// An empty env var is equivalent to unset for getEnv.
			t.Setenv(tc.key, "")
			if got := getEnv(tc.key, tc.def); got != tc.wantDef {
				t.Errorf("getEnv(%q, %q) on unset var = %q, want default %q", tc.key, tc.def, got, tc.wantDef)
			}
		})
		t.Run(tc.key+"/set", func(t *testing.T) {
			t.Setenv(tc.key, "env-value")
			if got := getEnv(tc.key, tc.def); got != "env-value" {
				t.Errorf("getEnv(%q, %q) with env set = %q, want env value %q", tc.key, tc.def, got, "env-value")
			}
		})
	}
}

// TestParseDurationEnv verifies the SPEC R5 fail-fast duration env parsing
// (main.go:58-68): an unparseable value returns an error (the caller exits),
// a valid value parses, and an unset/empty var falls back to the default.
func TestParseDurationEnv(t *testing.T) {
	t.Run("unset falls back to default", func(t *testing.T) {
		t.Setenv("TRANSACTION_TIMEOUT", "")
		got, err := parseDurationEnv("TRANSACTION_TIMEOUT", "30m")
		if err != nil {
			t.Fatalf("parseDurationEnv on unset var: %v", err)
		}
		if want := 30 * time.Minute; got != want {
			t.Errorf("parseDurationEnv on unset var = %v, want %v", got, want)
		}
	})
	t.Run("valid env value wins", func(t *testing.T) {
		t.Setenv("TRANSACTION_TIMEOUT", "45s")
		got, err := parseDurationEnv("TRANSACTION_TIMEOUT", "30m")
		if err != nil {
			t.Fatalf("parseDurationEnv: %v", err)
		}
		if want := 45 * time.Second; got != want {
			t.Errorf("parseDurationEnv = %v, want %v", got, want)
		}
	})
	t.Run("invalid env value errors", func(t *testing.T) {
		t.Setenv("CAPABILITY_STALENESS_WINDOW", "not-a-duration")
		if _, err := parseDurationEnv("CAPABILITY_STALENESS_WINDOW", "30s"); err == nil {
			t.Error("parseDurationEnv on invalid value = nil error, want error (fail-fast exit)")
		}
	})
}

// TestNewHealthServerServing verifies SPEC R5: before the first ApplySchema,
// the standard health service reports SERVING. main registers this state at
// startup via newHealthServer (main.go:271-272); the shutdown path flips it
// to NOT_SERVING.
func TestNewHealthServerServing(t *testing.T) {
	srv := newHealthServer()
	resp, err := srv.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health Check: %v", err)
	}
	if resp.Status != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Errorf("startup health status = %v, want SERVING", resp.Status)
	}
}

func TestTryRemotePullOnInitAnonymous(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true}
	catchUp, err := tryRemotePullOnInit(gs, "https://public.example/repo.git", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("tryRemotePullOnInit: %v", err)
	}
	if catchUp {
		t.Fatal("anonymous clone path must not flag a catch-up push")
	}
	if gs.cloneCalls != 1 {
		t.Fatalf("anonymous clone calls = %d, want 1", gs.cloneCalls)
	}
}

func TestTryRemotePullOnInitConfiguredSecretFailure(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true}
	secretErr := errors.New("secret unavailable")
	_, err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth",
		func(context.Context, string) (map[string]string, error) { return nil, secretErr }, nil, nil)
	if !errors.Is(err, secretErr) {
		t.Fatalf("tryRemotePullOnInit error = %v, want wrapped secret error", err)
	}
	if gs.cloneCalls != 0 {
		t.Fatalf("clone calls after secret failure = %d, want 0", gs.cloneCalls)
	}
}

// TestTryRemotePullOnInitSSHEmptyKeyFailsClosed verifies the ssh-empty-key
// branch of tryRemotePullOnInit's pre-flight auth resolver: a present-but-empty
// ssh-privatekey Secret is equivalent to an absent one and fail-closes with
// gitstore.ErrAuthConfigMissing before any git operation is attempted.
func TestTryRemotePullOnInitSSHEmptyKeyFailsClosed(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true}
	_, err := tryRemotePullOnInit(gs, "ssh://git@github.com/org/repo.git", "remote-auth",
		func(context.Context, string) (map[string]string, error) {
			return map[string]string{"ssh-privatekey": ""}, nil
		}, nil, nil)
	if !errors.Is(err, gitstore.ErrAuthConfigMissing) {
		t.Fatalf("ssh-empty-key pre-flight error = %v, want ErrAuthConfigMissing", err)
	}
	if gs.cloneCalls != 0 {
		t.Fatalf("clone calls after ssh-empty-key rejection = %d, want 0", gs.cloneCalls)
	}
}

// TestTryRemotePullOnInitUnsupportedSchemeFailsClosed verifies the
// unsupported-scheme branch of tryRemotePullOnInit's pre-flight auth resolver:
// a remote URL with a scheme url.Parse accepts but the resolver does not
// support returns gitstore.ErrUnsupportedURLScheme before any git operation.
func TestTryRemotePullOnInitUnsupportedSchemeFailsClosed(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true}
	_, err := tryRemotePullOnInit(gs, "ftp://example.com/repo.git", "remote-auth",
		func(context.Context, string) (map[string]string, error) {
			return map[string]string{"username": "user", "password": "pass"}, nil
		}, nil, nil)
	if !errors.Is(err, gitstore.ErrUnsupportedURLScheme) {
		t.Fatalf("unsupported-scheme pre-auth error = %v, want ErrUnsupportedURLScheme", err)
	}
	if gs.cloneCalls != 0 {
		t.Fatalf("clone calls after unsupported-scheme rejection = %d, want 0", gs.cloneCalls)
	}
}

// TestTryRemotePullOnInitFileSchemeAnonymous verifies the file:// branch of
// tryRemotePullOnInit's pre-flight auth resolver: file:// is a supported scheme
// (SPEC error-table row 987) with no auth keys (SPEC.md:91-100 defines keys only
// for ssh:// and https://), so even a configured secretRef must not block the
// clone — the remote proceeds anonymously.
func TestTryRemotePullOnInitFileSchemeAnonymous(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true}
	catchUp, err := tryRemotePullOnInit(gs, "file:///tmp/repo.git", "remote-auth",
		func(context.Context, string) (map[string]string, error) {
			return map[string]string{"password": "unrelated"}, nil
		}, nil, nil)
	if err != nil {
		t.Fatalf("file:// pre-flight error = %v, want nil (anonymous)", err)
	}
	if catchUp {
		t.Fatal("file:// clone path must not flag a catch-up push")
	}
	if gs.cloneCalls != 1 {
		t.Fatalf("file:// clone calls = %d, want 1", gs.cloneCalls)
	}
}

// TestTryRemotePullOnInitHTTPSMissingPasswordFailsClosed verifies the
// https missing/empty-password branch of tryRemotePullOnInit's pre-flight auth
// resolver: an https remote whose Secret lacks a password fails closed with
// gitstore.ErrAuthConfigMissing before any git operation is attempted.
func TestTryRemotePullOnInitHTTPSMissingPasswordFailsClosed(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true}
	_, err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth",
		func(context.Context, string) (map[string]string, error) {
			return map[string]string{"username": "user"}, nil
		}, nil, nil)
	if !errors.Is(err, gitstore.ErrAuthConfigMissing) {
		t.Fatalf("https-missing-password pre-flight error = %v, want ErrAuthConfigMissing", err)
	}
	if gs.cloneCalls != 0 {
		t.Fatalf("clone calls after https-missing-password rejection = %d, want 0", gs.cloneCalls)
	}
}

// TestTryRemotePullOnInitParseURLFailureFailsClosed verifies the url.Parse
// error branch of tryRemotePullOnInit's pre-flight auth resolver: a remote URL
// the parser rejects surfaces the parse error (never a sentinel, never a silent
// fall-through to anonymous/unauthenticated access) and stays in the error
// branch before any git operation.
func TestTryRemotePullOnInitParseURLFailureFailsClosed(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true}
	// malformedURL contains an invalid percent-escape, which url.Parse rejects
	// ("net/url: invalid URL escape \"%zz\"").
	malformedURL := "https://host/%zz"
	// Guard the fixture: this must actually be a URL url.Parse rejects, or the
	// test is asserting the wrong branch.
	//nolint:staticcheck // the fixture is intentionally an invalid URL so the
	// parse-error branch of the pre-flight resolver is exercised; the guard
	// asserts that url.Parse genuinely rejects it (see the test comment above).
	if _, err := url.Parse(malformedURL); err == nil {
		t.Fatalf("test fixture %q unexpectedly parses; pick a URL url.Parse rejects", malformedURL)
	}
	_, err := tryRemotePullOnInit(gs, malformedURL, "remote-auth",
		func(context.Context, string) (map[string]string, error) {
			return map[string]string{"password": "pass"}, nil
		}, nil, nil)
	if err == nil {
		t.Fatal("expected a url.Parse error for malformed URL, got nil")
	}
	var parseErr *url.Error
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected a *url.Error from the parse branch, got %T: %v", err, err)
	}
	if gs.cloneCalls != 0 {
		t.Fatalf("clone calls after parse failure = %d, want 0", gs.cloneCalls)
	}
}

// TestTryRemotePullOnInitNilReadSecretFailsClosed verifies the
// non-empty-secretRef-with-nil-readSecretFn branch of tryRemotePullOnInit's
// pre-flight auth resolver: a configured Secret ref with no way to read it
// fails closed with gitstore.ErrAuthConfigMissing before any git operation.
func TestTryRemotePullOnInitNilReadSecretFailsClosed(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true}
	_, err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth", nil, nil, nil)
	if !errors.Is(err, gitstore.ErrAuthConfigMissing) {
		t.Fatalf("nil-readSecretFn pre-flight error = %v, want ErrAuthConfigMissing", err)
	}
	if gs.cloneCalls != 0 {
		t.Fatalf("clone calls after nil-readSecretFn rejection = %d, want 0", gs.cloneCalls)
	}
}

func TestTryRemotePullOnInitPrivateRemoteAuthFailureIsNonBlocking(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true, cloneErr: gitstore.ErrAuthFailed}
	catchUp, err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth",
		func(context.Context, string) (map[string]string, error) {
			return map[string]string{"password": "expired"}, nil
		}, nil, nil)
	if err != nil {
		t.Fatalf("runtime clone failure blocked startup: %v", err)
	}
	if catchUp {
		t.Fatal("failed clone path must not flag a catch-up push")
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
	catchUp, err := tryRemotePullOnInit(gs, "https://public.example/repo.git", "", nil, nil,
		func() error { rehydrated = true; return nil })
	if err != nil {
		t.Fatalf("tryRemotePullOnInit: %v", err)
	}
	if catchUp {
		t.Fatal("clone path must not flag a catch-up push (nothing local to push)")
	}
	if gs.cloneCalls != 1 {
		t.Fatalf("clone calls = %d, want 1", gs.cloneCalls)
	}
	if !rehydrated {
		t.Fatal("expected rehydrate to run after successful clone-on-init, but it was not called")
	}
}

// TestTryRemotePullOnInitRehydrateFailureFailsStartup verifies that a failed
// re-hydration after a successful clone-on-init is fatal: the identical
// condition (empty main.lbug + committed git) is fatal in the SPEC R8 recovery
// path (rehydrateMainAfterRecovery), and serving a vacuous empty graph while
// graph-repo/ holds the cloned history would hide committed data. The error is
// propagated to main, which aborts startup (mirroring the recovery path).
func TestTryRemotePullOnInitRehydrateFailureFailsStartup(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true}
	_, err := tryRemotePullOnInit(gs, "https://public.example/repo.git", "", nil, nil,
		func() error { return errors.New("rehydrate boom") })
	if err == nil {
		t.Fatal("expected re-hydration failure to be surfaced (fatal at startup), got nil")
	}
	if !strings.Contains(err.Error(), "rehydrate boom") {
		t.Fatalf("error does not carry the re-hydration failure: %v", err)
	}
	if !strings.Contains(err.Error(), "cloned remote tree") {
		t.Fatalf("error does not identify the clone-on-init re-hydration path: %v", err)
	}
	if gs.cloneCalls != 1 {
		t.Fatalf("clone calls = %d, want 1 (clone ran before re-hydration)", gs.cloneCalls)
	}
}

// TestTryRemotePullOnInitCatchUpPush verifies SPEC R10 Init: when the local
// repo already has commits (not empty), tryRemotePullOnInit reports that the
// sync worker's first cycle must perform the catch-up push; the push itself is
// NOT performed here (the worker is constructed after this init path and
// applies the R10 error-table retry contract to the push).
func TestTryRemotePullOnInitCatchUpPush(t *testing.T) {
	gs := &scenarioGitStore{initPullGitStore: initPullGitStore{isEmpty: false}}
	catchUp, err := tryRemotePullOnInit(gs, "https://public.example/repo.git", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("tryRemotePullOnInit: %v", err)
	}
	if !catchUp {
		t.Fatal("expected catch-up push flag on a non-empty repo, got false")
	}
	if gs.pushCalls != 0 {
		t.Fatalf("direct push calls = %d, want 0 (push is deferred to the sync worker's first cycle)", gs.pushCalls)
	}
	if gs.cloneCalls != 0 {
		t.Fatalf("clone calls on non-empty repo = %d, want 0", gs.cloneCalls)
	}
	if len(gs.ops) != 0 {
		t.Fatalf("expected no remote operations on init (push deferred to worker), got ops=%v", gs.ops)
	}
}

// TestTryRemotePullOnInitCatchUpPushSecretFailureFailsStartup verifies the SPEC
// fail-startup clause (R1 Secret data keys, SPEC.md:122): an empty Secret or
// one missing the expected key fails startup when pullOnInit is true — on the
// catch-up-push path (non-empty local repo) as well as the clone path. The
// pre-flight auth check is not scoped to the clone path, so a non-empty repo
// booting with pullOnInit: true and a failing readSecretFn returns the
// pre-flight error and never flags a catch-up push.
func TestTryRemotePullOnInitCatchUpPushSecretFailureFailsStartup(t *testing.T) {
	gs := &initPullGitStore{isEmpty: false}
	secretErr := errors.New("secret unavailable")
	catchUp, err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth",
		func(context.Context, string) (map[string]string, error) { return nil, secretErr }, nil, nil)
	if !errors.Is(err, secretErr) {
		t.Fatalf("tryRemotePullOnInit error = %v, want wrapped secret error (fail startup)", err)
	}
	if catchUp {
		t.Fatal("catch-up push flagged after secret failure, want none (startup fails closed)")
	}
	if gs.pushCalls != 0 {
		t.Fatalf("push calls after secret failure on catch-up path = %d, want 0", gs.pushCalls)
	}
	if gs.cloneCalls != 0 {
		t.Fatalf("clone calls on non-empty repo = %d, want 0", gs.cloneCalls)
	}
}

// TestTryRemotePullOnInitCatchUpPushValidSecretPushes verifies the pre-flight
// auth check passes for a valid Secret on the catch-up-push path, so a
// non-empty repo booting with pullOnInit: true and a well-formed Secret still
// reports the catch-up push for the sync worker's first cycle.
func TestTryRemotePullOnInitCatchUpPushValidSecretPushes(t *testing.T) {
	gs := &initPullGitStore{isEmpty: false}
	catchUp, err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth",
		func(context.Context, string) (map[string]string, error) {
			return map[string]string{"password": "valid-pass"}, nil
		}, nil, nil)
	if err != nil {
		t.Fatalf("tryRemotePullOnInit with valid Secret: %v", err)
	}
	if !catchUp {
		t.Fatal("expected catch-up push flag with a valid Secret on a non-empty repo, got false")
	}
	if gs.pushCalls != 0 {
		t.Fatalf("direct push calls = %d, want 0 (push is deferred to the sync worker's first cycle)", gs.pushCalls)
	}
	if gs.cloneCalls != 0 {
		t.Fatalf("clone calls on non-empty repo = %d, want 0", gs.cloneCalls)
	}
}

// TestTryRemotePullOnInitStateCheckFailureNonBlocking verifies SPEC R10 Init:
// a repository-state (IsEmpty) check failure on init is logged and non-fatal —
// no clone is attempted, no catch-up push is flagged, no error blocks startup,
// and it does not call os.Exit.
func TestTryRemotePullOnInitStateCheckFailureNonBlocking(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true, initStateErr: errors.New("state check boom")}
	catchUp, err := tryRemotePullOnInit(gs, "https://public.example/repo.git", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("IsEmpty() failure blocked startup: %v", err)
	}
	if catchUp {
		t.Fatal("catch-up push flagged after state-check failure, want none (repo state unknown)")
	}
	if gs.cloneCalls != 0 {
		t.Fatalf("clone calls after state-check failure = %d, want 0", gs.cloneCalls)
	}
	if gs.pushCalls != 0 {
		t.Fatalf("push calls after state-check failure = %d, want 0", gs.pushCalls)
	}
}

// ---------------------------------------------------------------------------
// SPEC R8 startup corruption-recovery re-hydration (rehydrateMainAfterRecovery)
// ---------------------------------------------------------------------------

// recoveryGitStore is a gitstore stub for rehydrateMainAfterRecovery that
// reports the repository's commit state and hydration directories, and tracks
// the restore-main/clean-untracked steps the recovery path runs before
// re-hydration (a crash can leave the working tree stranded on a transaction
// branch, so the tree must be switched back to main before files are read).
type recoveryGitStore struct {
	gitstore.GitStore
	isEmpty      bool
	stateErr     error
	dirs         [2]string
	restoreErr   error
	cleanErr     error
	restoreCalls int
	cleanCalls   int
}

func (g *recoveryGitStore) IsEmpty(context.Context) (bool, error) { return g.isEmpty, g.stateErr }
func (g *recoveryGitStore) HydrationDirs() (string, string)       { return g.dirs[0], g.dirs[1] }
func (g *recoveryGitStore) WithGitLock(fn func() error) error     { return fn() }
func (g *recoveryGitStore) RestoreMain(context.Context) error {
	g.restoreCalls++
	return g.restoreErr
}
func (g *recoveryGitStore) CleanUntracked(context.Context) error {
	g.cleanCalls++
	return g.cleanErr
}

// recoveryStore is a store.Store stub that counts RehydrateMainFromFiles
// invocations. The transaction-only write model removed the main-graph-data
// probe (rehydrateMainAfterRecovery re-hydrates unconditionally when the repo
// is non-empty), so the stub no longer models count-query responses.
type recoveryStore struct {
	store.Store
	rehydrateCalls int
	rehydrateErr   error
}

func (s *recoveryStore) RehydrateMainFromFiles(context.Context, string, string) error {
	s.rehydrateCalls++
	return s.rehydrateErr
}

// TestRehydrateMainAfterRecovery pins the SPEC R8 re-hydration behavior: with
// the transaction-only write model there are no local-only writes to protect,
// so whenever the git repository has commits (not empty) main is re-hydrated
// from git unconditionally, and any failure must be surfaced (fail loudly)
// rather than silently serving a vacuous graph. The working tree is switched
// back to main (RestoreMain + CleanUntracked) before files are read, so a
// healthy main.lbug is never rebuilt from a stale transaction-branch snapshot.
func TestRehydrateMainAfterRecovery(t *testing.T) {
	ctx := context.Background()

	t.Run("empty git repo skips re-hydration (fresh install)", func(t *testing.T) {
		gs := &recoveryGitStore{isEmpty: true}
		st := &recoveryStore{}
		if err := rehydrateMainAfterRecovery(ctx, st, gs); err != nil {
			t.Fatalf("rehydrateMainAfterRecovery: %v", err)
		}
		if st.rehydrateCalls != 0 {
			t.Fatalf("re-hydration ran for an empty git repo: %d calls", st.rehydrateCalls)
		}
		if gs.restoreCalls != 0 || gs.cleanCalls != 0 {
			t.Fatalf("restore/clean ran for an empty git repo: restore=%d clean=%d",
				gs.restoreCalls, gs.cleanCalls)
		}
	})

	t.Run("committed git re-hydrates unconditionally", func(t *testing.T) {
		gs := &recoveryGitStore{isEmpty: false, dirs: [2]string{"entities", "edges"}}
		st := &recoveryStore{}
		if err := rehydrateMainAfterRecovery(ctx, st, gs); err != nil {
			t.Fatalf("rehydrateMainAfterRecovery: %v", err)
		}
		if st.rehydrateCalls != 1 {
			t.Fatalf("re-hydration calls = %d, want 1", st.rehydrateCalls)
		}
		if gs.restoreCalls != 1 || gs.cleanCalls != 1 {
			t.Fatalf("restore/clean before re-hydration: restore=%d clean=%d, want 1 each",
				gs.restoreCalls, gs.cleanCalls)
		}
	})

	t.Run("restore-main failure is surfaced (fail loudly)", func(t *testing.T) {
		gs := &recoveryGitStore{isEmpty: false, restoreErr: errors.New("restore boom")}
		st := &recoveryStore{}
		err := rehydrateMainAfterRecovery(ctx, st, gs)
		if err == nil {
			t.Fatal("expected restore-main failure to be surfaced, got nil")
		}
		if !strings.Contains(err.Error(), "restore boom") {
			t.Fatalf("error does not carry the restore-main failure: %v", err)
		}
		if st.rehydrateCalls != 0 {
			t.Fatalf("re-hydration ran after restore-main failure: %d calls", st.rehydrateCalls)
		}
	})

	t.Run("clean-untracked failure is surfaced (fail loudly)", func(t *testing.T) {
		gs := &recoveryGitStore{isEmpty: false, cleanErr: errors.New("clean boom")}
		st := &recoveryStore{}
		err := rehydrateMainAfterRecovery(ctx, st, gs)
		if err == nil {
			t.Fatal("expected clean-untracked failure to be surfaced, got nil")
		}
		if !strings.Contains(err.Error(), "clean boom") {
			t.Fatalf("error does not carry the clean-untracked failure: %v", err)
		}
		if st.rehydrateCalls != 0 {
			t.Fatalf("re-hydration ran after clean-untracked failure: %d calls", st.rehydrateCalls)
		}
	})

	t.Run("re-hydration failure is surfaced (fail loudly)", func(t *testing.T) {
		gs := &recoveryGitStore{isEmpty: false}
		st := &recoveryStore{rehydrateErr: errors.New("rehydrate boom")}
		err := rehydrateMainAfterRecovery(ctx, st, gs)
		if err == nil {
			t.Fatal("expected re-hydration failure to be surfaced, got nil")
		}
		if !strings.Contains(err.Error(), "rehydrate boom") {
			t.Fatalf("error does not carry the re-hydration failure: %v", err)
		}
		if gs.restoreCalls != 1 || gs.cleanCalls != 1 {
			t.Fatalf("restore/clean must run before the re-hydration attempt: restore=%d clean=%d",
				gs.restoreCalls, gs.cleanCalls)
		}
	})

	t.Run("git state check failure is surfaced", func(t *testing.T) {
		gs := &recoveryGitStore{stateErr: errors.New("state boom")}
		st := &recoveryStore{}
		err := rehydrateMainAfterRecovery(ctx, st, gs)
		if err == nil {
			t.Fatal("expected git state-check failure to be surfaced, got nil")
		}
		if st.rehydrateCalls != 0 {
			t.Fatalf("re-hydration ran after git state-check failure: %d calls", st.rehydrateCalls)
		}
		if gs.restoreCalls != 0 || gs.cleanCalls != 0 {
			t.Fatalf("restore/clean ran after git state-check failure: restore=%d clean=%d",
				gs.restoreCalls, gs.cleanCalls)
		}
	})
}

// TestRehydrateMainAfterRecoveryRestoresCommittedGraph pins SPEC R8 with real
// components: a file-backed git repository holding a committed file-per-element
// entity, and a freshly-opened (empty) file-backed main.lbug — the exact state
// ladybug.Open's corruption recovery produces (delete main.lbug, re-open
// empty). rehydrateMainAfterRecovery must restore the committed entity into
// main so the service does not serve a vacuous graph. This also pins that the
// emptiness probe (count queries) succeeds on a fresh, table-less database.
func TestRehydrateMainAfterRecoveryRestoresCommittedGraph(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	gs, err := gitstore.New(root)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	entityID := uuid.NewString()
	now := time.Now().UTC().Round(time.Millisecond)
	if err := gs.WithGitLock(func() error {
		if err := gs.WriteEntityFiles(ctx, "Component", []gitstore.Entity{
			{ID: entityID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx, "."); err != nil {
			return err
		}
		return gs.Commit(ctx, "transaction:recovery-test")
	}); err != nil {
		t.Fatalf("commit entity: %v", err)
	}
	empty, err := gs.IsEmpty(ctx)
	if err != nil || empty {
		t.Fatalf("fixture: expected non-empty git repo, empty=%v err=%v", empty, err)
	}

	dbStore, err := ladybug.Open(root)
	if err != nil {
		t.Fatalf("ladybug.Open: %v", err)
	}
	t.Cleanup(func() { _ = dbStore.Close() })

	if err := rehydrateMainAfterRecovery(ctx, dbStore, gs); err != nil {
		t.Fatalf("rehydrateMainAfterRecovery: %v", err)
	}

	ent, err := dbStore.GetEntity(ctx, entityID, "")
	if err != nil {
		t.Fatalf("committed entity was not restored into main: %v", err)
	}
	if ent.Type != "Component" {
		t.Fatalf("restored entity type = %q, want %q", ent.Type, "Component")
	}
}

// TestRehydrateMainAfterRecoveryRestoresCurrentMainNotStaleBranch pins SPEC R8
// with real components in the crash scenario that motivates the
// restore-main-before-re-hydration step: a pod killed mid-transaction leaves
// the working tree checked out on the transaction branch (BeginTransaction's
// HardResetToBranch), while main has advanced via a concurrent commit. The
// recovery path must switch the tree back to main before re-hydrating, so
// main.lbug is rebuilt from main's current files — not the stale
// transaction-branch snapshot — and committed data that landed on main after
// the transaction began survives.
func TestRehydrateMainAfterRecoveryRestoresCurrentMainNotStaleBranch(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	gs, err := gitstore.New(root)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}

	now := time.Now().UTC().Round(time.Millisecond)
	commitEntity := func(id string) error {
		return gs.WithGitLock(func() error {
			if err := gs.WriteEntityFiles(ctx, "Component", []gitstore.Entity{
				{ID: id, Type: "Component", CreatedAt: now, UpdatedAt: now},
			}); err != nil {
				return err
			}
			if err := gs.AddAll(ctx, "."); err != nil {
				return err
			}
			return gs.Commit(ctx, "transaction:recovery-"+id)
		})
	}

	// 1. Commit entity A to main — the graph state when the transaction began.
	entityA := uuid.NewString()
	if err := commitEntity(entityA); err != nil {
		t.Fatalf("commit entity A to main: %v", err)
	}

	// 2. A transaction begins: branch tx1 is created from main and the working
	// tree is hard-reset onto it (BeginTransaction's HardResetToBranch), so the
	// tree now shows only entity A.
	txID := uuid.NewString()
	if err := gs.WithGitLock(func() error {
		if err := gs.CreateBranch(ctx, txID); err != nil {
			return err
		}
		return gs.HardResetToBranch(ctx, txID)
	}); err != nil {
		t.Fatalf("begin transaction (create branch + hard reset): %v", err)
	}

	// 3. main advances via a concurrent commit of entity B: restore main, write
	// B, commit — then hard-reset the tree back onto tx1, simulating the crash
	// state where the tree sits on the stale transaction-branch snapshot while
	// main's ref points at a commit containing both A and B.
	entityB := uuid.NewString()
	if err := gs.WithGitLock(func() error {
		if err := gs.RestoreMain(ctx); err != nil {
			return err
		}
		if err := gs.WriteEntityFiles(ctx, "Component", []gitstore.Entity{
			{ID: entityB, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx, "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx, "transaction:recovery-"+entityB); err != nil {
			return err
		}
		return gs.HardResetToBranch(ctx, txID)
	}); err != nil {
		t.Fatalf("advance main then strand tree on tx1: %v", err)
	}

	// Guard: the working tree must still be on the stale snapshot (only A
	// visible) while main's ref has advanced, or this test is not exercising
	// the restore-before-read scenario it claims to.
	var treeFiles []gitstore.EntityFile
	if err := gs.WithGitLock(func() error {
		var err error
		treeFiles, err = gs.ReadAllEntityFiles(ctx, "Component")
		return err
	}); err != nil {
		t.Fatalf("read tree entities for fixture guard: %v", err)
	}
	if len(treeFiles) != 1 || treeFiles[0].ID != entityA {
		t.Fatalf("fixture: expected the tree to show only entity A (stale snapshot), got %+v", treeFiles)
	}

	dbStore, err := ladybug.Open(root)
	if err != nil {
		t.Fatalf("ladybug.Open: %v", err)
	}
	t.Cleanup(func() { _ = dbStore.Close() })

	if err := rehydrateMainAfterRecovery(ctx, dbStore, gs); err != nil {
		t.Fatalf("rehydrateMainAfterRecovery: %v", err)
	}

	// Both entities must be present: B survives because the tree was restored
	// to main before files were read; without that step main.lbug would be
	// rebuilt from the stale tx1 snapshot and B would be silently lost.
	for _, id := range []string{entityA, entityB} {
		if _, err := dbStore.GetEntity(ctx, id, ""); err != nil {
			t.Fatalf("entity %s missing after recovery (main.lbug rebuilt from stale branch snapshot?): %v", id, err)
		}
	}
}

// telemetrySpy implements flowv1.FlowEventBusServiceClient, capturing every
// PublishRequest so tests can assert the telemetry events tryRemotePullOnInit
// submits on startup failures (SPEC R1/R10).
type telemetrySpy struct {
	flowv1.FlowEventBusServiceClient

	mu    sync.Mutex
	calls []*flowv1.PublishRequest
}

func (s *telemetrySpy) Publish(
	_ context.Context, req *flowv1.PublishRequest, _ ...grpc.CallOption,
) (*flowv1.PublishResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, req)
	return &flowv1.PublishResponse{Acknowledged: true}, nil
}

func (s *telemetrySpy) getCalls() []*flowv1.PublishRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*flowv1.PublishRequest, len(s.calls))
	copy(out, s.calls)
	return out
}

// newTestAuditPub builds a real AsyncPublisher over a telemetrySpy so
// tryRemotePullOnInit's telemetry-publish branches are exercised end to end.
// The publisher drains asynchronously, so callers poll waitForTelemetry.
func newTestAuditPub(t *testing.T) (*telemetrySpy, *eventbus.AsyncPublisher) {
	t.Helper()
	spy := &telemetrySpy{}
	pub := eventbus.NewAsyncPublisher(spy, eventbus.WithBufferSize(10))
	t.Cleanup(pub.Stop)
	return spy, pub
}

// waitForTelemetry polls the spy until a PublishRequest with the given event
// type is published, then returns it.
func waitForTelemetry(t *testing.T, spy *telemetrySpy, eventType string) *flowv1.PublishRequest {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, req := range spy.getCalls() {
			if req.GetEvent().GetEventType() == eventType {
				return req
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no telemetry event %q published within deadline; got %d publish calls",
		eventType, len(spy.getCalls()))
	return nil
}

// TestTryRemotePullOnInitCloneFailurePublishesTelemetry verifies SPEC R1: a
// startup clone failure publishes a "cartographer.clone_failed" telemetry
// event on the Event Bus (via the async publisher) while startup stays
// non-blocking.
func TestTryRemotePullOnInitCloneFailurePublishesTelemetry(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true, cloneErr: errors.New("clone boom")}
	spy, pub := newTestAuditPub(t)
	catchUp, err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth",
		func(context.Context, string) (map[string]string, error) {
			return map[string]string{"password": "expired"}, nil
		}, pub, nil)
	if err != nil {
		t.Fatalf("clone failure blocked startup: %v", err)
	}
	if catchUp {
		t.Fatal("failed clone path must not flag a catch-up push")
	}
	if gs.cloneCalls != 1 {
		t.Fatalf("clone calls = %d, want 1", gs.cloneCalls)
	}
	req := waitForTelemetry(t, spy, "cartographer.clone_failed")
	if req.GetChannel() != "telemetry" {
		t.Fatalf("telemetry channel = %q, want %q", req.GetChannel(), "telemetry")
	}
	if got := req.GetEvent().GetAttributes()["url"]; got != "https://private.example/repo.git" {
		t.Fatalf("telemetry url attribute = %q, want the remote URL", got)
	}
	if got := req.GetEvent().GetAttributes()["error"]; got != "clone boom" {
		t.Fatalf("telemetry error attribute = %q, want %q", got, "clone boom")
	}
}

// TestTryRemotePullOnInitCatchUpPushEmitsNoTelemetry verifies the init path
// publishes no "cartographer.push_failed" telemetry on the catch-up path: the
// catch-up push is deferred to the sync worker's first cycle (SPEC R10 Init /
// GIT_PLAN.md:33), which is the sole push-failure emitter, so a startup must
// not report the same push through two emitters.
func TestTryRemotePullOnInitCatchUpPushEmitsNoTelemetry(t *testing.T) {
	gs := &initPullGitStore{isEmpty: false}
	spy, pub := newTestAuditPub(t)
	catchUp, err := tryRemotePullOnInit(gs, "https://public.example/repo.git", "", nil, pub, nil)
	if err != nil {
		t.Fatalf("tryRemotePullOnInit: %v", err)
	}
	if !catchUp {
		t.Fatal("expected catch-up push flag on a non-empty repo, got false")
	}
	if gs.pushCalls != 0 {
		t.Fatalf("direct push calls = %d, want 0 (push is deferred to the sync worker's first cycle)", gs.pushCalls)
	}
	// The async publisher gets a beat to drain; the init path must never
	// submit a push-failure event (that telemetry belongs to the worker).
	time.Sleep(50 * time.Millisecond)
	if calls := spy.getCalls(); len(calls) != 0 {
		t.Fatalf("tryRemotePullOnInit published %d telemetry events on the catch-up path, want 0: %+v",
			len(calls), calls)
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

// TestBuildResolveAuthFnParseURLFailure verifies the url.Parse error branch of
// buildResolveAuthFn (main.go:461-463): a remote URL the parser rejects must
// surface the parse error (never a sentinel, never a nil auth) and stay in the
// error branch, so a malformed remote URL cannot silently fall through to
// anonymous/unauthenticated access.
func TestBuildResolveAuthFnURLParseFailure(t *testing.T) {
	readSecretFn := func(ctx context.Context, name string) (map[string]string, error) {
		return map[string]string{"username": tSecretUsername, "password": tSecretPassword}, nil
	}
	// malformedURL contains a NUL control character, which url.Parse rejects
	// ("net/url: invalid control character in URL").
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
	})
}

// ---------------------------------------------------------------------------
// newReadSecretFn (SPEC R1 Secret read) tests
// ---------------------------------------------------------------------------

// TestNewReadSecretFn verifies SPEC R1 (SPEC.md:103): the Cartographer reads
// the remote-auth Secret via its pod's ServiceAccount on each remote operation.
// This is the only direct test of the real k8s wrapper — newReadSecretFn
// (main.go:887-901) — exercised through the fake clientset, the in-memory twin
// of the pod's ServiceAccount reader. Every other test injects a hand-rolled
// readSecretFn closure, so the Secret fetch by name in the pod namespace and
// the Data byte→string decoding are untested elsewhere.
func TestNewReadSecretFn(t *testing.T) {
	ctx := context.Background()
	const (
		namespace = "test-ns"
		secretRef = "remote-auth"
	)
	cs := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretRef, Namespace: namespace},
		Data: map[string][]byte{
			"username": []byte(tSecretUsername),
			"password": []byte(tSecretPassword),
			"ignored":  []byte("extra-key"),
		},
	})

	got, err := newReadSecretFn(cs, namespace)(ctx, secretRef)
	if err != nil {
		t.Fatalf("read Secret via fake clientset: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("decoded Secret keys = %d, want 3 (every Data key surfaced)", len(got))
	}
	if got["username"] != tSecretUsername {
		t.Errorf("decoded username = %q, want %q", got["username"], tSecretUsername)
	}
	if got["password"] != tSecretPassword {
		t.Errorf("decoded password = %q, want %q", got["password"], tSecretPassword)
	}
	// The scheme filter (which keys matter) lives in buildResolveAuthFn, not
	// the reader: a Data key the URL scheme ignores must still be surfaced.
	if got["ignored"] != "extra-key" {
		t.Errorf("decoded ignored key = %q, want %q", got["ignored"], "extra-key")
	}
}

// TestNewReadSecretFnNotFoundPropagates verifies the k8s error-propagation
// branch of newReadSecretFn: a Secret absent from the pod namespace surfaces
// the clientset's not-found StatusError unchanged (never a nil error, never a
// partial map) so the callers — buildResolveAuthFn and the pre-flight checks —
// fail closed on it.
func TestNewReadSecretFnNotFoundPropagates(t *testing.T) {
	cs := fake.NewSimpleClientset() // no Secrets in the pod namespace
	got, err := newReadSecretFn(cs, "test-ns")(context.Background(), "remote-auth")
	if err == nil {
		t.Fatal("expected a k8s not-found error for an absent Secret, got nil")
	}
	if got != nil {
		t.Fatalf("expected nil map on error, got %v", got)
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("error = %v, want a k8s not-found StatusError", err)
	}
}

// ---------------------------------------------------------------------------
// Graceful shutdown path (SPEC CQs)
// ---------------------------------------------------------------------------

// shutdownStore is a store.Store stub that tracks Close, the only durability
// call the shutdown path makes on the main store.
type shutdownStore struct {
	store.Store
	closeCalls int
}

func (s *shutdownStore) Close() error { s.closeCalls++; return nil }

// shutdownGitStore is a gitstore.GitStore stub that tracks the durability
// calls waitForShutdown performs during teardown.
type shutdownGitStore struct {
	gitstore.GitStore
	restoreCalls int
	cleanCalls   int
	closeCalls   int
}

func (g *shutdownGitStore) WithGitLock(fn func() error) error { return fn() }
func (g *shutdownGitStore) RestoreMain(context.Context) error { g.restoreCalls++; return nil }
func (g *shutdownGitStore) CleanUntracked(context.Context) error {
	g.cleanCalls++
	return nil
}
func (g *shutdownGitStore) Close() error { g.closeCalls++; return nil }

// lockErrGitStore is a gitstore.GitStore stub whose WithGitLock reports a lock
// acquisition failure without invoking the closure, so the teardown's
// working-tree branch is never reached. It verifies shutdown itself still runs
// to completion (Close is reached) while the lock/Restore/Clean failures are
// no longer silently swallowed.
type lockErrGitStore struct {
	shutdownGitStore
	lockErr error
}

func (g *lockErrGitStore) WithGitLock(fn func() error) error { return g.lockErr }

// TestIsFatalServeError verifies the Serve-return classification. A nil return
// (normal graceful stop) and grpc.ErrServerStopped (the startup-race graceful
// stop, and the case the shutdown goroutine's GracefulStop/Stop produces) must
// NOT be treated as fatal, so main falls through to the teardown join instead
// of os.Exit(1). A genuine serve failure still aborts.
func TestIsFatalServeError(t *testing.T) {
	if isFatalServeError(nil) {
		t.Error("nil Serve return must not be fatal (normal GracefulStop)")
	}
	if isFatalServeError(grpc.ErrServerStopped) {
		t.Error("grpc.ErrServerStopped must be treated as a normal shutdown, not fatal")
	}
	if !isFatalServeError(errors.New("accept: connection refused")) {
		t.Error("genuine serve failure must be fatal")
	}
}

// TestWaitForShutdownTeardownCompletes drives the real graceful-shutdown path
// end to end. It mirrors the main() serve loop: a real grpc.Server.Serve runs
// concurrently with waitForShutdown, and a signal fires the shutdown. The
// signal handler may or may not beat Serve's listener registration (the buggy
// startup race), so Serve legitimately returns either nil or grpc.ErrServerStopped
// — both must classify as non-fatal so main falls through to the teardown join
// instead of os.Exit(1), and the durability teardown (dbStore.Close, git
// RestoreMain/CleanUntracked/Close) must complete.
func TestWaitForShutdownTeardownCompletes(t *testing.T) {
	db := &shutdownStore{}
	gs := &shutdownGitStore{}
	server := service.NewCartographerServer(db, gs, nil, nil, nil, "", 30*time.Second,
		"default", 30*time.Second, store.DefaultChangeLogCap)

	healthSrv := health.NewServer()
	grpcServer := grpc.NewServer()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpc_health_v1.RegisterHealthServer(grpcServer, healthSrv)
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	sigCh := make(chan os.Signal, 1)
	shutdownDone := make(chan struct{})
	go waitForShutdown(shutdownDone, sigCh, healthSrv, grpcServer, server, db, gs, nil, nil, nil)

	serveErr := make(chan error, 1)
	go func() { serveErr <- grpcServer.Serve(lis) }()

	// Let Serve register, then drive the same shutdown the OS signal wiring
	// would.
	time.Sleep(50 * time.Millisecond)
	sigCh <- syscall.SIGTERM

	select {
	case err := <-serveErr:
		if isFatalServeError(err) {
			t.Fatalf("graceful stop Serve returned %v, classified fatal (should exit 0 and join teardown)", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after graceful stop")
	}

	// The teardown join must be reachable: shutdownDone closes only after the
	// durability teardown (dbStore.Close, git RestoreMain/CleanUntracked/Close)
	// has run.
	select {
	case <-shutdownDone:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown/drain wiring did not complete after graceful stop (teardown join unreachable)")
	}

	if db.closeCalls != 1 {
		t.Errorf("dbStore.Close calls = %d, want 1 (durability teardown skipped)", db.closeCalls)
	}
	if gs.restoreCalls != 1 {
		t.Errorf("git RestoreMain calls = %d, want 1", gs.restoreCalls)
	}
	if gs.cleanCalls != 1 {
		t.Errorf("git CleanUntracked calls = %d, want 1", gs.cleanCalls)
	}
	if gs.closeCalls != 1 {
		t.Errorf("git Close calls = %d, want 1", gs.closeCalls)
	}
}

// TestWaitForShutdownLockFailureStillCompletes verifies that a git lock
// acquisition failure during shutdown is propagated (no longer `_ =`-discarded)
// while the teardown still completes. WithGitLock failing means RestoreMain and
// CleanUntracked are never invoked (the block is untouched under a failed
// lock), but the durability teardown must continue past the git step: the main
// db Close still runs and the shutdownDone join is still reached, so the
// process does not hang and the operator gets a distinct log line correlating
// the stranded tree.
func TestWaitForShutdownLockFailureStillCompletes(t *testing.T) {
	db := &shutdownStore{}
	gs := &lockErrGitStore{lockErr: errors.New("git lock acquisition failed")}
	server := service.NewCartographerServer(db, gs, nil, nil, nil, "", 30*time.Second,
		"default", 30*time.Second, store.DefaultChangeLogCap)

	healthSrv := health.NewServer()
	grpcServer := grpc.NewServer()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpc_health_v1.RegisterHealthServer(grpcServer, healthSrv)
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	sigCh := make(chan os.Signal, 1)
	shutdownDone := make(chan struct{})
	go waitForShutdown(shutdownDone, sigCh, healthSrv, grpcServer, server, db, gs, nil, nil, nil)

	serveErr := make(chan error, 1)
	go func() { serveErr <- grpcServer.Serve(lis) }()

	time.Sleep(50 * time.Millisecond)
	sigCh <- syscall.SIGTERM

	select {
	case err := <-serveErr:
		if isFatalServeError(err) {
			t.Fatalf("graceful stop Serve returned %v, classified fatal", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after graceful stop")
	}

	select {
	case <-shutdownDone:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not complete after lock failure (teardown join unreachable)")
	}

	// The failed lock blocks the tree-branch but must not halt the teardown.
	if db.closeCalls != 1 {
		t.Errorf("dbStore.Close calls = %d, want 1 (teardown must not abort before Close)", db.closeCalls)
	}
	if gs.closeCalls != 1 {
		t.Errorf("git Close calls = %d, want 1", gs.closeCalls)
	}
	// Under a failed lock the working-tree branch is never entered: the lock
	// error is now surfaced instead of silently dropped, so Restore/Clean are
	// correctly skipped (we are not falsely claiming a clean tree).
	if gs.restoreCalls != 0 {
		t.Errorf("git RestoreMain calls under failed lock = %d, want 0", gs.restoreCalls)
	}
	if gs.cleanCalls != 0 {
		t.Errorf("git CleanUntracked calls under failed lock = %d, want 0", gs.cleanCalls)
	}
}
