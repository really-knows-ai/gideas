package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestBeginTransaction_SurfacesDropBranchDBFailure(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub := initTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	srv.store = &fakeStore{Store: st,
		onCreateBranchDB: func(context.Context, string) error { return fmt.Errorf("simulated CreateBranchDB failure") },
		onDropBranchDB:   func(context.Context, string) error { return fmt.Errorf("simulated DropBranchDB failure") },
	}

	ctx := testCtx()
	_, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v", status.Code(err))
	}
	if !strings.Contains(err.Error(), "simulated CreateBranchDB failure") {
		t.Fatalf("error should contain original failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "simulated DropBranchDB failure") {
		t.Fatalf("error should surface DropBranchDB cleanup failure, got: %v", err)
	}
}

func TestBeginTransaction_SurfacesCleanupFailures(t *testing.T) {
	tests := []struct {
		name      string
		failField string // "restore", "clean", "delete"
		wantMsg   string
	}{
		{"RestoreMain", "restore", "simulated RestoreMain failure"},
		{"CleanUntracked", "clean", "simulated CleanUntracked failure"},
		{"DeleteBranch", "delete", "simulated DeleteBranch failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opPub, _ := generateTestKey()
			scPub := initTestKey()
			st, _ := openTestStore(t)
			t.Cleanup(func() { _ = st.Close() })
			gs, _ := gitstore.New(t.TempDir())
			srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
				30*time.Second, "test-ns", 30*time.Minute, 100000)
			srv.MarkDBReady()

			srv.store = &fakeStore{Store: st, onCreateBranchDB: func(context.Context, string) error {
				return fmt.Errorf("simulated CreateBranchDB failure")
			}}
			srv.gitstore = &fakeGitStore{GitStore: gs,
				onRestoreMain: func(ctx context.Context) error {
					if tt.failField == "restore" {
						return fmt.Errorf("simulated RestoreMain failure")
					}
					return gs.RestoreMain(ctx)
				},
				onCleanUntracked: func(ctx context.Context) error {
					if tt.failField == "clean" {
						return fmt.Errorf("simulated CleanUntracked failure")
					}
					return gs.CleanUntracked(ctx)
				},
				onDeleteBranch: func(ctx context.Context, txID string) error {
					if tt.failField == "delete" {
						return fmt.Errorf("simulated DeleteBranch failure")
					}
					return gs.DeleteBranch(ctx, txID)
				},
			}

			ctx := testCtx()
			_, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if status.Code(err) != codes.ResourceExhausted {
				t.Fatalf("expected ResourceExhausted, got %v", status.Code(err))
			}
			if !strings.Contains(err.Error(), "simulated CreateBranchDB failure") {
				t.Fatalf("error should contain original failure, got: %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("error should surface %s cleanup failure, got: %v", tt.name, err)
			}
		})
	}
}

func TestBeginTransaction_SurfacesMultipleCleanupFailures(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub := initTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	srv.store = &fakeStore{Store: st,
		onCreateBranchDB: func(context.Context, string) error { return fmt.Errorf("simulated CreateBranchDB failure") },
		onDropBranchDB:   func(context.Context, string) error { return fmt.Errorf("simulated DropBranchDB failure") },
	}
	srv.gitstore = &fakeGitStore{GitStore: gs,
		onRestoreMain:    func(context.Context) error { return fmt.Errorf("simulated RestoreMain failure") },
		onCleanUntracked: func(context.Context) error { return fmt.Errorf("simulated CleanUntracked failure") },
		onDeleteBranch:   func(context.Context, string) error { return fmt.Errorf("simulated DeleteBranch failure") },
	}

	ctx := testCtx()
	_, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v", status.Code(err))
	}
	if !strings.Contains(err.Error(), "simulated CreateBranchDB failure") {
		t.Fatalf("error should contain original failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "simulated DropBranchDB failure") {
		t.Fatalf("error should surface DropBranchDB cleanup failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "simulated RestoreMain failure") {
		t.Fatalf("error should surface RestoreMain cleanup failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "simulated CleanUntracked failure") {
		t.Fatalf("error should surface CleanUntracked cleanup failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "simulated DeleteBranch failure") {
		t.Fatalf("error should surface DeleteBranch cleanup failure, got: %v", err)
	}
}

func TestBeginTransaction_SurfacesTxManagerCreateCleanupFailures(t *testing.T) {
	// When txManager.Create fails, BeginTransaction attempts to clean up
	// the git branch and branch DB. This test verifies those cleanup
	// failures are surfaced by pre-registering the txID in the manager.
	opPub, _ := generateTestKey()
	scPub := initTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Pre-register a txID so txManager.Create fails with "already exists".
	fixedID := "00000000-0000-4000-8000-000000000001"
	srv.txManager.active[fixedID] = &TransactionState{ID: fixedID}
	srv.newIDFn = func() string { return fixedID }

	srv.gitstore = &fakeGitStore{GitStore: gs, onDeleteBranch: func(context.Context, string) error {
		return fmt.Errorf("simulated DeleteBranch failure")
	}}

	ctx := testCtx()
	_, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v", status.Code(err))
	}
	if !strings.Contains(err.Error(), "simulated DeleteBranch failure") {
		t.Fatalf("error should surface DeleteBranch cleanup failure, got: %v", err)
	}
}
