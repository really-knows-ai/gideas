package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestAgent creates a FoundryAgent for schema validation tests. It uses
// a real TCP listener + no-op gRPC spy (same pattern as haiku-forge).
func newTestAgent(t *testing.T, schema []byte) *flow.Agent {
	t.Helper()
	client := newSpyClient(t)
	agent, err := flow.NewAgent(client, schema,
		flow.WithHeartbeatInterval(1<<62)) // effectively disable heartbeat ticking
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}
	return agent
}

// newSpyClient creates a flow.Client backed by a local TCP gRPC spy.
func newSpyClient(t *testing.T) *flow.Client {
	t.Helper()

	lis, err := newLocalListener()
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	spy := newAppraiseSpy()
	srv := newSpyGRPCServer(spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	client, err := flow.NewClient(flow.WithSidecarAddress(lis.Addr().String()))
	if err != nil {
		t.Fatalf("NewClient() failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}

// ---------------------------------------------------------------------------
// Tests: evalSchema validation
// ---------------------------------------------------------------------------

func TestEvalSchema_ValidAccept(t *testing.T) {
	agent := newTestAgent(t, evalSchema)

	validJSON := `{"verdict": "accept", "reason": "fix adequately addresses the issue"}`
	infer := staticInfer(validJSON)

	out, err := agent.Run(context.Background(), infer, nil)
	if err != nil {
		t.Fatalf("expected valid output to pass, got error: %v", err)
	}
	if string(out) != validJSON {
		t.Fatalf("output mismatch: got %s", out)
	}
}

func TestEvalSchema_ValidReject(t *testing.T) {
	agent := newTestAgent(t, evalSchema)

	validJSON := `{"verdict": "reject", "reason": "fix does not address the core issue"}`
	infer := staticInfer(validJSON)

	out, err := agent.Run(context.Background(), infer, nil)
	if err != nil {
		t.Fatalf("expected valid output to pass, got error: %v", err)
	}
	if string(out) != validJSON {
		t.Fatalf("output mismatch: got %s", out)
	}
}

func TestEvalSchema_RejectsInvalidVerdict(t *testing.T) {
	agent := newTestAgent(t, evalSchema)

	invalidJSON := `{"verdict": "maybe", "reason": "not sure"}`
	infer := staticInfer(invalidJSON)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected invalid verdict to fail schema validation")
	}
	if !strings.Contains(err.Error(), "output validation failed") {
		t.Fatalf("expected 'output validation failed' in error, got: %v", err)
	}
}

func TestEvalSchema_RejectsEmptyReason(t *testing.T) {
	agent := newTestAgent(t, evalSchema)

	emptyReason := `{"verdict": "accept", "reason": ""}`
	infer := staticInfer(emptyReason)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected empty reason to fail schema validation (minLength: 1)")
	}
}

func TestEvalSchema_RejectsMissingVerdict(t *testing.T) {
	agent := newTestAgent(t, evalSchema)

	missingVerdict := `{"reason": "some reason"}`
	infer := staticInfer(missingVerdict)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected missing verdict to fail schema validation")
	}
}

func TestEvalSchema_RejectsAdditionalProperties(t *testing.T) {
	agent := newTestAgent(t, evalSchema)

	extra := `{"verdict": "accept", "reason": "ok", "extra": "nope"}`
	infer := staticInfer(extra)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected additional properties to fail schema validation")
	}
}

// ---------------------------------------------------------------------------
// Tests: reviewSchema validation
// ---------------------------------------------------------------------------

func TestReviewSchema_ValidWithFeedback(t *testing.T) {
	agent := newTestAgent(t, reviewSchema)

	validJSON := `{"feedback": [{"message": "test issue", "severity": "medium", "cited_laws": ["law-1"]}]}`
	infer := staticInfer(validJSON)

	out, err := agent.Run(context.Background(), infer, nil)
	if err != nil {
		t.Fatalf("expected valid output to pass, got error: %v", err)
	}
	if string(out) != validJSON {
		t.Fatalf("output mismatch: got %s", out)
	}
}

func TestReviewSchema_ValidEmptyFeedback(t *testing.T) {
	agent := newTestAgent(t, reviewSchema)

	validJSON := `{"feedback": []}`
	infer := staticInfer(validJSON)

	out, err := agent.Run(context.Background(), infer, nil)
	if err != nil {
		t.Fatalf("expected empty feedback to pass, got error: %v", err)
	}
	if string(out) != validJSON {
		t.Fatalf("output mismatch: got %s", out)
	}
}

func TestReviewSchema_RejectsInvalidSeverity(t *testing.T) {
	agent := newTestAgent(t, reviewSchema)

	invalidSev := `{"feedback": [{"message": "test", "severity": "extreme", "cited_laws": []}]}`
	infer := staticInfer(invalidSev)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected invalid severity to fail schema validation")
	}
}

func TestReviewSchema_RejectsMissingSeverity(t *testing.T) {
	agent := newTestAgent(t, reviewSchema)

	missingSev := `{"feedback": [{"message": "test", "cited_laws": []}]}`
	infer := staticInfer(missingSev)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected missing severity to fail schema validation")
	}
}

func TestReviewSchema_RejectsMissingMessage(t *testing.T) {
	agent := newTestAgent(t, reviewSchema)

	missingMsg := `{"feedback": [{"severity": "low", "cited_laws": []}]}`
	infer := staticInfer(missingMsg)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected missing message to fail schema validation")
	}
}

func TestReviewSchema_RejectsAdditionalItemProperties(t *testing.T) {
	agent := newTestAgent(t, reviewSchema)

	extra := `{"feedback": [{"message": "test", "severity": "low", "cited_laws": [], "extra": "nope"}]}`
	infer := staticInfer(extra)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected additional properties in item to fail schema validation")
	}
}

func TestReviewSchema_RejectsAdditionalTopLevelProperties(t *testing.T) {
	agent := newTestAgent(t, reviewSchema)

	extra := `{"feedback": [], "notes": "extra"}`
	infer := staticInfer(extra)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected additional top-level properties to fail schema validation")
	}
}

// ---------------------------------------------------------------------------
// Tests: parseSeverity
// ---------------------------------------------------------------------------

func TestParseSeverity_AllValues(t *testing.T) {
	tests := []struct {
		input string
		want  flowv1.Severity
	}{
		{"low", flowv1.Severity_SEVERITY_LOW},
		{"medium", flowv1.Severity_SEVERITY_MEDIUM},
		{"high", flowv1.Severity_SEVERITY_HIGH},
		{"critical", flowv1.Severity_SEVERITY_CRITICAL},
		{"LOW", flowv1.Severity_SEVERITY_LOW},        // case-insensitive
		{"HIGH", flowv1.Severity_SEVERITY_HIGH},      // case-insensitive
		{"unknown", flowv1.Severity_SEVERITY_MEDIUM}, // default
		{"", flowv1.Severity_SEVERITY_MEDIUM},        // default
	}

	for _, tt := range tests {
		got := parseSeverity(tt.input)
		if got != tt.want {
			t.Errorf("parseSeverity(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests: makeInferFunc with mock Ollama
// ---------------------------------------------------------------------------

func TestMakeInferFunc_ReturnsCorrectResult(t *testing.T) {
	ollamaResp := map[string]any{
		"response":          `{"feedback": []}`,
		"done":              true,
		"prompt_eval_count": 100,
		"eval_count":        30,
		"total_duration":    2_000_000_000,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			http.NotFound(w, r)
			return
		}
		var reqBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if reqBody["model"] != "test-model" {
			t.Errorf("expected model 'test-model', got %v", reqBody["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ollamaResp)
	}))
	defer srv.Close()

	t.Setenv("OLLAMA_BASE_URL", srv.URL)

	inferFn := makeInferFunc("test-model")
	result, err := inferFn(context.Background(), []byte("review this haiku"))
	if err != nil {
		t.Fatalf("inferFn returned error: %v", err)
	}

	expectedOutput := `{"feedback": []}`
	if string(result.Output) != expectedOutput {
		t.Fatalf("output mismatch:\ngot:  %s\nwant: %s", result.Output, expectedOutput)
	}
	if result.Model != "test-model" {
		t.Fatalf("expected model 'test-model', got %q", result.Model)
	}
	if result.InputTokens != 100 {
		t.Fatalf("expected InputTokens=100, got %d", result.InputTokens)
	}
	if result.OutputTokens != 30 {
		t.Fatalf("expected OutputTokens=30, got %d", result.OutputTokens)
	}
	if result.DurationMs != 2000 {
		t.Fatalf("expected DurationMs=2000, got %d", result.DurationMs)
	}
	if result.Extra["provider"] != "ollama" {
		t.Fatalf("expected Extra[provider]=ollama, got %v", result.Extra["provider"])
	}
}

func TestMakeInferFunc_OllamaError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	t.Setenv("OLLAMA_BASE_URL", srv.URL)

	inferFn := makeInferFunc("nonexistent-model")
	_, err := inferFn(context.Background(), []byte("prompt"))
	if err == nil {
		t.Fatal("expected error when Ollama returns non-200")
	}
	if !strings.Contains(err.Error(), "ollama generate") {
		t.Fatalf("expected 'ollama generate' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: evaluateFeedback — parallel fix/refusal evaluation
// ---------------------------------------------------------------------------

func TestEvaluateFeedback_AcceptFix(t *testing.T) {
	spy := newAppraiseSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, evalSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	// Mock Ollama to return "accept" verdict.
	ollamaSrv := mockOllamaWithResponse(t, `{"verdict": "accept", "reason": "fix looks good"}`)
	defer ollamaSrv.Close()
	t.Setenv("OLLAMA_BASE_URL", ollamaSrv.URL)

	feedback := []*flowv1.FeedbackItem{
		{
			Id:       "fb-act-1",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
			Message:  "syllable count wrong",
			Severity: flowv1.Severity_SEVERITY_MEDIUM,
		},
	}

	_, err = evaluateFeedback(
		context.Background(), agent, makeInferFunc("test"),
		client, feedback, "write a haiku", "test haiku",
	)
	if err != nil {
		t.Fatalf("evaluateFeedback() error: %v", err)
	}

	if len(spy.AcceptedFixes) != 1 || spy.AcceptedFixes[0] != "fb-act-1" {
		t.Fatalf("expected AcceptFix for fb-act-1, got: %v", spy.AcceptedFixes)
	}
	if len(spy.RejectedFixes) != 0 {
		t.Fatalf("expected no rejected fixes, got: %v", spy.RejectedFixes)
	}
}

func TestEvaluateFeedback_RejectFix(t *testing.T) {
	spy := newAppraiseSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, evalSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	ollamaSrv := mockOllamaWithResponse(t, `{"verdict": "reject", "reason": "fix misses the point"}`)
	defer ollamaSrv.Close()
	t.Setenv("OLLAMA_BASE_URL", ollamaSrv.URL)

	feedback := []*flowv1.FeedbackItem{
		{
			Id:       "fb-act-2",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
			Message:  "theme doesn't match",
			Severity: flowv1.Severity_SEVERITY_HIGH,
		},
	}

	_, err = evaluateFeedback(
		context.Background(), agent, makeInferFunc("test"),
		client, feedback, "write a haiku", "test haiku",
	)
	if err != nil {
		t.Fatalf("evaluateFeedback() error: %v", err)
	}

	if len(spy.AcceptedFixes) != 0 {
		t.Fatalf("expected no accepted fixes, got: %v", spy.AcceptedFixes)
	}
	msg, ok := spy.RejectedFixes["fb-act-2"]
	if !ok {
		t.Fatal("expected RejectFix for fb-act-2")
	}
	if msg != "fix misses the point" {
		t.Fatalf("expected rejection message 'fix misses the point', got %q", msg)
	}
}

func TestEvaluateFeedback_AcceptRefusal(t *testing.T) {
	spy := newAppraiseSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, evalSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	ollamaSrv := mockOllamaWithResponse(t, `{"verdict": "accept", "reason": "refusal is well justified"}`)
	defer ollamaSrv.Close()
	t.Setenv("OLLAMA_BASE_URL", ollamaSrv.URL)

	feedback := []*flowv1.FeedbackItem{
		{
			Id:       "fb-wf-1",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
			Message:  "use different imagery",
			Severity: flowv1.Severity_SEVERITY_LOW,
			Justification: &flowv1.Justification{
				Kind: &flowv1.Justification_NovelArgument{
					NovelArgument: &flowv1.NovelArgument{
						Argument: "the imagery is central to the petition's theme",
					},
				},
			},
		},
	}

	_, err = evaluateFeedback(
		context.Background(), agent, makeInferFunc("test"),
		client, feedback, "write a haiku", "test haiku",
	)
	if err != nil {
		t.Fatalf("evaluateFeedback() error: %v", err)
	}

	if len(spy.AcceptedRefusals) != 1 || spy.AcceptedRefusals[0] != "fb-wf-1" {
		t.Fatalf("expected AcceptRefusal for fb-wf-1, got: %v", spy.AcceptedRefusals)
	}
}

func TestEvaluateFeedback_RejectRefusal(t *testing.T) {
	spy := newAppraiseSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, evalSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	ollamaSrv := mockOllamaWithResponse(t, `{"verdict": "reject", "reason": "justification is weak"}`)
	defer ollamaSrv.Close()
	t.Setenv("OLLAMA_BASE_URL", ollamaSrv.URL)

	feedback := []*flowv1.FeedbackItem{
		{
			Id:       "fb-wf-2",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
			Message:  "consider seasonal imagery",
			Severity: flowv1.Severity_SEVERITY_MEDIUM,
		},
	}

	_, err = evaluateFeedback(
		context.Background(), agent, makeInferFunc("test"),
		client, feedback, "write a haiku", "test haiku",
	)
	if err != nil {
		t.Fatalf("evaluateFeedback() error: %v", err)
	}

	msg, ok := spy.RejectedRefusals["fb-wf-2"]
	if !ok {
		t.Fatal("expected RejectRefusal for fb-wf-2")
	}
	if msg != "justification is weak" {
		t.Fatalf("expected rejection message 'justification is weak', got %q", msg)
	}
}

func TestEvaluateFeedback_SkipsNonEvaluableStates(t *testing.T) {
	spy := newAppraiseSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, evalSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	feedback := []*flowv1.FeedbackItem{
		{Id: "fb-new", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW, Message: "new item"},
		{Id: "fb-resolved", State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED, Message: "resolved"},
		{Id: "fb-rejected", State: flowv1.FeedbackState_FEEDBACK_STATE_REJECTED, Message: "rejected"},
	}

	// No Ollama server needed — no inference calls should be made.
	_, err = evaluateFeedback(context.Background(), agent, makeInferFunc("test"), client, feedback, "petition", "haiku")
	if err != nil {
		t.Fatalf("evaluateFeedback() error: %v", err)
	}

	if len(spy.AcceptedFixes) != 0 || len(spy.RejectedFixes) != 0 ||
		len(spy.AcceptedRefusals) != 0 || len(spy.RejectedRefusals) != 0 {
		t.Fatal("expected no feedback operations for non-evaluable states")
	}
}

func TestEvaluateFeedback_ParallelMultipleItems(t *testing.T) {
	spy := newAppraiseSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, evalSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	// All get the same "accept" response.
	ollamaSrv := mockOllamaWithResponse(t, `{"verdict": "accept", "reason": "looks good"}`)
	defer ollamaSrv.Close()
	t.Setenv("OLLAMA_BASE_URL", ollamaSrv.URL)

	feedback := []*flowv1.FeedbackItem{
		{Id: "fb-1", State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED, Message: "issue 1"},
		{Id: "fb-2", State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED, Message: "issue 2"},
		{Id: "fb-3", State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX, Message: "issue 3"},
	}

	_, err = evaluateFeedback(context.Background(), agent, makeInferFunc("test"), client, feedback, "petition", "haiku")
	if err != nil {
		t.Fatalf("evaluateFeedback() error: %v", err)
	}

	if len(spy.AcceptedFixes) != 2 {
		t.Fatalf("expected 2 accepted fixes, got %d: %v", len(spy.AcceptedFixes), spy.AcceptedFixes)
	}
	if len(spy.AcceptedRefusals) != 1 {
		t.Fatalf("expected 1 accepted refusal, got %d: %v", len(spy.AcceptedRefusals), spy.AcceptedRefusals)
	}
}

// ---------------------------------------------------------------------------
// Tests: reviewOutput unmarshalling
// ---------------------------------------------------------------------------

func TestReviewOutput_Unmarshal(t *testing.T) {
	raw := `{"feedback": [{"message": "test", "severity": "high", "cited_laws": ["law-1", "law-2"]}]}`
	var out reviewOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(out.Feedback) != 1 {
		t.Fatalf("expected 1 feedback item, got %d", len(out.Feedback))
	}
	if out.Feedback[0].Message != "test" {
		t.Fatalf("expected message 'test', got %q", out.Feedback[0].Message)
	}
	if out.Feedback[0].Severity != "high" {
		t.Fatalf("expected severity 'high', got %q", out.Feedback[0].Severity)
	}
	if len(out.Feedback[0].CitedLaws) != 2 {
		t.Fatalf("expected 2 cited laws, got %d", len(out.Feedback[0].CitedLaws))
	}
}

func TestEvalOutput_Unmarshal(t *testing.T) {
	raw := `{"verdict": "reject", "reason": "not good enough"}`
	var out evalOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if out.Verdict != "reject" {
		t.Fatalf("expected verdict 'reject', got %q", out.Verdict)
	}
	if out.Reason != "not good enough" {
		t.Fatalf("expected reason 'not good enough', got %q", out.Reason)
	}
}

// ---------------------------------------------------------------------------
// Tests: prompt builders
// ---------------------------------------------------------------------------

func TestBuildEvalPrompt_ContainsContext(t *testing.T) {
	fb := &flowv1.FeedbackItem{
		Id:       "fb-1",
		Message:  "syllable count is wrong",
		Severity: flowv1.Severity_SEVERITY_MEDIUM,
		History: []*flowv1.FeedbackEvent{
			{Actor: "refine", Action: "resolve", Message: "fixed the syllables"},
		},
	}

	prompt := buildEvalPrompt(fb, "write about autumn", "autumn leaves fall", "actioned")
	if !strings.Contains(prompt, "write about autumn") {
		t.Error("prompt should contain petition")
	}
	if !strings.Contains(prompt, "autumn leaves fall") {
		t.Error("prompt should contain haiku")
	}
	if !strings.Contains(prompt, "syllable count is wrong") {
		t.Error("prompt should contain original feedback message")
	}
	if !strings.Contains(prompt, "fixed the syllables") {
		t.Error("prompt should contain history")
	}
	if !strings.Contains(prompt, "FIXED") {
		t.Error("prompt should contain actioned instruction")
	}
}

func TestBuildEvalPrompt_WontFix_ContainsJustification(t *testing.T) {
	fb := &flowv1.FeedbackItem{
		Id:       "fb-1",
		Message:  "use seasonal imagery",
		Severity: flowv1.Severity_SEVERITY_LOW,
		Justification: &flowv1.Justification{
			Kind: &flowv1.Justification_NovelArgument{
				NovelArgument: &flowv1.NovelArgument{
					Argument: "the imagery directly serves the petition",
				},
			},
		},
	}

	prompt := buildEvalPrompt(fb, "write about autumn", "autumn leaves fall", "wont_fix")
	if !strings.Contains(prompt, "REFUSED") {
		t.Error("prompt should contain wont_fix instruction")
	}
	if !strings.Contains(prompt, "the imagery directly serves the petition") {
		t.Error("prompt should contain justification")
	}
}

func TestBuildReviewPrompt_ContainsLawsAndHistory(t *testing.T) {
	laws := []*flowv1.Law{
		{Id: "law-1", Tier: 1, Goal: "use seasonal imagery"},
		{Id: "law-2", Tier: 1, Goal: "exactly 5-7-5 syllables"},
	}
	feedback := []*flowv1.FeedbackItem{
		{State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED, Message: "old issue resolved"},
	}

	prompt := buildReviewPrompt("write about autumn", "autumn leaves fall", laws, feedback)
	if !strings.Contains(prompt, "write about autumn") {
		t.Error("prompt should contain petition")
	}
	if !strings.Contains(prompt, "autumn leaves fall") {
		t.Error("prompt should contain haiku")
	}
	if !strings.Contains(prompt, "law-1") {
		t.Error("prompt should contain law IDs")
	}
	if !strings.Contains(prompt, "use seasonal imagery") {
		t.Error("prompt should contain law goals")
	}
	if !strings.Contains(prompt, "old issue resolved") {
		t.Error("prompt should contain feedback history")
	}
	if !strings.Contains(prompt, "Do NOT re-raise resolved items") {
		t.Error("prompt should instruct not to re-raise resolved items")
	}
}

func TestBuildReviewPrompt_NoLawsOrFeedback(t *testing.T) {
	prompt := buildReviewPrompt("write about autumn", "autumn leaves fall", nil, nil)
	if !strings.Contains(prompt, "write about autumn") {
		t.Error("prompt should contain petition")
	}
	if strings.Contains(prompt, "GOVERNANCE LAWS") {
		t.Error("prompt should not contain laws section when no laws")
	}
	if strings.Contains(prompt, "PREVIOUS FEEDBACK HISTORY") {
		t.Error("prompt should not contain history section when no feedback")
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// staticInfer returns an InferFunc that always returns the given JSON string.
func staticInfer(jsonStr string) flow.InferFunc {
	return func(_ context.Context, _ []byte) (*flow.InferResult, error) {
		return &flow.InferResult{
			Output:       []byte(jsonStr),
			Model:        "test",
			InputTokens:  1,
			OutputTokens: 1,
			DurationMs:   1,
		}, nil
	}
}

// mockOllamaWithResponse creates an httptest server that returns the given
// response string from the Ollama /api/generate endpoint.
func mockOllamaWithResponse(t *testing.T, response string) *httptest.Server {
	t.Helper()
	ollamaResp := map[string]any{
		"response":          response,
		"done":              true,
		"prompt_eval_count": 50,
		"eval_count":        20,
		"total_duration":    1_000_000_000,
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ollamaResp)
	}))
}

// newSpyClientWithSpy creates a flow.Client and returns the spy for assertions.
// Returns a cleanup function.
func newSpyClientWithSpy(t *testing.T, spy *appraiseSpy) (*flow.Client, func()) {
	t.Helper()

	lis, err := newLocalListener()
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	srv := newSpyGRPCServer(spy)
	go func() { _ = srv.Serve(lis) }()

	client, err := flow.NewClient(flow.WithSidecarAddress(lis.Addr().String()))
	if err != nil {
		t.Fatalf("NewClient() failed: %v", err)
	}

	cleanup := func() {
		_ = client.Close()
		srv.GracefulStop()
	}
	return client, cleanup
}

// ---------------------------------------------------------------------------
// Tests: findingSchema validation
// ---------------------------------------------------------------------------

func TestFindingSchema_ValidSingleFinding(t *testing.T) {
	agent := newTestAgent(t, findingSchema)

	validJSON := `{"findings": [` +
		`{"goal": "Haiku should evoke rather than state",` +
		` "applies_to": ["haiku"],` +
		` "rationale": "The discussion revealed that..."}` +
		`]}`
	infer := staticInfer(validJSON)

	out, err := agent.Run(context.Background(), infer, nil)
	if err != nil {
		t.Fatalf("expected valid output, got error: %v", err)
	}
	if string(out) != validJSON {
		t.Fatalf("output mismatch: got %s", out)
	}
}

func TestFindingSchema_ValidEmptyFindings(t *testing.T) {
	agent := newTestAgent(t, findingSchema)

	validJSON := `{"findings": []}`
	infer := staticInfer(validJSON)

	out, err := agent.Run(context.Background(), infer, nil)
	if err != nil {
		t.Fatalf("expected empty findings to pass, got error: %v", err)
	}
	if string(out) != validJSON {
		t.Fatalf("output mismatch: got %s", out)
	}
}

func TestFindingSchema_RejectsEmptyGoal(t *testing.T) {
	agent := newTestAgent(t, findingSchema)

	emptyGoal := `{"findings": [` +
		`{"goal": "", "applies_to": ["haiku"],` +
		` "rationale": "reason"}]}`
	infer := staticInfer(emptyGoal)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected empty goal to fail schema validation")
	}
}

func TestFindingSchema_RejectsEmptyAppliesTo(t *testing.T) {
	agent := newTestAgent(t, findingSchema)

	emptyArr := `{"findings": [` +
		`{"goal": "test", "applies_to": [],` +
		` "rationale": "reason"}]}`
	infer := staticInfer(emptyArr)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected empty applies_to to fail (minItems: 1)")
	}
}

func TestFindingSchema_RejectsMissingRationale(t *testing.T) {
	agent := newTestAgent(t, findingSchema)

	missing := `{"findings": [` +
		`{"goal": "test", "applies_to": ["haiku"]}]}`
	infer := staticInfer(missing)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected missing rationale to fail")
	}
}

func TestFindingSchema_RejectsAdditionalProperties(t *testing.T) {
	agent := newTestAgent(t, findingSchema)

	extra := `{"findings": [` +
		`{"goal": "test", "applies_to": ["haiku"],` +
		` "rationale": "reason", "extra": "nope"}]}`
	infer := staticInfer(extra)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected additional properties to fail")
	}
}

// ---------------------------------------------------------------------------
// Tests: findingsOutput unmarshalling
// ---------------------------------------------------------------------------

func TestFindingsOutput_Unmarshal(t *testing.T) {
	raw := `{"findings": [` +
		`{"goal": "test goal",` +
		` "applies_to": ["haiku", "petition"],` +
		` "rationale": "test rationale"}]}`
	var out findingsOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(out.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(out.Findings))
	}
	f := out.Findings[0]
	if f.Goal != "test goal" {
		t.Fatalf("expected goal 'test goal', got %q", f.Goal)
	}
	if len(f.AppliesTo) != 2 {
		t.Fatalf("expected 2 applies_to, got %d", len(f.AppliesTo))
	}
	if f.Rationale != "test rationale" {
		t.Fatalf("expected rationale 'test rationale', got %q",
			f.Rationale)
	}
}

// ---------------------------------------------------------------------------
// Tests: hasNovelArgument
// ---------------------------------------------------------------------------

func TestHasNovelArgument_True(t *testing.T) {
	fb := &flowv1.FeedbackItem{
		Justification: &flowv1.Justification{
			Kind: &flowv1.Justification_NovelArgument{
				NovelArgument: &flowv1.NovelArgument{
					Argument: "this is novel",
				},
			},
		},
	}
	if !hasNovelArgument(fb) {
		t.Fatal("expected hasNovelArgument to return true")
	}
}

func TestHasNovelArgument_FalseForCitation(t *testing.T) {
	fb := &flowv1.FeedbackItem{
		Justification: &flowv1.Justification{
			Kind: &flowv1.Justification_Citation{
				Citation: &flowv1.Citation{
					CitationIds: []string{"law-1"},
				},
			},
		},
	}
	if hasNovelArgument(fb) {
		t.Fatal("expected hasNovelArgument to return false for citation")
	}
}

func TestHasNovelArgument_FalseForNil(t *testing.T) {
	fb := &flowv1.FeedbackItem{}
	if hasNovelArgument(fb) {
		t.Fatal("expected hasNovelArgument to return false for nil")
	}
}

func TestHasNovelArgument_FalseForEmptyArgument(t *testing.T) {
	fb := &flowv1.FeedbackItem{
		Justification: &flowv1.Justification{
			Kind: &flowv1.Justification_NovelArgument{
				NovelArgument: &flowv1.NovelArgument{
					Argument: "",
				},
			},
		},
	}
	if hasNovelArgument(fb) {
		t.Fatal("expected hasNovelArgument to return false " +
			"for empty argument")
	}
}

// ---------------------------------------------------------------------------
// Tests: buildFindingPrompt
// ---------------------------------------------------------------------------

func TestBuildFindingPrompt_ContainsAllDiscussions(t *testing.T) {
	items := []*flowv1.FeedbackItem{
		{
			Message:  "imagery is too direct",
			Severity: flowv1.Severity_SEVERITY_MEDIUM,
			State:    flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
			Justification: &flowv1.Justification{
				Kind: &flowv1.Justification_NovelArgument{
					NovelArgument: &flowv1.NovelArgument{
						Argument: "directness serves the petition",
					},
				},
			},
			History: []*flowv1.FeedbackEvent{
				{
					Actor:   "refine",
					Action:  "refuse",
					Message: "refusing to change imagery",
				},
			},
		},
		{
			Message:  "lacks seasonal reference",
			Severity: flowv1.Severity_SEVERITY_LOW,
			State:    flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
			Justification: &flowv1.Justification{
				Kind: &flowv1.Justification_NovelArgument{
					NovelArgument: &flowv1.NovelArgument{
						Argument: "seasonal references add depth",
					},
				},
			},
		},
	}

	prompt := buildFindingPrompt(items)

	// Should contain both discussions.
	if !strings.Contains(prompt, "Discussion 1") {
		t.Error("prompt should contain Discussion 1")
	}
	if !strings.Contains(prompt, "Discussion 2") {
		t.Error("prompt should contain Discussion 2")
	}

	// Should contain the original feedback messages.
	if !strings.Contains(prompt, "imagery is too direct") {
		t.Error("prompt should contain first feedback message")
	}
	if !strings.Contains(prompt, "lacks seasonal reference") {
		t.Error("prompt should contain second feedback message")
	}

	// Should contain the novel arguments.
	if !strings.Contains(prompt, "directness serves the petition") {
		t.Error("prompt should contain first novel argument")
	}
	if !strings.Contains(prompt, "seasonal references add depth") {
		t.Error("prompt should contain second novel argument")
	}

	// Should contain history.
	if !strings.Contains(prompt, "refusing to change imagery") {
		t.Error("prompt should contain history message")
	}

	// Should contain resolution paths.
	if !strings.Contains(prompt, "Refusal was accepted") {
		t.Error("prompt should contain wont_fix resolution path")
	}
	if !strings.Contains(prompt, "Fix was accepted") {
		t.Error("prompt should contain actioned resolution path")
	}

	// Should contain the core instruction.
	if !strings.Contains(prompt, "capture the learnings") {
		t.Error("prompt should contain learning capture instruction")
	}
}

func TestBuildFindingPrompt_SingleItem(t *testing.T) {
	items := []*flowv1.FeedbackItem{
		{
			Message:  "test feedback",
			Severity: flowv1.Severity_SEVERITY_HIGH,
			State:    flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
			Justification: &flowv1.Justification{
				Kind: &flowv1.Justification_NovelArgument{
					NovelArgument: &flowv1.NovelArgument{
						Argument: "novel insight",
					},
				},
			},
		},
	}

	prompt := buildFindingPrompt(items)
	if !strings.Contains(prompt, "Discussion 1") {
		t.Error("prompt should contain Discussion 1")
	}
	if strings.Contains(prompt, "Discussion 2") {
		t.Error("prompt should NOT contain Discussion 2")
	}
}

// ---------------------------------------------------------------------------
// Tests: mintFindings
// ---------------------------------------------------------------------------

func TestMintFindings_RecordsFindings(t *testing.T) {
	spy := newAppraiseSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, findingSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	findingJSON := `{"findings": [` +
		`{"goal": "Haiku should evoke emotion",` +
		` "applies_to": ["haiku"],` +
		` "rationale": "Discussion showed that..."}` +
		`]}`
	ollamaSrv := mockOllamaWithResponse(t, findingJSON)
	defer ollamaSrv.Close()
	t.Setenv("OLLAMA_BASE_URL", ollamaSrv.URL)

	items := []*flowv1.FeedbackItem{
		{
			Message:  "imagery is too direct",
			Severity: flowv1.Severity_SEVERITY_MEDIUM,
			State:    flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
			Justification: &flowv1.Justification{
				Kind: &flowv1.Justification_NovelArgument{
					NovelArgument: &flowv1.NovelArgument{
						Argument: "directness serves the petition",
					},
				},
			},
		},
	}

	err = mintFindings(
		context.Background(), agent,
		makeInferFunc("test"), client, items)
	if err != nil {
		t.Fatalf("mintFindings() error: %v", err)
	}

	if len(spy.RecordedFindings) != 1 {
		t.Fatalf("expected 1 recorded finding, got %d",
			len(spy.RecordedFindings))
	}
	f := spy.RecordedFindings[0]
	if f.Goal != "Haiku should evoke emotion" {
		t.Errorf("expected goal 'Haiku should evoke emotion', "+
			"got %q", f.Goal)
	}
	if len(f.AppliesTo) != 1 || f.AppliesTo[0] != "haiku" {
		t.Errorf("expected applies_to [haiku], got %v", f.AppliesTo)
	}
	if len(f.Representations) != 1 {
		t.Fatalf("expected 1 representation, got %d",
			len(f.Representations))
	}
	if f.Representations[0].GetType() != "text/markdown" {
		t.Errorf("expected type text/markdown, got %q",
			f.Representations[0].GetType())
	}
	if f.Representations[0].GetContent() != "Discussion showed that..." {
		t.Errorf("expected rationale content, got %q",
			f.Representations[0].GetContent())
	}
}

func TestMintFindings_MultipleFindings(t *testing.T) {
	spy := newAppraiseSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, findingSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	findingJSON := `{"findings": [` +
		`{"goal": "Finding one",` +
		` "applies_to": ["haiku"],` +
		` "rationale": "Reason one"},` +
		`{"goal": "Finding two",` +
		` "applies_to": ["haiku", "petition"],` +
		` "rationale": "Reason two"}` +
		`]}`
	ollamaSrv := mockOllamaWithResponse(t, findingJSON)
	defer ollamaSrv.Close()
	t.Setenv("OLLAMA_BASE_URL", ollamaSrv.URL)

	items := []*flowv1.FeedbackItem{
		{
			Message: "fb1",
			State:   flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
			Justification: &flowv1.Justification{
				Kind: &flowv1.Justification_NovelArgument{
					NovelArgument: &flowv1.NovelArgument{
						Argument: "arg1",
					},
				},
			},
		},
	}

	err = mintFindings(
		context.Background(), agent,
		makeInferFunc("test"), client, items)
	if err != nil {
		t.Fatalf("mintFindings() error: %v", err)
	}

	if len(spy.RecordedFindings) != 2 {
		t.Fatalf("expected 2 recorded findings, got %d",
			len(spy.RecordedFindings))
	}
	if spy.RecordedFindings[0].Goal != "Finding one" {
		t.Errorf("first finding goal mismatch: %q",
			spy.RecordedFindings[0].Goal)
	}
	if spy.RecordedFindings[1].Goal != "Finding two" {
		t.Errorf("second finding goal mismatch: %q",
			spy.RecordedFindings[1].Goal)
	}
}

func TestMintFindings_EmptyFindings_NoRecordCalls(t *testing.T) {
	spy := newAppraiseSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, findingSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	ollamaSrv := mockOllamaWithResponse(t, `{"findings": []}`)
	defer ollamaSrv.Close()
	t.Setenv("OLLAMA_BASE_URL", ollamaSrv.URL)

	items := []*flowv1.FeedbackItem{
		{
			Message: "fb1",
			State:   flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
			Justification: &flowv1.Justification{
				Kind: &flowv1.Justification_NovelArgument{
					NovelArgument: &flowv1.NovelArgument{
						Argument: "arg",
					},
				},
			},
		},
	}

	err = mintFindings(
		context.Background(), agent,
		makeInferFunc("test"), client, items)
	if err != nil {
		t.Fatalf("mintFindings() error: %v", err)
	}

	if len(spy.RecordedFindings) != 0 {
		t.Fatalf("expected 0 recorded findings, got %d",
			len(spy.RecordedFindings))
	}
}

func TestMintFindings_NilItems_NoOp(t *testing.T) {
	spy := newAppraiseSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, findingSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	// No Ollama server needed — should short-circuit.
	err = mintFindings(
		context.Background(), agent,
		makeInferFunc("test"), client, nil)
	if err != nil {
		t.Fatalf("mintFindings() error: %v", err)
	}

	if len(spy.RecordedFindings) != 0 {
		t.Fatalf("expected 0 recorded findings, got %d",
			len(spy.RecordedFindings))
	}
}

// ---------------------------------------------------------------------------
// Tests: evaluateFeedback returns novel-argument items
// ---------------------------------------------------------------------------

func TestEvaluateFeedback_ReturnsNovelResolvedItems(t *testing.T) {
	cases := []struct {
		name     string
		fbID     string
		state    flowv1.FeedbackState
		reason   string
		argument string
	}{
		{
			name:     "accept_refusal",
			fbID:     "fb-wf-novel",
			state:    flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
			reason:   "justified",
			argument: "imagery serves the petition",
		},
		{
			name:     "accept_fix",
			fbID:     "fb-act-novel",
			state:    flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
			reason:   "fix works",
			argument: "seasonal refs add depth",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := newAppraiseSpy()
			client, cleanup := newSpyClientWithSpy(t, spy)
			defer cleanup()

			agent, err := flow.NewAgent(client, evalSchema,
				flow.WithHeartbeatInterval(1<<62))
			if err != nil {
				t.Fatalf("NewAgent() failed: %v", err)
			}

			verdict := fmt.Sprintf(
				`{"verdict": "accept", "reason": %q}`,
				tc.reason)
			ollamaSrv := mockOllamaWithResponse(t, verdict)
			defer ollamaSrv.Close()
			t.Setenv("OLLAMA_BASE_URL", ollamaSrv.URL)

			feedback := []*flowv1.FeedbackItem{
				{
					Id:       tc.fbID,
					State:    tc.state,
					Message:  "test feedback",
					Severity: flowv1.Severity_SEVERITY_MEDIUM,
					Justification: &flowv1.Justification{
						Kind: &flowv1.Justification_NovelArgument{
							NovelArgument: &flowv1.NovelArgument{
								Argument: tc.argument,
							},
						},
					},
				},
			}

			resolved, err := evaluateFeedback(
				context.Background(), agent,
				makeInferFunc("test"),
				client, feedback, "petition", "haiku")
			if err != nil {
				t.Fatalf("evaluateFeedback() error: %v", err)
			}

			if len(resolved) != 1 {
				t.Fatalf("expected 1 novel resolved, got %d",
					len(resolved))
			}
			if resolved[0].GetId() != tc.fbID {
				t.Errorf("expected %s, got %s",
					tc.fbID, resolved[0].GetId())
			}
		})
	}
}

func TestEvaluateFeedback_NoNovelItems_ReturnsNil(t *testing.T) {
	spy := newAppraiseSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, evalSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	ollamaSrv := mockOllamaWithResponse(t,
		`{"verdict": "accept", "reason": "ok"}`)
	defer ollamaSrv.Close()
	t.Setenv("OLLAMA_BASE_URL", ollamaSrv.URL)

	// ACTIONED item with NO justification.
	feedback := []*flowv1.FeedbackItem{
		{
			Id:       "fb-act-plain",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
			Message:  "syllable count wrong",
			Severity: flowv1.Severity_SEVERITY_HIGH,
		},
	}

	resolved, err := evaluateFeedback(
		context.Background(), agent, makeInferFunc("test"),
		client, feedback, "petition", "haiku")
	if err != nil {
		t.Fatalf("evaluateFeedback() error: %v", err)
	}

	if len(resolved) != 0 {
		t.Fatalf("expected 0 novel resolved items, got %d",
			len(resolved))
	}
}

func TestEvaluateFeedback_CitationNotNovel(t *testing.T) {
	spy := newAppraiseSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, evalSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	ollamaSrv := mockOllamaWithResponse(t,
		`{"verdict": "accept", "reason": "citation valid"}`)
	defer ollamaSrv.Close()
	t.Setenv("OLLAMA_BASE_URL", ollamaSrv.URL)

	// WONT_FIX with citation justification — should NOT be novel.
	feedback := []*flowv1.FeedbackItem{
		{
			Id:       "fb-wf-citation",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
			Message:  "test",
			Severity: flowv1.Severity_SEVERITY_LOW,
			Justification: &flowv1.Justification{
				Kind: &flowv1.Justification_Citation{
					Citation: &flowv1.Citation{
						CitationIds: []string{"law-1"},
					},
				},
			},
		},
	}

	resolved, err := evaluateFeedback(
		context.Background(), agent, makeInferFunc("test"),
		client, feedback, "petition", "haiku")
	if err != nil {
		t.Fatalf("evaluateFeedback() error: %v", err)
	}

	if len(resolved) != 0 {
		t.Fatalf("expected 0 novel items for citation, got %d",
			len(resolved))
	}
}

func TestEvaluateFeedback_RejectedVerdictNotNovel(t *testing.T) {
	spy := newAppraiseSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, evalSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	// LLM rejects the refusal — item is not resolved.
	ollamaSrv := mockOllamaWithResponse(t,
		`{"verdict": "reject", "reason": "weak argument"}`)
	defer ollamaSrv.Close()
	t.Setenv("OLLAMA_BASE_URL", ollamaSrv.URL)

	feedback := []*flowv1.FeedbackItem{
		{
			Id:       "fb-wf-rejected",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
			Message:  "test",
			Severity: flowv1.Severity_SEVERITY_MEDIUM,
			Justification: &flowv1.Justification{
				Kind: &flowv1.Justification_NovelArgument{
					NovelArgument: &flowv1.NovelArgument{
						Argument: "novel but rejected",
					},
				},
			},
		},
	}

	resolved, err := evaluateFeedback(
		context.Background(), agent, makeInferFunc("test"),
		client, feedback, "petition", "haiku")
	if err != nil {
		t.Fatalf("evaluateFeedback() error: %v", err)
	}

	// Rejected verdict → not resolved → not in novel list.
	if len(resolved) != 0 {
		t.Fatalf(
			"expected 0 novel items for rejected verdict, got %d",
			len(resolved))
	}
}
