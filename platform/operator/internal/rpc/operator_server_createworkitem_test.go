package rpc

import (
	"context"
	"strings"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	apiv1 "github.com/foundry/flow/operator/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

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

	// Verify labels — no flow.foundry.io/flow label, only creator.
	if _, hasFlowLabel := created.Labels["flow.foundry.io/flow"]; hasFlowLabel {
		t.Fatal("Expected no flow.foundry.io/flow label on workitem")
	}
	if created.Labels["flow.foundry.io/creator"] != "intake" {
		t.Fatalf("Expected creator label 'intake', got %s", created.Labels["flow.foundry.io/creator"])
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
