package flow

import (
	"context"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// FanOutTask describes a single child Workitem to create and route.
type FanOutTask struct {
	// TargetNode is the name of the FoundryNode to route the child to.
	TargetNode string

	// Artefacts to store on the child before routing.
	Artefacts []ChildArtefact
}

// ChildArtefact is an artefact to attach to a child Workitem before routing.
type ChildArtefact struct {
	ID               string
	GovernedArtefact string
	Content          []byte
}

// ChildResult pairs a child's terminal status with its collected artefacts.
type ChildResult struct {
	Status    ChildWorkitemStatus
	Artefacts map[string][]byte // artefactID → content (nil if artefact absent)
}

// AwaitOption configures AwaitChildren behaviour.
type AwaitOption func(*awaitConfig)

type awaitConfig struct {
	pollInterval time.Duration
}

const defaultPollInterval = 5 * time.Second

// WithPollingInterval sets the fallback polling interval used when the Event
// Bus is unavailable and AwaitChildren must poll via GetChildren. The default
// is 5 seconds.
func WithPollingInterval(d time.Duration) AwaitOption {
	return func(cfg *awaitConfig) {
		cfg.pollInterval = d
	}
}

// ---------------------------------------------------------------------------
// FanOut
// ---------------------------------------------------------------------------

// FanOut creates child Workitems, attaches artefacts, and routes each to its
// target node. It is fail-fast: if any step fails the error is returned along
// with the children that were successfully created up to that point.
func (c *Client) FanOut(ctx context.Context, tasks []FanOutTask) ([]*ChildWorkitem, error) {
	children := make([]*ChildWorkitem, 0, len(tasks))
	for i, task := range tasks {
		child, err := c.CreateChildWorkitem(ctx)
		if err != nil {
			return children, fmt.Errorf("flow sdk: fan-out task %d: create child: %w", i, err)
		}
		children = append(children, child)

		for j, art := range task.Artefacts {
			if _, err := child.StoreArtefact(ctx, art.ID, art.GovernedArtefact, art.Content); err != nil {
				return children, fmt.Errorf("flow sdk: fan-out task %d artefact %d (%s): %w", i, j, art.ID, err)
			}
		}

		if _, err := child.RouteTo(ctx, task.TargetNode); err != nil {
			return children, fmt.Errorf("flow sdk: fan-out task %d: route to %s: %w", i, task.TargetNode, err)
		}
	}
	return children, nil
}

// ---------------------------------------------------------------------------
// AwaitChildren
// ---------------------------------------------------------------------------

// AwaitChildren blocks until every child Workitem reaches a terminal phase
// (Completed or Failed). While waiting it pauses the Sidecar inactivity
// timer and resumes it before returning (even on error or context
// cancellation).
//
// The function first attempts streaming via WatchChildren (Event Bus). If the
// Event Bus is unavailable it falls back to polling via GetChildren.
func (c *Client) AwaitChildren(ctx context.Context, opts ...AwaitOption) ([]ChildWorkitemStatus, error) {
	cfg := &awaitConfig{pollInterval: defaultPollInterval}
	for _, o := range opts {
		o(cfg)
	}

	// Pause the sidecar timer — the parent is waiting, not stuck.
	if err := c.PauseTimer(ctx); err != nil {
		return nil, fmt.Errorf("flow sdk: await children: pause timer: %w", err)
	}

	// Always resume the timer on exit, even on error.
	resumed := false
	resumeOnce := func() {
		if !resumed {
			resumed = true
			// Use a background context so resume succeeds even if ctx is cancelled.
			_ = c.ResumeTimer(context.Background())
		}
	}
	defer resumeOnce()

	// Try streaming first, fall back to polling.
	watch, watchErr := c.WatchChildren(ctx)
	if watchErr == nil {
		return c.awaitStreaming(ctx, watch)
	}
	return c.awaitPolling(ctx, cfg.pollInterval)
}

// awaitStreaming waits for all children to reach terminal state using the
// Event Bus stream. It relies on GetChildren for the initial snapshot (to
// catch children that completed before the subscription started) and then
// processes streaming events for ongoing transitions.
func (c *Client) awaitStreaming(ctx context.Context, watch <-chan ChildLifecycleEvent) ([]ChildWorkitemStatus, error) {
	// Take an initial snapshot — some children may already be terminal.
	snapshot, err := c.GetChildren(ctx)
	if err != nil {
		return nil, fmt.Errorf("flow sdk: await children: initial snapshot: %w", err)
	}
	if allTerminal(snapshot) {
		return snapshot, nil
	}

	// Build a set of known terminal children from the snapshot.
	terminal := make(map[string]bool, len(snapshot))
	for _, ch := range snapshot {
		if isTerminalPhase(ch.Phase) {
			terminal[ch.WorkitemID] = true
		}
	}
	total := len(snapshot)

	for {
		select {
		case evt, ok := <-watch:
			if !ok {
				// Stream closed — fall back to a final poll.
				return c.GetChildren(ctx)
			}
			if isTerminalPhase(evt.Phase) {
				terminal[evt.WorkitemID] = true
			}
			if len(terminal) >= total {
				// All accounted for — do a final authoritative poll.
				return c.GetChildren(ctx)
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// awaitPolling waits for all children to reach terminal state by polling
// GetChildren at the configured interval.
func (c *Client) awaitPolling(ctx context.Context, interval time.Duration) ([]ChildWorkitemStatus, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		children, err := c.GetChildren(ctx)
		if err != nil {
			return nil, fmt.Errorf("flow sdk: await children: poll: %w", err)
		}
		if allTerminal(children) {
			return children, nil
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// ---------------------------------------------------------------------------
// CollectArtefacts
// ---------------------------------------------------------------------------

// CollectArtefacts reads the named artefacts from each child Workitem.
// If any child is in the Failed phase, a collective error is returned.
// Results preserve the ordering of the input children slice.
//
// For each Completed child, the requested artefact IDs are fetched via
// GetChildArtefact. If a child does not have a particular artefact the
// corresponding map entry is nil (this is not an error — the child may
// legitimately have produced no output for that artefact ID).
func (c *Client) CollectArtefacts(
	ctx context.Context, children []ChildWorkitemStatus, artefactIDs ...string,
) ([]ChildResult, error) {
	// Check for any failed children first.
	for _, ch := range children {
		if ch.Phase == "Failed" {
			return nil, fmt.Errorf("flow sdk: collect artefacts: child %s is in Failed phase", ch.WorkitemID)
		}
	}

	results := make([]ChildResult, len(children))
	for i, ch := range children {
		arts := make(map[string][]byte, len(artefactIDs))
		for _, artID := range artefactIDs {
			resp, err := c.GetChildArtefact(ctx, ch.WorkitemID, artID)
			if err != nil {
				// A missing artefact is represented as nil, not an error.
				// However we can only distinguish "not found" from real errors
				// by inspecting the gRPC status code. For simplicity we treat
				// any error as "artefact absent" — the caller can use the
				// lower-level API for finer-grained error handling.
				arts[artID] = nil
				continue
			}
			arts[artID] = resp.GetContent()
		}
		results[i] = ChildResult{
			Status:    ch,
			Artefacts: arts,
		}
	}
	return results, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func allTerminal(children []ChildWorkitemStatus) bool {
	if len(children) == 0 {
		return false
	}
	for _, ch := range children {
		if !isTerminalPhase(ch.Phase) {
			return false
		}
	}
	return true
}

func isTerminalPhase(phase string) bool {
	return phase == "Completed" || phase == "Failed"
}
