package flow

import (
	"context"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Tests — Run: Heartbeat During Inference
// ---------------------------------------------------------------------------

func TestAgent_Run_HeartbeatDuringInference(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-hb-001")

	delay := 120 * time.Millisecond
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*InferOutput, error) {
		time.Sleep(delay)
		return &InferOutput{
			Output: []byte(`{"haiku": "slow inference"}`),
			Cost: &CostMetadata{
				Model:        testModel,
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   120,
			},
		}, nil
	}

	// Use a very short heartbeat interval to trigger multiple beats
	// during a simulated slow inference.
	agent, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModelName("test-model"),
		WithQueryTemplate(simpleQueryTemplate(t)),
		withHeartbeatInterval(20*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}
	agent.inferFn = inferFn

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

	delay := 50 * time.Millisecond
	inferFn := func(_ context.Context, _, _ string, _ []byte) (*InferOutput, error) {
		time.Sleep(delay)
		return &InferOutput{
			Output: []byte(`{"haiku": "done"}`),
			Cost: &CostMetadata{
				Model:        testModel,
				InputTokens:  10,
				OutputTokens: 5,
				DurationMs:   50,
			},
		}, nil
	}

	agent, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModelName("test-model"),
		WithQueryTemplate(simpleQueryTemplate(t)),
		withHeartbeatInterval(10*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}
	agent.inferFn = inferFn

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
