package flow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"text/template"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// ---------------------------------------------------------------------------
// Test schemas
// ---------------------------------------------------------------------------

// validHaikuSchema is a JSON Schema that requires an object with a "haiku"
// string field.
const validHaikuSchema = `{
	"type": "object",
	"properties": {
		"haiku": { "type": "string" }
	},
	"required": ["haiku"]
}`

const invalidSchemaJSON = `{not valid json`

const testModel = "test-model"

// ---------------------------------------------------------------------------
// Mock Provider
// ---------------------------------------------------------------------------

// mockProvider implements Provider for testing.
type mockProvider struct {
	output *InferOutput
	err    error

	// capturedModel, capturedSystem, capturedQuery record the last Infer call.
	capturedModel  string
	capturedSystem string
	capturedQuery  []byte

	// delay simulates a slow provider call for heartbeat testing.
	delay time.Duration
}

func (m *mockProvider) Infer(
	ctx context.Context, model, systemPrompt string, queryPrompt []byte,
) (*InferOutput, error) {
	m.capturedModel = model
	m.capturedSystem = systemPrompt
	m.capturedQuery = queryPrompt

	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return m.output, m.err
}

// ---------------------------------------------------------------------------
// Agent spy server — captures telemetry calls
// ---------------------------------------------------------------------------

// agentSpyServer extends the standard spy with telemetry call tracking.
type agentSpyServer struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFlowMonitorServiceServer

	mu             sync.Mutex
	heartbeatCount atomic.Int64
	telemetryCalls []recordedTelemetry
	lastMD         metadata.MD
}

type recordedTelemetry struct {
	EventType string
	Payload   []byte
}

func (s *agentSpyServer) Heartbeat(
	ctx context.Context, req *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	s.heartbeatCount.Add(1)
	s.mu.Lock()
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.mu.Unlock()
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *agentSpyServer) RecordTelemetry(
	ctx context.Context, req *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	s.mu.Lock()
	s.telemetryCalls = append(s.telemetryCalls, recordedTelemetry{
		EventType: req.GetEventType(),
		Payload:   req.GetPayload(),
	})
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.mu.Unlock()
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// getTelemetryCalls returns a copy of recorded telemetry calls.
func (s *agentSpyServer) getTelemetryCalls() []recordedTelemetry {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]recordedTelemetry, len(s.telemetryCalls))
	copy(cp, s.telemetryCalls)
	return cp
}

// ---------------------------------------------------------------------------
// Test helper — sets up Agent test infrastructure
// ---------------------------------------------------------------------------

type agentTestEnv struct {
	client *Client
	spy    *agentSpyServer
	srv    *grpc.Server
}

func setupAgentTestEnv(t *testing.T, workitemID string) *agentTestEnv {
	t.Helper()

	spy := &agentSpyServer{}
	client, srv := setupGRPCTestEnv(t, workitemID, func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFlowMonitorServiceServer(s, spy)
	})

	return &agentTestEnv{client: client, spy: spy, srv: srv}
}

// simpleQueryTemplate returns a template that just outputs {{.Input}}.
func simpleQueryTemplate(t *testing.T) *template.Template {
	t.Helper()
	tmpl, err := template.New("query").Parse("{{.Input}}")
	if err != nil {
		t.Fatalf("failed to parse query template: %v", err)
	}
	return tmpl
}

// newTestAgent creates a FoundryAgent with a mock provider for testing.
func newTestAgent(t *testing.T, env *agentTestEnv, mp *mockProvider) *Agent {
	t.Helper()
	agent, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModel(NewModel(testModel, mp)),
		WithQueryTemplate(simpleQueryTemplate(t)),
		WithHeartbeatInterval(time.Hour), // effectively disable heartbeat
	)
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}
	return agent
}

// ---------------------------------------------------------------------------
// Tests — Construction
// ---------------------------------------------------------------------------

func TestNewAgent_ValidConstruction(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-agent-001")

	mp := &mockProvider{}
	agent, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModel(NewModel(testModel, mp)),
		WithQueryTemplate(simpleQueryTemplate(t)),
	)
	if err != nil {
		t.Fatalf("NewAgent() with valid options returned error: %v", err)
	}
	if agent == nil {
		t.Fatal("NewAgent() returned nil agent")
	}
}

func TestNewAgent_InvalidSchemaJSON(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-agent-002")

	mp := &mockProvider{}
	_, err := NewAgent(env.client,
		WithSchema([]byte(invalidSchemaJSON)),
		WithModel(NewModel(testModel, mp)),
		WithQueryTemplate(simpleQueryTemplate(t)),
	)
	if err == nil {
		t.Fatal("NewAgent() with invalid JSON should return error")
	}
	if !strings.Contains(err.Error(), "invalid output schema") {
		t.Fatalf("expected 'invalid output schema' in error, got: %v", err)
	}
}

func TestNewAgent_NilClient(t *testing.T) {
	_, err := NewAgent(nil,
		WithSchema([]byte(validHaikuSchema)),
		WithModel(NewModel(testModel, &mockProvider{})),
		WithQueryTemplate(template.Must(template.New("q").Parse("{{.Input}}"))),
	)
	if err == nil {
		t.Fatal("NewAgent() with nil client should return error")
	}
	if !strings.Contains(err.Error(), "client must not be nil") {
		t.Fatalf("expected 'client must not be nil' in error, got: %v", err)
	}
}

func TestNewAgent_NilModel(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-agent-003")

	_, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithQueryTemplate(simpleQueryTemplate(t)),
	)
	if err == nil {
		t.Fatal("NewAgent() with nil model should return error")
	}
	if !strings.Contains(err.Error(), "model must not be nil") {
		t.Fatalf("expected 'model must not be nil' in error, got: %v", err)
	}
}

func TestNewAgent_MissingSchema(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-agent-004")

	_, err := NewAgent(env.client,
		WithModel(NewModel(testModel, &mockProvider{})),
		WithQueryTemplate(simpleQueryTemplate(t)),
	)
	if err == nil {
		t.Fatal("NewAgent() with nil schema should return error")
	}
	if !strings.Contains(err.Error(), "schema must not be nil") {
		t.Fatalf("expected 'schema must not be nil' in error, got: %v", err)
	}
}

func TestNewAgent_MissingQueryTemplate(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-agent-005")

	_, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModel(NewModel(testModel, &mockProvider{})),
	)
	if err == nil {
		t.Fatal("NewAgent() with nil query template should return error")
	}
	if !strings.Contains(err.Error(), "query template must not be nil") {
		t.Fatalf("expected 'query template must not be nil' in error, got: %v", err)
	}
}

func TestNewAgent_DefaultHeartbeatInterval(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-agent-007")

	agent, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModel(NewModel(testModel, &mockProvider{})),
		WithQueryTemplate(simpleQueryTemplate(t)),
	)
	if err != nil {
		t.Fatalf("NewAgent() returned error: %v", err)
	}
	if agent.cfg.heartbeatInterval != DefaultHeartbeatInterval {
		t.Fatalf("expected default heartbeat interval %v, got %v",
			DefaultHeartbeatInterval, agent.cfg.heartbeatInterval)
	}
}

func TestNewAgent_CustomHeartbeatInterval(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-agent-008")

	agent, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModel(NewModel(testModel, &mockProvider{})),
		WithQueryTemplate(simpleQueryTemplate(t)),
		WithHeartbeatInterval(5*time.Second),
	)
	if err != nil {
		t.Fatalf("NewAgent() returned error: %v", err)
	}
	if agent.cfg.heartbeatInterval != 5*time.Second {
		t.Fatalf("expected heartbeat interval 5s, got %v", agent.cfg.heartbeatInterval)
	}
}

// ---------------------------------------------------------------------------
// Tests — Run: Template Rendering
// ---------------------------------------------------------------------------

func TestAgent_Run_TemplateRendering(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-tmpl-001")

	mp := &mockProvider{
		output: &InferOutput{
			Output: []byte(`{"haiku": "test haiku"}`),
			Cost: &CostMetadata{
				Model:        testModel,
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		},
	}

	tmpl := template.Must(template.New("query").Parse(
		"Write a haiku about {{.Topic}} with style {{.Style}}"))

	agent, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModel(NewModel(testModel, mp)),
		WithQueryTemplate(tmpl),
		WithSystemPrompt("You are a haiku poet."),
		WithHeartbeatInterval(time.Hour),
	)
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}

	data := struct {
		Topic string
		Style string
	}{Topic: "autumn", Style: "classical"}

	_, err = agent.Run(context.Background(), data)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// Verify the query prompt was rendered correctly.
	expectedQuery := "Write a haiku about autumn with style classical"
	if string(mp.capturedQuery) != expectedQuery {
		t.Fatalf("query mismatch:\ngot:  %q\nwant: %q", string(mp.capturedQuery), expectedQuery)
	}

	// Verify the system prompt was passed through.
	if mp.capturedSystem != "You are a haiku poet." {
		t.Fatalf("system prompt mismatch: got %q", mp.capturedSystem)
	}

	// Verify the model was passed through.
	if mp.capturedModel != testModel {
		t.Fatalf("model mismatch: got %q", mp.capturedModel)
	}
}

// ---------------------------------------------------------------------------
// Tests — Run: Output Validation
// ---------------------------------------------------------------------------

func TestAgent_Run_OutputValidation_Pass(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-val-pass")

	validOutput := []byte(`{"haiku": "autumn moonlight"}`)

	mp := &mockProvider{
		output: &InferOutput{
			Output: validOutput,
			Cost: &CostMetadata{
				Model:        testModel,
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		},
	}

	agent := newTestAgent(t, env, mp)

	got, err := agent.Run(context.Background(), struct{ Input string }{Input: "write a haiku"})
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if string(got) != string(validOutput) {
		t.Fatalf("Run() output = %s, want %s", got, validOutput)
	}
}

func TestAgent_Run_OutputValidation_Fail(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-val-fail")

	// Output missing required "haiku" field.
	invalidOutput := []byte(`{"title": "not a haiku"}`)

	mp := &mockProvider{
		output: &InferOutput{
			Output: invalidOutput,
			Cost: &CostMetadata{
				Model:        testModel,
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		},
	}

	agent := newTestAgent(t, env, mp)

	_, err := agent.Run(context.Background(), struct{ Input string }{Input: "write a haiku"})
	if err == nil {
		t.Fatal("Run() should return error for invalid output")
	}
	if !strings.Contains(err.Error(), "output validation failed") {
		t.Fatalf("expected 'output validation failed' in error, got: %v", err)
	}

	// No telemetry should have been emitted for invalid output.
	calls := env.spy.getTelemetryCalls()
	if len(calls) != 0 {
		t.Fatalf("expected no telemetry calls for invalid output, got %d", len(calls))
	}
}

func TestAgent_Run_OutputNotJSON(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-val-notjson")

	mp := &mockProvider{
		output: &InferOutput{
			Output: []byte("this is not JSON"),
			Cost: &CostMetadata{
				Model:        testModel,
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   100,
			},
		},
	}

	agent := newTestAgent(t, env, mp)

	_, err := agent.Run(context.Background(), struct{ Input string }{Input: "input"})
	if err == nil {
		t.Fatal("Run() should return error for non-JSON output")
	}
	if !strings.Contains(err.Error(), "output validation failed") {
		t.Fatalf("expected 'output validation failed' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests — Run: Cost Telemetry
// ---------------------------------------------------------------------------

func TestAgent_Run_CostTelemetry(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-cost-001")

	mp := &mockProvider{
		output: &InferOutput{
			Output: []byte(`{"haiku": "test haiku"}`),
			Cost: &CostMetadata{
				Model:        "gpt-4o",
				InputTokens:  150,
				OutputTokens: 30,
				DurationMs:   2500,
				Extra:        map[string]any{"provider": "openai", "cached_tokens": int64(50)},
			},
		},
	}

	agent := newTestAgent(t, env, mp)

	_, err := agent.Run(context.Background(), struct{ Input string }{Input: "input"})
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	calls := env.spy.getTelemetryCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 telemetry call, got %d", len(calls))
	}

	if calls[0].EventType != "foundry.cost.llm" {
		t.Fatalf("expected event type 'foundry.cost.llm', got %q", calls[0].EventType)
	}

	var payload map[string]any
	if err := json.Unmarshal(calls[0].Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal telemetry payload: %v", err)
	}

	// Check standard fields.
	if payload["model"] != "gpt-4o" {
		t.Fatalf("expected model=gpt-4o, got %v", payload["model"])
	}
	// JSON numbers unmarshal as float64.
	if payload["input_tokens"].(float64) != 150 {
		t.Fatalf("expected input_tokens=150, got %v", payload["input_tokens"])
	}
	if payload["output_tokens"].(float64) != 30 {
		t.Fatalf("expected output_tokens=30, got %v", payload["output_tokens"])
	}
	if payload["duration_ms"].(float64) != 2500 {
		t.Fatalf("expected duration_ms=2500, got %v", payload["duration_ms"])
	}

	// Check extra fields are merged.
	if payload["provider"] != "openai" {
		t.Fatalf("expected provider=openai, got %v", payload["provider"])
	}
	if payload["cached_tokens"].(float64) != 50 {
		t.Fatalf("expected cached_tokens=50, got %v", payload["cached_tokens"])
	}
}

func TestAgent_Run_NilCostMetadata(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-cost-nil")

	mp := &mockProvider{
		output: &InferOutput{
			Output: []byte(`{"haiku": "test haiku"}`),
			Cost:   nil, // Provider doesn't report costs.
		},
	}

	agent := newTestAgent(t, env, mp)

	_, err := agent.Run(context.Background(), struct{ Input string }{Input: "input"})
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// No telemetry should be emitted when Cost is nil.
	calls := env.spy.getTelemetryCalls()
	if len(calls) != 0 {
		t.Fatalf("expected no telemetry calls when Cost is nil, got %d", len(calls))
	}
}

// ---------------------------------------------------------------------------
// Tests — Run: Multi-Step Accounting
// ---------------------------------------------------------------------------

func TestAgent_Run_MultiStep(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-multi-001")

	mp := &mockProvider{}

	agent := newTestAgent(t, env, mp)

	// First step.
	mp.output = &InferOutput{
		Output: []byte(`{"haiku": "first draft"}`),
		Cost: &CostMetadata{
			Model:        "gpt-4o",
			InputTokens:  100,
			OutputTokens: 20,
			DurationMs:   1000,
		},
	}
	if _, err := agent.Run(context.Background(), struct{ Input string }{Input: "generate"}); err != nil {
		t.Fatalf("Run() step 1 error: %v", err)
	}

	// Second step.
	mp.output = &InferOutput{
		Output: []byte(`{"haiku": "revised draft"}`),
		Cost: &CostMetadata{
			Model:        "gpt-4o-mini",
			InputTokens:  200,
			OutputTokens: 25,
			DurationMs:   800,
		},
	}
	if _, err := agent.Run(context.Background(), struct{ Input string }{Input: "revise"}); err != nil {
		t.Fatalf("Run() step 2 error: %v", err)
	}

	calls := env.spy.getTelemetryCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 telemetry calls (one per step), got %d", len(calls))
	}

	// Verify each step has independent cost data.
	var p1, p2 map[string]any
	_ = json.Unmarshal(calls[0].Payload, &p1)
	_ = json.Unmarshal(calls[1].Payload, &p2)

	if p1["model"] != "gpt-4o" {
		t.Fatalf("step 1: expected model=gpt-4o, got %v", p1["model"])
	}
	if p2["model"] != "gpt-4o-mini" {
		t.Fatalf("step 2: expected model=gpt-4o-mini, got %v", p2["model"])
	}
	if p1["input_tokens"].(float64) != 100 {
		t.Fatalf("step 1: expected input_tokens=100, got %v", p1["input_tokens"])
	}
	if p2["input_tokens"].(float64) != 200 {
		t.Fatalf("step 2: expected input_tokens=200, got %v", p2["input_tokens"])
	}
}

// ---------------------------------------------------------------------------
// Tests — Run: Heartbeat During Inference
// ---------------------------------------------------------------------------

func TestAgent_Run_HeartbeatDuringInference(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-hb-001")

	mp := &mockProvider{
		output: &InferOutput{
			Output: []byte(`{"haiku": "slow inference"}`),
			Cost: &CostMetadata{
				Model:        testModel,
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   120,
			},
		},
		delay: 120 * time.Millisecond,
	}

	// Use a very short heartbeat interval to trigger multiple beats
	// during a simulated slow inference.
	agent, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModel(NewModel(testModel, mp)),
		WithQueryTemplate(simpleQueryTemplate(t)),
		WithHeartbeatInterval(20*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}

	_, err = agent.Run(context.Background(), struct{ Input string }{Input: "input"})
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// With 20ms interval and 120ms sleep, we expect at least 2 heartbeats.
	hbCount := env.spy.heartbeatCount.Load()
	if hbCount < 2 {
		t.Fatalf("expected at least 2 heartbeat calls during inference, got %d", hbCount)
	}
}

func TestAgent_Run_HeartbeatStopsAfterInfer(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-hb-stop")

	mp := &mockProvider{
		output: &InferOutput{
			Output: []byte(`{"haiku": "done"}`),
			Cost: &CostMetadata{
				Model:        testModel,
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   50,
			},
		},
		delay: 50 * time.Millisecond,
	}

	agent, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModel(NewModel(testModel, mp)),
		WithQueryTemplate(simpleQueryTemplate(t)),
		WithHeartbeatInterval(10*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}

	_, err = agent.Run(context.Background(), struct{ Input string }{Input: "input"})
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// Record count right after Run returns.
	countAfterRun := env.spy.heartbeatCount.Load()

	// Wait well beyond the heartbeat interval — no new heartbeats should fire.
	time.Sleep(60 * time.Millisecond)
	countAfterWait := env.spy.heartbeatCount.Load()

	if countAfterWait != countAfterRun {
		t.Fatalf("heartbeat continued after Run returned: count after Run=%d, count after wait=%d",
			countAfterRun, countAfterWait)
	}
}

// ---------------------------------------------------------------------------
// Tests — Run: Error Handling
// ---------------------------------------------------------------------------

func TestAgent_Run_ProviderError(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-err-001")

	providerErr := errors.New("LLM provider unavailable")
	mp := &mockProvider{
		output: nil,
		err:    providerErr,
	}

	agent := newTestAgent(t, env, mp)

	_, err := agent.Run(context.Background(), struct{ Input string }{Input: "input"})
	if err == nil {
		t.Fatal("Run() should return error when provider fails")
	}
	if !strings.Contains(err.Error(), "provider infer failed") {
		t.Fatalf("expected 'provider infer failed' in error, got: %v", err)
	}
	if !errors.Is(err, providerErr) {
		t.Fatalf("expected error to wrap original provider error")
	}

	// No validation or telemetry should have been attempted.
	calls := env.spy.getTelemetryCalls()
	if len(calls) != 0 {
		t.Fatalf("expected no telemetry calls when provider fails, got %d", len(calls))
	}
}

func TestAgent_Run_NilResult(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-err-nil")

	mp := &mockProvider{
		output: nil,
		err:    nil,
	}

	agent := newTestAgent(t, env, mp)

	_, err := agent.Run(context.Background(), struct{ Input string }{Input: "input"})
	if err == nil {
		t.Fatal("Run() should return error when provider returns nil result")
	}
	if !strings.Contains(err.Error(), "nil result") {
		t.Fatalf("expected 'nil result' in error, got: %v", err)
	}
}

func TestAgent_Run_TemplateRenderError(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-err-tmpl")

	mp := &mockProvider{
		output: &InferOutput{
			Output: []byte(`{"haiku": "test"}`),
			Cost:   nil,
		},
	}

	// Template that references a method that doesn't exist on the data type.
	badTmpl := template.Must(template.New("query").Parse("{{.NonExistentMethod}}"))

	agent, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModel(NewModel(testModel, mp)),
		WithQueryTemplate(badTmpl),
		WithHeartbeatInterval(time.Hour),
	)
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}

	_, err = agent.Run(context.Background(), struct{ Input string }{Input: "input"})
	if err == nil {
		t.Fatal("Run() should return error when template rendering fails")
	}
	if !strings.Contains(err.Error(), "query template render failed") {
		t.Fatalf("expected 'query template render failed' in error, got: %v", err)
	}
}
