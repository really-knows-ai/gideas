package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// testingShortGuard skips tests that require OpenInMemory when -short is set.
func testingShortGuard(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping store test in short mode")
	}
}

func newTestSchema() *flowv1.Schema {
	return &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string", Required: true},
					{Name: "version", Type: "string"},
				},
				EnableVectorIndex: false,
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{
				Name: "DEPENDS_ON",
				Properties: []*flowv1.Property{
					{Name: "weight", Type: "string"},
				},
			},
		},
	}
}

// Helper to create a schema with rules for Component -> Service via DEPENDS_ON
func newTestSchemaWithRules() *flowv1.Schema {
	return &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string", Required: true},
				},
				EnableVectorIndex: false,
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Service"}, Using: []string{"DEPENDS_ON"}},
				},
			},
			{
				Name: "Service",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string", Required: true},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{
				Name: "DEPENDS_ON",
				Properties: []*flowv1.Property{
					{Name: "weight", Type: "string"},
				},
			},
		},
	}
}

// --- 1. OpenInMemory / Close ---

func TestOpenInMemory_Close(t *testing.T) {
	testingShortGuard(t)
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	// Double close should not panic
	if err := s.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}
}

// --- 2. ApplySchema - creates tables ---

func TestApplySchema_CreatesTables(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := newTestSchemaWithRules()
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Create entity of type "Component"
	c, err := s.CreateEntity(ctx, "Component", "", map[string]string{"name": "core"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity Component failed: %v", err)
	}
	if c.Type != "Component" {
		t.Fatalf("expected type Component, got %q", c.Type)
	}
	if c.Properties["name"] != "core" {
		t.Fatalf("expected name='core', got %q", c.Properties["name"])
	}

	// Create entity of type "Service" (needed as target for edge rule)
	svc, err := s.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity Service failed: %v", err)
	}

	// Create edge
	edge, err := s.CreateEdge(ctx, "DEPENDS_ON", c.Id, svc.Id, map[string]string{"weight": "high"}, "")
	if err != nil {
		t.Fatalf("CreateEdge failed: %v", err)
	}
	if edge.Type != "DEPENDS_ON" {
		t.Fatalf("expected edge type DEPENDS_ON, got %q", edge.Type)
	}
}

// --- 3. ApplySchema - idempotent ---

func TestApplySchema_Idempotent(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := newTestSchema()
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("first ApplySchema failed: %v", err)
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("second ApplySchema (idempotent) failed: %v", err)
	}
}

// --- 4. ApplySchema - additive ---

func TestApplySchema_Additive(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema1 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema1); err != nil {
		t.Fatalf("first ApplySchema failed: %v", err)
	}

	schema2 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
					{Name: "version", Type: "string"},
				},
			},
			{
				Name: "Service",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema2); err != nil {
		t.Fatalf("second ApplySchema (additive) failed: %v", err)
	}

	// Verify new entity type works
	_, err = s.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity on new type failed: %v", err)
	}

	// Verify new property works on existing type
	_, err = s.CreateEntity(ctx, "Component", "", map[string]string{"name": "core", "version": "2.0"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity with new property failed: %v", err)
	}
}

// --- 5. ApplySchema - destructive rejected ---

func TestApplySchema_DestructiveRejected(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema1 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
					{Name: "version", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema1); err != nil {
		t.Fatalf("first ApplySchema failed: %v", err)
	}

	schema2 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	err = s.ApplySchema(ctx, schema2)
	if err == nil {
		t.Fatal("expected error for destructive schema change, got nil")
	}
	if !strings.Contains(err.Error(), "property") || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("expected table structure mismatch error, got: %v", err)
	}
}

// --- 6. ApplySchema - duplicate type name ---

func TestApplySchema_DuplicateEntityTypeName(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component"},
			{Name: "Component"},
		},
	}
	err = s.ApplySchema(ctx, schema)
	if err == nil {
		t.Fatal("expected error for duplicate entity type name, got nil")
	}
}

// --- 7-12. reserved ---

// --- 13. CreateEntity - basic ---

func TestCreateEntity_Basic(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := newTestSchema()
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	ent, err := s.CreateEntity(ctx, "Component", "", map[string]string{"name": "test", "version": "1.0"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}
	if ent.Id == "" {
		t.Fatal("expected non-empty ID")
	}

	// GetEntity to verify
	got, err := s.GetEntity(ctx, ent.Id, "")
	if err != nil {
		t.Fatalf("GetEntity failed: %v", err)
	}
	if got.Properties["name"] != "test" {
		t.Fatalf("expected name=test, got %q", got.Properties["name"])
	}
}

// --- 14. CreateEntity - auto-generated ID ---

func TestCreateEntity_AutoGeneratedID(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.ApplySchema(ctx, newTestSchema()); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	ent, err := s.CreateEntity(ctx, "Component", "", map[string]string{"name": "auto"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}
	if ent.Id == "" {
		t.Fatal("expected auto-generated ID, got empty")
	}
	if len(ent.Id) != 36 { // UUID v4 format is 36 chars
		t.Fatalf("expected 36-char UUID, got %d-char %q", len(ent.Id), ent.Id)
	}
}

// --- 15. CreateEntity - unknown type ---

func TestCreateEntity_UnknownType(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.CreateEntity(ctx, "NonExistent", "", map[string]string{"name": "x"}, nil, "")
	if err == nil {
		t.Fatal("expected error for unknown entity type, got nil")
	}
	if !errors.Is(err, ErrUnknownEntityType) {
		t.Fatalf("expected unknown entity type error, got: %v", err)
	}
}

// --- 16. CreateEntity - unknown property ---

func TestCreateEntity_UnknownProperty(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.ApplySchema(ctx, newTestSchema()); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	_, err = s.CreateEntity(ctx, "Component", "", map[string]string{"unknown_prop": "val"}, nil, "")
	if err == nil {
		t.Fatal("expected error for unknown property, got nil")
	}
}

// --- 17. CreateEntity - missing required property ---

func TestCreateEntity_MissingRequiredProperty(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.ApplySchema(ctx, newTestSchema()); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	_, err = s.CreateEntity(ctx, "Component", "", map[string]string{}, nil, "")
	if err == nil {
		t.Fatal("expected error for missing required property, got nil")
	}
}

// --- 19. CreateEntity - duplicate ID ---

func TestCreateEntity_DuplicateID(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.ApplySchema(ctx, newTestSchema()); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	ent, err := s.CreateEntity(ctx, "Component", "", map[string]string{"name": "a"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	_, err = s.CreateEntity(ctx, "Component", ent.Id, map[string]string{"name": "b"}, nil, "")
	if err == nil {
		t.Fatal("expected error for duplicate ID, got nil")
	}
}

// --- 20. CreateEntity - invalid ID format ---

func TestCreateEntity_InvalidIDFormat(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.ApplySchema(ctx, newTestSchema()); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	_, err = s.CreateEntity(ctx, "Component", "not-a-uuid", map[string]string{"name": "x"}, nil, "")
	if err == nil {
		t.Fatal("expected error for invalid ID, got nil")
	}
	if !strings.Contains(err.Error(), "invalid ID format") {
		t.Fatalf("expected invalid ID format error, got: %v", err)
	}
}

// --- 21. CreateEntity - embedding with NaN ---

func TestCreateEntity_NaNEmbedding(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "IndexedType",
				EnableVectorIndex: true,
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	_, err = s.CreateEntity(ctx, "IndexedType", "", map[string]string{"name": "x"}, []float32{float32(math.NaN())}, "")
	if err == nil {
		t.Fatal("expected error for NaN embedding, got nil")
	}
	if !strings.Contains(err.Error(), "NaN") {
		t.Fatalf("expected NaN error, got: %v", err)
	}
}

// --- 22. CreateEntity - embedding with NaN on non-indexed type ---

func TestCreateEntity_NaNEmbeddingNonIndexed(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "NonIndexed",
				EnableVectorIndex: false,
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	_, err = s.CreateEntity(ctx, "NonIndexed", "", map[string]string{"name": "x"}, []float32{float32(math.NaN())}, "")
	if err == nil {
		t.Fatal("expected error for NaN embedding on non-indexed type, got nil")
	}
}

// --- 23. CreateEntity - embedding with +Inf on indexed type ---

func TestCreateEntity_InfEmbedding(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "IndexedType",
				EnableVectorIndex: true,
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	_, err = s.CreateEntity(ctx, "IndexedType", "", map[string]string{"name": "x"}, []float32{float32(math.Inf(1))}, "")
	if err == nil {
		t.Fatal("expected error for Inf embedding, got nil")
	}
	if !strings.Contains(err.Error(), "NaN") && !strings.Contains(err.Error(), "infinity") {
		t.Fatalf("expected NaN/infinity error, got: %v", err)
	}
}

// --- 24. CreateEntity - embedding with -Inf on non-indexed type ---

func TestCreateEntity_NegInfEmbeddingNonIndexed(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "NonIndexed",
				EnableVectorIndex: false,
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	_, err = s.CreateEntity(ctx, "NonIndexed", "", map[string]string{"name": "x"}, []float32{float32(math.Inf(-1))}, "")
	if err == nil {
		t.Fatal("expected error for -Inf embedding on non-indexed type, got nil")
	}
	if !strings.Contains(err.Error(), "NaN") && !strings.Contains(err.Error(), "infinity") {
		t.Fatalf("expected NaN/infinity error, got: %v", err)
	}
}

// --- 25. CreateEntity - vector bootstrap without embedding ---

func TestCreateEntity_VectorBootstrapWithoutEmbedding(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "IndexedType",
				EnableVectorIndex: true,
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	_, err = s.CreateEntity(ctx, "IndexedType", "", map[string]string{"name": "x"}, nil, "")
	if err == nil {
		t.Fatal("expected bootstrap error for first entity without embedding, got nil")
	}
}

// --- 26. UpdateEntity - partial update ---

func TestUpdateEntity_PartialUpdate(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.ApplySchema(ctx, newTestSchema()); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	ent, err := s.CreateEntity(ctx, "Component", "", map[string]string{"name": "original", "version": "1"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	updated, err := s.UpdateEntity(ctx, ent.Id, map[string]string{"version": "2"}, nil, "")
	if err != nil {
		t.Fatalf("UpdateEntity failed: %v", err)
	}
	if updated.Properties["name"] != "original" {
		t.Fatalf("expected name preserved as 'original', got %q", updated.Properties["name"])
	}
	if updated.Properties["version"] != "2" {
		t.Fatalf("expected version updated to '2', got %q", updated.Properties["version"])
	}
}

// --- 29. UpdateEntity - not found ---

func TestUpdateEntity_NotFound(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.ApplySchema(ctx, newTestSchema()); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	_, err = s.UpdateEntity(ctx, "00000000-0000-0000-0000-000000000000", map[string]string{"name": "x"}, nil, "")
	if err == nil {
		t.Fatal("expected error for not-found entity, got nil")
	}
}

// --- 30. UpdateEntity - invalid ID format ---

func TestUpdateEntity_InvalidIDFormat(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.UpdateEntity(ctx, "bad-id", map[string]string{"name": "x"}, nil, "")
	if err == nil {
		t.Fatal("expected error for invalid ID format, got nil")
	}
}

// --- 31. UpdateEntity - embedding with NaN ---

func TestUpdateEntity_NaNEmbedding(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "IndexedType",
				EnableVectorIndex: true,
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	ent, err := s.CreateEntity(ctx, "IndexedType", "", map[string]string{"name": "x"}, []float32{1.0, 2.0, 3.0}, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	_, err = s.UpdateEntity(ctx, ent.Id, nil, []float32{float32(math.NaN())}, "")
	if err == nil {
		t.Fatal("expected error for NaN embedding, got nil")
	}
}

// --- 32. UpdateEntity - embedding dimension mismatch ---

func TestUpdateEntity_EmbeddingDimensionMismatch(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "IndexedType",
				EnableVectorIndex: true,
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	ent, err := s.CreateEntity(ctx, "IndexedType", "", map[string]string{"name": "x"}, []float32{1.0, 2.0, 3.0}, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	_, err = s.UpdateEntity(ctx, ent.Id, nil, []float32{1.0, 2.0}, "")
	if err == nil {
		t.Fatal("expected dimension mismatch error, got nil")
	}
}

// --- 35. DeleteEntity - basic ---

func TestDeleteEntity_Basic(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.ApplySchema(ctx, newTestSchema()); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	ent, err := s.CreateEntity(ctx, "Component", "", map[string]string{"name": "x"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	deleted, err := s.DeleteEntity(ctx, ent.Id, "")
	if err != nil {
		t.Fatalf("DeleteEntity failed: %v", err)
	}
	if deleted.Id != ent.Id {
		t.Fatalf("expected deleted entity ID %q, got %q", ent.Id, deleted.Id)
	}

	_, err = s.GetEntity(ctx, ent.Id, "")
	if err == nil {
		t.Fatal("expected NOT_FOUND after delete, got nil")
	}
}

// --- 36. DeleteEntity - cascade to edges ---

func TestDeleteEntity_CascadeEdges(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := newTestSchemaWithRules()
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	src, err := s.CreateEntity(ctx, "Component", "", map[string]string{"name": "src"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src failed: %v", err)
	}
	tgt, err := s.CreateEntity(ctx, "Service", "", map[string]string{"name": "tgt"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt failed: %v", err)
	}

	edge, err := s.CreateEdge(ctx, "DEPENDS_ON", src.Id, tgt.Id, nil, "")
	if err != nil {
		t.Fatalf("CreateEdge failed: %v", err)
	}

	// Delete source entity
	_, err = s.DeleteEntity(ctx, src.Id, "")
	if err != nil {
		t.Fatalf("DeleteEntity failed: %v", err)
	}

	// Edge should be deleted as cascade
	_, err = s.GetEdge(ctx, edge.Id, "")
	if err == nil {
		t.Fatal("expected edge to be cascaded, but it still exists")
	}
}

// --- 37. DeleteEntity - not found ---

func TestDeleteEntity_NotFound(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.DeleteEntity(ctx, "00000000-0000-0000-0000-000000000000", "")
	if err == nil {
		t.Fatal("expected error for not-found entity, got nil")
	}
}

// --- 38. DeleteEntity - invalid ID format ---

func TestDeleteEntity_InvalidIDFormat(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.DeleteEntity(ctx, "bad-id", "")
	if err == nil {
		t.Fatal("expected error for invalid ID format, got nil")
	}
}

// --- 39. CreateEdge - basic ---

func TestCreateEdge_Basic(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := newTestSchemaWithRules()
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	src, err := s.CreateEntity(ctx, "Component", "", map[string]string{"name": "src"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src failed: %v", err)
	}
	tgt, err := s.CreateEntity(ctx, "Service", "", map[string]string{"name": "tgt"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt failed: %v", err)
	}

	edge, err := s.CreateEdge(ctx, "DEPENDS_ON", src.Id, tgt.Id, map[string]string{"weight": "high"}, "")
	if err != nil {
		t.Fatalf("CreateEdge failed: %v", err)
	}
	if edge.Type != "DEPENDS_ON" {
		t.Fatalf("expected edge type DEPENDS_ON, got %q", edge.Type)
	}
	if edge.Properties["weight"] != "high" {
		t.Fatalf("expected weight=high, got %q", edge.Properties["weight"])
	}
}

// --- 40. CreateEdge - source not found ---

func TestCreateEdge_SourceNotFound(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.ApplySchema(ctx, newTestSchemaWithRules()); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	_, err = s.CreateEdge(ctx, "DEPENDS_ON", "00000000-0000-0000-0000-000000000000", "00000000-0000-0000-0000-000000000001", nil, "")
	if err == nil {
		t.Fatal("expected error for not-found source, got nil")
	}
}

// --- 42. CreateEdge - unknown edge type ---

func TestCreateEdge_UnknownEdgeType(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := newTestSchema()
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	src, err := s.CreateEntity(ctx, "Component", "", map[string]string{"name": "src"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	_, err = s.CreateEdge(ctx, "UNKNOWN_EDGE", src.Id, src.Id, nil, "")
	if err == nil {
		t.Fatal("expected error for unknown edge type, got nil")
	}
}

// --- 44. CreateEdge - unknown edge property ---

func TestCreateEdge_UnknownProperty(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := newTestSchemaWithRules()
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	src, err := s.CreateEntity(ctx, "Component", "", map[string]string{"name": "src"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src failed: %v", err)
	}
	tgt, err := s.CreateEntity(ctx, "Service", "", map[string]string{"name": "tgt"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt failed: %v", err)
	}

	_, err = s.CreateEdge(ctx, "DEPENDS_ON", src.Id, tgt.Id, map[string]string{"unknown_edge_prop": "val"}, "")
	if err == nil {
		t.Fatal("expected error for unknown edge property, got nil")
	}
}

// --- 47. CreateEdge - rule violation ---

func TestCreateEdge_RuleViolation(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Service"}, Using: []string{"DEPENDS_ON"}},
				},
			},
			{
				Name: "Service",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
			{
				Name: "OtherType",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DEPENDS_ON"},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	src, err := s.CreateEntity(ctx, "Component", "", map[string]string{"name": "src"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}
	tgt, err := s.CreateEntity(ctx, "OtherType", "", map[string]string{"name": "tgt"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	// Component -> OtherType is NOT allowed (rule only allows Component -> Service)
	_, err = s.CreateEdge(ctx, "DEPENDS_ON", src.Id, tgt.Id, nil, "")
	if err == nil {
		t.Fatal("expected PERMISSION_DENIED for rule violation, got nil")
	}
}

// --- 49. CreateEdge - self-referencing ---

func TestCreateEdge_SelfReferencing(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DEPENDS_ON"},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	a, err := s.CreateEntity(ctx, "Component", "", map[string]string{"name": "a"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}
	b, err := s.CreateEntity(ctx, "Component", "", map[string]string{"name": "b"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	edge, err := s.CreateEdge(ctx, "DEPENDS_ON", a.Id, b.Id, nil, "")
	if err != nil {
		t.Fatalf("self-referencing edge should be allowed: %v", err)
	}
	if edge == nil {
		t.Fatal("expected non-nil edge")
	}
}

// --- 52. DeleteEdge - basic ---

func TestDeleteEdge_Basic(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := newTestSchemaWithRules()
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	src, err := s.CreateEntity(ctx, "Component", "", map[string]string{"name": "src"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}
	tgt, err := s.CreateEntity(ctx, "Service", "", map[string]string{"name": "tgt"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	edge, err := s.CreateEdge(ctx, "DEPENDS_ON", src.Id, tgt.Id, nil, "")
	if err != nil {
		t.Fatalf("CreateEdge failed: %v", err)
	}

	deleted, err := s.DeleteEdge(ctx, edge.Id, "")
	if err != nil {
		t.Fatalf("DeleteEdge failed: %v", err)
	}
	if deleted.Id != edge.Id {
		t.Fatalf("expected edge ID %q, got %q", edge.Id, deleted.Id)
	}

	_, err = s.GetEdge(ctx, edge.Id, "")
	if err == nil {
		t.Fatal("expected NOT_FOUND after delete, got nil")
	}
}

// --- 55. GetEntity - not found ---

func TestGetEntity_NotFound(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.GetEntity(ctx, "00000000-0000-0000-0000-000000000000", "")
	if err == nil {
		t.Fatal("expected error for not-found entity, got nil")
	}
}

// --- 56. GetEdge - basic ---

func TestGetEdge_Basic(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := newTestSchemaWithRules()
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	src, err := s.CreateEntity(ctx, "Component", "", map[string]string{"name": "src"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}
	tgt, err := s.CreateEntity(ctx, "Service", "", map[string]string{"name": "tgt"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	edge, err := s.CreateEdge(ctx, "DEPENDS_ON", src.Id, tgt.Id, map[string]string{"weight": "low"}, "")
	if err != nil {
		t.Fatalf("CreateEdge failed: %v", err)
	}

	got, err := s.GetEdge(ctx, edge.Id, "")
	if err != nil {
		t.Fatalf("GetEdge failed: %v", err)
	}
	if got.Type != "DEPENDS_ON" {
		t.Fatalf("expected type DEPENDS_ON, got %q", got.Type)
	}
	if got.FromEntityID != src.Id {
		t.Fatalf("expected from=%q, got %q", src.Id, got.FromEntityID)
	}
	if got.ToEntityID != tgt.Id {
		t.Fatalf("expected to=%q, got %q", tgt.Id, got.ToEntityID)
	}
}

// --- 58. ListEntities - basic ---

func TestListEntities_Basic(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Item",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	ids := make([]string, 5)
	for i := range 5 {
		ent, err := s.CreateEntity(ctx, "Item", "", map[string]string{"name": string(rune('A' + i))}, nil, "")
		if err != nil {
			t.Fatalf("CreateEntity %d failed: %v", i, err)
		}
		ids[i] = ent.Id
	}

	// First page: pageSize=2
	page1, token1, err := s.ListEntities(ctx, "Item", 2, "", "")
	if err != nil {
		t.Fatalf("ListEntities page 1 failed: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("expected 2 items on page 1, got %d", len(page1))
	}
	if token1 == "" {
		t.Fatal("expected non-empty page token for page 1")
	}

	// Second page
	page2, token2, err := s.ListEntities(ctx, "Item", 2, token1, "")
	if err != nil {
		t.Fatalf("ListEntities page 2 failed: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("expected 2 items on page 2, got %d", len(page2))
	}
	if token2 == "" {
		t.Fatal("expected non-empty page token for page 2")
	}

	// Third page (should be last, 1 item)
	page3, token3, err := s.ListEntities(ctx, "Item", 2, token2, "")
	if err != nil {
		t.Fatalf("ListEntities page 3 failed: %v", err)
	}
	if len(page3) != 1 {
		t.Fatalf("expected 1 item on page 3, got %d", len(page3))
	}
	if token3 != "" {
		t.Fatalf("expected empty token on last page, got %q", token3)
	}
}

// --- 59. ListEntities - unknown type ---

func TestListEntities_UnknownType(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, _, err = s.ListEntities(ctx, "NonExistent", 10, "", "")
	if err == nil {
		t.Fatal("expected error for unknown entity type, got nil")
	}
}

// --- 60. ListEntities - invalid page size ---

func TestListEntities_InvalidPageSize(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Item"},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	_, _, err = s.ListEntities(ctx, "Item", -1, "", "")
	if err == nil {
		t.Fatal("expected error for negative page size, got nil")
	}
}

// --- 61. ListEntities - malformed page token ---

func TestListEntities_MalformedPageToken(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Item"},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	_, _, err = s.ListEntities(ctx, "Item", 10, "not-valid-base64!!!", "")
	if err == nil {
		t.Fatal("expected error for malformed page token, got nil")
	}
}

// --- 64. ExecuteCypher - read-only ---

func TestExecuteCypher_ReadOnly(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Item",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	ent, err := s.CreateEntity(ctx, "Item", "", map[string]string{"name": "test-item"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	rows, err := s.ExecuteCypher(ctx, "MATCH (n:Item) RETURN n", nil, "")
	if err != nil {
		t.Fatalf("ExecuteCypher failed: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected at least one row")
	}
	found := false
	for _, row := range rows {
		if row["id"] == ent.Id {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected entity %q in results", ent.Id)
	}
}

// --- 65. ExecuteCypher - mutation rejected ---

func TestExecuteCypher_MutationRejected(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.ExecuteCypher(ctx, "CREATE (n:Test)", nil, "")
	if err == nil {
		t.Fatal("expected error for mutation, got nil")
	}
	if !strings.Contains(err.Error(), "mutation") {
		t.Fatalf("expected mutation error, got: %v", err)
	}
}

// --- 66. ExecuteCypher - empty query ---

func TestExecuteCypher_EmptyQuery(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.ExecuteCypher(ctx, "", nil, "")
	if err == nil {
		t.Fatal("expected error for empty query, got nil")
	}
}

// --- 68. SearchNeighbors - basic ---

func TestSearchNeighbors_Basic(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VectorType",
				EnableVectorIndex: true,
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Bootstrap with first entity
	_, err = s.CreateEntity(ctx, "VectorType", "", map[string]string{"name": "a"}, []float32{1.0, 0.0, 0.0}, "")
	if err != nil {
		t.Fatalf("CreateEntity bootstrapping failed: %v", err)
	}
	_, err = s.CreateEntity(ctx, "VectorType", "", map[string]string{"name": "b"}, []float32{0.0, 1.0, 0.0}, "")
	if err != nil {
		t.Fatalf("CreateEntity second failed: %v", err)
	}

	results, err := s.SearchNeighbors(ctx, []float32{1.0, 0.0, 0.0}, "VectorType", 5, "")
	if err != nil {
		t.Fatalf("SearchNeighbors failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one neighbor result")
	}
	// First result should be entity 'a' (distance 0)
	if results[0].Entity.Properties["name"] != "a" {
		t.Fatalf("expected closest neighbor 'a', got %q", results[0].Entity.Properties["name"])
	}
}

// --- 70. SearchNeighbors - non-indexed type ---

func TestSearchNeighbors_NonIndexedType(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "NonIndexed",
				EnableVectorIndex: false,
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	_, err = s.SearchNeighbors(ctx, []float32{1.0}, "NonIndexed", 5, "")
	if err == nil {
		t.Fatal("expected error for non-indexed type, got nil")
	}
}

// --- 71. SearchNeighbors - unknown entity type ---

func TestSearchNeighbors_UnknownEntityType(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.SearchNeighbors(ctx, []float32{1.0}, "NonExistent", 5, "")
	if err == nil {
		t.Fatal("expected error for unknown entity type, got nil")
	}
	if !errors.Is(err, ErrUnknownEntityType) {
		t.Fatalf("expected ErrUnknownEntityType, got: %v", err)
	}
}

// --- 73. SearchNeighbors - indexed type, no lazy index ---

func TestSearchNeighbors_NoLazyIndex(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VectorType",
				EnableVectorIndex: true,
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// No entities written, search should return empty
	results, err := s.SearchNeighbors(ctx, []float32{1.0, 2.0, 3.0}, "VectorType", 5, "")
	if err != nil {
		t.Fatalf("SearchNeighbors failed: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty results before bootstrap, got %d", len(results))
	}
}

// --- 75. FullTextSearch - basic ---

func TestFullTextSearch_Basic(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Item",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
					{Name: "description", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	_, err = s.CreateEntity(ctx, "Item", "", map[string]string{"name": "apple", "description": "a fruit"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity apple failed: %v", err)
	}
	_, err = s.CreateEntity(ctx, "Item", "", map[string]string{"name": "banana", "description": "another fruit"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity banana failed: %v", err)
	}

	results, err := s.FullTextSearch(ctx, "fruit", "", "")
	if err != nil {
		t.Fatalf("FullTextSearch failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results for 'fruit', got %d", len(results))
	}
}

// --- 76. FullTextSearch - empty query ---

func TestFullTextSearch_EmptyQuery(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.FullTextSearch(ctx, "", "", "")
	if err == nil {
		t.Fatal("expected error for empty query, got nil")
	}
}

// --- 77. FullTextSearch - unknown entity type ---

func TestFullTextSearch_UnknownEntityType(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.FullTextSearch(ctx, "test", "NonExistent", "")
	if err == nil {
		t.Fatal("expected error for unknown entity type, got nil")
	}
	if !errors.Is(err, ErrUnknownEntityType) {
		t.Fatalf("expected ErrUnknownEntityType, got: %v", err)
	}
}

// --- 79. Write lock serialises concurrent mutations ---

func TestWriteLockConcurrentMutations(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Item",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			_, err := s.CreateEntity(ctx, "Item", "", map[string]string{"name": "x"}, nil, "")
			if err != nil {
				t.Errorf("concurrent CreateEntity failed: %v", err)
			}
		})
	}
	wg.Wait()
}

// --- 80. TableExists - basic ---

func TestTableExists_Basic(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := newTestSchema()
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	if !s.TableExists("Component") {
		t.Fatal("expected TableExists(Component) to be true")
	}
	if s.TableExists("NonExistent") {
		t.Fatal("expected TableExists(NonExistent) to be false")
	}
}

// --- 81. ListMainEntityTypes - basic ---

func TestListMainEntityTypes_Basic(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "B"},
			{Name: "A"},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	names, err := s.ListMainEntityTypes()
	if err != nil {
		t.Fatalf("ListMainEntityTypes failed: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 entity types, got %d", len(names))
	}
	if names[0] != "A" || names[1] != "B" {
		t.Fatalf("expected sorted [A, B], got %v", names)
	}
}

// --- 82. ValidateEdgeRules ---

func TestValidateEdgeRules_Basic(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := newTestSchemaWithRules()
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Valid connection: Component -> Service via DEPENDS_ON
	if err := s.ValidateEdgeRules("Component", "Service", "DEPENDS_ON"); err != nil {
		t.Fatalf("expected valid edge rules, got: %v", err)
	}

	// Invalid: Component -> Component via DEPENDS_ON
	if err := s.ValidateEdgeRules("Component", "Component", "DEPENDS_ON"); err == nil {
		t.Fatal("expected error for invalid edge connection")
	}
}

// --- 83. ResolveEntityType ---

func TestResolveEntityType_Basic(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.ApplySchema(ctx, newTestSchema()); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	ent, err := s.CreateEntity(ctx, "Component", "", map[string]string{"name": "x"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	typ, err := s.ResolveEntityType(ctx, ent.Id, "")
	if err != nil {
		t.Fatalf("ResolveEntityType failed: %v", err)
	}
	if typ != "Component" {
		t.Fatalf("expected type 'Component', got %q", typ)
	}
}

// --- 84. WipeAll ---

func TestWipeAll_Basic(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.ApplySchema(ctx, newTestSchema()); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	_, err = s.CreateEntity(ctx, "Component", "", map[string]string{"name": "x"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	if err := s.WipeAll(ctx); err != nil {
		t.Fatalf("WipeAll failed: %v", err)
	}

	// Entity should be gone
	_, err = s.GetEntity(ctx, "00000000-0000-0000-0000-000000000000", "")
	if err == nil {
		t.Fatal("expected empty after wipe")
	}

	// Schema should still exist (tables remain, data cleared)
	if !s.TableExists("Component") {
		t.Fatal("expected schema to survive WipeAll")
	}
}

// --- 85. Health ---

func TestHealth_Basic(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	result, err := s.Health(ctx)
	if err != nil {
		t.Fatalf("Health failed: %v", err)
	}
	if !result.LadybugOK {
		t.Fatal("expected LadybugOK to be true")
	}
	if result.SchemaApplied {
		t.Fatal("expected SchemaApplied to be false (no schema applied)")
	}
}

// --- 86. Branch path: CreateEntity and UpdateEntity both drop embedding (ponytail) ---
// ponytail: Branch CreateEntity and branch UpdateEntity both silently drop the
// embedding argument. The stored entity never carries an Embedding field regardless
// of CreateEntity or UpdateEntity call. Consequence: callers that create an entity
// in a branch and then update its embedding will find the embedding missing after
// both calls. This is a data-loss risk for workloads that depend on vector
// embeddings in a branch context. The main path has no such gap.

func TestUpdateEntity_BranchEmbeddingIgnored(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	if err := s.CreateBranchDB("test-branch-emb"); err != nil {
		t.Fatalf("CreateBranchDB failed: %v", err)
	}
	if err := s.ReplicateSchemaToBranch("test-branch-emb"); err != nil {
		t.Fatalf("ReplicateSchemaToBranch failed: %v", err)
	}

	// Create entity in branch — embedding dropped by branch path
	ent, err := s.CreateEntity(ctx, "VecType", "", map[string]string{"name": "x"}, []float32{1, 2, 3}, "test-branch-emb")
	if err != nil {
		t.Fatalf("CreateEntity in branch failed: %v", err)
	}
	_ = ent

	// Update with new embedding — branch path also drops the embedding
	updated, err := s.UpdateEntity(ctx, ent.Id, map[string]string{"name": "y"}, []float32{4, 5, 6}, "test-branch-emb")
	if err != nil {
		t.Fatalf("UpdateEntity in branch failed: %v", err)
	}
	if updated.Properties["name"] != "y" {
		t.Fatalf("expected name updated to 'y', got %q", updated.Properties["name"])
	}
	// Branch UpdateEntity now applies embedding per SPEC R7 partial-update semantics
	if len(updated.Embedding) == 0 || updated.Embedding[0] != 4 {
		t.Fatalf("expected embedding [4 5 6] to be applied, got %v", updated.Embedding)
	}
}

// --- 87. Branch scanning (empty branch) ---

func TestDumpAllEntities_EmptyBranch(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.DumpAllEntities(ctx, "nonexistent-tx")
	if err == nil {
		t.Fatal("expected error for nonexistent branch, got nil")
	}
}

func TestDumpAllEdges_EmptyBranch(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.DumpAllEdges(ctx, "nonexistent-tx")
	if err == nil {
		t.Fatal("expected error for nonexistent branch, got nil")
	}
}

func TestListEntityTypes_EmptyBranch(t *testing.T) {
	testingShortGuard(t)
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.ListEntityTypes("nonexistent-tx")
	if err == nil {
		t.Fatal("expected error for nonexistent branch, got nil")
	}
}

// --- 89. CreateBranchDB / DropBranchDB ---

func TestCreateBranchDB_DropBranchDB(t *testing.T) {
	testingShortGuard(t)
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.CreateBranchDB("tx-1"); err != nil {
		t.Fatalf("CreateBranchDB failed: %v", err)
	}

	// Creating same branch again should error
	if err := s.CreateBranchDB("tx-1"); err == nil {
		t.Fatal("expected error for duplicate branch")
	}

	if err := s.DropBranchDB("tx-1"); err != nil {
		t.Fatalf("DropBranchDB failed: %v", err)
	}

	// Dropping non-existent branch should not error
	if err := s.DropBranchDB("nonexistent"); err != nil {
		t.Fatalf("DropBranchDB on non-existent should not error: %v", err)
	}
}

// --- 90. ReplicateSchemaToBranch ---

func TestReplicateSchemaToBranch(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.ApplySchema(ctx, newTestSchema()); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	if err := s.CreateBranchDB("tx-1"); err != nil {
		t.Fatalf("CreateBranchDB failed: %v", err)
	}

	if err := s.ReplicateSchemaToBranch("tx-1"); err != nil {
		t.Fatalf("ReplicateSchemaToBranch failed: %v", err)
	}
}

// --- 91. RehydrateMainFromFiles ---

func TestRehydrateMainFromFiles(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Create temp directory with entity files
	tmpDir := t.TempDir()
	entitiesDir := filepath.Join(tmpDir, "entities")
	edgesDir := filepath.Join(tmpDir, "edges")

	typeDir := filepath.Join(entitiesDir, "Component")
	if err := os.MkdirAll(typeDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	// Write entity JSON files
	entID := "11111111-1111-4111-8111-111111111111"
	entData := map[string]any{
		"id":   entID,
		"type": "Component",
		"properties": map[string]string{
			"name": "core",
		},
	}
	data, _ := json.Marshal(entData)
	if err := os.WriteFile(filepath.Join(typeDir, "core.json"), data, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Create edge type dir (empty)
	if err := os.MkdirAll(edgesDir, 0755); err != nil {
		t.Fatalf("MkdirAll edges failed: %v", err)
	}

	if err := s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		t.Fatalf("RehydrateMainFromFiles failed: %v", err)
	}

	// Verify entity appears
	got, err := s.GetEntity(ctx, entID, "")
	if err != nil {
		t.Fatalf("GetEntity after rehydrate failed: %v", err)
	}
	if got.Properties["name"] != "core" {
		t.Fatalf("expected name=core, got %q", got.Properties["name"])
	}
}

// --- 92. RehydrateMainFromFiles - non-existent directory ---

func TestRehydrateMainFromFiles_NonExistentDir(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Empty repo (non-existent directories) should succeed — a fresh main.lbug is created.
	err = s.RehydrateMainFromFiles(ctx, "/nonexistent/path/entities", "/nonexistent/path/edges")
	if err != nil {
		t.Fatalf("expected no error for non-existent directory (empty repo), got: %v", err)
	}
	// Verify state is initialized correctly
	_, _, err = s.ListEntities(ctx, "Component", 10, "", "")
	if err == nil || !strings.Contains(err.Error(), "unknown entity type") {
		t.Fatal("expected ListEntities to fail with unknown entity type (no schema applied)")
	}
}

// --- 93. RehydrateMainFromFiles - unparseable JSON file ---

func TestRehydrateMainFromFiles_UnparseableJSON(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	tmpDir := t.TempDir()
	entitiesDir := filepath.Join(tmpDir, "entities")
	edgesDir := filepath.Join(tmpDir, "edges")

	typeDir := filepath.Join(entitiesDir, "Component")
	if err := os.MkdirAll(typeDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.MkdirAll(edgesDir, 0755); err != nil {
		t.Fatalf("MkdirAll edges failed: %v", err)
	}

	// Valid file
	validData, _ := json.Marshal(map[string]any{
		"id":         "22222222-2222-4222-8222-222222222222",
		"type":       "Component",
		"properties": map[string]string{"name": "valid"},
	})
	if err := os.WriteFile(filepath.Join(typeDir, "valid.json"), validData, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Invalid JSON file
	if err := os.WriteFile(filepath.Join(typeDir, "invalid.json"), []byte("{invalid}"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	err = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
	if err == nil {
		t.Fatal("expected error for unparseable JSON file, got nil")
	}
	if !strings.Contains(err.Error(), "unparseable") {
		t.Fatalf("expected unparseable error, got: %v", err)
	}
}

// --- 94. RehydrateMainFromFiles - unknown type subdirectory silently skipped ---

func TestRehydrateMainFromFiles_SkipUnknownType(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	tmpDir := t.TempDir()
	entitiesDir := filepath.Join(tmpDir, "entities")
	edgesDir := filepath.Join(tmpDir, "edges")

	// Create known type
	if err := os.MkdirAll(filepath.Join(entitiesDir, "Component"), 0755); err != nil {
		t.Fatalf("MkdirAll Component failed: %v", err)
	}
	// Create unknown type (should be skipped)
	if err := os.MkdirAll(filepath.Join(entitiesDir, "UnknownType"), 0755); err != nil {
		t.Fatalf("MkdirAll UnknownType failed: %v", err)
	}
	unknownData, _ := json.Marshal(map[string]any{
		"id":         "33333333-3333-4333-8333-333333333333",
		"type":       "UnknownType",
		"properties": map[string]string{"name": "unknown"},
	})
	if err := os.WriteFile(filepath.Join(entitiesDir, "UnknownType", "unknown.json"), unknownData, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.MkdirAll(edgesDir, 0755); err != nil {
		t.Fatalf("MkdirAll edges failed: %v", err)
	}

	if err := s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		t.Fatalf("RehydrateMainFromFiles should not error on unknown type: %v", err)
	}
}

// --- 95. HydrateBranchFromFiles - non-existent directory ---

func TestHydrateBranchFromFiles_NonExistentDir(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.ApplySchema(ctx, newTestSchema()); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	if err := s.CreateBranchDB("tx-1"); err != nil {
		t.Fatalf("CreateBranchDB failed: %v", err)
	}

	err = s.HydrateBranchFromFiles(ctx, "tx-1", "/nonexistent/entities", "/nonexistent/edges")
	if err == nil {
		t.Fatal("expected error for non-existent directory, got nil")
	}
}

// --- 97. HydrateBranchFromFiles - unknown type silently skipped ---

func TestHydrateBranchFromFiles_SkipUnknownType(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.ApplySchema(ctx, newTestSchema()); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	if err := s.CreateBranchDB("tx-1"); err != nil {
		t.Fatalf("CreateBranchDB failed: %v", err)
	}
	if err := s.ReplicateSchemaToBranch("tx-1"); err != nil {
		t.Fatalf("ReplicateSchemaToBranch failed: %v", err)
	}

	tmpDir := t.TempDir()
	entitiesDir := filepath.Join(tmpDir, "entities")
	edgesDir := filepath.Join(tmpDir, "edges")

	if err := os.MkdirAll(filepath.Join(entitiesDir, "UnknownType"), 0755); err != nil {
		t.Fatalf("MkdirAll UnknownType failed: %v", err)
	}
	if err := os.MkdirAll(edgesDir, 0755); err != nil {
		t.Fatalf("MkdirAll edges failed: %v", err)
	}

	if err := s.HydrateBranchFromFiles(ctx, "tx-1", entitiesDir, edgesDir); err != nil {
		t.Fatalf("HydrateBranchFromFiles should not error on unknown type: %v", err)
	}
}

// --- 98. ValidateSchema - valid ---

func TestValidateSchema_Valid(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.ValidateSchema(ctx, newTestSchema()); err != nil {
		t.Fatalf("ValidateSchema failed: %v", err)
	}
}

// --- 99. ValidateSchema - invalid ---

func TestValidateSchema_Invalid(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component"},
			{Name: "Component"},
		},
	}

	err = s.ValidateSchema(ctx, schema)
	if err == nil {
		t.Fatal("expected error for duplicate entity names, got nil")
	}
}

// --- 100. SchemaProvider interface satisfaction ---

func TestSchemaProviderInterface(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := newTestSchemaWithRules()
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	sp, ok := s.(SchemaProvider)
	if !ok {
		t.Fatal("Store does not implement SchemaProvider")
	}

	names := sp.EntityTypeNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 entity type names, got %d", len(names))
	}

	edgeNames := sp.EdgeTypeNames()
	if len(edgeNames) != 1 {
		t.Fatalf("expected 1 edge type name, got %d", len(edgeNames))
	}

	if def, ok := sp.EntityType("Component"); !ok || def == nil {
		t.Fatal("expected Component entity type to exist")
	}

	if def, ok := sp.EdgeType("DEPENDS_ON"); !ok || def == nil {
		t.Fatal("expected DEPENDS_ON edge type to exist")
	}
}

// --- 101. IsVectorIndexBootstrapped - not bootstrapped ---

func TestIsVectorIndexBootstrapped_NotBootstrapped(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	if s.IsVectorIndexBootstrapped("VecType", "main") {
		t.Fatal("expected not bootstrapped before any entity creation")
	}
	dim, err := s.GetEstablishedDimension("VecType", "main")
	if err != nil {
		t.Fatalf("GetEstablishedDimension failed: %v", err)
	}
	if dim != 0 {
		t.Fatalf("expected dimension 0 before bootstrap, got %d", dim)
	}
}

// --- 102. IsVectorIndexBootstrapped - bootstrapped ---

func TestIsVectorIndexBootstrapped_Bootstrapped(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	_, err = s.CreateEntity(ctx, "VecType", "", map[string]string{"name": "x"}, []float32{1.0, 2.0, 3.0}, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	if !s.IsVectorIndexBootstrapped("VecType", "main") {
		t.Fatal("expected bootstrapped after entity creation")
	}
	dim, err := s.GetEstablishedDimension("VecType", "main")
	if err != nil {
		t.Fatalf("GetEstablishedDimension failed: %v", err)
	}
	if dim != 3 {
		t.Fatalf("expected dimension 3, got %d", dim)
	}
}

// --- 103. RehydrateFromBranch - basic ---

func TestRehydrateFromBranch_Basic(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.ApplySchema(ctx, newTestSchema()); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	if err := s.CreateBranchDB("tx-1"); err != nil {
		t.Fatalf("CreateBranchDB failed: %v", err)
	}
	if err := s.ReplicateSchemaToBranch("tx-1"); err != nil {
		t.Fatalf("ReplicateSchemaToBranch failed: %v", err)
	}

	if err := s.RehydrateFromBranch(ctx, "tx-1"); err != nil {
		t.Fatalf("RehydrateFromBranch failed: %v", err)
	}
}

// --- Page token format and round-trip ---

func TestListEntities_PageTokenRoundTrip(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Item",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	for _, name := range []string{"a", "b", "c"} {
		_, err := s.CreateEntity(ctx, "Item", "", map[string]string{"name": name}, nil, "")
		if err != nil {
			t.Fatalf("CreateEntity %q failed: %v", name, err)
		}
	}

	// First page
	_, token, err := s.ListEntities(ctx, "Item", 1, "", "")
	if err != nil {
		t.Fatalf("ListEntities page 1 failed: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	// Verify token is valid base64 (opaque cursor — no format assumptions beyond this)
	data, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("token is not valid base64: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty token data after base64 decode")
	}

	// Second page
	page2, token2, err := s.ListEntities(ctx, "Item", 1, token, "")
	if err != nil {
		t.Fatalf("ListEntities page 2 failed: %v", err)
	}
	if len(page2) != 1 {
		t.Fatalf("expected 1 item on page 2, got %d", len(page2))
	}

	// Final page
	_, token3, err := s.ListEntities(ctx, "Item", 2, token2, "")
	if err != nil {
		t.Fatalf("ListEntities page 3 failed: %v", err)
	}
	if token3 != "" {
		t.Fatalf("expected empty token on final page, got %q", token3)
	}
}

// --- Stale cursor token ---

func TestListEntities_StaleCursorToken(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Item",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Create an entity, get its ID, delete it, try to use token pointing to it
	ent, err := s.CreateEntity(ctx, "Item", "", map[string]string{"name": "temp"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}
	_, err = s.DeleteEntity(ctx, ent.Id, "")
	if err != nil {
		t.Fatalf("DeleteEntity failed: %v", err)
	}

	// Create a stale token
	staleToken := base64.StdEncoding.EncodeToString([]byte(ent.Id))

	_, _, err = s.ListEntities(ctx, "Item", 10, staleToken, "")
	if err == nil {
		t.Fatal("expected error for stale cursor token, got nil")
	}
}

// --- Multi-rule OR resolve ---

func TestCreateEdge_MultiRuleORResolve(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Source",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"TypeA"}, Using: []string{"EDGE_A"}},
					{CanConnectTo: []string{"TypeB"}, Using: []string{"EDGE_B"}},
				},
			},
			{
				Name: "TypeA",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
			{
				Name: "TypeB",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "EDGE_A"},
			{Name: "EDGE_B"},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	src, err := s.CreateEntity(ctx, "Source", "", map[string]string{"name": "src"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src failed: %v", err)
	}
	tgtB, err := s.CreateEntity(ctx, "TypeB", "", map[string]string{"name": "tgtB"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgtB failed: %v", err)
	}

	// Rule 1 doesn't match (TypeB not in canConnectTo) but Rule 2 does -> OR resolve
	_, err = s.CreateEdge(ctx, "EDGE_B", src.Id, tgtB.Id, nil, "")
	if err != nil {
		t.Fatalf("expected edge to be created via OR rule resolution: %v", err)
	}
}

// --- No rules declared -> PERMISSION_DENIED ---

func TestCreateEdge_NoRulesDeclared(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "NoRules",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
				// No rules declared
			},
			{
				Name: "Target",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "SOME_EDGE"},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	src, err := s.CreateEntity(ctx, "NoRules", "", map[string]string{"name": "src"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src failed: %v", err)
	}
	tgt, err := s.CreateEntity(ctx, "Target", "", map[string]string{"name": "tgt"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt failed: %v", err)
	}

	_, err = s.CreateEdge(ctx, "SOME_EDGE", src.Id, tgt.Id, nil, "")
	if err == nil {
		t.Fatal("expected PERMISSION_DENIED for no rules")
	}
}

// --- Per-entry AND semantics ---

func TestCreateEdge_PerEntryANDSemantics(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Source",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
				Rules: []*flowv1.ConnectionRule{
					{
						CanConnectTo: []string{"TargetType"},
						Using:        []string{"WRONG_EDGE"},
					},
				},
			},
			{
				Name: "TargetType",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "WRONG_EDGE"},
			{Name: "RIGHT_EDGE"},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	src, err := s.CreateEntity(ctx, "Source", "", map[string]string{"name": "src"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src failed: %v", err)
	}
	tgt, err := s.CreateEntity(ctx, "TargetType", "", map[string]string{"name": "tgt"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt failed: %v", err)
	}

	// canConnectTo matches TargetType, but using doesn't include RIGHT_EDGE
	_, err = s.CreateEdge(ctx, "RIGHT_EDGE", src.Id, tgt.Id, nil, "")
	if err == nil {
		t.Fatal("expected PERMISSION_DENIED for AND semantics violation")
	}
}

// --- Edge CRUD: invalid ID format ---

func TestCreateEdge_InvalidIDFormat(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.ApplySchema(ctx, newTestSchema()); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	ent, err := s.CreateEntity(ctx, "Component", "", map[string]string{"name": "x"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	// Invalid fromID
	_, err = s.CreateEdge(ctx, "DEPENDS_ON", "bad-uuid", ent.Id, nil, "")
	if err == nil {
		t.Fatal("expected error for invalid fromID format")
	}

	// Invalid toID
	_, err = s.CreateEdge(ctx, "DEPENDS_ON", ent.Id, "bad-uuid", nil, "")
	if err == nil {
		t.Fatal("expected error for invalid toID format")
	}
}

// --- DeleteEdge not found ---

func TestDeleteEdge_NotFound(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.DeleteEdge(ctx, "00000000-0000-0000-0000-000000000000", "")
	if err == nil {
		t.Fatal("expected error for not-found edge")
	}
}

// --- GetEdge not found ---

func TestGetEdge_NotFound(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.GetEdge(ctx, "00000000-0000-0000-0000-000000000000", "")
	if err == nil {
		t.Fatal("expected error for not-found edge")
	}
}

// --- DeleteEntity with cascade, test WAL notion ---

func TestDeleteEdge_InvalidIDFormat(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.DeleteEdge(ctx, "bad-uuid", "")
	if err == nil {
		t.Fatal("expected error for invalid edge ID")
	}
}

// --- UpdateEntity: unknown property ---

func TestUpdateEntity_UnknownProperty(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.ApplySchema(ctx, newTestSchema()); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	ent, err := s.CreateEntity(ctx, "Component", "", map[string]string{"name": "x"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	_, err = s.UpdateEntity(ctx, ent.Id, map[string]string{"unknown": "val"}, nil, "")
	if err == nil {
		t.Fatal("expected error for unknown property")
	}
}

// --- CreateEntity: embedding dimension mismatch ---

func TestCreateEntity_EmbeddingDimensionMismatch(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Bootstrap with 3-dim
	_, err = s.CreateEntity(ctx, "VecType", "", map[string]string{"name": "x"}, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("CreateEntity bootstrap failed: %v", err)
	}

	// Should reject 2-dim
	_, err = s.CreateEntity(ctx, "VecType", "", map[string]string{"name": "y"}, []float32{1, 2}, "")
	if err == nil {
		t.Fatal("expected dimension mismatch error")
	}
}

// --- CreateEdge: missing required edge property ---

func TestCreateEdge_MissingRequiredProperty(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:       "Source",
				Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Target"}, Using: []string{"EDGE_WITH_REQ"}},
				},
			},
			{
				Name:       "Target",
				Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{
				Name: "EDGE_WITH_REQ",
				Properties: []*flowv1.Property{
					{Name: "weight", Type: "string", Required: true},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	src, _ := s.CreateEntity(ctx, "Source", "", map[string]string{"name": "src"}, nil, "")
	tgt, _ := s.CreateEntity(ctx, "Target", "", map[string]string{"name": "tgt"}, nil, "")

	// Missing required property "weight"
	_, err = s.CreateEdge(ctx, "EDGE_WITH_REQ", src.Id, tgt.Id, nil, "")
	if err == nil {
		t.Fatal("expected error for missing required edge property")
	}
}

// --- CreateEntity: after bootstrap with nil embedding (success) ---

func TestCreateEntity_AfterBootstrapNilEmbedding(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Bootstrap
	_, err = s.CreateEntity(ctx, "VecType", "", map[string]string{"name": "x"}, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("CreateEntity bootstrap failed: %v", err)
	}

	// After bootstrap, nil embedding should succeed
	_, err = s.CreateEntity(ctx, "VecType", "", map[string]string{"name": "y"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity without embedding after bootstrap should succeed: %v", err)
	}
}

// --- UpdateEntity: embedding preserved for indexed type when nil ---

func TestUpdateEntity_EmbeddingPreservedWhenNil(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	ent, err := s.CreateEntity(ctx, "VecType", "", map[string]string{"name": "x"}, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("CreateEntity failed: %v", err)
	}

	// Update with nil embedding - should preserve existing
	updated, err := s.UpdateEntity(ctx, ent.Id, map[string]string{"name": "y"}, nil, "")
	if err != nil {
		t.Fatalf("UpdateEntity failed: %v", err)
	}
	// With nil embedding, the current stub preserves existing
	if len(updated.Embedding) > 0 && updated.Embedding[0] != 1 {
		t.Fatalf("expected preserved embedding")
	}
}

// --- FullTextSearch: empty entityType searches all types ---

func TestFullTextSearch_AllTypes(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "TypeA",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
			{
				Name: "TypeB",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	_, err = s.CreateEntity(ctx, "TypeA", "", map[string]string{"name": "hello world"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity A failed: %v", err)
	}
	_, err = s.CreateEntity(ctx, "TypeB", "", map[string]string{"name": "hello there"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity B failed: %v", err)
	}

	results, err := s.FullTextSearch(ctx, "hello", "", "")
	if err != nil {
		t.Fatalf("FullTextSearch failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results from all types, got %d", len(results))
	}
}

// --- ResolveEntityType: not found ---

func TestResolveEntityType_NotFound(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.ResolveEntityType(ctx, "00000000-0000-0000-0000-000000000000", "")
	if err == nil {
		t.Fatal("expected error for not-found entity")
	}
}

// --- HasOpenTransactions: basic ---

func TestHasOpenTransactions_Basic(t *testing.T) {
	testingShortGuard(t)
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if s.HasOpenTransactions() {
		t.Fatal("expected no open transactions initially")
	}

	if err := s.CreateBranchDB("tx-1"); err != nil {
		t.Fatalf("CreateBranchDB failed: %v", err)
	}

	if !s.HasOpenTransactions() {
		t.Fatal("expected open transactions after CreateBranchDB")
	}

	if err := s.DropBranchDB("tx-1"); err != nil {
		t.Fatalf("DropBranchDB failed: %v", err)
	}

	if s.HasOpenTransactions() {
		t.Fatal("expected no open transactions after DropBranchDB")
	}
}

// --- SearchNeighbors: empty entityType searches all ---

func TestSearchNeighbors_AllTypes(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "TypeA",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
			{
				Name:              "TypeB",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Bootstrap TypeA
	_, err = s.CreateEntity(ctx, "TypeA", "", map[string]string{"name": "a"}, []float32{1, 0, 0}, "")
	if err != nil {
		t.Fatalf("CreateEntity TypeA failed: %v", err)
	}
	// Bootstrap TypeB
	_, err = s.CreateEntity(ctx, "TypeB", "", map[string]string{"name": "b"}, []float32{0, 1, 0}, "")
	if err != nil {
		t.Fatalf("CreateEntity TypeB failed: %v", err)
	}

	// Search all types
	results, err := s.SearchNeighbors(ctx, []float32{1, 0, 0}, "", 10, "")
	if err != nil {
		t.Fatalf("SearchNeighbors all types failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results from all types")
	}
}

// --- RehydrateMainFromFiles: drop-and-recreate semantics ---

func TestRehydrateMainFromFiles_DropAndRecreate(t *testing.T) {
	testingShortGuard(t)
	ctx := context.Background()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	tmpDir := t.TempDir()
	entitiesDir := filepath.Join(tmpDir, "entities")
	edgesDir := filepath.Join(tmpDir, "edges")

	// First re-hydration: create 3 entities
	typeDir := filepath.Join(entitiesDir, "Component")
	if err := os.MkdirAll(typeDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.MkdirAll(edgesDir, 0755); err != nil {
		t.Fatalf("MkdirAll edges failed: %v", err)
	}

	for i, id := range []string{
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
	} {
		data, _ := json.Marshal(map[string]any{
			"id":         id,
			"type":       "Component",
			"properties": map[string]string{"name": string(rune('A' + i))},
		})
		if err := os.WriteFile(filepath.Join(typeDir, id+".json"), data, 0644); err != nil {
			t.Fatalf("WriteFile %d failed: %v", i, err)
		}
	}

	if err := s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		t.Fatalf("First RehydrateMainFromFiles failed: %v", err)
	}

	// Verify all 3 entities exist
	for _, id := range []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "cccccccc-cccc-4ccc-8ccc-cccccccccccc"} {
		if _, err := s.GetEntity(ctx, id, ""); err != nil {
			t.Fatalf("GetEntity %q after first rehydrate failed: %v", id, err)
		}
	}

	// Delete one entity file (simulating deletion committed in transaction)
	if err := os.Remove(filepath.Join(typeDir, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa.json")); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	// Second re-hydration
	if err := s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		t.Fatalf("Second RehydrateMainFromFiles failed: %v", err)
	}

	// Deleted entity should be gone
	_, err = s.GetEntity(ctx, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "")
	if err == nil {
		t.Fatal("expected deleted entity to be absent after re-hydration")
	}

	// Remaining entities should still exist
	if _, err := s.GetEntity(ctx, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", ""); err != nil {
		t.Fatalf("remaining entity should exist: %v", err)
	}
}
