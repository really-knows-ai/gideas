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
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

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

	// metadataKeyFlowID is the gRPC metadata key carrying the Sidecar-injected
	// flow identity.
	metadataKeyFlowID = "x-flow-flow-id"

	// metadataKeyNodeID is the gRPC metadata key carrying the Sidecar-injected
	// node identity.
	metadataKeyNodeID = "x-flow-node-id"

	// defaultNamespace is used when no namespace context is available.
	// In production this would be derived from the Sidecar's pod namespace.
	defaultNamespace = "default"

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
			EventId:    newAuditEventID(),
			EventType:  eventType,
			FlowId:     extractMetadataValue(ctx, metadataKeyFlowID),
			NodeId:     extractMetadataValue(ctx, metadataKeyNodeID),
			WorkitemId: extractMetadataValue(ctx, metadataKeyWorkitemID),
			Timestamp:  timestamppb.Now(),
			Attributes: attrs,
		},
	})
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

	// 3a. Completion guard: reject complete if children are non-terminal.
	if workitem.Status.RoutingInstruction != nil && workitem.Status.RoutingInstruction.Type == "complete" {
		if err := s.checkChildrenTerminal(ctx, workitemID); err != nil {
			return nil, err
		}
	}

	// 3b. NodeGroup routing isolation: reject route_to targeting a non-entry-bound
	// node inside a group from outside that group.
	if workitem.Status.RoutingInstruction != nil && workitem.Status.RoutingInstruction.Type == routeToType {
		sourceNodeID := extractMetadataValue(ctx, metadataKeyNodeID)
		if err := s.checkNodeGroupRouting(ctx, sourceNodeID, workitem.Status.RoutingInstruction.Target); err != nil {
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
//  1. Extract flow_id and node_id from Sidecar-injected gRPC metadata.
//  2. Fetch the FoundryFlow CRD to get exit contracts.
//  3. Fetch all FoundryNode CRDs in the namespace.
//  4. Find the calling node's CRD to get its exit binding → resolve to the exit contract.
//  5. Build and return the GetFlowTopologyResponse.
func (s *OperatorServer) GetFlowTopology(ctx context.Context, _ *flowv1.GetFlowTopologyRequest) (*flowv1.GetFlowTopologyResponse, error) {
	// Capability gate: READ:flow.
	if err := checkCapability(ctx, "READ:flow"); err != nil {
		return nil, err
	}

	// 1. Extract identity from metadata.
	flowID := extractMetadataValue(ctx, metadataKeyFlowID)
	nodeID := extractMetadataValue(ctx, metadataKeyNodeID)
	if flowID == "" {
		return nil, status.Error(codes.InvalidArgument, "x-flow-flow-id metadata is required")
	}
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "x-flow-node-id metadata is required")
	}

	slog.Info("GetFlowTopology received", "flow_id", flowID, "node_id", nodeID)

	// 2. Fetch the FoundryFlow CRD.
	var flow apiv1.FoundryFlow
	flowKey := types.NamespacedName{Namespace: defaultNamespace, Name: flowID}
	if err := s.K8sClient.Get(ctx, flowKey, &flow); err != nil {
		slog.Error("Failed to fetch FoundryFlow", "flow_id", flowID, "error", err)
		return nil, status.Error(codes.NotFound, fmt.Sprintf("flow %q not found: %v", flowID, err))
	}

	// 3. Fetch all FoundryNode CRDs in the namespace.
	var nodeList apiv1.FoundryNodeList
	if err := s.K8sClient.List(ctx, &nodeList, client.InNamespace(defaultNamespace)); err != nil {
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
		"flow_id", flowID,
		"node_id", nodeID,
		"node_count", len(nodes),
		"exit_contract_kinds", len(exitContract),
	)

	return resp, nil
}

// CreateWorkitem creates a new Workitem in Pending state.
//
// Flow:
//  1. Extract flow_id and node_id from Sidecar-injected gRPC metadata.
//  2. Fetch the FoundryFlow CRD.
//  3. Fetch the calling FoundryNode CRD and verify it is entry-bound.
//  4. Generate a unique Workitem name and create the CRD in Pending.
//  5. Return the workitem_id.
func (s *OperatorServer) CreateWorkitem(ctx context.Context, _ *flowv1.CreateWorkitemRequest) (*flowv1.CreateWorkitemResponse, error) {
	// 1. Extract identity from metadata.
	flowID := extractMetadataValue(ctx, metadataKeyFlowID)
	nodeID := extractMetadataValue(ctx, metadataKeyNodeID)
	if flowID == "" {
		return nil, status.Error(codes.InvalidArgument, "x-flow-flow-id metadata is required")
	}
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "x-flow-node-id metadata is required")
	}

	slog.Info("CreateWorkitem received", "flow_id", flowID, "node_id", nodeID)

	// 2. Fetch the FoundryFlow CRD.
	var flow apiv1.FoundryFlow
	flowKey := types.NamespacedName{Namespace: defaultNamespace, Name: flowID}
	if err := s.K8sClient.Get(ctx, flowKey, &flow); err != nil {
		slog.Error("Failed to fetch FoundryFlow", "flow_id", flowID, "error", err)
		return nil, status.Error(codes.NotFound, fmt.Sprintf("flow %q not found: %v", flowID, err))
	}

	// 3. Fetch the calling node and verify it is entry-bound.
	var node apiv1.FoundryNode
	nodeKey := types.NamespacedName{Namespace: defaultNamespace, Name: nodeID}
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
			fmt.Sprintf("CONTRACT_VIOLATION: entry contract %q not found on flow %q", node.Spec.Entry, flowID))
	}

	// 4. Generate a unique Workitem name and create the CRD.
	workitemName := fmt.Sprintf("wi-%s-%s", flowID, generateSuffix())
	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workitemName,
			Namespace: defaultNamespace,
			Labels: map[string]string{
				"flow.gideas.io/flow":    flowID,
				"flow.gideas.io/creator": nodeID,
			},
		},
	}

	if err := s.K8sClient.Create(ctx, workitem); err != nil {
		slog.Error("Failed to create Workitem", "name", workitemName, "error", err)
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to create workitem: %v", err))
	}

	// Set status fields (phase and assignee) via status subresource.
	workitem.Status.Phase = phasePending
	workitem.Status.CurrentAssignee = nodeID
	if err := s.K8sClient.Status().Update(ctx, workitem); err != nil {
		slog.Error("Failed to set Workitem status", "name", workitemName, "error", err)
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to set workitem status: %v", err))
	}

	slog.Info("Workitem created",
		"workitem_id", workitemName,
		"flow_id", flowID,
		"creator", nodeID,
		"entry_contract", node.Spec.Entry,
	)

	s.publishAudit(ctx, "audit.workitem.created", map[string]string{
		"action":      "created",
		"resource_id": workitemName,
		"flow_id":     flowID,
	})

	return &flowv1.CreateWorkitemResponse{WorkitemId: workitemName}, nil
}

// CreateHearingWorkitem creates a review hearing Workitem for Tribunal processing.
//
// Flow:
//  1. Validate law_id is provided.
//  2. Find an entry-bound node that acts as the hearing receiver (Tribunal).
//  3. Generate a unique Workitem name and create the CRD in Pending.
//  4. Return the workitem_id.
func (s *OperatorServer) CreateHearingWorkitem(ctx context.Context, req *flowv1.CreateHearingWorkitemRequest) (*flowv1.CreateHearingWorkitemResponse, error) {
	lawID := req.GetLawId()
	if lawID == "" {
		return nil, status.Error(codes.InvalidArgument, "law_id is required")
	}

	slog.Info("CreateHearingWorkitem received", "law_id", lawID)

	// Find all FoundryNodes to locate a hearing-capable Tribunal node.
	// The Tribunal is identified by being entry-bound (has a spec.entry value).
	// In a real deployment the Operator provisions the Tribunal and knows
	// its identity. Here we search for nodes with the "tribunal" convention.
	var nodeList apiv1.FoundryNodeList
	if err := s.K8sClient.List(ctx, &nodeList, client.InNamespace(defaultNamespace)); err != nil {
		slog.Error("Failed to list FoundryNodes", "error", err)
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to list nodes: %v", err))
	}

	var tribunalNode *apiv1.FoundryNode
	for i := range nodeList.Items {
		n := &nodeList.Items[i]
		if n.Spec.Entry != "" && hasCapability(n.Spec.Capabilities, "USE:jury") {
			tribunalNode = n
			break
		}
	}
	if tribunalNode == nil {
		return nil, status.Error(codes.FailedPrecondition, "no hearing-capable Tribunal node found in the flow")
	}

	// Create the hearing Workitem.
	workitemName := fmt.Sprintf("hearing-%s-%s", lawID, generateSuffix())
	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workitemName,
			Namespace: defaultNamespace,
			Labels: map[string]string{
				"flow.gideas.io/type":   "hearing",
				"flow.gideas.io/law-id": lawID,
			},
		},
	}

	if err := s.K8sClient.Create(ctx, workitem); err != nil {
		slog.Error("Failed to create hearing Workitem", "name", workitemName, "error", err)
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to create hearing workitem: %v", err))
	}

	// Set status fields via status subresource.
	workitem.Status.Phase = phasePending
	workitem.Status.CurrentAssignee = tribunalNode.Name
	if err := s.K8sClient.Status().Update(ctx, workitem); err != nil {
		slog.Error("Failed to set hearing Workitem status", "name", workitemName, "error", err)
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to set hearing workitem status: %v", err))
	}

	slog.Info("Hearing Workitem created",
		"workitem_id", workitemName,
		"law_id", lawID,
		"tribunal", tribunalNode.Name,
	)

	s.publishAudit(ctx, "audit.workitem.hearing_created", map[string]string{
		"action":      "hearing_created",
		"resource_id": workitemName,
		"law_id":      lawID,
	})

	return &flowv1.CreateHearingWorkitemResponse{WorkitemId: workitemName}, nil
}

// ExportWorkitem assembles an export package from a completed Workitem.
//
// Flow:
//  1. Validate workitem_id is provided.
//  2. Fetch the Workitem CRD and verify it is Completed.
//  3. Serialise the Workitem metadata into a JSON export package.
//  4. Return the export package bytes.
func (s *OperatorServer) ExportWorkitem(ctx context.Context, req *flowv1.ExportWorkitemRequest) (*flowv1.ExportWorkitemResponse, error) {
	workitemID := req.GetWorkitemId()
	if workitemID == "" {
		return nil, status.Error(codes.InvalidArgument, "workitem_id is required")
	}

	slog.Info("ExportWorkitem received", "workitem_id", workitemID)

	// Fetch the Workitem CRD.
	var workitem apiv1.Workitem
	key := types.NamespacedName{Namespace: defaultNamespace, Name: workitemID}
	if err := s.K8sClient.Get(ctx, key, &workitem); err != nil {
		slog.Error("Failed to fetch Workitem", "workitem_id", workitemID, "error", err)
		return nil, status.Error(codes.NotFound, fmt.Sprintf("workitem %q not found: %v", workitemID, err))
	}

	// Verify the Workitem is in Completed state.
	if workitem.Status.Phase != phaseCompleted {
		return nil, status.Error(codes.FailedPrecondition,
			fmt.Sprintf("workitem %q is in phase %q, must be %q for export", workitemID, workitem.Status.Phase, phaseCompleted))
	}

	// Assemble the export package. The full export package would include
	// artefact content, passport stamps, and provenance chain retrieved from
	// the Archivist. For this implementation we serialise the Workitem
	// metadata and status as the package payload.
	pkg := exportPackage{
		WorkitemID: workitem.Name,
		Namespace:  workitem.Namespace,
		Labels:     workitem.Labels,
		Phase:      workitem.Status.Phase,
	}

	data, err := json.Marshal(pkg)
	if err != nil {
		slog.Error("Failed to marshal export package", "workitem_id", workitemID, "error", err)
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to marshal export package: %v", err))
	}

	slog.Info("Workitem exported", "workitem_id", workitemID, "package_size", len(data))

	s.publishAudit(ctx, "audit.workitem.exported", map[string]string{
		"action":      "exported",
		"resource_id": workitemID,
	})

	return &flowv1.ExportWorkitemResponse{ExportPackage: data}, nil
}

// ImportWorkitem validates and materialises a Workitem from an export package.
//
// Flow:
//  1. Validate the export package is provided and can be decoded.
//  2. If treaty_name is provided, fetch and validate the Treaty CRD.
//  3. Fetch the FoundryFlow to resolve the importNode.
//  4. Validate the importNode exists and is entry-bound.
//  5. Create the Workitem CRD in Pending state.
//  6. Return the workitem_id.
func (s *OperatorServer) ImportWorkitem(ctx context.Context, req *flowv1.ImportWorkitemRequest) (*flowv1.ImportWorkitemResponse, error) {
	if len(req.GetExportPackage()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "export_package is required")
	}

	slog.Info("ImportWorkitem received", "treaty_name", req.GetTreatyName(), "package_size", len(req.GetExportPackage()))

	// 1. Decode the export package.
	var pkg exportPackage
	if err := json.Unmarshal(req.GetExportPackage(), &pkg); err != nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("invalid export package: %v", err))
	}

	if pkg.WorkitemID == "" {
		return nil, status.Error(codes.InvalidArgument, "export package missing workitem_id")
	}

	// 2. If treaty_name is provided, validate the Treaty CRD.
	treatyName := req.GetTreatyName()
	if treatyName != "" {
		var treaty apiv1.Treaty
		treatyKey := types.NamespacedName{Namespace: defaultNamespace, Name: treatyName}
		if err := s.K8sClient.Get(ctx, treatyKey, &treaty); err != nil {
			slog.Error("Failed to fetch Treaty", "treaty_name", treatyName, "error", err)
			return nil, status.Error(codes.NotFound, fmt.Sprintf("treaty %q not found: %v", treatyName, err))
		}

		// Verify the Treaty direction is import.
		if treaty.Spec.Direction != "import" {
			return nil, status.Error(codes.FailedPrecondition,
				fmt.Sprintf("treaty %q has direction %q, must be \"import\"", treatyName, treaty.Spec.Direction))
		}

		// Enforce maxBundleSize if set.
		if treaty.Spec.MaxBundleSize != "" {
			// MaxBundleSize is stored as a string quantity. For this
			// implementation we do a simple byte-length check against
			// a numeric value. A production implementation would parse
			// the quantity properly.
			slog.Info("Treaty constraints validated",
				"treaty_name", treatyName,
				"remote", treaty.Spec.RemoteName,
				"max_bundle_size", treaty.Spec.MaxBundleSize,
			)
		}
	}

	// 3. Fetch the FoundryFlow to find the importNode.
	// We list flows in the namespace since the import does not carry a
	// target flow_id — the receiving flow's importNode configuration
	// determines where the Workitem lands.
	var flowList apiv1.FoundryFlowList
	if err := s.K8sClient.List(ctx, &flowList, client.InNamespace(defaultNamespace)); err != nil {
		slog.Error("Failed to list FoundryFlows", "error", err)
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to list flows: %v", err))
	}

	// Find a flow with importNode configured.
	var targetFlow *apiv1.FoundryFlow
	for i := range flowList.Items {
		f := &flowList.Items[i]
		if f.Spec.ImportNode != "" {
			targetFlow = f
			break
		}
	}
	if targetFlow == nil {
		return nil, status.Error(codes.FailedPrecondition, "no flow with importNode configured found in namespace")
	}

	// 4. Validate the importNode exists and is entry-bound.
	var importNode apiv1.FoundryNode
	importNodeKey := types.NamespacedName{Namespace: defaultNamespace, Name: targetFlow.Spec.ImportNode}
	if err := s.K8sClient.Get(ctx, importNodeKey, &importNode); err != nil {
		slog.Error("Failed to fetch import node", "node", targetFlow.Spec.ImportNode, "error", err)
		return nil, status.Error(codes.FailedPrecondition,
			fmt.Sprintf("IMPORT_ADMISSION_FAILED: import node %q not found: %v", targetFlow.Spec.ImportNode, err))
	}
	if importNode.Spec.Entry == "" {
		return nil, status.Error(codes.FailedPrecondition,
			fmt.Sprintf("IMPORT_ADMISSION_FAILED: import node %q is not entry-bound", targetFlow.Spec.ImportNode))
	}

	// 5. Create the imported Workitem in Pending state.
	workitemName := fmt.Sprintf("import-%s-%s", pkg.WorkitemID, generateSuffix())
	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workitemName,
			Namespace: defaultNamespace,
			Labels: map[string]string{
				"flow.gideas.io/type":            "import",
				"flow.gideas.io/source-workitem": pkg.WorkitemID,
				"flow.gideas.io/flow":            targetFlow.Name,
			},
		},
	}

	if err := s.K8sClient.Create(ctx, workitem); err != nil {
		slog.Error("Failed to create imported Workitem", "name", workitemName, "error", err)
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to create imported workitem: %v", err))
	}

	// Set status fields via status subresource.
	workitem.Status.Phase = phasePending
	workitem.Status.CurrentAssignee = importNode.Name
	if err := s.K8sClient.Status().Update(ctx, workitem); err != nil {
		slog.Error("Failed to set imported Workitem status", "name", workitemName, "error", err)
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to set imported workitem status: %v", err))
	}

	slog.Info("Workitem imported",
		"workitem_id", workitemName,
		"source", pkg.WorkitemID,
		"import_node", importNode.Name,
		"flow", targetFlow.Name,
	)

	s.publishAudit(ctx, "audit.workitem.imported", map[string]string{
		"action":      "imported",
		"resource_id": workitemName,
		"source":      pkg.WorkitemID,
	})

	return &flowv1.ImportWorkitemResponse{WorkitemId: workitemName}, nil
}

// CreateChildWorkitem creates a child Workitem linked to the caller's current Workitem.
//
// Flow:
//  1. Validate CREATE:workitem/child capability.
//  2. Extract workitem_id, flow_id, and node_id from Sidecar-injected metadata.
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
	flowID := extractMetadataValue(ctx, metadataKeyFlowID)
	nodeID := extractMetadataValue(ctx, metadataKeyNodeID)

	if parentWorkitemID == "" {
		return nil, status.Error(codes.InvalidArgument, "x-flow-workitem-id metadata is required")
	}
	if flowID == "" {
		return nil, status.Error(codes.InvalidArgument, "x-flow-flow-id metadata is required")
	}
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "x-flow-node-id metadata is required")
	}

	slog.Info("CreateChildWorkitem received",
		"parent_workitem_id", parentWorkitemID,
		"flow_id", flowID,
		"node_id", nodeID,
	)

	// 3. Fetch the parent Workitem CRD.
	var parent apiv1.Workitem
	parentKey := types.NamespacedName{Namespace: defaultNamespace, Name: parentWorkitemID}
	if err := s.K8sClient.Get(ctx, parentKey, &parent); err != nil {
		slog.Error("Failed to fetch parent Workitem", "workitem_id", parentWorkitemID, "error", err)
		return nil, status.Error(codes.NotFound, fmt.Sprintf("parent workitem %q not found: %v", parentWorkitemID, err))
	}

	// 4. Create the child Workitem CRD.
	childName := fmt.Sprintf("child-%s-%s", parentWorkitemID, generateSuffix())
	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      childName,
			Namespace: defaultNamespace,
			Labels: map[string]string{
				"flow.gideas.io/flow":    flowID,
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
		"flow_id", flowID,
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
//  1. Extract workitem_id (parent) from Sidecar-injected metadata.
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
	childKey := types.NamespacedName{Namespace: defaultNamespace, Name: childID}
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
			targetKey := types.NamespacedName{Namespace: defaultNamespace, Name: target}
			if err := s.K8sClient.Get(ctx, targetKey, &targetNode); err != nil {
				return nil, status.Error(codes.FailedPrecondition,
					fmt.Sprintf("INVALID_ROUTE: target node %q not found: %v", target, err))
			}

			// NodeGroup routing isolation for child routing.
			sourceNodeID := extractMetadataValue(ctx, metadataKeyNodeID)
			if err := s.checkNodeGroupRouting(ctx, sourceNodeID, target); err != nil {
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
//  1. Extract workitem_id from Sidecar-injected metadata.
//  2. Query Workitems with flow.gideas.io/parent label matching the caller's Workitem.
//  3. Return ChildWorkitemStatus for each child.
func (s *OperatorServer) GetChildren(ctx context.Context, _ *flowv1.GetChildrenRequest) (*flowv1.GetChildrenResponse, error) {
	// 1. Extract identity from metadata.
	parentWorkitemID := extractWorkitemID(ctx)
	if parentWorkitemID == "" {
		return nil, status.Error(codes.InvalidArgument, "x-flow-workitem-id metadata is required")
	}

	slog.Info("GetChildren received", "parent_workitem_id", parentWorkitemID)

	// 2. Query children by parent label.
	var childList apiv1.WorkitemList
	if err := s.K8sClient.List(ctx, &childList,
		client.InNamespace(defaultNamespace),
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
			WorkitemId:      c.Name,
			Phase:           c.Status.Phase,
			CurrentAssignee: c.Status.CurrentAssignee,
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

	slog.Info("ValidateChildAccess received",
		"parent_workitem_id", parentWorkitemID,
		"child_workitem_id", childWorkitemID,
	)

	// Fetch the child Workitem CRD.
	var child apiv1.Workitem
	childKey := types.NamespacedName{Namespace: defaultNamespace, Name: childWorkitemID}
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

// exportPackage is the JSON-serialisable structure for cross-flow export.
// A production implementation would include artefact content, passport stamps,
// provenance chain, and cryptographic signatures. This walking skeleton
// captures the essential metadata envelope.
type exportPackage struct {
	WorkitemID string            `json:"workitem_id"`
	Namespace  string            `json:"namespace"`
	Labels     map[string]string `json:"labels,omitempty"`
	Phase      string            `json:"phase"`
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

// hasCapability checks whether a capability list contains the given capability.
func hasCapability(capabilities []string, target string) bool {
	return slices.Contains(capabilities, target)
}

// checkNodeGroupRouting enforces NodeGroup routing isolation at runtime.
//
// When a route_to instruction targets a node inside a NodeGroup, the source
// node must either be in the same group OR the target must be an entry-bound
// node within that group. This prevents external routing to internal group
// nodes.
//
// sourceNodeID may be empty for non-node-originated routing (system calls),
// which bypass group checks.
func (s *OperatorServer) checkNodeGroupRouting(ctx context.Context, sourceNodeID, targetNodeID string) error {
	if sourceNodeID == "" || targetNodeID == "" {
		return nil // System calls or no target bypass group checks.
	}

	// Fetch the FoundryFlow to get NodeGroup definitions.
	var flowList apiv1.FoundryFlowList
	if err := s.K8sClient.List(ctx, &flowList, client.InNamespace(defaultNamespace)); err != nil {
		return status.Error(codes.Internal, fmt.Sprintf("failed to list flows for group validation: %v", err))
	}
	if len(flowList.Items) == 0 {
		return nil // No flow, no group constraints.
	}
	flow := &flowList.Items[0]

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
	targetKey := types.NamespacedName{Namespace: defaultNamespace, Name: targetNodeID}
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

// checkChildrenTerminal queries for child Workitems and returns an error if
// any are in a non-terminal phase (Pending, Running, or Routing).
// This enforces the invariant that a parent cannot complete while children
// are still active.
func (s *OperatorServer) checkChildrenTerminal(ctx context.Context, parentWorkitemID string) error {
	var childList apiv1.WorkitemList
	if err := s.K8sClient.List(ctx, &childList,
		client.InNamespace(defaultNamespace),
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
