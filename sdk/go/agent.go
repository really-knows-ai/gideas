package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
}

// WithHeartbeatInterval overrides the default heartbeat interval for managed
// liveness during inference execution.
func WithHeartbeatInterval(d time.Duration) AgentOption {
	return func(c *agentConfig) {
		c.heartbeatInterval = d
	}
}

// InferFunc is the developer's inference logic. It receives a context and
// input data, and returns an InferResult containing the structured output
// and cost metadata for telemetry emission.
//
// The context carries a cancellation signal tied to the Agent's lifecycle.
// Input is the raw bytes the caller passes to Agent.Run.
type InferFunc func(ctx context.Context, input []byte) (*InferResult, error)

// InferResult holds inference output and cost metadata.
//
// Output must be JSON that conforms to the schema declared at Agent
// construction time. Cost fields are emitted as a foundry.cost.llm
// telemetry event after successful validation.
type InferResult struct {
	// Output is the structured JSON inference output. It is validated against
	// the Agent's output schema before being returned to the caller.
	Output []byte

	// Model is the model identifier used for the inference call (required).
	Model string

	// InputTokens is the number of tokens in the inference input.
	InputTokens int64

	// OutputTokens is the number of tokens in the inference output.
	OutputTokens int64

	// DurationMs is the wall-clock duration of the inference call in milliseconds.
	DurationMs int64

	// Extra contains optional additional cost fields (e.g. "provider",
	// "cached_tokens", "reasoning_tokens"). These are merged into the
	// foundry.cost.llm telemetry payload alongside the standard fields.
	Extra map[string]any
}

// Agent is the SDK's managed inference wrapper (FoundryAgent).
//
// It wraps existing SDK operations — Heartbeat() and RecordTelemetry() — into
// a managed lifecycle around the developer's inference logic, providing three
// behavioural guarantees:
//
//  1. Managed Liveness — automatic Heartbeat() calls during Infer execution.
//  2. Schema-First Output Validation — output validated against a JSON Schema
//     declared at construction time before it can affect artefact state or routing.
//  3. Atomic Cost Accounting — a foundry.cost.llm telemetry event emitted per
//     inference step immediately after the call returns.
//
// FoundryAgent introduces no new gRPC surface. It is the recommended pattern
// for all LLM-backed nodes.
type Agent struct {
	client *Client
	schema *jsonschema.Schema
	cfg    agentConfig
}

// NewAgent creates a FoundryAgent with a compiled output schema.
//
// The schema parameter must be valid JSON Schema bytes (Draft 2020-12, Draft 7,
// etc.). The schema is compiled at construction time so malformed schemas are
// caught early rather than at inference time.
//
// The Client must already be connected (via NewClient). The Agent uses the
// Client's Heartbeat() and RecordTelemetry() methods for managed liveness
// and cost accounting.
func NewAgent(client *Client, schema []byte, opts ...AgentOption) (*Agent, error) {
	if client == nil {
		return nil, fmt.Errorf("flow agent: client must not be nil")
	}

	cfg := agentConfig{
		heartbeatInterval: DefaultHeartbeatInterval,
	}
	for _, o := range opts {
		o(&cfg)
	}

	// Compile the JSON Schema at construction time.
	compiled, err := compileSchema(schema)
	if err != nil {
		return nil, fmt.Errorf("flow agent: invalid output schema: %w", err)
	}

	return &Agent{
		client: client,
		schema: compiled,
		cfg:    cfg,
	}, nil
}

// Run executes a single inference step with full lifecycle management:
//
//  1. Starts a heartbeat goroutine at the configured interval.
//  2. Calls the provided InferFunc with the input.
//  3. Stops the heartbeat goroutine.
//  4. Validates the output against the declared JSON Schema.
//  5. Emits a foundry.cost.llm telemetry event with cost metadata.
//  6. Returns the validated output bytes.
//
// Multiple calls to Run within a single handler invocation are supported.
// Each call independently manages heartbeat, validates output, and emits
// cost telemetry — preserving per-step accounting granularity.
//
// If the InferFunc returns an error, Run returns it immediately without
// attempting validation or telemetry emission.
//
// If output validation fails, Run returns a structured error and does not
// emit cost telemetry. Malformed inference output never enters the governed
// pipeline.
func (a *Agent) Run(ctx context.Context, infer InferFunc, input []byte) ([]byte, error) {
	// 1. Start managed heartbeat loop.
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go a.heartbeatLoop(hbCtx)

	// 2. Execute the developer's inference logic.
	result, err := infer(ctx, input)

	// 3. Stop heartbeat (deferred cancel fires).
	hbCancel()

	if err != nil {
		return nil, fmt.Errorf("flow agent: infer failed: %w", err)
	}
	if result == nil {
		return nil, fmt.Errorf("flow agent: infer returned nil result")
	}

	// 4. Validate output against the declared schema.
	if err := a.validateOutput(result.Output); err != nil {
		return nil, fmt.Errorf("flow agent: output validation failed: %w", err)
	}

	// 5. Emit foundry.cost.llm telemetry.
	if err := a.emitCostTelemetry(ctx, result); err != nil {
		// Telemetry failures are logged but do not fail work execution
		// (spec: "Telemetry failures do not block or fail work execution").
		slog.Warn("flow agent: cost telemetry emission failed (non-blocking)",
			"error", err,
			"model", result.Model,
		)
	}

	// 6. Return validated output.
	return result.Output, nil
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
func (a *Agent) validateOutput(output []byte) error {
	var outputDoc any
	if err := json.Unmarshal(output, &outputDoc); err != nil {
		return fmt.Errorf("output is not valid JSON: %w", err)
	}

	err := a.schema.Validate(outputDoc)
	if err != nil {
		return formatValidationError(err)
	}
	return nil
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
func (a *Agent) emitCostTelemetry(ctx context.Context, result *InferResult) error {
	payload := map[string]any{
		"model":         result.Model,
		"input_tokens":  result.InputTokens,
		"output_tokens": result.OutputTokens,
		"duration_ms":   result.DurationMs,
	}

	// Merge optional extra fields (provider, cached_tokens, reasoning_tokens, etc.)
	for k, v := range result.Extra {
		payload[k] = v
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal cost payload: %w", err)
	}

	return a.client.RecordTelemetry(ctx, telemetryEventLLMCost, data)
}
