// Codify-SMT is the reference codification node for the Foundry Judiciary.
//
// It translates a law goal into an SMT-LIB formal representation using a
// FoundryAgent (LLM). The Clerk fans out to codification nodes during
// petition drafting; each codification node produces a formal representation
// in its declared output format.
//
// The node receives a child Workitem from the Clerk with:
//
//   - "codification-goal" artefact -- JSON object with goal, applies_to,
//     tier, and action fields describing the law to be codified
//
// The node runs a FoundryAgent to translate the goal into SMT-LIB syntax,
// stores the result as the "codification-result" artefact (JSON object with
// type and content fields), and calls Complete().
//
// Configuration (YAML via NODE_CONFIG_PATH):
//
//	outputFormat: "application/smt-lib"  # MIME type of produced representation
//	systemPrompt: ""                     # optional override for the default prompt
//
// Usage:
//
//	go run ./nodes/codify-smt/main.go
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
// Well-known artefact IDs (matching Clerk fan-out contract)
// ---------------------------------------------------------------------------

const (
	// artefactCodificationGoal is written by the Clerk on each child
	// Workitem before fan-out. Contains the law goal and context.
	artefactCodificationGoal = "codification-goal"

	// artefactCodificationResult is produced by this node. The Clerk
	// collects it after fan-out completion.
	artefactCodificationResult = "codification-result"

	// defaultOutputFormat is the default MIME type for the SMT-LIB
	// formal representation produced by this node.
	defaultOutputFormat = "application/smt-lib"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// codifyConfig holds the codify-smt node's runtime configuration.
type codifyConfig struct {
	// OutputFormat is the MIME type of the formal representation this
	// node produces. Default: "application/smt-lib".
	OutputFormat string `yaml:"outputFormat"`

	// SystemPrompt optionally overrides the default system prompt.
	// Empty means use the built-in default.
	SystemPrompt string `yaml:"systemPrompt"`
}

// outputFormat returns the effective output MIME type.
func (c *codifyConfig) outputFormat() string {
	if c.OutputFormat == "" {
		return defaultOutputFormat
	}
	return c.OutputFormat
}

// systemPrompt returns the effective system prompt.
func (c *codifyConfig) systemPrompt() string {
	if c.SystemPrompt != "" {
		return c.SystemPrompt
	}
	return defaultSystemPrompt
}

// ---------------------------------------------------------------------------
// System Prompt
// ---------------------------------------------------------------------------

//nolint:lll // Prompt readability favors keeping it intact.
const defaultSystemPrompt = `You are an expert in formal verification and SMT-LIB (Satisfiability Modulo Theories) syntax.

Your task is to translate governance law goals into precise SMT-LIB assertions. The assertions must:

1. Capture the intent of the goal as formally and precisely as possible.
2. Use SMT-LIB 2.6 syntax (compatible with Z3 and CVC5 solvers).
3. Declare all necessary sorts, functions, and constants before use.
4. Include comments (prefixed with ;) explaining each declaration and assertion.
5. Be self-contained -- a solver should be able to process the output directly.

Focus on capturing the semantic meaning of the goal. If the goal references specific artefact types or quality attributes, model them as uninterpreted sorts or enumerated types as appropriate.

If the goal is too vague to formalise precisely, produce the closest reasonable approximation and include a comment explaining the limitation.`

// ---------------------------------------------------------------------------
// Query Template
// ---------------------------------------------------------------------------

//nolint:lll // Template readability favors keeping it intact.
const queryTemplateText = `Translate the following governance law goal into SMT-LIB formal logic.

Goal: {{.Goal}}

Context:
- Applies to: {{range $i, $a := .AppliesTo}}{{if $i}}, {{end}}{{$a}}{{end}}
- Tier: {{.Tier}}
- Action: {{.Action}}

You MUST respond with a JSON object containing exactly one key:
- "smt_content": the complete SMT-LIB representation as a single string

Output ONLY the JSON object, nothing else.`

// queryData is the template data for the codification prompt.
type queryData struct {
	Goal      string
	AppliesTo []string
	Tier      int32
	Action    string
}

// ---------------------------------------------------------------------------
// Input / Output Structures
// ---------------------------------------------------------------------------

// codificationGoal is the JSON structure of the "codification-goal" artefact
// written by the Clerk.
type codificationGoal struct {
	Goal      string   `json:"goal"`
	AppliesTo []string `json:"applies_to"`
	Tier      int32    `json:"tier"`
	Action    string   `json:"action"`
}

// codificationResult is the JSON structure of the "codification-result"
// artefact expected by the Clerk.
type codificationResult struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// agentOutput is the JSON structure the LLM is expected to produce.
type agentOutput struct {
	SMTContent string `json:"smt_content"`
}

// ---------------------------------------------------------------------------
// Entry Point
// ---------------------------------------------------------------------------

func main() {
	slog.Info("codify-smt: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("codify-smt: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("codify-smt: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("codify-smt: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	cfg, err := nodeconfig.Load[codifyConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("codify-smt: load config: %w", err)
	}

	return handleCodify(ctx, client, cfg)
}

// ---------------------------------------------------------------------------
// Core Logic
// ---------------------------------------------------------------------------

// handleCodify contains the codify-smt node's core logic, separated from
// handler boilerplate for testability.
func handleCodify(ctx context.Context, client *flow.Client, cfg *codifyConfig) error {
	_, _ = client.Heartbeat(ctx)

	// -- Step 1: Read the codification goal artefact --------------------
	goalResp, err := client.GetArtefact(ctx, artefactCodificationGoal)
	if err != nil {
		return fmt.Errorf("codify-smt: get codification-goal artefact: %w", err)
	}

	var goal codificationGoal
	if err := json.Unmarshal(goalResp.GetContent(), &goal); err != nil {
		return fmt.Errorf("codify-smt: parse codification-goal: %w", err)
	}

	if goal.Goal == "" {
		return fmt.Errorf("codify-smt: codification-goal has empty goal field")
	}

	// -- Step 2: Build the FoundryAgent ---------------------------------
	schemaBytes, err := buildOutputSchema()
	if err != nil {
		return fmt.Errorf("codify-smt: build output schema: %w", err)
	}

	queryTmpl, err := template.New("codify-query").Parse(queryTemplateText)
	if err != nil {
		return fmt.Errorf("codify-smt: parse query template: %w", err)
	}

	agent, err := flow.NewAgent(client,
		flow.WithSchema(schemaBytes),
		flow.WithModelName("kimi-k2.5:cloud"),
		flow.WithSystemPrompt(cfg.systemPrompt()),
		flow.WithQueryTemplate(queryTmpl),
	)
	if err != nil {
		return fmt.Errorf("codify-smt: create agent: %w", err)
	}

	return runCodify(ctx, client, agent, cfg, &goal)
}

// runCodify runs the agent and stores the result. Separated from
// handleCodify to allow tests to inject a mock model via
// OverrideModelForTest before calling this function.
func runCodify(
	ctx context.Context,
	client *flow.Client,
	agent *flow.Agent,
	cfg *codifyConfig,
	goal *codificationGoal,
) error {
	// -- Step 3: Run the agent ------------------------------------------
	data := queryData{
		Goal:      goal.Goal,
		AppliesTo: goal.AppliesTo,
		Tier:      goal.Tier,
		Action:    goal.Action,
	}

	output, err := agent.Run(ctx, data)
	if err != nil {
		return fmt.Errorf("codify-smt: agent run: %w", err)
	}

	// -- Step 4: Parse agent output and wrap as codification result ------
	var agentOut agentOutput
	if err := json.Unmarshal(output, &agentOut); err != nil {
		return fmt.Errorf("codify-smt: parse agent output: %w", err)
	}

	if agentOut.SMTContent == "" {
		return fmt.Errorf("codify-smt: agent produced empty smt_content")
	}

	result := codificationResult{
		Type:    cfg.outputFormat(),
		Content: agentOut.SMTContent,
	}

	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("codify-smt: marshal codification-result: %w", err)
	}

	slog.Info("codify-smt: representation produced",
		"output_format", cfg.outputFormat(),
		"content_length", len(agentOut.SMTContent),
	)

	// -- Step 5: Store codification result artefact ---------------------
	if _, err := client.StoreArtefact(ctx, artefactCodificationResult, "", resultJSON); err != nil {
		return fmt.Errorf("codify-smt: store codification-result: %w", err)
	}

	// -- Step 6: Complete -----------------------------------------------
	if _, err := client.Complete(ctx); err != nil {
		return fmt.Errorf("codify-smt: complete: %w", err)
	}

	slog.Info("codify-smt: done")
	return nil
}

// ---------------------------------------------------------------------------
// Output Schema
// ---------------------------------------------------------------------------

// buildOutputSchema returns a JSON Schema constraining the agent's output
// to a single "smt_content" string field.
func buildOutputSchema() ([]byte, error) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"smt_content": map[string]any{
				"type":      "string",
				"minLength": 1,
			},
		},
		"required":             []string{"smt_content"},
		"additionalProperties": false,
	}
	return json.Marshal(schema)
}
