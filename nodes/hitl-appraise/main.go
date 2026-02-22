// HITL Appraise is the human-in-the-loop reviewer node of the Foundry Cycle.
//
// It replaces the LLM-backed Appraise node with human review. Instead of
// running AI agents, it parks the Workitem in a HITL queue and waits for a
// human reviewer to complete their review through the Dashboard/BFF.
//
// The node:
//  1. Discovers its own stamp capability from the flow topology.
//  2. Reads the input and review artefacts (so the queue item carries context).
//  3. Enqueues the Workitem and pauses the Sidecar's inactivity timer.
//  4. Blocks until the human signals "done" via POST /queue/{id}/decide.
//  5. Resumes the timer, stamps the governed artefact, and routes to output.
//
// The human performs all review actions (feedback evaluation, new feedback,
// learning capture) through the Dashboard/BFF, which calls the Archivist
// and Librarian directly. The node's job is purely: park, wait, stamp, route.
//
// Configuration is loaded from a ConfigMap-mounted YAML file:
//
//	inputArtefact: "petition"
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
)

// hitlAppraiseConfig holds the node's configuration, loaded from a
// ConfigMap-mounted YAML file via nodeconfig.Load.
type hitlAppraiseConfig struct {
	// InputArtefact is the artefact ID of the input brief (e.g. "petition").
	// This cannot be derived from capabilities and must be configured.
	InputArtefact string `yaml:"inputArtefact"`
}

func main() {
	slog.Info("hitl-appraise: starting")

	qm, err := flow.NewQueueManager()
	if err != nil {
		slog.Error("hitl-appraise: create queue manager failed", "error", err)
		os.Exit(1)
	}

	if err := flow.Start(handler(qm), flow.WithQueueManager(qm)); err != nil {
		slog.Error("hitl-appraise: server failed", "error", err)
		os.Exit(1)
	}
}

// handler returns a flow.Handler that parks the Workitem in the HITL queue
// and waits for a human decision.
func handler(qm flow.QueueManager) flow.Handler {
	return func(ctx context.Context, wctx *flowv1.WorkitemContext) error {
		_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())

		client, err := flow.NewClient()
		if err != nil {
			return fmt.Errorf("hitl-appraise: create client: %w", err)
		}
		defer func() { _ = client.Close() }()

		cfg, err := nodeconfig.Load[hitlAppraiseConfig](nodeconfig.Path())
		if err != nil {
			return fmt.Errorf("hitl-appraise: load config: %w", err)
		}

		return handleAppraise(ctx, client, qm, cfg, wctx)
	}
}

// handleAppraise is the core handler logic, extracted for testability.
// It takes an injected client, QueueManager, and config rather than
// creating them internally.
func handleAppraise(
	ctx context.Context,
	client *flow.Client,
	qm flow.QueueManager,
	cfg *hitlAppraiseConfig,
	wctx *flowv1.WorkitemContext,
) error {
	workitemID := wctx.GetWorkitemId()

	// Discover stamp capability from topology.
	governedArtefact, stampName, err := discoverStamp(ctx, client)
	if err != nil {
		return fmt.Errorf("hitl-appraise: %w", err)
	}

	slog.Info("hitl-appraise: handling workitem",
		"workitem_id", workitemID,
		"input_artefact", cfg.InputArtefact,
		"governed_artefact", governedArtefact,
		"stamp", stampName,
	)

	// Read artefacts to establish context (makes them visible in logs).
	if _, err := client.GetArtefact(ctx, cfg.InputArtefact); err != nil {
		return fmt.Errorf("hitl-appraise: read %s: %w", cfg.InputArtefact, err)
	}
	if _, err := client.GetArtefact(ctx, governedArtefact); err != nil {
		return fmt.Errorf("hitl-appraise: read %s: %w", governedArtefact, err)
	}

	// Park the Workitem in the HITL queue.
	if err := qm.Enqueue(ctx, workitemID); err != nil {
		return fmt.Errorf("hitl-appraise: enqueue: %w", err)
	}

	// Pause the Sidecar timer — we'll be waiting for a human.
	if err := client.PauseTimer(ctx); err != nil {
		return fmt.Errorf("hitl-appraise: pause timer: %w", err)
	}

	// Block until the human decides (via POST /queue/{id}/decide).
	slog.Info("hitl-appraise: awaiting human decision", "workitem_id", workitemID)
	if _, err := qm.WaitForDecision(ctx, workitemID); err != nil {
		return fmt.Errorf("hitl-appraise: wait for decision: %w", err)
	}
	slog.Info("hitl-appraise: human decision received", "workitem_id", workitemID)

	// Resume the Sidecar timer.
	if err := client.ResumeTimer(ctx); err != nil {
		return fmt.Errorf("hitl-appraise: resume timer: %w", err)
	}

	// Stamp the governed artefact.
	if _, err := client.StampArtefact(ctx, governedArtefact, stampName); err != nil {
		return fmt.Errorf("hitl-appraise: stamp %s/%s: %w", governedArtefact, stampName, err)
	}

	// Route to default output (back to Sort).
	if _, err := client.RouteToOutput(ctx, "default"); err != nil {
		return fmt.Errorf("hitl-appraise: route to output: %w", err)
	}
	return nil
}

// discoverStamp queries the flow topology and extracts the node's stamp
// capability. Returns the governed artefact kind and stamp name.
func discoverStamp(ctx context.Context, client *flow.Client) (string, string, error) {
	topology, err := client.GetFlowTopology(ctx)
	if err != nil {
		return "", "", fmt.Errorf("get flow topology: %w", err)
	}

	stamps := flow.ParseStampCapabilities(topology.GetSelf().GetCapabilities())
	if len(stamps) == 0 {
		return "", "", fmt.Errorf("no STAMP:artefact capability found in topology")
	}

	// Use the first stamp capability. A node should have exactly one
	// in the appraise role, but we take the first defensively.
	sc := stamps[0]
	return sc.GovernedArtefact, sc.StampName, nil
}
