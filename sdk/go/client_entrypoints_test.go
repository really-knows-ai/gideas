package flow

import (
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Tests — New Entry Methods
// ---------------------------------------------------------------------------

func TestGetWorkitem_NoArgs(t *testing.T) {
	const wantID = "workitem-getwi-noargs-001"
	env := setupTestEnv(t, wantID)

	wi, err := env.client.GetWorkitem()
	if err != nil {
		t.Fatalf("GetWorkitem() returned error: %v", err)
	}
	if wi.ID() != wantID {
		t.Fatalf("expected Workitem.ID()=%q, got %q", wantID, wi.ID())
	}
}

func TestGetWorkitem_OneArg(t *testing.T) {
	const sessionID = "workitem-session-001"
	const otherID = "other-wid-001"
	env := setupTestEnv(t, sessionID)

	wi, err := env.client.GetWorkitem(otherID)
	if err != nil {
		t.Fatalf("GetWorkitem(%q) returned error: %v", otherID, err)
	}
	if wi.ID() != otherID {
		t.Fatalf("expected Workitem.ID()=%q, got %q", otherID, wi.ID())
	}
}

func TestGetWorkitem_MultiArgs(t *testing.T) {
	env := setupTestEnv(t, "workitem-multi-001")

	_, err := env.client.GetWorkitem("a", "b")
	if err == nil {
		t.Fatal("expected error for multi-arg GetWorkitem, got nil")
	}
}

func TestGetWorkitem_NoArgs_ReadsEnv(t *testing.T) {
	env := setupTestEnv(t, "workitem-env-001")

	t.Setenv("FLOW_WORKITEM_ID", "env-wid-001")
	// Re-create client session to pick up env var.
	sess := &session{
		workitemID:     "env-wid-001",
		conn:           env.client.session.conn,
		Sidecar:        env.client.session.Sidecar,
		Operator:       env.client.session.Operator,
		Archivist:      env.client.session.Archivist,
		Librarian:      env.client.session.Librarian,
		FrictionLedger: env.client.session.FrictionLedger,
	}
	env.client.session = sess

	wi, err := env.client.GetWorkitem()
	if err != nil {
		t.Fatalf("GetWorkitem() returned error: %v", err)
	}
	if wi.ID() != "env-wid-001" {
		t.Fatalf("expected Workitem.ID()=%q from env, got %q", "env-wid-001", wi.ID())
	}
}

func TestGetFlow(t *testing.T) {
	env := setupTestEnv(t, "workitem-getflow-001")

	f, err := env.client.GetFlow()
	if err != nil {
		t.Fatalf("GetFlow() returned error: %v", err)
	}
	if f == nil {
		t.Fatal("expected non-nil Flow")
	}

	ec := f.GetExitContract()
	if ec == nil {
		t.Fatal("expected non-nil exit contract")
	}
	stamps, ok := ec["doc"]
	if !ok {
		t.Fatal("missing doc in exit contract")
	}
	if len(stamps) != 2 || stamps[0] != stampLinter {
		t.Fatalf("unexpected doc stamps: %v", stamps)
	}
}

// TestGetGraph pins the SPEC R4 entry surface: Client.GetGraph returns a Graph
// bound to the client's session and carrying the SDK's bounded local
// ID-to-type cache (SPEC R3).
func TestGetGraph(t *testing.T) {
	env := setupTestEnv(t, "workitem-getgraph-001")

	g, err := env.client.GetGraph()
	if err != nil {
		t.Fatalf("GetGraph() returned error: %v", err)
	}
	if g == nil {
		t.Fatal("expected non-nil Graph")
	}
	if g.session != env.client.session {
		t.Error("expected Graph to share the client's session")
	}
	if g.idTypeMap == nil {
		t.Error("expected Graph to carry the SDK's ID-to-type cache")
	}
}

func TestGetGraph_UninitialisedClient(t *testing.T) {
	client := &Client{}
	if _, err := client.GetGraph(); err == nil {
		t.Fatal("expected error for an uninitialised client")
	}
}

func TestGetLaw_NewEntryMethod(t *testing.T) {
	env := setupTestEnv(t, "workitem-getlaw-entry-001")

	law, err := env.client.GetLaw("law-entry-001")
	if err != nil {
		t.Fatalf("GetLaw(%q) returned error: %v", "law-entry-001", err)
	}
	if law == nil {
		t.Fatal("expected non-nil Law")
	}
	if law.ID() != "law-entry-001" {
		t.Fatalf("expected law.ID()=law-entry-001, got %q", law.ID())
	}
	if law.GetGoal() != "test goal" {
		t.Fatalf("expected law.GetGoal()=test goal, got %q", law.GetGoal())
	}
}

func TestRecordFinding_NewEntryMethod(t *testing.T) {
	env := setupTestEnv(t, "workitem-recordfinding-entry-001")

	lawID, err := env.client.RecordFinding("test goal", []string{"docs"}, []*flowv1.Representation{
		{Type: "text/plain", Content: "test"},
	})
	if err != nil {
		t.Fatalf("RecordFinding() returned error: %v", err)
	}
	if lawID != "finding-001" {
		t.Fatalf("expected law_id=finding-001, got %q", lawID)
	}
}
