package rpc

import (
	"context"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	apiv1 "github.com/gideas/flow/operator/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// mockAuditPublisher captures published audit events.
type mockAuditPublisher struct {
	mu     sync.Mutex
	events []*flowv1.PublishRequest
}

func (m *mockAuditPublisher) Publish(_ context.Context, req *flowv1.PublishRequest, _ ...grpc.CallOption) (*flowv1.PublishResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, req)
	return &flowv1.PublishResponse{Acknowledged: true}, nil
}

func (m *mockAuditPublisher) last() *flowv1.PublishRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events) == 0 {
		return nil
	}
	return m.events[len(m.events)-1]
}

func (m *mockAuditPublisher) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events)
}

func TestAudit_SubmitResult(t *testing.T) {
	scheme := newScheme()
	pub := &mockAuditPublisher{}

	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{Name: "wi-audit-1", Namespace: "default"},
		Status:     apiv1.WorkitemStatus{Phase: "Running"},
	}
	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(workitem).
		WithStatusSubresource(workitem).
		Build()

	srv := NewOperatorServer(k8s)
	srv.Auditor = pub

	_, err := srv.SubmitResult(context.Background(), &flowv1.SubmitResultRequest{
		WorkitemId: "wi-audit-1",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type: flowv1.RoutingType_ROUTING_TYPE_COMPLETE,
		},
	})
	if err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}

	if pub.count() != 1 {
		t.Fatalf("expected 1 audit event, got %d", pub.count())
	}
	last := pub.last()
	if last.GetChannel() != flowv1.EventChannel_EVENT_CHANNEL_AUDIT {
		t.Fatalf("expected AUDIT channel, got %v", last.GetChannel())
	}
	if last.GetEvent().GetEventType() != "audit.workitem.routing_submitted" {
		t.Fatalf("expected event_type audit.workitem.routing_submitted, got %q", last.GetEvent().GetEventType())
	}
}

func TestAudit_CreateWorkitem(t *testing.T) {
	scheme := newScheme()
	pub := &mockAuditPublisher{}

	// Freeze time for deterministic workitem names.
	timeNow = func() metav1.Time {
		return metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC))
	}
	defer func() { timeNow = func() metav1.Time { return metav1.Now() } }()

	flow := &apiv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "flow-1", Namespace: "default"},
		Spec: apiv1.FoundryFlowSpec{
			EntryContracts: map[string]apiv1.Contract{
				"main-entry": {"txt": {"reviewed"}},
			},
		},
	}
	node := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "forge-node", Namespace: "default"},
		Spec: apiv1.FoundryNodeSpec{
			Entry: "main-entry",
		},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(flow, node).
		WithStatusSubresource(&apiv1.Workitem{}).
		Build()

	srv := NewOperatorServer(k8s)
	srv.Auditor = pub

	md := metadata.Pairs(
		"x-flow-flow-id", "flow-1",
		"x-flow-node-id", "forge-node",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := srv.CreateWorkitem(ctx, &flowv1.CreateWorkitemRequest{})
	if err != nil {
		t.Fatalf("CreateWorkitem: %v", err)
	}

	if pub.count() != 1 {
		t.Fatalf("expected 1 audit event, got %d", pub.count())
	}
	last := pub.last()
	evt := last.GetEvent()
	if evt.GetEventType() != "audit.workitem.created" {
		t.Fatalf("expected event_type audit.workitem.created, got %q", evt.GetEventType())
	}
	if evt.GetAttributes()["resource_id"] != resp.GetWorkitemId() {
		t.Fatalf("expected resource_id=%q, got %q", resp.GetWorkitemId(), evt.GetAttributes()["resource_id"])
	}
}

func TestAudit_NilPublisher(t *testing.T) {
	scheme := newScheme()

	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{Name: "wi-nil-pub", Namespace: "default"},
		Status:     apiv1.WorkitemStatus{Phase: "Running"},
	}
	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(workitem).
		WithStatusSubresource(workitem).
		Build()

	srv := NewOperatorServer(k8s) // No auditor set.

	_, err := srv.SubmitResult(context.Background(), &flowv1.SubmitResultRequest{
		WorkitemId: "wi-nil-pub",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type: flowv1.RoutingType_ROUTING_TYPE_COMPLETE,
		},
	})
	if err != nil {
		t.Fatalf("SubmitResult should succeed with nil publisher: %v", err)
	}
}

func TestAudit_ExportWorkitem(t *testing.T) {
	scheme := newScheme()
	pub := &mockAuditPublisher{}

	workitem := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{Name: "wi-export", Namespace: "default"},
		Status:     apiv1.WorkitemStatus{Phase: "Completed"},
	}
	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(workitem).
		WithStatusSubresource(workitem).
		Build()

	srv := NewOperatorServer(k8s)
	srv.Auditor = pub

	_, err := srv.ExportWorkitem(context.Background(), &flowv1.ExportWorkitemRequest{
		WorkitemId: "wi-export",
	})
	if err != nil {
		t.Fatalf("ExportWorkitem: %v", err)
	}

	if pub.count() != 1 {
		t.Fatalf("expected 1 audit event, got %d", pub.count())
	}
	evt := pub.last().GetEvent()
	if evt.GetEventType() != "audit.workitem.exported" {
		t.Fatalf("expected audit.workitem.exported, got %q", evt.GetEventType())
	}
}

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = apiv1.AddToScheme(s)
	return s
}
