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

func TestMatchCapability(t *testing.T) {
	tests := []struct {
		name     string
		pattern  string
		required string
		want     bool
	}{
		// Exact match (no wildcard).
		{"exact match", "STAMP:artefact/haiku/review", "STAMP:artefact/haiku/review", true},
		{"exact mismatch", "STAMP:artefact/haiku/review", "STAMP:artefact/haiku/approval", false},

		// Single * in artefact-kind position.
		{"wildcard kind match", "STAMP:artefact/*/appraise-security", "STAMP:artefact/haiku/appraise-security", true},
		{"wildcard artefact kind other", "STAMP:artefact/*/appraise-security", "STAMP:artefact/code/appraise-security", true},

		// appraise-* prefix match in stamp-name position.
		{"prefix wildcard stamp", "STAMP:artefact/haiku/appraise-*", "STAMP:artefact/haiku/appraise-security", true},
		{"prefix wildcard long", "STAMP:artefact/haiku/appraise-*", "STAMP:artefact/haiku/appraise-security-L001", true},
		{"prefix wildcard stamp no match", "STAMP:artefact/haiku/appraise-*", "STAMP:artefact/haiku/approval", false},

		// * does NOT match across /.
		{"wildcard no cross slash", "STAMP:artefact/*/appraise-*", "STAMP:artefact/haiku/extra/appraise-security", false},
		{"wildcard no cross slash 2", "STAMP:artefact/*/appraise-*", "STAMP:artefact/code/nested/appraise-review", false},

		// Multiple wildcards in different segments.
		{"two wildcards match", "STAMP:artefact/*/appraise-*", "STAMP:artefact/haiku/appraise-security", true},
		{"two wildcards other", "STAMP:artefact/*/appraise-*", "STAMP:artefact/doc/appraise-linter", true},
		{"two wildcards no match", "STAMP:artefact/*/appraise-*", "STAMP:artefact/doc/review", false},

		// Edge cases.
		{"empty pattern empty required", "", "", true},
		{"empty pattern non-empty", "", "STAMP:artefact/haiku/review", false},
		{"star only", "*", "anything/at/all", false}, // bare * does not match across / boundaries
		{"star only exact", "*", "*", true},

		// Non-STAMP capabilities.
		{"read flow exact", "READ:flow", "READ:flow", true},
		{"read flow mismatch", "READ:flow", "WRITE:flow", false},
		{"write artefact wildcard", "WRITE:artefact/*", "WRITE:artefact/haiku", true},
		{"write artefact wildcard no cross", "WRITE:artefact/*", "WRITE:artefact/haiku/extra", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchCapability(tt.pattern, tt.required)
			if got != tt.want {
				t.Errorf("MatchCapability(%q, %q) = %v, want %v", tt.pattern, tt.required, got, tt.want)
			}
		})
	}
}
