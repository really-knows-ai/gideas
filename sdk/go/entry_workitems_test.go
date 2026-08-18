package flow

import (
	"fmt"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Tests — EntryClient.CreateWorkitem
// ---------------------------------------------------------------------------

func TestEntryClient_CreateWorkitem_Success(t *testing.T) {
	spy := &entrySpyOperator{returnID: "wi-new-001"}
	ec := setupEntryTestEnv(t, spy, nil)

	md := map[string]string{"source": "friction-watcher", "law_id": testLaw42}
	id, err := ec.CreateWorkitem(md)
	if err != nil {
		t.Fatalf("CreateWorkitem() returned error: %v", err)
	}
	if id != "wi-new-001" {
		t.Fatalf("expected workitem_id=wi-new-001, got %q", id)
	}
	if spy.lastMetadata["source"] != "friction-watcher" {
		t.Fatalf("expected metadata source=friction-watcher, got %q", spy.lastMetadata["source"])
	}
	if spy.lastMetadata["law_id"] != testLaw42 {
		t.Fatalf("expected metadata law_id=%s, got %q", testLaw42, spy.lastMetadata["law_id"])
	}
}

func TestEntryClient_CreateWorkitem_NilMetadata(t *testing.T) {
	spy := &entrySpyOperator{returnID: "wi-nil-meta"}
	ec := setupEntryTestEnv(t, spy, nil)

	id, err := ec.CreateWorkitem(nil)
	if err != nil {
		t.Fatalf("CreateWorkitem(nil) returned error: %v", err)
	}
	if id != "wi-nil-meta" {
		t.Fatalf("expected workitem_id=wi-nil-meta, got %q", id)
	}
	if len(spy.lastMetadata) != 0 {
		t.Fatalf("expected empty metadata, got %v", spy.lastMetadata)
	}
}

func TestEntryClient_CreateWorkitem_Error(t *testing.T) {
	spy := &entrySpyOperator{returnErr: fmt.Errorf("permission denied")}
	ec := setupEntryTestEnv(t, spy, nil)

	_, err := ec.CreateWorkitem(nil)
	if err == nil {
		t.Fatal("expected error from CreateWorkitem, got nil")
	}
}

func TestEntryClient_CreateWorkitem_NoConnection(t *testing.T) {
	// EntryClient with no sidecar connection.
	ec := &EntryClient{}
	_, err := ec.CreateWorkitem(nil)
	if err == nil {
		t.Fatal("expected error when no sidecar connection, got nil")
	}
}

// ---------------------------------------------------------------------------
// Tests — EntryClient.ResumeWorkitem
// ---------------------------------------------------------------------------

func TestEntryClient_ResumeWorkitem_Success(t *testing.T) {
	opSpy := &entrySpyOperator{returnID: "unused"}
	ec := setupEntryTestEnv(t, opSpy, nil)

	err := ec.ResumeWorkitem("wi-held-001")
	if err != nil {
		t.Fatalf("ResumeWorkitem() returned error: %v", err)
	}
	if len(opSpy.resumeWorkitems) != 1 || opSpy.resumeWorkitems[0] != "wi-held-001" {
		t.Fatalf("expected resumed workitem_id=wi-held-001, got %v", opSpy.resumeWorkitems)
	}
}

func TestEntryClient_ResumeWorkitem_Error(t *testing.T) {
	opSpy := &entrySpyOperator{returnID: "unused", resumeErr: fmt.Errorf("workitem not found")}
	ec := setupEntryTestEnv(t, opSpy, nil)

	err := ec.ResumeWorkitem("wi-missing")
	if err == nil {
		t.Fatal("expected error from ResumeWorkitem, got nil")
	}
}

func TestEntryClient_ResumeWorkitem_NoConnection(t *testing.T) {
	ec := &EntryClient{}
	err := ec.ResumeWorkitem("wi-1")
	if err == nil {
		t.Fatal("expected error when no sidecar connection, got nil")
	}
}

// ---------------------------------------------------------------------------
// Tests — EntryClient.ListSuspendedWorkitems
// ---------------------------------------------------------------------------

func TestEntryClient_ListSuspendedWorkitems_Success(t *testing.T) {
	opSpy := &entrySpyOperator{
		returnID: "unused",
		listSuspendedResp: []*flowv1.SuspendedWorkitemInfo{
			{WorkitemId: "wi-held-1", ResumeCondition: `dispute_retired("pet-42")`},
			{WorkitemId: "wi-held-2", ResumeCondition: `dispute_retired("pet-42")`},
		},
	}
	ec := setupEntryTestEnv(t, opSpy, nil)

	ids, err := ec.ListSuspendedWorkitems("pet-42")
	if err != nil {
		t.Fatalf("ListSuspendedWorkitems() returned error: %v", err)
	}
	if len(ids) != 2 || ids[0] != "wi-held-1" || ids[1] != "wi-held-2" {
		t.Fatalf("expected [wi-held-1, wi-held-2], got %v", ids)
	}
	if opSpy.lastCondFilter != "pet-42" {
		t.Fatalf("expected condition_contains=pet-42, got %q", opSpy.lastCondFilter)
	}
}

func TestEntryClient_ListSuspendedWorkitems_Error(t *testing.T) {
	opSpy := &entrySpyOperator{
		returnID:         "unused",
		listSuspendedErr: fmt.Errorf("operator unavailable"),
	}
	ec := setupEntryTestEnv(t, opSpy, nil)

	_, err := ec.ListSuspendedWorkitems("pet-42")
	if err == nil {
		t.Fatal("expected error from ListSuspendedWorkitems, got nil")
	}
}

func TestEntryClient_ListSuspendedWorkitems_NoConnection(t *testing.T) {
	ec := &EntryClient{}
	_, err := ec.ListSuspendedWorkitems("pet-1")
	if err == nil {
		t.Fatal("expected error when no sidecar connection, got nil")
	}
}
