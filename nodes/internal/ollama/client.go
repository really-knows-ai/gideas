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
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

// Generate sends a prompt to the specified model and returns the complete
// response text. Streaming is disabled; the full response is returned at once.
func (c *Client) Generate(ctx context.Context, model, prompt string) (string, error) {
	body, err := json.Marshal(generateRequest{
		Model:  model,
		Prompt: prompt,
		Stream: false,
	})
	if err != nil {
		return "", fmt.Errorf("ollama: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("ollama: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var result generateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("ollama: decode response: %w", err)
	}

	return strings.TrimSpace(result.Response), nil
}
