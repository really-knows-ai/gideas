// Clerk is the petition-drafting node of the Foundry Judiciary.
//
// The Clerk receives a verdict and context artefacts (from an Arbiter
// consensus, HITL decision, or Tribunal hearing) and drafts a structured
// petition artefact describing the proposed law changes. It then fans out
// to Codification nodes for formal representations, collects the results,
// assembles the final petition, and routes to the Tribunal for review.
//
// On revision (feedback from Tribunal via Judiciary Gate), the Clerk reads
// the existing petition and feedback, revises the petition via the agent,
// re-fans-out to codification, and re-routes to Tribunal.
//
// The petition artefact is YAML/Markdown — human-readable for HITL reviewers.
// Its structure is defined in PLAN.md (Petition Artefact section).
//
// Prose drafting logic is ported from the deleted platform/clerk/internal/
// service/clerk_server.go.
//
// Configuration (YAML via NODE_CONFIG_PATH):
//
//	codificationNodes:          # list of codification node names to fan out to
//	  - codify-smt
//	  - codify-rego
//	codificationArtefactPrefix: "codification"  # prefix for codification result artefacts
//	defaultOutput:              "default"        # output name for routing to Tribunal
//
// Usage:
//
//	go run ./nodes/clerk/main.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/template"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// Well-known artefact IDs
// ---------------------------------------------------------------------------

const (
	// Input artefacts (written by upstream nodes: Arbiter, Tribunal, Advocate).
	artefactDeliberationResult = "deliberation-result"
	artefactVerdictContext     = "verdict-context"

	// The petition artefact: drafted by Clerk, reviewed by Tribunal.
	artefactPetition = "petition"

	// Feedback artefact ID (Tribunal adds feedback on this artefact).
	artefactPetitionFeedback = "petition"

	// Codification input artefact: written on child Workitems before fan-out.
	artefactCodificationGoal = "codification-goal"

	// Codification output artefact: produced by codification nodes.
	artefactCodificationResult = "codification-result"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// clerkConfig holds the Clerk's runtime configuration.
type clerkConfig struct {
	// CodificationNodes lists the codification node names to fan out to.
	// Each receives a child Workitem with the law goal as artefact and
	// produces a formal representation.
	CodificationNodes []string `yaml:"codificationNodes"`

	// CodificationArtefactPrefix is used as a prefix when storing
	// collected codification results. Default: "codification".
	CodificationArtefactPrefix string `yaml:"codificationArtefactPrefix"`

	// DefaultOutput is the output name for routing to Tribunal after
	// petition assembly. Default: "default".
	DefaultOutput string `yaml:"defaultOutput"`
}

func (c *clerkConfig) codificationPrefix() string {
	if c.CodificationArtefactPrefix == "" {
		return "codification"
	}
	return c.CodificationArtefactPrefix
}

func (c *clerkConfig) defaultOutput() string {
	if c.DefaultOutput == "" {
		return "default"
	}
	return c.DefaultOutput
}

// ---------------------------------------------------------------------------
// Deliberation Result (read from upstream)
// ---------------------------------------------------------------------------

// deliberationResult mirrors the JSON structure stored by Deliberation Gate.
type deliberationResult struct {
	Outcome        string               `json:"outcome"`
	Justifications []jurorJustification `json:"justifications"`
	RoundsUsed     int32                `json:"rounds_used"`
	Hung           bool                 `json:"hung"`
}

type jurorJustification struct {
	JurorID   string `json:"juror_id"`
	Outcome   string `json:"outcome"`
	Reasoning string `json:"reasoning"`
}

// ---------------------------------------------------------------------------
// Verdict Context (read from upstream)
// ---------------------------------------------------------------------------

// verdictContext carries the context that produced the verdict. Set by the
// upstream node (Arbiter, Tribunal, or Advocate) to provide the Clerk with
// the information needed to draft a petition.
type verdictContext struct {
	Trigger        string   `json:"trigger"`         // "deadlock-resolution", "friction-hearing", "ttl-hearing"
	SourceWorkitem string   `json:"source_workitem"` //nolint:tagliatelle // JSON convention
	Goal           string   `json:"goal"`
	AppliesTo      []string `json:"applies_to"`
	Tier           int32    `json:"tier"`
	LawID          string   `json:"law_id"` // for retire/demote operations
	Action         string   `json:"action"` // "create", "retire", "demote"
}

// ---------------------------------------------------------------------------
// Petition (the output artefact)
// ---------------------------------------------------------------------------

// petition is the YAML/Markdown structure stored as the "petition" artefact.
type petition struct {
	Petition petitionBody `json:"petition"`
}

type petitionBody struct {
	Context            petitionContext  `json:"context"`
	Changes            []petitionChange `json:"changes"`
	ProseJustification string           `json:"prose_justification"`
}

type petitionContext struct {
	Trigger        string `json:"trigger"`
	SourceWorkitem string `json:"source_workitem"` //nolint:tagliatelle
	Verdict        string `json:"verdict"`
	Justification  string `json:"justification"`
}

type petitionChange struct {
	Action          string        `json:"action"`
	Tier            int32         `json:"tier,omitempty"`
	Goal            string        `json:"goal,omitempty"`
	AppliesTo       []string      `json:"applies_to,omitempty"`
	LawID           string        `json:"law_id,omitempty"`
	Justification   string        `json:"justification,omitempty"`
	Representations []petitionRep `json:"representations,omitempty"`
}

type petitionRep struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// ---------------------------------------------------------------------------
// Query Template
// ---------------------------------------------------------------------------

// queryTemplateText is the prompt template for the Clerk's FoundryAgent.
// It receives the deliberation result and verdict context, and produces
// the prose justification for the petition.
//
//nolint:lll // Template readability favors keeping it intact.
const queryTemplateText = `You are a legislative clerk drafting a formal petition for a governance system.

Based on the following deliberation result and context, draft a clear, concise prose justification
for the proposed law change. The justification should summarize the jury's reasoning and explain
why the proposed action is warranted.

Deliberation Result:
- Outcome: {{.Outcome}}
- Rounds Used: {{.RoundsUsed}}
- Hung: {{.Hung}}
{{- range .Justifications}}
- Juror {{.JurorID}} voted "{{.Outcome}}": {{.Reasoning}}
{{- end}}

Context:
- Trigger: {{.Trigger}}
- Proposed Action: {{.Action}}
- Goal: {{.Goal}}
{{- if .LawID}}
- Target Law ID: {{.LawID}}
{{- end}}
{{- if .ExistingFeedback}}

Previous Tribunal Feedback (revision requested):
{{.ExistingFeedback}}

Address the feedback above in your revised justification.
{{- end}}

You MUST respond with a JSON object containing exactly one key:
- "prose_justification": your drafted prose justification for the petition

Output ONLY the JSON object, nothing else.`

// queryData is the template data for the clerk agent prompt.
type queryData struct {
	Outcome          string
	RoundsUsed       int32
	Hung             bool
	Justifications   []jurorJustification
	Trigger          string
	Action           string
	Goal             string
	LawID            string
	ExistingFeedback string
}

// ---------------------------------------------------------------------------
// Entry Point
// ---------------------------------------------------------------------------

func main() {
	slog.Info("clerk: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("clerk: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("clerk: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("clerk: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	cfg, err := nodeconfig.Load[clerkConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("clerk: load config: %w", err)
	}

	return handleClerk(ctx, client, cfg)
}

// ---------------------------------------------------------------------------
// Core Logic
// ---------------------------------------------------------------------------

// handleClerk contains the Clerk's core logic, separated from handler
// boilerplate for testability.
func handleClerk(ctx context.Context, client *flow.Client, cfg *clerkConfig) error {
	_, _ = client.Heartbeat(ctx)

	// ── Step 1: Read deliberation result ────────────────────────────
	resultResp, err := client.GetArtefact(ctx, artefactDeliberationResult)
	if err != nil {
		return fmt.Errorf("clerk: get deliberation-result: %w", err)
	}

	var deliberation deliberationResult
	if err := json.Unmarshal(resultResp.GetContent(), &deliberation); err != nil {
		return fmt.Errorf("clerk: parse deliberation-result: %w", err)
	}

	// ── Step 2: Read verdict context ────────────────────────────────
	ctxResp, err := client.GetArtefact(ctx, artefactVerdictContext)
	if err != nil {
		return fmt.Errorf("clerk: get verdict-context: %w", err)
	}

	var vctx verdictContext
	if err := json.Unmarshal(ctxResp.GetContent(), &vctx); err != nil {
		return fmt.Errorf("clerk: parse verdict-context: %w", err)
	}

	// ── Step 3: Check for existing feedback (revision path) ─────────
	existingFeedback := ""
	feedbackItems, err := client.GetFeedback(ctx, artefactPetitionFeedback)
	if err == nil && len(feedbackItems) > 0 {
		existingFeedback = formatFeedback(feedbackItems)
	}

	// ── Step 4: Draft petition prose via FoundryAgent ───────────────
	prose, err := draftProse(ctx, client, &deliberation, &vctx, existingFeedback)
	if err != nil {
		return err
	}

	// ── Step 5: Handle retire (no codification needed) ──────────────
	if vctx.Action == "retire" {
		return handleRetire(ctx, client, cfg, &deliberation, &vctx, prose)
	}

	// ── Step 6: Fan out to codification nodes ───────────────────────
	representations, err := fanOutCodification(ctx, client, cfg, &vctx)
	if err != nil {
		return err
	}

	// ── Step 7: Assemble and store petition ─────────────────────────
	return assemblePetition(ctx, client, cfg, &deliberation, &vctx, prose, representations)
}

// ---------------------------------------------------------------------------
// Prose Drafting (via FoundryAgent)
// ---------------------------------------------------------------------------

// draftProse uses a FoundryAgent to generate the prose justification.
func draftProse(
	ctx context.Context,
	client *flow.Client,
	deliberation *deliberationResult,
	vctx *verdictContext,
	existingFeedback string,
) (string, error) {
	schema, err := buildProseSchema()
	if err != nil {
		return "", fmt.Errorf("clerk: build prose schema: %w", err)
	}

	queryTmpl, err := template.New("clerk-query").Parse(queryTemplateText)
	if err != nil {
		return "", fmt.Errorf("clerk: parse query template: %w", err)
	}

	//nolint:lll // System prompt readability favors keeping it intact.
	systemPrompt := "You are a legislative clerk for a software governance system. Draft clear, formal prose justifications for law changes."

	agent, err := flow.NewAgent(client,
		flow.WithSchema(schema),
		flow.WithModel(flow.NewKimiK2Ollama()),
		flow.WithSystemPrompt(systemPrompt),
		flow.WithQueryTemplate(queryTmpl),
	)
	if err != nil {
		return "", fmt.Errorf("clerk: create agent: %w", err)
	}

	return runDraftProse(ctx, agent, deliberation, vctx, existingFeedback)
}

// runDraftProse runs the agent and extracts the prose. Separated for
// testability (allows mock model injection).
func runDraftProse(
	ctx context.Context,
	agent *flow.Agent,
	deliberation *deliberationResult,
	vctx *verdictContext,
	existingFeedback string,
) (string, error) {
	data := queryData{
		Outcome:          deliberation.Outcome,
		RoundsUsed:       deliberation.RoundsUsed,
		Hung:             deliberation.Hung,
		Justifications:   deliberation.Justifications,
		Trigger:          vctx.Trigger,
		Action:           vctx.Action,
		Goal:             vctx.Goal,
		LawID:            vctx.LawID,
		ExistingFeedback: existingFeedback,
	}

	output, err := agent.Run(ctx, data)
	if err != nil {
		return "", fmt.Errorf("clerk: agent run: %w", err)
	}

	var result struct {
		ProseJustification string `json:"prose_justification"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return "", fmt.Errorf("clerk: parse agent output: %w", err)
	}

	return result.ProseJustification, nil
}

// ---------------------------------------------------------------------------
// Retire Path (no codification)
// ---------------------------------------------------------------------------

// handleRetire drafts a petition for a retire action. No codification
// fan-out is needed — the petition just contains the retire instruction.
func handleRetire(
	ctx context.Context,
	client *flow.Client,
	cfg *clerkConfig,
	deliberation *deliberationResult,
	vctx *verdictContext,
	prose string,
) error {
	p := petition{
		Petition: petitionBody{
			Context: buildPetitionContext(deliberation, vctx),
			Changes: []petitionChange{
				{
					Action:        "retire",
					LawID:         vctx.LawID,
					Justification: prose,
				},
			},
			ProseJustification: prose,
		},
	}

	petitionJSON, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("clerk: marshal petition: %w", err)
	}

	if _, err := client.StoreArtefact(ctx, artefactPetition, "", petitionJSON); err != nil {
		return fmt.Errorf("clerk: store petition: %w", err)
	}

	if _, err := client.RouteToOutput(ctx, cfg.defaultOutput()); err != nil {
		return fmt.Errorf("clerk: route to output: %w", err)
	}

	slog.Info("clerk: retire petition drafted and routed")
	return nil
}

// ---------------------------------------------------------------------------
// Codification Fan-Out
// ---------------------------------------------------------------------------

// fanOutCodification fans out to codification nodes, awaits completion,
// and collects the formal representations.
func fanOutCodification(
	ctx context.Context,
	client *flow.Client,
	cfg *clerkConfig,
	vctx *verdictContext,
) ([]petitionRep, error) {
	if len(cfg.CodificationNodes) == 0 {
		// No codification nodes configured — produce markdown-only petition.
		return nil, nil
	}

	// Build codification goal artefact content.
	goalJSON, err := json.Marshal(map[string]any{
		"goal":       vctx.Goal,
		"applies_to": vctx.AppliesTo,
		"tier":       vctx.Tier,
		"action":     vctx.Action,
	})
	if err != nil {
		return nil, fmt.Errorf("clerk: marshal codification goal: %w", err)
	}

	// Build fan-out tasks.
	tasks := make([]flow.FanOutTask, len(cfg.CodificationNodes))
	for i, nodeName := range cfg.CodificationNodes {
		tasks[i] = flow.FanOutTask{
			TargetNode: nodeName,
			Artefacts: []flow.ChildArtefact{
				{
					ID:      artefactCodificationGoal,
					Content: goalJSON,
				},
			},
		}
	}

	// Fan out.
	_, err = client.FanOut(ctx, tasks)
	if err != nil {
		return nil, fmt.Errorf("clerk: codification fan-out: %w", err)
	}

	// Await all children.
	children, err := client.AwaitChildren(ctx, flow.WithPollingInterval(time.Millisecond))
	if err != nil {
		return nil, fmt.Errorf("clerk: await codification children: %w", err)
	}

	// Collect codification results.
	results, err := client.CollectArtefacts(ctx, children, artefactCodificationResult)
	if err != nil {
		return nil, fmt.Errorf("clerk: collect codification results: %w", err)
	}

	// Parse representations from collected artefacts.
	var representations []petitionRep
	for i, result := range results {
		content := result.Artefacts[artefactCodificationResult]
		if content == nil {
			slog.Warn("clerk: codification node produced no result",
				"node", cfg.CodificationNodes[i],
			)
			continue
		}

		var rep petitionRep
		if err := json.Unmarshal(content, &rep); err != nil {
			slog.Warn("clerk: invalid codification result",
				"node", cfg.CodificationNodes[i],
				"error", err,
			)
			continue
		}
		representations = append(representations, rep)
	}

	return representations, nil
}

// ---------------------------------------------------------------------------
// Petition Assembly
// ---------------------------------------------------------------------------

// assemblePetition builds the full petition with prose and formal
// representations, stores it, and routes to Tribunal.
func assemblePetition(
	ctx context.Context,
	client *flow.Client,
	cfg *clerkConfig,
	deliberation *deliberationResult,
	vctx *verdictContext,
	prose string,
	codificationReps []petitionRep,
) error {
	// Build the markdown representation (always present).
	markdownProse := draftMarkdownProse(vctx.Goal, deliberation)
	allReps := make([]petitionRep, 0, 1+len(codificationReps))
	allReps = append(allReps, petitionRep{Type: "text/markdown", Content: markdownProse})
	allReps = append(allReps, codificationReps...)

	change := petitionChange{
		Action:          vctx.Action,
		Tier:            vctx.Tier,
		Goal:            vctx.Goal,
		AppliesTo:       vctx.AppliesTo,
		Justification:   prose,
		Representations: allReps,
	}

	// For demote, also set the law_id.
	if vctx.Action == "demote" {
		change.LawID = vctx.LawID
	}

	p := petition{
		Petition: petitionBody{
			Context:            buildPetitionContext(deliberation, vctx),
			Changes:            []petitionChange{change},
			ProseJustification: prose,
		},
	}

	petitionJSON, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("clerk: marshal petition: %w", err)
	}

	if _, err := client.StoreArtefact(ctx, artefactPetition, "", petitionJSON); err != nil {
		return fmt.Errorf("clerk: store petition: %w", err)
	}

	if _, err := client.RouteToOutput(ctx, cfg.defaultOutput()); err != nil {
		return fmt.Errorf("clerk: route to output: %w", err)
	}

	slog.Info("clerk: petition drafted and routed",
		"action", vctx.Action,
		"codification_count", len(codificationReps),
	)
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildPetitionContext creates the petition context block.
func buildPetitionContext(deliberation *deliberationResult, vctx *verdictContext) petitionContext {
	justification := ""
	if len(deliberation.Justifications) > 0 {
		// Summarize as first juror's reasoning for simplicity.
		justification = deliberation.Justifications[0].Reasoning
	}

	return petitionContext{
		Trigger:        vctx.Trigger,
		SourceWorkitem: vctx.SourceWorkitem,
		Verdict:        deliberation.Outcome,
		Justification:  justification,
	}
}

// draftMarkdownProse formats the verdict as text/markdown prose suitable
// for a law representation. Ported from the deleted clerk server's
// draftProse function.
func draftMarkdownProse(goal string, deliberation *deliberationResult) string {
	var b strings.Builder

	b.WriteString("# Law\n\n")
	b.WriteString("## Goal\n\n")
	b.WriteString(goal)
	b.WriteString("\n\n")

	b.WriteString("## Verdict\n\n")
	fmt.Fprintf(&b, "**Outcome:** %s\n\n", deliberation.Outcome)
	fmt.Fprintf(&b, "**Rounds used:** %d\n\n", deliberation.RoundsUsed)

	if len(deliberation.Justifications) > 0 {
		b.WriteString("## Juror Reasoning\n\n")
		for _, j := range deliberation.Justifications {
			fmt.Fprintf(&b, "### %s (voted: %s)\n\n", j.JurorID, j.Outcome)
			b.WriteString(j.Reasoning)
			b.WriteString("\n\n")
		}
	}

	return b.String()
}

// formatFeedback renders feedback items as a readable string for the
// revision prompt.
func formatFeedback(items []*flowv1.FeedbackItem) string {
	var b strings.Builder
	for _, item := range items {
		fmt.Fprintf(&b, "- [%s] %s: %s\n",
			item.GetState().String(),
			item.GetSeverity().String(),
			item.GetMessage(),
		)
	}
	return b.String()
}

// buildProseSchema returns a JSON Schema for the agent's output.
func buildProseSchema() ([]byte, error) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prose_justification": map[string]any{
				"type":      "string",
				"minLength": 1,
			},
		},
		"required":             []string{"prose_justification"},
		"additionalProperties": false,
	}
	return json.Marshal(schema)
}
