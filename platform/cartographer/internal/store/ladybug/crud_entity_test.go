package ladybug

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/google/uuid"
)

func TestCreateEntity_Valid(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	e, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "comp1"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if e.Id == "" {
		t.Error("expected non-empty Id")
	}
	if e.Type != "Component" {
		t.Errorf("Type = %q, want %q", e.Type, "Component")
	}
	if e.Properties["name"] != "comp1" {
		t.Errorf("Properties[name] = %q, want %q", e.Properties["name"], "comp1")
	}
}

func TestCreateEntity_DuplicateID(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	id := uuid.New().String()
	_, err = s.CreateEntity(context.Background(), "Component", id, nil, nil, "")
	if err != nil {
		t.Fatalf("first CreateEntity: %v", err)
	}
	_, err = s.CreateEntity(context.Background(), "Component", id, nil, nil, "")
	if err == nil {
		t.Fatal("expected error for duplicate ID")
	}
	if !errors.Is(err, store.ErrEntityAlreadyExists) {
		t.Errorf("expected ErrEntityAlreadyExists, got %v", err)
	}
}

func TestCreateEntity_InvalidUUID(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.CreateEntity(context.Background(), "Component", "not-a-uuid", nil, nil, "")
	if err == nil {
		t.Fatal("expected error for invalid UUID")
	}
	if !errors.Is(err, store.ErrInvalidIDFormat) {
		t.Errorf("expected ErrInvalidIDFormat, got %v", err)
	}
}

func TestCreateEntity_NonCanonicalUUIDSpellingRejected(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	id := uuid.New().String()
	if _, err := s.CreateEntity(context.Background(), "Component", id, nil, nil, ""); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	// The same UUID in a non-canonical spelling must be rejected at the ID
	// format gate, never stored as a second entity — accepting it would let
	// two spellings of one UUID become two entities and bypass the
	// ALREADY_EXISTS check (SPEC:942). The rejection surfaces on the
	// INVALID_ARGUMENT-style store.ErrInvalidIDFormat path (SPEC:941).
	_, err = s.CreateEntity(context.Background(), "Component", strings.ToUpper(id), nil, nil, "")
	if err == nil {
		t.Fatal("expected error for non-canonical UUID spelling")
	}
	if !errors.Is(err, store.ErrInvalidIDFormat) {
		t.Errorf("expected ErrInvalidIDFormat, got %v", err)
	}
}

func TestCreateEntity_UnknownType(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.CreateEntity(context.Background(), "NoSuchType", "", nil, nil, "")
	if err == nil {
		t.Fatal("expected error for unknown entity type")
	}
	if !errors.Is(err, store.ErrUnknownEntityType) {
		t.Errorf("expected ErrUnknownEntityType, got %v", err)
	}
}

func TestCreateEntity_MissingRequiredProperty(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	reqSchema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string", Required: true},
				},
			},
		},
	}
	if err := s.ApplySchema(context.Background(), reqSchema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	_, err = s.CreateEntity(context.Background(), "Component", "", map[string]string{}, nil, "")
	if err == nil {
		t.Fatal("expected error for missing required property")
	}
	if !errors.Is(err, store.ErrMissingRequiredProperty) {
		t.Errorf("expected ErrMissingRequiredProperty, got %v", err)
	}
}

// TestCreateEntity_StructuralErrorBeforeDuplicateID asserts the check-order
// "structural validation → data-integrity" (SPEC ~943): a duplicate explicit id
// combined with an unknown or missing-required property must surface the
// structurally-prior INVALID_ARGUMENT error, not ErrEntityAlreadyExists.
func TestCreateEntity_StructuralErrorBeforeDuplicateID(t *testing.T) {
	reqSchema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string", Required: true},
				},
			},
		},
	}

	id := uuid.New().String()
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	// Seed an entity with the explicit id so a second create is a duplicate.
	if _, err := s.CreateEntity(context.Background(), "Component", id,
		map[string]string{"name": "first"}, nil, ""); err != nil {
		t.Fatalf("seed CreateEntity: %v", err)
	}

	// Duplicate id + unknown property → ErrUnknownProperty (INVALID_ARGUMENT),
	// not ErrEntityAlreadyExists.
	_, err = s.CreateEntity(context.Background(), "Component", id,
		map[string]string{"name": "second", "bogus": "x"}, nil, "")
	if !errors.Is(err, store.ErrUnknownProperty) {
		t.Fatalf("expected ErrUnknownProperty to take precedence, got %v", err)
	}

	// Duplicate id + missing required property → ErrMissingRequiredProperty
	// (INVALID_ARGUMENT), not ErrEntityAlreadyExists. Uses a fresh store whose
	// schema declares a required property.
	s2, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s2)
	if err := s2.ApplySchema(context.Background(), reqSchema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	id2 := uuid.New().String()
	if _, err := s2.CreateEntity(context.Background(), "Component", id2,
		map[string]string{"name": "first"}, nil, ""); err != nil {
		t.Fatalf("seed CreateEntity (required): %v", err)
	}
	_, err = s2.CreateEntity(context.Background(), "Component", id2,
		map[string]string{}, nil, "")
	if !errors.Is(err, store.ErrMissingRequiredProperty) {
		t.Fatalf("expected ErrMissingRequiredProperty to take precedence, got %v", err)
	}

	// The structural-before-data-integrity ordering (SPEC:946) extends to
	// embedding validation: a duplicate-ID create carrying an invalid
	// embedding must surface the structural INVALID_ARGUMENT, never
	// ErrEntityAlreadyExists. testSchema's VectorType is vector-indexed; seed a
	// VectorType entity with the same id to (a) make the second create a
	// duplicate and (b) bootstrap the dimension to 3.
	vecID := uuid.New().String()
	if _, err := s.CreateEntity(context.Background(), "VectorType", vecID,
		map[string]string{"name": "vec-first"}, []float32{1, 2, 3}, ""); err != nil {
		t.Fatalf("seed VectorType CreateEntity: %v", err)
	}

	// Duplicate id + NaN embedding → ErrNaNOrInfEmbedding (structural), not
	// ErrEntityAlreadyExists.
	_, err = s.CreateEntity(context.Background(), "VectorType", vecID,
		map[string]string{"name": "second"}, []float32{float32(math.NaN()), 0, 0}, "")
	if !errors.Is(err, store.ErrNaNOrInfEmbedding) {
		t.Fatalf("expected ErrNaNOrInfEmbedding to take precedence over duplicate id, got %v", err)
	}

	// Duplicate id + wrong-dimension embedding → ErrEmbeddingDimension
	// (structural), not ErrEntityAlreadyExists. VectorType's dimension is
	// locked to 3 by the seed above.
	_, err = s.CreateEntity(context.Background(), "VectorType", vecID,
		map[string]string{"name": "second"}, []float32{1, 2, 3, 4}, "")
	if !errors.Is(err, store.ErrEmbeddingDimension) {
		t.Fatalf("expected ErrEmbeddingDimension to take precedence over duplicate id, got %v", err)
	}

	// A duplicate-id create whose embedding is structurally valid still
	// surfaces the data-integrity check — matching dimension, no NaN.
	_, err = s.CreateEntity(context.Background(), "VectorType", vecID,
		map[string]string{"name": "second"}, []float32{4, 5, 6}, "")
	if !errors.Is(err, store.ErrEntityAlreadyExists) {
		t.Fatalf("expected ErrEntityAlreadyExists for structurally-valid duplicate create, got %v", err)
	}
}

func TestCreateEntity_NaNEmbeddingNonIndexed(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.CreateEntity(context.Background(), "Document", "",
		map[string]string{"title": "test"}, []float32{float32(math.NaN())}, "")
	if err == nil {
		t.Fatal("expected error for NaN embedding on non-indexed type")
	}
	if !errors.Is(err, store.ErrNaNOrInfEmbedding) {
		t.Errorf("expected ErrNaNOrInfEmbedding, got %v", err)
	}
}

func TestValidateUUID_Version4Required(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	// Version 1 UUID: xxxxxxxx-xxxx-1xxx-yxxx-xxxxxxxxxxxx
	_, err = s.GetEntity(context.Background(), "00000000-0000-1000-8000-000000000000", "")
	if err == nil {
		t.Fatal("expected error for non-v4 UUID")
	}
	if !errors.Is(err, store.ErrInvalidIDFormat) {
		t.Errorf("expected ErrInvalidIDFormat, got %v", err)
	}
}

func TestValidateUUID_InvalidFormat(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.GetEntity(context.Background(), "not-even-a-uuid", "")
	if err == nil {
		t.Fatal("expected error for malformed UUID")
	}
	if !errors.Is(err, store.ErrInvalidIDFormat) {
		t.Errorf("expected ErrInvalidIDFormat, got %v", err)
	}
}

func TestCreateEntity_UnknownProperty(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"bogus": "x"}, nil, "")
	if err == nil {
		t.Fatal("expected error for unknown property")
	}
	if !errors.Is(err, store.ErrUnknownProperty) {
		t.Errorf("expected ErrUnknownProperty, got %v", err)
	}
}

// SPEC R2: "after bootstrap, entities created without an embedding store NULL
// in the vector column". The pre-bootstrap ErrVectorBootstrap rejection
// (TestEmbeddingBootstrap_FirstEntityNoEmbedding) applies only until the first
// embedding establishes the dimension.
func TestCreateEntity_PostBootstrapNilEmbeddingStoresNULL(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	// Bootstrap VectorType to dimension 3.
	first, err := s.CreateEntity(ctx, "VectorType", "",
		map[string]string{"name": "v1"}, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("bootstrap CreateEntity: %v", err)
	}
	if len(first.Embedding) != 3 {
		t.Fatalf("expected bootstrapped embedding persisted, got %v", first.Embedding)
	}

	// A nil-embedding create after bootstrap succeeds and stores NULL: the
	// returned entity's Embedding is nil, and GetEntity returns nil too.
	plain, err := s.CreateEntity(ctx, "VectorType", "",
		map[string]string{"name": "v2"}, nil, "")
	if err != nil {
		t.Fatalf("post-bootstrap nil-embedding create must succeed, got %v", err)
	}
	if plain.Embedding != nil {
		t.Fatalf("expected nil Embedding on returned post-bootstrap entity, got %v", plain.Embedding)
	}
	got, err := s.GetEntity(ctx, plain.Id, "")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if got.Embedding != nil {
		t.Fatalf("expected NULL embedding stored for post-bootstrap entity, got %v", got.Embedding)
	}
}

// SPEC R7 (SPEC:442-443,480-481): a non-indexed entity type accepts an
// embedding of any dimension but does not persist or index it — the returned
// entity's Embedding and a subsequent GetEntity's embedding are both nil
// (accept-and-discard).
func TestCreateEntity_NonIndexedTypeDiscardsEmbedding(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	// Document is not vector-indexed in testSchema.
	e, err := s.CreateEntity(ctx, "Document", "",
		map[string]string{"title": "doc"}, []float32{1, 2, 3}, "")
	if err != nil {
		t.Fatalf("non-indexed type must accept an embedding: %v", err)
	}
	if e.Embedding != nil {
		t.Fatalf("expected nil Embedding on returned entity for non-indexed type, got %v", e.Embedding)
	}
	got, err := s.GetEntity(ctx, e.Id, "")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if got.Embedding != nil {
		t.Fatalf("expected non-indexed type to discard the embedding, got %v", got.Embedding)
	}
}
