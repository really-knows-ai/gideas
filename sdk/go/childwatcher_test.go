package flow

import (
	"io"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// controllableEventBusSpy — a spy that lets tests control event delivery
// ---------------------------------------------------------------------------

type controllableEventBusSpy struct {
	flowv1.UnimplementedFlowEventBusServiceServer
	events chan *flowv1.FlowEvent // send to this to push events
	done   chan struct{}          // close to end stream normally
	err    error                  // if set, Subscribe returns this error immediately
}

func (s *controllableEventBusSpy) Subscribe(
	req *flowv1.SubscribeRequest,
	stream grpc.ServerStreamingServer[flowv1.FlowEvent],
) error {
	if s.err != nil {
		return s.err
	}
	for {
		select {
		case evt, ok := <-s.events:
			if !ok {
				return nil // normal stream end -> io.EOF
			}
			if err := stream.Send(evt); err != nil {
				return err
			}
		case <-s.done:
			return nil // Stop() triggered end
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

// ---------------------------------------------------------------------------
// Tests — ChildWatcher Recv/Stop lifecycle
// ---------------------------------------------------------------------------

func TestChildWatcher_Recv_BlocksUntilEventArrives(t *testing.T) {
	parentID := "wi-watcher-block"
	ctrl := &controllableEventBusSpy{
		events: make(chan *flowv1.FlowEvent, 1),
		done:   make(chan struct{}),
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
			flowv1.RegisterFlowEventBusServiceServer(s, ctrl)
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

	// Send an event after a short delay.
	done := make(chan *ChildLifecycleEvent, 1)
	errCh := make(chan error, 1)
	go func() {
		evt, recvErr := watcher.Recv()
		if recvErr != nil {
			errCh <- recvErr
			return
		}
		done <- evt
	}()

	time.Sleep(50 * time.Millisecond)
	ctrl.events <- &flowv1.FlowEvent{
		WorkitemId: "child-w-001",
		EventType:  "workitem.phase_changed",
		Labels: []*flowv1.Label{
			{Key: "parent_workitem_id", Value: parentID},
			{Key: "phase", Value: "Running"},
			{Key: "node_id", Value: "codify-smt"},
		},
	}

	select {
	case evt := <-done:
		if evt.WorkitemID != "child-w-001" {
			t.Fatalf("expected child-w-001, got %q", evt.WorkitemID)
		}
		if evt.Phase != "Running" {
			t.Fatalf("expected Running phase, got %q", evt.Phase)
		}
		if evt.NodeID != "codify-smt" {
			t.Fatalf("expected codify-smt node, got %q", evt.NodeID)
		}
	case recvErr := <-errCh:
		t.Fatalf("Recv() returned error: %v", recvErr)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Recv to return")
	}
}

func TestChildWatcher_Recv_ReturnsIOEOF_OnStreamEnd(t *testing.T) {
	parentID := "wi-watcher-eof"
	ctrl := &controllableEventBusSpy{
		events: make(chan *flowv1.FlowEvent, 2),
		done:   make(chan struct{}),
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
			flowv1.RegisterFlowEventBusServiceServer(s, ctrl)
		},
	)

	wi, _ := client.GetWorkitem(parentID)
	watcher, _ := wi.WatchChildren()
	defer watcher.Stop()

	// Send two events then close the channel.
	ctrl.events <- &flowv1.FlowEvent{
		WorkitemId: "child-001",
		Labels: []*flowv1.Label{
			{Key: "phase", Value: "Running"},
		},
	}
	ctrl.events <- &flowv1.FlowEvent{
		WorkitemId: "child-001",
		Labels: []*flowv1.Label{
			{Key: "phase", Value: "Completed"},
		},
	}
	close(ctrl.events)

	// First Recv should get the first event.
	evt1, err := watcher.Recv()
	if err != nil {
		t.Fatalf("first Recv() error: %v", err)
	}
	if evt1.Phase != "Running" {
		t.Fatalf("expected Running, got %q", evt1.Phase)
	}

	// Second Recv should get the second event.
	evt2, err := watcher.Recv()
	if err != nil {
		t.Fatalf("second Recv() error: %v", err)
	}
	if evt2.Phase != "Completed" {
		t.Fatalf("expected Completed, got %q", evt2.Phase)
	}

	// Third Recv should return io.EOF (stream ended).
	_, err = watcher.Recv()
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestChildWatcher_Stop_CancelsStream(t *testing.T) {
	parentID := "wi-watcher-stop"
	ctrl := &controllableEventBusSpy{
		events: make(chan *flowv1.FlowEvent),
		done:   make(chan struct{}),
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
			flowv1.RegisterFlowEventBusServiceServer(s, ctrl)
		},
	)

	wi, _ := client.GetWorkitem(parentID)
	watcher, _ := wi.WatchChildren()

	// Recv in a goroutine, then Stop from main goroutine.
	errCh := make(chan error, 1)
	go func() {
		_, recvErr := watcher.Recv()
		errCh <- recvErr
	}()

	time.Sleep(50 * time.Millisecond)
	watcher.Stop()

	select {
	case err := <-errCh:
		if err != io.EOF {
			t.Fatalf("expected io.EOF after Stop, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Recv to return after Stop")
	}
}

func TestChildWatcher_Stop_Idempotent(t *testing.T) {
	parentID := "wi-watcher-idempotent"
	ctrl := &controllableEventBusSpy{
		events: make(chan *flowv1.FlowEvent),
		done:   make(chan struct{}),
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
			flowv1.RegisterFlowEventBusServiceServer(s, ctrl)
		},
	)

	wi, _ := client.GetWorkitem(parentID)
	watcher, _ := wi.WatchChildren()

	// Call Stop twice — should not panic or deadlock.
	watcher.Stop()
	watcher.Stop()
}

func TestChildWatcher_Recv_ReturnsRealError(t *testing.T) {
	parentID := "wi-watcher-realerr"
	ctrl := &controllableEventBusSpy{
		events: make(chan *flowv1.FlowEvent),
		done:   make(chan struct{}),
		err:    status.Errorf(codes.Unavailable, "eventbus down"),
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
			flowv1.RegisterFlowEventBusServiceServer(s, ctrl)
		},
	)

	wi, _ := client.GetWorkitem(parentID)
	watcher, err := wi.WatchChildren()
	if err != nil {
		t.Fatalf("WatchChildren() should succeed (streaming RPC): %v", err)
	}
	defer watcher.Stop()

	// The server error appears on first Recv, not on Subscribe.
	_, err = watcher.Recv()
	if err == nil {
		t.Fatal("expected error from Recv when Subscribe handler returns error")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unavailable {
		t.Fatalf("expected Unavailable error, got %v", err)
	}
}
