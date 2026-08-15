package flow

import "github.com/foundry/flow/sdk/go/internal/provider"

// Re-exported LLM provider contract. The provider subsystem — the InferFunc
// contract and the Ollama implementation (NewOllamaInferFunc) — lives in the
// internal/provider package; these aliases keep the Agent's public surface
// stable on the flow package.
type (
	// InferFunc performs LLM inference. Implementations must be safe for
	// concurrent use.
	InferFunc = provider.InferFunc

	// InferOutput holds the raw LLM response and optional cost metadata.
	InferOutput = provider.InferOutput

	// CostMetadata holds cost information sourced from the provider.
	CostMetadata = provider.CostMetadata
)
