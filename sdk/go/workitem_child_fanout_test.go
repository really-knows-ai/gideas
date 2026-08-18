package flow

import (
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// setupTestWorkitem creates a Workitem wired to a fanoutSpy via bufconn.
func setupTestWorkitem(t *testing.T, workitemID string, registerServices func(*grpc.Server)) *Workitem {
	t.Helper()
	client, _ := setupGRPCTestEnv(t, workitemID, registerServices)
	wi, err := client.GetWorkitem(workitemID)
	if err != nil {
		t.Fatalf("GetWorkitem() error: %v", err)
	}
	return wi
}

// ---------------------------------------------------------------------------
// Tests — CreateChild
// ---------------------------------------------------------------------------

func TestWorkitem_CreateChild_ReturnsHandle(t *testing.T) {
	spy := &fanoutSpy{}
	wi := setupTestWorkitem(t, "wi-create-child", func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

	child, err := wi.CreateChild()
	if err != nil {
		t.Fatalf("CreateChild() error: %v", err)
	}
	if child.ID() == "" {
		t.Fatal("expected non-empty child ID")
	}
	if child.session != wi.session {
		t.Fatal("child.session not wired to workitem session")
	}
}

// ---------------------------------------------------------------------------
// Tests — GetChildren
// ---------------------------------------------------------------------------

func TestWorkitem_GetChildren_ReturnsStatuses(t *testing.T) {
	spy := &fanoutSpy{}
	wi := setupTestWorkitem(t, "wi-get-children", func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

	spy.setGetChildrenResp(&flowv1.GetChildrenResponse{
		Children: []*flowv1.ChildWorkitemStatus{
			{WorkitemId: "child-001", Phase: PhaseRunning, CurrentAssignee: "node-a"},
			{WorkitemId: "child-002", Phase: PhaseCompleted},
		},
	})

	children, err := wi.GetChildren()
	if err != nil {
		t.Fatalf("GetChildren() error: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}
	if children[0].WorkitemID != "child-001" {
		t.Fatalf("expected child-001, got %q", children[0].WorkitemID)
	}
	if children[0].Phase != PhaseRunning {
		t.Fatalf("expected Running, got %q", children[0].Phase)
	}
}

// ---------------------------------------------------------------------------
// Tests — FanOut
// ---------------------------------------------------------------------------

func TestWorkitem_FanOut_CreatesAndRoutes(t *testing.T) {
	spy := &fanoutSpy{}
	wi := setupTestWorkitem(t, "wi-fanout", func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

	tasks := []FanOutTask{
		{
			TargetNode: "codify-smt",
			Artefacts: []ChildArtefact{
				{ID: "input", GovernedArtefact: "codification-input", Content: []byte("goal-1")},
			},
		},
		{
			TargetNode: "codify-rego",
			Artefacts: []ChildArtefact{
				{ID: "input", GovernedArtefact: "codification-input", Content: []byte("goal-2")},
			},
		},
		{
			TargetNode: "codify-prolog",
			Artefacts: []ChildArtefact{
				{ID: "input", GovernedArtefact: "codification-input", Content: []byte("goal-3")},
				{ID: "context", GovernedArtefact: "law-context", Content: []byte("ctx-3")},
			},
		},
	}

	children, err := wi.FanOut(tasks)
	if err != nil {
		t.Fatalf("FanOut() error: %v", err)
	}
	if len(children) != 3 {
		t.Fatalf("expected 3 children, got %d", len(children))
	}

	// Verify unique IDs.
	ids := make(map[string]bool)
	for _, ch := range children {
		if ids[ch.ID()] {
			t.Fatalf("duplicate child ID: %s", ch.ID())
		}
		ids[ch.ID()] = true
		// Verify each child has a wired session.
		if ch.session != wi.session {
			t.Fatal("child session not wired")
		}
	}

	// Verify artefacts were stored.
	spy.mu.Lock()
	storedCount := len(spy.storedArtefacts)
	spy.mu.Unlock()
	if storedCount != 4 { // 1 + 1 + 2
		t.Fatalf("expected 4 stored artefacts, got %d", storedCount)
	}

	// Verify all children were routed.
	spy.mu.Lock()
	routedCount := len(spy.routedChildren)
	spy.mu.Unlock()
	if routedCount != 3 {
		t.Fatalf("expected 3 routed children, got %d", routedCount)
	}
}

func TestWorkitem_FanOut_EmptyTasks(t *testing.T) {
	spy := &fanoutSpy{}
	wi := setupTestWorkitem(t, "wi-fanout-empty", func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

	children, err := wi.FanOut(nil)
	if err != nil {
		t.Fatalf("FanOut(nil) error: %v", err)
	}
	if len(children) != 0 {
		t.Fatalf("expected 0 children, got %d", len(children))
	}
}

func TestWorkitem_FanOut_FailFast_OnCreateError(t *testing.T) {
	spy := &fanoutSpy{
		createErr: status.Errorf(codes.Internal, "operator unavailable"),
	}
	wi := setupTestWorkitem(t, "wi-fanout-createerr", func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

	tasks := []FanOutTask{
		{TargetNode: "node-a", Artefacts: []ChildArtefact{
			{ID: "in", GovernedArtefact: "ga", Content: []byte("data")},
		}},
		{TargetNode: "node-b", Artefacts: []ChildArtefact{
			{ID: "in", GovernedArtefact: "ga", Content: []byte("data")},
		}},
	}

	children, err := wi.FanOut(tasks)
	if err == nil {
		t.Fatal("expected error from FanOut, got nil")
	}
	if len(children) != 0 {
		t.Fatalf("expected 0 children on first-create failure, got %d", len(children))
	}
}

func TestWorkitem_FanOut_FailFast_OnStoreError(t *testing.T) {
	spy := &fanoutSpy{
		storeErr: status.Errorf(codes.Internal, "archivist down"),
	}
	wi := setupTestWorkitem(t, "wi-fanout-storeerr", func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

	tasks := []FanOutTask{
		{TargetNode: "node-a", Artefacts: []ChildArtefact{
			{ID: "in", GovernedArtefact: "ga", Content: []byte("data")},
		}},
	}

	children, err := wi.FanOut(tasks)
	if err == nil {
		t.Fatal("expected error from FanOut on store failure, got nil")
	}
	if len(children) != 1 {
		t.Fatalf("expected 1 child (created before store failure), got %d", len(children))
	}
}

func TestWorkitem_FanOut_FailFast_OnRouteError(t *testing.T) {
	spy := &fanoutSpy{
		routeChildErr: status.Errorf(codes.FailedPrecondition, "target node not found"),
	}
	wi := setupTestWorkitem(t, "wi-fanout-routeerr", func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

	tasks := []FanOutTask{
		{TargetNode: "nonexistent", Artefacts: []ChildArtefact{
			{ID: "in", GovernedArtefact: "ga", Content: []byte("data")},
		}},
		{TargetNode: "also-missing"},
	}

	children, err := wi.FanOut(tasks)
	if err == nil {
		t.Fatal("expected error from FanOut on route failure, got nil")
	}
	// First child was created but route failed.
	if len(children) != 1 {
		t.Fatalf("expected 1 child (created before route failure), got %d", len(children))
	}
}
