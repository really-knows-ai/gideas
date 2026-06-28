// Package proxy implements forwarding handlers that relay gRPC calls
// from the Sidecar to the real cluster services. Each handler wraps a
// generated gRPC client and propagates Sidecar-injected identity metadata
// (x-flow-namespace, x-flow-workitem-id, x-flow-node-id) from the incoming
// server context to the outgoing client context.
package proxy

import (
	"context"
	"fmt"
	"log/slog"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/sidecar/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// OperatorProxy implements flowv1.OperatorServiceServer by forwarding
// all calls to the real Operator gRPC endpoint.
type OperatorProxy struct {
	flowv1.UnimplementedOperatorServiceServer
	client flowv1.OperatorServiceClient
	conn   *grpc.ClientConn

	// childTracker records child Workitem IDs created during the current
	// session so the Sidecar can authorise cross-Workitem operations
	// without an Operator round-trip. May be nil (child tracking disabled).
	childTracker *service.SidecarServer
}

// NewOperatorProxy dials the Operator gRPC endpoint and returns a proxy
// handler ready to be registered on the Sidecar's gRPC server.
// The childTracker, if non-nil, is notified when CreateChildWorkitem
// succeeds so that the session can authorise cross-Workitem operations.
func NewOperatorProxy(operatorAddr string, childTracker *service.SidecarServer) (*OperatorProxy, error) {
	conn, err := dialService(operatorAddr)
	if err != nil {
		return nil, err
	}

	return &OperatorProxy{
		client:       flowv1.NewOperatorServiceClient(conn),
		conn:         conn,
		childTracker: childTracker,
	}, nil
}

// Close releases the underlying gRPC connection to the Operator.
func (p *OperatorProxy) Close() error {
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// SubmitResult forwards the routing instruction to the Operator.
// The x-flow-workitem-id metadata header is propagated from the incoming
// Node request to the outgoing Operator request.
func (p *OperatorProxy) SubmitResult(
	ctx context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	// Log propagated metadata keys for debugging namespace propagation.
	md, _ := metadata.FromIncomingContext(ctx)
	nsKeys := md.Get("x-flow-namespace")
	wKeys := md.Get("x-flow-workitem-id")
	nKeys := md.Get("x-flow-node-id")
	actionStr := "nil"
	if req.GetAction() != nil {
		actionStr = fmt.Sprintf("%T", req.GetAction())
	}
	slog.Info("Forwarding SubmitResult to Operator",
		"workitem_id", req.GetWorkitemId(),
		"action_type", actionStr,
		"x_flow_namespace", nsKeys,
		"x_flow_workitem_id", wKeys,
		"x_flow_node_id", nKeys,
	)

	resp, err := p.client.SubmitResult(ctx, req)
	if err != nil {
		slog.Error("SubmitResult forwarding failed", "error", err)
		return nil, err
	}

	slog.Info("SubmitResult forwarded successfully",
		"workitem_id", req.GetWorkitemId(),
		"accepted", resp.GetAccepted(),
	)
	return resp, nil
}

// CreateWorkitem forwards to the Operator.
func (p *OperatorProxy) CreateWorkitem(
	ctx context.Context, req *flowv1.CreateWorkitemRequest,
) (*flowv1.CreateWorkitemResponse, error) {
	slog.Info("Forwarding CreateWorkitem to Operator")
	return p.client.CreateWorkitem(ctx, req)
}

// GetFlowTopology forwards the topology discovery request to the Operator.
// Identity metadata (namespace, node_id) is propagated from the incoming
// Sidecar-enriched context so the Operator can resolve the calling node's
// view of the flow topology.
func (p *OperatorProxy) GetFlowTopology(
	ctx context.Context, req *flowv1.GetFlowTopologyRequest,
) (*flowv1.GetFlowTopologyResponse, error) {
	slog.Info("Forwarding GetFlowTopology to Operator")
	return p.client.GetFlowTopology(ctx, req)
}

// ---------------------------------------------------------------------------
// Child Workitem RPCs
// ---------------------------------------------------------------------------

// CreateChildWorkitem forwards to the Operator and, on success, records the
// child Workitem ID in the session's local cache.
// This enables the ArchivistProxy to authorise cross-Workitem operations
// (e.g. StoreArtefact on a child) without an Operator round-trip.
func (p *OperatorProxy) CreateChildWorkitem(
	ctx context.Context, req *flowv1.CreateChildWorkitemRequest,
) (*flowv1.CreateChildWorkitemResponse, error) {
	slog.Info("Forwarding CreateChildWorkitem to Operator")

	resp, err := p.client.CreateChildWorkitem(ctx, req)
	if err != nil {
		slog.Error("CreateChildWorkitem forwarding failed", "error", err)
		return nil, err
	}

	// Track the child in the session so the Sidecar can authorise
	// cross-Workitem writes/reads without an Operator round-trip.
	if p.childTracker != nil {
		parentWorkitemID := extractWorkitemIDFromMD(ctx)
		if parentWorkitemID != "" && resp.GetChildWorkitemId() != "" {
			p.childTracker.TrackChild(parentWorkitemID, resp.GetChildWorkitemId())
			slog.Info("Tracked child Workitem in session",
				"parent_workitem_id", parentWorkitemID,
				"child_workitem_id", resp.GetChildWorkitemId(),
			)
		}
	}

	return resp, nil
}

// RouteChild forwards to the Operator. The Operator validates parent-child
// ownership and child state.
func (p *OperatorProxy) RouteChild(
	ctx context.Context, req *flowv1.RouteChildRequest,
) (*flowv1.RouteChildResponse, error) {
	slog.Info("Forwarding RouteChild to Operator",
		"child_workitem_id", req.GetChildWorkitemId(),
	)
	return p.client.RouteChild(ctx, req)
}

// GetChildren forwards to the Operator. The Operator queries children by
// the parent label and returns their status.
func (p *OperatorProxy) GetChildren(
	ctx context.Context, req *flowv1.GetChildrenRequest,
) (*flowv1.GetChildrenResponse, error) {
	slog.Info("Forwarding GetChildren to Operator")
	return p.client.GetChildren(ctx, req)
}

// ResumeWorkitem forwards to the Operator. The Operator validates that the
// target Workitem is in the Suspended phase and transitions it to Pending.
func (p *OperatorProxy) ResumeWorkitem(
	ctx context.Context, req *flowv1.ResumeWorkitemRequest,
) (*flowv1.ResumeWorkitemResponse, error) {
	slog.Info("Forwarding ResumeWorkitem to Operator",
		"workitem_id", req.GetWorkitemId(),
	)
	return p.client.ResumeWorkitem(ctx, req)
}

// ListSuspendedWorkitems forwards to the Operator. Returns suspended workitems
// whose resume condition contains the given filter string.
func (p *OperatorProxy) ListSuspendedWorkitems(
	ctx context.Context, req *flowv1.ListSuspendedWorkitemsRequest,
) (*flowv1.ListSuspendedWorkitemsResponse, error) {
	slog.Info("Forwarding ListSuspendedWorkitems to Operator",
		"condition_contains", req.GetConditionContains(),
	)
	return p.client.ListSuspendedWorkitems(ctx, req)
}

// extractWorkitemIDFromMD reads the workitem ID from incoming gRPC metadata.
// Returns empty string if not present.
func extractWorkitemIDFromMD(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get("x-flow-workitem-id")
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// metadataUnaryInterceptor copies incoming gRPC metadata to the outgoing
// context before invoking the upstream call, replacing per-method
// propagateMetadata() calls.
func metadataUnaryInterceptor(
	ctx context.Context, method string, req, reply any,
	cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption,
) error {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	return invoker(ctx, method, req, reply, cc, opts...)
}

// metadataStreamInterceptor is the streaming variant.
func metadataStreamInterceptor(
	ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn,
	method string, streamer grpc.Streamer, opts ...grpc.CallOption,
) (grpc.ClientStream, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	return streamer(ctx, desc, cc, method, opts...)
}
