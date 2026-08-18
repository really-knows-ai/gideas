package flow

import (
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
)

// ---------------------------------------------------------------------------
// Tests — WatchChildren
// ---------------------------------------------------------------------------

func TestWorkitem_WatchChildren_ReturnsWatcher(t *testing.T) {
	parentID := "wi-watch-watcher"
	ebSpy := &spyEventBusServer{
		events: []*flowv1.FlowEvent{
			{
				WorkitemId: "child-001",
				EventType:  "workitem.phase_changed",
				Labels: []*flowv1.Label{
					{Key: "parent_workitem_id", Value: parentID},
					{Key: "phase", Value: "Running"},
					{Key: "node_id", Value: "codify-smt"},
				},
			},
		},
	}

	client := setupGRPCTestEnvWithEventBus(t, parentID,
		func(s *grpc.Server) {
			flowv1.RegisterSidecarServiceServer(s, &spyServer{})
			flowv1.RegisterOperatorServiceServer(s, &spyServer{})
			flowv1.RegisterArchivistServiceServer(s, &spyServer{})
			flowv1.RegisterLibrarianServiceServer(s, &spyServer{})
			flowv1.RegisterFrictionLedgerServiceServer(s, &spyServer{})
		},
		func(s *grpc.Server) {
			flowv1.RegisterFlowEventBusServiceServer(s, ebSpy)
		},
	)

	wi, err := client.GetWorkitem(parentID)
	if err != nil {
		t.Fatalf("GetWorkitem() error: %v", err)
	}

	watcher, err := wi.WatchChildren()
	if err != nil {
		t.Fatalf("WatchChildren() error: %v", err)
	}
	defer watcher.Stop()

	if watcher == nil {
		t.Fatal("expected non-nil ChildWatcher")
	}

	// Verify Recv works.
	evt, err := watcher.Recv()
	if err != nil {
		t.Fatalf("Recv() error: %v", err)
	}
	if evt.WorkitemID != "child-001" {
		t.Fatalf("expected child-001, got %q", evt.WorkitemID)
	}
	if evt.Phase != PhaseRunning {
		t.Fatalf("expected Running, got %q", evt.Phase)
	}
}

func TestWorkitem_WatchChildren_NoEventBus_ReturnsError(t *testing.T) {
	spy := &fanoutSpy{}
	wi := setupTestWorkitem(t, "wi-watch-noeb", func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

	_, err := wi.WatchChildren()
	if err == nil {
		t.Fatal("expected error when EventBus is nil")
	}
}
