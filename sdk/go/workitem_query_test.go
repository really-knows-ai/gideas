package flow

import (
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Topology — GetTopology
// ---------------------------------------------------------------------------

func TestWorkitem_GetTopology(t *testing.T) {
	const wantID = "wi-gettopology-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	flow, err := wi.GetTopology()
	if err != nil {
		t.Fatalf("GetTopology() returned error: %v", err)
	}
	if flow == nil {
		t.Fatal("expected non-nil Flow")
	}

	// Verify metadata injection.
	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}

	// Verify the flow topology is surfaced.
	ec := flow.GetExitContract()
	if ec == nil {
		t.Fatal("expected a non-nil exit contract from GetTopology")
	}
}

// ---------------------------------------------------------------------------
// Friction — QueryFriction
// ---------------------------------------------------------------------------

func TestWorkitem_QueryFriction(t *testing.T) {
	const wantID = "wi-queryfriction-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	aggs, err := wi.QueryFriction(&flowv1.FrictionFilter{LawId: testLawFriction})
	if err != nil {
		t.Fatalf("QueryFriction() returned error: %v", err)
	}
	if len(aggs) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(aggs))
	}
	if aggs[0].GetLawId() != testLawFriction {
		t.Fatalf("law_id = %q, want %q", aggs[0].GetLawId(), "law-friction-001")
	}

	// Verify metadata injection.
	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

// ---------------------------------------------------------------------------
// Workitem fields — namespace mirrors session
// ---------------------------------------------------------------------------

func TestWorkitem_NamespaceMirrorsSession(t *testing.T) {
	const wantID = "wi-namespace-001"
	wi, _ := setupWorkitemTestEnv(t, wantID)

	if wi.namespace != wi.session.namespace {
		t.Fatalf("Workitem.namespace = %q, want session.namespace = %q", wi.namespace, wi.session.namespace)
	}
}

// ---------------------------------------------------------------------------
// Workitem — ID()
// ---------------------------------------------------------------------------

func TestWorkitem_ID(t *testing.T) {
	const wantID = "wi-id-001"
	wi, _ := setupWorkitemTestEnv(t, wantID)

	if wi.ID() != wantID {
		t.Fatalf("Workitem.ID() = %q, want %q", wi.ID(), wantID)
	}
}
