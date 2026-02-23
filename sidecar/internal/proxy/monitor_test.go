package proxy

import (
	"context"
	"net"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// captureMonitorProxyServer captures FlowMonitorService RPC calls for assertions.
type captureMonitorProxyServer struct {
	flowv1.UnimplementedFlowMonitorServiceServer
	lastFrictionReq  *flowv1.AddFrictionRequest
	lastTelemetryReq *flowv1.RecordTelemetryRequest
	lastQueryReq     *flowv1.QueryFrictionRequest
	capturedMD       metadata.MD

	// Configurable responses.
	frictionResp  *flowv1.AddFrictionResponse
	telemetryResp *flowv1.RecordTelemetryResponse
	queryResp     *flowv1.QueryFrictionResponse
}

func (s *captureMonitorProxyServer) AddFriction(
	ctx context.Context, req *flowv1.AddFrictionRequest,
) (*flowv1.AddFrictionResponse, error) {
	s.lastFrictionReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	if s.frictionResp != nil {
		return s.frictionResp, nil
	}
	return &flowv1.AddFrictionResponse{Acknowledged: true}, nil
}

func (s *captureMonitorProxyServer) RecordTelemetry(
	ctx context.Context, req *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	s.lastTelemetryReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	if s.telemetryResp != nil {
		return s.telemetryResp, nil
	}
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

func (s *captureMonitorProxyServer) QueryFriction(
	ctx context.Context, req *flowv1.QueryFrictionRequest,
) (*flowv1.QueryFrictionResponse, error) {
	s.lastQueryReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	if s.queryResp != nil {
		return s.queryResp, nil
	}
	return &flowv1.QueryFrictionResponse{}, nil
}

type monitorTestEnv struct {
	proxy      *MonitorProxy
	monitorSpy *captureMonitorProxyServer
}

func setupMonitorProxy(t *testing.T) *monitorTestEnv {
	t.Helper()

	monLis := bufconn.Listen(1024 * 1024)
	monSrv := grpc.NewServer()
	spy := &captureMonitorProxyServer{}
	flowv1.RegisterFlowMonitorServiceServer(monSrv, spy)
	go func() { _ = monSrv.Serve(monLis) }()
	t.Cleanup(func() { monSrv.Stop(); _ = monLis.Close() })

	monConn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return monLis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial monitor bufconn: %v", err)
	}
	t.Cleanup(func() { _ = monConn.Close() })

	p := &MonitorProxy{
		client: flowv1.NewFlowMonitorServiceClient(monConn),
		conn:   monConn,
	}

	return &monitorTestEnv{
		proxy:      p,
		monitorSpy: spy,
	}
}

// identityCtx creates a context with Sidecar-enriched identity metadata,
// simulating what the IdentityInterceptor produces. Includes WRITE:friction
// capability since these tests target AddFriction forwarding.
func identityCtx(flowID, workitemID, nodeID string) context.Context {
	md := metadata.Pairs(
		"x-flow-flow-id", flowID,
		"x-flow-workitem-id", workitemID,
		"x-flow-node-id", nodeID,
		"x-flow-capabilities", "WRITE:friction",
	)
	return metadata.NewIncomingContext(context.Background(), md)
}

// ---------------------------------------------------------------------------
// AddFriction tests
// ---------------------------------------------------------------------------

func TestMonitorProxy_AddFriction_InjectsIdentityAndForwards(t *testing.T) {
	env := setupMonitorProxy(t)

	ctx := identityCtx("flow-A", "wi-42", "node-X")

	resp, err := env.proxy.AddFriction(ctx, &flowv1.AddFrictionRequest{
		LawIds:    []string{"law-1", "law-2"},
		Magnitude: 3,
	})
	if err != nil {
		t.Fatalf("AddFriction: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	// Verify the request was forwarded with injected identity.
	req := env.monitorSpy.lastFrictionReq
	if req == nil {
		t.Fatal("AddFriction was not forwarded to Monitor backend")
	}
	if req.GetFlowId() != "flow-A" {
		t.Fatalf("expected flow_id=flow-A, got %q", req.GetFlowId())
	}
	if req.GetWorkitemId() != "wi-42" {
		t.Fatalf("expected workitem_id=wi-42, got %q", req.GetWorkitemId())
	}
	if req.GetNodeId() != "node-X" {
		t.Fatalf("expected node_id=node-X, got %q", req.GetNodeId())
	}
	if req.GetMagnitude() != 3 {
		t.Fatalf("expected magnitude=3, got %d", req.GetMagnitude())
	}
	if len(req.GetLawIds()) != 2 {
		t.Fatalf("expected 2 law_ids, got %d", len(req.GetLawIds()))
	}
}

func TestMonitorProxy_AddFriction_OverwritesNodeSuppliedIdentity(t *testing.T) {
	env := setupMonitorProxy(t)

	ctx := identityCtx("flow-real", "wi-real", "node-real")

	// Node tries to spoof identity by setting fields in the request body.
	resp, err := env.proxy.AddFriction(ctx, &flowv1.AddFrictionRequest{
		FlowId:     "flow-spoofed",
		WorkitemId: "wi-spoofed",
		NodeId:     "node-spoofed",
		Magnitude:  1,
	})
	if err != nil {
		t.Fatalf("AddFriction: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	req := env.monitorSpy.lastFrictionReq
	if req == nil {
		t.Fatal("AddFriction was not forwarded")
	}
	// Spoofed values should be overwritten with Sidecar-authoritative ones.
	if req.GetFlowId() != "flow-real" {
		t.Fatalf("expected flow_id=flow-real (overwritten), got %q", req.GetFlowId())
	}
	if req.GetWorkitemId() != "wi-real" {
		t.Fatalf("expected workitem_id=wi-real (overwritten), got %q", req.GetWorkitemId())
	}
	if req.GetNodeId() != "node-real" {
		t.Fatalf("expected node_id=node-real (overwritten), got %q", req.GetNodeId())
	}
}

// ---------------------------------------------------------------------------
// RecordTelemetry tests
// ---------------------------------------------------------------------------

func TestMonitorProxy_RecordTelemetry_InjectsIdentityAndForwards(t *testing.T) {
	env := setupMonitorProxy(t)

	ctx := identityCtx("flow-B", "wi-99", "node-Y")

	resp, err := env.proxy.RecordTelemetry(ctx, &flowv1.RecordTelemetryRequest{
		EventType: "foundry.test.event",
		Payload:   []byte(`{"key":"value"}`),
	})
	if err != nil {
		t.Fatalf("RecordTelemetry: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	req := env.monitorSpy.lastTelemetryReq
	if req == nil {
		t.Fatal("RecordTelemetry was not forwarded to Monitor backend")
	}
	if req.GetFlowId() != "flow-B" {
		t.Fatalf("expected flow_id=flow-B, got %q", req.GetFlowId())
	}
	if req.GetWorkitemId() != "wi-99" {
		t.Fatalf("expected workitem_id=wi-99, got %q", req.GetWorkitemId())
	}
	if req.GetNodeId() != "node-Y" {
		t.Fatalf("expected node_id=node-Y, got %q", req.GetNodeId())
	}
	if req.GetEventType() != "foundry.test.event" {
		t.Fatalf("expected event_type=foundry.test.event, got %q", req.GetEventType())
	}
	if string(req.GetPayload()) != `{"key":"value"}` {
		t.Fatalf("expected payload preserved, got %q", string(req.GetPayload()))
	}
}

func TestMonitorProxy_RecordTelemetry_OverwritesNodeSuppliedIdentity(t *testing.T) {
	env := setupMonitorProxy(t)

	ctx := identityCtx("flow-real", "wi-real", "node-real")

	// Node tries to spoof identity.
	_, err := env.proxy.RecordTelemetry(ctx, &flowv1.RecordTelemetryRequest{
		FlowId:     "flow-spoofed",
		WorkitemId: "wi-spoofed",
		NodeId:     "node-spoofed",
		EventType:  "foundry.test.spoof",
		Payload:    []byte("{}"),
	})
	if err != nil {
		t.Fatalf("RecordTelemetry: %v", err)
	}

	req := env.monitorSpy.lastTelemetryReq
	if req == nil {
		t.Fatal("RecordTelemetry was not forwarded")
	}
	if req.GetFlowId() != "flow-real" {
		t.Fatalf("expected flow_id=flow-real (overwritten), got %q", req.GetFlowId())
	}
	if req.GetWorkitemId() != "wi-real" {
		t.Fatalf("expected workitem_id=wi-real (overwritten), got %q", req.GetWorkitemId())
	}
	if req.GetNodeId() != "node-real" {
		t.Fatalf("expected node_id=node-real (overwritten), got %q", req.GetNodeId())
	}
}

// ---------------------------------------------------------------------------
// QueryFriction tests
// ---------------------------------------------------------------------------

func TestMonitorProxy_QueryFriction_ForwardsAndReturnsResponse(t *testing.T) {
	env := setupMonitorProxy(t)

	// Configure a response with aggregates.
	env.monitorSpy.queryResp = &flowv1.QueryFrictionResponse{
		FrictionAggregates: []*flowv1.FrictionAggregate{
			{LawId: "law-1", NodeId: "node-A", TotalMagnitude: 10, EventCount: 3},
			{LawId: "law-2", NodeId: "node-B", TotalMagnitude: 5, EventCount: 1},
		},
	}

	resp, err := env.proxy.QueryFriction(context.Background(), &flowv1.QueryFrictionRequest{
		Filter: &flowv1.FrictionFilter{
			LawId:  "law-1",
			NodeId: "node-A",
		},
	})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}

	if env.monitorSpy.lastQueryReq == nil {
		t.Fatal("QueryFriction was not forwarded to Monitor backend")
	}
	if env.monitorSpy.lastQueryReq.GetFilter().GetLawId() != "law-1" {
		t.Fatalf("expected filter law_id=law-1, got %q", env.monitorSpy.lastQueryReq.GetFilter().GetLawId())
	}

	aggs := resp.GetFrictionAggregates()
	if len(aggs) != 2 {
		t.Fatalf("expected 2 aggregates, got %d", len(aggs))
	}
	if aggs[0].GetLawId() != "law-1" || aggs[0].GetTotalMagnitude() != 10 {
		t.Fatalf("unexpected first aggregate: %+v", aggs[0])
	}
}

func TestMonitorProxy_QueryFriction_PropagatesMetadata(t *testing.T) {
	env := setupMonitorProxy(t)

	md := metadata.Pairs(
		"x-flow-flow-id", "flow-meta",
		"x-flow-workitem-id", "wi-meta",
		"x-flow-node-id", "node-meta",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := env.proxy.QueryFriction(ctx, &flowv1.QueryFrictionRequest{})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}

	assertMD := func(key, expected string) {
		t.Helper()
		vals := env.monitorSpy.capturedMD.Get(key)
		if len(vals) != 1 || vals[0] != expected {
			t.Fatalf("expected %s=%s in forwarded metadata, got %v", key, expected, vals)
		}
	}

	assertMD("x-flow-flow-id", "flow-meta")
	assertMD("x-flow-workitem-id", "wi-meta")
	assertMD("x-flow-node-id", "node-meta")
}

// ---------------------------------------------------------------------------
// Metadata propagation for ingestion RPCs
// ---------------------------------------------------------------------------

func TestMonitorProxy_AddFriction_PropagatesMetadata(t *testing.T) {
	env := setupMonitorProxy(t)

	ctx := identityCtx("flow-md", "wi-md", "node-md")

	_, err := env.proxy.AddFriction(ctx, &flowv1.AddFrictionRequest{Magnitude: 1})
	if err != nil {
		t.Fatalf("AddFriction: %v", err)
	}

	assertMD := func(key, expected string) {
		t.Helper()
		vals := env.monitorSpy.capturedMD.Get(key)
		if len(vals) != 1 || vals[0] != expected {
			t.Fatalf("expected %s=%s in forwarded metadata, got %v", key, expected, vals)
		}
	}

	assertMD("x-flow-flow-id", "flow-md")
	assertMD("x-flow-workitem-id", "wi-md")
	assertMD("x-flow-node-id", "node-md")
}

func TestMonitorProxy_RecordTelemetry_PropagatesMetadata(t *testing.T) {
	env := setupMonitorProxy(t)

	ctx := identityCtx("flow-md2", "wi-md2", "node-md2")

	_, err := env.proxy.RecordTelemetry(ctx, &flowv1.RecordTelemetryRequest{
		EventType: "test",
	})
	if err != nil {
		t.Fatalf("RecordTelemetry: %v", err)
	}

	assertMD := func(key, expected string) {
		t.Helper()
		vals := env.monitorSpy.capturedMD.Get(key)
		if len(vals) != 1 || vals[0] != expected {
			t.Fatalf("expected %s=%s in forwarded metadata, got %v", key, expected, vals)
		}
	}

	assertMD("x-flow-flow-id", "flow-md2")
	assertMD("x-flow-workitem-id", "wi-md2")
	assertMD("x-flow-node-id", "node-md2")
}

// ---------------------------------------------------------------------------
// No metadata (graceful degradation)
// ---------------------------------------------------------------------------

func TestMonitorProxy_RecordTelemetry_NoMetadata_StillForwards(t *testing.T) {
	env := setupMonitorProxy(t)

	// No identity metadata — simulates a system-to-system call.
	resp, err := env.proxy.RecordTelemetry(context.Background(), &flowv1.RecordTelemetryRequest{
		EventType: "system.event",
		Payload:   []byte("{}"),
	})
	if err != nil {
		t.Fatalf("RecordTelemetry: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	req := env.monitorSpy.lastTelemetryReq
	if req == nil {
		t.Fatal("RecordTelemetry was not forwarded")
	}
	// Identity fields should be empty (not panicked or failed).
	if req.GetFlowId() != "" {
		t.Fatalf("expected empty flow_id, got %q", req.GetFlowId())
	}
	if req.GetEventType() != "system.event" {
		t.Fatalf("expected event_type=system.event, got %q", req.GetEventType())
	}
}

// ---------------------------------------------------------------------------
// WRITE:friction capability enforcement tests
// ---------------------------------------------------------------------------

// nodeCtxWithCaps creates a context simulating a node-originated call with
// the given capabilities, as the Sidecar's IdentityInterceptor would produce.
func nodeCtxWithCaps(flowID, workitemID, nodeID, caps string) context.Context {
	md := metadata.Pairs(
		"x-flow-flow-id", flowID,
		"x-flow-workitem-id", workitemID,
		"x-flow-node-id", nodeID,
		"x-flow-capabilities", caps,
	)
	return metadata.NewIncomingContext(context.Background(), md)
}

func TestMonitorProxy_AddFriction_CapabilityDenied(t *testing.T) {
	env := setupMonitorProxy(t)

	// Node call WITHOUT WRITE:friction capability.
	ctx := nodeCtxWithCaps("flow-A", "wi-1", "node-X", "READ:artefact,WRITE:artefact")

	_, err := env.proxy.AddFriction(ctx, &flowv1.AddFrictionRequest{
		Magnitude: 5,
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing WRITE:friction")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}

	// Verify the request was NOT forwarded to the monitor.
	if env.monitorSpy.lastFrictionReq != nil {
		t.Fatal("AddFriction should not have been forwarded when capability is missing")
	}
}

func TestMonitorProxy_AddFriction_CapabilityGranted(t *testing.T) {
	env := setupMonitorProxy(t)

	// Node call WITH WRITE:friction capability.
	ctx := nodeCtxWithCaps("flow-B", "wi-2", "node-Y", "READ:artefact,WRITE:friction")

	resp, err := env.proxy.AddFriction(ctx, &flowv1.AddFrictionRequest{
		Magnitude: 3,
	})
	if err != nil {
		t.Fatalf("expected success with WRITE:friction capability, got %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	// Verify the request WAS forwarded with injected identity.
	req := env.monitorSpy.lastFrictionReq
	if req == nil {
		t.Fatal("AddFriction should have been forwarded")
	}
	if req.GetFlowId() != "flow-B" {
		t.Fatalf("expected flow_id=flow-B, got %q", req.GetFlowId())
	}
}

func TestMonitorProxy_AddFriction_SystemCall_NoCapabilityCheck(t *testing.T) {
	env := setupMonitorProxy(t)

	// System call — no node_id, so capability check should not apply.
	ctx := context.Background()

	resp, err := env.proxy.AddFriction(ctx, &flowv1.AddFrictionRequest{
		Magnitude: 1,
	})
	if err != nil {
		t.Fatalf("system call should bypass capability enforcement, got %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}
}

func TestMonitorProxy_AddFriction_NodeCallNoCapabilities_Denied(t *testing.T) {
	env := setupMonitorProxy(t)

	// Node call with node identity but no capabilities at all.
	md := metadata.Pairs(
		"x-flow-flow-id", "flow-C",
		"x-flow-workitem-id", "wi-3",
		"x-flow-node-id", "node-Z",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := env.proxy.AddFriction(ctx, &flowv1.AddFrictionRequest{
		Magnitude: 1,
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for node call with empty capabilities")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}
