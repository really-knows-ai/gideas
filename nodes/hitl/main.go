// HITL is a generic, config-driven human-in-the-loop node for the Foundry Cycle.
//
// A single image (hitl:latest) derives its behaviour entirely from the
// FoundryNode CRD configuration. Multiple CRD instances with different
// outputs, capabilities, and exit bindings produce different HITL
// experiences without code changes.
//
// Behaviour mapping:
//
//   - spec.outputs become human action choices (route on decision).
//   - STAMP:artefact/<kind>/<stamp> capability triggers stamping on decision.
//   - READ:artefact/<kind> capabilities determine which artefacts are read
//     for context before enqueueing.
//   - WRITE:feedback/* capabilities signal to the Dashboard that the
//     feedback UI should be shown (the node itself does not write feedback).
//   - spec.exit (non-empty) enables a "cancel" action that calls
//     Complete(WithReason(COMPLETION_REASON_CANCELLED)).
//
// The node:
//  1. Discovers outputs, capabilities, and exit binding from the flow topology.
//  2. Reads artefacts identified by READ:artefact/<kind> capabilities.
//  3. Enqueues the Workitem and pauses the Sidecar's inactivity timer.
//  4. Blocks until the human decides via POST /queue/{id}/decide.
//  5. Validates the choice against the derived valid set.
//  6. Resumes the timer, optionally stamps, and routes or cancels.
//
// The GET /choices endpoint serves the derived choice list so the Dashboard
// can build the appropriate UI.
//
// Configuration (YAML via NODE_CONFIG_PATH, default /etc/foundry/node-config.yaml):
//
//	choiceLabels:
//	  approved: "Approve Petition"
//	  resolution: "Submit Resolution"
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
)

// choiceCancel is the reserved choice value for the cancel action.
const choiceCancel = "cancel"

// hitlConfig holds the node's configuration, loaded from a
// ConfigMap-mounted YAML file via nodeconfig.Load.
type hitlConfig struct {
	// ChoiceLabels maps output names to human-friendly display labels.
	// If an output has no entry, the output name is used as-is.
	ChoiceLabels map[string]string `yaml:"choiceLabels"`
}

// choiceType classifies a choice for the Dashboard.
type choiceType string

const (
	choiceTypeRoute  choiceType = "route"
	choiceTypeCancel choiceType = "cancel"
)

// derivedBehaviour holds everything the node needs at runtime, computed
// once from topology and config during handler startup.
type derivedBehaviour struct {
	// readArtefacts are the artefact kinds to read (from READ:artefact/<kind>).
	readArtefacts []string

	// stamps are the STAMP capabilities (from STAMP:artefact/<kind>/<stamp>).
	stamps []flow.StampCapability

	// outputChoices are the valid route choices (from topology outputs).
	outputChoices []string

	// hasCancel is true when the node is exit-bound (spec.exit is set).
	hasCancel bool

	// hasFeedback is true when any WRITE:feedback/* capability is present.
	hasFeedback bool

	// validChoices maps every valid choice value (output names + "cancel") to true.
	validChoices map[string]bool
}

// choiceEntry is a single entry in the GET /choices response.
type choiceEntry struct {
	Value string     `json:"value"`
	Label string     `json:"label"`
	Type  choiceType `json:"type"`
}

// choicesResponse is the JSON body returned by GET /choices.
type choicesResponse struct {
	Choices     []choiceEntry `json:"choices"`
	HasFeedback bool          `json:"hasFeedback"`
	HasCancel   bool          `json:"hasCancel"`
}

func main() {
	slog.Info("hitl: starting")

	cfg, err := nodeconfig.Load[hitlConfig](nodeconfig.Path())
	if err != nil {
		slog.Error("hitl: load config failed", "error", err)
		os.Exit(1)
	}

	qm, err := flow.NewQueueManager(
		flow.WithCustomRoutes(func(mux *http.ServeMux) {
			mux.HandleFunc("GET /choices", handleChoices(cfg))
		}),
	)
	if err != nil {
		slog.Error("hitl: create queue manager failed", "error", err)
		os.Exit(1)
	}

	if err := flow.Start(handler(qm), flow.WithQueueManager(qm)); err != nil {
		slog.Error("hitl: server failed", "error", err)
		os.Exit(1)
	}
}

// handler returns a flow.Handler that parks the Workitem in the HITL queue,
// waits for a human decision, and routes or cancels accordingly.
func handler(qm flow.QueueManager) flow.Handler {
	return func(ctx context.Context, wctx *flowv1.WorkitemContext) error {
		_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())

		client, err := flow.NewClient()
		if err != nil {
			return fmt.Errorf("hitl: create client: %w", err)
		}
		defer func() { _ = client.Close() }()

		return handleHITL(ctx, client, qm, wctx)
	}
}

// handleHITL is the core handler logic, extracted for testability.
func handleHITL(
	ctx context.Context,
	client *flow.Client,
	qm flow.QueueManager,
	wctx *flowv1.WorkitemContext,
) error {
	workitemID := wctx.GetWorkitemId()

	// ── Step 1: Discover behaviour from topology ────────────────────
	topology, err := client.GetFlowTopology(ctx)
	if err != nil {
		return fmt.Errorf("hitl: get flow topology: %w", err)
	}

	behaviour := deriveBehaviour(topology)

	slog.Info("hitl: handling workitem",
		"workitem_id", workitemID,
		"outputs", behaviour.outputChoices,
		"stamps", len(behaviour.stamps),
		"has_cancel", behaviour.hasCancel,
		"has_feedback", behaviour.hasFeedback,
		"read_artefacts", behaviour.readArtefacts,
	)

	// ── Step 2: Read artefacts for context ──────────────────────────
	for _, artefactKind := range behaviour.readArtefacts {
		if _, err := client.GetArtefact(ctx, artefactKind); err != nil {
			return fmt.Errorf("hitl: read artefact %s: %w", artefactKind, err)
		}
	}

	// ── Step 3: Enqueue and pause ───────────────────────────────────
	if err := qm.Enqueue(ctx, workitemID); err != nil {
		return fmt.Errorf("hitl: enqueue: %w", err)
	}

	if err := client.PauseTimer(ctx); err != nil {
		return fmt.Errorf("hitl: pause timer: %w", err)
	}

	// ── Step 4: Wait for human decision ─────────────────────────────
	slog.Info("hitl: awaiting human decision", "workitem_id", workitemID)
	choice, err := qm.WaitForDecision(ctx, workitemID)
	if err != nil {
		return fmt.Errorf("hitl: wait for decision: %w", err)
	}

	// Empty choice indicates QueueManager shutdown (not a human decision).
	if choice == "" {
		return fmt.Errorf("hitl: received empty choice (queue manager shut down before decision)")
	}

	// ── Step 5: Validate choice ─────────────────────────────────────
	if !behaviour.validChoices[choice] {
		return fmt.Errorf("hitl: invalid choice %q: not in valid set", choice)
	}

	slog.Info("hitl: human decision received", "workitem_id", workitemID, "choice", choice)

	// ── Step 6: Resume timer ────────────────────────────────────────
	if err := client.ResumeTimer(ctx); err != nil {
		return fmt.Errorf("hitl: resume timer: %w", err)
	}

	// ── Step 7: Dispatch ────────────────────────────────────────────
	if choice == choiceCancel {
		return completeWithCancelled(ctx, client)
	}

	return stampAndRoute(ctx, client, behaviour, choice)
}

// completeWithCancelled calls Complete with COMPLETION_REASON_CANCELLED.
func completeWithCancelled(ctx context.Context, client *flow.Client) error {
	if _, err := client.Complete(ctx, flow.WithReason(flowv1.CompletionReason_COMPLETION_REASON_CANCELLED)); err != nil {
		return fmt.Errorf("hitl: complete (cancelled): %w", err)
	}
	return nil
}

// stampAndRoute applies any stamp capabilities and routes to the chosen output.
func stampAndRoute(
	ctx context.Context,
	client *flow.Client,
	behaviour *derivedBehaviour,
	choice string,
) error {
	// Stamp all governed artefacts.
	for _, sc := range behaviour.stamps {
		if _, err := client.StampArtefact(ctx, sc.GovernedArtefact, sc.StampName); err != nil {
			return fmt.Errorf("hitl: stamp %s/%s: %w", sc.GovernedArtefact, sc.StampName, err)
		}
	}

	// Route to the chosen output.
	if _, err := client.RouteToOutput(ctx, choice); err != nil {
		return fmt.Errorf("hitl: route to output %q: %w", choice, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Behaviour derivation
// ---------------------------------------------------------------------------

// deriveBehaviour computes the complete runtime behaviour from the flow
// topology and node config. This is a pure function with no side effects.
func deriveBehaviour(
	topology *flowv1.GetFlowTopologyResponse,
) *derivedBehaviour {
	self := topology.GetSelf()
	capabilities := self.GetCapabilities()

	b := &derivedBehaviour{
		readArtefacts: parseReadArtefacts(capabilities),
		stamps:        flow.ParseStampCapabilities(capabilities),
		hasFeedback:   hasWriteFeedback(capabilities),
		hasCancel:     len(topology.GetExitContract()) > 0,
	}

	// Build output choices from topology outputs.
	for _, out := range self.GetOutputs() {
		b.outputChoices = append(b.outputChoices, out.GetName())
	}

	// Build valid choice set.
	b.validChoices = make(map[string]bool, len(b.outputChoices)+1)
	for _, name := range b.outputChoices {
		b.validChoices[name] = true
	}
	if b.hasCancel {
		b.validChoices[choiceCancel] = true
	}

	return b
}

// buildChoicesResponse constructs the GET /choices JSON response from derived
// behaviour and config labels.
func buildChoicesResponse(
	topology *flowv1.GetFlowTopologyResponse,
	cfg *hitlConfig,
) *choicesResponse {
	behaviour := deriveBehaviour(topology)
	labels := cfg.ChoiceLabels
	if labels == nil {
		labels = map[string]string{}
	}

	resp := &choicesResponse{
		HasFeedback: behaviour.hasFeedback,
		HasCancel:   behaviour.hasCancel,
	}

	// Route choices (one per output).
	for _, name := range behaviour.outputChoices {
		label := labels[name]
		if label == "" {
			label = name
		}
		resp.Choices = append(resp.Choices, choiceEntry{
			Value: name,
			Label: label,
			Type:  choiceTypeRoute,
		})
	}

	// Cancel choice (if exit-bound).
	if behaviour.hasCancel {
		label := labels[choiceCancel]
		if label == "" {
			label = "Cancel"
		}
		resp.Choices = append(resp.Choices, choiceEntry{
			Value: choiceCancel,
			Label: label,
			Type:  choiceTypeCancel,
		})
	}

	return resp
}

// ---------------------------------------------------------------------------
// GET /choices endpoint
// ---------------------------------------------------------------------------

// handleChoices returns an HTTP handler that serves the derived choice list.
// On first invocation the handler calls GetFlowTopology to discover the
// node's configuration. The result is cached for subsequent requests.
//
// If the topology cannot be loaded, the endpoint returns 503 Service
// Unavailable so the Dashboard knows to retry.
func handleChoices(cfg *hitlConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Create a temporary client to fetch topology.
		client, err := flow.NewClient()
		if err != nil {
			http.Error(w, "hitl: create client failed", http.StatusServiceUnavailable)
			return
		}
		defer func() { _ = client.Close() }()

		topology, err := client.GetFlowTopology(r.Context())
		if err != nil {
			http.Error(w, "hitl: get topology failed", http.StatusServiceUnavailable)
			return
		}

		resp := buildChoicesResponse(topology, cfg)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// ---------------------------------------------------------------------------
// Capability parsing helpers
// ---------------------------------------------------------------------------

const readArtefactPrefix = "READ:artefact/"

// parseReadArtefacts extracts artefact kinds from READ:artefact/<kind>
// capabilities. Bare "READ:artefact" (no qualifier) is skipped.
func parseReadArtefacts(capabilities []string) []string {
	var kinds []string
	for _, cap := range capabilities {
		if !strings.HasPrefix(cap, readArtefactPrefix) {
			continue
		}
		kind := cap[len(readArtefactPrefix):]
		if kind != "" {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

const writeFeedbackPrefix = "WRITE:feedback"

// hasWriteFeedback returns true if any WRITE:feedback capability (with or
// without qualifier) is present in the capability list.
func hasWriteFeedback(capabilities []string) bool {
	for _, cap := range capabilities {
		if strings.HasPrefix(cap, writeFeedbackPrefix) {
			return true
		}
	}
	return false
}
