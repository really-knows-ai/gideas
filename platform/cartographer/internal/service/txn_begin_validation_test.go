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
	"google.golang.org/protobuf/types/known/durationpb"
)

// TestBeginTransaction_TimeoutValidation asserts the SPEC R2 (SPEC:207), R9
// (SPEC:557-559), and error-table row 917 contract for BeginTransaction: a
// requested timeout that is non-positive or exceeds the 7-day hard maximum is
// rejected with INVALID_ARGUMENT — no silent capping (the previous behavior)
// and no silent default-substitution — mirroring ExtendTimeout. Exactly 7 days
// is accepted (strict > comparison, matching TestExtendTimeout_AcceptedAt7DayBoundary).
func TestBeginTransaction_TimeoutValidation(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()
	ctx := testCtx()

	// Request a timeout far exceeding the hard max (7 days): rejected, not capped.
	_, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(14 * 24 * time.Hour),
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for over-max timeout, got %v (%v)", status.Code(err), err)
	}

	// Non-positive timeouts are rejected, not silently defaulted.
	for _, d := range []time.Duration{0, -1 * time.Minute} {
		_, err = srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
			Timeout: durationpb.New(d),
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument for timeout %v, got %v (%v)", d, status.Code(err), err)
		}
	}

	// Exactly 7 days is the accepted boundary, and the applied timeout surfaces
	// the granted value verbatim.
	resp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(7 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("expected 7-day-boundary begin to be accepted, got %v", err)
	}
	if resp.AppliedTimeout.AsDuration() != 7*24*time.Hour {
		t.Fatalf("expected applied timeout 7d, got %v", resp.AppliedTimeout.AsDuration())
	}
}

func TestBeginTransaction_ResourceExhausted(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub := initTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Replace the store with one that fails on CreateBranchDB.
	srv.store = &fakeStore{Store: st, onCreateBranchDB: func(context.Context, string) error {
		return fmt.Errorf("simulated CreateBranchDB failure")
	}}

	ctx := testCtx()
	_, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err == nil {
		t.Fatal("expected error for resource exhausted, got nil")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v", status.Code(err))
	}
}

// TestBeginTransaction_GitBranchCreationResourceExhausted pins the SPEC
// error-table row "BeginTransaction resource exhausted" → RESOURCE_EXHAUSTED
// ("Out of file handles, memory, or disk space; branch or LadybugDB creation
// failed"): a git-side branch-creation failure (CreateBranch, HardResetToBranch
// — e.g. disk full) must surface RESOURCE_EXHAUSTED, matching the store-side
// branch-DB path, not INTERNAL via mapGitError.
func TestBeginTransaction_GitBranchCreationResourceExhausted(t *testing.T) {
	tests := []struct {
		name   string
		failFn func(*fakeGitStore)
	}{
		{"CreateBranch", func(g *fakeGitStore) {
			g.onCreateBranch = func(context.Context, string) error {
				return fmt.Errorf("simulated CreateBranch failure")
			}
		}},
		{"HardResetToBranch", func(g *fakeGitStore) {
			g.onHardResetToBranch = func(context.Context, string) error {
				return fmt.Errorf("simulated HardResetToBranch failure")
			}
		}},
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

			failing := &fakeGitStore{GitStore: gs}
			tt.failFn(failing)
			srv.gitstore = failing

			ctx := testCtx()
			_, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if status.Code(err) != codes.ResourceExhausted {
				t.Fatalf("expected ResourceExhausted for git branch creation failure, got %v", status.Code(err))
			}
			if !strings.Contains(err.Error(), "simulated "+tt.name+" failure") {
				t.Fatalf("error should contain original failure, got: %v", err)
			}
		})
	}
}
