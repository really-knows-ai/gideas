package service

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCreateEntity_Valid(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	resp, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "test", "version": "1.0"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}
	if resp.EntityId == "" {
		t.Fatal("expected non-empty entity ID")
	}
	if resp.EntityType != "Component" {
		t.Fatalf("expected Component, got %q", resp.EntityType)
	}
}

// TestCreateEntity_SingleTypeSpecificCapabilityPasses pins SPEC R3
// (SPEC:242): a caller holding only WRITE:graph/entity/<type> (plus
// WRITE:graph/tx to begin the transaction) is authorised for a CreateEntity
// of that type (the per-type branch, not the wildcard fallback).
func TestCreateEntity_SingleTypeSpecificCapabilityPasses(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("WRITE:graph/entity/Component", "WRITE:graph/tx")

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	resp, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "per-type", "version": "1.0"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity with per-type capability failed: %v", err)
	}
	if resp.EntityId == "" {
		t.Fatal("expected non-empty entity ID")
	}
}

func TestCreateEntity_UnknownType(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Unknown",
		Properties:    map[string]string{"name": "x"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for unknown type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEntity_DuplicateID(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	resp, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "first"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("first CreateEntity failed: %v", err)
	}
	// Same ID again.
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Id:            resp.EntityId,
		Properties:    map[string]string{"name": "second"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for duplicate ID, got nil")
	}
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %v", status.Code(err))
	}
}

func TestCreateEntity_MissingRequiredProperty(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{},
		TransactionId: txID,
	})
	if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "missing required property") {
		t.Fatalf("CreateEntity without required property should fail: %v", err)
	}
}

// TestCreateEntity_InvalidIDWinsOverMissingCapability asserts the SPEC
// CreateEntity validation order (structural before capability): a caller lacking
// WRITE capabilities but supplying a structurally-invalid explicit `id` gets
// INVALID_ARGUMENT (not PERMISSION_DENIED) — mirroring
// TestCreateEdge_UnknownEdgeTypeWinsOverMissingCapability. The transaction is
// begun with full capabilities; the mutation call itself carries only READ
// capabilities.
func TestCreateEntity_InvalidIDWinsOverMissingCapability(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	// Only READ capabilities — the caller holds no write capability at all.
	noWriteCtx := capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")
	_, err := srv.CreateEntity(noWriteCtx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Id:            "not-a-uuid",
		Properties:    map[string]string{"name": "x"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for invalid entity ID, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected structural InvalidArgument to win over capability check, got %v (%v)", status.Code(err), err)
	}
}

// TestCreateEntity_UnknownPropertyWinsOverMissingCapability asserts the SPEC
// CreateEntity validation order (SPEC:1004: structural validation →
// data-integrity; R7 §1: unknown property → INVALID_ARGUMENT): a caller
// lacking WRITE capabilities but supplying an unknown property gets
// INVALID_ARGUMENT (not PERMISSION_DENIED) — mirroring
// TestCreateEdge_UnknownEdgeTypeWinsOverMissingCapability. The transaction is
// begun with full capabilities; the mutation call itself carries only READ
// capabilities.
func TestCreateEntity_UnknownPropertyWinsOverMissingCapability(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	// Only READ capabilities — the caller holds no write capability at all.
	noWriteCtx := capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")
	_, err := srv.CreateEntity(noWriteCtx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "x", "nonexistent": "value"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for unknown property, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected structural InvalidArgument to win over capability check, got %v (%v)", status.Code(err), err)
	}
	if !strings.Contains(err.Error(), "unknown property") {
		t.Fatalf("expected the unknown-property rejection, got %v", err)
	}
}

// TestCreateEntity_MissingRequiredPropertyWinsOverMissingCapability pins the
// same SPEC check-order guarantee (SPEC:1004, R7 §1: missing required
// property → INVALID_ARGUMENT) for the missing-required-property branch: a
// caller lacking WRITE capabilities but omitting a required property gets
// INVALID_ARGUMENT (not PERMISSION_DENIED), mirroring the unknown-property
// test above.
func TestCreateEntity_MissingRequiredPropertyWinsOverMissingCapability(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	// Only READ capabilities — the caller holds no write capability at all.
	noWriteCtx := capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")
	_, err := srv.CreateEntity(noWriteCtx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for missing required property, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected structural InvalidArgument to win over capability check, got %v (%v)", status.Code(err), err)
	}
	if !strings.Contains(err.Error(), "missing required property") {
		t.Fatalf("expected the missing-required-property rejection, got %v", err)
	}
}

func TestCreateEntity_UnknownProperty(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)

	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "test", "nonexistent": "value"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for unknown property, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEntity_InvalidIDFormat(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)

	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Id:            "not-a-uuid",
		Properties:    map[string]string{"name": "test"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for invalid ID format, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEntity_NaNEmbedding(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	txID := beginTestTx(t, srv, ctx)

	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "VecType",
		Properties:    map[string]string{"name": "bad"},
		Embedding:     []float32{float32(math.NaN()), 0.0, 0.0},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for NaN embedding, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEntity_NaNEmbeddingNonIndexed(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	// Component is a non-indexed type (enableVectorIndex not set).
	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)

	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "bad"},
		Embedding:     []float32{float32(math.NaN()), 0.0, 0.0},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for NaN embedding on non-indexed type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEntity_EmbeddingDimensionMismatch(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	txID := beginTestTx(t, srv, ctx)
	// Bootstrap with a 3-dim entity in the transaction branch.
	_, _ = srv.store.CreateEntity(ctx, "VecType", "", map[string]string{"name": "a"}, []float32{1.0, 0.0, 0.0}, txID)

	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "VecType",
		Properties:    map[string]string{"name": "b"},
		Embedding:     []float32{1.0, 0.0}, // only 2 dims
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for dimension mismatch, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEntity_VectorBootstrap(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	txID := beginTestTx(t, srv, ctx)

	// First entity must include an embedding to bootstrap the vector dimension.
	_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "VecType",
		Properties:    map[string]string{"name": "no-embedding"},
		TransactionId: txID,
		// No Embedding field.
	})
	if err == nil {
		t.Fatal("expected error for missing bootstrap embedding, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", status.Code(err))
	}
}

func TestCreateEntity_MissingWriteCapability(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()
	ctx := capabilityContext("READ:graph/entity/*,READ:graph/tx", scPriv, "sidecar")

	applyTestSchema(ctx, t, srv.store)

	// The caller holds no WRITE capability. A registered (valid) transaction
	// lets the request pass the active-transaction gate (SPEC RPC check order:
	// active transaction → structural → capability) so the missing-WRITE
	// capability check is reached and PERMISSION_DENIED surfaces. A
	// nonexistent transaction ID would instead surface NOT_FOUND, which is why
	// a real transaction is required to reach this branch.
	state, err := srv.txManager.Create(testMutationEntityID, 30*time.Minute, "")
	if err != nil {
		t.Fatalf("register transaction: %v", err)
	}
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "x"},
		TransactionId: state.ID,
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing WRITE capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}
