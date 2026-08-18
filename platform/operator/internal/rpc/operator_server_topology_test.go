package rpc

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	apiv1 "github.com/foundry/flow/operator/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetFlowTopology_HappyPath(t *testing.T) {
	scheme := newScheme()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "haiku-flow", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts: map[string]apiv1.Contract{"main": {"haiku": nil}},
			ExitContracts: map[string]apiv1.Contract{
				"governed": {"haiku": {"review", "approval"}},
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
				{Name: "appraisal", Target: "appraisal"},
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
			Capabilities: []string{"READ:artefact", "ATTEST:artefact/haiku/appraisal", "WRITE:feedback/new"},
		},
	}

	appraisalNode := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "appraisal", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Image:        "appraisal:latest",
			Capabilities: []string{"READ:artefact", "ATTEST:artefact/haiku/appraisal", "WRITE:feedback/new"},
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, sortNode, quenchNode, appraisalNode).
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
	if _, ok := resp.GetNodes()["appraisal"]; !ok {
		t.Fatal("Expected appraisal in nodes map")
	}

	// Verify exit contract.
	if len(resp.GetExitContract()) != 1 {
		t.Fatalf("Expected 1 exit contract kind, got %d", len(resp.GetExitContract()))
	}
	haikuStamps := resp.GetExitContract()["haiku"]
	if haikuStamps == nil {
		t.Fatal("Expected haiku in exit contract")
	}
	if len(haikuStamps.GetStamps()) != 2 {
		t.Fatalf("Expected 2 stamps in haiku exit contract, got %d", len(haikuStamps.GetStamps()))
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
				"STAMP:artefact/doc/review",
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
