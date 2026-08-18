package main

// Graceful shutdown path (SPEC CQs)

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/service"
	"github.com/foundry/flow/cartographer/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

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
}

func (g *shutdownGitStore) WithGitLock(fn func() error) error { return fn() }
func (g *shutdownGitStore) RestoreMain(context.Context) error { g.restoreCalls++; return nil }
func (g *shutdownGitStore) CleanUntracked(context.Context) error {
	g.cleanCalls++
	return nil
}

// lockErrGitStore is a gitstore.GitStore stub whose WithGitLock reports a lock
// acquisition failure without invoking the closure, so the teardown's
// working-tree branch is never reached. It verifies shutdown itself still runs
// to completion while the lock/Restore/Clean failures are no longer silently
// swallowed.
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
// RestoreMain/CleanUntracked) must complete.
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
	// durability teardown (dbStore.Close, git RestoreMain/CleanUntracked)
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
