package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

func TestWipeGraph_Clean(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	_, err := srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{})
	if err != nil {
		t.Fatalf("WipeGraph on empty graph failed: %v", err)
	}
}

// TestWipeGraph_CommitsDeletionWithMessageWipe pins the SPEC R2 WipeGraph
// contract "commits the deletion with message \"wipe\"" (SPEC:207): the git
// wipe commit must carry the exact "wipe" message. A regression that changed
// or dropped the wipe commit message (or skipped the commit entirely) would
// fail this test.
func TestWipeGraph_CommitsDeletionWithMessageWipe(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()
	ctx := context.Background()
	applyTestSchema(ctx, t, st)
	// Establish a non-empty git main so the wipe has content to delete.
	commitGitEntity(ctx, t, gs, testMutationEntityID, "pre-wipe")

	if _, err := srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{}); err != nil {
		t.Fatalf("WipeGraph: %v", err)
	}
	logs, err := gs.GitLogOneline(ctx, "wipe")
	if err != nil {
		t.Fatalf("GitLogOneline: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected exactly one wipe commit, got %d: %v", len(logs), logs)
	}
	if !strings.Contains(logs[0], "wipe") {
		t.Fatalf("wipe commit does not carry the SPEC \"wipe\" message: %q", logs[0])
	}
}

// TestWipeGraph_SetsPushNeeded pins the SPEC R10 push contract for WipeGraph's
// wipe commit: the wipe is a mutation-making commit on main ("backing up every
// committed change", SPEC R10), so it must set the sync worker's push-needed
// flag. Without the flag the remote backup retains the pre-wipe graph
// indefinitely, and a manual reprovision from the remote (R10 Init clone)
// would resurrect exactly the data the destructive change deleted.
func TestWipeGraph_SetsPushNeeded(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs}
	srv, fc := newSyncServer(t, syncGit)
	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	ctx := context.Background()
	applyTestSchema(ctx, t, srv.store)
	if _, err := srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{}); err != nil {
		t.Fatalf("WipeGraph: %v", err)
	}
	if !srv.syncWorker.pushNeeded() {
		t.Fatal("push flag not set after WipeGraph's wipe commit")
	}
	// The next timer cycle must deliver the push and clear the flag.
	fc.FireTicker()
	waitFor(t, func() bool { return !srv.syncWorker.pushNeeded() }, "push flag cleared after cycle")
	syncGit.mu.Lock()
	pushCalls := syncGit.pushCalls
	syncGit.mu.Unlock()
	if pushCalls != 1 {
		t.Fatalf("expected exactly 1 push after the wipe, got %d", pushCalls)
	}
}
