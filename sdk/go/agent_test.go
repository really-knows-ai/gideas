package flow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
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

// ---------------------------------------------------------------------------
// Tests — Construction
// ---------------------------------------------------------------------------

func TestNewAgent_ValidSchema(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-agent-001")

	agent, err := NewAgent(env.client, []byte(validHaikuSchema))
	if err != nil {
		t.Fatalf("NewAgent() with valid schema returned error: %v", err)
	}
	if agent == nil {
		t.Fatal("NewAgent() returned nil agent")
	}
}

func TestNewAgent_InvalidSchemaJSON(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-agent-002")

	_, err := NewAgent(env.client, []byte(invalidSchemaJSON))
	if err == nil {
		t.Fatal("NewAgent() with invalid JSON should return error")
	}
	if !strings.Contains(err.Error(), "invalid output schema") {
		t.Fatalf("expected 'invalid output schema' in error, got: %v", err)
	}
}

func TestNewAgent_NilClient(t *testing.T) {
	_, err := NewAgent(nil, []byte(validHaikuSchema))
	if err == nil {
		t.Fatal("NewAgent() with nil client should return error")
	}
	if !strings.Contains(err.Error(), "client must not be nil") {
		t.Fatalf("expected 'client must not be nil' in error, got: %v", err)
	}
}

func TestNewAgent_DefaultHeartbeatInterval(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-agent-003")

	agent, err := NewAgent(env.client, []byte(validHaikuSchema))
	if err != nil {
		t.Fatalf("NewAgent() returned error: %v", err)
	}
	if agent.cfg.heartbeatInterval != DefaultHeartbeatInterval {
		t.Fatalf("expected default heartbeat interval %v, got %v",
			DefaultHeartbeatInterval, agent.cfg.heartbeatInterval)
	}
}

func TestNewAgent_CustomHeartbeatInterval(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-agent-004")

	agent, err := NewAgent(env.client, []byte(validHaikuSchema),
		WithHeartbeatInterval(5*time.Second))
	if err != nil {
		t.Fatalf("NewAgent() returned error: %v", err)
	}
	if agent.cfg.heartbeatInterval != 5*time.Second {
		t.Fatalf("expected heartbeat interval 5s, got %v", agent.cfg.heartbeatInterval)
	}
}

// ---------------------------------------------------------------------------
// Tests — Run: Output Validation
// ---------------------------------------------------------------------------

func TestAgent_Run_OutputValidation_Pass(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-val-pass")

	agent, err := NewAgent(env.client, []byte(validHaikuSchema),
		WithHeartbeatInterval(time.Hour)) // long interval to avoid heartbeat noise
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}

	validOutput := []byte(`{"haiku": "autumn moonlight"}`)

	infer := func(ctx context.Context, input []byte) (*InferResult, error) {
		return &InferResult{
			Output:       validOutput,
			Model:        "test-model",
			InputTokens:  10,
			OutputTokens: 5,
			DurationMs:   100,
		}, nil
	}

	got, err := agent.Run(context.Background(), infer, []byte("write a haiku"))
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if string(got) != string(validOutput) {
		t.Fatalf("Run() output = %s, want %s", got, validOutput)
	}
}

func TestAgent_Run_OutputValidation_Fail(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-val-fail")

	agent, err := NewAgent(env.client, []byte(validHaikuSchema),
		WithHeartbeatInterval(time.Hour))
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}

	// Output missing required "haiku" field.
	invalidOutput := []byte(`{"title": "not a haiku"}`)

	infer := func(ctx context.Context, input []byte) (*InferResult, error) {
		return &InferResult{
			Output:       invalidOutput,
			Model:        "test-model",
			InputTokens:  10,
			OutputTokens: 5,
			DurationMs:   100,
		}, nil
	}

	_, err = agent.Run(context.Background(), infer, []byte("write a haiku"))
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

	agent, err := NewAgent(env.client, []byte(validHaikuSchema),
		WithHeartbeatInterval(time.Hour))
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}

	infer := func(ctx context.Context, input []byte) (*InferResult, error) {
		return &InferResult{
			Output:       []byte("this is not JSON"),
			Model:        "test-model",
			InputTokens:  10,
			OutputTokens: 5,
			DurationMs:   100,
		}, nil
	}

	_, err = agent.Run(context.Background(), infer, []byte("input"))
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

	agent, err := NewAgent(env.client, []byte(validHaikuSchema),
		WithHeartbeatInterval(time.Hour))
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}

	infer := func(ctx context.Context, input []byte) (*InferResult, error) {
		return &InferResult{
			Output:       []byte(`{"haiku": "test haiku"}`),
			Model:        "gpt-4o",
			InputTokens:  150,
			OutputTokens: 30,
			DurationMs:   2500,
			Extra:        map[string]any{"provider": "openai", "cached_tokens": int64(50)},
		}, nil
	}

	_, err = agent.Run(context.Background(), infer, []byte("input"))
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

// ---------------------------------------------------------------------------
// Tests — Run: Multi-Step Accounting
// ---------------------------------------------------------------------------

func TestAgent_Run_MultiStep(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-multi-001")

	agent, err := NewAgent(env.client, []byte(validHaikuSchema),
		WithHeartbeatInterval(time.Hour))
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}

	// First inference step — generate.
	infer1 := func(ctx context.Context, input []byte) (*InferResult, error) {
		return &InferResult{
			Output:       []byte(`{"haiku": "first draft"}`),
			Model:        "gpt-4o",
			InputTokens:  100,
			OutputTokens: 20,
			DurationMs:   1000,
		}, nil
	}

	// Second inference step — revise.
	infer2 := func(ctx context.Context, input []byte) (*InferResult, error) {
		return &InferResult{
			Output:       []byte(`{"haiku": "revised draft"}`),
			Model:        "gpt-4o-mini",
			InputTokens:  200,
			OutputTokens: 25,
			DurationMs:   800,
		}, nil
	}

	if _, err := agent.Run(context.Background(), infer1, []byte("generate")); err != nil {
		t.Fatalf("Run() step 1 error: %v", err)
	}
	if _, err := agent.Run(context.Background(), infer2, []byte("revise")); err != nil {
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

	// Use a very short heartbeat interval to trigger multiple beats
	// during a simulated slow inference.
	agent, err := NewAgent(env.client, []byte(validHaikuSchema),
		WithHeartbeatInterval(20*time.Millisecond))
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}

	infer := func(ctx context.Context, input []byte) (*InferResult, error) {
		// Simulate a slow inference call.
		time.Sleep(120 * time.Millisecond)
		return &InferResult{
			Output:       []byte(`{"haiku": "slow inference"}`),
			Model:        "test-model",
			InputTokens:  10,
			OutputTokens: 5,
			DurationMs:   120,
		}, nil
	}

	_, err = agent.Run(context.Background(), infer, []byte("input"))
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

	agent, err := NewAgent(env.client, []byte(validHaikuSchema),
		WithHeartbeatInterval(10*time.Millisecond))
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}

	infer := func(ctx context.Context, input []byte) (*InferResult, error) {
		time.Sleep(50 * time.Millisecond)
		return &InferResult{
			Output:       []byte(`{"haiku": "done"}`),
			Model:        "test-model",
			InputTokens:  10,
			OutputTokens: 5,
			DurationMs:   50,
		}, nil
	}

	_, err = agent.Run(context.Background(), infer, []byte("input"))
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

func TestAgent_Run_InferError(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-err-001")

	agent, err := NewAgent(env.client, []byte(validHaikuSchema),
		WithHeartbeatInterval(time.Hour))
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}

	inferErr := errors.New("LLM provider unavailable")
	infer := func(ctx context.Context, input []byte) (*InferResult, error) {
		return nil, inferErr
	}

	_, err = agent.Run(context.Background(), infer, []byte("input"))
	if err == nil {
		t.Fatal("Run() should return error when infer fails")
	}
	if !strings.Contains(err.Error(), "infer failed") {
		t.Fatalf("expected 'infer failed' in error, got: %v", err)
	}
	if !errors.Is(err, inferErr) {
		t.Fatalf("expected error to wrap original infer error")
	}

	// No validation or telemetry should have been attempted.
	calls := env.spy.getTelemetryCalls()
	if len(calls) != 0 {
		t.Fatalf("expected no telemetry calls when infer fails, got %d", len(calls))
	}
}

func TestAgent_Run_NilResult(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-err-nil")

	agent, err := NewAgent(env.client, []byte(validHaikuSchema),
		WithHeartbeatInterval(time.Hour))
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}

	infer := func(ctx context.Context, input []byte) (*InferResult, error) {
		return nil, nil
	}

	_, err = agent.Run(context.Background(), infer, []byte("input"))
	if err == nil {
		t.Fatal("Run() should return error when infer returns nil result")
	}
	if !strings.Contains(err.Error(), "nil result") {
		t.Fatalf("expected 'nil result' in error, got: %v", err)
	}
}
