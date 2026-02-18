// Package ollama provides a minimal HTTP client for the Ollama API.
//
// It wraps the /api/generate endpoint for single-shot text generation.
// The client is configured via environment variables:
//
//	OLLAMA_BASE_URL  — Base URL of the Ollama server (default: http://localhost:11434)
package ollama

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
	defaultBaseURL = "http://localhost:11434"
	envBaseURL     = "OLLAMA_BASE_URL"
)

// Client is a minimal Ollama API client for text generation.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New creates a new Ollama client. It reads the base URL from the
// OLLAMA_BASE_URL environment variable, falling back to the default.
func New() *Client {
	baseURL := os.Getenv(envBaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

// generateRequest is the JSON body sent to /api/generate.
type generateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

// generateResponse is the JSON body returned by /api/generate (non-streaming).
type generateResponse struct {
	Response        string `json:"response"`
	Done            bool   `json:"done"`
	PromptEvalCount int64  `json:"prompt_eval_count"`
	EvalCount       int64  `json:"eval_count"`
	TotalDuration   int64  `json:"total_duration"` // nanoseconds
}

// GenerateResult holds the LLM response text alongside token and timing
// metadata extracted from the Ollama API response.
type GenerateResult struct {
	// Response is the generated text, trimmed of leading/trailing whitespace.
	Response string

	// PromptTokens is the number of tokens evaluated in the prompt
	// (prompt_eval_count from Ollama).
	PromptTokens int64

	// OutputTokens is the number of tokens generated in the response
	// (eval_count from Ollama).
	OutputTokens int64

	// DurationMs is the total wall-clock duration of the generation in
	// milliseconds, converted from Ollama's nanosecond total_duration.
	DurationMs int64
}

// Generate sends a prompt to the specified model and returns the complete
// response text. Streaming is disabled; the full response is returned at once.
func (c *Client) Generate(ctx context.Context, model, prompt string) (string, error) {
	result, err := c.GenerateRich(ctx, model, prompt)
	if err != nil {
		return "", err
	}
	return result.Response, nil
}

// GenerateRich sends a prompt to the specified model and returns the complete
// response along with token and timing metadata. Streaming is disabled;
// the full response is returned at once.
//
// Use this method when you need token counts and duration for telemetry
// (e.g. when wrapping inference in a FoundryAgent). For simple text
// generation, use Generate instead.
func (c *Client) GenerateRich(ctx context.Context, model, prompt string) (*GenerateResult, error) {
	body, err := json.Marshal(generateRequest{
		Model:  model,
		Prompt: prompt,
		Stream: false,
	})
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var raw generateResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}

	return &GenerateResult{
		Response:     strings.TrimSpace(raw.Response),
		PromptTokens: raw.PromptEvalCount,
		OutputTokens: raw.EvalCount,
		DurationMs:   raw.TotalDuration / 1_000_000, // ns → ms
	}, nil
}
