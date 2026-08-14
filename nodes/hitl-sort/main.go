// HITL Sort is a human-in-the-loop routing node for the Foundry Cycle.
//
// Unlike the algorithmic Sort node, HITL Sort parks a Workitem in the HITL
// queue and waits for a human to explicitly choose a routing output from a
// configured set of options. This is used when routing decisions require
// human judgment that cannot be automated.
//
// The node:
//  1. Discovers available outputs from the flow topology.
//  2. Validates configured humanChoices against topology outputs.
//  3. Enqueues the Workitem and pauses the Sidecar's inactivity timer.
//  4. Blocks until the human picks a choice via POST /queue/{id}/decide.
//  5. Validates the returned choice against configured humanChoices.
//  6. Resumes the timer, optionally stamps the governed artefact, routes.
//
// The GET /choices endpoint is registered on the QueueManager's HTTP mux so
// the Dashboard can discover available routing options for this node.
//
// Configuration (YAML via NODE_CONFIG_PATH, default /etc/foundry/node-config.yaml):
//
//	humanChoices:
//	  - output: approve
//	    label: "Approve for Release"
//	  - output: reject
//	    label: "Send Back for Revision"
//	stamp: true  # optional, default false
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/nodes/internal/nodeconfig"
	"github.com/foundry/flow/nodes/internal/nodeutil"
	flow "github.com/foundry/flow/sdk/go"
)

// hitlSortConfig holds the node's configuration, loaded from a
// ConfigMap-mounted YAML file via nodeconfig.Load.
type hitlSortConfig struct {
	// HumanChoices defines the set of routing options presented to humans.
	// Each entry maps a topology output name to a human-friendly label.
	HumanChoices []choiceMapping `yaml:"humanChoices"`

	// Stamp controls whether the node applies a STAMP to the governed
	// artefact after the human decides. Default false.
	Stamp bool `yaml:"stamp"`
}

// choiceMapping maps a topology output name to a human-friendly display label.
type choiceMapping struct {
	Output string `yaml:"output"`
	Label  string `yaml:"label"`
}

func main() {
	slog.Info("hitl-sort: starting")

	cfg, err := nodeconfig.Load[hitlSortConfig](nodeconfig.Path())
	if err != nil {
		slog.Error("hitl-sort: load config failed", "error", err)
		os.Exit(1)
	}

	qm, err := flow.NewQueueManager(
		flow.WithCustomRoutes(func(mux *http.ServeMux) {
			mux.HandleFunc("GET /choices", handleChoices(cfg))
		}),
	)
	if err != nil {
		slog.Error("hitl-sort: create queue manager failed", "error", err)
		os.Exit(1)
	}

	if err := flow.Start(handler(qm, cfg), flow.WithQueueManager(qm)); err != nil {
		slog.Error("hitl-sort: server failed", "error", err)
		os.Exit(1)
	}
}

// handler returns a flow.Handler that parks the Workitem in the HITL queue,
// waits for a human routing decision, and routes accordingly.
func handler(qm flow.QueueManager, cfg *hitlSortConfig) flow.Handler {
	return func(ctx context.Context, wctx *flowv1.WorkitemContext) error {
		client, workitem, err := nodeutil.SetupHandler(ctx, wctx, "hitl-sort")
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()

		return handleSort(ctx, client, workitem, qm, cfg, wctx)
	}
}

// handleSort is the core handler logic, extracted for testability.
func handleSort(
	ctx context.Context,
	client *flow.Client,
	workitem *flow.Workitem,
	qm flow.QueueManager,
	cfg *hitlSortConfig,
	wctx *flowv1.WorkitemContext,
) error {
	workitemID := wctx.GetWorkitemId()

	// Discover topology to build the valid output set and optionally find stamp.
	// ponytail: uses RawOperator escape hatch for raw proto access to outputs
	// (not yet exposed on *flow.Flow).
	topology, err := client.RawOperator().GetFlowTopology(ctx, &flowv1.GetFlowTopologyRequest{})
	if err != nil {
		return fmt.Errorf("hitl-sort: get flow topology: %w", err)
	}

	// Build set of available output names from topology.
	availableOutputs := make(map[string]bool)
	for _, out := range topology.GetSelf().GetOutputs() {
		availableOutputs[out.GetName()] = true
	}

	// Validate configured choices against available topology outputs.
	for _, cm := range cfg.HumanChoices {
		if !availableOutputs[cm.Output] {
			return fmt.Errorf("hitl-sort: configured choice %q is not a valid output in topology", cm.Output)
		}
	}

	// Build the set of valid choice output names for runtime validation.
	validChoices := make(map[string]bool, len(cfg.HumanChoices))
	for _, cm := range cfg.HumanChoices {
		validChoices[cm.Output] = true
	}

	// Discover stamp capability if stamping is configured.
	var governedArtefact, stampName string
	if cfg.Stamp {
		stamps := flow.ParseStampCapabilities(topology.GetSelf().GetCapabilities())
		if len(stamps) == 0 {
			return fmt.Errorf("hitl-sort: no STAMP:artefact capability found in topology")
		}
		governedArtefact = stamps[0].GovernedArtefact
		stampName = stamps[0].StampName
	}

	slog.Info("hitl-sort: handling workitem",
		"workitem_id", workitemID,
		"choices", len(cfg.HumanChoices),
		"stamp", cfg.Stamp,
	)

	// Park the Workitem in the HITL queue, wait for the human decision,
	// validate it, and resume the timer.
	choice, err := nodeutil.AwaitHumanDecision(ctx, qm, workitem, workitemID, "hitl-sort", validChoices)
	if err != nil {
		return err
	}

	// Optionally stamp the governed artefact, then route to the chosen output.
	var stamps []flow.StampCapability
	if cfg.Stamp {
		stamps = []flow.StampCapability{{GovernedArtefact: governedArtefact, StampName: stampName}}
	}
	return nodeutil.StampAndRoute(workitem, stamps, "hitl-sort", choice)
}

// handleChoices returns the configured humanChoices as JSON so the Dashboard
// can build the choice UI. Registered via WithCustomRoutes on the QueueManager.
func handleChoices(cfg *hitlSortConfig) http.HandlerFunc {
	type choiceResponse struct {
		Output string `json:"output"`
		Label  string `json:"label"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		choices := make([]choiceResponse, 0, len(cfg.HumanChoices))
		for _, cm := range cfg.HumanChoices {
			choices = append(choices, choiceResponse(cm))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(choices)
	}
}
