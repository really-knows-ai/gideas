package flow

import (
	"context"
	"fmt"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Children
// ---------------------------------------------------------------------------

// CreateChild creates a new child Workitem under the current Workitem and
// returns a handle for artefact and routing operations.
func (w *Workitem) CreateChild() (*ChildWorkitem, error) {
	resp, err := w.session.Operator.CreateChildWorkitem(context.Background(), &flowv1.CreateChildWorkitemRequest{})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: create child workitem failed: %w", err)
	}
	return &ChildWorkitem{
		session: w.session,
		id:      resp.GetChildWorkitemId(),
	}, nil
}

// GetChildren returns the status of all child Workitems created by the
// current Workitem.
func (w *Workitem) GetChildren() ([]ChildWorkitemStatus, error) {
	resp, err := w.session.Operator.GetChildren(context.Background(), &flowv1.GetChildrenRequest{})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get children failed: %w", err)
	}
	children := make([]ChildWorkitemStatus, 0, len(resp.GetChildren()))
	for _, ch := range resp.GetChildren() {
		children = append(children, ChildWorkitemStatus{
			WorkitemID:       ch.GetWorkitemId(),
			Phase:            ch.GetPhase(),
			CurrentAssignee:  ch.GetCurrentAssignee(),
			CompletionReason: ch.GetCompletionReason().String(),
		})
	}
	return children, nil
}

// ---------------------------------------------------------------------------
// FanOut
// ---------------------------------------------------------------------------

// FanOut creates child Workitems, attaches artefacts, and routes each to its
// target node. It is fail-fast: if any step fails the error is returned along
// with the children that were successfully created up to that point.
func (w *Workitem) FanOut(tasks []FanOutTask) ([]*ChildWorkitem, error) {
	children := make([]*ChildWorkitem, 0, len(tasks))
	for i, task := range tasks {
		child, err := w.CreateChild()
		if err != nil {
			return children, fmt.Errorf("flow sdk: fan-out task %d: create child: %w", i, err)
		}
		children = append(children, child)

		for j, art := range task.Artefacts {
			if err := child.StoreArtefact(art.ID, art.GovernedArtefact, art.Content); err != nil {
				return children, fmt.Errorf("flow sdk: fan-out task %d artefact %d (%s): %w", i, j, art.ID, err)
			}
		}

		if err := child.RouteTo(task.TargetNode); err != nil {
			return children, fmt.Errorf("flow sdk: fan-out task %d: route to %s: %w", i, task.TargetNode, err)
		}
	}
	return children, nil
}

// ---------------------------------------------------------------------------
// AwaitAll
// ---------------------------------------------------------------------------

// AwaitAll blocks until every child Workitem reaches a terminal phase
// (Completed or Failed). While waiting it pauses the Sidecar inactivity
// timer and resumes it before returning (even on error).
//
// The function first attempts streaming via WatchChildren (Event Bus). If the
// Event Bus is unavailable it falls back to polling via GetChildren at a 5s
// interval.
func (w *Workitem) AwaitAll() ([]ChildWorkitemStatus, error) {
	// Pause the sidecar timer — the parent is waiting, not stuck.
	if err := w.PauseTimer(); err != nil {
		return nil, fmt.Errorf("flow sdk: await all: pause timer: %w", err)
	}

	// Always resume the timer on exit, even on error.
	resumed := false
	resumeOnce := func() {
		if !resumed {
			resumed = true
			_ = w.ResumeTimer()
		}
	}
	defer resumeOnce()

	// Try streaming first, fall back to polling.
	if w.session.EventBus != nil {
		watcher, wErr := w.WatchChildren()
		if wErr == nil {
			defer watcher.Stop()
			statuses, sErr := w.awaitStreaming(watcher)
			if sErr == nil {
				resumeOnce()
				return statuses, nil
			}
			// Stream failed — fall through to polling.
		}
	}

	statuses, err := w.awaitPolling()
	if err != nil {
		return nil, err
	}
	resumeOnce()
	return statuses, nil
}

// awaitStreaming waits for all children to reach terminal state using the
// Event Bus stream. It uses GetChildren for the initial snapshot (to catch
// children that completed before the subscription started), then runs a
// background goroutine to receive stream events while concurrently polling
// every 5s.  This ensures progress even when the Event Bus does not deliver
// completion events.
func (w *Workitem) awaitStreaming(watcher *ChildWatcher) ([]ChildWorkitemStatus, error) {
	snapshot, err := w.GetChildren()
	if err != nil {
		return nil, fmt.Errorf("flow sdk: await all: initial snapshot: %w", err)
	}
	if allTerminal(snapshot) {
		return snapshot, nil
	}

	terminal := make(map[string]bool, len(snapshot))
	for _, ch := range snapshot {
		if isTerminalPhase(ch.Phase) {
			terminal[ch.WorkitemID] = true
		}
	}
	total := len(snapshot)

	// Pump Recv() events into a channel so we can select between
	// events and the polling ticker.
	events := make(chan *ChildLifecycleEvent, 16)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			evt, err := watcher.Recv()
			if err != nil {
				close(events)
				return
			}
			select {
			case events <- evt:
			case <-done:
				return
			}
		}
	}()

	pollTicker := time.NewTicker(5 * time.Second)
	defer pollTicker.Stop()

	for {
		select {
		case <-pollTicker.C:
			children, err := w.GetChildren()
			if err != nil {
				return nil, fmt.Errorf("flow sdk: await all: poll: %w", err)
			}
			if allTerminal(children) {
				return children, nil
			}
			for _, ch := range children {
				if isTerminalPhase(ch.Phase) {
					terminal[ch.WorkitemID] = true
				}
			}
		case evt, ok := <-events:
			if !ok {
				return w.GetChildren()
			}
			if isTerminalPhase(evt.Phase) {
				terminal[evt.WorkitemID] = true
			}
			if len(terminal) >= total {
				return w.GetChildren()
			}
		}
	}
}

// awaitPolling waits for all children to reach terminal state by polling
// GetChildren at a 5-second interval.
func (w *Workitem) awaitPolling() ([]ChildWorkitemStatus, error) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		children, err := w.GetChildren()
		if err != nil {
			return nil, fmt.Errorf("flow sdk: await all: poll: %w", err)
		}
		if allTerminal(children) {
			return children, nil
		}
		<-ticker.C
	}
}

// ---------------------------------------------------------------------------
// WatchChildren
// ---------------------------------------------------------------------------

// WatchChildren opens a streaming subscription to the Event Bus on the
// "workitem" channel, filtered by parent_workitem_id matching the current
// Workitem. Returns a ChildWatcher with Recv/Stop lifecycle.
//
// Requires a direct Event Bus connection (set EVENT_BUS_ADDRESS or use
// WithEventBusAddress). Returns an error if the EventBus client is nil.
func (w *Workitem) WatchChildren() (*ChildWatcher, error) {
	if w.session.EventBus == nil {
		return nil, fmt.Errorf("flow sdk: watch children requires Event Bus connection (set EVENT_BUS_ADDRESS)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := w.session.EventBus.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel: "workitem",
		Filter: &flowv1.SubscribeFilter{
			EventType: "workitem.phase_changed",
			MatchLabels: []*flowv1.Label{
				{Key: "parent_workitem_id", Value: w.id},
			},
		},
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("flow sdk: watch children subscribe failed: %w", err)
	}

	return &ChildWatcher{
		stream: stream,
		cancel: cancel,
	}, nil
}
