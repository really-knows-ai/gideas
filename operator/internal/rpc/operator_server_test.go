package rpc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	apiv1 "github.com/gideas/flow/operator/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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
	err = k8s.Get(context.Background(), nsName("test-123"), &updated)
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
	err = k8s.Get(context.Background(), nsName("test-456"), &updated)
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

// nsName is a test helper to construct a NamespacedName in the default namespace.
func nsName(name string) types.NamespacedName {
	return types.NamespacedName{Namespace: "default", Name: name}
}

// ---------------------------------------------------------------------------
// GetFlowTopology tests
// ---------------------------------------------------------------------------

// topoCtx creates a context with Sidecar-injected flow and node identity metadata.
func topoCtx(flowID, nodeID string) context.Context {
	md := metadata.Pairs(
		"x-flow-flow-id", flowID,
		"x-flow-node-id", nodeID,
		"x-flow-capabilities", "READ:flow",
	)
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
				{Name: "arbiter", Target: "arbiter"},
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

// ---------------------------------------------------------------------------
// Helper: deterministic time for suffix generation
// ---------------------------------------------------------------------------

// fixedTime overrides timeNow for deterministic test output.
func fixedTime(t *testing.T) {
	t.Helper()
	orig := timeNow
	t.Cleanup(func() { timeNow = orig })
	timeNow = func() metav1.Time {
		return metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC))
	}
}

// assertGRPCCode checks that the error has the expected gRPC status code.
func assertGRPCCode(t *testing.T, err error, expected codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("Expected gRPC error with code %s, got nil", expected)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("Expected gRPC status error, got: %v", err)
	}
	if st.Code() != expected {
		t.Fatalf("Expected gRPC code %s, got %s: %s", expected, st.Code(), st.Message())
	}
}

// ---------------------------------------------------------------------------
// CreateWorkitem tests
// ---------------------------------------------------------------------------

func TestCreateWorkitem_HappyPath(t *testing.T) {
	fixedTime(t)
	scheme := newScheme()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {"doc": nil}},
			ExitContracts:    map[string]apiv1.Contract{},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
		},
	}

	entryNode := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "intake", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image:        "intake:latest",
			Entry:        "main",
			Capabilities: []string{"READ:flow"},
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, entryNode).
		WithStatusSubresource(&apiv1.Workitem{}).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := topoCtx("test-flow", "intake")

	resp, err := srv.CreateWorkitem(ctx, &flowv1.CreateWorkitemRequest{})
	if err != nil {
		t.Fatalf("CreateWorkitem() returned error: %v", err)
	}

	if resp.GetWorkitemId() == "" {
		t.Fatal("Expected non-empty workitem_id")
	}

	// Verify prefix.
	if !strings.HasPrefix(resp.GetWorkitemId(), "wi-test-flow-") {
		t.Fatalf("Expected workitem_id prefix 'wi-test-flow-', got %s", resp.GetWorkitemId())
	}

	// Verify the CRD was created with correct status.
	var created apiv1.Workitem
	err = k8s.Get(context.Background(), nsName(resp.GetWorkitemId()), &created)
	if err != nil {
		t.Fatalf("Failed to get created workitem: %v", err)
	}
	if created.Status.Phase != phasePending {
		t.Fatalf("Expected phase Pending, got %s", created.Status.Phase)
	}
	if created.Status.CurrentAssignee != "intake" {
		t.Fatalf("Expected assignee 'intake', got %s", created.Status.CurrentAssignee)
	}

	// Verify labels.
	if created.Labels["flow.gideas.io/flow"] != "test-flow" {
		t.Fatalf("Expected flow label 'test-flow', got %s", created.Labels["flow.gideas.io/flow"])
	}
	if created.Labels["flow.gideas.io/creator"] != "intake" {
		t.Fatalf("Expected creator label 'intake', got %s", created.Labels["flow.gideas.io/creator"])
	}
}

func TestCreateWorkitem_NodeNotEntryBound(t *testing.T) {
	scheme := newScheme()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {}},
			ExitContracts:    map[string]apiv1.Contract{},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
		},
	}

	// Worker node without entry binding.
	worker := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image: "worker:latest",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, worker).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := topoCtx("test-flow", "worker")

	_, err := srv.CreateWorkitem(ctx, &flowv1.CreateWorkitemRequest{})
	assertGRPCCode(t, err, codes.FailedPrecondition)

	if !strings.Contains(err.Error(), "ENTRY_NOT_BOUND") {
		t.Fatalf("Expected ENTRY_NOT_BOUND error, got: %v", err)
	}
}

func TestCreateWorkitem_MissingFlowID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	// Only node_id in metadata, no flow_id.
	md := metadata.Pairs("x-flow-node-id", "intake")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.CreateWorkitem(ctx, &flowv1.CreateWorkitemRequest{})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestCreateWorkitem_MissingNodeID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	md := metadata.Pairs("x-flow-flow-id", "test-flow")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.CreateWorkitem(ctx, &flowv1.CreateWorkitemRequest{})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestCreateWorkitem_EntryContractNotOnFlow(t *testing.T) {
	scheme := newScheme()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {}},
			ExitContracts:    map[string]apiv1.Contract{},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
		},
	}

	// Node bound to entry contract "other" which does not exist on the flow.
	node := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "intake", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image: "intake:latest",
			Entry: "nonexistent-contract",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, node).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := topoCtx("test-flow", "intake")

	_, err := srv.CreateWorkitem(ctx, &flowv1.CreateWorkitemRequest{})
	assertGRPCCode(t, err, codes.FailedPrecondition)

	if !strings.Contains(err.Error(), "CONTRACT_VIOLATION") {
		t.Fatalf("Expected CONTRACT_VIOLATION error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CreateHearingWorkitem tests
// ---------------------------------------------------------------------------

func TestCreateHearingWorkitem_HappyPath(t *testing.T) {
	fixedTime(t)
	scheme := newScheme()

	// Tribunal node: entry-bound and has USE:jury capability.
	tribunal := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "tribunal", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image:        "tribunal:latest",
			Entry:        "hearing",
			Capabilities: []string{"USE:jury", "USE:clerk", "READ:law"},
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tribunal).
		WithStatusSubresource(&apiv1.Workitem{}).
		Build()

	srv := NewOperatorServer(k8s)

	resp, err := srv.CreateHearingWorkitem(context.Background(), &flowv1.CreateHearingWorkitemRequest{
		LawId: "law-42",
	})
	if err != nil {
		t.Fatalf("CreateHearingWorkitem() returned error: %v", err)
	}

	if resp.GetWorkitemId() == "" {
		t.Fatal("Expected non-empty workitem_id")
	}
	if !strings.HasPrefix(resp.GetWorkitemId(), "hearing-law-42-") {
		t.Fatalf("Expected workitem_id prefix 'hearing-law-42-', got %s", resp.GetWorkitemId())
	}

	// Verify the CRD was created.
	var created apiv1.Workitem
	err = k8s.Get(context.Background(), nsName(resp.GetWorkitemId()), &created)
	if err != nil {
		t.Fatalf("Failed to get created hearing workitem: %v", err)
	}
	if created.Status.Phase != phasePending {
		t.Fatalf("Expected phase Pending, got %s", created.Status.Phase)
	}
	if created.Status.CurrentAssignee != "tribunal" {
		t.Fatalf("Expected assignee 'tribunal', got %s", created.Status.CurrentAssignee)
	}

	// Verify labels.
	if created.Labels["flow.gideas.io/type"] != "hearing" {
		t.Fatalf("Expected type label 'hearing', got %s", created.Labels["flow.gideas.io/type"])
	}
	if created.Labels["flow.gideas.io/law-id"] != "law-42" {
		t.Fatalf("Expected law-id label 'law-42', got %s", created.Labels["flow.gideas.io/law-id"])
	}
}

func TestCreateHearingWorkitem_MissingLawID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	_, err := srv.CreateHearingWorkitem(context.Background(), &flowv1.CreateHearingWorkitemRequest{})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestCreateHearingWorkitem_NoTribunalNode(t *testing.T) {
	scheme := newScheme()

	// Node without USE:jury capability — not a Tribunal.
	worker := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image:        "worker:latest",
			Capabilities: []string{"READ:flow"},
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(worker).
		Build()

	srv := NewOperatorServer(k8s)

	_, err := srv.CreateHearingWorkitem(context.Background(), &flowv1.CreateHearingWorkitemRequest{
		LawId: "law-99",
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)
}

// ---------------------------------------------------------------------------
// ExportWorkitem tests
// ---------------------------------------------------------------------------

func TestExportWorkitem_HappyPath(t *testing.T) {
	scheme := newScheme()

	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "export-test",
			Namespace: "default",
			Labels:    map[string]string{"flow.gideas.io/flow": "my-flow"},
		},
		Status: apiv1.WorkitemStatus{
			Phase: "Completed",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(workitem).
		WithStatusSubresource(workitem).
		Build()

	srv := NewOperatorServer(k8s)

	resp, err := srv.ExportWorkitem(context.Background(), &flowv1.ExportWorkitemRequest{
		WorkitemId: "export-test",
	})
	if err != nil {
		t.Fatalf("ExportWorkitem() returned error: %v", err)
	}

	if len(resp.GetExportPackage()) == 0 {
		t.Fatal("Expected non-empty export package")
	}

	// Verify the package is valid JSON containing the workitem_id.
	var pkg exportPackage
	if err := json.Unmarshal(resp.GetExportPackage(), &pkg); err != nil {
		t.Fatalf("Failed to unmarshal export package: %v", err)
	}
	if pkg.WorkitemID != "export-test" {
		t.Fatalf("Expected workitem_id 'export-test', got %s", pkg.WorkitemID)
	}
	if pkg.Phase != "Completed" {
		t.Fatalf("Expected phase 'Completed', got %s", pkg.Phase)
	}
}

func TestExportWorkitem_NotCompleted(t *testing.T) {
	scheme := newScheme()

	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "running-workitem",
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

	_, err := srv.ExportWorkitem(context.Background(), &flowv1.ExportWorkitemRequest{
		WorkitemId: "running-workitem",
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)
}

func TestExportWorkitem_MissingWorkitemID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	_, err := srv.ExportWorkitem(context.Background(), &flowv1.ExportWorkitemRequest{})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestExportWorkitem_WorkitemNotFound(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	_, err := srv.ExportWorkitem(context.Background(), &flowv1.ExportWorkitemRequest{
		WorkitemId: "nonexistent",
	})
	assertGRPCCode(t, err, codes.NotFound)
}

// ---------------------------------------------------------------------------
// ImportWorkitem tests
// ---------------------------------------------------------------------------

func TestImportWorkitem_HappyPath(t *testing.T) {
	fixedTime(t)
	scheme := newScheme()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "receiving-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"import-entry": {"doc": nil}},
			ExitContracts:    map[string]apiv1.Contract{},
			ImportNode:       "import-intake",
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
		},
	}

	importNode := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "import-intake", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image:        "intake:latest",
			Entry:        "import-entry",
			Capabilities: []string{"READ:flow"},
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, importNode).
		WithStatusSubresource(&apiv1.Workitem{}).
		Build()

	srv := NewOperatorServer(k8s)

	pkg := exportPackage{
		WorkitemID: "source-wi-123",
		Namespace:  "remote",
		Phase:      "Completed",
	}
	data, _ := json.Marshal(pkg)

	resp, err := srv.ImportWorkitem(context.Background(), &flowv1.ImportWorkitemRequest{
		ExportPackage: data,
	})
	if err != nil {
		t.Fatalf("ImportWorkitem() returned error: %v", err)
	}

	if resp.GetWorkitemId() == "" {
		t.Fatal("Expected non-empty workitem_id")
	}
	if !strings.HasPrefix(resp.GetWorkitemId(), "import-source-wi-123-") {
		t.Fatalf("Expected workitem_id prefix 'import-source-wi-123-', got %s", resp.GetWorkitemId())
	}

	// Verify the CRD was created.
	var created apiv1.Workitem
	err = k8s.Get(context.Background(), nsName(resp.GetWorkitemId()), &created)
	if err != nil {
		t.Fatalf("Failed to get imported workitem: %v", err)
	}
	if created.Status.Phase != phasePending {
		t.Fatalf("Expected phase Pending, got %s", created.Status.Phase)
	}
	if created.Status.CurrentAssignee != "import-intake" {
		t.Fatalf("Expected assignee 'import-intake', got %s", created.Status.CurrentAssignee)
	}

	// Verify labels.
	if created.Labels["flow.gideas.io/type"] != "import" {
		t.Fatalf("Expected type label 'import', got %s", created.Labels["flow.gideas.io/type"])
	}
	if created.Labels["flow.gideas.io/source-workitem"] != "source-wi-123" {
		t.Fatalf("Expected source-workitem label 'source-wi-123', got %s", created.Labels["flow.gideas.io/source-workitem"])
	}
}

func TestImportWorkitem_WithTreaty(t *testing.T) {
	fixedTime(t)
	scheme := newScheme()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "receiving-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"import-entry": {}},
			ExitContracts:    map[string]apiv1.Contract{},
			ImportNode:       "import-intake",
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
		},
	}

	importNode := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "import-intake", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image: "intake:latest",
			Entry: "import-entry",
		},
	}

	treaty := &apiv1.Treaty{
		ObjectMeta: metav1.ObjectMeta{Name: "remote-treaty", Namespace: "default"},
		Spec: apiv1.TreatySpec{
			RemoteName: "remote-flow",
			Direction:  "import",
			CACert:     "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, importNode, treaty).
		WithStatusSubresource(&apiv1.Workitem{}).
		Build()

	srv := NewOperatorServer(k8s)

	pkg := exportPackage{WorkitemID: "remote-wi", Namespace: "remote", Phase: "Completed"}
	data, _ := json.Marshal(pkg)

	resp, err := srv.ImportWorkitem(context.Background(), &flowv1.ImportWorkitemRequest{
		ExportPackage: data,
		TreatyName:    "remote-treaty",
	})
	if err != nil {
		t.Fatalf("ImportWorkitem() with treaty returned error: %v", err)
	}
	if resp.GetWorkitemId() == "" {
		t.Fatal("Expected non-empty workitem_id")
	}
}

func TestImportWorkitem_TreatyWrongDirection(t *testing.T) {
	scheme := newScheme()

	treaty := &apiv1.Treaty{
		ObjectMeta: metav1.ObjectMeta{Name: "export-treaty", Namespace: "default"},
		Spec: apiv1.TreatySpec{
			RemoteName: "remote-flow",
			Direction:  "export",
			CACert:     "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(treaty).
		Build()

	srv := NewOperatorServer(k8s)

	pkg := exportPackage{WorkitemID: "wi-1", Namespace: "remote", Phase: "Completed"}
	data, _ := json.Marshal(pkg)

	_, err := srv.ImportWorkitem(context.Background(), &flowv1.ImportWorkitemRequest{
		ExportPackage: data,
		TreatyName:    "export-treaty",
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)
}

func TestImportWorkitem_EmptyPackage(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	_, err := srv.ImportWorkitem(context.Background(), &flowv1.ImportWorkitemRequest{})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestImportWorkitem_InvalidPackageJSON(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	_, err := srv.ImportWorkitem(context.Background(), &flowv1.ImportWorkitemRequest{
		ExportPackage: []byte("not-json"),
	})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestImportWorkitem_NoImportNodeConfigured(t *testing.T) {
	scheme := newScheme()

	// Flow without importNode.
	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "no-import-flow", Namespace: "default"},
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

	pkg := exportPackage{WorkitemID: "wi-1", Namespace: "remote", Phase: "Completed"}
	data, _ := json.Marshal(pkg)

	_, err := srv.ImportWorkitem(context.Background(), &flowv1.ImportWorkitemRequest{
		ExportPackage: data,
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)
}

func TestImportWorkitem_ImportNodeNotEntryBound(t *testing.T) {
	scheme := newScheme()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {}},
			ExitContracts:    map[string]apiv1.Contract{},
			ImportNode:       "bad-import",
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
		},
	}

	// Import node without entry binding.
	importNode := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-import", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image: "import:latest",
			// No entry binding.
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, importNode).
		Build()

	srv := NewOperatorServer(k8s)

	pkg := exportPackage{WorkitemID: "wi-1", Namespace: "remote", Phase: "Completed"}
	data, _ := json.Marshal(pkg)

	_, err := srv.ImportWorkitem(context.Background(), &flowv1.ImportWorkitemRequest{
		ExportPackage: data,
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)

	if !strings.Contains(err.Error(), "IMPORT_ADMISSION_FAILED") {
		t.Fatalf("Expected IMPORT_ADMISSION_FAILED error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// GetFlowTopology capability enforcement tests
// ---------------------------------------------------------------------------

func TestGetFlowTopology_CapabilityDenied(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	// Node call with WRITE:artefact but NOT READ:flow.
	md := metadata.Pairs(
		"x-flow-flow-id", "test-flow",
		"x-flow-node-id", "node-1",
		"x-flow-capabilities", "WRITE:artefact,READ:artefact",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.GetFlowTopology(ctx, &flowv1.GetFlowTopologyRequest{})
	assertGRPCCode(t, err, codes.PermissionDenied)
}

func TestGetFlowTopology_NodeCallNoCapabilities_Denied(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	// Node identity present but no capabilities at all.
	md := metadata.Pairs(
		"x-flow-flow-id", "test-flow",
		"x-flow-node-id", "node-1",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.GetFlowTopology(ctx, &flowv1.GetFlowTopologyRequest{})
	assertGRPCCode(t, err, codes.PermissionDenied)
}
