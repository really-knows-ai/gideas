// Package rpc implements the Operator's gRPC service layer.
//
// The gRPC handlers are deliberately thin. Their sole responsibility is to
// translate incoming gRPC requests into Kubernetes CRD state mutations. All
// downstream consequences (routing, lifecycle transitions) are handled by
// the controller reconciliation loop.
package rpc

import (
	"context"
	"fmt"
	"log/slog"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	apiv1 "github.com/gideas/flow/operator/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// metadataKeyWorkitemID is the gRPC metadata key carrying the Sidecar-injected
	// workitem identity.
	metadataKeyWorkitemID = "x-flow-workitem-id"

	// defaultNamespace is used when no namespace context is available.
	// In production this would be derived from the Sidecar's pod namespace.
	defaultNamespace = "default"

	// phaseRouting signals to the reconciler that a routing instruction
	// has been submitted and needs processing.
	phaseRouting = "Routing"
)

// OperatorServer implements the flowv1.OperatorServiceServer interface.
// It holds a reference to the controller-runtime Kubernetes client for
// reading and updating CRDs.
type OperatorServer struct {
	flowv1.UnimplementedOperatorServiceServer
	K8sClient client.Client
}

// NewOperatorServer returns an OperatorServer wired to the given Kubernetes client.
func NewOperatorServer(k8sClient client.Client) *OperatorServer {
	return &OperatorServer{K8sClient: k8sClient}
}

// SubmitResult handles the node's routing instruction submission.
//
// Flow:
//  1. Resolve workitem_id from the request body, falling back to gRPC metadata.
//  2. Fetch the Workitem CRD from the cluster.
//  3. Update status.routingInstruction with the incoming data.
//  4. Transition status.phase to "Routing" to signal the reconciler.
//  5. Return a successful Ack.
func (s *OperatorServer) SubmitResult(ctx context.Context, req *flowv1.SubmitResultRequest) (*flowv1.SubmitResultResponse, error) {
	// 1. Resolve workitem ID.
	workitemID := req.GetWorkitemId()
	if workitemID == "" {
		workitemID = extractWorkitemID(ctx)
	}
	if workitemID == "" {
		return nil, status.Error(codes.InvalidArgument, "workitem_id is required (in request body or x-flow-workitem-id metadata)")
	}

	slog.Info("SubmitResult received",
		"workitem_id", workitemID,
		"routing_type", routingTypeString(req.GetRoutingInstruction()),
		"target", routingTargetString(req.GetRoutingInstruction()),
	)

	// 2. Fetch the Workitem CRD.
	var workitem apiv1.Workitem
	key := types.NamespacedName{
		Namespace: defaultNamespace,
		Name:      workitemID,
	}
	if err := s.K8sClient.Get(ctx, key, &workitem); err != nil {
		slog.Error("Failed to fetch Workitem", "workitem_id", workitemID, "error", err)
		return nil, status.Error(codes.NotFound, fmt.Sprintf("workitem %q not found: %v", workitemID, err))
	}

	// 3. Update routing instruction.
	workitem.Status.RoutingInstruction = convertRoutingInstruction(req.GetRoutingInstruction())

	// 4. Transition phase to Routing.
	workitem.Status.Phase = phaseRouting

	// 5. Persist the status update.
	if err := s.K8sClient.Status().Update(ctx, &workitem); err != nil {
		slog.Error("Failed to update Workitem status", "workitem_id", workitemID, "error", err)
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to update workitem status: %v", err))
	}

	slog.Info("Workitem status updated",
		"workitem_id", workitemID,
		"phase", phaseRouting,
	)

	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// extractWorkitemID reads the x-flow-workitem-id from incoming gRPC metadata.
func extractWorkitemID(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(metadataKeyWorkitemID)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// convertRoutingInstruction maps the proto RoutingInstruction to the CRD type.
func convertRoutingInstruction(ri *flowv1.RoutingInstruction) *apiv1.RoutingInstruction {
	if ri == nil {
		return nil
	}

	var routingType string
	switch ri.GetType() {
	case flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO_OUTPUT:
		routingType = "route_to_output"
	case flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO:
		routingType = "route_to"
	case flowv1.RoutingType_ROUTING_TYPE_COMPLETE:
		routingType = "complete"
	default:
		routingType = "unknown"
	}

	return &apiv1.RoutingInstruction{
		Type:   routingType,
		Target: ri.GetTarget(),
	}
}

// routingTypeString returns a loggable string for the routing type.
func routingTypeString(ri *flowv1.RoutingInstruction) string {
	if ri == nil {
		return "<nil>"
	}
	return ri.GetType().String()
}

// routingTargetString returns a loggable string for the routing target.
func routingTargetString(ri *flowv1.RoutingInstruction) string {
	if ri == nil {
		return "<nil>"
	}
	return ri.GetTarget()
}
