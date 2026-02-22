package flow

import "testing"

func TestParseStampCapability_Valid(t *testing.T) {
	tests := []struct {
		input string
		kind  string
		stamp string
	}{
		{"STAMP:artefact/haiku/review", "haiku", "review"},
		{"STAMP:artefact/doc/security-review", "doc", "security-review"},
		{"STAMP:artefact/petition-draft/linter", "petition-draft", "linter"},
	}
	for _, tt := range tests {
		sc, ok := ParseStampCapability(tt.input)
		if !ok {
			t.Errorf("ParseStampCapability(%q) returned ok=false", tt.input)
			continue
		}
		if sc.GovernedArtefact != tt.kind {
			t.Errorf("ParseStampCapability(%q) kind=%q, want %q", tt.input, sc.GovernedArtefact, tt.kind)
		}
		if sc.StampName != tt.stamp {
			t.Errorf("ParseStampCapability(%q) stamp=%q, want %q", tt.input, sc.StampName, tt.stamp)
		}
	}
}

func TestParseStampCapability_Invalid(t *testing.T) {
	tests := []string{
		"READ:flow",
		"WRITE:feedback/new",
		"STAMP:artefact/",
		"STAMP:artefact/haiku/",
		"STAMP:artefact//review",
		"STAMP:artefact",
		"",
		"STAMP:",
		"READ:artefact",
	}
	for _, input := range tests {
		if _, ok := ParseStampCapability(input); ok {
			t.Errorf("ParseStampCapability(%q) returned ok=true, want false", input)
		}
	}
}

func TestParseStampCapabilities_MixedList(t *testing.T) {
	caps := []string{
		"READ:flow",
		"STAMP:artefact/haiku/review",
		"WRITE:feedback/new",
		"STAMP:artefact/doc/linter",
		"READ:artefact",
	}

	stamps := ParseStampCapabilities(caps)
	if len(stamps) != 2 {
		t.Fatalf("expected 2 stamp capabilities, got %d", len(stamps))
	}
	if stamps[0].GovernedArtefact != "haiku" || stamps[0].StampName != "review" {
		t.Errorf("stamps[0] = %+v, want haiku/review", stamps[0])
	}
	if stamps[1].GovernedArtefact != "doc" || stamps[1].StampName != "linter" {
		t.Errorf("stamps[1] = %+v, want doc/linter", stamps[1])
	}
}

func TestParseStampCapabilities_Empty(t *testing.T) {
	stamps := ParseStampCapabilities(nil)
	if stamps != nil {
		t.Fatalf("expected nil, got %v", stamps)
	}

	stamps = ParseStampCapabilities([]string{})
	if stamps != nil {
		t.Fatalf("expected nil for empty list, got %v", stamps)
	}
}

func TestParseStampCapabilities_NoMatches(t *testing.T) {
	caps := []string{"READ:flow", "WRITE:feedback/new", "READ:artefact"}
	stamps := ParseStampCapabilities(caps)
	if stamps != nil {
		t.Fatalf("expected nil when no stamps, got %v", stamps)
	}
}
