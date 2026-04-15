// Package rpc implements the Operator's gRPC service layer.
//
// The gRPC handlers are deliberately thin. Their sole responsibility is to
// translate incoming gRPC requests into Kubernetes CRD state mutations. All
// downstream consequences (routing, lifecycle transitions) are handled by
// the controller reconciliation loop.
package rpc

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"strings"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	apiv1 "github.com/gideas/flow/operator/api/v1"
	"github.com/gideas/flow/pkg/eventbus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// metadataKeyWorkitemID is the gRPC metadata key carrying the Sidecar-injected
	// workitem identity.
	metadataKeyWorkitemID = "x-flow-workitem-id"

	// metadataKeyNamespace is the gRPC metadata key carrying the Sidecar-injected
	// Kubernetes namespace (flow identity boundary).
	metadataKeyNamespace = "x-flow-namespace"

	// metadataKeyNodeID is the gRPC metadata key carrying the Sidecar-injected
	// node identity.
	metadataKeyNodeID = "x-flow-node-id"

	// phaseRouting signals to the reconciler that a routing instruction
	// has been submitted and needs processing.
	phaseRouting = "Routing"

	// phasePending is the initial state for newly created Workitems.
	phasePending = "Pending"

	// phaseCompleted indicates a Workitem has finished processing.
	phaseCompleted = "Completed"

	// routeToType is the routing instruction type for direct node routing.
	routeToType = "route_to"

	// routeToOutputType is the routing instruction type for output routing.
	routeToOutputType = "route_to_output"

	// completeType is the routing instruction type for workitem completion.
	completeType = "complete"

	// suspendType is the routing instruction type for workitem suspension.
	suspendType = "suspend"

	// nilString is a log placeholder for nil values.
	nilString = "<nil>"
)

// OperatorServer implements the flowv1.OperatorServiceServer interface.
// It holds a reference to the controller-runtime Kubernetes client for
// reading and updating CRDs.
type OperatorServer struct {
	flowv1.UnimplementedOperatorServiceServer
	K8sClient client.Client
	Auditor   *eventbus.AsyncPublisher // nil-safe: audit publishing degrades gracefully
}

// NewOperatorServer returns an OperatorServer wired to the given Kubernetes client.
func NewOperatorServer(k8sClient client.Client) *OperatorServer {
	return &OperatorServer{K8sClient: k8sClient}
}

// publishAudit submits an audit event to the async publisher for non-blocking
// delivery to the Event Bus. If the publisher is nil, audit publishing is
// silently disabled.
func (s *OperatorServer) publishAudit(ctx context.Context, eventType string, attrs map[string]string) {
	if s.Auditor == nil {
		return
	}
	s.Auditor.Submit(&flowv1.PublishRequest{
		Channel: "audit",
		Event: &flowv1.FlowEvent{
			EventId:       newAuditEventID(),
			EventType:     eventType,
			FlowNamespace: extractMetadataValue(ctx, metadataKeyNamespace),
			NodeId:        extractMetadataValue(ctx, metadataKeyNodeID),
			WorkitemId:    extractMetadataValue(ctx, metadataKeyWorkitemID),
			Timestamp:     timestamppb.Now(),
			Attributes:    attrs,
		},
	})
}

// resolveFlow lists FoundryFlows in the given namespace and returns the
// singleton. Returns an error if zero or more than one flow is found.
func (s *OperatorServer) resolveFlow(ctx context.Context, namespace string) (*apiv1.FoundryFlow, error) {
	var flows apiv1.FoundryFlowList
	if err := s.K8sClient.List(ctx, &flows, client.InNamespace(namespace)); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to list FoundryFlows in namespace %q: %v", namespace, err))
	}
	if len(flows.Items) == 0 {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("no FoundryFlow found in namespace %q", namespace))
	}
	if len(flows.Items) > 1 {
		return nil, status.Error(codes.FailedPrecondition,
			fmt.Sprintf("expected exactly 1 FoundryFlow in namespace %q, found %d (singleton violation)", namespace, len(flows.Items)))
	}
	return &flows.Items[0], nil
}

// newAuditEventID returns a random hex-encoded identifier for audit events.
func newAuditEventID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return fmt.Sprintf("%x", b)
}

// ---------------------------------------------------------------------------
// Capability enforcement
// ---------------------------------------------------------------------------

const (
	// metadataKeyCapabilities is the gRPC metadata key carrying the
	// Sidecar-injected, comma-separated capability grants for the node.
	metadataKeyCapabilities = "x-flow-capabilities"
)

// checkCapability enforces deny-by-default capability gating for
// node-originated requests. System-to-system calls (no x-flow-node-id)
// pass through unconditionally.
func checkCapability(ctx context.Context, required string) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil // No metadata — system call.
	}
	nodeIDs := md.Get(metadataKeyNodeID)
	if len(nodeIDs) == 0 {
		return nil // No node identity — system call.
	}

	caps := md.Get(metadataKeyCapabilities)
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

// SubmitResult handles the node's routing instruction submission.
//
// Flow:
//  1. Resolve workitem_id from the request body, falling back to gRPC metadata.
//  2. Extract namespace from x-flow-namespace metadata.
//  3. Fetch the Workitem CRD from the cluster.
//  4. Update status.routingInstruction with the incoming data.
//  5. Transition status.phase to "Routing" to signal the reconciler.
//  6. Return a successful Ack.
func (s *OperatorServer) SubmitResult(ctx context.Context, req *flowv1.SubmitResultRequest) (*flowv1.SubmitResultResponse, error) {
	// 1. Resolve workitem ID.
	workitemID := req.GetWorkitemId()
	if workitemID == "" {
		workitemID = extractWorkitemID(ctx)
	}
	if workitemID == "" {
		return nil, status.Error(codes.InvalidArgument, "workitem_id is required (in request body or x-flow-workitem-id metadata)")
	}

	namespace := extractMetadataValue(ctx, metadataKeyNamespace)

	slog.Info("SubmitResult received",
		"workitem_id", workitemID,
		"namespace", namespace,
		"action", submitActionString(req),
	)

	// 2. Fetch the Workitem CRD.
	var workitem apiv1.Workitem
	key := types.NamespacedName{
		Namespace: namespace,
		Name:      workitemID,
	}
	if err := s.K8sClient.Get(ctx, key, &workitem); err != nil {
		slog.Error("Failed to fetch Workitem", "workitem_id", workitemID, "error", err)
		return nil, status.Error(codes.NotFound, fmt.Sprintf("workitem %q not found: %v", workitemID, err))
	}

	// 3. Convert action to CRD routing instruction.
	workitem.Status.RoutingInstruction = convertSubmitAction(req)

	// 3a. Completion guard: reject complete if children are non-terminal.
	if workitem.Status.RoutingInstruction != nil && workitem.Status.RoutingInstruction.Type == completeType {
		if err := s.checkChildrenTerminal(ctx, namespace, workitemID); err != nil {
			return nil, err
		}
	}

	// 3b. NodeGroup routing isolation: reject route_to targeting a non-entry-bound
	// node inside a group from outside that group.
	if workitem.Status.RoutingInstruction != nil && workitem.Status.RoutingInstruction.Type == routeToType {
		sourceNodeID := extractMetadataValue(ctx, metadataKeyNodeID)
		if err := s.checkNodeGroupRouting(ctx, namespace, sourceNodeID, workitem.Status.RoutingInstruction.Target); err != nil {
			return nil, err
		}
	}

	// 3c. Suspend timeout validation: reject if timeout exceeds maxSuspendTimeout,
	// apply defaultSuspendTimeout if none specified.
	if workitem.Status.RoutingInstruction != nil && workitem.Status.RoutingInstruction.Type == suspendType {
		if err := s.validateSuspendTimeout(ctx, namespace, workitem.Status.RoutingInstruction); err != nil {
			return nil, err
		}
	}

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

	s.publishAudit(ctx, "audit.workitem.routing_submitted", map[string]string{
		"action":      "routing_submitted",
		"resource_id": workitemID,
	})

	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

// GetFlowTopology returns the Flow topology visible to the calling node.
//
// Flow:
//  1. Extract namespace and node_id from Sidecar-injected gRPC metadata.
//  2. Resolve the singleton FoundryFlow CRD in the namespace.
//  3. Fetch all FoundryNode CRDs in the namespace.
//  4. Find the calling node's CRD to get its exit binding → resolve to the exit contract.
//  5. Build and return the GetFlowTopologyResponse.
func (s *OperatorServer) GetFlowTopology(ctx context.Context, _ *flowv1.GetFlowTopologyRequest) (*flowv1.GetFlowTopologyResponse, error) {
	// Capability gate: READ:flow.
	if err := checkCapability(ctx, "READ:flow"); err != nil {
		return nil, err
	}

	// 1. Extract identity from metadata.
	namespace := extractMetadataValue(ctx, metadataKeyNamespace)
	nodeID := extractMetadataValue(ctx, metadataKeyNodeID)
	if namespace == "" {
		return nil, status.Error(codes.InvalidArgument, "x-flow-namespace metadata is required")
	}
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "x-flow-node-id metadata is required")
	}

	slog.Info("GetFlowTopology received", "namespace", namespace, "node_id", nodeID)

	// 2. Resolve the singleton FoundryFlow CRD.
	flow, err := s.resolveFlow(ctx, namespace)
	if err != nil {
		return nil, err
	}

	// 3. Fetch all FoundryNode CRDs in the namespace.
	var nodeList apiv1.FoundryNodeList
	if err := s.K8sClient.List(ctx, &nodeList, client.InNamespace(namespace)); err != nil {
		slog.Error("Failed to list FoundryNodes", "error", err)
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to list nodes: %v", err))
	}

	// 4. Find the calling node and build the response.
	var callingNode *apiv1.FoundryNode
	nodes := make(map[string]*flowv1.FlowNode, len(nodeList.Items))
	for i := range nodeList.Items {
		n := &nodeList.Items[i]
		fn := crdNodeToProto(n)
		nodes[n.Name] = fn
		if n.Name == nodeID {
			callingNode = n
		}
	}

	if callingNode == nil {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("calling node %q not found", nodeID))
	}

	// 5. Resolve exit contract if the calling node is exit-bound.
	exitContract := make(map[string]*flowv1.StampRequirements)
	if callingNode.Spec.Exit != "" {
		if contract, ok := flow.Spec.ExitContracts[callingNode.Spec.Exit]; ok {
			for kind, stamps := range contract {
				exitContract[kind] = &flowv1.StampRequirements{Stamps: stamps}
			}
		}
	}

	resp := &flowv1.GetFlowTopologyResponse{
		Self:         nodes[nodeID],
		Nodes:        nodes,
		ExitContract: exitContract,
	}

	slog.Info("GetFlowTopology response built",
		"namespace", namespace,
		"node_id", nodeID,
		"node_count", len(nodes),
		"exit_contract_kinds", len(exitContract),
	)

	return resp, nil
}

// CreateWorkitem creates a new Workitem in Pending state.
//
// Flow:
//  1. Extract namespace and node_id from Sidecar-injected gRPC metadata.
//  2. Resolve the singleton FoundryFlow CRD in the namespace.
//  3. Fetch the calling FoundryNode CRD and verify it is entry-bound.
//  4. Generate a unique Workitem name and create the CRD in Pending.
//  5. Return the workitem_id.
func (s *OperatorServer) CreateWorkitem(ctx context.Context, req *flowv1.CreateWorkitemRequest) (*flowv1.CreateWorkitemResponse, error) {
	// 1. Extract identity from metadata.
	namespace := extractMetadataValue(ctx, metadataKeyNamespace)
	nodeID := extractMetadataValue(ctx, metadataKeyNodeID)
	if namespace == "" {
		return nil, status.Error(codes.InvalidArgument, "x-flow-namespace metadata is required")
	}
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "x-flow-node-id metadata is required")
	}

	slog.Info("CreateWorkitem received", "namespace", namespace, "node_id", nodeID)

	// 2. Resolve the singleton FoundryFlow CRD.
	flow, err := s.resolveFlow(ctx, namespace)
	if err != nil {
		return nil, err
	}

	// 3. Fetch the calling node and verify it is entry-bound.
	var node apiv1.FoundryNode
	nodeKey := types.NamespacedName{Namespace: namespace, Name: nodeID}
	if err := s.K8sClient.Get(ctx, nodeKey, &node); err != nil {
		slog.Error("Failed to fetch FoundryNode", "node_id", nodeID, "error", err)
		return nil, status.Error(codes.NotFound, fmt.Sprintf("node %q not found: %v", nodeID, err))
	}
	if node.Spec.Entry == "" {
		return nil, status.Error(codes.FailedPrecondition, "ENTRY_NOT_BOUND: calling node is not entry-bound")
	}

	// Validate the entry contract exists on the flow.
	if _, ok := flow.Spec.EntryContracts[node.Spec.Entry]; !ok {
		return nil, status.Error(codes.FailedPrecondition,
			fmt.Sprintf("CONTRACT_VIOLATION: entry contract %q not found on flow in namespace %q", node.Spec.Entry, namespace))
	}

	// 4. Generate a unique Workitem name and create the CRD.
	workitemName := fmt.Sprintf("wi-%s", generateSuffix())
	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workitemName,
			Namespace: namespace,
			Labels: map[string]string{
				"flow.gideas.io/creator": nodeID,
			},
		},
	}

	if err := s.K8sClient.Create(ctx, workitem); err != nil {
		slog.Error("Failed to create Workitem", "name", workitemName, "error", err)
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to create workitem: %v", err))
	}

	// Set status fields (phase, assignee, metadata) via status subresource.
	workitem.Status.Phase = phasePending
	workitem.Status.CurrentAssignee = nodeID
	workitem.Status.Metadata = req.GetMetadata()
	if err := s.K8sClient.Status().Update(ctx, workitem); err != nil {
		slog.Error("Failed to set Workitem status", "name", workitemName, "error", err)
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to set workitem status: %v", err))
	}

	slog.Info("Workitem created",
		"workitem_id", workitemName,
		"namespace", namespace,
		"creator", nodeID,
		"entry_contract", node.Spec.Entry,
	)

	s.publishAudit(ctx, "audit.workitem.created", map[string]string{
		"action":      "created",
		"resource_id": workitemName,
		"namespace":   namespace,
	})

	return &flowv1.CreateWorkitemResponse{WorkitemId: workitemName}, nil
}

// CreateChildWorkitem creates a child Workitem linked to the caller's current Workitem.
//
// Flow:
//  1. Validate CREATE:workitem/child capability.
//  2. Extract workitem_id, namespace, and node_id from Sidecar-injected metadata.
//  3. Fetch the parent Workitem CRD to confirm it exists.
//  4. Generate a unique child Workitem name and create the CRD in Pending.
//  5. Set ParentWorkitemID and flow.gideas.io/parent label.
//  6. Return the child_workitem_id.
func (s *OperatorServer) CreateChildWorkitem(ctx context.Context, _ *flowv1.CreateChildWorkitemRequest) (*flowv1.CreateChildWorkitemResponse, error) {
	// 1. Capability gate: CREATE:workitem/child.
	if err := checkCapability(ctx, "CREATE:workitem/child"); err != nil {
		return nil, err
	}

	// 2. Extract identity from metadata.
	parentWorkitemID := extractWorkitemID(ctx)
	namespace := extractMetadataValue(ctx, metadataKeyNamespace)
	nodeID := extractMetadataValue(ctx, metadataKeyNodeID)

	if parentWorkitemID == "" {
		return nil, status.Error(codes.InvalidArgument, "x-flow-workitem-id metadata is required")
	}
	if namespace == "" {
		return nil, status.Error(codes.InvalidArgument, "x-flow-namespace metadata is required")
	}
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "x-flow-node-id metadata is required")
	}

	slog.Info("CreateChildWorkitem received",
		"parent_workitem_id", parentWorkitemID,
		"namespace", namespace,
		"node_id", nodeID,
	)

	// 3. Fetch the parent Workitem CRD.
	var parent apiv1.Workitem
	parentKey := types.NamespacedName{Namespace: namespace, Name: parentWorkitemID}
	if err := s.K8sClient.Get(ctx, parentKey, &parent); err != nil {
		slog.Error("Failed to fetch parent Workitem", "workitem_id", parentWorkitemID, "error", err)
		return nil, status.Error(codes.NotFound, fmt.Sprintf("parent workitem %q not found: %v", parentWorkitemID, err))
	}

	// 4. Create the child Workitem CRD.
	childName := fmt.Sprintf("child-%s-%s", parentWorkitemID, generateSuffix())
	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      childName,
			Namespace: namespace,
			Labels: map[string]string{
				"flow.gideas.io/creator": nodeID,
				"flow.gideas.io/parent":  parentWorkitemID,
			},
		},
	}

	if err := s.K8sClient.Create(ctx, child); err != nil {
		slog.Error("Failed to create child Workitem", "name", childName, "error", err)
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to create child workitem: %v", err))
	}

	// 5. Set status fields via status subresource.
	child.Status.Phase = phasePending
	child.Status.ParentWorkitemID = parentWorkitemID
	if err := s.K8sClient.Status().Update(ctx, child); err != nil {
		slog.Error("Failed to set child Workitem status", "name", childName, "error", err)
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to set child workitem status: %v", err))
	}

	slog.Info("Child Workitem created",
		"child_workitem_id", childName,
		"parent_workitem_id", parentWorkitemID,
		"namespace", namespace,
		"creator", nodeID,
	)

	s.publishAudit(ctx, "audit.workitem.child_created", map[string]string{
		"action":             "child_created",
		"resource_id":        childName,
		"parent_workitem_id": parentWorkitemID,
	})

	return &flowv1.CreateChildWorkitemResponse{ChildWorkitemId: childName}, nil
}

// RouteChild submits a routing instruction for a child Workitem.
//
// Flow:
//  1. Extract workitem_id (parent) and namespace from Sidecar-injected metadata.
//  2. Validate child_workitem_id is provided.
//  3. Fetch the child Workitem CRD.
//  4. Validate the child's ParentWorkitemID matches the caller's Workitem.
//  5. Validate the child is in Pending state (not yet routed).
//  6. Validate the routing instruction target exists.
//  7. Write the routing instruction and transition to Routing.
func (s *OperatorServer) RouteChild(ctx context.Context, req *flowv1.RouteChildRequest) (*flowv1.RouteChildResponse, error) {
	// 1. Extract parent identity from metadata.
	parentWorkitemID := extractWorkitemID(ctx)
	if parentWorkitemID == "" {
		return nil, status.Error(codes.InvalidArgument, "x-flow-workitem-id metadata is required")
	}

	namespace := extractMetadataValue(ctx, metadataKeyNamespace)

	// 2. Validate child_workitem_id.
	childID := req.GetChildWorkitemId()
	if childID == "" {
		return nil, status.Error(codes.InvalidArgument, "child_workitem_id is required")
	}

	slog.Info("RouteChild received",
		"parent_workitem_id", parentWorkitemID,
		"child_workitem_id", childID,
		"routing_type", routingTypeString(req.GetRoutingInstruction()),
		"target", routingTargetString(req.GetRoutingInstruction()),
	)

	// 3. Fetch the child Workitem CRD.
	var child apiv1.Workitem
	childKey := types.NamespacedName{Namespace: namespace, Name: childID}
	if err := s.K8sClient.Get(ctx, childKey, &child); err != nil {
		slog.Error("Failed to fetch child Workitem", "child_workitem_id", childID, "error", err)
		return nil, status.Error(codes.NotFound, fmt.Sprintf("child workitem %q not found: %v", childID, err))
	}

	// 4. Validate parent-child relationship.
	if child.Status.ParentWorkitemID != parentWorkitemID {
		return nil, status.Error(codes.PermissionDenied,
			fmt.Sprintf("CHILD_NOT_OWNED: child workitem %q is not owned by caller's workitem %q", childID, parentWorkitemID))
	}

	// 5. Validate child is in Pending state.
	if child.Status.Phase != phasePending {
		return nil, status.Error(codes.FailedPrecondition,
			fmt.Sprintf("CHILD_ALREADY_ROUTED: child workitem %q is in phase %q, must be Pending", childID, child.Status.Phase))
	}

	// 6. Validate routing instruction target exists.
	ri := req.GetRoutingInstruction()
	if ri == nil {
		return nil, status.Error(codes.InvalidArgument, "routing_instruction is required")
	}
	crdRI := convertRoutingInstruction(ri)

	if crdRI.Type == routeToType || crdRI.Type == routeToOutputType {
		target := crdRI.Target
		if target == "" {
			return nil, status.Error(codes.InvalidArgument, "routing instruction target is required")
		}
		// For route_to, validate the target node exists.
		if crdRI.Type == routeToType {
			var targetNode apiv1.FoundryNode
			targetKey := types.NamespacedName{Namespace: namespace, Name: target}
			if err := s.K8sClient.Get(ctx, targetKey, &targetNode); err != nil {
				return nil, status.Error(codes.FailedPrecondition,
					fmt.Sprintf("INVALID_ROUTE: target node %q not found: %v", target, err))
			}

			// NodeGroup routing isolation for child routing.
			sourceNodeID := extractMetadataValue(ctx, metadataKeyNodeID)
			if err := s.checkNodeGroupRouting(ctx, namespace, sourceNodeID, target); err != nil {
				return nil, err
			}
		}
	}

	// 7. Write the routing instruction and transition to Routing.
	child.Status.RoutingInstruction = crdRI
	child.Status.Phase = phaseRouting
	if err := s.K8sClient.Status().Update(ctx, &child); err != nil {
		slog.Error("Failed to update child Workitem status", "child_workitem_id", childID, "error", err)
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to update child workitem status: %v", err))
	}

	slog.Info("Child Workitem routed",
		"child_workitem_id", childID,
		"parent_workitem_id", parentWorkitemID,
		"routing_type", crdRI.Type,
		"routing_target", crdRI.Target,
	)

	s.publishAudit(ctx, "audit.workitem.child_routed", map[string]string{
		"action":             "child_routed",
		"resource_id":        childID,
		"parent_workitem_id": parentWorkitemID,
	})

	return &flowv1.RouteChildResponse{Accepted: true}, nil
}

// GetChildren returns the current state of all child Workitems for the caller's Workitem.
//
// Flow:
//  1. Extract workitem_id and namespace from Sidecar-injected metadata.
//  2. Query Workitems with flow.gideas.io/parent label matching the caller's Workitem.
//  3. Return ChildWorkitemStatus for each child.
func (s *OperatorServer) GetChildren(ctx context.Context, _ *flowv1.GetChildrenRequest) (*flowv1.GetChildrenResponse, error) {
	// 1. Extract identity from metadata.
	parentWorkitemID := extractWorkitemID(ctx)
	if parentWorkitemID == "" {
		return nil, status.Error(codes.InvalidArgument, "x-flow-workitem-id metadata is required")
	}

	namespace := extractMetadataValue(ctx, metadataKeyNamespace)

	slog.Info("GetChildren received", "parent_workitem_id", parentWorkitemID)

	// 2. Query children by parent label.
	var childList apiv1.WorkitemList
	if err := s.K8sClient.List(ctx, &childList,
		client.InNamespace(namespace),
		client.MatchingLabels{"flow.gideas.io/parent": parentWorkitemID},
	); err != nil {
		slog.Error("Failed to list child Workitems", "parent_workitem_id", parentWorkitemID, "error", err)
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to list child workitems: %v", err))
	}

	// 3. Build response.
	children := make([]*flowv1.ChildWorkitemStatus, len(childList.Items))
	for i := range childList.Items {
		c := &childList.Items[i]
		children[i] = &flowv1.ChildWorkitemStatus{
			WorkitemId:       c.Name,
			Phase:            c.Status.Phase,
			CurrentAssignee:  c.Status.CurrentAssignee,
			CompletionReason: completionReasonFromString(c.Status.CompletionReason),
			// Artefact references are populated via Archivist cross-Workitem
			// reads (Phase 7). For now, return an empty list.
		}
	}

	slog.Info("GetChildren response built",
		"parent_workitem_id", parentWorkitemID,
		"child_count", len(children),
	)

	return &flowv1.GetChildrenResponse{Children: children}, nil
}

// ValidateChildAccess validates a parent-child Workitem relationship and the
// child's completion state. This is a service-facing RPC called by the
// Archivist (not by nodes through the Sidecar) to authorise cross-Workitem
// artefact reads.
//
// Returns valid=true only when:
//  1. The child Workitem exists.
//  2. The child's ParentWorkitemID matches the given parent_workitem_id.
//  3. The child is in Completed state.
//
// No capability gate — this is a service-to-service call.
func (s *OperatorServer) ValidateChildAccess(ctx context.Context, req *flowv1.ValidateChildAccessRequest) (*flowv1.ValidateChildAccessResponse, error) {
	parentWorkitemID := req.GetParentWorkitemId()
	childWorkitemID := req.GetChildWorkitemId()

	if parentWorkitemID == "" {
		return nil, status.Error(codes.InvalidArgument, "parent_workitem_id is required")
	}
	if childWorkitemID == "" {
		return nil, status.Error(codes.InvalidArgument, "child_workitem_id is required")
	}

	namespace := extractMetadataValue(ctx, metadataKeyNamespace)

	slog.Info("ValidateChildAccess received",
		"parent_workitem_id", parentWorkitemID,
		"child_workitem_id", childWorkitemID,
	)

	// Fetch the child Workitem CRD.
	var child apiv1.Workitem
	childKey := types.NamespacedName{Namespace: namespace, Name: childWorkitemID}
	if err := s.K8sClient.Get(ctx, childKey, &child); err != nil {
		slog.Error("ValidateChildAccess: child not found",
			"child_workitem_id", childWorkitemID,
			"error", err,
		)
		return nil, status.Error(codes.NotFound,
			fmt.Sprintf("child workitem %q not found: %v", childWorkitemID, err))
	}

	// Check parent-child relationship.
	if child.Status.ParentWorkitemID != parentWorkitemID {
		slog.Info("ValidateChildAccess: parent mismatch",
			"child_workitem_id", childWorkitemID,
			"expected_parent", parentWorkitemID,
			"actual_parent", child.Status.ParentWorkitemID,
		)
		return &flowv1.ValidateChildAccessResponse{
			Valid: false,
			Phase: child.Status.Phase,
		}, nil
	}

	// Check child is Completed.
	if child.Status.Phase != phaseCompleted {
		slog.Info("ValidateChildAccess: child not completed",
			"child_workitem_id", childWorkitemID,
			"phase", child.Status.Phase,
		)
		return &flowv1.ValidateChildAccessResponse{
			Valid: false,
			Phase: child.Status.Phase,
		}, nil
	}

	slog.Info("ValidateChildAccess: access granted",
		"parent_workitem_id", parentWorkitemID,
		"child_workitem_id", childWorkitemID,
	)

	return &flowv1.ValidateChildAccessResponse{
		Valid: true,
		Phase: child.Status.Phase,
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// extractWorkitemID reads the x-flow-workitem-id from incoming gRPC metadata.
func extractWorkitemID(ctx context.Context) string {
	return extractMetadataValue(ctx, metadataKeyWorkitemID)
}

// extractMetadataValue reads a single value from incoming gRPC metadata.
func extractMetadataValue(ctx context.Context, key string) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(key)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// crdNodeToProto converts a FoundryNode CRD to a proto FlowNode.
func crdNodeToProto(n *apiv1.FoundryNode) *flowv1.FlowNode {
	outputs := make([]*flowv1.FlowOutput, len(n.Spec.Outputs))
	for i, o := range n.Spec.Outputs {
		outputs[i] = &flowv1.FlowOutput{
			Name:   o.Name,
			Target: o.Target,
		}
	}
	return &flowv1.FlowNode{
		Name:         n.Name,
		Capabilities: n.Spec.Capabilities,
		Outputs:      outputs,
	}
}

// convertSubmitAction maps the proto oneof action to the CRD RoutingInstruction type.
func convertSubmitAction(req *flowv1.SubmitResultRequest) *apiv1.RoutingInstruction {
	switch a := req.GetAction().(type) {
	case *flowv1.SubmitResultRequest_Complete:
		ri := &apiv1.RoutingInstruction{Type: completeType}
		if a.Complete != nil && a.Complete.GetReason() != flowv1.CompletionReason_COMPLETION_REASON_UNSPECIFIED {
			ri.CompletionReason = a.Complete.GetReason().String()
		}
		return ri
	case *flowv1.SubmitResultRequest_Route:
		if a.Route != nil && a.Route.GetOutput() {
			return &apiv1.RoutingInstruction{
				Type:   "route_to_output",
				Target: a.Route.GetTarget(),
			}
		}
		return &apiv1.RoutingInstruction{
			Type:   "route_to",
			Target: a.Route.GetTarget(),
		}
	case *flowv1.SubmitResultRequest_Suspend:
		ri := &apiv1.RoutingInstruction{Type: suspendType}
		if a.Suspend != nil {
			ri.SuspendCondition = a.Suspend.GetCondition()
			if a.Suspend.GetTimeout() != nil {
				ri.SuspendTimeout = a.Suspend.GetTimeout().AsDuration().String()
			}
		}
		return ri
	default:
		return nil
	}
}

// convertRoutingInstruction maps the proto RoutingInstruction to the CRD type.
// Used by RouteChild which still uses the legacy RoutingInstruction message.
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

// submitActionString returns a loggable string for the submit action.
func submitActionString(req *flowv1.SubmitResultRequest) string {
	switch a := req.GetAction().(type) {
	case *flowv1.SubmitResultRequest_Complete:
		if a.Complete != nil && a.Complete.GetReason() != flowv1.CompletionReason_COMPLETION_REASON_UNSPECIFIED {
			return "complete(" + a.Complete.GetReason().String() + ")"
		}
		return "complete"
	case *flowv1.SubmitResultRequest_Route:
		if a.Route != nil && a.Route.GetOutput() {
			return "route_to_output(" + a.Route.GetTarget() + ")"
		}
		return "route_to(" + a.Route.GetTarget() + ")"
	case *flowv1.SubmitResultRequest_Suspend:
		return "suspend"
	default:
		return nilString
	}
}

// routingTypeString returns a loggable string for the routing type.
// Used by RouteChild which still uses the legacy RoutingInstruction message.
func routingTypeString(ri *flowv1.RoutingInstruction) string {
	if ri == nil {
		return nilString
	}
	return ri.GetType().String()
}

// routingTargetString returns a loggable string for the routing target.
func routingTargetString(ri *flowv1.RoutingInstruction) string {
	if ri == nil {
		return nilString
	}
	return ri.GetTarget()
}

// completionReasonFromString converts a CRD string to the proto CompletionReason enum.
func completionReasonFromString(s string) flowv1.CompletionReason {
	if v, ok := flowv1.CompletionReason_value[s]; ok {
		return flowv1.CompletionReason(v)
	}
	return flowv1.CompletionReason_COMPLETION_REASON_UNSPECIFIED
}

// ResumeWorkitem explicitly resumes a suspended Workitem. The Operator validates
// the Workitem is in Suspended phase and transitions it to Pending for
// re-dispatch to the same node type that suspended it.
func (s *OperatorServer) ResumeWorkitem(ctx context.Context, req *flowv1.ResumeWorkitemRequest) (*flowv1.ResumeWorkitemResponse, error) {
	workitemID := req.GetWorkitemId()
	if workitemID == "" {
		return nil, status.Error(codes.InvalidArgument, "workitem_id is required")
	}

	namespace := extractMetadataValue(ctx, metadataKeyNamespace)

	slog.Info("ResumeWorkitem received",
		"workitem_id", workitemID,
		"namespace", namespace,
	)

	// Fetch the Workitem CRD.
	var workitem apiv1.Workitem
	key := types.NamespacedName{Namespace: namespace, Name: workitemID}
	if err := s.K8sClient.Get(ctx, key, &workitem); err != nil {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("workitem %q not found: %v", workitemID, err))
	}

	// Validate the Workitem is in Suspended phase.
	if workitem.Status.Phase != "Suspended" {
		return nil, status.Error(codes.FailedPrecondition,
			fmt.Sprintf("workitem %q is in phase %q, must be Suspended", workitemID, workitem.Status.Phase))
	}

	// Transition to Pending for re-dispatch. The CurrentAssignee is preserved
	// so the reconciler dispatches to the same node type.
	workitem.Status.Phase = phasePending
	// Clear suspend-related fields.
	workitem.Status.ResumeCondition = ""
	workitem.Status.SuspendedAt = nil
	workitem.Status.ResumeTimeout = ""
	workitem.Status.RoutingInstruction = nil

	if err := s.K8sClient.Status().Update(ctx, &workitem); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to update workitem status: %v", err))
	}

	slog.Info("Workitem resumed",
		"workitem_id", workitemID,
	)

	s.publishAudit(ctx, "audit.workitem.resumed", map[string]string{
		"action":      "resumed",
		"resource_id": workitemID,
	})

	return &flowv1.ResumeWorkitemResponse{Accepted: true}, nil
}

// ListSuspendedWorkitems returns workitems in the Suspended phase whose
// ResumeCondition contains the given filter string. Used by watcher nodes
// to discover workitems held on a specific condition.
func (s *OperatorServer) ListSuspendedWorkitems(ctx context.Context, req *flowv1.ListSuspendedWorkitemsRequest) (*flowv1.ListSuspendedWorkitemsResponse, error) {
	conditionFilter := req.GetConditionContains()
	if conditionFilter == "" {
		return nil, status.Error(codes.InvalidArgument, "condition_contains is required")
	}

	namespace := extractMetadataValue(ctx, metadataKeyNamespace)

	slog.Info("ListSuspendedWorkitems received",
		"condition_contains", conditionFilter,
		"namespace", namespace,
	)

	// List all Workitems in the namespace.
	var workitemList apiv1.WorkitemList
	listOpts := []client.ListOption{
		client.InNamespace(namespace),
	}
	if err := s.K8sClient.List(ctx, &workitemList, listOpts...); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to list workitems: %v", err))
	}

	// Filter to Suspended phase with matching condition.
	var results []*flowv1.SuspendedWorkitemInfo
	for i := range workitemList.Items {
		wi := &workitemList.Items[i]
		if wi.Status.Phase != "Suspended" {
			continue
		}
		if !strings.Contains(wi.Status.ResumeCondition, conditionFilter) {
			continue
		}
		results = append(results, &flowv1.SuspendedWorkitemInfo{
			WorkitemId:      wi.Name,
			ResumeCondition: wi.Status.ResumeCondition,
		})
	}

	slog.Info("ListSuspendedWorkitems completed",
		"condition_contains", conditionFilter,
		"count", len(results),
	)

	return &flowv1.ListSuspendedWorkitemsResponse{
		Workitems: results,
	}, nil
}

// generateSuffix returns a short unique suffix for Workitem names.
// Uses the current nanosecond timestamp for uniqueness in the walking skeleton.
// A production implementation would use crypto/rand or a UUID library.
func generateSuffix() string {
	return fmt.Sprintf("%d", timeNow().UnixNano()%1_000_000_000)
}

// timeNow is a function variable for generating timestamps.
// Tests can override this to produce deterministic names.
var timeNow = func() metav1.Time { return metav1.Now() }

// checkNodeGroupRouting enforces NodeGroup routing isolation at runtime.
//
// When a route_to instruction targets a node inside a NodeGroup, the source
// node must either be in the same group OR the target must be an entry-bound
// node within that group. This prevents external routing to internal group
// nodes.
//
// sourceNodeID may be empty for non-node-originated routing (system calls),
// which bypass group checks.
func (s *OperatorServer) checkNodeGroupRouting(ctx context.Context, namespace, sourceNodeID, targetNodeID string) error {
	if sourceNodeID == "" || targetNodeID == "" {
		return nil // System calls or no target bypass group checks.
	}

	// Fetch the FoundryFlow to get NodeGroup definitions.
	flow, err := s.resolveFlow(ctx, namespace)
	if err != nil {
		return nil // No flow, no group constraints.
	}

	if len(flow.Spec.NodeGroups) == 0 {
		return nil // No groups defined.
	}

	// Build node-to-group lookup.
	nodeToGroup := make(map[string]string)
	for groupName, group := range flow.Spec.NodeGroups {
		for _, nodeName := range group.Nodes {
			nodeToGroup[nodeName] = groupName
		}
	}

	targetGroup, targetInGroup := nodeToGroup[targetNodeID]
	if !targetInGroup {
		return nil // Target is not in any group; no restriction.
	}

	sourceGroup := nodeToGroup[sourceNodeID]
	if sourceGroup == targetGroup {
		return nil // Same group; routing is allowed.
	}

	// Target is in a group but source is not (or is in a different group).
	// Only allow routing to entry-bound nodes within the target group.
	var targetNode apiv1.FoundryNode
	targetKey := types.NamespacedName{Namespace: namespace, Name: targetNodeID}
	if err := s.K8sClient.Get(ctx, targetKey, &targetNode); err != nil {
		return status.Error(codes.Internal, fmt.Sprintf("failed to fetch target node %q for group validation: %v", targetNodeID, err))
	}

	if targetNode.Spec.Entry == "" {
		return status.Error(codes.FailedPrecondition,
			fmt.Sprintf("GROUP_ROUTING_DENIED: routing from node %q to non-entry-bound node %q in group %q is not allowed",
				sourceNodeID, targetNodeID, targetGroup))
	}

	return nil
}

// validateSuspendTimeout validates and resolves the suspend timeout on a
// routing instruction against the flow's SuspensionConfig. It:
//   - Rejects invalid duration strings.
//   - Rejects timeouts exceeding maxSuspendTimeout.
//   - Applies defaultSuspendTimeout (falling back to maxSuspendTimeout) when
//     no explicit timeout is specified.
//
// The routing instruction is mutated in place with the resolved timeout.
func (s *OperatorServer) validateSuspendTimeout(ctx context.Context, namespace string, ri *apiv1.RoutingInstruction) error {
	flow, err := s.resolveFlow(ctx, namespace)
	if err != nil {
		// If flow lookup fails, allow the suspend through without timeout
		// validation. The scheduler will also validate.
		slog.Warn("Could not resolve flow for suspend timeout validation",
			"namespace", namespace,
			"error", err,
		)
		return nil
	}

	if flow.Spec.Suspension == nil {
		// No suspension config — no validation or defaults to apply.
		return nil
	}
	cfg := flow.Spec.Suspension

	if ri.SuspendTimeout == "" {
		// No explicit timeout — apply default.
		if cfg.DefaultSuspendTimeout != nil {
			ri.SuspendTimeout = cfg.DefaultSuspendTimeout.Duration.String()
		} else if cfg.MaxSuspendTimeout != nil {
			ri.SuspendTimeout = cfg.MaxSuspendTimeout.Duration.String()
		}
		return nil
	}

	// Explicit timeout — validate format and cap.
	requested, parseErr := time.ParseDuration(ri.SuspendTimeout)
	if parseErr != nil {
		return status.Error(codes.InvalidArgument,
			fmt.Sprintf("INVALID_SUSPEND: invalid suspend timeout %q: %v", ri.SuspendTimeout, parseErr))
	}

	if cfg.MaxSuspendTimeout != nil && requested > cfg.MaxSuspendTimeout.Duration {
		return status.Error(codes.InvalidArgument,
			fmt.Sprintf("SUSPEND_TIMEOUT_EXCEEDED: requested suspend timeout %s exceeds maxSuspendTimeout %s",
				ri.SuspendTimeout, cfg.MaxSuspendTimeout.Duration))
	}

	return nil
}

// checkChildrenTerminal queries for child Workitems and returns an error if
// any are in a non-terminal phase (Pending, Running, or Routing).
// This enforces the invariant that a parent cannot complete while children
// are still active.
func (s *OperatorServer) checkChildrenTerminal(ctx context.Context, namespace, parentWorkitemID string) error {
	var childList apiv1.WorkitemList
	if err := s.K8sClient.List(ctx, &childList,
		client.InNamespace(namespace),
		client.MatchingLabels{"flow.gideas.io/parent": parentWorkitemID},
	); err != nil {
		return status.Error(codes.Internal, fmt.Sprintf("failed to query child workitems: %v", err))
	}

	for i := range childList.Items {
		phase := childList.Items[i].Status.Phase
		if phase != phaseCompleted && phase != "Failed" {
			return status.Error(codes.FailedPrecondition,
				fmt.Sprintf("CHILDREN_NOT_TERMINAL: child workitem %q is in phase %q; parent cannot complete", childList.Items[i].Name, phase))
		}
	}

	return nil
}
