package proxy

import (
	"context"
	"testing"
	"time"

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
	t.Cleanup(func() { _ = env.proxy.Close() })

	env.proxy.PublishFriction(
		"flow-1", "wi-1", "node-1",
		[]string{"law-1", "law-2"},
		3.5,
	)

	// Give async publisher time to drain.
	time.Sleep(100 * time.Millisecond)

	req := env.busSpy.lastPublishReq
	if req == nil {
		t.Fatal("Publish was not called")
	}
	if req.GetChannel() != "telemetry" {
		t.Fatalf("expected telemetry channel, got %v", req.GetChannel())
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

	// Verify law_ids are in labels.
	lawLabels := labelsForKey(evt.GetLabels(), "law_id")
	if len(lawLabels) != 2 || lawLabels[0] != "law-1" || lawLabels[1] != "law-2" {
		t.Fatalf("expected law_id labels [law-1 law-2], got %v", lawLabels)
	}

	if evt.GetAttributes()["magnitude"] != "3.5" {
		t.Fatalf("expected magnitude=3.5, got %q", evt.GetAttributes()["magnitude"])
	}
}

func TestEventBusProxy_PublishTelemetry(t *testing.T) {
	env := setupEventBusProxy(t)
	t.Cleanup(func() { _ = env.proxy.Close() })

	env.proxy.PublishTelemetry(
		"flow-2", "node-2", "wi-2",
		"foundry.cost.llm",
		[]byte(`{"model":"gpt-4"}`),
	)

	// Give async publisher time to drain.
	time.Sleep(100 * time.Millisecond)

	req := env.busSpy.lastPublishReq
	if req == nil {
		t.Fatal("Publish was not called")
	}
	if req.GetChannel() != "telemetry" {
		t.Fatalf("expected telemetry channel, got %v", req.GetChannel())
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
	t.Cleanup(func() { _ = env.proxy.Close() })

	env.proxy.PublishFriction(
		"flow-3", "wi-3", "node-3",
		nil,
		1.0,
	)

	// Give async publisher time to drain.
	time.Sleep(100 * time.Millisecond)

	evt := env.busSpy.lastPublishReq.GetEvent()
	if len(evt.GetLabels()) != 0 {
		t.Fatalf("expected no labels when no law IDs, got %v", evt.GetLabels())
	}
}

// labelsForKey extracts all label values for a given key.
func labelsForKey(labels []*flowv1.Label, key string) []string {
	var vals []string
	for _, l := range labels {
		if l.GetKey() == key {
			vals = append(vals, l.GetValue())
		}
	}
	return vals
}
