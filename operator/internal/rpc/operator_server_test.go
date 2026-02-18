package rpc

import (
	"context"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	apiv1 "github.com/gideas/flow/operator/api/v1"
	"google.golang.org/grpc/metadata"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = apiv1.AddToScheme(s)
	return s
}

func TestSubmitResult_HappyPath(t *testing.T) {
	scheme := newScheme()

	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-123",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase: "Running",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(workitem).
		WithStatusSubresource(workitem).
		Build()

	srv := NewOperatorServer(k8s)

	resp, err := srv.SubmitResult(context.Background(), &flowv1.SubmitResultRequest{
		WorkitemId: "test-123",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_COMPLETE,
			Target: "",
		},
	})
	if err != nil {
		t.Fatalf("SubmitResult() returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}

	// Verify the CRD was updated.
	var updated apiv1.Workitem
	err = k8s.Get(context.Background(), nsName("default", "test-123"), &updated)
	if err != nil {
		t.Fatalf("Failed to get updated workitem: %v", err)
	}
	if updated.Status.Phase != "Routing" {
		t.Fatalf("Expected phase Routing, got %s", updated.Status.Phase)
	}
	if updated.Status.RoutingInstruction == nil {
		t.Fatal("Expected routing instruction to be set")
	}
	if updated.Status.RoutingInstruction.Type != "complete" {
		t.Fatalf("Expected routing type 'complete', got %s", updated.Status.RoutingInstruction.Type)
	}
}

func TestSubmitResult_WorkitemFromMetadata(t *testing.T) {
	scheme := newScheme()

	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-456",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase: "Running",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(workitem).
		WithStatusSubresource(workitem).
		Build()

	srv := NewOperatorServer(k8s)

	// Set workitem_id via metadata, not request body.
	md := metadata.Pairs("x-flow-workitem-id", "test-456")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := srv.SubmitResult(ctx, &flowv1.SubmitResultRequest{
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO_OUTPUT,
			Target: "review",
		},
	})
	if err != nil {
		t.Fatalf("SubmitResult() returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}

	var updated apiv1.Workitem
	err = k8s.Get(context.Background(), nsName("default", "test-456"), &updated)
	if err != nil {
		t.Fatalf("Failed to get updated workitem: %v", err)
	}
	if updated.Status.RoutingInstruction.Type != "route_to_output" {
		t.Fatalf("Expected routing type 'route_to_output', got %s", updated.Status.RoutingInstruction.Type)
	}
	if updated.Status.RoutingInstruction.Target != "review" {
		t.Fatalf("Expected target 'review', got %s", updated.Status.RoutingInstruction.Target)
	}
}

func TestSubmitResult_MissingWorkitemID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	_, err := srv.SubmitResult(context.Background(), &flowv1.SubmitResultRequest{})
	if err == nil {
		t.Fatal("Expected error for missing workitem_id")
	}
}

func TestSubmitResult_WorkitemNotFound(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	_, err := srv.SubmitResult(context.Background(), &flowv1.SubmitResultRequest{
		WorkitemId: "nonexistent",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type: flowv1.RoutingType_ROUTING_TYPE_COMPLETE,
		},
	})
	if err == nil {
		t.Fatal("Expected error for nonexistent workitem")
	}
}

func TestConvertRoutingInstruction(t *testing.T) {
	tests := []struct {
		name       string
		proto      *flowv1.RoutingInstruction
		wantType   string
		wantTarget string
		wantNil    bool
	}{
		{
			name:    "nil instruction",
			proto:   nil,
			wantNil: true,
		},
		{
			name:       "complete",
			proto:      &flowv1.RoutingInstruction{Type: flowv1.RoutingType_ROUTING_TYPE_COMPLETE},
			wantType:   "complete",
			wantTarget: "",
		},
		{
			name:       "route_to_output",
			proto:      &flowv1.RoutingInstruction{Type: flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO_OUTPUT, Target: "review"},
			wantType:   "route_to_output",
			wantTarget: "review",
		},
		{
			name:       "route_to",
			proto:      &flowv1.RoutingInstruction{Type: flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO, Target: "node-b"},
			wantType:   "route_to",
			wantTarget: "node-b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertRoutingInstruction(tt.proto)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("Expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("Expected non-nil result")
			}
			if got.Type != tt.wantType {
				t.Fatalf("Expected type %s, got %s", tt.wantType, got.Type)
			}
			if got.Target != tt.wantTarget {
				t.Fatalf("Expected target %s, got %s", tt.wantTarget, got.Target)
			}
		})
	}
}

// nsName is a test helper to construct a NamespacedName.
func nsName(namespace, name string) types.NamespacedName {
	return types.NamespacedName{Namespace: namespace, Name: name}
}

// ---------------------------------------------------------------------------
// GetFlowTopology tests
// ---------------------------------------------------------------------------

// topoCtx creates a context with Sidecar-injected flow and node identity metadata.
func topoCtx(flowID, nodeID string) context.Context {
	md := metadata.Pairs("x-flow-flow-id", flowID, "x-flow-node-id", nodeID)
	return metadata.NewIncomingContext(context.Background(), md)
}

func TestGetFlowTopology_HappyPath(t *testing.T) {
	scheme := newScheme()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "haiku-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts: map[string]apiv1.Contract{"main": {"haiku": nil}},
			ExitContracts: map[string]apiv1.Contract{
				"governed": {"haiku": {"linter", "review", "approval"}},
			},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 100},
		},
	}

	sortNode := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "sort", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image: "sort:latest",
			Outputs: []apiv1.Output{
				{Name: "quench", Target: "quench"},
				{Name: "appraise", Target: "appraise"},
				{Name: "refine", Target: "refine"},
				{Name: "assay", Target: "assay"},
			},
			Capabilities: []string{
				"READ:flow",
				"READ:artefact",
				"READ:feedback",
				"WRITE:feedback/deadlocked",
				"STAMP:artefact/haiku/approval",
			},
			Exit: "governed",
		},
	}

	quenchNode := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "quench", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image:        "quench:latest",
			Capabilities: []string{"READ:artefact", "STAMP:artefact/haiku/linter", "WRITE:feedback/new"},
		},
	}

	appraiseNode := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "appraise", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image:        "appraise:latest",
			Capabilities: []string{"READ:artefact", "STAMP:artefact/haiku/review", "WRITE:feedback/new"},
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, sortNode, quenchNode, appraiseNode).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := topoCtx("haiku-flow", "sort")

	resp, err := srv.GetFlowTopology(ctx, &flowv1.GetFlowTopologyRequest{})
	if err != nil {
		t.Fatalf("GetFlowTopology() returned error: %v", err)
	}

	// Verify self.
	if resp.GetSelf().GetName() != "sort" {
		t.Fatalf("Expected self.name=sort, got %s", resp.GetSelf().GetName())
	}
	if len(resp.GetSelf().GetOutputs()) != 4 {
		t.Fatalf("Expected 4 outputs on self, got %d", len(resp.GetSelf().GetOutputs()))
	}

	// Verify nodes map.
	if len(resp.GetNodes()) != 3 {
		t.Fatalf("Expected 3 nodes, got %d", len(resp.GetNodes()))
	}
	if _, ok := resp.GetNodes()["quench"]; !ok {
		t.Fatal("Expected quench in nodes map")
	}
	if _, ok := resp.GetNodes()["appraise"]; !ok {
		t.Fatal("Expected appraise in nodes map")
	}

	// Verify exit contract.
	if len(resp.GetExitContract()) != 1 {
		t.Fatalf("Expected 1 exit contract kind, got %d", len(resp.GetExitContract()))
	}
	haikuStamps := resp.GetExitContract()["haiku"]
	if haikuStamps == nil {
		t.Fatal("Expected haiku in exit contract")
	}
	if len(haikuStamps.GetStamps()) != 3 {
		t.Fatalf("Expected 3 stamps in haiku exit contract, got %d", len(haikuStamps.GetStamps()))
	}
}

func TestGetFlowTopology_NonExitNode_EmptyExitContract(t *testing.T) {
	scheme := newScheme()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {}},
			ExitContracts:    map[string]apiv1.Contract{"governed": {"doc": {"stamp-a"}}},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
		},
	}

	node := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image:        "worker:latest",
			Capabilities: []string{"READ:flow"},
			// No exit binding.
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, node).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := topoCtx("test-flow", "worker")

	resp, err := srv.GetFlowTopology(ctx, &flowv1.GetFlowTopologyRequest{})
	if err != nil {
		t.Fatalf("GetFlowTopology() returned error: %v", err)
	}

	if len(resp.GetExitContract()) != 0 {
		t.Fatalf("Expected empty exit contract for non-exit node, got %d kinds", len(resp.GetExitContract()))
	}
}

func TestGetFlowTopology_MissingFlowID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	md := metadata.Pairs("x-flow-node-id", "sort")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.GetFlowTopology(ctx, &flowv1.GetFlowTopologyRequest{})
	if err == nil {
		t.Fatal("Expected error for missing flow_id")
	}
}

func TestGetFlowTopology_MissingNodeID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	md := metadata.Pairs("x-flow-flow-id", "test-flow")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.GetFlowTopology(ctx, &flowv1.GetFlowTopologyRequest{})
	if err == nil {
		t.Fatal("Expected error for missing node_id")
	}
}

func TestGetFlowTopology_FlowNotFound(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	_, err := srv.GetFlowTopology(topoCtx("nonexistent", "sort"), &flowv1.GetFlowTopologyRequest{})
	if err == nil {
		t.Fatal("Expected error for nonexistent flow")
	}
}

func TestGetFlowTopology_NodeNotFound(t *testing.T) {
	scheme := newScheme()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {}},
			ExitContracts:    map[string]apiv1.Contract{},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow).
		Build()

	srv := NewOperatorServer(k8s)

	_, err := srv.GetFlowTopology(topoCtx("test-flow", "nonexistent"), &flowv1.GetFlowTopologyRequest{})
	if err == nil {
		t.Fatal("Expected error for nonexistent node")
	}
}

func TestGetFlowTopology_NodeCapabilities(t *testing.T) {
	scheme := newScheme()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {}},
			ExitContracts:    map[string]apiv1.Contract{},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
		},
	}

	node := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "validator", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image: "validator:latest",
			Capabilities: []string{
				"READ:flow",
				"STAMP:artefact/doc/linter",
				"STAMP:artefact/doc/security",
			},
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, node).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := topoCtx("test-flow", "validator")

	resp, err := srv.GetFlowTopology(ctx, &flowv1.GetFlowTopologyRequest{})
	if err != nil {
		t.Fatalf("GetFlowTopology() returned error: %v", err)
	}

	validatorNode := resp.GetNodes()["validator"]
	if validatorNode == nil {
		t.Fatal("Expected validator in nodes map")
	}
	if len(validatorNode.GetCapabilities()) != 3 {
		t.Fatalf("Expected 3 capabilities, got %d", len(validatorNode.GetCapabilities()))
	}
}
