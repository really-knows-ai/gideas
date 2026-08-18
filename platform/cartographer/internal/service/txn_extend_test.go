package service

import (
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestExtendTimeout_NonPositiveDuration(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	_, err = srv.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{
		TransactionId: txID,
		Duration:      durationpb.New(0),
	})
	if err == nil {
		t.Fatal("expected error for zero duration, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}

	_, err = srv.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{
		TransactionId: txID,
		Duration:      durationpb.New(-1 * time.Second),
	})
	if err == nil {
		t.Fatal("expected error for negative duration, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestExtendTimeout_ExceedsMaxTotalLifetime(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(1 * time.Minute),
	})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	// Extending to 8 days would exceed the 7-day hard max (created at now,
	// 8 days from now > 7 days).
	_, err = srv.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{
		TransactionId: txID,
		Duration:      durationpb.New(8 * 24 * time.Hour),
	})
	if err == nil {
		t.Fatal("expected error for exceeding max total lifetime, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestExtendTimeout_AcceptedAt7DayBoundary(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	// Replace with a fake clock so the boundary is deterministic.
	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(7*24*time.Hour, 100000)
	srv.txManager.clock = fc

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(1 * time.Minute),
	})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	// created at the fake now, so a lifetime of exactly 7 days is
	// totalLifetime == hardMaxTimeout, which strict `>` accepts.
	_, err = srv.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{
		TransactionId: txID,
		Duration:      durationpb.New(7 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("expected 7-day-boundary extend to be accepted, got %v", err)
	}
}

// TestExtendTimeout_PersistFailureRevertsInMemoryState pins the
// revert-on-persist-failure contract for ExtendTimeout: when the durable branch
// state write fails, the RPC reports the failure and the in-memory
// ExpiresAt/AppliedTimeout mutations are reverted, so no silent
// in-memory/durable divergence exists (recovery on restart restores the
// persisted, un-extended timeout — the in-memory state must match it).
func TestExtendTimeout_PersistFailureRevertsInMemoryState(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	failingStore := newTxStateFailingStore(base, func(state store.BranchTransactionState) bool {
		// Fail only the ExtendTimeout persist (the state carrying the extended
		// 10m AppliedTimeout), not BeginTransaction's own initial persist.
		return state.AppliedTimeout == 10*time.Minute
	})
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(failingStore, gs, opPub, initTestKey(), nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()
	ctx := testCtx()

	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	txID := begin.TransactionId
	state, err := srv.txManager.Lookup(txID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	oldExpiresAt := state.ExpiresAt
	oldAppliedTimeout := state.AppliedTimeout

	_, err = srv.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{
		TransactionId: txID,
		Duration:      durationpb.New(10 * time.Minute),
	})
	if err == nil {
		t.Fatal("expected persist failure to be reported, got nil")
	}
	if state.ExpiresAt != oldExpiresAt || state.AppliedTimeout != oldAppliedTimeout {
		t.Fatalf("in-memory expiry mutated despite persist failure: expiresAt=%v (want %v) applied=%v (want %v)",
			state.ExpiresAt, oldExpiresAt, state.AppliedTimeout, oldAppliedTimeout)
	}
	// The transaction must still be usable with its original expiry.
	if _, unlock, err := srv.txManager.LockActive(txID); err != nil {
		t.Fatalf("transaction unusable after reverted extend failure: %v", err)
	} else {
		unlock()
	}
}

// TestExtendTimeout_MissingTxCapability pins SPEC R3 (SPEC:244): a caller
// without WRITE:graph/tx is denied ExtendTimeout with PERMISSION_DENIED
// before any transaction lookup or validation.
func TestExtendTimeout_MissingTxCapability(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Only entity capabilities, no WRITE:graph/tx.
	ctx := capabilityContext("READ:graph/entity/*,WRITE:graph/entity/*", scPriv, "sidecar")

	_, err := srv.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{
		TransactionId: testMutationEntityID,
		Duration:      durationpb.New(time.Minute),
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing WRITE:graph/tx capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}
