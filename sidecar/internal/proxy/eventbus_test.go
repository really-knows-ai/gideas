package proxy

import (
	"context"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
)

// captureEventBusServer captures FlowEventBusService Publish calls for assertions.
type captureEventBusServer struct {
	flowv1.UnimplementedFlowEventBusServiceServer
	lastPublishReq *flowv1.PublishRequest
	publishCount   int
	publishResp    *flowv1.PublishResponse
}

func (s *captureEventBusServer) Publish(
	_ context.Context, req *flowv1.PublishRequest,
) (*flowv1.PublishResponse, error) {
	s.lastPublishReq = req
	s.publishCount++
	if s.publishResp != nil {
		return s.publishResp, nil
	}
	return &flowv1.PublishResponse{Acknowledged: true, Sequence: uint64(s.publishCount)}, nil
}

type eventBusTestEnv struct {
	proxy  *EventBusProxy
	busSpy *captureEventBusServer
}

func setupEventBusProxy(t *testing.T) *eventBusTestEnv {
	t.Helper()

	spy := &captureEventBusServer{}
	conn := dialBufconn(t, func(s *grpc.Server) {
		flowv1.RegisterFlowEventBusServiceServer(s, spy)
	})

	p := NewEventBusProxyFromClient(flowv1.NewFlowEventBusServiceClient(conn))

	return &eventBusTestEnv{
		proxy:  p,
		busSpy: spy,
	}
}

func TestEventBusProxy_PublishFriction(t *testing.T) {
	env := setupEventBusProxy(t)

	err := env.proxy.PublishFriction(
		context.Background(),
		"flow-1", "wi-1", "node-1",
		[]string{"law-1", "law-2"},
		3.5,
	)
	if err != nil {
		t.Fatalf("PublishFriction: %v", err)
	}

	req := env.busSpy.lastPublishReq
	if req == nil {
		t.Fatal("Publish was not called")
	}
	if req.GetChannel() != flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY {
		t.Fatalf("expected TELEMETRY channel, got %v", req.GetChannel())
	}

	evt := req.GetEvent()
	if evt.GetEventType() != "friction" {
		t.Fatalf("expected event_type=friction, got %q", evt.GetEventType())
	}
	if evt.GetFlowId() != "flow-1" {
		t.Fatalf("expected flow_id=flow-1, got %q", evt.GetFlowId())
	}
	if evt.GetWorkitemId() != "wi-1" {
		t.Fatalf("expected workitem_id=wi-1, got %q", evt.GetWorkitemId())
	}
	if evt.GetNodeId() != "node-1" {
		t.Fatalf("expected node_id=node-1, got %q", evt.GetNodeId())
	}
	if evt.GetAttributes()["law_ids"] != "law-1,law-2" {
		t.Fatalf("expected law_ids=law-1,law-2, got %q", evt.GetAttributes()["law_ids"])
	}
	if evt.GetAttributes()["magnitude"] != "3.5" {
		t.Fatalf("expected magnitude=3.5, got %q", evt.GetAttributes()["magnitude"])
	}
}

func TestEventBusProxy_PublishTelemetry(t *testing.T) {
	env := setupEventBusProxy(t)

	err := env.proxy.PublishTelemetry(
		context.Background(),
		"flow-2", "node-2", "wi-2",
		"foundry.cost.llm",
		[]byte(`{"model":"gpt-4"}`),
	)
	if err != nil {
		t.Fatalf("PublishTelemetry: %v", err)
	}

	req := env.busSpy.lastPublishReq
	if req == nil {
		t.Fatal("Publish was not called")
	}
	if req.GetChannel() != flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY {
		t.Fatalf("expected TELEMETRY channel, got %v", req.GetChannel())
	}

	evt := req.GetEvent()
	if evt.GetEventType() != "foundry.cost.llm" {
		t.Fatalf("expected event_type=foundry.cost.llm, got %q", evt.GetEventType())
	}
	if evt.GetFlowId() != "flow-2" {
		t.Fatalf("expected flow_id=flow-2, got %q", evt.GetFlowId())
	}
	if string(evt.GetPayload()) != `{"model":"gpt-4"}` {
		t.Fatalf("expected payload preserved, got %q", string(evt.GetPayload()))
	}
}

func TestEventBusProxy_PublishFriction_NoLawIDs(t *testing.T) {
	env := setupEventBusProxy(t)

	err := env.proxy.PublishFriction(
		context.Background(),
		"flow-3", "wi-3", "node-3",
		nil,
		1.0,
	)
	if err != nil {
		t.Fatalf("PublishFriction: %v", err)
	}

	evt := env.busSpy.lastPublishReq.GetEvent()
	if _, ok := evt.GetAttributes()["law_ids"]; ok {
		t.Fatalf("expected no law_ids attribute when empty, got %q", evt.GetAttributes()["law_ids"])
	}
}
