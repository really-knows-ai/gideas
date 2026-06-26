package v1

import (
	"encoding/json"
	"testing"
)

func TestLawGroupSpecJSONShape(t *testing.T) {
	t.Parallel()

	spec := LawGroupSpec{
		Mode:   "law-by-law",
		Passes: 3,
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal LawGroupSpec: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal LawGroupSpec: %v", err)
	}

	if mode, ok := decoded["mode"].(string); !ok || mode != "law-by-law" {
		t.Fatalf("expected mode 'law-by-law', got %#v", decoded["mode"])
	}

	if passes, ok := decoded["passes"].(float64); !ok || passes != 3 {
		t.Fatalf("expected passes 3, got %#v", decoded["passes"])
	}
}

func TestLawGroupDefaultMode(t *testing.T) {
	t.Parallel()

	// Verify the zero-value LawGroupSpec has empty mode and zero passes.
	var spec LawGroupSpec
	if spec.Mode != "" {
		t.Fatalf("expected empty mode for zero-value, got %q", spec.Mode)
	}
	if spec.Passes != 0 {
		t.Fatalf("expected 0 passes for zero-value, got %d", spec.Passes)
	}
}
