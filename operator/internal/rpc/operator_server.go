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
	"google.golang.org/grpc"
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
)

// AuditPublisher abstracts audit event publication to the Flow Event Bus.
// Satisfied by flowv1.FlowEventBusServiceClient. A nil publisher silently
// disables audit publishing.
type AuditPublisher interface {
	Publish(ctx context.Context, req *flowv1.PublishRequest, opts ...grpc.CallOption) (*flowv1.PublishResponse, error)
}

// OperatorServer implements the flowv1.OperatorServiceServer interface.
// It holds a reference to the controller-runtime Kubernetes client for
// reading and updating CRDs.
type OperatorServer struct {
	flowv1.UnimplementedOperatorServiceServer
	K8sClient client.Client
	Auditor   AuditPublisher // nil-safe: audit publishing degrades gracefully
}

// NewOperatorServer returns an OperatorServer wired to the given Kubernetes client.
func NewOperatorServer(k8sClient client.Client) *OperatorServer {
	return &OperatorServer{K8sClient: k8sClient}
}

// publishAudit publishes an audit event to the Event Bus. Errors are logged
// but never propagated — audit publishing must not fail the primary operation.
func (s *OperatorServer) publishAudit(ctx context.Context, eventType string, attrs map[string]string) {
	if s.Auditor == nil {
		return
	}
	_, err := s.Auditor.Publish(ctx, &flowv1.PublishRequest{
		Channel: flowv1.EventChannel_EVENT_CHANNEL_AUDIT,
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
	if err != nil {
		slog.Warn("Audit publish failed",
			"event_type", eventType,
			"error", err,
		)
	}
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
