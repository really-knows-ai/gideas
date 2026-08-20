package nodeutil

import (
	"context"
	"fmt"
	"log/slog"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flow "github.com/foundry/flow/sdk/go"
)

// NewHITLHandler returns a flow.Handler that runs the shared HITL handler
// preamble — assignment log, SDK client, workitem fetch (SetupHandler) and
// client close — then delegates to process. name is the log/error prefix
// (e.g. "hitl", "hitl-approval"); it must not include a trailing colon.
// process receives the client, workitem, queue manager, and workitem context.
func NewHITLHandler(
	qm flow.QueueManager,
	name string,
	process func(
		ctx context.Context,
		client *flow.Client,
		workitem *flow.Workitem,
		qm flow.QueueManager,
		wctx *flowv1.WorkitemContext,
	) error,
) flow.Handler {
	return func(ctx context.Context, wctx *flowv1.WorkitemContext) error {
		client, workitem, err := SetupHandler(ctx, wctx, name)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		return process(ctx, client, workitem, qm, wctx)
	}
}

// RunHITLNode wires the standard HITL node startup sequence: create the
// QueueManager from opts, build the handler from it via newHandler, and
// start the SDK server. name is the log/error prefix used for the
// queue-manager-creation error. Node-specific config loading is the
// caller's responsibility (either before this call or inside the handler).
func RunHITLNode(
	name string,
	newHandler func(qm flow.QueueManager) flow.Handler,
	opts ...flow.QueueManagerOption,
) error {
	qm, err := flow.NewQueueManager(opts...)
	if err != nil {
		return fmt.Errorf("%s: create queue manager failed: %w", name, err)
	}
	return flow.Start(newHandler(qm))
}

// AwaitHumanDecision parks the workitem in the HITL queue, pauses the sidecar
// inactivity timer, waits for a human decision, validates it, and resumes the
// timer. It returns the validated choice.
//
// name is the log/error prefix (e.g. "hitl", "hitl-approval"). When valid is
// nil the returned choice is accepted without validation — including an empty
// choice (hitl-appraise intentionally accepts any decision). Otherwise an
// empty choice (queue-manager shutdown) is an error and a choice outside the
// valid set is rejected, both before the timer is resumed.
func AwaitHumanDecision(
	ctx context.Context,
	qm flow.QueueManager,
	workitem *flow.Workitem,
	workitemID string,
	name string,
	valid map[string]bool,
) (string, error) {
	if err := qm.Enqueue(ctx, workitemID); err != nil {
		return "", fmt.Errorf("%s: enqueue: %w", name, err)
	}
	if err := workitem.PauseTimer(); err != nil {
		return "", fmt.Errorf("%s: pause timer: %w", name, err)
	}

	slog.Info(name+": awaiting human decision", "workitem_id", workitemID)
	choice, err := qm.WaitForDecision(ctx, workitemID)
	if err != nil {
		return "", fmt.Errorf("%s: wait for decision: %w", name, err)
	}

	if valid != nil {
		// Empty choice indicates QueueManager shutdown (not a human decision).
		if choice == "" {
			return "", fmt.Errorf("%s: received empty choice (queue manager shut down before decision)", name)
		}
		if !valid[choice] {
			return "", fmt.Errorf("%s: invalid choice %q: not in valid set", name, choice)
		}
	}

	slog.Info(name+": human decision received", "workitem_id", workitemID, "choice", choice)

	if err := workitem.ResumeTimer(); err != nil {
		return "", fmt.Errorf("%s: resume timer: %w", name, err)
	}
	return choice, nil
}

// StampAndRoute applies each stamp to its governed artefact, then routes the
// workitem to routeTo. An empty routeTo skips routing. Used by the HITL node
// binaries after a human decision.
func StampAndRoute(
	workitem *flow.Workitem,
	stamps []flow.StampCapability,
	name string,
	routeTo string,
) error {
	for _, sc := range stamps {
		art, err := workitem.GetArtefact(sc.GovernedArtefact)
		if err != nil {
			return fmt.Errorf("%s: get artefact %s: %w", name, sc.GovernedArtefact, err)
		}
		if err := art.Stamp(sc.StampName); err != nil {
			return fmt.Errorf("%s: stamp %s/%s: %w", name, sc.GovernedArtefact, sc.StampName, err)
		}
	}
	if routeTo != "" {
		if err := workitem.RouteTo(routeTo); err != nil {
			return fmt.Errorf("%s: route to output %q: %w", name, routeTo, err)
		}
	}
	return nil
}

// CompleteCancelled completes the workitem with COMPLETION_REASON_CANCELLED.
func CompleteCancelled(workitem *flow.Workitem, name string) error {
	if err := workitem.Complete(flow.WithReason(flowv1.CompletionReason_COMPLETION_REASON_CANCELLED)); err != nil {
		return fmt.Errorf("%s: complete (cancelled): %w", name, err)
	}
	return nil
}

// DiscoverStamp queries the flow topology and extracts the node's stamp
// capability. It returns the governed artefact kind and stamp name. The
// first STAMP:artefact/<kind>/<stamp> capability wins (a node should have
// exactly one in the stamping role, but the first is taken defensively).
func DiscoverStamp(ctx context.Context, client *flow.Client) (string, string, error) {
	// ponytail: uses RawOperator escape hatch for proto access to capabilities.
	topology, err := client.RawOperator().GetFlowTopology(ctx, &flowv1.GetFlowTopologyRequest{})
	if err != nil {
		return "", "", fmt.Errorf("get flow topology: %w", err)
	}

	stamps := flow.ParseStampCapabilities(topology.GetSelf().GetCapabilities())
	if len(stamps) == 0 {
		return "", "", fmt.Errorf("no STAMP:artefact capability found in topology")
	}

	sc := stamps[0]
	return sc.GovernedArtefact, sc.StampName, nil
}
