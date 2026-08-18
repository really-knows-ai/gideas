package flow

import (
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Laws — GetLawGroups
// ---------------------------------------------------------------------------

func TestWorkitem_GetLawGroups(t *testing.T) {
	const wantID = "wi-getlawgroups-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	// The spy QueryLaws returns [{Id: "law-1"}] (no group field, so "default").
	// The spy ListLawGroups returns [group-a, group-b].
	groups, err := wi.GetLawGroups(testTextMarkdown)
	_ = env
	if err != nil {
		t.Fatalf("GetLawGroups() returned error: %v", err)
	}

	// Laws have no group, so they fall under "default".
	// "default" is not in ListLawGroups, so built-in defaults are used.
	if len(groups) != 1 {
		t.Fatalf("expected 1 law group, got %d", len(groups))
	}
	if groups[0].Name() != DefaultGroup {
		t.Fatalf("group[0].Name() = %q, want %q", groups[0].Name(), DefaultGroup)
	}
	if groups[0].Mode() != GroupModeBundle {
		t.Fatalf("group[0].Mode() = %q, want %q", groups[0].Mode(), GroupModeBundle)
	}
}

func TestWorkitem_GetLawGroups_EmptyRepType(t *testing.T) {
	const wantID = "wi-getlawgroups-empty-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	// Empty repType queries all laws (no filter).
	groups, err := wi.GetLawGroups("")
	_ = env // used below
	if err != nil {
		t.Fatalf("GetLawGroups(\"\") returned error: %v", err)
	}

	// Same result as with "text/markdown": one "default" group.
	if len(groups) != 1 {
		t.Fatalf("expected 1 law group, got %d", len(groups))
	}
	if groups[0].Name() != DefaultGroup {
		t.Fatalf("group[0].Name() = %q, want %q", groups[0].Name(), DefaultGroup)
	}

	// Verify QueryLaws was called.
	if env.spy.lastQueryLawsReq == nil {
		t.Fatal("QueryLaws was not called")
	}
	// Filter should be nil for empty repType.
	if env.spy.lastQueryLawsReq.GetFilter() != nil {
		t.Fatal("expected nil filter for empty repType")
	}
}

// ---------------------------------------------------------------------------
// Laws — VerifyLawAttestations
// ---------------------------------------------------------------------------

func TestWorkitem_VerifyLawAttestations(t *testing.T) {
	const wantID = "wi-verifyattest-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	// spy QueryLaws returns [{Id: "law-1"}] with text/markdown rep, no group.
	// spy ListLawGroups returns [group-a (bundle), group-b (law-by-law)].
	// "default" group (from law with no group) is absent → built-in defaults → bundle.
	// Bundle mode → lawgrp-default replaces per-law stamps.
	missing, err := wi.VerifyLawAttestations(testPetitionID)
	if err != nil {
		t.Fatalf("VerifyLawAttestations() returned error: %v", err)
	}
	if len(missing) != 1 || missing[0] != "lawgrp-default" {
		t.Fatalf("expected [lawgrp-default], got %v", missing)
	}

	// Verify QueryLaws was called with the correct governed artefact.
	if env.spy.lastQueryLawsReq == nil {
		t.Fatal("QueryLaws was not called")
	}
	f := env.spy.lastQueryLawsReq.GetFilter()
	if f == nil {
		t.Fatal("expected non-nil filter")
	}
	if f.GetGovernedArtefact() != testPetitionID {
		t.Fatalf("governed_artefact = %q, want %q", f.GetGovernedArtefact(), "petition")
	}

	// Verify ListLawGroups was called for group mode resolution.
	if env.spy.lastListLawGroupsReq == nil {
		t.Fatal("ListLawGroups was not called")
	}
}

func TestWorkitem_VerifyLawAttestations_Groups(t *testing.T) {
	const wantID = "wi-verifyattest-groups-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	// Configure spy to return laws in both group-a (bundle) and group-b (law-by-law).
	env.spy.queryLawsResp = &flowv1.QueryLawsResponse{
		Laws: []*flowv1.Law{
			{Id: "law-bundle-1", Group: "group-a", Representations: []*flowv1.Representation{{Type: "text/markdown"}}},
			{Id: "law-lbl-1", Group: "group-b", Representations: []*flowv1.Representation{{Type: "text/plain"}}},
			{Id: "law-lbl-2", Group: "group-b", Representations: []*flowv1.Representation{{Type: "text/markdown"}}},
		},
	}

	// group-a bundle → lawgrp-group-a (no per-law stamps)
	// group-b law-by-law → law-law-lbl-1-text-plain, law-law-lbl-2-text-markdown + lawgrp-group-b
	missing, err := wi.VerifyLawAttestations(testPetitionID)
	if err != nil {
		t.Fatalf("VerifyLawAttestations() returned error: %v", err)
	}

	expected := []string{
		"lawgrp-group-a",
		"law-law-lbl-1-text-plain",
		"law-law-lbl-2-text-markdown",
		"lawgrp-group-b",
	}
	if len(missing) != len(expected) {
		t.Fatalf("expected %d missing stamps, got %d: %v", len(expected), len(missing), missing)
	}
	got := make(map[string]bool, len(missing))
	for _, s := range missing {
		got[s] = true
	}
	for _, s := range expected {
		if !got[s] {
			t.Errorf("missing stamp %q not found in output", s)
		}
	}
}

// ---------------------------------------------------------------------------
// Laws — Cite
// ---------------------------------------------------------------------------

func TestWorkitem_Cite(t *testing.T) {
	const wantID = "wi-cite-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	err := wi.Cite(testLaw1, testLaw2)
	if err != nil {
		t.Fatalf("Cite() returned error: %v", err)
	}

	req := env.spy.lastCiteReq
	if req == nil {
		t.Fatal("Cite was not called on the server")
	}
	if len(req.GetLawIds()) != 2 {
		t.Fatalf("expected 2 law IDs, got %d", len(req.GetLawIds()))
	}
	if req.GetLawIds()[0] != testLaw1 || req.GetLawIds()[1] != testLaw2 {
		t.Fatalf("law_ids = %v, want [law-1 law-2]", req.GetLawIds())
	}
}
