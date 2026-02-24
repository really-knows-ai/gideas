// Package proxy implements forwarding handlers that relay gRPC calls
// from the Sidecar to the real cluster services. Each handler wraps a
// generated gRPC client and propagates Sidecar-injected identity metadata
// (x-flow-flow-id, x-flow-workitem-id, x-flow-node-id) from the incoming
// server context to the outgoing client context.
package proxy

import (
	"context"
	"log/slog"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/sidecar/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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
	childTracker service.ChildTracker
}

// NewOperatorProxy dials the Operator gRPC endpoint and returns a proxy
// handler ready to be registered on the Sidecar's gRPC server.
// The childTracker, if non-nil, is notified when CreateChildWorkitem
// succeeds so that the session can authorise cross-Workitem operations.
func NewOperatorProxy(operatorAddr string, childTracker service.ChildTracker) (*OperatorProxy, error) {
	conn, err := grpc.NewClient(
		operatorAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
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
	outCtx := propagateMetadata(ctx)

	slog.Info("Forwarding SubmitResult to Operator",
		"workitem_id", req.GetWorkitemId(),
	)

	resp, err := p.client.SubmitResult(outCtx, req)
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
	outCtx := propagateMetadata(ctx)
	slog.Info("Forwarding CreateWorkitem to Operator")
	return p.client.CreateWorkitem(outCtx, req)
}

// CreateHearingWorkitem forwards to the Operator.
func (p *OperatorProxy) CreateHearingWorkitem(
	ctx context.Context, req *flowv1.CreateHearingWorkitemRequest,
) (*flowv1.CreateHearingWorkitemResponse, error) {
	outCtx := propagateMetadata(ctx)
	slog.Info("Forwarding CreateHearingWorkitem to Operator", "law_id", req.GetLawId())
	return p.client.CreateHearingWorkitem(outCtx, req)
}

// ExportWorkitem forwards to the Operator.
func (p *OperatorProxy) ExportWorkitem(
	ctx context.Context, req *flowv1.ExportWorkitemRequest,
) (*flowv1.ExportWorkitemResponse, error) {
	outCtx := propagateMetadata(ctx)
	slog.Info("Forwarding ExportWorkitem to Operator", "workitem_id", req.GetWorkitemId())
	return p.client.ExportWorkitem(outCtx, req)
}

// ImportWorkitem forwards to the Operator.
func (p *OperatorProxy) ImportWorkitem(
	ctx context.Context, req *flowv1.ImportWorkitemRequest,
) (*flowv1.ImportWorkitemResponse, error) {
	outCtx := propagateMetadata(ctx)
	slog.Info("Forwarding ImportWorkitem to Operator", "treaty", req.GetTreatyName())
	return p.client.ImportWorkitem(outCtx, req)
}

// GetFlowTopology forwards the topology discovery request to the Operator.
// Identity metadata (flow_id, node_id) is propagated from the incoming
// Sidecar-enriched context so the Operator can resolve the calling node's
// view of the flow topology.
func (p *OperatorProxy) GetFlowTopology(
	ctx context.Context, req *flowv1.GetFlowTopologyRequest,
) (*flowv1.GetFlowTopologyResponse, error) {
	outCtx := propagateMetadata(ctx)
	slog.Info("Forwarding GetFlowTopology to Operator")
	return p.client.GetFlowTopology(outCtx, req)
}

// ---------------------------------------------------------------------------
// Child Workitem RPCs
// ---------------------------------------------------------------------------

// CreateChildWorkitem forwards to the Operator and, on success, records the
// child Workitem ID in the session's local cache via the ChildTracker.
// This enables the ArchivistProxy to authorise cross-Workitem operations
// (e.g. StoreArtefact on a child) without an Operator round-trip.
func (p *OperatorProxy) CreateChildWorkitem(
	ctx context.Context, req *flowv1.CreateChildWorkitemRequest,
) (*flowv1.CreateChildWorkitemResponse, error) {
	outCtx := propagateMetadata(ctx)

	slog.Info("Forwarding CreateChildWorkitem to Operator")

	resp, err := p.client.CreateChildWorkitem(outCtx, req)
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
	outCtx := propagateMetadata(ctx)
	slog.Info("Forwarding RouteChild to Operator",
		"child_workitem_id", req.GetChildWorkitemId(),
	)
	return p.client.RouteChild(outCtx, req)
}

// GetChildren forwards to the Operator. The Operator queries children by
// the parent label and returns their status.
func (p *OperatorProxy) GetChildren(
	ctx context.Context, req *flowv1.GetChildrenRequest,
) (*flowv1.GetChildrenResponse, error) {
	outCtx := propagateMetadata(ctx)
	slog.Info("Forwarding GetChildren to Operator")
	return p.client.GetChildren(outCtx, req)
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

// propagateMetadata copies incoming gRPC metadata from the server context
// to outgoing metadata on a new client context. The identity injection
// interceptor (service.IdentityInterceptor) enriches the incoming metadata
// with authoritative x-flow-flow-id, x-flow-workitem-id, and x-flow-node-id
// before this function is called, so all proxied requests carry the complete
// Sidecar-injected identity context.
func propagateMetadata(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}
	return metadata.NewOutgoingContext(ctx, md)
}
