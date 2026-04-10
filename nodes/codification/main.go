// Codification is a fan-out orchestrator for formal law representations in
// the Foundry Judiciary Clerk cycle.
//
// It sits between Forge/Refine and Sort. The node reads the petition artefact,
// iterates its changes, and fans out to codify-* nodes per-change for every
// change that requires formal representations. Retire changes are skipped
// (retirement is purely administrative). After all children complete, the
// node maps codification results back to originating changes, stores the
// updated petition, and routes to Sort.
//
// The Codification node is pure orchestration. It has no LLM, no agent, and
// no domain logic beyond petition parsing and child result mapping.
//
// Input artefacts:
//
//   - "petition" (configurable) -- JSON petition with changes[]
//
// Output artefacts:
//
//   - "petition" (configurable) -- updated petition with representations
//     attached to qualifying changes
//
// Configuration (YAML via NODE_CONFIG_PATH):
//
//	petitionArtefact: "petition"
//	codificationNodes:
//	  - codify-smt
//	defaultOutput: "default"
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// Well-known constants
// ---------------------------------------------------------------------------

const (
	// Default artefact and output names.
	defaultPetitionArtefact = "petition"
	defaultOutput           = "default"

	// Artefact IDs matching the codify-* child contract.
	artefactCodificationGoal   = "codification-goal"
	artefactCodificationResult = "codification-result"

	// GovernedArtefact names for child workitem artefacts.
	governedCodificationGoal   = "codification-goal"
	governedCodificationResult = "codification-result"

	// Petition change actions.
	actionCreate = "create"
	actionUpdate = "update"
	actionDemote = "demote"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// codificationConfig holds the node's runtime configuration.
type codificationConfig struct {
	// PetitionArtefact is the artefact ID to read and update.
	// Default: "petition".
	PetitionArtefact string `yaml:"petitionArtefact"`

	// CodificationNodes lists the codify-* node names to fan out to.
	// Each qualifying petition change is sent to every listed node.
	CodificationNodes []string `yaml:"codificationNodes"`

	// DefaultOutput is the output name to route to after codification.
	// Default: "default".
	DefaultOutput string `yaml:"defaultOutput"`
}

// petitionArtefact returns the effective petition artefact ID.
func (c *codificationConfig) petitionArtefact() string {
	if c.PetitionArtefact != "" {
		return c.PetitionArtefact
	}
	return defaultPetitionArtefact
}

// defaultOutputName returns the effective default output name.
func (c *codificationConfig) defaultOutputName() string {
	if c.DefaultOutput != "" {
		return c.DefaultOutput
	}
	return defaultOutput
}

// ---------------------------------------------------------------------------
// Petition Types (same shape as law-applicator)
// ---------------------------------------------------------------------------

type petition struct {
	Petition petitionBody `json:"petition"`
}

type petitionBody struct {
	Context            petitionContext  `json:"context"`
	Changes            []petitionChange `json:"changes"`
	ProseJustification string           `json:"prose_justification"`
}

type petitionContext struct {
	Trigger         string `json:"trigger"`
	VerdictDecision string `json:"verdict_decision"` //nolint:tagliatelle // Matches petition schema
	Justification   string `json:"justification"`
}

type petitionChange struct {
	Action          string        `json:"action"`
	Tier            int32         `json:"tier,omitempty"`
	Goal            string        `json:"goal,omitempty"`
	AppliesTo       []string      `json:"applies_to,omitempty"`
	LawID           string        `json:"law_id,omitempty"`
	FromTier        int32         `json:"from_tier,omitempty"`
	ToTier          int32         `json:"to_tier,omitempty"`
	Justification   string        `json:"justification,omitempty"`
	Representations []petitionRep `json:"representations,omitempty"`
}

type petitionRep struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// needsCodification returns true if the change action requires formal
// representations (create, update, demote). Retire is purely administrative.
func (c *petitionChange) needsCodification() bool {
	switch c.Action {
	case actionCreate, actionUpdate, actionDemote:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Codification Goal / Result (matching codify-* child contract)
// ---------------------------------------------------------------------------

// codificationGoal is stored on each child workitem as the
// "codification-goal" artefact. Matches the structure codify-smt expects.
type codificationGoal struct {
	Goal      string   `json:"goal"`
	AppliesTo []string `json:"applies_to"`
	Tier      int32    `json:"tier"`
	Action    string   `json:"action"`
}

// codificationResult is produced by codify-* children as the
// "codification-result" artefact.
type codificationResult struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// ---------------------------------------------------------------------------
// Entry Point
// ---------------------------------------------------------------------------

func main() {
	slog.Info("codification: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("codification: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("codification: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("codification: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	cfg, err := nodeconfig.Load[codificationConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("codification: load config: %w", err)
	}

	return handleCodification(ctx, client, cfg)
}

// ---------------------------------------------------------------------------
// Core Logic
// ---------------------------------------------------------------------------

//nolint:cyclop // Orchestration function — sequential phases are inherently complex.
func handleCodification(ctx context.Context, client *flow.Client, cfg *codificationConfig) error {
	_, _ = client.Heartbeat(ctx)

	// -- Step 1: Read petition artefact ----------------------------------
	petResp, err := client.GetArtefact(ctx, cfg.petitionArtefact())
	if err != nil {
		return fmt.Errorf("codification: get petition: %w", err)
	}

	var pet petition
	if err := json.Unmarshal(petResp.GetContent(), &pet); err != nil {
		return fmt.Errorf("codification: parse petition: %w", err)
	}

	slog.Info("codification: read petition",
		"changes", len(pet.Petition.Changes),
	)

	// -- Step 2: Partition changes into qualifying and skip ---------------
	var qualifying []indexedChange
	for i := range pet.Petition.Changes {
		if pet.Petition.Changes[i].needsCodification() {
			qualifying = append(qualifying, indexedChange{
				originalIndex: i,
				change:        &pet.Petition.Changes[i],
			})
		}
	}

	slog.Info("codification: partitioned changes",
		"qualifying", len(qualifying),
		"skipped", len(pet.Petition.Changes)-len(qualifying),
	)

	// -- Step 3: If no changes need codification, skip fan-out -----------
	if len(qualifying) == 0 || len(cfg.CodificationNodes) == 0 {
		slog.Info("codification: no fan-out needed, storing petition as-is")
		return storePetitionAndRoute(ctx, client, cfg, &pet)
	}

	// -- Step 4: Build FanOutTasks (change x codifier) -------------------
	tasks, err := buildFanOutTasks(qualifying, cfg.CodificationNodes)
	if err != nil {
		return err
	}

	slog.Info("codification: fan-out",
		"tasks", len(tasks),
		"qualifying_changes", len(qualifying),
		"codifiers", len(cfg.CodificationNodes),
	)

	// -- Step 5: FanOut --------------------------------------------------
	_, err = client.FanOut(ctx, tasks)
	if err != nil {
		return fmt.Errorf("codification: fan-out: %w", err)
	}

	// -- Step 6: AwaitChildren -------------------------------------------
	statuses, err := client.AwaitChildren(ctx)
	if err != nil {
		return fmt.Errorf("codification: await children: %w", err)
	}

	// -- Step 7: CollectArtefacts ----------------------------------------
	results, err := client.CollectArtefacts(ctx, statuses, artefactCodificationResult)
	if err != nil {
		return fmt.Errorf("codification: collect artefacts: %w", err)
	}

	// -- Step 8: Map results back to originating changes -----------------
	numCodifiers := len(cfg.CodificationNodes)
	for i, result := range results {
		raw := result.Artefacts[artefactCodificationResult]
		if raw == nil {
			slog.Warn("codification: child returned no codification-result",
				"child_index", i,
				"workitem_id", result.Status.WorkitemID,
			)
			continue
		}

		var cr codificationResult
		if jsonErr := json.Unmarshal(raw, &cr); jsonErr != nil {
			return fmt.Errorf(
				"codification: parse codification-result from child %s: %w",
				result.Status.WorkitemID, jsonErr)
		}

		// Map task index back to qualifying change index.
		// Tasks are ordered: for each qualifying change, one task per codifier.
		// changeIdx = taskIdx / numCodifiers
		changeIdx := i / numCodifiers
		if changeIdx >= len(qualifying) {
			return fmt.Errorf("codification: child %d maps to out-of-range change index %d", i, changeIdx)
		}

		qualifying[changeIdx].change.Representations = append(
			qualifying[changeIdx].change.Representations,
			petitionRep(cr),
		)
	}

	slog.Info("codification: representations attached")

	// -- Step 9: Store updated petition and route ------------------------
	return storePetitionAndRoute(ctx, client, cfg, &pet)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// indexedChange pairs a petition change with its original index in the
// Changes slice, so results can be mapped back after fan-out.
type indexedChange struct {
	originalIndex int
	change        *petitionChange
}

// buildFanOutTasks creates one FanOutTask per qualifying change per codifier.
// Order: for change 0 → [codifier0, codifier1, ...], for change 1 → [...].
func buildFanOutTasks(
	qualifying []indexedChange,
	codifiers []string,
) ([]flow.FanOutTask, error) {
	tasks := make([]flow.FanOutTask, 0, len(qualifying)*len(codifiers))

	for _, ic := range qualifying {
		goal := codificationGoal{
			Goal:      ic.change.Goal,
			AppliesTo: ic.change.AppliesTo,
			Tier:      ic.change.Tier,
			Action:    ic.change.Action,
		}

		goalJSON, err := json.Marshal(goal)
		if err != nil {
			return nil, fmt.Errorf("codification: marshal goal for change %d: %w", ic.originalIndex, err)
		}

		for _, codifier := range codifiers {
			tasks = append(tasks, flow.FanOutTask{
				TargetNode: codifier,
				Artefacts: []flow.ChildArtefact{
					{
						ID:               artefactCodificationGoal,
						GovernedArtefact: governedCodificationGoal,
						Content:          goalJSON,
					},
				},
			})
		}
	}

	return tasks, nil
}

// storePetitionAndRoute marshals the petition, stores it, and routes to the
// default output.
func storePetitionAndRoute(
	ctx context.Context,
	client *flow.Client,
	cfg *codificationConfig,
	pet *petition,
) error {
	petJSON, err := json.Marshal(pet)
	if err != nil {
		return fmt.Errorf("codification: marshal petition: %w", err)
	}

	if _, err := client.StoreArtefact(ctx, cfg.petitionArtefact(), "", petJSON); err != nil {
		return fmt.Errorf("codification: store petition: %w", err)
	}

	if _, err := client.RouteToOutput(ctx, cfg.defaultOutputName()); err != nil {
		return fmt.Errorf("codification: route to output: %w", err)
	}

	slog.Info("codification: done, routed to output",
		"output", cfg.defaultOutputName(),
	)
	return nil
}
