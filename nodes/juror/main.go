// Juror is a config-driven judicial voting node for the Foundry Judiciary.
//
// A single Juror node image supports all five standard judicial personalities
// (textualist, reformer, conservator, pragmatist, devils-advocate). The
// personality is selected via YAML configuration at deployment time, allowing
// the Arbiter and Tribunal to fan out to differently-configured instances of
// the same image for deliberation diversity.
//
// The Juror receives a child Workitem from a fan-out parent (Arbiter or
// Tribunal) with:
//
//   - "question" artefact — the deliberation question
//   - "evidence" artefact — structured markdown evidence bundle
//   - "allowed-outcomes" artefact — JSON array of valid vote values
//   - "prior-round-reasoning" artefact (optional) — anonymised peer arguments
//     from a previous round, present only on retry
//
// The Juror runs a FoundryAgent with its configured personality system prompt,
// produces a JSON verdict (outcome + reasoning), stores it as the "verdict"
// artefact, and calls Complete().
//
// Configuration (YAML via NODE_CONFIG_PATH):
//
//	personality: textualist  # textualist|reformer|conservator|pragmatist|devils-advocate
//	systemPrompt: ""         # optional override; empty = use default for personality
//
// Usage:
//
//	go run ./nodes/juror/main.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"text/template"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// Well-known artefact IDs
// ---------------------------------------------------------------------------

const (
	artefactQuestion   = "question"
	artefactEvidence   = "evidence"
	artefactOutcomes   = "allowed-outcomes"
	artefactPriorRound = "prior-round-reasoning"
	artefactVerdict    = "verdict"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// jurorConfig holds the Juror's runtime configuration.
type jurorConfig struct {
	// Personality selects the judicial philosophy.
	// Valid: textualist, reformer, conservator, pragmatist, devils-advocate.
	// Default: textualist.
	Personality string `yaml:"personality"`

	// SystemPrompt optionally overrides the default system prompt for
	// the selected personality. Empty means use the built-in default.
	SystemPrompt string `yaml:"systemPrompt"`
}

// personality returns the effective personality, defaulting to textualist.
func (c *jurorConfig) personality() string {
	if c.Personality == "" {
		return "textualist"
	}
	return c.Personality
}

// systemPrompt returns the effective system prompt for the configured
// personality. If SystemPrompt is set in config, it is used directly.
// Otherwise the built-in default for the personality is returned.
func (c *jurorConfig) systemPrompt() string {
	if c.SystemPrompt != "" {
		return c.SystemPrompt
	}
	return defaultPrompts[c.personality()]
}

// ---------------------------------------------------------------------------
// Personality Prompts (ported from jury/internal/jurors/)
// ---------------------------------------------------------------------------

//nolint:lll // Prompt readability favors keeping them intact.
var defaultPrompts = map[string]string{
	"textualist": `You are a strict legal textualist serving on a governance jury.

Your judicial philosophy:
- Interpret cited laws and evidence at face value, exactly as written.
- Favour the side with stronger, more explicit legal citations.
- Do not infer intent beyond what is explicitly stated in the evidence.
- Precedent and exact rule language take priority over practical considerations.
- If the rules are clear, follow them — even if the outcome seems impractical.

Evaluate the evidence and question presented to you. Vote for the outcome that is most supported by the explicit rules and citations provided.`,

	"reformer": `You are a judicial reformer serving on a governance jury.

Your judicial philosophy:
- Favour evolution and improvement over the status quo.
- Be willing to promote promising new rules that show evidence of value.
- Outdated or underperforming laws should be retired to make room for better ones.
- Side with novel, well-reasoned arguments even if they challenge precedent.
- Progress requires accepting measured risk; stagnation is a form of failure.

Evaluate the evidence and question presented to you. Vote for the outcome that best advances improvement and evolution of the governance system.`,

	"conservator": `You are a judicial conservator serving on a governance jury.

Your judicial philosophy:
- Favour stability and existing precedent above novelty.
- Apply a high bar for change — the burden of proof lies with whoever proposes change.
- Be reluctant to promote new rules; prefer to maintain the status quo unless evidence is overwhelming.
- Retiring newer, unproven laws is acceptable; retiring well-established ones is not.
- Consistency and predictability are more valuable than theoretical optimality.

Evaluate the evidence and question presented to you. Vote for the outcome that best preserves stability and existing precedent.`,

	"pragmatist": `You are a pragmatic analyst serving on a governance jury.

Your judicial philosophy:
- Weigh practical impact and cost-effectiveness above all else.
- Consider friction economics — favour outcomes that reduce future cost and rework.
- Evaluate whether the proposed outcome is realistic and achievable in practice.
- Rules that cause more harm than good should be questioned.
- The best outcome is the one that produces the most value with the least friction.

Evaluate the evidence and question presented to you. Vote for the outcome that is most practical and cost-effective.`,

	"devils-advocate": `You are a devil's advocate serving on a governance jury.

Your judicial philosophy:
- Challenge the majority position and stress-test the reasoning of all sides.
- If evidence seems one-sided or a conclusion appears obvious, push back and identify weaknesses.
- Play the contrarian role to force the jury toward more considered, robust consensus.
- Ask "what could go wrong?" and "what are we missing?" before voting.
- Only agree with the apparent majority if you genuinely cannot find flaws in their reasoning.

Evaluate the evidence and question presented to you. Vote for the outcome that you believe would survive the strongest possible scrutiny, even if it goes against the apparent consensus.`,
}

// ---------------------------------------------------------------------------
// Query Template
// ---------------------------------------------------------------------------

// queryTemplateText is the shared query prompt rendered per-invocation.
// It includes question, evidence, allowed outcomes, and optional prior-round
// reasoning for multi-round deliberation.
//
//nolint:lll // Template readability favors keeping it intact.
const queryTemplateText = `Question:
{{.Question}}

Evidence:
{{.Evidence}}

Allowed outcomes: {{range $i, $o := .AllowedOutcomes}}{{if $i}}, {{end}}{{$o}}{{end}}
{{- if .PriorRoundReasoning}}

Prior round arguments from other jurors (anonymous):
{{.PriorRoundReasoning}}

Consider these arguments carefully but form your own independent judgment.
{{- end}}

You MUST respond with a JSON object containing exactly two keys:
- "outcome": one of the allowed outcomes listed above
- "reasoning": your justification for choosing that outcome

Output ONLY the JSON object, nothing else.`

// jurorQueryData is the template data for the query prompt.
type jurorQueryData struct {
	Question            string
	Evidence            string
	AllowedOutcomes     []string
	PriorRoundReasoning string
}

// ---------------------------------------------------------------------------
// Entry Point
// ---------------------------------------------------------------------------

func main() {
	slog.Info("juror: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("juror: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("juror: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("juror: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	cfg, err := nodeconfig.Load[jurorConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("juror: load config: %w", err)
	}

	return handleJuror(ctx, client, cfg)
}

// ---------------------------------------------------------------------------
// Core Logic
// ---------------------------------------------------------------------------

// handleJuror contains the Juror's core logic, separated from handler
// boilerplate for testability.
func handleJuror(ctx context.Context, client *flow.Client, cfg *jurorConfig) error {
	_, _ = client.Heartbeat(ctx)

	// ── Step 1: Read input artefacts from the child Workitem ─────────
	questionResp, err := client.GetArtefact(ctx, artefactQuestion)
	if err != nil {
		return fmt.Errorf("juror: get question artefact: %w", err)
	}
	question := string(questionResp.GetContent())

	evidenceResp, err := client.GetArtefact(ctx, artefactEvidence)
	if err != nil {
		return fmt.Errorf("juror: get evidence artefact: %w", err)
	}
	evidence := string(evidenceResp.GetContent())

	outcomesResp, err := client.GetArtefact(ctx, artefactOutcomes)
	if err != nil {
		return fmt.Errorf("juror: get allowed-outcomes artefact: %w", err)
	}
	var allowedOutcomes []string
	if err := json.Unmarshal(outcomesResp.GetContent(), &allowedOutcomes); err != nil {
		return fmt.Errorf("juror: parse allowed-outcomes: %w", err)
	}

	// Prior-round reasoning is optional (only present on retry rounds).
	priorRound := ""
	priorResp, err := client.GetArtefact(ctx, artefactPriorRound)
	if err == nil {
		priorRound = string(priorResp.GetContent())
	}

	// ── Step 2: Build dynamic output schema ─────────────────────────
	schemaBytes, err := buildOutputSchema(allowedOutcomes)
	if err != nil {
		return fmt.Errorf("juror: build output schema: %w", err)
	}

	// ── Step 3: Create the FoundryAgent with personality ─────────────
	queryTmpl, err := template.New("juror-query").Parse(queryTemplateText)
	if err != nil {
		return fmt.Errorf("juror: parse query template: %w", err)
	}

	agent, err := flow.NewAgent(client,
		flow.WithSchema(schemaBytes),
		flow.WithModel(flow.NewKimiK2Ollama()),
		flow.WithSystemPrompt(cfg.systemPrompt()),
		flow.WithQueryTemplate(queryTmpl),
	)
	if err != nil {
		return fmt.Errorf("juror: create agent: %w", err)
	}

	return runJuror(ctx, client, agent, question, evidence, allowedOutcomes, priorRound)
}

// runJuror runs the agent and stores the verdict. Separated from handleJuror
// to allow tests to inject a mock model via OverrideModelForTest before
// calling this function.
func runJuror(
	ctx context.Context,
	client *flow.Client,
	agent *flow.Agent,
	question, evidence string,
	allowedOutcomes []string,
	priorRound string,
) error {
	// ── Step 4: Run the agent ────────────────────────────────────────
	data := jurorQueryData{
		Question:            question,
		Evidence:            evidence,
		AllowedOutcomes:     allowedOutcomes,
		PriorRoundReasoning: priorRound,
	}

	output, err := agent.Run(ctx, data)
	if err != nil {
		return fmt.Errorf("juror: agent run: %w", err)
	}

	slog.Info("juror: verdict produced", "output", string(output))

	// ── Step 5: Store verdict artefact ───────────────────────────────
	if _, err := client.StoreArtefact(ctx, artefactVerdict, "", output); err != nil {
		return fmt.Errorf("juror: store verdict artefact: %w", err)
	}

	// ── Step 6: Complete ─────────────────────────────────────────────
	if _, err := client.Complete(ctx); err != nil {
		return fmt.Errorf("juror: complete: %w", err)
	}

	slog.Info("juror: done")
	return nil
}

// ---------------------------------------------------------------------------
// Dynamic Output Schema
// ---------------------------------------------------------------------------

// buildOutputSchema generates a JSON Schema that constrains the "outcome"
// field to the allowed outcomes enum.
func buildOutputSchema(allowedOutcomes []string) ([]byte, error) {
	if len(allowedOutcomes) == 0 {
		return nil, fmt.Errorf("juror schema: allowed_outcomes must not be empty")
	}

	enumValues := make([]any, len(allowedOutcomes))
	for i, o := range allowedOutcomes {
		enumValues[i] = o
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"outcome": map[string]any{
				"type": "string",
				"enum": enumValues,
			},
			"reasoning": map[string]any{
				"type":      "string",
				"minLength": 1,
			},
		},
		"required":             []string{"outcome", "reasoning"},
		"additionalProperties": false,
	}

	return json.Marshal(schema)
}
