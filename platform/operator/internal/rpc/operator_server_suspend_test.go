package rpc

import (
	"context"
	"strings"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	apiv1 "github.com/foundry/flow/operator/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/durationpb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

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
