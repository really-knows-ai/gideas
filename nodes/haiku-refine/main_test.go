package main

import (
	"context"
	"encoding/json"
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
// a real TCP listener + no-op gRPC spy (same pattern as haiku-appraise).
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

	spy := newRefineSpy()
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

// newSpyClientWithSpy creates a flow.Client and returns the spy for assertions.
// Returns a cleanup function.
func newSpyClientWithSpy(t *testing.T, spy *refineSpy) (*flow.Client, func()) {
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

// ---------------------------------------------------------------------------
// Tests: triageSchema validation
// ---------------------------------------------------------------------------

func TestTriageSchema_ValidAction(t *testing.T) {
	agent := newTestAgent(t, triageSchema)

	validJSON := `{"decision": "action", "message": "I will fix the syllable count"}`
	infer := staticInfer(validJSON)

	out, err := agent.Run(context.Background(), infer, nil)
	if err != nil {
		t.Fatalf("expected valid output to pass, got error: %v", err)
	}
	if string(out) != validJSON {
		t.Fatalf("output mismatch: got %s", out)
	}
}

func TestTriageSchema_ValidRefuseWithCitation(t *testing.T) {
	agent := newTestAgent(t, triageSchema)

	validJSON := `{"decision": "refuse", "message": "existing law supports this",` +
		` "justification_type": "citation", "citation_ids": ["law-1"]}`
	infer := staticInfer(validJSON)

	out, err := agent.Run(context.Background(), infer, nil)
	if err != nil {
		t.Fatalf("expected valid output to pass, got error: %v", err)
	}
	if string(out) != validJSON {
		t.Fatalf("output mismatch: got %s", out)
	}
}

func TestTriageSchema_ValidRefuseWithNovelArgument(t *testing.T) {
	agent := newTestAgent(t, triageSchema)

	validJSON := `{"decision": "refuse", "message": "the feedback is subjective",` +
		` "justification_type": "novel_argument",` +
		` "argument": "seasonal imagery is not required by any law"}`
	infer := staticInfer(validJSON)

	out, err := agent.Run(context.Background(), infer, nil)
	if err != nil {
		t.Fatalf("expected valid output to pass, got error: %v", err)
	}
	if string(out) != validJSON {
		t.Fatalf("output mismatch: got %s", out)
	}
}

func TestTriageSchema_RejectsInvalidDecision(t *testing.T) {
	agent := newTestAgent(t, triageSchema)

	invalidJSON := `{"decision": "maybe", "message": "not sure"}`
	infer := staticInfer(invalidJSON)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected invalid decision to fail schema validation")
	}
	if !strings.Contains(err.Error(), "output validation failed") {
		t.Fatalf("expected 'output validation failed' in error, got: %v", err)
	}
}

func TestTriageSchema_RejectsEmptyMessage(t *testing.T) {
	agent := newTestAgent(t, triageSchema)

	emptyMsg := `{"decision": "action", "message": ""}`
	infer := staticInfer(emptyMsg)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected empty message to fail schema validation (minLength: 1)")
	}
}

func TestTriageSchema_RejectsMissingDecision(t *testing.T) {
	agent := newTestAgent(t, triageSchema)

	missingDecision := `{"message": "some fix"}`
	infer := staticInfer(missingDecision)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected missing decision to fail schema validation")
	}
}

func TestTriageSchema_RejectsAdditionalProperties(t *testing.T) {
	agent := newTestAgent(t, triageSchema)

	extra := `{"decision": "action", "message": "fix it", "extra": "nope"}`
	infer := staticInfer(extra)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected additional properties to fail schema validation")
	}
}

func TestTriageSchema_RejectsInvalidJustificationType(t *testing.T) {
	agent := newTestAgent(t, triageSchema)

	invalidJust := `{"decision": "refuse", "message": "nope", "justification_type": "invalid"}`
	infer := staticInfer(invalidJust)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected invalid justification_type to fail schema validation")
	}
}

// ---------------------------------------------------------------------------
// Tests: haikuSchema validation
// ---------------------------------------------------------------------------

func TestHaikuSchema_Valid(t *testing.T) {
	agent := newTestAgent(t, haikuSchema)

	validJSON := `{"haiku": "autumn moonlight\na worm digs silently\ninto the chestnut"}`
	infer := staticInfer(validJSON)

	out, err := agent.Run(context.Background(), infer, nil)
	if err != nil {
		t.Fatalf("expected valid output to pass, got error: %v", err)
	}
	if string(out) != validJSON {
		t.Fatalf("output mismatch: got %s", out)
	}
}

func TestHaikuSchema_RejectsEmptyHaiku(t *testing.T) {
	agent := newTestAgent(t, haikuSchema)

	emptyHaiku := `{"haiku": ""}`
	infer := staticInfer(emptyHaiku)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected empty haiku to fail schema validation (minLength: 1)")
	}
}

func TestHaikuSchema_RejectsMissingHaiku(t *testing.T) {
	agent := newTestAgent(t, haikuSchema)

	missing := `{}`
	infer := staticInfer(missing)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected missing haiku to fail schema validation")
	}
}

func TestHaikuSchema_RejectsAdditionalProperties(t *testing.T) {
	agent := newTestAgent(t, haikuSchema)

	extra := `{"haiku": "test haiku", "extra": "nope"}`
	infer := staticInfer(extra)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected additional properties to fail schema validation")
	}
}

// ---------------------------------------------------------------------------
// Tests: buildJustification
// ---------------------------------------------------------------------------

func TestBuildJustification_Citation(t *testing.T) {
	out := triageOutput{
		Decision:          decisionRefuse,
		Message:           "existing law says so",
		JustificationType: justTypeCitation,
		CitationIDs:       []string{"law-1", "law-2"},
	}

	just, err := buildJustification(out)
	if err != nil {
		t.Fatalf("buildJustification() error: %v", err)
	}

	cit := just.GetCitation()
	if cit == nil {
		t.Fatal("expected Citation justification")
	}
	if len(cit.GetCitationIds()) != 2 {
		t.Fatalf("expected 2 citation IDs, got %d", len(cit.GetCitationIds()))
	}
	if cit.GetCitationIds()[0] != "law-1" || cit.GetCitationIds()[1] != "law-2" {
		t.Fatalf("unexpected citation IDs: %v", cit.GetCitationIds())
	}
}

func TestBuildJustification_NovelArgument(t *testing.T) {
	out := triageOutput{
		Decision:          decisionRefuse,
		Message:           "subjective feedback",
		JustificationType: justTypeNovelArgument,
		Argument:          "the imagery serves the petition's core theme",
	}

	just, err := buildJustification(out)
	if err != nil {
		t.Fatalf("buildJustification() error: %v", err)
	}

	novel := just.GetNovelArgument()
	if novel == nil {
		t.Fatal("expected NovelArgument justification")
	}
	if novel.GetArgument() != "the imagery serves the petition's core theme" {
		t.Fatalf("unexpected argument: %q", novel.GetArgument())
	}
}

func TestBuildJustification_CitationNoCitationIDs(t *testing.T) {
	out := triageOutput{
		Decision:          decisionRefuse,
		Message:           "no ids",
		JustificationType: justTypeCitation,
		CitationIDs:       nil,
	}

	_, err := buildJustification(out)
	if err == nil {
		t.Fatal("expected error when citation has no citation_ids")
	}
	if !strings.Contains(err.Error(), "at least one citation_id") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildJustification_NovelArgumentEmpty(t *testing.T) {
	out := triageOutput{
		Decision:          decisionRefuse,
		Message:           "no argument",
		JustificationType: justTypeNovelArgument,
		Argument:          "",
	}

	_, err := buildJustification(out)
	if err == nil {
		t.Fatal("expected error when novel_argument has empty argument")
	}
	if !strings.Contains(err.Error(), "non-empty argument") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildJustification_MissingType(t *testing.T) {
	out := triageOutput{
		Decision:          decisionRefuse,
		Message:           "no type",
		JustificationType: "",
	}

	_, err := buildJustification(out)
	if err == nil {
		t.Fatal("expected error when justification_type is empty")
	}
	if !strings.Contains(err.Error(), "requires justification_type") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildJustification_InvalidType(t *testing.T) {
	out := triageOutput{
		Decision:          decisionRefuse,
		Message:           "bad type",
		JustificationType: "precedent",
	}

	_, err := buildJustification(out)
	if err == nil {
		t.Fatal("expected error when justification_type is invalid")
	}
}

// ---------------------------------------------------------------------------
// Tests: triageOutput unmarshalling
// ---------------------------------------------------------------------------

func TestTriageOutput_UnmarshalAction(t *testing.T) {
	raw := `{"decision": "action", "message": "will fix syllables"}`
	var out triageOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if out.Decision != decisionAction {
		t.Fatalf("expected decision %q, got %q", decisionAction, out.Decision)
	}
	if out.Message != "will fix syllables" {
		t.Fatalf("expected message 'will fix syllables', got %q", out.Message)
	}
}

func TestTriageOutput_UnmarshalRefuse(t *testing.T) {
	raw := `{"decision": "refuse", "message": "law supports me",` +
		` "justification_type": "citation", "citation_ids": ["law-42"]}`
	var out triageOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if out.Decision != decisionRefuse {
		t.Fatalf("expected decision %q, got %q", decisionRefuse, out.Decision)
	}
	if out.JustificationType != justTypeCitation {
		t.Fatalf("expected justification_type %q, got %q", justTypeCitation, out.JustificationType)
	}
	if len(out.CitationIDs) != 1 || out.CitationIDs[0] != "law-42" {
		t.Fatalf("unexpected citation_ids: %v", out.CitationIDs)
	}
}

func TestHaikuOutput_Unmarshal(t *testing.T) {
	raw := `{"haiku": "autumn moonlight\na worm digs silently\ninto the chestnut"}`
	var out haikuOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if !strings.Contains(out.Haiku, "autumn moonlight") {
		t.Fatalf("unexpected haiku: %q", out.Haiku)
	}
}

// ---------------------------------------------------------------------------
// Tests: triageFeedback — parallel per-item triage
// ---------------------------------------------------------------------------

func TestTriageFeedback_ActionPath(t *testing.T) {
	spy := newRefineSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, triageSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	ollamaSrv := mockOllamaWithResponse(t, `{"decision": "action", "message": "will fix syllable count"}`)
	defer ollamaSrv.Close()
	t.Setenv("OLLAMA_BASE_URL", ollamaSrv.URL)

	feedback := []*flowv1.FeedbackItem{
		{
			Id:       "fb-1",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_NEW,
			Message:  "syllable count is wrong",
			Severity: flowv1.Severity_SEVERITY_MEDIUM,
		},
	}

	actioned, err := triageFeedback(
		context.Background(), agent, makeInferFunc("test"),
		client, feedback, "write a haiku", "test haiku", nil,
	)
	if err != nil {
		t.Fatalf("triageFeedback() error: %v", err)
	}

	if len(actioned) != 1 {
		t.Fatalf("expected 1 actioned item, got %d", len(actioned))
	}
	if actioned[0].FeedbackID != "fb-1" {
		t.Fatalf("expected actioned feedback ID 'fb-1', got %q", actioned[0].FeedbackID)
	}
	if actioned[0].FixDesc != "will fix syllable count" {
		t.Fatalf("expected fix desc, got %q", actioned[0].FixDesc)
	}

	msg, ok := spy.ResolvedFeedback["fb-1"]
	if !ok {
		t.Fatal("expected ResolveFeedback for fb-1")
	}
	if msg != "will fix syllable count" {
		t.Fatalf("expected resolve message 'will fix syllable count', got %q", msg)
	}
}

func TestTriageFeedback_RefuseWithCitation(t *testing.T) {
	spy := newRefineSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, triageSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	ollamaSrv := mockOllamaWithResponse(t,
		`{"decision": "refuse", "message": "law says so", "justification_type": "citation", "citation_ids": ["law-1"]}`)
	defer ollamaSrv.Close()
	t.Setenv("OLLAMA_BASE_URL", ollamaSrv.URL)

	feedback := []*flowv1.FeedbackItem{
		{
			Id:       "fb-2",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_NEW,
			Message:  "use different imagery",
			Severity: flowv1.Severity_SEVERITY_LOW,
		},
	}

	actioned, err := triageFeedback(
		context.Background(), agent, makeInferFunc("test"),
		client, feedback, "write a haiku", "test haiku", nil,
	)
	if err != nil {
		t.Fatalf("triageFeedback() error: %v", err)
	}

	if len(actioned) != 0 {
		t.Fatalf("expected 0 actioned items for refusal, got %d", len(actioned))
	}

	just, ok := spy.RefusedFeedback["fb-2"]
	if !ok {
		t.Fatal("expected RefuseFeedback for fb-2")
	}
	cit := just.GetCitation()
	if cit == nil {
		t.Fatal("expected Citation justification")
	}
	if len(cit.GetCitationIds()) != 1 || cit.GetCitationIds()[0] != "law-1" {
		t.Fatalf("unexpected citation IDs: %v", cit.GetCitationIds())
	}
}

func TestTriageFeedback_RefuseWithNovelArgument(t *testing.T) {
	spy := newRefineSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, triageSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	ollamaSrv := mockOllamaWithResponse(t,
		`{"decision": "refuse", "message": "imagery serves the petition",`+
			` "justification_type": "novel_argument",`+
			` "argument": "the autumn theme requires this exact imagery"}`)
	defer ollamaSrv.Close()
	t.Setenv("OLLAMA_BASE_URL", ollamaSrv.URL)

	feedback := []*flowv1.FeedbackItem{
		{
			Id:       "fb-3",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_NEW,
			Message:  "change the seasonal reference",
			Severity: flowv1.Severity_SEVERITY_LOW,
		},
	}

	actioned, err := triageFeedback(
		context.Background(), agent, makeInferFunc("test"),
		client, feedback, "write a haiku", "test haiku", nil,
	)
	if err != nil {
		t.Fatalf("triageFeedback() error: %v", err)
	}

	if len(actioned) != 0 {
		t.Fatalf("expected 0 actioned items, got %d", len(actioned))
	}

	just, ok := spy.RefusedFeedback["fb-3"]
	if !ok {
		t.Fatal("expected RefuseFeedback for fb-3")
	}
	novel := just.GetNovelArgument()
	if novel == nil {
		t.Fatal("expected NovelArgument justification")
	}
	if novel.GetArgument() != "the autumn theme requires this exact imagery" {
		t.Fatalf("unexpected argument: %q", novel.GetArgument())
	}
}

func TestTriageFeedback_SkipsNonTriageableStates(t *testing.T) {
	spy := newRefineSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, triageSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	feedback := []*flowv1.FeedbackItem{
		{Id: "fb-resolved", State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED, Message: "done"},
		{Id: "fb-actioned", State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED, Message: "already actioned"},
		{Id: "fb-wontfix", State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX, Message: "already refused"},
		{Id: "fb-deadlocked", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED, Message: "deadlocked"},
	}

	// No Ollama server — no inference calls should be made.
	actioned, err := triageFeedback(
		context.Background(), agent, makeInferFunc("test"),
		client, feedback, "petition", "haiku", nil,
	)
	if err != nil {
		t.Fatalf("triageFeedback() error: %v", err)
	}

	if len(actioned) != 0 {
		t.Fatalf("expected 0 actioned items, got %d", len(actioned))
	}
	if len(spy.ResolvedFeedback) != 0 || len(spy.RefusedFeedback) != 0 {
		t.Fatal("expected no feedback operations for non-triageable states")
	}
}

func TestTriageFeedback_RejectedStateIsTriageable(t *testing.T) {
	spy := newRefineSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, triageSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	ollamaSrv := mockOllamaWithResponse(t, `{"decision": "action", "message": "will try again"}`)
	defer ollamaSrv.Close()
	t.Setenv("OLLAMA_BASE_URL", ollamaSrv.URL)

	feedback := []*flowv1.FeedbackItem{
		{
			Id:       "fb-rejected",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_REJECTED,
			Message:  "previous fix was inadequate",
			Severity: flowv1.Severity_SEVERITY_HIGH,
			History: []*flowv1.FeedbackEvent{
				{Actor: "refine", Action: "resolve", Message: "fixed syllables"},
				{Actor: "appraise", Action: "reject", Message: "fix was insufficient"},
			},
		},
	}

	actioned, err := triageFeedback(
		context.Background(), agent, makeInferFunc("test"),
		client, feedback, "write a haiku", "test haiku", nil,
	)
	if err != nil {
		t.Fatalf("triageFeedback() error: %v", err)
	}

	if len(actioned) != 1 {
		t.Fatalf("expected 1 actioned item (REJECTED should be triageable), got %d", len(actioned))
	}

	_, ok := spy.ResolvedFeedback["fb-rejected"]
	if !ok {
		t.Fatal("expected ResolveFeedback for fb-rejected")
	}
}

func TestTriageFeedback_RejectedCanBeRefusedAgain(t *testing.T) {
	spy := newRefineSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, triageSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	ollamaSrv := mockOllamaWithResponse(t,
		`{"decision": "refuse", "message": "still disagree",`+
			` "justification_type": "novel_argument",`+
			` "argument": "stronger reasoning this time"}`)
	defer ollamaSrv.Close()
	t.Setenv("OLLAMA_BASE_URL", ollamaSrv.URL)

	feedback := []*flowv1.FeedbackItem{
		{
			Id:       "fb-rejected-again",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_REJECTED,
			Message:  "prior refusal was weak",
			Severity: flowv1.Severity_SEVERITY_MEDIUM,
		},
	}

	actioned, err := triageFeedback(
		context.Background(), agent, makeInferFunc("test"),
		client, feedback, "write a haiku", "test haiku", nil,
	)
	if err != nil {
		t.Fatalf("triageFeedback() error: %v", err)
	}

	if len(actioned) != 0 {
		t.Fatalf("expected 0 actioned items (REJECTED re-refused), got %d", len(actioned))
	}

	just, ok := spy.RefusedFeedback["fb-rejected-again"]
	if !ok {
		t.Fatal("expected RefuseFeedback for fb-rejected-again")
	}
	if just.GetNovelArgument().GetArgument() != "stronger reasoning this time" {
		t.Fatalf("unexpected argument: %q", just.GetNovelArgument().GetArgument())
	}
}

func TestTriageFeedback_ContemptGuardForcesAction(t *testing.T) {
	spy := newRefineSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, triageSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	// No Ollama server — contempt guard should skip LLM entirely.
	feedback := []*flowv1.FeedbackItem{
		{
			Id:           "fb-contempt",
			State:        flowv1.FeedbackState_FEEDBACK_STATE_REJECTED,
			Message:      "must comply with ruling",
			Severity:     flowv1.Severity_SEVERITY_CRITICAL,
			LinkedRuling: "ruling-001",
		},
	}

	actioned, err := triageFeedback(
		context.Background(), agent, makeInferFunc("test"),
		client, feedback, "write a haiku", "test haiku", nil,
	)
	if err != nil {
		t.Fatalf("triageFeedback() error: %v", err)
	}

	if len(actioned) != 1 {
		t.Fatalf("expected 1 actioned item (contempt guard), got %d", len(actioned))
	}
	if actioned[0].FixDesc != contemptMessage {
		t.Fatalf("expected fix desc %q, got %q", contemptMessage, actioned[0].FixDesc)
	}

	msg, ok := spy.ResolvedFeedback["fb-contempt"]
	if !ok {
		t.Fatal("expected ResolveFeedback for fb-contempt")
	}
	if msg != contemptMessage {
		t.Fatalf("expected resolve message %q, got %q", contemptMessage, msg)
	}

	// Should NOT have called RefuseFeedback.
	if len(spy.RefusedFeedback) != 0 {
		t.Fatalf("expected no refusals (contempt guard), got: %v", spy.RefusedFeedback)
	}
}

func TestTriageFeedback_ContemptGuardOnlyOnRejected(t *testing.T) {
	// A NEW item with a linked_ruling should NOT trigger the contempt guard.
	// (In practice this shouldn't happen, but the code should be safe.)
	spy := newRefineSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, triageSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	ollamaSrv := mockOllamaWithResponse(t, `{"decision": "action", "message": "will fix it"}`)
	defer ollamaSrv.Close()
	t.Setenv("OLLAMA_BASE_URL", ollamaSrv.URL)

	feedback := []*flowv1.FeedbackItem{
		{
			Id:           "fb-new-with-ruling",
			State:        flowv1.FeedbackState_FEEDBACK_STATE_NEW,
			Message:      "some issue",
			Severity:     flowv1.Severity_SEVERITY_MEDIUM,
			LinkedRuling: "ruling-002",
		},
	}

	actioned, err := triageFeedback(
		context.Background(), agent, makeInferFunc("test"),
		client, feedback, "write a haiku", "test haiku", nil,
	)
	if err != nil {
		t.Fatalf("triageFeedback() error: %v", err)
	}

	if len(actioned) != 1 {
		t.Fatalf("expected 1 actioned item (NEW goes through LLM), got %d", len(actioned))
	}
	// Should have gone through LLM, not forced.
	if actioned[0].FixDesc == contemptMessage {
		t.Fatal("NEW item with linked_ruling should NOT be force-actioned via contempt guard")
	}
}

func TestTriageFeedback_ParallelMultipleItems(t *testing.T) {
	spy := newRefineSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, triageSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	// All items get "action" response.
	ollamaSrv := mockOllamaWithResponse(t, `{"decision": "action", "message": "will fix it"}`)
	defer ollamaSrv.Close()
	t.Setenv("OLLAMA_BASE_URL", ollamaSrv.URL)

	feedback := []*flowv1.FeedbackItem{
		{Id: "fb-a", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW, Message: "issue A"},
		{Id: "fb-b", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW, Message: "issue B"},
		{Id: "fb-c", State: flowv1.FeedbackState_FEEDBACK_STATE_REJECTED, Message: "issue C"},
	}

	actioned, err := triageFeedback(
		context.Background(), agent, makeInferFunc("test"),
		client, feedback, "petition", "haiku", nil,
	)
	if err != nil {
		t.Fatalf("triageFeedback() error: %v", err)
	}

	if len(actioned) != 3 {
		t.Fatalf("expected 3 actioned items, got %d", len(actioned))
	}
	if len(spy.ResolvedFeedback) != 3 {
		t.Fatalf("expected 3 resolved items, got %d", len(spy.ResolvedFeedback))
	}
}

func TestTriageFeedback_EmptyFeedback(t *testing.T) {
	spy := newRefineSpy()
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	agent, err := flow.NewAgent(client, triageSchema,
		flow.WithHeartbeatInterval(1<<62))
	if err != nil {
		t.Fatalf("NewAgent() failed: %v", err)
	}

	actioned, err := triageFeedback(
		context.Background(), agent, makeInferFunc("test"),
		client, nil, "petition", "haiku", nil,
	)
	if err != nil {
		t.Fatalf("triageFeedback() error: %v", err)
	}

	if len(actioned) != 0 {
		t.Fatalf("expected 0 actioned items for empty feedback, got %d", len(actioned))
	}
}

// ---------------------------------------------------------------------------
// Tests: prompt builders
// ---------------------------------------------------------------------------

func TestBuildTriagePrompt_ContainsContext(t *testing.T) {
	fb := &flowv1.FeedbackItem{
		Id:       "fb-1",
		Message:  "syllable count is wrong",
		Severity: flowv1.Severity_SEVERITY_MEDIUM,
		History: []*flowv1.FeedbackEvent{
			{Actor: "appraise", Action: "add", Message: "raised syllable issue"},
		},
	}

	laws := []*flowv1.Law{
		{Id: "law-1", Tier: 1, Goal: "exactly 5-7-5 syllables"},
	}

	prompt := buildTriagePrompt(fb, "write about autumn", "autumn leaves fall", laws)
	if !strings.Contains(prompt, "write about autumn") {
		t.Error("prompt should contain petition")
	}
	if !strings.Contains(prompt, "autumn leaves fall") {
		t.Error("prompt should contain haiku")
	}
	if !strings.Contains(prompt, "syllable count is wrong") {
		t.Error("prompt should contain feedback message")
	}
	if !strings.Contains(prompt, "raised syllable issue") {
		t.Error("prompt should contain history")
	}
	if !strings.Contains(prompt, "law-1") {
		t.Error("prompt should contain law IDs")
	}
	if !strings.Contains(prompt, "exactly 5-7-5 syllables") {
		t.Error("prompt should contain law goals")
	}
}

func TestBuildTriagePrompt_NoLaws(t *testing.T) {
	fb := &flowv1.FeedbackItem{
		Id:       "fb-1",
		Message:  "some issue",
		Severity: flowv1.Severity_SEVERITY_LOW,
	}

	prompt := buildTriagePrompt(fb, "petition", "haiku", nil)
	if !strings.Contains(prompt, "petition") {
		t.Error("prompt should contain petition")
	}
	if strings.Contains(prompt, "APPLICABLE LAWS") {
		t.Error("prompt should not contain laws section when no laws")
	}
}

func TestBuildRevisionPrompt_ContainsActionedItems(t *testing.T) {
	laws := []*flowv1.Law{
		{Id: "law-1", Tier: 1, Goal: "use seasonal imagery"},
	}
	actioned := []actionedItem{
		{FeedbackID: "fb-1", Message: "syllable count wrong", FixDesc: "will fix syllables"},
		{FeedbackID: "fb-2", Message: "theme mismatch", FixDesc: "will align with petition"},
	}

	prompt := buildRevisionPrompt("write about autumn", "autumn leaves fall", laws, actioned)
	if !strings.Contains(prompt, "write about autumn") {
		t.Error("prompt should contain petition")
	}
	if !strings.Contains(prompt, "autumn leaves fall") {
		t.Error("prompt should contain current haiku")
	}
	if !strings.Contains(prompt, "law-1") {
		t.Error("prompt should contain law IDs")
	}
	if !strings.Contains(prompt, "syllable count wrong") {
		t.Error("prompt should contain actioned feedback message")
	}
	if !strings.Contains(prompt, "will fix syllables") {
		t.Error("prompt should contain fix description")
	}
	if !strings.Contains(prompt, "will align with petition") {
		t.Error("prompt should contain second fix description")
	}
	if !strings.Contains(prompt, "FIXES TO APPLY") {
		t.Error("prompt should contain fixes section header")
	}
}

func TestBuildRevisionPrompt_NoLaws(t *testing.T) {
	actioned := []actionedItem{
		{FeedbackID: "fb-1", Message: "issue", FixDesc: "fix"},
	}

	prompt := buildRevisionPrompt("petition", "haiku", nil, actioned)
	if strings.Contains(prompt, "GOVERNANCE LAWS") {
		t.Error("prompt should not contain laws section when no laws")
	}
	if !strings.Contains(prompt, "FIXES TO APPLY") {
		t.Error("prompt should contain fixes section even without laws")
	}
}

// ---------------------------------------------------------------------------
// Tests: makeInferFunc with mock Ollama
// ---------------------------------------------------------------------------

func TestMakeInferFunc_ReturnsCorrectResult(t *testing.T) {
	ollamaResp := map[string]any{
		"response":          `{"haiku": "test haiku"}`,
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
	result, err := inferFn(context.Background(), []byte("revise this haiku"))
	if err != nil {
		t.Fatalf("inferFn returned error: %v", err)
	}

	expectedOutput := `{"haiku": "test haiku"}`
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
