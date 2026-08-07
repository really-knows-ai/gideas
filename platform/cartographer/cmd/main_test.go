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

// TestTryRemotePullOnInitSSHEmptyKeyFailsClosed verifies the ssh-empty-key
// branch of tryRemotePullOnInit's pre-flight auth resolver: a present-but-empty
// ssh-privatekey Secret is equivalent to an absent one and fail-closes with
// gitstore.ErrAuthConfigMissing before any git operation is attempted.
func TestTryRemotePullOnInitSSHEmptyKeyFailsClosed(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true}
	err := tryRemotePullOnInit(gs, "ssh://git@github.com/org/repo.git", "remote-auth",
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
	err := tryRemotePullOnInit(gs, "ftp://example.com/repo.git", "remote-auth",
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

// TestTryRemotePullOnInitHTTPSMissingPasswordFailsClosed verifies the
// https missing/empty-password branch of tryRemotePullOnInit's pre-flight auth
// resolver: an https remote whose Secret lacks a password fails closed with
// gitstore.ErrAuthConfigMissing before any git operation is attempted.
func TestTryRemotePullOnInitHTTPSMissingPasswordFailsClosed(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true}
	err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth",
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
	err := tryRemotePullOnInit(gs, malformedURL, "remote-auth",
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
	err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth", nil, nil, nil)
	if !errors.Is(err, gitstore.ErrAuthConfigMissing) {
		t.Fatalf("nil-readSecretFn pre-flight error = %v, want ErrAuthConfigMissing", err)
	}
	if gs.cloneCalls != 0 {
		t.Fatalf("clone calls after nil-readSecretFn rejection = %d, want 0", gs.cloneCalls)
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

// TestTryRemotePullOnInitCatchUpPushSecretFailureDeferred verifies SPEC R10
// Init: on the catch-up-push path (non-empty local repo) a missing or invalid
// Secret must log and defer — it never aborts startup. Pre-flight auth
// failures are scoped to the clone path only, so a non-empty repo booting with
// pullOnInit: true and a failing readSecretFn still attempts the push.
func TestTryRemotePullOnInitCatchUpPushSecretFailureDeferred(t *testing.T) {
	gs := &initPullGitStore{isEmpty: false}
	secretErr := errors.New("secret unavailable")
	err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth",
		func(context.Context, string) (map[string]string, error) { return nil, secretErr }, nil, nil)
	if err != nil {
		t.Fatalf("secret failure on catch-up-push path blocked startup: %v", err)
	}
	if gs.pushCalls != 1 {
		t.Fatalf("catch-up push calls = %d, want 1 (push still attempted)", gs.pushCalls)
	}
	if gs.cloneCalls != 0 {
		t.Fatalf("clone calls on non-empty repo = %d, want 0", gs.cloneCalls)
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
// SPEC R8 startup corruption-recovery re-hydration (rehydrateMainAfterRecovery)
// ---------------------------------------------------------------------------

// recoveryGitStore is a gitstore stub for rehydrateMainAfterRecovery that
// reports the repository's commit state and hydration directories.
type recoveryGitStore struct {
	gitstore.GitStore
	isEmpty  bool
	stateErr error
	dirs     [2]string
}

func (g *recoveryGitStore) IsEmpty(context.Context) (bool, error) { return g.isEmpty, g.stateErr }
func (g *recoveryGitStore) HydrationDirs() (string, string)       { return g.dirs[0], g.dirs[1] }

// recoveryStore is a store.Store stub that reports whether main holds graph
// data (via the same count queries rehydrateMainAfterRecovery issues) and
// counts RehydrateMainFromFiles invocations.
type recoveryStore struct {
	store.Store
	hasEntities    bool
	hasEdges       bool
	cypherErr      error
	rehydrateCalls int
	rehydrateErr   error
}

func (s *recoveryStore) ExecuteCypher(
	_ context.Context, cypher string, _ map[string]any, _ string,
) ([]store.CypherRow, error) {
	if s.cypherErr != nil {
		return nil, s.cypherErr
	}
	count := float64(0)
	if strings.Contains(cypher, "-[r]->") {
		if s.hasEdges {
			count = 1
		}
	} else if s.hasEntities {
		count = 1
	}
	return []store.CypherRow{{Values: []any{count}}}, nil
}

func (s *recoveryStore) RehydrateMainFromFiles(context.Context, string, string) error {
	s.rehydrateCalls++
	return s.rehydrateErr
}

// TestRehydrateMainAfterRecovery pins the SPEC R8 gating of the startup
// re-hydration: it must run only when the git repository has commits AND main
// holds no graph data, and any failure must be surfaced (fail loudly) rather
// than silently serving a vacuous graph.
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
	})

	t.Run("populated main skips re-hydration (protects uncommitted writes)", func(t *testing.T) {
		gs := &recoveryGitStore{isEmpty: false}
		st := &recoveryStore{hasEntities: true}
		if err := rehydrateMainAfterRecovery(ctx, st, gs); err != nil {
			t.Fatalf("rehydrateMainAfterRecovery: %v", err)
		}
		if st.rehydrateCalls != 0 {
			t.Fatalf("re-hydration ran for a populated main: %d calls", st.rehydrateCalls)
		}
	})

	t.Run("main holding only edges also skips re-hydration", func(t *testing.T) {
		gs := &recoveryGitStore{isEmpty: false}
		st := &recoveryStore{hasEdges: true}
		if err := rehydrateMainAfterRecovery(ctx, st, gs); err != nil {
			t.Fatalf("rehydrateMainAfterRecovery: %v", err)
		}
		if st.rehydrateCalls != 0 {
			t.Fatalf("re-hydration ran for a main holding edges: %d calls", st.rehydrateCalls)
		}
	})

	t.Run("empty main with committed git re-hydrates", func(t *testing.T) {
		gs := &recoveryGitStore{isEmpty: false, dirs: [2]string{"entities", "edges"}}
		st := &recoveryStore{}
		if err := rehydrateMainAfterRecovery(ctx, st, gs); err != nil {
			t.Fatalf("rehydrateMainAfterRecovery: %v", err)
		}
		if st.rehydrateCalls != 1 {
			t.Fatalf("re-hydration calls = %d, want 1", st.rehydrateCalls)
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
	})

	t.Run("count-query failure is surfaced", func(t *testing.T) {
		gs := &recoveryGitStore{isEmpty: false}
		st := &recoveryStore{cypherErr: errors.New("count boom")}
		err := rehydrateMainAfterRecovery(ctx, st, gs)
		if err == nil {
			t.Fatal("expected count-query failure to be surfaced, got nil")
		}
		if st.rehydrateCalls != 0 {
			t.Fatalf("re-hydration ran after count-query failure: %d calls", st.rehydrateCalls)
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
	err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth",
		func(context.Context, string) (map[string]string, error) {
			return map[string]string{"password": "expired"}, nil
		}, pub, nil)
	if err != nil {
		t.Fatalf("clone failure blocked startup: %v", err)
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

// TestTryRemotePullOnInitPushFailurePublishesTelemetry verifies SPEC R10: a
// failed catch-up push on init publishes a "cartographer.push_failed"
// telemetry event while startup stays non-blocking.
func TestTryRemotePullOnInitPushFailurePublishesTelemetry(t *testing.T) {
	gs := &initPullGitStore{isEmpty: false, pushErr: errors.New("push boom")}
	spy, pub := newTestAuditPub(t)
	err := tryRemotePullOnInit(gs, "https://public.example/repo.git", "", nil, pub, nil)
	if err != nil {
		t.Fatalf("catch-up push failure blocked startup: %v", err)
	}
	if gs.pushCalls != 1 {
		t.Fatalf("push calls = %d, want 1", gs.pushCalls)
	}
	req := waitForTelemetry(t, spy, "cartographer.push_failed")
	if req.GetChannel() != "telemetry" {
		t.Fatalf("telemetry channel = %q, want %q", req.GetChannel(), "telemetry")
	}
	if got := req.GetEvent().GetAttributes()["url"]; got != "https://public.example/repo.git" {
		t.Fatalf("telemetry url attribute = %q, want the remote URL", got)
	}
	if got := req.GetEvent().GetAttributes()["error"]; got != "push boom" {
		t.Fatalf("telemetry error attribute = %q, want %q", got, "push boom")
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
		"default", 30*time.Second, 100000)

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
	go waitForShutdown(shutdownDone, sigCh, healthSrv, grpcServer, server, db, gs, nil, nil)

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
		"default", 30*time.Second, 100000)

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
	go waitForShutdown(shutdownDone, sigCh, healthSrv, grpcServer, server, db, gs, nil, nil)

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
