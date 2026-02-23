package proxy

import (
	"context"
	"log/slog"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// MonitorProxy implements flowv1.FlowMonitorServiceServer by forwarding
// calls to the real Flow Monitor gRPC endpoint. For AddFriction and
// RecordTelemetry, identity fields (flow_id, workitem_id, node_id) are
// extracted from the Sidecar-enriched incoming metadata and injected into
// the request body before forwarding, because the SDK omits these fields
// and the upstream Monitor requires them.
type MonitorProxy struct {
	flowv1.UnimplementedFlowMonitorServiceServer
	client flowv1.FlowMonitorServiceClient
	conn   *grpc.ClientConn
}

// NewMonitorProxy dials the Flow Monitor gRPC endpoint and returns a proxy
// handler ready to be registered on the Sidecar's gRPC server.
func NewMonitorProxy(monitorAddr string) (*MonitorProxy, error) {
	conn, err := grpc.NewClient(
		monitorAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, err
	}

	return &MonitorProxy{
		client: flowv1.NewFlowMonitorServiceClient(conn),
		conn:   conn,
	}, nil
}

// Close releases the underlying gRPC connection to the Flow Monitor.
func (p *MonitorProxy) Close() error {
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// AddFriction enforces the WRITE:friction capability gate and then injects
// Sidecar-authoritative identity into the request before forwarding to the
// upstream Monitor. Per spec (specs/03-node/02-configuration.md), WRITE:friction
// is the one capability enforced by the Sidecar rather than the owning service.
// Node-supplied flow_id, workitem_id, and node_id are overwritten with the
// values from the Sidecar session to prevent spoofing.
func (p *MonitorProxy) AddFriction(
	ctx context.Context, req *flowv1.AddFrictionRequest,
) (*flowv1.AddFrictionResponse, error) {
	// WRITE:friction is Sidecar-enforced (spec exception).
	if err := checkCapability(ctx, "WRITE:friction"); err != nil {
		return nil, err
	}

	flowID, workitemID, nodeID := extractIdentityFromMetadata(ctx)

	// Overwrite identity fields with Sidecar-authoritative values.
	req.FlowId = flowID
	req.WorkitemId = workitemID
	req.NodeId = nodeID

	outCtx := propagateMetadata(ctx)

	slog.Info("Forwarding AddFriction to Monitor",
		"flow_id", flowID,
		"workitem_id", workitemID,
		"node_id", nodeID,
		"magnitude", req.GetMagnitude(),
	)

	return p.client.AddFriction(outCtx, req)
}

// RecordTelemetry injects Sidecar-authoritative identity into the request
// and forwards to the upstream Monitor. The SDK convenience method only sets
// event_type and payload; flow_id, workitem_id, and node_id are injected
// here from the session metadata.
func (p *MonitorProxy) RecordTelemetry(
	ctx context.Context, req *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	flowID, workitemID, nodeID := extractIdentityFromMetadata(ctx)

	// Inject identity fields from Sidecar session.
	req.FlowId = flowID
	req.WorkitemId = workitemID
	req.NodeId = nodeID

	outCtx := propagateMetadata(ctx)

	slog.Info("Forwarding RecordTelemetry to Monitor",
		"flow_id", flowID,
		"workitem_id", workitemID,
		"node_id", nodeID,
		"event_type", req.GetEventType(),
	)

	return p.client.RecordTelemetry(outCtx, req)
}

// QueryFriction forwards to the upstream Monitor (passthrough with metadata
// propagation). No identity injection into the request body is needed since
// QueryFriction uses a FrictionFilter supplied by the caller.
func (p *MonitorProxy) QueryFriction(
	ctx context.Context, req *flowv1.QueryFrictionRequest,
) (*flowv1.QueryFrictionResponse, error) {
	return p.client.QueryFriction(propagateMetadata(ctx), req)
}

// checkCapability is the Sidecar-side capability gate for WRITE:friction.
// It reads x-flow-capabilities and x-flow-node-id from incoming gRPC metadata.
// If x-flow-node-id is absent (system-to-system call), the check passes.
// If x-flow-node-id is present (node-originated), the required capability
// must be present in x-flow-capabilities or the request is denied.
func checkCapability(ctx context.Context, required string) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil // No metadata — system call.
	}
	nodeIDs := md.Get("x-flow-node-id")
	if len(nodeIDs) == 0 {
		return nil // No node identity — system call.
	}

	caps := md.Get("x-flow-capabilities")
	for _, c := range caps {
		for cap := range strings.SplitSeq(c, ",") {
			if strings.TrimSpace(cap) == required {
				return nil
			}
		}
	}

	return status.Errorf(codes.PermissionDenied,
		"CAPABILITY_DENIED: missing required capability %q", required)
}
