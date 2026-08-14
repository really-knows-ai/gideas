package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// forwardState tracks a single active port-forward.
type forwardState struct {
	localPort  int
	remotePort int
	namespace  string
	podName    string
	stopFunc   func() // calls SPDY stop func and removes from map; set by ForwardPod
}

// inflightForward records a same-key ForwardPod creation in progress so a
// concurrent same-key call waits for its result instead of creating a
// duplicate SPDY forward. Fields are written by the creator before done is
// closed; the channel close publishes them to waiting callers.
type inflightForward struct {
	done      chan struct{}
	forwardID string
	localPort int
	err       error
}

// PortForwardManager manages zero or more pod port-forwards.
type PortForwardManager struct {
	mu            sync.Mutex
	config        *rest.Config
	clientset     kubernetes.Interface
	forwards      map[string]*forwardState    // key: "namespace/podName:remotePort"
	inflight      map[string]*inflightForward // key: "namespace/podName:remotePort", non-nil while a creation for that key is in progress
	spdyForwarder SPDYForwarder               // production or mock SPDY forwarder
}

// SPDYForwarder abstracts the SPDY port-forward creation for testability.
type SPDYForwarder interface {
	ForwardPod(ctx context.Context, namespace, podName string, remotePort int) (localPort int, stop func(), err error)
}

// PortForwarder is the interface consumed by the TUI, allowing mock
// implementations in tests without requiring a cluster or real port-forwards.
type PortForwarder interface {
	FindReadyPod(ctx context.Context, namespace, labelSelector string) (podName string, found bool, err error)
	ForwardPod(ctx context.Context, namespace, podName string, remotePort int) (forwardID string, localPort int, err error)
	Close(forwardID string) error
	CloseAll() error
}

// Compile-time check that *PortForwardManager implements PortForwarder.
var _ PortForwarder = (*PortForwardManager)(nil)

// NewPortForwardManager creates a PortForwardManager. If spdyForwarder is nil,
// a production SPDY forwarder wrapping client-go/tools/portforward is created.
func NewPortForwardManager(config *rest.Config, clientset kubernetes.Interface, spdyForwarder SPDYForwarder) *PortForwardManager {
	m := &PortForwardManager{
		config:    config,
		clientset: clientset,
		forwards:  make(map[string]*forwardState),
		inflight:  make(map[string]*inflightForward),
	}
	if spdyForwarder != nil {
		m.spdyForwarder = spdyForwarder
	} else {
		m.spdyForwarder = &productionSPDYForwarder{config: config}
	}
	return m
}

// productionSPDYForwarder implements SPDYForwarder using client-go/tools/portforward.
type productionSPDYForwarder struct {
	config *rest.Config
}

func (f *productionSPDYForwarder) ForwardPod(ctx context.Context, namespace, podName string, remotePort int) (localPort int, stop func(), err error) {
	transport, upgrader, err := spdy.RoundTripperFor(f.config)
	if err != nil {
		return 0, nil, fmt.Errorf("spdy round tripper: %w", err)
	}

	hostURL, err := url.Parse(f.config.Host)
	if err != nil {
		return 0, nil, fmt.Errorf("parse host URL: %w", err)
	}
	hostURL.Path = fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", namespace, podName)

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", hostURL)

	stopChan := make(chan struct{})
	readyChan := make(chan struct{})

	// Port mapping: "0:<remotePort>" for OS-assigned local port
	ports := []string{fmt.Sprintf("0:%d", remotePort)}

	fw, err := portforward.New(dialer, ports, stopChan, readyChan, nil, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("port forward: %w", err)
	}

	// Start forwarding in a goroutine
	go func() {
		_ = fw.ForwardPorts() // blocks until stopChan is closed or error
	}()

	// Wait for the forward to be ready or context cancellation
	select {
	case <-readyChan:
		// Get assigned local port
		ports, err := fw.GetPorts()
		if err != nil {
			return 0, nil, fmt.Errorf("get forwarded ports: %w", err)
		}
		if len(ports) == 0 {
			return 0, nil, fmt.Errorf("no forwarded ports assigned")
		}
		localPort = int(ports[0].Local)
	case <-ctx.Done():
		close(stopChan)
		return 0, nil, ctx.Err()
	}

	stop = func() {
		close(stopChan)
	}

	return localPort, stop, nil
}

// FindReadyPod lists pods matching labelSelector and returns the first Ready pod.
// A pod is Ready when: Running phase + Ready=True condition + non-empty PodIP.
func (m *PortForwardManager) FindReadyPod(ctx context.Context, namespace, labelSelector string) (podName string, found bool, err error) {
	pods, err := m.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return "", false, err
	}

	for _, pod := range pods.Items {
		if PodReady(&pod) {
			return pod.Name, true, nil
		}
	}
	return "", false, nil
}

// ForwardPod creates a port-forward to namespace/podName on remotePort.
// Returns a forwardID ("namespace/podName:remotePort") and the local port.
// Idempotent: if a forward to the same target already exists, returns existing.
// The map-insert is made atomic with the forward creation via the inflight
// guard: a concurrent same-key call observes the in-flight creation and waits
// for its result instead of creating a duplicate SPDY forward whose stop func
// would orphan the earlier one.
func (m *PortForwardManager) ForwardPod(ctx context.Context, namespace, podName string, remotePort int) (forwardID string, localPort int, err error) {
	key := fmt.Sprintf("%s/%s:%d", namespace, podName, remotePort)

	m.mu.Lock()
	// Already-tracked forward for this target: idempotent return.
	if existing, ok := m.forwards[key]; ok {
		m.mu.Unlock()
		return key, existing.localPort, nil
	}
	// A concurrent same-key creation is in progress: wait for its result
	// rather than creating a duplicate.
	if inflight, ok := m.inflight[key]; ok {
		m.mu.Unlock()
		select {
		case <-inflight.done:
		case <-ctx.Done():
			return "", 0, ctx.Err()
		}
		return inflight.forwardID, inflight.localPort, inflight.err
	}
	// Become the creator for this key. The inflight entry is removed once the
	// creation completes, so a later call sees either this record or the
	// tracked forward.
	in := &inflightForward{done: make(chan struct{})}
	m.inflight[key] = in
	m.mu.Unlock()

	// Delegate to the SPDY forwarder outside the lock.
	lp, stop, err := m.spdyForwarder.ForwardPod(ctx, namespace, podName, remotePort)

	m.mu.Lock()
	delete(m.inflight, key)
	if err == nil {
		// Store the SPDY stop function and the cleanup function separately.
		// The cleanup removes from the map; the SPDY stop closes the SPDY
		// connection.
		spdyStop := stop
		cleanup := func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			delete(m.forwards, key)
		}

		state := &forwardState{
			localPort:  lp,
			remotePort: remotePort,
			namespace:  namespace,
			podName:    podName,
		}
		state.stopFunc = func() {
			if spdyStop != nil {
				spdyStop()
			}
			cleanup()
		}

		m.forwards[key] = state
		in.forwardID = key
		in.localPort = lp
	}
	in.err = err
	m.mu.Unlock()
	close(in.done) // publish the result to any waiting same-key callers

	if err != nil {
		return "", 0, err
	}
	return key, lp, nil
}

// Close closes a port-forward by forwardID.
func (m *PortForwardManager) Close(forwardID string) error {
	m.mu.Lock()
	state, ok := m.forwards[forwardID]
	m.mu.Unlock()

	if !ok {
		return fmt.Errorf("forward %q not found", forwardID)
	}

	state.stopFunc()

	return nil
}

// CloseAll closes all active forwards and clears the map.
func (m *PortForwardManager) CloseAll() error {
	m.mu.Lock()
	keys := make([]string, 0, len(m.forwards))
	for k := range m.forwards {
		keys = append(keys, k)
	}
	m.mu.Unlock()

	var firstErr error
	for _, key := range keys {
		if err := m.Close(key); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	m.mu.Lock()
	m.forwards = make(map[string]*forwardState)
	m.mu.Unlock()

	return firstErr
}


