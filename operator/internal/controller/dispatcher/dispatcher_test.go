package dispatcher

import (
	"context"
	"fmt"
	"testing"

	flowv1gen "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// ---------------------------------------------------------------------------
// Fake SidecarServiceClient for testing
// ---------------------------------------------------------------------------

type fakeSidecarClient struct {
	lastReq   *flowv1gen.AssignWorkRequest
	returnAck *flowv1gen.Ack
	returnErr error
}

func (f *fakeSidecarClient) Heartbeat(_ context.Context, _ *flowv1gen.HeartbeatRequest, _ ...grpc.CallOption) (*flowv1gen.HeartbeatResponse, error) {
	return nil, fmt.Errorf("not implemented in test")
}

func (f *fakeSidecarClient) AssignWork(_ context.Context, req *flowv1gen.AssignWorkRequest, _ ...grpc.CallOption) (*flowv1gen.Ack, error) {
	f.lastReq = req
	if f.returnErr != nil {
		return nil, f.returnErr
	}
	return f.returnAck, nil
}

func (f *fakeSidecarClient) PauseTimer(_ context.Context, _ *flowv1gen.PauseTimerRequest, _ ...grpc.CallOption) (*flowv1gen.PauseTimerResponse, error) {
	return nil, fmt.Errorf("not implemented in test")
}

func (f *fakeSidecarClient) ResumeTimer(_ context.Context, _ *flowv1gen.ResumeTimerRequest, _ ...grpc.CallOption) (*flowv1gen.ResumeTimerResponse, error) {
	return nil, fmt.Errorf("not implemented in test")
}

// newDialFunc returns a DialFunc that always returns the given fake client.
func newDialFunc(fakeClient *fakeSidecarClient) func(string) (flowv1gen.SidecarServiceClient, func() error, error) {
	return func(addr string) (flowv1gen.SidecarServiceClient, func() error, error) {
		return fakeClient, func() error { return nil }, nil
	}
}

// ---------------------------------------------------------------------------
// Helper: create a ready Pod
// ---------------------------------------------------------------------------

func readyPod(name, nodeName, podIP string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				LabelNodeName: nodeName,
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: podIP,
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodReady,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}
}

func notReadyPod(name, nodeName, podIP string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				LabelNodeName: nodeName,
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			PodIP: podIP,
		},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestAssign_Success(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	pod := readyPod("step2-pod-abc", "step-2", "10.0.0.5")
	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pod).
		Build()

	fakeClient := &fakeSidecarClient{
		returnAck: &flowv1gen.Ack{Accepted: true, Message: "ok"},
	}

	d := &Dispatcher{
		K8sClient:   k8s,
		Namespace:   "default",
		SidecarPort: 50051,
		DialFunc:    newDialFunc(fakeClient),
	}

	result, err := d.Assign(context.Background(), "step-2", "flow-1", "wi-1")
	if err != nil {
		t.Fatalf("Assign() error: %v", err)
	}

	if result.PodName != "step2-pod-abc" {
		t.Errorf("expected pod name step2-pod-abc, got %s", result.PodName)
	}
	if result.PodIP != "10.0.0.5" {
		t.Errorf("expected pod IP 10.0.0.5, got %s", result.PodIP)
	}

	// Verify the gRPC call was made with correct context.
	if fakeClient.lastReq == nil {
		t.Fatal("no AssignWork request sent")
	}
	if fakeClient.lastReq.GetContext().GetWorkitemId() != "wi-1" {
		t.Errorf("expected workitem_id=wi-1, got %s", fakeClient.lastReq.GetContext().GetWorkitemId())
	}
	if fakeClient.lastReq.GetContext().GetFlowId() != "flow-1" {
		t.Errorf("expected flow_id=flow-1, got %s", fakeClient.lastReq.GetContext().GetFlowId())
	}
	if fakeClient.lastReq.GetContext().GetNodeId() != "step-2" {
		t.Errorf("expected node_id=step-2, got %s", fakeClient.lastReq.GetContext().GetNodeId())
	}
}

func TestAssign_NoReadyPods(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	pod := notReadyPod("step2-pod-pending", "step-2", "10.0.0.6")
	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pod).
		Build()

	d := New(k8s, "default")

	_, err := d.Assign(context.Background(), "step-2", "flow-1", "wi-2")
	if err == nil {
		t.Fatal("expected error when no ready pods")
	}
}

func TestAssign_NoPods(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	d := New(k8s, "default")

	_, err := d.Assign(context.Background(), "step-2", "flow-1", "wi-3")
	if err == nil {
		t.Fatal("expected error when no pods at all")
	}
}

func TestAssign_PodRejected(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	pod := readyPod("step2-pod-xyz", "step-2", "10.0.0.7")
	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pod).
		Build()

	fakeClient := &fakeSidecarClient{
		returnAck: &flowv1gen.Ack{Accepted: false, Message: "busy"},
	}

	d := &Dispatcher{
		K8sClient:   k8s,
		Namespace:   "default",
		SidecarPort: 50051,
		DialFunc:    newDialFunc(fakeClient),
	}

	_, err := d.Assign(context.Background(), "step-2", "flow-1", "wi-4")
	if err == nil {
		t.Fatal("expected error when pod rejects assignment")
	}
}

func TestAssign_DialFailure(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	pod := readyPod("step2-pod-dial", "step-2", "10.0.0.8")
	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pod).
		Build()

	d := &Dispatcher{
		K8sClient:   k8s,
		Namespace:   "default",
		SidecarPort: 50051,
		DialFunc: func(addr string) (flowv1gen.SidecarServiceClient, func() error, error) {
			return nil, nil, fmt.Errorf("connection refused")
		},
	}

	_, err := d.Assign(context.Background(), "step-2", "flow-1", "wi-5")
	if err == nil {
		t.Fatal("expected error on dial failure")
	}
}

func TestAssign_gRPCError(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	pod := readyPod("step2-pod-grpc", "step-2", "10.0.0.9")
	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pod).
		Build()

	fakeClient := &fakeSidecarClient{
		returnErr: fmt.Errorf("sidecar unavailable"),
	}

	d := &Dispatcher{
		K8sClient:   k8s,
		Namespace:   "default",
		SidecarPort: 50051,
		DialFunc:    newDialFunc(fakeClient),
	}

	_, err := d.Assign(context.Background(), "step-2", "flow-1", "wi-6")
	if err == nil {
		t.Fatal("expected error on gRPC failure")
	}
}

func TestDiscoverReadyPods_FiltersCorrectly(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	ready1 := readyPod("pod-ready-1", "step-2", "10.0.0.1")
	ready2 := readyPod("pod-ready-2", "step-2", "10.0.0.2")
	pending := notReadyPod("pod-pending", "step-2", "10.0.0.3")
	otherNode := readyPod("pod-other", "step-3", "10.0.0.4")

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ready1, ready2, pending, otherNode).
		Build()

	d := New(k8s, "default")

	pods, err := d.discoverReadyPods(context.Background(), "step-2")
	if err != nil {
		t.Fatalf("discoverReadyPods() error: %v", err)
	}

	if len(pods) != 2 {
		t.Fatalf("expected 2 ready pods for step-2, got %d", len(pods))
	}

	// Verify they're the right pods.
	names := map[string]bool{}
	for _, p := range pods {
		names[p.Name] = true
	}
	if !names["pod-ready-1"] || !names["pod-ready-2"] {
		t.Fatalf("unexpected pods: %v", names)
	}
}

func TestIsPodReady(t *testing.T) {
	tests := []struct {
		name     string
		pod      corev1.Pod
		expected bool
	}{
		{
			name: "ready",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodReady, Status: corev1.ConditionTrue},
					},
				},
			},
			expected: true,
		},
		{
			name: "not ready",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodReady, Status: corev1.ConditionFalse},
					},
				},
			},
			expected: false,
		},
		{
			name: "no conditions",
			pod: corev1.Pod{
				Status: corev1.PodStatus{},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPodReady(tt.pod); got != tt.expected {
				t.Errorf("isPodReady() = %v, want %v", got, tt.expected)
			}
		})
	}
}
