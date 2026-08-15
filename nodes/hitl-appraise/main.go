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

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/nodes/internal/nodeconfig"
	"github.com/foundry/flow/nodes/internal/nodeutil"
	flow "github.com/foundry/flow/sdk/go"
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

	if err := nodeutil.RunHITLNode("hitl-appraise", handler); err != nil {
		slog.Error("hitl-appraise: server failed", "error", err)
		os.Exit(1)
	}
}

// handler returns a flow.Handler that parks the Workitem in the HITL queue
// and waits for a human decision.
func handler(qm flow.QueueManager) flow.Handler {
	return nodeutil.NewHITLHandler(qm, "hitl-appraise", func(
		ctx context.Context,
		client *flow.Client,
		workitem *flow.Workitem,
		qm flow.QueueManager,
		wctx *flowv1.WorkitemContext,
	) error {
		cfg, err := nodeconfig.Load[hitlAppraiseConfig](nodeconfig.Path())
		if err != nil {
			return fmt.Errorf("hitl-appraise: load config: %w", err)
		}

		return handleAppraise(ctx, client, workitem, qm, cfg, wctx)
	})
}

// handleAppraise is the core handler logic, extracted for testability.
// It takes an injected client, QueueManager, and config rather than
// creating them internally.
func handleAppraise(
	ctx context.Context,
	client *flow.Client,
	workitem *flow.Workitem,
	qm flow.QueueManager,
	cfg *hitlAppraiseConfig,
	wctx *flowv1.WorkitemContext,
) error {
	workitemID := wctx.GetWorkitemId()

	// Discover stamp capability from topology.
	// ponytail: uses client.GetFlowTopology(ctx) for raw proto access to capabilities
	// (not yet exposed on *flow.Flow or *flow.Node for discoverStamp).
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
	if _, err := workitem.GetArtefact(cfg.InputArtefact); err != nil {
		return fmt.Errorf("hitl-appraise: read %s: %w", cfg.InputArtefact, err)
	}
	if _, err := workitem.GetArtefact(governedArtefact); err != nil {
		return fmt.Errorf("hitl-appraise: read %s: %w", governedArtefact, err)
	}

	// Park the Workitem in the HITL queue, wait for the human decision, and
	// resume the timer. hitl-appraise accepts any decision (nil valid set).
	if _, err := nodeutil.AwaitHumanDecision(ctx, qm, workitem, workitemID, "hitl-appraise", nil); err != nil {
		return err
	}

	// Stamp the governed artefact and route to default output (back to Sort).
	return nodeutil.StampAndRoute(
		workitem,
		[]flow.StampCapability{{GovernedArtefact: governedArtefact, StampName: stampName}},
		"hitl-appraise",
		"default",
	)
}

// discoverStamp queries the flow topology and extracts the node's stamp
// capability. Returns the governed artefact kind and stamp name.
func discoverStamp(ctx context.Context, client *flow.Client) (string, string, error) {
	// ponytail: uses RawOperator escape hatch for proto access to capabilities.
	topology, err := client.RawOperator().GetFlowTopology(ctx, &flowv1.GetFlowTopologyRequest{})
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
