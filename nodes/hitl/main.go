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
	"maps"
	"net/http"
	"os"
	"strings"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/nodes/internal/nodeconfig"
	"github.com/foundry/flow/nodes/internal/nodeutil"
	flow "github.com/foundry/flow/sdk/go"
)

// choiceCancel is the reserved choice value for the cancel action.
const choiceCancel = "cancel"

// choiceType values classify a choice for the Dashboard. The type field is
// part of the shared nodeutil.ChoiceEntry wire shape.
const (
	choiceTypeRoute  = "route"
	choiceTypeCancel = "cancel"
)

// hitlConfig holds the node's configuration, loaded from a
// ConfigMap-mounted YAML file via nodeconfig.Load.
type hitlConfig struct {
	// ChoiceLabels maps output names to human-friendly display labels.
	// If an output has no entry, the output name is used as-is.
	ChoiceLabels map[string]string `yaml:"choiceLabels"`

	// Choices optionally restricts the presented routing choices to exactly
	// the listed outputs (in the given order). When empty, all topology
	// outputs are presented (current behavior).
	Choices []choiceEntry `yaml:"choices"`
}

// choiceEntry maps a topology output name to a human-friendly display label.
type choiceEntry struct {
	Output string `yaml:"output"`
	Label  string `yaml:"label"`
}

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

func main() {
	slog.Info("hitl: starting")

	cfg, err := nodeconfig.Load[hitlConfig](nodeconfig.Path())
	if err != nil {
		slog.Error("hitl: load config failed", "error", err)
		os.Exit(1)
	}

	if err := nodeutil.RunHITLNode("hitl", func(qm flow.QueueManager) flow.Handler {
		return handler(qm, cfg)
	}, flow.WithCustomRoutes(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /choices", handleChoices(cfg))
	})); err != nil {
		slog.Error("hitl: server failed", "error", err)
		os.Exit(1)
	}
}

// handler returns a flow.Handler that parks the Workitem in the HITL queue,
// waits for a human decision, and routes or cancels accordingly.
func handler(qm flow.QueueManager, cfg *hitlConfig) flow.Handler {
	return nodeutil.NewHITLHandler(qm, "hitl", func(
		ctx context.Context,
		client *flow.Client,
		workitem *flow.Workitem,
		qm flow.QueueManager,
		wctx *flowv1.WorkitemContext,
	) error {
		return handleHITL(ctx, client, workitem, qm, cfg, wctx)
	})
}

// handleHITL is the core handler logic, extracted for testability.
func handleHITL(
	ctx context.Context,
	client *flow.Client,
	workitem *flow.Workitem,
	qm flow.QueueManager,
	cfg *hitlConfig,
	wctx *flowv1.WorkitemContext,
) error {
	workitemID := wctx.GetWorkitemId()

	// ── Step 1: Discover behaviour from topology ────────────────────
	topology, err := workitem.GetTopology()
	if err != nil {
		return fmt.Errorf("hitl: get flow topology: %w", err)
	}

	// ponytail: GetTopology returns *flow.Flow which wraps the proto.
	// The Flow type does not expose GetSelf() outputs directly.
	// For now we use RawOperator escape hatch to access the raw
	// proto for output discovery. This ceiling should be removed when
	// Flow exposes output methods.
	rawTopology, rawErr := client.RawOperator().GetFlowTopology(ctx, &flowv1.GetFlowTopologyRequest{})
	if rawErr != nil {
		return fmt.Errorf("hitl: get raw topology: %w", err)
	}

	behaviour, err := deriveBehaviour(topology, rawTopology, cfg)
	if err != nil {
		return err
	}

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
		if _, err := workitem.GetArtefact(artefactKind); err != nil {
			return fmt.Errorf("hitl: read artefact %s: %w", artefactKind, err)
		}
	}

	// ── Steps 3-6: Enqueue, pause, wait, validate, resume ───────────
	choice, err := nodeutil.AwaitHumanDecision(ctx, qm, workitem, workitemID, "hitl", behaviour.validChoices)
	if err != nil {
		return err
	}

	// ── Step 7: Dispatch ────────────────────────────────────────────
	if choice == choiceCancel {
		return nodeutil.CompleteCancelled(workitem, "hitl")
	}

	return nodeutil.StampAndRoute(workitem, behaviour.stamps, "hitl", choice)
}

// ---------------------------------------------------------------------------
// Behaviour derivation
// ---------------------------------------------------------------------------

// deriveBehaviour computes the complete runtime behaviour from the flow
// topology and node config. This is a pure function with no side effects.
//
// ponytail: Accepts both *flow.Flow (new domain) and the raw proto response
// for output discovery (not yet exposed on *flow.Flow). Remove the rawTopology
// parameter when Flow exposes GetSelf().GetOutputs().
func deriveBehaviour(
	topology *flow.Flow,
	rawTopology *flowv1.GetFlowTopologyResponse,
	cfg *hitlConfig,
) (*derivedBehaviour, error) {
	self := rawTopology.GetSelf()
	capabilities := self.GetCapabilities()

	b := &derivedBehaviour{
		readArtefacts: parseReadArtefacts(capabilities),
		stamps:        flow.ParseStampCapabilities(capabilities),
		hasFeedback:   hasWriteFeedback(capabilities),
		hasCancel:     len(topology.GetExitContract()) > 0,
	}

	// Build the available output set from topology.
	available := make(map[string]bool, len(self.GetOutputs()))
	for _, out := range self.GetOutputs() {
		available[out.GetName()] = true
	}

	// Build output choices. A configured choices list restricts the set to
	// exactly the listed outputs (config order).
	if cfg != nil && len(cfg.Choices) > 0 {
		for _, ce := range cfg.Choices {
			if !available[ce.Output] {
				return nil, fmt.Errorf("hitl: configured choice %q is not a valid output in topology", ce.Output)
			}
			b.outputChoices = append(b.outputChoices, ce.Output)
		}
	} else {
		for _, out := range self.GetOutputs() {
			b.outputChoices = append(b.outputChoices, out.GetName())
		}
	}

	// Build valid choice set.
	b.validChoices = make(map[string]bool, len(b.outputChoices)+1)
	for _, name := range b.outputChoices {
		b.validChoices[name] = true
	}
	if b.hasCancel {
		b.validChoices[choiceCancel] = true
	}

	return b, nil
}

// buildChoicesResponse constructs the GET /choices JSON response from derived
// behaviour and config labels.
func buildChoicesResponse(
	topology *flow.Flow,
	rawTopology *flowv1.GetFlowTopologyResponse,
	cfg *hitlConfig,
) (*nodeutil.ChoicesResponse, error) {
	behaviour, err := deriveBehaviour(topology, rawTopology, cfg)
	if err != nil {
		return nil, err
	}
	labels := map[string]string{}
	maps.Copy(labels, cfg.ChoiceLabels)
	// choices[].label takes precedence over choiceLabels when present.
	for _, ce := range cfg.Choices {
		if ce.Label != "" {
			labels[ce.Output] = ce.Label
		}
	}

	resp := &nodeutil.ChoicesResponse{
		HasFeedback: behaviour.hasFeedback,
		HasCancel:   behaviour.hasCancel,
	}

	// Route choices (one per output).
	for _, name := range behaviour.outputChoices {
		label := labels[name]
		if label == "" {
			label = name
		}
		resp.Choices = append(resp.Choices, nodeutil.ChoiceEntry{
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
		resp.Choices = append(resp.Choices, nodeutil.ChoiceEntry{
			Value: choiceCancel,
			Label: label,
			Type:  choiceTypeCancel,
		})
	}

	return resp, nil
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

		// ponytail: GetFlowTopology still takes ctx — the new domain objects
		// do not yet expose output lists. This HTTP handler uses the request
		// context (not the workitem handler context), which is appropriate.
		topology, err := client.GetFlow()
		if err != nil {
			http.Error(w, "hitl: get topology failed", http.StatusServiceUnavailable)
			return
		}
		rawTopology, err := client.RawOperator().GetFlowTopology(r.Context(), &flowv1.GetFlowTopologyRequest{})
		if err != nil {
			http.Error(w, "hitl: get topology failed", http.StatusServiceUnavailable)
			return
		}

		resp, err := buildChoicesResponse(topology, rawTopology, cfg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

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
