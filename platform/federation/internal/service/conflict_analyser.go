package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

const (
	ollamaDefaultBaseURL = "http://localhost:11434"
	ollamaDefaultTimeout = 5 * time.Minute

	// conflictAnalysisSystemPrompt instructs the LLM to evaluate whether a
	// candidate law conflicts with a set of existing laws.
	conflictAnalysisSystemPrompt = `You are a legal conflict analyst for a governance system.
Your task is to determine whether a proposed (candidate) law conflicts with any of the existing laws provided.

A conflict exists when:
- The candidate law directly contradicts an existing law
- The candidate law creates ambiguity when applied alongside an existing law
- The candidate law renders an existing law unenforceable or redundant

You MUST respond with valid JSON matching this exact schema:
{
  "has_conflicts": boolean,
  "conflicting_law_ids": ["id1", "id2"],
  "remediation_text": "explanation of conflicts and suggested remediation"
}

If there are no conflicts, respond with:
{"has_conflicts": false, "conflicting_law_ids": [], "remediation_text": ""}

Be precise. Only flag actual conflicts, not merely related or overlapping laws.`
)

// ollamaConflictAnalyserReport is the JSON structure expected from the LLM.
type ollamaConflictAnalyserReport struct {
	HasConflicts      bool     `json:"has_conflicts"`
	ConflictingLawIDs []string `json:"conflicting_law_ids"`
	RemediationText   string   `json:"remediation_text"`
}

// OllamaConflictAnalyser implements ConflictAnalyser by calling an Ollama
// LLM endpoint to evaluate semantic matches. The Federation service is
// architecturally independent of the Flow SDK, so this uses the Ollama
// HTTP API directly rather than importing the SDK's provider abstraction.
type OllamaConflictAnalyser struct {
	baseURL    string
	model      string
	httpClient *http.Client
}

// OllamaConflictAnalyserOption configures an OllamaConflictAnalyser.
type OllamaConflictAnalyserOption func(*OllamaConflictAnalyser)

// WithOllamaBaseURL overrides the Ollama API base URL.
func WithOllamaBaseURL(url string) OllamaConflictAnalyserOption {
	return func(a *OllamaConflictAnalyser) {
		a.baseURL = strings.TrimRight(url, "/")
	}
}

// WithOllamaModel overrides the model name.
func WithOllamaModel(model string) OllamaConflictAnalyserOption {
	return func(a *OllamaConflictAnalyser) {
		a.model = model
	}
}

// NewOllamaConflictAnalyser creates a production ConflictAnalyser backed by
// an Ollama LLM endpoint.
func NewOllamaConflictAnalyser(opts ...OllamaConflictAnalyserOption) *OllamaConflictAnalyser {
	a := &OllamaConflictAnalyser{
		baseURL: ollamaDefaultBaseURL,
		model:   "kimi-k2.5:cloud",
		httpClient: &http.Client{
			Timeout: ollamaDefaultTimeout,
		},
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// AnalyseConflicts calls the LLM to evaluate whether the candidate law
// conflicts with any of the similar laws found by distributed search.
func (a *OllamaConflictAnalyser) AnalyseConflicts(
	ctx context.Context,
	candidateLaw *flowv1.Law,
	similarLaws []*flowv1.SimilarLaw,
) (*ConflictReport, error) {
	queryPrompt := buildConflictQueryPrompt(candidateLaw, similarLaws)

	prompt := conflictAnalysisSystemPrompt + "\n\n" + queryPrompt

	body, err := json.Marshal(ollamaGenerateRequest{
		Model:  a.model,
		Prompt: prompt,
		Stream: false,
	})
	if err != nil {
		return nil, fmt.Errorf("conflict analyser: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("conflict analyser: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("conflict analyser: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("conflict analyser: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var raw ollamaGenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("conflict analyser: decode response: %w", err)
	}

	// Parse the LLM's JSON output.
	output := strings.TrimSpace(raw.Response)
	var report ollamaConflictAnalyserReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return nil, fmt.Errorf("conflict analyser: parse LLM output: %w (raw: %s)", err, output)
	}

	return &ConflictReport{
		HasConflicts:      report.HasConflicts,
		ConflictingLawIDs: report.ConflictingLawIDs,
		RemediationText:   report.RemediationText,
	}, nil
}

// ollamaGenerateRequest is the JSON body sent to Ollama's /api/generate.
type ollamaGenerateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

// ollamaGenerateResponse is the JSON body returned by Ollama's /api/generate.
type ollamaGenerateResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

// buildConflictQueryPrompt builds the query portion of the LLM prompt from
// the candidate law and the list of similar laws.
func buildConflictQueryPrompt(candidateLaw *flowv1.Law, similarLaws []*flowv1.SimilarLaw) string {
	var b strings.Builder

	b.WriteString("## Candidate Law\n")
	b.WriteString(fmt.Sprintf("ID: %s\n", candidateLaw.GetId()))
	b.WriteString(fmt.Sprintf("Goal: %s\n", candidateLaw.GetGoal()))
	b.WriteString(fmt.Sprintf("Group: %s\n", candidateLaw.GetGroup()))
	b.WriteString(fmt.Sprintf("Tier: %s\n", candidateLaw.GetTier().String()))
	if len(candidateLaw.GetRepresentations()) > 0 {
		b.WriteString("Representations:\n")
		for _, r := range candidateLaw.GetRepresentations() {
			b.WriteString(fmt.Sprintf("  - Type: %s\n    Content: %s\n", r.GetType(), r.GetContent()))
		}
	}

	b.WriteString("\n## Existing Laws to Compare Against\n")
	for i, sl := range similarLaws {
		law := sl.GetLaw()
		if law == nil {
			continue
		}
		b.WriteString(fmt.Sprintf("\n### Existing Law %d (similarity: %.2f)\n", i+1, sl.GetSimilarityScore()))
		b.WriteString(fmt.Sprintf("ID: %s\n", law.GetId()))
		b.WriteString(fmt.Sprintf("Goal: %s\n", law.GetGoal()))
		b.WriteString(fmt.Sprintf("Group: %s\n", law.GetGroup()))
		b.WriteString(fmt.Sprintf("Tier: %s\n", law.GetTier().String()))
		if len(law.GetRepresentations()) > 0 {
			b.WriteString("Representations:\n")
			for _, r := range law.GetRepresentations() {
				b.WriteString(fmt.Sprintf("  - Type: %s\n    Content: %s\n", r.GetType(), r.GetContent()))
			}
		}
	}

	b.WriteString("\nAnalyse the candidate law against the existing laws and respond with valid JSON.")

	return b.String()
}
