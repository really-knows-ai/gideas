package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestComputeSchemaHashCompleteAndDeterministic(t *testing.T) {
	base := testSchemaProvider{
		entityNames: []string{"Service", "Component"},
		edgeNames:   []string{"DEPENDS_ON"},
		entities: map[string]*store.EntityTypeDef{
			"Service": {
				Name: "Service", EnableVectorIndex: true,
				Properties: []store.PropertyDef{
					{Name: "version", Type: "string"}, {Name: "name", Type: "string", Required: true},
				},
				Rules: []store.ConnectionRuleDef{{
					CanConnectTo: []string{"Service", "Component"}, Using: []string{"CALLS", "DEPENDS_ON"},
				}},
			},
			"Component": {Name: "Component"},
		},
		edges: map[string]*store.EdgeTypeDef{
			"DEPENDS_ON": {Name: "DEPENDS_ON", Properties: []store.PropertyDef{{Name: "weight", Type: "string"}}},
		},
	}
	reordered := testSchemaProvider{
		entityNames: []string{"Component", "Service"}, edgeNames: []string{"DEPENDS_ON"},
		entities: map[string]*store.EntityTypeDef{
			"Component": {Name: "Component"},
			"Service": {
				Name: "Service", EnableVectorIndex: true,
				Properties: []store.PropertyDef{
					{Name: "name", Type: "string", Required: true}, {Name: "version", Type: "string"},
				},
				Rules: []store.ConnectionRuleDef{{
					CanConnectTo: []string{"Component", "Service"}, Using: []string{"DEPENDS_ON", "CALLS"},
				}},
			},
		},
		edges: base.edges,
	}
	baseline := computeSchemaHash(base)
	if got := computeSchemaHash(reordered); got != baseline {
		t.Fatalf("hash changed with ordering: %q != %q", got, baseline)
	}

	mutations := []struct {
		name   string
		mutate func(testSchemaProvider)
	}{
		{"required", func(s testSchemaProvider) { s.entities["Service"].Properties[0].Required = true }},
		{"vector", func(s testSchemaProvider) { s.entities["Service"].EnableVectorIndex = false }},
		{"rule", func(s testSchemaProvider) { s.entities["Service"].Rules[0].Using = []string{"CALLS"} }},
		{"property", func(s testSchemaProvider) { s.edges["DEPENDS_ON"].Properties[0].Type = "integer" }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			copyProvider := cloneTestSchemaProvider(base)
			mutation.mutate(copyProvider)
			if got := computeSchemaHash(copyProvider); got == baseline {
				t.Fatalf("hash did not change for %s mutation", mutation.name)
			}
		})
	}
}

func TestApplySchema_Valid(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
		},
	}
	_, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: schema})
	if err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	_ = st
}

func TestApplySchema_Idempotent(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
		},
	}
	if _, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: schema}); err != nil {
		t.Fatalf("first ApplySchema failed: %v", err)
	}
	if _, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: schema}); err != nil {
		t.Fatalf("second ApplySchema failed: %v", err)
	}
}

func TestApplySchema_InvalidSchema(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	// Duplicate entity type name is an invalid schema.
	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
			{Name: "Component", Properties: []*flowv1.Property{{Name: "name2", Type: "string"}}},
		},
	}
	_, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: schema})
	if err == nil {
		t.Fatal("expected error for duplicate entity type name, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// TestApplySchema_ValidationErrorPaths covers each SPEC error-table schema
// validation path at the service level. Each invalid schema must be rejected
// with INVALID_ARGUMENT via ApplySchema.
func TestApplySchema_ValidationErrorPaths(t *testing.T) {
	tests := []struct {
		name   string
		schema *flowv1.Schema
	}{
		{
			// An ApplySchemaRequest whose schema field is unset (nil) must be
			// rejected with INVALID_ARGUMENT, not panic downstream in the
			// store's catalog diff (which would surface as INTERNAL).
			name:   "nil schema (omitted schema field)",
			schema: nil,
		},
		{
			name: "duplicate property name",
			schema: &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
				{Name: "Component", Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
					{Name: "name", Type: "string"},
				}},
			}},
		},
		{
			name: "name is a reserved word",
			schema: &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
				{Name: "MATCH", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
			}},
		},
		{
			name: "name is the reserved internal placeholder",
			schema: &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
				{Name: "_untyped", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
			}},
		},
		{
			name: "edge type name is the reserved internal placeholder",
			schema: &flowv1.Schema{EdgeTypes: []*flowv1.EdgeType{
				{Name: "_untyped", Properties: []*flowv1.Property{{Name: "weight", Type: "string"}}},
			}},
		},
		{
			name: "name violates cypher identifier regex",
			schema: &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
				{Name: "1bad-name", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
			}},
		},
		{
			name: "property name collides with implicit column",
			schema: &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
				{Name: "Component", Properties: []*flowv1.Property{{Name: "id", Type: "string"}}},
			}},
		},
		{
			name: "property name collides with embedding column on vector-indexed type",
			schema: &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
				{Name: "Component", EnableVectorIndex: true, Properties: []*flowv1.Property{{Name: "embedding", Type: "string"}}},
			}},
		},
		{
			name: "edge property collides with implicit column: from",
			schema: &flowv1.Schema{EdgeTypes: []*flowv1.EdgeType{
				{Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "from", Type: "string"}}},
			}},
		},
		{
			name: "edge property collides with implicit column: to",
			schema: &flowv1.Schema{EdgeTypes: []*flowv1.EdgeType{
				{Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "to", Type: "string"}}},
			}},
		},
		{
			name: "edge property collides with implicit column: type",
			schema: &flowv1.Schema{EdgeTypes: []*flowv1.EdgeType{
				{Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "type", Type: "string"}}},
			}},
		},
		{
			name: "invalid property type in schema",
			schema: &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
				{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "int"}}},
			}},
		},
		{
			name: "empty canConnectTo list",
			schema: &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
				{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
					Rules: []*flowv1.ConnectionRule{{CanConnectTo: []string{}, Using: []string{"DEPENDS_ON"}}}},
			}},
		},
		{
			name: "empty using list",
			schema: &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
				{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
					Rules: []*flowv1.ConnectionRule{{CanConnectTo: []string{"Component"}, Using: []string{}}}},
			}},
		},
		{
			name: "undeclared type reference",
			schema: &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
				{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
					Rules: []*flowv1.ConnectionRule{{CanConnectTo: []string{"Missing"}, Using: []string{"DEPENDS_ON"}}}},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := newTestServer(t)
			ctx := context.Background()
			_, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: tt.schema})
			if err == nil {
				t.Fatal("expected ApplySchema to reject invalid schema, got nil")
			}
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
			}
		})
	}
}

func TestApplySchema_DestructiveChange(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	// Apply initial schema.
	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{
				{Name: "name", Type: "string"},
				{Name: "toremove", Type: "string"},
			}},
		},
	}
	if _, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: schema}); err != nil {
		t.Fatalf("first ApplySchema failed: %v", err)
	}

	// Re-apply schema with a property removed (destructive).
	destructive := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{
				{Name: "name", Type: "string"},
			}},
		},
	}
	_, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: destructive})
	if err == nil {
		t.Fatal("expected error for destructive schema change, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", status.Code(err))
	}
	if !strings.Contains(err.Error(), "destructive") {
		t.Fatalf("expected destructive schema change error, got %v", err)
	}

	// After WipeGraph, destructive change should succeed.
	if _, err := srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{}); err != nil {
		t.Fatalf("WipeGraph failed: %v", err)
	}
	if _, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: destructive}); err != nil {
		t.Fatalf("ApplySchema after WipeGraph failed: %v", err)
	}

	// Verify the new schema is applied.
	if !st.TableExists("Component") {
		t.Fatal("Component table should exist after ApplySchema")
	}
	// Create an entity with the new schema (no toremove property).
	_, err = st.CreateEntity(ctx, "Component", "", map[string]string{"name": "test"}, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity after wipe+apply failed: %v", err)
	}
}

func TestApplySchema_BeforeDBReady(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	// Do NOT call MarkDBReady.
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	ctx := context.Background()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
		},
	}
	_, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: schema})
	if err == nil {
		t.Fatal("expected error for ApplySchema before DB ready, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", status.Code(err))
	}
}

// TestApplySchema_InvalidSchemaBeforeDBReady pins the SPEC ApplySchema check
// order (SPEC:1021: database readiness → schema validation → table structure
// mismatch) for the readiness gate: a schema that is ALSO structurally invalid
// (duplicate entity type name → INVALID_ARGUMENT if validation ran) must still
// surface FAILED_PRECONDITION ("ApplySchema called before database ready")
// when the database is not ready — the readiness gate precedes schema
// validation. TestApplySchema_BeforeDBReady uses a valid schema, so only a
// combined fault can detect a reorder that surfaced the validation error first.
func TestApplySchema_InvalidSchemaBeforeDBReady(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := ladybug.Open(t.TempDir())
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	// Do NOT call MarkDBReady.
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	ctx := context.Background()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
			{Name: "Component", Properties: []*flowv1.Property{{Name: "version", Type: "string"}}},
		},
	}
	_, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: schema})
	if err == nil {
		t.Fatal("expected error for ApplySchema before DB ready, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition (readiness gate first), got %v", status.Code(err))
	}
}

// TestApplySchema_InvalidSchemaWinsOverDestructive pins the SPEC ApplySchema
// check order (SPEC:1021: database readiness → schema validation → table
// structure mismatch) for the validation gate: a schema that is both
// structurally invalid (duplicate property name → INVALID_ARGUMENT) and
// destructive (removes an applied property → FAILED_PRECONDITION if the
// structure diff ran) must surface INVALID_ARGUMENT — schema validation
// precedes the table-structure mismatch check.
func TestApplySchema_InvalidSchemaWinsOverDestructive(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	initial := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{
				{Name: "name", Type: "string"},
				{Name: "toremove", Type: "string"},
			}},
		},
	}
	if _, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: initial}); err != nil {
		t.Fatalf("initial ApplySchema failed: %v", err)
	}

	// Combined fault: removes `toremove` (destructive → FAILED_PRECONDITION if
	// the catalog diff ran) and declares `version` twice (invalid →
	// INVALID_ARGUMENT). Validation runs before the structure diff, so
	// INVALID_ARGUMENT must win.
	invalidAndDestructive := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "version", Type: "string"},
			}},
		},
	}
	_, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: invalidAndDestructive})
	if err == nil {
		t.Fatal("expected error for invalid destructive schema, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument (validation gate before structure mismatch), got %v", status.Code(err))
	}
}

func TestApplySchema_AdditiveChange(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	// Apply initial schema.
	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
		},
	}
	if _, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: schema}); err != nil {
		t.Fatalf("first ApplySchema failed: %v", err)
	}

	// Additive change: add a new property and a new entity type.
	additive := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
			}},
			{Name: "Service", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "weight", Type: "string"}}},
		},
	}
	_, err := srv.ApplySchema(ctx, &flowv1.ApplySchemaRequest{Schema: additive})
	if err != nil {
		t.Fatalf("additive ApplySchema failed: %v", err)
	}
}
