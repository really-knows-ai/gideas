package flow

import (
	"fmt"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Tests — EntryClient.QueryLaws
// ---------------------------------------------------------------------------

func TestEntryClient_QueryLaws_Success(t *testing.T) {
	laws := []*flowv1.Law{
		{Id: "law-1", Goal: "test goal 1", Tier: flowv1.LawTier_LAW_TIER_FINDING},
		{Id: "law-2", Goal: "test goal 2", Tier: flowv1.LawTier_LAW_TIER_RULING},
	}
	libSpy := &entrySpyLibrarian{returnLaws: laws}
	opSpy := &entrySpyOperator{returnID: "unused"}
	ec := setupEntryTestEnv(t, opSpy, nil, libSpy)

	got, err := ec.QueryLaws("", "")
	if err != nil {
		t.Fatalf("QueryLaws() returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 laws, got %d", len(got))
	}
	if got[0].GetId() != "law-1" {
		t.Errorf("expected first law id=law-1, got %q", got[0].GetId())
	}
	if got[1].GetId() != "law-2" {
		t.Errorf("expected second law id=law-2, got %q", got[1].GetId())
	}
	// No filter should have been sent.
	if libSpy.lastFilter != nil {
		t.Errorf("expected nil filter for empty args, got %+v", libSpy.lastFilter)
	}
}

func TestEntryClient_QueryLaws_WithFilter(t *testing.T) {
	libSpy := &entrySpyLibrarian{returnLaws: []*flowv1.Law{
		{Id: "law-3", Goal: "filtered goal"},
	}}
	opSpy := &entrySpyOperator{returnID: "unused"}
	ec := setupEntryTestEnv(t, opSpy, nil, libSpy)

	got, err := ec.QueryLaws("haiku", "smt")
	if err != nil {
		t.Fatalf("QueryLaws() returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 law, got %d", len(got))
	}
	if libSpy.lastFilter == nil {
		t.Fatal("expected non-nil filter")
	}
	if libSpy.lastFilter.GetGovernedArtefact() != "haiku" {
		t.Errorf("expected governed_artefact=haiku, got %q", libSpy.lastFilter.GetGovernedArtefact())
	}
	if libSpy.lastFilter.GetRepresentationType() != "smt" {
		t.Errorf("expected representation_type=smt, got %q", libSpy.lastFilter.GetRepresentationType())
	}
}

func TestEntryClient_QueryLaws_Error(t *testing.T) {
	libSpy := &entrySpyLibrarian{returnErr: fmt.Errorf("librarian unavailable")}
	opSpy := &entrySpyOperator{returnID: "unused"}
	ec := setupEntryTestEnv(t, opSpy, nil, libSpy)

	_, err := ec.QueryLaws("", "")
	if err == nil {
		t.Fatal("expected error from QueryLaws, got nil")
	}
}

func TestEntryClient_QueryLaws_NoConnection(t *testing.T) {
	ec := &EntryClient{}
	_, err := ec.QueryLaws("", "")
	if err == nil {
		t.Fatal("expected error when no sidecar connection, got nil")
	}
}

// ---------------------------------------------------------------------------
// Tests — EntryClient.RetireDisputeRecord
// ---------------------------------------------------------------------------

func TestEntryClient_RetireDisputeRecord_Success(t *testing.T) {
	libSpy := &entrySpyLibrarian{}
	opSpy := &entrySpyOperator{returnID: "unused"}
	ec := setupEntryTestEnv(t, opSpy, nil, libSpy)

	err := ec.RetireDisputeRecord("pet-42")
	if err != nil {
		t.Fatalf("RetireDisputeRecord() returned error: %v", err)
	}
	if len(libSpy.retiredPetitionIDs) != 1 || libSpy.retiredPetitionIDs[0] != "pet-42" {
		t.Fatalf("expected retired petition_id=pet-42, got %v", libSpy.retiredPetitionIDs)
	}
}

func TestEntryClient_RetireDisputeRecord_Error(t *testing.T) {
	libSpy := &entrySpyLibrarian{retireErr: fmt.Errorf("not found")}
	opSpy := &entrySpyOperator{returnID: "unused"}
	ec := setupEntryTestEnv(t, opSpy, nil, libSpy)

	err := ec.RetireDisputeRecord("pet-99")
	if err == nil {
		t.Fatal("expected error from RetireDisputeRecord, got nil")
	}
}

func TestEntryClient_RetireDisputeRecord_NoConnection(t *testing.T) {
	ec := &EntryClient{}
	err := ec.RetireDisputeRecord("pet-1")
	if err == nil {
		t.Fatal("expected error when no sidecar connection, got nil")
	}
}
