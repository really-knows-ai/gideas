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
