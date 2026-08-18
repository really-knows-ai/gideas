package main

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/service"
)

func TestTryRemotePullOnInitAnonymous(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true}
	catchUp, err := tryRemotePullOnInit(gs, "https://public.example/repo.git", "", "", nil, nil, nil)
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

// TestTryRemotePullOnInitCloneBounded pins the clone-on-init deadline: the
// clone is a git operation, so it carries the same per-operation deadline the
// sync worker applies to every git op (service.DefaultGitOperationTimeout) —
// a hung remote aborts the clone with a context deadline instead of blocking
// startup forever (clone failures are then logged and non-blocking per SPEC
// R10 Init).
func TestTryRemotePullOnInitCloneBounded(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true}
	if _, err := tryRemotePullOnInit(gs, "https://public.example/repo.git", "", "", nil, nil, nil); err != nil {
		t.Fatalf("tryRemotePullOnInit: %v", err)
	}
	if gs.cloneCalls != 1 {
		t.Fatalf("clone calls = %d, want 1", gs.cloneCalls)
	}
	if gs.cloneCtx == nil {
		t.Fatal("expected the clone to receive a context")
	}
	deadline, ok := gs.cloneCtx.Deadline()
	if !ok {
		t.Fatal("expected the clone-on-init context to carry a deadline (bounded clone), got none")
	}
	wantDeadline := time.Now().Add(service.DefaultGitOperationTimeout)
	if deadline.Before(time.Now()) {
		t.Fatalf("clone deadline %v is already in the past", deadline)
	}
	if delta := wantDeadline.Sub(deadline); delta > 5*time.Second {
		t.Fatalf("clone deadline %v is more than 5s before the expected %v (wrong timeout?)", deadline, wantDeadline)
	}
}

// TestTryRemotePullOnInitPreflightReadBounded pins the pre-flight Secret-read
// deadline: the boot-path auth check is a network-touching boot step, so its
// Secret read carries the same per-operation deadline the sync worker applies
// to every git operation (service.DefaultGitOperationTimeout, SPEC R10 /
// SPEC:981) — a hung or unreachable k8s API server must fail startup within a
// bounded window instead of blocking it indefinitely.
func TestTryRemotePullOnInitPreflightReadBounded(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true}
	var readCtx context.Context
	readSecretFn := func(ctx context.Context, name string) (map[string]string, error) {
		readCtx = ctx
		return map[string]string{"password": "valid-pass"}, nil
	}
	if _, err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth", "",
		readSecretFn, nil, nil); err != nil {
		t.Fatalf("tryRemotePullOnInit: %v", err)
	}
	if gs.cloneCalls != 1 {
		t.Fatalf("clone calls = %d, want 1 (pre-flight passed, clone ran)", gs.cloneCalls)
	}
	if readCtx == nil {
		t.Fatal("expected the pre-flight Secret read to receive a context, got nil")
	}
	deadline, ok := readCtx.Deadline()
	if !ok {
		t.Fatal("expected the pre-flight Secret read to carry a deadline (bounded read), got none")
	}
	wantDeadline := time.Now().Add(service.DefaultGitOperationTimeout)
	if deadline.Before(time.Now()) {
		t.Fatalf("pre-flight read deadline %v is already in the past", deadline)
	}
	if delta := wantDeadline.Sub(deadline); delta > 5*time.Second {
		t.Fatalf("pre-flight read deadline %v deviates %v from %v (wrong timeout?)", deadline, delta, wantDeadline)
	}
}

func TestTryRemotePullOnInitConfiguredSecretFailure(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true}
	secretErr := errors.New("secret unavailable")
	_, err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth", "",
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
	_, err := tryRemotePullOnInit(gs, "ssh://git@github.com/org/repo.git", "remote-auth", "",
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
	_, err := tryRemotePullOnInit(gs, "ftp://example.com/repo.git", "remote-auth", "",
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
	catchUp, err := tryRemotePullOnInit(gs, "file:///tmp/repo.git", "remote-auth", "",
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
	// The file:// short-circuit precedes the Secret read: even a failing
	// reader must not block a file:// remote (SPEC.md:91-100 defines auth keys
	// only for ssh:// and https://).
	failGS := &initPullGitStore{isEmpty: true}
	catchUp, err = tryRemotePullOnInit(failGS, "file:///tmp/repo.git", "remote-auth", "",
		func(context.Context, string) (map[string]string, error) {
			return nil, errors.New("secret unavailable")
		}, nil, nil)
	if err != nil {
		t.Fatalf("file:// pre-flight with failing reader error = %v, want nil (anonymous)", err)
	}
	if catchUp {
		t.Fatal("file:// clone path with failing reader must not flag a catch-up push")
	}
	if failGS.cloneCalls != 1 {
		t.Fatalf("file:// clone calls with failing reader = %d, want 1", failGS.cloneCalls)
	}
}

// TestTryRemotePullOnInitHTTPSMissingPasswordFailsClosed verifies the
// https missing/empty-password branch of tryRemotePullOnInit's pre-flight auth
// resolver: an https remote whose Secret lacks a password fails closed with
// gitstore.ErrAuthConfigMissing before any git operation is attempted.
func TestTryRemotePullOnInitHTTPSMissingPasswordFailsClosed(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true}
	_, err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth", "",
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
	_, err := tryRemotePullOnInit(gs, malformedURL, "remote-auth", "",
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
	_, err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth", "", nil, nil, nil)
	if !errors.Is(err, gitstore.ErrAuthConfigMissing) {
		t.Fatalf("nil-readSecretFn pre-flight error = %v, want ErrAuthConfigMissing", err)
	}
	if gs.cloneCalls != 0 {
		t.Fatalf("clone calls after nil-readSecretFn rejection = %d, want 0", gs.cloneCalls)
	}
}

func TestTryRemotePullOnInitPrivateRemoteAuthFailureIsNonBlocking(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true, cloneErr: gitstore.ErrAuthFailed}
	catchUp, err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth", "",
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
	catchUp, err := tryRemotePullOnInit(gs, "https://public.example/repo.git", "", "", nil, nil,
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
	_, err := tryRemotePullOnInit(gs, "https://public.example/repo.git", "", "", nil, nil,
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
	catchUp, err := tryRemotePullOnInit(gs, "https://public.example/repo.git", "", "", nil, nil, nil)
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
	catchUp, err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth", "",
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
	catchUp, err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth", "",
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
	catchUp, err := tryRemotePullOnInit(gs, "https://public.example/repo.git", "", "", nil, nil, nil)
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

// TestStartupCatchUpPushNeeded verifies the pullOnInit=false startup catch-up
// push decision (SPEC R10 Init, SPEC.md:640-641): a non-empty local repo —
// which may hold unsent commits from a prior pod lifetime — must be flagged
// for the sync worker's first cycle, independent of pullOnInit, while an empty
// repo (nothing local to push) and a failed repo-state check (repo state
// unknown, non-blocking) must not.
func TestStartupCatchUpPushNeeded(t *testing.T) {
	ctx := context.Background()

	t.Run("non-empty repo needs a catch-up push", func(t *testing.T) {
		gs := &initPullGitStore{isEmpty: false}
		if !startupCatchUpPushNeeded(ctx, gs) {
			t.Fatal("expected catch-up push for a non-empty repo (unsent commits from a prior pod lifetime)")
		}
	})

	t.Run("empty repo needs no catch-up push", func(t *testing.T) {
		gs := &initPullGitStore{isEmpty: true}
		if startupCatchUpPushNeeded(ctx, gs) {
			t.Fatal("expected no catch-up push for an empty repo (nothing local to push)")
		}
	})

	t.Run("state-check failure is non-blocking", func(t *testing.T) {
		gs := &initPullGitStore{isEmpty: false, initStateErr: errors.New("state check boom")}
		if startupCatchUpPushNeeded(ctx, gs) {
			t.Fatal("expected no catch-up push after a repo-state check failure (repo state unknown)")
		}
	})
}
