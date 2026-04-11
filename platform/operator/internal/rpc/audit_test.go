package rpc

import (
	"context"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	apiv1 "github.com/gideas/flow/operator/api/v1"
	"github.com/gideas/flow/pkg/eventbus"
	"google.golang.org/grpc/metadata"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// spyPublisher implements eventbus.Publisher and captures published requests
// for test assertions.
type spyPublisher struct {
	mu     sync.Mutex
	events []*flowv1.PublishRequest
}

func (s *spyPublisher) Publish(_ context.Context, req *flowv1.PublishRequest) (*flowv1.PublishResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, req)
	return &flowv1.PublishResponse{Acknowledged: true}, nil
}

func (s *spyPublisher) last() *flowv1.PublishRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		return nil
	}
	return s.events[len(s.events)-1]
}

func (s *spyPublisher) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// newTestAuditor creates a spyPublisher and an AsyncPublisher for tests.
// The returned stop function must be called to drain the publisher.
func newTestAuditor() (*spyPublisher, *eventbus.AsyncPublisher, func()) {
	spy := &spyPublisher{}
	pub := eventbus.NewAsyncPublisherFromPublisher(spy)
	return spy, pub, func() { pub.Stop() }
}

func TestAudit_SubmitResult(t *testing.T) {
	scheme := newScheme()
	spy, pub, stop := newTestAuditor()
	defer stop()

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

	md := metadata.Pairs("x-flow-namespace", "default")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.SubmitResult(ctx, &flowv1.SubmitResultRequest{
		WorkitemId: "wi-audit-1",
		Action:     &flowv1.SubmitResultRequest_Complete{Complete: &flowv1.CompleteAction{}},
	})
	if err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}

	// Stop the publisher to flush buffered events before asserting.
	stop()

	if spy.count() != 1 {
		t.Fatalf("expected 1 audit event, got %d", spy.count())
	}
	last := spy.last()
	if last.GetChannel() != "audit" {
		t.Fatalf("expected AUDIT channel, got %v", last.GetChannel())
	}
	if last.GetEvent().GetEventType() != "audit.workitem.routing_submitted" {
		t.Fatalf("expected event_type audit.workitem.routing_submitted, got %q", last.GetEvent().GetEventType())
	}
}

func TestAudit_CreateWorkitem(t *testing.T) {
	scheme := newScheme()
	spy, pub, stop := newTestAuditor()
	defer stop()

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
		"x-flow-namespace", "default",
		"x-flow-node-id", "forge-node",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := srv.CreateWorkitem(ctx, &flowv1.CreateWorkitemRequest{})
	if err != nil {
		t.Fatalf("CreateWorkitem: %v", err)
	}

	stop()

	if spy.count() != 1 {
		t.Fatalf("expected 1 audit event, got %d", spy.count())
	}
	last := spy.last()
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

	md := metadata.Pairs("x-flow-namespace", "default")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.SubmitResult(ctx, &flowv1.SubmitResultRequest{
		WorkitemId: "wi-nil-pub",
		Action:     &flowv1.SubmitResultRequest_Complete{Complete: &flowv1.CompleteAction{}},
	})
	if err != nil {
		t.Fatalf("SubmitResult should succeed with nil publisher: %v", err)
	}
}

func TestAudit_CreateChildWorkitem(t *testing.T) {
	scheme := newScheme()
	spy, pub, stop := newTestAuditor()
	defer stop()

	timeNow = func() metav1.Time {
		return metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC))
	}
	defer func() { timeNow = func() metav1.Time { return metav1.Now() } }()

	parent := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{Name: "parent-audit", Namespace: "default"},
		Status:     apiv1.WorkitemStatus{Phase: "Running"},
	}
	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(parent).
		WithStatusSubresource(parent, &apiv1.Workitem{}).
		Build()

	srv := NewOperatorServer(k8s)
	srv.Auditor = pub

	md := metadata.Pairs(
		"x-flow-namespace", "default",
		"x-flow-node-id", "clerk",
		"x-flow-workitem-id", "parent-audit",
		"x-flow-capabilities", "CREATE:workitem/child",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := srv.CreateChildWorkitem(ctx, &flowv1.CreateChildWorkitemRequest{})
	if err != nil {
		t.Fatalf("CreateChildWorkitem: %v", err)
	}

	stop()

	if spy.count() != 1 {
		t.Fatalf("expected 1 audit event, got %d", spy.count())
	}
	evt := spy.last().GetEvent()
	if evt.GetEventType() != "audit.workitem.child_created" {
		t.Fatalf("expected audit.workitem.child_created, got %q", evt.GetEventType())
	}
	if evt.GetAttributes()["resource_id"] != resp.GetChildWorkitemId() {
		t.Fatalf("expected resource_id=%q, got %q", resp.GetChildWorkitemId(), evt.GetAttributes()["resource_id"])
	}
	if evt.GetAttributes()["parent_workitem_id"] != "parent-audit" {
		t.Fatalf("expected parent_workitem_id=parent-audit, got %q", evt.GetAttributes()["parent_workitem_id"])
	}
}

func TestAudit_RouteChild(t *testing.T) {
	scheme := newScheme()
	spy, pub, stop := newTestAuditor()
	defer stop()

	child := &apiv1.Workitem{
		ObjectMeta: metav1.ObjectMeta{Name: "child-audit", Namespace: "default"},
		Status: apiv1.WorkitemStatus{
			Phase:            "Pending",
			ParentWorkitemID: "parent-audit",
		},
	}
	targetNode := &apiv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "target-node", Namespace: "default"},
		Spec:       apiv1.FoundryNodeSpec{Image: "target:latest"},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(child, targetNode).
		WithStatusSubresource(child).
		Build()

	srv := NewOperatorServer(k8s)
	srv.Auditor = pub

	md := metadata.Pairs(
		"x-flow-workitem-id", "parent-audit",
		"x-flow-namespace", "default",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := srv.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: "child-audit",
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO,
			Target: "target-node",
		},
	})
	if err != nil {
		t.Fatalf("RouteChild: %v", err)
	}

	stop()

	if spy.count() != 1 {
		t.Fatalf("expected 1 audit event, got %d", spy.count())
	}
	evt := spy.last().GetEvent()
	if evt.GetEventType() != "audit.workitem.child_routed" {
		t.Fatalf("expected audit.workitem.child_routed, got %q", evt.GetEventType())
	}
	if evt.GetAttributes()["resource_id"] != "child-audit" {
		t.Fatalf("expected resource_id=child-audit, got %q", evt.GetAttributes()["resource_id"])
	}
}
