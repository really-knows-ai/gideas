package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	ollamaDefaultBaseURL = "http://localhost:11434"
	ollamaEnvBaseURL     = "OLLAMA_BASE_URL"
	ollamaDefaultTimeout = 5 * time.Minute
)

// OllamaProvider implements Provider for Ollama's /api/generate endpoint.
//
// It concatenates the system prompt and query prompt into a single prompt
// string for Ollama's non-streaming generate API. Token counts and timing
// are extracted from the response to populate CostMetadata.
type OllamaProvider struct {
	baseURL    string
	httpClient *http.Client
}

// OllamaOption configures an OllamaProvider.
type OllamaOption func(*OllamaProvider)

// WithBaseURL overrides the base URL for the Ollama API.
// If not set, the provider reads OLLAMA_BASE_URL from the environment,
// falling back to http://localhost:11434.
func WithBaseURL(url string) OllamaOption {
	return func(p *OllamaProvider) {
		p.baseURL = strings.TrimRight(url, "/")
	}
}

// WithTimeout overrides the HTTP client timeout.
// The default is 5 minutes, appropriate for long LLM generation calls.
func WithTimeout(d time.Duration) OllamaOption {
	return func(p *OllamaProvider) {
		p.httpClient.Timeout = d
	}
}

// NewOllamaProvider creates an OllamaProvider.
//
// By default the base URL is read from the OLLAMA_BASE_URL environment
// variable, falling back to http://localhost:11434. Use WithBaseURL to
// override explicitly.
func NewOllamaProvider(opts ...OllamaOption) *OllamaProvider {
	baseURL := os.Getenv(ollamaEnvBaseURL)
	if baseURL == "" {
		baseURL = ollamaDefaultBaseURL
	}

	p := &OllamaProvider{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: ollamaDefaultTimeout,
		},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// ollamaGenerateRequest is the JSON body sent to /api/generate.
type ollamaGenerateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

// ollamaGenerateResponse is the JSON body returned by /api/generate (non-streaming).
type ollamaGenerateResponse struct {
	Response        string `json:"response"`
	Done            bool   `json:"done"`
	PromptEvalCount int64  `json:"prompt_eval_count"`
	EvalCount       int64  `json:"eval_count"`
	TotalDuration   int64  `json:"total_duration"` // nanoseconds
}

// Infer sends a prompt to the Ollama /api/generate endpoint and returns the
// response with cost metadata.
//
// The system prompt and query prompt are concatenated with a double newline
// separator into a single prompt string. This matches Ollama's single-prompt
// API model. For providers that support message roles (OpenAI-compat), a
// different Provider implementation would map these to system/user messages.
func (p *OllamaProvider) Infer(
	ctx context.Context, model, systemPrompt string, queryPrompt []byte,
) (*InferOutput, error) {
	// Concatenate system + query prompts.
	var prompt string
	if systemPrompt != "" {
		prompt = systemPrompt + "\n\n" + string(queryPrompt)
	} else {
		prompt = string(queryPrompt)
	}

	body, err := json.Marshal(ollamaGenerateRequest{
		Model:  model,
		Prompt: prompt,
		Stream: false,
	})
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var raw ollamaGenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}

	return &InferOutput{
		Output: []byte(strings.TrimSpace(raw.Response)),
		Cost: &CostMetadata{
			Model:        model,
			InputTokens:  raw.PromptEvalCount,
			OutputTokens: raw.EvalCount,
			DurationMs:   raw.TotalDuration / 1_000_000, // ns -> ms
			Extra:        map[string]any{"provider": "ollama"},
		},
	}, nil
}
