package flow

import "context"

// GptOss120bOllama is the GPT-OSS 120B cloud model served via Ollama.
type GptOss120bOllama struct {
	p provider
}

// NewGptOss120bOllama creates a GptOss120bOllama model.
func NewGptOss120bOllama() *GptOss120bOllama {
	return &GptOss120bOllama{p: newOllamaProvider()}
}

// Infer delegates to the Ollama provider with the GPT-OSS 120B model ID.
func (m *GptOss120bOllama) Infer(ctx context.Context, systemPrompt string, queryPrompt []byte) (*InferOutput, error) {
	return m.p.infer(ctx, "gpt-oss:120b-cloud", systemPrompt, queryPrompt)
}
