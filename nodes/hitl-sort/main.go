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

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
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
		_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())

		client, err := flow.NewClient()
		if err != nil {
			return fmt.Errorf("hitl-sort: create client: %w", err)
		}
		defer func() { _ = client.Close() }()

		return handleSort(ctx, client, qm, cfg, wctx)
	}
}

// handleSort is the core handler logic, extracted for testability.
func handleSort(
	ctx context.Context,
	client *flow.Client,
	qm flow.QueueManager,
	cfg *hitlSortConfig,
	wctx *flowv1.WorkitemContext,
) error {
	workitemID := wctx.GetWorkitemId()

	// Discover topology to build the valid output set and optionally find stamp.
	topology, err := client.GetFlowTopology(ctx)
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

	// Park the Workitem in the HITL queue.
	if err := qm.Enqueue(ctx, workitemID); err != nil {
		return fmt.Errorf("hitl-sort: enqueue: %w", err)
	}

	// Pause the Sidecar timer — we'll be waiting for a human.
	if err := client.PauseTimer(ctx); err != nil {
		return fmt.Errorf("hitl-sort: pause timer: %w", err)
	}

	// Block until the human picks a choice (via POST /queue/{id}/decide).
	slog.Info("hitl-sort: awaiting human routing decision", "workitem_id", workitemID)
	choice, err := qm.WaitForDecision(ctx, workitemID)
	if err != nil {
		return fmt.Errorf("hitl-sort: wait for decision: %w", err)
	}

	// Validate the choice against configured humanChoices.
	// An empty choice indicates QueueManager shutdown (not a human decision).
	if choice == "" {
		return fmt.Errorf("hitl-sort: received empty choice (queue manager shut down before decision)")
	}
	if !validChoices[choice] {
		return fmt.Errorf("hitl-sort: invalid choice %q: must be one of the configured humanChoices", choice)
	}

	slog.Info("hitl-sort: human decision received", "workitem_id", workitemID, "choice", choice)

	// Resume the Sidecar timer.
	if err := client.ResumeTimer(ctx); err != nil {
		return fmt.Errorf("hitl-sort: resume timer: %w", err)
	}

	// Optionally stamp the governed artefact.
	if cfg.Stamp {
		if _, err := client.StampArtefact(ctx, governedArtefact, stampName); err != nil {
			return fmt.Errorf("hitl-sort: stamp %s/%s: %w", governedArtefact, stampName, err)
		}
	}

	// Route to the chosen output.
	if _, err := client.RouteToOutput(ctx, choice); err != nil {
		return fmt.Errorf("hitl-sort: route to output %q: %w", choice, err)
	}
	return nil
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
