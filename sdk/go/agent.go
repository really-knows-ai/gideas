package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"text/template"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	// DefaultHeartbeatInterval is the default interval for managed heartbeat
	// during inference execution. Chosen to provide comfortable margin within
	// typical node inactivity timeouts (30-60s).
	DefaultHeartbeatInterval = 15 * time.Second

	// telemetryEventLLMCost is the standard event type for LLM inference cost
	// accounting. See specs/04-sdk/06-sdk-telemetry.md#inference-cost-accounting-convention.
	telemetryEventLLMCost = "foundry.cost.llm"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// AgentOption configures the Agent.
type AgentOption func(*agentConfig)

type agentConfig struct {
	heartbeatInterval time.Duration
	schema            []byte
	modelName         string
	inferFn           InferFunc
	systemPrompt      string
	queryTemplate     *template.Template
}

// WithHeartbeatInterval overrides the default heartbeat interval for managed
// liveness during inference execution.
func WithHeartbeatInterval(d time.Duration) AgentOption {
	return func(c *agentConfig) {
		c.heartbeatInterval = d
	}
}

// WithSchema sets the JSON Schema bytes for output validation.
// The schema is compiled at construction time; malformed schemas are caught
// early rather than at inference time.
func WithSchema(schema []byte) AgentOption {
	return func(c *agentConfig) {
		c.schema = schema
	}
}

// WithModelName sets the model name string for inference dispatch.
// The Agent uses this to identify the model when calling the infer function.
// Required; empty model names are rejected at construction time.
func WithModelName(name string) AgentOption {
	return func(c *agentConfig) {
		c.modelName = name
	}
}

// WithSystemPrompt sets the system prompt, rendered once at construction
// time with config/constructor params (artefact names, role instructions, etc.).
func WithSystemPrompt(prompt string) AgentOption {
	return func(c *agentConfig) {
		c.systemPrompt = prompt
	}
}

// WithQueryTemplate sets the query prompt template, rendered per Run() call
// with runtime data (petition text, laws, feedback items, etc.).
func WithQueryTemplate(tmpl *template.Template) AgentOption {
	return func(c *agentConfig) {
		c.queryTemplate = tmpl
	}
}

// Agent is the SDK's managed inference wrapper (FoundryAgent).
//
// It owns the full inference pipeline: provider dispatch, prompt rendering,
// heartbeat lifecycle, schema validation, and cost telemetry. Concrete agents
// (defined per-node) extend it with their specific schema, system prompt,
// query prompt template, and typed Run() interface.
//
// The Agent provides three behavioural guarantees:
//
//  1. Managed Liveness — automatic Heartbeat() calls during provider execution.
//  2. Schema-First Output Validation — output validated against a JSON Schema
//     declared at construction time before it can affect artefact state or routing.
//  3. Atomic Cost Accounting — a foundry.cost.llm telemetry event emitted per
//     inference step immediately after the call returns.
//
// FoundryAgent introduces no new gRPC surface. It is the recommended pattern
// for all LLM-backed nodes.
type Agent struct {
	client        *Client
	schema        *jsonschema.Schema
	inferFn       InferFunc
	modelName     string
	systemPrompt  string
	queryTemplate *template.Template
	cfg           agentConfig
}

// NewAgent creates a FoundryAgent with functional options.
//
// Required options:
//   - WithModelName: model name string
//   - WithSchema: JSON Schema bytes for output validation
//   - WithQueryTemplate: query prompt template
//
// By default, the Agent uses an Ollama-backed InferFunc. Tests can override
// this with OverrideModelForTest.
//
// The Client must already be connected (via NewClient). The Agent uses the
// Client's Heartbeat() and RecordTelemetry() methods for managed liveness
// and cost accounting.
func NewAgent(client *Client, opts ...AgentOption) (*Agent, error) {
	if client == nil {
		return nil, fmt.Errorf("flow agent: client must not be nil")
	}

	cfg := agentConfig{
		heartbeatInterval: DefaultHeartbeatInterval,
	}
	for _, o := range opts {
		o(&cfg)
	}

	if cfg.modelName == "" {
		return nil, fmt.Errorf("flow agent: model name must not be empty (use WithModelName)")
	}
	if cfg.schema == nil {
		return nil, fmt.Errorf("flow agent: schema must not be nil (use WithSchema)")
	}
	if cfg.queryTemplate == nil {
		return nil, fmt.Errorf("flow agent: query template must not be nil (use WithQueryTemplate)")
	}

	// Default to Ollama-backed infer function if none set.
	if cfg.inferFn == nil {
		cfg.inferFn = NewOllamaInferFunc()
	}

	// Compile the JSON Schema at construction time.
	compiled, err := compileSchema(cfg.schema)
	if err != nil {
		return nil, fmt.Errorf("flow agent: invalid output schema: %w", err)
	}

	return &Agent{
		client:        client,
		schema:        compiled,
		inferFn:       cfg.inferFn,
		modelName:     cfg.modelName,
		systemPrompt:  cfg.systemPrompt,
		queryTemplate: cfg.queryTemplate,
		cfg:           cfg,
	}, nil
}

// Run executes a single inference step with full lifecycle management:
//
//  1. Renders the query prompt from the template with templateData.
//  2. Starts a heartbeat goroutine at the configured interval.
//  3. Calls model.Infer(ctx, systemPrompt, renderedQuery).
//  4. Stops the heartbeat goroutine.
//  5. Validates the output against the declared JSON Schema.
//  6. Emits a foundry.cost.llm telemetry event with cost metadata.
//  7. Prompt injection detection (stub/no-op for now).
//  8. Returns the validated output bytes.
//
// Multiple calls to Run within a single handler invocation are supported.
// Each call independently manages heartbeat, validates output, and emits
// cost telemetry — preserving per-step accounting granularity.
//
// If the provider returns an error, Run returns it immediately without
// attempting validation or telemetry emission.
//
// If output validation fails, Run returns a structured error and does not
// emit cost telemetry. Malformed inference output never enters the governed
// pipeline.
func (a *Agent) Run(ctx context.Context, templateData any) ([]byte, error) {
	// 1. Render query prompt from template.
	var queryBuf bytes.Buffer
	if err := a.queryTemplate.Execute(&queryBuf, templateData); err != nil {
		return nil, fmt.Errorf("flow agent: query template render failed: %w", err)
	}

	// 2. Start managed heartbeat loop.
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go a.heartbeatLoop(hbCtx)

	// 3. Execute the provider's inference logic.
	result, err := a.inferFn(ctx, a.modelName, a.systemPrompt, queryBuf.Bytes())

	// 4. Stop heartbeat (deferred cancel fires).
	hbCancel()

	if err != nil {
		return nil, fmt.Errorf("flow agent: provider infer failed: %w", err)
	}
	if result == nil {
		return nil, fmt.Errorf("flow agent: provider returned nil result")
	}

	// 5. Validate output against the declared schema.
	cleaned, err := a.validateOutput(result.Output)
	if err != nil {
		return nil, fmt.Errorf("flow agent: output validation failed: %w", err)
	}

	// 6. Emit foundry.cost.llm telemetry.
	if result.Cost != nil {
		if err := a.emitCostTelemetry(ctx, result.Cost); err != nil {
			// Telemetry failures are logged but do not fail work execution
			// (spec: "Telemetry failures do not block or fail work execution").
			slog.Warn("flow agent: cost telemetry emission failed (non-blocking)",
				"error", err,
				"model", a.modelName,
			)
		}
	}

	// 7. Prompt injection detection — stub/no-op.
	// TODO: Evaluate Go libraries and integrate real detection.
	// Hook point: after template rendering, after provider call, before return.

	// 8. Return validated output.
	return cleaned, nil
}

// ---------------------------------------------------------------------------
// Internal — Managed Heartbeat
// ---------------------------------------------------------------------------

// heartbeatLoop sends Heartbeat() calls at the configured interval until
// the context is cancelled.
func (a *Agent) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(a.cfg.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := a.client.Heartbeat(ctx); err != nil {
				// Log but do not fail — the heartbeat is a liveness signal,
				// not a correctness requirement.
				if ctx.Err() == nil {
					slog.Warn("flow agent: heartbeat failed", "error", err)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Internal — Schema Validation
// ---------------------------------------------------------------------------

// compileSchema compiles JSON Schema bytes into a reusable validator.
func compileSchema(schema []byte) (*jsonschema.Schema, error) {
	var schemaDoc any
	if err := json.Unmarshal(schema, &schemaDoc); err != nil {
		return nil, fmt.Errorf("schema is not valid JSON: %w", err)
	}

	c := jsonschema.NewCompiler()
	if err := c.AddResource("schema.json", schemaDoc); err != nil {
		return nil, fmt.Errorf("failed to add schema resource: %w", err)
	}

	compiled, err := c.Compile("schema.json")
	if err != nil {
		return nil, fmt.Errorf("failed to compile schema: %w", err)
	}

	return compiled, nil
}

// validateOutput validates JSON output bytes against the compiled schema.
// Returns the cleaned (code-fence stripped) output.
func (a *Agent) validateOutput(output []byte) ([]byte, error) {
	// Strip markdown code fences (```json ... ```) if present.
	cleaned := stripCodeFences(output)

	var outputDoc any
	if err := json.Unmarshal(cleaned, &outputDoc); err != nil {
		return nil, fmt.Errorf("output is not valid JSON: %w", err)
	}

	err := a.schema.Validate(outputDoc)
	if err != nil {
		return nil, formatValidationError(err)
	}
	return cleaned, nil
}

// stripCodeFences removes markdown JSON code fences from output.
func stripCodeFences(in []byte) []byte {
	s := strings.TrimSpace(string(in))
	// Remove opening ```json or ``` and closing ```
	if strings.HasPrefix(s, "```") {
		s = s[strings.Index(s, "\n")+1:]
	}
	if before, ok := strings.CutSuffix(s, "```"); ok {
		s = before
	}
	return []byte(strings.TrimSpace(s))
}

// formatValidationError converts jsonschema validation errors into a
// human-readable format.
func formatValidationError(err error) error {
	// The jsonschema library's Error() method produces a detailed,
	// hierarchical error string. Use it directly.
	return fmt.Errorf("schema validation failed: %s", err.Error())
}

// ---------------------------------------------------------------------------
// Internal — Cost Telemetry
// ---------------------------------------------------------------------------

// emitCostTelemetry records a foundry.cost.llm event via RecordTelemetry.
func (a *Agent) emitCostTelemetry(ctx context.Context, cost *CostMetadata) error {
	payload := map[string]any{
		"model":         cost.Model,
		"input_tokens":  cost.InputTokens,
		"output_tokens": cost.OutputTokens,
		"duration_ms":   cost.DurationMs,
	}

	// Merge optional extra fields (provider, cached_tokens, reasoning_tokens, etc.)
	maps.Copy(payload, cost.Extra)

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal cost payload: %w", err)
	}

	return a.client.RecordTelemetry(ctx, telemetryEventLLMCost, data)
}

// OverrideModelForTest replaces the infer function on an Agent. Named to make
// misuse in production code obvious. Use only in tests.
func OverrideModelForTest(a *Agent, fn InferFunc) {
	a.inferFn = fn
}
