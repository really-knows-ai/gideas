package flow

import (
	"math"
	"strings"
	"testing"
)

// The SPEC error table ("Embedding contains NaN or infinity") applies the
// NaN/infinity check to CreateEntity and UpdateEntity. These tests pin the
// SDK-side rejection boundary on the Transaction layer, which calls the same
// validateEmbedding guard (transaction.go) as the write paths.
func TestTxEmbeddingNaNInfinityRejection(t *testing.T) {
	bad := []struct {
		name string
		emb  []float32
	}{
		{"nan", []float32{float32(math.NaN())}},
		{"positive-infinity", []float32{float32(math.Inf(1))}},
		{"negative-infinity", []float32{float32(math.Inf(-1))}},
	}
	methods := []struct {
		name string
		fn   func(tx *Transaction, emb []float32) error
	}{
		{"CreateEntity", func(tx *Transaction, emb []float32) error {
			_, err := tx.CreateEntity(componentType, nil, nil, emb)
			return err
		}},
		{"UpdateEntity", func(tx *Transaction, emb []float32) error {
			_, err := tx.UpdateEntity(testUUIDEntity, nil, emb)
			return err
		}},
	}
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			for _, tc := range bad {
				t.Run(tc.name, func(t *testing.T) {
					tx := newMockTx(&mockCartographerClient{})
					if err := m.fn(tx, tc.emb); err == nil {
						t.Errorf("expected error for NaN/infinity embedding on %s", m.name)
					}
				})
			}
		})
	}
}

// TestTransaction_RejectsNonCanonicalUUID pins the SDK's client-side
// canonical RFC4122 §3 UUID v4 validation on the write path (SPEC:162;
// error-table row "Invalid entity or edge ID format"): non-canonical
// spellings that still parse as UUIDs — uppercase hex, 32-char no-hyphen,
// braced {...}, urn:uuid: — plus outright non-UUIDs are rejected before they
// reach the wire, mirroring the validateEmbedding client-side guard. Without
// this guard the Cartographer would persist each spelling verbatim as a
// distinct <id>.json file, creating two entities for one UUID and bypassing
// the CreateEntity ALREADY_EXISTS check. The mock's RPC fields are left nil,
// so a rejection that slips through would panic here rather than pass.
func TestTransaction_RejectsNonCanonicalUUID(t *testing.T) {
	bad := []struct {
		name string
		id   string
	}{
		{"uppercase-hex", "550E8400-E29B-41D4-A716-446655440000"},
		{"no-hyphen", "550e8400e29b41d4a716446655440000"},
		{"braced", "{550e8400-e29b-41d4-a716-446655440000}"},
		{"urn-prefixed", "urn:uuid:550e8400-e29b-41d4-a716-446655440000"},
		{"not-a-uuid", "entity-1"},
	}
	methods := []struct {
		name string
		fn   func(tx *Transaction, id string) error
	}{
		{"CreateEntity", func(tx *Transaction, id string) error {
			_, err := tx.CreateEntity(componentType, &id, nil, nil)
			return err
		}},
		{"UpdateEntity", func(tx *Transaction, id string) error {
			_, err := tx.UpdateEntity(id, nil, nil)
			return err
		}},
		{"DeleteEntity", func(tx *Transaction, id string) error {
			_, err := tx.DeleteEntity(id)
			return err
		}},
		{"CreateEdge-from", func(tx *Transaction, id string) error {
			_, err := tx.CreateEdge("DEPENDS_ON", id, testUUIDTo, nil)
			return err
		}},
		{"CreateEdge-to", func(tx *Transaction, id string) error {
			_, err := tx.CreateEdge("DEPENDS_ON", testUUIDFrom, id, nil)
			return err
		}},
		{"DeleteEdge", func(tx *Transaction, id string) error {
			_, err := tx.DeleteEdge(id)
			return err
		}},
	}
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			for _, tc := range bad {
				t.Run(tc.name, func(t *testing.T) {
					tx := newMockTx(&mockCartographerClient{})
					err := m.fn(tx, tc.id)
					if err == nil {
						t.Fatalf("expected client-side rejection of %q", tc.id)
					}
					if !strings.Contains(err.Error(), "canonical") {
						t.Errorf("expected canonical-form rejection error, got %v", err)
					}
				})
			}
		})
	}
}
