package flow

import "context"

// Provider abstracts LLM inference backends. Each implementation decides
// how to present the system and query prompts to its backend (concatenation,
// message roles, etc.).
//
// Implementations must be safe for concurrent use.
type Provider interface {
	Infer(ctx context.Context, model, systemPrompt string, queryPrompt []byte) (*InferOutput, error)
}

// ---------------------------------------------------------------------------
// Model — binds a model identifier to a Provider
// ---------------------------------------------------------------------------

// Model binds a model identifier to a Provider. It encapsulates which model
// to use and how to reach it. Agents receive a Model at construction time;
// they never reference the provider or model string directly.
//
// Create a Model once, share it across multiple agents. Different agents
// can use different Models (backed by the same or different providers).
type Model struct {
	id       string
	provider Provider
}

// NewModel creates a Model. The id is the model identifier passed to the
// provider on each inference call (e.g. "kimi-k2.5:cloud", "gpt-4o").
// The provider is the transport backend that executes the inference.
func NewModel(id string, provider Provider) *Model {
	return &Model{id: id, provider: provider}
}

// Infer delegates to the underlying provider with the bound model ID.
func (m *Model) Infer(ctx context.Context, systemPrompt string, queryPrompt []byte) (*InferOutput, error) {
	return m.provider.Infer(ctx, m.id, systemPrompt, queryPrompt)
}

// InferOutput holds the raw LLM response and optional cost metadata.
type InferOutput struct {
	// Output is the raw response bytes from the LLM.
	Output []byte

	// Cost holds cost information sourced from the provider.
	// nil if the provider doesn't report costs.
	Cost *CostMetadata
}

// CostMetadata holds cost information sourced from the provider.
// Only the provider knows actual token counts, pricing, and timing.
type CostMetadata struct {
	// Model is the model identifier used for the inference call.
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
