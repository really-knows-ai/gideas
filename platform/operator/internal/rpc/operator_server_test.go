package rpc

import (
	"context"
	"strings"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	apiv1 "github.com/gideas/flow/operator/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Routing type string constants for test assertions.
const (
	riComplete      = "complete"
	riRouteToOutput = "route_to_output"
	riRouteTo       = "route_to"
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

	resp, err := srv.SubmitResult(nsCtx(), &flowv1.SubmitResultRequest{
		WorkitemId: "test-123",
		Action:     &flowv1.SubmitResultRequest_Complete{Complete: &flowv1.CompleteAction{}},
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
	if updated.Status.Phase != phaseRouting {
		t.Fatalf("Expected phase Routing, got %s", updated.Status.Phase)
	}
	if updated.Status.RoutingInstruction == nil {
		t.Fatal("Expected routing instruction to be set")
	}
	if updated.Status.RoutingInstruction.Type != riComplete {
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
	md := metadata.Pairs("x-flow-workitem-id", "test-456", "x-flow-namespace", "default")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := srv.SubmitResult(ctx, &flowv1.SubmitResultRequest{
		Action: &flowv1.SubmitResultRequest_Route{Route: &flowv1.RouteAction{Target: "review", Output: true}},
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
	if updated.Status.RoutingInstruction.Type != riRouteToOutput {
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

	_, err := srv.SubmitResult(nsCtx(), &flowv1.SubmitResultRequest{
		WorkitemId: "nonexistent",
		Action:     &flowv1.SubmitResultRequest_Complete{Complete: &flowv1.CompleteAction{}},
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

// nsCtx returns a context carrying x-flow-namespace=default metadata.
// Used by tests that call RPCs requiring namespace for CRD lookups.
func nsCtx() context.Context {
	md := metadata.Pairs("x-flow-namespace", "default")
	return metadata.NewIncomingContext(context.Background(), md)
}

// ---------------------------------------------------------------------------
// GetFlowTopology tests
// ---------------------------------------------------------------------------

// topoCtx creates a context with Sidecar-injected namespace and node identity metadata.
func topoCtx(namespace, nodeID string) context.Context {
	md := metadata.Pairs(
		"x-flow-namespace", namespace,
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
	ctx := topoCtx("default", "sort")

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
	ctx := topoCtx("default", "worker")

	resp, err := srv.GetFlowTopology(ctx, &flowv1.GetFlowTopologyRequest{})
	if err != nil {
		t.Fatalf("GetFlowTopology() returned error: %v", err)
	}

	if len(resp.GetExitContract()) != 0 {
		t.Fatalf("Expected empty exit contract for non-exit node, got %d kinds", len(resp.GetExitContract()))
	}
}

func TestGetFlowTopology_MissingNamespace(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	md := metadata.Pairs("x-flow-node-id", "sort")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.GetFlowTopology(ctx, &flowv1.GetFlowTopologyRequest{})
	if err == nil {
		t.Fatal("Expected error for missing namespace")
	}
}

func TestGetFlowTopology_MissingNodeID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	md := metadata.Pairs("x-flow-namespace", "default")
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

	_, err := srv.GetFlowTopology(topoCtx("empty-ns", "sort"), &flowv1.GetFlowTopologyRequest{})
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

	_, err := srv.GetFlowTopology(topoCtx("default", "nonexistent"), &flowv1.GetFlowTopologyRequest{})
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
	ctx := topoCtx("default", "validator")

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
	ctx := topoCtx("default", "intake")

	resp, err := srv.CreateWorkitem(ctx, &flowv1.CreateWorkitemRequest{})
	if err != nil {
		t.Fatalf("CreateWorkitem() returned error: %v", err)
	}

	if resp.GetWorkitemId() == "" {
		t.Fatal("Expected non-empty workitem_id")
	}

	// Verify prefix (no longer includes flow name).
	if !strings.HasPrefix(resp.GetWorkitemId(), "wi-") {
		t.Fatalf("Expected workitem_id prefix 'wi-', got %s", resp.GetWorkitemId())
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

	// Verify labels — no flow.gideas.io/flow label, only creator.
	if _, hasFlowLabel := created.Labels["flow.gideas.io/flow"]; hasFlowLabel {
		t.Fatal("Expected no flow.gideas.io/flow label on workitem")
	}
	if created.Labels["flow.gideas.io/creator"] != "intake" {
		t.Fatalf("Expected creator label 'intake', got %s", created.Labels["flow.gideas.io/creator"])
	}
}

func TestCreateWorkitem_WithMetadata(t *testing.T) {
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
		ObjectMeta: metav1.ObjectMeta{Name: "watcher", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image:        "watcher:latest",
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
	ctx := topoCtx("default", "watcher")

	resp, err := srv.CreateWorkitem(ctx, &flowv1.CreateWorkitemRequest{
		Metadata: map[string]string{"law_id": "law-42", "trigger": "friction"},
	})
	if err != nil {
		t.Fatalf("CreateWorkitem() returned error: %v", err)
	}

	// Verify the CRD stores the metadata.
	var created apiv1.Workitem
	err = k8s.Get(context.Background(), nsName(resp.GetWorkitemId()), &created)
	if err != nil {
		t.Fatalf("Failed to get created workitem: %v", err)
	}
	if len(created.Status.Metadata) != 2 {
		t.Fatalf("Expected 2 metadata entries, got %d: %v", len(created.Status.Metadata), created.Status.Metadata)
	}
	if created.Status.Metadata["law_id"] != "law-42" {
		t.Fatalf("Expected metadata law_id=law-42, got %s", created.Status.Metadata["law_id"])
	}
	if created.Status.Metadata["trigger"] != "friction" {
		t.Fatalf("Expected metadata trigger=friction, got %s", created.Status.Metadata["trigger"])
	}
}

func TestCreateWorkitem_NoMetadata(t *testing.T) {
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
	ctx := topoCtx("default", "intake")

	// Empty request — no metadata.
	resp, err := srv.CreateWorkitem(ctx, &flowv1.CreateWorkitemRequest{})
	if err != nil {
		t.Fatalf("CreateWorkitem() returned error: %v", err)
	}

	var created apiv1.Workitem
	err = k8s.Get(context.Background(), nsName(resp.GetWorkitemId()), &created)
	if err != nil {
		t.Fatalf("Failed to get created workitem: %v", err)
	}
	// Metadata should be nil/empty when not provided.
	if len(created.Status.Metadata) != 0 {
		t.Fatalf("Expected empty metadata, got %v", created.Status.Metadata)
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
	ctx := topoCtx("default", "worker")

	_, err := srv.CreateWorkitem(ctx, &flowv1.CreateWorkitemRequest{})
	assertGRPCCode(t, err, codes.FailedPrecondition)

	if !strings.Contains(err.Error(), "ENTRY_NOT_BOUND") {
		t.Fatalf("Expected ENTRY_NOT_BOUND error, got: %v", err)
	}
}

func TestCreateWorkitem_MissingNamespace(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	// Only node_id in metadata, no namespace.
	md := metadata.Pairs("x-flow-node-id", "intake")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.CreateWorkitem(ctx, &flowv1.CreateWorkitemRequest{})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestCreateWorkitem_MissingNodeID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	md := metadata.Pairs("x-flow-namespace", "default")
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
	ctx := topoCtx("default", "intake")

	_, err := srv.CreateWorkitem(ctx, &flowv1.CreateWorkitemRequest{})
	assertGRPCCode(t, err, codes.FailedPrecondition)

	if !strings.Contains(err.Error(), "CONTRACT_VIOLATION") {
		t.Fatalf("Expected CONTRACT_VIOLATION error, got: %v", err)
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
		"x-flow-namespace", "default",
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
		"x-flow-namespace", "default",
		"x-flow-node-id", "node-1",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.GetFlowTopology(ctx, &flowv1.GetFlowTopologyRequest{})
	assertGRPCCode(t, err, codes.PermissionDenied)
}

// ---------------------------------------------------------------------------
// childCtx creates a context with Sidecar-injected metadata for child Workitem
// operations. The caller has CREATE:workitem/child capability.
// ---------------------------------------------------------------------------

func childCtx(namespace, nodeID, workitemID string) context.Context {
	md := metadata.Pairs(
		"x-flow-namespace", namespace,
		"x-flow-node-id", nodeID,
		"x-flow-workitem-id", workitemID,
		"x-flow-capabilities", "CREATE:workitem/child,READ:flow",
	)
	return metadata.NewIncomingContext(context.Background(), md)
}

// workitemCtx creates a context with Sidecar-injected metadata that carries
// the workitem identity and namespace but no special capabilities.
func workitemCtx(workitemID string) context.Context {
	md := metadata.Pairs(
		"x-flow-workitem-id", workitemID,
		"x-flow-namespace", "default",
	)
	return metadata.NewIncomingContext(context.Background(), md)
}

// ---------------------------------------------------------------------------
// CreateChildWorkitem tests
// ---------------------------------------------------------------------------

func TestCreateChildWorkitem_HappyPath(t *testing.T) {
	fixedTime(t)
	scheme := newScheme()

	parent := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "parent-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "clerk",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(parent).
		WithStatusSubresource(parent, &apiv1.Workitem{}).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := childCtx("default", "clerk", "parent-wi")

	resp, err := srv.CreateChildWorkitem(ctx, &flowv1.CreateChildWorkitemRequest{})
	if err != nil {
		t.Fatalf("CreateChildWorkitem() returned error: %v", err)
	}

	if resp.GetChildWorkitemId() == "" {
		t.Fatal("Expected non-empty child_workitem_id")
	}
	if !strings.HasPrefix(resp.GetChildWorkitemId(), "child-parent-wi-") {
		t.Fatalf("Expected prefix 'child-parent-wi-', got %s", resp.GetChildWorkitemId())
	}

	// Verify the CRD was created.
	var child apiv1.Workitem
	err = k8s.Get(context.Background(), nsName(resp.GetChildWorkitemId()), &child)
	if err != nil {
		t.Fatalf("Failed to get created child workitem: %v", err)
	}
	if child.Status.Phase != phasePending {
		t.Fatalf("Expected phase Pending, got %s", child.Status.Phase)
	}
	if child.Status.ParentWorkitemID != "parent-wi" {
		t.Fatalf("Expected ParentWorkitemID 'parent-wi', got %s", child.Status.ParentWorkitemID)
	}

	// Verify labels — no flow.gideas.io/flow label.
	if child.Labels["flow.gideas.io/parent"] != "parent-wi" {
		t.Fatalf("Expected parent label 'parent-wi', got %s", child.Labels["flow.gideas.io/parent"])
	}
	if _, hasFlowLabel := child.Labels["flow.gideas.io/flow"]; hasFlowLabel {
		t.Fatal("Expected no flow.gideas.io/flow label on child workitem")
	}
	if child.Labels["flow.gideas.io/creator"] != "clerk" {
		t.Fatalf("Expected creator label 'clerk', got %s", child.Labels["flow.gideas.io/creator"])
	}
}

func TestCreateChildWorkitem_CapabilityDenied(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	// Node call without CREATE:workitem/child capability.
	md := metadata.Pairs(
		"x-flow-namespace", "default",
		"x-flow-node-id", "node-1",
		"x-flow-workitem-id", "wi-1",
		"x-flow-capabilities", "READ:flow,WRITE:artefact",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.CreateChildWorkitem(ctx, &flowv1.CreateChildWorkitemRequest{})
	assertGRPCCode(t, err, codes.PermissionDenied)
}

func TestCreateChildWorkitem_MissingWorkitemID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	// Has capability but no workitem_id.
	md := metadata.Pairs(
		"x-flow-namespace", "default",
		"x-flow-node-id", "node-1",
		"x-flow-capabilities", "CREATE:workitem/child",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.CreateChildWorkitem(ctx, &flowv1.CreateChildWorkitemRequest{})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestCreateChildWorkitem_MissingNamespace(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	md := metadata.Pairs(
		"x-flow-node-id", "node-1",
		"x-flow-workitem-id", "wi-1",
		"x-flow-capabilities", "CREATE:workitem/child",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.CreateChildWorkitem(ctx, &flowv1.CreateChildWorkitemRequest{})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestCreateChildWorkitem_MissingNodeID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	md := metadata.Pairs(
		"x-flow-namespace", "default",
		"x-flow-workitem-id", "wi-1",
		"x-flow-capabilities", "CREATE:workitem/child",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.CreateChildWorkitem(ctx, &flowv1.CreateChildWorkitemRequest{})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestCreateChildWorkitem_ParentNotFound(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	ctx := childCtx("default", "clerk", "nonexistent-parent")

	_, err := srv.CreateChildWorkitem(ctx, &flowv1.CreateChildWorkitemRequest{})
	assertGRPCCode(t, err, codes.NotFound)
}

// ---------------------------------------------------------------------------
// RouteChild tests
// ---------------------------------------------------------------------------

func TestRouteChild_HappyPath(t *testing.T) {
	scheme := newScheme()

	parent := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "parent-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "clerk",
		},
	}

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
			Labels: map[string]string{
				"flow.gideas.io/parent": "parent-wi",
			},
		},
		Status: apiv1.WorkitemStatus{
			Phase:            phasePending,
			ParentWorkitemID: "parent-wi",
		},
	}

	targetNode := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "codify-smt", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image: "codify-smt:latest",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(parent, child, targetNode).
		WithStatusSubresource(parent, child).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := workitemCtx("parent-wi")

	resp, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: "child-wi",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO,
			Target: "codify-smt",
		},
	})
	if err != nil {
		t.Fatalf("RouteChild() returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}

	// Verify the child was updated.
	var updated apiv1.Workitem
	err = k8s.Get(context.Background(), nsName("child-wi"), &updated)
	if err != nil {
		t.Fatalf("Failed to get updated child: %v", err)
	}
	if updated.Status.Phase != phaseRouting {
		t.Fatalf("Expected phase Routing, got %s", updated.Status.Phase)
	}
	if updated.Status.RoutingInstruction == nil {
		t.Fatal("Expected routing instruction to be set")
	}
	if updated.Status.RoutingInstruction.Type != riRouteTo {
		t.Fatalf("Expected routing type 'route_to', got %s", updated.Status.RoutingInstruction.Type)
	}
	if updated.Status.RoutingInstruction.Target != "codify-smt" {
		t.Fatalf("Expected target 'codify-smt', got %s", updated.Status.RoutingInstruction.Target)
	}
}

func TestRouteChild_ChildNotOwned(t *testing.T) {
	scheme := newScheme()

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:            phasePending,
			ParentWorkitemID: "other-parent",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(child).
		WithStatusSubresource(child).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := workitemCtx("my-parent")

	_, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: "child-wi",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO,
			Target: "some-node",
		},
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	if !strings.Contains(err.Error(), "CHILD_NOT_OWNED") {
		t.Fatalf("Expected CHILD_NOT_OWNED error, got: %v", err)
	}
}

func TestRouteChild_ChildAlreadyRouted(t *testing.T) {
	scheme := newScheme()

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:            "Running",
			ParentWorkitemID: "parent-wi",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(child).
		WithStatusSubresource(child).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := workitemCtx("parent-wi")

	_, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: "child-wi",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO,
			Target: "some-node",
		},
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), "CHILD_ALREADY_ROUTED") {
		t.Fatalf("Expected CHILD_ALREADY_ROUTED error, got: %v", err)
	}
}

func TestRouteChild_ChildNotFound(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	ctx := workitemCtx("parent-wi")

	_, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: "nonexistent-child",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO,
			Target: "some-node",
		},
	})
	assertGRPCCode(t, err, codes.NotFound)
}

func TestRouteChild_MissingChildID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	ctx := workitemCtx("parent-wi")

	_, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO,
			Target: "some-node",
		},
	})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestRouteChild_MissingParentID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	_, err := srv.RouteChild(context.Background(), &flowv1.RouteChildRequest{
		ChildWorkitemId: "child-wi",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO,
			Target: "some-node",
		},
	})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestRouteChild_MissingInstruction(t *testing.T) {
	scheme := newScheme()

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:            phasePending,
			ParentWorkitemID: "parent-wi",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(child).
		WithStatusSubresource(child).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := workitemCtx("parent-wi")

	_, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: "child-wi",
	})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestRouteChild_TargetNodeNotFound(t *testing.T) {
	scheme := newScheme()

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:            phasePending,
			ParentWorkitemID: "parent-wi",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(child).
		WithStatusSubresource(child).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := workitemCtx("parent-wi")

	_, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: "child-wi",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO,
			Target: "nonexistent-node",
		},
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), "INVALID_ROUTE") {
		t.Fatalf("Expected INVALID_ROUTE error, got: %v", err)
	}
}

func TestRouteChild_RouteToOutput_NoTargetValidation(t *testing.T) {
	// route_to_output does not validate target node existence (that's the
	// reconciler's job), but it does require a target.
	scheme := newScheme()

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:            phasePending,
			ParentWorkitemID: "parent-wi",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(child).
		WithStatusSubresource(child).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := workitemCtx("parent-wi")

	resp, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: "child-wi",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO_OUTPUT,
			Target: "review",
		},
	})
	if err != nil {
		t.Fatalf("RouteChild(route_to_output) returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}
}

func TestRouteChild_Complete(t *testing.T) {
	scheme := newScheme()

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:            phasePending,
			ParentWorkitemID: "parent-wi",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(child).
		WithStatusSubresource(child).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := workitemCtx("parent-wi")

	resp, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: "child-wi",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type: flowv1.RoutingType_ROUTING_TYPE_COMPLETE,
		},
	})
	if err != nil {
		t.Fatalf("RouteChild(complete) returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}

	// Verify the child was transitioned to Routing with complete instruction.
	var updated apiv1.Workitem
	err = k8s.Get(context.Background(), nsName("child-wi"), &updated)
	if err != nil {
		t.Fatalf("Failed to get updated child: %v", err)
	}
	if updated.Status.RoutingInstruction.Type != riComplete {
		t.Fatalf("Expected routing type 'complete', got %s", updated.Status.RoutingInstruction.Type)
	}
}

// ---------------------------------------------------------------------------
// GetChildren tests
// ---------------------------------------------------------------------------

func TestGetChildren_HappyPath(t *testing.T) {
	scheme := newScheme()

	child1 := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-1",
			Namespace: "default",
			Labels:    map[string]string{"flow.gideas.io/parent": "parent-wi"},
		},
		Status: apiv1.WorkitemStatus{
			Phase:            "Running",
			CurrentAssignee:  "codify-smt",
			ParentWorkitemID: "parent-wi",
		},
	}

	child2 := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-2",
			Namespace: "default",
			Labels:    map[string]string{"flow.gideas.io/parent": "parent-wi"},
		},
		Status: apiv1.WorkitemStatus{
			Phase:            "Completed",
			ParentWorkitemID: "parent-wi",
		},
	}

	// Unrelated workitem (different parent).
	unrelated := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unrelated-wi",
			Namespace: "default",
			Labels:    map[string]string{"flow.gideas.io/parent": "other-parent"},
		},
		Status: apiv1.WorkitemStatus{
			Phase:            "Running",
			ParentWorkitemID: "other-parent",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(child1, child2, unrelated).
		Build()

	srv := NewOperatorServer(k8s)
	ctx := workitemCtx("parent-wi")

	resp, err := srv.GetChildren(ctx, &flowv1.GetChildrenRequest{})
	if err != nil {
		t.Fatalf("GetChildren() returned error: %v", err)
	}

	if len(resp.GetChildren()) != 2 {
		t.Fatalf("Expected 2 children, got %d", len(resp.GetChildren()))
	}

	// Verify child data (order may vary with fake client).
	childMap := make(map[string]*flowv1.ChildWorkitemStatus)
	for _, c := range resp.GetChildren() {
		childMap[c.GetWorkitemId()] = c
	}

	c1, ok := childMap["child-1"]
	if !ok {
		t.Fatal("Expected child-1 in response")
	}
	if c1.GetPhase() != "Running" {
		t.Fatalf("Expected child-1 phase Running, got %s", c1.GetPhase())
	}
	if c1.GetCurrentAssignee() != "codify-smt" {
		t.Fatalf("Expected child-1 assignee 'codify-smt', got %s", c1.GetCurrentAssignee())
	}

	c2, ok := childMap["child-2"]
	if !ok {
		t.Fatal("Expected child-2 in response")
	}
	if c2.GetPhase() != "Completed" {
		t.Fatalf("Expected child-2 phase Completed, got %s", c2.GetPhase())
	}
}

func TestGetChildren_NoChildren(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	ctx := workitemCtx("parent-wi")

	resp, err := srv.GetChildren(ctx, &flowv1.GetChildrenRequest{})
	if err != nil {
		t.Fatalf("GetChildren() returned error: %v", err)
	}

	if len(resp.GetChildren()) != 0 {
		t.Fatalf("Expected 0 children, got %d", len(resp.GetChildren()))
	}
}

func TestGetChildren_MissingWorkitemID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	_, err := srv.GetChildren(context.Background(), &flowv1.GetChildrenRequest{})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

// ---------------------------------------------------------------------------
// Completion guard tests (CHILDREN_NOT_TERMINAL)
// ---------------------------------------------------------------------------

func TestSubmitResult_CompletionGuard_ChildrenPending(t *testing.T) {
	scheme := newScheme()

	parent := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "parent-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "clerk",
		},
	}

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
			Labels:    map[string]string{"flow.gideas.io/parent": "parent-wi"},
		},
		Status: apiv1.WorkitemStatus{
			Phase:            phasePending,
			ParentWorkitemID: "parent-wi",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(parent, child).
		WithStatusSubresource(parent, child).
		Build()

	srv := NewOperatorServer(k8s)

	_, err := srv.SubmitResult(nsCtx(), &flowv1.SubmitResultRequest{
		WorkitemId: "parent-wi",
		Action:     &flowv1.SubmitResultRequest_Complete{Complete: &flowv1.CompleteAction{}},
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), "CHILDREN_NOT_TERMINAL") {
		t.Fatalf("Expected CHILDREN_NOT_TERMINAL error, got: %v", err)
	}
}

func TestSubmitResult_CompletionGuard_ChildrenRunning(t *testing.T) {
	scheme := newScheme()

	parent := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "parent-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "clerk",
		},
	}

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
			Labels:    map[string]string{"flow.gideas.io/parent": "parent-wi"},
		},
		Status: apiv1.WorkitemStatus{
			Phase:            "Running",
			ParentWorkitemID: "parent-wi",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(parent, child).
		WithStatusSubresource(parent, child).
		Build()

	srv := NewOperatorServer(k8s)

	_, err := srv.SubmitResult(nsCtx(), &flowv1.SubmitResultRequest{
		WorkitemId: "parent-wi",
		Action:     &flowv1.SubmitResultRequest_Complete{Complete: &flowv1.CompleteAction{}},
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), "CHILDREN_NOT_TERMINAL") {
		t.Fatalf("Expected CHILDREN_NOT_TERMINAL error, got: %v", err)
	}
}

func TestSubmitResult_CompletionGuard_AllChildrenCompleted(t *testing.T) {
	scheme := newScheme()

	parent := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "parent-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "clerk",
		},
	}

	child1 := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-1",
			Namespace: "default",
			Labels:    map[string]string{"flow.gideas.io/parent": "parent-wi"},
		},
		Status: apiv1.WorkitemStatus{
			Phase:            "Completed",
			ParentWorkitemID: "parent-wi",
		},
	}

	child2 := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-2",
			Namespace: "default",
			Labels:    map[string]string{"flow.gideas.io/parent": "parent-wi"},
		},
		Status: apiv1.WorkitemStatus{
			Phase:            "Failed",
			ParentWorkitemID: "parent-wi",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(parent, child1, child2).
		WithStatusSubresource(parent, child1, child2).
		Build()

	srv := NewOperatorServer(k8s)

	resp, err := srv.SubmitResult(nsCtx(), &flowv1.SubmitResultRequest{
		WorkitemId: "parent-wi",
		Action:     &flowv1.SubmitResultRequest_Complete{Complete: &flowv1.CompleteAction{}},
	})
	if err != nil {
		t.Fatalf("Expected completion to succeed when all children are terminal, got: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}
}

func TestSubmitResult_CompletionGuard_NoChildren(t *testing.T) {
	scheme := newScheme()

	parent := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "parent-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "clerk",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(parent).
		WithStatusSubresource(parent).
		Build()

	srv := NewOperatorServer(k8s)

	resp, err := srv.SubmitResult(nsCtx(), &flowv1.SubmitResultRequest{
		WorkitemId: "parent-wi",
		Action:     &flowv1.SubmitResultRequest_Complete{Complete: &flowv1.CompleteAction{}},
	})
	if err != nil {
		t.Fatalf("Expected completion to succeed with no children, got: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}
}

func TestSubmitResult_CompletionGuard_NonCompleteSkipsCheck(t *testing.T) {
	scheme := newScheme()

	parent := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "parent-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "clerk",
		},
	}

	// Non-terminal child that would block completion.
	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
			Labels:    map[string]string{"flow.gideas.io/parent": "parent-wi"},
		},
		Status: apiv1.WorkitemStatus{
			Phase:            "Running",
			ParentWorkitemID: "parent-wi",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(parent, child).
		WithStatusSubresource(parent, child).
		Build()

	srv := NewOperatorServer(k8s)

	// route_to_output does NOT trigger the completion guard.
	resp, err := srv.SubmitResult(nsCtx(), &flowv1.SubmitResultRequest{
		WorkitemId: "parent-wi",
		Action:     &flowv1.SubmitResultRequest_Route{Route: &flowv1.RouteAction{Target: "review", Output: true}},
	})
	if err != nil {
		t.Fatalf("Expected route_to_output to succeed despite non-terminal child, got: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}
}

// ---------------------------------------------------------------------------
// NodeGroup routing isolation tests (GROUP_ROUTING_DENIED)
// ---------------------------------------------------------------------------

func TestSubmitResult_GroupRoutingDenied_RouteToInternalGroupNode(t *testing.T) {
	scheme := newScheme()

	// Flow with a NodeGroup containing an internal (non-entry-bound) node.
	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {}},
			ExitContracts:    map[string]apiv1.Contract{},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
			NodeGroups: map[string]apiv1.NodeGroup{
				"codification": {
					Nodes: []string{"codify-internal"},
				},
			},
		},
	}

	// The workitem is on an external node (outside the group).
	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wi-external",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "external-node",
		},
	}

	// The target node is inside the group but not entry-bound.
	internalNode := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "codify-internal", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image: "codify:latest",
			// No Entry binding — not entry-bound.
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, workitem, internalNode).
		WithStatusSubresource(workitem).
		Build()

	srv := NewOperatorServer(k8s)

	// Create context with source node identity.
	md := metadata.Pairs(
		"x-flow-node-id", "external-node",
		"x-flow-namespace", "default",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.SubmitResult(ctx, &flowv1.SubmitResultRequest{
		WorkitemId: "wi-external",
		Action:     &flowv1.SubmitResultRequest_Route{Route: &flowv1.RouteAction{Target: "codify-internal"}},
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), "GROUP_ROUTING_DENIED") {
		t.Fatalf("Expected GROUP_ROUTING_DENIED error, got: %v", err)
	}
}

func TestSubmitResult_GroupRoutingAllowed_RouteToEntryBoundGroupNode(t *testing.T) {
	scheme := newScheme()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {}},
			ExitContracts:    map[string]apiv1.Contract{},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
			NodeGroups: map[string]apiv1.NodeGroup{
				"codification": {
					Nodes: []string{"codify-entry"},
				},
			},
		},
	}

	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wi-external",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "external-node",
		},
	}

	// The target is inside the group and IS entry-bound.
	entryNode := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "codify-entry", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image: "codify:latest",
			Entry: "main",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, workitem, entryNode).
		WithStatusSubresource(workitem).
		Build()

	srv := NewOperatorServer(k8s)

	md := metadata.Pairs("x-flow-node-id", "external-node", "x-flow-namespace", "default")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := srv.SubmitResult(ctx, &flowv1.SubmitResultRequest{
		WorkitemId: "wi-external",
		Action:     &flowv1.SubmitResultRequest_Route{Route: &flowv1.RouteAction{Target: "codify-entry"}},
	})
	if err != nil {
		t.Fatalf("Expected route to entry-bound group node to succeed, got: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}
}

func TestSubmitResult_GroupRoutingAllowed_IntraGroupRouting(t *testing.T) {
	scheme := newScheme()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {}},
			ExitContracts:    map[string]apiv1.Contract{},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
			NodeGroups: map[string]apiv1.NodeGroup{
				"codification": {
					Nodes: []string{"codify-a", "codify-b"},
				},
			},
		},
	}

	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wi-internal",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "codify-a",
		},
	}

	nodeA := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "codify-a", Namespace: "default"},
		Spec:       apiv1.FoundryNodeSpec{Image: "codify-a:latest"},
	}

	nodeB := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "codify-b", Namespace: "default"},
		Spec:       apiv1.FoundryNodeSpec{Image: "codify-b:latest"},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, workitem, nodeA, nodeB).
		WithStatusSubresource(workitem).
		Build()

	srv := NewOperatorServer(k8s)

	// Source node is codify-a, target is codify-b — same group.
	md := metadata.Pairs("x-flow-node-id", "codify-a", "x-flow-namespace", "default")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := srv.SubmitResult(ctx, &flowv1.SubmitResultRequest{
		WorkitemId: "wi-internal",
		Action:     &flowv1.SubmitResultRequest_Route{Route: &flowv1.RouteAction{Target: "codify-b"}},
	})
	if err != nil {
		t.Fatalf("Expected intra-group routing to succeed, got: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}
}

func TestSubmitResult_GroupRoutingAllowed_NoGroups(t *testing.T) {
	scheme := newScheme()

	// Flow without any NodeGroups — routing should be unrestricted.
	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {}},
			ExitContracts:    map[string]apiv1.Contract{},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
		},
	}

	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wi-free",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "node-a",
		},
	}

	nodeB := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "node-b", Namespace: "default"},
		Spec:       apiv1.FoundryNodeSpec{Image: "node-b:latest"},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, workitem, nodeB).
		WithStatusSubresource(workitem).
		Build()

	srv := NewOperatorServer(k8s)

	md := metadata.Pairs("x-flow-node-id", "node-a", "x-flow-namespace", "default")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := srv.SubmitResult(ctx, &flowv1.SubmitResultRequest{
		WorkitemId: "wi-free",
		Action:     &flowv1.SubmitResultRequest_Route{Route: &flowv1.RouteAction{Target: "node-b"}},
	})
	if err != nil {
		t.Fatalf("Expected routing without groups to succeed, got: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}
}

func TestRouteChild_GroupRoutingDenied(t *testing.T) {
	scheme := newScheme()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {}},
			ExitContracts:    map[string]apiv1.Contract{},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
			NodeGroups: map[string]apiv1.NodeGroup{
				"codification": {
					Nodes: []string{"codify-internal"},
				},
			},
		},
	}

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:            phasePending,
			ParentWorkitemID: "parent-wi",
		},
	}

	// Internal node is not entry-bound.
	internalNode := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "codify-internal", Namespace: "default"},
		Spec:       apiv1.FoundryNodeSpec{Image: "codify:latest"},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, child, internalNode).
		WithStatusSubresource(child).
		Build()

	srv := NewOperatorServer(k8s)

	// Source is external-clerk which is NOT in the codification group.
	md := metadata.Pairs(
		"x-flow-workitem-id", "parent-wi",
		"x-flow-node-id", "external-clerk",
		"x-flow-namespace", "default",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: "child-wi",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO,
			Target: "codify-internal",
		},
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), "GROUP_ROUTING_DENIED") {
		t.Fatalf("Expected GROUP_ROUTING_DENIED error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ValidateChildAccess tests
// ---------------------------------------------------------------------------

func TestValidateChildAccess_Valid(t *testing.T) {
	scheme := newScheme()

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
			Labels:    map[string]string{"flow.gideas.io/parent": "parent-wi"},
		},
		Status: apiv1.WorkitemStatus{
			Phase:            phaseCompleted,
			ParentWorkitemID: "parent-wi",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(child).
		WithStatusSubresource(child).
		Build()

	srv := NewOperatorServer(k8s)

	resp, err := srv.ValidateChildAccess(nsCtx(), &flowv1.ValidateChildAccessRequest{
		ParentWorkitemId: "parent-wi",
		ChildWorkitemId:  "child-wi",
	})
	if err != nil {
		t.Fatalf("ValidateChildAccess() returned error: %v", err)
	}
	if !resp.GetValid() {
		t.Fatal("Expected valid=true")
	}
	if resp.GetPhase() != phaseCompleted {
		t.Fatalf("Expected phase Completed, got %s", resp.GetPhase())
	}
}

func TestValidateChildAccess_WrongParent(t *testing.T) {
	scheme := newScheme()

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-wi",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:            phaseCompleted,
			ParentWorkitemID: "actual-parent",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(child).
		WithStatusSubresource(child).
		Build()

	srv := NewOperatorServer(k8s)

	resp, err := srv.ValidateChildAccess(nsCtx(), &flowv1.ValidateChildAccessRequest{
		ParentWorkitemId: "wrong-parent",
		ChildWorkitemId:  "child-wi",
	})
	if err != nil {
		t.Fatalf("ValidateChildAccess() returned error: %v", err)
	}
	if resp.GetValid() {
		t.Fatal("Expected valid=false for wrong parent")
	}
	if resp.GetPhase() != phaseCompleted {
		t.Fatalf("Expected phase Completed, got %s", resp.GetPhase())
	}
}

func TestValidateChildAccess_ChildNotCompleted(t *testing.T) {
	phases := []string{"Pending", "Running", "Failed"}

	for _, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			scheme := newScheme()

			child := &apiv1.Workitem{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "child-wi",
					Namespace: "default",
				},
				Status: apiv1.WorkitemStatus{
					Phase:            phase,
					ParentWorkitemID: "parent-wi",
				},
			}

			k8s := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(child).
				WithStatusSubresource(child).
				Build()

			srv := NewOperatorServer(k8s)

			resp, err := srv.ValidateChildAccess(nsCtx(), &flowv1.ValidateChildAccessRequest{
				ParentWorkitemId: "parent-wi",
				ChildWorkitemId:  "child-wi",
			})
			if err != nil {
				t.Fatalf("ValidateChildAccess() returned error: %v", err)
			}
			if resp.GetValid() {
				t.Fatalf("Expected valid=false for phase %s", phase)
			}
			if resp.GetPhase() != phase {
				t.Fatalf("Expected phase %s, got %s", phase, resp.GetPhase())
			}
		})
	}
}

func TestValidateChildAccess_ChildNotFound(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	_, err := srv.ValidateChildAccess(nsCtx(), &flowv1.ValidateChildAccessRequest{
		ParentWorkitemId: "parent-wi",
		ChildWorkitemId:  "nonexistent-child",
	})
	assertGRPCCode(t, err, codes.NotFound)
}

func TestValidateChildAccess_MissingParentID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	_, err := srv.ValidateChildAccess(nsCtx(), &flowv1.ValidateChildAccessRequest{
		ChildWorkitemId: "child-wi",
	})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestValidateChildAccess_MissingChildID(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	_, err := srv.ValidateChildAccess(nsCtx(), &flowv1.ValidateChildAccessRequest{
		ParentWorkitemId: "parent-wi",
	})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

// ---------------------------------------------------------------------------
// Suspend timeout validation tests (SubmitResult step 3c)
// ---------------------------------------------------------------------------

func TestSubmitResult_Suspend_TimeoutExceedsMax(t *testing.T) {
	scheme := newScheme()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {}},
			ExitContracts:    map[string]apiv1.Contract{},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
			Suspension: &apiv1.SuspensionConfig{
				MaxSuspendTimeout: &metav1.Duration{Duration: 10 * time.Minute},
			},
		},
	}

	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wi-suspend",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "worker",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, workitem).
		WithStatusSubresource(workitem).
		Build()

	srv := NewOperatorServer(k8s)

	// 1h timeout exceeds 10m max.
	_, err := srv.SubmitResult(nsCtx(), &flowv1.SubmitResultRequest{
		WorkitemId: "wi-suspend",
		Action: &flowv1.SubmitResultRequest_Suspend{
			Suspend: &flowv1.SuspendAction{
				Timeout: durationpb.New(1 * time.Hour),
			},
		},
	})
	assertGRPCCode(t, err, codes.InvalidArgument)
	if !strings.Contains(err.Error(), "SUSPEND_TIMEOUT_EXCEEDED") {
		t.Fatalf("Expected SUSPEND_TIMEOUT_EXCEEDED error, got: %v", err)
	}
}

func TestSubmitResult_Suspend_NoExplicitTimeout_UsesDefault(t *testing.T) {
	scheme := newScheme()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {}},
			ExitContracts:    map[string]apiv1.Contract{},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
			Suspension: &apiv1.SuspensionConfig{
				DefaultSuspendTimeout: &metav1.Duration{Duration: 15 * time.Minute},
				MaxSuspendTimeout:     &metav1.Duration{Duration: 1 * time.Hour},
			},
		},
	}

	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wi-suspend",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "worker",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, workitem).
		WithStatusSubresource(workitem).
		Build()

	srv := NewOperatorServer(k8s)

	// No timeout specified — should apply default and succeed.
	resp, err := srv.SubmitResult(nsCtx(), &flowv1.SubmitResultRequest{
		WorkitemId: "wi-suspend",
		Action: &flowv1.SubmitResultRequest_Suspend{
			Suspend: &flowv1.SuspendAction{
				Condition: `children.all(c, c.phase == "Completed")`,
			},
		},
	})
	if err != nil {
		t.Fatalf("SubmitResult(suspend) returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}

	// Verify the workitem was updated with the resolved timeout.
	var updated apiv1.Workitem
	err = k8s.Get(context.Background(), nsName("wi-suspend"), &updated)
	if err != nil {
		t.Fatalf("Failed to get updated workitem: %v", err)
	}
	if updated.Status.Phase != phaseRouting {
		t.Fatalf("Expected phase Routing, got %s", updated.Status.Phase)
	}
	if updated.Status.RoutingInstruction == nil {
		t.Fatal("Expected routing instruction to be set")
	}
	if updated.Status.RoutingInstruction.Type != suspendType {
		t.Fatalf("Expected routing type 'suspend', got %s", updated.Status.RoutingInstruction.Type)
	}
	// The default timeout should have been applied by validateSuspendTimeout.
	expectedTimeout := (15 * time.Minute).String()
	if updated.Status.RoutingInstruction.SuspendTimeout != expectedTimeout {
		t.Fatalf("Expected SuspendTimeout=%s (default applied), got %q",
			expectedTimeout, updated.Status.RoutingInstruction.SuspendTimeout)
	}
}

func TestSubmitResult_Suspend_ValidTimeout_Accepted(t *testing.T) {
	scheme := newScheme()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {}},
			ExitContracts:    map[string]apiv1.Contract{},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
			Suspension: &apiv1.SuspensionConfig{
				MaxSuspendTimeout: &metav1.Duration{Duration: 1 * time.Hour},
			},
		},
	}

	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wi-suspend",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "worker",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, workitem).
		WithStatusSubresource(workitem).
		Build()

	srv := NewOperatorServer(k8s)

	// 30m is within the 1h max.
	resp, err := srv.SubmitResult(nsCtx(), &flowv1.SubmitResultRequest{
		WorkitemId: "wi-suspend",
		Action: &flowv1.SubmitResultRequest_Suspend{
			Suspend: &flowv1.SuspendAction{
				Timeout: durationpb.New(30 * time.Minute),
			},
		},
	})
	if err != nil {
		t.Fatalf("SubmitResult(suspend) returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}

	var updated apiv1.Workitem
	err = k8s.Get(context.Background(), nsName("wi-suspend"), &updated)
	if err != nil {
		t.Fatalf("Failed to get updated workitem: %v", err)
	}
	if updated.Status.RoutingInstruction.Type != suspendType {
		t.Fatalf("Expected routing type 'suspend', got %s", updated.Status.RoutingInstruction.Type)
	}
}

func TestSubmitResult_Suspend_NoSuspensionConfig_Accepted(t *testing.T) {
	scheme := newScheme()

	// Flow without SuspensionConfig — no validation.
	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts:   map[string]apiv1.Contract{"main": {}},
			ExitContracts:    map[string]apiv1.Contract{},
			GovernancePolicy: apiv1.GovernancePolicy{MaxVisits: 10},
		},
	}

	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wi-suspend",
			Namespace: "default",
		},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			CurrentAssignee: "worker",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, workitem).
		WithStatusSubresource(workitem).
		Build()

	srv := NewOperatorServer(k8s)

	resp, err := srv.SubmitResult(nsCtx(), &flowv1.SubmitResultRequest{
		WorkitemId: "wi-suspend",
		Action: &flowv1.SubmitResultRequest_Suspend{
			Suspend: &flowv1.SuspendAction{
				Condition: `children.all(c, c.phase == "Completed")`,
				Timeout:   durationpb.New(2 * time.Hour),
			},
		},
	})
	if err != nil {
		t.Fatalf("Expected suspend to succeed without SuspensionConfig, got: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("Expected Accepted=true")
	}
}

// ---------------------------------------------------------------------------
// ListSuspendedWorkitems tests
// ---------------------------------------------------------------------------

func TestListSuspendedWorkitems_ReturnsSuspendedWithMatchingCondition(t *testing.T) {
	scheme := newScheme()

	wi1 := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{Name: "wi-held-001", Namespace: "default"},
		Status: apiv1.WorkitemStatus{
			Phase:           "Suspended",
			ResumeCondition: `dispute_retired("pet-123")`,
		},
	}
	wi2 := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{Name: "wi-held-002", Namespace: "default"},
		Status: apiv1.WorkitemStatus{
			Phase:           "Suspended",
			ResumeCondition: `dispute_retired("pet-123")`,
		},
	}
	// This one should NOT match — different petition_id.
	wi3 := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{Name: "wi-other", Namespace: "default"},
		Status: apiv1.WorkitemStatus{
			Phase:           "Suspended",
			ResumeCondition: `dispute_retired("pet-999")`,
		},
	}
	// This one should NOT match — not Suspended.
	wi4 := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{Name: "wi-running", Namespace: "default"},
		Status: apiv1.WorkitemStatus{
			Phase:           "Running",
			ResumeCondition: `dispute_retired("pet-123")`,
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(wi1, wi2, wi3, wi4).
		Build()

	srv := NewOperatorServer(k8s)

	resp, err := srv.ListSuspendedWorkitems(nsCtx(), &flowv1.ListSuspendedWorkitemsRequest{
		ConditionContains: "pet-123",
	})
	if err != nil {
		t.Fatalf("ListSuspendedWorkitems() returned error: %v", err)
	}

	if len(resp.GetWorkitems()) != 2 {
		t.Fatalf("expected 2 workitems, got %d", len(resp.GetWorkitems()))
	}

	ids := make(map[string]bool)
	for _, wi := range resp.GetWorkitems() {
		ids[wi.GetWorkitemId()] = true
	}
	if !ids["wi-held-001"] || !ids["wi-held-002"] {
		t.Fatalf("expected wi-held-001 and wi-held-002, got %v", ids)
	}
}

func TestListSuspendedWorkitems_EmptyFilter_ReturnsError(t *testing.T) {
	scheme := newScheme()
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewOperatorServer(k8s)

	_, err := srv.ListSuspendedWorkitems(nsCtx(), &flowv1.ListSuspendedWorkitemsRequest{
		ConditionContains: "",
	})
	if err == nil {
		t.Fatal("expected error for empty condition_contains, got nil")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestListSuspendedWorkitems_NoMatches_ReturnsEmptyList(t *testing.T) {
	scheme := newScheme()

	wi1 := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{Name: "wi-suspended", Namespace: "default"},
		Status: apiv1.WorkitemStatus{
			Phase:           "Suspended",
			ResumeCondition: `dispute_retired("pet-999")`,
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(wi1).
		Build()

	srv := NewOperatorServer(k8s)

	resp, err := srv.ListSuspendedWorkitems(nsCtx(), &flowv1.ListSuspendedWorkitemsRequest{
		ConditionContains: "pet-123",
	})
	if err != nil {
		t.Fatalf("ListSuspendedWorkitems() returned error: %v", err)
	}
	if len(resp.GetWorkitems()) != 0 {
		t.Fatalf("expected 0 workitems, got %d", len(resp.GetWorkitems()))
	}
}
