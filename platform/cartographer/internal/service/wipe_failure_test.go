package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestWipeGraph_MidWipeFailure(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()
	ctx := context.Background()

	// Replace the store with one that fails on WipeAll.
	srv.store = &fakeStore{Store: st, onWipeSchema: func(context.Context) error {
		return fmt.Errorf("simulated WipeSchema failure")
	}}

	// The git operations (withGitLock) will succeed, but WipeAll will fail,
	// triggering the mid-wipe error.
	_, err := srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{})
	if err == nil {
		t.Fatal("expected error for mid-wipe failure, got nil")
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", status.Code(err))
	}
}

// TestWipeGraph_GitSideMidWipeFailure pins SPEC error-table row 940 ("WipeGraph
// mid-wipe failure → INTERNAL") for the git-side wipe steps: git rm entities,
// the "wipe" commit, and clean untracked. These failures were previously
// returned as raw plain errors, which grpc-go converted to codes.Unknown; only
// the store-side failure produced INTERNAL.
func TestWipeGraph_GitSideMidWipeFailure(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	for _, tc := range []struct {
		name      string
		configure func(*fakeGitStore)
	}{
		{"git rm entities", func(g *fakeGitStore) {
			g.onGitRm = func(context.Context, string) error {
				return fmt.Errorf("simulated GitRm failure")
			}
		}},
		{"wipe commit", func(g *fakeGitStore) {
			g.onCommit = func(context.Context, string) error {
				return fmt.Errorf("simulated wipe commit failure")
			}
		}},
		{"clean untracked", func(g *fakeGitStore) {
			g.onCleanUntracked = func(context.Context) error {
				return fmt.Errorf("simulated clean untracked failure")
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := openTestStore(t)
			t.Cleanup(func() { _ = st.Close() })
			gs, _ := gitstore.New(t.TempDir())
			failingGit := &fakeGitStore{GitStore: gs}
			tc.configure(failingGit)
			srv := NewCartographerServer(st, failingGit, opPub, scPub, nil, "",
				30*time.Second, "test-ns", 30*time.Minute, 100000)
			srv.MarkDBReady()

			_, err := srv.WipeGraph(context.Background(), &flowv1.WipeGraphRequest{})
			if err == nil {
				t.Fatal("expected error for git-side mid-wipe failure, got nil")
			}
			if status.Code(err) != codes.Internal {
				t.Fatalf("expected Internal, got %v", status.Code(err))
			}
		})
	}
}

// TestWipeGraph_StoreSideFailureConvergesLiveStoreToWipedGit pins the SPEC R2
// WipeGraph recovery contract for the window between the git wipe and the
// store-side wipe: the git main is already wiped (wipe commit) before
// WipeSchema runs under the write lock, so if WipeSchema fails (INTERNAL) the
// live main.lbug must not keep serving the pre-wipe graph while git main is
// wiped. WipeGraph converges main.lbug to the (wiped) git state within the RPC
// by re-hydrating from the now-absent git entities/edges dirs — the pre-wipe
// entity is no longer readable from the live store — while still returning
// INTERNAL per SPEC R2 "graph state may be partially cleaned… INTERNAL" (R8
// restart re-hydration remains the fallback if the in-RPC re-hydration also
// fails).
func TestWipeGraph_StoreSideFailureConvergesLiveStoreToWipedGit(t *testing.T) {
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
	// The store-side wipe fails on demand; the real store's data is cleared by
	// the in-RPC re-hydration.
	srv := NewCartographerServer(&fakeStore{Store: st, onWipeSchema: func(context.Context) error {
		return fmt.Errorf("simulated WipeSchema failure")
	}}, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()
	ctx := context.Background()

	applyTestSchema(ctx, t, st)
	// Establish a pre-wipe graph in BOTH git main and the live main.lbug.
	commitGitEntity(ctx, t, gs, testMutationEntityID, "pre-wipe")
	if _, err := st.CreateEntity(ctx, "Component", testMutationEntityID,
		map[string]string{"name": "pre-wipe"}, nil, "main"); err != nil {
		t.Fatalf("seed pre-wipe entity into main.lbug: %v", err)
	}
	if _, err := st.GetEntity(ctx, testMutationEntityID, "main"); err != nil {
		t.Fatalf("pre-wipe entity should be present in main.lbug before wipe: %v", err)
	}

	// WipeSchema fails after the git wipe already committed → INTERNAL.
	if _, err := srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{}); err == nil {
		t.Fatal("expected error for store-side mid-wipe failure, got nil")
	} else if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", status.Code(err))
	}

	// The live store must no longer serve the pre-wipe graph: the in-RPC
	// re-hydration from the wiped git cleared it, so the store and git main
	// have converged.
	if _, err := st.GetEntity(ctx, testMutationEntityID, "main"); !errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("pre-wipe entity must be cleared from main.lbug after store-side wipe failure, got err=%v", err)
	}
}
