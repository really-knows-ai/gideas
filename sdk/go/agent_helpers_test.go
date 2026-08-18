package flow

import (
	"context"
	"sync/atomic"
	"testing"
	"text/template"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// ---------------------------------------------------------------------------
// Test schemas
// ---------------------------------------------------------------------------

// withHeartbeatInterval is the test-only AgentOption that overrides the
// default managed-heartbeat interval (the production constructor was deleted
// as test-only surface).
func withHeartbeatInterval(d time.Duration) AgentOption {
	return func(c *agentConfig) {
		c.heartbeatInterval = d
	}
}

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
// Agent spy server — captures telemetry calls
// ---------------------------------------------------------------------------

// agentSpyServer extends the standard spy with telemetry call tracking.
type agentSpyServer struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	heartbeatCount atomic.Int64
	telemetryCalls []recordedTelemetry
	lastMD         metadata.MD
}

type recordedTelemetry struct {
	EventType string
	Payload   []byte
}

func (s *agentSpyServer) Heartbeat(
	ctx context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	s.heartbeatCount.Add(1)
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *agentSpyServer) RecordTelemetry(
	ctx context.Context, req *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	s.telemetryCalls = append(s.telemetryCalls, recordedTelemetry{
		EventType: req.GetEventType(),
		Payload:   req.GetPayload(),
	})
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// getTelemetryCalls returns a copy of recorded telemetry calls.
func (s *agentSpyServer) getTelemetryCalls() []recordedTelemetry {
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

// newTestAgent creates a FoundryAgent with a custom InferFunc for testing.
func newTestAgent(t *testing.T, env *agentTestEnv, inferFn InferFunc) *Agent {
	t.Helper()
	agent, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModelName("test-model"),
		WithQueryTemplate(simpleQueryTemplate(t)),
		withHeartbeatInterval(time.Hour), // effectively disable heartbeat
	)
	if err != nil {
		t.Fatalf("NewAgent() error: %v", err)
	}
	agent.inferFn = inferFn
	return agent
}
