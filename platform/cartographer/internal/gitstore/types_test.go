package gitstore

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestEntityJSONRoundTrip(t *testing.T) {
	id := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	now := time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC)
	emb := []float32{0.12, -0.34, 0.56}

	ej := EntityJSON{
		ID:         id,
		Type:       "Component",
		Properties: map[string]string{"name": "auth-service", "version": "2.1.0"},
		Embedding:  &emb,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	data, err := json.Marshal(ej)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var got EntityJSON
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if got.ID != id {
		t.Fatalf("expected ID %v, got %v", id, got.ID)
	}
	if got.Type != "Component" {
		t.Fatalf("expected Type 'Component', got %q", got.Type)
	}
	if got.Properties["name"] != "auth-service" {
		t.Fatalf("expected name=auth-service, got %q", got.Properties["name"])
	}
	if got.Properties["version"] != "2.1.0" {
		t.Fatalf("expected version=2.1.0, got %q", got.Properties["version"])
	}
	if !got.CreatedAt.Equal(now) {
		t.Fatalf("expected CreatedAt %v, got %v", now, got.CreatedAt)
	}
	if !got.UpdatedAt.Equal(now) {
		t.Fatalf("expected UpdatedAt %v, got %v", now, got.UpdatedAt)
	}
	if got.Embedding == nil {
		t.Fatal("expected non-nil Embedding")
	}
	if len(*got.Embedding) != 3 {
		t.Fatalf("expected 3 embedding values, got %d", len(*got.Embedding))
	}
	if (*got.Embedding)[0] != 0.12 || (*got.Embedding)[1] != -0.34 || (*got.Embedding)[2] != 0.56 {
		t.Fatalf("unexpected embedding values: %v", *got.Embedding)
	}
}

func TestEntityJSONNullProperties(t *testing.T) {
	id := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	ej := EntityJSON{
		ID:   id,
		Type: "Component",
	}

	data, err := json.Marshal(ej)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal to map failed: %v", err)
	}

	if _, ok := raw["properties"]; ok {
		t.Fatal("expected 'properties' to be omitted when nil")
	}
	if _, ok := raw["embedding"]; ok {
		t.Fatal("expected 'embedding' to be omitted when nil")
	}
}

func TestEntityJSONNullEmbedding(t *testing.T) {
	id := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	ej := EntityJSON{
		ID:         id,
		Type:       "Component",
		Properties: map[string]string{"name": "test"},
		Embedding:  nil,
	}

	data, err := json.Marshal(ej)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal to map failed: %v", err)
	}

	if _, ok := raw["embedding"]; ok {
		t.Fatal("expected 'embedding' to be omitted when nil")
	}
}

func TestEntityJSONEmptyEmbedding(t *testing.T) {
	id := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	emb := []float32{}
	ej := EntityJSON{
		ID:         id,
		Type:       "Component",
		Properties: map[string]string{"name": "test"},
		Embedding:  &emb,
	}

	data, err := json.Marshal(ej)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal to map failed: %v", err)
	}

	if _, ok := raw["embedding"]; !ok {
		t.Fatal("expected 'embedding' to be present when non-nil empty slice")
	}
}

func TestEdgeJSONRoundTrip(t *testing.T) {
	id := uuid.MustParse("660e8400-e29b-41d4-a716-446655440000")
	fromID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	toID := uuid.MustParse("770e8400-e29b-41d4-a716-446655440000")
	now := time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC)

	ej := EdgeJSON{
		ID:           id,
		Type:         "DEPENDS_ON",
		FromEntityID: fromID,
		ToEntityID:   toID,
		Properties:   map[string]string{"weight": "high"},
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	data, err := json.Marshal(ej)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var got EdgeJSON
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if got.ID != id {
		t.Fatalf("expected ID %v, got %v", id, got.ID)
	}
	if got.Type != "DEPENDS_ON" {
		t.Fatalf("expected Type 'DEPENDS_ON', got %q", got.Type)
	}
	if got.FromEntityID != fromID {
		t.Fatalf("expected FromEntityID %v, got %v", fromID, got.FromEntityID)
	}
	if got.ToEntityID != toID {
		t.Fatalf("expected ToEntityID %v, got %v", toID, got.ToEntityID)
	}
	if got.Properties["weight"] != "high" {
		t.Fatalf("expected weight=high, got %q", got.Properties["weight"])
	}
	if !got.CreatedAt.Equal(now) {
		t.Fatalf("expected CreatedAt %v, got %v", now, got.CreatedAt)
	}
	if !got.UpdatedAt.Equal(now) {
		t.Fatalf("expected UpdatedAt %v, got %v", now, got.UpdatedAt)
	}
}

func TestEdgeJSONNullProperties(t *testing.T) {
	id := uuid.MustParse("660e8400-e29b-41d4-a716-446655440000")
	fromID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	toID := uuid.MustParse("770e8400-e29b-41d4-a716-446655440000")

	ej := EdgeJSON{
		ID:           id,
		Type:         "DEPENDS_ON",
		FromEntityID: fromID,
		ToEntityID:   toID,
	}

	data, err := json.Marshal(ej)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal to map failed: %v", err)
	}

	if _, ok := raw["properties"]; ok {
		t.Fatal("expected 'properties' to be omitted when nil")
	}
}

func TestUnmarshalMissingFields(t *testing.T) {
	// Missing optional fields should unmarshal to zero values without error.
	data := []byte(`{"id":"550e8400-e29b-41d4-a716-446655440000","type":"Component"}`)
	var ej EntityJSON
	if err := json.Unmarshal(data, &ej); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if ej.Properties != nil {
		t.Fatal("expected nil Properties for missing field")
	}
	if ej.Embedding != nil {
		t.Fatal("expected nil Embedding for missing field")
	}
	if !ej.CreatedAt.IsZero() {
		t.Fatal("expected zero CreatedAt for missing field")
	}
	if !ej.UpdatedAt.IsZero() {
		t.Fatal("expected zero UpdatedAt for missing field")
	}
}
