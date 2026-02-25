package flow

import "context"

// provider abstracts LLM inference backends. Each implementation decides
// how to present the system and query prompts to its backend (concatenation,
// message roles, etc.).
//
// Implementations must be safe for concurrent use.
type provider interface {
	infer(ctx context.Context, model, systemPrompt string, queryPrompt []byte) (*InferOutput, error)
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
