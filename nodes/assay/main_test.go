package main

import (
	"context"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestAgent creates a FoundryAgent for schema validation tests.
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

	spy := newAssaySpy()
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

// staticInfer returns an InferFunc that always returns the given JSON.
func staticInfer(jsonOutput string) flow.InferFunc {
	return func(ctx context.Context, input []byte) (*flow.InferResult, error) {
		return &flow.InferResult{
			Output:       []byte(jsonOutput),
			Model:        "test-model",
			InputTokens:  10,
			OutputTokens: 10,
			DurationMs:   100,
		}, nil
	}
}

// ---------------------------------------------------------------------------
// Tests: deliberationSchema validation
// ---------------------------------------------------------------------------

func TestDeliberationSchema_ValidResolve(t *testing.T) {
	agent := newTestAgent(t, deliberationSchema)

	validJSON := `{"verdict": "resolve", "reasoning": "feedback is valid and should be addressed"}`
	infer := staticInfer(validJSON)

	out, err := agent.Run(context.Background(), infer, nil)
	if err != nil {
		t.Fatalf("expected valid output to pass, got error: %v", err)
	}
	if string(out) != validJSON {
		t.Fatalf("output mismatch: got %s", out)
	}
}

func TestDeliberationSchema_ValidReject(t *testing.T) {
	agent := newTestAgent(t, deliberationSchema)

	validJSON := `{"verdict": "reject", "reasoning": "feedback is not applicable"}`
	infer := staticInfer(validJSON)

	out, err := agent.Run(context.Background(), infer, nil)
	if err != nil {
		t.Fatalf("expected valid output to pass, got error: %v", err)
	}
	if string(out) != validJSON {
		t.Fatalf("output mismatch: got %s", out)
	}
}

func TestDeliberationSchema_ValidConflict(t *testing.T) {
	agent := newTestAgent(t, deliberationSchema)

	validJSON := `{"verdict": "conflict", "reasoning": "irreconcilable positions", "suggested_statement": "escalate to HITL"}`
	infer := staticInfer(validJSON)

	out, err := agent.Run(context.Background(), infer, nil)
	if err != nil {
		t.Fatalf("expected valid output to pass, got error: %v", err)
	}
	if string(out) != validJSON {
		t.Fatalf("output mismatch: got %s", out)
	}
}

func TestDeliberationSchema_RejectsInvalidVerdict(t *testing.T) {
	agent := newTestAgent(t, deliberationSchema)

	invalidJSON := `{"verdict": "maybe", "reasoning": "not sure"}`
	infer := staticInfer(invalidJSON)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected invalid verdict to fail schema validation")
	}
	if !strings.Contains(err.Error(), "output validation failed") {
		t.Fatalf("expected validation error, got: %v", err)
	}
}

func TestDeliberationSchema_RejectsMissingReasoning(t *testing.T) {
	agent := newTestAgent(t, deliberationSchema)

	invalidJSON := `{"verdict": "resolve"}`
	infer := staticInfer(invalidJSON)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected missing reasoning to fail schema validation")
	}
	if !strings.Contains(err.Error(), "output validation failed") {
		t.Fatalf("expected validation error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: codificationSchema validation
// ---------------------------------------------------------------------------

func TestCodificationSchema_ValidWithDeterministic(t *testing.T) {
	agent := newTestAgent(t, codificationSchema)

	validJSON := `{
		"has_deterministic": true,
		"subjective": "Poetry must be happy and optimistic",
		"deterministic": "(declare-const artefact-content String)(assert (not (str.contains artefact-content \"sausage\")))"
	}`
	infer := staticInfer(validJSON)

	out, err := agent.Run(context.Background(), infer, nil)
	if err != nil {
		t.Fatalf("expected valid output to pass, got error: %v", err)
	}
	// JSON whitespace may differ, just check it parses
	if len(out) == 0 {
		t.Fatal("expected non-empty output")
	}
}

func TestCodificationSchema_ValidWithoutDeterministic(t *testing.T) {
	agent := newTestAgent(t, codificationSchema)

	validJSON := `{
		"has_deterministic": false,
		"subjective": "Poetry must be beautiful and elegant"
	}`
	infer := staticInfer(validJSON)

	out, err := agent.Run(context.Background(), infer, nil)
	if err != nil {
		t.Fatalf("expected valid output to pass, got error: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected non-empty output")
	}
}

func TestCodificationSchema_RejectsMissingSubjective(t *testing.T) {
	agent := newTestAgent(t, codificationSchema)

	invalidJSON := `{"has_deterministic": false}`
	infer := staticInfer(invalidJSON)

	_, err := agent.Run(context.Background(), infer, nil)
	if err == nil {
		t.Fatal("expected missing subjective to fail schema validation")
	}
	if !strings.Contains(err.Error(), "output validation failed") {
		t.Fatalf("expected validation error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Jury sizing and consensus thresholds
// ---------------------------------------------------------------------------

func TestDetermineJurySize(t *testing.T) {
	tests := []struct {
		severity flowv1.Severity
		want     int
	}{
		{flowv1.Severity_SEVERITY_LOW, 3},
		{flowv1.Severity_SEVERITY_MEDIUM, 3},
		{flowv1.Severity_SEVERITY_HIGH, 5},
		{flowv1.Severity_SEVERITY_CRITICAL, 7},
	}

	for _, tt := range tests {
		got := determineJurySize(tt.severity)
		if got != tt.want {
			t.Errorf("determineJurySize(%v) = %d, want %d", tt.severity, got, tt.want)
		}
	}
}

func TestDetermineConsensusThreshold(t *testing.T) {
	tests := []struct {
		severity flowv1.Severity
		want     float64
	}{
		{flowv1.Severity_SEVERITY_LOW, thresholdSimpleMajority},
		{flowv1.Severity_SEVERITY_MEDIUM, thresholdSimpleMajority},
		{flowv1.Severity_SEVERITY_HIGH, thresholdSuperMajority},
		{flowv1.Severity_SEVERITY_CRITICAL, thresholdUnanimity},
	}

	for _, tt := range tests {
		got := determineConsensusThreshold(tt.severity)
		if got != tt.want {
			t.Errorf("determineConsensusThreshold(%v) = %f, want %f", tt.severity, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests: Utility functions
// ---------------------------------------------------------------------------

func TestFilterDeadlocked(t *testing.T) {
	feedback := []*flowv1.FeedbackItem{
		{Id: "1", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW},
		{Id: "2", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
		{Id: "3", State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED},
		{Id: "4", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
	}

	result := filterDeadlocked(feedback)
	if len(result) != 2 {
		t.Fatalf("expected 2 deadlocked items, got %d", len(result))
	}
	if result[0].GetId() != "2" || result[1].GetId() != "4" {
		t.Fatalf("expected items 2 and 4, got %s and %s", result[0].GetId(), result[1].GetId())
	}
}

func TestGenerateRulingStatement(t *testing.T) {
	item := &flowv1.FeedbackItem{
		Message: "Test feedback message",
		History: []*flowv1.FeedbackEvent{
			{Actor: "refiner", Message: "I disagree"},
			{Actor: "reviewer", Message: "You must fix this"},
		},
	}

	statement := generateRulingStatement(item, "resolve")
	if !strings.Contains(statement, "Judicial Ruling") {
		t.Error("expected statement to contain 'Judicial Ruling'")
	}
	if !strings.Contains(statement, "Test feedback message") {
		t.Error("expected statement to contain original message")
	}
	if !strings.Contains(statement, "Discussion Summary") {
		t.Error("expected statement to contain discussion summary")
	}
}

func TestGetEnvOrDefault(t *testing.T) {
	const testKey = "TEST_ASSAY_ENV"
	const testValue = "test-value"
	const defaultValue = "default-value"

	// Test with environment variable not set
	result := getEnvOrDefault(testKey, defaultValue)
	if result != defaultValue {
		t.Errorf("expected default value %q, got %q", defaultValue, result)
	}

	// Test with environment variable set
	t.Setenv(testKey, testValue)
	result = getEnvOrDefault(testKey, defaultValue)
	if result != testValue {
		t.Errorf("expected env value %q, got %q", testValue, result)
	}
}

func TestGetEnvIntOrDefault(t *testing.T) {
	const testKey = "TEST_ASSAY_INT_ENV"
	const testValue = "42"
	const defaultValue = 10

	// Test with environment variable not set
	result := getEnvIntOrDefault(testKey, defaultValue)
	if result != defaultValue {
		t.Errorf("expected default value %d, got %d", defaultValue, result)
	}

	// Test with environment variable set
	t.Setenv(testKey, testValue)
	result = getEnvIntOrDefault(testKey, defaultValue)
	if result != 42 {
		t.Errorf("expected env value 42, got %d", result)
	}

	// Test with invalid integer
	t.Setenv(testKey, "invalid")
	result = getEnvIntOrDefault(testKey, defaultValue)
	if result != defaultValue {
		t.Errorf("expected default value %d for invalid int, got %d", defaultValue, result)
	}
}
