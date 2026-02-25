package flow

import "context"

// KimiK2Ollama is the Kimi K2.5 cloud model served via Ollama.
type KimiK2Ollama struct {
	p provider
}

// NewKimiK2Ollama creates a KimiK2Ollama model.
func NewKimiK2Ollama() *KimiK2Ollama {
	return &KimiK2Ollama{p: newOllamaProvider()}
}

// Infer delegates to the Ollama provider with the Kimi K2.5 model ID.
func (m *KimiK2Ollama) Infer(ctx context.Context, systemPrompt string, queryPrompt []byte) (*InferOutput, error) {
	return m.p.infer(ctx, "kimi-k2.5:cloud", systemPrompt, queryPrompt)
}
