package flow

import "context"

// Model abstracts LLM inference. Concrete implementations encapsulate
// both the model identity and the transport backend (provider).
// Implementations must be safe for concurrent use.
type Model interface {
	Infer(ctx context.Context, systemPrompt string, queryPrompt []byte) (*InferOutput, error)
}

// OverrideModelForTest replaces the model on an Agent. Named to make misuse
// in production code obvious. Use only in tests.
func OverrideModelForTest(a *Agent, m Model) {
	a.model = m
}
