package api

import (
	"context"
	"errors"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
)

// ─── Mock SPDYForwarder ────────────────────────────────────────────────────

// mockSPDYForwarder implements SPDYForwarder for testing without a real cluster.
type mockSPDYForwarder struct {
	mu       sync.Mutex
	forwards map[string]int // key -> localPort (live forwards)
	nextPort int
	err      error // if set, all ForwardPod calls fail
}

func newMockSPDYForwarder() *mockSPDYForwarder {
	return &mockSPDYForwarder{
		forwards: make(map[string]int),
		nextPort: 10000,
	}
}

func (m *mockSPDYForwarder) ForwardPod(ctx context.Context, namespace, podName string, remotePort int) (localPort int, stop func(), err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.err != nil {
		return 0, nil, m.err
	}

	key := namespace + "/" + podName
	m.nextPort++
	localPort = m.nextPort
	m.forwards[key] = localPort

	stopped := false
	stop = func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if !stopped {
			stopped = true
			delete(m.forwards, key)
		}
	}

	return localPort, stop, nil
}

// activeCount returns the number of active mock forwards.
func (m *mockSPDYForwarder) activeCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.forwards)
}

// ─── Test Helpers ──────────────────────────────────────────────────────────

// makePod creates a pod with the given phase, ready condition, and IP.
func makePod(name, namespace, phase string, ready bool, podIP string, labels map[string]string) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPhase(phase),
			PodIP: podIP,
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodReady,
					Status: condStatus(ready),
				},
			},
		},
	}
	return p
}

func condStatus(ready bool) corev1.ConditionStatus {
	if ready {
		return corev1.ConditionTrue
	}
	return corev1.ConditionFalse
}

// ─── T1: FindReadyPod filters correctly — Running+Ready+PodIP ──────────────

func TestFindReadyPod_Found(t *testing.T) {
	pod := makePod("archivist-0", "system-ns", "Running", true, "10.0.0.1", map[string]string{"app": "archivist"})
	fakeClient := k8sfake.NewSimpleClientset(pod)
	mgr := NewPortForwardManager(nil, fakeClient, newMockSPDYForwarder())

	name, found, err := mgr.FindReadyPod(context.Background(), "system-ns", "app=archivist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if name != "archivist-0" {
		t.Errorf("expected pod name archivist-0, got %q", name)
	}
}

// ─── T2: FindReadyPod filters correctly — Running but not Ready ────────────

func TestFindReadyPod_NotReady(t *testing.T) {
	pod := makePod("archivist-0", "system-ns", "Running", false, "10.0.0.1", map[string]string{"app": "archivist"})
	fakeClient := k8sfake.NewSimpleClientset(pod)
	mgr := NewPortForwardManager(nil, fakeClient, newMockSPDYForwarder())

	_, found, err := mgr.FindReadyPod(context.Background(),"system-ns", "app=archivist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false for not-ready pod")
	}
}

// ─── T3: FindReadyPod filters correctly — Not Running ──────────────────────

func TestFindReadyPod_NotRunning(t *testing.T) {
	pod := makePod("archivist-0", "system-ns", "Pending", true, "10.0.0.1", map[string]string{"app": "archivist"})
	fakeClient := k8sfake.NewSimpleClientset(pod)
	mgr := NewPortForwardManager(nil, fakeClient, newMockSPDYForwarder())

	_, found, err := mgr.FindReadyPod(context.Background(),"system-ns", "app=archivist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false for pending pod")
	}
}

// ─── T4: FindReadyPod filters correctly — No PodIP ─────────────────────────

func TestFindReadyPod_NoPodIP(t *testing.T) {
	pod := makePod("archivist-0", "system-ns", "Running", true, "", map[string]string{"app": "archivist"})
	fakeClient := k8sfake.NewSimpleClientset(pod)
	mgr := NewPortForwardManager(nil, fakeClient, newMockSPDYForwarder())

	_, found, err := mgr.FindReadyPod(context.Background(),"system-ns", "app=archivist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false for pod with no IP")
	}
}

// ─── T5: FindReadyPod returns first match when multiple qualify ────────────

func TestFindReadyPod_FirstMatch(t *testing.T) {
	fakeClient := k8sfake.NewSimpleClientset(
		makePod("archivist-b", "system-ns", "Running", true, "10.0.0.2", map[string]string{"app": "archivist"}),
		makePod("archivist-a", "system-ns", "Running", true, "10.0.0.1", map[string]string{"app": "archivist"}),
	)
	mgr := NewPortForwardManager(nil, fakeClient, newMockSPDYForwarder())

	name, found, err := mgr.FindReadyPod(context.Background(), "system-ns", "app=archivist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	// Fake clientset iterates in alphabetical order by default
	if name != "archivist-a" && name != "archivist-b" {
		t.Errorf("expected one of the archivist pods, got %q", name)
	}
}

// ─── T6: FindReadyPod returns not found when no pods match label ───────────

func TestFindReadyPod_NoMatch(t *testing.T) {
	pod := makePod("other", "system-ns", "Running", true, "10.0.0.1", map[string]string{"app": "other"})
	fakeClient := k8sfake.NewSimpleClientset(pod)
	mgr := NewPortForwardManager(nil, fakeClient, newMockSPDYForwarder())

	_, found, err := mgr.FindReadyPod(context.Background(),"system-ns", "app=archivist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false for non-matching label")
	}
}

// ─── T7: FindReadyPod propagates API error ─────────────────────────────────

// errorPodsClient wraps a fake Clientset but returns errors for Pods() calls.
type errorPodsClient struct {
	*k8sfake.Clientset
	err error
}

func (c *errorPodsClient) CoreV1() corev1client.CoreV1Interface {
	real := c.Clientset.CoreV1()
	return &errorCoreV1Pods{CoreV1Interface: real, err: c.err}
}

type errorCoreV1Pods struct {
	corev1client.CoreV1Interface
	err error
}

func (c *errorCoreV1Pods) Pods(namespace string) corev1client.PodInterface {
	return &errorPodInterface{err: c.err}
}

type errorPodInterface struct {
	corev1client.PodInterface
	err error
}

func (c *errorPodInterface) List(ctx context.Context, opts metav1.ListOptions) (*corev1.PodList, error) {
	return nil, c.err
}

func TestFindReadyPod_APIFrror(t *testing.T) {
	errorClient := &errorPodsClient{
		Clientset: k8sfake.NewSimpleClientset(),
		err:       errors.New("permission denied"),
	}
	mgr := NewPortForwardManager(nil, errorClient, newMockSPDYForwarder())

	_, _, err := mgr.FindReadyPod(context.Background(),"system-ns", "app=archivist")
	if err == nil {
		t.Fatal("expected error from API")
	}
}

// ─── T8: ForwardPod creates and tracks a forward (mock SPDY) ───────────────

func TestForwardPod_CreatesAndTracks(t *testing.T) {
	mockSPDY := newMockSPDYForwarder()
	mgr := NewPortForwardManager(nil, nil, mockSPDY)

	ctx := context.Background()
	forwardID, localPort, err := mgr.ForwardPod(ctx, "ns", "pod", 8080)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedID := "ns/pod:8080"
	if forwardID != expectedID {
		t.Errorf("expected forwardID %q, got %q", expectedID, forwardID)
	}
	if localPort == 0 {
		t.Error("expected non-zero local port")
	}
	if len(mgr.forwards) != 1 {
		t.Errorf("expected 1 forward, got %d", len(mgr.forwards))
	}
}

// ─── T9: ForwardPod is idempotent for the same target ──────────────────────

func TestForwardPod_Idempotent(t *testing.T) {
	mockSPDY := newMockSPDYForwarder()
	mgr := NewPortForwardManager(nil, nil, mockSPDY)

	ctx := context.Background()
	fid1, port1, err := mgr.ForwardPod(ctx, "ns", "pod", 8080)
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}

	fid2, port2, err := mgr.ForwardPod(ctx, "ns", "pod", 8080)
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}

	if fid1 != fid2 {
		t.Errorf("expected same forwardID, got %q vs %q", fid1, fid2)
	}
	if port1 != port2 {
		t.Errorf("expected same localPort, got %d vs %d", port1, port2)
	}
	if len(mgr.forwards) != 1 {
		t.Errorf("expected 1 forward (idempotent), got %d", len(mgr.forwards))
	}
}

// ─── T10: ForwardPod returns different ports for different targets ─────────

func TestForwardPod_DifferentTargets(t *testing.T) {
	mockSPDY := newMockSPDYForwarder()
	mgr := NewPortForwardManager(nil, nil, mockSPDY)

	ctx := context.Background()
	fid1, port1, _ := mgr.ForwardPod(ctx, "ns", "pod-a", 8080)
	fid2, port2, _ := mgr.ForwardPod(ctx, "ns", "pod-b", 8080)

	if fid1 == fid2 {
		t.Error("expected different forwardIDs for different targets")
	}
	if port1 == port2 {
		t.Error("expected different local ports for different targets")
	}
}

// ─── T11: Close(forwardID) closes the forward ──────────────────────────────

func TestForwardPod_Close(t *testing.T) {
	mockSPDY := newMockSPDYForwarder()
	mgr := NewPortForwardManager(nil, nil, mockSPDY)

	ctx := context.Background()
	fid, _, _ := mgr.ForwardPod(ctx, "ns", "pod", 8080)

	if err := mgr.Close(fid); err != nil {
		t.Fatalf("unexpected error on close: %v", err)
	}

	if len(mgr.forwards) != 0 {
		t.Errorf("expected 0 forwards after close, got %d", len(mgr.forwards))
	}

	// Second close should return error
	if err := mgr.Close(fid); err == nil {
		t.Error("expected error on closing non-existent forward")
	}
}

// ─── T12: CloseAll clears all forwards ─────────────────────────────────────

func TestCloseAll_ClearsAll(t *testing.T) {
	mockSPDY := newMockSPDYForwarder()
	mgr := NewPortForwardManager(nil, nil, mockSPDY)

	ctx := context.Background()
	mgr.ForwardPod(ctx, "ns", "pod-a", 8080)
	mgr.ForwardPod(ctx, "ns", "pod-b", 9090)
	mgr.ForwardPod(ctx, "ns", "pod-c", 3000)

	if err := mgr.CloseAll(); err != nil {
		t.Fatalf("unexpected error on CloseAll: %v", err)
	}

	if len(mgr.forwards) != 0 {
		t.Errorf("expected 0 forwards after CloseAll, got %d", len(mgr.forwards))
	}
}

// ─── T13: CloseHITLForward clears only the HITL forward ────────────────────

func TestCloseHITLForward_ClearsOnlyHITL(t *testing.T) {
	mockSPDY := newMockSPDYForwarder()
	mgr := NewPortForwardManager(nil, nil, mockSPDY)

	ctx := context.Background()
	fid1, _, _ := mgr.ForwardPod(ctx, "ns", "pod-a", 8080)
	fid2, _, _ := mgr.ForwardPod(ctx, "ns", "pod-b", 9090)

	// Set HITL forward to first
	if err := mgr.SetHITLForward("ns", "pod-a", 8080); err != nil {
		t.Fatalf("SetHITLForward: %v", err)
	}

	// Close HITL
	if err := mgr.CloseHITLForward(); err != nil {
		t.Fatalf("CloseHITLForward: %v", err)
	}

	if len(mgr.forwards) != 1 {
		t.Errorf("expected 1 forward after HITL close, got %d", len(mgr.forwards))
	}

	// The remaining forward should be the non-HITL one
	if _, ok := mgr.forwards[fid2]; !ok {
		t.Errorf("expected non-HITL forward %q to remain", fid2)
	}
	if _, ok := mgr.forwards[fid1]; ok {
		t.Errorf("expected HITL forward %q to be removed", fid1)
	}
}

// ─── T14: SetHITLForward closes previous HITL forward ──────────────────────

func TestSetHITLForward_ClosesPrevious(t *testing.T) {
	mockSPDY := newMockSPDYForwarder()
	mgr := NewPortForwardManager(nil, nil, mockSPDY)

	ctx := context.Background()
	fid1, _, _ := mgr.ForwardPod(ctx, "ns", "pod-a", 8080)
	fid2, _, _ := mgr.ForwardPod(ctx, "ns", "pod-b", 9090)

	// Set HITL to first
	if err := mgr.SetHITLForward("ns", "pod-a", 8080); err != nil {
		t.Fatalf("first SetHITLForward: %v", err)
	}

	// Set HITL to second — should close the first
	if err := mgr.SetHITLForward("ns", "pod-b", 9090); err != nil {
		t.Fatalf("second SetHITLForward: %v", err)
	}

	// Only the second should remain as HITL; first should be closed
	if _, ok := mgr.forwards[fid1]; ok {
		t.Error("expected first forward to be closed when HITL switches")
	}
	if _, ok := mgr.forwards[fid2]; !ok {
		t.Error("expected second forward to still exist")
	}

	// hitlKey should be the second
	mgr.mu.Lock()
	if mgr.hitlKey != fid2 {
		t.Errorf("expected hitlKey %q, got %q", fid2, mgr.hitlKey)
	}
	mgr.mu.Unlock()
}

// ─── T15: ActiveForwards returns correct keys ──────────────────────────────

func TestActiveForwards_ReturnsKeys(t *testing.T) {
	mockSPDY := newMockSPDYForwarder()
	mgr := NewPortForwardManager(nil, nil, mockSPDY)

	ctx := context.Background()
	mgr.ForwardPod(ctx, "ns", "pod-a", 8080)
	mgr.ForwardPod(ctx, "ns", "pod-b", 9090)

	keys := mgr.ActiveForwards()
	if len(keys) != 2 {
		t.Fatalf("expected 2 active forwards, got %d", len(keys))
	}

	// Keys should be sorted
	if keys[0] != "ns/pod-a:8080" {
		t.Errorf("expected first key ns/pod-a:8080, got %q", keys[0])
	}
	if keys[1] != "ns/pod-b:9090" {
		t.Errorf("expected second key ns/pod-b:9090, got %q", keys[1])
	}
}

// ─── T16: GetHITLLocalPort returns correct port ────────────────────────────

func TestGetHITLLocalPort_Found(t *testing.T) {
	mockSPDY := newMockSPDYForwarder()
	mgr := NewPortForwardManager(nil, nil, mockSPDY)

	ctx := context.Background()
	_, lp, _ := mgr.ForwardPod(ctx, "ns", "pod", 8080)
	mgr.SetHITLForward("ns", "pod", 8080)

	port, ok := mgr.GetHITLLocalPort()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if port != lp {
		t.Errorf("expected local port %d, got %d", lp, port)
	}
}

// ─── T17: GetHITLLocalPort returns false when no HITL forward ──────────────

func TestGetHITLLocalPort_NotFound(t *testing.T) {
	mockSPDY := newMockSPDYForwarder()
	mgr := NewPortForwardManager(nil, nil, mockSPDY)

	port, ok := mgr.GetHITLLocalPort()
	if ok {
		t.Fatal("expected ok=false")
	}
	if port != 0 {
		t.Errorf("expected port 0, got %d", port)
	}
}


