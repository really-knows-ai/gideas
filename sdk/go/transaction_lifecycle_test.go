package flow

import (
	"context"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestTxMethodsAfterRollback(t *testing.T) {
	tx := newMockTx(&mockCartographerClient{})
	tx.rolledBack = true

	tests := []struct {
		name string
		fn   func() error
	}{
		{"CreateEntity", func() error { _, err := tx.CreateEntity("", nil, nil, nil); return err }},
		{"UpdateEntity", func() error { _, err := tx.UpdateEntity("", nil, nil); return err }},
		{"DeleteEntity", func() error { _, err := tx.DeleteEntity(""); return err }},
		{"CreateEdge", func() error { _, err := tx.CreateEdge("", "", "", nil); return err }},
		{"DeleteEdge", func() error { _, err := tx.DeleteEdge(""); return err }},
		{"ListEntities", func() error { _, err := tx.ListEntities(""); return err }},
		{"ExecuteCypher", func() error { _, err := tx.ExecuteCypher("", nil); return err }},
		{"SearchNeighbors", func() error { _, err := tx.SearchNeighbors(nil, "", 0); return err }},
		{"FullTextSearch", func() error { _, err := tx.FullTextSearch("", ""); return err }},
		{"Diff", func() error { _, err := tx.Diff(); return err }},
		{"Refresh", func() error { return tx.Refresh() }},
		{"Commit", func() error { return tx.Commit() }},
		{"Rollback", func() error { return tx.Rollback() }},
		{"ExtendTimeout", func() error { return tx.ExtendTimeout(time.Hour) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.fn(); err == nil {
				t.Error("expected error after rollback")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Transaction lifecycle methods (SPEC R4 mapping table: Diff→GetTransactionDiff,
// Refresh→RefreshTransaction, Commit→CommitTransaction, Rollback→RollbackTransaction,
// ExtendTimeout→ExtendTimeout)
// ---------------------------------------------------------------------------

// TestTx_Diff pins tx.Diff's wire mapping and the structured diff conversion
// (SPEC R9): the transaction ID is injected and the added/modified/deleted
// entity and edge lists surface with their full DiffEntry payloads.
func TestTx_Diff(t *testing.T) {
	var capturedTxID string
	mock := &mockCartographerClient{
		getTxDiff: func(
			ctx context.Context, req *flowv1.GetTransactionDiffRequest,
		) (*flowv1.GetTransactionDiffResponse, error) {
			capturedTxID = req.GetTransactionId()
			return &flowv1.GetTransactionDiffResponse{
				AddedEntities: []*flowv1.DiffEntry{
					{
						Id: "e-new", Type: componentType,
						Properties: map[string]string{"name": "n"},
						Suspected:  true, Embedding: []float32{0.1, 0.2},
					},
				},
				ModifiedEntities: []*flowv1.DiffEntry{{Id: "e-mod", Type: componentType}},
				DeletedEntities:  []*flowv1.DiffEntry{{Id: "e-del", Type: componentType}},
				AddedEdges: []*flowv1.DiffEntry{
					{Id: "edge-new", Type: "DEPENDS_ON", FromEntityId: "from-1", ToEntityId: "to-1", Suspected: true},
				},
				ModifiedEdges: []*flowv1.DiffEntry{{Id: "edge-mod", Type: "DEPENDS_ON"}},
				DeletedEdges:  []*flowv1.DiffEntry{{Id: "edge-del", Type: "DEPENDS_ON"}},
			}, nil
		},
	}
	tx := newMockTx(mock)

	diff, err := tx.Diff()
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected tx ID %s on Diff, got %q", embassyTestTxID, capturedTxID)
	}
	if len(diff.AddedEntities) != 1 {
		t.Fatalf("expected 1 added entity, got %d", len(diff.AddedEntities))
	}
	added := diff.AddedEntities[0]
	if added.ID != "e-new" || added.Type != componentType || !added.Suspected {
		t.Errorf("unexpected added entity: %+v", added)
	}
	if len(added.Embedding) != 2 || added.Embedding[1] != 0.2 {
		t.Errorf("expected added entity embedding [0.1 0.2], got %v", added.Embedding)
	}
	if len(diff.ModifiedEntities) != 1 || diff.ModifiedEntities[0].ID != "e-mod" {
		t.Errorf("unexpected modified entities: %+v", diff.ModifiedEntities)
	}
	if len(diff.DeletedEntities) != 1 || diff.DeletedEntities[0].ID != "e-del" {
		t.Errorf("unexpected deleted entities: %+v", diff.DeletedEntities)
	}
	if len(diff.AddedEdges) != 1 {
		t.Fatalf("expected 1 added edge, got %d", len(diff.AddedEdges))
	}
	addedEdge := diff.AddedEdges[0]
	if addedEdge.ID != "edge-new" || addedEdge.FromEntityID != "from-1" ||
		addedEdge.ToEntityID != "to-1" || !addedEdge.Suspected {
		t.Errorf("unexpected added edge: %+v", addedEdge)
	}
	if len(diff.ModifiedEdges) != 1 || diff.ModifiedEdges[0].ID != "edge-mod" {
		t.Errorf("unexpected modified edges: %+v", diff.ModifiedEdges)
	}
	if len(diff.DeletedEdges) != 1 || diff.DeletedEdges[0].ID != "edge-del" {
		t.Errorf("unexpected deleted edges: %+v", diff.DeletedEdges)
	}
}

// TestTx_Refresh pins tx.Refresh's wire mapping: the transaction ID is
// injected and Refresh leaves the handle non-terminal (SPEC R9 — Refresh is
// not a terminal operation).
func TestTx_Refresh(t *testing.T) {
	var capturedTxID string
	mock := &mockCartographerClient{
		refreshTx: func(
			ctx context.Context, req *flowv1.RefreshTransactionRequest,
		) (*flowv1.RefreshTransactionResponse, error) {
			capturedTxID = req.GetTransactionId()
			return &flowv1.RefreshTransactionResponse{}, nil
		},
	}
	tx := newMockTx(mock)

	if err := tx.Refresh(); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected tx ID %s on Refresh, got %q", embassyTestTxID, capturedTxID)
	}
	if tx.checkTerminal() != nil {
		t.Error("expected the handle to remain non-terminal after Refresh")
	}
}

// TestTx_Refresh_ConflictAborted pins the SDK surfacing the SPEC R9 Refresh
// conflict verbatim: an overlapping change on main is rejected with ABORTED
// (SPEC error-table row "Refresh conflict").
func TestTx_Refresh_ConflictAborted(t *testing.T) {
	mock := &mockCartographerClient{
		refreshTx: func(
			ctx context.Context, req *flowv1.RefreshTransactionRequest,
		) (*flowv1.RefreshTransactionResponse, error) {
			return nil, status.Error(codes.Aborted, "same entity modified on main")
		},
	}
	tx := newMockTx(mock)

	err := tx.Refresh()
	if status.Code(err) != codes.Aborted {
		t.Fatalf("expected Aborted surfaced from Refresh on conflict, got %v (%v)", status.Code(err), err)
	}
}

// TestTx_Commit pins tx.Commit's wire mapping and terminal-state transition:
// the transaction ID is injected, and after a successful Commit the handle is
// marked committed so the R4 example's deferred `tx.Rollback()` after Commit
// returns the ignorable ErrTransactionCommitted instead of reaching the wire.
func TestTx_Commit(t *testing.T) {
	var capturedTxID string
	var capturedAck bool
	var rollbackCalled bool
	mock := &mockCartographerClient{
		commitTx: func(ctx context.Context, req *flowv1.CommitTransactionRequest) (*flowv1.CommitTransactionResponse, error) {
			capturedTxID = req.GetTransactionId()
			capturedAck = req.GetAck()
			return &flowv1.CommitTransactionResponse{}, nil
		},
		rollbackTx: func(
			ctx context.Context, req *flowv1.RollbackTransactionRequest,
		) (*flowv1.RollbackTransactionResponse, error) {
			rollbackCalled = true
			return &flowv1.RollbackTransactionResponse{}, nil
		},
	}
	tx := newMockTx(mock)

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected tx ID %s on Commit, got %q", embassyTestTxID, capturedTxID)
	}
	if capturedAck {
		t.Error("expected ack=false on the wire for a plain Commit (push is asynchronous, SPEC R10)")
	}
	if err := tx.Commit(); err != ErrTransactionCommitted {
		t.Errorf("expected ErrTransactionCommitted on a second Commit, got %v", err)
	}
	// R4 example: `defer func() { _ = tx.Rollback() }()` — the deferred
	// rollback after a successful Commit must be a local no-op, never a wire
	// call.
	if err := tx.Rollback(); err != ErrTransactionCommitted {
		t.Errorf("expected ErrTransactionCommitted from the deferred Rollback after Commit, got %v", err)
	}
	if rollbackCalled {
		t.Error("expected no Rollback wire call after a successful Commit")
	}
}

// TestTx_Commit_WithAck pins the SPEC R10 commit(WithAck()) blocking-push
// mode: the ack flag reaches the wire so the Cartographer wakes the sync
// worker and blocks until the push completes (a plain Commit leaves it false —
// see TestTx_Commit).
func TestTx_Commit_WithAck(t *testing.T) {
	var capturedTxID string
	var capturedAck bool
	mock := &mockCartographerClient{
		commitTx: func(ctx context.Context, req *flowv1.CommitTransactionRequest) (*flowv1.CommitTransactionResponse, error) {
			capturedTxID = req.GetTransactionId()
			capturedAck = req.GetAck()
			return &flowv1.CommitTransactionResponse{}, nil
		},
	}
	tx := newMockTx(mock)

	if err := tx.Commit(WithAck()); err != nil {
		t.Fatalf("Commit(WithAck) returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected tx ID %s on Commit, got %q", embassyTestTxID, capturedTxID)
	}
	if !capturedAck {
		t.Error("expected ack=true on the wire for Commit(WithAck())")
	}
}

// TestTx_Rollback pins tx.Rollback's wire mapping and terminal-state
// transition: the transaction ID is injected, and after a successful Rollback
// the handle rejects every further operation locally.
func TestTx_Rollback(t *testing.T) {
	var capturedTxID string
	mock := &mockCartographerClient{
		rollbackTx: func(
			ctx context.Context, req *flowv1.RollbackTransactionRequest,
		) (*flowv1.RollbackTransactionResponse, error) {
			capturedTxID = req.GetTransactionId()
			return &flowv1.RollbackTransactionResponse{}, nil
		},
	}
	tx := newMockTx(mock)

	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected tx ID %s on Rollback, got %q", embassyTestTxID, capturedTxID)
	}
	if _, err := tx.CreateEntity(componentType, nil, nil, nil); err != ErrTransactionRolledBack {
		t.Errorf("expected ErrTransactionRolledBack on a write after Rollback, got %v", err)
	}
}

// TestTx_ExtendTimeout pins tx.ExtendTimeout's wire mapping: the transaction
// ID and the requested duration reach the wire (SPEC R9 — duration resets the
// expiry timer), and the response's applied_timeout (the value the server
// granted, SPEC:237-246) is surfaced on the handle via AppliedTimeout rather
// than discarded.
func TestTx_ExtendTimeout(t *testing.T) {
	var capturedTxID string
	var capturedDuration time.Duration
	mock := &mockCartographerClient{
		extendTimeout: func(ctx context.Context, req *flowv1.ExtendTimeoutRequest) (*flowv1.ExtendTimeoutResponse, error) {
			capturedTxID = req.GetTransactionId()
			capturedDuration = req.GetDuration().AsDuration()
			return &flowv1.ExtendTimeoutResponse{AppliedTimeout: durationpb.New(24 * time.Hour)}, nil
		},
	}
	tx := newMockTx(mock)

	if err := tx.ExtendTimeout(24 * time.Hour); err != nil {
		t.Fatalf("ExtendTimeout returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected tx ID %s on ExtendTimeout, got %q", embassyTestTxID, capturedTxID)
	}
	if capturedDuration != 24*time.Hour {
		t.Errorf("expected the requested 24h duration on the wire, got %v", capturedDuration)
	}
	if got := tx.AppliedTimeout(); got != 24*time.Hour {
		t.Errorf("expected the server-granted 24h applied timeout on the handle, got %v", got)
	}
}

// TestTx_ExtendTimeout_RejectsOversized pins the R9 ExtendTimeout branch: a
// duration exceeding the 7-day hard maximum is rejected with INVALID_ARGUMENT
// by the Cartographer (SPEC error-table row "Invalid transaction timeout
// duration"), and the SDK surfaces the rejection verbatim.
func TestTx_ExtendTimeout_RejectsOversized(t *testing.T) {
	mock := &mockCartographerClient{
		extendTimeout: func(ctx context.Context, req *flowv1.ExtendTimeoutRequest) (*flowv1.ExtendTimeoutResponse, error) {
			return nil, status.Error(codes.InvalidArgument, "total lifetime cannot exceed 7 days")
		},
	}
	tx := newMockTx(mock)

	err := tx.ExtendTimeout(10 * 24 * time.Hour)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument surfaced for an oversized extension, got %v (%v)", status.Code(err), err)
	}
}
