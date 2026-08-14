package flow

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestParseStampCapability_Valid(t *testing.T) {
	tests := []struct {
		input string
		kind  string
		stamp string
	}{
		{"STAMP:artefact/haiku/review", "haiku", "review"},
		{"STAMP:artefact/doc/security-review", "doc", "security-review"},
		{"STAMP:artefact/petition-draft/linter", "petition-draft", stampLinter},
		{"ATTEST:artefact/haiku/review", "haiku", "review"},
		{"ATTEST:artefact/doc/security-review", "doc", "security-review"},
		{"ATTEST:artefact/petition-draft/linter", "petition-draft", stampLinter},
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
		"ATTEST:artefact/",
		"ATTEST:artefact/haiku/",
		"ATTEST:artefact//review",
		"ATTEST:artefact",
		"ATTEST:",
		"ATTEST:artefact/*/",
	}
	for _, input := range tests {
		if _, ok := ParseStampCapability(input); ok {
			t.Errorf("ParseStampCapability(%q) returned ok=true, want false", input)
		}
	}
}

func TestParseStampCapabilities_MixedList(t *testing.T) {
	const kindHaiku = "haiku"
	const kindDoc = "doc"
	const stampReview = "review"

	caps := []string{
		"READ:flow",
		"STAMP:artefact/haiku/review",
		"WRITE:feedback/new",
		"STAMP:artefact/doc/linter",
		"ATTEST:artefact/haiku/review",
		"READ:artefact",
	}

	stamps := ParseStampCapabilities(caps)
	if len(stamps) != 3 {
		t.Fatalf("expected 3 stamp capabilities, got %d", len(stamps))
	}
	if stamps[0].GovernedArtefact != kindHaiku || stamps[0].StampName != stampReview {
		t.Errorf("stamps[0] = %+v, want haiku/review", stamps[0])
	}
	if stamps[1].GovernedArtefact != kindDoc || stamps[1].StampName != stampLinter {
		t.Errorf("stamps[1] = %+v, want doc/linter", stamps[1])
	}
	if stamps[2].GovernedArtefact != kindHaiku || stamps[2].StampName != stampReview {
		t.Errorf("stamps[2] = %+v, want haiku/review", stamps[2])
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
		{"wildcard kind match", "STAMP:artefact/*/review", "STAMP:artefact/haiku/review", true},
		{"wildcard artefact kind other", "STAMP:artefact/*/review", "STAMP:artefact/code/review", true},

		// review prefix match in stamp-name position.
		{"prefix wildcard stamp", "STAMP:artefact/haiku/review", "STAMP:artefact/haiku/review", true},
		{"prefix wildcard stamp no match", "STAMP:artefact/haiku/review", "STAMP:artefact/haiku/approval", false},

		// * does NOT match across /.
		{"wildcard no cross slash", "STAMP:artefact/*/review", "STAMP:artefact/haiku/extra/review", false},
		{"wildcard no cross slash 2", "STAMP:artefact/*/review", "STAMP:artefact/code/nested/review", false},

		// Multiple wildcards in different segments (covered by detailed Phase 08 tests below).

		// Edge cases.
		{"empty pattern empty required", "", "", true},
		{"empty pattern non-empty", "", "STAMP:artefact/haiku/review", false},
		// (bare * tests covered by Phase 08 cases below)

		// Phase 08 wildcard edge cases.
		{"*/review mtch haiku", "STAMP:artefact/*/review", "STAMP:artefact/haiku/review", true},
		{"*/review mtch code", "STAMP:artefact/*/review", "STAMP:artefact/code/review", true},
		{"*/review empty sfx", "STAMP:artefact/*/review", "STAMP:artefact/haiku/review", true},
		{"*/review no appr.", "STAMP:artefact/*/review", "STAMP:artefact/haiku/approval", false},
		{"*/review cross /", "STAMP:artefact/*/review", "STAMP:artefact/haiku/extra/review", false},
		{"exact review match", "STAMP:artefact/haiku/review", "STAMP:artefact/haiku/review", true},
		{"exact review no match approval", "STAMP:artefact/haiku/review", "STAMP:artefact/haiku/approval", false},

		// Bare star in different positions.
		{"top level star only", "*", "*", true},
		{"top level star no cross", "*", "a/b", false},

		// Non-STAMP capabilities.
		{"read flow exact", "READ:flow", "READ:flow", true},
		{"read flow mismatch", "READ:flow", "WRITE:flow", false},
		{"write artefact wildcard", "WRITE:artefact/*", "WRITE:artefact/haiku", true},
		{"write artefact wildcard no cross", "WRITE:artefact/*", "WRITE:artefact/haiku/extra", false},

		// ATTEST: prefix matching.
		{"ATTEST exact match", "ATTEST:artefact/haiku/review", "ATTEST:artefact/haiku/review", true},
		{"ATTEST wildcard kind", "ATTEST:artefact/*/review", "ATTEST:artefact/haiku/review", true},
		{"ATTEST prefix wildcard", "ATTEST:artefact/haiku/review", "ATTEST:artefact/haiku/review", true},
		{"ATTEST mismatch verb", "ATTEST:artefact/haiku/review", "STAMP:artefact/haiku/review", false},
		{"STAMP exact match (unchanged)", "STAMP:artefact/haiku/review", "STAMP:artefact/haiku/review", true},
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

func TestCheckCapability(t *testing.T) {
	// No metadata — system call passes.
	if err := CheckCapability(context.Background(), "READ:flow"); err != nil {
		t.Fatalf("expected pass with no metadata, got %v", err)
	}

	// No x-flow-node-id — system call passes even with capabilities absent.
	md := metadata.Pairs("x-flow-capabilities", "WRITE:artefact")
	if err := CheckCapability(metadata.NewIncomingContext(context.Background(), md), "READ:flow"); err != nil {
		t.Fatalf("expected pass without node identity, got %v", err)
	}

	// Node-originated with the required capability — passes.
	md = metadata.Pairs(
		MetadataKeyNodeID, "node-1",
		MetadataKeyCapabilities, "WRITE:artefact, READ:flow",
	)
	if err := CheckCapability(metadata.NewIncomingContext(context.Background(), md), "READ:flow"); err != nil {
		t.Fatalf("expected pass with matching capability, got %v", err)
	}

	// Node-originated with whitespace-padded and empty entries — the shared
	// capability-string normalization (pkg/metadata.NormalizeCapabilities)
	// trims each entry and drops empty entries before matching.
	md = metadata.Pairs(
		MetadataKeyNodeID, "node-1",
		MetadataKeyCapabilities, " WRITE:artefact , READ:flow ,, ",
	)
	if err := CheckCapability(metadata.NewIncomingContext(context.Background(), md), "READ:flow"); err != nil {
		t.Fatalf("expected pass with whitespace-padded capability, got %v", err)
	}

	// Node-originated without the required capability — denied.
	md = metadata.Pairs(
		MetadataKeyNodeID, "node-1",
		MetadataKeyCapabilities, "WRITE:artefact",
	)
	err := CheckCapability(metadata.NewIncomingContext(context.Background(), md), "READ:flow")
	if err == nil {
		t.Fatal("expected denial for missing capability")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}
