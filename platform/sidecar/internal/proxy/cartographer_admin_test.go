package proxy

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowmeta "github.com/foundry/flow/pkg/metadata"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// TestCartographerProxy_E2E_TxLifecycle_PassesWithTxGrants pins the
// transaction-family fixed gates (SPEC R3): a node holding WRITE:graph/tx (and
// READ:graph/tx for GetTransactionDiff) has every transaction RPC forwarded to
// the Cartographer — BeginTransaction, CommitTransaction, RollbackTransaction,
// RefreshTransaction, ExtendTimeout, and GetTransactionDiff.
func TestCartographerProxy_E2E_TxLifecycle_PassesWithTxGrants(t *testing.T) {
	const caps = "WRITE:graph/tx,READ:graph/tx"
	capture, client, _ := setupCartographerWire(t, caps)
	ctx := context.Background()

	if _, err := client.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{}); err != nil {
		t.Fatalf("BeginTransaction with WRITE:graph/tx: %v", err)
	}
	if capture.lastMethod() != methodBeginTransaction {
		t.Fatalf("expected the Cartographer to receive BeginTransaction, got %q", capture.lastMethod())
	}

	if _, err := client.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: txID}); err != nil {
		t.Fatalf("CommitTransaction with WRITE:graph/tx: %v", err)
	}
	if capture.lastMethod() != methodCommitTransaction {
		t.Fatalf("expected the Cartographer to receive CommitTransaction, got %q", capture.lastMethod())
	}

	if _, err := client.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{TransactionId: txID}); err != nil {
		t.Fatalf("RollbackTransaction with WRITE:graph/tx: %v", err)
	}
	if capture.lastMethod() != methodRollbackTransaction {
		t.Fatalf("expected the Cartographer to receive RollbackTransaction, got %q", capture.lastMethod())
	}

	if _, err := client.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{TransactionId: txID}); err != nil {
		t.Fatalf("RefreshTransaction with WRITE:graph/tx: %v", err)
	}
	if capture.lastMethod() != methodRefreshTransaction {
		t.Fatalf("expected the Cartographer to receive RefreshTransaction, got %q", capture.lastMethod())
	}

	if _, err := client.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{TransactionId: txID}); err != nil {
		t.Fatalf("ExtendTimeout with WRITE:graph/tx: %v", err)
	}
	if capture.lastMethod() != methodExtendTimeout {
		t.Fatalf("expected the Cartographer to receive ExtendTimeout, got %q", capture.lastMethod())
	}

	if _, err := client.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: txID}); err != nil {
		t.Fatalf("GetTransactionDiff with READ:graph/tx: %v", err)
	}
	if capture.lastMethod() != methodGetTransactionDiff {
		t.Fatalf("expected the Cartographer to receive GetTransactionDiff, got %q", capture.lastMethod())
	}
}

// TestCartographerProxy_E2E_TxGate_BlocksWithoutTxGrants pins the fixed-gate
// block side (SPEC R3): a node holding no WRITE:graph/tx or READ:graph/tx
// grant is denied at the Sidecar with PERMISSION_DENIED for every transaction
// RPC, and no request reaches the Cartographer. The RPCs are issued with a raw
// client because a caller without WRITE:graph/tx cannot obtain a transaction
// handle (BeginTransaction is itself gated).
func TestCartographerProxy_E2E_TxGate_BlocksWithoutTxGrants(t *testing.T) {
	const caps = "READ:graph/entity/*"
	w := setupCartographerWireFull(t, caps)
	ctx := context.Background()

	txRPCCalls := []struct {
		name string
		call func() error
	}{
		{methodBeginTransaction, func() error {
			_, err := w.rawClient.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
			return err
		}},
		{methodCommitTransaction, func() error {
			_, err := w.rawClient.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: txID})
			return err
		}},
		{methodRollbackTransaction, func() error {
			_, err := w.rawClient.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{TransactionId: txID})
			return err
		}},
		{methodRefreshTransaction, func() error {
			_, err := w.rawClient.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{TransactionId: txID})
			return err
		}},
		{methodGetTransactionDiff, func() error {
			_, err := w.rawClient.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: txID})
			return err
		}},
		{methodExtendTimeout, func() error {
			_, err := w.rawClient.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{TransactionId: txID})
			return err
		}},
	}
	for _, c := range txRPCCalls {
		if err := c.call(); err == nil {
			t.Fatalf("%s: expected PERMISSION_DENIED without a tx grant, got nil error", c.name)
		} else if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
			t.Fatalf("%s: expected PermissionDenied, got %v", c.name, err)
		}
	}
	if w.capture.count() != 0 {
		t.Fatalf("fixed tx gate must prevent every RPC from reaching the Cartographer, got %d requests", w.capture.count())
	}
}

// TestCartographerProxy_E2E_Sync_PassesWithWildcardWrite pins the WRITE
// admin-gate success path (SPEC R3): Sync requires WRITE:graph/entity/*, and a
// node holding the wildcard grant has the request forwarded to the
// Cartographer.
func TestCartographerProxy_E2E_Sync_PassesWithWildcardWrite(t *testing.T) {
	const caps = "WRITE:graph/entity/*"
	capture, client, _ := setupCartographerWire(t, caps)
	ctx := context.Background()

	if _, err := client.Sync(ctx, &flowv1.SyncRequest{}); err != nil {
		t.Fatalf("Sync with WRITE:graph/entity/* should pass the fixed gate: %v", err)
	}
	if capture.lastMethod() != methodSync {
		t.Fatalf("expected the Cartographer to receive Sync, got %q", capture.lastMethod())
	}
}

// TestCartographerProxy_E2E_Sync_BlocksWithoutWildcardWrite pins the WRITE
// admin-gate block side (SPEC R3): a node holding READ capabilities but no
// WRITE:graph/entity/* grant is denied at the Sidecar with PERMISSION_DENIED
// and Sync never reaches the Cartographer.
func TestCartographerProxy_E2E_Sync_BlocksWithoutWildcardWrite(t *testing.T) {
	const caps = "READ:graph/entity/*"
	capture, client, _ := setupCartographerWire(t, caps)
	ctx := context.Background()
	before := capture.count()

	if _, err := client.Sync(ctx, &flowv1.SyncRequest{}); err == nil {
		t.Fatal("expected Sync to be blocked without WRITE:graph/entity/*")
	} else if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
	if capture.count() != before {
		t.Fatal("fixed gate block must prevent Sync from reaching the Cartographer")
	}
}

// TestCartographerProxy_E2E_ExportGraph_BlocksWithoutWildcardRead pins the
// READ admin-gate block side (SPEC R3): ExportGraph requires READ:graph/entity/*,
// and a node holding no read grant is denied at the Sidecar with
// PERMISSION_DENIED before the stream is established — the Cartographer never
// receives the request.
func TestCartographerProxy_E2E_ExportGraph_BlocksWithoutWildcardRead(t *testing.T) {
	const caps = "WRITE:graph/entity/*"
	capture, client, _ := setupCartographerWire(t, caps)
	ctx := context.Background()

	// ExportGraph is server-streaming: the Sidecar's status error is delivered
	// on the stream (Recv), not on the establishment call itself.
	stream, err := client.ExportGraph(ctx, &flowv1.ExportGraphRequest{Format: "json"})
	if err == nil {
		_, err = stream.Recv()
	}
	if err == nil {
		t.Fatal("expected ExportGraph to be blocked without READ:graph/entity/*")
	} else if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
	if capture.lastExportReq() != nil {
		t.Fatal("fixed gate block must prevent ExportGraph from reaching the Cartographer")
	}
}

// TestCartographerProxy_E2E_Mode1_SpecificTypeRead_PassesWithHeldType pins the
// mode-1 specific-type READ branch (SPEC R3:262): a node holding
// READ:graph/entity/Component has SearchNeighbors, FullTextSearch, and
// ListEntities for that type forwarded to the Cartographer.
func TestCartographerProxy_E2E_Mode1_SpecificTypeRead_PassesWithHeldType(t *testing.T) {
	const caps = "READ:graph/entity/Component"
	capture, client, _ := setupCartographerWire(t, caps)
	ctx := context.Background()

	if _, err := client.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
		Embedding:  []float32{0.1, 0.2},
		EntityType: entityTypeComponent,
		TopK:       10,
	}); err != nil {
		t.Fatalf("SearchNeighbors with held type should pass mode-1: %v", err)
	}
	if capture.lastMethod() != methodSearchNeighbors {
		t.Fatalf("expected the Cartographer to receive SearchNeighbors, got %q", capture.lastMethod())
	}

	if _, err := client.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{
		Query: "auth service", EntityType: entityTypeComponent,
	}); err != nil {
		t.Fatalf("FullTextSearch with held type should pass mode-1: %v", err)
	}
	if capture.lastMethod() != methodFullTextSearch {
		t.Fatalf("expected the Cartographer to receive FullTextSearch, got %q", capture.lastMethod())
	}

	if _, err := client.ListEntities(ctx, &flowv1.ListEntitiesRequest{EntityType: entityTypeComponent}); err != nil {
		t.Fatalf("ListEntities with held type should pass mode-1: %v", err)
	}
	if capture.lastMethod() != methodListEntities {
		t.Fatalf("expected the Cartographer to receive ListEntities, got %q", capture.lastMethod())
	}
}

// TestCartographerProxy_E2E_Mode1_SpecificTypeRead_BlocksUnheldType pins the
// mode-1 specific-type READ block side (SPEC R3:262): a node holding only
// READ:graph/entity/Service cannot read entities of type Component — each
// request is denied at the Sidecar with PERMISSION_DENIED and never reaches
// the Cartographer.
func TestCartographerProxy_E2E_Mode1_SpecificTypeRead_BlocksUnheldType(t *testing.T) {
	const caps = "READ:graph/entity/Service"
	capture, client, _ := setupCartographerWire(t, caps)
	ctx := context.Background()
	before := capture.count()

	readCalls := []struct {
		name string
		call func() error
	}{
		{methodSearchNeighbors, func() error {
			_, err := client.SearchNeighbors(ctx, &flowv1.SearchNeighborsRequest{
				Embedding:  []float32{0.1, 0.2},
				EntityType: entityTypeComponent,
				TopK:       10,
			})
			return err
		}},
		{methodFullTextSearch, func() error {
			_, err := client.FullTextSearch(ctx, &flowv1.FullTextSearchRequest{
				Query: "auth service", EntityType: entityTypeComponent,
			})
			return err
		}},
		{methodListEntities, func() error {
			_, err := client.ListEntities(ctx, &flowv1.ListEntitiesRequest{EntityType: entityTypeComponent})
			return err
		}},
	}
	for _, c := range readCalls {
		if err := c.call(); err == nil {
			t.Fatalf("%s: expected PERMISSION_DENIED for an unheld type, got nil error", c.name)
		} else if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
			t.Fatalf("%s: expected PermissionDenied, got %v", c.name, err)
		}
	}
	if capture.count() != before {
		t.Fatal("mode-1 block must prevent the requests from reaching the Cartographer")
	}
}

// TestCartographerProxy_E2E_SessionMode_SignsCapabilitiesWithKey pins the
// identity interceptor's session-mode enrichment branch (SPEC R3 / Capability
// Authorisation Chain): when the caller's workitem has an active assignment
// session, the interceptor resolves the node identity from the session and
// signs the attested capabilities with the Sidecar's configured key. The
// session's node identity differs from the Sidecar's entry-bound fallback
// identity, so the test proves the session branch (not the fallback) produced
// the signed metadata.
func TestCartographerProxy_E2E_SessionMode_SignsCapabilitiesWithKey(t *testing.T) {
	const caps = "READ:graph/entity/*"
	w := setupCartographerWireFull(t, caps)
	// Register the workitem as an active assignment whose session node
	// identity differs from the Sidecar's fallback node identity.
	startAssignmentSession(t, w.sidecarSrv, "wi-1", "session-node")
	// The raw client carries no workitem interceptor, so attach the workitem
	// identity explicitly to trigger the interceptor's session-mode branch.
	ctx := metadata.AppendToOutgoingContext(context.Background(), flowmeta.MetadataKeyWorkitemID, "wi-1")

	if _, err := w.rawClient.ExecuteCypher(
		ctx, &flowv1.ExecuteCypherRequest{Cypher: "MATCH (c:Component) RETURN c"},
	); err != nil {
		t.Fatalf("session-mode ExecuteCypher via SDK→Sidecar→Cartographer failed: %v", err)
	}
	if w.capture.count() == 0 {
		t.Fatal("expected the fake Cartographer to receive the ExecuteCypher request")
	}
	assertSignedCapabilitiesOnMD(t, w.sidecarPub, w.capture.metadata(), caps)
	if got := w.capture.metadata().Get(flowmeta.MetadataKeyNodeID); len(got) != 1 || got[0] != "session-node" {
		t.Fatalf("expected the session's node identity to be injected, got %v", got)
	}
}
